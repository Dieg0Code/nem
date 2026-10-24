// Package ingest lee las sesiones de los agentes y las persiste en el Store de
// nem. La abstracción central es Source: una fuente de sesiones que aísla a nem
// de CÓMO persiste cada agente. Los agentes que guardan transcripts JSONL
// (Codex, Claude Code, Antigravity) se adaptan con NewFileSource sobre un
// Parser puro (archivo → ParsedChat); los de otra naturaleza (opencode y su
// SQLite) implementan Source directo. La orquestación inserta de forma
// idempotente.
package ingest

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Dieg0Code/nem/internal/db"
	"github.com/klauspost/compress/zstd"
)

// Roles con los que se persisten los mensajes. user/assistant son la
// conversación; reasoning es el razonamiento del agente (thinking/reasoning);
// tool son las llamadas a herramientas y sus salidas (compactadas).
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleReasoning = "reasoning"
	RoleTool      = "tool"
)

// maxToolChars acota el tamaño del contenido relacionado a tools (argumentos y
// salidas). Evita inflar la DB y contaminar el índice FTS5 con vuelcos enormes
// de logs o archivos. El reasoning NO se trunca (es conciso y valioso).
const maxToolChars = 2000

// maxReasoningChars acota el razonamiento. Es más generoso que las tools (el
// razonamiento es la señal que nem más quiere preservar) pero igual bounded.
const maxReasoningChars = 4000

// truncate recorta s a max caracteres (runas), agregando una marca si se cortó.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "\n…[truncado]"
}

// ParsedChat es el resultado de parsear un archivo de sesión: el chat y sus
// mensajes, listos para persistir.
type ParsedChat struct {
	Chat     db.Chat
	Messages []db.Message
	// CursorKey y Fingerprint identifican la sesión en su fuente y su huella
	// al leerla ("" = la fuente no es Incremental). Ingest los registra tras
	// persistir la sesión.
	CursorKey   string
	Fingerprint string
}

// Source es una fuente de sesiones de un agente. Es el seam que desacopla a
// nem del medio de persistencia de cada agente: Ingest solo conoce esta
// interfaz, y todo lo específico de un agente (formato, rutas, esquema) vive
// encapsulado en su Source. Si un agente cambia su forma de persistir, solo
// cambia su Source.
type Source interface {
	// Name identifica al agente: "codex" | "claude" | "antigravity" | "opencode".
	Name() string
	// Sessions itera las sesiones de la fuente. Cada elemento es una sesión
	// parseada o un error de esa sesión; los errores por sesión no abortan la
	// iteración. Una fuente cuyo agente no está instalado itera vacío.
	Sessions() iter.Seq2[*ParsedChat, error]
}

// Incremental es una Source capaz de saltarse las sesiones que no cambiaron
// desde la última ingesta. Es opcional: una Source que no la implementa se
// re-lee entera en cada corrida (la inserción igual es idempotente, solo más
// lenta).
type Incremental interface {
	Source
	// ChangedSessions itera solo las sesiones para las que changed(clave,
	// huella) devuelve true; las demás se saltan sin leerse. La clave es
	// estable por sesión (la ruta del transcript, o db#id si varias sesiones
	// comparten archivo).
	ChangedSessions(changed func(key, fingerprint string) bool) iter.Seq2[*ParsedChat, error]
}

// cursorVersion entra en la huella de cada sesión. Subirlo cuando un parser
// cambie lo que extrae, para que la próxima ingesta re-lea todo.
const cursorVersion = 1

// Parser convierte un archivo de sesión de un agente en un ParsedChat. Es la
// abstracción que implementan los agentes con sesiones en archivos (codex,
// claude, antigravity); se eleva a Source con NewFileSource.
type Parser interface {
	// Source identifica al agente: "codex" | "claude" | "antigravity".
	Source() string
	// DefaultRoot devuelve el directorio raíz por defecto de las sesiones.
	DefaultRoot() (string, error)
	// Parse parsea el contenido de un archivo de sesión.
	Parse(r io.Reader, sessionPath string) (*ParsedChat, error)
}

// Report resume el resultado de una corrida de ingesta.
type Report struct {
	Source    string
	Scanned   int // sesiones examinadas (archivos o filas, según la fuente)
	Chats     int
	Messages  int64
	Skipped   int
	Unchanged int // sesiones saltadas sin parsear (huella igual a la ingestada)
	Errors    []string
}

// config contiene las opciones de una fuente.
type config struct {
	root string
}

// Option configura una fuente de sesiones.
type Option func(*config) error

// WithRoot fuerza la raíz de sesiones de la fuente: el directorio de
// transcripts para las fuentes de archivos, la ruta de la DB para opencode.
func WithRoot(root string) Option {
	return func(c *config) error {
		if root == "" {
			return errors.New("root cannot be empty")
		}
		c.root = root
		return nil
	}
}

// Ingest consume las sesiones de la fuente y las persiste en el store de forma
// idempotente (re-ingestar no duplica). Si la fuente es Incremental, salta las
// sesiones cuya huella no cambió desde la última corrida. Los errores por
// sesión no abortan la corrida: se acumulan en el Report.
func Ingest(store db.Store, src Source) (*Report, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	if src == nil {
		return nil, errors.New("source is required")
	}

	report := &Report{Source: src.Name()}
	sessions := src.Sessions()
	if inc, ok := src.(Incremental); ok {
		seen, err := store.IngestCursors(src.Name())
		if err != nil {
			return nil, err
		}
		sessions = inc.ChangedSessions(func(key, fingerprint string) bool {
			if seen[key] == fingerprint {
				report.Unchanged++
				return false
			}
			return true
		})
	}
	// markSeen registra la huella de una sesión ya persistida. Un fallo no
	// pierde datos (solo obliga a re-leerla la próxima vez), pero se reporta.
	markSeen := func(parsed *ParsedChat) {
		if parsed.CursorKey == "" || parsed.Fingerprint == "" {
			return
		}
		if err := store.SetIngestCursor(src.Name(), parsed.CursorKey, parsed.Fingerprint); err != nil {
			report.Errors = append(report.Errors, err.Error())
		}
	}

	for parsed, err := range sessions {
		report.Scanned++
		if err != nil {
			report.Errors = append(report.Errors, err.Error())
			continue
		}
		if parsed == nil || len(parsed.Messages) == 0 {
			report.Skipped++
			if parsed != nil {
				markSeen(parsed)
			}
			continue
		}
		if err := store.UpsertChat(&parsed.Chat); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", parsed.Chat.SessionPath, err))
			continue
		}
		n, err := store.InsertMessages(parsed.Messages)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", parsed.Chat.SessionPath, err))
			continue
		}
		report.Chats++
		report.Messages += n
		markSeen(parsed)
	}

	return report, nil
}

// fileSource adapta un Parser basado en archivos JSONL a la interfaz Source:
// camina la raíz y parsea cada transcript encontrado.
type fileSource struct {
	parser Parser
	root   string // "" = DefaultRoot() del parser
}

// NewFileSource eleva un Parser de archivos a Source. Sin WithRoot usa el
// DefaultRoot del parser.
func NewFileSource(p Parser, options ...Option) (Source, error) {
	if p == nil {
		return nil, errors.New("parser is required")
	}
	cfg := &config{}
	for _, option := range options {
		if err := option(cfg); err != nil {
			return nil, fmt.Errorf("failed to apply source option: %w", err)
		}
	}
	return &fileSource{parser: p, root: cfg.root}, nil
}

func (s *fileSource) Name() string { return s.parser.Source() }

func (s *fileSource) Sessions() iter.Seq2[*ParsedChat, error] {
	return s.ChangedSessions(func(string, string) bool { return true })
}

func (s *fileSource) ChangedSessions(changed func(key, fingerprint string) bool) iter.Seq2[*ParsedChat, error] {
	return func(yield func(*ParsedChat, error) bool) {
		root := s.root
		if root == "" {
			r, err := s.parser.DefaultRoot()
			if err != nil {
				yield(nil, err)
				return
			}
			root = r
		}
		files, err := sessionFiles(root)
		if err != nil {
			yield(nil, err)
			return
		}
		// La huella se decide en serie (changed no tiene por qué ser seguro
		// para concurrencia); solo el parseo de lo que cambió va en paralelo.
		var todo []pendingFile
		for _, path := range files {
			fp, err := fingerprint(path)
			if err != nil {
				if !yield(nil, fmt.Errorf("%s: %w", path, err)) {
					return
				}
				continue
			}
			if changed(path, fp) {
				todo = append(todo, pendingFile{path: path, fingerprint: fp})
			}
		}

		// Parseo paralelo con salida en orden: a lo más `workers` archivos en
		// vuelo por delante del consumidor, así la memoria queda acotada. Los
		// canales tienen buffer: si el consumidor corta, los workers en vuelo
		// terminan sin bloquearse.
		workers := runtime.GOMAXPROCS(0)
		results := make([]chan parseResult, len(todo))
		launch := func(i int) {
			ch := make(chan parseResult, 1)
			results[i] = ch
			go func() {
				parsed, err := parseFile(s.parser, todo[i].path, todo[i].fingerprint)
				ch <- parseResult{parsed, err}
			}()
		}
		for i := 0; i < len(todo) && i < workers; i++ {
			launch(i)
		}
		for i, job := range todo {
			r := <-results[i]
			if next := i + workers; next < len(todo) {
				launch(next)
			}
			if r.err != nil {
				if !yield(nil, fmt.Errorf("%s: %w", job.path, r.err)) {
					return
				}
				continue
			}
			if !yield(r.parsed, nil) {
				return
			}
		}
	}
}

// pendingFile es un archivo de sesión cuya huella cambió y hay que parsear.
type pendingFile struct {
	path        string
	fingerprint string
}

type parseResult struct {
	parsed *ParsedChat
	err    error
}

// fingerprint calcula la huella de un archivo de sesión. Se toma del archivo
// abierto (en Windows el stat por ruta puede devolver un tamaño atrasado
// mientras el agente escribe) y ANTES de parsearlo: si el agente agrega turnos
// mientras parseamos, la huella queda vieja y la próxima corrida lo re-lee
// (nunca se pierde un append).
func fingerprint(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("failed to stat session file: %w", err)
	}
	return fmt.Sprintf("v%d:%d:%d", cursorVersion, info.Size(), info.ModTime().UnixNano()), nil
}

// parseFile abre y parsea un único archivo, garantizando el cierre, y le
// asocia la huella fp tomada antes de leerlo.
func parseFile(p Parser, path, fp string) (*ParsedChat, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()
	var r io.Reader = f
	if isZstd(path) {
		dec, err := zstd.NewReader(f, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, fmt.Errorf("failed to open zstd session: %w", err)
		}
		defer dec.Close()
		r = dec
	}
	parsed, err := p.Parse(r, path)
	if err != nil {
		return nil, err
	}
	parsed.CursorKey = path
	parsed.Fingerprint = fp
	return parsed, nil
}

// isZstd indica si el transcript está comprimido: Codex archiva sus sesiones
// viejas como .jsonl.zst, y sin leerlas una instalación nueva perdería todo el
// historial previo.
func isZstd(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".jsonl.zst")
}

// sessionFiles devuelve todos los .jsonl (y .jsonl.zst) bajo root (recursivo). Si root no
// existe, devuelve lista vacía sin error (el agente puede no estar instalado).
func sessionFiles(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to stat root %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("root %s is not a directory", root)
	}

	var files []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // saltar entradas ilegibles, no abortar
		}
		if !d.IsDir() && (strings.EqualFold(filepath.Ext(path), ".jsonl") || isZstd(path)) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk %s: %w", root, err)
	}
	return files, nil
}

// parseTimestamp convierte un timestamp ISO8601 a unix seconds. Devuelve 0 si
// no se puede parsear.
func parseTimestamp(s string) int64 {
	if s == "" {
		return 0
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix()
		}
	}
	return 0
}

// titleFromCwd deriva un título legible del directorio de trabajo de la sesión,
// tolerando separadores de Windows y Unix.
func titleFromCwd(cwd string) string {
	if cwd == "" {
		return ""
	}
	norm := strings.ReplaceAll(cwd, "\\", "/")
	norm = strings.TrimRight(norm, "/")
	if idx := strings.LastIndex(norm, "/"); idx >= 0 {
		return norm[idx+1:]
	}
	return norm
}

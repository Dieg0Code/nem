// Package session detecta la sesión de agente activa: la sesión más
// recientemente modificada entre los agentes soportados (transcript .jsonl
// para Codex/Claude/Antigravity, fila de la DB para opencode).
package session

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Dieg0Code/nem/internal/ingest"
)

// Session identifica una sesión de agente detectada.
type Session struct {
	ChatID  string
	Source  string // "codex" | "claude" | "antigravity" | "opencode"
	Path    string
	ModTime time.Time
}

// Detector resuelve la sesión activa.
type Detector interface {
	// Detect devuelve la sesión activa, o (nil, nil) si no encuentra ninguna.
	Detect() (*Session, error)
}

type config struct {
	codexRoot       string
	claudeRoot      string
	antigravityRoot string
	opencodeDB      string
}

// Option configura el Detector.
type Option func(*config) error

// WithCodexRoot fuerza el root de sesiones de Codex (default ~/.codex/sessions).
func WithCodexRoot(root string) Option {
	return func(c *config) error {
		if root == "" {
			return errors.New("codex root cannot be empty")
		}
		c.codexRoot = root
		return nil
	}
}

// WithClaudeRoot fuerza el root de sesiones de Claude (default ~/.claude/projects).
func WithClaudeRoot(root string) Option {
	return func(c *config) error {
		if root == "" {
			return errors.New("claude root cannot be empty")
		}
		c.claudeRoot = root
		return nil
	}
}

// WithAntigravityRoot fuerza el root de sesiones de Antigravity (default
// ~/.gemini/antigravity-cli/brain).
func WithAntigravityRoot(root string) Option {
	return func(c *config) error {
		if root == "" {
			return errors.New("antigravity root cannot be empty")
		}
		c.antigravityRoot = root
		return nil
	}
}

// WithOpencodeDB fuerza la ruta de la DB de sesiones de opencode (default
// ~/.local/share/opencode/opencode.db, honrando XDG_DATA_HOME).
func WithOpencodeDB(path string) Option {
	return func(c *config) error {
		if path == "" {
			return errors.New("opencode db path cannot be empty")
		}
		c.opencodeDB = path
		return nil
	}
}

type detector struct {
	codexRoot       string
	claudeRoot      string
	antigravityRoot string
	opencodeDB      string
}

// NewDetector crea un Detector. Por defecto usa ~/.codex/sessions,
// ~/.claude/projects, ~/.gemini/antigravity-cli/brain y la DB de opencode.
func NewDetector(options ...Option) (Detector, error) {
	cfg := &config{}
	for _, option := range options {
		if err := option(cfg); err != nil {
			return nil, fmt.Errorf("failed to apply session option: %w", err)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve home directory: %w", err)
	}
	if cfg.codexRoot == "" {
		cfg.codexRoot = filepath.Join(home, ".codex", "sessions")
	}
	if cfg.claudeRoot == "" {
		cfg.claudeRoot = filepath.Join(home, ".claude", "projects")
	}
	if cfg.antigravityRoot == "" {
		cfg.antigravityRoot = filepath.Join(home, ".gemini", "antigravity-cli", "brain")
	}
	if cfg.opencodeDB == "" {
		// Best-effort: si no se puede resolver queda "", y la detección de
		// opencode simplemente no participa.
		cfg.opencodeDB, _ = ingest.DefaultOpencodeDBPath()
	}
	return &detector{
		codexRoot:       cfg.codexRoot,
		claudeRoot:      cfg.claudeRoot,
		antigravityRoot: cfg.antigravityRoot,
		opencodeDB:      cfg.opencodeDB,
	}, nil
}

var uuidRE = regexp.MustCompile(`([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})`)

// agentSessionEnv mapea las variables que cada harness setea SOLO (sin que el
// usuario configure nada) con el id de su sesión activa, a su fuente en nem.
var agentSessionEnv = []struct{ key, source string }{
	{"CLAUDE_CODE_SESSION_ID", "claude"},
	{"CODEX_COMPANION_SESSION_ID", "codex"},
}

// fromEnv resuelve la sesión que el propio agente declara en su entorno. Es la
// verdad exacta de "qué sesión soy" — sin la carrera de mtime entre agentes
// concurrentes. Devuelve nil si ningún harness la expuso.
func fromEnv() *Session {
	for _, e := range agentSessionEnv {
		if id := strings.TrimSpace(os.Getenv(e.key)); id != "" {
			return &Session{ChatID: id, Source: e.source}
		}
	}
	return nil
}

// Detect devuelve la sesión activa. Primero confía en la que el agente declara en
// su entorno (exacta, robusta ante varios agentes en paralelo); si no hay ninguna,
// cae al heurístico del .jsonl más reciente entre Codex y Claude.
func (d *detector) Detect() (*Session, error) {
	if s := fromEnv(); s != nil {
		return s, nil
	}

	codex := newest(d.codexRoot)
	claude := newest(d.claudeRoot)
	antigravity := newestAntigravity(d.antigravityRoot)
	opencode := newestOpencode(d.opencodeDB)

	var best *fileHit
	var source string
	if codex != nil {
		best, source = codex, "codex"
	}
	if claude != nil && (best == nil || claude.modTime.After(best.modTime)) {
		best, source = claude, "claude"
	}
	if antigravity != nil && (best == nil || antigravity.modTime.After(best.modTime)) {
		best, source = antigravity, "antigravity"
	}
	if opencode != nil && (best == nil || opencode.modTime.After(best.modTime)) {
		best, source = opencode, "opencode"
	}
	if best == nil {
		return nil, nil
	}

	chatID := best.chatID
	if chatID == "" {
		chatID = chatIDFromPath(best.path, source)
	}
	return &Session{
		ChatID:  chatID,
		Source:  source,
		Path:    best.path,
		ModTime: best.modTime,
	}, nil
}

// Recent devuelve sesiones modificadas dentro de window, ordenadas de más nueva
// a más antigua. Se usa para advertir concurrencia operacional entre agentes.
func Recent(window time.Duration, options ...Option) ([]Session, error) {
	d, err := NewDetector(options...)
	if err != nil {
		return nil, err
	}
	dd := d.(*detector)
	since := time.Now().Add(-window)
	var out []Session
	out = append(out, recentUnder(dd.codexRoot, "codex", since)...)
	out = append(out, recentUnder(dd.claudeRoot, "claude", since)...)
	out = append(out, recentUnder(dd.antigravityRoot, "antigravity", since)...)
	out = append(out, recentOpencode(dd.opencodeDB, since)...)
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out, nil
}

type fileHit struct {
	path    string
	modTime time.Time
	chatID  string // "" = derivar del path con chatIDFromPath
}

// newest devuelve el .jsonl más recientemente modificado bajo root, o nil.
func newest(root string) *fileHit {
	var best *fileHit
	_ = filepath.WalkDir(root, func(path string, dirent fs.DirEntry, err error) error {
		if err != nil || dirent.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".jsonl") {
			return nil
		}
		info, err := dirent.Info()
		if err != nil {
			return nil
		}
		if best == nil || info.ModTime().After(best.modTime) {
			best = &fileHit{path: path, modTime: info.ModTime()}
		}
		return nil
	})
	return best
}

func newestAntigravity(root string) *fileHit {
	var best *fileHit
	_ = filepath.WalkDir(root, func(path string, dirent fs.DirEntry, err error) error {
		if err != nil || dirent.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Base(path), "transcript.jsonl") {
			return nil
		}
		info, err := dirent.Info()
		if err != nil {
			return nil
		}
		if best == nil || info.ModTime().After(best.modTime) {
			best = &fileHit{path: path, modTime: info.ModTime()}
		}
		return nil
	})
	return best
}

// newestOpencode devuelve la sesión de opencode más recientemente actualizada,
// leída de su DB (no hay archivo por sesión cuyo mtime mirar). Best-effort:
// cualquier fallo se trata como "sin sesión".
func newestOpencode(dbPath string) *fileHit {
	if dbPath == "" {
		return nil
	}
	stamps, err := ingest.OpencodeSessionStamps(dbPath, time.Time{})
	if err != nil || len(stamps) == 0 {
		return nil
	}
	return &fileHit{path: dbPath, modTime: stamps[0].Updated, chatID: stamps[0].ID}
}

// recentOpencode lista las sesiones de opencode actualizadas desde `since`.
func recentOpencode(dbPath string, since time.Time) []Session {
	if dbPath == "" {
		return nil
	}
	stamps, err := ingest.OpencodeSessionStamps(dbPath, since)
	if err != nil {
		return nil
	}
	var out []Session
	for _, s := range stamps {
		out = append(out, Session{
			ChatID:  s.ID,
			Source:  "opencode",
			Path:    dbPath,
			ModTime: s.Updated,
		})
	}
	return out
}

func recentUnder(root, source string, since time.Time) []Session {
	var out []Session
	_ = filepath.WalkDir(root, func(path string, dirent fs.DirEntry, err error) error {
		if err != nil || dirent.IsDir() {
			return nil
		}
		if source == "antigravity" {
			if !strings.EqualFold(filepath.Base(path), "transcript.jsonl") {
				return nil
			}
		} else if !strings.EqualFold(filepath.Ext(path), ".jsonl") {
			return nil
		}
		info, err := dirent.Info()
		if err != nil || info.ModTime().Before(since) {
			return nil
		}
		out = append(out, Session{
			ChatID:  chatIDFromPath(path, source),
			Source:  source,
			Path:    path,
			ModTime: info.ModTime(),
		})
		return nil
	})
	return out
}

// chatIDFromPath deriva el chat id del path del archivo, consistente con los
// parsers: UUID del nombre para Codex; nombre sin extensión (sessionId) para
// Claude.
func chatIDFromPath(path, source string) string {
	if source == "antigravity" {
		dir := filepath.Dir(path)
		dir = filepath.Dir(dir)
		return filepath.Base(filepath.Dir(dir))
	}
	base := filepath.Base(path)
	if source == "codex" {
		if m := uuidRE.FindString(base); m != "" {
			return m
		}
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}

package ingest

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"time"

	"github.com/Dieg0Code/nem/internal/db"

	// Registra el driver database/sql "sqlite" (Go puro, mismo motor que ya usa
	// el store de nem vía glebarez/sqlite).
	_ "github.com/glebarez/go-sqlite"
)

// opencodeSource implementa Source para opencode, que a diferencia de los
// agentes de transcripts JSONL persiste sus sesiones en una base SQLite
// (~/.local/share/opencode/opencode.db): tablas session, message y part, con el
// contenido real en columnas `data` JSON. Este archivo encapsula TODO lo
// específico de ese esquema; si opencode vuelve a cambiar su forma de
// persistir, solo cambia este archivo.
type opencodeSource struct {
	dbPath string // "" = DefaultOpencodeDBPath()
}

// NewOpencodeSource crea la fuente de sesiones de opencode. WithRoot fuerza la
// ruta de la DB (útil para tests); sin opciones usa la ruta por defecto.
func NewOpencodeSource(options ...Option) (Source, error) {
	cfg := &config{}
	for _, option := range options {
		if err := option(cfg); err != nil {
			return nil, fmt.Errorf("failed to apply source option: %w", err)
		}
	}
	return &opencodeSource{dbPath: cfg.root}, nil
}

func (s *opencodeSource) Name() string { return "opencode" }

// DefaultOpencodeDBPath devuelve la ruta por defecto de la DB de opencode,
// honrando XDG_DATA_HOME (opencode usa rutas XDG incluso en Windows).
func DefaultOpencodeDBPath() (string, error) {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "opencode", "opencode.db"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "opencode", "opencode.db"), nil
}

// opencodeDSN arma el DSN de solo lectura: nem jamás escribe la DB del agente,
// y el busy_timeout tolera que opencode esté corriendo (WAL) en paralelo.
func opencodeDSN(path string) string {
	return "file:" + filepath.ToSlash(path) + "?mode=ro&_pragma=busy_timeout(5000)"
}

func (s *opencodeSource) Sessions() iter.Seq2[*ParsedChat, error] {
	return func(yield func(*ParsedChat, error) bool) {
		path := s.dbPath
		if path == "" {
			p, err := DefaultOpencodeDBPath()
			if err != nil {
				yield(nil, err)
				return
			}
			path = p
		}
		if _, err := os.Stat(path); err != nil {
			// Sin DB no hay opencode instalado (o aún sin sesiones): fuente vacía.
			return
		}

		sdb, err := sql.Open("sqlite", opencodeDSN(path))
		if err != nil {
			yield(nil, fmt.Errorf("%s: %w", path, err))
			return
		}
		defer sdb.Close()

		sessions, err := opencodeSessions(sdb)
		if err != nil {
			yield(nil, fmt.Errorf("%s: %w", path, err))
			return
		}
		for _, sess := range sessions {
			parsed, err := loadOpencodeSession(sdb, path, sess)
			if err != nil {
				if !yield(nil, fmt.Errorf("%s: session %s: %w", path, sess.id, err)) {
					return
				}
				continue
			}
			if !yield(parsed, nil) {
				return
			}
		}
	}
}

// opencodeSession es la fila de la tabla session que nem consume.
type opencodeSession struct {
	id        string
	title     string
	directory string
	createdMs int64
}

// opencodeSessions lista las sesiones de la DB, de más antigua a más nueva.
// title/directory son nullable en el esquema de opencode: COALESCE evita que
// una sola sesión sin título aborte la ingesta completa.
func opencodeSessions(sdb *sql.DB) ([]opencodeSession, error) {
	rows, err := sdb.Query(`SELECT id, COALESCE(title, ''), COALESCE(directory, ''), time_created
		FROM session ORDER BY time_created, id`)
	if err != nil {
		return nil, fmt.Errorf("failed to query sessions: %w", err)
	}
	defer rows.Close()

	var out []opencodeSession
	for rows.Next() {
		var s opencodeSession
		if err := rows.Scan(&s.id, &s.title, &s.directory, &s.createdMs); err != nil {
			return nil, fmt.Errorf("failed to scan session row: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// opencodeMessageData es el JSON de message.data (el mensaje sin id ni
// sessionID); solo consumimos el rol.
type opencodeMessageData struct {
	Role string `json:"role"`
}

// opencodePartData es el JSON de part.data: solo los campos que nem consume.
// Los tipos que no mapean a conversación (snapshot, patch, step-start,
// step-finish, file, agent, retry, compaction, subtask) se ignoran.
type opencodePartData struct {
	Type      string             `json:"type"`
	Text      string             `json:"text"`
	Synthetic bool               `json:"synthetic"`
	Ignored   bool               `json:"ignored"`
	Tool      string             `json:"tool"`
	State     *opencodeToolState `json:"state"`
}

// opencodeToolState es el estado de un ToolPart (pending|running|completed|error).
type opencodeToolState struct {
	Status string          `json:"status"`
	Input  json.RawMessage `json:"input"`
	Output string          `json:"output"`
	Error  string          `json:"error"`
}

// loadOpencodeSession arma el ParsedChat de una sesión recorriendo sus parts en
// orden de conversación (time_created del mensaje, luego ids ULID ascendentes).
func loadOpencodeSession(sdb *sql.DB, dbPath string, sess opencodeSession) (*ParsedChat, error) {
	chat := db.Chat{
		ID:          sess.id,
		Source:      "opencode",
		SessionPath: dbPath,
		Title:       sess.title,
		CreatedAt:   sess.createdMs / 1000,
	}
	if chat.Title == "" {
		chat.Title = titleFromCwd(sess.directory)
	}

	rows, err := sdb.Query(`
		SELECT m.data, p.id, p.time_created, p.data
		FROM message m JOIN part p ON p.message_id = m.id
		WHERE m.session_id = ?
		ORDER BY m.time_created, m.id, p.id`, sess.id)
	if err != nil {
		return nil, fmt.Errorf("failed to query parts: %w", err)
	}
	defer rows.Close()

	var msgs []db.Message
	var seq int64

	// add agrega un mensaje con el id del part (prt_*, ULID global) si no es
	// vacío. El Seq NO se deriva de cuántos mensajes se emitieron sino de la
	// posición del part en la sesión (seq avanza por cada fila, emitida o no):
	// así un part que pasa de vacío a lleno entre dos ingestas conserva su Seq
	// posicional y no colisiona con los ya persistidos (InsertMessages es
	// DoNothing en conflicto). Los gaps son inofensivos: todo el consumo de Seq
	// es por rangos.
	add := func(partID, role, content string, ts int64) {
		if content == "" {
			return
		}
		msgs = append(msgs, db.Message{
			ID:        partID,
			ChatID:    sess.id,
			Role:      role,
			Content:   content,
			Timestamp: ts,
			Seq:       seq,
		})
	}

	for rows.Next() {
		var msgData, partData []byte
		var partCreatedMs int64
		var partID string
		if err := rows.Scan(&msgData, &partID, &partCreatedMs, &partData); err != nil {
			return nil, fmt.Errorf("failed to scan part row: %w", err)
		}
		seq++ // posición del part en la sesión, se emita o no

		var m opencodeMessageData
		var p opencodePartData
		if json.Unmarshal(msgData, &m) != nil || json.Unmarshal(partData, &p) != nil {
			continue // fila corrupta: saltar, no abortar
		}
		ts := partCreatedMs / 1000

		switch p.Type {
		case "text":
			// synthetic/ignored es contexto inyectado por opencode, no conversación.
			if p.Synthetic || p.Ignored {
				continue
			}
			switch m.Role {
			case "user":
				add(partID, RoleUser, p.Text, ts)
			case "assistant":
				add(partID, RoleAssistant, p.Text, ts)
			}

		case "reasoning":
			add(partID, RoleReasoning, truncate(p.Text, maxReasoningChars), ts)

		case "tool":
			add(partID, RoleTool, compactOpencodeTool(p), ts)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan opencode session: %w", err)
	}

	return &ParsedChat{Chat: chat, Messages: msgs}, nil
}

// compactOpencodeTool arma "<tool> <input>\n<output>" compacto y truncado. En
// opencode la llamada y su resultado viven en el mismo part, así que van en un
// solo mensaje. Un tool part sin estado no tiene nada que decir: se descarta
// (devolver "" hace que add() lo salte).
func compactOpencodeTool(p opencodePartData) string {
	if p.State == nil {
		return ""
	}
	s := p.Tool
	if len(p.State.Input) > 0 {
		s += " " + string(p.State.Input)
	}
	out := p.State.Output
	if p.State.Status == "error" {
		out = p.State.Error
	}
	if out != "" {
		s += "\n" + out
	}
	return truncate(s, maxToolChars)
}

// OpencodeStamp es (id, última actualización) de una sesión de opencode. Lo
// consume el detector de sesión activa sin conocer el esquema de la DB.
type OpencodeStamp struct {
	ID      string
	Updated time.Time
}

// OpencodeSessionStamps devuelve las sesiones actualizadas desde `since`, de
// más nueva a más antigua. dbPath "" usa la ruta por defecto; sin DB devuelve
// nil sin error (opencode no instalado).
func OpencodeSessionStamps(dbPath string, since time.Time) ([]OpencodeStamp, error) {
	if dbPath == "" {
		p, err := DefaultOpencodeDBPath()
		if err != nil {
			return nil, err
		}
		dbPath = p
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil, nil
	}

	sdb, err := sql.Open("sqlite", opencodeDSN(dbPath))
	if err != nil {
		return nil, fmt.Errorf("failed to open opencode db: %w", err)
	}
	defer sdb.Close()

	rows, err := sdb.Query(
		`SELECT id, time_updated FROM session WHERE time_updated >= ? ORDER BY time_updated DESC`,
		since.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("failed to query session stamps: %w", err)
	}
	defer rows.Close()

	var out []OpencodeStamp
	for rows.Next() {
		var id string
		var updatedMs int64
		if err := rows.Scan(&id, &updatedMs); err != nil {
			return nil, fmt.Errorf("failed to scan session stamp: %w", err)
		}
		out = append(out, OpencodeStamp{ID: id, Updated: time.UnixMilli(updatedMs)})
	}
	return out, rows.Err()
}

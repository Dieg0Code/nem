package ingest

import (
	"database/sql"
	"fmt"
	json "github.com/segmentio/encoding/json"
	"iter"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Dieg0Code/nem/internal/db"

	// Registra el driver database/sql "sqlite" (Go puro, mismo motor que ya usa
	// el store de nem vía glebarez/sqlite).
	_ "github.com/glebarez/go-sqlite"
)

// opencodeSource implementa Source para opencode, que a diferencia de los
// agentes de transcripts JSONL persiste sus sesiones en una base SQLite
// (~/.local/share/opencode/opencode.db) con el contenido real en columnas
// `data` JSON. opencode tiene DOS esquemas conviviendo en la misma DB:
//
//   - legacy: tablas session + message + part (un part por mensaje).
//   - v2 (opencode >= 2): tablas session_v2 + session_message, donde un mensaje
//     colapsa sus parts en un array `content` dentro de data JSON.
//
// Al actualizar a opencode 2 las sesiones viejas QUEDAN en las tablas legacy
// (opencode no las migra a session_message con ids nuevos) y solo las sesiones
// nuevas nacen en v2. Por eso nem lee cada sesión del esquema donde opencode la
// dejó: legacy para las que siguen en la tabla `session` (ya ingestadas, ids
// `prt_*` estables → el cursor las salta sin duplicar), v2 para las nacidas bajo
// opencode 2. Así un nem nuevo sobre un opencode actualizado conserva todo el
// historial. Este archivo encapsula TODO lo específico de ambos esquemas; si
// opencode vuelve a cambiar su forma de persistir, solo cambia este archivo.
type opencodeSource struct {
	dbPath string // "" = DefaultOpencodeDBPath()
}

// Esquema del que proviene una sesión de opencode (ver doc de opencodeSource).
const (
	opencodeSchemaLegacy = iota
	opencodeSchemaV2
)

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
	return s.ChangedSessions(func(string, string) bool { return true })
}

// ChangedSessions implementa Incremental: todas las sesiones viven en la misma
// DB, así que la clave es db#id y la huella sale de los agregados de sus filas
// (opencode actualiza time_updated en cada edición de un part, también durante
// el streaming). Calcular las huellas es una sola query barata.
func (s *opencodeSource) ChangedSessions(changed func(key, fingerprint string) bool) iter.Seq2[*ParsedChat, error] {
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

		sessions, err := opencodeAllSessions(sdb)
		if err != nil {
			yield(nil, fmt.Errorf("%s: %w", path, err))
			return
		}
		for _, sess := range sessions {
			key := path + "#" + sess.id
			if !changed(key, sess.fingerprint) {
				continue
			}
			parsed, err := loadOpencodeAny(sdb, path, sess)
			if err != nil {
				if !yield(nil, fmt.Errorf("%s: session %s: %w", path, sess.id, err)) {
					return
				}
				continue
			}
			parsed.CursorKey = key
			parsed.Fingerprint = sess.fingerprint
			if !yield(parsed, nil) {
				return
			}
		}
	}
}

// opencodeSession es la fila de session (o session_v2) que nem consume.
type opencodeSession struct {
	id        string
	title     string
	directory string
	createdMs int64
	schema    int // opencodeSchemaLegacy | opencodeSchemaV2
	// v2Tail marca una sesión legacy que siguió creciendo en session_message
	// (opencode 1.19 ya escribía ahí, y una sesión vieja puede retomarse en 2.x).
	v2Tail bool
	// fingerprint resume el estado de la sesión: su time_updated y la cantidad,
	// última edición (y tamaño, en v2) de sus mensajes y parts.
	fingerprint string
}

// loadOpencodeAny arma el ParsedChat de una sesión según su esquema: una
// sesión legacy con cola v2 suma, después de sus mensajes legacy, los de
// session_message que no existen en legacy.
func loadOpencodeAny(sdb *sql.DB, dbPath string, sess opencodeSession) (*ParsedChat, error) {
	if sess.schema == opencodeSchemaV2 {
		return loadOpencodeSessionV2(sdb, dbPath, sess)
	}
	parsed, err := loadOpencodeSession(sdb, dbPath, sess)
	if err != nil || !sess.v2Tail {
		return parsed, err
	}
	tail, err := opencodeV2Messages(sdb, sess.id, true)
	if err != nil {
		return nil, err
	}
	parsed.Messages = append(parsed.Messages, tail...)
	return parsed, nil
}

// opencodeHasTable indica si la DB de opencode tiene la tabla name: distingue
// el esquema legacy del v2 sin depender de la versión instalada.
func opencodeHasTable(sdb *sql.DB, name string) (bool, error) {
	var got string
	err := sdb.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&got)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to inspect opencode schema: %w", err)
	}
	return true, nil
}

// opencodeAllSessions lista las sesiones de ambos esquemas. Una sesión que
// está en la tabla legacy se lee de legacy (sus ids de mensaje no coinciden
// con los de v2: releerla de v2 la duplicaría) más su cola v2, si la tiene;
// las demás de session_v2 se leen de v2. Las legacy sin cola conservan su
// huella de siempre, así las ya ingestadas siguen saltándose.
func opencodeAllSessions(sdb *sql.DB) ([]opencodeSession, error) {
	var out []opencodeSession
	hasLegacy, err := opencodeHasTable(sdb, "session")
	if err != nil {
		return nil, err
	}
	if hasLegacy {
		if out, err = opencodeSessions(sdb); err != nil {
			return nil, err
		}
	}
	hasV2, err := opencodeHasTable(sdb, "session_v2")
	if err != nil || !hasV2 {
		return out, err
	}
	legacy := make(map[string]bool, len(out))
	for _, s := range out {
		legacy[s.id] = true
	}
	if hasLegacy {
		tails, err := opencodeV2Tails(sdb)
		if err != nil {
			return nil, err
		}
		for i := range out {
			if t, ok := tails[out[i].id]; ok {
				out[i].v2Tail = true
				out[i].fingerprint += t
			}
		}
	}
	v2, err := opencodeSessionsV2(sdb, legacy)
	if err != nil {
		return nil, err
	}
	return append(out, v2...), nil
}

// opencodeV2Tails devuelve, por sesión legacy, la huella de sus mensajes de
// conversación en session_message que no existen en la tabla message.
func opencodeV2Tails(sdb *sql.DB) (map[string]string, error) {
	rows, err := sdb.Query(`SELECT session_id, count(*), max(time_updated), sum(length(data))
		FROM session_message
		WHERE type IN ('user', 'assistant')
			AND session_id IN (SELECT id FROM session)
			AND id NOT IN (SELECT id FROM message)
		GROUP BY session_id`)
	if err != nil {
		return nil, fmt.Errorf("failed to query v2 tails: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var id string
		var n, mx, size int64
		if err := rows.Scan(&id, &n, &mx, &size); err != nil {
			return nil, fmt.Errorf("failed to scan v2 tail row: %w", err)
		}
		out[id] = fmt.Sprintf(":v2:%d:%d:%d", n, mx, size)
	}
	return out, rows.Err()
}

// opencodeSessionsV2 lista las sesiones de session_v2 que no están en skip, de
// más antigua a más nueva, con su huella. El tamaño total de data entra en la
// huella: una edición durante el streaming puede no mover max(time_updated).
func opencodeSessionsV2(sdb *sql.DB, skip map[string]bool) ([]opencodeSession, error) {
	rows, err := sdb.Query(`SELECT s.id, COALESCE(s.title, ''), COALESCE(s.directory, ''), s.time_created,
			COALESCE(s.time_updated, 0), COALESCE(m.n, 0), COALESCE(m.mx, 0), COALESCE(m.sz, 0)
		FROM session_v2 s
		LEFT JOIN (SELECT session_id, count(*) n, max(time_updated) mx, sum(length(data)) sz
			FROM session_message WHERE type IN ('user', 'assistant') GROUP BY session_id) m
			ON m.session_id = s.id
		ORDER BY s.time_created, s.id`)
	if err != nil {
		return nil, fmt.Errorf("failed to query v2 sessions: %w", err)
	}
	defer rows.Close()

	var out []opencodeSession
	for rows.Next() {
		s := opencodeSession{schema: opencodeSchemaV2}
		var updated, msgs, msgMax, size int64
		if err := rows.Scan(&s.id, &s.title, &s.directory, &s.createdMs, &updated, &msgs, &msgMax, &size); err != nil {
			return nil, fmt.Errorf("failed to scan v2 session row: %w", err)
		}
		if skip[s.id] {
			continue
		}
		s.fingerprint = fmt.Sprintf("v%d:v2:%d:%d:%d:%d", cursorVersion, updated, msgs, msgMax, size)
		out = append(out, s)
	}
	return out, rows.Err()
}

// opencodeSessions lista las sesiones de la DB, de más antigua a más nueva, con
// su huella. title/directory son nullable en el esquema de opencode: COALESCE
// evita que una sola sesión sin título aborte la ingesta completa.
func opencodeSessions(sdb *sql.DB) ([]opencodeSession, error) {
	rows, err := sdb.Query(`SELECT s.id, COALESCE(s.title, ''), COALESCE(s.directory, ''), s.time_created,
			COALESCE(s.time_updated, 0), COALESCE(m.n, 0), COALESCE(m.mx, 0), COALESCE(p.n, 0), COALESCE(p.mx, 0)
		FROM session s
		LEFT JOIN (SELECT session_id, count(*) n, max(time_updated) mx FROM message GROUP BY session_id) m
			ON m.session_id = s.id
		LEFT JOIN (SELECT session_id, count(*) n, max(time_updated) mx FROM part GROUP BY session_id) p
			ON p.session_id = s.id
		ORDER BY s.time_created, s.id`)
	if err != nil {
		return nil, fmt.Errorf("failed to query sessions: %w", err)
	}
	defer rows.Close()

	var out []opencodeSession
	for rows.Next() {
		var s opencodeSession
		var updated, msgs, msgMax, parts, partMax int64
		if err := rows.Scan(&s.id, &s.title, &s.directory, &s.createdMs,
			&updated, &msgs, &msgMax, &parts, &partMax); err != nil {
			return nil, fmt.Errorf("failed to scan session row: %w", err)
		}
		s.fingerprint = fmt.Sprintf("v%d:%d:%d:%d:%d:%d", cursorVersion, updated, msgs, msgMax, parts, partMax)
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

// opencodeV2Message es el JSON de session_message.data para los tipos que nem
// consume: user trae el texto directo; assistant, sus parts en content.
type opencodeV2Message struct {
	Text    string           `json:"text"`
	Content []opencodeV2Part `json:"content"`
}

// opencodeV2Part es un elemento de content de un mensaje assistant v2.
type opencodeV2Part struct {
	Type  string               `json:"type"`
	Text  string               `json:"text"`
	Name  string               `json:"name"`
	State *opencodeV2ToolState `json:"state"`
}

// opencodeV2ToolState es el estado de una tool v2: la salida viaja como bloques
// en content (output queda por si alguna tool aún lo usa).
type opencodeV2ToolState struct {
	Status  string          `json:"status"`
	Input   json.RawMessage `json:"input"`
	Output  string          `json:"output"`
	Error   string          `json:"error"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// opencodeV2SeqBase separa los Seq de v2 de los legacy (posicionales, chicos):
// en una sesión legacy con cola v2, la cola siempre ordena después.
const opencodeV2SeqBase = 1 << 32

// opencodeV2PartBits reserva bits del Seq para el índice del part dentro de su
// fila. El Seq de un mensaje v2 es base + seq_de_la_fila<<bits + índice: sale
// del orden que asigna opencode, así que no se corre cuando otra fila crece.
const opencodeV2PartBits = 16

// loadOpencodeSessionV2 arma el ParsedChat de una sesión que vive en v2.
func loadOpencodeSessionV2(sdb *sql.DB, dbPath string, sess opencodeSession) (*ParsedChat, error) {
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

	msgs, err := opencodeV2Messages(sdb, sess.id, false)
	if err != nil {
		return nil, err
	}
	return &ParsedChat{Chat: chat, Messages: msgs}, nil
}

// opencodeV2Messages lee los mensajes de conversación de una sesión en
// session_message, en el orden de opencode; onlyNew se queda con los que no
// existen en la tabla legacy message (la cola v2 de una sesión legacy). Los
// tipos que no son conversación (system, synthetic, idle, compaction,
// model-switched) se ignoran. Un mensaje user es un Message con el id de la
// fila; cada part de un assistant, uno con id "<fila>#<índice>".
func opencodeV2Messages(sdb *sql.DB, chatID string, onlyNew bool) ([]db.Message, error) {
	q := `SELECT id, type, seq, time_created, data FROM session_message
		WHERE session_id = ? AND type IN ('user', 'assistant')`
	if onlyNew {
		q += ` AND id NOT IN (SELECT id FROM message)`
	}
	rows, err := sdb.Query(q+` ORDER BY seq, id`, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to query session messages: %w", err)
	}
	defer rows.Close()

	var msgs []db.Message
	for rows.Next() {
		var rowID, typ string
		var rowSeq, createdMs int64
		var data []byte
		if err := rows.Scan(&rowID, &typ, &rowSeq, &createdMs, &data); err != nil {
			return nil, fmt.Errorf("failed to scan session message row: %w", err)
		}
		var m opencodeV2Message
		if json.Unmarshal(data, &m) != nil {
			continue // fila corrupta: saltar, no abortar
		}
		ts := createdMs / 1000
		base := opencodeV2SeqBase + rowSeq<<opencodeV2PartBits
		add := func(id, role, content string, part int) {
			if content == "" {
				return
			}
			msgs = append(msgs, db.Message{ID: id, ChatID: chatID, Role: role, Content: content, Timestamp: ts, Seq: base + int64(part)})
		}
		if typ == "user" {
			add(rowID, RoleUser, m.Text, 0)
			continue
		}
		for i, p := range m.Content {
			if i >= 1<<opencodeV2PartBits {
				break // el índice invadiría el Seq de la fila siguiente
			}
			id := rowID + "#" + strconv.Itoa(i)
			switch p.Type {
			case "text":
				add(id, RoleAssistant, p.Text, i)
			case "reasoning":
				add(id, RoleReasoning, truncate(p.Text, maxReasoningChars), i)
			case "tool":
				add(id, RoleTool, compactOpencodeToolV2(p), i)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan opencode v2 session: %w", err)
	}
	return msgs, nil
}

// compactOpencodeToolV2 es compactOpencodeTool para el esquema v2.
func compactOpencodeToolV2(p opencodeV2Part) string {
	if p.State == nil {
		return ""
	}
	s := p.Name
	if len(p.State.Input) > 0 {
		s += " " + string(p.State.Input)
	}
	out := p.State.Output
	if out == "" {
		var b strings.Builder
		for _, c := range p.State.Content {
			if c.Type == "text" && c.Text != "" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(c.Text)
			}
		}
		out = b.String()
	}
	if p.State.Status == "error" && p.State.Error != "" {
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

	// Con opencode 2 las sesiones nuevas viven solo en session_v2; la unión
	// cubre también las legacy (y una DB con un solo esquema).
	var parts []string
	for _, table := range []string{"session", "session_v2"} {
		ok, err := opencodeHasTable(sdb, table)
		if err != nil {
			return nil, err
		}
		if ok {
			parts = append(parts, `SELECT id, time_updated FROM `+table+` WHERE time_updated >= ?`)
		}
	}
	if len(parts) == 0 {
		return nil, nil
	}
	args := make([]any, len(parts))
	for i := range args {
		args[i] = since.UnixMilli()
	}
	rows, err := sdb.Query(
		`SELECT id, max(time_updated) FROM (`+strings.Join(parts, " UNION ALL ")+`)
		GROUP BY id ORDER BY 2 DESC`, args...)
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

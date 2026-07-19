package ingest

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dieg0Code/nem/internal/db"
)

// newOpencodeFixtureDB crea una opencode.db mínima con el esquema real
// (session/message/part, contenido en columnas data JSON) y una sesión de
// ejemplo. Devuelve la ruta de la DB.
func newOpencodeFixtureDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	sdb, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer sdb.Close()

	stmts := []string{
		`CREATE TABLE session (id TEXT PRIMARY KEY, project_id TEXT, parent_id TEXT,
			slug TEXT, directory TEXT, path TEXT, title TEXT, version TEXT,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,

		`INSERT INTO session VALUES ('ses_1', 'prj_1', NULL, 'fix-bug',
			'C:\Users\Diego\dev\nem', NULL, 'fix the bug', '1.18.3', 1750000000000, 1750000600000)`,

		// user: texto real + texto sintético (contexto inyectado, se ignora)
		`INSERT INTO message VALUES ('msg_1', 'ses_1', 1750000010000, 1750000010000,
			'{"role":"user","time":{"created":1750000010000},"agent":"build"}')`,
		`INSERT INTO part VALUES ('prt_01', 'msg_1', 'ses_1', 1750000010000, 1750000010000,
			'{"type":"text","text":"arregla el bug"}')`,
		`INSERT INTO part VALUES ('prt_02', 'msg_1', 'ses_1', 1750000010000, 1750000010000,
			'{"type":"text","text":"<system-context>ruido</system-context>","synthetic":true}')`,

		// assistant: reasoning + tool (call y output en el mismo part) + texto,
		// más parts operacionales que se ignoran (step-start/step-finish).
		`INSERT INTO message VALUES ('msg_2', 'ses_1', 1750000020000, 1750000020000,
			'{"role":"assistant","time":{"created":1750000020000},"parentID":"msg_1"}')`,
		`INSERT INTO part VALUES ('prt_10', 'msg_2', 'ses_1', 1750000020000, 1750000020000,
			'{"type":"step-start"}')`,
		`INSERT INTO part VALUES ('prt_11', 'msg_2', 'ses_1', 1750000020000, 1750000020000,
			'{"type":"reasoning","text":"hay que revisar el lock","time":{"start":1750000020000}}')`,
		`INSERT INTO part VALUES ('prt_12', 'msg_2', 'ses_1', 1750000021000, 1750000021000,
			'{"type":"tool","callID":"c1","tool":"bash","state":{"status":"completed","input":{"command":"go test"},"output":"FAIL: data race","title":"go test","metadata":{},"time":{"start":1,"end":2}}}')`,
		`INSERT INTO part VALUES ('prt_13', 'msg_2', 'ses_1', 1750000022000, 1750000022000,
			'{"type":"text","text":"voy a arreglar el data race"}')`,
		`INSERT INTO part VALUES ('prt_14', 'msg_2', 'ses_1', 1750000022000, 1750000022000,
			'{"type":"step-finish","reason":"stop","cost":0,"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}}')`,

		// Segunda sesión con title NULL (recién creada, aún sin título): el
		// fallback es el directorio, y el NULL no debe abortar la ingesta.
		`INSERT INTO session VALUES ('ses_2', 'prj_1', NULL, 'untitled',
			'D:\work\api', NULL, NULL, '1.18.3', 1750001000000, 1750001000000)`,
		`INSERT INTO message VALUES ('msg_9', 'ses_2', 1750001000000, 1750001000000,
			'{"role":"user","time":{"created":1750001000000},"agent":"build"}')`,
		`INSERT INTO part VALUES ('prt_90', 'msg_9', 'ses_2', 1750001000000, 1750001000000,
			'{"type":"text","text":"hola"}')`,
	}
	for _, s := range stmts {
		if _, err := sdb.Exec(s); err != nil {
			t.Fatalf("fixture exec: %v\n%s", err, s)
		}
	}
	return path
}

func TestOpencodeSource_Sessions(t *testing.T) {
	path := newOpencodeFixtureDB(t)
	src, err := NewOpencodeSource(WithRoot(path))
	if err != nil {
		t.Fatalf("NewOpencodeSource: %v", err)
	}
	if src.Name() != "opencode" {
		t.Errorf("name = %q", src.Name())
	}

	var chats []*ParsedChat
	for pc, err := range src.Sessions() {
		if err != nil {
			t.Fatalf("Sessions() error: %v", err)
		}
		chats = append(chats, pc)
	}
	if len(chats) != 2 {
		t.Fatalf("got %d sessions, want 2", len(chats))
	}
	pc := chats[0]

	// La sesión con title NULL cae al directorio y no aborta la corrida.
	if chats[1].Chat.ID != "ses_2" || chats[1].Chat.Title != "api" {
		t.Errorf("second chat = %q/%q, want ses_2/api (NULL title falls back to directory)",
			chats[1].Chat.ID, chats[1].Chat.Title)
	}

	if pc.Chat.ID != "ses_1" {
		t.Errorf("chat id = %q", pc.Chat.ID)
	}
	if pc.Chat.Source != "opencode" {
		t.Errorf("source = %q", pc.Chat.Source)
	}
	if pc.Chat.Title != "fix the bug" {
		t.Errorf("title = %q", pc.Chat.Title)
	}
	if pc.Chat.CreatedAt != 1750000000 {
		t.Errorf("created_at = %d, want 1750000000 (ms→s)", pc.Chat.CreatedAt)
	}

	// user, reasoning, tool, assistant; synthetic y step-* se ignoran pero SÍ
	// consumen Seq: el Seq es la posición del part en la sesión, para que un
	// part que pasa de vacío a lleno entre ingestas no desplace a los demás.
	wantRoles := []string{RoleUser, RoleReasoning, RoleTool, RoleAssistant}
	wantSeqs := []int64{1, 4, 5, 6} // prt_02 (synthetic) = 2, prt_10 (step-start) = 3
	if len(pc.Messages) != len(wantRoles) {
		t.Fatalf("got %d messages, want %d: %v", len(pc.Messages), len(wantRoles), roles(pc.Messages))
	}
	for i, want := range wantRoles {
		if pc.Messages[i].Role != want {
			t.Errorf("msg %d role = %q, want %q", i, pc.Messages[i].Role, want)
		}
		if pc.Messages[i].Seq != wantSeqs[i] {
			t.Errorf("msg %d seq = %d, want %d", i, pc.Messages[i].Seq, wantSeqs[i])
		}
	}
	if got := pc.Messages[0].Content; got != "arregla el bug" {
		t.Errorf("user text = %q", got)
	}
	// La tool call y su output viven en el mismo part → un solo mensaje.
	if got := pc.Messages[2].Content; !strings.Contains(got, "bash") ||
		!strings.Contains(got, "go test") || !strings.Contains(got, "data race") {
		t.Errorf("tool content = %q", got)
	}
	// ids estables: el id del part (ULID global).
	if pc.Messages[0].ID != "prt_01" || pc.Messages[2].ID != "prt_12" {
		t.Errorf("ids = %q, %q", pc.Messages[0].ID, pc.Messages[2].ID)
	}
	// timestamps en segundos (la DB los guarda en ms).
	if pc.Messages[0].Timestamp != 1750000010 {
		t.Errorf("timestamp = %d, want 1750000010", pc.Messages[0].Timestamp)
	}
}

func TestOpencodeSource_MissingDBIsEmpty(t *testing.T) {
	src, err := NewOpencodeSource(WithRoot(filepath.Join(t.TempDir(), "no-such", "opencode.db")))
	if err != nil {
		t.Fatalf("NewOpencodeSource: %v", err)
	}
	for pc, err := range src.Sessions() {
		t.Fatalf("expected empty iteration, got (%v, %v)", pc, err)
	}
}

func TestOpencodeSource_IngestIdempotent(t *testing.T) {
	path := newOpencodeFixtureDB(t)
	src, err := NewOpencodeSource(WithRoot(path))
	if err != nil {
		t.Fatalf("NewOpencodeSource: %v", err)
	}

	store, err := db.New(db.WithPath(filepath.Join(t.TempDir(), "nem.db")))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	rep, err := Ingest(store, src)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if rep.Chats != 2 || rep.Messages != 5 {
		t.Fatalf("first ingest: chats=%d messages=%d, want 2/5 (errors: %v)", rep.Chats, rep.Messages, rep.Errors)
	}

	rep2, err := Ingest(store, src)
	if err != nil {
		t.Fatalf("Ingest 2: %v", err)
	}
	if rep2.Messages != 0 {
		t.Errorf("second ingest inserted %d messages, want 0 (idempotent)", rep2.Messages)
	}
}

func TestOpencodeSessionStamps(t *testing.T) {
	path := newOpencodeFixtureDB(t)

	stamps, err := OpencodeSessionStamps(path, time.Time{})
	if err != nil {
		t.Fatalf("OpencodeSessionStamps: %v", err)
	}
	// De más nueva a más antigua: ses_2 (1750001000000) antes que ses_1.
	if len(stamps) != 2 || stamps[0].ID != "ses_2" || stamps[1].ID != "ses_1" {
		t.Fatalf("stamps = %+v, want [ses_2, ses_1]", stamps)
	}
	if got := stamps[1].Updated.UnixMilli(); got != 1750000600000 {
		t.Errorf("updated = %d, want 1750000600000", got)
	}

	// Ventana que solo alcanza a la más nueva.
	stamps, err = OpencodeSessionStamps(path, time.UnixMilli(1750000600001))
	if err != nil {
		t.Fatalf("OpencodeSessionStamps(since): %v", err)
	}
	if len(stamps) != 1 || stamps[0].ID != "ses_2" {
		t.Errorf("stamps since = %+v, want [ses_2]", stamps)
	}

	// DB inexistente: nil sin error (opencode no instalado).
	stamps, err = OpencodeSessionStamps(filepath.Join(t.TempDir(), "nope.db"), time.Time{})
	if err != nil || stamps != nil {
		t.Errorf("missing db = (%v, %v), want (nil, nil)", stamps, err)
	}
}

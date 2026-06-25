package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dieg0Code/nem/internal/db"
)

func TestCodexParser_Parse(t *testing.T) {
	const session = `
{"timestamp":"2026-02-16T21:32:36.689Z","type":"session_meta","payload":{"id":"019c685e-7fe0-72c1-9bb7-809cec15e170","timestamp":"2026-02-16T21:32:29.538Z","cwd":"c:\\Users\\Diego Obando\\ai-lab\\nano-language-model"}}
{"timestamp":"2026-02-16T21:33:00.000Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"system instructions"}]}}
{"timestamp":"2026-02-16T21:33:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"implementa el decay"}]}}
{"timestamp":"2026-02-16T21:33:02.000Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"**Planning the decay**"}],"content":null}}
{"timestamp":"2026-02-16T21:33:03.000Z","type":"response_item","payload":{"type":"function_call","name":"shell_command","arguments":"{\"command\":\"go test\"}","call_id":"c1"}}
{"timestamp":"2026-02-16T21:33:04.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"ok 0.3s"}}
{"timestamp":"2026-02-16T21:33:05.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"listo, apliqué el decay"}]}}
{"timestamp":"2026-02-16T21:33:06.000Z","type":"event_msg","payload":{"type":"token_count"}}
`
	pc, err := NewCodexParser().Parse(strings.NewReader(session), "rollout-019c685e-7fe0-72c1-9bb7-809cec15e170.jsonl")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if pc.Chat.ID != "019c685e-7fe0-72c1-9bb7-809cec15e170" {
		t.Errorf("chat id = %q", pc.Chat.ID)
	}
	if pc.Chat.Title != "nano-language-model" {
		t.Errorf("title = %q, want nano-language-model", pc.Chat.Title)
	}
	if pc.Chat.Source != "codex" {
		t.Errorf("source = %q", pc.Chat.Source)
	}

	// developer y token_count se ignoran; quedan: user, reasoning, tool, tool, assistant.
	wantRoles := []string{RoleUser, RoleReasoning, RoleTool, RoleTool, RoleAssistant}
	if len(pc.Messages) != len(wantRoles) {
		t.Fatalf("got %d messages, want %d: %+v", len(pc.Messages), len(wantRoles), roles(pc.Messages))
	}
	for i, want := range wantRoles {
		if pc.Messages[i].Role != want {
			t.Errorf("msg %d role = %q, want %q", i, pc.Messages[i].Role, want)
		}
		if pc.Messages[i].Seq != int64(i+1) {
			t.Errorf("msg %d seq = %d, want %d", i, pc.Messages[i].Seq, i+1)
		}
	}
	if got := pc.Messages[2].Content; !strings.Contains(got, "shell_command") || !strings.Contains(got, "go test") {
		t.Errorf("tool call content = %q", got)
	}
}

func TestClaudeParser_Parse(t *testing.T) {
	const session = `
{"type":"permission-mode","permissionMode":"default","sessionId":"sess-1"}
{"type":"user","uuid":"u1","timestamp":"2026-05-09T00:31:46.588Z","sessionId":"sess-1","cwd":"C:\\Users\\Diego\\dev\\nem","message":{"role":"user","content":"arregla el bug"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-05-09T00:31:50.000Z","sessionId":"sess-1","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hay que revisar el lock"},{"type":"text","text":"voy a revisar"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test"}}]}}
{"type":"user","uuid":"u2","timestamp":"2026-05-09T00:32:00.000Z","sessionId":"sess-1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"FAIL: data race"}]}}
{"type":"ai-title","title":"fix the bug"}
`
	pc, err := NewClaudeParser().Parse(strings.NewReader(session), "sess-1.jsonl")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if pc.Chat.ID != "sess-1" {
		t.Errorf("chat id = %q", pc.Chat.ID)
	}
	if pc.Chat.Title != "nem" {
		t.Errorf("title = %q, want nem", pc.Chat.Title)
	}

	// user(str), reasoning, assistant text, tool_use, tool_result
	wantRoles := []string{RoleUser, RoleReasoning, RoleAssistant, RoleTool, RoleTool}
	if len(pc.Messages) != len(wantRoles) {
		t.Fatalf("got %d messages, want %d: %v", len(pc.Messages), len(wantRoles), roles(pc.Messages))
	}
	for i, want := range wantRoles {
		if pc.Messages[i].Role != want {
			t.Errorf("msg %d role = %q, want %q", i, pc.Messages[i].Role, want)
		}
	}
	if got := pc.Messages[4].Content; !strings.Contains(got, "data race") {
		t.Errorf("tool_result content = %q", got)
	}
	// ids estables derivados del uuid + índice de bloque
	if pc.Messages[1].ID != "a1:0" || pc.Messages[2].ID != "a1:1" {
		t.Errorf("block ids = %q, %q", pc.Messages[1].ID, pc.Messages[2].ID)
	}
}

func TestAntigravityParser_Parse(t *testing.T) {
	const session = `
{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-05-22T23:14:50Z","content":"<USER_REQUEST>\narregla el bug\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nThe current local time is: 2026-05-22T19:14:50-04:00.\n</ADDITIONAL_METADATA>"}
{"step_index":1,"source":"SYSTEM","type":"CONVERSATION_HISTORY","status":"DONE","created_at":"2026-05-22T23:14:50Z"}
{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-05-22T23:14:51Z","content":"Voy a listar el directorio.","tool_calls":[{"name":"list_dir","args":{"DirectoryPath":"\"C:\\\\Users\\\\Diego Obando\\\\dev\\\\nem\""}}]}
{"step_index":3,"source":"MODEL","type":"LIST_DIRECTORY","status":"DONE","created_at":"2026-05-22T23:14:52Z","content":"{\"name\":\"main.go\"}"}
{"step_index":4,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-05-22T23:14:53Z","content":"Listo, arreglado."}
{"step_index":5,"source":"SYSTEM","type":"ERROR_MESSAGE","status":"DONE","created_at":"2026-05-22T23:14:54Z","content":"transient error"}
`
	path := filepath.Join("brain", "febc0852-250c-4f04-844f-8940e7e3e33a", ".system_generated", "logs", "transcript.jsonl")
	pc, err := NewAntigravityParser().Parse(strings.NewReader(session), path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if pc.Chat.ID != "febc0852-250c-4f04-844f-8940e7e3e33a" {
		t.Errorf("chat id = %q", pc.Chat.ID)
	}
	if pc.Chat.Title != "nem" {
		t.Errorf("title = %q, want nem", pc.Chat.Title)
	}
	if pc.Chat.Source != "antigravity" {
		t.Errorf("source = %q", pc.Chat.Source)
	}

	// user(desenvuelto), assistant(narración)+tool(list_dir), tool(salida), assistant.
	// SYSTEM (history, error) se ignora.
	wantRoles := []string{RoleUser, RoleAssistant, RoleTool, RoleTool, RoleAssistant}
	if len(pc.Messages) != len(wantRoles) {
		t.Fatalf("got %d messages, want %d: %v", len(pc.Messages), len(wantRoles), roles(pc.Messages))
	}
	for i, want := range wantRoles {
		if pc.Messages[i].Role != want {
			t.Errorf("msg %d role = %q, want %q", i, pc.Messages[i].Role, want)
		}
	}
	if got := pc.Messages[0].Content; got != "arregla el bug" {
		t.Errorf("user text = %q, want unwrapped 'arregla el bug'", got)
	}
	if got := pc.Messages[2].Content; !strings.Contains(got, "list_dir") || !strings.Contains(got, "DirectoryPath") {
		t.Errorf("tool call content = %q", got)
	}
	// ids estables derivados de conv-id + step + block.
	if pc.Messages[1].ID != "febc0852-250c-4f04-844f-8940e7e3e33a:2:0" ||
		pc.Messages[2].ID != "febc0852-250c-4f04-844f-8940e7e3e33a:2:1" {
		t.Errorf("block ids = %q, %q", pc.Messages[1].ID, pc.Messages[2].ID)
	}
}

func TestAntigravityParser_SkipsNonTranscript(t *testing.T) {
	// El walker compartido encuentra transcript_full.jsonl e history.jsonl; el
	// parser debe ignorarlos (devolver chat sin mensajes -> contado skipped).
	const full = `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-05-22T23:14:50Z","content":"hola"}`
	for _, name := range []string{"transcript_full.jsonl", "history.jsonl"} {
		pc, err := NewAntigravityParser().Parse(strings.NewReader(full), filepath.Join("brain", "id", ".system_generated", "logs", name))
		if err != nil {
			t.Fatalf("Parse(%s) error = %v", name, err)
		}
		if len(pc.Messages) != 0 {
			t.Errorf("Parse(%s) returned %d messages, want 0 (skipped)", name, len(pc.Messages))
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"corto no se toca", "hola", 10, "hola"},
		{"exacto no se toca", "hola", 4, "hola"},
		{"largo se trunca", "holamundo", 4, "hola\n…[truncado]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncate(tt.in, tt.max); got != tt.want {
				t.Errorf("truncate(%q,%d) = %q, want %q", tt.in, tt.max, got, tt.want)
			}
		})
	}
}

func TestIngest_OrchestrationIdempotent(t *testing.T) {
	// Fixture: una sesión claude mínima en un root temporal.
	root := t.TempDir()
	session := `{"type":"user","uuid":"u1","timestamp":"2026-05-09T00:31:46.588Z","sessionId":"sess-x","cwd":"/home/x/proj","message":{"role":"user","content":"hola decay"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-05-09T00:31:50.000Z","sessionId":"sess-x","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}
`
	if err := os.WriteFile(filepath.Join(root, "sess-x.jsonl"), []byte(session), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := db.New(db.WithPath(filepath.Join(t.TempDir(), "nem.db")))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	rep, err := Ingest(store, NewClaudeParser(), WithRoot(root))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if rep.Chats != 1 || rep.Messages != 2 {
		t.Fatalf("first ingest: chats=%d messages=%d, want 1/2", rep.Chats, rep.Messages)
	}

	// Segunda corrida: idempotente, 0 mensajes nuevos.
	rep2, err := Ingest(store, NewClaudeParser(), WithRoot(root))
	if err != nil {
		t.Fatalf("Ingest 2: %v", err)
	}
	if rep2.Messages != 0 {
		t.Errorf("second ingest inserted %d messages, want 0 (idempotent)", rep2.Messages)
	}

	n, err := store.CountMessages("sess-x")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("CountMessages = %d, want 2", n)
	}
}

// roles devuelve los roles de los mensajes (helper de diagnóstico para los tests).
func roles(msgs []db.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role
	}
	return out
}

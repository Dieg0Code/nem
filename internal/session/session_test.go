package session

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// clearAgentEnv neutraliza las env de agente para que un test del fallback por
// mtime sea determinista incluso corriendo dentro de un agente que las setea.
func clearAgentEnv(t *testing.T) {
	t.Helper()
	for _, e := range agentSessionEnv {
		t.Setenv(e.key, "")
	}
}

func TestDetect_PrefersClaudeEnv(t *testing.T) {
	clearAgentEnv(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "fdc78269-aaaa-bbbb-cccc-000000000000")

	d, err := NewDetector(WithClaudeRoot(t.TempDir()), WithCodexRoot(t.TempDir()))
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	s, err := d.Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if s == nil || s.ChatID != "fdc78269-aaaa-bbbb-cccc-000000000000" || s.Source != "claude" {
		t.Fatalf("Detect = %+v, want the env chat id / claude", s)
	}
}

func TestDetect_CodexEnv(t *testing.T) {
	clearAgentEnv(t)
	t.Setenv("CODEX_COMPANION_SESSION_ID", "11111111-2222-3333-4444-555555555555")

	d, _ := NewDetector(WithClaudeRoot(t.TempDir()), WithCodexRoot(t.TempDir()))
	s, err := d.Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if s == nil || s.Source != "codex" || s.ChatID != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("Detect = %+v, want the codex env session", s)
	}
}

// TestDetect_FallsBackToNewestFile: sin env, gana el .jsonl más reciente (el
// comportamiento histórico, intacto).
func TestDetect_FallsBackToNewestFile(t *testing.T) {
	clearAgentEnv(t)
	claudeRoot := t.TempDir()
	dir := filepath.Join(claudeRoot, "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(dir, "old-session.jsonl")
	newer := filepath.Join(dir, "new-session.jsonl")
	if err := os.WriteFile(older, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatal(err)
	}

	d, _ := NewDetector(WithClaudeRoot(claudeRoot), WithCodexRoot(t.TempDir()),
		WithAntigravityRoot(t.TempDir()), WithOpencodeDB(filepath.Join(t.TempDir(), "none.db")))
	s, err := d.Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if s == nil || s.ChatID != "new-session" || s.Source != "claude" {
		t.Fatalf("Detect = %+v, want new-session / claude", s)
	}
}

func TestRecent_ReturnsSessionsByModTime(t *testing.T) {
	clearAgentEnv(t)
	codexRoot := t.TempDir()
	claudeRoot := t.TempDir()
	antigravityRoot := t.TempDir()

	codexPath := filepath.Join(codexRoot, "2026", "06", "30", "rollout-2026-06-30T00-00-00-11111111-2222-3333-4444-555555555555.jsonl")
	claudePath := filepath.Join(claudeRoot, "proj", "claude-session.jsonl")
	antiPath := filepath.Join(antigravityRoot, "anti-session", ".system_generated", "logs", "transcript.jsonl")
	for _, p := range []string{codexPath, claudePath, antiPath} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	for p, ts := range map[string]time.Time{
		codexPath:  now.Add(-30 * time.Minute),
		claudePath: now.Add(-10 * time.Minute),
		antiPath:   now.Add(-20 * time.Minute),
	} {
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatal(err)
		}
	}

	got, err := Recent(time.Hour, WithCodexRoot(codexRoot), WithClaudeRoot(claudeRoot), WithAntigravityRoot(antigravityRoot),
		WithOpencodeDB(filepath.Join(t.TempDir(), "none.db")))
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Recent len = %d, want 3: %+v", len(got), got)
	}
	if got[0].Source != "claude" || got[1].Source != "antigravity" || got[2].Source != "codex" {
		t.Fatalf("Recent order = %+v, want claude, antigravity, codex", got)
	}
	if got[1].ChatID != "anti-session" {
		t.Fatalf("antigravity chat id = %q, want anti-session", got[1].ChatID)
	}
}

// newOpencodeSessionDB crea una DB mínima de opencode (solo la tabla session,
// que es lo único que consulta la detección) con una sesión.
func newOpencodeSessionDB(t *testing.T, id string, updated time.Time) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	sdb, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer sdb.Close()
	if _, err := sdb.Exec(`CREATE TABLE session (id TEXT PRIMARY KEY, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := sdb.Exec(`INSERT INTO session VALUES (?, ?, ?)`, id, updated.UnixMilli(), updated.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDetect_OpencodeFromDB(t *testing.T) {
	clearAgentEnv(t)
	claudeRoot := t.TempDir()
	dir := filepath.Join(claudeRoot, "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(dir, "claude-session.jsonl")
	if err := os.WriteFile(older, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatal(err)
	}

	dbPath := newOpencodeSessionDB(t, "ses_active", time.Now())

	d, _ := NewDetector(WithClaudeRoot(claudeRoot), WithCodexRoot(t.TempDir()),
		WithAntigravityRoot(t.TempDir()), WithOpencodeDB(dbPath))
	s, err := d.Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if s == nil || s.ChatID != "ses_active" || s.Source != "opencode" {
		t.Fatalf("Detect = %+v, want ses_active / opencode", s)
	}
}

func TestRecent_IncludesOpencode(t *testing.T) {
	clearAgentEnv(t)
	dbPath := newOpencodeSessionDB(t, "ses_recent", time.Now().Add(-5*time.Minute))

	got, err := Recent(time.Hour,
		WithCodexRoot(t.TempDir()), WithClaudeRoot(t.TempDir()),
		WithAntigravityRoot(t.TempDir()), WithOpencodeDB(dbPath))
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 1 || got[0].ChatID != "ses_recent" || got[0].Source != "opencode" {
		t.Fatalf("Recent = %+v, want [ses_recent/opencode]", got)
	}
}

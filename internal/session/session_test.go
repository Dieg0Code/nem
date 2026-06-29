package session

import (
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

	d, _ := NewDetector(WithClaudeRoot(claudeRoot), WithCodexRoot(t.TempDir()))
	s, err := d.Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if s == nil || s.ChatID != "new-session" || s.Source != "claude" {
		t.Fatalf("Detect = %+v, want new-session / claude", s)
	}
}

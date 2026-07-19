package skill

import (
	"os"
	"path/filepath"
	"testing"
)

// readSkill devuelve el contenido del SKILL.md de nem bajo el home dado.
func readSkill(t *testing.T, root string) (string, bool) {
	t.Helper()
	path := filepath.Join(root, "skills", "nem", "SKILL.md")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b), true
}

func TestInstall_BothAgents(t *testing.T) {
	claude := t.TempDir()
	codex := t.TempDir()
	const content = "SKILL CONTENT"

	inst, err := New(WithClaudeRoot(claude), WithCodexRoot(codex), WithContent(content))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rep, err := inst.Install()
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if len(rep.Installed) != 2 {
		t.Fatalf("installed %d, want 2: %+v", len(rep.Installed), rep.Installed)
	}
	if len(rep.Skipped) != 0 {
		t.Errorf("skipped = %v, want empty", rep.Skipped)
	}
	for _, root := range []string{claude, codex} {
		got, ok := readSkill(t, root)
		if !ok {
			t.Errorf("SKILL.md missing under %s", root)
		}
		if got != content {
			t.Errorf("content under %s = %q, want %q", root, got, content)
		}
	}
}

func TestInstall_Antigravity(t *testing.T) {
	ag := t.TempDir()
	inst, err := New(WithAntigravityRoot(ag), WithContent("X"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rep, err := inst.Install()
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(rep.Installed) != 1 || rep.Installed[0].Agent != "antigravity" {
		t.Fatalf("installed = %+v, want only antigravity", rep.Installed)
	}
	if _, ok := readSkill(t, ag); !ok {
		t.Errorf("SKILL.md missing under antigravity root")
	}
}

func TestInstall_AgentAbsentIsSkipped(t *testing.T) {
	claude := t.TempDir()
	codex := filepath.Join(t.TempDir(), "does-not-exist")

	inst, err := New(WithClaudeRoot(claude), WithCodexRoot(codex), WithContent("x"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rep, err := inst.Install()
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if len(rep.Installed) != 1 || rep.Installed[0].Agent != "claude" {
		t.Errorf("installed = %+v, want only claude", rep.Installed)
	}
	if len(rep.Skipped) != 1 || rep.Skipped[0] != "codex" {
		t.Errorf("skipped = %v, want [codex]", rep.Skipped)
	}
	if _, err := os.Stat(filepath.Join(codex, "skills")); !os.IsNotExist(err) {
		t.Errorf("skills/ should not be created under absent agent root")
	}
}

func TestInstall_IdempotentOverwrite(t *testing.T) {
	claude := t.TempDir()
	inst, err := New(WithClaudeRoot(claude), WithContent("TEMPLATE"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := inst.Install(); err != nil {
		t.Fatalf("first install: %v", err)
	}
	// Ensuciar el archivo y reinstalar.
	path := filepath.Join(claude, "skills", "nem", "SKILL.md")
	if err := os.WriteFile(path, []byte("DIRTY"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := inst.Install(); err != nil {
		t.Fatalf("second install: %v", err)
	}
	got, _ := readSkill(t, claude)
	if got != "TEMPLATE" {
		t.Errorf("after reinstall content = %q, want TEMPLATE", got)
	}
}

func TestInstall_CreatesSkillsChain(t *testing.T) {
	claude := t.TempDir() // existe, pero sin subdir skills/
	inst, _ := New(WithClaudeRoot(claude), WithContent("x"))
	if _, err := inst.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, ok := readSkill(t, claude); !ok {
		t.Errorf("SKILL.md not written when skills/ was missing")
	}
}

func TestInstall_DoesNotTouchSiblingSkills(t *testing.T) {
	claude := t.TempDir()
	other := filepath.Join(claude, "skills", "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	otherFile := filepath.Join(other, "SKILL.md")
	if err := os.WriteFile(otherFile, []byte("OTHER"), 0o644); err != nil {
		t.Fatal(err)
	}

	inst, _ := New(WithClaudeRoot(claude), WithContent("ARC"))
	if _, err := inst.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}

	b, err := os.ReadFile(otherFile)
	if err != nil || string(b) != "OTHER" {
		t.Errorf("sibling skill modified: got %q err=%v", string(b), err)
	}
}

func TestInstall_DefaultContentIsEmbeddedTemplate(t *testing.T) {
	claude := t.TempDir()
	inst, err := New(WithClaudeRoot(claude)) // sin WithContent
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := inst.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got, _ := readSkill(t, claude)
	if got != Template() {
		t.Errorf("default content does not match embedded Template()")
	}
	if Template() == "" {
		t.Error("embedded Template() is empty")
	}
}

func TestInstall_Opencode(t *testing.T) {
	oc := t.TempDir()
	inst, err := New(WithOpencodeRoot(oc), WithContent("X"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rep, err := inst.Install()
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(rep.Installed) != 1 || rep.Installed[0].Agent != "opencode" {
		t.Fatalf("installed = %+v, want only opencode", rep.Installed)
	}
	if _, ok := readSkill(t, oc); !ok {
		t.Errorf("SKILL.md missing under opencode root")
	}
}

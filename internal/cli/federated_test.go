package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dieg0Code/nem/internal/config"
	"github.com/Dieg0Code/nem/internal/db"
	"github.com/Dieg0Code/nem/internal/output"
	"github.com/spf13/cobra"
)

// seedMsg crea un store en dbPath con un chat y un mensaje de conversación.
func seedMsg(t *testing.T, dbPath, chatID, title, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := db.New(db.WithPath(dbPath))
	if err != nil {
		t.Fatalf("db.New(%s): %v", dbPath, err)
	}
	defer store.Close()
	if err := store.UpsertChat(&db.Chat{ID: chatID, Title: title, Source: "manual", CreatedAt: 1700000000}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertMessages([]db.Message{
		{ID: chatID + ":1", ChatID: chatID, Role: "user", Content: content, Seq: 1},
	}); err != nil {
		t.Fatal(err)
	}
}

// seedTeam crea y registra un team store con un commit resoluble por hash.
func seedTeam(t *testing.T, name, content string) string {
	t.Helper()
	dir, err := config.TeamDir(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath, _ := config.TeamDBPath(name)
	store, err := db.New(db.WithPath(dbPath))
	if err != nil {
		t.Fatalf("team db.New: %v", err)
	}
	chatID := name + "-c1"
	if err := store.UpsertChat(&db.Chat{ID: chatID, Title: name + " proj", Source: "manual", CreatedAt: 1700000000}); err != nil {
		t.Fatal(err)
	}
	msgs := []db.Message{{ID: chatID + ":1", ChatID: chatID, Role: "user", Content: content, Seq: 1}}
	if _, err := store.InsertMessages(msgs); err != nil {
		t.Fatal(err)
	}
	snap, _ := output.BuildSnapshot(msgs)
	hash := name + "team0commit"
	if err := store.CreateCommit(&db.Commit{
		Hash: hash, ChatID: chatID, Branch: "main",
		Message: "team decision", Snapshot: snap, CreatedAt: 1700000100,
		MsgFrom: chatID + ":1", MsgTo: chatID + ":1",
	}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if err := config.AddTeam(name, config.Team{URL: "x"}); err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestFederatedSearch(t *testing.T) {
	t.Setenv("NEM_HOME", t.TempDir())

	dbPath, _ := config.DBPath()
	seedMsg(t, dbPath, "p1", "personal proj", "deployment pipeline personal note")
	seedTeam(t, "acme", "deployment runbook for the whole team")

	// Federado (default): aparecen ambos orígenes etiquetados.
	out := runCmd(t, func(cmd *cobra.Command) error {
		return runSearch(cmd, "deployment", 10, output.FormatMarkdown, "", "hybrid", true, "", false)
	})
	if !strings.Contains(out, "[personal") {
		t.Errorf("federated search missing personal tag:\n%s", out)
	}
	if !strings.Contains(out, "[team:acme") {
		t.Errorf("federated search missing team tag:\n%s", out)
	}

	// --local: solo personal.
	local := runCmd(t, func(cmd *cobra.Command) error {
		return runSearch(cmd, "deployment", 10, output.FormatMarkdown, "", "hybrid", true, "", true)
	})
	if strings.Contains(local, "team:acme") {
		t.Errorf("--local leaked team results:\n%s", local)
	}

	// --team acme: solo el team.
	only := runCmd(t, func(cmd *cobra.Command) error {
		return runSearch(cmd, "deployment", 10, output.FormatMarkdown, "", "hybrid", true, "acme", false)
	})
	if strings.Contains(only, "[personal") {
		t.Errorf("--team acme leaked personal results:\n%s", only)
	}
	if !strings.Contains(only, "[team:acme") {
		t.Errorf("--team acme missing team results:\n%s", only)
	}
}

func TestFederatedReadResolvesTeamCommit(t *testing.T) {
	t.Setenv("NEM_HOME", t.TempDir())

	dbPath, _ := config.DBPath()
	seedMsg(t, dbPath, "p1", "personal", "unrelated personal content")
	hash := seedTeam(t, "acme", "the team decision content")

	// Federado: el hash vive solo en el team → se resuelve y se etiqueta.
	out := runCmd(t, func(cmd *cobra.Command) error {
		return runRead(cmd, "", hash, output.FormatLLM, "")
	})
	if !strings.Contains(out, "team:acme") {
		t.Errorf("read did not tag team origin:\n%s", out)
	}
	if !strings.Contains(out, "team decision content") {
		t.Errorf("read did not render team commit content:\n%s", out)
	}
}

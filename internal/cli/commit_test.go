package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dieg0Code/nem/internal/config"
	"github.com/Dieg0Code/nem/internal/db"
	"github.com/spf13/cobra"
)

func TestCommitInto_DeterministicHash(t *testing.T) {
	staged := []db.Message{
		{ID: "c1:1", ChatID: "c1", Role: "user", Content: "hola", Seq: 1},
		{ID: "c1:2", ChatID: "c1", Role: "assistant", Content: "chao", Seq: 2},
	}
	a := testStore(t)
	b := testStore(t)
	c1, err := commitInto(a, "c1", "msg", "tester", staged)
	if err != nil {
		t.Fatalf("commitInto a: %v", err)
	}
	c2, err := commitInto(b, "c1", "msg", "tester", staged)
	if err != nil {
		t.Fatalf("commitInto b: %v", err)
	}
	if c1.Hash != c2.Hash {
		t.Errorf("hash not deterministic: %s vs %s", c1.Hash, c2.Hash)
	}
}

// registerEmptyTeam crea y registra un team store vacío bajo el NEM_HOME actual.
func registerEmptyTeam(t *testing.T, name string) {
	t.Helper()
	dir, err := config.TeamDir(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath, _ := config.TeamDBPath(name)
	s, err := db.New(db.WithPath(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := config.AddTeam(name, config.Team{URL: "x"}); err != nil {
		t.Fatal(err)
	}
}

// seedPersonalStaged crea el store personal con un chat, mensajes y staging listo
// para commitear.
func seedPersonalStaged(t *testing.T, chatID string) {
	t.Helper()
	dbPath, _ := config.DBPath()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := db.New(db.WithPath(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertChat(&db.Chat{ID: chatID, Title: "proj", Source: "manual", CreatedAt: 1700000000}); err != nil {
		t.Fatal(err)
	}
	msgs := []db.Message{
		{ID: chatID + ":1", ChatID: chatID, Role: "user", Content: "auth via JWT works", Seq: 1},
		{ID: chatID + ":2", ChatID: chatID, Role: "assistant", Content: "noted, JWT it is", Seq: 2},
	}
	if _, err := s.InsertMessages(msgs); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StageMessages(chatID, msgs); err != nil {
		t.Fatal(err)
	}
}

func TestCommitToTeam(t *testing.T) {
	t.Setenv("NEM_HOME", t.TempDir())
	seedPersonalStaged(t, "c1")
	registerEmptyTeam(t, "acme")

	out := runCmd(t, func(cmd *cobra.Command) error {
		return runCommit(cmd, "c1", "auth decision", "acme")
	})
	if !strings.Contains(out, "team:acme") {
		t.Errorf("commit output missing team tag:\n%s", out)
	}

	// El commit vive en el team store, con su contenido buscable (mensajes copiados).
	dbPath, _ := config.TeamDBPath("acme")
	ts, _ := db.New(db.WithPath(dbPath))
	defer ts.Close()
	commits, err := ts.ListAllCommits()
	if err != nil || len(commits) != 1 {
		t.Fatalf("team commits = %d (%v), want 1", len(commits), err)
	}
	if commits[0].ChatID != "c1" {
		t.Errorf("team commit chat = %q, want c1", commits[0].ChatID)
	}

	// El staging personal quedó limpio y el personal NO tiene el commit.
	ps, _ := openStore()
	defer ps.Close()
	if n, _ := ps.CountStaged("c1"); n != 0 {
		t.Errorf("personal staging = %d, want 0", n)
	}
	if pc, _ := ps.ListAllCommits(); len(pc) != 0 {
		t.Errorf("personal commits = %d, want 0 (committed to team only)", len(pc))
	}
}

func TestPromoteIdempotent(t *testing.T) {
	t.Setenv("NEM_HOME", t.TempDir())
	seedPersonalStaged(t, "c1")
	registerEmptyTeam(t, "acme")

	// Commit personal primero.
	ps, _ := openStore()
	staged, _ := ps.StagedMessages("c1")
	commit, err := commitInto(ps, "c1", "auth decision", "tester", staged)
	if err != nil {
		t.Fatal(err)
	}
	ps.Close()

	// Promover dos veces: la segunda es no-op.
	first := runCmd(t, func(cmd *cobra.Command) error {
		return runCommitPromote(cmd, commit.Hash, "acme")
	})
	if !strings.Contains(first, "promoted commit") {
		t.Errorf("first promote = %q, want 'promoted commit'", first)
	}
	second := runCmd(t, func(cmd *cobra.Command) error {
		return runCommitPromote(cmd, commit.Hash, "acme")
	})
	if !strings.Contains(second, "already in team") {
		t.Errorf("second promote = %q, want 'already in team'", second)
	}

	dbPath, _ := config.TeamDBPath("acme")
	ts, _ := db.New(db.WithPath(dbPath))
	defer ts.Close()
	if commits, _ := ts.ListAllCommits(); len(commits) != 1 {
		t.Errorf("team commits after double promote = %d, want 1", len(commits))
	}
}

func TestFactAddToTeam(t *testing.T) {
	t.Setenv("NEM_HOME", t.TempDir())
	seedMsg(t, mustDBPath(t), "p1", "personal", "x") // initialize personal store
	registerEmptyTeam(t, "acme")

	out := runCmd(t, func(cmd *cobra.Command) error {
		return runFactAdd(cmd, "the team uses trunk-based dev", factAddOpts{team: "acme"})
	})
	if !strings.Contains(out, "team:acme") {
		t.Errorf("fact add output missing team tag:\n%s", out)
	}

	dbPath, _ := config.TeamDBPath("acme")
	ts, _ := db.New(db.WithPath(dbPath))
	defer ts.Close()
	facts, _ := ts.ListFacts(false)
	if len(facts) != 1 {
		t.Fatalf("team facts = %d, want 1", len(facts))
	}

	// El fact NO está en el personal.
	ps, _ := openStore()
	defer ps.Close()
	if pf, _ := ps.ListFacts(false); len(pf) != 0 {
		t.Errorf("personal facts = %d, want 0", len(pf))
	}
}

// TestTeamFactLifecycle cubre que un fact de equipo se pueda gestionar tras
// crearlo: list/done sobre el team store (el gap que marcó el gate).
func TestTeamFactLifecycle(t *testing.T) {
	t.Setenv("NEM_HOME", t.TempDir())
	seedMsg(t, mustDBPath(t), "p1", "personal", "x")
	registerEmptyTeam(t, "acme")

	// Un reminder en el team.
	if err := runFactAdd(&cobra.Command{}, "deploy el viernes", factAddOpts{team: "acme", due: "2099-01-01"}); err != nil {
		t.Fatalf("fact add --team: %v", err)
	}

	// list --team lo muestra.
	list := runCmd(t, func(cmd *cobra.Command) error { return runFactList(cmd, false, "acme") })
	if !strings.Contains(list, "deploy el viernes") {
		t.Errorf("fact list --team did not show the reminder:\n%s", list)
	}

	// Resolver su id desde el team store y marcarlo done --team.
	dbPath, _ := config.TeamDBPath("acme")
	ts, _ := db.New(db.WithPath(dbPath))
	all, _ := ts.ListFacts(false)
	ts.Close()
	if len(all) != 1 {
		t.Fatalf("team facts = %d, want 1", len(all))
	}
	if err := runFactDone(&cobra.Command{}, all[0].ID, "acme"); err != nil {
		t.Fatalf("fact done --team: %v", err)
	}

	// Ya no aparece entre los vigentes del team.
	ts2, _ := db.New(db.WithPath(dbPath))
	defer ts2.Close()
	if vig, _ := ts2.ListFacts(false); len(vig) != 0 {
		t.Errorf("team reminder still active after done: %+v", vig)
	}
}

func mustDBPath(t *testing.T) string {
	t.Helper()
	p, err := config.DBPath()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

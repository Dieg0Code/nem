package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dieg0Code/nem/internal/config"
	"github.com/Dieg0Code/nem/internal/db"
	"github.com/Dieg0Code/nem/internal/output"
	"github.com/Dieg0Code/nem/internal/sync"
	"github.com/spf13/cobra"
)

// gitIdentity fija una identidad git por entorno para que los commits del Syncer
// no dependan de la config global de la máquina (como en CI).
func gitIdentity(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "nem-test")
	t.Setenv("GIT_AUTHOR_EMAIL", "nem@test.local")
	t.Setenv("GIT_COMMITTER_NAME", "nem-test")
	t.Setenv("GIT_COMMITTER_EMAIL", "nem@test.local")
}

// bareRemoteWithCommit crea un remote git bare con un commit publicado, simulando
// un store de equipo que ya tiene contenido. Devuelve la URL (ruta) del bare.
func bareRemoteWithCommit(t *testing.T) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "team.git")
	if out, err := exec.Command("git", "init", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}

	// Publicador: un store sembrado que pushea al bare.
	pubDir := t.TempDir()
	store, err := db.New(db.WithPath(filepath.Join(pubDir, "nem.db")))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer store.Close()

	if err := store.UpsertChat(&db.Chat{ID: "c1", Title: "proj", Source: "manual", CreatedAt: 1700000000}); err != nil {
		t.Fatal(err)
	}
	msgs := []db.Message{
		{ID: "c1:1", ChatID: "c1", Role: "user", Content: "cómo hacemos el auth", Seq: 1},
		{ID: "c1:2", ChatID: "c1", Role: "assistant", Content: "resuelto con JWT", Seq: 2},
	}
	if _, err := store.InsertMessages(msgs); err != nil {
		t.Fatal(err)
	}
	snap, _ := output.BuildSnapshot(msgs)
	if err := store.CreateCommit(&db.Commit{
		Hash: "abc123def456", ChatID: "c1", Branch: "main",
		Message: "decisión auth", Snapshot: snap, CreatedAt: 1700000100,
		MsgFrom: "c1:1", MsgTo: "c1:2",
	}); err != nil {
		t.Fatal(err)
	}

	if err := sync.RemoteAdd(pubDir, "origin", bare); err != nil {
		t.Fatalf("RemoteAdd: %v", err)
	}
	syncer, err := sync.NewSyncer(store, sync.WithDir(pubDir))
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	if _, err := syncer.Sync(); err != nil {
		t.Fatalf("publisher Sync: %v", err)
	}
	return bare
}

// runCmd ejecuta una RunE capturando stdout.
func runCmd(t *testing.T, run func(cmd *cobra.Command) error) string {
	t.Helper()
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := run(cmd); err != nil {
		t.Fatalf("command error: %v", err)
	}
	return buf.String()
}

func TestTeamAddListRemove(t *testing.T) {
	gitIdentity(t)
	bare := bareRemoteWithCommit(t)
	t.Setenv("NEM_HOME", t.TempDir())

	// add: clona e importa el commit publicado.
	out := runCmd(t, func(cmd *cobra.Command) error { return runTeamAdd(cmd, "acme", bare) })
	if !strings.Contains(out, "imported 1 commits") {
		t.Errorf("team add output = %q, want imported 1", out)
	}
	if _, ok, err := config.GetTeam("acme"); err != nil || !ok {
		t.Fatalf("team not registered: ok=%v err=%v", ok, err)
	}

	// El commit del equipo está en la DB del team store.
	dbPath, _ := config.TeamDBPath("acme")
	tstore, err := db.New(db.WithPath(dbPath))
	if err != nil {
		t.Fatalf("open team db: %v", err)
	}
	c, err := tstore.GetCommit("abc123def456")
	tstore.Close()
	if err != nil || c == nil {
		t.Fatalf("team commit not imported: %v", err)
	}

	// add de nuevo falla (ya registrado).
	if err := runTeamAdd(&cobra.Command{}, "acme", bare); err == nil {
		t.Error("second team add = nil, want already-added error")
	}

	// list muestra el team y lo marca ready.
	list := runCmd(t, func(cmd *cobra.Command) error { return runTeamList(cmd) })
	if !strings.Contains(list, "acme") || !strings.Contains(list, "ready") {
		t.Errorf("team list = %q, want acme/ready", list)
	}

	// remove --purge: desregistra y borra del disco.
	dir, _ := config.TeamDir("acme")
	runCmd(t, func(cmd *cobra.Command) error { return runTeamRemove(cmd, "acme", true) })
	if _, ok, _ := config.GetTeam("acme"); ok {
		t.Error("team still registered after remove")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("team dir still exists after purge: %v", err)
	}
}

func TestTeamAddRejectsBadName(t *testing.T) {
	t.Setenv("NEM_HOME", t.TempDir())
	if err := runTeamAdd(&cobra.Command{}, "bad/name", "irrelevant"); err == nil {
		t.Error("team add bad/name = nil, want error")
	}
}

func TestTeamSyncUnknown(t *testing.T) {
	t.Setenv("NEM_HOME", t.TempDir())
	if err := runTeamSync(&cobra.Command{}, "ghost"); err == nil {
		t.Error("team sync ghost = nil, want unknown-team error")
	}
}

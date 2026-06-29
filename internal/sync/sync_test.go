package sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dieg0Code/nem/internal/db"
	"github.com/Dieg0Code/nem/internal/output"
)

// seedStore crea un store temporal con un chat, mensajes y un commit cuyo
// snapshot contiene un secreto.
func seedStore(t *testing.T) (db.Store, string) {
	t.Helper()
	store, err := db.New(db.WithPath(filepath.Join(t.TempDir(), "nem.db")))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.UpsertChat(&db.Chat{ID: "c1", Title: "proj", Source: "codex", CreatedAt: 1700000000}); err != nil {
		t.Fatal(err)
	}
	msgs := []db.Message{
		// Token armado por fragmentos: evita un literal con forma de secreto en
		// el fuente (que dispararía el secret-scanning de GitHub).
		{ID: "c1:1", ChatID: "c1", Role: "user", Content: "mi token es " + "hf_" + "abcdefghijklmnopqrstuvwxyz123456" + " ok", Seq: 1},
		{ID: "c1:2", ChatID: "c1", Role: "assistant", Content: "listo, lo guardo", Seq: 2},
	}
	if _, err := store.InsertMessages(msgs); err != nil {
		t.Fatal(err)
	}
	snap, _ := output.BuildSnapshot(msgs)
	commit := &db.Commit{
		Hash: "deadbeefcafe", ChatID: "c1", Branch: "main",
		Message: "guarda token", Snapshot: snap, CreatedAt: 1700000100,
		MsgFrom: "c1:1", MsgTo: "c1:2",
	}
	if err := store.CreateCommit(commit); err != nil {
		t.Fatal(err)
	}
	return store, "c1"
}

func TestSync_ExportRedactsSecrets(t *testing.T) {
	store, _ := seedStore(t)
	dir := t.TempDir()
	sy, err := NewSyncer(store, WithDir(dir))
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	s := sy.(*syncer)

	n, counts, err := s.export()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if n != 1 {
		t.Fatalf("exported %d commits, want 1", n)
	}
	if counts["huggingface-token"] != 1 {
		t.Errorf("expected 1 hf token redacted, counts=%v", counts)
	}

	// El archivo exportado NO debe contener el secreto en claro.
	data, err := os.ReadFile(filepath.Join(dir, "store", "chats", "deadbeefcafe.jsonl"))
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	if strings.Contains(string(data), "hf_abcdefghij") {
		t.Errorf("secret leaked into export file:\n%s", data)
	}
	if !strings.Contains(string(data), "REDACTED:huggingface-token") {
		t.Errorf("placeholder missing in export file:\n%s", data)
	}
}

func TestSync_ImportRoundTrip(t *testing.T) {
	src, _ := seedStore(t)
	dir := t.TempDir()
	srcSync := mustSyncer(t, src, dir)
	if _, _, err := srcSync.export(); err != nil {
		t.Fatalf("export: %v", err)
	}

	// Store destino vacío: importar desde el mismo dir.
	dst, err := db.New(db.WithPath(filepath.Join(t.TempDir(), "dst.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dst.Close() })
	dstSync := mustSyncer(t, dst, dir)

	imported, err := dstSync.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if imported != 1 {
		t.Fatalf("imported %d, want 1", imported)
	}

	// El commit existe en el destino y su contenido viene redactado.
	c, err := dst.GetCommit("deadbeefcafe")
	if err != nil || c == nil {
		t.Fatalf("commit not imported: %v", err)
	}
	if strings.Contains(c.Snapshot, "hf_abcdefghij") {
		t.Errorf("secret leaked into imported snapshot: %s", c.Snapshot)
	}

	// Idempotencia: reimportar no duplica.
	again, err := dstSync.Import()
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Errorf("second import added %d, want 0", again)
	}
}

func TestSync_FactsRoundTrip(t *testing.T) {
	src, err := db.New(db.WithPath(filepath.Join(t.TempDir(), "src.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	// Un fact con secreto en el contenido y un reminder ya completado (rastro).
	if err := src.AddFact(&db.Fact{
		ID: "f1", Content: "el token es " + "hf_" + "abcdefghijklmnopqrstuvwxyz123456",
		Kind: "note", Source: "human", Author: "diego", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := src.AddFact(&db.Fact{
		ID: "f2", Content: "deploy el viernes", Kind: "reminder",
		Done: true, DoneAt: 50, CreatedAt: 1, UpdatedAt: 2,
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	srcSync := mustSyncer(t, src, dir)
	counts := map[string]int{}
	n, err := srcSync.exportFacts(counts)
	if err != nil {
		t.Fatalf("exportFacts: %v", err)
	}
	if n != 2 {
		t.Fatalf("exported %d facts, want 2", n)
	}
	if counts["huggingface-token"] != 1 {
		t.Errorf("expected 1 hf token redacted, counts=%v", counts)
	}

	// El archivo del fact con secreto no debe contenerlo en claro (un archivo por
	// fact, igual que un archivo por commit).
	data, err := os.ReadFile(filepath.Join(dir, "store", "facts", "f1.jsonl"))
	if err != nil {
		t.Fatalf("read facts export: %v", err)
	}
	if strings.Contains(string(data), "hf_abcdefghij") {
		t.Errorf("secret leaked into facts file:\n%s", data)
	}

	// Importar en un store vacío reconstruye los facts (redactados, con rastro).
	dst, err := db.New(db.WithPath(filepath.Join(t.TempDir(), "dst.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dst.Close() })
	dstSync := mustSyncer(t, dst, dir)
	m, err := dstSync.importFacts()
	if err != nil {
		t.Fatalf("importFacts: %v", err)
	}
	if m != 2 {
		t.Fatalf("imported %d facts, want 2", m)
	}

	f1, _ := dst.GetFact("f1")
	if f1 == nil || strings.Contains(f1.Content, "hf_abcdefghij") {
		t.Errorf("f1 leaked secret or missing: %+v", f1)
	}
	if f1.Author != "diego" {
		t.Errorf("f1 author = %q, want diego", f1.Author)
	}
	f2, _ := dst.GetFact("f2")
	if f2 == nil || !f2.Done {
		t.Errorf("f2 done-state did not survive: %+v", f2)
	}

	// Idempotencia: reimportar no rompe (mismo UpdatedAt → no-op).
	if _, err := dstSync.importFacts(); err != nil {
		t.Fatalf("second importFacts: %v", err)
	}

	// Poda: borrar un fact y re-exportar elimina su archivo (no resucita).
	if err := src.DeleteFact("f2"); err != nil {
		t.Fatalf("DeleteFact: %v", err)
	}
	if _, err := srcSync.exportFacts(map[string]int{}); err != nil {
		t.Fatalf("re-export: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "store", "facts", "f2.jsonl")); !os.IsNotExist(err) {
		t.Errorf("deleted fact file f2.jsonl still present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "store", "facts", "f1.jsonl")); err != nil {
		t.Errorf("surviving fact file f1.jsonl missing: %v", err)
	}
}

// TestSync_FactsExportImportDoesNotClobberLocal protege contra el P1: en un sync
// normal, exportFacts REDACTA el contenido y luego importFacts reimporta ese mismo
// archivo en el MISMO store. El contenido local en claro no debe perderse.
func TestSync_FactsExportImportDoesNotClobberLocal(t *testing.T) {
	src, err := db.New(db.WithPath(filepath.Join(t.TempDir(), "src.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	secret := "el token es " + "hf_" + "abcdefghijklmnopqrstuvwxyz123456"
	if err := src.AddFact(&db.Fact{ID: "f1", Content: secret, Kind: "note", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	sy := mustSyncer(t, src, dir)
	if _, err := sy.exportFacts(map[string]int{}); err != nil {
		t.Fatalf("exportFacts: %v", err)
	}
	// Reimportar en el MISMO store (lo que hace Sync tras el push).
	if _, err := sy.importFacts(); err != nil {
		t.Fatalf("importFacts: %v", err)
	}

	got, _ := src.GetFact("f1")
	if got == nil || got.Content != secret {
		t.Errorf("local fact content clobbered by redacted re-import: got %q, want original", got.Content)
	}
}

func TestImportFacts_RejectsUnsafeID(t *testing.T) {
	dir := t.TempDir()
	factsDir := filepath.Join(dir, "store", "facts")
	if err := os.MkdirAll(factsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Un archivo de fact con un id de path-traversal no debe llegar a la DB.
	evil := `{"type":"fact","id":"../../evil","content":"x","updated_at":1}` + "\n"
	if err := os.WriteFile(filepath.Join(factsDir, "evil.jsonl"), []byte(evil), 0o644); err != nil {
		t.Fatal(err)
	}

	dst, err := db.New(db.WithPath(filepath.Join(t.TempDir(), "dst.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dst.Close() })

	n, err := mustSyncer(t, dst, dir).importFacts()
	if err != nil {
		t.Fatalf("importFacts: %v", err)
	}
	if n != 0 {
		t.Errorf("imported %d unsafe facts, want 0", n)
	}
	if facts, _ := dst.ListFacts(true); len(facts) != 0 {
		t.Errorf("unsafe fact reached the DB: %+v", facts)
	}
}

func TestValidFactID(t *testing.T) {
	good := []string{"658d483b-1e2c-4f00-9a3b-aabbccddeeff", "abc_123", "X-Y-Z"}
	bad := []string{"", ".", "..", "../evil", `a\b`, "a/b", "with space", "dots..dots"}
	for _, id := range good {
		if !validFactID(id) {
			t.Errorf("validFactID(%q) = false, want true", id)
		}
	}
	for _, id := range bad {
		if validFactID(id) {
			t.Errorf("validFactID(%q) = true, want false", id)
		}
	}
}

func mustSyncer(t *testing.T, store db.Store, dir string) *syncer {
	t.Helper()
	s, err := NewSyncer(store, WithDir(dir))
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	return s.(*syncer)
}

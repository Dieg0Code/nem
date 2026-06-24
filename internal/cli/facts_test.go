package cli

import (
	"testing"

	"github.com/Dieg0Code/nem/internal/db"
)

func testStore(t *testing.T) db.Store {
	t.Helper()
	s, err := db.New(db.WithPath(":memory:"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestResolveFact_EmptyID protege contra el bug que encontró el gate: un id vacío
// no debe matchear cualquier fact vía prefijo (HasPrefix(_, "") es siempre true).
func TestResolveFact_EmptyID(t *testing.T) {
	s := testStore(t)
	if err := s.AddFact(&db.Fact{ID: "only", Content: "x", Kind: "note", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("AddFact: %v", err)
	}
	if _, err := resolveFact(s, ""); err == nil {
		t.Error("resolveFact(\"\") = nil error, want error")
	}
	if _, err := resolveFact(s, "  "); err == nil {
		t.Error("resolveFact(whitespace) = nil error, want error")
	}
}

// TestResolveFact_Prefix verifica la resolución por prefijo único.
func TestResolveFact_Prefix(t *testing.T) {
	s := testStore(t)
	_ = s.AddFact(&db.Fact{ID: "abc123", Content: "x", Kind: "note", CreatedAt: 1, UpdatedAt: 1})
	f, err := resolveFact(s, "abc")
	if err != nil || f == nil || f.ID != "abc123" {
		t.Fatalf("resolveFact(prefix) = (%v, %v), want abc123", f, err)
	}
}

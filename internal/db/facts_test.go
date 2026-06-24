package db

import "testing"

// TestFacts_AddListGet cubre el ciclo básico: agregar, listar (newest first) y
// recuperar por id.
func TestFacts_AddListGet(t *testing.T) {
	s := newTestStore(t)

	if err := s.AddFact(&Fact{ID: "a", Content: "older", Kind: "note", Source: "human", CreatedAt: 100, UpdatedAt: 100}); err != nil {
		t.Fatalf("AddFact a: %v", err)
	}
	if err := s.AddFact(&Fact{ID: "b", Content: "newer", Kind: "note", Source: "claude", CreatedAt: 200, UpdatedAt: 200}); err != nil {
		t.Fatalf("AddFact b: %v", err)
	}

	facts, err := s.ListFacts(false)
	if err != nil {
		t.Fatalf("ListFacts: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("ListFacts len = %d, want 2", len(facts))
	}
	if facts[0].ID != "b" {
		t.Errorf("ListFacts not newest-first: got %q first, want b", facts[0].ID)
	}

	got, err := s.GetFact("a")
	if err != nil || got == nil {
		t.Fatalf("GetFact a = (%v, %v)", got, err)
	}
	if got.Content != "older" {
		t.Errorf("GetFact content = %q, want older", got.Content)
	}

	if missing, err := s.GetFact("nope"); err != nil || missing != nil {
		t.Errorf("GetFact(missing) = (%v, %v), want (nil, nil)", missing, err)
	}
}

// TestFacts_Supersede verifica el alma append-only: superseder no borra, deja
// rastro y saca al viejo de la lista de vigentes.
func TestFacts_Supersede(t *testing.T) {
	s := newTestStore(t)
	_ = s.AddFact(&Fact{ID: "old", Content: "v1", Kind: "note", CreatedAt: 1, UpdatedAt: 1})
	_ = s.AddFact(&Fact{ID: "new", Content: "v2", Kind: "note", CreatedAt: 2, UpdatedAt: 2})

	if err := s.SupersedeFact("old", "new", 2); err != nil {
		t.Fatalf("SupersedeFact: %v", err)
	}

	active, _ := s.ListFacts(false)
	if len(active) != 1 || active[0].ID != "new" {
		t.Fatalf("active facts = %+v, want only new", active)
	}

	all, _ := s.ListFacts(true)
	if len(all) != 2 {
		t.Fatalf("all facts len = %d, want 2 (trail preserved)", len(all))
	}
	old, _ := s.GetFact("old")
	if !old.Superseded || old.SupersededBy != "new" {
		t.Errorf("old fact not marked superseded: %+v", old)
	}

	if err := s.SupersedeFact("ghost", "new", 3); err == nil {
		t.Error("SupersedeFact(missing) = nil error, want error")
	}
}

// TestFacts_Done verifica que un recordatorio completado sale de las vigentes
// pero se conserva en el rastro (--all).
func TestFacts_Done(t *testing.T) {
	s := newTestStore(t)
	_ = s.AddFact(&Fact{ID: "r", Content: "entregar notas", Kind: "reminder", DueAt: 1000, CreatedAt: 1, UpdatedAt: 1})

	if err := s.MarkFactDone("r", 1234); err != nil {
		t.Fatalf("MarkFactDone: %v", err)
	}

	if active, _ := s.ListFacts(false); len(active) != 0 {
		t.Errorf("done reminder still in active list: %+v", active)
	}
	all, _ := s.ListFacts(true)
	if len(all) != 1 || !all[0].Done || all[0].DoneAt != 1234 {
		t.Errorf("done reminder not preserved correctly: %+v", all)
	}
	if err := s.MarkFactDone("ghost", 1); err == nil {
		t.Error("MarkFactDone(missing) = nil error, want error")
	}
}

// TestFacts_DoneOnlyReminders verifica que un hecho estable (sin fecha) NO se
// puede completar: así un id confundido no oculta memoria semántica durable.
func TestFacts_DoneOnlyReminders(t *testing.T) {
	s := newTestStore(t)
	_ = s.AddFact(&Fact{ID: "note1", Content: "Diego es profe", Kind: "note", CreatedAt: 1, UpdatedAt: 1})

	if err := s.MarkFactDone("note1", 99); err == nil {
		t.Error("MarkFactDone on a stable note = nil error, want error")
	}
	if active, _ := s.ListFacts(false); len(active) != 1 {
		t.Errorf("stable note got hidden after a done attempt: %+v", active)
	}
}

// TestFacts_Delete verifica el borrado de raíz y el error en id inexistente.
func TestFacts_Delete(t *testing.T) {
	s := newTestStore(t)
	_ = s.AddFact(&Fact{ID: "x", Content: "oops", Kind: "note", CreatedAt: 1, UpdatedAt: 1})

	if err := s.DeleteFact("x"); err != nil {
		t.Fatalf("DeleteFact: %v", err)
	}
	if facts, _ := s.ListFacts(true); len(facts) != 0 {
		t.Errorf("after delete, facts = %+v, want empty", facts)
	}
	if err := s.DeleteFact("x"); err == nil {
		t.Error("DeleteFact(missing) = nil error, want error")
	}
}

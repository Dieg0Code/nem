package db

import (
	"testing"
)

func usageStore(t *testing.T) Store {
	t.Helper()
	s, err := New(WithPath(":memory:"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestLogSearch_RoundTripAndPrune verifica el round-trip de LogSearch →
// RecentSearches y que el prune de 24h corre en la misma escritura.
func TestLogSearch_RoundTripAndPrune(t *testing.T) {
	s := usageStore(t)
	now := int64(1_000_000_000)

	// Un log viejo (más de 24h) y uno fresco.
	if err := s.LogSearch("old", "vieja query", []string{"commit:aaa"}, now-searchLogTTL-10); err != nil {
		t.Fatalf("LogSearch old: %v", err)
	}
	if err := s.LogSearch("new", "team memory", []string{"commit:bbb", "chat:c1"}, now); err != nil {
		t.Fatalf("LogSearch new: %v", err)
	}

	logs, err := s.RecentSearches(now - 900)
	if err != nil {
		t.Fatalf("RecentSearches: %v", err)
	}
	if len(logs) != 1 || logs[0].ID != "new" {
		t.Fatalf("expected only the fresh log, got %+v", logs)
	}
	if logs[0].ServedIDs != `["commit:bbb","chat:c1"]` {
		t.Errorf("served ids not preserved: %q", logs[0].ServedIDs)
	}

	// El log viejo fue pruneado por la segunda escritura (ventana amplia).
	all, err := s.RecentSearches(0)
	if err != nil {
		t.Fatalf("RecentSearches all: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("old log should have been pruned; got %+v", all)
	}
}

// TestRecordNodeTermHits_Upsert verifica que el par (término, nodo) nace en 1 y
// acumula en escrituras siguientes, actualizando LastHit.
func TestRecordNodeTermHits_Upsert(t *testing.T) {
	s := usageStore(t)
	if _, err := s.UpsertNodes([]Node{{ID: "commit:abc", Kind: "commit", Title: "x"}}); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	if err := s.RecordNodeTermHits([]string{"auth", "jwt"}, "commit:abc", 100); err != nil {
		t.Fatalf("RecordNodeTermHits 1: %v", err)
	}
	if err := s.RecordNodeTermHits([]string{"auth"}, "commit:abc", 200); err != nil {
		t.Fatalf("RecordNodeTermHits 2: %v", err)
	}

	m, err := s.MatchNodeTerms([]string{"auth"}, 10)
	if err != nil {
		t.Fatalf("MatchNodeTerms: %v", err)
	}
	if len(m) != 1 || m[0].TermHits != 2 || m[0].TermLastHit != 200 {
		t.Fatalf("expected auth→commit:abc hits=2 lastHit=200, got %+v", m)
	}
}

// TestMatchNodeTerms_OrderAndLimit verifica el ORDER BY (hits desc) y el LIMIT
// del query crudo: el nodo con más hits agregados sale primero y el corte deja
// fuera al de menos uso.
func TestMatchNodeTerms_OrderAndLimit(t *testing.T) {
	s := usageStore(t)
	nodes := []Node{
		{ID: "commit:hot", Kind: "commit", Title: "caliente"},
		{ID: "commit:warm", Kind: "commit", Title: "tibio"},
		{ID: "commit:cold", Kind: "commit", Title: "frío"},
	}
	if _, err := s.UpsertNodes(nodes); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}
	hits := map[string]int{"commit:hot": 3, "commit:warm": 2, "commit:cold": 1}
	for id, n := range hits {
		for range n {
			if err := s.RecordNodeTermHits([]string{"auth"}, id, 100); err != nil {
				t.Fatalf("RecordNodeTermHits %s: %v", id, err)
			}
		}
	}

	m, err := s.MatchNodeTerms([]string{"auth"}, 2)
	if err != nil {
		t.Fatalf("MatchNodeTerms: %v", err)
	}
	if len(m) != 2 {
		t.Fatalf("LIMIT 2 not honored, got %d rows", len(m))
	}
	if m[0].ID != "commit:hot" || m[1].ID != "commit:warm" {
		t.Errorf("expected order hot(3) > warm(2), got %s(%d) > %s(%d)",
			m[0].ID, m[0].TermHits, m[1].ID, m[1].TermHits)
	}
}

// TestConsumeServedID verifica el dedup: consumir un nodo lo saca del conjunto
// servido del log (una búsqueda atribuye cada nodo una sola vez) y deja el
// resto intacto. Consumir de un log inexistente es un no-op.
func TestConsumeServedID(t *testing.T) {
	s := usageStore(t)
	if err := s.LogSearch("s1", "auth", []string{"commit:a", "commit:b"}, 100); err != nil {
		t.Fatalf("LogSearch: %v", err)
	}

	if err := s.ConsumeServedID("s1", "commit:a"); err != nil {
		t.Fatalf("ConsumeServedID: %v", err)
	}
	logs, err := s.RecentSearches(0)
	if err != nil {
		t.Fatalf("RecentSearches: %v", err)
	}
	if logs[0].ServedIDs != `["commit:b"]` {
		t.Errorf("commit:a should be consumed, commit:b kept; got %q", logs[0].ServedIDs)
	}

	if err := s.ConsumeServedID("nope", "commit:a"); err != nil {
		t.Errorf("consuming from a missing log must be a no-op, got %v", err)
	}
}

// TestMatchNodeTerms_AggregatesAndExcludesSuperseded verifica que la señal se
// agrega (SUM) sobre los términos matcheados y que un nodo superseded jamás
// vuelve por popularidad.
func TestMatchNodeTerms_AggregatesAndExcludesSuperseded(t *testing.T) {
	s := usageStore(t)
	nodes := []Node{
		{ID: "commit:live", Kind: "commit", Title: "vigente"},
		{ID: "commit:dead", Kind: "commit", Title: "muerta", Superseded: true, SupersededBy: "commit:live"},
	}
	if _, err := s.UpsertNodes(nodes); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}

	// El nodo muerto tiene MUCHO más uso histórico que el vigente.
	for range 5 {
		if err := s.RecordNodeTermHits([]string{"auth", "jwt"}, "commit:dead", 100); err != nil {
			t.Fatalf("RecordNodeTermHits dead: %v", err)
		}
	}
	if err := s.RecordNodeTermHits([]string{"auth", "jwt"}, "commit:live", 200); err != nil {
		t.Fatalf("RecordNodeTermHits live: %v", err)
	}
	// Señal huérfana: un nodo que ya no existe en el árbol no debe aparecer.
	if err := s.RecordNodeTermHits([]string{"auth"}, "commit:gone", 300); err != nil {
		t.Fatalf("RecordNodeTermHits gone: %v", err)
	}

	m, err := s.MatchNodeTerms([]string{"auth", "jwt"}, 10)
	if err != nil {
		t.Fatalf("MatchNodeTerms: %v", err)
	}
	if len(m) != 1 {
		t.Fatalf("expected only the live node, got %+v", m)
	}
	if m[0].ID != "commit:live" || m[0].TermHits != 2 {
		t.Errorf("expected commit:live with aggregated hits=2 (auth+jwt), got %+v", m[0])
	}
}

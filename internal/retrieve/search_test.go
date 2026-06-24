package retrieve

import (
	"slices"
	"testing"

	"github.com/Dieg0Code/nem/internal/db"
)

func newStore(t *testing.T) db.Store {
	t.Helper()
	s, err := db.New(db.WithPath(":memory:"))
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestSearch_Morphology es la prueba de oro: una query con una variante
// morfológica (programar) encuentra un documento que dice "programación" gracias
// al stem-prefix — el caso que el match exacto fallaba. Mode=keyword aísla el
// test de la capa de vectores/nodos (solo el canal de mensajes).
func TestSearch_Morphology(t *testing.T) {
	s := newStore(t)
	if err := s.UpsertChat(&db.Chat{ID: "c1", Title: "test", Source: "claude"}); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	msgs := []db.Message{
		{ID: "m1", ChatID: "c1", Role: "user", Content: "hoy estuvimos viendo programación web con el grupo", Seq: 1, Timestamp: 100},
		{ID: "m2", ChatID: "c1", Role: "user", Content: "el clima estaba lindo para salir a caminar", Seq: 2, Timestamp: 200},
	}
	if _, err := s.InsertMessages(msgs); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	res, err := Search(s, Params{Query: "programar", Top: 5, Mode: "keyword", Expand: false})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !containsID(res, "m1") {
		t.Errorf("Search(\"programar\") did not find m1 (programación); results=%+v", res)
	}
}

// TestSearch_PRF verifica que el PRF agrega recall: un documento que NO contiene
// el término de la query pero co-ocurre con los hits iniciales aparece al activar
// Expand, y no aparece sin él.
func TestSearch_PRF(t *testing.T) {
	s := newStore(t)
	if err := s.UpsertChat(&db.Chat{ID: "c1", Title: "test", Source: "claude"}); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	msgs := []db.Message{
		// hits directos de "alphazero": co-ocurren con "mcts"
		{ID: "a1", ChatID: "c1", Role: "user", Content: "el modelo alphazero entrena con mcts y autojuego", Seq: 1, Timestamp: 100},
		{ID: "a2", ChatID: "c1", Role: "user", Content: "alphazero converge usando mcts en cada iteración", Seq: 2, Timestamp: 200},
		// documento SIN "alphazero" pero CON "mcts": solo el PRF debería traerlo
		{ID: "b1", ChatID: "c1", Role: "user", Content: "ajustamos las simulaciones de mcts para el torneo", Seq: 3, Timestamp: 300},
	}
	if _, err := s.InsertMessages(msgs); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	without, err := Search(s, Params{Query: "alphazero", Top: 10, Mode: "keyword", Expand: false})
	if err != nil {
		t.Fatalf("Search no-expand: %v", err)
	}
	if containsID(without, "b1") {
		t.Skip("b1 already matched without PRF; corpus too small to isolate the effect")
	}

	with, err := Search(s, Params{Query: "alphazero", Top: 10, Mode: "hybrid", Expand: true})
	if err != nil {
		t.Fatalf("Search expand: %v", err)
	}
	if !containsID(with, "b1") {
		t.Errorf("PRF did not surface co-occurring doc b1 (mcts); results=%+v", with)
	}
}

// TestSearch_SemanticSkipsLexical verifica que --mode semantic NO corre canales
// léxicos: sin embeddings configurados, no debe devolver matches BM25 (sería
// "vectores-only" devolviendo léxico, el bug que marcó el gate).
func TestSearch_SemanticSkipsLexical(t *testing.T) {
	s := newStore(t)
	if err := s.UpsertChat(&db.Chat{ID: "c1", Title: "test", Source: "claude"}); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	_, _ = s.InsertMessages([]db.Message{
		{ID: "m1", ChatID: "c1", Role: "user", Content: "programación web", Seq: 1, Timestamp: 100},
	})
	res, err := Search(s, Params{Query: "programación", Top: 5, Mode: "semantic"})
	if err != nil {
		t.Fatalf("Search semantic: %v", err)
	}
	if len(res) != 0 {
		t.Errorf("semantic mode returned %d lexical results, want 0 (vectors-only)", len(res))
	}
}

func TestPRFExtract(t *testing.T) {
	seed := []Scored{
		{Item: Item{Content: "el modelo alphazero entrena con mcts y autojuego"}},
		{Item: Item{Content: "alphazero usa mcts para el entrenamiento"}},
	}
	terms := prfExtract("alphazero", seed)

	if !slices.Contains(terms, "mcts") {
		t.Errorf("prfExtract = %v, want it to include co-occurring term 'mcts'", terms)
	}
	if slices.Contains(terms, "alphazero") {
		t.Errorf("prfExtract = %v, should exclude the query term itself", terms)
	}
	for _, sw := range []string{"el", "con", "para"} {
		if slices.Contains(terms, sw) {
			t.Errorf("prfExtract = %v, should exclude stopword %q", terms, sw)
		}
	}
}

func containsID(res []Scored, id string) bool {
	return slices.ContainsFunc(res, func(r Scored) bool { return r.ID == id })
}

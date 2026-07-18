package retrieve

import (
	"testing"
	"time"

	"github.com/Dieg0Code/nem/internal/db"
)

// TestLearnedTerms verifica la normalización: stopwords fuera, tokens cortos
// fuera, variantes morfológicas de la misma familia colapsan al mismo stem.
func TestLearnedTerms(t *testing.T) {
	a := learnedTerms("la programación de esto")
	b := learnedTerms("programar")
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("stopwords/short tokens should drop; got %v / %v", a, b)
	}
	if a[0] != b[0] {
		t.Errorf("programación/programar should normalize equal: %v vs %v", a, b)
	}
	if got := learnedTerms("de la y"); len(got) != 0 {
		t.Errorf("stopword-only query should yield no terms, got %v", got)
	}
}

// TestRecordRead_StrictCorrelation verifica la correlación estricta: solo
// aprende el nodo que fue SERVIDO por una búsqueda reciente; un read de algo
// no servido (p.ej. venido del outline) no genera señal.
func TestRecordRead_StrictCorrelation(t *testing.T) {
	s := newStore(t)
	if _, err := s.UpsertNodes([]db.Node{
		{ID: "commit:served", Kind: "commit", Title: "servido"},
		{ID: "commit:other", Kind: "commit", Title: "no servido"},
	}); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}
	now := time.Now().Unix()
	if err := s.LogSearch("s1", "memoria federada", []string{"commit:served"}, now); err != nil {
		t.Fatalf("LogSearch: %v", err)
	}

	RecordRead(s, "commit:served", now+60) // dentro de la ventana, servido
	RecordRead(s, "commit:other", now+60)  // dentro de la ventana, NO servido

	terms := learnedTerms("memoria federada")
	m, err := s.MatchNodeTerms(terms, 10)
	if err != nil {
		t.Fatalf("MatchNodeTerms: %v", err)
	}
	if len(m) != 1 || m[0].ID != "commit:served" {
		t.Fatalf("only the served node should learn; got %+v", m)
	}

	// Fuera de la ventana de 15 min tampoco se aprende.
	RecordRead(s, "commit:served", now+int64(learnedWindow.Seconds())+120)
	m, err = s.MatchNodeTerms(terms, 10)
	if err != nil {
		t.Fatalf("MatchNodeTerms 2: %v", err)
	}
	if m[0].TermHits != int64(len(terms)) {
		t.Errorf("out-of-window read must not add hits; got %+v", m[0])
	}
}

// TestRecordRead_Dedup verifica el anti-inflación: un read repetido y una
// ráfaga de queries casi idénticas que sirvieron el mismo nodo atribuyen UNA
// sola vez (a la búsqueda más reciente), consumiendo el nodo de los logs.
func TestRecordRead_Dedup(t *testing.T) {
	s := newStore(t)
	if _, err := s.UpsertNodes([]db.Node{{ID: "commit:x", Kind: "commit", Title: "x"}}); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}
	now := time.Now().Unix()
	// Ráfaga de refinamientos: tres queries con el mismo término sirviendo el nodo.
	for i, q := range []string{"authwordx", "authwordx jwt", "authwordx decision"} {
		if err := s.LogSearch(string(rune('a'+i)), q, []string{"commit:x"}, now+int64(i)); err != nil {
			t.Fatalf("LogSearch: %v", err)
		}
	}

	RecordRead(s, "commit:x", now+60)
	RecordRead(s, "commit:x", now+120) // read repetido: ya consumido, no suma

	m, err := s.MatchNodeTerms(learnedTerms("authwordx"), 10)
	if err != nil {
		t.Fatalf("MatchNodeTerms: %v", err)
	}
	if len(m) != 1 || m[0].TermHits != 1 {
		t.Errorf("burst of refined queries + repeat read must attribute once; got %+v", m)
	}
}

// TestSearch_LearnedRespectsScope pinnea el invariante de privacidad dentro de
// Search: con un scope activo (Allowed no vacío) el canal aprendido se apaga,
// aunque el llamador pase Learned=true por error.
func TestSearch_LearnedRespectsScope(t *testing.T) {
	s := newStore(t)
	if _, err := s.UpsertNodes([]db.Node{
		{ID: "commit:x", Kind: "commit", ChatID: "c-fuera-de-scope", Title: "secreto", Summary: "sin match léxico", CreatedAt: 100},
	}); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}
	now := time.Now().Unix()
	if err := s.LogSearch("s1", "faktoria", []string{"commit:x"}, now); err != nil {
		t.Fatalf("LogSearch: %v", err)
	}
	RecordRead(s, "commit:x", now+30)

	res, err := Search(s, Params{
		Query: "faktoria", Top: 10, Mode: "hybrid",
		Learned: true, Allowed: []string{"otro-chat"}, // scope activo
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if containsID(res, "commit:x") {
		t.Errorf("learned channel must be off under an active scope; results=%+v", res)
	}
}

// TestSearch_LearnedChannel verifica el loop completo: un par query→read previo
// hace que el nodo aparezca en una búsqueda futura con términos parecidos
// (variante morfológica incluida), y que Learned=false lo apaga.
func TestSearch_LearnedChannel(t *testing.T) {
	s := newStore(t)
	// El summary del nodo NO contiene los términos de la query: solo la señal
	// aprendida puede traerlo.
	if _, err := s.UpsertNodes([]db.Node{
		{ID: "commit:x", Kind: "commit", ChatID: "c1", Title: "decisión JWT", Summary: "se eligió stateless con firma asimétrica", CreatedAt: 100},
	}); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}
	now := time.Now().Unix()
	if err := s.LogSearch("s1", "autenticación federada", []string{"commit:x"}, now); err != nil {
		t.Fatalf("LogSearch: %v", err)
	}
	RecordRead(s, "commit:x", now+30)

	// Variante morfológica de la query original: el stem la conecta.
	with, err := Search(s, Params{Query: "autenticar federar", Top: 10, Mode: "hybrid", Learned: true})
	if err != nil {
		t.Fatalf("Search learned: %v", err)
	}
	if !containsID(with, "commit:x") {
		t.Errorf("learned channel did not surface commit:x; results=%+v", with)
	}

	without, err := Search(s, Params{Query: "autenticar federar", Top: 10, Mode: "hybrid", Learned: false})
	if err != nil {
		t.Fatalf("Search no-learned: %v", err)
	}
	if containsID(without, "commit:x") {
		t.Errorf("commit:x should not appear without the learned channel; results=%+v", without)
	}
}

// TestSearch_LearnedIsBounded es el guard anti rich-get-richer: la señal
// aprendida entra por RRF (1/(k+rank)), así que un nodo con uso masivo aporta
// lo mismo que el tope de un canal — un match léxico fresco no es desplazado
// del podio por popularidad.
func TestSearch_LearnedIsBounded(t *testing.T) {
	s := newStore(t)
	if err := s.UpsertChat(&db.Chat{ID: "c1", Title: "test", Source: "claude"}); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	// Match léxico directo y fresco.
	if _, err := s.InsertMessages([]db.Message{
		{ID: "m1", ChatID: "c1", Role: "user", Content: "decidimos migrar el auth a passkeys", Seq: 1, Timestamp: time.Now().Unix()},
	}); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}
	// Nodo con uso históricamente masivo para el mismo término, summary sin match.
	if _, err := s.UpsertNodes([]db.Node{
		{ID: "commit:pop", Kind: "commit", ChatID: "c1", Title: "vieja decisión", Summary: "otra cosa", CreatedAt: 100},
	}); err != nil {
		t.Fatalf("UpsertNodes: %v", err)
	}
	now := time.Now().Unix()
	terms := learnedTerms("auth")
	for range 1000 {
		if err := s.RecordNodeTermHits(terms, "commit:pop", now); err != nil {
			t.Fatalf("RecordNodeTermHits: %v", err)
		}
	}

	res, err := Search(s, Params{Query: "auth", Top: 10, Mode: "hybrid", Learned: true, Expand: false})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) == 0 || res[0].ID != "m1" {
		t.Errorf("fresh lexical match must stay on top despite 1000 hits on commit:pop; results=%+v", res)
	}
}

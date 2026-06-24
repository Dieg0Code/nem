package retrieve

import (
	"sort"
	"strings"

	"github.com/Dieg0Code/nem/internal/db"
)

// Constantes del Pseudo-Relevance Feedback (RM3-lite).
const (
	prfTopK  = 8 // cuántos hits iniciales se usan como "documentos relevantes"
	prfTerms = 8 // cuántos términos de expansión se extraen de esos hits
)

// Params configura una búsqueda completa.
type Params struct {
	Query   string   // query en lenguaje natural
	Top     int      // máximo de resultados (default 10)
	Roles   []string // roles de mensaje a incluir (ya resueltos; nil = todos)
	Allowed []string // scope: chatIDs visibles (nil = sin filtro)
	Mode    string   // "hybrid" (default) | "keyword" | "semantic"
	Expand  bool     // activar PRF (expansión por relevancia)
}

// Search corre el pipeline completo de recuperación y devuelve los resultados ya
// fusionados (RRF + recencia): canales léxicos (BM25 sobre mensajes y nodos, con
// stem-prefix vía FTSQuery), vectores opcionales, y —si Expand— un canal extra de
// Pseudo-Relevance Feedback. Centraliza lo que antes estaba duplicado en el CLI y
// el MCP. El agente sigue siendo el reranker final que lee los hits.
func Search(store db.Store, p Params) ([]Scored, error) {
	if p.Mode == "" {
		p.Mode = "hybrid"
	}
	top := p.Top
	if top <= 0 {
		top = 10
	}
	fetch := top * 2
	if fetch < 10 {
		fetch = 10
	}

	base, err := baseChannels(store, FTSQuery(p.Query), p, fetch)
	if err != nil {
		return nil, err
	}

	channels := base
	// PRF solo en hybrid: keyword es exacto/predecible y semantic es vectores-only.
	if p.Expand && p.Mode == "hybrid" {
		channels = append(channels, prfChannels(store, p, base, fetch)...)
	}
	return Fuse(channels, top), nil
}

// baseChannels arma los canales según el modo:
//   - keyword:  léxico exacto (mensajes), sin nodos ni vectores
//   - hybrid:   mensajes + nodos + vectores (todo)
//   - semantic: solo vectores (embeddings)
func baseChannels(store db.Store, fts string, p Params, fetch int) ([]Channel, error) {
	var channels []Channel

	// Canales léxicos (BM25): en keyword y hybrid, NO en semantic.
	if p.Mode != "semantic" {
		msgHits, err := store.SearchMessages(fts, fetch, p.Roles, p.Allowed)
		if err != nil {
			return nil, err
		}
		channels = append(channels, Channel{Name: "messages", Items: messageItems(msgHits)})

		if p.Mode == "hybrid" {
			nodeHits, err := store.SearchNodes(fts, fetch, p.Allowed)
			if err != nil {
				return nil, err
			}
			channels = append(channels, Channel{Name: "nodes", Items: nodeItems(nodeHits)})
		}
	}

	// Capa semántica (embeddings): en hybrid y semantic, NO en keyword. nil si
	// está apagada o caída.
	if p.Mode != "keyword" {
		if vc, err := VectorChannel(store, p.Query, fetch, p.Allowed); err != nil {
			return nil, err
		} else if vc != nil {
			channels = append(channels, *vc)
		}
	}
	return channels, nil
}

// messageItems normaliza hits de mensaje a Items, prefiriendo el snippet (la
// ventana del match) sobre el contenido completo, para que el agente vea por qué
// matcheó sin abrir el mensaje.
func messageItems(hits []db.SearchHit) []Item {
	items := make([]Item, 0, len(hits))
	for _, h := range hits {
		content := h.Snippet
		if strings.TrimSpace(content) == "" {
			content = h.Content
		}
		items = append(items, Item{
			Kind: "message", ID: h.ID, ChatID: h.ChatID, Title: h.ChatTitle,
			Source: h.ChatSource, Role: h.Role, Content: content, Timestamp: h.Timestamp,
		})
	}
	return items
}

// nodeItems normaliza hits de nodo del índice a Items.
func nodeItems(hits []db.NodeHit) []Item {
	items := make([]Item, 0, len(hits))
	for _, h := range hits {
		items = append(items, Item{
			Kind: "node", ID: h.ID, ChatID: h.ChatID, Title: h.Title,
			NodeKind: h.Kind, Content: h.Summary, Timestamp: h.CreatedAt,
		})
	}
	return items
}

// prfChannels implementa el Pseudo-Relevance Feedback: fusiona los canales base,
// extrae los términos más distintivos de los primeros hits y re-busca con ellos.
// El resultado se agrega como canal(es) EXTRA al RRF — aditivo y acotado por
// 1/(k+rank): suma recall por co-ocurrencia sin poder dominar ni descartar los
// hits originales. Devuelve nil si no hay con qué expandir.
func prfChannels(store db.Store, p Params, base []Channel, fetch int) []Channel {
	seed := Fuse(base, prfTopK)
	if len(seed) == 0 {
		return nil
	}
	terms := prfExtract(p.Query, seed)
	if len(terms) == 0 {
		return nil
	}
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + t + `"`
	}
	fts := strings.Join(quoted, " OR ")

	var channels []Channel
	if msgHits, err := store.SearchMessages(fts, fetch, p.Roles, p.Allowed); err == nil {
		channels = append(channels, Channel{Name: "expanded-messages", Items: messageItems(msgHits)})
	}
	if nodeHits, err := store.SearchNodes(fts, fetch, p.Allowed); err == nil {
		channels = append(channels, Channel{Name: "expanded-nodes", Items: nodeItems(nodeHits)})
	}
	return channels
}

// prfExtract toma los términos más frecuentes (por co-ocurrencia) del contenido
// de los hits semilla, descartando stopwords, términos cortos y los que ya están
// en la query original (o son variantes morfológicas de ellos, vía stem-prefijo).
func prfExtract(query string, seed []Scored) []string {
	excludeExact := map[string]bool{}
	var queryStems []string
	for _, t := range tokenize(query) {
		excludeExact[t] = true
		queryStems = append(queryStems, stemPrefixes(t)...)
	}
	isVariant := func(t string) bool {
		for _, qs := range queryStems {
			if strings.HasPrefix(t, qs) {
				return true
			}
		}
		return false
	}

	freq := map[string]int{}
	for _, s := range seed {
		for _, t := range tokenize(s.Content) {
			if len([]rune(t)) < minStemLen || stopwords[t] || excludeExact[t] || isVariant(t) {
				continue
			}
			freq[t]++
		}
	}

	type tc struct {
		term string
		n    int
	}
	arr := make([]tc, 0, len(freq))
	for t, n := range freq {
		arr = append(arr, tc{t, n})
	}
	sort.Slice(arr, func(i, j int) bool {
		if arr[i].n != arr[j].n {
			return arr[i].n > arr[j].n
		}
		return arr[i].term < arr[j].term // estable
	})

	out := make([]string, 0, prfTerms)
	for _, x := range arr {
		if len(out) >= prfTerms {
			break
		}
		out = append(out, x.term)
	}
	return out
}

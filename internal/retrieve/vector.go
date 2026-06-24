package retrieve

import (
	"context"
	"slices"
	"sort"

	"github.com/Dieg0Code/nem/internal/db"
	"github.com/Dieg0Code/nem/internal/embed"
)

// VectorChannel arma el canal de embeddings (capa opcional). Devuelve nil si los
// embeddings están apagados o el backend no responde (la búsqueda sigue con
// BM25 + estructura). Hace cosine de la query contra los vectores de los nodos
// y devuelve los top como Items de nodo. allowed (scope) vacío = sin filtro.
func VectorChannel(store db.Store, query string, top int, allowed []string) (*Channel, error) {
	emb, err := embed.FromConfig()
	if err != nil || emb == nil {
		return nil, nil // embeddings apagados
	}
	qv, err := emb.Embed(context.Background(), []string{query})
	if err != nil || len(qv) == 0 || len(qv[0]) == 0 {
		return nil, nil // backend caído: no abortar la búsqueda
	}
	embs, err := store.AllEmbeddings()
	if err != nil {
		return nil, err
	}
	if len(embs) == 0 {
		return nil, nil
	}

	// Solo comparamos vectores de la MISMA dimensión que la query: si se cambió
	// el modelo de embeddings (p.ej. a uno mejor con otra dimensión) sin
	// re-indexar, los vectores viejos son incomparables. Hay que descartarlos,
	// no solo porque su cosine sería 0, sino porque el RRF rankea por POSICIÓN:
	// un vector obsoleto igual recibiría crédito. Recordar: `nem index --force`
	// tras cambiar de modelo.
	qdim := len(qv[0])
	type scored struct {
		id    string
		score float64
	}
	ranked := make([]scored, 0, len(embs))
	for _, e := range embs {
		if e.Dim != qdim {
			continue
		}
		ranked = append(ranked, scored{e.NodeID, embed.Cosine(qv[0], embed.Decode(e.Vec))})
	}
	if len(ranked) == 0 {
		return nil, nil // todos los vectores son de otro modelo: sin señal usable
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	items := make([]Item, 0, top)
	for _, r := range ranked {
		if len(items) >= top {
			break
		}
		n, _ := store.GetNode(r.id)
		if n == nil {
			continue
		}
		if len(allowed) > 0 && n.ChatID != "" && !slices.Contains(allowed, n.ChatID) {
			continue
		}
		items = append(items, Item{
			Kind: "node", ID: n.ID, ChatID: n.ChatID, Title: n.Title,
			NodeKind: n.Kind, Content: n.Summary, Timestamp: n.CreatedAt,
		})
	}
	if len(items) == 0 {
		return nil, nil
	}
	return &Channel{Name: "vectors", Items: items}, nil
}

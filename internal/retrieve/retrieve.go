// Package retrieve fusiona varios canales de búsqueda (BM25 sobre mensajes, BM25
// sobre nodos del índice y —opcional— vectores) en un único ranking mediante
// Reciprocal Rank Fusion (RRF) + un boost de recencia. El agente es el reranker
// final: rerankea leyendo los hits; acá solo se ordenan candidatos baratos.
package retrieve

import "sort"

// Item es un candidato de cualquier canal, normalizado para fusionar y mostrar.
type Item struct {
	Kind      string // "message" | "node"
	ID        string // id del mensaje o del nodo
	ChatID    string
	Title     string // título del chat/nodo (path corto)
	Source    string // codex | claude
	Role      string // rol (hits de mensaje)
	NodeKind  string // project|chat|commit (hits de nodo)
	Content   string // snippet o summary
	Timestamp int64
	// StoreID es el origen del item en lectura federada: "" = store personal,
	// si no el nombre del team store. Participa de itemKey, así un mismo ID local
	// en stores distintos (p.ej. tras promover un chat) no colapsa en uno.
	StoreID string
}

// Channel es una lista de items ya ordenada por relevancia (mejor primero).
type Channel struct {
	Name  string
	Items []Item
}

// Scored es un item con su score fusionado.
type Scored struct {
	Item
	Score float64
}

type config struct {
	k       float64 // constante RRF (amortigua el peso de los rangos altos)
	recency float64 // peso del boost de recencia (gentil; desempata)
}

// Option configura la fusión.
type Option func(*config)

// WithRRFK fija la constante k de RRF (default 60).
func WithRRFK(k float64) Option { return func(c *config) { c.k = k } }

// WithRecencyWeight fija el peso del boost de recencia (default 0.02).
func WithRecencyWeight(w float64) Option { return func(c *config) { c.recency = w } }

// itemKey identifica un item de forma única entre canales (mismo item en varios
// canales se refuerza, que es la gracia de RRF). Incluye StoreID para que el mismo
// ID local en stores distintos no se funda: en lectura no-federada todos los items
// comparten StoreID vacío, así el refuerzo intra-store se mantiene intacto.
func itemKey(it Item) string { return it.StoreID + "\x00" + it.Kind + "\x00" + it.ID }

// Fuse combina los canales por RRF + recencia y devuelve los top `limit`.
func Fuse(channels []Channel, limit int, options ...Option) []Scored {
	cfg := &config{k: 60, recency: 0.02}
	for _, o := range options {
		o(cfg)
	}

	type acc struct {
		item  Item
		score float64
	}
	merged := map[string]*acc{}

	// RRF: cada canal aporta 1/(k+rank) a cada item (rank 1-based).
	for _, ch := range channels {
		for rank, it := range ch.Items {
			key := itemKey(it)
			a := merged[key]
			if a == nil {
				a = &acc{item: it}
				merged[key] = a
			}
			a.score += 1.0 / (cfg.k + float64(rank+1))
		}
	}
	if len(merged) == 0 {
		return nil
	}

	// Boost de recencia: normaliza timestamps a [0,1] (más nuevo = 1) y suma un
	// término chico, para que sea desempate, no señal dominante.
	var minTS, maxTS int64
	first := true
	for _, a := range merged {
		if a.item.Timestamp == 0 {
			continue
		}
		if first || a.item.Timestamp < minTS {
			minTS = a.item.Timestamp
		}
		if first || a.item.Timestamp > maxTS {
			maxTS = a.item.Timestamp
		}
		first = false
	}
	span := float64(maxTS - minTS)

	out := make([]Scored, 0, len(merged))
	for _, a := range merged {
		score := a.score
		if span > 0 && a.item.Timestamp > 0 {
			norm := float64(a.item.Timestamp-minTS) / span
			score += cfg.recency * norm
		}
		out = append(out, Scored{Item: a.item, Score: score})
	}

	// Orden estable: por score desc, desempate por timestamp desc, luego id.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Timestamp != out[j].Timestamp {
			return out[i].Timestamp > out[j].Timestamp
		}
		return out[i].ID < out[j].ID
	})

	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

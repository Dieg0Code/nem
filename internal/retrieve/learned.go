// learned.go implementa la señal aprendida del ranking: pares query→read.
// Cuando un agente busca y luego lee un resultado servido, ese par es un juicio
// de relevancia (click-through). Se acumula por (término, nodo) en la DB y
// vuelve al ranking como un canal RRF EXTRA — aditivo y acotado por 1/(k+rank),
// igual que el PRF: puede reforzar, nunca dominar ni desplazar un match fresco.
package retrieve

import (
	"encoding/json"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/Dieg0Code/nem/internal/db"
)

const (
	// learnedWindow es cuánto puede pasar entre un search y el read que se le
	// atribuye. Correlación estricta: además el nodo debe haber sido SERVIDO.
	learnedWindow = 15 * time.Minute
	// learnedTop acota cuántos nodos aprendidos entran como canal.
	learnedTop = 10
)

// learnedTerms normaliza una query a los términos que indexan la señal: tokens
// sin stopwords, reducidos a su stem-prefijo cuando existe (mismo esquema que
// FTSQuery, así "búsqueda" y "buscar" acumulan en el mismo término).
func learnedTerms(query string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tokenize(query) {
		if stopwords[t] || len([]rune(t)) < 3 {
			continue
		}
		term := t
		if st := stemPrefixes(t); len(st) > 0 {
			term = st[0]
		}
		if !seen[term] {
			seen[term] = true
			out = append(out, term)
		}
	}
	return out
}

// usageDecay atenúa la señal según cuán viejo es el último hit: 1.0 reciente,
// decayendo linealmente hasta tocar el piso de 0.3 a los ~126 días. Mismo
// esquema que el peso de los facts: el uso viejo pesa menos, nunca cero.
func usageDecay(lastHit, now int64) float64 {
	if lastHit <= 0 {
		return 1.0
	}
	days := float64(now-lastHit) / 86400.0
	f := 1.0 - days/180.0
	return math.Max(0.3, math.Min(1.0, f))
}

// learnedChannel construye el canal de nodos históricamente leídos tras
// búsquedas como esta, ordenados por hits*decay. Se sobre-trae de la DB (que
// ordena por hits crudos) para que el decay decida la MEMBRESÍA del canal, no
// solo el orden: un nodo con uso fresco puede desplazar a uno atrincherado con
// más hits viejos. Devuelve nil si no hay señal. Best-effort: un fallo de DB
// apaga el canal, nunca rompe el search.
func learnedChannel(store db.Store, query string, now int64) *Channel {
	terms := learnedTerms(query)
	if len(terms) == 0 {
		return nil
	}
	matches, err := store.MatchNodeTerms(terms, learnedTop*3)
	if err != nil || len(matches) == 0 {
		return nil
	}
	type scored struct {
		m     db.NodeTermMatch
		score float64
	}
	arr := make([]scored, 0, len(matches))
	for _, m := range matches {
		arr = append(arr, scored{m, float64(m.TermHits) * usageDecay(m.TermLastHit, now)})
	}
	sort.SliceStable(arr, func(i, j int) bool { return arr[i].score > arr[j].score })
	if len(arr) > learnedTop {
		arr = arr[:learnedTop]
	}

	items := make([]Item, 0, len(arr))
	for _, s := range arr {
		n := s.m.Node
		items = append(items, Item{
			Kind: "node", ID: n.ID, ChatID: n.ChatID, Title: n.Title,
			NodeKind: n.Kind, Content: n.Summary, Timestamp: n.CreatedAt,
		})
	}
	return &Channel{Name: "learned", Items: items}
}

// ServedNodeIDs extrae de un resultado fusionado los node IDs del store
// personal que se le sirvieron al agente: nodos por su ID directo y mensajes
// por el nodo de su chat. Es el universo contra el que un read posterior puede
// correlacionar (correlación estricta: solo se aprende lo que fue servido).
func ServedNodeIDs(results []Scored) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, r := range results {
		if r.StoreID != "" { // solo el store personal aprende
			continue
		}
		switch r.Kind {
		case "node":
			add(r.ID)
		default: // message
			add("chat:" + r.ChatID)
		}
	}
	return out
}

// RecordRead registra un read como señal aprendida. Atribuye el read a la
// búsqueda MÁS RECIENTE de la ventana que sirvió este nodo (esa es la query que
// llevó al click), y luego consume el nodo de TODOS los logs que lo sirvieron:
// así un read repetido, o una ráfaga de queries casi idénticas, cuentan una vez
// y no inflan el par. Best-effort: nunca rompe el read.
func RecordRead(store db.Store, nodeID string, now int64) {
	if nodeID == "" {
		return
	}
	logs, err := store.RecentSearches(now - int64(learnedWindow.Seconds()))
	if err != nil {
		return
	}
	recorded := false
	for _, log := range logs { // RecentSearches viene más nueva primero
		var served []string
		if json.Unmarshal([]byte(log.ServedIDs), &served) != nil {
			continue
		}
		if !slices.Contains(served, nodeID) {
			continue
		}
		if !recorded {
			_ = store.RecordNodeTermHits(learnedTerms(log.Query), nodeID, now)
			recorded = true
		}
		_ = store.ConsumeServedID(log.ID, nodeID)
	}
}

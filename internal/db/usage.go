package db

import (
	"encoding/json"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// searchLogTTL es cuánto vive una entrada de SearchLog antes de prunearse.
// Solo sirve para correlacionar con reads inmediatos; 24h es holgado.
const searchLogTTL = int64(24 * 60 * 60)

// nodeTermTTL es cuánto vive un par (término, nodo) sin hits nuevos antes de
// prunearse: señal de hace más de un año ya no describe cómo buscas hoy, y sin
// TTL la tabla crecería para siempre (el decay tiene piso, nunca llega a cero).
const nodeTermTTL = int64(365 * 24 * 60 * 60)

// LogSearch registra una búsqueda servida (query + node IDs que aparecieron en
// los resultados) para correlacionarla con los reads que sigan. Prunea las
// entradas viejas en la misma llamada, así la tabla se mantiene chica sola.
func (s *store) LogSearch(id, query string, servedIDs []string, now int64) error {
	served, err := json.Marshal(servedIDs)
	if err != nil {
		return fmt.Errorf("failed to encode served ids: %w", err)
	}
	log := SearchLog{ID: id, Query: query, ServedIDs: string(served), CreatedAt: now}
	if err := s.gdb.Create(&log).Error; err != nil {
		return fmt.Errorf("failed to log search: %w", err)
	}
	// Prune best-effort: un fallo acá no invalida el log recién escrito.
	_ = s.gdb.Where("created_at < ?", now-searchLogTTL).Delete(&SearchLog{}).Error
	return nil
}

// RecentSearches devuelve las búsquedas logueadas desde `since` (inclusive),
// más nueva primero.
func (s *store) RecentSearches(since int64) ([]SearchLog, error) {
	var logs []SearchLog
	err := s.gdb.Where("created_at >= ?", since).Order("created_at DESC").Find(&logs).Error
	if err != nil {
		return nil, fmt.Errorf("failed to load recent searches: %w", err)
	}
	return logs, nil
}

// RecordNodeTermHits suma 1 al contador de cada par (término, nodo) y actualiza
// LastHit — el equivalente para nodos de RecordFactHit. Upsert: los pares nuevos
// nacen con Hits=1. Prunea de paso los pares sin uso hace más de un año, así la
// tabla se mantiene acotada sola (igual que LogSearch con su TTL).
func (s *store) RecordNodeTermHits(terms []string, nodeID string, now int64) error {
	if len(terms) == 0 || nodeID == "" {
		return nil
	}
	rows := make([]NodeTerm, 0, len(terms))
	for _, t := range terms {
		rows = append(rows, NodeTerm{Term: t, NodeID: nodeID, Hits: 1, LastHit: now})
	}
	err := s.gdb.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "term"}, {Name: "node_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"hits":     gorm.Expr("node_terms.hits + 1"),
			"last_hit": now,
		}),
	}).Create(&rows).Error
	if err != nil {
		return fmt.Errorf("failed to record node term hits: %w", err)
	}
	// Prune best-effort: un fallo acá no invalida los hits recién escritos.
	_ = s.gdb.Where("last_hit < ?", now-nodeTermTTL).Delete(&NodeTerm{}).Error
	return nil
}

// RevisionHealth devuelve las marcas para el canario anti-cámara-de-eco: la
// última vez que se revisó una decisión (el supersede más reciente entre los
// facts) y desde cuándo existe el store (el commit más antiguo). Ambos 0 si
// nunca / no hay. La interpretación (cuándo advertir) vive en internal/facts.
func (s *store) RevisionHealth() (lastRevisionAt, storeSince int64, err error) {
	// COALESCE: MAX/MIN sobre cero filas devuelve NULL, no 0.
	if err = s.gdb.Raw(
		`SELECT COALESCE(MAX(updated_at), 0) FROM facts WHERE superseded = 1`,
	).Scan(&lastRevisionAt).Error; err != nil {
		return 0, 0, fmt.Errorf("failed to read revision health: %w", err)
	}
	if err = s.gdb.Raw(
		`SELECT COALESCE(MIN(created_at), 0) FROM commits`,
	).Scan(&storeSince).Error; err != nil {
		return 0, 0, fmt.Errorf("failed to read store age: %w", err)
	}
	return lastRevisionAt, storeSince, nil
}

// ConsumeServedID saca un node ID del conjunto servido de un SearchLog: marca
// el par (búsqueda, nodo) como ya atribuido, para que reads repetidos o queries
// refinadas casi idénticas no inflen el contador (un log atribuye cada nodo UNA
// vez). No-op si el log ya no existe o no contenía el nodo.
func (s *store) ConsumeServedID(logID, nodeID string) error {
	var log SearchLog
	if err := s.gdb.First(&log, "id = ?", logID).Error; err != nil {
		return nil // pruneado o inexistente: nada que consumir
	}
	var served []string
	if err := json.Unmarshal([]byte(log.ServedIDs), &served); err != nil {
		return nil
	}
	kept := make([]string, 0, len(served))
	for _, id := range served {
		if id != nodeID {
			kept = append(kept, id)
		}
	}
	if len(kept) == len(served) {
		return nil
	}
	b, err := json.Marshal(kept)
	if err != nil {
		return fmt.Errorf("failed to encode served ids: %w", err)
	}
	if err := s.gdb.Model(&SearchLog{}).Where("id = ?", logID).
		Update("served_ids", string(b)).Error; err != nil {
		return fmt.Errorf("failed to consume served id: %w", err)
	}
	return nil
}

// NodeTermMatch es un nodo con su señal de uso agregada sobre los términos que
// matchearon: TermHits suma los hits y TermLastHit toma el más reciente.
type NodeTermMatch struct {
	Node
	TermHits    int64
	TermLastHit int64
}

// MatchNodeTerms devuelve los nodos históricamente leídos tras búsquedas con
// estos términos, ordenados por hits agregados. Excluye nodos superseded (una
// decisión muerta no debe subir por popularidad) y descarta señal huérfana de
// nodos que ya no existen en el árbol.
func (s *store) MatchNodeTerms(terms []string, limit int) ([]NodeTermMatch, error) {
	if len(terms) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	var matches []NodeTermMatch
	err := s.gdb.Raw(`
		SELECT n.*, SUM(t.hits) AS term_hits, MAX(t.last_hit) AS term_last_hit
		FROM node_terms t
		JOIN nodes n ON n.id = t.node_id
		WHERE t.term IN (?) AND n.superseded = ?
		GROUP BY t.node_id
		ORDER BY term_hits DESC, term_last_hit DESC
		LIMIT ?`, terms, false, limit).Scan(&matches).Error
	if err != nil {
		return nil, fmt.Errorf("failed to match node terms: %w", err)
	}
	return matches, nil
}

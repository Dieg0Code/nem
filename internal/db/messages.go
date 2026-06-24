package db

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// insertBatchSize controla el tamaño de lote en inserciones masivas (ingest).
const insertBatchSize = 500

// InsertMessages inserta mensajes en lotes, ignorando los que ya existen por id
// (idempotente: re-ingestar una sesión no duplica). Los triggers FTS5 mantienen
// el índice de búsqueda sincronizado automáticamente. Devuelve cuántos se
// insertaron realmente.
func (s *store) InsertMessages(msgs []Message) (int64, error) {
	if len(msgs) == 0 {
		return 0, nil
	}
	tx := s.gdb.Clauses(clause.OnConflict{DoNothing: true}).
		CreateInBatches(msgs, insertBatchSize)
	if tx.Error != nil {
		return 0, fmt.Errorf("failed to insert messages: %w", tx.Error)
	}
	return tx.RowsAffected, nil
}

// LastMessages devuelve los últimos n mensajes del chat ordenados por Seq
// ascendente (el orden cronológico de la conversación). Si roles no está vacío,
// filtra por rol ANTES de aplicar el límite ("los últimos n de esos roles").
func (s *store) LastMessages(chatID string, n int, roles []string) ([]Message, error) {
	if n <= 0 {
		return nil, nil
	}
	// Tomamos los n más altos por Seq y luego los devolvemos en orden ascendente.
	q := s.gdb.Where("chat_id = ?", chatID)
	if len(roles) > 0 {
		q = q.Where("role IN ?", roles)
	}
	var msgs []Message
	err := q.Order("seq DESC").Limit(n).Find(&msgs).Error
	if err != nil {
		return nil, fmt.Errorf("failed to load last messages for chat %s: %w", chatID, err)
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// MessageByID devuelve un mensaje por id dentro de un chat (nil, nil si no existe).
func (s *store) MessageByID(chatID, msgID string) (*Message, error) {
	var m Message
	err := s.gdb.First(&m, "chat_id = ? AND id = ?", chatID, msgID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get message %s: %w", msgID, err)
	}
	return &m, nil
}

// MessagesBySeqRange devuelve los mensajes del chat con Seq en [fromSeq, toSeq]
// (inclusive), en orden ascendente. Si roles no está vacío, filtra por rol.
func (s *store) MessagesBySeqRange(chatID string, fromSeq, toSeq int64, roles []string) ([]Message, error) {
	if fromSeq > toSeq {
		fromSeq, toSeq = toSeq, fromSeq
	}
	q := s.gdb.Where("chat_id = ? AND seq >= ? AND seq <= ?", chatID, fromSeq, toSeq)
	if len(roles) > 0 {
		q = q.Where("role IN ?", roles)
	}
	var msgs []Message
	err := q.Order("seq ASC").Find(&msgs).Error
	if err != nil {
		return nil, fmt.Errorf("failed to load message range for chat %s: %w", chatID, err)
	}
	return msgs, nil
}

// Stamp es el par (rol, timestamp) de un mensaje, para medir duraciones sin
// cargar el contenido completo.
type Stamp struct {
	Role      string
	Timestamp int64
}

// MessageStamps devuelve los (rol, timestamp) de los mensajes del chat con
// timestamp válido (>0), en orden de Seq (cronológico). Liviano: solo 2 columnas.
func (s *store) MessageStamps(chatID string) ([]Stamp, error) {
	var stamps []Stamp
	err := s.gdb.Model(&Message{}).
		Where("chat_id = ? AND timestamp > 0", chatID).
		Order("seq ASC").
		Find(&stamps).Error
	if err != nil {
		return nil, fmt.Errorf("failed to load message stamps for chat %s: %w", chatID, err)
	}
	return stamps, nil
}

// SearchMessages corre una búsqueda full-text (FTS5/BM25) sobre el contenido de
// los mensajes y devuelve los mejores hits con metadata del chat. El orden es
// por relevancia BM25 (menor score = más relevante).
func (s *store) SearchMessages(query string, limit int, roles, chatIDs []string) ([]SearchHit, error) {
	if limit <= 0 {
		limit = 10
	}
	// Los filtros se arman condicionalmente: vacío = sin filtro.
	args := []any{query}
	roleClause := ""
	if len(roles) > 0 {
		roleClause = "AND m.role IN (?)"
		args = append(args, roles)
	}
	chatClause := ""
	if len(chatIDs) > 0 {
		chatClause = "AND m.chat_id IN (?)"
		args = append(args, chatIDs)
	}
	args = append(args, limit)

	sql := fmt.Sprintf(`
		SELECT m.id, m.chat_id, m.role, m.content, m.timestamp, m.token_count, m.seq,
		       c.title AS chat_title, c.source AS chat_source,
		       snippet(messages_fts, 1, '', '', '…', 18) AS snippet,
		       bm25(messages_fts) AS score
		FROM messages_fts
		JOIN messages m ON m.id = messages_fts.message_id
		LEFT JOIN chats c ON c.id = m.chat_id
		WHERE messages_fts MATCH ? %s %s
		ORDER BY score
		LIMIT ?`, roleClause, chatClause)

	var hits []SearchHit
	if err := s.gdb.Raw(sql, args...).Scan(&hits).Error; err != nil {
		return nil, fmt.Errorf("failed to search messages: %w", err)
	}
	return hits, nil
}

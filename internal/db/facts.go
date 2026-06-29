package db

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// AddFact persiste una afirmación nueva (capa de memoria semántica). El llamador
// fija ID/CreatedAt/UpdatedAt.
func (s *store) AddFact(f *Fact) error {
	if err := s.gdb.Create(f).Error; err != nil {
		return fmt.Errorf("failed to add fact: %w", err)
	}
	return nil
}

// UpsertFact inserta f, o lo reemplaza si ya existe y el entrante es al menos tan
// nuevo (UpdatedAt >= el existente). Es el reconciliador de facts en sync entre
// máquinas: last-writer-wins por UpdatedAt (supersede/done/pinned viajan juntos).
func (s *store) UpsertFact(f *Fact) error {
	var existing Fact
	err := s.gdb.Where("id = ?", f.ID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := s.gdb.Create(f).Error; err != nil {
			return fmt.Errorf("failed to insert fact: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to look up fact: %w", err)
	}
	// Empate incluido: solo un entrante ESTRICTAMENTE más nuevo reemplaza. Así el
	// re-import del propio export es idempotente — clave porque el export REDACTA el
	// contenido: con <=, reimportar tu propio fact redactado NO pisa el original en
	// claro de tu DB local (mismo UpdatedAt). Un update real (supersede/done) sí
	// bumpea UpdatedAt y gana.
	if f.UpdatedAt <= existing.UpdatedAt {
		return nil // el existente es al menos tan nuevo: se conserva
	}
	if err := s.gdb.Save(f).Error; err != nil {
		return fmt.Errorf("failed to update fact: %w", err)
	}
	return nil
}

// ListFacts devuelve las afirmaciones de la más nueva a la más vieja. Por
// defecto solo las VIGENTES (ni reemplazadas ni completadas);
// includeInactive=true incluye las superseded y los reminders done (el rastro).
func (s *store) ListFacts(includeInactive bool) ([]Fact, error) {
	q := s.gdb.Model(&Fact{})
	if !includeInactive {
		// COALESCE para ser robustos ante filas viejas donde una columna agregada
		// por una migración posterior quedó NULL (NULL != false en SQL).
		q = q.Where("COALESCE(superseded, 0) = 0 AND COALESCE(done, 0) = 0")
	}
	var facts []Fact
	if err := q.Order("created_at DESC").Find(&facts).Error; err != nil {
		return nil, fmt.Errorf("failed to list facts: %w", err)
	}
	return facts, nil
}

// MarkFactDone marca un RECORDATORIO como completado (deja de aparecer en las
// vistas vigentes, pero se conserva). Solo afecta facts con fecha (due_at > 0):
// un hecho estable (note/schedule) no se "completa" — así un id confundido no
// puede ocultar memoria semántica durable. Error si no existe o no es reminder.
func (s *store) MarkFactDone(id string, doneAt int64) error {
	res := s.gdb.Model(&Fact{}).Where("id = ? AND due_at > 0", id).
		Updates(map[string]any{"done": true, "done_at": doneAt, "updated_at": doneAt})
	if res.Error != nil {
		return fmt.Errorf("failed to mark fact %s done: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("fact %s not found or is not a reminder (only reminders can be done)", id)
	}
	return nil
}

// GetFact devuelve una afirmación por id, o (nil, nil) si no existe.
func (s *store) GetFact(id string) (*Fact, error) {
	var f Fact
	err := s.gdb.First(&f, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get fact %s: %w", id, err)
	}
	return &f, nil
}

// SupersedeFact marca oldID como reemplazada por newID (append-only: no se pisa,
// se deja rastro). Error si oldID no existe.
func (s *store) SupersedeFact(oldID, newID string, updatedAt int64) error {
	res := s.gdb.Model(&Fact{}).Where("id = ?", oldID).
		Updates(map[string]any{"superseded": true, "superseded_by": newID, "updated_at": updatedAt})
	if res.Error != nil {
		return fmt.Errorf("failed to supersede fact %s: %w", oldID, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("fact %s not found", oldID)
	}
	return nil
}

// RecordFactHit suma 1 al contador de uso de cada fact y actualiza LastHit. Es
// la señal "aprendida" del peso: un fact que matchea un search o se resuelve por
// id sube. Best-effort: no devuelve error de "no encontrado" (los ids vienen de
// una pasada previa); un fallo de DB sí se propaga.
func (s *store) RecordFactHit(ids []string, now int64) error {
	if len(ids) == 0 {
		return nil
	}
	err := s.gdb.Model(&Fact{}).Where("id IN ?", ids).
		Updates(map[string]any{"hits": gorm.Expr("COALESCE(hits, 0) + 1"), "last_hit": now}).Error
	if err != nil {
		return fmt.Errorf("failed to record fact hit: %w", err)
	}
	return nil
}

// SetFactPinned fija/quita el pin manual de un fact (mitad explícita del peso
// híbrido): un fact pinned nunca se colapsa por presupuesto. Error si no existe.
func (s *store) SetFactPinned(id string, pinned bool, updatedAt int64) error {
	res := s.gdb.Model(&Fact{}).Where("id = ?", id).
		Updates(map[string]any{"pinned": pinned, "updated_at": updatedAt})
	if res.Error != nil {
		return fmt.Errorf("failed to set pinned on fact %s: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("fact %s not found", id)
	}
	return nil
}

// DeleteFact borra una afirmación de raíz (para corregir un error de tipeo, no
// un cambio de los hechos: para eso usar supersede). Error si no existe.
func (s *store) DeleteFact(id string) error {
	res := s.gdb.Where("id = ?", id).Delete(&Fact{})
	if res.Error != nil {
		return fmt.Errorf("failed to delete fact %s: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("fact %s not found", id)
	}
	return nil
}

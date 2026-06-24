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

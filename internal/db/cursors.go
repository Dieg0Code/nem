package db

import (
	"fmt"

	"gorm.io/gorm/clause"
)

// IngestCursors devuelve las huellas de las sesiones ya ingestadas de la fuente,
// indexadas por ruta de sesión.
func (s *store) IngestCursors(source string) (map[string]string, error) {
	var rows []IngestCursor
	if err := s.gdb.Where("source = ?", source).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list ingest cursors for %s: %w", source, err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Path] = r.Fingerprint
	}
	return out, nil
}

// SetIngestCursor registra la huella de una sesión ingestada, reemplazando la
// anterior si existía.
func (s *store) SetIngestCursor(source, path, fingerprint string) error {
	c := IngestCursor{Path: path, Source: source, Fingerprint: fingerprint}
	err := s.gdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "path"}},
		DoUpdates: clause.AssignmentColumns([]string{"source", "fingerprint"}),
	}).Create(&c).Error
	if err != nil {
		return fmt.Errorf("failed to set ingest cursor for %s: %w", path, err)
	}
	return nil
}

package db

import (
	"fmt"

	"gorm.io/gorm"
)

// ftsTables crea la tabla virtual FTS5 sobre el contenido de los mensajes y los
// triggers (ftsTriggers) que la mantienen sincronizada. Al ser standalone
// (no external-content) guardamos message_id sin indexar para mapear los
// resultados de búsqueda de vuelta a la tabla messages.
//
// Cada fila FTS lleva el mismo rowid que su fila base, así borrar o editar un
// mensaje es un lookup O(1) por rowid. Filtrar por message_id (UNINDEXED)
// recorría toda la tabla FTS por cada fila. El "AND message_id = old.id" es
// la salvaguarda: si un VACUUM renumeró los rowids, el trigger no toca el
// índice de otro mensaje.
//
// El tokenizer porter da stemming en inglés; unicode61 normaliza acentos para
// que "decay" y "Decáy" matcheen. BM25 viene incluido en FTS5.
const ftsTables = `
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    message_id UNINDEXED,
    content,
    tokenize = 'porter unicode61'
);

CREATE VIRTUAL TABLE IF NOT EXISTS nodes_fts USING fts5(
    node_id UNINDEXED,
    title,
    summary,
    tokenize = 'porter unicode61'
);

CREATE TABLE IF NOT EXISTS fts_realign_needed (flag INTEGER PRIMARY KEY);
`

// ftsTriggers mantiene messages_fts y nodes_fts sincronizadas con sus tablas.
// El insert libera antes su rowid en la tabla FTS: si los rowids se
// desalinearon, una fila FTS desplazada no hace fallar la inserción. Si ese
// rowid lo ocupaba otro id, deja una marca en fts_realign_needed para que la
// próxima apertura realinee (la fila desplazada ya no es de nadie).
const ftsTriggers = `
CREATE TRIGGER IF NOT EXISTS messages_fts_ai AFTER INSERT ON messages BEGIN
    INSERT OR IGNORE INTO fts_realign_needed(flag)
        SELECT 1 FROM messages_fts WHERE rowid = new.rowid AND message_id <> new.id;
    DELETE FROM messages_fts WHERE rowid = new.rowid;
    INSERT INTO messages_fts(rowid, message_id, content) VALUES (new.rowid, new.id, new.content);
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_ad AFTER DELETE ON messages BEGIN
    DELETE FROM messages_fts WHERE rowid = old.rowid AND message_id = old.id;
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_au AFTER UPDATE ON messages BEGIN
    UPDATE messages_fts SET content = new.content WHERE rowid = old.rowid AND message_id = old.id;
END;

CREATE TRIGGER IF NOT EXISTS nodes_fts_ai AFTER INSERT ON nodes BEGIN
    INSERT OR IGNORE INTO fts_realign_needed(flag)
        SELECT 1 FROM nodes_fts WHERE rowid = new.rowid AND node_id <> new.id;
    DELETE FROM nodes_fts WHERE rowid = new.rowid;
    INSERT INTO nodes_fts(rowid, node_id, title, summary) VALUES (new.rowid, new.id, new.title, new.summary);
END;

CREATE TRIGGER IF NOT EXISTS nodes_fts_ad AFTER DELETE ON nodes BEGIN
    DELETE FROM nodes_fts WHERE rowid = old.rowid AND node_id = old.id;
END;

CREATE TRIGGER IF NOT EXISTS nodes_fts_au AFTER UPDATE ON nodes BEGIN
    UPDATE nodes_fts SET title = new.title, summary = new.summary WHERE rowid = old.rowid AND node_id = old.id;
END;
`

// ftsRowidVersion marca (en PRAGMA user_version) los stores cuyos triggers FTS
// ya van por rowid. Los anteriores filtraban por id y tienen que recrearse.
const ftsRowidVersion = 1

// ftsDropTriggers quita los triggers de antes de ftsRowidVersion; ftsTriggers
// los recrea por rowid.
const ftsDropTriggers = `
DROP TRIGGER IF EXISTS messages_fts_ai;
DROP TRIGGER IF EXISTS messages_fts_ad;
DROP TRIGGER IF EXISTS messages_fts_au;
DROP TRIGGER IF EXISTS nodes_fts_ai;
DROP TRIGGER IF EXISTS nodes_fts_ad;
DROP TRIGGER IF EXISTS nodes_fts_au;
`

// ftsAligned es un chequeo O(1) de que los rowids FTS siguen a los de su tabla
// base. Un VACUUM o un dump/restore puede renumerar los rowids implícitos de
// messages y nodes (no tienen INTEGER PRIMARY KEY); al compactar huecos, el
// rowid máximo cambia y deja de coincidir con el de la tabla FTS. Solo una
// inserción puede volver a igualar los máximos, y en ese caso el trigger ya
// dejó la marca en fts_realign_needed.
const ftsAligned = `
SELECT NOT EXISTS (SELECT 1 FROM fts_realign_needed)
   AND (SELECT rowid FROM messages ORDER BY rowid DESC LIMIT 1)
    IS (SELECT rowid FROM messages_fts ORDER BY rowid DESC LIMIT 1)
   AND (SELECT rowid FROM nodes ORDER BY rowid DESC LIMIT 1)
    IS (SELECT rowid FROM nodes_fts ORDER BY rowid DESC LIMIT 1)
`

// ftsRealign repara solo las filas FTS cuyo rowid no coincide con el de su fila
// base. En la práctica casi todas coinciden (base y FTS se insertaron a la par),
// así que no hace falta reconstruir todo el índice.
const ftsRealign = `
DELETE FROM messages_fts WHERE rowid IN (
    SELECT f.rowid FROM messages_fts f
    LEFT JOIN messages m ON m.rowid = f.rowid AND m.id = f.message_id
    WHERE m.id IS NULL
);
INSERT INTO messages_fts(rowid, message_id, content)
    SELECT m.rowid, m.id, m.content FROM messages m
    WHERE NOT EXISTS (SELECT 1 FROM messages_fts f WHERE f.rowid = m.rowid);

DELETE FROM nodes_fts WHERE rowid IN (
    SELECT f.rowid FROM nodes_fts f
    LEFT JOIN nodes n ON n.rowid = f.rowid AND n.id = f.node_id
    WHERE n.id IS NULL
);
INSERT INTO nodes_fts(rowid, node_id, title, summary)
    SELECT n.rowid, n.id, n.title, n.summary FROM nodes n
    WHERE NOT EXISTS (SELECT 1 FROM nodes_fts f WHERE f.rowid = n.rowid);

DELETE FROM fts_realign_needed;
`

// migrate corre AutoMigrate sobre los modelos relacionales y luego crea la capa
// FTS5 en SQL crudo. Es idempotente: seguro de llamar en cada apertura.
func migrate(gdb *gorm.DB) error {
	if err := gdb.AutoMigrate(models()...); err != nil {
		return fmt.Errorf("failed to auto-migrate models: %w", err)
	}
	return gdb.Transaction(func(tx *gorm.DB) error {
		var version int
		if err := tx.Raw("PRAGMA user_version").Scan(&version).Error; err != nil {
			return fmt.Errorf("failed to read schema version: %w", err)
		}
		if err := tx.Exec(ftsTables).Error; err != nil {
			return fmt.Errorf("failed to create FTS5 tables: %w", err)
		}
		if version < ftsRowidVersion {
			if err := tx.Exec(ftsDropTriggers).Error; err != nil {
				return fmt.Errorf("failed to drop legacy FTS5 triggers: %w", err)
			}
		}
		var aligned bool
		if err := tx.Raw(ftsAligned).Scan(&aligned).Error; err != nil {
			return fmt.Errorf("failed to check FTS5 alignment: %w", err)
		}
		if version < ftsRowidVersion || !aligned {
			if err := tx.Exec(ftsRealign).Error; err != nil {
				return fmt.Errorf("failed to realign FTS5 rows: %w", err)
			}
		}
		if err := tx.Exec(ftsTriggers).Error; err != nil {
			return fmt.Errorf("failed to create FTS5 triggers: %w", err)
		}
		if version < ftsRowidVersion {
			if err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", ftsRowidVersion)).Error; err != nil {
				return fmt.Errorf("failed to set schema version: %w", err)
			}
		}
		return nil
	})
}

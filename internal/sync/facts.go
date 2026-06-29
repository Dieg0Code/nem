package sync

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dieg0Code/nem/internal/db"
)

// factLine es la representación JSONL de un fact en el store versionable. Lleva
// los campos durables (incluido el rastro superseded/done) para que el estado de
// la memoria semántica se reconstruya igual en cualquier máquina.
type factLine struct {
	Type         string `json:"type"` // "fact"
	ID           string `json:"id"`
	Content      string `json:"content"`
	Kind         string `json:"kind,omitempty"`
	Source       string `json:"source,omitempty"`
	Author       string `json:"author,omitempty"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
	Superseded   bool   `json:"superseded,omitempty"`
	SupersededBy string `json:"superseded_by,omitempty"`
	DueAt        int64  `json:"due_at,omitempty"`
	Done         bool   `json:"done,omitempty"`
	DoneAt       int64  `json:"done_at,omitempty"`
	Stability    string `json:"stability,omitempty"`
	Pinned       bool   `json:"pinned,omitempty"`
	HasAnchor    bool   `json:"has_anchor,omitempty"`
	AnchorAt     int64  `json:"anchor_at,omitempty"`
}

// factsDir es el directorio versionable de facts dentro del store. Igual que los
// commits (un archivo por commit), cada fact vive en su propio archivo
// <id>.jsonl: así dos clones que agregan facts distintos no chocan al rebasar —
// son archivos separados que git fusiona sin conflicto.
func (s *syncer) factsDir() string {
	return filepath.Join(s.cfg.dir, "store", "facts")
}

// exportFacts escribe un archivo por fact (incluido el rastro) en store/facts/,
// redactando el contenido, y poda los archivos de facts borrados. Devuelve cuántos
// exportó.
func (s *syncer) exportFacts(counts map[string]int) (int, error) {
	facts, err := s.store.ListFacts(true)
	if err != nil {
		return 0, err
	}
	dir := s.factsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("failed to create facts dir: %w", err)
	}

	want := make(map[string]bool, len(facts))
	for _, fact := range facts {
		// Defensa en profundidad: un id inseguro nunca debe convertirse en una ruta
		// fuera de store/facts. Los facts locales son UUIDs; esto blinda contra un
		// id corrupto que se haya colado a la DB.
		if !validFactID(fact.ID) {
			continue
		}
		res := s.cfg.redactor.Redact(fact.Content)
		for k, v := range res.Counts {
			counts[k] += v
		}
		name := factFileName(fact.ID)
		want[name] = true
		if err := writeFactFile(filepath.Join(dir, name), factLine{
			Type: "fact", ID: fact.ID, Content: res.Text, Kind: fact.Kind,
			Source: fact.Source, Author: fact.Author,
			CreatedAt: fact.CreatedAt, UpdatedAt: fact.UpdatedAt,
			Superseded: fact.Superseded, SupersededBy: fact.SupersededBy,
			DueAt: fact.DueAt, Done: fact.Done, DoneAt: fact.DoneAt,
			Stability: fact.Stability, Pinned: fact.Pinned,
			HasAnchor: fact.HasAnchor, AnchorAt: fact.AnchorAt,
		}); err != nil {
			return 0, err
		}
	}

	// Poda los archivos de facts que ya no existen localmente (un fact borrado de
	// raíz), para que un re-import no lo resucite en el MISMO store.
	//
	// Límite consciente de v1: el borrado duro (nem fact rm) no se propaga de forma
	// confiable ENTRE clones. Sin tombstones no se distingue "borrado en otro clon"
	// de "nuevo acá", así que otro clon que aún tenga el fact lo re-exportará. La
	// forma soportada de retirar un fact COMPARTIDO es supersede/done, que viajan
	// como updates (UpdatedAt mayor) y sí ganan en todos los clones.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("failed to read facts dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		if !want[e.Name()] {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return 0, fmt.Errorf("failed to prune %s: %w", e.Name(), err)
			}
		}
	}
	return len(facts), nil
}

// factFileName deriva el nombre de archivo de un fact desde su id.
func factFileName(id string) string { return id + ".jsonl" }

// validFactID acepta solo ids seguros como segmento de ruta: no vacío, sin
// separadores ni "..", y compuesto de caracteres portables (los UUID que genera
// nem pasan). Bloquea path-traversal vía un id remoto malicioso o corrupto.
func validFactID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// writeFactFile escribe (atómicamente vía truncado) la línea JSONL de un fact.
func writeFactFile(path string, line factLine) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", path, err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	if err := writeJSONL(w, line); err != nil {
		return err
	}
	return w.Flush()
}

// importFacts lee cada archivo de fact de store/facts/ y hace upsert (LWW por
// UpdatedAt). Devuelve cuántos procesó. Ausencia del directorio no es error.
func (s *syncer) importFacts() (int, error) {
	dir := s.factsDir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("failed to read facts dir: %w", err)
	}

	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		count, err := s.importFactFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return n, err
		}
		n += count
	}
	return n, nil
}

// importFactFile hace upsert de cada línea de fact de un archivo.
func (s *syncer) importFactFile(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("failed to open %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	n := 0
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var fl factLine
		if err := json.Unmarshal(line, &fl); err != nil {
			continue
		}
		// Frontera de entrada de contenido remoto: descartar ids inseguros para que
		// nunca lleguen a la DB ni a una ruta de export posterior.
		if !validFactID(fl.ID) {
			continue
		}
		if err := s.store.UpsertFact(&db.Fact{
			ID: fl.ID, Content: fl.Content, Kind: fl.Kind,
			Source: fl.Source, Author: fl.Author,
			CreatedAt: fl.CreatedAt, UpdatedAt: fl.UpdatedAt,
			Superseded: fl.Superseded, SupersededBy: fl.SupersededBy,
			DueAt: fl.DueAt, Done: fl.Done, DoneAt: fl.DoneAt,
			Stability: fl.Stability, Pinned: fl.Pinned,
			HasAnchor: fl.HasAnchor, AnchorAt: fl.AnchorAt,
		}); err != nil {
			return n, err
		}
		n++
	}
	return n, sc.Err()
}

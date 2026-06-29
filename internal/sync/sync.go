// Package sync exporta los commits de nem a JSONL versionable por git, los
// sincroniza con un remoto, y reimporta lo que llega. El scrubbing de secretos
// corre SIEMPRE en la exportación: es la única frontera por la que el contenido
// sale de la máquina.
package sync

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Dieg0Code/nem/internal/db"
	"github.com/Dieg0Code/nem/internal/output"
	"github.com/Dieg0Code/nem/internal/redact"
)

// gitignoreContent excluye todo lo que no debe versionarse: la DB binaria y la
// config local. Solo store/ se sincroniza.
const gitignoreContent = "/nem.db\n/nem.db-*\n/config.toml\n"

// exportHeader es la primera línea de cada archivo de commit exportado.
type exportHeader struct {
	Type       string `json:"type"` // "commit"
	Hash       string `json:"hash"`
	ChatID     string `json:"chat_id"`
	ChatTitle  string `json:"chat_title"`
	ChatSource string `json:"chat_source"`
	Branch     string `json:"branch"`
	Message    string `json:"message"`
	Author     string `json:"author,omitempty"`
	CreatedAt  int64  `json:"created_at"`
}

// exportMsg es cada línea de mensaje (ya redactada) de un commit exportado.
type exportMsg struct {
	Type      string `json:"type"` // "msg"
	Role      string `json:"role"`
	Content   string `json:"content"`
	Timestamp int64  `json:"timestamp"`
	Seq       int64  `json:"seq"`
}

// Report resume una corrida de sync.
type Report struct {
	Exported      int
	Imported      int
	FactsExported int
	FactsImported int
	Redacted      map[string]int
	Pushed        bool
}

// Syncer orquesta export → git → import.
type Syncer interface {
	// Sync ejecuta el ciclo completo export → git → import.
	Sync() (*Report, error)
	// Import solo reimporta los archivos de commit del store a la DB (para clone).
	Import() (int, error)
}

type config struct {
	dir      string
	remote   string
	branch   string
	redactor redact.Redactor
}

// Option configura el Syncer.
type Option func(*config) error

// WithDir fija el directorio del store (default ~/.nem, vía paths del caller).
func WithDir(dir string) Option {
	return func(c *config) error {
		if dir == "" {
			return errors.New("dir cannot be empty")
		}
		c.dir = dir
		return nil
	}
}

// WithRemote fija el nombre del remote (default "origin").
func WithRemote(name string) Option {
	return func(c *config) error {
		c.remote = name
		return nil
	}
}

// WithRedactor inyecta un redactor propio (default redact.New()).
func WithRedactor(r redact.Redactor) Option {
	return func(c *config) error {
		if r == nil {
			return errors.New("redactor cannot be nil")
		}
		c.redactor = r
		return nil
	}
}

type syncer struct {
	store    db.Store
	cfg      *config
	git      gitRepo
	chatsDir string
}

// NewSyncer crea un Syncer. dir es obligatorio (raíz del store, p.ej. ~/.nem).
func NewSyncer(store db.Store, options ...Option) (Syncer, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	cfg := &config{remote: "origin"} // branch vacío = detectar el real del repo
	for _, option := range options {
		if err := option(cfg); err != nil {
			return nil, fmt.Errorf("failed to apply sync option: %w", err)
		}
	}
	if cfg.dir == "" {
		return nil, errors.New("dir is required (use WithDir)")
	}
	if cfg.redactor == nil {
		r, err := redact.New()
		if err != nil {
			return nil, err
		}
		cfg.redactor = r
	}
	return &syncer{
		store:    store,
		cfg:      cfg,
		git:      gitRepo{dir: cfg.dir},
		chatsDir: filepath.Join(cfg.dir, "store", "chats"),
	}, nil
}

// Sync ejecuta el ciclo completo: exporta los commits (redactados), los
// commitea en git, sincroniza con el remoto si existe, y reimporta lo nuevo.
func (s *syncer) Sync() (*Report, error) {
	if err := EnsureRepo(s.cfg.dir); err != nil {
		return nil, err
	}

	exported, counts, err := s.export()
	if err != nil {
		return nil, err
	}
	report := &Report{Exported: exported, Redacted: counts}

	// Los facts (memoria semántica) también viajan: es lo más valioso de compartir
	// en un store de equipo. Se redactan con el mismo contador (frontera de egress).
	factsExported, err := s.exportFacts(counts)
	if err != nil {
		return nil, err
	}
	report.FactsExported = factsExported

	// add -A versiona store/ y .gitignore; nem.db queda excluido por el ignore.
	if err := s.git.addAll(".gitignore", "store"); err != nil {
		return nil, err
	}
	if _, err := s.git.commit(fmt.Sprintf("nem sync %s", time.Now().Format(time.RFC3339))); err != nil {
		return nil, err
	}

	if s.git.hasRemote(s.cfg.remote) {
		branch := s.cfg.branch
		if branch == "" {
			branch = s.git.currentBranch() // usa el branch real del repo
		}
		if err := s.git.pullRebase(s.cfg.remote, branch); err != nil {
			return nil, err
		}
		if err := s.git.push(s.cfg.remote, branch); err != nil {
			return nil, err
		}
		report.Pushed = true
	}

	imported, err := s.importAll()
	if err != nil {
		return nil, err
	}
	report.Imported = imported

	factsImported, err := s.importFacts()
	if err != nil {
		return nil, err
	}
	report.FactsImported = factsImported
	return report, nil
}

// Import reimporta los archivos de commit y de facts del store a la DB (para
// clone / team add). Devuelve cuántos commits se importaron.
func (s *syncer) Import() (int, error) {
	imported, err := s.importAll()
	if err != nil {
		return 0, err
	}
	if _, err := s.importFacts(); err != nil {
		return imported, err
	}
	return imported, nil
}

// export escribe un archivo JSONL por commit, redactando el contenido. Devuelve
// cuántos commits exportó y el conteo de secretos redactados.
func (s *syncer) export() (int, map[string]int, error) {
	commits, err := s.store.ListAllCommits()
	if err != nil {
		return 0, nil, err
	}
	if err := os.MkdirAll(s.chatsDir, 0o755); err != nil {
		return 0, nil, fmt.Errorf("failed to create chats dir: %w", err)
	}

	counts := map[string]int{}
	for _, c := range commits {
		chat, err := s.store.GetChat(c.ChatID)
		if err != nil {
			return 0, nil, err
		}
		title, source := "", ""
		if chat != nil {
			title, source = chat.Title, chat.Source
		}
		snap, err := output.ParseSnapshot(c.Snapshot)
		if err != nil {
			return 0, nil, err
		}
		if err := s.writeCommitFile(c, title, source, snap, counts); err != nil {
			return 0, nil, err
		}
	}
	return len(commits), counts, nil
}

func (s *syncer) writeCommitFile(c db.Commit, title, source string, snap []output.SnapMessage, counts map[string]int) error {
	path := filepath.Join(s.chatsDir, c.Hash+".jsonl")
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", path, err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)

	header := exportHeader{
		Type: "commit", Hash: c.Hash, ChatID: c.ChatID,
		ChatTitle: title, ChatSource: source, Branch: c.Branch,
		Message: c.Message, Author: c.Author, CreatedAt: c.CreatedAt,
	}
	if err := writeJSONL(w, header); err != nil {
		return err
	}
	for _, m := range snap {
		res := s.cfg.redactor.Redact(m.Content)
		for k, v := range res.Counts {
			counts[k] += v
		}
		if err := writeJSONL(w, exportMsg{
			Type: "msg", Role: m.Role, Content: res.Text,
			Timestamp: m.Timestamp, Seq: m.Seq,
		}); err != nil {
			return err
		}
	}
	return w.Flush()
}

// importAll lee los archivos de commit del store y crea en la DB los que falten.
func (s *syncer) importAll() (int, error) {
	files, err := filepath.Glob(filepath.Join(s.chatsDir, "*.jsonl"))
	if err != nil {
		return 0, fmt.Errorf("failed to list commit files: %w", err)
	}
	imported := 0
	for _, path := range files {
		ok, err := s.importFile(path)
		if err != nil {
			return imported, err
		}
		if ok {
			imported++
		}
	}
	return imported, nil
}

func (s *syncer) importFile(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("failed to open %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var header exportHeader
	var msgs []db.Message
	first := true
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		if first {
			if err := json.Unmarshal(line, &header); err != nil {
				return false, fmt.Errorf("bad header in %s: %w", path, err)
			}
			first = false
			continue
		}
		var m exportMsg
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		msgs = append(msgs, db.Message{
			ID:        fmt.Sprintf("%s:%d", header.Hash, m.Seq),
			ChatID:    header.ChatID,
			Role:      m.Role,
			Content:   m.Content,
			Timestamp: m.Timestamp,
			Seq:       m.Seq,
		})
	}
	if err := sc.Err(); err != nil {
		return false, fmt.Errorf("failed to read %s: %w", path, err)
	}
	if header.Hash == "" {
		return false, nil
	}

	existing, err := s.store.GetCommit(header.Hash)
	if err != nil {
		return false, err
	}
	if existing != nil {
		return false, nil // ya importado
	}

	if err := s.store.UpsertChat(&db.Chat{
		ID: header.ChatID, Title: header.ChatTitle,
		Source: header.ChatSource, CreatedAt: header.CreatedAt,
	}); err != nil {
		return false, err
	}
	if _, err := s.store.InsertMessages(msgs); err != nil {
		return false, err
	}

	snapshot, err := output.BuildSnapshot(msgs)
	if err != nil {
		return false, err
	}
	commit := &db.Commit{
		Hash: header.Hash, ChatID: header.ChatID, Branch: header.Branch,
		Message: header.Message, Author: header.Author, Snapshot: snapshot, CreatedAt: header.CreatedAt,
	}
	if len(msgs) > 0 {
		commit.MsgFrom = msgs[0].ID
		commit.MsgTo = msgs[len(msgs)-1].ID
	}
	if err := s.store.CreateCommit(commit); err != nil {
		return false, err
	}
	return true, nil
}

func writeJSONL(w *bufio.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("failed to marshal jsonl: %w", err)
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	return w.WriteByte('\n')
}

// EnsureRepo inicializa el repo git en dir (si no lo es) y escribe el .gitignore
// que excluye la DB binaria y la config local.
func EnsureRepo(dir string) error {
	g := gitRepo{dir: dir}
	if err := g.initRepo(); err != nil {
		return err
	}
	gi := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(gi); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(gi, []byte(gitignoreContent), 0o644); err != nil {
			return fmt.Errorf("failed to write .gitignore: %w", err)
		}
	}
	return nil
}

// RemoteAdd configura el remote del store.
func RemoteAdd(dir, name, url string) error {
	if err := EnsureRepo(dir); err != nil {
		return err
	}
	return gitRepo{dir: dir}.remoteAdd(name, url)
}

// RemoteList devuelve los remotes configurados (`git remote -v`).
func RemoteList(dir string) (string, error) {
	return gitRepo{dir: dir}.remotesVerbose()
}

// Clone clona url en dir (que no debe existir o estar vacío).
func Clone(url, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return fmt.Errorf("%s is already a git repo", dir)
	}
	g := gitRepo{dir: filepath.Dir(dir)}
	if _, err := g.run("clone", url, dir); err != nil {
		return err
	}
	return nil
}

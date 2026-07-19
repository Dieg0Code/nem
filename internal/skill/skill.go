// Package skill instala el "agent skill" de nem: un SKILL.md que le enseña al
// agente (Claude Code, Codex, Antigravity, opencode) cuándo y cómo usar nem,
// cerrando el loop de que el agente persista su propio contexto. nem es dueño
// del subdirectorio "nem" dentro de skills/ y lo regenera de forma idempotente;
// nunca toca otros skills.
package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// skillName es el nombre del subdirectorio (y del skill) que nem administra.
// nem SOLO escribe dentro de este subdir; jamás toca otros skills del usuario.
const skillName = "nem"

// Agent identifica a un agente soportado por la instalación del skill.
type Agent struct {
	// Name es el identificador del agente: "claude" | "codex" | "antigravity" |
	// "opencode".
	Name string
	// Root es el home del agente (~/.claude, ~/.codex,
	// ~/.gemini/antigravity-cli o ~/.config/opencode).
	Root string
}

// Installed describe un skill efectivamente escrito para un agente.
type Installed struct {
	Agent string // nombre del agente ("claude", "codex", ...)
	Path  string // ruta absoluta del SKILL.md escrito
}

// Report resume una corrida de Install: qué agentes recibieron el skill y
// cuáles se saltaron por no estar presentes en el equipo.
type Report struct {
	Installed []Installed // agentes a los que se escribió el SKILL.md
	Skipped   []string    // agentes ausentes (no existe su home dir)
}

// Installer escribe el skill de nem en los agentes presentes.
type Installer interface {
	// Install escribe SKILL.md en cada agente detectado y devuelve el reporte.
	Install() (*Report, error)
}

type config struct {
	agents  []Agent
	content string
}

// Option configura al Installer.
type Option func(*config) error

// WithClaudeRoot fija el home de Claude Code (default ~/.claude). Útil para
// tests (apuntar a t.TempDir()).
func WithClaudeRoot(root string) Option {
	return func(c *config) error {
		if root == "" {
			return errors.New("claude root cannot be empty")
		}
		c.agents = append(c.agents, Agent{Name: "claude", Root: root})
		return nil
	}
}

// WithCodexRoot fija el home de Codex (default ~/.codex). Útil para tests.
func WithCodexRoot(root string) Option {
	return func(c *config) error {
		if root == "" {
			return errors.New("codex root cannot be empty")
		}
		c.agents = append(c.agents, Agent{Name: "codex", Root: root})
		return nil
	}
}

// WithAntigravityRoot fija el home del CLI de Antigravity
// (default ~/.gemini/antigravity-cli). Útil para tests.
func WithAntigravityRoot(root string) Option {
	return func(c *config) error {
		if root == "" {
			return errors.New("antigravity root cannot be empty")
		}
		c.agents = append(c.agents, Agent{Name: "antigravity", Root: root})
		return nil
	}
}

// WithOpencodeRoot fija el config dir de opencode (default ~/.config/opencode,
// donde opencode busca skills/). Útil para tests.
func WithOpencodeRoot(root string) Option {
	return func(c *config) error {
		if root == "" {
			return errors.New("opencode root cannot be empty")
		}
		c.agents = append(c.agents, Agent{Name: "opencode", Root: root})
		return nil
	}
}

// WithContent reemplaza el contenido del skill (default: el SKILL.md embebido).
// Pensado para tests; en producción se usa el template compilado.
func WithContent(content string) Option {
	return func(c *config) error {
		if content == "" {
			return errors.New("content cannot be empty")
		}
		c.content = content
		return nil
	}
}

// New crea un Installer. Sin opciones With*Root usa los homes por defecto
// (~/.claude, ~/.codex, ~/.gemini/antigravity-cli, ~/.config/opencode)
// derivados de os.UserHomeDir().
func New(options ...Option) (Installer, error) {
	cfg := &config{content: skillTemplate}
	for _, option := range options {
		if err := option(cfg); err != nil {
			return nil, fmt.Errorf("failed to apply skill option: %w", err)
		}
	}
	if len(cfg.agents) == 0 {
		defaults, err := defaultAgents()
		if err != nil {
			return nil, err
		}
		cfg.agents = defaults
	}
	if cfg.content == "" {
		return nil, errors.New("skill content is empty (embedded template missing?)")
	}
	return &installer{cfg: cfg}, nil
}

type installer struct {
	cfg *config
}

// defaultAgents arma la lista de agentes por defecto (~/.claude, ~/.codex,
// ~/.gemini/antigravity-cli, ~/.config/opencode), consistente con las raíces
// por defecto de las fuentes de ingest.
func defaultAgents() ([]Agent, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve home directory: %w", err)
	}
	return []Agent{
		{Name: "claude", Root: filepath.Join(home, ".claude")},
		{Name: "codex", Root: filepath.Join(home, ".codex")},
		{Name: "antigravity", Root: filepath.Join(home, ".gemini", "antigravity-cli")},
		{Name: "opencode", Root: opencodeConfigDir(home)},
	}, nil
}

// opencodeConfigDir resuelve el config dir de opencode, honrando
// XDG_CONFIG_HOME (opencode usa rutas XDG incluso en Windows).
func opencodeConfigDir(home string) string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "opencode")
	}
	return filepath.Join(home, ".config", "opencode")
}

// Install escribe el SKILL.md en cada agente cuyo home exista. Los agentes
// ausentes se saltan (no es error: el usuario puede no tenerlos instalados).
// La escritura es idempotente: sobrescribe siempre el skill "nem".
func (i *installer) Install() (*Report, error) {
	report := &Report{}
	for _, a := range i.cfg.agents {
		present, err := dirExists(a.Root)
		if err != nil {
			return nil, err
		}
		if !present {
			report.Skipped = append(report.Skipped, a.Name)
			continue
		}
		path, err := i.writeSkill(a)
		if err != nil {
			return nil, fmt.Errorf("failed to install skill for %s: %w", a.Name, err)
		}
		report.Installed = append(report.Installed, Installed{Agent: a.Name, Path: path})
	}
	return report, nil
}

// writeSkill crea <root>/skills/nem/ y escribe SKILL.md ahí. Solo toca el subdir
// "nem"; nunca otros skills del usuario.
func (i *installer) writeSkill(a Agent) (string, error) {
	dir := filepath.Join(a.Root, "skills", skillName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create skill dir: %w", err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(i.cfg.content), 0o644); err != nil {
		return "", fmt.Errorf("failed to write SKILL.md: %w", err)
	}
	return path, nil
}

// dirExists indica si path existe y es un directorio. Un path inexistente NO es
// error (el agente simplemente no está instalado).
func dirExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to stat %s: %w", path, err)
	}
	return info.IsDir(), nil
}

package config

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Scope define un alcance de acceso de lectura (ver internal/scope).
type Scope struct {
	Titles  []string `toml:"titles,omitempty"`
	Sources []string `toml:"sources,omitempty"`
	Chats   []string `toml:"chats,omitempty"`
}

// Backend configura un backend pluggable (summarize/embed): "heuristic" |
// "ollama" | "api" | "static", con modelo y endpoint opcionales.
type Backend struct {
	Backend  string `toml:"backend,omitempty"`
	Model    string `toml:"model,omitempty"`
	Endpoint string `toml:"endpoint,omitempty"`
}

// FactsConfig configura la capa semántica siempre-cargada.
type FactsConfig struct {
	// Budget es el tope de facts estables siempre-cargados (0 → default).
	Budget int `toml:"budget,omitempty"`
}

// Team es un store de equipo registrado. Guarda solo el remote git: la ruta local
// se deriva siempre del nombre (~/.nem/teams/<name>, vía config.TeamDir), que es
// más robusto que una ruta absoluta persistida ante cambios de NEM_HOME/home.
type Team struct {
	URL string `toml:"url,omitempty"`
}

// UserConfig identifica a quién atribuir commits y facts en stores compartidos.
type UserConfig struct {
	Name string `toml:"name,omitempty"`
}

// File es el contenido completo de ~/.nem/config.toml.
type File struct {
	Scopes    map[string]Scope `toml:"scopes,omitempty"`
	Summarize Backend          `toml:"summarize,omitempty"`
	Embed     Backend          `toml:"embed,omitempty"`
	Facts     FactsConfig      `toml:"facts,omitempty"`
	Teams     map[string]Team  `toml:"teams,omitempty"`
	User      UserConfig       `toml:"user,omitempty"`
}

// FactsBudget devuelve el presupuesto configurado de la capa de facts, o 0 si no
// se fijó (el llamador aplica el default). Nunca falla la lectura silenciosa.
func FactsBudget() int {
	f, err := Load()
	if err != nil {
		return 0
	}
	return f.Facts.Budget
}

// Load lee config.toml. Si no existe, devuelve un File vacío (sin error).
func Load() (*File, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &File{}, nil
		}
		return nil, fmt.Errorf("failed to read config %s: %w", path, err)
	}
	var f File
	if err := toml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("failed to parse config %s: %w", path, err)
	}
	return &f, nil
}

// Save escribe el File a config.toml (sobrescribe; los comentarios se pierden).
func Save(f *File) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(f); err != nil {
		return fmt.Errorf("failed to encode config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create config dir: %w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("failed to write config %s: %w", path, err)
	}
	return nil
}

// Scopes devuelve los scopes configurados (mapa vacío si no hay).
func Scopes() (map[string]Scope, error) {
	f, err := Load()
	if err != nil {
		return nil, err
	}
	if f.Scopes == nil {
		return map[string]Scope{}, nil
	}
	return f.Scopes, nil
}

// Teams devuelve los stores de equipo registrados (mapa vacío si no hay).
func Teams() (map[string]Team, error) {
	f, err := Load()
	if err != nil {
		return nil, err
	}
	if f.Teams == nil {
		return map[string]Team{}, nil
	}
	return f.Teams, nil
}

// GetTeam devuelve un team registrado por nombre (ok=false si no existe).
func GetTeam(name string) (Team, bool, error) {
	teams, err := Teams()
	if err != nil {
		return Team{}, false, err
	}
	t, ok := teams[name]
	return t, ok, nil
}

// AddTeam registra (o sobrescribe) un team y persiste la config.
func AddTeam(name string, t Team) error {
	if err := ValidTeamName(name); err != nil {
		return err
	}
	f, err := Load()
	if err != nil {
		return err
	}
	if f.Teams == nil {
		f.Teams = map[string]Team{}
	}
	f.Teams[name] = t
	return Save(f)
}

// RemoveTeam desregistra un team y persiste la config. No falla si no existía.
func RemoveTeam(name string) error {
	f, err := Load()
	if err != nil {
		return err
	}
	if f.Teams == nil {
		return nil
	}
	delete(f.Teams, name)
	return Save(f)
}

// UserName devuelve a quién atribuir commits y facts en stores compartidos. Usa
// config user.name; si no está, cae a `git config user.name`, luego a la variable
// de entorno de usuario del SO, y por último a "unknown". Nunca falla.
func UserName() string {
	if f, err := Load(); err == nil && strings.TrimSpace(f.User.Name) != "" {
		return strings.TrimSpace(f.User.Name)
	}
	if out, err := exec.Command("git", "config", "user.name").Output(); err == nil {
		if name := strings.TrimSpace(string(out)); name != "" {
			return name
		}
	}
	for _, env := range []string{"USER", "USERNAME"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v
		}
	}
	return "unknown"
}

// settableKeys son las claves que `nem config set/get` maneja.
var settableKeys = map[string]bool{
	"summarize.backend": true, "summarize.model": true, "summarize.endpoint": true,
	"embed.backend": true, "embed.model": true, "embed.endpoint": true,
	"facts.budget": true,
	"user.name":    true,
}

// Get devuelve el valor de una clave punteada (p.ej. "summarize.backend").
func Get(key string) (string, error) {
	f, err := Load()
	if err != nil {
		return "", err
	}
	switch key {
	case "summarize.backend":
		return f.Summarize.Backend, nil
	case "summarize.model":
		return f.Summarize.Model, nil
	case "summarize.endpoint":
		return f.Summarize.Endpoint, nil
	case "embed.backend":
		return f.Embed.Backend, nil
	case "embed.model":
		return f.Embed.Model, nil
	case "embed.endpoint":
		return f.Embed.Endpoint, nil
	case "facts.budget":
		return strconv.Itoa(f.Facts.Budget), nil
	case "user.name":
		return f.User.Name, nil
	default:
		return "", fmt.Errorf("unknown config key %q", key)
	}
}

// Set fija una clave punteada y persiste el archivo.
func Set(key, value string) error {
	if !settableKeys[key] {
		return fmt.Errorf("unknown config key %q (valid: summarize.backend/model/endpoint, embed.backend/model/endpoint, facts.budget)", key)
	}
	f, err := Load()
	if err != nil {
		return err
	}
	if key == "facts.budget" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return fmt.Errorf("facts.budget must be a non-negative integer, got %q", value)
		}
		f.Facts.Budget = n
		return Save(f)
	}
	switch key {
	case "user.name":
		f.User.Name = value
	case "summarize.backend":
		f.Summarize.Backend = value
	case "summarize.model":
		f.Summarize.Model = value
	case "summarize.endpoint":
		f.Summarize.Endpoint = value
	case "embed.backend":
		f.Embed.Backend = value
	case "embed.model":
		f.Embed.Model = value
	case "embed.endpoint":
		f.Embed.Endpoint = value
	}
	return Save(f)
}

// Keys devuelve las claves configurables (para `nem config list`).
func Keys() []string {
	return []string{
		"summarize.backend", "summarize.model", "summarize.endpoint",
		"embed.backend", "embed.model", "embed.endpoint",
		"facts.budget", "user.name",
	}
}

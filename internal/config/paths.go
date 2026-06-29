// Package config resuelve las rutas del store local de nem (~/.nem) y la
// configuración persistida en config.toml.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// baseDir devuelve el directorio raíz bajo el que vive ~/.nem. Respeta $NEM_HOME
// (útil para tests y para alojar los stores en otra ruta); si no está fijado, usa
// el home del usuario.
func baseDir() (string, error) {
	if h := os.Getenv("NEM_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home directory: %w", err)
	}
	return home, nil
}

// Dir devuelve la ruta raíz del store local de nem (~/.nem).
func Dir() (string, error) {
	base, err := baseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, ".nem"), nil
}

// DBPath devuelve la ruta de la base SQLite local (~/.nem/nem.db).
func DBPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "nem.db"), nil
}

// ConfigPath devuelve la ruta del archivo de configuración (~/.nem/config.toml).
func ConfigPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// StoreDir devuelve la ruta del directorio versionado por git (~/.nem/store).
func StoreDir() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "store"), nil
}

// ChatsDir devuelve la ruta donde se exportan los .jsonl por commit
// (~/.nem/store/chats).
func ChatsDir() (string, error) {
	store, err := StoreDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(store, "chats"), nil
}

// TeamsDir devuelve la raíz de los stores de equipo (~/.nem/teams).
func TeamsDir() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "teams"), nil
}

// TeamDir devuelve el directorio de un store de equipo (~/.nem/teams/<name>).
// El nombre debe pasar ValidTeamName.
func TeamDir(name string) (string, error) {
	if err := ValidTeamName(name); err != nil {
		return "", err
	}
	teams, err := TeamsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(teams, name), nil
}

// TeamDBPath devuelve la ruta de la DB SQLite de un store de equipo.
func TeamDBPath(name string) (string, error) {
	dir, err := TeamDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "nem.db"), nil
}

// TeamChatsDir devuelve el dir de export por-commit de un store de equipo.
func TeamChatsDir(name string) (string, error) {
	dir, err := TeamDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "store", "chats"), nil
}

// ValidTeamName valida que name sea seguro como segmento de ruta y como clave de
// config: no vacío, sin separadores ni "..", sin espacios.
func ValidTeamName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("team name cannot be empty")
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("team name %q cannot contain path separators", name)
	case strings.ContainsAny(name, " \t"):
		return fmt.Errorf("team name %q cannot contain whitespace", name)
	case name == "." || name == "..":
		return fmt.Errorf("team name %q is not allowed", name)
	}
	return nil
}

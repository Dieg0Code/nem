package cli

import (
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/Dieg0Code/nem/internal/config"
	"github.com/Dieg0Code/nem/internal/db"
	"github.com/Dieg0Code/nem/internal/scope"
	"github.com/Dieg0Code/nem/internal/session"
	"github.com/spf13/cobra"
)

// openStore abre el Store personal de nem. Falla con un mensaje claro si nem no
// fue inicializado todavía.
func openStore() (db.Store, error) {
	dbPath, err := config.DBPath()
	if err != nil {
		return nil, err
	}
	store, err := openStoreAt(dbPath)
	if err != nil {
		return nil, fmt.Errorf("nem is not initialized on this machine; run 'nem init' first")
	}
	return store, nil
}

// openStoreAt abre el Store SQLite en dbPath. Devuelve un error si el archivo no
// existe (no crea stores implícitamente).
func openStoreAt(dbPath string) (db.Store, error) {
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no nem store at %s", dbPath)
	}
	store, err := db.New(db.WithPath(dbPath))
	if err != nil {
		return nil, fmt.Errorf("failed to open nem store: %w", err)
	}
	return store, nil
}

// openStoreFor abre el Store personal (team=="") o el de un team registrado.
func openStoreFor(team string) (db.Store, error) {
	if team == "" {
		return openStore()
	}
	if _, ok, err := config.GetTeam(team); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("unknown team %q (run 'nem team add %s <url>' first)", team, team)
	}
	dbPath, err := config.TeamDBPath(team)
	if err != nil {
		return nil, err
	}
	store, err := openStoreAt(dbPath)
	if err != nil {
		return nil, fmt.Errorf("team %q is registered but not cloned (run 'nem team add %s <url>'): %w", team, team, err)
	}
	return store, nil
}

// namedStore empareja un Store con el nombre de su origen ("" = personal).
type namedStore struct {
	Name  string
	Store db.Store
}

// allStores abre el store personal y cada team registrado y presente en disco.
// El llamador es responsable de cerrar cada Store. Los teams registrados pero no
// clonados se omiten silenciosamente (no rompen la lectura federada).
func allStores() ([]namedStore, error) {
	personal, err := openStore()
	if err != nil {
		return nil, err
	}
	teams, err := openTeamStores()
	if err != nil {
		personal.Close()
		return nil, err
	}
	return append([]namedStore{{Name: "", Store: personal}}, teams...), nil
}

// openTeamStores abre cada team registrado y presente en disco, en orden estable
// por nombre. Los teams registrados pero no clonados se omiten silenciosamente.
// El llamador cierra cada Store (closeStores).
func openTeamStores() ([]namedStore, error) {
	teams, err := config.Teams()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(teams))
	for name := range teams {
		names = append(names, name)
	}
	slices.Sort(names)

	stores := make([]namedStore, 0, len(names))
	for _, name := range names {
		dbPath, err := config.TeamDBPath(name)
		if err != nil {
			closeStores(stores)
			return nil, err
		}
		if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
			continue // registrado pero no clonado: se omite en silencio
		}
		// Un nem.db que existe pero no abre (corrupto, permisos, schema) NO se
		// omite en silencio: ocultarlo daría memoria de equipo incompleta sin avisar.
		store, err := db.New(db.WithPath(dbPath))
		if err != nil {
			closeStores(stores)
			return nil, fmt.Errorf("failed to open team store %q (%s): %w", name, dbPath, err)
		}
		stores = append(stores, namedStore{Name: name, Store: store})
	}
	return stores, nil
}

// closeStores cierra todos los stores abiertos por allStores.
func closeStores(stores []namedStore) {
	for _, s := range stores {
		s.Store.Close()
	}
}

// resolveActiveChat resuelve el chat sobre el que operan los comandos: el flag
// --chat si se pasó, si no la sesión de agente detectada. Devuelve "" si no hay
// ninguna.
func resolveActiveChat(override string) (chatID, source string, err error) {
	if override != "" {
		return override, "", nil
	}
	d, err := session.NewDetector()
	if err != nil {
		return "", "", err
	}
	s, err := d.Detect()
	if err != nil {
		return "", "", err
	}
	if s == nil {
		return "", "", nil
	}
	return s.ChatID, s.Source, nil
}

// shortHash acorta un hash de commit para mostrar.
func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// activeScopeName resuelve el scope activo: flag --scope, si no la variable de
// entorno NEM_SCOPE, si no "" (acceso completo).
func activeScopeName(cmd *cobra.Command) string {
	if v, err := cmd.Flags().GetString("scope"); err == nil && v != "" {
		return v
	}
	return os.Getenv("NEM_SCOPE")
}

// resolveScope traduce el scope activo a la lista de chat ids permitidos.
// Devuelve scoped=false (y allowed=nil) cuando no hay scope activo: en ese caso
// los comandos no filtran nada (comportamiento por defecto).
func resolveScope(cmd *cobra.Command, store db.Store) (allowed []string, scoped bool, err error) {
	name := activeScopeName(cmd)
	if name == "" {
		return nil, false, nil
	}
	scopes, err := config.Scopes()
	if err != nil {
		return nil, false, err
	}
	r, err := scope.New(scope.WithName(name), scope.WithScopes(scopes))
	if err != nil {
		return nil, false, err
	}
	chats, err := store.ListChats()
	if err != nil {
		return nil, false, err
	}
	refs := make([]scope.ChatRef, len(chats))
	for i, c := range chats {
		refs[i] = scope.ChatRef{ID: c.ID, Title: c.Title, Source: c.Source}
	}
	allowed, err = r.AllowedChatIDs(refs)
	if err != nil {
		return nil, false, err
	}
	return allowed, true, nil
}

// inScope indica si chatID está permitido bajo el scope resuelto.
func inScope(allowed []string, chatID string) bool {
	return slices.Contains(allowed, chatID)
}

package cli

import (
	"fmt"
	"os"

	"github.com/Dieg0Code/nem/internal/config"
	"github.com/Dieg0Code/nem/internal/db"
	"github.com/Dieg0Code/nem/internal/sync"
	"github.com/spf13/cobra"
)

// newInitCmd crea el comando `nem init`: prepara ~/.nem/, store/ y la base
// SQLite con el esquema migrado. Es idempotente.
func newInitCmd() *cobra.Command {
	var noSkill, noIngest bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize the local nem store in ~/.nem",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(cmd, noSkill, noIngest)
		},
	}
	cmd.Flags().BoolVar(&noSkill, "no-skill", false,
		"do not install the nem agent skill into the detected agents")
	cmd.Flags().BoolVar(&noIngest, "no-ingest", false,
		"do not ingest existing agent sessions on init")
	return cmd
}

func runInit(cmd *cobra.Command, noSkill, noIngest bool) error {
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	chatsDir, err := config.ChatsDir()
	if err != nil {
		return err
	}

	// Crea ~/.nem/store/chats/ (y los padres) si no existen.
	if err := os.MkdirAll(chatsDir, 0o755); err != nil {
		return fmt.Errorf("failed to create store directory: %w", err)
	}

	dbPath, err := config.DBPath()
	if err != nil {
		return err
	}

	// Abrir el Store crea nem.db y aplica la migración (idempotente).
	store, err := db.New(db.WithPath(dbPath))
	if err != nil {
		return err
	}
	defer store.Close()

	// Inicializa el repo git + .gitignore para que sync/remote funcionen.
	if err := sync.EnsureRepo(dir); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "nem initialized at %s\n", dir)

	// Trae las sesiones existentes para que el store sea útil de inmediato
	// (idempotente; no fatal: el store ya quedó usable).
	if !noIngest {
		fmt.Fprintln(out, "ingesting agent sessions...")
		sources, err := allSources()
		if err != nil {
			fmt.Fprintf(out, "warning: ingest incomplete: %v\n", err)
		} else if err := ingestSessions(out, store, sources); err != nil {
			fmt.Fprintf(out, "warning: ingest incomplete: %v\n", err)
		}
	}

	// Instala el agent skill (no fatal: el store ya quedó usable).
	if !noSkill {
		if err := installSkill(cmd); err != nil {
			fmt.Fprintf(out, "warning: could not install agent skill: %v\n", err)
		}
	}
	return nil
}

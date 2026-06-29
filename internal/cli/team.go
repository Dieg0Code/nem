package cli

import (
	"fmt"
	"os"
	"sort"

	"github.com/Dieg0Code/nem/internal/config"
	"github.com/Dieg0Code/nem/internal/db"
	"github.com/Dieg0Code/nem/internal/sync"
	"github.com/spf13/cobra"
)

// newTeamCmd crea `nem team`: gestiona los stores de equipo compartidos
// (~/.nem/teams/<name>), cada uno con su propia DB y su propio remote git.
func newTeamCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "team",
		Short: "Manage shared team stores (a common memory base for a team)",
	}
	cmd.AddCommand(newTeamAddCmd(), newTeamListCmd(), newTeamSyncCmd(), newTeamRemoveCmd())
	return cmd
}

// newTeamAddCmd crea `nem team add <name> <url>`: clona un store de equipo desde
// su remote git e importa sus commits.
func newTeamAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <name> <url>",
		Short: "Clone a shared team store from its git remote",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTeamAdd(cmd, args[0], args[1])
		},
	}
}

func runTeamAdd(cmd *cobra.Command, name, url string) error {
	if err := config.ValidTeamName(name); err != nil {
		return err
	}
	if _, ok, err := config.GetTeam(name); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("team %q is already added", name)
	}

	teamsDir, err := config.TeamsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(teamsDir, 0o755); err != nil {
		return fmt.Errorf("failed to create teams dir: %w", err)
	}
	dir, err := config.TeamDir(name)
	if err != nil {
		return err
	}
	if err := sync.Clone(url, dir); err != nil {
		return err
	}

	dbPath, err := config.TeamDBPath(name)
	if err != nil {
		return err
	}
	store, err := db.New(db.WithPath(dbPath))
	if err != nil {
		return err
	}
	defer store.Close()

	syncer, err := sync.NewSyncer(store, sync.WithDir(dir))
	if err != nil {
		return err
	}
	n, err := syncer.Import()
	if err != nil {
		return err
	}

	if err := config.AddTeam(name, config.Team{URL: url}); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "team %q added at %s · imported %d commits\n", name, dir, n)
	return nil
}

// newTeamListCmd crea `nem team list`: muestra los teams registrados.
func newTeamListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered team stores",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTeamList(cmd)
		},
	}
}

func runTeamList(cmd *cobra.Command) error {
	teams, err := config.Teams()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if len(teams) == 0 {
		fmt.Fprintln(out, "no team stores (add one with 'nem team add <name> <url>')")
		return nil
	}
	names := make([]string, 0, len(teams))
	for name := range teams {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t := teams[name]
		status := "ready"
		if dbPath, err := config.TeamDBPath(name); err == nil {
			if _, err := os.Stat(dbPath); os.IsNotExist(err) {
				status = "not cloned"
			}
		}
		fmt.Fprintf(out, "%-20s %s  [%s]\n", name, t.URL, status)
	}
	return nil
}

// newTeamSyncCmd crea `nem team sync <name>`: sincroniza un store de equipo con
// su remote (redactando secretos antes de publicar).
func newTeamSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync <name>",
		Short: "Sync a team store with its remote (redacts secrets first)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTeamSync(cmd, args[0])
		},
	}
}

func runTeamSync(cmd *cobra.Command, name string) error {
	store, err := openStoreFor(name)
	if err != nil {
		return err
	}
	defer store.Close()

	dir, err := config.TeamDir(name)
	if err != nil {
		return err
	}
	syncer, err := sync.NewSyncer(store, sync.WithDir(dir))
	if err != nil {
		return err
	}
	rep, err := syncer.Sync()
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "team %q: exported %d commits\n", name, rep.Exported)
	if total := totalRedacted(rep.Redacted); total > 0 {
		fmt.Fprintf(out, "redacted %d secrets: %s\n", total, summarizeRedacted(rep.Redacted))
	}
	if rep.Pushed {
		fmt.Fprintln(out, "synced with the remote")
	} else {
		fmt.Fprintln(out, "local commit (the team store has no remote)")
	}
	fmt.Fprintf(out, "imported %d new commits\n", rep.Imported)
	if rep.FactsExported > 0 || rep.FactsImported > 0 {
		fmt.Fprintf(out, "facts: %d exported, %d merged\n", rep.FactsExported, rep.FactsImported)
	}
	return nil
}

// newTeamRemoveCmd crea `nem team remove <name>`: desregistra un team. Con
// --purge además borra su store local del disco.
func newTeamRemoveCmd() *cobra.Command {
	var purge bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Deregister a team store (use --purge to also delete it from disk)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTeamRemove(cmd, args[0], purge)
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "also delete the cloned store from disk")
	return cmd
}

func runTeamRemove(cmd *cobra.Command, name string, purge bool) error {
	if _, ok, err := config.GetTeam(name); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("unknown team %q", name)
	}
	out := cmd.OutOrStdout()

	// Si se purga, borrar el clon ANTES de desregistrar: si RemoveAll falla, el team
	// sigue registrado y el comando se puede reintentar (no queda un clon huérfano
	// que el CLI ya no rastrea).
	var dir string
	if purge {
		d, err := config.TeamDir(name)
		if err != nil {
			return err
		}
		dir = d
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("failed to purge %s: %w", dir, err)
		}
	}
	if err := config.RemoveTeam(name); err != nil {
		return err
	}
	fmt.Fprintf(out, "team %q removed from the registry\n", name)
	if purge {
		fmt.Fprintf(out, "deleted %s\n", dir)
	}
	return nil
}

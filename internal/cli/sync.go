package cli

import (
	"fmt"
	"sort"

	"github.com/Dieg0Code/nem/internal/config"
	"github.com/Dieg0Code/nem/internal/sync"
	"github.com/spf13/cobra"
)

// newSyncCmd crea `nem sync`: exporta los commits (redactando secretos), los
// versiona con git y sincroniza con el remoto si está configurado.
func newSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Sync commits with the remote (redacts secrets before they leave)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSync(cmd)
		},
	}
}

func runSync(cmd *cobra.Command) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	dir, err := config.Dir()
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
	fmt.Fprintf(out, "exported %d commits\n", rep.Exported)
	if total := totalRedacted(rep.Redacted); total > 0 {
		fmt.Fprintf(out, "redacted %d secrets: %s\n", total, summarizeRedacted(rep.Redacted))
	}
	if rep.Pushed {
		fmt.Fprintln(out, "synced with the remote")
	} else {
		fmt.Fprintln(out, "local commit (no remote; use 'nem remote add origin <url>')")
	}
	fmt.Fprintf(out, "imported %d new commits\n", rep.Imported)
	if rep.FactsExported > 0 || rep.FactsImported > 0 {
		fmt.Fprintf(out, "facts: %d exported, %d merged\n", rep.FactsExported, rep.FactsImported)
	}
	return nil
}

func totalRedacted(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func summarizeRedacted(m map[string]int) string {
	kinds := make([]string, 0, len(m))
	for k := range m {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	s := ""
	for i, k := range kinds {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprintf("%d %s", m[k], k)
	}
	return s
}

package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dieg0Code/nem/internal/db"
	"github.com/Dieg0Code/nem/internal/timing"
	"github.com/Dieg0Code/nem/internal/when"
	"github.com/spf13/cobra"
)

// newOutlineCmd crea `nem outline [nodeID]`: imprime el árbol del índice (la
// tabla de contenidos de toda tu memoria) para que el agente decida en qué rama
// bajar. Sin argumento muestra los proyectos; con un nodeID muestra ese subárbol.
func newOutlineCmd() *cobra.Command {
	var (
		depth    int
		chatFlag string
	)
	cmd := &cobra.Command{
		Use:   "outline [nodeID]",
		Short: "Show the index tree (table of contents) to navigate",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			start := ""
			if len(args) == 1 {
				start = args[0]
			}
			return runOutline(cmd, chatFlag, start, depth)
		},
	}
	cmd.Flags().IntVar(&depth, "depth", 2, "how many levels to expand")
	cmd.Flags().StringVar(&chatFlag, "chat", "", "unused placeholder for symmetry")
	_ = cmd.Flags().MarkHidden("chat")
	return cmd
}

func runOutline(cmd *cobra.Command, _ string, start string, depth int) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	allowed, scoped, err := resolveScope(cmd, store)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()

	// Tier privilegiado: las afirmaciones durables (quién es, dónde trabaja, su
	// rutina) encabezan el mapa SIEMPRE, sin pasar por la recuperación
	// probabilística. Solo en la vista raíz y sin scope (los facts son globales).
	if start == "" && !scoped {
		printFacts(cmd, store)
	}

	var roots []db.Node
	if start == "" {
		roots, err = store.RootNodes()
	} else {
		n, e := store.GetNode(start)
		if e != nil {
			return e
		}
		if n == nil {
			return fmt.Errorf("node %q not found (run 'nem index' first?)", start)
		}
		roots = []db.Node{*n}
	}
	if err != nil {
		return err
	}
	if len(roots) == 0 {
		fmt.Fprintln(out, "empty index — run 'nem index' to build the tree")
		return nil
	}

	for _, r := range roots {
		printNode(cmd, store, r, 0, depth, allowed, scoped)
	}
	return nil
}

// printFacts imprime al tope del outline la memoria durable: los hechos estables
// (always-loaded) y, aparte, los recordatorios vigentes ordenados por fecha.
// Silencioso si no hay nada, para no ensuciar la vista.
func printFacts(cmd *cobra.Command, store db.Store) {
	facts, err := store.ListFacts(false)
	if err != nil || len(facts) == 0 {
		return
	}
	out := cmd.OutOrStdout()
	stable, reminders := splitFacts(facts)

	if len(stable) > 0 {
		fmt.Fprintln(out, "## Facts (always-loaded)")
		for _, f := range stable {
			fmt.Fprintf(out, "- %s  (fact:%s)\n", f.Content, shortHash(f.ID))
		}
		fmt.Fprintln(out)
	}
	if len(reminders) > 0 {
		nowUnix := time.Now().Unix()
		fmt.Fprintln(out, "## Reminders")
		for _, f := range reminders {
			fmt.Fprintf(out, "- [%s] %s  (fact:%s)\n", when.Humanize(f.DueAt, nowUnix), f.Content, shortHash(f.ID))
		}
		fmt.Fprintln(out)
	}
}

// splitFacts separa los hechos estables de los recordatorios con fecha, y
// ordena los recordatorios por vencimiento (más próximo/atrasado primero).
func splitFacts(facts []db.Fact) (stable, reminders []db.Fact) {
	for _, f := range facts {
		if f.DueAt > 0 {
			reminders = append(reminders, f)
		} else {
			stable = append(stable, f)
		}
	}
	sort.Slice(reminders, func(i, j int) bool { return reminders[i].DueAt < reminders[j].DueAt })
	return stable, reminders
}

// printNode imprime un nodo y recurre por sus hijos hasta `depth`.
func printNode(cmd *cobra.Command, store db.Store, n db.Node, level, depth int, allowed []string, scoped bool) {
	// Filtro de scope: ocultar nodos cuyo chat está fuera del scope.
	if scoped && n.ChatID != "" && !inScope(allowed, n.ChatID) {
		return
	}
	indent := strings.Repeat("  ", level)
	id := n.ID
	line := fmt.Sprintf("%s- [%s] %s", indent, n.Kind, n.Title)
	if n.ActiveSecs > 0 {
		line += "  · ~" + timing.Format(time.Duration(n.ActiveSecs)*time.Second) + " activas"
	}
	if s := strings.TrimSpace(n.Summary); s != "" {
		line += "  — " + s
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s  (%s)\n", line, id)

	if level+1 >= depth {
		return
	}
	children, err := store.ChildNodes(n.ID)
	if err != nil {
		return
	}
	for _, c := range children {
		printNode(cmd, store, c, level+1, depth, allowed, scoped)
	}
}

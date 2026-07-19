package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/Dieg0Code/nem/internal/db"
	"github.com/Dieg0Code/nem/internal/ingest"
	"github.com/spf13/cobra"
)

// agentNames son los agentes soportados, en el orden en que se reportan.
// Fuente única que comparten `nem ingest` (args válidos), `nem init` y los
// mensajes de error.
var agentNames = []string{"codex", "claude", "antigravity", "opencode"}

// newIngestCmd crea `nem ingest [agente]`. Sin argumento ingesta todos.
func newIngestCmd() *cobra.Command {
	return &cobra.Command{
		Use:       fmt.Sprintf("ingest [%s]", strings.Join(agentNames, "|")),
		Short:     "Ingest agent sessions (Codex, Claude Code, Antigravity, opencode) into the nem store",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: agentNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIngest(cmd, args)
		},
	}
}

// sourceFor construye la fuente de sesiones de un agente por nombre.
func sourceFor(name string) (ingest.Source, error) {
	switch name {
	case "codex":
		return ingest.NewFileSource(ingest.NewCodexParser())
	case "claude":
		return ingest.NewFileSource(ingest.NewClaudeParser())
	case "antigravity":
		return ingest.NewFileSource(ingest.NewAntigravityParser())
	case "opencode":
		return ingest.NewOpencodeSource()
	}
	return nil, fmt.Errorf("unknown source %q (use %s)", name, quoteList(agentNames))
}

// allSources devuelve las fuentes de todos los agentes soportados.
func allSources() ([]ingest.Source, error) {
	var out []ingest.Source
	for _, name := range agentNames {
		src, err := sourceFor(name)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, nil
}

// quoteList arma "'a', 'b' or 'c'" para mensajes de error.
func quoteList(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = "'" + n + "'"
	}
	if len(quoted) == 1 {
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " or " + quoted[len(quoted)-1]
}

func runIngest(cmd *cobra.Command, args []string) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	var sources []ingest.Source
	if len(args) == 0 {
		sources, err = allSources()
	} else {
		var src ingest.Source
		src, err = sourceFor(args[0])
		sources = []ingest.Source{src}
	}
	if err != nil {
		return err
	}

	return ingestSessions(cmd.OutOrStdout(), store, sources)
}

// ingestSessions corre cada fuente contra el store e imprime un resumen por
// agente. Compartido por `nem ingest` y `nem init`.
func ingestSessions(out io.Writer, store db.Store, sources []ingest.Source) error {
	for _, src := range sources {
		rep, err := ingest.Ingest(store, src)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: %d chats, %d new messages (%d scanned, %d skipped)\n",
			rep.Source, rep.Chats, rep.Messages, rep.Scanned, rep.Skipped)
		if len(rep.Errors) > 0 {
			fmt.Fprintf(out, "  %d sessions with errors (first 3):\n", len(rep.Errors))
			for i, e := range rep.Errors {
				if i == 3 {
					break
				}
				fmt.Fprintf(out, "    - %s\n", e)
			}
		}
	}
	return nil
}

package cli

import (
	"fmt"
	"io"

	"github.com/Dieg0Code/nem/internal/db"
	"github.com/Dieg0Code/nem/internal/ingest"
	"github.com/spf13/cobra"
)

// newIngestCmd crea `nem ingest [codex|claude|antigravity]`. Sin argumento
// ingesta todos.
func newIngestCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "ingest [codex|claude|antigravity]",
		Short:     "Ingest Codex, Claude Code and/or Antigravity sessions into the nem store",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"codex", "claude", "antigravity"},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIngest(cmd, args)
		},
	}
}

// allParsers devuelve los parsers de todos los agentes soportados, en el orden
// en que se reportan. Fuente única que comparten `nem ingest` (sin args) y
// `nem init`.
func allParsers() []ingest.Parser {
	return []ingest.Parser{
		ingest.NewCodexParser(),
		ingest.NewClaudeParser(),
		ingest.NewAntigravityParser(),
	}
}

func runIngest(cmd *cobra.Command, args []string) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	var parsers []ingest.Parser
	switch {
	case len(args) == 0:
		parsers = allParsers()
	case args[0] == "codex":
		parsers = []ingest.Parser{ingest.NewCodexParser()}
	case args[0] == "claude":
		parsers = []ingest.Parser{ingest.NewClaudeParser()}
	case args[0] == "antigravity":
		parsers = []ingest.Parser{ingest.NewAntigravityParser()}
	default:
		return fmt.Errorf("unknown source %q (use 'codex', 'claude' or 'antigravity')", args[0])
	}

	return ingestSessions(cmd.OutOrStdout(), store, parsers)
}

// ingestSessions corre cada parser contra el store e imprime un resumen por
// agente. Compartido por `nem ingest` y `nem init`.
func ingestSessions(out io.Writer, store db.Store, parsers []ingest.Parser) error {
	for _, p := range parsers {
		rep, err := ingest.Ingest(store, p)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: %d chats, %d new messages (%d files, %d skipped)\n",
			rep.Source, rep.Chats, rep.Messages, rep.Files, rep.Skipped)
		if len(rep.Errors) > 0 {
			fmt.Fprintf(out, "  %d files with errors (first 3):\n", len(rep.Errors))
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

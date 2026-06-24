package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Dieg0Code/nem/internal/output"
	"github.com/Dieg0Code/nem/internal/retrieve"
	"github.com/spf13/cobra"
)

// snippetLen acota el contenido mostrado por hit.
const snippetLen = 240

// newSearchCmd crea `nem search "<query>"`: búsqueda híbrida (BM25 sobre mensajes
// + BM25 sobre el árbol de índice, fusionados por RRF + recencia). El agente
// rerankea leyendo los resultados.
func newSearchCmd() *cobra.Command {
	var (
		top    int
		format string
		role   string
		mode   string
		expand bool
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search memory (hybrid: messages + index tree, BM25/RRF)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSearch(cmd, args[0], top, format, role, mode, expand)
		},
	}
	cmd.Flags().IntVar(&top, "top", 10, "number of results")
	cmd.Flags().StringVar(&format, "format", output.FormatMarkdown, "llm | json | markdown")
	cmd.Flags().StringVar(&role, "role", "", "message roles to include (default: conversation + reasoning; 'all' includes tool)")
	cmd.Flags().StringVar(&mode, "mode", "hybrid", "hybrid | keyword | semantic")
	cmd.Flags().BoolVar(&expand, "expand", true, "expand recall via relevance feedback (PRF); ignored in --mode keyword")
	return cmd
}

func runSearch(cmd *cobra.Command, query string, top int, format, role, mode string, expand bool) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	roles, err := resolveRoles(role)
	if err != nil {
		return err
	}
	allowed, _, err := resolveScope(cmd, store)
	if err != nil {
		return err
	}

	results, err := retrieve.Search(store, retrieve.Params{
		Query: query, Top: top, Roles: roles, Allowed: allowed, Mode: mode, Expand: expand,
	})
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if len(results) == 0 {
		fmt.Fprintf(out, "no results for %q\n", query)
		return nil
	}
	if format == output.FormatJSON {
		return renderSearchJSON(cmd, query, results)
	}

	fmt.Fprintf(out, "%q — %d results\n\n", query, len(results))
	for i, r := range results {
		title := r.Title
		if title == "" {
			title = "(untitled)"
		}
		switch r.Kind {
		case "node":
			fmt.Fprintf(out, "%d. [index:%s · %s]  %s\n", i+1, r.NodeKind, title, r.ID)
			fmt.Fprintf(out, "   %s\n\n", snippet(r.Content))
		default: // message
			fmt.Fprintf(out, "%d. [%s · %s]  msg:%s\n", i+1, r.Source, title, r.ID)
			fmt.Fprintf(out, "   %s: %s\n\n", r.Role, snippet(r.Content))
		}
	}
	return nil
}

func renderSearchJSON(cmd *cobra.Command, query string, results []retrieve.Scored) error {
	type jsonHit struct {
		Kind     string  `json:"kind"`
		ID       string  `json:"id"`
		ChatID   string  `json:"chat_id,omitempty"`
		Title    string  `json:"title"`
		Source   string  `json:"source,omitempty"`
		Role     string  `json:"role,omitempty"`
		NodeKind string  `json:"node_kind,omitempty"`
		Content  string  `json:"content"`
		Score    float64 `json:"score"`
	}
	out := make([]jsonHit, 0, len(results))
	for _, r := range results {
		out = append(out, jsonHit{
			Kind: r.Kind, ID: r.ID, ChatID: r.ChatID, Title: r.Title, Source: r.Source,
			Role: r.Role, NodeKind: r.NodeKind, Content: r.Content, Score: r.Score,
		})
	}
	b, err := json.MarshalIndent(map[string]any{"query": query, "hits": out}, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to render json: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(b))
	return nil
}

// validRoles son los roles que nem persiste. "all" es un atajo especial.
var validRoles = map[string]bool{"user": true, "assistant": true, "reasoning": true, "tool": true}

// resolveRoles traduce el flag --role a la lista de roles para filtrar.
// Vacío = conversación + reasoning (excluye el ruido de tool). "all" = nil
// (todos los roles, incluido tool).
func resolveRoles(flag string) ([]string, error) {
	flag = strings.TrimSpace(flag)
	switch flag {
	case "":
		return []string{"user", "assistant", "reasoning"}, nil
	case "all":
		return nil, nil
	default:
		parts := strings.Split(flag, ",")
		roles := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if !validRoles[p] {
				return nil, fmt.Errorf("unknown role %q (valid: user, assistant, reasoning, tool, all)", p)
			}
			roles = append(roles, p)
		}
		if len(roles) == 0 {
			return nil, fmt.Errorf("no valid roles in %q", flag)
		}
		return roles, nil
	}
}

// snippet acorta el contenido a una sola línea para el listado.
func snippet(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r", " "), "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > snippetLen {
		return string(r[:snippetLen]) + "…"
	}
	return s
}

package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Dieg0Code/nem/internal/db"
	"github.com/Dieg0Code/nem/internal/facts"
	"github.com/Dieg0Code/nem/internal/output"
	"github.com/Dieg0Code/nem/internal/retrieve"
	"github.com/google/uuid"
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
		team   string
		local  bool
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search memory (hybrid: messages + index tree, BM25/RRF)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSearch(cmd, args[0], top, format, role, mode, expand, team, local)
		},
	}
	cmd.Flags().IntVar(&top, "top", 10, "number of results")
	cmd.Flags().StringVar(&format, "format", output.FormatMarkdown, "llm | json | markdown")
	cmd.Flags().StringVar(&role, "role", "", "message roles to include (default: conversation + reasoning; 'all' includes tool)")
	cmd.Flags().StringVar(&mode, "mode", "hybrid", "hybrid | keyword | semantic")
	cmd.Flags().BoolVar(&expand, "expand", true, "expand recall via relevance feedback (PRF); ignored in --mode keyword")
	cmd.Flags().StringVar(&team, "team", "", "search only this team store (default: personal + all teams)")
	cmd.Flags().BoolVar(&local, "local", false, "search only the personal store")
	return cmd
}

func runSearch(cmd *cobra.Command, query string, top int, format, role, mode string, expand bool, team string, local bool) error {
	roles, err := resolveRoles(role)
	if err != nil {
		return err
	}

	stores, err := searchStores(cmd, team, local)
	if err != nil {
		return err
	}
	defer closeStores(stores)

	// Lectura federada: corre el search por cada store, estampa el origen y fusiona
	// los rankings por-store en uno solo (RRF re-rankea cross-store).
	scopeName := activeScopeName(cmd)
	channels := make([]retrieve.Channel, 0, len(stores))
	for _, ns := range stores {
		var allowed []string
		if ns.Name == "" && scopeName != "" {
			a, _, err := resolveScope(cmd, ns.Store)
			if err != nil {
				return err
			}
			allowed = a
		}
		res, err := retrieve.Search(ns.Store, retrieve.Params{
			Query: query, Top: top, Roles: roles, Allowed: allowed, Mode: mode, Expand: expand,
			Learned: ns.Name == "" && scopeName == "",
		})
		if err != nil {
			return err
		}
		items := make([]retrieve.Item, len(res))
		for i, r := range res {
			it := r.Item
			it.StoreID = ns.Name
			items[i] = it
		}
		channels = append(channels, retrieve.Channel{Name: "store:" + ns.Name, Items: items})

		// Señal "aprendida" del peso de los facts: solo en el personal y sin scope.
		// Nunca se escribe en stores de equipo durante una lectura.
		if ns.Name == "" && scopeName == "" {
			recordFactHits(ns.Store, query)
		}
	}
	results := retrieve.Fuse(channels, top)

	// Log de la búsqueda servida (para correlacionar con el read que siga): solo
	// el store personal y sin scope, igual que recordFactHits. Best-effort.
	if scopeName == "" {
		for _, ns := range stores {
			if ns.Name == "" {
				if served := retrieve.ServedNodeIDs(results); len(served) > 0 {
					_ = ns.Store.LogSearch(uuid.NewString(), query, served, time.Now().Unix())
				}
				break
			}
		}
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
			fmt.Fprintf(out, "%d. [%s · index:%s · %s]  %s\n", i+1, originTag(r.StoreID), r.NodeKind, title, r.ID)
			fmt.Fprintf(out, "   %s\n\n", snippet(r.Content))
		default: // message
			fmt.Fprintf(out, "%d. [%s · %s · %s]  msg:%s\n", i+1, originTag(r.StoreID), r.Source, title, r.ID)
			fmt.Fprintf(out, "   %s: %s\n\n", r.Role, snippet(r.Content))
		}
	}
	return nil
}

// searchStores resuelve los stores sobre los que corre el search según los flags:
// --team acota a un team; --local o un --scope activo acotan al personal; por
// defecto, personal + todos los teams (lectura federada). El llamador cierra los
// stores con closeStores.
func searchStores(cmd *cobra.Command, team string, local bool) ([]namedStore, error) {
	if team != "" {
		s, err := openStoreFor(team)
		if err != nil {
			return nil, err
		}
		return []namedStore{{Name: team, Store: s}}, nil
	}
	// Un scope activo referencia chat ids del store personal: federar no aplica.
	if local || activeScopeName(cmd) != "" {
		s, err := openStore()
		if err != nil {
			return nil, err
		}
		return []namedStore{{Name: "", Store: s}}, nil
	}
	return allStores()
}

// originTag etiqueta el origen de un resultado para mostrar: "" → "personal".
func originTag(storeID string) string {
	if storeID == "" {
		return "personal"
	}
	return "team:" + storeID
}

func renderSearchJSON(cmd *cobra.Command, query string, results []retrieve.Scored) error {
	type jsonHit struct {
		Kind     string  `json:"kind"`
		ID       string  `json:"id"`
		ChatID   string  `json:"chat_id,omitempty"`
		Title    string  `json:"title"`
		Store    string  `json:"store"`
		Source   string  `json:"source,omitempty"`
		Role     string  `json:"role,omitempty"`
		NodeKind string  `json:"node_kind,omitempty"`
		Content  string  `json:"content"`
		Score    float64 `json:"score"`
	}
	out := make([]jsonHit, 0, len(results))
	for _, r := range results {
		out = append(out, jsonHit{
			Kind: r.Kind, ID: r.ID, ChatID: r.ChatID, Title: r.Title, Store: originTag(r.StoreID), Source: r.Source,
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

// recordFactHits sube el contador de uso de los facts que matchean la query (la
// mitad "aprendida" del peso). Best-effort: un fallo nunca debe romper el search.
func recordFactHits(store db.Store, query string) {
	all, err := store.ListFacts(false)
	if err != nil {
		return
	}
	if m := facts.MatchQuery(all, query); len(m) > 0 {
		_ = store.RecordFactHit(facts.IDs(m), time.Now().Unix())
	}
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

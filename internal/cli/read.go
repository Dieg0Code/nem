package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Dieg0Code/nem/internal/db"
	"github.com/Dieg0Code/nem/internal/output"
	"github.com/spf13/cobra"
)

// chatReadLimit acota cuántos mensajes de conversación se muestran al leer un
// nodo de chat (drill-down acotado para no quemar tokens).
const chatReadLimit = 40

// newReadCmd crea `nem read <ref>`: muestra contenido. ref puede ser HEAD, un
// hash de commit, `commit:<hash>` o `chat:<id>` (nodos del árbol).
func newReadCmd() *cobra.Command {
	var (
		format   string
		chatFlag string
		team     string
	)
	cmd := &cobra.Command{
		Use:   "read <HEAD|hash|commit:hash|chat:id>",
		Short: "Show the contents of a commit or chat node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRead(cmd, chatFlag, args[0], format, team)
		},
	}
	cmd.Flags().StringVar(&format, "format", output.FormatMarkdown, "llm | json | markdown")
	cmd.Flags().StringVar(&chatFlag, "chat", "", "chat id (for HEAD; default: detected session)")
	cmd.Flags().StringVar(&team, "team", "", "resolve the commit only in this team store")
	return cmd
}

func runRead(cmd *cobra.Command, chatFlag, ref, format, team string) error {
	// HEAD y chat: están atados a la sesión local → siempre el store personal.
	if strings.EqualFold(ref, "HEAD") || strings.HasPrefix(ref, "chat:") {
		return runReadLocal(cmd, chatFlag, ref, format)
	}

	// Commit por hash/commit:<hash>: se resuelve de forma federada (personal +
	// teams), o acotado a --team. El primer match único gana; ambigüedad entre
	// stores pide desambiguar con --team.
	stores, err := readStores(cmd, team)
	if err != nil {
		return err
	}
	defer closeStores(stores)

	bare := strings.TrimPrefix(ref, "commit:")
	var (
		found *db.Commit
		owner namedStore
		hits  int
	)
	for _, ns := range stores {
		c, err := ns.Store.GetCommit(bare)
		if err != nil {
			return fmt.Errorf("in %s: %w", originTag(ns.Name), err)
		}
		if c != nil {
			found, owner = c, ns
			hits++
		}
	}
	if hits == 0 {
		return fmt.Errorf("commit %q not found", ref)
	}
	if hits > 1 {
		return fmt.Errorf("commit %q exists in more than one store; disambiguate with --team <name> or a longer hash", ref)
	}

	// El scope solo aplica al store personal (sus chat ids).
	if owner.Name == "" {
		allowed, scoped, err := resolveScope(cmd, owner.Store)
		if err != nil {
			return err
		}
		if scoped && !inScope(allowed, found.ChatID) {
			return fmt.Errorf("commit %q not found in scope %q", ref, activeScopeName(cmd))
		}
	}
	return renderCommit(cmd, owner.Store, found, originTag(owner.Name), format)
}

// runReadLocal maneja las refs atadas a la sesión (HEAD y chat:<id>) contra el
// store personal, respetando el scope activo.
func runReadLocal(cmd *cobra.Command, chatFlag, ref, format string) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	allowed, scoped, err := resolveScope(cmd, store)
	if err != nil {
		return err
	}

	if strings.HasPrefix(ref, "chat:") {
		chatID := strings.TrimPrefix(ref, "chat:")
		if scoped && !inScope(allowed, chatID) {
			return fmt.Errorf("chat %q not found in scope %q", chatID, activeScopeName(cmd))
		}
		return readChat(cmd, store, chatID, format)
	}

	commit, err := resolveCommit(store, chatFlag, ref)
	if err != nil {
		return err
	}
	if commit == nil {
		return fmt.Errorf("commit %q not found", ref)
	}
	if scoped && !inScope(allowed, commit.ChatID) {
		return fmt.Errorf("commit %q not found in scope %q", ref, activeScopeName(cmd))
	}
	return renderCommit(cmd, store, commit, "", format)
}

// renderCommit renderiza un commit resuelto, con su origen (vacío = sin etiqueta).
func renderCommit(cmd *cobra.Command, store db.Store, commit *db.Commit, origin, format string) error {
	snap, err := output.ParseSnapshot(commit.Snapshot)
	if err != nil {
		return err
	}
	chat, err := store.GetChat(commit.ChatID)
	if err != nil {
		return err
	}
	doc := output.Doc{
		Date:     time.Unix(commit.CreatedAt, 0),
		Messages: snap,
		Commit:   commit,
		Origin:   origin,
	}
	if chat != nil {
		doc.Title = chat.Title
		doc.Source = chat.Source
	}
	rendered, err := output.Render(doc, format)
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), rendered)
	return nil
}

// readStores resuelve los stores donde buscar un commit por hash: --team acota a
// un team; un --scope activo acota al personal; por defecto personal + teams.
func readStores(cmd *cobra.Command, team string) ([]namedStore, error) {
	if team != "" {
		s, err := openStoreFor(team)
		if err != nil {
			return nil, err
		}
		return []namedStore{{Name: team, Store: s}}, nil
	}
	if activeScopeName(cmd) != "" {
		s, err := openStore()
		if err != nil {
			return nil, err
		}
		return []namedStore{{Name: "", Store: s}}, nil
	}
	return allStores()
}

// readChat renderiza los últimos mensajes de conversación de un chat.
func readChat(cmd *cobra.Command, store db.Store, chatID, format string) error {
	chat, err := store.GetChat(chatID)
	if err != nil {
		return err
	}
	if chat == nil {
		return fmt.Errorf("chat %q not found", chatID)
	}
	msgs, err := store.LastMessages(chatID, chatReadLimit, []string{"user", "assistant", "reasoning"})
	if err != nil {
		return err
	}
	snap := make([]output.SnapMessage, 0, len(msgs))
	for _, m := range msgs {
		snap = append(snap, output.SnapMessage{
			Role: m.Role, Content: m.Content, Timestamp: m.Timestamp, Seq: m.Seq,
		})
	}
	doc := output.Doc{
		Title:    chat.Title,
		Source:   chat.Source,
		Date:     time.Unix(chat.CreatedAt, 0),
		Messages: snap,
	}
	rendered, err := output.Render(doc, format)
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), rendered)
	return nil
}

// resolveCommit resuelve "HEAD" (último commit del chat activo) o un hash/prefijo.
func resolveCommit(store db.Store, chatFlag, ref string) (*db.Commit, error) {
	if strings.EqualFold(ref, "HEAD") {
		chatID, _, err := resolveActiveChat(chatFlag)
		if err != nil {
			return nil, err
		}
		if chatID == "" {
			return nil, errors.New("no active session detected for HEAD; use --chat <id>")
		}
		return store.HeadCommit(chatID)
	}
	return store.GetCommit(ref)
}

package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Dieg0Code/nem/internal/db"
	"github.com/Dieg0Code/nem/internal/when"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// newFactCmd crea `nem fact`: la capa de memoria SEMÁNTICA de nem. A diferencia
// de los commits (derivados de conversaciones, recuperados por probabilidad),
// un fact es una afirmación durable que el agente o el humano escribe directo y
// que se carga SIEMPRE al arrancar (encabeza `nem outline`). Es donde vive
// "quién soy, dónde trabajo, mi rutina" para que ningún agente lo tenga que
// adivinar buscando entre miles de mensajes.
func newFactCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fact",
		Short: "Durable facts an agent always loads at session start (semantic memory)",
	}
	cmd.AddCommand(newFactAddCmd(), newFactListCmd(), newFactDoneCmd(), newFactRmCmd())
	return cmd
}

// newFactAddCmd: `nem fact add "<afirmación>" [--supersedes <id>] [--kind note] [--due <when>]`.
func newFactAddCmd() *cobra.Command {
	var (
		supersedes string
		kind       string
		due        string
	)
	cmd := &cobra.Command{
		Use:   "add <text>",
		Short: "Assert a durable fact, or a dated reminder with --due",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := strings.TrimSpace(strings.Join(args, " "))
			if text == "" {
				return errors.New("fact text is required")
			}
			return runFactAdd(cmd, text, supersedes, kind, due)
		},
	}
	cmd.Flags().StringVar(&supersedes, "supersedes", "", "id of a fact this one replaces (kept as trail, not deleted)")
	cmd.Flags().StringVar(&kind, "kind", "", "fact kind: note (default) | reminder | schedule")
	cmd.Flags().StringVar(&due, "due", "", "due date for a reminder: YYYY-MM-DD, YYYY-MM-DD HH:MM, +3d, today, tomorrow")
	return cmd
}

// errScopedFacts se devuelve cuando hay un scope activo: los facts son un tier
// GLOBAL (no filtrable por scope), así que bajo un scope quedan inaccesibles —
// igual que `outline` los oculta— para no saltarse el límite de lectura.
var errScopedFacts = errors.New("facts are global and not available under a scope; unset --scope/NEM_SCOPE to access them")

func runFactAdd(cmd *cobra.Command, text, supersedes, kind, due string) error {
	if activeScopeName(cmd) != "" {
		return errScopedFacts
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	// La fuente refleja quién afirma: la sesión de agente detectada, o "human"
	// cuando se corre a mano.
	_, source, _ := resolveActiveChat("")
	if source == "" {
		source = "human"
	}

	now := time.Now()
	ts := now.Unix()
	f := &db.Fact{
		ID:        uuid.NewString(),
		Content:   text,
		Kind:      strings.TrimSpace(kind),
		Source:    source,
		CreatedAt: ts,
		UpdatedAt: ts,
	}
	// --due convierte la afirmación en recordatorio (kind por defecto reminder).
	if due != "" {
		dueAt, err := when.Parse(due, now)
		if err != nil {
			return err
		}
		f.DueAt = dueAt
		if f.Kind == "" {
			f.Kind = "reminder"
		}
	}
	if f.Kind == "" {
		f.Kind = "note"
	}

	if supersedes != "" {
		// Aceptar el id corto que imprime `fact list` (resuelve prefijos).
		old, err := resolveFact(store, supersedes)
		if err != nil {
			return fmt.Errorf("can't supersede: %w", err)
		}
		supersedes = old.ID
	}
	if err := store.AddFact(f); err != nil {
		return err
	}
	if supersedes != "" {
		if err := store.SupersedeFact(supersedes, f.ID, ts); err != nil {
			return err
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "fact saved (%s)\n", shortHash(f.ID))
	return nil
}

// newFactListCmd: `nem fact list [--all]`.
func newFactListCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List durable facts (vigentes by default; --all includes superseded)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runFactList(cmd, all)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include superseded facts (the trail)")
	return cmd
}

func runFactList(cmd *cobra.Command, all bool) error {
	if activeScopeName(cmd) != "" {
		return errScopedFacts
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	facts, err := store.ListFacts(all)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if len(facts) == 0 {
		fmt.Fprintln(out, "no facts yet — add one: nem fact add \"...\"")
		return nil
	}
	nowUnix := time.Now().Unix()
	for _, f := range facts {
		mark := ""
		switch {
		case f.Superseded:
			mark = " (superseded)"
		case f.Done:
			mark = " (done)"
		case f.DueAt > 0:
			mark = " (due " + when.Humanize(f.DueAt, nowUnix) + ")"
		}
		fmt.Fprintf(out, "- %s  [%s · %s]%s\n  %s\n", shortHash(f.ID), f.Kind, f.Source, mark, f.Content)
	}
	return nil
}

// newFactDoneCmd: `nem fact done <id>` marca un recordatorio como completado.
func newFactDoneCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "done <id>",
		Short: "Mark a reminder as completed (kept as trail; stops surfacing)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFactDone(cmd, args[0])
		},
	}
	return cmd
}

func runFactDone(cmd *cobra.Command, id string) error {
	if activeScopeName(cmd) != "" {
		return errScopedFacts
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	f, err := resolveFact(store, id)
	if err != nil {
		return err
	}
	if err := store.MarkFactDone(f.ID, time.Now().Unix()); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "marked done %s\n", shortHash(f.ID))
	return nil
}

// newFactRmCmd: `nem fact rm <id>` (borra de raíz; para corregir errores).
func newFactRmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rm <id>",
		Short: "Delete a fact outright (to fix a mistake; to update, use add --supersedes)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFactRm(cmd, args[0])
		},
	}
	return cmd
}

func runFactRm(cmd *cobra.Command, id string) error {
	if activeScopeName(cmd) != "" {
		return errScopedFacts
	}
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	// Resolver prefijo: aceptar el id corto que muestra `fact list`.
	f, err := resolveFact(store, id)
	if err != nil {
		return err
	}
	if err := store.DeleteFact(f.ID); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "deleted fact %s\n", shortHash(f.ID))
	return nil
}

// resolveFact acepta un id completo o un prefijo (como el que imprime list) y
// devuelve el fact único que matchea.
func resolveFact(store db.Store, id string) (*db.Fact, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("fact id is required")
	}
	if f, err := store.GetFact(id); err != nil {
		return nil, err
	} else if f != nil {
		return f, nil
	}
	facts, err := store.ListFacts(true)
	if err != nil {
		return nil, err
	}
	var match *db.Fact
	for i := range facts {
		if strings.HasPrefix(facts[i].ID, id) {
			if match != nil {
				return nil, fmt.Errorf("ambiguous fact id %q (matches more than one)", id)
			}
			match = &facts[i]
		}
	}
	if match == nil {
		return nil, fmt.Errorf("fact %q not found", id)
	}
	return match, nil
}

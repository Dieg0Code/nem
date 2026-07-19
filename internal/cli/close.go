package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Dieg0Code/nem/internal/config"
	"github.com/Dieg0Code/nem/internal/db"
	"github.com/Dieg0Code/nem/internal/index"
	"github.com/spf13/cobra"
)

// newCloseCmd crea `nem close`: el cierre operativo de una sesión. Es el atajo
// dogfood para no olvidar el ciclo ingest → add → commit → index.
func newCloseCmd() *cobra.Command {
	var (
		message  string
		lastN    int
		chatFlag string
		role     string
		noIndex  bool
	)
	cmd := &cobra.Command{
		Use:   "close",
		Short: "Ingest, commit new messages since HEAD, and refresh the index",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClose(cmd, closeOpts{
				message: message, lastN: lastN, chatFlag: chatFlag, role: role, noIndex: noIndex,
			})
		},
	}
	cmd.Flags().StringVarP(&message, "message", "m", "", "commit message (required)")
	cmd.Flags().IntVarP(&lastN, "last", "L", 0, "refuse to close if more than N new messages would be committed (0 = no limit)")
	cmd.Flags().StringVar(&chatFlag, "chat", "", "chat id (default: detected session)")
	cmd.Flags().StringVar(&role, "role", "", "roles to stage, comma-separated (default: conversation + reasoning; 'all' = every role, incl. tool)")
	cmd.Flags().BoolVar(&noIndex, "no-index", false, "skip refreshing the index after the commit")
	_ = cmd.MarkFlagRequired("message")
	return cmd
}

type closeOpts struct {
	message  string
	lastN    int
	chatFlag string
	role     string
	noIndex  bool
}

// closeSources es inyectable para que los tests corran close sin tocar los
// homes reales de los agentes.
var closeSources = allSources

func runClose(cmd *cobra.Command, opts closeOpts) error {
	if strings.TrimSpace(opts.message) == "" {
		return errors.New("commit message is required")
	}
	if opts.lastN < 0 {
		return errors.New("--last cannot be negative")
	}

	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "ingesting agent sessions...")
	sources, err := closeSources()
	if err != nil {
		return err
	}
	if err := ingestSessions(out, store, sources); err != nil {
		return err
	}

	chatID, _, err := resolveActiveChat(opts.chatFlag)
	if err != nil {
		return err
	}
	if chatID == "" {
		return errors.New("no active session detected; use --chat <id>")
	}
	if chat, err := store.GetChat(chatID); err != nil {
		return err
	} else if chat == nil {
		return fmt.Errorf("chat %s was not found after ingest", chatID)
	}
	if n, err := store.CountStaged(chatID); err != nil {
		return err
	} else if n > 0 {
		return fmt.Errorf("chat %s already has %d staged messages; run 'nem commit -m \"...\"' first or clear the staging before 'nem close'", chatID, n)
	}

	roles, err := resolveRoles(opts.role)
	if err != nil {
		return err
	}
	head, err := store.HeadCommit(chatID)
	if err != nil {
		return err
	}
	msgs, err := messagesAfterHead(store, chatID, head, roles)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		return errors.New("no new messages since HEAD")
	}
	if opts.lastN > 0 && len(msgs) > opts.lastN {
		return fmt.Errorf("refusing to close %d new messages with --last %d; rerun without --last to commit the full contiguous range", len(msgs), opts.lastN)
	}
	stagedNew, err := store.StageMessages(chatID, msgs)
	if err != nil {
		return err
	}
	staged, err := store.StagedMessages(chatID)
	if err != nil {
		return err
	}
	if len(staged) == 0 {
		return errors.New("nothing staged; cannot close session")
	}
	commit, err := commitInto(store, chatID, opts.message, config.UserName(), staged)
	if err != nil {
		return err
	}
	if err := store.ClearStaging(chatID); err != nil {
		return err
	}

	fmt.Fprintf(out, "staged %d new messages (total committed: %d)\n", stagedNew, len(staged))
	fmt.Fprintf(out, "commit %s: %q (%d messages) → personal\n", shortHash(commit.Hash), opts.message, len(staged))

	if !opts.noIndex {
		rep, err := buildIndex(store)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "indexed: %d projects, %d chats, %d commits (%d nodes)\n",
			rep.Projects, rep.Chats, rep.Commits, rep.Nodes)
		if rep.Embedded > 0 {
			fmt.Fprintf(out, "embedded: %d nodes\n", rep.Embedded)
		}
	}
	fmt.Fprintf(out, "closed %s at %s\n", chatID, shortHash(commit.Hash))
	return nil
}

func messagesAfterHead(store db.Store, chatID string, head *db.Commit, roles []string) ([]db.Message, error) {
	fromSeq := int64(1)
	if head != nil {
		msg, err := store.MessageByID(chatID, head.MsgTo)
		if err != nil {
			return nil, err
		}
		if msg == nil {
			return nil, fmt.Errorf("HEAD points to message %q, but it is missing from chat %s", head.MsgTo, chatID)
		}
		fromSeq = msg.Seq + 1
	}
	return store.MessagesBySeqRange(chatID, fromSeq, maxSeq, roles)
}

func buildIndex(store db.Store) (*index.Report, error) {
	opts, err := indexOpts("", "", "")
	if err != nil {
		return nil, err
	}
	b, err := index.New(store, opts...)
	if err != nil {
		return nil, err
	}
	return b.Build()
}

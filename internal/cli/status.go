package cli

import (
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Dieg0Code/nem/internal/db"
	"github.com/Dieg0Code/nem/internal/session"
	"github.com/spf13/cobra"
)

const maxSeq = math.MaxInt64

// newStatusCmd crea `nem status`: muestra la sesión activa y su estado.
func newStatusCmd() *cobra.Command {
	var (
		chatFlag     string
		recentWindow string
		noGit        bool
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the active session and uncommitted messages",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd, statusOpts{chatFlag: chatFlag, recentWindow: recentWindow, noGit: noGit})
		},
	}
	cmd.Flags().StringVar(&chatFlag, "chat", "", "chat id (default: detected session)")
	cmd.Flags().StringVar(&recentWindow, "recent-window", "2h", "lookback window for parallel agent sessions")
	cmd.Flags().BoolVar(&noGit, "no-git", false, "skip local git tag/release hints")
	return cmd
}

type statusOpts struct {
	chatFlag     string
	recentWindow string
	noGit        bool
}

func runStatus(cmd *cobra.Command, opts statusOpts) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	out := cmd.OutOrStdout()

	chatID, source, err := resolveActiveChat(opts.chatFlag)
	if err != nil {
		return err
	}
	if chatID == "" {
		fmt.Fprintln(out, "No active session detected (Codex/Claude/Antigravity/opencode).")
		fmt.Fprintln(out, "Open a session with your agent, or use 'nem status --chat <id>'.")
		return nil
	}

	chat, err := store.GetChat(chatID)
	if err != nil {
		return err
	}
	if chat == nil {
		fmt.Fprintf(out, "Active session detected (%s): %s\n", source, chatID)
		fmt.Fprintln(out, "Not ingested yet. Run 'nem ingest' to pull it in.")
		return nil
	}

	count, err := store.CountMessages(chatID)
	if err != nil {
		return err
	}
	staged, err := store.CountStaged(chatID)
	if err != nil {
		return err
	}
	head, err := store.HeadCommit(chatID)
	if err != nil {
		return err
	}

	title := chat.Title
	if title == "" {
		title = "(untitled)"
	}
	fmt.Fprintf(out, "Active session: %s · %s\n", chat.Source, title)
	fmt.Fprintf(out, "  chat:     %s\n", chat.ID)
	fmt.Fprintf(out, "  messages: %d\n", count)
	fmt.Fprintf(out, "  staged:   %d\n", staged)
	if head != nil {
		fmt.Fprintf(out, "  HEAD:     %s  %q\n", shortHash(head.Hash), head.Message)
	} else {
		fmt.Fprintf(out, "  HEAD:     (no commits)\n")
	}
	pending, err := messagesSinceHead(store, chatID, head)
	if err != nil {
		return err
	}
	if pending > 0 {
		fmt.Fprintf(out, "  uncommitted: %d messages since HEAD\n", pending)
	}
	if head == nil && count > 0 {
		fmt.Fprintln(out, "  hint:     run 'nem close -m \"...\"' to persist this session")
	}
	if !opts.noGit {
		if hint := releaseHint(store, chatID, head); hint != "" {
			fmt.Fprintf(out, "  release:  %s\n", hint)
		}
	}

	if activeScopeName(cmd) != "" {
		allowed, scoped, err := resolveScope(cmd, store)
		if err != nil {
			return err
		}
		if scoped {
			state := "active chat in scope"
			if !inScope(allowed, chat.ID) {
				state = "active chat OUT of scope"
			}
			fmt.Fprintf(out, "  scope:    %s (%s)\n", activeScopeName(cmd), state)
		}
	}
	if err := writeRecentSessions(out, chatID, opts.recentWindow); err != nil {
		return err
	}
	return nil
}

func messagesSinceHead(store db.Store, chatID string, head *db.Commit) (int, error) {
	if head == nil {
		n, err := store.CountMessages(chatID)
		return int(n), err
	}
	msg, err := store.MessageByID(chatID, head.MsgTo)
	if err != nil {
		return 0, err
	}
	if msg == nil {
		return 0, nil
	}
	msgs, err := store.MessagesBySeqRange(chatID, msg.Seq+1, maxSeq, nil)
	if err != nil {
		return 0, err
	}
	return len(msgs), nil
}

func latestMessageTime(store db.Store, chatID string) int64 {
	stamps, err := store.MessageStamps(chatID)
	if err != nil || len(stamps) == 0 {
		return 0
	}
	return stamps[len(stamps)-1].Timestamp
}

func releaseHint(store db.Store, chatID string, head *db.Commit) string {
	tagUnix, tag, err := latestGitTagTime()
	if err != nil || tagUnix == 0 {
		return ""
	}
	if latestMessageTime(store, chatID) <= tagUnix {
		return ""
	}
	if head != nil && head.CreatedAt >= tagUnix {
		return ""
	}
	return fmt.Sprintf("%s detected; consider 'nem close -m \"...\"'", tag)
}

func latestGitTagTime() (int64, string, error) {
	tagBytes, err := exec.Command("git", "describe", "--tags", "--abbrev=0").Output()
	if err != nil {
		return 0, "", err
	}
	tag := strings.TrimSpace(string(tagBytes))
	if tag == "" {
		return 0, "", nil
	}
	tsBytes, err := exec.Command("git", "log", "-1", "--format=%ct", tag).Output()
	if err != nil {
		return 0, "", err
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(string(tsBytes)), 10, 64)
	if err != nil {
		return 0, "", err
	}
	return ts, tag, nil
}

func writeRecentSessions(out interface{ Write([]byte) (int, error) }, activeChatID, windowFlag string) error {
	window, err := time.ParseDuration(windowFlag)
	if err != nil {
		return fmt.Errorf("bad --recent-window: %w", err)
	}
	if window <= 0 {
		return nil
	}
	recent, err := session.Recent(window)
	if err != nil {
		return err
	}
	shown := 0
	for _, s := range recent {
		if s.ChatID == "" || s.ChatID == activeChatID {
			continue
		}
		if shown == 0 {
			fmt.Fprintln(out, "  recent sessions:")
		}
		fmt.Fprintf(out, "    - %s · %s · %s\n", s.Source, s.ChatID, s.ModTime.Format(time.RFC3339))
		shown++
		if shown == 5 {
			break
		}
	}
	return nil
}

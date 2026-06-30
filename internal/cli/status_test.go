package cli

import (
	"strings"
	"testing"

	"github.com/Dieg0Code/nem/internal/db"
	"github.com/spf13/cobra"
)

func TestStatusShowsUncommittedMessagesSinceHead(t *testing.T) {
	t.Setenv("NEM_HOME", t.TempDir())
	seedChatMessages(t, "c1", []db.Message{
		{ID: "c1:1", Role: "user", Content: "first", Timestamp: 1700000001, Seq: 1},
		{ID: "c1:2", Role: "assistant", Content: "second", Timestamp: 1700000002, Seq: 2},
	})
	s, _ := openStore()
	first, _ := s.MessagesBySeqRange("c1", 1, 1, nil)
	if _, err := commitInto(s, "c1", "first checkpoint", "tester", first); err != nil {
		t.Fatal(err)
	}
	s.Close()

	out := runCmd(t, func(cmd *cobra.Command) error {
		return runStatus(cmd, statusOpts{chatFlag: "c1", recentWindow: "0s", noGit: true})
	})
	if !strings.Contains(out, "uncommitted: 1 messages since HEAD") {
		t.Fatalf("status output = %q, want uncommitted count", out)
	}
}

func TestStatusSuggestsCloseWhenNoHead(t *testing.T) {
	t.Setenv("NEM_HOME", t.TempDir())
	seedChatMessages(t, "c1", []db.Message{{ID: "c1:1", Role: "user", Content: "x", Timestamp: 1700000001, Seq: 1}})

	out := runCmd(t, func(cmd *cobra.Command) error {
		return runStatus(cmd, statusOpts{chatFlag: "c1", recentWindow: "0s", noGit: true})
	})
	if !strings.Contains(out, "hint:") || !strings.Contains(out, "nem close") {
		t.Fatalf("status output = %q, want close hint", out)
	}
}

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dieg0Code/nem/internal/db"
	"github.com/Dieg0Code/nem/internal/ingest"
	"github.com/spf13/cobra"
)

func seedChatMessages(t *testing.T, chatID string, msgs []db.Message) {
	t.Helper()
	dbPath := mustDBPath(t)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := db.New(db.WithPath(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertChat(&db.Chat{ID: chatID, Title: "nem", Source: "manual", CreatedAt: 1700000000}); err != nil {
		t.Fatal(err)
	}
	for i := range msgs {
		msgs[i].ChatID = chatID
		if msgs[i].Seq == 0 {
			msgs[i].Seq = int64(i + 1)
		}
		if msgs[i].ID == "" {
			msgs[i].ID = chatID + ":" + string(rune('1'+i))
		}
	}
	if _, err := s.InsertMessages(msgs); err != nil {
		t.Fatal(err)
	}
}

func TestCloseCommitsAndIndexes(t *testing.T) {
	t.Setenv("NEM_HOME", t.TempDir())
	seedChatMessages(t, "c1", []db.Message{
		{ID: "c1:1", Role: "user", Content: "ship release", Timestamp: 1700000001, Seq: 1},
		{ID: "c1:2", Role: "assistant", Content: "release shipped", Timestamp: 1700000002, Seq: 2},
	})

	oldSources := closeSources
	closeSources = func() ([]ingest.Source, error) { return nil, nil }
	t.Cleanup(func() { closeSources = oldSources })

	out := runCmd(t, func(cmd *cobra.Command) error {
		return runClose(cmd, closeOpts{message: "close release", chatFlag: "c1", lastN: 2})
	})
	if !strings.Contains(out, "closed c1") {
		t.Fatalf("close output = %q, want closed c1", out)
	}
	s, _ := openStore()
	defer s.Close()
	if staged, _ := s.CountStaged("c1"); staged != 0 {
		t.Fatalf("staged = %d, want 0", staged)
	}
	if head, _ := s.HeadCommit("c1"); head == nil || head.Message != "close release" {
		t.Fatalf("HEAD = %+v, want close release", head)
	}
	if nodes, _ := s.CountNodes(); nodes == 0 {
		t.Fatalf("nodes = 0, want index to run")
	}
}

func TestCloseNoIndex(t *testing.T) {
	t.Setenv("NEM_HOME", t.TempDir())
	seedChatMessages(t, "c1", []db.Message{{ID: "c1:1", Role: "user", Content: "x", Timestamp: 1700000001, Seq: 1}})

	oldSources := closeSources
	closeSources = func() ([]ingest.Source, error) { return nil, nil }
	t.Cleanup(func() { closeSources = oldSources })

	_ = runCmd(t, func(cmd *cobra.Command) error {
		return runClose(cmd, closeOpts{message: "close without index", chatFlag: "c1", lastN: 1, noIndex: true})
	})
	s, _ := openStore()
	defer s.Close()
	if nodes, _ := s.CountNodes(); nodes != 0 {
		t.Fatalf("nodes = %d, want 0 with --no-index", nodes)
	}
}

func TestCloseCommitsFullRangeWhenNoHead(t *testing.T) {
	t.Setenv("NEM_HOME", t.TempDir())
	var msgs []db.Message
	for i := 1; i <= 25; i++ {
		msgs = append(msgs, db.Message{
			ID: fmt.Sprintf("c1:%d", i), Role: "user", Content: "msg", Timestamp: int64(1700000000 + i), Seq: int64(i),
		})
	}
	seedChatMessages(t, "c1", msgs)

	oldSources := closeSources
	closeSources = func() ([]ingest.Source, error) { return nil, nil }
	t.Cleanup(func() { closeSources = oldSources })

	_ = runCmd(t, func(cmd *cobra.Command) error {
		return runClose(cmd, closeOpts{message: "close all", chatFlag: "c1"})
	})
	s, _ := openStore()
	defer s.Close()
	head, _ := s.HeadCommit("c1")
	if head == nil || head.MsgTo != "c1:25" {
		t.Fatalf("HEAD MsgTo = %v, want final message", head)
	}
	pending, err := messagesSinceHead(s, "c1", head)
	if err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("pending = %d, want 0", pending)
	}
}

func TestCloseRefusesWhenNoNewMessages(t *testing.T) {
	t.Setenv("NEM_HOME", t.TempDir())
	seedChatMessages(t, "c1", []db.Message{{ID: "c1:1", Role: "user", Content: "x", Timestamp: 1700000001, Seq: 1}})
	oldSources := closeSources
	closeSources = func() ([]ingest.Source, error) { return nil, nil }
	t.Cleanup(func() { closeSources = oldSources })

	_ = runCmd(t, func(cmd *cobra.Command) error {
		return runClose(cmd, closeOpts{message: "first", chatFlag: "c1"})
	})
	if err := runClose(&cobra.Command{}, closeOpts{message: "second", chatFlag: "c1"}); err == nil || !strings.Contains(err.Error(), "no new messages") {
		t.Fatalf("second close err = %v, want no new messages", err)
	}
}

func TestCloseRefusesPreExistingStaging(t *testing.T) {
	t.Setenv("NEM_HOME", t.TempDir())
	seedPersonalStaged(t, "c1")
	oldSources := closeSources
	closeSources = func() ([]ingest.Source, error) { return nil, nil }
	t.Cleanup(func() { closeSources = oldSources })

	err := runClose(&cobra.Command{}, closeOpts{message: "close", chatFlag: "c1"})
	if err == nil || !strings.Contains(err.Error(), "already has") {
		t.Fatalf("close err = %v, want pre-existing staging error", err)
	}
}

func TestCloseLastIsGuardrail(t *testing.T) {
	t.Setenv("NEM_HOME", t.TempDir())
	seedChatMessages(t, "c1", []db.Message{
		{ID: "c1:1", Role: "user", Content: "one", Timestamp: 1700000001, Seq: 1},
		{ID: "c1:2", Role: "user", Content: "two", Timestamp: 1700000002, Seq: 2},
	})
	oldSources := closeSources
	closeSources = func() ([]ingest.Source, error) { return nil, nil }
	t.Cleanup(func() { closeSources = oldSources })

	err := runClose(&cobra.Command{}, closeOpts{message: "close", chatFlag: "c1", lastN: 1})
	if err == nil || !strings.Contains(err.Error(), "refusing to close 2 new messages") {
		t.Fatalf("close err = %v, want guardrail error", err)
	}
}

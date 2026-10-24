package db

import (
	"context"
	"path/filepath"
	"testing"
)

// newTestStore abre un Store sobre un archivo temporal ya migrado.
func newTestStore(t *testing.T) *store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nem.db")
	s, err := New(WithPath(path))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s.(*store)
}

func TestNew_RequiresPath(t *testing.T) {
	if _, err := New(); err == nil {
		t.Fatal("New() without path: expected error, got nil")
	}
}

func TestMigrate_CreatesSchema(t *testing.T) {
	s := newTestStore(t)

	// Migrate debe ser idempotente.
	if err := s.Migrate(); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}

	want := []string{"chats", "messages", "commits", "stagings", "memories", "messages_fts"}
	for _, table := range want {
		var count int64
		err := s.gdb.Raw(
			"SELECT count(*) FROM sqlite_master WHERE name = ?", table,
		).Scan(&count).Error
		if err != nil {
			t.Fatalf("query sqlite_master for %q: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %q: expected to exist, got count %d", table, count)
		}
	}
}

func TestFTS_TriggerSyncAndBM25(t *testing.T) {
	s := newTestStore(t)

	if err := s.gdb.Create(&Chat{ID: "c1", Source: "manual"}).Error; err != nil {
		t.Fatalf("create chat: %v", err)
	}
	msgs := []Message{
		{ID: "m1", ChatID: "c1", Role: "user", Content: "necesito implementar decay para jugadores inactivos", Seq: 1},
		{ID: "m2", ChatID: "c1", Role: "assistant", Content: "aplicalo en el cron de ratings", Seq: 2},
		{ID: "m3", ChatID: "c1", Role: "user", Content: "skip the decay para ese edge case", Seq: 3},
	}
	if err := s.gdb.Create(&msgs).Error; err != nil {
		t.Fatalf("create messages: %v", err)
	}

	tests := []struct {
		name  string
		query string
		want  []string // message_id esperados, en orden BM25
	}{
		{name: "term presente en dos mensajes", query: "decay", want: []string{"m1", "m3"}},
		{name: "term presente en uno", query: "ratings", want: []string{"m2"}},
		{name: "term ausente", query: "kubernetes", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := s.gdb.Raw(
				`SELECT message_id FROM messages_fts
				 WHERE messages_fts MATCH ?
				 ORDER BY bm25(messages_fts)`, tt.query,
			).Rows()
			if err != nil {
				t.Fatalf("fts query: %v", err)
			}
			defer rows.Close()

			var got []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					t.Fatalf("scan: %v", err)
				}
				got = append(got, id)
			}

			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("position %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestListChats(t *testing.T) {
	s := newTestStore(t)
	for _, c := range []Chat{
		{ID: "a", Title: "ataxx", Source: "codex", CreatedAt: 1},
		{ID: "b", Title: "teach", Source: "claude", CreatedAt: 2},
	} {
		if err := s.UpsertChat(&c); err != nil {
			t.Fatal(err)
		}
	}
	chats, err := s.ListChats()
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(chats) != 2 {
		t.Fatalf("got %d chats, want 2", len(chats))
	}
}

func TestSearchMessages_ChatFilter(t *testing.T) {
	s := newTestStore(t)
	for _, c := range []Chat{{ID: "c1", Source: "codex"}, {ID: "c2", Source: "claude"}} {
		if err := s.UpsertChat(&c); err != nil {
			t.Fatal(err)
		}
	}
	msgs := []Message{
		{ID: "c1:1", ChatID: "c1", Role: "assistant", Content: "decay logic here", Seq: 1},
		{ID: "c2:1", ChatID: "c2", Role: "assistant", Content: "decay logic there", Seq: 1},
	}
	if _, err := s.InsertMessages(msgs); err != nil {
		t.Fatal(err)
	}

	// Sin filtro de chat: ambos.
	all, err := s.SearchMessages("decay", 10, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("no filter: got %d hits, want 2", len(all))
	}

	// Filtrado a c1: solo uno.
	scoped, err := s.SearchMessages("decay", 10, nil, []string{"c1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 1 || scoped[0].ChatID != "c1" {
		t.Fatalf("chat filter: got %+v, want only c1", scoped)
	}
}

func TestLastMessages_RoleFilter(t *testing.T) {
	s := newTestStore(t)
	if err := s.gdb.Create(&Chat{ID: "c1", Source: "manual"}).Error; err != nil {
		t.Fatal(err)
	}
	msgs := []Message{
		{ID: "m1", ChatID: "c1", Role: "user", Content: "u1", Seq: 1},
		{ID: "m2", ChatID: "c1", Role: "assistant", Content: "a1", Seq: 2},
		{ID: "m3", ChatID: "c1", Role: "tool", Content: "t1", Seq: 3},
		{ID: "m4", ChatID: "c1", Role: "assistant", Content: "a2", Seq: 4},
		{ID: "m5", ChatID: "c1", Role: "reasoning", Content: "r1", Seq: 5},
	}
	if _, err := s.InsertMessages(msgs); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		n     int
		roles []string
		want  []string // ids esperados, en orden ascendente
	}{
		{"all roles, last 2", 2, nil, []string{"m4", "m5"}},
		{"only assistant", 5, []string{"assistant"}, []string{"m2", "m4"}},
		{"conversation+reasoning excludes tool", 5, []string{"user", "assistant", "reasoning"}, []string{"m1", "m2", "m4", "m5"}},
		{"last 1 assistant", 1, []string{"assistant"}, []string{"m4"}},
		{"only tool", 5, []string{"tool"}, []string{"m3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.LastMessages("c1", tt.n, tt.roles)
			if err != nil {
				t.Fatalf("LastMessages: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d msgs, want %d: %+v", len(got), len(tt.want), ids(got))
			}
			for i := range tt.want {
				if got[i].ID != tt.want[i] {
					t.Errorf("pos %d: got %q, want %q", i, got[i].ID, tt.want[i])
				}
			}
		})
	}
}

func ids(msgs []Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.ID
	}
	return out
}

func TestFTS_TriggerOnDelete(t *testing.T) {
	s := newTestStore(t)

	if err := s.gdb.Create(&Chat{ID: "c1", Source: "manual"}).Error; err != nil {
		t.Fatalf("create chat: %v", err)
	}
	if err := s.gdb.Create(&Message{ID: "m1", ChatID: "c1", Content: "decay logic", Seq: 1}).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}
	if err := s.gdb.Delete(&Message{}, "id = ?", "m1").Error; err != nil {
		t.Fatalf("delete message: %v", err)
	}

	var count int64
	if err := s.gdb.Raw(
		"SELECT count(*) FROM messages_fts WHERE message_id = ?", "m1",
	).Scan(&count).Error; err != nil {
		t.Fatalf("count fts: %v", err)
	}
	if count != 0 {
		t.Errorf("after delete: expected 0 fts rows, got %d", count)
	}
}

func TestNew_PragmasOnEveryConnection(t *testing.T) {
	s := newTestStore(t)
	sqlDB, err := s.gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	// Dos conexiones simultáneas obligan al pool a abrir una segunda: los
	// pragmas deben valer en ambas, no solo en la primera.
	ctx := context.Background()
	for i := range 2 {
		conn, err := sqlDB.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		var fk int
		var mmap int64
		if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRowContext(ctx, "PRAGMA mmap_size").Scan(&mmap); err != nil {
			t.Fatal(err)
		}
		if fk != 1 || mmap == 0 {
			t.Errorf("conn %d: foreign_keys=%d mmap_size=%d, want 1 and >0", i, fk, mmap)
		}
	}
}

package retrieve

import "testing"

func ids(s []Scored) []string {
	out := make([]string, len(s))
	for i, x := range s {
		out[i] = x.ID
	}
	return out
}

func msg(id string, ts int64) Item {
	return Item{Kind: "message", ID: id, Timestamp: ts}
}

func TestFuse_SingleChannelKeepsOrder(t *testing.T) {
	ch := Channel{Name: "bm25", Items: []Item{msg("a", 0), msg("b", 0), msg("c", 0)}}
	got := ids(Fuse([]Channel{ch}, 0, WithRecencyWeight(0)))
	want := []string{"a", "b", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestFuse_CrossChannelReinforcement(t *testing.T) {
	// "x" aparece en ambos canales (aunque en rank bajo) → debe ganarle a items
	// que están primeros en un solo canal.
	c1 := Channel{Name: "bm25", Items: []Item{msg("a", 0), msg("b", 0), msg("x", 0)}}
	c2 := Channel{Name: "nodes", Items: []Item{msg("c", 0), msg("d", 0), msg("x", 0)}}
	got := Fuse([]Channel{c1, c2}, 0, WithRecencyWeight(0))
	if got[0].ID != "x" {
		t.Errorf("expected 'x' first (reinforced across channels), got %v", ids(got))
	}
}

func TestFuse_RecencyTiebreak(t *testing.T) {
	// Mismo rank en canales separados → mismo RRF; el más nuevo gana por recencia.
	c1 := Channel{Name: "a", Items: []Item{msg("old", 1000)}}
	c2 := Channel{Name: "b", Items: []Item{msg("new", 2000)}}
	got := Fuse([]Channel{c1, c2}, 0, WithRecencyWeight(0.1))
	if got[0].ID != "new" {
		t.Errorf("expected 'new' first by recency, got %v", ids(got))
	}
}

func TestFuse_Limit(t *testing.T) {
	ch := Channel{Name: "x", Items: []Item{msg("a", 0), msg("b", 0), msg("c", 0)}}
	got := Fuse([]Channel{ch}, 2)
	if len(got) != 2 {
		t.Fatalf("limit: got %d, want 2", len(got))
	}
}

func TestFuse_Empty(t *testing.T) {
	if got := Fuse(nil, 5); got != nil {
		t.Errorf("empty: got %v, want nil", got)
	}
}

func TestFuse_PreservesStoreID(t *testing.T) {
	// La lectura federada estampa StoreID por origen; Fuse debe preservarlo en cada
	// resultado para que el listado pueda etiquetar de dónde vino.
	personal := Item{Kind: "message", ID: "p1", StoreID: ""}
	team := Item{Kind: "message", ID: "t1", StoreID: "acme"}
	got := Fuse([]Channel{
		{Name: "store:", Items: []Item{personal}},
		{Name: "store:acme", Items: []Item{team}},
	}, 0, WithRecencyWeight(0))

	byID := map[string]string{}
	for _, r := range got {
		byID[r.ID] = r.StoreID
	}
	if byID["p1"] != "" {
		t.Errorf("p1 StoreID = %q, want personal (empty)", byID["p1"])
	}
	if byID["t1"] != "acme" {
		t.Errorf("t1 StoreID = %q, want acme", byID["t1"])
	}
}

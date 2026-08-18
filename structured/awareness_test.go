package structured

import (
	"testing"

	"github.com/go-crdt/crdt/awareness"
)

// A cell selection published by one replica reaches another through the ordinary
// awareness channel and decodes to the same cell.
func TestCellSelectionRidesAlong(t *testing.T) {
	pub, sub := awareness.New(), awareness.New()
	row := RowID{Site: 1, Seq: 3}
	col := ColID{Site: 2, Seq: 4}
	update := PublishCellSelection(pub, 1, row, col, map[string]string{"name": "Ada"})
	if !sub.Apply(update) {
		t.Fatal("Apply of a fresh selection changed nothing")
	}
	peers := sub.Peers()
	if len(peers) != 1 {
		t.Fatalf("Peers = %d, want 1", len(peers))
	}
	p := peers[0]
	if p.Meta["name"] != "Ada" {
		t.Fatalf("presentation metadata lost: %v", p.Meta)
	}
	gotRow, gotCol, ok := CellSelectionOf(p)
	if !ok || gotRow != row || gotCol != col {
		t.Fatalf("CellSelectionOf = %v,%v,%v; want %v,%v", gotRow, gotCol, ok, row, col)
	}
}

// A node selection rides along the same way.
func TestNodeSelectionRidesAlong(t *testing.T) {
	pub, sub := awareness.New(), awareness.New()
	node := NodeID{Site: 5, Seq: 6}
	update := PublishNodeSelection(pub, 1, node, nil)
	sub.Apply(update)
	p := sub.Peers()[0]
	got, ok := NodeSelectionOf(p)
	if !ok || got != node {
		t.Fatalf("NodeSelectionOf = %v,%v; want %v", got, ok, node)
	}
}

func TestSelectionAbsentOrMalformed(t *testing.T) {
	// A peer with no selection metadata reports not-ok for both readers.
	bare := awareness.Peer{Site: 1}
	if _, _, ok := CellSelectionOf(bare); ok {
		t.Fatal("CellSelectionOf on a peer with no selection = ok")
	}
	if _, ok := NodeSelectionOf(bare); ok {
		t.Fatal("NodeSelectionOf on a peer with no selection = ok")
	}
	// A selection present but not one this package encodes reports not-ok.
	junk := awareness.Peer{Site: 1, Meta: map[string]string{SelectionMetaKey: "not-a-coordinate"}}
	if _, _, ok := CellSelectionOf(junk); ok {
		t.Fatal("CellSelectionOf on junk = ok")
	}
	if _, ok := NodeSelectionOf(junk); ok {
		t.Fatal("NodeSelectionOf on junk = ok")
	}
}

// Publishing a selection must not mutate the caller's metadata map, and a
// caller's own selection key is overridden by the real selection.
func TestPublishDoesNotMutateCallerMeta(t *testing.T) {
	reg := awareness.New()
	meta := map[string]string{"name": "Grace", SelectionMetaKey: "stale"}
	row := RowID{Site: 1, Seq: 1}
	col := ColID{Site: 1, Seq: 1}
	PublishCellSelection(reg, 1, row, col, meta)
	if meta[SelectionMetaKey] != "stale" {
		t.Fatalf("the caller's map was mutated: %v", meta)
	}
	p := reg.Peers()[0]
	gotRow, gotCol, ok := CellSelectionOf(p)
	if !ok || gotRow != row || gotCol != col {
		t.Fatalf("published selection = %v,%v,%v, want the real one", gotRow, gotCol, ok)
	}
}

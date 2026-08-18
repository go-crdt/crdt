package structured

import (
	"github.com/go-crdt/crdt"
	"github.com/go-crdt/crdt/awareness"
)

// Presence for a structured document is a cell or a node, not a text offset, so
// it rides in an awareness update's metadata rather than in its cursor. The
// awareness package stays exactly as it is: this is the one convention on top of
// it — a single metadata key holding the selected coordinate, encoded the same
// way the document keys it — so that where a peer is pointing travels with who
// the peer is, on the ephemeral channel presence already uses.

// SelectionMetaKey is the metadata key a structured selection travels under.
const SelectionMetaKey = "sel"

// withSelection returns a copy of meta carrying sel, without mutating the
// caller's map. A caller's own SelectionMetaKey entry is overwritten: the
// selection is the authority here.
func withSelection(meta map[string]string, sel string) map[string]string {
	out := make(map[string]string, len(meta)+1)
	for k, v := range meta {
		out[k] = v
	}
	out[SelectionMetaKey] = sel
	return out
}

// PublishCellSelection records that this replica has the cell (row, col)
// selected and returns the awareness update to broadcast. meta carries any
// presentation details — a display name, a colour — alongside the selection.
func PublishCellSelection(reg *awareness.Registry, site crdt.SiteID, row RowID, col ColID, meta map[string]string) awareness.Update {
	return reg.Publish(site, awareness.Cursor{}, withSelection(meta, cellKey(row, col)))
}

// CellSelectionOf returns the cell a peer has selected, and whether it has a
// valid one. A peer publishing a selection this package did not encode — or none
// — reports not ok.
func CellSelectionOf(p awareness.Peer) (RowID, ColID, bool) {
	sel, held := p.Meta[SelectionMetaKey]
	if !held {
		return RowID{}, ColID{}, false
	}
	return decodeCellKey(sel)
}

// PublishNodeSelection records that this replica has node selected and returns
// the awareness update to broadcast.
func PublishNodeSelection(reg *awareness.Registry, site crdt.SiteID, node NodeID, meta map[string]string) awareness.Update {
	return reg.Publish(site, awareness.Cursor{}, withSelection(meta, encodeID(crdt.ID(node))))
}

// NodeSelectionOf returns the node a peer has selected, and whether it has a
// valid one.
func NodeSelectionOf(p awareness.Peer) (NodeID, bool) {
	sel, held := p.Meta[SelectionMetaKey]
	if !held {
		return NodeID{}, false
	}
	id, ok := decodeID(sel)
	if !ok {
		return NodeID{}, false
	}
	return NodeID(id), true
}

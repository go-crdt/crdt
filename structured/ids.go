package structured

import "github.com/go-crdt/crdt"

// A RowID names a row for as long as the row exists, whatever concurrent edits
// do to the rows around it. It is a [crdt.List] element identity minted by the
// RGA, so it is unique across replicas, reload-safe, and never reused — the
// property a formula reference depends on.
type RowID crdt.ID

// A ColID names a column, on the same terms as a [RowID].
type ColID crdt.ID

// A NodeID names a diagram node, on the same terms as a [RowID]. A node's fields
// are stored against it, and a connector's endpoints reference it.
type NodeID crdt.ID

// A ConnID names a diagram connector, on the same terms as a [RowID].
type ConnID crdt.ID

// String renders each identity as the "seq@site" notation the crdt package uses.
func (r RowID) String() string  { return crdt.ID(r).String() }
func (c ColID) String() string  { return crdt.ID(c).String() }
func (n NodeID) String() string { return crdt.ID(n).String() }
func (c ConnID) String() string { return crdt.ID(c).String() }

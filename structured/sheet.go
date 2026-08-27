package structured

import "github.com/go-crdt/crdt"

// The three parts a sheet is made of. Their names are constant and valid, which
// is why every place that reaches for one discards the error [crdt.Composite]
// returns for an invalid name: it cannot happen here.
var (
	rowsPart  = crdt.Part{Kind: crdt.PartMap, Name: "rows"}
	colsPart  = crdt.Part{Kind: crdt.PartMap, Name: "cols"}
	cellsPart = crdt.Part{Kind: crdt.PartMap, Name: "cells"}
)

// A Sheet is a collaborative spreadsheet: rows and columns are two ordered
// collections of stable identities, and cells are a map keyed by a (row, column)
// identity pair. Editing it produces operations that any number of replicas may exchange,
// offline and in any order, and every replica converges to the same sheet.
//
// It is one [crdt.Composite] — the same core a [Diagram] is — so it borrows the
// crdt package's convergence rather than restating it. What this type adds is the
// addressing: a cell named by the identities of its row and column, so that a
// concurrent change to the sheet's shape leaves every other cell, and every
// formula reference, exactly where it was.
//
// # Why the axes are sequences and not lists
//
// They were [crdt.List] — an RGA — and a row could then be added and removed but
// not moved. Dragging a row to another place had to be written as a delete and
// an insert, which is two operations, and a second replica dragging the same row
// at the same time splits them: the row ends up twice over, or not at all, and
// its cells follow whichever copy survives.
//
// [Sequence] carries a row's place as a rank, so moving one is a single write
// and two replicas moving the same row are two writes to one field — a conflict
// [crdt.Map] already settles. The identity is untouched by a move, so the cells
// and every formula reference come with it for free.
//
// A Sheet is not safe for concurrent use. Construct one with [NewSheet] or
// [LoadSheet].
type Sheet struct {
	doc   *crdt.Composite
	rows  *Sequence
	cols  *Sequence
	cells *crdt.Map
}

// bind returns the three parts of a composite, creating any that a fresh
// document lacks. The names are constant and valid, so the errors are impossible
// and discarded.
func bind(doc *crdt.Composite) *Sheet {
	rows, _ := doc.Map(rowsPart.Name)
	cols, _ := doc.Map(colsPart.Name)
	cells, _ := doc.Map(cellsPart.Name)
	return &Sheet{doc: doc, rows: SequenceOf(rows), cols: SequenceOf(cols), cells: cells}
}

// NewSheet returns an empty sheet that issues operations as site. Every replica
// editing a sheet concurrently must pass a distinct [crdt.SiteID].
func NewSheet(site crdt.SiteID) *Sheet { return bind(crdt.NewComposite(site)) }

// LoadSheet rebuilds a sheet from a snapshot, to be edited as site.
func LoadSheet(site crdt.SiteID, snapshot []byte) (*Sheet, error) {
	doc, err := crdt.LoadComposite(site, snapshot)
	if err != nil {
		return nil, err
	}
	return bind(doc), nil
}

// Site returns the replica identity this sheet issues operations as.
func (s *Sheet) Site() crdt.SiteID { return s.doc.Site() }

// Snapshot encodes the whole sheet, for a joining peer or for persistence. Two
// replicas holding the same operations produce identical bytes.
func (s *Sheet) Snapshot() []byte { return s.doc.Snapshot() }

// Version returns what this replica holds, to hand a peer that will send back
// what it is missing; see [Sheet.OpsSince].
func (s *Sheet) Version() crdt.CompositeVersion { return s.doc.Version() }

// OpsSince returns the operations this replica holds that v does not, batched by
// part and ready to send to the peer that produced v. Pass a nil version for
// everything.
func (s *Sheet) OpsSince(v crdt.CompositeVersion) ([]crdt.PartOps, error) {
	return s.doc.OpsSince(v)
}

// Apply integrates batches of operations from peers, tolerating duplicates and
// reordering exactly as the underlying parts do.
func (s *Sheet) Apply(batches ...crdt.PartOps) error { return s.doc.Apply(batches...) }

// Pending reports how many received operations are still waiting, across every
// part, for the operations they depend on.
func (s *Sheet) Pending() int { return s.doc.Pending() }

// InsertRow adds a row at index pos and returns its identity and the operations
// to broadcast. pos may equal [Sheet.RowCount], which appends.
func (s *Sheet) InsertRow(pos int) (RowID, crdt.PartOps, error) {
	item, ops, err := insertOnAxis(s.rows, pos)
	if err != nil {
		return RowID{}, crdt.PartOps{}, err
	}
	return RowID(item), crdt.PartOps{Part: rowsPart, Map: ops}, nil
}

// AppendRow adds a row after the last and returns its identity and the
// operations to broadcast.
func (s *Sheet) AppendRow() (RowID, crdt.PartOps, error) { return s.InsertRow(s.rows.Len()) }

// InsertCol adds a column at index pos and returns its identity and the
// operations to broadcast.
func (s *Sheet) InsertCol(pos int) (ColID, crdt.PartOps, error) {
	item, ops, err := insertOnAxis(s.cols, pos)
	if err != nil {
		return ColID{}, crdt.PartOps{}, err
	}
	return ColID(item), crdt.PartOps{Part: colsPart, Map: ops}, nil
}

// AppendCol adds a column after the last and returns its identity and the
// operations to broadcast.
func (s *Sheet) AppendCol() (ColID, crdt.PartOps, error) { return s.InsertCol(s.cols.Len()) }

// insertOnAxis adds an identity at a position on one of the two axes. A row has
// no value of its own — it is the identity — so nothing is written but its
// place.
func insertOnAxis(axis *Sequence, pos int) (crdt.ID, []crdt.MapOp, error) {
	// Inserting names a gap rather than a thing, and there is one more gap than
	// there are things: pos may be the length, which appends, and the gap
	// before the first is named by the identity of nothing.
	if pos < 0 || pos > axis.Len() {
		return crdt.ID{}, nil, crdt.ErrOutOfRange
	}
	after := SeqStart
	if pos > 0 {
		after, _ = axis.At(pos - 1)
	}
	item, ops, err := axis.Insert(after, nil)
	return crdt.ID(item), ops, err
}

// axisAt turns a position into the identity there. Moving and deleting name a
// thing rather than a gap, so unlike inserting they have no position before the
// first and none at the end.
func axisAt(axis *Sequence, pos int) (ItemID, error) {
	item, ok := axis.At(pos)
	if !ok {
		return ItemID{}, crdt.ErrOutOfRange
	}
	return item, nil
}

// MoveRow moves the row at index from to index to, and returns the operation to
// broadcast. It is one operation: a row's place is one field.
//
// The row keeps its identity, so its cells and every formula naming them come
// with it and nothing else in the sheet moves.
func (s *Sheet) MoveRow(from, to int) (crdt.PartOps, error) {
	op, err := moveOnAxis(s.rows, from, to)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: rowsPart, Map: []crdt.MapOp{op}}, nil
}

// MoveCol moves the column at index from to index to, on the same terms as
// [Sheet.MoveRow].
func (s *Sheet) MoveCol(from, to int) (crdt.PartOps, error) {
	op, err := moveOnAxis(s.cols, from, to)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: colsPart, Map: []crdt.MapOp{op}}, nil
}

// moveOnAxis moves one identity from one position to another, reading both
// positions as the sheet reads now: to is where the thing ends up, so moving
// something later along the axis lands it after what currently sits there, and
// moving it earlier lands it before.
func moveOnAxis(axis *Sequence, from, to int) (crdt.MapOp, error) {
	moving, err := axisAt(axis, from)
	if err != nil {
		return crdt.MapOp{}, err
	}
	if _, err := axisAt(axis, to); err != nil {
		return crdt.MapOp{}, err
	}
	if from == to {
		return crdt.MapOp{}, ErrNoChange
	}
	after := SeqStart
	if to > from {
		after, _ = axisAt(axis, to)
	} else if to > 0 {
		after, _ = axisAt(axis, to-1)
	}
	return axis.Move(moving, after)
}

// DeleteRow removes the row at index pos and returns the operations to
// broadcast. The cells of a removed row are left in place, unreferenced by any
// live row, so they neither show through [Sheet.Rows] nor cost a second batch to
// remove.
func (s *Sheet) DeleteRow(pos int) (crdt.PartOps, error) {
	ops, err := deleteOnAxis(s.rows, pos)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: rowsPart, Map: ops}, nil
}

// DeleteCol removes the column at index pos and returns the operations to
// broadcast, on the same terms as [Sheet.DeleteRow].
func (s *Sheet) DeleteCol(pos int) (crdt.PartOps, error) {
	ops, err := deleteOnAxis(s.cols, pos)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: colsPart, Map: ops}, nil
}

func deleteOnAxis(axis *Sequence, pos int) ([]crdt.MapOp, error) {
	item, err := axisAt(axis, pos)
	if err != nil {
		return nil, err
	}
	return axis.Remove(item)
}

// RowCount returns the number of rows present.
func (s *Sheet) RowCount() int { return s.rows.Len() }

// ColCount returns the number of columns present.
func (s *Sheet) ColCount() int { return s.cols.Len() }

// Rows returns the identities of the rows present, in order. Two replicas
// holding the same operations return the same slice.
func (s *Sheet) Rows() []RowID {
	items := s.rows.Items()
	out := make([]RowID, 0, len(items))
	for _, item := range items {
		out = append(out, RowID(item))
	}
	return out
}

// Cols returns the identities of the columns present, in order.
func (s *Sheet) Cols() []ColID {
	items := s.cols.Items()
	out := make([]ColID, 0, len(items))
	for _, item := range items {
		out = append(out, ColID(item))
	}
	return out
}

// SetCell writes cell at the intersection of row and col and returns the
// operation to broadcast. The cell is addressed by identity, so the write lands
// on the same cell on every replica however the sheet's shape has changed.
func (s *Sheet) SetCell(row RowID, col ColID, cell Cell) (crdt.PartOps, error) {
	op, err := s.cells.Set(cellKey(row, col), cell.encode())
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: cellsPart, Map: []crdt.MapOp{op}}, nil
}

// GetCell returns the cell at the intersection of row and col and whether one is
// set. A cell whose stored bytes are not a cell this package wrote — which a peer
// may inject — reads as absent.
func (s *Sheet) GetCell(row RowID, col ColID) (Cell, bool) {
	value, ok := s.cells.Get(cellKey(row, col))
	if !ok {
		return Cell{}, false
	}
	return decodeCell(value)
}

// ClearCell removes the cell at the intersection of row and col and returns the
// operation to broadcast.
func (s *Sheet) ClearCell(row RowID, col ColID) (crdt.PartOps, error) {
	op, err := s.cells.Delete(cellKey(row, col))
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: cellsPart, Map: []crdt.MapOp{op}}, nil
}

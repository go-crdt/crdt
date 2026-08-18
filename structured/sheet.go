package structured

import "github.com/go-crdt/crdt"

// The three parts a sheet is made of. Their names are constant and valid, which
// is why every place that reaches for one discards the error [crdt.Composite]
// returns for an invalid name: it cannot happen here.
var (
	rowsPart  = crdt.Part{Kind: crdt.PartList, Name: "rows"}
	colsPart  = crdt.Part{Kind: crdt.PartList, Name: "cols"}
	cellsPart = crdt.Part{Kind: crdt.PartMap, Name: "cells"}
)

// axisMark is the one-byte value a row or column element carries. A list value
// must not be empty, and a row's identity is the element, not its value, so the
// value is a single constant byte carrying nothing.
const axisMark = 1

// A Sheet is a collaborative spreadsheet: rows and columns are two ordered lists
// of stable identities, and cells are a map keyed by a (row, column) identity
// pair. Editing it produces operations that any number of replicas may exchange,
// offline and in any order, and every replica converges to the same sheet.
//
// It is one [crdt.Composite] — the same core a [Diagram] is — so it borrows the
// crdt package's convergence rather than restating it. What this type adds is the
// addressing: a cell named by the identities of its row and column, so that a
// concurrent change to the sheet's shape leaves every other cell, and every
// formula reference, exactly where it was.
//
// A Sheet is not safe for concurrent use. Construct one with [NewSheet] or
// [LoadSheet].
type Sheet struct {
	doc   *crdt.Composite
	rows  *crdt.List
	cols  *crdt.List
	cells *crdt.Map
}

// bind returns the three parts of a composite, creating any that a fresh
// document lacks. The names are constant and valid, so the errors are impossible
// and discarded.
func bind(doc *crdt.Composite) *Sheet {
	rows, _ := doc.List(rowsPart.Name)
	cols, _ := doc.List(colsPart.Name)
	cells, _ := doc.Map(cellsPart.Name)
	return &Sheet{doc: doc, rows: rows, cols: cols, cells: cells}
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
func (s *Sheet) OpsSince(v crdt.CompositeVersion) []crdt.PartOps { return s.doc.OpsSince(v) }

// Apply integrates batches of operations from peers, tolerating duplicates and
// reordering exactly as the underlying parts do.
func (s *Sheet) Apply(batches ...crdt.PartOps) error { return s.doc.Apply(batches...) }

// Pending reports how many received operations are still waiting, across every
// part, for the operations they depend on.
func (s *Sheet) Pending() int { return s.doc.Pending() }

// InsertRow adds a row at index pos and returns its identity and the operation
// to broadcast. pos may equal [Sheet.RowCount], which appends.
func (s *Sheet) InsertRow(pos int) (RowID, crdt.PartOps, error) {
	ops, err := s.rows.Insert(pos, []byte{axisMark})
	if err != nil {
		return RowID{}, crdt.PartOps{}, err
	}
	return RowID(ops[0].ID), crdt.PartOps{Part: rowsPart, List: ops}, nil
}

// AppendRow adds a row after the last and returns its identity and the operation
// to broadcast.
func (s *Sheet) AppendRow() (RowID, crdt.PartOps, error) { return s.InsertRow(s.rows.Len()) }

// InsertCol adds a column at index pos and returns its identity and the
// operation to broadcast.
func (s *Sheet) InsertCol(pos int) (ColID, crdt.PartOps, error) {
	ops, err := s.cols.Insert(pos, []byte{axisMark})
	if err != nil {
		return ColID{}, crdt.PartOps{}, err
	}
	return ColID(ops[0].ID), crdt.PartOps{Part: colsPart, List: ops}, nil
}

// AppendCol adds a column after the last and returns its identity and the
// operation to broadcast.
func (s *Sheet) AppendCol() (ColID, crdt.PartOps, error) { return s.InsertCol(s.cols.Len()) }

// DeleteRow removes the row at index pos and returns the operation to broadcast.
// The cells of a removed row are left in place, unreferenced by any live row, so
// they neither show through [Sheet.Rows] nor cost a second batch to remove.
func (s *Sheet) DeleteRow(pos int) (crdt.PartOps, error) {
	ops, err := s.rows.Delete(pos, 1)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: rowsPart, List: ops}, nil
}

// DeleteCol removes the column at index pos and returns the operation to
// broadcast, on the same terms as [Sheet.DeleteRow].
func (s *Sheet) DeleteCol(pos int) (crdt.PartOps, error) {
	ops, err := s.cols.Delete(pos, 1)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: colsPart, List: ops}, nil
}

// RowCount returns the number of rows present.
func (s *Sheet) RowCount() int { return s.rows.Len() }

// ColCount returns the number of columns present.
func (s *Sheet) ColCount() int { return s.cols.Len() }

// Rows returns the identities of the rows present, in order. Two replicas
// holding the same operations return the same slice.
func (s *Sheet) Rows() []RowID {
	out := make([]RowID, 0, s.rows.Len())
	for i := range s.rows.Len() {
		id, _ := s.rows.Anchor(i) // i is in range, so the error cannot occur
		out = append(out, RowID(id))
	}
	return out
}

// Cols returns the identities of the columns present, in order.
func (s *Sheet) Cols() []ColID {
	out := make([]ColID, 0, s.cols.Len())
	for i := range s.cols.Len() {
		id, _ := s.cols.Anchor(i)
		out = append(out, ColID(id))
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

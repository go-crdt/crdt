package structured

import (
	"errors"
	"reflect"
	"testing"

	"github.com/go-crdt/crdt"
)

// applyAll feeds every batch to a sheet, failing the test on any error.
func applyAll(t *testing.T, s *Sheet, batches ...crdt.PartOps) {
	t.Helper()
	if err := s.Apply(batches...); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

// mustRow and mustCol add one, discarding the operations: the tests below that
// use them are about what the sheet reads, not about what it broadcasts.
func mustRow(t *testing.T, s *Sheet) RowID {
	t.Helper()
	id, _, err := s.AppendRow()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustCol(t *testing.T, s *Sheet) ColID {
	t.Helper()
	id, _, err := s.AppendCol()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// baseSheet builds two synchronised replicas with rows r0,r1 and column c0, and
// returns them together with those identities.
func baseSheet(t *testing.T) (a, b *Sheet, r0, r1 RowID, c0 ColID) {
	t.Helper()
	a, b = NewSheet(1), NewSheet(2)
	var batches []crdt.PartOps
	r0, batch, err := a.AppendRow()
	if err != nil {
		t.Fatal(err)
	}
	batches = append(batches, batch)
	r1, batch, err = a.AppendRow()
	if err != nil {
		t.Fatal(err)
	}
	batches = append(batches, batch)
	c0, batch, err = a.AppendCol()
	if err != nil {
		t.Fatal(err)
	}
	batches = append(batches, batch)
	applyAll(t, b, batches...)
	return a, b, r0, r1, c0
}

// Two replicas write two different cells at once: both writes survive, because
// the cells are two different map keys that never interact.
func TestSheetDifferentCellsBothSurvive(t *testing.T) {
	a, b, r0, r1, c0 := baseSheet(t)
	fromA, err := a.SetCell(r0, c0, Literal("A"))
	if err != nil {
		t.Fatal(err)
	}
	fromB, err := b.SetCell(r1, c0, Literal("B"))
	if err != nil {
		t.Fatal(err)
	}
	applyAll(t, a, fromB)
	applyAll(t, b, fromA)

	for _, s := range []*Sheet{a, b} {
		if c, ok := s.GetCell(r0, c0); !ok || c.Text != "A" {
			t.Fatalf("cell (r0,c0) = %+v,%v, want A", c, ok)
		}
		if c, ok := s.GetCell(r1, c0); !ok || c.Text != "B" {
			t.Fatalf("cell (r1,c0) = %+v,%v, want B", c, ok)
		}
	}
	if !reflectEqualSnapshot(a, b) {
		t.Fatal("replicas diverged")
	}
}

// Two replicas write the SAME cell at once: last-writer-wins breaks the tie the
// same way on both, so they converge on one value.
func TestSheetSameCellLWWDeterministic(t *testing.T) {
	a, b, r0, _, c0 := baseSheet(t)
	fromA, err := a.SetCell(r0, c0, Literal("from-a"))
	if err != nil {
		t.Fatal(err)
	}
	fromB, err := b.SetCell(r0, c0, Literal("from-b"))
	if err != nil {
		t.Fatal(err)
	}
	applyAll(t, a, fromB)
	applyAll(t, b, fromA)

	ca, _ := a.GetCell(r0, c0)
	cb, _ := b.GetCell(r0, c0)
	if !reflect.DeepEqual(ca, cb) {
		t.Fatalf("diverged: a=%+v b=%+v", ca, cb)
	}
	// Both minted clock 1, so the higher site — 2 — wins.
	if ca.Text != "from-b" {
		t.Fatalf("winner = %q, want the higher site's write", ca.Text)
	}
	if !reflectEqualSnapshot(a, b) {
		t.Fatal("replicas diverged")
	}
}

// The property the whole cell-addressing scheme exists for: a row inserted on one
// replica, concurrent with an edit on another, must not disturb any other cell's
// identity — and a formula that references a cell by stable identity must still
// point at it afterwards.
func TestSheetConcurrentRowInsertKeepsFormulaRef(t *testing.T) {
	a, b, r0, r1, c0 := baseSheet(t)

	// Base state, synced to both: (r1,c0) is a literal, (r0,c0) is a formula that
	// references (r1,c0) by identity.
	seedLit, err := a.SetCell(r1, c0, Literal("10"))
	if err != nil {
		t.Fatal(err)
	}
	seedFormula, err := a.SetCell(r0, c0, Formula("=below", CellRef{Row: r1, Col: c0}))
	if err != nil {
		t.Fatal(err)
	}
	applyAll(t, b, seedLit, seedFormula)

	// Concurrently: A inserts a row at the very top; B rewrites the referenced
	// cell, never having seen A's insert.
	rTop, insert, err := a.InsertRow(0)
	if err != nil {
		t.Fatal(err)
	}
	rewrite, err := b.SetCell(r1, c0, Literal("20"))
	if err != nil {
		t.Fatal(err)
	}
	applyAll(t, a, rewrite)
	applyAll(t, b, insert)

	for name, s := range map[string]*Sheet{"a": a, "b": b} {
		// The rows converged to [rTop, r0, r1] — the insert renumbered positions but
		// touched no identity.
		if got, want := s.Rows(), []RowID{rTop, r0, r1}; !reflect.DeepEqual(got, want) {
			t.Fatalf("%s rows = %v, want %v", name, got, want)
		}
		// The formula cell keeps its reference, intact and still naming (r1,c0).
		formula, ok := s.GetCell(r0, c0)
		if !ok || formula.Kind != CellFormula {
			t.Fatalf("%s (r0,c0) = %+v,%v, want a formula", name, formula, ok)
		}
		if want := []CellRef{{Row: r1, Col: c0}}; !reflect.DeepEqual(formula.Refs, want) {
			t.Fatalf("%s formula refs = %v, want %v (a concurrent insert broke a reference)", name, formula.Refs, want)
		}
		// The referenced cell is still reachable by that identity, holding B's write.
		ref := formula.Refs[0]
		target, ok := s.GetCell(ref.Row, ref.Col)
		if !ok || target.Text != "20" {
			t.Fatalf("%s referenced cell (r1,c0) = %+v,%v, want 20", name, target, ok)
		}
	}
	if !reflectEqualSnapshot(a, b) {
		t.Fatal("replicas diverged")
	}
}

func TestSheetAccessorsAndInsertAt(t *testing.T) {
	s := NewSheet(1)
	if s.Site() != 1 {
		t.Fatalf("Site = %d", s.Site())
	}
	r0, _, _ := s.AppendRow()
	r2, _, _ := s.AppendRow()
	// Insert between the two.
	r1, _, err := s.InsertRow(1)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.Rows(), []RowID{r0, r1, r2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Rows = %v, want %v", got, want)
	}
	if s.RowCount() != 3 {
		t.Fatalf("RowCount = %d", s.RowCount())
	}
	c0, _, _ := s.AppendCol()
	c2, _, _ := s.AppendCol()
	c1, _, err := s.InsertCol(1)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.Cols(), []ColID{c0, c1, c2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Cols = %v, want %v", got, want)
	}
	if s.ColCount() != 3 {
		t.Fatalf("ColCount = %d", s.ColCount())
	}
	// A set, then a clear, of one cell.
	if _, err := s.SetCell(r1, c1, Literal("x")); err != nil {
		t.Fatal(err)
	}
	if c, ok := s.GetCell(r1, c1); !ok || c.Text != "x" {
		t.Fatalf("GetCell = %+v,%v", c, ok)
	}
	if _, err := s.ClearCell(r1, c1); err != nil {
		t.Fatal(err)
	}
	if c, ok := s.GetCell(r1, c1); ok {
		t.Fatalf("after ClearCell, GetCell = %+v,%v, want absent", c, ok)
	}
	// An unset cell reads absent.
	if c, ok := s.GetCell(r0, c0); ok {
		t.Fatalf("unset cell = %+v,%v, want absent", c, ok)
	}
	// Delete a row and a column.
	if _, err := s.DeleteRow(0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteCol(2); err != nil {
		t.Fatal(err)
	}
	if got, want := s.Rows(), []RowID{r1, r2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after DeleteRow, Rows = %v, want %v", got, want)
	}
	if got, want := s.Cols(), []ColID{c0, c1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after DeleteCol, Cols = %v, want %v", got, want)
	}
}

func TestSheetDeleteAndInsertOutOfRange(t *testing.T) {
	s := NewSheet(1)
	if _, _, err := s.InsertRow(5); !errors.Is(err, crdt.ErrOutOfRange) {
		t.Fatalf("InsertRow(5) = %v, want ErrOutOfRange", err)
	}
	if _, _, err := s.InsertCol(5); !errors.Is(err, crdt.ErrOutOfRange) {
		t.Fatalf("InsertCol(5) = %v, want ErrOutOfRange", err)
	}
	if _, err := s.DeleteRow(0); !errors.Is(err, crdt.ErrOutOfRange) {
		t.Fatalf("DeleteRow(0) on empty sheet = %v, want ErrOutOfRange", err)
	}
	if _, err := s.DeleteCol(0); !errors.Is(err, crdt.ErrOutOfRange) {
		t.Fatalf("DeleteCol(0) on empty sheet = %v, want ErrOutOfRange", err)
	}
}

// A peer may write raw bytes into the cells map that are not a cell this package
// wrote; such a cell reads as absent rather than as garbage.
func TestSheetGetCellRejectsForeignBytes(t *testing.T) {
	s := NewSheet(1)
	r0, _, _ := s.AppendRow()
	c0, _, _ := s.AppendCol()
	foreign := crdt.MapOp{
		Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: 1,
		Key: cellKey(r0, c0), Value: []byte{0xFF, 0xFF},
	}
	applyAll(t, s, crdt.PartOps{Part: cellsPart, Map: []crdt.MapOp{foreign}})
	if c, ok := s.GetCell(r0, c0); ok {
		t.Fatalf("GetCell on foreign bytes = %+v,%v, want absent", c, ok)
	}
}

func TestSheetSnapshotRoundTrip(t *testing.T) {
	a, _, r0, _, c0 := baseSheet(t)
	if _, err := a.SetCell(r0, c0, Literal("v")); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSheet(3, a.Snapshot())
	if err != nil {
		t.Fatalf("LoadSheet: %v", err)
	}
	if !reflectEqualSnapshot(a, loaded) {
		t.Fatal("a sheet snapshot did not reload to itself")
	}
	if _, err := LoadSheet(1, []byte("garbage")); err == nil {
		t.Fatal("LoadSheet accepted garbage")
	}
}

// Every local edit refuses rather than wrapping the clock once a peer has driven
// it to the ceiling. The three parts have separate clocks, so each is exhausted
// on its own.
func TestSheetEditsRefuseWhenClockExhausted(t *testing.T) {
	s := NewSheet(1)
	// Two rows and two columns to work on, made while there is still clock.
	mustRow(t, s)
	mustRow(t, s)
	mustCol(t, s)
	mustCol(t, s)
	rows, cols := s.Rows(), s.Cols()

	ceiling := func(part crdt.Part) {
		t.Helper()
		top := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock,
			Key: fieldKey("other", "g"), Value: []byte("x")}
		applyAll(t, s, crdt.PartOps{Part: part, Map: []crdt.MapOp{top}})
	}

	ceiling(rowsPart)
	if _, _, err := s.AppendRow(); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("AppendRow = %v, want ErrExhausted", err)
	}
	if _, err := s.MoveRow(0, 1); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("MoveRow = %v, want ErrExhausted", err)
	}
	if _, err := s.DeleteRow(0); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("DeleteRow = %v, want ErrExhausted", err)
	}

	ceiling(colsPart)
	if _, _, err := s.AppendCol(); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("AppendCol = %v, want ErrExhausted", err)
	}
	if _, err := s.MoveCol(0, 1); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("MoveCol = %v, want ErrExhausted", err)
	}
	if _, err := s.DeleteCol(0); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("DeleteCol = %v, want ErrExhausted", err)
	}

	ceiling(cellsPart)
	if _, err := s.SetCell(rows[0], cols[0], Literal("v")); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("SetCell = %v, want ErrExhausted", err)
	}
	if _, err := s.ClearCell(rows[0], cols[0]); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("ClearCell = %v, want ErrExhausted", err)
	}
}

func reflectEqualSnapshot(a, b *Sheet) bool {
	return reflect.DeepEqual(a.Snapshot(), b.Snapshot())
}

// rowOrder reads the rows as the positions of the identities given, so a
// reordering can be stated as a line rather than as four comparisons.
func rowOrder(s *Sheet, named map[RowID]string) string {
	out := ""
	for _, row := range s.Rows() {
		if out != "" {
			out += " "
		}
		out += named[row]
	}
	return out
}

// A row can be dragged to another place, which is what the axes became
// sequences for. It is one operation, and the row keeps its identity, so its
// cells come with it.
func TestMovingARowTakesItsCellsWithIt(t *testing.T) {
	s := NewSheet(1)
	a, b, c := mustRow(t, s), mustRow(t, s), mustRow(t, s)
	col := mustCol(t, s)
	named := map[RowID]string{a: "a", b: "b", c: "c"}
	for row, name := range named {
		if _, err := s.SetCell(row, col, Literal(name)); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := rowOrder(s, named), "a b c"; got != want {
		t.Fatalf("the rows read %q, want %q", got, want)
	}

	// The last row to the front, then the first to the end.
	ops, err := s.MoveRow(2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(ops.Map); n != 1 {
		t.Fatalf("a move took %d operations, want 1", n)
	}
	if got, want := rowOrder(s, named), "c a b"; got != want {
		t.Fatalf("after moving the last row first the rows read %q, want %q", got, want)
	}
	if _, err := s.MoveRow(1, 2); err != nil {
		t.Fatal(err)
	}
	if got, want := rowOrder(s, named), "c b a"; got != want {
		t.Fatalf("after moving a row later the rows read %q, want %q", got, want)
	}

	// And earlier, but not all the way to the front, which is the third of the
	// three ways a move can go.
	d := mustRow(t, s)
	named[d] = "d"
	if got, want := rowOrder(s, named), "c b a d"; got != want {
		t.Fatalf("the rows read %q, want %q", got, want)
	}
	if _, err := s.MoveRow(3, 1); err != nil {
		t.Fatal(err)
	}
	if got, want := rowOrder(s, named), "c d b a"; got != want {
		t.Fatalf("after moving a row earlier the rows read %q, want %q", got, want)
	}

	// Every cell is still where its row is, which is the property the identity
	// buys: nothing else in the sheet moved.
	for _, row := range s.Rows() {
		cell, ok := s.GetCell(row, col)
		if row == d {
			if ok {
				t.Fatal("the row added last has a cell")
			}
			continue
		}
		if !ok || cell.Text != named[row] {
			t.Fatalf("the cell of row %q reads %q", named[row], cell.Text)
		}
	}
}

func TestMovingAColumn(t *testing.T) {
	s := NewSheet(1)
	row := mustRow(t, s)
	first, second := mustCol(t, s), mustCol(t, s)
	if _, err := s.SetCell(row, first, Literal("1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetCell(row, second, Literal("2")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveCol(1, 0); err != nil {
		t.Fatal(err)
	}
	if cols := s.Cols(); cols[0] != second || cols[1] != first {
		t.Fatal("the columns did not swap")
	}
	cell, _ := s.GetCell(row, s.Cols()[0])
	if cell.Text != "2" {
		t.Fatalf("the first column now reads %q, want the column that moved there", cell.Text)
	}
}

// A formula names its dependencies by the identities of their row and column,
// so dragging a row about cannot break one.
func TestAFormulaSurvivesItsRowBeingMoved(t *testing.T) {
	s := NewSheet(1)
	top, bottom := mustRow(t, s), mustRow(t, s)
	col := mustCol(t, s)
	if _, err := s.SetCell(top, col, Literal("7")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetCell(bottom, col, Formula("=above", CellRef{Row: top, Col: col})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveRow(1, 0); err != nil {
		t.Fatal(err)
	}
	cell, ok := s.GetCell(bottom, col)
	if !ok || len(cell.Refs) != 1 {
		t.Fatal("the formula lost its reference")
	}
	if cell.Refs[0].Row != top || cell.Refs[0].Col != col {
		t.Fatal("the formula's reference no longer names the cell it did")
	}
}

// Two replicas dragging the same row at the same time. As a delete and an
// insert this leaves the row twice or not at all; as one field write both
// replicas settle it the same way.
func TestTwoReplicasMoveTheSameRow(t *testing.T) {
	a := NewSheet(1)
	first, second, third := mustRow(t, a), mustRow(t, a), mustRow(t, a)
	named := map[RowID]string{first: "1", second: "2", third: "3"}

	b, err := LoadSheet(2, a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	fromA, err := a.MoveRow(0, 2) // the first row to the end
	if err != nil {
		t.Fatal(err)
	}
	fromB, err := b.MoveRow(0, 1) // and into the middle
	if err != nil {
		t.Fatal(err)
	}
	applyAll(t, a, fromB)
	applyAll(t, b, fromA)

	if rowOrder(a, named) != rowOrder(b, named) {
		t.Fatalf("the replicas disagree: %q and %q", rowOrder(a, named), rowOrder(b, named))
	}
	if a.RowCount() != 3 {
		t.Fatalf("the sheet holds %d rows after the concurrent move, want 3: %q",
			a.RowCount(), rowOrder(a, named))
	}
	seen := map[RowID]bool{}
	for _, row := range a.Rows() {
		if seen[row] {
			t.Fatal("a row is read twice")
		}
		seen[row] = true
	}
}

func TestWhatAMoveOnAnAxisRefuses(t *testing.T) {
	s := NewSheet(1)
	mustRow(t, s)
	mustRow(t, s)
	mustCol(t, s)

	if _, err := s.MoveRow(0, 0); !errors.Is(err, ErrNoChange) {
		t.Fatalf("moving a row to where it is gave %v, want ErrNoChange", err)
	}
	for _, c := range []struct{ from, to int }{{-1, 0}, {0, -1}, {2, 0}, {0, 2}} {
		if _, err := s.MoveRow(c.from, c.to); !errors.Is(err, crdt.ErrOutOfRange) {
			t.Fatalf("MoveRow(%d, %d) = %v, want ErrOutOfRange", c.from, c.to, err)
		}
	}
	for _, c := range []struct{ from, to int }{{-1, 0}, {0, 1}, {1, 0}} {
		if _, err := s.MoveCol(c.from, c.to); err == nil {
			t.Fatalf("MoveCol(%d, %d) with one column was accepted", c.from, c.to)
		}
	}
	// The rows are untouched by any of it.
	if s.RowCount() != 2 || s.ColCount() != 1 {
		t.Fatal("a refused move changed the sheet")
	}
}

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
// it to the ceiling. The list and the map are separate parts with separate
// clocks, so each is exhausted on its own.
func TestSheetEditsRefuseWhenClockExhausted(t *testing.T) {
	s := NewSheet(1)
	// Exhaust the rows list clock, and leave a row present to delete.
	topRow := crdt.ListOp{Kind: crdt.OpInsert, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock, Value: []byte{axisMark}}
	applyAll(t, s, crdt.PartOps{Part: rowsPart, List: []crdt.ListOp{topRow}})
	if _, _, err := s.AppendRow(); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("AppendRow = %v, want ErrExhausted", err)
	}
	if _, err := s.DeleteRow(0); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("DeleteRow = %v, want ErrExhausted", err)
	}
	// Same for the columns list.
	topCol := crdt.ListOp{Kind: crdt.OpInsert, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock, Value: []byte{axisMark}}
	applyAll(t, s, crdt.PartOps{Part: colsPart, List: []crdt.ListOp{topCol}})
	if _, _, err := s.AppendCol(); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("AppendCol = %v, want ErrExhausted", err)
	}
	if _, err := s.DeleteCol(0); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("DeleteCol = %v, want ErrExhausted", err)
	}
	// And the cells map.
	rows := s.Rows()
	cols := s.Cols()
	topCell := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock, Key: "seed"}
	applyAll(t, s, crdt.PartOps{Part: cellsPart, Map: []crdt.MapOp{topCell}})
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

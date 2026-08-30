package crdt

import (
	"errors"
	"testing"
)

// A superseded run is understood here and produced nowhere, which is the point:
// a later release may send one, and the two ends of a session are not deployed
// at the same moment. See go-crdt/crdt#80.
func TestASupersededRunIsAccountedForAndDoesNothing(t *testing.T) {
	a := New(1)
	if _, err := a.Insert(0, "hello"); err != nil {
		t.Fatal(err)
	}
	// Three deletions, which are what a run may stand in for: nothing names a
	// deletion, so nothing is left waiting when one is not sent.
	if _, err := a.Delete(0, 3); err != nil {
		t.Fatal(err)
	}
	b := New(2)

	all := a.OpsSince(nil)
	if len(all) != 8 {
		t.Fatalf("five characters and three deletions produced %d operations", len(all))
	}
	// The insertions, in the order their site issued them: OpsSince returns
	// document order, which interleaves them with the deletions.
	inserts := make([]Op, 5)
	for _, op := range all {
		if op.Kind == OpInsert {
			inserts[op.ID.Seq-1] = op
		}
	}
	run := Op{Kind: OpSuperseded, ID: ID{Site: 1, Seq: 8}, Clock: 8, Span: 3}
	if err := b.Apply(append(inserts, run)...); err != nil {
		t.Fatalf("applying a superseded run: %v", err)
	}

	if got := b.String(); got != "hello" {
		t.Fatalf("the peer reads %q; a run says the operations had no effect worth keeping", got)
	}
	if !b.Version().Includes(ID{Site: 1, Seq: 8}) {
		t.Fatalf("the peer's version is %v; the run has to account for what it stands in for", b.Version())
	}
	if n := b.Pending(); n != 0 {
		t.Fatalf("%d operations are still waiting; the run left a hole", n)
	}

	// And it waits its turn like anything else: a run that does not start where
	// this replica has got to is parked, not applied.
	c := New(3)
	early := Op{Kind: OpSuperseded, ID: ID{Site: 1, Seq: 5}, Clock: 5, Span: 2}
	if err := c.Apply(early); err != nil {
		t.Fatal(err)
	}
	if n := c.Pending(); n != 1 {
		t.Fatalf("a run reaching back to sequence four was not parked: %d waiting", n)
	}
}

// And it says its own name, for the diagnostics every other kind appears in.
func TestASupersededRunIsNamed(t *testing.T) {
	if got := OpSuperseded.String(); got != "superseded" {
		t.Fatalf("OpSuperseded prints as %q", got)
	}
}

// What it refuses, because it arrives from a peer.
func TestASupersededRunIsRefusedWhenItIsNotOne(t *testing.T) {
	for _, bad := range []struct {
		why string
		op  Op
	}{
		{"no span", Op{Kind: OpSuperseded, ID: ID{Site: 1, Seq: 3}, Clock: 3}},
		{"a span past the beginning", Op{Kind: OpSuperseded, ID: ID{Site: 1, Seq: 3}, Clock: 3, Span: 4}},
		{"a clock of its own", Op{Kind: OpSuperseded, ID: ID{Site: 1, Seq: 3}, Clock: 9, Span: 1}},
		{"a character", Op{Kind: OpSuperseded, ID: ID{Site: 1, Seq: 3}, Clock: 3, Span: 1, Char: 'x'}},
		{"a target", Op{Kind: OpSuperseded, ID: ID{Site: 1, Seq: 3}, Clock: 3, Span: 1, Target: ID{Site: 1, Seq: 1}}},
		{"a span on a delete", Op{Kind: OpDelete, ID: ID{Site: 1, Seq: 3}, Clock: 3, Target: ID{Site: 1, Seq: 1}, Span: 2}},
	} {
		if _, err := bad.op.MarshalBinary(); !errors.Is(err, ErrInvalidOp) {
			t.Fatalf("%s: MarshalBinary = %v, want ErrInvalidOp", bad.why, err)
		}
		if err := New(9).Apply(bad.op); !errors.Is(err, ErrInvalidOp) {
			t.Fatalf("%s: Apply = %v, want ErrInvalidOp", bad.why, err)
		}
	}

	// And on the wire: the field a delete spends on its target's sequence
	// number is not free here, so one meaning has one encoding.
	good := Op{Kind: OpSuperseded, ID: ID{Site: 1, Seq: 3}, Clock: 3, Span: 2}
	raw, err := good.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var back Op
	if err := back.UnmarshalBinary(raw); err != nil {
		t.Fatal(err)
	}
	if back != good {
		t.Fatalf("round trip gave %+v, want %+v", back, good)
	}
	raw[len(raw)-1] = 1
	if err := back.UnmarshalBinary(raw); err == nil {
		t.Fatal("a run with something in the field it does not use was accepted")
	}
}

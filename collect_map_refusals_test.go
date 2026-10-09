package crdt

import (
	"errors"
	"testing"
)

// A superseded run is not a write, so it cannot resurrect a collected key.
//
// resurrects answers "this write would bring back a key whose tombstone we
// dropped", and admit turns a yes into ErrStranded for the whole batch. A
// superseded run names no key at all -- it stands in for operations its sender
// no longer holds -- so the comparison underneath finds m.records[""] absent and
// reads that as a key this replica does not hold.
//
// A mutation sweep walked through the refusal that keeps it out of that
// comparison on 2026-10-09. Measured on a map collected below 10:
//
//	resurrects(a superseded run) = false   as written
//	resurrects(a superseded run) = true    with the refusal gone
//
// and true there is Apply returning ErrStranded. This is the second of exactly
// this shape found in one campaign: Doc.collides had the same problem with the
// same kind of operation, and the same fix. A superseded run carries a sequence
// range and nothing else, so every comparison that reads one of its other
// fields is reading a zero value that means nothing.
func TestASupersededRunDoesNotResurrectACollectedKey(t *testing.T) {
	m := NewMap(1)
	if _, err := m.Set("k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Delete("k"); err != nil {
		t.Fatal(err)
	}
	// Collect the tombstone, which is what gives this map a floor at all.
	if dropped := m.Collect(m.Version(), MaxClock); dropped != 1 {
		t.Fatalf("collected %d tombstones, want 1 -- without a floor there is no refusal to test", dropped)
	}
	if m.CollectedBelow() == 0 {
		t.Fatal("the map has no collection floor, so resurrects returns false for every operation")
	}

	// A run from a peer, covering its own first operations, with a clock at or
	// below the floor: exactly the shape the comparison would read as a write
	// under a collected tombstone.
	sup := MapOp{Kind: MapSuperseded, ID: ID{Site: 2, Seq: 3}, Clock: 3, Span: 3}
	if sup.Clock > m.CollectedBelow() {
		t.Fatalf("the run's clock %d is above the floor %d, so it never reaches the comparison", sup.Clock, m.CollectedBelow())
	}
	if err := m.Apply(sup); err != nil {
		t.Fatalf("a superseded run gave %v; ErrStranded here refuses a peer's legitimate catch-up "+
			"because the run names no key and the absent key reads as a collected one", err)
	}

	// The control, and it is what keeps this test honest: a real write under the
	// floor, naming a key this replica no longer holds, must still be refused.
	set := MapOp{Kind: MapSet, ID: ID{Site: 3, Seq: 1}, Clock: 1, Key: "k", Value: []byte("back")}
	if err := m.Apply(set); !errors.Is(err, ErrStranded) {
		t.Errorf("a write below the collection floor naming a dropped key gave %v, want ErrStranded", err)
	}
}

// Five more refusals in the nine files swept on 2026-10-09 survived and are
// named here rather than pinned, each measured rather than argued:
//
//	collect_map.go:157's OTHER half, "m.collectedBelow == 0". Dropping only
//	that clause leaves the whole suite green and this test with it: a map that
//	has collected nothing has a floor of zero, and the comparison underneath --
//	op.Clock > m.collectedBelow -- is true for every operation a validator
//	would accept, since a clock of zero is not one. What carries the gate is
//	the kind.
//
//	anchor.go:55, "the root is visible". Deleting it sends the root through
//	lookupChar, which answers (head, -1, found) for it, and aliveAt(-1) is
//	true -- so Visible returns true either way. Probed on a document with
//	text: true before, true after.
//
//	purge.go:124, "nothing was purged". The loop under it walks for a block
//	marked gone, and gone is set in exactly two places, both of them a purge.
//	A document that has not purged has none, so the loop finds nothing and
//	CanServe returns nil by the longer route.
//
//	collect_map.go:285 and :289, two reads in the snapshot loader, refused
//	again by what follows them.
//
//	tree.go:246, flush's "nothing is held". The loop under it does not run
//	when d.dirty is nil, and the assignment after it writes the zero values
//	that are already there.

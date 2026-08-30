package crdt

import (
	"errors"
	"testing"
)

// meetOfAll is the version every one of these replicas has delivered, which is
// one of the two things [Map.Collect] asks to be given.
func meetOfAll(ms ...*Map) VersionVector {
	out := VersionVector{}
	for site, n := range ms[0].Version() {
		out[site] = n
	}
	for _, m := range ms[1:] {
		v := m.Version()
		for site := range out {
			if v[site] < out[site] {
				out[site] = v[site]
			}
		}
	}
	return out
}

// A concurrent write arriving after a collection is refused, and it is not
// misuse that put it there.
//
// [Map.Collect] asks for a version every replica has delivered, and that is
// exactly what it is given here. The write is an ordinary concurrent operation
// from a site that had not seen the deletion, still on its way. Its clock is
// not bounded by the version: a site that has seen nothing issues at clock one.
//
// The guard reads a clock and the collection asked about a version. They do not
// answer the same question.
func TestAConcurrentWriteIsRefusedAfterACollection(t *testing.T) {
	writer, deleter, third := NewMap(1), NewMap(2), NewMap(3)
	set, err := writer.Set("k", []byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	del, err := deleter.Delete("k")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []*Map{writer, third} {
		if err := m.Apply(del); err != nil {
			t.Fatalf("applying the deletion: %v", err)
		}
	}

	floor := meetOfAll(writer, deleter, third)
	if !floor.Includes(del.ID) {
		t.Fatalf("the fixture is wrong: %v is not in the meet %v", del.ID, floor)
	}
	// And the other thing: a promise that no operation with a clock at or under
	// it can still arrive. Site 1 has sent nothing that this replica holds, so
	// there is nothing to bound its first operation by and the promise is zero.
	below := clockFloorOver(third, 1, 2, 3)
	if below != 0 {
		t.Fatalf("clock floor = %d; site 1 has sent nothing here, so nothing bounds what it may send", below)
	}
	if n := third.Collect(floor, below); n != 0 {
		t.Fatalf("collected %d tombstones under a floor of nothing", n)
	}

	// So the tombstone is still here to be compared against, and the write is
	// an ordinary operation again.
	if err := third.Apply(set); errors.Is(err, ErrStranded) {
		t.Fatalf("a concurrent write nobody misused was refused with %v", err)
	} else if err != nil {
		t.Fatalf("applying the write: %v", err)
	}
}

// clockFloorOver is what a caller that knows who is out there can promise: the
// least, over every site there is, of the clock of the last operation this
// replica has from it. A site it has heard nothing from bounds nothing, and
// takes the answer to zero.
func clockFloorOver(m *Map, sites ...SiteID) uint64 {
	seen := m.LastClocks()
	floor := ^uint64(0)
	for _, site := range sites {
		clock, heard := seen[site]
		if !heard {
			return 0
		}
		if clock < floor {
			floor = clock
		}
	}
	return floor
}

// And the write that would have won leaves two replicas holding different
// documents, which is the whole of why it matters.
//
// Site 3 writes and site 2 deletes, neither having seen the other, so both are
// at clock one and the tie goes to the higher site: the write beats the
// deletion. A replica that kept the tombstone makes that comparison and brings
// the key back. A replica that collected it cannot, because what it needed to
// compare against is gone.
//
// Both applied the same operations. This is the same shape as the text and list
// collection withdrawn in v0.35.0.
func TestCollectingLosesAComparisonALaterWriteNeeded(t *testing.T) {
	writer, deleter := NewMap(3), NewMap(2)
	set, err := writer.Set("k", []byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	del, err := deleter.Delete("k")
	if err != nil {
		t.Fatal(err)
	}

	collected, kept := NewMap(4), NewMap(5)
	for _, m := range []*Map{collected, kept, writer} {
		if err := m.Apply(del); err != nil {
			t.Fatalf("applying the deletion: %v", err)
		}
	}
	floor := meetOfAll(writer, deleter, collected, kept)
	if !floor.Includes(del.ID) {
		t.Fatalf("the fixture is wrong: %v is not in the meet %v", del.ID, floor)
	}
	below := clockFloorOver(collected, 2, 3, 4, 5)
	if n := collected.Collect(floor, below); n != 0 {
		t.Fatalf("collected %d tombstones while site 3's write was still on its way", n)
	}

	errCollected, errKept := collected.Apply(set), kept.Apply(set)
	t.Logf("the replica that collected says %v; the one that kept the tombstone says %v", errCollected, errKept)
	gotC, heldC := collected.Get("k")
	gotK, heldK := kept.Get("k")
	if heldC != heldK || string(gotC) != string(gotK) {
		t.Fatalf("two replicas that applied the same operations disagree: collected holds=%v %q, kept holds=%v %q",
			heldC, gotC, heldK, gotK)
	}
}

// And when the guard does fire, the composite says so instead of swallowing it.
//
// Getting it to fire takes deliberate misuse — a floor no caller could have
// promised — which is the point: under a floor somebody could promise, nothing
// here happens at all. What must not happen is the old behaviour, where the
// operation was refused, the caller was told the batch had been applied, and
// everything that site sent afterwards waited for a predecessor that would never
// come.
func TestACompositeReportsWhatAMapPartRefused(t *testing.T) {
	part := Part{Kind: PartMap, Name: "cells"}
	writer := NewMap(3)
	set, err := writer.Set("k", []byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	deleter := NewMap(2)
	del, err := deleter.Delete("k")
	if err != nil {
		t.Fatal(err)
	}

	doc := NewComposite(9)
	if err := doc.Apply(PartOps{Part: part, Map: []MapOp{del}}); err != nil {
		t.Fatal(err)
	}
	// A floor of "everything has arrived" is a lie here: site 3's write has not.
	if n := doc.Collect(doc.Version(), settledClocks(doc.Version())); n != 1 {
		t.Fatalf("collected %d tombstones, want the one there is", n)
	}

	err = doc.Apply(PartOps{Part: part, Map: []MapOp{set}})
	if !errors.Is(err, ErrStranded) {
		t.Fatalf("Composite.Apply = %v, want the map part's %v passed on", err, ErrStranded)
	}
	// ApplyChanges is the same promise to a caller that is watching.
	_, err = doc.ApplyChanges(PartOps{Part: part, Map: []MapOp{set}})
	if !errors.Is(err, ErrStranded) {
		t.Fatalf("Composite.ApplyChanges = %v, want the map part's %v passed on", err, ErrStranded)
	}
}

// A map loaded from a snapshot can still say what each site had reached, or a
// document that has been saved and reopened could never be collected again.
//
// The snapshot keeps the winning write per key and nothing of the ones that
// lost, so what comes back is at or below the truth. That is the safe
// direction: a floor built from it refuses more than it needs to and never
// promises more than it can.
func TestALoadedMapRemembersWhatEachSiteHadReached(t *testing.T) {
	a, b := NewMap(1), NewMap(2)
	first, err := a.Set("k", []byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Set("j", []byte("w"))
	if err != nil {
		t.Fatal(err)
	}
	// Both, or the second parks on the first and nothing is integrated at all.
	if err := b.Apply(first, second); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Set("m", []byte("x")); err != nil {
		t.Fatal(err)
	}

	before := b.LastClocks()
	if len(before) != 2 {
		t.Fatalf("LastClocks = %v, want one entry per site that has written", before)
	}
	loaded, err := LoadMap(2, b.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	after := loaded.LastClocks()
	if len(after) != len(before) {
		t.Fatalf("a loaded map says %v; before saving it said %v", after, before)
	}
	for site, clock := range after {
		if clock > before[site] {
			t.Fatalf("a loaded map claims site %d had reached %d, above the %d it had", site, clock, before[site])
		}
	}
}

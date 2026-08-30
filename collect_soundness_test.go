package crdt

import (
	"errors"
	"testing"
)

// meetOfAll is the version every one of these replicas has delivered, which is
// what [Map.Collect] asks to be given.
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
	if n := third.Collect(floor); n == 0 {
		t.Fatal("the fixture is wrong: nothing was collected")
	}

	if err := third.Apply(set); errors.Is(err, ErrStranded) {
		t.Fatalf("a concurrent write nobody misused was refused with %v", err)
	} else if err != nil {
		t.Fatalf("applying the write: %v", err)
	}
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
	if n := collected.Collect(floor); n == 0 {
		t.Fatal("the fixture is wrong: nothing was collected")
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

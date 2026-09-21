// Copyright (c) the go-crdt authors.
// SPDX-License-Identifier: BSD-3-Clause

package crdt

import (
	"bytes"
	"strings"
	"testing"
)

// The duplicate deletions are written in one order whatever order they arrived
// in, which is what makes the encoding canonical: two replicas holding the same
// operations produce the same bytes.
//
// The order used to be produced by draining the map and sorting it on every
// call. It is now kept as entries arrive, with only the new ones sorted. This is
// the property that change could break, so it is asserted against arrival order
// rather than against a golden file: the same operations, delivered forwards and
// backwards, must give byte-identical snapshots.
func TestDuplicateDeletionsAreWrittenInOneOrderWhateverOrderTheyArriveIn(t *testing.T) {
	const n = 40
	// From three sites, and NOT in site order: idLess takes the site first, so
	// deletions arriving 9, 3, 7 are recorded in an order the sort has to
	// change. Delivering them in site order would make the sort a no-op and
	// this test would pass with it removed -- which is what the first draft
	// did.
	var losers []Op
	for _, site := range []SiteID{9, 3, 7} {
		losers = append(losers, concurrentDeletionsFrom(t, site, n, 0, n)...)
	}

	forwards := docWithText(t, n)
	if err := forwards.Apply(losers...); err != nil {
		t.Fatal(err)
	}
	backwards := docWithText(t, n)
	reversed := make([]Op, len(losers))
	for i, op := range losers {
		reversed[len(losers)-1-i] = op
	}
	if err := backwards.Apply(reversed...); err != nil {
		t.Fatal(err)
	}

	if got, want := len(forwards.dupDeletes), len(backwards.dupDeletes); got != want {
		t.Fatalf("the two replicas hold %d and %d duplicate deletions", got, want)
	}
	if len(forwards.dupDeletes) == 0 {
		t.Fatal("no duplicate deletion was recorded, so this test asserts nothing")
	}
	if !bytes.Equal(forwards.Snapshot(), backwards.Snapshot()) {
		t.Fatal("the same operations in two orders gave different snapshots")
	}
}

// Every duplicate deletion the map holds is in the order that is written, and
// nothing is in it twice.
//
// The order is kept beside the map rather than derived from it, so the two can
// drift -- a write that reached the map directly would leave the entry out of
// every snapshot, silently. Found the hard way: a benchmark that filled the map
// itself made Snapshot look four thousand times faster, because it had stopped
// writing the table at all.
func TestTheWrittenOrderHoldsExactlyWhatTheTableHolds(t *testing.T) {
	const n = 40
	d := docWithText(t, n)
	if err := d.Apply(concurrentDeletions(t, n)...); err != nil {
		t.Fatal(err)
	}
	// Applying the same operations again must change neither.
	again := concurrentDeletions(t, n)
	if err := d.Apply(again...); err != nil {
		t.Fatal(err)
	}

	if len(d.dupOrder) != len(d.dupDeletes) {
		t.Fatalf("the written order holds %d entries and the table holds %d", len(d.dupOrder), len(d.dupDeletes))
	}
	seen := make(map[ID]bool, len(d.dupOrder))
	for _, id := range d.dupOrder {
		if seen[id] {
			t.Fatalf("%v is in the written order twice", id)
		}
		seen[id] = true
		if _, held := d.dupDeletes[id]; !held {
			t.Fatalf("%v is in the written order and not in the table", id)
		}
	}

	// And the order really is sorted once it has been asked for.
	ordered := d.duplicatesInOrder()
	for i := 1; i < len(ordered); i++ {
		if !idLess(ordered[i-1], ordered[i]) {
			t.Fatalf("entry %d (%v) does not come before %d (%v)", i-1, ordered[i-1], i, ordered[i])
		}
	}
}

// A document that has asked for the order and then received more entries sorts
// only the new ones and still comes out in order.
//
// The merge is what this is about: the first call leaves a sorted run behind,
// and the second has to put the arrivals into it rather than beside it.
func TestArrivalsAfterASortAreMergedIntoIt(t *testing.T) {
	d := docWithText(t, 40)
	// The first batch from a HIGH site and the second from a low one, because
	// idLess takes the site first: the arrivals then sort BEFORE what is already
	// in order, which is the only arrangement the merge is needed for. With the
	// sites the other way round the two runs concatenate in order and the test
	// passes with the merge removed -- which is what the first draft did.
	first := concurrentDeletionsFrom(t, 9, 40, 0, 20)
	if err := d.Apply(first...); err != nil {
		t.Fatal(err)
	}
	before := len(d.duplicatesInOrder()) // sorts, and records how much is sorted
	if before == 0 {
		t.Fatal("nothing was recorded, so the merge is never reached")
	}

	// From a THIRD site. A second replica of site 2 would restart its own
	// sequence at one and reissue the identifiers the first batch already used,
	// which Apply rightly ignores -- and then this test would assert nothing.
	second := concurrentDeletionsFrom(t, 3, 40, 20, 40)
	if err := d.Apply(second...); err != nil {
		t.Fatal(err)
	}
	ordered := d.duplicatesInOrder()
	if len(ordered) <= before {
		t.Fatalf("the order holds %d entries, which is not more than the %d before", len(ordered), before)
	}
	for i := 1; i < len(ordered); i++ {
		if !idLess(ordered[i-1], ordered[i]) {
			t.Fatalf("after the merge, entry %d (%v) does not come before %d (%v)", i-1, ordered[i-1], i, ordered[i])
		}
	}
	if len(ordered) != len(d.dupDeletes) {
		t.Fatalf("the merge left %d entries where the table holds %d", len(ordered), len(d.dupDeletes))
	}
}

// docWithText is a replica of n characters, site 1.
func docWithText(t *testing.T, n int) *Doc {
	t.Helper()
	d := New(1)
	if _, err := d.Insert(0, strings.Repeat("x", n)); err != nil {
		t.Fatal(err)
	}
	for i := n - 1; i >= 0; i-- {
		if _, err := d.Delete(i, 1); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// concurrentDeletions is what a second replica's deletions of the same n
// characters look like: every one of them loses, because site 1 deleted first.
func concurrentDeletions(t *testing.T, n int) []Op {
	t.Helper()
	return concurrentDeletionsFrom(t, 2, n, 0, n)
}

// concurrentDeletionsFrom is the same from a named site, for the characters in
// [from, to). The site matters: two replicas built under one site reissue the
// same identifiers, and Apply ignores the second lot.
func concurrentDeletionsFrom(t *testing.T, site SiteID, n, from, to int) []Op {
	t.Helper()
	seed := New(1)
	if _, err := seed.Insert(0, strings.Repeat("x", n)); err != nil {
		t.Fatal(err)
	}
	other, err := Load(site, seed.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var ops []Op
	for i := to - 1; i >= from; i-- {
		got, err := other.Delete(i, 1)
		if err != nil {
			t.Fatal(err)
		}
		ops = append(ops, got...)
	}
	return ops
}

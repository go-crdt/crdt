package crdt

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
)

// The claim collection has to earn, asked of a list: a replica that has
// collected and one that has not must still agree, whatever they do next and in
// whatever order they hear about it.
func TestCollectedAndUncollectedListsStillAgree(t *testing.T) {
	for seed := uint64(1); seed <= 400; seed++ {
		r := rand.New(rand.NewPCG(seed, 11))
		a, b := NewList(1), NewList(2)

		for round := 0; round < 100; round++ {
			from, to := a, b
			if r.IntN(2) == 0 {
				from, to = b, a
			}
			var ops []ListOp
			var err error
			if from.Len() > 0 && r.IntN(3) == 0 {
				at := r.IntN(from.Len())
				ops, err = from.Delete(at, 1+r.IntN(min(4, from.Len()-at)))
			} else {
				at := 0
				if from.Len() > 0 {
					at = r.IntN(from.Len() + 1)
				}
				ops, err = from.Insert(at, []byte(fmt.Sprintf("v%d", r.IntN(1000))))
			}
			if err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
			if err := to.Apply(ops...); err != nil {
				t.Fatalf("seed %d: apply: %v", seed, err)
			}
		}
		if !sameValues(a.Values(), b.Values()) {
			t.Fatalf("seed %d: the two disagreed before anything was collected", seed)
		}

		collected := a.Collect(a.Version())
		if !sameValues(a.Values(), b.Values()) {
			t.Fatalf("seed %d: collecting %d elements changed the list", seed, collected)
		}

		var fromA, fromB []ListOp
		for round := 0; round < 30; round++ {
			at := 0
			if a.Len() > 0 {
				at = r.IntN(a.Len() + 1)
			}
			ops, err := a.Insert(at, []byte(fmt.Sprintf("A%d", round)))
			if err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
			fromA = append(fromA, ops...)

			at = 0
			if b.Len() > 0 {
				at = r.IntN(b.Len() + 1)
			}
			ops, err = b.Insert(at, []byte(fmt.Sprintf("B%d", round)))
			if err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
			fromB = append(fromB, ops...)
		}
		r.Shuffle(len(fromA), func(i, j int) { fromA[i], fromA[j] = fromA[j], fromA[i] })
		r.Shuffle(len(fromB), func(i, j int) { fromB[i], fromB[j] = fromB[j], fromB[i] })
		if err := b.Apply(fromA...); err != nil {
			t.Fatalf("seed %d: b could not take a's work: %v", seed, err)
		}
		if err := a.Apply(fromB...); err != nil {
			t.Fatalf("seed %d: a could not take b's work: %v", seed, err)
		}
		if a.Pending() != 0 || b.Pending() != 0 {
			t.Fatalf("seed %d: operations stranded: a %d, b %d", seed, a.Pending(), b.Pending())
		}
		if !sameValues(a.Values(), b.Values()) {
			t.Fatalf("seed %d: after collecting %d elements the replicas disagree", seed, collected)
		}
	}
}

func sameValues(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if string(a[i]) != string(b[i]) {
			return false
		}
	}
	return true
}

// revisedList builds a list the way one is used: values appended, some of them
// taken away again.
func revisedList(t *testing.T, edits int) *List {
	t.Helper()
	l := NewList(1)
	for n := 0; n < edits; {
		if _, err := l.Insert(l.Len(), []byte(fmt.Sprintf("value number %d", n))); err != nil {
			t.Fatal(err)
		}
		n++
		if n%3 == 0 && l.Len() > 0 {
			if _, err := l.Delete(0, 1); err != nil {
				t.Fatal(err)
			}
			n++
		}
	}
	return l
}

// Collecting makes the list smaller, leaves it holding the same values, and
// leaves it editable.
func TestCollectAListShrinksAndKeepsTheValues(t *testing.T) {
	l := revisedList(t, 2000)
	before, want := len(l.Snapshot()), l.Values()
	n := l.Collect(l.Version())
	if n == 0 {
		t.Fatal("nothing was collected from a list a third of which is deleted")
	}
	if !sameValues(l.Values(), want) {
		t.Fatal("collecting changed the values")
	}
	after := len(l.Snapshot())
	if after >= before {
		t.Fatalf("the snapshot did not shrink: %d bytes became %d", before, after)
	}
	t.Logf("collected %d elements; %d bytes became %d (%.2fx)", n, before, after, float64(before)/float64(after))
	for _, at := range []int{0, l.Len(), l.Len() / 2} {
		if _, err := l.Insert(at, []byte("more")); err != nil {
			t.Fatalf("editing at %d after collecting: %v", at, err)
		}
	}
}

// A collected list has to survive being written down and read back.
func TestCollectedListReloads(t *testing.T) {
	l := revisedList(t, 400)
	n := l.Collect(l.Version())
	want, floor := l.Values(), l.Floor()

	back, err := LoadList(1, l.Snapshot())
	if err != nil {
		t.Fatalf("a collected list did not reload: %v", err)
	}
	if !sameValues(back.Values(), want) {
		t.Fatal("the reloaded list holds something else")
	}
	for site, seq := range floor {
		if back.Floor()[site] != seq {
			t.Fatalf("the floor did not survive: site %d is %d, want %d", site, back.Floor()[site], seq)
		}
	}
	if n == 0 || len(back.Floor()) == 0 {
		t.Fatalf("collected %d elements but the reloaded floor is empty", n)
	}
	peer, err := LoadList(2, l.Snapshot())
	if err != nil {
		t.Fatalf("a second replica could not load it: %v", err)
	}
	ops, err := peer.Insert(peer.Len(), []byte("after"))
	if err != nil {
		t.Fatal(err)
	}
	if err := back.Apply(ops...); err != nil {
		t.Fatalf("the reloaded list refused a peer's work: %v", err)
	}
	if back.Pending() != 0 {
		t.Fatalf("%d operations from a peer were stranded", back.Pending())
	}
	if !sameValues(back.Values(), peer.Values()) {
		t.Fatal("the two disagree after an ordinary edit")
	}
}

// Reading the past below the floor is refused rather than answered wrongly.
func TestListHistoryRefusesBelowTheFloor(t *testing.T) {
	l := NewList(1)
	if _, err := l.Insert(0, []byte("gone")); err != nil {
		t.Fatal(err)
	}
	early := l.Version()
	if _, err := l.Insert(l.Len(), []byte("kept")); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Delete(0, 1); err != nil {
		t.Fatal(err)
	}

	was, err := l.ValuesAt(early)
	if err != nil {
		t.Fatalf("ValuesAt before collecting: %v", err)
	}
	if len(was) != 1 || string(was[0]) != "gone" {
		t.Fatalf("the list at the early version holds %q", was)
	}
	if len(l.Floor()) != 0 {
		t.Fatal("a list that has never collected has a floor")
	}
	if n := l.Collect(l.Version()); n != 1 {
		t.Fatalf("collected %d elements, want 1", n)
	}
	if _, err := l.ValuesAt(early); !errors.Is(err, ErrCollected) {
		t.Fatalf("ValuesAt below the floor = %v, want ErrCollected", err)
	}
	if _, err := l.LenAt(early); !errors.Is(err, ErrCollected) {
		t.Fatalf("LenAt below the floor = %v, want ErrCollected", err)
	}
	if _, err := l.ValuesAt(l.Version()); err != nil {
		t.Fatalf("ValuesAt at the current version = %v, want an answer", err)
	}
}

// Collecting against a version some replica has not reached strands it, and the
// point is that it is told.
func TestCollectingAListTooEarlyIsReported(t *testing.T) {
	a, b := NewList(1), NewList(2)
	mine, err := a.Insert(0, []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(mine...); err != nil {
		t.Fatal(err)
	}
	theirs, err := b.Insert(b.Len(), []byte("two"))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(theirs...); err != nil {
		t.Fatal(err)
	}
	// b writes after the element a is about to take away, not having heard.
	late, err := b.Insert(1, []byte("between"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Delete(0, 1); err != nil {
		t.Fatal(err)
	}
	if n := a.Collect(a.Version()); n != 1 {
		t.Fatalf("collected %d elements, want 1", n)
	}
	if err := a.Apply(late...); !errors.Is(err, ErrStranded) {
		t.Fatalf("work anchored to a collected element = %v, want ErrStranded", err)
	}
	if a.Pending() != 0 {
		t.Fatalf("%d operations were parked as well as reported", a.Pending())
	}
}

// Nothing to collect is not an error and changes nothing.
func TestCollectingAListWithNothingToTake(t *testing.T) {
	l := NewList(1)
	for i := 0; i < 40; i++ {
		if _, err := l.Insert(l.Len(), []byte("stays")); err != nil {
			t.Fatal(err)
		}
	}
	before := len(l.Snapshot())
	if n := l.Collect(l.Version()); n != 0 {
		t.Fatalf("collected %d elements from a list nothing was deleted from", n)
	}
	if len(l.Snapshot()) != before || len(l.Floor()) != 0 {
		t.Fatal("collecting nothing changed something")
	}
	if _, err := l.Delete(0, 1); err != nil {
		t.Fatal(err)
	}
	if n := l.Collect(VersionVector{}); n != 0 {
		t.Fatalf("collected %d elements against a version that has seen nothing", n)
	}
}

// A version 1 list snapshot — the format before collection was written down —
// still loads, and says the same thing as the same list written today. Without
// this the reader for it stops being exercised the moment the current version
// moves on, which is how a format that claims to accept an older one quietly
// stops doing so.
func TestLoadListStillAcceptsVersionOne(t *testing.T) {
	old := wellFormedList()
	old.version = listVersionV1
	raw := old.build()
	if raw[4] != listVersionV1 {
		t.Fatalf("the fixture wrote version %d, want %d", raw[4], listVersionV1)
	}
	was, err := LoadList(2, raw)
	if err != nil {
		t.Fatalf("a version 1 list snapshot did not load: %v", err)
	}
	now, err := LoadList(2, wellFormedList().build())
	if err != nil {
		t.Fatalf("LoadList: %v", err)
	}
	if !sameValues(was.Values(), now.Values()) {
		t.Fatal("version 1 and the current version read differently")
	}
	if was.Tombstones() != now.Tombstones() {
		t.Fatalf("version 1 keeps %d tombstones, the current version %d",
			was.Tombstones(), now.Tombstones())
	}
	if fresh := was.Snapshot(); fresh[4] != listVersion {
		t.Fatalf("re-encoding wrote version %d, want %d", fresh[4], listVersion)
	}
	if len(was.Floor()) != 0 {
		t.Fatal("a list loaded from version 1 came back with a floor")
	}
}

// The version 2 header is a trust boundary, the same one the text's is.
func TestLoadListRejectsAMalformedCollectionHeader(t *testing.T) {
	for _, c := range []struct {
		name   string
		break_ func(b *listBuilder)
	}{
		{"a floor at sequence zero", func(b *listBuilder) { b.floor = [][2]uint64{{1, 0}} }},
		{"a floor above the clock ceiling", func(b *listBuilder) { b.floor = [][2]uint64{{1, MaxClock + 1}} }},
		{"a floor naming operations the list has not seen", func(b *listBuilder) { b.floor = [][2]uint64{{1, 4}} }},
		{"a site in the floor twice", func(b *listBuilder) { b.floor = [][2]uint64{{1, 1}, {1, 2}} }},
		{"more floor entries than there are bytes", func(b *listBuilder) {
			b.floor = [][2]uint64{{1, 1}}
			b.floorCount = 1 << 20
		}},
		{"a tally of nothing", func(b *listBuilder) { b.gone = [][2]uint64{{1, 0}} }},
		{"a tally larger than the site ever issued", func(b *listBuilder) { b.gone = [][2]uint64{{1, 4}} }},
		{"a site tallied twice", func(b *listBuilder) {
			b.floor = [][2]uint64{{1, 3}}
			b.gone = [][2]uint64{{1, 1}, {1, 2}}
		}},
		{"more tallies than there are bytes", func(b *listBuilder) {
			b.gone = [][2]uint64{{1, 1}}
			b.goneCount = 1 << 20
		}},
		{"a tally the elements do not account for", func(b *listBuilder) {
			b.floor = [][2]uint64{{1, 2}}
			b.gone = [][2]uint64{{1, 1}}
		}},
		{"a tally the floor does not cover", func(b *listBuilder) { b.gone = [][2]uint64{{1, 1}} }},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := wellFormedList()
			c.break_(&b)
			if _, err := LoadList(2, b.build()); !errors.Is(err, ErrMalformed) {
				t.Fatalf("LoadList = %v, want ErrMalformed", err)
			}
		})
	}
}

// Collecting an element two replicas removed at once takes their duplicate
// deletion with it, which the loader would otherwise refuse to place.
func TestCollectingAnElementTwoReplicasRemovedAtOnce(t *testing.T) {
	a, b := NewList(1), NewList(2)
	mine, err := a.Insert(0, []byte("doomed"))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(mine...); err != nil {
		t.Fatal(err)
	}
	keep, err := a.Insert(a.Len(), []byte("kept"))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(keep...); err != nil {
		t.Fatal(err)
	}
	// Both remove the same element without hearing the other first.
	hers, err := a.Delete(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	his, err := b.Delete(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(his...); err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(hers...); err != nil {
		t.Fatal(err)
	}
	if a.Tombstones() != 1 {
		t.Fatalf("%d tombstones, want 1", a.Tombstones())
	}
	if n := a.Collect(a.Version()); n != 1 {
		t.Fatalf("collected %d elements, want 1", n)
	}
	back, err := LoadList(3, a.Snapshot())
	if err != nil {
		t.Fatalf("a list whose duplicate deletion was collected did not reload: %v", err)
	}
	if !sameValues(back.Values(), a.Values()) {
		t.Fatal("the reloaded list holds something else")
	}
}

// An element two replicas removed at once stays while either of those removals
// is one somebody might not have. Collecting it would leave the second removal
// with nothing to be recorded against.
func TestADuplicateDeletionThatIsNotStableKeepsTheElement(t *testing.T) {
	a, b := NewList(1), NewList(2)
	mine, err := a.Insert(0, []byte("doomed"))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(mine...); err != nil {
		t.Fatal(err)
	}
	hers, err := a.Delete(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	his, err := b.Delete(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(his...); err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(hers...); err != nil {
		t.Fatal(err)
	}

	// A version that has a's removal but not b's: b's is the one that could
	// still be on its way to somebody.
	partial := VersionVector{}
	for site, seq := range a.Version() {
		if site == 2 {
			continue
		}
		partial[site] = seq
	}
	if n := a.Collect(partial); n != 0 {
		t.Fatalf("collected %d elements while a removal of one was not stable", n)
	}
	// With both, it goes.
	if n := a.Collect(a.Version()); n != 1 {
		t.Fatalf("collected %d elements once both removals were stable, want 1", n)
	}
}

// A deletion naming a collected element is stranded too, not only an insertion —
// and ApplyChanges reports it as Apply does, rather than only one of the two.
func TestADeletionOfACollectedElementIsStranded(t *testing.T) {
	a, b := NewList(1), NewList(2)
	mine, err := a.Insert(0, []byte("gone"))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(mine...); err != nil {
		t.Fatal(err)
	}
	keep, err := a.Insert(a.Len(), []byte("kept"))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(keep...); err != nil {
		t.Fatal(err)
	}
	// b removes the first element without having heard that a removed it too.
	late, err := b.Delete(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Delete(0, 1); err != nil {
		t.Fatal(err)
	}
	if n := a.Collect(a.Version()); n != 1 {
		t.Fatalf("collected %d elements, want 1", n)
	}
	if _, err := a.ApplyChanges(late...); !errors.Is(err, ErrStranded) {
		t.Fatalf("ApplyChanges of a deletion naming a collected element = %v, want ErrStranded", err)
	}
	if a.Pending() != 0 {
		t.Fatalf("%d operations were parked as well as reported", a.Pending())
	}
}

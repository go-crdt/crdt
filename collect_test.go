package crdt

import (
	"errors"
	"math/rand/v2"
	"testing"
)

// The claim collection has to earn: a replica that has collected and one that
// has not must still agree, whatever they do next and in whatever order they
// hear about it.
func TestCollectedAndUncollectedStillAgree(t *testing.T) {
	for seed := uint64(1); seed <= 400; seed++ {
		r := rand.New(rand.NewPCG(seed, 7))
		a, b := New(1), New(2)

		// A shared history, written by both and heard by both.
		for round := 0; round < 120; round++ {
			from, to := a, b
			if r.IntN(2) == 0 {
				from, to = b, a
			}
			var ops []Op
			var err error
			if from.Len() > 0 && r.IntN(3) == 0 {
				at := r.IntN(from.Len())
				n := 1 + r.IntN(min(8, from.Len()-at))
				ops, err = from.Delete(at, n)
			} else {
				at := 0
				if from.Len() > 0 {
					at = r.IntN(from.Len() + 1)
				}
				ops, err = from.Insert(at, string(rune('a'+r.IntN(26))))
			}
			if err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
			if err := to.Apply(ops...); err != nil {
				t.Fatalf("seed %d: apply: %v", seed, err)
			}
		}
		if a.String() != b.String() {
			t.Fatalf("seed %d: the two disagreed before anything was collected", seed)
		}

		// Both have everything, so the stable version is what they share.
		stable := a.Version()
		collected := a.Collect(stable)
		if a.String() != b.String() {
			t.Fatalf("seed %d: collecting %d characters changed the text", seed, collected)
		}

		// Now they carry on independently and exchange afterwards, which is the
		// case that would expose a placement that moved.
		var fromA, fromB []Op
		for round := 0; round < 40; round++ {
			at := 0
			if a.Len() > 0 {
				at = r.IntN(a.Len() + 1)
			}
			ops, err := a.Insert(at, string(rune('A'+r.IntN(26))))
			if err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
			fromA = append(fromA, ops...)

			at = 0
			if b.Len() > 0 {
				at = r.IntN(b.Len() + 1)
			}
			ops, err = b.Insert(at, string(rune('0'+r.IntN(10))))
			if err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
			fromB = append(fromB, ops...)
		}
		// Delivered in an order neither of them chose.
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
		if a.String() != b.String() {
			t.Fatalf("seed %d: after collecting %d characters the replicas disagree:\n a=%q\n b=%q",
				seed, collected, a.String(), b.String())
		}
	}
}

// revisedDoc writes a document the way one is actually written: text added, some
// of it taken away again, which is the only shape with anything to collect.
func revisedDoc(t *testing.T, edits int) *Doc {
	t.Helper()
	const line = "a sentence somebody wrote, and then thought about again. "
	doc := New(1)
	for n := 0; n < edits; {
		if _, err := doc.Insert(doc.Len(), line); err != nil {
			t.Fatal(err)
		}
		n++
		if n%3 == 0 && doc.Len() >= len(line) {
			if _, err := doc.Delete(0, len(line)); err != nil {
				t.Fatal(err)
			}
			n++
		}
	}
	return doc
}

// Collecting makes the document smaller and leaves it saying the same thing,
// still editable at both ends and in the middle.
func TestCollectShrinksAndKeepsTheText(t *testing.T) {
	doc := revisedDoc(t, 2000)
	before, text := len(doc.Snapshot()), doc.String()
	n := doc.Collect(doc.Version())
	if n == 0 {
		t.Fatal("nothing was collected from a document two thirds of which is deleted")
	}
	if doc.String() != text {
		t.Fatal("collecting changed the text")
	}
	if doc.Len() != len([]rune(text)) {
		t.Fatalf("length %d, want %d", doc.Len(), len([]rune(text)))
	}
	after := len(doc.Snapshot())
	if after >= before {
		t.Fatalf("the snapshot did not shrink: %d bytes became %d", before, after)
	}
	t.Logf("collected %d characters; %d bytes became %d (%.2fx)", n, before, after, float64(before)/float64(after))

	for _, at := range []int{0, doc.Len(), doc.Len() / 2} {
		if _, err := doc.Insert(at, "|"); err != nil {
			t.Fatalf("editing at %d after collecting: %v", at, err)
		}
	}
}

// A collected document has to survive being written down and read back, which is
// what the version 6 format exists for: the loader counts the operations it
// reads and the snapshot tells it how many are missing, so the accounting stays
// exact rather than being relaxed to let a gap through.
func TestCollectedDocumentReloads(t *testing.T) {
	doc := revisedDoc(t, 300)
	n := doc.Collect(doc.Version())
	text, floor := doc.String(), doc.Floor()

	back, err := Load(1, doc.Snapshot())
	if err != nil {
		t.Fatalf("a collected document did not reload: %v", err)
	}
	if back.String() != text {
		t.Fatal("the reloaded document says something else")
	}
	if back.Len() != len([]rune(text)) {
		t.Fatalf("reloaded length %d, want %d", back.Len(), len([]rune(text)))
	}
	// The floor has to survive too, or a document forgets that it must refuse.
	for site, seq := range floor {
		if back.Floor()[site] != seq {
			t.Fatalf("the floor did not survive the round trip: site %d is %d, want %d",
				site, back.Floor()[site], seq)
		}
	}
	if n == 0 || len(back.Floor()) == 0 {
		t.Fatalf("collected %d characters but the reloaded floor is empty", n)
	}
	// And it must still take work from a peer that never collected.
	peer, err := Load(2, doc.Snapshot())
	if err != nil {
		t.Fatalf("a second replica could not load it: %v", err)
	}
	ops, err := peer.Insert(peer.Len(), " and more")
	if err != nil {
		t.Fatal(err)
	}
	if err := back.Apply(ops...); err != nil {
		t.Fatalf("the reloaded document refused a peer's work: %v", err)
	}
	if back.Pending() != 0 {
		t.Fatalf("%d operations from a peer were stranded", back.Pending())
	}
	if back.String() != peer.String() {
		t.Fatal("the two disagree after an ordinary edit")
	}
}

// Reading the past below the floor is refused rather than answered wrongly.
func TestHistoryRefusesBelowTheFloor(t *testing.T) {
	// Two sites, so that what one of them types is a run of its own and can die
	// whole: a site typing straight on extends the run it is already in, and a
	// run only part of which is deleted is not collectible.
	a, b := New(1), New(2)
	mine, err := a.Insert(0, "gone")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(mine...); err != nil {
		t.Fatal(err)
	}
	early := a.Version()

	theirs, err := b.Insert(b.Len(), "kept")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(theirs...); err != nil {
		t.Fatal(err)
	}
	cut, err := a.Delete(0, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(cut...); err != nil {
		t.Fatal(err)
	}

	// Before collecting, the past is readable and says what it said.
	was, err := a.TextAt(early)
	if err != nil {
		t.Fatalf("TextAt before collecting: %v", err)
	}
	if was != "gone" {
		t.Fatalf("the document at the early version reads %q, want %q", was, "gone")
	}
	if len(a.Floor()) != 0 {
		t.Fatal("a document that has never collected has a floor")
	}

	// Both have everything, so what they share is stable.
	if n := a.Collect(a.Version()); n != 4 {
		t.Fatalf("collected %d characters, want the 4 that died together", n)
	}
	if a.String() != "kept" {
		t.Fatalf("after collecting the document reads %q, want %q", a.String(), "kept")
	}
	for _, ask := range []struct {
		name string
		err  error
	}{
		{"TextAt", func() error { _, err := a.TextAt(early); return err }()},
		{"LenAt", func() error { _, err := a.LenAt(early); return err }()},
		{"ChangesSince", func() error { _, err := a.ChangesSince(early); return err }()},
	} {
		if !errors.Is(ask.err, ErrCollected) {
			t.Fatalf("%s below the floor = %v, want ErrCollected", ask.name, ask.err)
		}
	}
	// At or above the floor it still answers.
	if _, err := a.TextAt(a.Version()); err != nil {
		t.Fatalf("TextAt at the current version = %v, want an answer", err)
	}
}

// Collection given a version some replica has not reached strands that replica,
// and the point of ErrStranded is that it is told rather than left to wait.
func TestCollectingTooEarlyIsReportedNotSwallowed(t *testing.T) {
	a, b := New(1), New(2)
	mine, err := a.Insert(0, "AAA")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(mine...); err != nil {
		t.Fatal(err)
	}
	theirs, err := b.Insert(b.Len(), "BBB")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(theirs...); err != nil {
		t.Fatal(err)
	}

	// b writes inside a's run, anchored to a character of it, and has not yet
	// heard that a is about to take that run away.
	late, err := b.Insert(1, "!")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.Delete(0, 3); err != nil {
		t.Fatal(err)
	}
	// The precondition broken deliberately: b has not delivered the deletion, so
	// a's own version is not a version every replica has reached.
	if n := a.Collect(a.Version()); n != 3 {
		t.Fatalf("collected %d characters, want 3", n)
	}

	if err := a.Apply(late...); !errors.Is(err, ErrStranded) {
		t.Fatalf("applying work anchored to a collected character = %v, want ErrStranded", err)
	}
	if a.Pending() != 0 {
		t.Fatalf("%d operations were parked as well as reported", a.Pending())
	}
	// And a replica that never collected still takes the same work happily.
	fresh := New(3)
	if err := fresh.Apply(mine...); err != nil {
		t.Fatal(err)
	}
	if err := fresh.Apply(theirs...); err != nil {
		t.Fatal(err)
	}
	if err := fresh.Apply(late...); err != nil {
		t.Fatalf("a replica that never collected refused the same work: %v", err)
	}
}

// Nothing to collect is not an error and not a rebuild: a document nobody has
// deleted from keeps every byte it had.
func TestCollectingWhenThereIsNothingToTake(t *testing.T) {
	doc := New(1)
	for i := 0; i < 50; i++ {
		if _, err := doc.Insert(doc.Len(), "text that stays. "); err != nil {
			t.Fatal(err)
		}
	}
	before, text := len(doc.Snapshot()), doc.String()
	if n := doc.Collect(doc.Version()); n != 0 {
		t.Fatalf("collected %d characters from a document nothing was deleted from", n)
	}
	if doc.String() != text {
		t.Fatal("collecting nothing changed the text")
	}
	if len(doc.Snapshot()) != before {
		t.Fatal("collecting nothing changed the snapshot")
	}
	if len(doc.Floor()) != 0 {
		t.Fatal("collecting nothing raised the floor")
	}
	// A deletion no stable version covers stays as well.
	if _, err := doc.Delete(0, 5); err != nil {
		t.Fatal(err)
	}
	if n := doc.Collect(VersionVector{}); n != 0 {
		t.Fatalf("collected %d characters against a version that has seen nothing", n)
	}
}

// A deletion naming a character that was collected is stranded too, not only an
// insertion.
func TestADeletionOfACollectedCharacterIsStranded(t *testing.T) {
	a, b := New(1), New(2)
	mine, err := a.Insert(0, "AAA")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(mine...); err != nil {
		t.Fatal(err)
	}
	theirs, err := b.Insert(b.Len(), "BBB")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(theirs...); err != nil {
		t.Fatal(err)
	}
	// b removes a character of a's run without having heard that a removed the
	// whole of it.
	late, err := b.Delete(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Delete(0, 3); err != nil {
		t.Fatal(err)
	}
	if n := a.Collect(a.Version()); n != 3 {
		t.Fatalf("collected %d characters, want 3", n)
	}
	if err := a.Apply(late...); !errors.Is(err, ErrStranded) {
		t.Fatalf("a deletion naming a collected character = %v, want ErrStranded", err)
	}
}

// Collection spanning more than one site: the floor and the tallies are written
// per site, and a document only one site ever wrote to would never show whether
// the other entries are written in an order a loader can read.
func TestCollectingAcrossTwoSites(t *testing.T) {
	a, b := New(7), New(3)
	exchange := func(ops []Op, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Apply(ops...); err != nil {
			t.Fatal(err)
		}
		if err := b.Apply(ops...); err != nil {
			t.Fatal(err)
		}
	}
	exchange(a.Insert(0, "aaa"))
	exchange(b.Insert(b.Len(), "bbb"))
	exchange(a.Insert(a.Len(), "keep"))
	// Both of the early runs die, one from each site.
	exchange(a.Delete(0, 6))

	n := a.Collect(a.Version())
	if n != 6 {
		t.Fatalf("collected %d characters, want the 6 that died", n)
	}
	if a.String() != "keep" {
		t.Fatalf("after collecting the document reads %q, want %q", a.String(), "keep")
	}
	if len(a.Floor()) < 2 {
		t.Fatalf("the floor names %d sites, want both", len(a.Floor()))
	}
	back, err := Load(9, a.Snapshot())
	if err != nil {
		t.Fatalf("a document collected across two sites did not reload: %v", err)
	}
	if back.String() != "keep" {
		t.Fatalf("the reloaded document reads %q, want %q", back.String(), "keep")
	}
	if len(back.Floor()) != len(a.Floor()) {
		t.Fatalf("the reloaded floor names %d sites, want %d", len(back.Floor()), len(a.Floor()))
	}
}

// What collection is worth on a real editing history rather than a made-up one:
// the automerge paper, a quarter of a million edits at positions a person chose.
// A synthetic document can be built to make any answer look right, and this one
// cannot.
func TestCollectionOnTheRealEditingTrace(t *testing.T) {
	patches, _ := loadTrace(t)
	d := New(1)
	replay(t, d, patches)

	before, tombs, text := len(d.Snapshot()), d.Tombstones(), d.String()
	n := d.Collect(d.Version())
	after := len(d.Snapshot())

	if d.String() != text {
		t.Fatal("collection changed a real document's text")
	}
	if n == 0 {
		t.Fatal("nothing was collected from a document three fifths of which is tombstones")
	}
	back, err := Load(2, d.Snapshot())
	if err != nil {
		t.Fatalf("a real collected document did not reload: %v", err)
	}
	if back.String() != text {
		t.Fatal("the reloaded document says something else")
	}
	t.Logf("%d characters, %d tombstones; collected %d of them (%.1f%%)",
		len([]rune(text)), tombs, n, 100*float64(n)/float64(tombs))
	t.Logf("snapshot %d -> %d bytes (%.2fx)", before, after, float64(before)/float64(after))

	// The number this exists to defend: most of a real document's tombstones can
	// go, and the ones that stay are the ones a survivor still needs.
	if share := float64(n) / float64(tombs); share < 0.5 {
		t.Fatalf("only %.1f%% of the tombstones were collectible; the rule has stopped working", 100*share)
	}
}

// A peer whose version is below the floor cannot be caught up with a difference:
// what OpsSince has left has holes, and holes park. CanReplay is what a caller
// asks before choosing between a difference and a snapshot.
func TestCanReplayIsFalseBelowTheFloor(t *testing.T) {
	a, b := New(1), New(2)
	mine, err := a.Insert(0, "AAA")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(mine...); err != nil {
		t.Fatal(err)
	}
	behind := b.Version()
	theirs, err := b.Insert(b.Len(), "BBB")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(theirs...); err != nil {
		t.Fatal(err)
	}
	if !a.CanReplay(behind) {
		t.Fatal("a replica that has collected nothing said it could not replay")
	}
	if _, err := a.Delete(0, 3); err != nil {
		t.Fatal(err)
	}
	if n := a.Collect(a.Version()); n != 3 {
		t.Fatalf("collected %d characters, want 3", n)
	}
	if a.CanReplay(behind) {
		t.Fatal("a collected replica claimed it could still replay from below its floor")
	}
	if !a.CanReplay(a.Version()) {
		t.Fatal("a collected replica refused to replay from its own version")
	}
	// The peer is re-seeded from a snapshot instead, and the two then agree.
	fresh, err := Load(2, a.Snapshot())
	if err != nil {
		t.Fatalf("re-seeding from a snapshot: %v", err)
	}
	if fresh.String() != a.String() {
		t.Fatalf("re-seeded to %q, want %q", fresh.String(), a.String())
	}
}

// The point of the change: a difference below the floor is refused rather than
// handed over with holes in it.
//
// A replica applying a batch with a gap in a site's sequence numbers parks
// everything after the gap and says nothing — every operation in it is
// well-formed, and the one that would let the rest through is not coming. So
// the replica that would have sent it says no, and the caller sends a snapshot.
func TestOpsSinceRefusesBelowTheFloor(t *testing.T) {
	a, b := New(1), New(2)
	mine, err := a.Insert(0, "AAA")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(mine...); err != nil {
		t.Fatal(err)
	}
	behind := b.Version()
	theirs, err := b.Insert(b.Len(), "BBB")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(theirs...); err != nil {
		t.Fatal(err)
	}

	// Before collecting, a difference from where b stood is an answer.
	if _, err := a.OpsSince(behind); err != nil {
		t.Fatalf("OpsSince before collecting = %v, want an answer", err)
	}
	if _, err := a.Delete(0, 3); err != nil {
		t.Fatal(err)
	}
	if n := a.Collect(a.Version()); n != 3 {
		t.Fatalf("collected %d characters, want 3", n)
	}
	if _, err := a.OpsSince(behind); !errors.Is(err, ErrCollected) {
		t.Fatalf("OpsSince below the floor = %v, want ErrCollected", err)
	}
	// From its own version, and from anything at or above the floor, it still
	// answers.
	if _, err := a.OpsSince(a.Version()); err != nil {
		t.Fatalf("OpsSince at the current version = %v, want an answer", err)
	}
}

// A list refuses the same way, and a composite refuses for the whole document
// rather than a part at a time.
func TestListAndCompositeOpsSinceRefuseBelowTheFloor(t *testing.T) {
	l := NewList(1)
	if _, err := l.Insert(0, []byte("one")); err != nil {
		t.Fatal(err)
	}
	early := l.Version()
	peer := NewList(2)
	if err := peer.Apply(must(l.OpsSince(nil))...); err != nil {
		t.Fatal(err)
	}
	theirs, err := peer.Insert(peer.Len(), []byte("two"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Apply(theirs...); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Delete(0, 1); err != nil {
		t.Fatal(err)
	}
	if n := l.Collect(l.Version()); n != 1 {
		t.Fatalf("collected %d elements, want 1", n)
	}
	if _, err := l.OpsSince(early); !errors.Is(err, ErrCollected) {
		t.Fatalf("List.OpsSince below the floor = %v, want ErrCollected", err)
	}

	c := NewComposite(1)
	body, err := c.Text("body")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := body.Insert(0, "AAA"); err != nil {
		t.Fatal(err)
	}
	earlyComposite := c.Version()
	other := NewComposite(2)
	if err := other.Apply(must(c.OpsSince(nil))...); err != nil {
		t.Fatal(err)
	}
	otherBody, err := other.Text("body")
	if err != nil {
		t.Fatal(err)
	}
	more, err := otherBody.Insert(otherBody.Len(), "BBB")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Apply(PartOps{Part: Part{Kind: PartText, Name: "body"}, Text: more}); err != nil {
		t.Fatal(err)
	}
	if _, err := body.Delete(0, 3); err != nil {
		t.Fatal(err)
	}
	if n := c.Collect(c.Version()); n != 3 {
		t.Fatalf("collected %d, want 3", n)
	}
	if _, err := c.OpsSince(earlyComposite); !errors.Is(err, ErrCollected) {
		t.Fatalf("Composite.OpsSince below the floor = %v, want ErrCollected", err)
	}
	if _, err := c.OpsSince(c.Version()); err != nil {
		t.Fatalf("Composite.OpsSince at the current version = %v, want an answer", err)
	}
}

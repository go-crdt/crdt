package crdt

import (
	"errors"
	"testing"
)

// revisedText writes a document the way one is written, so that whole runs die.
func revisedText(t *testing.T, edits int) *Doc {
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

// Purging discards characters nobody can read and changes nothing anybody can.
func TestPurgeKeepsWhatTheDocumentSays(t *testing.T) {
	doc := revisedText(t, 600)
	want, tombs := doc.String(), doc.Tombstones()
	n := doc.Purge()
	if n == 0 {
		t.Fatal("nothing was purged from a document two thirds of which is deleted")
	}
	if got := doc.String(); got != want {
		t.Fatalf("purging changed the text: %d characters became %d", len([]rune(want)), len([]rune(got)))
	}
	if doc.Len() != len([]rune(want)) {
		t.Fatalf("length %d, want %d", doc.Len(), len([]rune(want)))
	}
	if doc.Tombstones() != tombs {
		t.Fatalf("%d tombstones after purging, want %d — the identities have to stay",
			doc.Tombstones(), tombs)
	}
	t.Logf("purged %d characters of %d", n, tombs)

	// And it still takes edits at both ends and in the middle.
	for _, at := range []int{0, doc.Len(), doc.Len() / 2} {
		if _, err := doc.Insert(at, "|"); err != nil {
			t.Fatalf("editing at %d after purging: %v", at, err)
		}
	}
	// Purging twice takes nothing the second time.
	if again := doc.Purge(); again != 0 {
		t.Fatalf("purging again discarded %d characters", again)
	}
}

// A purged document has to survive being written down and read back, with the
// characters still missing and everything else where it was.
func TestPurgedDocumentReloads(t *testing.T) {
	doc := revisedText(t, 300)
	before := len(doc.Snapshot())
	n := doc.Purge()
	if n == 0 {
		t.Fatal("nothing was purged")
	}
	after := len(doc.Snapshot())
	if after >= before {
		t.Fatalf("the snapshot did not shrink: %d bytes became %d", before, after)
	}
	t.Logf("purged %d characters; %d bytes became %d (%.2fx)", n, before, after, float64(before)/float64(after))

	back, err := Load(2, doc.Snapshot())
	if err != nil {
		t.Fatalf("a purged document did not reload: %v", err)
	}
	if back.String() != doc.String() {
		t.Fatal("the reloaded document says something else")
	}
	if back.Tombstones() != doc.Tombstones() {
		t.Fatalf("the reloaded document holds %d tombstones, want %d",
			back.Tombstones(), doc.Tombstones())
	}
	// It re-encodes to the same bytes: purging is part of what a snapshot says.
	if string(back.Snapshot()) != string(doc.Snapshot()) {
		t.Fatal("re-encoding a purged document did not reproduce it")
	}
	// And it still takes work from a peer that never purged.
	peer, err := Load(3, doc.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	ops, err := peer.Insert(peer.Len(), " and more")
	if err != nil {
		t.Fatal(err)
	}
	if err := back.Apply(ops...); err != nil {
		t.Fatalf("a purged document refused a peer's work: %v", err)
	}
	if back.Pending() != 0 {
		t.Fatalf("%d operations were stranded", back.Pending())
	}
	if back.String() != peer.String() {
		t.Fatal("the two disagree after an ordinary edit")
	}
}

// A purged run whose characters are not all deleted is refused rather than
// repaired: a character with nothing in it would otherwise be visible, and
// nothing sound could have written one.
//
// This is the reader's own rejection, and until the fixture builder could set
// the flag nothing could reach it — every other test in this file round-trips a
// document that [Doc.Purge] produced, which is well formed by construction.
func TestLoadRefusesAPurgedRunThatIsStillPartlyAlive(t *testing.T) {
	// The control comes from the only thing that writes these for real, so that
	// the rejection below cannot be passing merely because the flag was set.
	doc := revisedText(t, 40)
	if doc.Purge() == 0 {
		t.Fatal("nothing was purged, so this proves nothing")
	}
	if _, err := Load(2, doc.Snapshot()); err != nil {
		t.Fatalf("a document Purge wrote did not load: %v", err)
	}

	// Four characters, one deletion covering one of them, and the run claiming
	// its characters were discarded. The other three have nothing behind them.
	partly := wellFormedRun()
	partly.runs[0].purged = true
	partly.runs[0].text = nil // a purged run costs nothing in the text column
	partly.runs[0].length = 4
	if _, err := Load(2, partly.build()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a partly deleted purged run loaded with %v, want ErrMalformed", err)
	}
}

// A document that reloads has to remember what it gave up.
//
// Without this the floor comes back as zero, and readable would answer that
// every version is still serveable — telling a caller it can serve a peer whose
// history it has thrown away. Not reachable while nothing calls readable, and
// exactly the defect a map's collection floor had until it was written down.
func TestThePurgeFloorSurvivesASnapshot(t *testing.T) {
	doc := revisedText(t, 40)
	if doc.Purge() == 0 {
		t.Fatal("nothing was purged, so this proves nothing")
	}
	before := doc.PurgedBelow()
	if before == 0 {
		t.Fatal("a document that purged reports a floor of zero")
	}

	back, err := Load(2, doc.Snapshot())
	if err != nil {
		t.Fatalf("a purged document did not reload: %v", err)
	}
	if got := back.PurgedBelow(); got != before {
		t.Fatalf("PurgedBelow() = %d after a round trip, want %d", got, before)
	}

	// And a document that never purged still says so, rather than inheriting a
	// floor from the field being written at all.
	fresh := revisedText(t, 4)
	plain, err := Load(2, fresh.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if got := plain.PurgedBelow(); got != 0 {
		t.Fatalf("a document that never purged reloaded with a floor of %d", got)
	}
}

// A floor above the clock ceiling names a clock no operation could carry.
func TestLoadRefusesAPurgeFloorAboveTheCeiling(t *testing.T) {
	tooHigh := wellFormedRun()
	tooHigh.purgedBelow = MaxClock + 1
	if _, err := Load(2, tooHigh.build()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a floor above the ceiling loaded with %v, want ErrMalformed", err)
	}
}

// readable is the refusal this needs, and nothing calls it yet, so what it
// promises is pinned here rather than by a caller.
//
// It answers whether a version is late enough that nothing purged was still
// visible in it: a peer at such a version can be served, and one behind it
// cannot, because the characters it would need are gone.
func TestReadableAnswersForAVersionBehindThePurge(t *testing.T) {
	// A document that has purged nothing can serve anybody, including a version
	// that has seen nothing at all.
	fresh := revisedText(t, 4)
	if !fresh.readable(VersionVector{}) {
		t.Fatal("a document that purged nothing refused the empty version")
	}

	doc := revisedText(t, 40)
	// The version as it stood when everything was still there. Taken before the
	// purge, so it is exactly a peer that stopped listening early.
	early := doc.Version().Clone()
	if doc.Purge() == 0 {
		t.Fatal("nothing was purged, so this proves nothing")
	}

	// That peer saw the insertions and, for at least one purged character, not
	// the deletion that removed it -- so it cannot be served.
	behind := VersionVector{}
	for site, seq := range early {
		behind[site] = seq / 2
	}
	if doc.readable(behind) {
		t.Fatal("a version from before the deletions was reported serveable")
	}

	// And the version the document itself is at has every deletion, so nothing
	// purged was visible in it.
	if !doc.readable(doc.Version()) {
		t.Fatal("a document refused its own version")
	}
}

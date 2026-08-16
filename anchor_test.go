package crdt

import (
	"errors"
	"math/rand/v2"
	"testing"
)

// An anchor names a character, and an offset names a place. The difference is
// the whole point: everything below is a way of asking whether the anchor still
// names the same character after the document has moved underneath it.

func TestAnchorTracksItsCharacter(t *testing.T) {
	d := New(1)
	insert(t, d, 0, "the quick brown fox")

	anchor, err := d.Anchor(10) // the "b" of brown
	if err != nil {
		t.Fatalf("Anchor: %v", err)
	}
	if got, want := charAt(t, d, anchor), 'b'; got != want {
		t.Fatalf("the anchor names %q, want %q", got, want)
	}

	// Editing before it moves the offset and not the character.
	insert(t, d, 0, ">> ")
	pos, ok := d.Position(anchor)
	if !ok || pos != 13 {
		t.Fatalf("Position after inserting before = %d, %v; want 13, true", pos, ok)
	}
	if got := charAt(t, d, anchor); got != 'b' {
		t.Fatalf("the anchor now names %q", got)
	}

	// Editing after it moves nothing.
	insert(t, d, d.Len(), " indeed")
	if pos, _ := d.Position(anchor); pos != 13 {
		t.Fatalf("Position after inserting after = %d, want 13", pos)
	}

	// Deleting before it closes the gap.
	remove(t, d, 0, 3)
	if pos, _ := d.Position(anchor); pos != 10 {
		t.Fatalf("Position after deleting before = %d, want 10", pos)
	}
}

// charAt reads the character an anchor names, through the public surface.
func charAt(t *testing.T, d *Doc, anchor ID) rune {
	t.Helper()
	pos, ok := d.Position(anchor)
	if !ok {
		t.Fatalf("Position(%v) reports the character is unknown", anchor)
	}
	return []rune(d.String())[pos]
}

// A deleted character still has a place, which is where a comment on it belongs.
func TestAnchorToADeletedCharacter(t *testing.T) {
	d := New(1)
	insert(t, d, 0, "keep DELETE keep")
	anchor, err := d.Anchor(5) // the "D"
	if err != nil {
		t.Fatal(err)
	}
	if !d.Visible(anchor) {
		t.Fatal("the character is reported as gone before anything deleted it")
	}

	remove(t, d, 5, 7) // "DELETE "
	if d.Visible(anchor) {
		t.Fatal("the character is reported as present after being deleted")
	}
	pos, ok := d.Position(anchor)
	if !ok {
		t.Fatal("a deleted character has no position")
	}
	if pos != 5 {
		t.Fatalf("Position of the deleted character = %d, want 5 — where the text closed up", pos)
	}
	if got, want := d.String(), "keep keep"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

// The end of the document is a place too, and the one insertions there do not
// move.
func TestAnchorAtTheEnd(t *testing.T) {
	d := New(1)
	insert(t, d, 0, "abc")
	anchor, err := d.Anchor(d.Len())
	if err != nil {
		t.Fatal(err)
	}
	if !anchor.IsRoot() {
		t.Fatalf("Anchor at the end = %v, want the zero ID", anchor)
	}
	if !d.Visible(anchor) {
		t.Fatal("the end of the document is reported as gone")
	}
	insert(t, d, 3, "de")
	pos, ok := d.Position(anchor)
	if !ok || pos != d.Len() {
		t.Fatalf("Position of the end = %d, %v; want %d, true", pos, ok, d.Len())
	}
}

func TestAnchorErrors(t *testing.T) {
	d := New(1)
	insert(t, d, 0, "abc")
	for _, pos := range []int{-1, 4} {
		if _, err := d.Anchor(pos); !errors.Is(err, ErrOutOfRange) {
			t.Errorf("Anchor(%d) = %v, want ErrOutOfRange", pos, err)
		}
	}
	if pos, ok := d.Position(ID{Site: 9, Seq: 9}); ok {
		t.Errorf("Position of an unknown anchor = %d, true; want it reported unknown", pos)
	}
	if d.Visible(ID{Site: 9, Seq: 9}) {
		t.Error("an unknown anchor is reported as visible")
	}
}

// The property a comment anchor actually depends on: an anchor taken on one
// replica names the same character on another, after concurrent editing that
// neither replica had seen when the anchor was taken.
func TestAnchorMeansTheSameOnEveryReplica(t *testing.T) {
	for seed := range uint64(60) {
		rng := rand.New(rand.NewPCG(seed, 17))
		a, b := New(1), New(2)
		seedOps := insert(t, a, 0, "the quick brown fox jumps over the lazy dog")
		apply(t, b, seedOps)

		// Anchor every character, on a.
		anchors := make([]ID, a.Len())
		for pos := range anchors {
			anchor, err := a.Anchor(pos)
			if err != nil {
				t.Fatalf("seed %d: Anchor(%d): %v", seed, pos, err)
			}
			anchors[pos] = anchor
		}
		before := []rune(a.String())

		// Both edit, blind, then exchange.
		var fromA, fromB []Op
		for range 6 {
			fromA = append(fromA, insert(t, a, rng.IntN(a.Len()+1), "AA")...)
			fromB = append(fromB, insert(t, b, rng.IntN(b.Len()+1), "bb")...)
			if a.Len() > 4 {
				fromA = append(fromA, remove(t, a, rng.IntN(a.Len()-2), 2)...)
			}
			if b.Len() > 4 {
				fromB = append(fromB, remove(t, b, rng.IntN(b.Len()-2), 2)...)
			}
		}
		apply(t, a, fromB)
		apply(t, b, fromA)
		if a.String() != b.String() {
			t.Fatalf("seed %d: diverged", seed)
		}
		text := []rune(a.String())

		for pos, anchor := range anchors {
			posA, okA := a.Position(anchor)
			posB, okB := b.Position(anchor)
			if !okA || !okB || posA != posB {
				t.Fatalf("seed %d: anchor %d resolves to %d on a and %d on b (%v, %v)",
					seed, pos, posA, posB, okA, okB)
			}
			if a.Visible(anchor) != b.Visible(anchor) {
				t.Fatalf("seed %d: anchor %d is visible on one replica and not the other", seed, pos)
			}
			// While it is still there, it must still be the character it named.
			if a.Visible(anchor) && text[posA] != before[pos] {
				t.Fatalf("seed %d: anchor %d named %q and now names %q",
					seed, pos, before[pos], text[posA])
			}
		}
	}
}

func TestAuthor(t *testing.T) {
	a, b := New(1), New(2)
	seedOps := insert(t, a, 0, "aaaa")
	apply(t, b, seedOps)
	apply(t, a, insert(t, b, 4, "bbbb"))
	apply(t, b, insert(t, a, 8, "aa"))

	if got, want := a.String(), "aaaabbbbaa"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	for pos, want := range map[int]SiteID{0: 1, 3: 1, 4: 2, 7: 2, 8: 1, 9: 1} {
		got, err := a.Author(pos)
		if err != nil {
			t.Fatalf("Author(%d): %v", pos, err)
		}
		if got != want {
			t.Errorf("Author(%d) = %d, want %d", pos, got, want)
		}
	}
	for _, pos := range []int{-1, 10} {
		if _, err := a.Author(pos); !errors.Is(err, ErrOutOfRange) {
			t.Errorf("Author(%d) = %v, want ErrOutOfRange", pos, err)
		}
	}

	want := []AuthorRun{{Pos: 0, Len: 4, Site: 1}, {Pos: 4, Len: 4, Site: 2}, {Pos: 8, Len: 2, Site: 1}}
	assertRuns(t, a, want)
	// Both replicas hold the same document, so both report the same runs.
	assertRuns(t, b, want)
}

// Stretches by one replica are joined even when the document holds them in
// several blocks, so the answer describes the text rather than the storage.
func TestAuthorRunsAreJoinedAndSkipWhatIsGone(t *testing.T) {
	a, b := New(1), New(2)
	seedOps := insert(t, a, 0, "0123456789")
	apply(t, b, seedOps)

	// b writes inside a's run, splitting it, then a deletes b's writing again.
	fromB := insert(t, b, 5, "XX")
	apply(t, a, fromB)
	remove(t, a, 5, 2)
	apply(t, b, a.OpsSince(b.Version()))

	if got, want := a.String(), "0123456789"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	// Site 1 wrote all of what is left, in three blocks, and it is one run.
	want := []AuthorRun{{Pos: 0, Len: 10, Site: 1}}
	assertRuns(t, a, want)
	assertRuns(t, b, want)

	if got := New(1).AuthorRuns(); len(got) != 0 {
		t.Fatalf("an empty document has %d author runs, want none", len(got))
	}
}

func assertRuns(t *testing.T, d *Doc, want []AuthorRun) {
	t.Helper()
	got := d.AuthorRuns()
	if len(got) != len(want) {
		t.Fatalf("AuthorRuns() = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AuthorRuns() = %+v, want %+v", got, want)
		}
	}
}

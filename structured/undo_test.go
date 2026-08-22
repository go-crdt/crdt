package structured

import (
	"errors"
	"testing"

	"github.com/go-crdt/crdt"
)

func TestUndoAndRedoOneEdit(t *testing.T) {
	u := NewUndo(crdt.New(1))
	if _, err := u.Insert(0, "hello"); err != nil {
		t.Fatal(err)
	}
	if got := u.Doc().String(); got != "hello" {
		t.Fatalf("the document reads %q", got)
	}
	if !u.CanUndo() || u.CanRedo() {
		t.Fatal("after one edit there is something to undo and nothing to redo")
	}
	if _, err := u.Undo(); err != nil {
		t.Fatal(err)
	}
	if got := u.Doc().String(); got != "" {
		t.Fatalf("after undoing the insert the document reads %q", got)
	}
	if u.CanUndo() || !u.CanRedo() {
		t.Fatal("after undoing there is something to redo and nothing to undo")
	}
	if _, err := u.Redo(); err != nil {
		t.Fatal(err)
	}
	if got := u.Doc().String(); got != "hello" {
		t.Fatalf("after redoing the document reads %q", got)
	}
	// And back down again, twice over, to show the two are one walk in
	// opposite directions rather than two stacks that drift apart.
	for range 3 {
		if _, err := u.Undo(); err != nil {
			t.Fatal(err)
		}
		if got := u.Doc().String(); got != "" {
			t.Fatalf("undo gave %q", got)
		}
		if _, err := u.Redo(); err != nil {
			t.Fatal(err)
		}
		if got := u.Doc().String(); got != "hello" {
			t.Fatalf("redo gave %q", got)
		}
	}
}

func TestUndoingARemovalPutsTheTextBack(t *testing.T) {
	u := NewUndo(crdt.New(1))
	if _, err := u.Insert(0, "the quick brown fox"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Delete(4, 6); err != nil { // "quick "
		t.Fatal(err)
	}
	if got := u.Doc().String(); got != "the brown fox" {
		t.Fatalf("after the removal the document reads %q", got)
	}
	if _, err := u.Undo(); err != nil {
		t.Fatal(err)
	}
	if got := u.Doc().String(); got != "the quick brown fox" {
		t.Fatalf("after undoing the removal the document reads %q", got)
	}
	if _, err := u.Redo(); err != nil {
		t.Fatal(err)
	}
	if got := u.Doc().String(); got != "the brown fox" {
		t.Fatalf("after redoing the removal the document reads %q", got)
	}
}

// Removing from the very start has no character to go back after, which is the
// one anchor that cannot move.
func TestUndoingARemovalAtTheStart(t *testing.T) {
	u := NewUndo(crdt.New(1))
	if _, err := u.Insert(0, "abcdef"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Delete(0, 3); err != nil {
		t.Fatal(err)
	}
	if got := u.Doc().String(); got != "def" {
		t.Fatalf("the document reads %q", got)
	}
	if _, err := u.Undo(); err != nil {
		t.Fatal(err)
	}
	if got := u.Doc().String(); got != "abcdef" {
		t.Fatalf("after undoing the document reads %q", got)
	}
}

// A group is what makes typing a word undo as a word.
func TestAGroupUndoesAsOne(t *testing.T) {
	u := NewUndo(crdt.New(1))
	u.Begin()
	for i, ch := range []string{"w", "o", "r", "d"} {
		if _, err := u.Insert(i, ch); err != nil {
			t.Fatal(err)
		}
	}
	u.Commit()
	if got := u.Doc().String(); got != "word" {
		t.Fatalf("the document reads %q", got)
	}
	if _, err := u.Undo(); err != nil {
		t.Fatal(err)
	}
	if got := u.Doc().String(); got != "" {
		t.Fatalf("one undo left %q, want the whole group gone", got)
	}
	if _, err := u.Redo(); err != nil {
		t.Fatal(err)
	}
	if got := u.Doc().String(); got != "word" {
		t.Fatalf("one redo gave %q", got)
	}
	// Begin inside a group is not a second group, so a caller need not track
	// whether one is open.
	u.Begin()
	if _, err := u.Insert(4, "!"); err != nil {
		t.Fatal(err)
	}
	u.Begin()
	if _, err := u.Insert(5, "?"); err != nil {
		t.Fatal(err)
	}
	u.Commit()
	if _, err := u.Undo(); err != nil {
		t.Fatal(err)
	}
	if got := u.Doc().String(); got != "word" {
		t.Fatalf("the nested Begin made a second group: %q", got)
	}
	// An undo asked for with a group still open closes it first, so it takes
	// back what was just done.
	u.Begin()
	if _, err := u.Insert(4, "XYZ"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Undo(); err != nil {
		t.Fatal(err)
	}
	if got := u.Doc().String(); got != "word" {
		t.Fatalf("undo with an open group gave %q", got)
	}
	// An empty group leaves nothing behind: the next undo reaches past it.
	u.Begin()
	u.Commit()
	if _, err := u.Undo(); err != nil {
		t.Fatal(err)
	}
	if got := u.Doc().String(); got != "" {
		t.Fatalf("after undoing past the empty group: %q", got)
	}
}

// A new edit is a new future: what Redo was holding is no longer part of it.
func TestANewEditClearsTheRedo(t *testing.T) {
	u := NewUndo(crdt.New(1))
	if _, err := u.Insert(0, "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Undo(); err != nil {
		t.Fatal(err)
	}
	if !u.CanRedo() {
		t.Fatal("nothing to redo after an undo")
	}
	if _, err := u.Insert(0, "two"); err != nil {
		t.Fatal(err)
	}
	if u.CanRedo() {
		t.Fatal("a new edit left the old redo standing")
	}
	if _, err := u.Redo(); !errors.Is(err, ErrNoChange) {
		t.Fatalf("Redo = %v, want ErrNoChange", err)
	}
}

func TestNothingToUndo(t *testing.T) {
	u := NewUndo(crdt.New(1))
	if _, err := u.Undo(); !errors.Is(err, ErrNoChange) {
		t.Fatalf("Undo on an untouched document = %v, want ErrNoChange", err)
	}
	if _, err := u.Redo(); !errors.Is(err, ErrNoChange) {
		t.Fatalf("Redo on an untouched document = %v, want ErrNoChange", err)
	}
	if _, err := u.Insert(9, "x"); err == nil {
		t.Fatal("inserting past the end was accepted")
	}
	if _, err := u.Delete(0, 9); err == nil {
		t.Fatal("removing past the end was accepted")
	}
	if _, err := u.Delete(-1, 1); err == nil {
		t.Fatal("removing from before the start was accepted")
	}
	// A refused edit records nothing, so there is still nothing to undo.
	if u.CanUndo() {
		t.Fatal("a refused edit was recorded")
	}
}

// pair returns two replicas of the same document and a way to pass operations
// between them, which is what an undo has to survive.
func pair(t *testing.T) (*Undo, *crdt.Doc, func(*testing.T, []crdt.Op, *crdt.Doc)) {
	t.Helper()
	mine := crdt.New(1)
	theirs := crdt.New(2)
	send := func(t *testing.T, ops []crdt.Op, to *crdt.Doc) {
		t.Helper()
		if err := to.Apply(ops...); err != nil {
			t.Fatal(err)
		}
	}
	return NewUndo(mine), theirs, send
}

// The point of the whole design: an undo takes back my edit and leaves the work
// somebody else did in the meantime alone. A stack of states could not.
func TestUndoLeavesAPeersWorkAlone(t *testing.T) {
	u, theirs, send := pair(t)

	ops, err := u.Insert(0, "mine")
	if err != nil {
		t.Fatal(err)
	}
	send(t, ops, theirs)

	// They type around it, and I hear about it.
	got, err := theirs.Insert(4, " and theirs")
	if err != nil {
		t.Fatal(err)
	}
	send(t, got, u.Doc())
	if s := u.Doc().String(); s != "mine and theirs" {
		t.Fatalf("before the undo the document reads %q", s)
	}

	// Undo mine. Theirs stays.
	back, err := u.Undo()
	if err != nil {
		t.Fatal(err)
	}
	send(t, back, theirs)
	if s := u.Doc().String(); s != " and theirs" {
		t.Fatalf("after the undo I read %q, want their work left alone", s)
	}
	if s := theirs.String(); s != " and theirs" {
		t.Fatalf("after the undo they read %q", s)
	}
}

// Undoing a removal puts the text back where it was, not where that offset now
// is — and both replicas agree about the order, because the sequence decides it.
func TestUndoingARemovalWithAPeerTypingAtTheSpot(t *testing.T) {
	u, theirs, send := pair(t)
	ops, err := u.Insert(0, "alpha beta")
	if err != nil {
		t.Fatal(err)
	}
	send(t, ops, theirs)

	ops, err = u.Delete(0, 6) // "alpha "
	if err != nil {
		t.Fatal(err)
	}
	send(t, ops, theirs)
	if s := u.Doc().String(); s != "beta" {
		t.Fatalf("after the removal: %q", s)
	}

	// They type at the front while it is gone.
	got, err := theirs.Insert(0, "X")
	if err != nil {
		t.Fatal(err)
	}
	send(t, got, u.Doc())

	back, err := u.Undo()
	if err != nil {
		t.Fatal(err)
	}
	send(t, back, theirs)
	if u.Doc().String() != theirs.String() {
		t.Fatalf("the replicas disagree: %q and %q", u.Doc().String(), theirs.String())
	}
	if s := u.Doc().String(); s != "alpha Xbeta" && s != "Xalpha beta" {
		t.Fatalf("the text came back as %q, which is neither order the sequence allows", s)
	}
	t.Logf("both replicas read %q", u.Doc().String())
}

// Undoing something a peer has already removed does nothing, rather than doing
// it twice or failing.
func TestUndoingWhatAPeerAlreadyRemoved(t *testing.T) {
	u, theirs, send := pair(t)
	ops, err := u.Insert(0, "gone soon")
	if err != nil {
		t.Fatal(err)
	}
	send(t, ops, theirs)

	got, err := theirs.Delete(0, 9)
	if err != nil {
		t.Fatal(err)
	}
	send(t, got, u.Doc())
	if s := u.Doc().String(); s != "" {
		t.Fatalf("they removed it and I read %q", s)
	}

	back, err := u.Undo()
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 0 {
		t.Fatalf("undoing what was already gone produced %d operations", len(back))
	}
	if s := u.Doc().String(); s != "" {
		t.Fatalf("after the undo: %q", s)
	}
	// And it is still a step: redoing puts it back, which is what the caller
	// pressing redo expects to see.
	again, err := u.Redo()
	if err != nil {
		t.Fatal(err)
	}
	send(t, again, theirs)
	if u.Doc().String() != theirs.String() {
		t.Fatalf("the replicas disagree after the redo: %q and %q", u.Doc().String(), theirs.String())
	}
}

// An undo travels as an ordinary edit, so a peer needs no code to receive one —
// and after a long back-and-forth the two still read the same document.
func TestUndoAndRedoConverge(t *testing.T) {
	u, theirs, send := pair(t)
	ops, err := u.Insert(0, "one two three")
	if err != nil {
		t.Fatal(err)
	}
	send(t, ops, theirs)

	for round := range 8 {
		// Both edits stay at the front, inside my own thirteen characters —
		// their work is at the end. Reaching into it would be a test of two
		// people editing the same words, which is a different question.
		if round%2 == 0 {
			ops, err = u.Delete(0, 1)
		} else {
			ops, err = u.Insert(0, "!")
		}
		if err != nil {
			t.Fatal(err)
		}
		send(t, ops, theirs)

		// They edit too, between my edit and my undo — at the end, where
		// nothing I do reaches. Typing where I am about to delete would be a
		// test of what happens when two people edit the same words, which is a
		// different question from whether an undo of mine spares their work.
		got, err := theirs.Insert(theirs.Len(), "*")
		if err != nil {
			t.Fatal(err)
		}
		send(t, got, u.Doc())

		back, err := u.Undo()
		if err != nil {
			t.Fatal(err)
		}
		send(t, back, theirs)
		if u.Doc().String() != theirs.String() {
			t.Fatalf("round %d: %q against %q", round, u.Doc().String(), theirs.String())
		}

		again, err := u.Redo()
		if err != nil {
			t.Fatal(err)
		}
		send(t, again, theirs)
		if u.Doc().String() != theirs.String() {
			t.Fatalf("round %d after redo: %q against %q", round, u.Doc().String(), theirs.String())
		}
	}
	// Their eight characters are all still there: an undo of mine never took
	// one of theirs.
	stars := 0
	for _, r := range u.Doc().String() {
		if r == '*' {
			stars++
		}
	}
	if stars != 8 {
		t.Fatalf("%d of their characters survived, want 8: %q", stars, u.Doc().String())
	}
}

// With no clock left nothing can be written, and every entry point says so
// rather than reporting an undo it did not make.
func TestUndoWithNoClockLeft(t *testing.T) {
	u := NewUndo(crdt.New(1))
	if _, err := u.Insert(0, "abcdef"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Delete(0, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Undo(); err != nil { // puts "ab" back, while there is clock
		t.Fatal(err)
	}
	// A peer drives the clock to the ceiling, leaving this replica no room.
	top := crdt.Op{Kind: crdt.OpInsert, ID: crdt.ID{Site: 9, Seq: 1},
		Clock: crdt.MaxClock, Char: 'z'}
	if err := u.Doc().Apply(top); err != nil {
		t.Fatal(err)
	}
	before := u.Doc().String()

	if _, err := u.Insert(0, "x"); err == nil {
		t.Fatal("inserting with no clock left was accepted")
	}
	if _, err := u.Delete(0, 1); err == nil {
		t.Fatal("removing with no clock left was accepted")
	}
	// Undo puts text back, which is an insertion, and Redo takes it out again.
	if _, err := u.Undo(); err == nil {
		t.Fatal("undoing with no clock left was accepted")
	}
	if _, err := u.Redo(); err == nil {
		t.Fatal("redoing with no clock left was accepted")
	}
	if got := u.Doc().String(); got != before {
		t.Fatalf("a refused undo changed the document to %q", got)
	}
}

// The character the text went back after can itself be gone by the time the
// undo happens — a peer removed it. A removed character still has the place the
// text closed up to, and that place can be the end of the document, so where the
// text goes is held to what there is.
func TestUndoingARemovalWhoseAnchorAPeerRemoved(t *testing.T) {
	u, theirs, send := pair(t)
	ops, err := u.Insert(0, "ab")
	if err != nil {
		t.Fatal(err)
	}
	send(t, ops, theirs)

	ops, err = u.Delete(1, 1) // "b", which followed "a"
	if err != nil {
		t.Fatal(err)
	}
	send(t, ops, theirs)

	// They take away the very character it followed.
	got, err := theirs.Delete(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	send(t, got, u.Doc())
	if s := u.Doc().String(); s != "" {
		t.Fatalf("both characters should be gone, and I read %q", s)
	}

	back, err := u.Undo()
	if err != nil {
		t.Fatal(err)
	}
	send(t, back, theirs)
	if u.Doc().String() != theirs.String() {
		t.Fatalf("the replicas disagree: %q and %q", u.Doc().String(), theirs.String())
	}
	if s := u.Doc().String(); s != "b" {
		t.Fatalf("the text came back as %q, want just the character that was undone", s)
	}
}

// The other half of the same refusal: undoing a *removal* has to write the text
// back, and with no clock left it cannot. It says so rather than reporting an
// undo it did not make, and the step stays on the stack.
func TestUndoingARemovalWithNoClockLeft(t *testing.T) {
	u := NewUndo(crdt.New(1))
	if _, err := u.Insert(0, "abcdef"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Delete(0, 2); err != nil {
		t.Fatal(err)
	}
	top := crdt.Op{Kind: crdt.OpInsert, ID: crdt.ID{Site: 9, Seq: 1},
		Clock: crdt.MaxClock, Char: 'z'}
	if err := u.Doc().Apply(top); err != nil {
		t.Fatal(err)
	}
	before := u.Doc().String()

	if _, err := u.Undo(); err == nil {
		t.Fatal("undoing a removal with no clock left was accepted")
	}
	if got := u.Doc().String(); got != before {
		t.Fatalf("the refused undo changed the document to %q", got)
	}
	if !u.CanUndo() {
		t.Fatal("the refused undo took the step off the stack")
	}
}

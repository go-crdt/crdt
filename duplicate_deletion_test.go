package crdt

import (
	"bytes"
	"testing"
)

// A losing duplicate deletion is part of a document, not bookkeeping beside it.
//
// When two replicas delete the same character without having seen each other,
// only one of the two operations can be the character's recorded deletion. The
// other still happened, and a replica that has not seen it has to be able to
// receive it, so it is kept and filed against the character it removed.
//
// That [Doc.OpsSince] sends it is already pinned by
// TestSnapshotCarriesDuplicateDeletions. What is pinned here is what it costs to
// leave one out — because #80 plans to send superseded runs in place of
// operations, and a run covering a losing duplicate would do exactly this.
//
// The replica ends with the same text as everybody else and a different
// document, and nothing puts it back: the operation arriving later by another
// route is accepted without complaint and recorded nowhere, because the version
// already covers it. This is the shape of the collection defect in #78 and of
// the server one in collab#83 — a version vector moving past an operation
// without the replica learning what it did.
func TestAVersionThatAdvancesPastADuplicateDeletionDivergesForGood(t *testing.T) {
	whole := twoReplicasThatDeletedTheSameCharacter(t)

	var loser Op
	kept := make([]Op, 0, 8)
	for _, op := range whole.OpsSince(VersionVector{}) {
		if _, isDup := whole.dupDeletes[op.ID]; op.Kind == OpDelete && isDup {
			loser = op
			continue
		}
		kept = append(kept, op)
	}
	if loser.Kind != OpDelete {
		t.Fatal("no losing duplicate among the operations this replica sends")
	}

	short := New(3)
	if err := short.Apply(kept...); err != nil {
		t.Fatal(err)
	}
	// Everything except the loser, and a version that says it arrived: what a
	// superseded run covering it would leave behind.
	short.vv[loser.ID.Site] = loser.ID.Seq

	if got, want := short.String(), whole.String(); got != want {
		t.Fatalf("the text was supposed to agree: %q against %q", got, want)
	}
	if bytes.Equal(whole.Snapshot(), short.Snapshot()) {
		t.Fatal("leaving out a losing duplicate was supposed to be visible in the document")
	}

	// It arrives later by another route, from a peer that sent it whole.
	if err := short.Apply(loser); err != nil {
		t.Fatalf("the late operation was refused: %v", err)
	}
	if len(short.dupDeletes) != 0 {
		t.Fatal("the late operation was recorded, so this is recoverable and the comment above is wrong")
	}
	if bytes.Equal(whole.Snapshot(), short.Snapshot()) {
		t.Fatal("the late operation repaired the document, so this is recoverable")
	}
}

// And the rule that follows from it, stated where it can fail rather than in an
// issue: a superseded run may stand in for operations nothing else names, and a
// losing duplicate deletion is named — by the character it is filed against.
//
// No superseded run is sent today, so this passes without asserting anything.
// It is here for the half of #80 that will send them.
func TestNoSupersededRunCoversALosingDuplicateDeletion(t *testing.T) {
	whole := twoReplicasThatDeletedTheSameCharacter(t)

	for _, op := range whole.OpsSince(VersionVector{}) {
		if op.Kind != OpSuperseded {
			continue
		}
		for delID := range whole.dupDeletes {
			if delID.Site == op.ID.Site && delID.Seq >= op.first() && delID.Seq <= op.ID.Seq {
				t.Fatalf("a superseded run of %d over site %d covers the losing duplicate %v, "+
					"which the character it removed still names", op.Span, op.ID.Site, delID)
			}
		}
	}
}

// twoReplicasThatDeletedTheSameCharacter returns a replica that has converged
// with a second one, each having deleted the same character without seeing the
// other, so that exactly one duplicate deletion is recorded.
func twoReplicasThatDeletedTheSameCharacter(t *testing.T) *Doc {
	t.Helper()
	a, b := New(1), New(2)
	typed, err := a.Insert(0, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(typed...); err != nil {
		t.Fatal(err)
	}
	da, err := a.Delete(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	db, err := b.Delete(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(db...); err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(da...); err != nil {
		t.Fatal(err)
	}
	if len(a.dupDeletes) != 1 {
		t.Fatalf("expected one duplicate deletion, got %d", len(a.dupDeletes))
	}
	if !bytes.Equal(a.Snapshot(), b.Snapshot()) {
		t.Fatal("the two deleters did not converge")
	}
	return a
}

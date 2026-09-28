package crdt

import (
	"slices"
	"testing"
)

// What this text CRDT does with two people typing at the same anchor, which is
// a known property of RGA and not a defect of this implementation.
//
// Kleppmann, Gomes, Mulligan & Beresford, "Interleaving anomalies in
// collaborative text editors" (PaPoC '19), prove that RGA rules out the SEVERE
// anomaly — two concurrent words jumbled character by character, which Logoot
// and LSEQ do exhibit — and show that it still admits a lesser one when a
// user's insertions are not sequential.
//
// Their figure 4, reproduced here: User 1 types " reader" after "Hello", then
// MOVES THE CURSOR BACK and types " dear" at the same place. User 2, having
// seen neither, types " Alice" there too. All three runs are anchored to the
// same character — the "o" of "Hello" — and RGA orders insertions sharing an
// anchor by timestamp alone, so where User 2's word lands among User 1's two is
// not determined by what either of them did:
//
//	(1) Hello dear reader Alice!    User 2's word after both of User 1's
//	(2) Hello dear Alice reader!    BETWEEN them — the lesser anomaly
//	(3) Hello Alice dear reader!    before both
//
// Measured on this implementation: it produces (2) when User 1 holds the lower
// site identity and (1) when it holds the higher, and both merge orders agree
// in each case. So the anomaly is present, it is convergent, and which of the
// three appears is decided by the tie-break between equal Lamport clocks.
//
// This test does not pin WHICH: that would freeze a tie-break nothing promises.
// It pins the two things that are promises — every merge order agrees, and the
// result is one of the three the paper enumerates. A fourth outcome would be
// character-level interleaving, which is the severe anomaly RGA is proved not
// to have, and would be a real defect here.
//
// That paper also proposes a fix, and the fix does not work: Weidner & Kleppmann
// report that its non-interleaving property "cannot be satisfied by any
// algorithm" and that its algorithm "is incorrect — it does not converge" ("The
// Art of the Fugue", §3.2). What works is a different algorithm — Fugue, or
// FugueMax, which is proved maximally non-interleaving — and that replaces how
// concurrent insertions at one anchor are ordered, which is this package's
// centre. So the outcomes below are what this design gives, not what it settles
// for pending a patch.
func TestWhereAConcurrentWordLandsAmongTwoOfYourOwn(t *testing.T) {
	// The three merges RGA admits for figure 4. Anything else is a defect.
	admitted := []string{
		"Hello dear reader Alice!",
		"Hello dear Alice reader!",
		"Hello Alice dear reader!",
	}

	figure4 := func(t *testing.T, userOne, userTwo SiteID, oneFirst bool) string {
		t.Helper()
		base := New(userOne)
		if _, err := base.Insert(0, "Hello!"); err != nil {
			t.Fatal(err)
		}
		shared := base.Snapshot()

		one, err := Load(userOne, shared)
		if err != nil {
			t.Fatal(err)
		}
		two, err := Load(userTwo, shared)
		if err != nil {
			t.Fatal(err)
		}
		// User 1, sequentially but not contiguously: the cursor goes back.
		if _, err := one.Insert(5, " reader"); err != nil {
			t.Fatal(err)
		}
		if _, err := one.Insert(5, " dear"); err != nil {
			t.Fatal(err)
		}
		// User 2, concurrently, at the same anchor.
		if _, err := two.Insert(5, " Alice"); err != nil {
			t.Fatal(err)
		}

		first, second := one, two
		if !oneFirst {
			first, second = two, one
		}
		merged, err := Load(99, first.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		if err := merged.Apply(second.OpsSince(merged.Version())...); err != nil {
			t.Fatal(err)
		}
		return merged.String()
	}

	for _, c := range []struct {
		name             string
		userOne, userTwo SiteID
	}{
		{"user 1 holds the lower site identity", 1, 2},
		{"user 1 holds the higher site identity", 2, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			forwards := figure4(t, c.userOne, c.userTwo, true)
			backwards := figure4(t, c.userOne, c.userTwo, false)

			// Convergence, which is the promise. Without this the rest would
			// be a description of one arbitrary run.
			if forwards != backwards {
				t.Fatalf("the merge order decided the document:\n one first: %q\n two first: %q", forwards, backwards)
			}
			if !slices.Contains(admitted, forwards) {
				t.Fatalf("merged to %q, which is none of the three RGA admits for this history —"+
					" a fourth outcome means the characters interleaved, which RGA is proved not to do", forwards)
			}
			t.Logf("merged to %q", forwards)
		})
	}
}

// The minimal pair from the same literature, and the one that pins a GUARANTEE
// rather than a limitation.
//
// Weidner & Kleppmann, "The Art of the Fugue", Appendix A.1, define both
// anomalies on three characters:
//
//	forward   one user inserts a then b, another concurrently inserts x;
//	          interleaving iff a merged outcome is "axb"
//	backward  one user inserts b and then PREPENDS a, another inserts x;
//	          interleaving iff a merged outcome is "axb"
//
// RGA is PROVED free of the forward one. That makes "axb" from the forward
// history a defect in this implementation rather than a property of the
// algorithm — which is why it is asserted here and why the test above, which
// only checks that the outcome is one the algorithm admits, does not cover it.
//
// Their A.1.8 derives the backward case for RGA and lands on "axb". Measured
// here it appears when the writer of a and b holds the lower site identity,
// which is that derivation's "Assuming A < B".
func TestTwoOfYourCharactersStayTogetherWhenYouTypeForwards(t *testing.T) {
	// history builds the three-character example and returns the merge.
	history := func(t *testing.T, mine, theirs SiteID, backward, mineFirst bool) string {
		t.Helper()
		a, b := New(mine), New(theirs)
		if backward {
			if _, err := a.Insert(0, "b"); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Insert(0, "a"); err != nil { // prepended
				t.Fatal(err)
			}
		} else {
			if _, err := a.Insert(0, "a"); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Insert(1, "b"); err != nil { // typed on
				t.Fatal(err)
			}
		}
		if _, err := b.Insert(0, "x"); err != nil {
			t.Fatal(err)
		}
		first, second := a, b
		if !mineFirst {
			first, second = b, a
		}
		merged, err := Load(99, first.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		if err := merged.Apply(second.OpsSince(merged.Version())...); err != nil {
			t.Fatal(err)
		}
		return merged.String()
	}

	for _, sites := range []struct{ mine, theirs SiteID }{{1, 2}, {2, 1}} {
		for _, mineFirst := range []bool{true, false} {
			forward := history(t, sites.mine, sites.theirs, false, mineFirst)
			if forward == "axb" {
				t.Errorf("typing forwards merged to %q with sites %d and %d: RGA is proved free of"+
					" forward interleaving, so this is a defect here and not the algorithm",
					forward, sites.mine, sites.theirs)
			}
			// And the two characters must still both be there, or the case
			// above would be satisfied by losing one.
			if len(forward) != 3 {
				t.Errorf("typing forwards merged to %q, want all three characters", forward)
			}

			// The backward case is the known one. It is not asserted either
			// way -- which of the outcomes appears is a tie-break nothing
			// promises -- but it must converge and keep every character.
			backward := history(t, sites.mine, sites.theirs, true, mineFirst)
			if len(backward) != 3 {
				t.Errorf("prepending merged to %q, want all three characters", backward)
			}
			t.Logf("sites %d/%d: forwards %q, backwards %q", sites.mine, sites.theirs, forward, backward)
		}
	}
}

package crdt

import (
	"errors"
	"testing"
)

// Two replicas that chose the same site, and what the receiver can tell.
//
// The four cases below are the whole claim: it fires where the collision has a
// consequence, and it is silent everywhere it has nothing to compare. The
// silences are the important half — a guard that refused a batch it could not
// judge would break a peer doing nothing wrong, and there are three ways to
// have nothing to compare.
func TestApplyRefusesAnOperationWearingAnAppliedName(t *testing.T) {
	// The forgery, or the accident: one site, two histories.
	mine := New(7)
	if _, err := mine.Insert(0, "GENUINE"); err != nil {
		t.Fatal(err)
	}
	theirs := New(7)
	forged, err := theirs.Insert(0, "FORGED-AND-MUCH-LONGER-THAN-GENUINE")
	if err != nil {
		t.Fatal(err)
	}
	if err := mine.Apply(forged...); !errors.Is(err, ErrCollidingID) {
		t.Fatalf("Apply returned %v, want ErrCollidingID", err)
	}
	// Here nothing of the forgery landed, because the batch's first operation is
	// the one that collides and admit stops there. That is worth asserting and
	// worth not over-promising: the batch stops at the FIRST collision it
	// reaches, so a batch whose collision comes late has applied what came
	// before it. This is a report that usually arrives in time, not a boundary.
	// The boundary is go-crdt/collab#175.
	if got := mine.String(); got != "GENUINE" {
		t.Errorf("the document holds %q, want the forgery to have stopped at the first collision", got)
	}
	// And the tail is what made it dangerous: without the guard those
	// operations graft onto this replica's own, because they are past its own
	// count and so are not even duplicates.
	if got := len(forged); got < 8 {
		t.Fatalf("the forged batch is %d operations, too short to reach past GENUINE", got)
	}
}

// A true duplicate is still ignored in silence, which is the contract Op
// documents and the reason this guard is a premise check rather than a policy.
func TestApplyStillIgnoresAGenuineDuplicate(t *testing.T) {
	d := New(7)
	ops, err := d.Insert(0, "hello")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := d.Apply(ops...); err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		if got := d.String(); got != "hello" {
			t.Fatalf("round %d holds %q", i, got)
		}
	}
	// A peer that resends, reordered, is the same case.
	backwards := make([]Op, len(ops))
	for i, op := range ops {
		backwards[len(ops)-1-i] = op
	}
	if err := d.Apply(backwards...); err != nil {
		t.Fatalf("reordered resend: %v", err)
	}
}

// Two replicas on one site that wrote the SAME text are a collision with no
// consequence, and are not refused: the operations compare equal, so applying
// them twice really does change nothing.
func TestTwoReplicasOnOneSiteThatAgreeAreNotRefused(t *testing.T) {
	mine, theirs := New(7), New(7)
	if _, err := mine.Insert(0, "same"); err != nil {
		t.Fatal(err)
	}
	ops, err := theirs.Insert(0, "same")
	if err != nil {
		t.Fatal(err)
	}
	if err := mine.Apply(ops...); err != nil {
		t.Fatalf("Apply refused an agreeing history: %v", err)
	}
	if got := mine.String(); got != "same" {
		t.Errorf("holds %q, want %q", got, "same")
	}
}

// The three ways to have nothing to compare, each of which must stay silent.
func TestTheGuardIsSilentWhereItCannotJudge(t *testing.T) {
	t.Run("a deletion creates no character", func(t *testing.T) {
		// Both replicas insert the same text, so the inserts agree; then each
		// deletes a different character, and the deletions collide on ID
		// without either naming a character this replica could look up.
		mine, theirs := New(7), New(7)
		for _, d := range []*Doc{mine, theirs} {
			if _, err := d.Insert(0, "abcd"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := mine.Delete(0, 1); err != nil {
			t.Fatal(err)
		}
		dels, err := theirs.Delete(3, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := mine.Apply(dels...); err != nil {
			t.Fatalf("a colliding deletion was refused: %v", err)
		}
		// It is not caught, and the document is wrong in exactly the way this
		// guard does not cover: the deletion was absorbed as already-applied,
		// so 'd' is still there and 'a' is the one gone.
		if got := mine.String(); got != "bcd" {
			t.Logf("after the colliding deletion: %q", got)
		}
	})

	t.Run("a purged run no longer knows what it said", func(t *testing.T) {
		mine := New(7)
		ops, err := mine.Insert(0, "gone")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mine.Delete(0, 4); err != nil {
			t.Fatal(err)
		}
		mine.Purge()
		// An operation naming one of those characters, with a different
		// character: purged, so there is nothing to compare and nothing to say.
		forged := ops[0]
		forged.Char = 'X'
		if err := mine.Apply(forged); err != nil && !errors.Is(err, ErrPurged) {
			t.Fatalf("want silence or ErrPurged, got %v", err)
		} else if errors.Is(err, ErrCollidingID) {
			t.Fatal("claimed a collision it could not see")
		}
	})

	t.Run("an operation past this replica's count is not a duplicate at all", func(t *testing.T) {
		// The guard only looks at names this replica has already applied; an
		// operation it has never seen is integrated, which is the whole reason
		// the graft in the first test is possible.
		mine := New(7)
		if _, err := mine.Insert(0, "ab"); err != nil {
			t.Fatal(err)
		}
		theirs := New(7)
		ops, err := theirs.Insert(0, "abXY")
		if err != nil {
			t.Fatal(err)
		}
		// Keep only what runs past mine: those two agree on nothing this
		// replica holds, and are accepted.
		var past []Op
		for _, op := range ops {
			if !mine.vv.Includes(op.ID) {
				past = append(past, op)
			}
		}
		if len(past) == 0 {
			t.Skip("nothing past this replica's count in this history")
		}
		if err := mine.Apply(past...); err != nil {
			t.Fatalf("operations past our count were refused: %v", err)
		}
	})
}

// What the guard costs on the batch that pays for it: a peer resending a history
// this replica already holds in full, so every operation in it takes the lookup.
//
// Measured on an Apple M4 Max, medians of eight runs of 500 iterations over
// twenty thousand operations, against a control with the check compiled out:
//
//	                        ns per resent operation
//	with the check                            17.89
//	without it                                12.99
//
// So 4.9 ns an operation, +38% on this path, and 98 µs for the whole twenty
// thousand. The spread inside each arm is under 2%, which is why a 38%
// difference can be stated at all.
//
// The ordinary path -- operations this replica has not seen, which is somebody
// typing -- was measured the same way through BenchmarkApplyRemote and gave
// 349 052 ns with the check against 355 446 without it: the arm carrying the
// check came out FASTER, which is not a result but a noise floor, the control
// having spent two of its eight runs near 440 000. So no cost is measurable
// there, which is not the same as there being none. An operation this replica has
// not seen fails the version-vector test and stops before the index lookup.
func BenchmarkApplyAResendOfWhatWeHold(b *testing.B) {
	d := New(7)
	ops, err := d.Insert(0, string(make([]rune, 0, 20000))+randomish(20000))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := d.Apply(ops...); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(len(ops)), "ns/op/resent")
}

func randomish(n int) string {
	out := make([]rune, n)
	for i := range out {
		out[i] = rune('a' + i%26)
	}
	return string(out)
}

// A batch that contains its own collision must get the same answer every time.
//
// This is what FuzzApply found within a second of the check being made before
// the batch landed instead of as it lands: with nothing applied yet, neither of
// two operations sharing an ID collides with anything, so the batch was accepted
// and one of them silently dropped -- and replaying the same bytes then refused
// them, because by that point the dropped one disagreed with the applied one. A
// transport may replay freely, so an answer that changes between two identical
// calls is worse than either answer given twice.
//
// No honest replica can produce such a batch: a site's sequence number rises
// once per operation. It is the shape of a collision arriving in one piece.
func TestABatchContainingItsOwnCollisionAnswersTheSameEveryTime(t *testing.T) {
	for _, order := range []string{"a first", "b first"} {
		t.Run(order, func(t *testing.T) {
			a := Op{Kind: OpInsert, ID: ID{Site: 7, Seq: 1}, Clock: 1, Char: 'a'}
			b := Op{Kind: OpInsert, ID: ID{Site: 7, Seq: 1}, Clock: 1, Char: 'b'}
			batch := []Op{a, b}
			if order == "b first" {
				batch = []Op{b, a}
			}
			d := New(9)
			first := d.Apply(batch...)
			if !errors.Is(first, ErrCollidingID) {
				t.Fatalf("the first pass returned %v, want ErrCollidingID", first)
			}
			// The same bytes again, which is what a transport may do.
			if second := d.Apply(batch...); !errors.Is(second, ErrCollidingID) {
				t.Fatalf("the replay returned %v, want the same refusal", second)
			}
		})
	}
}

// The guard has to reach the caller through a Composite, which is how every
// consumer of this package applies operations.
//
// It is tested rather than assumed because a Composite dropped a text part's
// error on purpose, with a comment saying a text could not fail -- true until
// this. The same file records what dropping a map's cost when that stopped being
// true: sixty-three errors thrown away and a replica holding fifteen hundred
// operations back for good.
func TestACompositeHandsTheCollisionToItsCaller(t *testing.T) {
	forge := func(t *testing.T) []PartOps {
		t.Helper()
		theirs := NewComposite(7)
		text, err := theirs.Text("body")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := text.Insert(0, "FORGED-AND-MUCH-LONGER-THAN-GENUINE"); err != nil {
			t.Fatal(err)
		}
		return theirs.OpsSince(nil)
	}
	mineWith := func(t *testing.T) *Composite {
		t.Helper()
		mine := NewComposite(7)
		text, err := mine.Text("body")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := text.Insert(0, "GENUINE"); err != nil {
			t.Fatal(err)
		}
		return mine
	}
	// Both entry points, because they are two different switches over the part
	// kinds and only one of them was reached by the first version of this.
	t.Run("Apply", func(t *testing.T) {
		mine := mineWith(t)
		if err := mine.Apply(forge(t)...); !errors.Is(err, ErrCollidingID) {
			t.Fatalf("Composite.Apply returned %v, want ErrCollidingID", err)
		}
	})
	t.Run("ApplyChanges", func(t *testing.T) {
		mine := mineWith(t)
		if _, err := mine.ApplyChanges(forge(t)...); !errors.Is(err, ErrCollidingID) {
			t.Fatalf("Composite.ApplyChanges returned %v, want ErrCollidingID", err)
		}
	})
}

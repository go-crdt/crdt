package crdt

import (
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

// The walk that wakes parked operations is bounded by what is PARKED, never by
// the range an operation claims to cover.
//
// A superseded run says "everything from had+1 to last at this site is
// accounted for", and last is a sequence number: a peer may put any number up
// to MaxClock -- 2^62 -- in it. wake has two ways to find what that releases.
// It can walk the parked operations and keep the ones in range, which costs
// what is parked. Or it can walk the range and look each number up, which costs
// the range. The refusal here is what chooses, and it chooses by whichever is
// smaller.
//
// Deleting it is the one mutation in this package's sweep that produced no
// answer at all. It did not fail the suite: it made the suite wait, and the
// sweep gave up on it after four minutes with the verdict HUNG. That is the
// worst shape a missing test has, because a guard whose absence HANGS looks
// exactly like a guard nothing needs until somebody sends the number.
//
// The deadline below is a damage bound, not a performance claim. The call
// either returns in microseconds or walks 2^62 numbers, and nothing lands in
// between, so no amount of load on the machine can move a correct run across
// it. Without it the test does not fail, it hangs until go test kills the
// package ten minutes later and names nothing.
func TestWakingParkedOperationsCostsWhatIsParkedNotWhatIsClaimed(t *testing.T) {
	m := NewMap(1)

	// One parked operation, so the cheap branch has something to find and the
	// control below is not vacuous.
	parked := MapOp{Kind: MapSet, ID: ID{Site: 2, Seq: 5}, Clock: 5, Key: "k", Value: []byte("v")}
	if err := m.Apply(parked); err != nil {
		t.Fatal(err)
	}
	if m.parked != 1 {
		t.Fatalf("the fixture parked %d operations, want 1 -- nothing below is about wake otherwise", m.parked)
	}

	done := make(chan int, 1)
	go func() {
		// The widest run a peer may legitimately describe.
		done <- len(m.wake(nil, 2, 0, MaxClock))
	}()

	select {
	case released := <-done:
		if released != 1 {
			t.Errorf("the run released %d parked operations, want 1", released)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("wake did not return: it is walking the range an operation claims (up to MaxClock) " +
			"instead of what is parked, and a peer chooses that number")
	}
}

// An operation naming a kind this build does not know is invalid, not
// malformed, whatever follows the kind byte.
//
// The twin of the same refusal in decodeListOp, and it survived the same sweep
// for the same reason: with the check gone, an unknown kind falls through to
// the branch that reads a key and op.validate() refuses it at the end with the
// same error. The answer changes only when the bytes run out first, and then it
// becomes ErrMalformed -- a transport fault where the truth is a dialect this
// build does not speak.
func TestAnUnknownMapOpKindIsInvalidRatherThanMalformed(t *testing.T) {
	src := NewMap(1)
	op, err := src.Set("k", []byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	batch, err := AppendMapOps(nil, []MapOp{op})
	if err != nil {
		t.Fatal(err)
	}
	one := batch[1:] // the batch's count varint off, leaving one operation

	if _, _, err := decodeMapOp(one); err != nil {
		t.Fatalf("the control does not decode: %v", err)
	}

	unknown := append([]byte(nil), one...)
	unknown[0] = 9
	if _, _, err := decodeMapOp(unknown); !errors.Is(err, ErrInvalidOp) {
		t.Errorf("an unknown kind followed by a well-formed operation gave %v, want ErrInvalidOp", err)
	}
	for _, after := range []int{0, 1, 4} {
		if _, _, err := decodeMapOp(append([]byte{9}, one[1:1+after]...)); !errors.Is(err, ErrInvalidOp) {
			t.Errorf("an unknown kind with %d bytes after it gave %v, want ErrInvalidOp", after, err)
		}
	}
}

// A snapshot may not name a collection floor above the clock ceiling.
//
// collectedBelow decides which writes this replica refuses as already
// collected. A snapshot arrives from outside -- a store, a peer, an operator's
// file -- and a floor of MaxClock+1 refuses every write there is, which is a
// document that silently stops accepting edits rather than a document that
// reports an error. The refusal that stops it survived the sweep.
//
// The snapshot here is real and the floor is spliced into it, so the only thing
// separating the two calls below is the number under test.
func TestASnapshotMayNotCollectAboveTheClockCeiling(t *testing.T) {
	m := NewMap(1)
	if _, err := m.Set("k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	snap := m.Snapshot()

	// The floor follows the magic and the one-byte version, and it is zero in a
	// map that has collected nothing: one byte to replace.
	at := len(mapMagic) + 1
	if snap[at] != 0 {
		t.Fatalf("the floor at offset %d is %d, not the zero this test splices over", at, snap[at])
	}
	if _, err := LoadMap(2, snap); err != nil {
		t.Fatalf("the control does not load: %v", err)
	}

	for _, c := range []struct {
		name  string
		floor uint64
		want  bool
	}{
		{"at the ceiling", MaxClock, true},
		{"one above it", MaxClock + 1, false},
	} {
		crafted := append([]byte(nil), snap[:at]...)
		crafted = binary.AppendUvarint(crafted, c.floor)
		crafted = append(crafted, snap[at+1:]...)

		_, err := LoadMap(2, crafted)
		if c.want && err != nil {
			t.Errorf("a floor %s was refused with %v, so this test is not about the ceiling", c.name, err)
		}
		if !c.want && !errors.Is(err, ErrMalformed) {
			t.Errorf("a floor %s gave %v, want ErrMalformed -- it would refuse every write there is", c.name, err)
		}
	}
}

// Two refusals in this file survived the same sweep and are deliberately not
// pinned here, because both are the same value by another line.
//
//	map.go:861, the "!ok" after the site count in LoadMap. Deleting it leaves
//	nSites at zero, the loop is skipped, and the reads after it fail on the
//	same exhausted buffer -- ErrMalformed either way.
//
//	map.go:1002, the "len(b) == 0" in cloneBytes. Deleting it leaves
//	append([]byte(nil), b...) with nothing to append, which returns nil: the
//	same nil the refusal returns, allocated no differently.
//
// Both were re-run against this file with
//
//	mutsweep -only map.go:861,map.go:1002 -- go test -count 1 ./...
//
// and both still survive, which is the measurement rather than the argument.

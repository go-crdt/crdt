package crdt

import (
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

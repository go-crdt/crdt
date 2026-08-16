package awareness

import (
	"math"
	"testing"

	"github.com/go-crdt/crdt"
)

// A registry adopts the counter it is told, and the site it names goes on
// counting from there — so a counter at the top of the range would make that
// site's own next publication wrap to zero, which every registry then discards
// as stale. The peer would sit on the list frozen, and nothing it did
// afterwards would ever be seen again. The ceiling is what keeps that
// unreachable; see [crdt.MaxClock].
func TestACounterAboveTheCeilingIsRefused(t *testing.T) {
	for _, clock := range []uint64{crdt.MaxClock + 1, math.MaxUint64} {
		r := New()
		if r.Apply(Update{Site: 9, Clock: clock, Cursor: Cursor{Head: 1}}) {
			t.Fatalf("Apply(clock=%d) was accepted", clock)
		}
		if peers := r.Peers(); len(peers) != 0 {
			t.Fatalf("a refused update left %d peers behind", len(peers))
		}
		// The site is still able to announce itself, and at a counter its own
		// peers will accept.
		u := r.Publish(9, Cursor{Head: 2}, nil)
		if u.Clock == 0 || u.Clock > crdt.MaxClock {
			t.Fatalf("Publish after a refused update gave clock %d", u.Clock)
		}
		if !New().Apply(u) {
			t.Fatal("a fresh registry refused what Publish produced")
		}
	}
}

// The ceiling itself is a legal counter — the top of the range, not past it —
// and publishing from there stops rather than passing it. Standing still is
// visible; wrapping is not.
func TestPublishingFromTheCeilingStops(t *testing.T) {
	r := New()
	if !r.Apply(Update{Site: 9, Clock: crdt.MaxClock, Cursor: Cursor{Head: 1}}) {
		t.Fatal("the ceiling itself must be accepted")
	}
	u := r.Publish(9, Cursor{Head: 2}, nil)
	if u.Clock != crdt.MaxClock {
		t.Fatalf("Publish from the ceiling gave clock %d, want %d", u.Clock, uint64(crdt.MaxClock))
	}
	if g := r.Leave(9); g.Clock != crdt.MaxClock {
		t.Fatalf("Leave from the ceiling gave clock %d, want %d", g.Clock, uint64(crdt.MaxClock))
	}
	// The control: from anywhere below, the counter still advances.
	below := New()
	below.Apply(Update{Site: 9, Clock: crdt.MaxClock - 1, Cursor: Cursor{Head: 1}})
	if u := below.Publish(9, Cursor{Head: 2}, nil); u.Clock != crdt.MaxClock {
		t.Fatalf("Publish below the ceiling gave clock %d, want %d", u.Clock, uint64(crdt.MaxClock))
	}
}

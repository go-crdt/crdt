package awareness

import (
	"bytes"
	"errors"
	"testing"
)

// The byte that says whether a participant is gone holds a boolean, and nothing
// else.
//
// This package had never been swept: the campaign's rows covered crdt's root and
// crdt/structured, and the fourteen refusals in here were in neither. The sweep
// of 2026-10-11 found exactly one survivor, and it is this guard's `gone > 1`
// clause.
//
// What it protects is the encoding being CANONICAL -- the same state being the
// same bytes, which is what lets this package's snapshots be compared by their
// bytes and what every other decoder in crdt insists on. Without the clause,
// `out.Gone = gone == 1` reads 2, 7 and 255 all as false, so a present
// participant can be described by 255 different byte strings. Measured on a real
// update, with the guard deleted:
//
//	gone=0   accepted, re-encodes to the same bytes
//	gone=2   accepted, re-encodes to DIFFERENT bytes
//	gone=7   accepted, re-encodes to DIFFERENT bytes
//	gone=255 accepted, re-encodes to DIFFERENT bytes
//
// An update arrives from a peer on the presence channel, which is the busiest
// thing on the wire, so "a peer could send it" is not hypothetical here.
func TestTheGoneByteHoldsABooleanAndNothingElse(t *testing.T) {
	present := Update{
		Site:   9,
		Clock:  3,
		Cursor: Cursor{Anchor: 1, Head: 2},
		Meta:   map[string]string{"n": "a"},
	}
	raw, err := present.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	// The gone byte follows the two uvarints for site and clock. Found by
	// decoding them rather than by counting, so the fixture survives a change
	// to how either is written.
	d := decoder{buf: raw}
	d.uvarint()
	d.uvarint()
	at := len(raw) - len(d.buf)
	if at >= len(raw) {
		t.Fatalf("the header is %d bytes of %d, so there is no gone byte to reach", at, len(raw))
	}
	if raw[at] != 0 {
		t.Fatalf("the gone byte of a present update is %d, not the zero this test writes over", raw[at])
	}

	// The control, and it has to come first: these bytes decode, so what
	// follows is about the one byte being changed.
	var control Update
	if err := control.UnmarshalBinary(raw); err != nil {
		t.Fatalf("the control does not decode: %v", err)
	}
	if control.Gone {
		t.Fatal("the control decoded as gone, so the byte under test is not the one being written")
	}

	for _, gone := range []byte{2, 3, 7, 128, 255} {
		in := bytes.Clone(raw)
		in[at] = gone
		var u Update
		if err := u.UnmarshalBinary(in); !errors.Is(err, ErrMalformed) {
			t.Errorf("a gone byte of %d gave %v, want ErrMalformed; accepted, it is a %d-byte "+
				"spelling of the same state as 0, and the encoding stops being canonical", gone, err, len(in))
		}
	}

	// And the two it does accept still mean what they say, so this test cannot
	// pass by refusing everything.
	for _, c := range []struct {
		gone byte
		want bool
	}{{0, false}, {1, true}} {
		in := bytes.Clone(raw)
		in[at] = c.gone
		var u Update
		err := u.UnmarshalBinary(in)
		if c.gone == 1 {
			// A gone update carries nothing after the flag, so the body that
			// follows here is refused -- by the length check, not by this guard.
			if err == nil {
				t.Error("a gone update with a cursor body after it was accepted")
			}
			continue
		}
		if err != nil {
			t.Errorf("a gone byte of %d gave %v, want it accepted", c.gone, err)
		}
		if u.Gone != c.want {
			t.Errorf("a gone byte of %d decoded to Gone=%v, want %v", c.gone, u.Gone, c.want)
		}
	}
}

// Which part of that guard carries it was measured too, because the guard is
// four clauses in one line and only one of them is held here:
//
//	the whole guard deleted              this test goes red
//	only `gone > 1` dropped              this test goes red
//	only the three `!okN` checks dropped the whole suite stays green
//
// The reads failing is refused again below -- a site, a clock and a byte that
// did not decode leave a buffer the next read runs out of. The clause about what
// the byte MEANS is the one nothing else says.

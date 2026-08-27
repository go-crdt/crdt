package crdt

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

// A Lamport clock is raised by whatever arrives, so the number a peer puts on an
// operation is a number this replica adopts. Without a ceiling that is enough to
// break a replica permanently, and it takes one operation: the clock wraps, and
// every edit made afterwards carries a timestamp below its own sequence number —
// which is to say an operation this replica's own validator rejects, and which
// loses every tie it takes part in. These tests are the ceiling, from both
// sides: what it refuses, and what it must still accept.

// uv appends an unsigned varint, which is what every field below is.
func uv(dst []byte, v uint64) []byte { return binary.AppendUvarint(dst, v) }

// remoteInsert returns an insertion from another site carrying the given clock,
// built by hand because no honest replica would issue one at the ceiling.
func remoteInsert(site SiteID, clock uint64) Op {
	return Op{Kind: OpInsert, ID: ID{Site: site, Seq: 1}, Clock: clock, Char: 'x'}
}

func TestClockAboveTheCeilingIsRefused(t *testing.T) {
	for _, clock := range []uint64{MaxClock + 1, math.MaxUint64} {
		d := New(1)
		if err := d.Apply(remoteInsert(2, clock)); !errors.Is(err, ErrInvalidOp) {
			t.Fatalf("Apply(clock=%d) = %v, want ErrInvalidOp", clock, err)
		}
		// Refused means untouched: the document must still be writable, and what
		// it writes must be something its peers will take.
		ops, err := d.Insert(0, "a")
		if err != nil {
			t.Fatalf("Insert after a refused operation: %v", err)
		}
		for _, op := range ops {
			if err := op.validate(); err != nil {
				t.Fatalf("the document minted %+v, which it rejects: %v", op, err)
			}
		}
		if got := d.String(); got != "a" {
			t.Fatalf("String() = %q, want %q", got, "a")
		}
	}

	l := NewList(1)
	above := ListOp{Kind: OpInsert, ID: ID{Site: 2, Seq: 1}, Clock: MaxClock + 1, Value: []byte("x")}
	if err := l.Apply(above); !errors.Is(err, ErrInvalidOp) {
		t.Fatalf("List.Apply above the ceiling = %v, want ErrInvalidOp", err)
	}
	m := NewMap(1)
	if err := m.Apply(MapOp{Kind: MapSet, ID: ID{Site: 2, Seq: 1}, Clock: MaxClock + 1, Key: "k"}); !errors.Is(err, ErrInvalidOp) {
		t.Fatalf("Map.Apply above the ceiling = %v, want ErrInvalidOp", err)
	}
}

// The ceiling itself is a legal clock — it is the top of the range, not past it.
// What a replica sitting on it may not do is mint, and it says so rather than
// wrapping. That is the whole of the residual: a peer claiming the ceiling can
// stop this replica writing, but it cannot make it write something wrong.
func TestTheCeilingItselfIsLegalAndStopsMinting(t *testing.T) {
	d := New(1)
	if err := d.Apply(remoteInsert(2, MaxClock)); err != nil {
		t.Fatalf("Apply(clock=MaxClock) = %v, want it accepted", err)
	}
	ops, err := d.Insert(0, "a")
	if !errors.Is(err, ErrExhausted) {
		t.Fatalf("Insert = %v, want ErrExhausted", err)
	}
	if ops != nil {
		t.Fatalf("Insert returned %d operations while reporting exhaustion", len(ops))
	}
	if got := d.String(); got != "x" {
		t.Fatalf("a refused insert changed the text to %q", got)
	}
	if _, err := d.Delete(0, 1); !errors.Is(err, ErrExhausted) {
		t.Fatalf("Delete = %v, want ErrExhausted", err)
	}
	if got := d.String(); got != "x" {
		t.Fatalf("a refused delete changed the text to %q", got)
	}

	l := NewList(1)
	at := ListOp{Kind: OpInsert, ID: ID{Site: 2, Seq: 1}, Clock: MaxClock, Value: []byte("x")}
	if err := l.Apply(at); err != nil {
		t.Fatalf("List.Apply(clock=MaxClock) = %v", err)
	}
	if _, err := l.Insert(0, []byte("a")); !errors.Is(err, ErrExhausted) {
		t.Fatalf("List.Insert = %v, want ErrExhausted", err)
	}
	if _, err := l.Delete(0, 1); !errors.Is(err, ErrExhausted) {
		t.Fatalf("List.Delete = %v, want ErrExhausted", err)
	}
	if l.Len() != 1 {
		t.Fatalf("a refused delete changed the list to %d values", l.Len())
	}

	m := NewMap(1)
	if err := m.Apply(MapOp{Kind: MapSet, ID: ID{Site: 2, Seq: 1}, Clock: MaxClock, Key: "k"}); err != nil {
		t.Fatalf("Map.Apply(clock=MaxClock) = %v", err)
	}
	if _, err := m.Set("j", []byte("a")); !errors.Is(err, ErrExhausted) {
		t.Fatalf("Map.Set = %v, want ErrExhausted", err)
	}
	if _, err := m.Delete("k"); !errors.Is(err, ErrExhausted) {
		t.Fatalf("Map.Delete = %v, want ErrExhausted", err)
	}
	if m.Len() != 1 {
		t.Fatalf("a refused delete changed the map to %d keys", m.Len())
	}
}

// An edit asks for room once, for all of it, so it either happens or does not.
// Half an insertion would leave the caller's text and the document disagreeing
// with no way to tell how far it got.
func TestAnEditThatDoesNotFitIsNotHalfMade(t *testing.T) {
	d := New(1)
	if err := d.Apply(remoteInsert(2, MaxClock-2)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Insert(0, "abc"); !errors.Is(err, ErrExhausted) {
		t.Fatalf("Insert of three into room for two = %v, want ErrExhausted", err)
	}
	if got := d.String(); got != "x" {
		t.Fatalf("String() = %q, want the document untouched", got)
	}
	// Two fit exactly, and the second one is the last this site can ever issue.
	ops, err := d.Insert(0, "ab")
	if err != nil {
		t.Fatalf("Insert of two into room for two: %v", err)
	}
	if len(ops) != 2 || ops[1].Clock != MaxClock {
		t.Fatalf("ops = %+v, want the second one at the ceiling", ops)
	}
	for _, op := range ops {
		if err := op.validate(); err != nil {
			t.Fatalf("minted %+v, which is invalid: %v", op, err)
		}
	}
	if _, err := d.Insert(0, "c"); !errors.Is(err, ErrExhausted) {
		t.Fatalf("Insert with no room left = %v, want ErrExhausted", err)
	}
	// A list refuses a batch that does not fit for the same reason.
	l := NewList(1)
	if err := l.Apply(ListOp{Kind: OpInsert, ID: ID{Site: 2, Seq: 1}, Clock: MaxClock - 1, Value: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Insert(0, []byte("a"), []byte("b")); !errors.Is(err, ErrExhausted) {
		t.Fatalf("List.Insert of two into room for one = %v, want ErrExhausted", err)
	}
	if l.Len() != 1 {
		t.Fatalf("a refused insert changed the list to %d values", l.Len())
	}
}

// An operation above the ceiling must not be encodable either: a sender that
// produced one would be handing a peer bytes the peer is bound to refuse.
func TestTheCeilingHoldsOnTheWire(t *testing.T) {
	if _, err := remoteInsert(2, MaxClock+1).MarshalBinary(); !errors.Is(err, ErrInvalidOp) {
		t.Fatalf("Op.MarshalBinary above the ceiling = %v, want ErrInvalidOp", err)
	}
	bad := MapOp{Kind: MapSet, ID: ID{Site: 2, Seq: 1}, Clock: MaxClock + 1}
	if _, err := bad.MarshalBinary(); !errors.Is(err, ErrInvalidOp) {
		t.Fatalf("MapOp.MarshalBinary above the ceiling = %v, want ErrInvalidOp", err)
	}
	// The control: the ceiling itself goes out and comes back unchanged, so what
	// the two checks above rejected was the excess and nothing else.
	good := remoteInsert(2, MaxClock)
	encoded, err := good.MarshalBinary()
	if err != nil {
		t.Fatalf("the ceiling itself must encode: %v", err)
	}
	var back Op
	if err := back.UnmarshalBinary(encoded); err != nil {
		t.Fatalf("the ceiling itself must decode: %v", err)
	}
	if back != good {
		t.Fatalf("round trip gave %+v, want %+v", back, good)
	}
}

// A snapshot is bytes from elsewhere, and its version vector is what the rest of
// the loader measures against. A vector promising a sequence number above the
// ceiling promises an operation no replica could have issued.
//
// Every case below is a pair: the same bytes with a legal clock must load. A
// rejection on its own would prove only that the snapshot was wrong somehow.
func TestSnapshotsRefuseAClockAboveTheCeiling(t *testing.T) {
	// A version 2 document: header, one site, one run of one character.
	docRun := func(vvSeq, clock uint64) []byte {
		s := append([]byte{}, snapshotMagic[:]...)
		s = append(s, snapshotVersion)
		s = uv(s, 1)     // one site
		s = uv(s, 1)     // site 1
		s = uv(s, vvSeq) // at this sequence number
		s = uv(s, 0)     // version 6: an empty collection floor
		s = uv(s, 0)     // and nothing collected
		s = uv(s, 1)     // one run
		// Version 5 writes each field in a column of its own, length-prefixed.
		// The sequence number is a step from nothing, and the clock the
		// distance above it — a clock below its sequence is not expressible,
		// which is the point of writing it that way.
		col := func(vs ...uint64) []byte {
			var b []byte
			for _, v := range vs {
				b = uv(b, v)
			}
			return b
		}
		for _, c := range [][]byte{
			col(1),                    // run sites
			col(zigzag(int64(vvSeq))), // run sequence, as a step
			col(clock - vvSeq),        // clock, as the distance above it
			col(0),                    // origin site: root
			col(0),                    // origin sequence, a step of zero
			col(1),                    // one character
			col(uint64('a')),          // the text
			col(0),                    // no deletions
			nil,                       // and so no deletion fields
		} {
			s = uv(s, uint64(len(c)))
			s = append(s, c...)
		}
		s = uv(s, 0) // no duplicate deletes
		return s
	}
	// The version 1 form, one record per character, still read.
	docChar := func(clock uint64) []byte {
		s := append([]byte{}, snapshotMagic[:]...)
		s = append(s, snapshotVersionV1)
		s = uv(s, 1)
		s = uv(s, 1)
		s = uv(s, 1)     // site 1 at sequence number 1
		s = uv(s, 1)     // one character
		s = uv(s, 1)     // its site
		s = uv(s, 1)     // its sequence number
		s = uv(s, clock) // its clock
		s = uv(s, 0)
		s = uv(s, 0) // origin = root
		s = uv(s, 'a')
		s = uv(s, 0)
		s = uv(s, 0) // not deleted
		s = uv(s, 0) // no duplicate deletes
		return s
	}
	listElem := func(vvSeq, clock uint64) []byte {
		s := append([]byte{}, listMagic[:]...)
		s = append(s, listVersion)
		s = uv(s, 1)
		s = uv(s, 1)
		s = uv(s, vvSeq)
		s = uv(s, 0)     // version 2: an empty collection floor
		s = uv(s, 0)     // and nothing collected
		s = uv(s, 1)     // one element
		s = uv(s, 1)     // its site
		s = uv(s, vvSeq) // its sequence number
		s = uv(s, clock) // its clock
		s = uv(s, 0)
		s = uv(s, 0) // origin = root
		s = uv(s, 0)
		s = uv(s, 0) // not deleted
		s = uv(s, 1) // one byte of value
		s = append(s, 'a')
		return uv(s, 0) // no duplicate deletes
	}
	mapRec := func(vvSeq, clock uint64) []byte {
		s := append([]byte{}, mapMagic[:]...)
		s = append(s, mapVersion)
		s = uv(s, 0) // version 2: nothing collected
		s = uv(s, 1)
		s = uv(s, 1)
		s = uv(s, vvSeq)
		s = uv(s, 1) // one key
		s = appendKey(s, "k")
		s = uv(s, 1)     // its site
		s = uv(s, vvSeq) // its sequence number
		s = uv(s, clock) // its clock
		return append(s, 0)
	}

	cases := []struct {
		name string
		load func(uint64, uint64) error
		vv   uint64
	}{
		{"document run", func(vvSeq, clock uint64) error {
			_, err := Load(2, docRun(vvSeq, clock))
			return err
		}, 1},
		{"version 1 character", func(_, clock uint64) error {
			_, err := Load(2, docChar(clock))
			return err
		}, 1},
		{"list element", func(vvSeq, clock uint64) error {
			_, err := LoadList(2, listElem(vvSeq, clock))
			return err
		}, 1},
		{"map record", func(vvSeq, clock uint64) error {
			_, err := LoadMap(2, mapRec(vvSeq, clock))
			return err
		}, 1},
	}
	for _, c := range cases {
		// The control: a clock at the ceiling is legal and must load.
		if err := c.load(c.vv, MaxClock); err != nil {
			t.Fatalf("%s at the ceiling did not load: %v", c.name, err)
		}
		if err := c.load(c.vv, MaxClock+1); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s above the ceiling = %v, want ErrMalformed", c.name, err)
		}
		// And a version vector promising a sequence number past the ceiling
		// promises an operation that cannot exist, whatever follows it.
		if err := c.load(MaxClock+1, math.MaxUint64); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s with a vector above the ceiling = %v, want ErrMalformed", c.name, err)
		}
	}
}

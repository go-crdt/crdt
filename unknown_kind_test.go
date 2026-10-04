package crdt

import (
	"errors"
	"testing"
)

// A kind this build does not know is a kind it refuses -- the reservation the
// forward compatibility in go-crdt/crdt#80 rests on, stated where OpSuperseded
// is declared and enforced in three decoders that each read the kind from the
// first byte of a record.
//
// Nothing tested it. Deleting the check in decodeOp left the whole suite green:
// ParseOps does not call Op.validate, so the refusal one layer down -- which is
// what makes the mutant survivable -- happens at apply time and not at the parse
// boundary a server reaches first. The same check in decodeListOp and
// decodeMapOp had no test either.
//
// The unmodified batch is parsed first in every case. Without that, "the parse
// refused it" would also be satisfied by bytes that were never valid, which is
// the way a test like this passes for the wrong reason.
func TestEveryDecoderRefusesAnUnknownOperationKind(t *testing.T) {
	text := func() []byte {
		d := New(1)
		ops, err := d.Insert(0, "a")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := AppendOps(nil, ops)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}()
	list := func() []byte {
		l := NewList(1)
		ops, err := l.Insert(0, []byte("a"))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := AppendListOps(nil, ops)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}()
	mapped := func() []byte {
		m := NewMap(1)
		op, err := m.Set("k", []byte("v"))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := AppendMapOps(nil, []MapOp{op})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}()

	for _, tc := range []struct {
		name  string
		raw   []byte
		parse func([]byte) error
	}{
		{"text", text, func(b []byte) error { _, err := ParseOps(b); return err }},
		{"list", list, func(b []byte) error { _, err := ParseListOps(b); return err }},
		{"map", mapped, func(b []byte) error { _, err := ParseMapOps(b); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// One operation, so the count is one byte and the kind is the next.
			if len(tc.raw) < 2 || tc.raw[0] != 1 {
				t.Fatalf("expected a one-operation batch, got % x", tc.raw)
			}
			if err := tc.parse(tc.raw); err != nil {
				t.Fatalf("the unmodified batch does not parse, so this test cannot "+
					"say anything about the kind byte: %v", err)
			}
			for _, kind := range []byte{0, 4, 7, 99, 255} {
				altered := append([]byte(nil), tc.raw...)
				altered[1] = kind
				err := tc.parse(altered)
				if !errors.Is(err, ErrInvalidOp) {
					t.Errorf("kind %d parsed as %v, want ErrInvalidOp: a kind this "+
						"build does not know has to be refused where the bytes are "+
						"read, not deeper in", kind, err)
				}
			}
		})
	}
}

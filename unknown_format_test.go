package crdt

import (
	"errors"
	"testing"
)

// A snapshot this build has no reader for is told apart from bytes that are
// damaged, because the person holding them needs different things.
//
// The ordinary way to meet the first is a peer running a newer build -- a
// snapshot travels, and in go-crdt/collab a joining client loads one the server
// sends it. The answer is to upgrade. Reported as a malformed encoding, which is
// what it used to be, it reads as corruption and sends somebody after the wrong
// thing entirely.
func TestAFormatThisBuildDoesNotKnowIsToldApartFromDamage(t *testing.T) {
	doc := New(1)
	if _, err := doc.Insert(0, "hello"); err != nil {
		t.Fatal(err)
	}
	list := NewList(1)
	if _, err := list.Insert(0, []byte("x")); err != nil {
		t.Fatal(err)
	}
	m := NewMap(1)
	if _, err := m.Set("k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	comp := NewComposite(1)
	ct, err := comp.Text("t")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ct.Insert(0, "hi"); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name string
		snap []byte
		load func([]byte) error
	}{
		{"a document", doc.Snapshot(), func(b []byte) error { _, err := Load(2, b); return err }},
		{"a list", list.Snapshot(), func(b []byte) error { _, err := LoadList(2, b); return err }},
		{"a map", m.Snapshot(), func(b []byte) error { _, err := LoadMap(2, b); return err }},
		{"a composite", comp.Snapshot(), func(b []byte) error { _, err := LoadComposite(2, b); return err }},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The version byte is the one immediately after the magic, and the
			// magics differ in length, so it is found by walking past the
			// printable bytes rather than by a constant that would be wrong for
			// three of the four.
			at := 0
			for at < len(c.snap) && c.snap[at] >= 'a' && c.snap[at] <= 'z' {
				at++
			}
			future := append([]byte(nil), c.snap...)
			future[at] = 200 // a version no build has ever written

			err := c.load(future)
			if !errors.Is(err, ErrUnknownFormat) {
				t.Fatalf("a version this build cannot read gave %v, want ErrUnknownFormat", err)
			}
			// And a caller that only ever asked the broad question still gets
			// the same answer as before.
			if !errors.Is(err, ErrMalformed) {
				t.Fatal("ErrUnknownFormat stopped answering to ErrMalformed, which is a broken promise")
			}

			// Damage is not a format this build does not know, and the two must
			// not collapse into one another.
			damaged := append([]byte(nil), c.snap...)
			damaged[len(damaged)-1] ^= 0xFF
			damaged = damaged[:len(damaged)-1]
			if err := c.load(damaged); err != nil && errors.Is(err, ErrUnknownFormat) {
				t.Fatalf("truncated bytes were reported as an unknown format: %v", err)
			}
		})
	}
}

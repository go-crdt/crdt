package crdt

import (
	"testing"
	"unicode/utf8"
)

// Everything a replica receives comes off a network, so every decoder in this
// package is a trust boundary. The fuzz targets assert the two things that
// matter there: hostile bytes are rejected rather than crashing the process, and
// bytes that are accepted describe a document that holds together.

// corpus returns encoded operations and a snapshot from a small real session, to
// seed the fuzzer with input shaped like the real thing.
func corpus(t *testing.T) (ops []byte, snapshot []byte) {
	t.Helper()
	a, b := New(1), New(2)
	edits := insert(t, a, 0, "seed 世界")
	apply(t, b, edits)
	edits = append(edits, remove(t, a, 2, 2)...)
	edits = append(edits, insert(t, b, 0, "x")...)
	apply(t, a, edits[len(edits)-1:])
	encoded, err := AppendOps(nil, edits)
	if err != nil {
		t.Fatal(err)
	}
	return encoded, a.Snapshot()
}

func FuzzParseOps(f *testing.F) {
	ops, _ := corpus(&testing.T{})
	f.Add(ops)
	f.Add([]byte{})
	f.Add([]byte{1})

	f.Fuzz(func(t *testing.T, data []byte) {
		parsed, err := ParseOps(data)
		if err != nil {
			return
		}
		// Anything that decodes must re-encode to something that decodes the same
		// way; otherwise two replicas could disagree about what they received.
		encoded, err := AppendOps(nil, parsed)
		if err != nil {
			t.Fatalf("re-encoding accepted operations failed: %v", err)
		}
		again, err := ParseOps(encoded)
		if err != nil {
			t.Fatalf("re-encoded operations no longer parse: %v", err)
		}
		if len(again) != len(parsed) {
			t.Fatalf("round trip changed the batch size: %d, want %d", len(again), len(parsed))
		}
		for i := range again {
			if again[i] != parsed[i] {
				t.Fatalf("round trip changed operation %d: %+v, want %+v", i, again[i], parsed[i])
			}
		}
	})
}

// FuzzApply feeds arbitrary operations to a document. Whatever it accepts, the
// document must stay coherent: its length must match its text, applying the same
// batch twice must change nothing, and nothing may panic.
func FuzzApply(f *testing.F) {
	ops, _ := corpus(&testing.T{})
	f.Add(ops)
	f.Add([]byte{0})

	f.Fuzz(func(t *testing.T, data []byte) {
		parsed, err := ParseOps(data)
		if err != nil {
			return
		}
		d := New(99)
		if err := d.Apply(parsed...); err != nil {
			return
		}
		text := d.String()
		if got, want := d.Len(), utf8.RuneCountInString(text); got != want {
			t.Fatalf("Len() = %d, but the text holds %d runes", got, want)
		}
		if d.Tombstones() < 0 {
			t.Fatalf("Tombstones() = %d", d.Tombstones())
		}
		before := string(d.Snapshot())
		if err := d.Apply(parsed...); err != nil {
			t.Fatalf("replaying an accepted batch was rejected: %v", err)
		}
		if got := string(d.Snapshot()); got != before {
			t.Fatal("replaying an accepted batch changed the document")
		}
	})
}

func FuzzLoad(f *testing.F) {
	_, snapshot := corpus(&testing.T{})
	f.Add(snapshot)
	f.Add([]byte("crdt\x01"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		d, err := Load(1, data)
		if err != nil {
			return
		}
		text := d.String()
		if got, want := d.Len(), utf8.RuneCountInString(text); got != want {
			t.Fatalf("Len() = %d, but the text holds %d runes", got, want)
		}
		// Re-encoding a loaded document must be a fixed point: the snapshot
		// format has one canonical form, whatever an accepted input looked like.
		again, err := Load(1, d.Snapshot())
		if err != nil {
			t.Fatalf("a document could not reload its own snapshot: %v", err)
		}
		if got := again.String(); got != text {
			t.Fatalf("reloading changed the text: %q, want %q", got, text)
		}
		if string(again.Snapshot()) != string(d.Snapshot()) {
			t.Fatal("re-encoding a loaded snapshot is not a fixed point")
		}
		// The history has to survive a round trip through the wire format too.
		replayed := New(2)
		if err := replayed.Apply(d.OpsSince(nil)...); err != nil {
			t.Fatalf("replaying a loaded document's history was rejected: %v", err)
		}
		if got := replayed.String(); got != text {
			t.Fatalf("replaying the history gave %q, want %q", got, text)
		}
	})
}

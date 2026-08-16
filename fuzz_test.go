package crdt

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf16"
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

// A version vector arrives from a peer asking to be caught up, so it is decoded
// from bytes someone else wrote.
func FuzzVersionVector(f *testing.F) {
	seed, err := VersionVector{1: 4, 9: 2}.MarshalBinary()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add([]byte{0})

	f.Fuzz(func(t *testing.T, data []byte) {
		var v VersionVector
		if err := v.UnmarshalBinary(data); err != nil {
			return
		}
		encoded, err := v.MarshalBinary()
		if err != nil {
			t.Fatalf("re-encoding an accepted vector failed: %v", err)
		}
		var again VersionVector
		if err := again.UnmarshalBinary(encoded); err != nil {
			t.Fatalf("a re-encoded vector no longer decodes: %v", err)
		}
		if !again.Equal(v) {
			t.Fatalf("round trip gave %v, want %v", again, v)
		}
		// Whatever it decodes to must be usable as a peer's position: asking a
		// document what it holds beyond that vector must not panic.
		d := New(1)
		if _, err := d.Insert(0, "text"); err != nil {
			t.Fatal(err)
		}
		_ = d.OpsSince(v)
	})
}

// FuzzUTF16Offsets is the one target here that is not about a decoder, and it
// earns its place for the same reason the others do: the offsets come from
// outside. A browser computes them, and it computes them over a string this
// package built out of whatever anyone typed.
//
// The invariant is stated against unicode/utf16 rather than against anything in
// utf16.go — a second implementation of the encoding, in the standard library —
// and it is checked on a document with a stretch cut out of it, because a
// tombstoned supplementary character is the case where a count kept per subtree
// can be wrong without the text looking wrong.
func FuzzUTF16Offsets(f *testing.F) {
	f.Add("a\U0001F600b", 1, 1)
	f.Add("\U0001F600\U0001F601\U0001F602", 0, 2)
	f.Add("plain ascii", 3, 4)
	f.Add("é日\U00020BB7\U0001D538x", 2, 2)
	f.Fuzz(func(t *testing.T, text string, cut, length int) {
		// Whatever the fuzzer produced is made into something a document can
		// hold, rather than skipped: a skipped input tests nothing.
		text = strings.ToValidUTF8(text, "")
		if runes := []rune(text); len(runes) > 256 {
			text = string(runes[:256])
		}
		d := New(1)
		if _, err := d.Insert(0, text); err != nil {
			t.Fatalf("Insert(0, %q): %v", text, err)
		}
		if n := d.Len(); n > 0 {
			cut = ((cut % n) + n) % n
			length = ((length % (n - cut + 1)) + (n - cut + 1)) % (n - cut + 1)
			if _, err := d.Delete(cut, length); err != nil {
				t.Fatalf("Delete(%d, %d): %v", cut, length, err)
			}
		}

		runes := []rune(d.String())
		units := utf16.Encode(runes)
		if d.LenUTF16() != len(units) {
			t.Fatalf("LenUTF16 = %d, unicode/utf16 encodes %q in %d units", d.LenUTF16(), d.String(), len(units))
		}
		at := 0
		for i, r := range runes {
			pos, err := d.RuneOffset(at)
			if err != nil || pos != i {
				t.Fatalf("RuneOffset(%d) = %d, %v; want %d, nil", at, pos, err, i)
			}
			back, err := d.UTF16Offset(i)
			if err != nil || back != at {
				t.Fatalf("UTF16Offset(%d) = %d, %v; want %d, nil", i, back, err, at)
			}
			if n := len(utf16.Encode([]rune{r})); n == 2 {
				if pos, err := d.RuneOffset(at + 1); err != ErrSurrogateBoundary {
					t.Fatalf("RuneOffset(%d) = %d, %v; that offset splits %q", at+1, pos, err, string(r))
				}
			}
			at += len(utf16.Encode([]rune{r}))
		}
		if pos, err := d.RuneOffset(at); err != nil || pos != len(runes) {
			t.Fatalf("RuneOffset(%d) = %d, %v; want %d, nil", at, pos, err, len(runes))
		}
	})
}

// A list snapshot arrives from a server, so its decoder is a trust boundary like
// every other in this package.
func FuzzLoadList(f *testing.F) {
	l := NewList(1)
	if _, err := l.Insert(0, []byte("one"), []byte("two"), []byte("three")); err != nil {
		f.Fatal(err)
	}
	if _, err := l.Delete(1, 1); err != nil {
		f.Fatal(err)
	}
	f.Add(l.Snapshot())
	f.Add([]byte("crdl\x01"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		loaded, err := LoadList(1, data)
		if err != nil {
			return
		}
		if got, want := loaded.Len()+loaded.Tombstones(), len(loaded.elements); got != want {
			t.Fatalf("Len()+Tombstones() = %d, but the list holds %d elements", got, want)
		}
		// Re-encoding an accepted snapshot must be a fixed point: the format has
		// one canonical form, whatever an accepted input looked like.
		again, err := LoadList(1, loaded.Snapshot())
		if err != nil {
			t.Fatalf("a list could not reload its own snapshot: %v", err)
		}
		if !bytes.Equal(again.Snapshot(), loaded.Snapshot()) {
			t.Fatal("re-encoding a loaded snapshot is not a fixed point")
		}
		// And the history has to replay into a fresh replica.
		replayed := NewList(2)
		if err := replayed.Apply(loaded.OpsSince(nil)...); err != nil {
			t.Fatalf("replaying a loaded list's history was rejected: %v", err)
		}
		if !bytes.Equal(replayed.Snapshot(), loaded.Snapshot()) {
			t.Fatal("replaying the history did not reproduce the state")
		}
	})
}

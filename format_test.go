package crdt

import (
	"errors"
	"testing"
)

// What Reads says is what the loaders do, established by trying every version
// rather than by keeping a list in step with them by hand.
//
// This is the whole worth of the thing: a peer says what it reads so another can
// avoid sending it something unreadable, and a number that has drifted from the
// loader is worse than no number, because it is believed.
func TestFormatsMatchTheLoaders(t *testing.T) {
	for _, c := range []struct {
		format Format
		load   func(version byte) error
	}{
		{FormatText, func(v byte) error { _, err := Load(2, stamped(t, textSnapshot(t), v)); return err }},
		{FormatList, func(v byte) error { _, err := LoadList(2, stamped(t, listSnapshot(t), v)); return err }},
		{FormatMap, func(v byte) error { _, err := LoadMap(2, stamped(t, mapSnapshot(t), v)); return err }},
		{FormatComposite, func(v byte) error { _, err := LoadComposite(2, stamped(t, compositeSnapshot(t), v)); return err }},
	} {
		t.Run(c.format.String(), func(t *testing.T) {
			claimed := Reads(c.format)
			if len(claimed) == 0 {
				t.Fatal("this build reads no version of a format it lists")
			}
			// Every version 1..255 is tried, so the claim is checked in both
			// directions at once: nothing claimed may be refused for its
			// version, and nothing unclaimed may be accepted. A range would
			// have missed the gap where 7 is.
			set := map[byte]bool{}
			for _, v := range claimed {
				set[v] = true
			}
			for v := 1; v <= 255; v++ {
				err := c.load(byte(v))
				unknown := errors.Is(err, ErrUnknownFormat)
				switch {
				case set[byte(v)] && unknown:
					t.Errorf("version %d is claimed readable and is refused as unknown", v)
				case !set[byte(v)] && !unknown:
					t.Errorf("version %d is not claimed and was not refused as unknown: %v", v, err)
				}
			}
			// What is written must be among what is read, or this build cannot
			// read itself.
			if w := Writes(c.format); !set[w] {
				t.Errorf("Writes = %d, which is not among the versions read %v", w, claimed)
			}
		})
	}
}

// A format this build does not know says so with a zero rather than by guessing.
func TestAnUnknownFormatReadsNothing(t *testing.T) {
	future := Format(200)
	if got := Reads(future); len(got) != 0 {
		t.Errorf("Reads(unknown) = %v, want nothing", got)
	}
	if got := Writes(future); got != 0 {
		t.Errorf("Writes(unknown) = %d, want 0", got)
	}
	if got, want := future.String(), "unknown format"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	if len(Formats()) != 4 {
		t.Errorf("Formats() lists %d formats, want 4", len(Formats()))
	}
}

// stamped rewrites a snapshot's version byte, which is the one immediately after
// the magic. The bytes below it are then wrong for that version -- that is the
// point: the loader must refuse for the version or not at all.
func stamped(t *testing.T, snapshot []byte, version byte) []byte {
	t.Helper()
	at := 0
	for at < len(snapshot) && snapshot[at] >= 'a' && snapshot[at] <= 'z' {
		at++
	}
	out := append([]byte(nil), snapshot...)
	out[at] = version
	return out
}

func textSnapshot(t *testing.T) []byte {
	t.Helper()
	d := New(1)
	if _, err := d.Insert(0, "hello"); err != nil {
		t.Fatal(err)
	}
	return d.Snapshot()
}

func listSnapshot(t *testing.T) []byte {
	t.Helper()
	l := NewList(1)
	if _, err := l.Insert(0, []byte("x")); err != nil {
		t.Fatal(err)
	}
	return l.Snapshot()
}

func mapSnapshot(t *testing.T) []byte {
	t.Helper()
	m := NewMap(1)
	if _, err := m.Set("k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	return m.Snapshot()
}

func compositeSnapshot(t *testing.T) []byte {
	t.Helper()
	c := NewComposite(1)
	text, err := c.Text("t")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := text.Insert(0, "hi"); err != nil {
		t.Fatal(err)
	}
	return c.Snapshot()
}

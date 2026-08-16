package crdt

import (
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf16"
)

// The control corpus in utf16_control_test.go decides whether the answers are
// right, because JavaScript decides that. What is here is everything the corpus
// cannot state: the error contracts, the cases that need two replicas, and a
// randomised sweep wide enough to reach shapes the corpus does not name.

// mustInsert and mustDelete keep the tests below about what is being asserted.
func mustInsert(t *testing.T, d *Doc, pos int, text string) {
	t.Helper()
	if _, err := d.Insert(pos, text); err != nil {
		t.Fatalf("Insert(%d, %q): %v", pos, text, err)
	}
}

func mustDelete(t *testing.T, d *Doc, pos, length int) {
	t.Helper()
	if _, err := d.Delete(pos, length); err != nil {
		t.Fatalf("Delete(%d, %d): %v", pos, length, err)
	}
}

func TestLenUTF16CountsCodeUnits(t *testing.T) {
	for _, c := range []struct {
		text  string
		runes int
		units int
	}{
		{"", 0, 0},
		{"abc", 3, 3},
		{"élan", 4, 4},         // multi-byte in UTF-8, one unit each
		{"日本語", 3, 3},          // BMP, three bytes each, one unit each
		{"\U0001F600", 1, 2},   // one emoji
		{"a\U0001F600b", 3, 4}, // and one in company
		{"\U0001D538", 1, 2},   // mathematical double-struck A
		{"🇫🇷", 2, 4},           // two regional indicators
		{"\U0001F600é日", 3, 4}, // one of each kind
	} {
		d := New(1)
		mustInsert(t, d, 0, c.text)
		if got := d.Len(); got != c.runes {
			t.Errorf("%q: Len = %d, want %d", c.text, got, c.runes)
		}
		if got := d.LenUTF16(); got != c.units {
			t.Errorf("%q: LenUTF16 = %d, want %d", c.text, got, c.units)
		}
		if got := len(utf16.Encode([]rune(c.text))); got != c.units {
			t.Errorf("%q: the corpus is wrong: unicode/utf16 encodes it in %d units", c.text, got)
		}
	}
}

// An offset inside a surrogate pair is the case the whole design turns on, so
// it is spelled out here as well as swept over in the control corpus.
func TestOffsetInsideASurrogatePair(t *testing.T) {
	d := New(1)
	mustInsert(t, d, 0, "a\U0001F600b") // units: a | 😀 😀 | b
	if got := d.LenUTF16(); got != 4 {
		t.Fatalf("LenUTF16 = %d, want 4", got)
	}
	for _, c := range []struct {
		u16  int
		rune int
	}{{0, 0}, {1, 1}, {3, 2}, {4, 3}} {
		at, err := d.RuneOffset(c.u16)
		if err != nil || at != c.rune {
			t.Errorf("RuneOffset(%d) = %d, %v; want %d, nil", c.u16, at, err, c.rune)
		}
		back, err := d.UTF16Offset(c.rune)
		if err != nil || back != c.u16 {
			t.Errorf("UTF16Offset(%d) = %d, %v; want %d, nil", c.rune, back, err, c.u16)
		}
	}

	// Offset 2 is between the emoji's two code units.
	if at, err := d.RuneOffset(2); err != ErrSurrogateBoundary {
		t.Errorf("RuneOffset(2) = %d, %v; want ErrSurrogateBoundary", at, err)
	}
	if _, err := d.InsertUTF16(2, "x"); err != ErrSurrogateBoundary {
		t.Errorf("InsertUTF16(2, %q) = %v, want ErrSurrogateBoundary", "x", err)
	}
	if _, err := d.DeleteUTF16(2, 1); err != ErrSurrogateBoundary {
		t.Errorf("DeleteUTF16(2, 1) = %v, want ErrSurrogateBoundary", err)
	}
	// A range whose far end splits the pair is refused just as its near end is.
	if _, err := d.DeleteUTF16(1, 1); err != ErrSurrogateBoundary {
		t.Errorf("DeleteUTF16(1, 1) = %v, want ErrSurrogateBoundary", err)
	}
	// Nothing was changed by any of the refusals.
	if got := d.String(); got != "a\U0001F600b" {
		t.Errorf("the document is now %q; a refused edit changed it", got)
	}

	// The rounding a tolerant caller would do, which the documentation promises
	// is one step: a split offset is always one past the character's first unit.
	at, err := d.RuneOffset(2 - 1)
	if err != nil || at != 1 {
		t.Errorf("RuneOffset(1) = %d, %v; want 1, nil — rounding down must not need a second API", at, err)
	}
}

// Insertions and deletions at every boundary of a surrogate pair, in a document
// that is nothing but surrogate pairs, so that every offset is either a
// boundary between characters or inside one.
func TestEveryBoundaryOfEveryPair(t *testing.T) {
	const text = "\U0001F600\U0001F601\U0001F602"
	for u16 := 0; u16 <= 6; u16++ {
		d := New(1)
		mustInsert(t, d, 0, text)
		ops, err := d.InsertUTF16(u16, "|")
		if u16%2 == 1 {
			if err != ErrSurrogateBoundary {
				t.Errorf("InsertUTF16(%d): %v, want ErrSurrogateBoundary", u16, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("InsertUTF16(%d): %v", u16, err)
		}
		runes := []rune(text)
		want := string(runes[:u16/2]) + "|" + string(runes[u16/2:])
		if got := d.String(); got != want {
			t.Errorf("InsertUTF16(%d) gives %q, want %q", u16, got, want)
		}
		if len(ops) != 1 {
			t.Errorf("InsertUTF16(%d) produced %d operations, want 1", u16, len(ops))
		}

		if u16 >= 6 {
			continue
		}
		e := New(1)
		mustInsert(t, e, 0, text)
		if _, err := e.DeleteUTF16(u16, 2); err != nil {
			t.Fatalf("DeleteUTF16(%d, 2): %v", u16, err)
		}
		want = string(runes[:u16/2]) + string(runes[u16/2+1:])
		if got := e.String(); got != want {
			t.Errorf("DeleteUTF16(%d, 2) gives %q, want %q", u16, got, want)
		}
	}
}

// A UTF-16 length is a count of units, not of characters: four units of emoji
// are two characters, and the count of operations proves which was removed.
func TestDeleteUTF16CountsUnitsNotCharacters(t *testing.T) {
	d := New(1)
	mustInsert(t, d, 0, "a\U0001F600\U0001F601b")
	ops, err := d.DeleteUTF16(1, 4)
	if err != nil {
		t.Fatalf("DeleteUTF16(1, 4): %v", err)
	}
	if len(ops) != 2 {
		t.Errorf("removing four code units produced %d operations, want 2", len(ops))
	}
	if got := d.String(); got != "ab" {
		t.Errorf("the document is %q, want %q", got, "ab")
	}
	if got := d.LenUTF16(); got != 2 {
		t.Errorf("LenUTF16 = %d, want 2", got)
	}
}

func TestUTF16OffsetsOutOfRange(t *testing.T) {
	for _, name := range []string{"plain", "supplementary"} {
		d := New(1)
		text := "abc"
		units := 3
		if name == "supplementary" {
			text, units = "a\U0001F600b", 4
		}
		mustInsert(t, d, 0, text)

		for _, pos := range []int{-1, units + 1} {
			if _, err := d.RuneOffset(pos); err != ErrOutOfRange {
				t.Errorf("%s: RuneOffset(%d) = %v, want ErrOutOfRange", name, pos, err)
			}
			if _, err := d.InsertUTF16(pos, "x"); err != ErrOutOfRange {
				t.Errorf("%s: InsertUTF16(%d) = %v, want ErrOutOfRange", name, pos, err)
			}
			if _, err := d.DeleteUTF16(pos, 0); err != ErrOutOfRange {
				t.Errorf("%s: DeleteUTF16(%d, 0) = %v, want ErrOutOfRange", name, pos, err)
			}
		}
		for _, pos := range []int{-1, 4} {
			if _, err := d.UTF16Offset(pos); err != ErrOutOfRange {
				t.Errorf("%s: UTF16Offset(%d) = %v, want ErrOutOfRange", name, pos, err)
			}
		}
		if _, err := d.DeleteUTF16(0, -1); err != ErrOutOfRange {
			t.Errorf("%s: DeleteUTF16(0, -1) = %v, want ErrOutOfRange", name, err)
		}
		if _, err := d.DeleteUTF16(1, units); err != ErrOutOfRange {
			t.Errorf("%s: DeleteUTF16(1, %d) = %v, want ErrOutOfRange", name, units, err)
		}
		// The end of the document converts both ways.
		if at, err := d.RuneOffset(units); err != nil || at != d.Len() {
			t.Errorf("%s: RuneOffset(%d) = %d, %v; want %d, nil", name, units, at, err, d.Len())
		}
		if at, err := d.UTF16Offset(d.Len()); err != nil || at != units {
			t.Errorf("%s: UTF16Offset(%d) = %d, %v; want %d, nil", name, d.Len(), at, err, units)
		}
		// InsertUTF16 passes the text through to Insert, errors included.
		if _, err := d.InsertUTF16(0, "\xff"); err != ErrInvalidText {
			t.Errorf("%s: InsertUTF16 with invalid UTF-8 = %v, want ErrInvalidText", name, err)
		}
	}
}

// An empty document has no offsets but zero, in either unit.
func TestUTF16OnAnEmptyDocument(t *testing.T) {
	d := New(1)
	if got := d.LenUTF16(); got != 0 {
		t.Fatalf("LenUTF16 = %d, want 0", got)
	}
	if at, err := d.RuneOffset(0); err != nil || at != 0 {
		t.Errorf("RuneOffset(0) = %d, %v", at, err)
	}
	if at, err := d.UTF16Offset(0); err != nil || at != 0 {
		t.Errorf("UTF16Offset(0) = %d, %v", at, err)
	}
	if ops, err := d.InsertUTF16(0, ""); err != nil || ops != nil {
		t.Errorf("InsertUTF16(0, \"\") = %v, %v", ops, err)
	}
}

// Deleting a supplementary character has to take both of its units off the
// count, and a document rebuilt from a snapshot has to agree about all of it.
func TestUTF16SurvivesDeletionAndSnapshots(t *testing.T) {
	d := New(1)
	mustInsert(t, d, 0, "a\U0001F600b\U0001F601c")
	if got := d.LenUTF16(); got != 7 {
		t.Fatalf("LenUTF16 = %d, want 7", got)
	}
	mustDelete(t, d, 1, 1) // the first emoji
	if got, want := d.LenUTF16(), 5; got != want {
		t.Errorf("after deleting an emoji LenUTF16 = %d, want %d", got, want)
	}
	checkIndex(t, d)

	loaded, err := Load(2, d.Snapshot())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	checkIndex(t, loaded)
	if got := loaded.LenUTF16(); got != d.LenUTF16() {
		t.Errorf("the reloaded document reports %d code units, the original %d", got, d.LenUTF16())
	}
	for u16 := 0; u16 <= d.LenUTF16(); u16++ {
		a, errA := d.RuneOffset(u16)
		b, errB := loaded.RuneOffset(u16)
		if a != b || (errA == nil) != (errB == nil) {
			t.Errorf("RuneOffset(%d): original %d/%v, reloaded %d/%v", u16, a, errA, b, errB)
		}
	}

	// And a peer that was sent the operations rather than the snapshot.
	peer := New(3)
	if err := peer.Apply(d.OpsSince(nil)...); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	checkIndex(t, peer)
	if got := peer.LenUTF16(); got != d.LenUTF16() {
		t.Errorf("the peer reports %d code units, the original %d", got, d.LenUTF16())
	}
}

// Two replicas deleting the same emoji at once produce two deletions of one
// character. Only one of them can be the character's recorded deletion, and the
// count of code units must fall by two once, not by four.
func TestConcurrentDeletionOfOneSupplementaryCharacter(t *testing.T) {
	ada, grace := New(1), New(2)
	ops, err := ada.Insert(0, "x\U0001F600y")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := grace.Apply(ops...); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	fromAda, err := ada.Delete(1, 1)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	fromGrace, err := grace.Delete(1, 1)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := ada.Apply(fromGrace...); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := grace.Apply(fromAda...); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, d := range []*Doc{ada, grace} {
		checkIndex(t, d)
		if got := d.LenUTF16(); got != 2 {
			t.Errorf("site %d reports %d code units, want 2", d.Site(), got)
		}
	}
}

// Every character of every plane, walked in both directions against
// unicode/utf16 — a different implementation of the same encoding, in the
// standard library, rather than this package's own arithmetic.
func TestUTF16AgainstTheStandardLibrary(t *testing.T) {
	alphabet := []rune("ab é 日本 \U0001F600\U0001D538\U00020BB7\U0001F1EB")
	rng := rand.New(rand.NewPCG(20260816, 3))
	d := New(1)
	for step := range 400 {
		switch {
		case d.Len() > 0 && step%7 == 3:
			pos := rng.IntN(d.Len())
			mustDelete(t, d, pos, 1+rng.IntN(min(4, d.Len()-pos)))
		default:
			var b strings.Builder
			for range 1 + rng.IntN(4) {
				b.WriteRune(alphabet[rng.IntN(len(alphabet))])
			}
			mustInsert(t, d, rng.IntN(d.Len()+1), b.String())
		}
		if step%13 != 0 {
			continue
		}
		checkIndex(t, d)
		checkAgainstEncoding(t, d)
	}
	checkAgainstEncoding(t, d)
}

// checkAgainstEncoding compares every offset of the document, in both
// directions, against the encoding unicode/utf16 produces for its text.
func checkAgainstEncoding(t *testing.T, d *Doc) {
	t.Helper()
	runes := []rune(d.String())
	units := utf16.Encode(runes)
	if d.LenUTF16() != len(units) {
		t.Fatalf("LenUTF16 = %d, unicode/utf16 encodes the text in %d units", d.LenUTF16(), len(units))
	}
	// wantRune[u] is the rune offset an offset of u units names, or -1 when it
	// splits a character.
	wantRune := make([]int, len(units)+1)
	at := 0
	for i, r := range runes {
		wantRune[at] = i
		if len(utf16.Encode([]rune{r})) == 2 {
			wantRune[at+1] = -1
		}
		at += len(utf16.Encode([]rune{r}))
	}
	wantRune[len(units)] = len(runes)

	for u16, want := range wantRune {
		got, err := d.RuneOffset(u16)
		if want < 0 {
			if err != ErrSurrogateBoundary {
				t.Fatalf("RuneOffset(%d) = %d, %v; want ErrSurrogateBoundary", u16, got, err)
			}
			continue
		}
		if err != nil || got != want {
			t.Fatalf("RuneOffset(%d) = %d, %v; want %d, nil", u16, got, err, want)
		}
		back, err := d.UTF16Offset(want)
		if err != nil || back != u16 {
			t.Fatalf("UTF16Offset(%d) = %d, %v; want %d, nil", want, back, err, u16)
		}
	}
}

// Editing through the UTF-16 API and through the rune API must produce the same
// document, operation for operation, or the two are not the same edit.
func TestUTF16EditingMatchesRuneEditing(t *testing.T) {
	rng := rand.New(rand.NewPCG(4, 20260816))
	alphabet := []rune("xy\U0001F600é\U0001D538日")
	runeDoc, unitDoc := New(1), New(1)
	for range 300 {
		var b strings.Builder
		for range 1 + rng.IntN(3) {
			b.WriteRune(alphabet[rng.IntN(len(alphabet))])
		}
		pos := rng.IntN(runeDoc.Len() + 1)
		u16, err := runeDoc.UTF16Offset(pos)
		if err != nil {
			t.Fatalf("UTF16Offset(%d): %v", pos, err)
		}
		mustInsert(t, runeDoc, pos, b.String())
		if _, err := unitDoc.InsertUTF16(u16, b.String()); err != nil {
			t.Fatalf("InsertUTF16(%d, %q): %v", u16, b.String(), err)
		}
		if runeDoc.Len() > 2 {
			pos = rng.IntN(runeDoc.Len() - 1)
			from, err := runeDoc.UTF16Offset(pos)
			if err != nil {
				t.Fatalf("UTF16Offset(%d): %v", pos, err)
			}
			to, err := runeDoc.UTF16Offset(pos + 2)
			if err != nil {
				t.Fatalf("UTF16Offset(%d): %v", pos+2, err)
			}
			mustDelete(t, runeDoc, pos, 2)
			if _, err := unitDoc.DeleteUTF16(from, to-from); err != nil {
				t.Fatalf("DeleteUTF16(%d, %d): %v", from, to-from, err)
			}
		}
		if a, b := runeDoc.Snapshot(), unitDoc.Snapshot(); string(a) != string(b) {
			t.Fatalf("the two documents diverged: %q against %q", runeDoc, unitDoc)
		}
	}
}

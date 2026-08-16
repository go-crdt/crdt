package crdt

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

// UTF-16 addressing cannot be checked against itself. A test that computes the
// expected offsets the same way the implementation does agrees with whatever
// the implementation believes, including its mistakes, and the mistakes this
// code exists to prevent are exactly the ones that look right.
//
// So the expected answers come from JavaScript, which is not a second opinion
// but the definition: a JavaScript string is a sequence of UTF-16 code units,
// `length` counts them, and `slice` cuts between them, and it is a browser
// handing us those offsets that this API is for. testdata/utf16-control.js
// records what node computes for a corpus of documents holding emoji, extended
// CJK, mathematical alphanumerics, combining marks, regional indicators and
// plain BMP text; regenerate it with
//
//	node testdata/utf16-control.js > testdata/utf16-control.json

type controlCorpus struct {
	Generator string         `json:"generator"`
	Docs      []controlEntry `json:"docs"`
}

type controlEntry struct {
	Name     string `json:"name"`
	Text     string `json:"text"`
	UTF16Len int    `json:"utf16Len"`
	RuneLen  int    `json:"runeLen"`
	Offsets  []struct {
		U16   int  `json:"u16"`
		Split bool `json:"split"`
		Rune  int  `json:"rune"`
	} `json:"offsets"`
	Inserts []struct {
		At        int    `json:"at"`
		Text      string `json:"text"`
		Want      string `json:"want"`
		WantLen16 int    `json:"wantLen16"`
	} `json:"inserts"`
	Deletes []struct {
		At        int    `json:"at"`
		Len       int    `json:"len"`
		Want      string `json:"want"`
		WantLen16 int    `json:"wantLen16"`
	} `json:"deletes"`
	Damaged []struct {
		At    int      `json:"at"`
		Units []uint16 `json:"units"`
	} `json:"damaged"`
}

func loadControl(t *testing.T) controlCorpus {
	t.Helper()
	raw, err := os.ReadFile("testdata/utf16-control.json")
	if err != nil {
		t.Fatalf("reading the control corpus: %v", err)
	}
	var corpus controlCorpus
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("decoding the control corpus: %v", err)
	}
	if len(corpus.Docs) == 0 {
		t.Fatal("the control corpus is empty")
	}
	return corpus
}

// filler is two characters — one in the Basic Multilingual Plane and one above
// it — inserted between the characters of a document and then deleted. A
// deleted supplementary character must count for nothing, and a block that
// holds one must still answer as though it did not.
const filler = "·\U0001F4A5"

// The same text, assembled four ways. The answers must not depend on how the
// document was typed, and each shape reaches a different part of the index: one
// block or one per character, deletions leading a block or trailing it.
var constructions = []struct {
	name string
	// edits says whether the insert and delete cases are replayed against this
	// shape as well as the read-only conversions. Two of the four is enough:
	// every edit case rebuilds the document, and the remaining two differ from
	// the first two in block shape rather than in what an edit does.
	edits bool
	build func(t *testing.T, text string) *Doc
}{
	{name: "one-run", edits: true, build: buildOneRun},
	{name: "one-block-per-character", build: buildPerCharacter},
	{name: "tombstones-leading", edits: true, build: func(t *testing.T, s string) *Doc {
		return buildTombstoned(t, s, true, false)
	}},
	{name: "tombstones-trailing-fragmented", build: func(t *testing.T, s string) *Doc {
		return buildTombstoned(t, s, false, true)
	}},
}

func buildOneRun(t *testing.T, text string) *Doc {
	t.Helper()
	d := New(1)
	if _, err := d.Insert(0, text); err != nil {
		t.Fatalf("Insert(0, %q): %v", text, err)
	}
	return d
}

// buildPerCharacter types the text backwards, one character at a time at
// position zero, so that no character continues the run before it and the
// document holds as many blocks as characters.
func buildPerCharacter(t *testing.T, text string) *Doc {
	t.Helper()
	d := New(1)
	runes := []rune(text)
	for i := len(runes) - 1; i >= 0; i-- {
		if _, err := d.Insert(0, string(runes[i])); err != nil {
			t.Fatalf("Insert(0, %q): %v", string(runes[i]), err)
		}
	}
	return d
}

// buildTombstoned puts filler around every character and then deletes it, so
// the text is the one asked for but every block is riddled with tombstones —
// leading each character or trailing it, and either in one run or in one block
// per character.
func buildTombstoned(t *testing.T, text string, leading, fragmented bool) *Doc {
	t.Helper()
	d := New(1)
	runes := []rune(text)
	group := func(r rune) string {
		if leading {
			return filler + string(r)
		}
		return string(r) + filler
	}
	if fragmented {
		for i := len(runes) - 1; i >= 0; i-- {
			if _, err := d.Insert(0, group(runes[i])); err != nil {
				t.Fatalf("Insert(0, %q): %v", group(runes[i]), err)
			}
		}
	} else {
		var b strings.Builder
		for _, r := range runes {
			b.WriteString(group(r))
		}
		if _, err := d.Insert(0, b.String()); err != nil {
			t.Fatalf("Insert(0, %q): %v", b.String(), err)
		}
	}
	// From the end, so that the positions of the groups not yet reached are the
	// ones just computed.
	for i := len(runes) - 1; i >= 0; i-- {
		at := 3 * i
		if !leading {
			at++
		}
		if _, err := d.Delete(at, 2); err != nil {
			t.Fatalf("Delete(%d, 2): %v", at, err)
		}
	}
	if got := d.String(); got != text {
		t.Fatalf("after removing the filler the document is %q, want %q", got, text)
	}
	return d
}

func TestUTF16AgainstJavaScript(t *testing.T) {
	corpus := loadControl(t)
	for _, entry := range corpus.Docs {
		t.Run(entry.Name, func(t *testing.T) {
			for _, c := range constructions {
				t.Run(c.name, func(t *testing.T) {
					d := c.build(t, entry.Text)
					checkIndex(t, d)
					checkControlOffsets(t, d, entry)
					if !c.edits {
						return
					}
					checkControlEdits(t, c.build, entry)
				})
			}
		})
	}
}

// checkControlOffsets compares every offset in the document, in both
// directions, against what JavaScript reports for the same string.
func checkControlOffsets(t *testing.T, d *Doc, entry controlEntry) {
	t.Helper()
	if got := d.String(); got != entry.Text {
		t.Fatalf("the document reads %q, want %q", got, entry.Text)
	}
	if got := d.LenUTF16(); got != entry.UTF16Len {
		t.Errorf("LenUTF16 = %d, JavaScript String.length = %d", got, entry.UTF16Len)
	}
	if got := d.Len(); got != entry.RuneLen {
		t.Errorf("Len = %d, JavaScript code points = %d", got, entry.RuneLen)
	}
	for _, o := range entry.Offsets {
		at, err := d.RuneOffset(o.U16)
		switch {
		case o.Split:
			if err != ErrSurrogateBoundary {
				t.Errorf("RuneOffset(%d) = %d, %v; that offset splits a character, want ErrSurrogateBoundary", o.U16, at, err)
			}
			continue
		case err != nil:
			t.Errorf("RuneOffset(%d): %v", o.U16, err)
			continue
		case at != o.Rune:
			t.Errorf("RuneOffset(%d) = %d, JavaScript reports code point %d", o.U16, at, o.Rune)
			continue
		}
		back, err := d.UTF16Offset(at)
		if err != nil {
			t.Errorf("UTF16Offset(%d): %v", at, err)
		} else if back != o.U16 {
			t.Errorf("UTF16Offset(%d) = %d, want %d", at, back, o.U16)
		}
	}
	checkControlDamage(t, d, entry)
}

// checkControlDamage asserts two things about the offsets that split a
// character: that we refuse to edit at them, and that the control instrument's
// own answer there is not a string at all. The second is the argument for the
// first — an offset JavaScript can only honour by producing a lone surrogate is
// not one that can be silently rounded into something the caller meant.
func checkControlDamage(t *testing.T, d *Doc, entry controlEntry) {
	t.Helper()
	for _, dm := range entry.Damaged {
		if _, err := d.InsertUTF16(dm.At, "X"); err != ErrSurrogateBoundary {
			t.Errorf("InsertUTF16(%d, %q) = %v, want ErrSurrogateBoundary", dm.At, "X", err)
		}
		if _, err := d.DeleteUTF16(dm.At, 1); err != ErrSurrogateBoundary {
			t.Errorf("DeleteUTF16(%d, 1) = %v, want ErrSurrogateBoundary", dm.At, err)
		}
		if got := string(utf16.Decode(dm.Units)); !strings.ContainsRune(got, utf8.RuneError) {
			t.Errorf("inserting at %d in JavaScript produced %q, expected a lone surrogate", dm.At, got)
		}
	}
}

// checkControlEdits replays every recorded edit against a document rebuilt for
// it, and compares the text and the length against what JavaScript's own splice
// produced.
func checkControlEdits(t *testing.T, build func(*testing.T, string) *Doc, entry controlEntry) {
	t.Helper()
	for _, ins := range entry.Inserts {
		d := build(t, entry.Text)
		if _, err := d.InsertUTF16(ins.At, ins.Text); err != nil {
			t.Errorf("InsertUTF16(%d, %q): %v", ins.At, ins.Text, err)
			continue
		}
		if got := d.String(); got != ins.Want {
			t.Errorf("InsertUTF16(%d, %q) gives %q, JavaScript gives %q", ins.At, ins.Text, got, ins.Want)
		}
		if got := d.LenUTF16(); got != ins.WantLen16 {
			t.Errorf("after InsertUTF16(%d, %q) LenUTF16 = %d, want %d", ins.At, ins.Text, got, ins.WantLen16)
		}
		checkIndex(t, d)
	}
	for _, del := range entry.Deletes {
		d := build(t, entry.Text)
		if _, err := d.DeleteUTF16(del.At, del.Len); err != nil {
			t.Errorf("DeleteUTF16(%d, %d): %v", del.At, del.Len, err)
			continue
		}
		if got := d.String(); got != del.Want {
			t.Errorf("DeleteUTF16(%d, %d) gives %q, JavaScript gives %q", del.At, del.Len, got, del.Want)
		}
		if got := d.LenUTF16(); got != del.WantLen16 {
			t.Errorf("after DeleteUTF16(%d, %d) LenUTF16 = %d, want %d", del.At, del.Len, got, del.WantLen16)
		}
		checkIndex(t, d)
	}
}

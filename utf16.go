package crdt

// Editors do not count in runes. CodeMirror, the DOM, the Language Server
// Protocol and every index into a JavaScript string count UTF-16 code units, in
// which a character outside the Basic Multilingual Plane — an emoji, an
// extended CJK ideograph, most mathematical alphanumerics — is two units rather
// than one. A browser handing its cursor offset straight to [Doc.Insert]
// therefore inserts in the wrong place as soon as the document holds a single
// emoji, and produces a document in which nothing afterwards can tell that it
// happened.
//
// The rune API is unchanged and remains the primary one. What is added here is
// the same three operations addressed in UTF-16 units, and the conversion both
// ways, so that a caller who counts in UTF-16 never converts by hand and never
// gets it subtly wrong.
//
// The cost is paid only by documents that hold supplementary characters. A
// document of ASCII, of French, of Greek, of BMP CJK — anything below U+10000 —
// has identical rune and UTF-16 offsets, and every function here then returns
// its argument in constant time without reading the document at all. A document
// that does hold them descends the index in tree.go, which carries a count of
// them per subtree beside the count of visible characters it already carried.
//
// Nothing else grows a second form. [Doc.Anchor], [Doc.Position] and
// [Doc.Author] speak of the document as it stands, so [Doc.RuneOffset] and
// [Doc.UTF16Offset] convert their offsets exactly. [Change] does not: its
// offsets are against the text as it stood after the changes before it, so
// converting one of them against the finished document is right only for the
// last. A caller applying changes to its own copy holds that intermediate text
// and has to convert there, against the copy it is patching.

import "errors"

// ErrSurrogateBoundary reports a UTF-16 offset that falls between the two code
// units of one character.
//
// Such an offset names a position that does not exist: half of an emoji is not
// a place a cursor can be, and no editor's user ever put it there. It is
// refused rather than rounded because rounding would move an edit somewhere the
// caller did not ask for and leave nothing behind to say so — the same
// reasoning that has [Doc.Insert] refuse invalid UTF-8 rather than substitute
// replacement characters.
//
// It is not a hypothetical. JavaScript will happily do the operation, and
// `"a😀b".slice(0, 2) + "x"` is a string containing a lone high surrogate: not
// text, not valid UTF-8, and not anything this package can hold. An offset that
// splits a character has already lost the information needed to honour it.
//
// A caller who must tolerate such an offset can round it down in one step,
// without a second API: an offset that splits a character is always exactly one
// past that character's first unit, so [Doc.RuneOffset] of pos-1 is the
// position of the character it landed inside.
var ErrSurrogateBoundary = errors.New("crdt: UTF-16 offset splits a surrogate pair")

// supUnit returns the extra UTF-16 code unit a character costs beyond the one
// every character costs: one above the Basic Multilingual Plane, zero within
// it.
//
// The test is exactly r > 0xFFFF and needs no other case, because every
// character a document can hold is a valid Unicode scalar value: [Op.validate]
// and the snapshot decoder both refuse the surrogate range outright, and
// [Doc.Insert] refuses text that is not valid UTF-8.
func supUnit(r rune) int32 {
	if r > 0xFFFF {
		return 1
	}
	return 0
}

// countSup counts the characters of text that take two UTF-16 code units.
func countSup(text []rune) int32 {
	var n int32
	for _, r := range text {
		n += supUnit(r)
	}
	return n
}

// LenUTF16 returns the length of the document in UTF-16 code units — the number
// JavaScript's String.prototype.length reports for [Doc.String].
//
// It is a counter, not a walk: the count of visible supplementary characters is
// maintained beside the count of visible characters, so this reads the document
// no more than [Doc.Len] does.
func (d *Doc) LenUTF16() int { return d.visible + d.sup }

// UTF16Offset converts a rune offset into the UTF-16 offset naming the same
// position. pos may equal [Doc.Len], which converts to [Doc.LenUTF16].
//
// This is the direction an editor needs to place someone else's cursor, or to
// report where an edit of its own landed.
func (d *Doc) UTF16Offset(pos int) (int, error) {
	if pos < 0 || pos > d.visible {
		return 0, ErrOutOfRange
	}
	if d.sup == 0 {
		return pos, nil // no character in the document is more than one unit
	}
	if pos == d.visible {
		return d.visible + d.sup, nil
	}
	return pos + d.supBefore(pos), nil
}

// RuneOffset converts a UTF-16 offset into the rune offset naming the same
// position. pos may equal [Doc.LenUTF16], which converts to [Doc.Len].
//
// An offset falling between the two code units of one character is refused with
// [ErrSurrogateBoundary]; see there for why, and for the one-line way to round
// it down instead.
func (d *Doc) RuneOffset(pos int) (int, error) {
	if pos < 0 || pos > d.visible+d.sup {
		return 0, ErrOutOfRange
	}
	if d.sup == 0 {
		return pos, nil
	}
	if pos == d.visible+d.sup {
		return d.visible, nil
	}
	at, split := d.runeAtUnit(pos)
	if split {
		return 0, ErrSurrogateBoundary
	}
	return at, nil
}

// InsertUTF16 is [Doc.Insert] with pos counted in UTF-16 code units rather than
// in runes. The text itself is a Go string, and so is measured in neither.
func (d *Doc) InsertUTF16(pos int, text string) ([]Op, error) {
	at, err := d.RuneOffset(pos)
	if err != nil {
		return nil, err
	}
	return d.Insert(at, text)
}

// DeleteUTF16 is [Doc.Delete] with pos and length counted in UTF-16 code units
// rather than in runes.
//
// Both ends of the range are converted, so length is a number of code units and
// the number of characters removed may be fewer — deleting the four units of
// two emoji removes two characters. A range whose either end splits a character
// is refused; see [ErrSurrogateBoundary].
func (d *Doc) DeleteUTF16(pos, length int) ([]Op, error) {
	if length < 0 {
		return nil, ErrOutOfRange
	}
	from, err := d.RuneOffset(pos)
	if err != nil {
		return nil, err
	}
	to, err := d.RuneOffset(pos + length)
	if err != nil {
		return nil, err
	}
	return d.Delete(from, to-from)
}

// supBefore returns how many of the document's first pos visible characters are
// supplementary. pos must be in range and below [Doc.Len].
//
// It descends the index exactly as seek does, taking the supplementary count of
// every subtree it steps over whole, so it costs the height of the tree rather
// than the length of the document.
func (d *Doc) supBefore(pos int) int {
	return d.index.supBefore(pos)
}

// runeAtUnit returns the rune offset of the character holding UTF-16 offset u,
// and whether u falls between that character's two units. u must be below
// [Doc.LenUTF16].
//
// The descent is by units rather than by characters — a subtree spans
// subVis+subSup of them — and both counts are carried down, because the answer
// is a character count and the question is a unit count.
func (d *Doc) runeAtUnit(u int) (int, bool) {
	return d.index.runeAtUnit(u)
}

// visibleSpans yields the stretches of a block still visible, in order. It is
// the shape [Doc.String] walks a block in, named here because counting UTF-16
// units walks it the same way and there is no reason for two of them.
func (b *block) visibleSpans(yield func([]rune) bool) {
	at := 0
	for _, r := range b.dels {
		if int(r.from) > at {
			if !yield(b.text[at:r.from]) {
				return
			}
		}
		at = int(r.to)
	}
	if at < len(b.text) {
		yield(b.text[at:])
	}
}

// visibleSup counts the visible characters of the block that are supplementary.
func (b *block) visibleSup() int32 {
	if b.nsup == 0 {
		return 0 // no character here is supplementary, deleted or not
	}
	var n int32
	for span := range b.visibleSpans {
		n += countSup(span)
	}
	return n
}

// supBefore counts the supplementary characters among the block's first k
// visible characters. k must not exceed how many the block has.
func (b *block) supBefore(k int) int32 {
	var n int32
	if b.nsup == 0 {
		return n
	}
	for span := range b.visibleSpans {
		if len(span) > k {
			span = span[:k]
		}
		n += countSup(span)
		k -= len(span)
		if k == 0 {
			break
		}
	}
	return n
}

// runeAtUnit returns the index, among the block's visible characters, of the
// character holding UTF-16 offset u, and whether u falls between that
// character's two units. u must not exceed the block's own visible units, and
// equalling them reports the position after its last character.
func (b *block) runeAtUnit(u int) (int, bool) {
	if b.nsup == 0 {
		return u, false // every character here is one unit, so the two counts agree
	}
	k := 0
	for span := range b.visibleSpans {
		for _, r := range span {
			if u == 0 {
				return k, false
			}
			if u -= int(1 + supUnit(r)); u < 0 {
				return k, true // u was the second unit of this character
			}
			k++
		}
	}
	return k, false
}

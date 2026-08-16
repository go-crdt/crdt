package crdt

import (
	"strings"
	"unicode/utf8"
)

// A view of the text — an editor, a preview, anything holding a copy — has to
// be told what changed, not what the text now is. Handed only the new text it
// would have to replace everything, and replacing everything throws away the
// selection, the scroll position, the folded regions and the decorations, on
// every keystroke anybody else makes.

// A Change is one contiguous edit to the visible text: remove Removed
// characters at Pos, then put Text there. Either part may be empty.
//
// Offsets are in runes, and each change is expressed against the text as it
// stands after the changes before it. Applying them in order to a copy of the
// text is what brings the copy up to date.
type Change struct {
	Pos     int
	Removed int
	Text    string
}

// Apply integrates operations from peers. Duplicates are ignored, and an
// operation that arrives before the operations it depends on is buffered until
// they do, so the caller needs no ordered delivery.
//
// A malformed operation is rejected and nothing in the batch is applied.
func (d *Doc) Apply(ops ...Op) error {
	_, err := d.applyWith(false, ops)
	return err
}

// ApplyChanges is [Doc.Apply], and also reports what the document did: the edits
// a view of the text has to make to catch up, in the order it has to make them.
//
// Only what actually happened is reported. An operation already applied, or one
// still waiting for the operations it depends on, changes nothing and says
// nothing; when it does land, the change is reported then.
//
// Finding where each edit landed costs a walk up the index per operation, which
// [Doc.Apply] does not pay. Use that one when nothing is watching.
func (d *Doc) ApplyChanges(ops ...Op) ([]Change, error) {
	return d.applyWith(true, ops)
}

func (d *Doc) applyWith(watching bool, ops []Op) ([]Change, error) {
	for _, op := range ops {
		if err := op.validate(); err != nil {
			return nil, err
		}
	}
	if watching {
		d.collect = &collector{}
		defer func() { d.collect = nil }()
	}
	for _, op := range ops {
		d.admit(op)
	}
	if !watching {
		return nil, nil
	}
	return d.collect.changes(), nil
}

// A collector gathers changes as the operations land.
//
// The text of a change is accumulated in a buffer rather than appended to the
// string, because a peer typing a paragraph produces one operation per
// character: growing a string each time would copy the whole paragraph per
// keystroke, which measured 57 times the cost of applying the operations at all.
type collector struct {
	out   []Change
	text  []byte // the characters accumulated for the change being built
	runes int    // how many runes that is
}

// seal writes the accumulated text into the change it belongs to.
func (c *collector) seal() {
	if c.text != nil {
		c.out[len(c.out)-1].Text = string(c.text)
		c.text, c.runes = nil, 0
	}
}

func (c *collector) changes() []Change {
	c.seal()
	return c.out
}

// insert notes a character having appeared at pos. A character landing where the
// last one left off extends that edit rather than starting one, because a view
// would rather be told about the word than the letters.
func (c *collector) insert(pos int, ch rune) {
	if n := len(c.out); n > 0 {
		if last := c.out[n-1]; c.text != nil && last.Pos+c.runes == pos {
			c.text = utf8.AppendRune(c.text, ch)
			c.runes++
			return
		}
		c.seal()
	}
	c.out = append(c.out, Change{Pos: pos})
	c.text = utf8.AppendRune(nil, ch)
	c.runes = 1
}

// remove notes the character at pos going. Deleting a stretch removes character
// after character at the same offset, the text closing up behind each one, so
// those are one edit too.
func (c *collector) remove(pos int) {
	if n := len(c.out); n > 0 {
		if last := &c.out[n-1]; c.text == nil && last.Text == "" && last.Pos == pos {
			last.Removed++
			return
		}
		c.seal()
	}
	c.out = append(c.out, Change{Pos: pos, Removed: 1})
}

// recordInsert notes a character having appeared, if anyone is listening.
func (d *Doc) recordInsert(b *block, i int, ch rune) {
	if d.collect != nil {
		d.collect.insert(d.visiblePos(b, i), ch)
	}
}

// recordDelete notes a character about to go, if anyone is listening. It must be
// called while the character is still visible, since that is what gives it an
// offset of its own.
func (d *Doc) recordDelete(b *block, i int) {
	if d.collect != nil {
		d.collect.remove(d.visiblePos(b, i))
	}
}

// ChangesFrom returns the edits that turn text into the document's current text,
// for a caller holding a copy it cannot otherwise reconcile — a view that has
// just been reconnected, say. It is a convenience over [Doc.String], not a
// cheaper path: it compares the two.
func ChangesFrom(text, want string) []Change {
	from, to := []rune(text), []rune(want)
	// Trim what is the same at each end; what is left is one edit, which is
	// exactly right for the case this exists for — a view catching up after
	// missing a stretch of a session.
	head := 0
	for head < len(from) && head < len(to) && from[head] == to[head] {
		head++
	}
	tail := 0
	for tail < len(from)-head && tail < len(to)-head &&
		from[len(from)-1-tail] == to[len(to)-1-tail] {
		tail++
	}
	if head == len(from) && head == len(to) {
		return nil
	}
	var b strings.Builder
	for _, r := range to[head : len(to)-tail] {
		b.WriteRune(r)
	}
	return []Change{{Pos: head, Removed: len(from) - head - tail, Text: b.String()}}
}

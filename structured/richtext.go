package structured

import (
	"encoding/binary"
	"sort"

	"github.com/go-crdt/crdt"
)

// A RichText is text that carries formatting: bold, italic, a colour, a link, a
// comment — anything that covers a stretch of characters rather than one of
// them.
//
// # Why the marks are not in the text
//
// The obvious way to write formatting into a sequence CRDT is to put it there:
// a bold-on character, a bold-off character, or a per-character attribute.
// Both lose.
//
// Markers in the sequence come apart. Two replicas that bold overlapping
// stretches produce interleaved on and off markers, and the text between them
// reads as bold on one replica and not on the other, because which marker won
// depends on where each landed rather than on what either person meant.
//
// A per-character attribute does converge, and costs a write per character: a
// person selecting a paragraph and pressing bold sends one operation for every
// letter of it, forever, and each of those operations is stored forever.
//
// # How this one works
//
// A mark is one operation naming two anchors, and the formatting of the
// document is worked out when it is read. That is the shape [Tree] and
// [Sequence] use for the same reason: the answer is a function of the state, so
// two replicas holding the same operations read the same formatting whatever
// order it arrived in.
//
// Where two marks of the same name disagree about a character — one bolding it,
// another taking bold away — the later of the two wins, by the (clock, site)
// order [crdt.Map] resolves its own writes by. Nothing is discarded: a mark that
// lost to a later one is still there, and a third mark can put it back.
//
// # What an anchor is, and why formatting grows the way it does
//
// An anchor is not an offset. An offset stored anywhere in a document people
// are editing means something else a moment later. An anchor names a character
// and a side of it: the boundary immediately before it, or immediately after
// it. Text typed into the gap that boundary sits in falls on one side or the
// other, and that is exactly the question "does what I am typing continue the
// bold".
//
// Which side is the caller's to choose, because the answer differs by what the
// mark is. Typing at the end of a bold word should continue it, so bold ends at
// the boundary before the next character and grows. Typing at the end of a link
// should not become part of the link, so a link ends at the boundary after its
// last character and does not. See [Expand].
type RichText struct {
	doc   *crdt.Composite
	text  *crdt.Doc
	marks *crdt.Map
}

// The two parts a rich text is made of. The names are constant and valid, so
// the errors [crdt.Composite] returns for an invalid name cannot happen and are
// discarded.
var (
	textPart  = crdt.Part{Kind: crdt.PartText, Name: "text"}
	marksPart = crdt.Part{Kind: crdt.PartMap, Name: "marks"}
)

// Expand says whether text typed at the edge of a mark joins it.
//
// It is the difference between bold, which continues as you type at the end of
// it, and a link, which does not.
type Expand uint8

const (
	// ExpandNone is a mark neither edge of which grows: typing at either end
	// stays outside it. A link, a comment, a footnote reference.
	ExpandNone Expand = 0
	// ExpandStart makes text typed immediately before the mark part of it.
	ExpandStart Expand = 1 << iota
	// ExpandEnd makes text typed immediately after the mark part of it, which
	// is what bold and italic do.
	ExpandEnd
	// ExpandBoth grows at either edge.
	ExpandBoth = ExpandStart | ExpandEnd
)

// A Span is a stretch of the text over which the formatting does not change.
type Span struct {
	// Pos is where the stretch starts, in visible characters.
	Pos int
	// Text is what it holds.
	Text string
	// Marks is the formatting covering all of it, by name. A mark with no value
	// of its own — bold — has a nil value; a mark that carries one — a colour,
	// the target of a link — has it here.
	Marks map[string][]byte
}

// NewRichText returns an empty rich text this site can edit.
func NewRichText(site crdt.SiteID) *RichText { return bindRich(crdt.NewComposite(site)) }

// RichTextOf reads a composite as a rich text, for a document that holds one
// among other parts.
func RichTextOf(doc *crdt.Composite) *RichText { return bindRich(doc) }

func bindRich(doc *crdt.Composite) *RichText {
	text, _ := doc.Text(textPart.Name)
	marks, _ := doc.Map(marksPart.Name)
	return &RichText{doc: doc, text: text, marks: marks}
}

// LoadRichText rebuilds one from a snapshot, to be edited as site.
func LoadRichText(site crdt.SiteID, snapshot []byte) (*RichText, error) {
	doc, err := crdt.LoadComposite(site, snapshot)
	if err != nil {
		return nil, err
	}
	return bindRich(doc), nil
}

// Composite returns the document underneath, which is what is snapshotted and
// what operations are applied to.
func (r *RichText) Composite() *crdt.Composite { return r.doc }

// Doc returns the text part, for anything this type does not wrap — anchors for
// a cursor, the authorship of a character.
func (r *RichText) Doc() *crdt.Doc { return r.text }

// Site returns the replica this text edits as.
func (r *RichText) Site() crdt.SiteID { return r.doc.Site() }

// Text returns the characters, without the formatting.
func (r *RichText) Text() string { return r.text.String() }

// Len returns how many visible characters there are.
func (r *RichText) Len() int { return r.text.Len() }

// Insert puts s at visible offset pos.
func (r *RichText) Insert(pos int, s string) (crdt.PartOps, error) {
	ops, err := r.text.Insert(pos, s)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: textPart, Text: ops}, nil
}

// Delete removes count characters from visible offset pos.
func (r *RichText) Delete(pos, count int) (crdt.PartOps, error) {
	ops, err := r.text.Delete(pos, count)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: textPart, Text: ops}, nil
}

// Mark puts a mark on the characters at [from, to), carrying value, which may
// be nil for a mark that is only on or off.
//
// expand says whether text typed at either edge joins it; see [Expand].
func (r *RichText) Mark(from, to int, name string, value []byte, expand Expand) (crdt.PartOps, error) {
	return r.write(markAdd, from, to, name, value, expand)
}

// Unmark takes a mark off the characters at [from, to).
//
// It is a mark of its own rather than the removal of one, because the mark it
// undoes may not have arrived yet, may cover more than this, and may be one of
// several. What it says is that these characters do not carry this name as of
// now, and a later mark can say otherwise.
func (r *RichText) Unmark(from, to int, name string) (crdt.PartOps, error) {
	return r.write(markRemove, from, to, name, nil, ExpandNone)
}

const (
	markAdd    = 1
	markRemove = 2
)

func (r *RichText) write(kind byte, from, to int, name string, value []byte, expand Expand) (crdt.PartOps, error) {
	if name == "" {
		return crdt.PartOps{}, crdt.ErrInvalidOp
	}
	if from < 0 || to > r.text.Len() || from >= to {
		return crdt.PartOps{}, crdt.ErrOutOfRange
	}
	start := r.anchorStart(from, expand&ExpandStart != 0)
	end := r.anchorEnd(to, expand&ExpandEnd != 0)
	id, mint, err := mintID(r.marks)
	if err != nil {
		return crdt.PartOps{}, err
	}
	set, err := r.marks.Set(encodeID(id), encodeMark(mark{
		kind: kind, expand: expand, name: name, value: value, start: start, end: end,
	}))
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: marksPart, Map: []crdt.MapOp{mint, set}}, nil
}

// anchorStart returns the boundary a mark starting at from is tied to. A mark
// that grows at the start hangs off the character before from, so text typed
// into the gap lands after the boundary and inside the mark; one that does not
// hangs off the character at from, so the same text lands before it and outside.
//
// The caller has already checked that 0 <= from < to <= Len, which is what
// makes every offset below one the text can answer for.
func (r *RichText) anchorStart(from int, grow bool) anchor {
	if !grow {
		id, _ := r.text.Anchor(from)
		return anchor{kind: beforeChar, id: id}
	}
	if from == 0 {
		return anchor{kind: atStart}
	}
	id, _ := r.text.Anchor(from - 1)
	return anchor{kind: afterChar, id: id}
}

// anchorEnd returns the boundary a mark ending at to is tied to, by the same
// reasoning the other way round.
func (r *RichText) anchorEnd(to int, grow bool) anchor {
	if grow {
		if to == r.text.Len() {
			return anchor{kind: atEnd}
		}
		id, _ := r.text.Anchor(to)
		return anchor{kind: beforeChar, id: id}
	}
	id, _ := r.text.Anchor(to - 1)
	return anchor{kind: afterChar, id: id}
}

// The four kinds of boundary a mark can be tied to.
const (
	atStart    = 0 // before every character there is or will be
	atEnd      = 1 // after every character there is or will be
	beforeChar = 2
	afterChar  = 3
)

type anchor struct {
	kind byte
	id   crdt.ID
}

type mark struct {
	kind   byte
	expand Expand
	name   string
	value  []byte
	start  anchor
	end    anchor
}

// at resolves a boundary to where it sits in the text now.
//
// A boundary tied to a character that has since been deleted sits where the
// text closed up, which is where the mark belongs: a comment on a deleted
// sentence belongs where the sentence was.
func (r *RichText) at(a anchor) (int, bool) {
	switch a.kind {
	case atStart:
		return 0, true
	case atEnd:
		return r.text.Len(), true
	}
	pos, ok := r.text.Position(a.id)
	if !ok {
		// A character this replica has never seen. The operations explaining it
		// have not arrived, so the mark is not read yet rather than read wrong.
		return 0, false
	}
	if a.kind == afterChar && r.text.Visible(a.id) {
		pos++
	}
	return pos, true
}

// Spans returns the text broken into stretches over which the formatting does
// not change, in order, covering every character exactly once.
func (r *RichText) Spans() []Span {
	text := []rune(r.text.String())
	if len(text) == 0 {
		return nil
	}
	live := r.resolved()

	// Every place the formatting can change, and nowhere else.
	cuts := map[int]bool{0: true, len(text): true}
	for _, m := range live {
		cuts[m.from] = true
		cuts[m.to] = true
	}
	at := make([]int, 0, len(cuts))
	for cut := range cuts {
		if cut >= 0 && cut <= len(text) {
			at = append(at, cut)
		}
	}
	sort.Ints(at)

	spans := make([]Span, 0, len(at))
	for i := 0; i+1 < len(at); i++ {
		from, to := at[i], at[i+1]
		spans = append(spans, Span{
			Pos:   from,
			Text:  string(text[from:to]),
			Marks: marksOver(live, from),
		})
	}
	return spans
}

// MarksAt returns the formatting of the character at pos.
func (r *RichText) MarksAt(pos int) map[string][]byte {
	if pos < 0 || pos >= r.text.Len() {
		return nil
	}
	return marksOver(r.resolved(), pos)
}

// a resolved mark: one record, placed in the text as it reads now, with the
// stamp that decides which of two disagreeing marks is the later.
type placed struct {
	mark
	from, to int
	clock    uint64
	site     crdt.SiteID
}

// resolved returns every mark this replica can place, in no particular order.
func (r *RichText) resolved() []placed {
	keys := r.marks.Keys()
	out := make([]placed, 0, len(keys))
	for _, key := range keys {
		if key == mintKey {
			continue // the counter identities are drawn from, not a mark
		}
		value, _ := r.marks.Get(key)
		m, ok := decodeMark(value)
		if !ok {
			// Bytes this version cannot read: a later one wrote them, or a peer
			// wrote them by hand. Skipped rather than guessed at.
			continue
		}
		from, ok1 := r.at(m.start)
		to, ok2 := r.at(m.end)
		if !ok1 || !ok2 || from >= to {
			// Not placeable yet, or placed nowhere: a mark whose text was
			// wholly deleted covers nothing.
			continue
		}
		// Keys returns the live keys, so every one of them has a stamp.
		clock, site, _ := r.marks.Stamp(key)
		out = append(out, placed{mark: m, from: from, to: to, clock: clock, site: site})
	}
	return out
}

// marksOver returns the formatting of one character: for each name, whichever
// mark carrying it covers the character and was written last.
func marksOver(live []placed, pos int) map[string][]byte {
	winner := map[string]placed{}
	for _, m := range live {
		if pos < m.from || pos >= m.to {
			continue
		}
		best, seen := winner[m.name]
		if !seen || laterMark(m, best) {
			winner[m.name] = m
		}
	}
	out := map[string][]byte{}
	for name, m := range winner {
		if m.kind == markAdd {
			out[name] = m.value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// laterMark is the order two marks of the same name are settled by: the same
// (clock, site) the map itself resolves two writes to one key by. Two live
// writes never share both, so nothing here is left to chance.
func laterMark(a, b placed) bool {
	if a.clock != b.clock {
		return a.clock > b.clock
	}
	return a.site > b.site
}

func encodeMark(m mark) []byte {
	out := []byte{m.kind, byte(m.expand)}
	out = binary.AppendUvarint(out, uint64(len(m.name)))
	out = append(out, m.name...)
	out = binary.AppendUvarint(out, uint64(len(m.value)))
	out = append(out, m.value...)
	out = appendAnchor(out, m.start)
	return appendAnchor(out, m.end)
}

func appendAnchor(out []byte, a anchor) []byte {
	out = append(out, a.kind)
	if a.kind == beforeChar || a.kind == afterChar {
		out = binary.AppendUvarint(out, uint64(a.id.Site))
		out = binary.AppendUvarint(out, a.id.Seq)
	}
	return out
}

func decodeMark(value []byte) (mark, bool) {
	var m mark
	if len(value) < 2 {
		return mark{}, false
	}
	m.kind, m.expand = value[0], Expand(value[1])
	if m.kind != markAdd && m.kind != markRemove {
		return mark{}, false
	}
	rest := value[2:]
	name, rest, ok := takeBytes(rest)
	if !ok || len(name) == 0 {
		return mark{}, false
	}
	m.name = string(name)
	val, rest, ok := takeBytes(rest)
	if !ok {
		return mark{}, false
	}
	if len(val) > 0 {
		m.value = val
	}
	m.start, rest, ok = takeAnchor(rest)
	if !ok {
		return mark{}, false
	}
	m.end, rest, ok = takeAnchor(rest)
	if !ok || len(rest) != 0 {
		return mark{}, false
	}
	return m, true
}

func takeBytes(in []byte) (out, rest []byte, ok bool) {
	n, used := binary.Uvarint(in)
	if used <= 0 || n > uint64(len(in)-used) {
		return nil, nil, false
	}
	return in[used : used+int(n)], in[used+int(n):], true
}

func takeAnchor(in []byte) (anchor, []byte, bool) {
	if len(in) == 0 {
		return anchor{}, nil, false
	}
	a := anchor{kind: in[0]}
	rest := in[1:]
	switch a.kind {
	case atStart, atEnd:
		return a, rest, true
	case beforeChar, afterChar:
		site, used := binary.Uvarint(rest)
		if used <= 0 {
			return anchor{}, nil, false
		}
		rest = rest[used:]
		seq, used := binary.Uvarint(rest)
		if used <= 0 || seq == 0 {
			return anchor{}, nil, false
		}
		a.id = crdt.ID{Site: crdt.SiteID(site), Seq: seq}
		return a, rest[used:], true
	}
	return anchor{}, nil, false
}

// Snapshot encodes the whole thing, text and formatting together.
func (r *RichText) Snapshot() []byte { return r.doc.Snapshot() }

// Version returns what this replica holds.
func (r *RichText) Version() crdt.CompositeVersion { return r.doc.Version() }

// OpsSince returns the operations a peer at v has not seen.
func (r *RichText) OpsSince(v crdt.CompositeVersion) ([]crdt.PartOps, error) {
	return r.doc.OpsSince(v)
}

// Apply integrates operations from peers.
func (r *RichText) Apply(batches ...crdt.PartOps) error { return r.doc.Apply(batches...) }

// Pending reports how many received operations are still waiting for the ones
// they depend on.
func (r *RichText) Pending() int { return r.doc.Pending() }

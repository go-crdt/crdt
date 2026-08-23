package structured

import (
	"encoding/binary"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/go-crdt/crdt"
)

// A Blocks is a document made of blocks: paragraphs, headings, list items,
// quotes, code, each holding text that carries formatting, each nested to a
// depth. It is what a page in a notebook, an outline, a wiki article and the
// body of a message all are.
//
// # Why not one rich text per block
//
// The obvious shape is a [RichText] per block, held in a [crdt.Composite] under
// a part apiece. It converges, and it does not scale, for a reason that has
// nothing to do with merging: a part cannot be taken out of a composite, and a
// version carries one entry per part. A thousand-block document is then a
// thousand parts whose version is exchanged on every sync, and the version of
// an empty document that once had a thousand blocks is the same size as the
// version of a full one. Measured, in this package's own tests: a thousand
// blocks written a part apiece is 18894 bytes of version vector, and written
// this way it is 35. It is the same argument [Ink] settles the same way — one
// stream of points rather than one part per stroke.
//
// # How this one works
//
// There is one text, holding every block's characters in reading order, and one
// map holding what each block is. A block begins at a marker character in the
// text, and the map records that marker's type, its depth and whatever else the
// caller puts on it, keyed by the marker's own identity. So a document of any
// number of blocks is three parts: the text, the marks over it, and the blocks.
//
// The marker is a real character rather than a boundary between two of them,
// and that is the whole reason this works. Two people can edit the same seam at
// the same moment and mean different things: one is finishing a paragraph, the
// other is starting the next. Both are the same offset. A boundary stored as
// "before the first character of this block" makes the first intention
// expressible and the second impossible; stored as "after the last character of
// the one before", the other way round. Neither can be both, because there is
// only one place in the sequence to insert at. A marker gives the seam two
// sides: text typed before it is the end of one block and text typed after it is
// the start of the next, and the sequence already knows how to keep two
// concurrent insertions at two different places apart. Nothing has to be
// arbitrated and nothing is lost.
//
// The marker is U+FFFF, a Unicode noncharacter — permanently reserved, never
// legal in interchange, so no text a person can type contains one. Text offered
// to this type is refused if it does one anyway ([ErrReservedRune]), and a
// marker that arrives from a peer with no record against it reads as an untyped
// block rather than as an error, because what a document reads as has to be a
// function of the state it is in.
//
// # Why nesting is a depth and not a parent
//
// A [Tree] is what nesting normally wants, and it is not what this wants. The
// order of the blocks is already decided, by the text, and a parent pointer is a
// second statement about the same arrangement — one that can contradict the
// first, so that a block reads before the block it hangs under. There is no
// answer to that contradiction which is not an arbitration.
//
// A depth cannot contradict anything. Every sequence of depths is a document
// somebody can read: a jump of two is a list that starts two levels in, which is
// what a person who indents twice meant. Two replicas indenting the same block
// at the same moment are two writes to one field, which [crdt.Map] settles the
// same way on both, and the loser can indent again.
type Blocks struct {
	doc  *crdt.Composite
	rich *RichText
	recs *RecordMap
}

// The part a document's blocks live in, beside the two a [RichText] uses. The
// name is constant and valid, so the error [crdt.Composite] returns for an
// invalid one cannot happen and is discarded.
var blocksPart = crdt.Part{Kind: crdt.PartMap, Name: "blocks"}

// BlockMark is the character a block begins at. It is U+FFFF, a Unicode
// noncharacter: permanently reserved and never legal in interchange, so it is
// not something a person types.
//
// It is exported so that anything reading the text part directly — an anchor, a
// cursor, an authorship pass — can tell a marker from a character somebody
// wrote, rather than having to know the number.
const BlockMark = '\uFFFF'

// The two fields every block has, beside whatever the caller puts on it. Both
// start with a NUL so they cannot collide with a caller's field name, which is
// the convention [Sequence] and [Tree] hold their own fields to.
const (
	blockTypeField  = "\x00type"
	blockDepthField = "\x00depth"
)

// ErrReservedRune reports text offered to a [Blocks] that contains
// [BlockMark]. The marker is what tells one block from the next, so a caller
// that could write one could write a block boundary into the middle of a word,
// and no reader could tell that apart from a block somebody meant.
var ErrReservedRune = errors.New("structured: text contains the block marker")

// A BlockID names a block for as long as the block exists, whatever concurrent
// edits do to the blocks around it. It is the identity of the marker character
// the block begins at, minted by the RGA, so it is unique across replicas,
// reload-safe, and never reused.
type BlockID crdt.ID

// String renders the identity in the "seq@site" notation the crdt package uses.
func (b BlockID) String() string { return crdt.ID(b).String() }

// key is the identity in the form the map is keyed by.
func (b BlockID) key() string { return encodeID(crdt.ID(b)) }

// DocStart is the place before the first block: passing it to [Blocks.Insert]
// puts a block at the top of the document.
var DocStart = BlockID{}

// IsStart reports whether b names the place before the first block rather than
// a block.
func (b BlockID) IsStart() bool { return crdt.ID(b).IsRoot() }

// A Block is one block as it reads now.
type Block struct {
	// ID names the block.
	ID BlockID
	// Type is what the block is — "paragraph", "heading", "quote", whatever
	// the caller uses. It is free text rather than an enumeration, because
	// this package holds no renderer and has no business deciding what a
	// document can contain. The empty string is a block nobody has typed.
	Type string
	// Depth is how deeply the block is nested; zero is the top level.
	Depth int
	// Text is the block's characters, without the marker and without the
	// formatting. See [Blocks.Spans] for the formatting.
	Text string
}

// NewBlocks returns an empty document this site can edit.
func NewBlocks(site crdt.SiteID) *Blocks { return bindBlocks(crdt.NewComposite(site)) }

// BlocksOf reads a composite as a block document, for a document that holds
// these parts among others.
func BlocksOf(doc *crdt.Composite) *Blocks { return bindBlocks(doc) }

func bindBlocks(doc *crdt.Composite) *Blocks {
	m, _ := doc.Map(blocksPart.Name)
	return &Blocks{doc: doc, rich: RichTextOf(doc), recs: RecordsOf(m)}
}

// LoadBlocks rebuilds a document from a snapshot, to be edited as site.
func LoadBlocks(site crdt.SiteID, snapshot []byte) (*Blocks, error) {
	doc, err := crdt.LoadComposite(site, snapshot)
	if err != nil {
		return nil, err
	}
	return bindBlocks(doc), nil
}

// Composite returns the document underneath, which is what is snapshotted and
// what operations are applied to.
func (b *Blocks) Composite() *crdt.Composite { return b.doc }

// RichText returns the text and its formatting as one rich text, for anything
// this type does not wrap. Its offsets are over the whole document, markers
// included; see [Blocks.At] to convert one.
func (b *Blocks) RichText() *RichText { return b.rich }

// Records returns the record map the blocks live in, for reading a block's own
// fields.
func (b *Blocks) Records() *RecordMap { return b.recs }

// Site returns the replica this document edits as.
func (b *Blocks) Site() crdt.SiteID { return b.doc.Site() }

// text is the text part, which every position below is an offset into.
func (b *Blocks) text() *crdt.Doc { return b.rich.Doc() }

// A marker is one block's opening character, as the text reads now.
type marker struct {
	id  crdt.ID
	pos int // the marker's own offset; the block's text starts at pos+1
	end int // one past the block's last character
}

// markers returns every block's marker, in reading order.
//
// It is one pass over the text plus one index descent per marker, which is what
// makes reading a document proportional to the document rather than to the
// number of operations that built it.
func (b *Blocks) markers() []marker {
	text := b.text()
	runes := []rune(text.String())
	var out []marker
	for i, r := range runes {
		if r != BlockMark {
			continue
		}
		if n := len(out); n > 0 {
			out[n-1].end = i
		}
		id, _ := text.Anchor(i) // i < len(runes), so this cannot fail
		out = append(out, marker{id: id, pos: i, end: len(runes)})
	}
	return out
}

// find returns the marker of one block and where it sits among the others.
func (b *Blocks) find(id BlockID) (marker, int, bool) {
	for i, m := range b.markers() {
		if m.id == crdt.ID(id) {
			return m, i, true
		}
	}
	return marker{}, 0, false
}

// Len returns how many blocks there are. It is the document's length in blocks;
// its length in characters is [RichText.Len] of [Blocks.RichText], which counts
// the markers too.
func (b *Blocks) Len() int { return len(b.markers()) }

// List returns every block, in reading order.
func (b *Blocks) List() []Block {
	ms := b.markers()
	if len(ms) == 0 {
		return nil
	}
	runes := []rune(b.text().String())
	out := make([]Block, 0, len(ms))
	for _, m := range ms {
		out = append(out, b.readBlock(m, runes))
	}
	return out
}

// Block returns one block as it reads now.
func (b *Blocks) Block(id BlockID) (Block, bool) {
	m, _, ok := b.find(id)
	if !ok {
		return Block{}, false
	}
	return b.readBlock(m, []rune(b.text().String())), true
}

// Text returns one block's characters, without the formatting.
func (b *Blocks) Text(id BlockID) (string, bool) {
	m, _, ok := b.find(id)
	if !ok {
		return "", false
	}
	return string([]rune(b.text().String())[m.pos+1 : m.end]), true
}

// Plain returns the whole document as text, with the markers taken out and the
// blocks separated by sep — "\n" for something a person reads.
//
// It is a rendering, not the state: the state is the blocks.
func (b *Blocks) Plain(sep string) string {
	parts := make([]string, 0, 8)
	for _, blk := range b.List() {
		parts = append(parts, blk.Text)
	}
	return strings.Join(parts, sep)
}

// depthOf reads how deeply a block is nested, which is what a new block beside
// it inherits.
func (b *Blocks) depthOf(id BlockID) int {
	raw, ok := b.recs.GetField(id.key(), blockDepthField)
	if !ok {
		return 0
	}
	n, used := binary.Uvarint(raw)
	if used <= 0 {
		return 0
	}
	return int(n)
}

func (b *Blocks) readBlock(m marker, runes []rune) Block {
	key := BlockID(m.id).key()
	typ, _ := b.recs.GetField(key, blockTypeField)
	return Block{
		ID:    BlockID(m.id),
		Type:  string(typ),
		Depth: b.depthOf(BlockID(m.id)),
		Text:  string(runes[m.pos+1 : m.end]),
	}
}

// At returns where a place inside a block sits in the underlying text, which is
// what [Blocks.RichText] and [crdt.Doc.Anchor] are in terms of.
//
// offset may equal the block's length, which is the place after its last
// character.
func (b *Blocks) At(id BlockID, offset int) (int, error) {
	m, _, ok := b.find(id)
	if !ok {
		return 0, crdt.ErrInvalidOp
	}
	start := m.pos + 1
	if offset < 0 || start+offset > m.end {
		return 0, crdt.ErrOutOfRange
	}
	return start + offset, nil
}

// Insert puts a new empty block of type typ after the block named by after, or
// at the top of the document for [DocStart]. The new block is nested as deeply
// as the one it follows, which is what pressing return at the end of a list
// item means.
func (b *Blocks) Insert(after BlockID, typ string) (BlockID, []crdt.PartOps, error) {
	depth := 0
	pos := 0
	if !after.IsStart() {
		m, _, ok := b.find(after)
		if !ok {
			return BlockID{}, nil, crdt.ErrInvalidOp
		}
		pos = m.end
		depth = b.depthOf(BlockID(m.id))
	}
	return b.open(pos, typ, depth)
}

// Split cuts a block in two at offset, and returns the new block, which holds
// everything from offset on and follows the one split. It is what pressing
// return in the middle of a paragraph does.
//
// The new block is of type typ and is nested as deeply as the one it came from.
// Splitting at the end of a block leaves an empty one after it, and splitting at
// the start leaves an empty one before it; both are what a person pressing
// return at those places means.
func (b *Blocks) Split(id BlockID, offset int, typ string) (BlockID, []crdt.PartOps, error) {
	pos, err := b.At(id, offset)
	if err != nil {
		return BlockID{}, nil, err
	}
	return b.open(pos, typ, b.depthOf(id))
}

// open writes a marker at pos and the record that says what it is.
func (b *Blocks) open(pos int, typ string, depth int) (BlockID, []crdt.PartOps, error) {
	textOps, err := b.text().Insert(pos, string(BlockMark))
	if err != nil {
		return BlockID{}, nil, err
	}
	// The character now at pos is the marker just written, so this is its
	// identity; the insertion succeeded, so the offset is one the text can
	// answer for.
	id, _ := b.text().Anchor(pos)
	block := BlockID(id)

	out := []crdt.PartOps{{Part: textPart, Text: textOps}}
	var mapOps []crdt.MapOp
	if typ != "" {
		op, err := b.recs.SetField(block.key(), blockTypeField, []byte(typ))
		if err != nil {
			// The marker is written and the block exists, untyped. The record
			// cannot be written first — there is no identity to key it by until
			// the marker exists — so this is the one order there is, and what
			// it leaves is a block nobody has typed rather than no block at
			// all. [Blocks.SetType] puts it right.
			return BlockID{}, nil, err
		}
		mapOps = append(mapOps, op)
	}
	if depth > 0 {
		op, err := b.recs.SetField(block.key(), blockDepthField, binary.AppendUvarint(nil, uint64(depth)))
		if err != nil {
			return BlockID{}, nil, err
		}
		mapOps = append(mapOps, op)
	}
	if len(mapOps) > 0 {
		out = append(out, crdt.PartOps{Part: blocksPart, Map: mapOps})
	}
	return block, out, nil
}

// Merge takes the boundary between a block and the one before it away, so that
// its text joins the block above. It is what pressing backspace at the start of
// a block does.
//
// The first block of a document has nothing above it to join, and merging it is
// an error rather than a silent no-op: a caller that asks has miscounted.
func (b *Blocks) Merge(id BlockID) ([]crdt.PartOps, error) {
	m, i, ok := b.find(id)
	if !ok {
		return nil, crdt.ErrInvalidOp
	}
	if i == 0 {
		return nil, crdt.ErrOutOfRange
	}
	return b.close(m, m.pos, 1)
}

// Remove takes a block and everything in it out of the document.
func (b *Blocks) Remove(id BlockID) ([]crdt.PartOps, error) {
	m, _, ok := b.find(id)
	if !ok {
		return nil, crdt.ErrInvalidOp
	}
	return b.close(m, m.pos, m.end-m.pos)
}

// close forgets what the block was and deletes count characters from pos.
//
// The record goes first, and the order is the point. Neither write can be taken
// back, so whichever fails second leaves the other done: the record first means
// a failure leaves a block that has lost its type, which is a document somebody
// can read and go on editing, and a failure of the record itself leaves the
// document exactly as it was. The other order leaves text deleted with the
// caller holding nothing to send for it. [Blocks.open] cannot do the same,
// because a marker has to exist before there is an identity to key its record
// by, and says so.
//
// The record is one write per field, which is what a record map's deletion
// costs. It is not what makes the block gone — the marker is — so a concurrent
// write to a field of it changes nothing that is read.
func (b *Blocks) close(m marker, pos, count int) ([]crdt.PartOps, error) {
	mapOps, err := b.recs.DeleteRecord(BlockID(m.id).key())
	if err != nil {
		return nil, err
	}
	textOps, err := b.text().Delete(pos, count)
	if err != nil {
		return nil, err
	}
	out := []crdt.PartOps{{Part: textPart, Text: textOps}}
	if len(mapOps) > 0 {
		out = append(out, crdt.PartOps{Part: blocksPart, Map: mapOps})
	}
	return out, nil
}

// InsertText puts s into a block at offset, which may equal the block's length.
func (b *Blocks) InsertText(id BlockID, offset int, s string) (crdt.PartOps, error) {
	if strings.ContainsRune(s, BlockMark) {
		return crdt.PartOps{}, ErrReservedRune
	}
	pos, err := b.At(id, offset)
	if err != nil {
		return crdt.PartOps{}, err
	}
	ops, err := b.text().Insert(pos, s)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: textPart, Text: ops}, nil
}

// DeleteText takes count characters out of a block at offset.
//
// It cannot reach past the end of the block, so a deletion can never take a
// marker out by accident and turn two blocks into one; that is [Blocks.Merge],
// which is a different thing to ask for.
func (b *Blocks) DeleteText(id BlockID, offset, count int) (crdt.PartOps, error) {
	m, _, ok := b.find(id)
	if !ok {
		return crdt.PartOps{}, crdt.ErrInvalidOp
	}
	start := m.pos + 1
	if offset < 0 || count < 0 || start+offset+count > m.end {
		return crdt.PartOps{}, crdt.ErrOutOfRange
	}
	ops, err := b.text().Delete(start+offset, count)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: textPart, Text: ops}, nil
}

// SetType says what a block is. The empty string takes the type off.
func (b *Blocks) SetType(id BlockID, typ string) (crdt.PartOps, error) {
	if _, _, ok := b.find(id); !ok {
		return crdt.PartOps{}, crdt.ErrInvalidOp
	}
	if typ == "" {
		op, err := b.recs.DeleteField(id.key(), blockTypeField)
		if err != nil {
			return crdt.PartOps{}, err
		}
		return crdt.PartOps{Part: blocksPart, Map: []crdt.MapOp{op}}, nil
	}
	op, err := b.recs.SetField(id.key(), blockTypeField, []byte(typ))
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: blocksPart, Map: []crdt.MapOp{op}}, nil
}

// SetDepth says how deeply a block is nested. Zero is the top level.
func (b *Blocks) SetDepth(id BlockID, depth int) (crdt.PartOps, error) {
	if depth < 0 {
		return crdt.PartOps{}, crdt.ErrOutOfRange
	}
	if _, _, ok := b.find(id); !ok {
		return crdt.PartOps{}, crdt.ErrInvalidOp
	}
	op, err := b.recs.SetField(id.key(), blockDepthField, binary.AppendUvarint(nil, uint64(depth)))
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: blocksPart, Map: []crdt.MapOp{op}}, nil
}

// SetField puts one of the caller's own fields on a block: the level of a
// heading, the language of a code block, whether a task is done.
//
// A field name may be anything valid UTF-8 that does not start with a NUL,
// which is what this type keeps its own two under.
func (b *Blocks) SetField(id BlockID, field string, value []byte) (crdt.PartOps, error) {
	if !validField(field) {
		return crdt.PartOps{}, crdt.ErrInvalidOp
	}
	if _, _, ok := b.find(id); !ok {
		return crdt.PartOps{}, crdt.ErrInvalidOp
	}
	op, err := b.recs.SetField(id.key(), field, value)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: blocksPart, Map: []crdt.MapOp{op}}, nil
}

// Field reads one of the caller's own fields.
func (b *Blocks) Field(id BlockID, field string) ([]byte, bool) {
	if !validField(field) {
		return nil, false
	}
	return b.recs.GetField(id.key(), field)
}

// validField refuses the empty name, a name that is not text, and the reserved
// prefix this type keeps its own fields under.
func validField(field string) bool {
	return field != "" && field[0] != 0 && utf8.ValidString(field)
}

// Mark puts a mark on the text from one place in the document to another,
// carrying value, which may be nil for a mark that is only on or off.
//
// A mark may span blocks — a comment on two paragraphs, a sentence someone
// started emphasising and finished in the next one — because the text it runs
// over is one text. expand says whether text typed at either edge joins it; see
// [Expand].
func (b *Blocks) Mark(from BlockID, fromOff int, to BlockID, toOff int, name string, value []byte, expand Expand) (crdt.PartOps, error) {
	f, t, err := b.span(from, fromOff, to, toOff)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return b.rich.Mark(f, t, name, value, expand)
}

// Unmark takes a mark off the text between two places, on the terms
// [RichText.Unmark] describes: it is a mark of its own rather than the removal
// of one.
func (b *Blocks) Unmark(from BlockID, fromOff int, to BlockID, toOff int, name string) (crdt.PartOps, error) {
	f, t, err := b.span(from, fromOff, to, toOff)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return b.rich.Unmark(f, t, name)
}

func (b *Blocks) span(from BlockID, fromOff int, to BlockID, toOff int) (int, int, error) {
	f, err := b.At(from, fromOff)
	if err != nil {
		return 0, 0, err
	}
	t, err := b.At(to, toOff)
	if err != nil {
		return 0, 0, err
	}
	return f, t, nil
}

// Spans returns one block's text broken into stretches over which the
// formatting does not change, in order, covering every character of it exactly
// once. Offsets are within the block.
//
// A block with no characters has no spans, which is the same answer
// [RichText.Spans] gives an empty text.
func (b *Blocks) Spans(id BlockID) []Span {
	m, _, ok := b.find(id)
	if !ok {
		return nil
	}
	start, end := m.pos+1, m.end
	if start >= end {
		return nil
	}
	var out []Span
	for _, s := range b.rich.Spans() {
		runes := []rune(s.Text)
		lo, hi := max(s.Pos, start), min(s.Pos+len(runes), end)
		if lo >= hi {
			continue
		}
		out = append(out, Span{
			Pos:   lo - start,
			Text:  string(runes[lo-s.Pos : hi-s.Pos]),
			Marks: s.Marks,
		})
	}
	// The marker between two blocks can carry marks of its own, so two spans of
	// one block can arrive with nothing between them that differs. Joining them
	// keeps the result a function of the text rather than of where the markers
	// happen to be.
	return joinSpans(out)
}

// MarksAt returns the formatting of one character of a block.
func (b *Blocks) MarksAt(id BlockID, offset int) map[string][]byte {
	m, _, ok := b.find(id)
	if !ok {
		return nil
	}
	if offset < 0 || m.pos+1+offset >= m.end {
		return nil
	}
	return b.rich.MarksAt(m.pos + 1 + offset)
}

// joinSpans merges neighbouring spans that carry the same formatting.
func joinSpans(in []Span) []Span {
	out := in[:0]
	for _, s := range in {
		if n := len(out); n > 0 && sameMarks(out[n-1].Marks, s.Marks) {
			out[n-1].Text += s.Text
			continue
		}
		out = append(out, s)
	}
	return out
}

func sameMarks(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for name, av := range a {
		bv, ok := b[name]
		if !ok || string(av) != string(bv) {
			return false
		}
	}
	return true
}

// Outline returns the blocks with their nesting made explicit: each block
// paired with the block it hangs under, which is the nearest one before it at a
// smaller depth.
//
// It is derived, not stored — see the type's own documentation for why nesting
// is a depth — so two replicas holding the same document read the same outline.
// A block at the top level has [DocStart] as its parent.
func (b *Blocks) Outline() []Outlined {
	blocks := b.List()
	out := make([]Outlined, 0, len(blocks))
	// The most recent block seen at each depth, which is what the next deeper
	// block hangs under.
	var stack []BlockID
	for _, blk := range blocks {
		if blk.Depth < len(stack) {
			stack = stack[:blk.Depth]
		}
		parent := DocStart
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		out = append(out, Outlined{Block: blk, Parent: parent})
		for len(stack) < blk.Depth {
			// A jump of more than one level: the intervening levels have no
			// block of their own, so the one that opened them stands in.
			stack = append(stack, blk.ID)
		}
		stack = append(stack, blk.ID)
	}
	return out
}

// An Outlined is a block together with the block it hangs under.
type Outlined struct {
	Block
	Parent BlockID
}

// Children returns the blocks that hang directly under one block, in reading
// order. [DocStart] returns the blocks at the top level.
func (b *Blocks) Children(parent BlockID) []BlockID {
	var out []BlockID
	for _, o := range b.Outline() {
		if o.Parent == parent {
			out = append(out, o.ID)
		}
	}
	return out
}

// IDs returns the identity of every block, in reading order. It is what
// [Blocks.List] returns without reading any text, for a caller that only wants
// to know what is there.
func (b *Blocks) IDs() []BlockID {
	ms := b.markers()
	out := make([]BlockID, 0, len(ms))
	for _, m := range ms {
		out = append(out, BlockID(m.id))
	}
	return out
}

// Snapshot returns the document as bytes.
func (b *Blocks) Snapshot() []byte { return b.doc.Snapshot() }

// Version returns what this replica has, one entry per part — three, whatever
// the document holds.
func (b *Blocks) Version() crdt.CompositeVersion { return b.doc.Version() }

// OpsSince returns the operations a peer at v has not seen.
func (b *Blocks) OpsSince(v crdt.CompositeVersion) []crdt.PartOps { return b.doc.OpsSince(v) }

// Apply takes operations from a peer.
func (b *Blocks) Apply(batches ...crdt.PartOps) error { return b.doc.Apply(batches...) }

// Pending reports how many operations are held back waiting for ones they
// depend on.
func (b *Blocks) Pending() int { return b.doc.Pending() }

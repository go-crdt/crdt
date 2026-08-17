package crdt

import (
	"errors"
	"sort"
	"strings"
	"unicode/utf8"
)

// ErrOutOfRange reports a position or length outside the document.
var ErrOutOfRange = errors.New("crdt: position out of range")

// ErrInvalidText reports text that is not valid UTF-8. The package refuses it
// rather than substituting replacement characters, which would silently corrupt
// a document that no later edit could repair.
var ErrInvalidText = errors.New("crdt: invalid UTF-8")

// A block is a run of characters one site inserted consecutively, each after the
// one before it. Everything the algorithm needs about character k follows from
// the first character and k, so a thousand characters typed in a row cost one
// block header and their text rather than a thousand headers:
//
//	identity   {id.Site, id.Seq + k}
//	clock      clock + k
//	origin     character k-1, or originID for k == 0
//
// Blocks are never unlinked — a concurrent insertion may still name a deleted
// character as its origin — and they are only ever split, by an insertion
// landing inside one, or extended, by the next character of the same run.
type block struct {
	id       ID     // identity of the first character
	clock    uint64 // Lamport clock of the first character
	originID ID     // what the first character was inserted after
	text     []rune
	// dels records the stretches of this run that have been deleted, ascending
	// and never overlapping. One record covers a whole stretch, so a word
	// backspaced away costs one entry rather than one per letter; keeping an
	// entry per character instead was measured, on a real editing history, as
	// more than half the memory the document used.
	dels []delRange
	next *block
	// prev makes the walk in visibleAt run both ways. Editing is local: the next
	// position asked for is usually just before or just after the last one, and
	// a list that can only be walked forwards turns "just before" into a walk
	// from the beginning of the document.
	prev *block
	// A block is also a node of the index in tree.go, which puts the same blocks
	// in the same order and summarises what a walk would otherwise have to
	// count: subVis is the visible characters this subtree holds, subMin the
	// block of it whose first character sorts lowest. The count is an int32
	// because two billion visible characters is eight gigabytes of text before
	// anything else, and the field would otherwise cost every block eight bytes.
	left, right, up *block
	subMin          *block
	subVis          int32
	// subSup is the same summary for supplementary characters — those written as
	// two UTF-16 code units rather than one — which is what turns a UTF-16 offset
	// into a descent instead of a walk. See utf16.go.
	subSup int32
	// nsup counts the supplementary characters in text, deleted ones included, so
	// that a block holding none — every block of a document with no emoji in it —
	// answers any question about UTF-16 units without looking at its characters.
	nsup   int32
	height uint8
}

// A delRange is one stretch of a block that was deleted in one go: characters
// [from, to) were removed by one site's operations id.Seq, id.Seq+1, … in that
// order, so the operation that removed character from+k is id.Seq+k.
//
// The contiguity is not an assumption about the world; it is enforced. A
// deletion only joins a range when its identity continues that range's
// sequence, and starts a new one otherwise.
type delRange struct {
	from, to uint32
	id       ID
}

func (r delRange) holds(i int) bool { return uint32(i) >= r.from && uint32(i) < r.to }

func (b *block) idAt(i int) ID        { return ID{Site: b.id.Site, Seq: b.id.Seq + uint64(i)} }
func (b *block) clockAt(i int) uint64 { return b.clock + uint64(i) }

// deadIndex returns the index of the range covering offset i, or -1 when the
// character is still visible. Ranges ascend, so the search is a bisection; most
// blocks hold none or one.
func (b *block) deadIndex(i int) int {
	lo, hi := 0, len(b.dels)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if b.dels[mid].to <= uint32(i) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(b.dels) && b.dels[lo].holds(i) {
		return lo
	}
	return -1
}

func (b *block) aliveAt(i int) bool { return b.deadIndex(i) < 0 }

// delIDAt returns the operation that deleted character i, or the zero ID if it
// is still visible.
func (b *block) delIDAt(i int) ID {
	n := b.deadIndex(i)
	if n < 0 {
		return ID{}
	}
	r := b.dels[n]
	return ID{Site: r.id.Site, Seq: r.id.Seq + uint64(uint32(i)-r.from)}
}

// visibleFrom counts the visible characters at offset i or later.
func (b *block) visibleFrom(i int) int {
	n := len(b.text) - i
	for _, r := range b.dels {
		if int(r.to) <= i {
			continue
		}
		from := max(int(r.from), i)
		n -= int(r.to) - from
	}
	return n
}

// visibleUpto counts the visible characters at offset i or earlier.
func (b *block) visibleUpto(i int) int {
	n := i + 1
	for _, r := range b.dels {
		if int(r.from) > i {
			break
		}
		n -= min(int(r.to), i+1) - int(r.from)
	}
	return n
}

// visibleOffset returns the offset of the k'th visible character at offset i or
// later, counting from zero.
func (b *block) visibleOffset(i, k int) int {
	for _, r := range b.dels {
		if int(r.to) <= i {
			continue
		}
		if gap := int(r.from) - i; k < gap {
			return i + k
		} else if gap > 0 {
			k -= gap
		}
		i = int(r.to)
	}
	return i + k
}

// markDeleted records that character i was removed by the operation id. The
// character must still be visible.
//
// A deletion extends the record ending where it lands, when its identity
// continues that record's sequence — which is what lets one record stand for a
// whole stretch. Backspacing over a word produces one record; scattered
// corrections produce one each.
//
// Nothing else can ever be joined, and that is a consequence rather than a
// simplification: a site's operations are applied in sequence order, so a record
// whose identities *follow* this deletion's cannot already exist. There is no
// case where a deletion fills a gap between two records, or leads into one.
func (b *block) markDeleted(i int, id ID) {
	at := uint32(i)
	n := 0
	for n < len(b.dels) && b.dels[n].to <= at {
		n++
	}
	if n > 0 {
		if prev := &b.dels[n-1]; prev.to == at && prev.continuesTo(at) == id {
			prev.to = at + 1
			return
		}
	}
	b.dels = append(b.dels, delRange{})
	copy(b.dels[n+1:], b.dels[n:])
	b.dels[n] = delRange{from: at, to: at + 1, id: id}
}

// continuesTo returns the identity this record's sequence would give to the
// character at offset i.
func (r delRange) continuesTo(i uint32) ID {
	return ID{Site: r.id.Site, Seq: r.id.Seq + uint64(i-r.from)}
}

// isolate makes character i a record of its own, cutting whatever record holds
// it, and returns that record's index. It is what lets one character's deletion
// be replaced when two replicas delete it at once.
func (b *block) isolate(i int) int {
	n := b.deadIndex(i)
	r := b.dels[n]
	at := uint32(i)
	if r.from == at && r.to == at+1 {
		return n
	}
	cut := make([]delRange, 0, 3)
	if r.from < at {
		cut = append(cut, delRange{from: r.from, to: at, id: r.id})
	}
	cut = append(cut, delRange{from: at, to: at + 1, id: r.continuesTo(at)})
	if r.to > at+1 {
		cut = append(cut, delRange{from: at + 1, to: r.to, id: r.continuesTo(at + 1)})
	}
	rest := append([]delRange(nil), b.dels[n+1:]...)
	b.dels = append(append(b.dels[:n:n], cut...), rest...)
	if r.from < at {
		return n + 1
	}
	return n
}

// A delCursor walks a block's deletion records in step with its characters, so
// that reading every character's deletion costs one pass rather than a search
// each time.
type delCursor struct {
	b *block
	n int
}

// at returns the operation that deleted character i, or the zero ID.
func (c *delCursor) at(i int) ID {
	for c.n < len(c.b.dels) && int(c.b.dels[c.n].to) <= i {
		c.n++
	}
	if c.n < len(c.b.dels) && c.b.dels[c.n].holds(i) {
		return c.b.dels[c.n].continuesTo(uint32(i))
	}
	return ID{}
}

// originAt returns what character i was inserted after.
func (b *block) originAt(i int) ID {
	if i == 0 {
		return b.originID
	}
	return b.idAt(i - 1)
}

// A Doc is one replica of a text document. It is not safe for concurrent use;
// serialize access from the outside, as an editor naturally does.
//
// The zero Doc is unusable — construct one with [New] or [Load].
type Doc struct {
	site  SiteID
	clock uint64 // Lamport clock: at least as high as any clock seen

	head *block // sentinel for the root ID; head.next is the first block
	vv   VersionVector

	// tree is the root of the index over the same blocks; see tree.go. dirty
	// holds the one block whose visible-character count the tree has not been
	// told about yet, which is what keeps typing from paying for the index on
	// every keystroke.
	tree     *block
	dirty    *block
	dirtyVis int32
	dirtySup int32

	// bySite indexes each site's blocks by the sequence numbers they cover,
	// ascending. Finding the character an operation names is a binary search
	// over one site's blocks rather than a map lookup per character, which
	// keeps the index proportional to runs typed rather than to characters.
	//
	// A deletion consumes a sequence number and creates no character, so a
	// site's covered ranges have holes; the search reports them as absent,
	// which is what makes an operation naming a deletion unusable.
	bySite map[SiteID][]*block

	// pending holds operations whose dependencies have not arrived, filed under
	// the single operation each is waiting for. Integrating an operation wakes
	// exactly the operations that were waiting for it, so delivery in any order
	// costs one attempt per operation rather than a rescan of everything still
	// parked — the difference between linear and quadratic when a peer sends a
	// long history back to front.
	pending map[ID][]Op
	parked  int

	// dupDeletes records the losing operations when two replicas concurrently
	// delete the same character. Only one of them can be the character's
	// recorded deletion, and the other still has to be repeatable to peers that
	// have not seen it.
	dupDeletes map[ID]ID

	// mark is where the most recent local edit ended: character markPos of
	// block mark, which is the markIdx'th visible character. Finding a position
	// means walking the document, and someone typing asks for very nearly the
	// same position every keystroke, so the walk resumes from here. Any other
	// change clears it.
	mark    *block
	markPos int
	markIdx int

	// collect gathers what each operation did to the visible text, for callers
	// that have to keep a view in step. It is nil unless someone asked.
	collect *collector

	visible int // characters not tombstoned
	total   int // characters ever inserted
	// sup is how many of the visible characters are supplementary — outside the
	// Basic Multilingual Plane, and so two UTF-16 code units rather than one.
	// Held at the document as well as in the index because [Doc.LenUTF16] must
	// answer without reading, and therefore without flushing, the tree.
	sup int
}

// New returns an empty document that issues operations as site. Every replica
// editing a document concurrently must pass a distinct site; see [SiteID].
func New(site SiteID) *Doc {
	d := &Doc{
		site:   site,
		head:   &block{},
		vv:     VersionVector{},
		bySite: map[SiteID][]*block{},
	}
	d.startIndex()
	return d
}

// lookupChar returns the block and offset holding the character an operation
// created. The root ID names the position before the first character, reported
// as offset -1 of the sentinel, so that "insert after the origin" needs no
// special case.
func (d *Doc) lookupChar(id ID) (*block, int, bool) {
	if id.IsRoot() {
		return d.head, -1, true
	}
	blocks := d.bySite[id.Site]
	// The last block starting at or before the sequence number is the only one
	// that can hold it.
	n := sort.Search(len(blocks), func(i int) bool { return blocks[i].id.Seq > id.Seq })
	if n == 0 {
		return nil, 0, false
	}
	b := blocks[n-1]
	off := id.Seq - b.id.Seq
	if off >= uint64(len(b.text)) {
		return nil, 0, false
	}
	return b, int(off), true
}

// indexAdd files a block under its site, keeping the ranges ascending.
func (d *Doc) indexAdd(b *block) {
	blocks := d.bySite[b.id.Site]
	n := sort.Search(len(blocks), func(i int) bool { return blocks[i].id.Seq >= b.id.Seq })
	blocks = append(blocks, nil)
	copy(blocks[n+1:], blocks[n:])
	blocks[n] = b
	d.bySite[b.id.Site] = blocks
}

// split cuts b in two before offset i, which must be inside it, and returns the
// new right-hand block. Both halves are capped at their own length so that
// extending one can never write over the other's characters.
func (d *Doc) split(b *block, i int) *block {
	right := &block{
		id:       b.idAt(i),
		clock:    b.clockAt(i),
		originID: b.idAt(i - 1),
		text:     b.text[i:len(b.text):len(b.text)],
		next:     b.next,
	}
	// Dividing the supplementary count needs the characters read, so it is done
	// only for a block that has any — which for a document with no emoji in it is
	// no block at all.
	if b.nsup != 0 {
		right.nsup = countSup(right.text)
		b.nsup -= right.nsup
	}
	if len(b.dels) > 0 {
		right.dels = b.cutDels(i)
	}
	b.text = b.text[:i:i]
	right.prev = b
	if right.next != nil {
		right.next.prev = right
	}
	b.next = right
	d.indexAdd(right)
	// The characters the right-hand block takes over are the ones the left-hand
	// one no longer holds; index puts them back under the new block.
	d.addVis(b, -int32(right.visibleFrom(0)), -right.visibleSup())
	d.index(b, right)
	return right
}

// cutDels takes the deletion records from offset i onwards off b and returns
// them rebased to zero, for the block being split off. A record straddling the
// cut is divided, the right-hand part inheriting the identities its sequence
// gives those characters.
func (b *block) cutDels(i int) []delRange {
	at := uint32(i)
	n := 0
	for n < len(b.dels) && b.dels[n].to <= at {
		n++
	}
	moved := make([]delRange, 0, len(b.dels)-n)
	keep := b.dels[:n:n]
	if n < len(b.dels) && b.dels[n].from < at {
		straddling := b.dels[n]
		keep = append(keep, delRange{from: straddling.from, to: at, id: straddling.id})
		moved = append(moved, delRange{to: straddling.to - at, id: straddling.continuesTo(at)})
		n++
	}
	for _, r := range b.dels[n:] {
		moved = append(moved, delRange{from: r.from - at, to: r.to - at, id: r.id})
	}
	b.dels = keep
	return moved
}

// extends reports whether a character is exactly the next one of b's run: the
// same site, the next sequence number and clock, and inserted after b's last
// character. Only then does it belong in the same block.
func extends(b *block, id ID, clock uint64, origin ID) bool {
	n := uint64(len(b.text))
	if n == 0 || b.id.Site != id.Site {
		return false
	}
	return b.id.Seq+n == id.Seq &&
		b.clock+n == clock &&
		origin == ID{Site: b.id.Site, Seq: b.id.Seq + n - 1}
}

// Site returns the replica identity this document issues operations as.
func (d *Doc) Site() SiteID { return d.site }

// Len returns the number of visible characters, counted in runes.
func (d *Doc) Len() int { return d.visible }

// Tombstones returns the number of deleted characters still held in memory.
// They cannot be dropped, because a concurrent insertion may name one as its
// origin; see docs/performance.md.
func (d *Doc) Tombstones() int { return d.total - d.visible }

// Pending returns the number of operations buffered awaiting their
// dependencies. A healthy replica returns to zero once delivery catches up; a
// number that only grows means a peer is withholding operations.
func (d *Doc) Pending() int { return d.parked }

// Version returns a copy of the version vector describing which operations this
// replica holds. Pass it to a peer's [Doc.OpsSince] to be sent exactly what is
// missing.
func (d *Doc) Version() VersionVector { return d.vv.Clone() }

// String returns the document text.
func (d *Doc) String() string {
	var b strings.Builder
	b.Grow(d.visible)
	for blk := d.head.next; blk != nil; blk = blk.next {
		if len(blk.dels) == 0 {
			// The common case by far: a whole run still visible, written in one go.
			b.WriteString(string(blk.text))
			continue
		}
		at := 0
		for _, r := range blk.dels {
			if int(r.from) > at {
				b.WriteString(string(blk.text[at:r.from]))
			}
			at = int(r.to)
		}
		if at < len(blk.text) {
			b.WriteString(string(blk.text[at:]))
		}
	}
	return b.String()
}

// visibleAt returns the block and offset of the visible character at index pos,
// which the caller must have checked is in range.
//
// Editing is local — the position wanted is nearly always a few characters from
// the last one — so the walk starts at the mark and goes whichever way it has
// to, and steps over whole runs that are still intact rather than counting
// their characters. Without the mark, or when the position turns out to be
// further off than the index would have cost, it descends the index instead.
func (d *Doc) visibleAt(pos int) (*block, int) {
	if d.mark != nil {
		if pos >= d.markIdx {
			if b, i, ok := forward(d.mark, d.markPos, d.markIdx, pos, scanBudget); ok {
				return b, i
			}
		} else if b, i, ok := backward(d.mark, d.markPos, d.markIdx, pos, scanBudget); ok {
			return b, i
		}
	}
	return d.seek(pos)
}

// forward walks towards the end of the document from a known position, giving
// up after budget runs so that a position far from the mark is answered by the
// index instead.
func forward(b *block, i, n, pos, budget int) (*block, int, bool) {
	for ; ; b, i = b.next, 0 {
		rest := b.visibleFrom(i)
		if n+rest <= pos {
			if budget == 0 {
				return nil, 0, false
			}
			budget--
			n += rest
			continue
		}
		return b, b.visibleOffset(i, pos-n), true
	}
}

// backward walks towards the start of the document from a known position, under
// the same budget as forward.
func backward(b *block, i, n, pos, budget int) (*block, int, bool) {
	for {
		// The visible characters at offset i or earlier are the ones ending at
		// n, so the first of them is n-v+1'th in the document.
		first := n - b.visibleUpto(i) + 1
		if pos >= first {
			return b, b.visibleOffset(0, pos-first), true
		}
		if budget == 0 {
			return nil, 0, false
		}
		budget--
		n = first - 1
		b = b.prev
		i = len(b.text) - 1
	}
}

// leftOf returns the visible character before the one at (b, i), which is the
// character at index pos. It is a step back, unless a stretch of tombstoned
// runs lies between the two, which is what the budget catches.
func (d *Doc) leftOf(b *block, i, pos int) (*block, int) {
	if left, at, ok := backward(b, i, pos, pos-1, scanBudget); ok {
		return left, at
	}
	return d.seek(pos - 1)
}

// mint allocates this site's next operation identity and Lamport timestamp.
// Seq follows the site's own version vector entry, so the site's sequence never
// has a gap; the clock jumps past everything the replica has seen.
func (d *Doc) mint() (ID, uint64) {
	d.clock++
	return ID{Site: d.site, Seq: d.vv[d.site] + 1}, d.clock
}

// Insert adds text at rune offset pos and returns the operations that describe
// it. The operations are already applied here; send them to every peer.
//
// pos may equal [Doc.Len], which appends. Inserting the empty string is a
// no-op that returns no operations.
func (d *Doc) Insert(pos int, text string) ([]Op, error) {
	if pos < 0 || pos > d.visible {
		return nil, ErrOutOfRange
	}
	if !utf8.ValidString(text) {
		return nil, ErrInvalidText
	}
	if text == "" {
		return nil, nil
	}
	n := utf8.RuneCountInString(text)
	if err := room(d.clock, n); err != nil {
		return nil, err
	}
	left, leftAt := d.head, -1
	if pos > 0 {
		left, leftAt = d.visibleAt(pos - 1)
	}
	ops := make([]Op, 0, n)
	for _, r := range text {
		id, clock := d.mint()
		op := Op{Kind: OpInsert, ID: id, Clock: clock, Origin: originOf(left, leftAt), Char: r}
		left, leftAt = d.integrate(op)
		ops = append(ops, op)
	}
	d.mark, d.markPos, d.markIdx = left, leftAt, pos+len(ops)-1
	return ops, nil
}

// originOf names the character an insertion at this position follows.
func originOf(b *block, i int) ID {
	if i < 0 {
		return ID{}
	}
	return b.idAt(i)
}

// Delete removes length runes starting at rune offset pos and returns the
// operations that describe it. Deleting nothing is a no-op.
func (d *Doc) Delete(pos, length int) ([]Op, error) {
	if pos < 0 || length < 0 || pos+length > d.visible {
		return nil, ErrOutOfRange
	}
	if length == 0 {
		return nil, nil
	}
	if err := room(d.clock, length); err != nil {
		return nil, err
	}
	// Collect the targets before deleting any of them: tombstoning a character
	// shifts every later visible index.
	targets := make([]ID, 0, length)
	b, i := d.visibleAt(pos)
	// Remember the character just before what is about to go, so the next edit
	// resumes from here instead of from the start of the document. Integration
	// clears the mark, so this has to be captured now and set afterwards.
	var leftB *block
	var leftAt int
	if pos > 0 {
		leftB, leftAt = d.leftOf(b, i, pos)
	}
	for len(targets) < length {
		if i == len(b.text) {
			b, i = b.next, 0
			continue
		}
		if b.aliveAt(i) {
			targets = append(targets, b.idAt(i))
		}
		i++
	}
	ops := make([]Op, 0, length)
	for _, target := range targets {
		id, clock := d.mint()
		op := Op{Kind: OpDelete, ID: id, Clock: clock, Target: target}
		d.integrate(op)
		ops = append(ops, op)
	}
	if leftB != nil {
		d.mark, d.markPos, d.markIdx = leftB, leftAt, pos-1
	}
	return ops, nil
}

// admit integrates an operation, or parks it under whatever it is waiting for,
// and then does the same for everything that was waiting on what it integrated.
//
// The work list is explicit rather than recursive: a history delivered back to
// front unblocks a chain as long as the history itself.
func (d *Doc) admit(op Op) {
	queue := []Op{op}
	for len(queue) > 0 {
		next := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if d.vv.Includes(next.ID) {
			continue // already applied; applying twice must not change anything
		}
		if !d.ready(next) {
			d.park(next)
			continue
		}
		d.integrate(next)
		if woken, ok := d.pending[next.ID]; ok {
			delete(d.pending, next.ID)
			d.parked -= len(woken)
			queue = append(queue, woken...)
		}
	}
}

// park files an operation under the one it is waiting for.
func (d *Doc) park(op Op) {
	if d.pending == nil {
		d.pending = map[ID][]Op{}
	}
	waitingFor := d.blockedOn(op)
	d.pending[waitingFor] = append(d.pending[waitingFor], op)
	d.parked++
}

// blockedOn names the operation that has to land before this one can be tried
// again: its own predecessor from the same site if that has not arrived, and
// otherwise the character it refers to.
func (d *Doc) blockedOn(op Op) ID {
	if op.ID.Seq != d.vv[op.ID.Site]+1 {
		return ID{Site: op.ID.Site, Seq: op.ID.Seq - 1}
	}
	if op.Kind == OpInsert {
		return op.Origin
	}
	return op.Target
}

// ready reports whether an operation can be integrated now: its site's previous
// operation has landed, and the character it refers to exists.
func (d *Doc) ready(op Op) bool {
	if op.ID.Seq != d.vv[op.ID.Site]+1 {
		return false
	}
	if op.Kind == OpInsert {
		_, _, ok := d.lookupChar(op.Origin)
		return ok
	}
	_, _, ok := d.lookupChar(op.Target)
	return ok
}

// integrate applies a ready, validated operation and reports where the character
// it created ended up. The position is meaningless for a deletion.
func (d *Doc) integrate(op Op) (*block, int) {
	// Any change can move what the mark points at, or what index it sits at.
	// [Doc.Insert] re-establishes it once its whole run is in.
	d.mark = nil
	if op.Clock > d.clock {
		d.clock = op.Clock
	}
	d.vv[op.ID.Site] = op.ID.Seq
	if op.Kind == OpDelete {
		d.tombstone(op)
		return nil, 0
	}
	d.visible++
	d.total++
	d.sup += int(supUnit(op.Char))
	b, i := d.place(op.ID, op.Clock, op.Origin, op.Char)
	d.recordInsert(b, i, op.Char)
	return b, i
}

// beforeChar is [before] against character i of at, which is the comparison the
// integration walk makes most.
//
// It exists so that the character's identity is not built unless the comparison
// reaches it. Only a tie on both clock and site does, and nothing an honest
// replica produces ties on the clock at all — so on the path a person typing
// takes, this is the two comparisons it always was.
func beforeChar(clock uint64, id ID, at *block, i int) bool {
	c := at.clockAt(i)
	if clock != c {
		return clock < c
	}
	if id.Site != at.id.Site {
		return id.Site < at.id.Site
	}
	return id.Seq < at.id.Seq+uint64(i)
}

// place puts one character into the sequence and returns where it landed.
//
// Integration walks forward from the origin past everything that sorts after the
// new character. Within a block the clocks ascend, so "sorts after the new
// character" can only switch from false to true as the walk advances: whichever
// it is at the first character of a run holds for the whole run. The walk
// therefore costs one comparison per run rather than one per character.
func (d *Doc) place(id ID, clock uint64, origin ID, ch rune) (*block, int) {
	at, i, _ := d.lookupChar(origin)
	i++
	for budget := scanBudget; ; {
		if i < len(at.text) {
			if !beforeChar(clock, id, at, i) {
				break // the character here sorts first; the new one goes before it
			}
			i = len(at.text) // the rest of this run sorts after; step over it
			continue
		}
		next := at.next
		if next == nil || !before(clock, id, next.clock, next.id) {
			break
		}
		if budget == 0 {
			// The run of characters sorting after this one is longer than a
			// descent, and a peer sending operations that all name one origin
			// makes it as long as the document. The index finds its end.
			at = d.lastOver(at, clock, id)
			i = len(at.text)
			break
		}
		budget--
		at, i = next, len(next.text)
	}
	if i < len(at.text) {
		d.split(at, i)
	}

	// The character usually continues the run it lands after — that is what
	// typing is, and what delivering someone else's typing is — so it is
	// appended in place: no block, no index entry, nothing to merge afterwards.
	if extends(at, id, clock, origin) {
		at.text = append(at.text, ch)
		at.nsup += supUnit(ch)
		d.addVis(at, 1, supUnit(ch))
		return at, len(at.text) - 1
	}

	// Otherwise it starts a run of its own. It can never join the run in the
	// block that follows: doing so would need the sequence number that block
	// already holds.
	fresh := &block{id: id, clock: clock, originID: origin, text: []rune{ch}, nsup: supUnit(ch), next: at.next, prev: at}
	if fresh.next != nil {
		fresh.next.prev = fresh
	}
	at.next = fresh
	d.indexAdd(fresh)
	d.index(at, fresh)
	return fresh, 0
}

// tombstone marks a character deleted.
//
// Two replicas may delete the same character concurrently. The character can
// only record one of those operations, so it keeps the lower ID — the same
// choice on every replica, whatever order the two arrive in — and the other is
// remembered separately so it can still be replayed to peers.
func (d *Doc) tombstone(op Op) {
	b, i, _ := d.lookupChar(op.Target)
	switch existing := b.delIDAt(i); {
	case existing.IsRoot():
		sup := supUnit(b.text[i])
		// While it is still visible, which is what gives it an offset of its own.
		d.recordDelete(b, i)
		b.markDeleted(i, op.ID)
		d.addVis(b, -1, -sup)
		d.visible--
		d.sup -= int(sup)
	case idLess(op.ID, existing):
		b.dels[b.isolate(i)].id = op.ID
		d.recordDuplicate(existing, op.Target)
	default:
		d.recordDuplicate(op.ID, op.Target)
	}
}

func (d *Doc) recordDuplicate(delID, target ID) {
	if d.dupDeletes == nil {
		d.dupDeletes = map[ID]ID{}
	}
	d.dupDeletes[delID] = target
}

// idLess orders IDs by site then sequence. It is the tie-break for concurrent
// deletions of one character, where no Lamport clock is available because only
// the winning operation's ID is kept.
func idLess(a, b ID) bool {
	if a.Site != b.Site {
		return a.Site < b.Site
	}
	return a.Seq < b.Seq
}

// OpsSince returns the operations this replica holds that vv does not, ready to
// be sent to the peer that produced vv. Pass a nil vector for the whole history.
//
// The result is in document order, which for insertions is a causal order: a
// character always follows its origin. Deletions may arrive before the
// insertions they refer to, which the receiving [Doc.Apply] buffers.
//
// A deletion's Lamport timestamp is not retained — it never affects ordering —
// so replayed deletions carry their sequence number as their clock.
func (d *Doc) OpsSince(vv VersionVector) []Op {
	var ops []Op
	for b := d.head.next; b != nil; b = b.next {
		cursor := delCursor{b: b}
		for i, r := range b.text {
			id := b.idAt(i)
			if !vv.Includes(id) {
				ops = append(ops, Op{
					Kind:   OpInsert,
					ID:     id,
					Clock:  b.clockAt(i),
					Origin: b.originAt(i),
					Char:   r,
				})
			}
			if del := cursor.at(i); !del.IsRoot() && !vv.Includes(del) {
				ops = append(ops, deleteOp(del, id))
			}
		}
	}
	dups := make([]ID, 0, len(d.dupDeletes))
	for delID := range d.dupDeletes {
		if !vv.Includes(delID) {
			dups = append(dups, delID)
		}
	}
	sortIDs(dups)
	for _, delID := range dups {
		ops = append(ops, deleteOp(delID, d.dupDeletes[delID]))
	}
	return ops
}

// deleteOp rebuilds a deletion from the two IDs a document retains.
func deleteOp(delID, target ID) Op {
	return Op{Kind: OpDelete, ID: delID, Clock: delID.Seq, Target: target}
}

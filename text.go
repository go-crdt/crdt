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
	// dead is nil while every character in the block is visible. Otherwise it
	// has one entry per character: the identity of the operation that deleted
	// it, or the zero ID if it is still visible.
	dead []ID
	next *block
	// prev makes the walk in visibleAt run both ways. Editing is local: the next
	// position asked for is usually just before or just after the last one, and
	// a list that can only be walked forwards turns "just before" into a walk
	// from the beginning of the document.
	prev *block
}

func (b *block) idAt(i int) ID        { return ID{Site: b.id.Site, Seq: b.id.Seq + uint64(i)} }
func (b *block) clockAt(i int) uint64 { return b.clock + uint64(i) }
func (b *block) aliveAt(i int) bool   { return b.dead == nil || b.dead[i].IsRoot() }

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

	visible int // characters not tombstoned
	total   int // characters ever inserted
}

// New returns an empty document that issues operations as site. Every replica
// editing a document concurrently must pass a distinct site; see [SiteID].
func New(site SiteID) *Doc {
	return &Doc{
		site:   site,
		head:   &block{},
		vv:     VersionVector{},
		bySite: map[SiteID][]*block{},
	}
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
	if b.dead != nil {
		right.dead = b.dead[i:len(b.dead):len(b.dead)]
		b.dead = b.dead[:i:i]
	}
	b.text = b.text[:i:i]
	right.prev = b
	if right.next != nil {
		right.next.prev = right
	}
	b.next = right
	d.indexAdd(right)
	return right
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
		if blk.dead == nil {
			// The common case by far: a whole run still visible, written in one go.
			b.WriteString(string(blk.text))
			continue
		}
		for i, r := range blk.text {
			if blk.dead[i].IsRoot() {
				b.WriteRune(r)
			}
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
// their characters. Without the mark it starts at the beginning.
func (d *Doc) visibleAt(pos int) (*block, int) {
	if d.mark != nil {
		if pos >= d.markIdx {
			return forward(d.mark, d.markPos, d.markIdx, pos)
		}
		return backward(d.mark, d.markPos, d.markIdx, pos)
	}
	return forward(d.head.next, 0, 0, pos)
}

// forward walks towards the end of the document from a known position.
func forward(b *block, i, n, pos int) (*block, int) {
	for ; ; b, i = b.next, 0 {
		if b.dead == nil {
			if rest := len(b.text) - i; n+rest <= pos {
				n += rest
				continue
			}
			return b, i + pos - n
		}
		for ; i < len(b.text); i++ {
			if !b.aliveAt(i) {
				continue
			}
			if n == pos {
				return b, i
			}
			n++
		}
	}
}

// backward walks towards the start of the document from a known position.
func backward(b *block, i, n, pos int) (*block, int) {
	for {
		// Step back over tombstones and block boundaries until (b, i) names a
		// visible character again — which is then the n'th.
		for i < 0 || !b.aliveAt(i) {
			if i < 0 {
				b = b.prev
				i = len(b.text) - 1
				continue
			}
			i--
		}
		if n == pos {
			return b, i
		}
		if b.dead == nil {
			// Every character before this one in the run is visible too, so the
			// one wanted is here whenever it is within reach.
			if n-pos <= i {
				return b, i - (n - pos)
			}
			n -= i + 1 // stepping off the front of the run costs i+1 of them
			i = -1
			continue
		}
		n--
		i--
	}
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
	left, leftAt := d.head, -1
	if pos > 0 {
		left, leftAt = d.visibleAt(pos - 1)
	}
	ops := make([]Op, 0, utf8.RuneCountInString(text))
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
		leftB, leftAt = backward(b, i, pos, pos-1)
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

// Apply integrates operations from peers. Duplicates are ignored, and an
// operation that arrives before the operations it depends on is buffered until
// they do, so the caller needs no ordered delivery.
//
// A malformed operation is rejected and nothing in the batch is applied.
func (d *Doc) Apply(ops ...Op) error {
	for _, op := range ops {
		if err := op.validate(); err != nil {
			return err
		}
	}
	for _, op := range ops {
		d.admit(op)
	}
	return nil
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
	return d.place(op.ID, op.Clock, op.Origin, op.Char)
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
	for {
		if i < len(at.text) {
			if !before(clock, id.Site, at.clockAt(i), at.id.Site) {
				break // the character here sorts first; the new one goes before it
			}
			i = len(at.text) // the rest of this run sorts after; step over it
			continue
		}
		next := at.next
		if next == nil || !before(clock, id.Site, next.clock, next.id.Site) {
			break
		}
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
		if at.dead != nil {
			at.dead = append(at.dead, ID{})
		}
		return at, len(at.text) - 1
	}

	// Otherwise it starts a run of its own. It can never join the run in the
	// block that follows: doing so would need the sequence number that block
	// already holds.
	fresh := &block{id: id, clock: clock, originID: origin, text: []rune{ch}, next: at.next, prev: at}
	if fresh.next != nil {
		fresh.next.prev = fresh
	}
	at.next = fresh
	d.indexAdd(fresh)
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
	switch {
	case b.aliveAt(i):
		if b.dead == nil {
			b.dead = make([]ID, len(b.text))
		}
		b.dead[i] = op.ID
		d.visible--
	case idLess(op.ID, b.dead[i]):
		d.recordDuplicate(b.dead[i], op.Target)
		b.dead[i] = op.ID
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
			if b.dead != nil && !b.dead[i].IsRoot() && !vv.Includes(b.dead[i]) {
				ops = append(ops, deleteOp(b.dead[i], id))
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

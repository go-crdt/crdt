package crdt

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// ErrOutOfRange reports a position or length outside the document.
var ErrOutOfRange = errors.New("crdt: position out of range")

// ErrInvalidText reports text that is not valid UTF-8. The package refuses it
// rather than substituting replacement characters, which would silently corrupt
// a document that no later edit could repair.
var ErrInvalidText = errors.New("crdt: invalid UTF-8")

// An item is one character of the sequence, alive or tombstoned. Items are never
// unlinked: a concurrent insertion may still name a deleted character as its
// origin, so the list only grows.
//
// origin is a pointer rather than an ID because the origin is always already in
// the list — the root sentinel at worst — and the pointer costs half as much.
type item struct {
	id     ID
	origin *item
	clock  uint64
	ch     rune
	delID  ID // zero while the character is visible
	next   *item
}

// alive reports whether the character is still visible.
func (it *item) alive() bool { return it.delID.IsRoot() }

// A Doc is one replica of a text document. It is not safe for concurrent use;
// serialize access from the outside, as an editor naturally does.
//
// The zero Doc is unusable — construct one with [New] or [Load].
type Doc struct {
	site  SiteID
	clock uint64 // Lamport clock: at least as high as any clock seen

	head *item // sentinel for the root ID; head.next is the first character
	vv   VersionVector

	// chars indexes characters by the operation that created them: chars[site][n]
	// is the character made by that site's operation number n+1, or nil where
	// that operation was a deletion and made none.
	//
	// A map keyed by ID would be the obvious structure and costs about as much
	// again as the character it points at. It is not needed: a site's sequence
	// numbers start at one and have no gaps, and [Doc.Apply] refuses an operation
	// until its predecessor has landed, so position in a slice *is* the sequence
	// number. Lookup stays O(1) and the per-character overhead drops to one
	// pointer.
	chars map[SiteID][]*item

	// pending holds operations whose dependencies have not arrived.
	pending []Op

	// dupDeletes records the losing operations when two replicas concurrently
	// delete the same character. Only one of them can be the item's delID, and
	// the other still has to be repeatable to peers that have not seen it.
	dupDeletes map[ID]ID

	// mark is the character inserted by the most recent local edit, and markIdx
	// its visible index. Finding a position means walking the list, and someone
	// typing asks for very nearly the same position every keystroke, so the walk
	// resumes from here instead of from the start. Any other change clears it.
	mark    *item
	markIdx int

	visible int // characters not tombstoned
	total   int // characters ever inserted
}

// New returns an empty document that issues operations as site. Every replica
// editing a document concurrently must pass a distinct site; see [SiteID].
func New(site SiteID) *Doc {
	return &Doc{
		site:  site,
		head:  &item{},
		vv:    VersionVector{},
		chars: map[SiteID][]*item{},
	}
}

// lookup returns the character an operation created. The root ID names the
// sentinel before the first character, which every document has.
func (d *Doc) lookup(id ID) (*item, bool) {
	if id.IsRoot() {
		return d.head, true
	}
	made := d.chars[id.Site]
	if id.Seq > uint64(len(made)) {
		return nil, false
	}
	it := made[id.Seq-1]
	return it, it != nil
}

// record files the character an operation created, or nil for a deletion, which
// creates none. Operations from a site arrive in order and none is skipped, so
// this is always an append.
func (d *Doc) record(id ID, it *item) {
	d.chars[id.Site] = append(d.chars[id.Site], it)
}

// Site returns the replica identity this document issues operations as.
func (d *Doc) Site() SiteID { return d.site }

// Len returns the number of visible characters, counted in runes.
func (d *Doc) Len() int { return d.visible }

// Tombstones returns the number of deleted characters still held in memory.
// They cannot be dropped, because a concurrent insertion may name one as its
// origin; see docs/memory.md.
func (d *Doc) Tombstones() int { return d.total - d.visible }

// Pending returns the number of operations buffered awaiting their
// dependencies. A healthy replica returns to zero once delivery catches up; a
// number that only grows means a peer is withholding operations.
func (d *Doc) Pending() int { return len(d.pending) }

// Version returns a copy of the version vector describing which operations this
// replica holds. Pass it to a peer's [Doc.OpsSince] to be sent exactly what is
// missing.
func (d *Doc) Version() VersionVector { return d.vv.Clone() }

// String returns the document text.
func (d *Doc) String() string {
	var b strings.Builder
	b.Grow(d.visible)
	for it := d.head.next; it != nil; it = it.next {
		if it.alive() {
			b.WriteRune(it.ch)
		}
	}
	return b.String()
}

// visibleAt returns the item holding the visible character at index pos, which
// the caller must have checked is in range. It resumes from the mark whenever
// the mark is at or before the position wanted.
func (d *Doc) visibleAt(pos int) *item {
	it, n := d.head.next, 0
	if d.mark != nil && d.markIdx <= pos {
		it, n = d.mark, d.markIdx
	}
	for ; ; it = it.next {
		if !it.alive() {
			continue
		}
		if n == pos {
			return it
		}
		n++
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
	left := d.head
	if pos > 0 {
		left = d.visibleAt(pos - 1)
	}
	ops := make([]Op, 0, utf8.RuneCountInString(text))
	for _, r := range text {
		id, clock := d.mint()
		op := Op{Kind: OpInsert, ID: id, Clock: clock, Origin: left.id, Char: r}
		d.integrate(op)
		ops = append(ops, op)
		left, _ = d.lookup(id)
	}
	d.mark, d.markIdx = left, pos+len(ops)-1
	return ops, nil
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
	it := d.visibleAt(pos)
	for len(targets) < length {
		if it.alive() {
			targets = append(targets, it.id)
		}
		it = it.next
	}
	ops := make([]Op, 0, length)
	for _, target := range targets {
		id, clock := d.mint()
		op := Op{Kind: OpDelete, ID: id, Clock: clock, Target: target}
		d.integrate(op)
		ops = append(ops, op)
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
	progress := false
	for _, op := range ops {
		switch {
		case d.vv.Includes(op.ID):
			// Already applied; applying twice must not change anything.
		case d.ready(op):
			d.integrate(op)
			progress = true
		default:
			d.pending = append(d.pending, op)
		}
	}
	if progress {
		d.drain()
	}
	return nil
}

// ready reports whether an operation can be integrated now: its site's previous
// operation has landed, and the character it refers to exists.
func (d *Doc) ready(op Op) bool {
	if op.ID.Seq != d.vv[op.ID.Site]+1 {
		return false
	}
	if op.Kind == OpInsert {
		_, ok := d.lookup(op.Origin)
		return ok
	}
	_, ok := d.lookup(op.Target)
	return ok
}

// drain integrates buffered operations until no more become ready. Each pass
// can unblock operations that depend on the ones it just applied, so it repeats
// until a pass changes nothing.
func (d *Doc) drain() {
	for {
		moved := false
		kept := d.pending[:0]
		for _, op := range d.pending {
			switch {
			case d.vv.Includes(op.ID):
				moved = true
			case d.ready(op):
				d.integrate(op)
				moved = true
			default:
				kept = append(kept, op)
			}
		}
		d.pending = kept
		if !moved {
			return
		}
	}
}

// integrate applies a ready, validated operation.
func (d *Doc) integrate(op Op) {
	// Any change can move what the mark points at, or what index it sits at.
	// [Doc.Insert] re-establishes it once its whole run is in.
	d.mark = nil
	if op.Clock > d.clock {
		d.clock = op.Clock
	}
	d.vv[op.ID.Site] = op.ID.Seq
	if op.Kind == OpDelete {
		// A deletion makes no character, but it does consume a sequence number,
		// and the index is addressed by sequence number.
		d.record(op.ID, nil)
		d.tombstone(op)
		return
	}
	origin, _ := d.lookup(op.Origin)
	// Walk past the characters that sort after the new one. Every character
	// inserted after origin by a replica that had already seen origin carries a
	// higher Lamport clock than origin, and so does everything inserted after
	// it; the first character that sorts before the new one therefore lies
	// outside the region this insertion can land in, and the scan can stop.
	prev := origin
	for at := origin.next; at != nil && before(op.Clock, op.ID.Site, at.clock, at.id.Site); at = at.next {
		prev = at
	}
	it := &item{id: op.ID, origin: origin, clock: op.Clock, ch: op.Char, next: prev.next}
	prev.next = it
	d.record(op.ID, it)
	d.visible++
	d.total++
}

// tombstone marks a character deleted.
//
// Two replicas may delete the same character concurrently. The item can only
// name one of those operations, so it keeps the lower ID — the same choice on
// every replica, whatever order the two arrive in — and the other is remembered
// separately so it can still be replayed to peers.
func (d *Doc) tombstone(op Op) {
	target, _ := d.lookup(op.Target)
	switch {
	case target.alive():
		target.delID = op.ID
		d.visible--
	case idLess(op.ID, target.delID):
		d.recordDuplicate(target.delID, op.Target)
		target.delID = op.ID
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
// deletions of one character, where no Lamport clock is available because the
// item records only the winning operation's ID.
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
	for it := d.head.next; it != nil; it = it.next {
		if !vv.Includes(it.id) {
			ops = append(ops, Op{
				Kind:   OpInsert,
				ID:     it.id,
				Clock:  it.clock,
				Origin: it.origin.id,
				Char:   it.ch,
			})
		}
		if !it.delID.IsRoot() && !vv.Includes(it.delID) {
			ops = append(ops, deleteOp(it.delID, it.id))
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

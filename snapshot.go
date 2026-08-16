package crdt

import (
	"encoding/binary"
	"unicode/utf8"
)

// snapshotMagic prefixes every snapshot, followed by a one-byte format version,
// so a decoder rejects foreign or future bytes instead of misreading them.
var snapshotMagic = [...]byte{'c', 'r', 'd', 't'}

const snapshotVersion = 1

// Snapshot encodes the whole document — every character, alive or tombstoned,
// in document order, plus the version vector. It is what a server sends a
// client joining an existing session, and what it persists.
//
// The encoding is per character rather than per run, deliberately: it is the
// interchange format, and holding it independent of how a replica happens to
// store the document in memory is what lets that change without a flag day.
//
// The encoding is deterministic: two replicas holding the same operations
// produce identical bytes, so a snapshot doubles as a convergence check.
//
// The full history is recoverable from a snapshot: [Doc.OpsSince] on a loaded
// document returns the same operations it would have on the original.
func (d *Doc) Snapshot() []byte {
	out := make([]byte, 0, 5+8*d.total)
	out = append(out, snapshotMagic[:]...)
	out = append(out, snapshotVersion)

	sites := d.vv.sites()
	out = binary.AppendUvarint(out, uint64(len(sites)))
	for _, site := range sites {
		out = binary.AppendUvarint(out, uint64(site))
		out = binary.AppendUvarint(out, d.vv[site])
	}

	out = binary.AppendUvarint(out, uint64(d.total))
	for b := d.head.next; b != nil; b = b.next {
		for i, r := range b.text {
			out = binary.AppendUvarint(out, uint64(b.id.Site))
			out = binary.AppendUvarint(out, b.id.Seq+uint64(i))
			out = binary.AppendUvarint(out, b.clockAt(i))
			origin := b.originAt(i)
			out = binary.AppendUvarint(out, uint64(origin.Site))
			out = binary.AppendUvarint(out, origin.Seq)
			out = binary.AppendUvarint(out, uint64(r))
			var del ID
			if b.dead != nil {
				del = b.dead[i]
			}
			out = binary.AppendUvarint(out, uint64(del.Site))
			out = binary.AppendUvarint(out, del.Seq)
		}
	}

	dups := make([]ID, 0, len(d.dupDeletes))
	for delID := range d.dupDeletes {
		dups = append(dups, delID)
	}
	sortIDs(dups)
	out = binary.AppendUvarint(out, uint64(len(dups)))
	for _, delID := range dups {
		target := d.dupDeletes[delID]
		out = binary.AppendUvarint(out, uint64(delID.Site))
		out = binary.AppendUvarint(out, delID.Seq)
		out = binary.AppendUvarint(out, uint64(target.Site))
		out = binary.AppendUvarint(out, target.Seq)
	}
	return out
}

// Load rebuilds a document from a snapshot, to be edited as site. The site need
// not be one that appears in the snapshot — a client joining an existing
// document brings its own.
func Load(site SiteID, snapshot []byte) (*Doc, error) {
	r := &reader{buf: snapshot}
	magic, ok := r.bytes(len(snapshotMagic))
	if !ok || string(magic) != string(snapshotMagic[:]) {
		return nil, ErrMalformed
	}
	if v, ok := r.bytes(1); !ok || v[0] != snapshotVersion {
		return nil, ErrMalformed
	}

	d := New(site)
	nSites, ok := r.uvarint()
	if !ok {
		return nil, ErrMalformed
	}
	for range nSites {
		s, ok1 := r.uvarint()
		seq, ok2 := r.uvarint()
		if !ok1 || !ok2 || seq == 0 {
			return nil, ErrMalformed
		}
		// A site listed twice would leave which of the two entries applies up to
		// decoding order, and the version vector is what every other check here
		// is measured against.
		if _, dup := d.vv[SiteID(s)]; dup {
			return nil, ErrMalformed
		}
		d.vv[SiteID(s)] = seq
	}

	// A snapshot has to account for every operation its version vector claims,
	// exactly once. Anything less and the document could not reproduce its own
	// history: a peer replaying it would stall on the missing sequence number, or
	// silently drop a deletion two characters both claimed.
	ledger := &ledger{vv: d.vv, seen: map[ID]struct{}{}, counts: map[SiteID]uint64{}}

	nItems, ok := r.uvarint()
	if !ok || nItems > uint64(len(r.buf)) {
		return nil, ErrMalformed
	}
	for range nItems {
		c, ok := r.character()
		if !ok {
			return nil, ErrMalformed
		}
		if err := d.adopt(c, ledger); err != nil {
			return nil, err
		}
	}

	nDups, ok := r.uvarint()
	if !ok || nDups > uint64(len(r.buf)) {
		return nil, ErrMalformed
	}
	for range nDups {
		delID, ok1 := r.id()
		target, ok2 := r.id()
		if !ok1 || !ok2 || !ledger.claim(delID) {
			return nil, ErrMalformed
		}
		// A duplicate deletion only ever arises when a character was already
		// deleted, and the character keeps the lower of the two operations.
		// Anything else describes a document no replica could reach: a deletion
		// of a character still visible would take effect on replay and diverge.
		b, i, known := d.lookupChar(target)
		if !known || b.aliveAt(i) || !idLess(b.dead[i], delID) {
			return nil, ErrMalformed
		}
		d.recordDuplicate(delID, target)
	}
	if len(r.buf) != 0 || !ledger.complete() {
		return nil, ErrMalformed
	}

	// The Lamport clock must be at least as high as anything the document
	// records, including sequence numbers of deletions, whose clocks a snapshot
	// does not keep. Otherwise this replica's next operation could claim a clock
	// below its own sequence number.
	for _, seq := range d.vv {
		if seq > d.clock {
			d.clock = seq
		}
	}
	return d, nil
}

// A character is one decoded entry of a snapshot.
type character struct {
	id     ID
	clock  uint64
	origin ID
	ch     rune
	delID  ID
}

// adopt puts a decoded character into the document, checking that it is
// consistent with what came before.
//
// The order a snapshot states is not a matter of choice: integration determines
// exactly where a character goes. So rather than trusting the stated order and
// re-deriving the check, the character is integrated exactly as an operation
// would be — and then required to have landed at the end. Anywhere else means
// the snapshot claims an order integration could not have produced.
func (d *Doc) adopt(c character, l *ledger) error {
	if !l.claim(c.id) {
		return ErrMalformed
	}
	if _, _, known := d.lookupChar(c.origin); !known {
		return ErrMalformed
	}
	if !c.delID.IsRoot() && !l.claim(c.delID) {
		return ErrMalformed
	}

	b, i := d.place(c.id, c.clock, c.origin, c.ch)
	if b.next != nil || i != len(b.text)-1 {
		return ErrMalformed
	}
	d.total++
	if c.delID.IsRoot() {
		d.visible++
	} else {
		if b.dead == nil {
			b.dead = make([]ID, len(b.text))
		}
		b.dead[i] = c.delID
	}
	if c.clock > d.clock {
		d.clock = c.clock
	}
	return nil
}

// A ledger tracks which operations a snapshot has accounted for, so that Load
// can insist on exactly the set the version vector promises.
type ledger struct {
	vv     VersionVector
	seen   map[ID]struct{}
	counts map[SiteID]uint64
}

// claim records one operation identity, rejecting anything the version vector
// does not cover and anything claimed twice.
func (l *ledger) claim(id ID) bool {
	if id.IsRoot() || !l.vv.Includes(id) {
		return false
	}
	if _, dup := l.seen[id]; dup {
		return false
	}
	l.seen[id] = struct{}{}
	l.counts[id.Site]++
	return true
}

// complete reports whether every operation the version vector promises was
// claimed. Sequence numbers have no gaps, so counting them is enough.
func (l *ledger) complete() bool {
	for site, seq := range l.vv {
		if l.counts[site] != seq {
			return false
		}
	}
	return true
}

// A reader consumes a snapshot, reporting failure rather than panicking on
// truncated input.
type reader struct{ buf []byte }

func (r *reader) bytes(n int) ([]byte, bool) {
	if len(r.buf) < n {
		return nil, false
	}
	out := r.buf[:n]
	r.buf = r.buf[n:]
	return out, true
}

func (r *reader) uvarint() (uint64, bool) {
	v, used := binary.Uvarint(r.buf)
	if used <= 0 {
		return 0, false
	}
	r.buf = r.buf[used:]
	return v, true
}

func (r *reader) id() (ID, bool) {
	site, ok1 := r.uvarint()
	seq, ok2 := r.uvarint()
	if !ok1 || !ok2 {
		return ID{}, false
	}
	return ID{Site: SiteID(site), Seq: seq}, true
}

// character decodes one entry, rejecting field combinations that cannot describe
// a real character before anything is built from them.
func (r *reader) character() (character, bool) {
	id, ok := r.id()
	if !ok {
		return character{}, false
	}
	clock, ok := r.uvarint()
	if !ok {
		return character{}, false
	}
	origin, ok := r.id()
	if !ok {
		return character{}, false
	}
	ch, ok := r.uvarint()
	if !ok || ch > utf8.MaxRune || (ch >= 0xD800 && ch <= 0xDFFF) {
		return character{}, false
	}
	delID, ok := r.id()
	if !ok || clock < id.Seq || !origin.wellFormed() || !delID.wellFormed() {
		return character{}, false
	}
	return character{id: id, clock: clock, origin: origin, ch: rune(ch), delID: delID}, true
}

// sortIDs orders IDs in place by site then sequence, keeping every derived
// encoding deterministic. The lists are short — insertion sort avoids pulling in
// a comparison closure for a handful of elements.
func sortIDs(ids []ID) {
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && idLess(ids[j], ids[j-1]); j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
}

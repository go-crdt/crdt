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
	for it := d.head.next; it != nil; it = it.next {
		out = binary.AppendUvarint(out, uint64(it.id.Site))
		out = binary.AppendUvarint(out, it.id.Seq)
		out = binary.AppendUvarint(out, it.clock)
		out = binary.AppendUvarint(out, uint64(it.origin.id.Site))
		out = binary.AppendUvarint(out, it.origin.id.Seq)
		out = binary.AppendUvarint(out, uint64(it.ch))
		out = binary.AppendUvarint(out, uint64(it.delID.Site))
		out = binary.AppendUvarint(out, it.delID.Seq)
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
	last := d.head
	for range nItems {
		it, ok := r.item()
		if !ok {
			return nil, ErrMalformed
		}
		if err := d.link(last, it, ledger); err != nil {
			return nil, err
		}
		last = it
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
		// deleted, and the item keeps the lower of the two operations. Anything
		// else describes a document no replica could reach: a deletion of a
		// character still visible would take effect on replay and diverge.
		it, known := d.byID[target]
		if !known || target.IsRoot() || it.alive() || !idLess(it.delID, delID) {
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

// link appends a decoded item to the document, checking that it is consistent
// with what came before: an identity and a deletion the ledger accepts, and an
// origin that already exists.
func (d *Doc) link(last, it *item, l *ledger) error {
	if !l.claim(it.id) {
		return ErrMalformed
	}
	origin, known := d.byID[it.origin.id]
	if !known {
		return ErrMalformed
	}
	if !it.delID.IsRoot() && !l.claim(it.delID) {
		return ErrMalformed
	}
	// The order a snapshot states is not a matter of choice: integration
	// determines exactly where a character goes, so an order no replica could
	// have produced is corrupt input. Walking from the origin to the end of what
	// has been decoded so far repeats the scan [Doc.integrate] would have made —
	// everything the character was placed after has to sort after it.
	for at := origin.next; at != nil; at = at.next {
		if !before(it.clock, it.id.Site, at.clock, at.id.Site) {
			return ErrMalformed
		}
		if at == last {
			break
		}
	}
	it.origin = origin
	last.next = it
	d.byID[it.id] = it
	d.total++
	if it.alive() {
		d.visible++
	}
	if it.clock > d.clock {
		d.clock = it.clock
	}
	return nil
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

// item decodes one character. The origin is left as a bare ID for [Doc.link] to
// resolve against the items already decoded.
func (r *reader) item() (*item, bool) {
	id, ok := r.id()
	if !ok {
		return nil, false
	}
	clock, ok := r.uvarint()
	if !ok {
		return nil, false
	}
	origin, ok := r.id()
	if !ok {
		return nil, false
	}
	ch, ok := r.uvarint()
	if !ok || ch > utf8.MaxRune || (ch >= 0xD800 && ch <= 0xDFFF) {
		return nil, false
	}
	delID, ok := r.id()
	if !ok || clock < id.Seq || !origin.wellFormed() || !delID.wellFormed() {
		return nil, false
	}
	return &item{id: id, origin: &item{id: origin}, clock: clock, ch: rune(ch), delID: delID}, true
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

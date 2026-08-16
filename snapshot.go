package crdt

import (
	"encoding/binary"
	"unicode/utf8"
)

// snapshotMagic prefixes every snapshot, followed by a one-byte format version,
// so a decoder rejects foreign or future bytes instead of misreading them.
var snapshotMagic = [...]byte{'c', 'r', 'd', 't'}

// snapshotVersion 2 writes runs; version 1 wrote one record per character and is
// still read, so a document stored by an older build still opens.
const (
	snapshotVersion   = 2
	snapshotVersionV1 = 1
)

// Snapshot encodes the whole document — every character, alive or tombstoned,
// in document order, plus the version vector. It is what a server sends a
// client joining an existing session, and what it persists.
//
// Characters are written in runs: one header for a stretch one site typed
// consecutively, then its text, then the stretches of it that have been
// deleted. Writing one record per character instead, as version 1 did, cost
// twenty-five bytes for every character of a real document — measured against
// other implementations, between eight and twenty-four times what they need.
//
// The runs written are maximal, whatever boundaries the document happens to
// hold in memory. That is what keeps the encoding canonical: two replicas that
// have applied the same operations produce identical bytes even if the
// operations arrived in different orders, so a snapshot doubles as a convergence
// check. It also keeps the format independent of the layout a replica stores,
// which is what let that layout change twice without a flag day.
//
// The full history is recoverable from a snapshot: [Doc.OpsSince] on a loaded
// document returns the same operations it would have on the original.
func (d *Doc) Snapshot() []byte {
	out := make([]byte, 0, 5+2*d.total)
	out = append(out, snapshotMagic[:]...)
	out = append(out, snapshotVersion)

	sites := d.vv.sites()
	out = binary.AppendUvarint(out, uint64(len(sites)))
	for _, site := range sites {
		out = binary.AppendUvarint(out, uint64(site))
		out = binary.AppendUvarint(out, d.vv[site])
	}

	runs := d.runs()
	out = binary.AppendUvarint(out, uint64(len(runs)))
	for _, r := range runs {
		out = binary.AppendUvarint(out, uint64(r.id.Site))
		out = binary.AppendUvarint(out, r.id.Seq)
		out = binary.AppendUvarint(out, r.clock)
		out = binary.AppendUvarint(out, uint64(r.origin.Site))
		out = binary.AppendUvarint(out, r.origin.Seq)
		out = binary.AppendUvarint(out, uint64(len(r.text)))
		for _, ch := range r.text {
			out = binary.AppendUvarint(out, uint64(ch))
		}
		out = binary.AppendUvarint(out, uint64(len(r.dels)))
		at := uint32(0)
		for _, del := range r.dels {
			// Offsets ascend and never overlap, so each is written as the gap
			// since the end of the one before it.
			out = binary.AppendUvarint(out, uint64(del.from-at))
			out = binary.AppendUvarint(out, uint64(del.to-del.from))
			out = binary.AppendUvarint(out, uint64(del.id.Site))
			out = binary.AppendUvarint(out, del.id.Seq)
			at = del.to
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

// A run is a stretch of characters one site typed consecutively, as the snapshot
// writes it.
type run struct {
	id     ID
	clock  uint64
	origin ID
	text   []rune
	dels   []delRange
}

// appendDel adds a deletion record, joining it to the one before when they touch
// and its identities continue.
//
// This is what makes the encoding canonical. The blocks themselves already are:
// two blocks that continue one another are never adjacent, because a character
// bridging them would need the sequence number the right-hand one already holds.
// Their deletion records are not — cutting a block divides a record, and
// replacing one character's deletion when two replicas delete it at once cuts
// another — so two replicas can hold the same deletions in differently shaped
// records. Writing them joined hides that difference, which is the difference
// between a snapshot that can be compared and one that cannot.
func appendDel(dels []delRange, r delRange) []delRange {
	if n := len(dels); n > 0 {
		if last := &dels[n-1]; last.to == r.from && last.continuesTo(r.from) == r.id {
			last.to = r.to
			return dels
		}
	}
	return append(dels, r)
}

// runs returns the document as the snapshot writes it: one run per block, with
// deletion records joined.
func (d *Doc) runs() []run {
	var out []run
	for b := d.head.next; b != nil; b = b.next {
		fresh := run{id: b.id, clock: b.clock, origin: b.originID, text: b.text}
		for _, del := range b.dels {
			fresh.dels = appendDel(fresh.dels, del)
		}
		out = append(out, fresh)
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
	v, ok := r.bytes(1)
	if !ok || (v[0] != snapshotVersion && v[0] != snapshotVersionV1) {
		return nil, ErrMalformed
	}
	version := v[0]

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

	count, ok := r.uvarint()
	if !ok || count > uint64(len(r.buf)) {
		return nil, ErrMalformed
	}
	for range count {
		var err error
		if version == snapshotVersionV1 {
			err = d.readCharacter(r, ledger)
		} else {
			err = d.readRun(r, ledger)
		}
		if err != nil {
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
		if !known || b.aliveAt(i) || !idLess(b.delIDAt(i), delID) {
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

// readCharacter decodes one version 1 entry, which described a single character.
func (d *Doc) readCharacter(r *reader, l *ledger) error {
	c, ok := r.character()
	if !ok {
		return ErrMalformed
	}
	return d.adopt(c, l)
}

// readRun decodes one version 2 entry and adopts its characters one by one, so
// that a run gets exactly the checks a character-at-a-time snapshot got: the
// shorter encoding buys space, not trust.
func (d *Doc) readRun(r *reader, l *ledger) error {
	id, ok1 := r.id()
	clock, ok2 := r.uvarint()
	origin, ok3 := r.id()
	length, ok4 := r.uvarint()
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return ErrMalformed
	}
	// A run of no characters says nothing and would let a snapshot claim any
	// number of them; each character costs at least a byte still to be read.
	if length == 0 || length > uint64(len(r.buf)) || clock < id.Seq ||
		!origin.wellFormed() || !id.wellFormed() {
		return ErrMalformed
	}
	text := make([]rune, length)
	for i := range text {
		ch, ok := r.uvarint()
		if !ok || ch > utf8.MaxRune || (ch >= 0xD800 && ch <= 0xDFFF) {
			return ErrMalformed
		}
		text[i] = rune(ch)
	}

	// The deleted stretches, as gaps and lengths. They must ascend, not overlap
	// and stay inside the run, or the characters they name would be the wrong
	// ones.
	nDels, ok := r.uvarint()
	if !ok || nDels > uint64(len(r.buf)) {
		return ErrMalformed
	}
	dels := make([]delRange, 0, nDels)
	at := uint64(0)
	for range nDels {
		gap, ok1 := r.uvarint()
		span, ok2 := r.uvarint()
		delID, ok3 := r.id()
		if !ok1 || !ok2 || !ok3 || span == 0 || gap > length ||
			!delID.wellFormed() || delID.IsRoot() {
			return ErrMalformed
		}
		from := at + gap
		if from+span > length {
			return ErrMalformed
		}
		dels = append(dels, delRange{from: uint32(from), to: uint32(from + span), id: delID})
		at = from + span
	}

	cursor := 0
	for i, ch := range text {
		c := character{
			id:     ID{Site: id.Site, Seq: id.Seq + uint64(i)},
			clock:  clock + uint64(i),
			origin: origin,
			ch:     ch,
		}
		if i > 0 {
			c.origin = ID{Site: id.Site, Seq: id.Seq + uint64(i) - 1}
		}
		for cursor < len(dels) && int(dels[cursor].to) <= i {
			cursor++
		}
		if cursor < len(dels) && dels[cursor].holds(i) {
			c.delID = dels[cursor].continuesTo(uint32(i))
		}
		if err := d.adopt(c, l); err != nil {
			return err
		}
	}
	return nil
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
		d.sup += int(supUnit(c.ch))
	} else {
		// place counted the character as visible, as it is when it arrives; the
		// snapshot says it was deleted before this replica ever saw it.
		b.markDeleted(i, c.delID)
		d.addVis(b, -1, -supUnit(c.ch))
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

package crdt

import (
	"encoding/binary"
	"unicode/utf8"
)

// snapshotMagic prefixes every snapshot, followed by a one-byte format version,
// so a decoder rejects foreign or future bytes instead of misreading them.
var snapshotMagic = [...]byte{'c', 'r', 'd', 't'}

// snapshotVersion 5 writes the runs in columns — every site, then every
// sequence number, then every clock, and so on — rather than one run at a time.
// It is the same fields in the same encodings; only the order changes, and it
// changes because a compressor looking for repetition in version 4 sees a run's
// identity beside its text beside its deletions. A column is a stream of
// similar numbers, which is what a compressor is good at: the same document
// goes from 148 KB compressed to 97, against diamond-types' 109. The measurement
// and the choice of compressor are in docs/performance.md; nothing here
// compresses, because Snapshot promises the same state is the same bytes and a
// compressor is deterministic for a build rather than across versions of
// itself. Version 4 writes a run's header as steps: its own sequence number
// and its origin's from the last the same site used, and its clock as the
// distance above its own sequence, which it can never fall below. Version 3
// writes a deletion's sequence number as a signed step from the previous
// deletion by the same site; version 2 wrote it in full; version 1 wrote one
// record per character. All four are readable; version 1's note
// still read, so a document stored by an older build still opens.
const (
	snapshotVersion   = 6
	snapshotVersionV5 = 5
	snapshotVersionV4 = 4
	snapshotVersionV3 = 3
	snapshotVersionV2 = 2
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

	// Version 6 carries two tables here, both of them always empty: they were
	// written for a collection that was withdrawn because it left two replicas
	// holding different documents. Reading refuses a snapshot whose tables are
	// not empty, because nothing sound could have produced one.
	out = binary.AppendUvarint(out, 0)
	out = binary.AppendUvarint(out, 0)

	runs := d.runs()
	out = binary.AppendUvarint(out, uint64(len(runs)))

	// The columns. Same fields as version 4, same step encodings; what changes
	// is that each is written all together instead of interleaved with the
	// others, so that a compressor sees a stream of similar numbers rather than
	// a run's identity beside its text beside its deletions.
	//
	// A step is still measured against the last value the same site used in the
	// same position, which is unchanged by the reordering: the runs are walked
	// in document order here exactly as they were.
	var (
		runSites  []byte // the site of each run
		seqs      []byte // its sequence number, as a step
		clocks    []byte // its clock, as the distance above that sequence number
		oSites    []byte // its origin's site
		oSeqs     []byte // its origin's sequence number, as a step
		lengths   []byte // how many characters it holds
		text      []byte // the characters themselves, every run's after the last
		delCounts []byte // how many deleted stretches each run has
		delFields []byte // those stretches: gap, span, site, sequence step
	)
	lastDelSeq := map[SiteID]uint64{}
	lastRunSeq := map[SiteID]uint64{}
	lastOriginSeq := map[SiteID]uint64{}
	for _, r := range runs {
		runSites = binary.AppendUvarint(runSites, uint64(r.id.Site))
		seqs = binary.AppendUvarint(seqs, zigzag(int64(r.id.Seq)-int64(lastRunSeq[r.id.Site])))
		lastRunSeq[r.id.Site] = r.id.Seq
		clocks = binary.AppendUvarint(clocks, r.clock-r.id.Seq)
		oSites = binary.AppendUvarint(oSites, uint64(r.origin.Site))
		oSeqs = binary.AppendUvarint(oSeqs, zigzag(int64(r.origin.Seq)-int64(lastOriginSeq[r.origin.Site])))
		lastOriginSeq[r.origin.Site] = r.origin.Seq
		lengths = binary.AppendUvarint(lengths, uint64(len(r.text)))
		for _, ch := range r.text {
			text = binary.AppendUvarint(text, uint64(ch))
		}
		delCounts = binary.AppendUvarint(delCounts, uint64(len(r.dels)))
		at := uint32(0)
		for _, del := range r.dels {
			delFields = binary.AppendUvarint(delFields, uint64(del.from-at))
			delFields = binary.AppendUvarint(delFields, uint64(del.to-del.from))
			delFields = binary.AppendUvarint(delFields, uint64(del.id.Site))
			delFields = binary.AppendUvarint(delFields, zigzag(int64(del.id.Seq)-int64(lastDelSeq[del.id.Site])))
			lastDelSeq[del.id.Site] = del.id.Seq
			at = del.to
		}
	}

	// Each column is length-prefixed, so a reader can take them apart without
	// knowing what is in them — which is also what lets whoever stores this
	// compress them one at a time, which measured better than compressing the
	// whole thing at once.
	for _, col := range [][]byte{runSites, seqs, clocks, oSites, oSeqs, lengths, text, delCounts, delFields} {
		out = binary.AppendUvarint(out, uint64(len(col)))
		out = append(out, col...)
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

// zigzag maps a signed step onto an unsigned one so that a small step backwards
// costs as little as a small step forwards. A deletion's sequence number is
// written against the last one its site used, and a person deleting text moves
// both ways through it.
func zigzag(v int64) uint64 {
	return uint64(v<<1) ^ uint64(v>>63)
}

// unzigzag is its inverse. A step is only ever read back, never trusted: what it
// resolves to is checked against the version vector like any other identity.
func unzigzag(v uint64) int64 {
	return int64(v>>1) ^ -int64(v&1)
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
	if !ok || (v[0] != snapshotVersion && v[0] != snapshotVersionV5 && v[0] != snapshotVersionV4 &&
		v[0] != snapshotVersionV3 && v[0] != snapshotVersionV2 && v[0] != snapshotVersionV1) {
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
		// A sequence number above the clock ceiling names an operation no replica
		// could have issued; see [MaxClock].
		if !ok1 || !ok2 || seq == 0 || seq > MaxClock {
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

	if version >= snapshotVersion {
		if err := readCollected(r); err != nil {
			return nil, err
		}
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
	// The step a version 3 deletion is written against: the last sequence number
	// this site deleted with, carried across runs because the encoder carries it
	// the same way. Per site, so that a document with several authors does not
	// pay a full-width jump every time the writer changes.
	lastDelSeq := map[SiteID]uint64{}
	// The run header goes the same way, and for the same reason: a run's own
	// sequence number and its origin's both climb, so the step is small where
	// the number is large. Measured on the automerge-paper trace: identities
	// 42 444 bytes to 31 273, origins 42 382 to 25 484.
	lastRunSeq := map[SiteID]uint64{}
	lastOriginSeq := map[SiteID]uint64{}

	// Where each field is read from. Version 5 gives every field a column of
	// its own; every version before it interleaved them, which is the same
	// thing with all nine readers pointing at the one stream — the fields go
	// past in the same order either way, so there is one decoder rather than
	// two, and the older format is exercised by every test the newer one is.
	cols := sameStream(r)
	if version >= snapshotVersionV5 {
		var err error
		if cols, err = readColumns(r); err != nil {
			return nil, err
		}
	}
	for range count {
		var err error
		if version == snapshotVersionV1 {
			err = d.readCharacter(r, ledger)
		} else {
			err = d.readRun(cols, ledger, version, lastDelSeq, lastRunSeq, lastOriginSeq)
		}
		if err != nil {
			return nil, err
		}
	}
	if version >= snapshotVersionV5 {
		// Every column must have been consumed exactly. A column with bytes
		// left over describes runs the count did not claim, which is a second
		// encoding of the same document and so not one this format allows.
		if !cols.empty() {
			return nil, ErrMalformed
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
func (d *Doc) readRun(c *columns, l *ledger, version byte, lastDelSeq, lastRunSeq, lastOriginSeq map[SiteID]uint64) error {
	id, ok1 := steppedID(c.sites, c.seqs, version, lastRunSeq)
	clock, ok2 := c.clocks.uvarint()
	origin, ok3 := steppedID(c.oSites, c.oSeqs, version, lastOriginSeq)
	length, ok4 := c.lengths.uvarint()
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return ErrMalformed
	}
	if version >= snapshotVersionV4 {
		// The clock arrives as the distance above this run's own sequence
		// number, so it is added back here and held to the ceiling below like
		// any other clock.
		//
		// Nothing is checked before the addition, and it is worth writing down
		// why, because a guard here looks prudent. The addition can overflow,
		// but an overflow cannot pass: it means distance + sequence ≥ 2^64, so
		// the sum lands at distance + sequence − 2^64, which is below the
		// sequence unless the distance is itself 2^64 or more — and it cannot
		// be, because it arrived in sixty-four bits. Every overflow therefore
		// produces a clock below its own sequence number, which is refused.
		//
		// Nor is there a second encoding to worry about: for a given sequence
		// and clock the distance between them is one number, so the canonicity
		// the padded-varint refusal protects is not at stake. I wrote the guard
		// first, then tried to write the test that needed it, and the test could
		// not be written.
		clock += id.Seq
	}
	// A run of no characters says nothing and would let a snapshot claim any
	// number of them; each character costs at least a byte still to be read.
	if length == 0 || length > uint64(len(c.text.buf)) || clock > MaxClock ||
		clock < id.Seq || !origin.wellFormed() || !id.wellFormed() {
		return ErrMalformed
	}
	text := make([]rune, length)
	for i := range text {
		ch, ok := c.text.uvarint()
		if !ok || ch > utf8.MaxRune || (ch >= 0xD800 && ch <= 0xDFFF) {
			return ErrMalformed
		}
		text[i] = rune(ch)
	}

	// The deleted stretches, as gaps and lengths. They must ascend, not overlap
	// and stay inside the run, or the characters they name would be the wrong
	// ones.
	nDels, ok := c.delCounts.uvarint()
	if !ok || nDels > uint64(len(c.delFld.buf)) {
		return ErrMalformed
	}
	dels := make([]delRange, 0, nDels)
	at := uint64(0)
	for range nDels {
		gap, ok1 := c.delFld.uvarint()
		span, ok2 := c.delFld.uvarint()
		delID, ok3 := c.delFld.delID(version, lastDelSeq)
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
	v, used := uvarint(r.buf)
	if used <= 0 {
		return 0, false
	}
	r.buf = r.buf[used:]
	return v, true
}

// uvarint decodes an unsigned varint and refuses an encoding longer than the
// value needs, which [binary.Uvarint] on its own accepts.
//
// Every encoding in this package is canonical: the same state is the same bytes.
// That is what lets the test suite compare snapshots rather than values, which
// is the stronger claim, and what lets a caller store or compare them. The claim
// is only as good as the layer underneath it, and without this check it was not
// true of anything arriving from outside — a peer could write 1 as {0x81, 0x00}
// and hand over a snapshot that decodes to a document already held and yet does
// not match its bytes.
//
// A varint's last byte carries the value's highest bits, so a zero there says
// those bits are all zero and the encoding is longer than the value needs.
// [binary.AppendUvarint] never emits that, except for zero itself, which is one
// byte — so nothing this package writes can be refused here.
func uvarint(buf []byte) (uint64, int) {
	v, used := binary.Uvarint(buf)
	if used <= 0 {
		return 0, 0
	}
	if used > 1 && buf[used-1] == 0 {
		return 0, 0
	}
	return v, used
}

// steppedID reads an identity whose sequence number version 4 writes as a signed
// step from the last one that site used in the same position — a run's own, or a
// run's origin. Each position keeps its own running value, which is why the map
// is a parameter rather than a field.
//
// The root origin is site 0, sequence 0, and it is common; a step of zero from a
// running value of zero is one byte, which is what it was before.
func steppedID(siteCol, seqCol *reader, version byte, last map[SiteID]uint64) (ID, bool) {
	if version < snapshotVersionV4 {
		// Before version 4 the two came from one stream, one after the other,
		// which is what id already reads. Spelling it out again here would be
		// the same code twice, and the copy would be the one no test reaches.
		return siteCol.id()
	}
	site, ok1 := siteCol.uvarint()
	step, ok2 := seqCol.uvarint()
	if !ok1 || !ok2 {
		return ID{}, false
	}
	at := SiteID(site)
	seq := uint64(int64(last[at]) + unzigzag(step))
	last[at] = seq
	return ID{Site: at, Seq: seq}, true
}

// delID reads a deletion's identity, which version 3 writes as a signed step
// from the last sequence number that site deleted with.
//
// A step is signed, so it can land where no operation is: at zero, or below it,
// or past the clock ceiling. Nothing extra is checked here for that, and the
// reason is worth writing down because the opposite looks prudent. A landing of
// zero is refused by the identity check the caller already makes; any other bad
// landing produces an identity the version vector never promised, and a snapshot
// that does not account for exactly the operations its version vector claims is
// refused whole. Adding a range check here as well caught nothing the tests
// could distinguish — removing it left every rejection still rejected — so it
// would have been a line that looked like a safeguard and was not.
//
// The running value is left poisoned on a bad step, which costs nothing:
// decoding stops at the first refusal.
func (r *reader) delID(version byte, last map[SiteID]uint64) (ID, bool) {
	if version < snapshotVersionV3 {
		return r.id()
	}
	site, ok1 := r.uvarint()
	step, ok2 := r.uvarint()
	if !ok1 || !ok2 {
		return ID{}, false
	}
	at := SiteID(site)
	seq := uint64(int64(last[at]) + unzigzag(step))
	last[at] = seq
	return ID{Site: at, Seq: seq}, true
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
	if !ok || clock > MaxClock || clock < id.Seq ||
		!origin.wellFormed() || !delID.wellFormed() {
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

// columns is where each field of a run is read from. Version 5 gives every one
// its own stream; earlier versions interleave them, which is this with all nine
// pointing at the same reader.
type columns struct {
	sites, seqs, clocks     *reader
	oSites, oSeqs, lengths  *reader
	text, delCounts, delFld *reader
}

// sameStream is how every version before 5 is read: the fields go past in the
// order the columns are listed in, so one reader serves all of them.
func sameStream(r *reader) *columns {
	return &columns{sites: r, seqs: r, clocks: r, oSites: r, oSeqs: r,
		lengths: r, text: r, delCounts: r, delFld: r}
}

// readColumns takes apart the nine length-prefixed streams a version 5 snapshot
// writes. A length past the end of what is left is refused here rather than
// discovered field by field later.
func readColumns(r *reader) (*columns, error) {
	c := &columns{}
	for _, into := range []**reader{&c.sites, &c.seqs, &c.clocks, &c.oSites,
		&c.oSeqs, &c.lengths, &c.text, &c.delCounts, &c.delFld} {
		n, ok := r.uvarint()
		if !ok || n > uint64(len(r.buf)) {
			return nil, ErrMalformed
		}
		// The length was just held to what is left, so this cannot come up
		// short. Checking it again would be a line that looks like a guard and
		// never runs — the coverage says so, and removing it changed nothing.
		buf, _ := r.bytes(int(n))
		*into = &reader{buf: buf}
	}
	return c, nil
}

// empty reports whether every column was consumed to its end.
func (c *columns) empty() bool {
	for _, r := range []*reader{c.sites, c.seqs, c.clocks, c.oSites, c.oSeqs,
		c.lengths, c.text, c.delCounts, c.delFld} {
		if len(r.buf) != 0 {
			return false
		}
	}
	return true
}

// readCollected reads what version 6 added: the floor this replica can still be
// read back to, and how many of each site's operations collection took away.
//
// Loading is a trust boundary here as everywhere else in this file. A floor
// naming operations the document has not seen, or a tally claiming more of a
// site's operations than that site ever issued, describes no document a replica
// could have produced, and is refused rather than believed.
// readCollected reads the two tables version 6 of a text and version 2 of a
// list carry, and refuses a snapshot whose tables are not empty.
//
// They were written for a collection that has been withdrawn, so nothing that
// could be trusted has ever filled them. A snapshot that has them filled came
// from a replica that collected the way that turned out to leave two replicas
// holding different documents, and reading it back would carry that
// disagreement in rather than leave it outside.
func readCollected(r *reader) error {
	for range 2 {
		n, ok := r.uvarint()
		if !ok || n != 0 {
			return ErrMalformed
		}
	}
	return nil
}

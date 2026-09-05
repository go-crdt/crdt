package crdt

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"io"
	"math"
	"slices"
	"sort"
	"unicode/utf8"
)

// snapshotMagic prefixes every snapshot, followed by a one-byte format version,
// so a decoder rejects foreign or future bytes instead of misreading them.
var snapshotMagic = [...]byte{'c', 'r', 'd', 't'}

// snapshotVersion 9 adds the purge: a floor in the header saying the clock
// below which this replica has discarded characters, and a column saying which
// runs they were discarded from. It is written only by a document that has
// actually purged — see [Doc.formatVersion] — so a document that has not costs
// exactly what version 8 costs, to the byte.
//
// Version 8 gives every column an encoding of its own rather than one
// uvarint per value, and splits the deletion fields into four columns so that
// each is a stream of one kind of number. The version before it spent 15% of a
// real document on three columns holding a single repeated value — 10 824
// clocks all zero, 10 824 run sites and 50 276 deletion sites all the same
// author — because a column had no way to say "this number, that many times".
// It now does: a column is a sequence of groups, either a run of one value or a
// literal stretch, which is the run-length encoding Automerge and diamond-types
// both settled on. On the automerge-paper trace that is 478 474 bytes to
// 259 890, and the three columns above go from 71 924 bytes to twelve. Nothing
// here compresses, still: a column says in its first byte how it is stored and
// the reader understands a deflated one, but nothing writes one, because
// Snapshot promises the same state is the same bytes and a compressor is
// deterministic for a build rather than across versions of itself. What that
// costs is measured in [readColumnStream].
//
// Versions 1 to 6 were read until #99. They were the encodings this format
// arrived at — one record per character, then runs, then stepped identities,
// then columns — and nothing holds them: there are no production servers, the
// project is in development, and a reader kept for bytes nobody has is a reader
// nobody tests against anything real. They are refused as unknown now, which is
// what a peer needs to hear, and [Reads] says so.
//
// Version 7 is refused for a different reason, and it is the one number here
// nothing here has ever written. It was reserved for the purge while version 8
// was in flight, because two branches each calling the next number theirs is how
// one version byte comes to mean two things, which is the failure this numbering
// exists to prevent. Version 8 landed first, so the purge took 9 and 7 was left
// standing. It stays refused rather than recycled: a build off to one side did
// write the purge's earlier shape under it, and those bytes are not these.
// Reusing the number would mean reading them as this format and believing the
// answer.
const (
	snapshotVersion   = 9
	snapshotVersionV8 = 8
)

// textFormats is every version of a text snapshot this build reads, ascending.
//
// One list, read by [Load] and by [Reads]: a peer is told what the loader does
// rather than what a second list says it does, and a version added to one
// without the other is not a way this can go wrong.
//
// Both are here, and they are not a highest and its predecessor kept for old
// bytes: this build writes either one, choosing per document in
// [Doc.formatVersion], so 8 is a version it still produces. A reader that
// dropped it would refuse the snapshots of every document that has purged
// nothing, which is most of them.
var textFormats = []byte{snapshotVersionV8, snapshotVersion}

// knownFormat reports whether v is one of them.
func knownFormat(versions []byte, v byte) bool {
	for _, known := range versions {
		if known == v {
			return true
		}
	}
	return false
}

// runThreshold is how many of the same value in a row it takes to be worth
// writing as a run rather than leaving in the literal stretch around it.
//
// It is measured, not chosen. A run costs a count and a value; leaving a pair
// in the literals costs the two values, and also costs the two group headers
// that cutting the literal stretch in half needs. So a pair is a loss and a
// triple is a small win, which is what the trace says: over the twelve columns
// of automerge-paper, a threshold of two costs 266 270 bytes, three costs
// 259 840, four costs 259 808 and six costs 261 016. Three is where it turns,
// and it is the smallest number that never makes a column bigger than writing
// it out plainly would have.
const runThreshold = 3

// A column's payload begins with one byte saying how the groups that follow are
// stored. Only [columnGroups] is ever written; [columnDeflated] is understood so
// that a peer or a later release can send it without this one refusing the whole
// snapshot. See the note on writing it in [readColumnStream].
const (
	columnGroups   = 0
	columnDeflated = 1
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
	version := d.formatVersion()
	out = append(out, version)

	sites := d.vv.sites()
	out = binary.AppendUvarint(out, uint64(len(sites)))
	for _, site := range sites {
		out = binary.AppendUvarint(out, uint64(site))
		out = binary.AppendUvarint(out, d.vv[site])
	}

	// Two tables, both of them always empty: they were written for a collection
	// that was withdrawn because it left two replicas holding different
	// documents. Reading refuses a snapshot whose tables are not empty, because
	// nothing sound could have produced one.
	out = binary.AppendUvarint(out, 0)
	out = binary.AppendUvarint(out, 0)

	// Version 9: the clock below which characters have been discarded. A
	// document that reloads has to remember what it gave up, or it would answer
	// that it can serve a peer whose history it no longer holds. Kept for the
	// same reason a map keeps its collection floor, and learnt the same way: the
	// floor a replica does not write down is a floor it does not have.
	//
	// Written only by a document that has purged; see formatVersion.
	if version == snapshotVersion {
		out = binary.AppendUvarint(out, d.purgedBelow)
	}

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
	//
	// Version 8 splits what version 5 wrote as one column of deletion fields
	// into four, one per field. Interleaved, gap beside span beside site beside
	// sequence step, no two neighbours are the same kind of number and nothing
	// repeats; apart, the sites are 50 276 copies of one number and cost four
	// bytes, and the gaps 50 286 bytes become 1 483.
	var (
		runSites  rleWriter // the site of each run
		seqs      rleWriter // its sequence number, as a step
		clocks    rleWriter // its clock, as the distance above that sequence number
		oSites    rleWriter // its origin's site
		oSeqs     rleWriter // its origin's sequence number, as a step
		lengths   rleWriter // how many characters it holds
		text      rleWriter // the characters themselves, every run's after the last
		delCounts rleWriter // how many deleted stretches each run has
		delGaps   rleWriter // where each stretch starts, from the end of the last
		delSpans  rleWriter // how many characters it covers
		delSites  rleWriter // who deleted them
		delSeqs   rleWriter // with which sequence number, as a step
		purged    rleWriter // whether each run's characters were discarded
	)
	lastDelSeq := map[SiteID]uint64{}
	lastRunSeq := map[SiteID]uint64{}
	lastOriginSeq := map[SiteID]uint64{}
	for _, r := range runs {
		runSites.add(uint64(r.id.Site))
		seqs.add(zigzag(int64(r.id.Seq) - int64(lastRunSeq[r.id.Site])))
		lastRunSeq[r.id.Site] = r.id.Seq
		clocks.add(r.clock - r.id.Seq)
		oSites.add(uint64(r.origin.Site))
		oSeqs.add(zigzag(int64(r.origin.Seq) - int64(lastOriginSeq[r.origin.Site])))
		lastOriginSeq[r.origin.Site] = r.origin.Seq
		lengths.add(r.size())
		if version == snapshotVersion {
			purged.add(boolByte(r.gone))
		}
		for _, ch := range r.text {
			text.add(uint64(ch))
		}
		delCounts.add(uint64(len(r.dels)))
		at := uint32(0)
		for _, del := range r.dels {
			delGaps.add(uint64(del.from - at))
			delSpans.add(uint64(del.to - del.from))
			delSites.add(uint64(del.id.Site))
			delSeqs.add(zigzag(int64(del.id.Seq) - int64(lastDelSeq[del.id.Site])))
			lastDelSeq[del.id.Site] = del.id.Seq
			at = del.to
		}
	}

	// Each column is length-prefixed and says in its first byte how it is
	// stored, so a reader can take them apart without knowing what is in them —
	// which is also what lets whoever stores this compress them one at a time,
	// which measured better than compressing the whole thing at once.
	cols := []*rleWriter{&runSites, &seqs, &clocks, &oSites, &oSeqs,
		&lengths, &text, &delCounts, &delGaps, &delSpans, &delSites, &delSeqs}
	if version == snapshotVersion {
		cols = append(cols, &purged)
	}
	for _, w := range cols {
		col := w.finish()
		out = binary.AppendUvarint(out, uint64(len(col)+1))
		out = append(out, columnGroups)
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

// formatVersion is the oldest format this document can be written in, which is
// the current one only when it has something to say that the current one added.
//
// # Understand before write
//
// A version this build writes is a version every peer and every store that will
// read it has to understand already, and the two ends of a session are not
// deployed at the same moment. This package has learnt that twice: #83 taught a
// text and a list to understand a superseded run in one release so that a later
// one could send it, and go-crdt/collab#98 added a required field to the wire
// and broke three transports that had not been rebuilt, at the cost of a
// retract.
//
// So version 9 — the purge floor in the header, the flag per run in the
// columns — is understood by [Load] from this release, and written by nobody
// who has not called [Doc.Purge]. A document that never purges keeps writing
// version 8, which every build since v0.38.0 reads. There is no flag day and no
// release to wait for: the format moves for a document at the moment its owner
// asks for something only the new format can say, which is the strongest form
// of understand-first available to a snapshot, because a purge cannot be
// expressed in version 8 at all.
//
// The floor is the whole condition. A purged run without one is refused by
// [Load] — [Doc.Purge] always sets it — so a document with the floor at zero has
// no purged run for version 8 to lose.
//
// It costs nothing to keep the two apart, and it now costs almost nothing to
// stop. Under version 6's encoding the flag was a uvarint per run, so on the
// automerge-paper trace it was 10 824 bytes on a document that had purged
// nothing — 2.26% of a snapshot for a feature it had not used, which is what
// made the conditional bump worth its complication in the first place. Version
// 8's groups collapse a column holding one repeated value to a count and a
// value, so the same all-nought column is four bytes of groups, seven with its
// encoding byte, its length prefix and the floor: 0.003%.
//
// So the bytes no longer argue for this, and it is kept anyway, on the
// compatibility argument above alone. That is worth saying out loud, because the
// measurement that justified it has evaporated and the reason it was built has
// not: a document that has not purged still writes the version every build since
// v0.38.0 reads, and the seven bytes were never the point.
func (d *Doc) formatVersion() byte {
	if d.purgedBelow > 0 {
		return snapshotVersion
	}
	return snapshotVersionV8
}

// boolByte is the one value a flag column spends.
func boolByte(b bool) uint64 {
	if b {
		return 1
	}
	return 0
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

// An rleWriter builds one column: a sequence of groups, each either a run of
// one value repeated or a literal stretch of values that do not repeat enough
// to be worth a run.
//
// A group's header is a zigzagged count, so a run of n reads as 2n and a
// literal stretch of m as 2m−1: the two never collide, and both cost one byte
// while they are small. A run's count is at least [runThreshold], which is what
// makes the encoding canonical — for a given sequence of values there is exactly
// one grouping, and [column.uvarint] refuses every other. Two replicas holding
// the same document still write the same bytes, which is what the whole format
// is for.
//
// Only whole equal blocks become runs, so the writer holds one back: it does not
// know whether the block being counted is long enough until a different value
// arrives or the column ends.
type rleWriter struct {
	out  []byte // the groups already written
	lit  []byte // the values of the literal stretch being accumulated
	litN uint64 // how many of them
	val  uint64 // the value repeating right now
	n    uint64 // how many times it has repeated
	have bool
}

// add takes the next value of the column.
func (w *rleWriter) add(v uint64) {
	if w.have && v == w.val {
		w.n++
		return
	}
	w.flushBlock()
	w.val, w.n, w.have = v, 1, true
}

// flushBlock disposes of the equal block just ended: as a run if it is long
// enough, and otherwise into the literal stretch, which it therefore continues
// rather than interrupts.
func (w *rleWriter) flushBlock() {
	switch {
	case w.n == 0:
	case w.n >= runThreshold:
		w.flushLiteral()
		w.out = binary.AppendUvarint(w.out, zigzag(int64(w.n)))
		w.out = binary.AppendUvarint(w.out, w.val)
	default:
		for range w.n {
			w.lit = binary.AppendUvarint(w.lit, w.val)
		}
		w.litN += w.n
	}
	w.n = 0
}

// flushLiteral writes the literal stretch, which is always as long as it can be:
// a run only ever cuts it where the values themselves say so.
func (w *rleWriter) flushLiteral() {
	if w.litN == 0 {
		return
	}
	w.out = binary.AppendUvarint(w.out, zigzag(-int64(w.litN)))
	w.out = append(w.out, w.lit...)
	w.lit, w.litN = w.lit[:0], 0
}

// finish closes the column and returns its groups.
func (w *rleWriter) finish() []byte {
	w.flushBlock()
	w.flushLiteral()
	return w.out
}

// A run is a stretch of characters one site typed consecutively, as the snapshot
// writes it.
type run struct {
	id     ID
	clock  uint64
	origin ID
	text   []rune
	dels   []delRange
	// gone is a run whose characters were discarded by [Doc.Purge]. Its length
	// is the one the deletions describe, and the text column holds nothing for
	// it — which is the whole saving, and which a reader has to be told rather
	// than left to infer, because a run that is merely deleted looks the same
	// from every other column.
	gone bool
}

// size is how many characters this run holds, whether or not it still has them.
func (r run) size() uint64 {
	if r.gone {
		// See the note on block.size: a purged run always has deletions
		// covering it, so the last one's end is its length.
		return uint64(r.dels[len(r.dels)-1].to)
	}
	return uint64(len(r.text))
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
		fresh := run{id: b.id, clock: b.clock, origin: b.originID, text: b.text, gone: b.gone}
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
	if !ok {
		return nil, ErrMalformed
	}
	if !knownFormat(textFormats, v[0]) {
		return nil, unknownFormat(v[0], snapshotVersion)
	}
	version := v[0]

	d := New(site)
	nSites, ok := r.uvarint()
	if !ok {
		return nil, ErrMalformed
	}
	// How many operations the version vector promises, in total. Every version
	// before 8 was its own bound: a column held one uvarint per value, so a
	// snapshot of n bytes described at most n of anything. A run-length column
	// does not, and that is the point of it — twelve bytes now stand for 71 924
	// values. So the bound moves to the only other thing that says how large the
	// document is, which is the version vector at the front, and every column is
	// held to it below.
	ops := uint64(0)
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
		// The total is held to the same ceiling as one site's. A replica that
		// has seen every operation carries a clock at least as high as the
		// number of them it issued itself, so a document of more than [MaxClock]
		// operations is one no set of replicas could have produced, and reading
		// it would mean believing a size before anything has been checked.
		if seq > MaxClock-ops {
			return nil, ErrMalformed
		}
		ops += seq
		d.vv[SiteID(s)] = seq
	}

	if err := readCollected(r); err != nil {
		return nil, err
	}

	if version == snapshotVersion {
		below, ok := r.uvarint()
		// A floor above the ceiling names a clock no operation could carry, so
		// it describes a document that could not have been written.
		if !ok || below > MaxClock {
			return nil, ErrMalformed
		}
		d.purgedBelow = below
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
	// The step a deletion's sequence number is written against: the last
	// sequence number this site deleted with, carried across runs because the
	// encoder carries it the same way. Per site, so that a document with several
	// authors does not pay a full-width jump every time the writer changes.
	lastDelSeq := map[SiteID]uint64{}
	// The run header goes the same way, and for the same reason: a run's own
	// sequence number and its origin's both climb, so the step is small where
	// the number is large. Measured on the automerge-paper trace: identities
	// 42 444 bytes to 31 273, origins 42 382 to 25 484.
	lastRunSeq := map[SiteID]uint64{}
	lastOriginSeq := map[SiteID]uint64{}

	// Where each field is read from: every field has a column of its own, and
	// the four deletion columns are separate rather than one stream of gap,
	// span, site and step over and over.
	cols, err := readColumnsV8(r, ops, version == snapshotVersion)
	if err != nil {
		return nil, err
	}
	for range count {
		if err := d.readRun(cols, ledger, lastDelSeq, lastRunSeq, lastOriginSeq); err != nil {
			return nil, err
		}
	}
	// Every column must have been consumed exactly. A column with values left
	// over describes runs the count did not claim, which is a second encoding of
	// the same document and so not one this format allows.
	if !cols.empty() {
		return nil, ErrMalformed
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

// readRun decodes one run and adopts its characters one by one, so that a run
// gets exactly the checks each of its characters would get on its own: the
// shorter encoding buys space, not trust.
//
// A purged run is the exception, and it has to be: it carries no characters at
// all, only a length, so adopting them one by one means writing out a million
// characters that were never read in order to throw them away. What it costs to
// read would then be what it says rather than what it holds, and what it says
// is one uvarint away from any number the version vector will promise. So its
// identities are claimed as stretches, in one go, and only its first character
// is integrated. That is not a weaker check, and the argument is at the fold
// below: every later character of a purged run is placed by the same two
// comparisons, on inputs the run header already gave, with the same answer.
func (d *Doc) readRun(c *columns, l *ledger, lastDelSeq, lastRunSeq, lastOriginSeq map[SiteID]uint64) error {
	id, ok1 := steppedID(c.sites, c.seqs, lastRunSeq)
	clock, ok2 := c.clocks.uvarint()
	origin, ok3 := steppedID(c.oSites, c.oSeqs, lastOriginSeq)
	length, ok4 := c.lengths.uvarint()
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return ErrMalformed
	}
	gone := false
	if c.purged != nil {
		flag, ok := c.purged.uvarint()
		if !ok || flag > 1 {
			return ErrMalformed
		}
		gone = flag == 1
		// A purged run in a document whose floor is zero could not have been
		// written: [Doc.Purge] sets the floor to the highest clock it discarded
		// under, every time it discards anything. Refusing it is also what makes
		// the floor alone enough to decide the format version, so that a
		// document with nothing purged is never written in one that says it has.
		if gone && d.purgedBelow == 0 {
			return ErrMalformed
		}
	}
	// The clock arrives as the distance above this run's own sequence number, so
	// it is added back here and held to the ceiling below like any other clock.
	//
	// Nothing is checked before the addition, and it is worth writing down why,
	// because a guard here looks prudent. The addition can overflow, but an
	// overflow cannot pass: it means distance + sequence ≥ 2^64, so the sum
	// lands at distance + sequence − 2^64, which is below the sequence unless
	// the distance is itself 2^64 or more — and it cannot be, because it arrived
	// in sixty-four bits. Every overflow therefore produces a clock below its
	// own sequence number, which is refused.
	//
	// Nor is there a second encoding to worry about: for a given sequence and
	// clock the distance between them is one number, so the canonicity the
	// padded-varint refusal protects is not at stake. I wrote the guard first,
	// then tried to write the test that needed it, and the test could not be
	// written.
	clock += id.Seq
	// A run of no characters says nothing and would let a snapshot claim any
	// number of them; each character costs at least a value still to be read.
	//
	// A purged run costs nothing in the text column, so the bytes do not hold
	// it; what holds it is [ledger.claimSpan] below, and saying so twice would
	// mean two places to keep true.
	if length == 0 || (!gone && length > c.text.remaining()) || clock > MaxClock ||
		clock < id.Seq || !origin.wellFormed() || !id.wellFormed() {
		return ErrMalformed
	}
	// A purged run is integrated with one character that is never read: it is
	// entirely deleted, which is checked below, so nothing can see it, and it is
	// dropped again once the run is in. Going through the ordinary path with the
	// first character rather than around it is what keeps a purged run placed
	// exactly where an unpurged one would be.
	text := []rune{' '}
	if !gone {
		text = make([]rune, 0, c.text.hint(length))
		for range length {
			ch, ok := c.text.uvarint()
			if !ok || ch > utf8.MaxRune || (ch >= 0xD800 && ch <= 0xDFFF) {
				return ErrMalformed
			}
			text = append(text, rune(ch))
		}
	}

	// The deleted stretches, as gaps and lengths. They must ascend, not overlap
	// and stay inside the run, or the characters they name would be the wrong
	// ones.
	nDels, ok := c.delCounts.uvarint()
	if !ok || nDels > c.delGaps.remaining() {
		return ErrMalformed
	}
	dels := make([]delRange, 0, c.delGaps.hint(nDels))
	at := uint64(0)
	for range nDels {
		gap, ok1 := c.delGaps.uvarint()
		span, ok2 := c.delSpans.uvarint()
		delID, ok3 := steppedID(c.delSites, c.delSeqs, lastDelSeq)
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

	if gone {
		// The run's identities, claimed as two stretches rather than one at a
		// time: the characters, and the deletions that account for them. This is
		// where a purged run's length is held -- claimSpan refuses a stretch
		// reaching past what its site has issued, which is the bound that used
		// to be written out above, said once and in the place that enforces it.
		if !l.claimSpan(id, length) {
			return ErrMalformed
		}
		// A delRange keeps its offsets in thirty-two bits and [block.size] reads
		// the last one as the run's length, so a longer run would be described
		// by records that cannot describe it: the arithmetic above has already
		// wrapped by the time they are read. Refused rather than trusted to
		// disagree with itself, which is how it was refused before -- by
		// running out of memory first.
		if length > math.MaxUint32 {
			return ErrMalformed
		}
		for _, del := range dels {
			if !l.claimSpan(del.id, uint64(del.to-del.from)) {
				return ErrMalformed
			}
		}
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
		if gone {
			// Its claims were made whole, above; what is left is where it goes.
			if err := d.emplace(c); err != nil {
				return err
			}
			continue
		}
		if err := d.adopt(c, l); err != nil {
			return err
		}
	}

	if gone {
		// A purged run must be entirely deleted, or a character with nothing in
		// it would be visible. Nothing sound could have written one that is not,
		// so this is refused rather than repaired.
		covered := uint64(0)
		for _, del := range dels {
			covered += uint64(del.to - del.from)
		}
		if covered != length {
			return ErrMalformed
		}
		b, _, known := d.lookupChar(id)
		if !known || b.id != id || len(b.text) != 1 {
			return ErrMalformed
		}
		// The rest of the run, which was not integrated and did not need to be.
		//
		// Character k of a purged run names character k-1 as its origin, and
		// character k-1 is the last character of the document at that moment --
		// the character before it landed there, and this one is checked to. So
		// place walks from a block that is last, finds nothing after it, splits
		// nothing, and extends it: same site, and the sequence and the clock
		// both continue by construction, because they were read as one run
		// header. It appends in place, every time, and the only inputs are the
		// fields already checked above. The work is done once over the stretch
		// instead of once per character; nothing is taken on trust that was not
		// taken on trust before.
		//
		// The deletions replace the single record integration left behind,
		// joined the way [Doc.runs] joins them on the way out, so that a reader
		// handed two records a writer would have written as one holds what that
		// writer held.
		var joined []delRange
		for _, del := range dels {
			joined = appendDel(joined, del)
		}
		b.gone = true
		b.text = nil
		b.nsup = 0
		b.dels = joined
		d.total += int(length - 1)
		if last := clock + length - 1; last > d.clock {
			d.clock = last
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
	if !c.delID.IsRoot() && !l.claim(c.delID) {
		return ErrMalformed
	}
	return d.emplace(c)
}

// emplace is adopt without the claims: where the character goes, and what that
// costs the document's counters. A purged run claims the identities of its
// whole stretch at once and then integrates only its first character, so the
// two halves are separate.
func (d *Doc) emplace(c character) error {
	if _, _, known := d.lookupChar(c.origin); !known {
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
//
// Most identities arrive one at a time and are held in seen. A purged run's do
// not: it stands for a stretch of consecutive operations it does not carry, and
// naming them one by one would cost the reader what the run says rather than
// what it holds. Those arrive as spans -- per site, ascending, disjoint, and
// merged where they touch, so that a document really typed and really purged
// leaves one entry behind rather than one per keystroke.
//
// The two halves have to agree, because an operation counted twice is an
// operation the version vector promised that nothing accounted for, and the
// counts alone cannot tell: claim looks for the identity it is given inside the
// spans, and complete looks for the spans over the identities already seen,
// which is the same collision arriving in the other order.
type ledger struct {
	vv     VersionVector
	seen   map[ID]struct{}
	counts map[SiteID]uint64
	spans  map[SiteID][]span
}

// A span is the half-open stretch of sequence numbers [from, to) that one site
// issued and one purged run accounts for.
type span struct{ from, to uint64 }

// claim records one operation identity, rejecting anything the version vector
// does not cover and anything claimed twice -- whether it was claimed on its own
// or as part of a stretch.
func (l *ledger) claim(id ID) bool {
	if id.IsRoot() || !l.vv.Includes(id) {
		return false
	}
	if _, dup := l.seen[id]; dup {
		return false
	}
	if covers(l.spans[id.Site], id.Seq) {
		return false
	}
	l.seen[id] = struct{}{}
	l.counts[id.Site]++
	return true
}

// claimSpan records n consecutive operation identities from id in one entry,
// rejecting a stretch the version vector does not cover whole and one that
// overlaps a stretch already claimed.
//
// The version vector is what bounds n, and for a purged run it is the only
// thing that does: the run carries no characters, so nothing about the bytes
// says how long it is. Every character of it is an operation the vector
// promises, and they run consecutively from the run's own identity, so the run
// cannot reach past what its site has issued.
func (l *ledger) claimSpan(id ID, n uint64) bool {
	// The first identity of the stretch, then its last: the same two refusals
	// claim makes, over a range rather than a point.
	if id.IsRoot() || !l.vv.Includes(id) || n > l.vv[id.Site]-id.Seq+1 {
		return false
	}
	from, to := id.Seq, id.Seq+n
	list := l.spans[id.Site]
	// The first stretch that could reach this one, and then whether it does.
	at := sort.Search(len(list), func(i int) bool { return list[i].to > from })
	if at < len(list) && list[at].from < to {
		return false
	}
	list = append(list, span{})
	copy(list[at+1:], list[at:])
	list[at] = span{from: from, to: to}
	// Joined to either neighbour it touches, so that a document purged run by
	// run leaves one entry rather than one per run.
	if at+1 < len(list) && list[at].to == list[at+1].from {
		list[at].to = list[at+1].to
		list = append(list[:at+1], list[at+2:]...)
	}
	if at > 0 && list[at-1].to == list[at].from {
		list[at-1].to = list[at].to
		list = append(list[:at], list[at+1:]...)
	}
	if l.spans == nil {
		l.spans = map[SiteID][]span{}
	}
	l.spans[id.Site] = list
	l.counts[id.Site] += n
	return true
}

// covers reports whether one of these stretches holds the sequence number seq.
// They ascend and do not overlap, so the search is a bisection.
func covers(list []span, seq uint64) bool {
	at := sort.Search(len(list), func(i int) bool { return list[i].to > seq })
	return at < len(list) && list[at].from <= seq
}

// complete reports whether every operation the version vector promises was
// claimed, exactly once. Sequence numbers have no gaps, so counting them is
// almost enough: a stretch claimed after a sequence number already seen on its
// own counts one operation twice and leaves another unaccounted for, and the
// total is the same either way. That is the one collision claim cannot refuse
// as it happens -- the stretch was not there yet -- so it is looked for here.
func (l *ledger) complete() bool {
	for site, seq := range l.vv {
		if l.counts[site] != seq {
			return false
		}
	}
	for id := range l.seen {
		if covers(l.spans[id.Site], id.Seq) {
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

// steppedID reads an identity from two columns: one of sites, one of sequence
// numbers. A sequence number is written as a signed step from the last one that
// site used in that position — a deletion's, a run's own, and its origin's —
// and each position keeps its own running value, which is why the map is a
// parameter rather than a field.
//
// The root origin is site 0, sequence 0, and it is common; a step of zero from a
// running value of zero is one byte, which is what it was before.
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
func steppedID(siteCol, seqCol *column, last map[SiteID]uint64) (ID, bool) {
	site, ok1 := siteCol.uvarint()
	seq, ok2 := seqCol.uvarint()
	if !ok1 || !ok2 {
		return ID{}, false
	}
	at := SiteID(site)
	seq = uint64(int64(last[at]) + unzigzag(seq))
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

// sortIDs orders IDs in place by site then sequence, keeping every derived
// encoding deterministic.
//
// The lists are usually short, and for a handful of elements insertion sort
// beats anything that has to be called through a closure. They are not always
// short: every one of these holds the duplicate deletions a document is
// carrying, and two replicas deleting the same character make one apiece. A
// document being undone and redone makes a great many — measured, thirty-four
// thousand of them in a room of twenty — and insertion sort is quadratic, so
// [Doc.Snapshot] spent eighty-one per cent of a run in here and one type of
// document became a hundred times slower than every other.
//
// So the short case keeps its insertion sort and the long one gets a real
// sort. The order is the same either way, which is what the encodings depend
// on.
func sortIDs(ids []ID) {
	if len(ids) > sortIDsInsertionMax {
		slices.SortFunc(ids, func(a, b ID) int {
			switch {
			case idLess(a, b):
				return -1
			case idLess(b, a):
				return 1
			default:
				return 0
			}
		})
		return
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && idLess(ids[j], ids[j-1]); j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
}

// sortIDsInsertionMax is where insertion sort stops being the cheaper of the
// two. It is not a fine measurement: the point is that the quadratic branch
// cannot be reached with a list long enough for quadratic to matter.
const sortIDsInsertionMax = 32

// columns is where each field of a run is read from: every one has a stream of
// its own.
type columns struct {
	sites, seqs, clocks    *column
	oSites, oSeqs, lengths *column
	text, delCounts        *column
	delGaps, delSpans      *column
	delSites, delSeqs      *column
	// purged is version 9's addition: whether each run's characters were
	// discarded. It is nil for every earlier version, where no run could have
	// been, and readRun asks only when it is there.
	purged *column
	// withPurged says the thirteenth column is there. It is a field rather than
	// an argument because all is walked before the columns are filled in, so
	// asking whether purged is nil would answer no every time.
	withPurged bool
}

// all lists the columns once, so that nothing below can walk a different set of
// them from the one readColumnsV8 filled in.
func (c *columns) all() []**column {
	out := []**column{&c.sites, &c.seqs, &c.clocks, &c.oSites, &c.oSeqs,
		&c.lengths, &c.text, &c.delCounts, &c.delGaps, &c.delSpans,
		&c.delSites, &c.delSeqs}
	if c.withPurged {
		out = append(out, &c.purged)
	}
	return out
}

// readColumnsV8 takes apart the twelve length-prefixed columns of a version 8
// snapshot. Each begins with a byte saying how it is stored, and holds groups
// rather than plain values, so how many values it holds is counted here: a
// run-length column no longer costs a byte per value, and a run's length has to
// be held to the values that are actually there rather than to the bytes they
// occupy.
//
// ops is what the version vector promises. No column can hold more values than
// that — one per run, one per character or one per deleted stretch, and each of
// those is an operation the vector has to account for — so a column claiming
// more is refused before anything is decoded from it.
//
// purged says whether the thirteenth column, version 9's, is there.
func readColumnsV8(r *reader, ops uint64, purged bool) (*columns, error) {
	c := &columns{withPurged: purged}
	for _, into := range c.all() {
		n, ok := r.uvarint()
		// A column is at least its encoding byte, so a length of zero is not a
		// short column but a missing one.
		if !ok || n == 0 || n > uint64(len(r.buf)) {
			return nil, ErrMalformed
		}
		buf, _ := r.bytes(int(n))
		groups, err := readColumnStream(buf, ops)
		if err != nil {
			return nil, err
		}
		values, ok := countColumn(groups, ops)
		if !ok {
			return nil, ErrMalformed
		}
		*into = &column{r: &reader{buf: groups}, n: values}
	}
	return c, nil
}

// readColumnStream unwraps a column's payload to the groups inside it.
//
// [columnDeflated] is understood and never written, which is deliberate and is
// the whole of what this package does about compressing the text. Measured on
// automerge-paper, deflating the text column takes a version 8 snapshot from
// 259 878 bytes to 128 259 — and takes the same snapshot compressed by whoever
// stores it from 110 487 to 110 512, which is 25 bytes the wrong way.
// docs/performance.md says compression belongs beside the format rather than
// inside it, because a compressor's output is deterministic for a build and not
// across versions of itself, and Snapshot promises the same state is the same
// bytes. Emitting this would break that promise for a saving only a caller that
// stores snapshots raw would ever see. So the reader learns it now — a peer
// that sends it is understood rather than refused — and whether to write it is
// a decision with a number attached, in docs/performance.md, and one constant
// away.
//
// A compressed column is the one place where a snapshot's own length stops
// bounding what reading it costs, so the output is held to twenty bytes per
// operation the version vector promises: a value is at most a ten-byte uvarint
// and its group header at most another ten.
//
// No test can tell that ceiling from its absence by what Load returns, and the
// reason is worth writing down rather than leaving as a gap in the suite. A
// column may hold at most one value per operation, and eleven bytes is the most
// a value and its share of a header can cost, so anything this refuses is
// refused again by countColumn a few lines below. What it changes is when: this
// refuses while the bytes are arriving, and countColumn only once they have all
// been allocated. Lifting it leaves every rejection still rejected, which is why
// the test beside it says what it says.
func readColumnStream(buf []byte, ops uint64) ([]byte, error) {
	switch buf[0] {
	case columnGroups:
		return buf[1:], nil
	case columnDeflated:
		// The ceiling on ops is only there so that the multiplication cannot
		// wrap; a version vector promising 2^58 operations describes a document
		// no machine holds, and it is refused long before this by not adding up.
		limit := min(ops, uint64(1)<<58) * 20
		var out bytes.Buffer
		n, err := io.Copy(&out, io.LimitReader(
			flate.NewReader(bytes.NewReader(buf[1:])), int64(limit)+1))
		if err != nil || uint64(n) > limit {
			return nil, ErrMalformed
		}
		return out.Bytes(), nil
	}
	return nil, ErrMalformed
}

// countColumn says how many values a column holds, by walking its group headers
// without decoding them. It stops at limit rather than counting past it, which
// is what keeps the total from wrapping: a single run may claim 2^63 values.
//
// It reads the grouping and not the rules about it — a group that is legal here
// and refused by [column.uvarint] is refused there, and refused is refused.
func countColumn(buf []byte, limit uint64) (uint64, bool) {
	r := reader{buf: buf}
	total := uint64(0)
	for len(r.buf) > 0 {
		count, ok := r.uvarint()
		if !ok {
			return 0, false
		}
		var values uint64
		if count%2 == 0 {
			values = count / 2
			if values < runThreshold {
				return 0, false
			}
			if _, ok := r.uvarint(); !ok {
				return 0, false
			}
		} else {
			// A literal stretch of m values is written as 2m−1, so m is never
			// zero and the halving cannot wrap however large the header is.
			values = count/2 + 1
			for range values {
				if _, ok := r.uvarint(); !ok {
					return 0, false
				}
			}
		}
		total += values
		if total > limit {
			return 0, false
		}
	}
	return total, true
}

// A column is one field of a run, read one value at a time.
type column struct {
	r *reader
	n uint64 // values not yet produced

	left    uint64 // values left in the group being read
	lit     bool   // that group is a literal stretch
	first   bool   // the next value is the first of it
	val     uint64 // the last value produced
	have    bool   // one has been
	prevLit bool   // the group before this one was a literal stretch
	same    uint64 // how many of the same value the literal stretch ends with
}

// uvarint produces the column's next value.
//
// The refusals here are what make a version 8 column canonical, and they are the
// same three facts said from the other side. A run is at least [runThreshold]
// long, or the writer would have left it in the literals. Two literal stretches
// never touch, or the writer would have written one. And two groups never touch
// on the same value, nor does a literal stretch hold [runThreshold] of one, or
// the writer would have seen a longer equal block than the one it wrote. Between
// them there is exactly one grouping of any sequence of values, so a peer cannot
// hand over a snapshot that decodes to a document already held and yet does not
// match its bytes.
//
// What is not refused here is anything [countColumn] has already walked. It read
// every group header and every value of this column before a value was taken
// from it, so a read cannot come up short and a run cannot be shorter than the
// threshold. Refusing those again would be lines that look like guards and never
// run — the coverage says so, and removing them left every rejection rejected.
func (c *column) uvarint() (uint64, bool) {
	if c.left == 0 && !c.open() {
		return 0, false
	}
	if c.lit {
		v, _ := c.r.uvarint()
		switch {
		case c.first:
			// A literal stretch beginning on the value the group before it
			// ended on is an equal block the writer would not have cut.
			if c.have && v == c.val {
				return 0, false
			}
			c.same, c.first = 1, false
		case v == c.val:
			c.same++
			if c.same >= runThreshold {
				return 0, false
			}
		default:
			c.same = 1
		}
		c.val = v
	}
	c.have = true
	c.left--
	c.n--
	return c.val, true
}

// open starts the next group. A column with nothing left in it is where a run
// asking for one more field than the snapshot holds is refused.
func (c *column) open() bool {
	if len(c.r.buf) == 0 {
		return false
	}
	count, _ := c.r.uvarint()
	if count%2 == 0 {
		v, _ := c.r.uvarint()
		if c.have && v == c.val {
			return false
		}
		c.left, c.lit, c.val, c.prevLit = count/2, false, v, false
		return true
	}
	if c.prevLit {
		return false
	}
	c.left, c.lit, c.first, c.prevLit = count/2+1, true, true, true
	return true
}

// remaining is the most values the column can still produce, which is what a
// count read from another column is held to. A column knows the number, counted
// by [countColumn] before any of it was decoded.
func (c *column) remaining() uint64 { return c.n }

// hint sizes a slice for n values without believing n. A run-length column can
// promise far more values than it holds bytes, so the bytes are the ceiling on
// what is allocated up front; the slice grows to the rest as it arrives.
func (c *column) hint(n uint64) uint64 {
	return min(n, uint64(len(c.r.buf))+1)
}

// empty reports whether every column was consumed to its end.
func (c *columns) empty() bool {
	for _, col := range c.all() {
		if (*col).left != 0 || len((*col).r.buf) != 0 {
			return false
		}
	}
	return true
}

// readCollected reads the two tables a text snapshot and a list snapshot both
// carry, and refuses one whose tables are not empty.
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

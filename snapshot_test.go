package crdt

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
)

func TestSnapshotRoundTrip(t *testing.T) {
	a := New(1)
	insert(t, a, 0, "hello 世界")
	remove(t, a, 5, 1)
	insert(t, a, 5, "—")

	loaded, err := Load(2, a.Snapshot())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := loaded.String(), a.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got, want := loaded.Len(), a.Len(); got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}
	if got, want := loaded.Tombstones(), a.Tombstones(); got != want {
		t.Errorf("Tombstones() = %d, want %d", got, want)
	}
	if !loaded.Version().Equal(a.Version()) {
		t.Errorf("Version() = %v, want %v", loaded.Version(), a.Version())
	}
	if got, want := loaded.Site(), SiteID(2); got != want {
		t.Errorf("Site() = %d, want %d: Load must adopt the site it is given", got, want)
	}
	if string(loaded.Snapshot()) != string(a.Snapshot()) {
		t.Error("re-encoding a loaded snapshot did not reproduce it")
	}
}

// A snapshot must carry the whole history, not just the current text: a replica
// loaded from one has to be able to serve any peer, however far behind.
func TestSnapshotPreservesHistory(t *testing.T) {
	a := New(1)
	insert(t, a, 0, "abcdef")
	remove(t, a, 1, 2)

	loaded, err := Load(9, a.Snapshot())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	fresh := New(3)
	apply(t, fresh, loaded.OpsSince(nil))
	if got, want := fresh.String(), a.String(); got != want {
		t.Fatalf("replaying a loaded snapshot gave %q, want %q", got, want)
	}
	if !fresh.Version().Equal(a.Version()) {
		t.Fatalf("Version() = %v, want %v", fresh.Version(), a.Version())
	}
}

// A site's Lamport clock has to survive a snapshot even when no surviving
// character records it — which happens whenever a replica's last operations were
// deletions. Otherwise its next operation claims a clock below its own sequence
// number, and every peer rejects it.
func TestLoadRestoresClockFromDeletions(t *testing.T) {
	a, b := New(1), New(2)
	fromA := insert(t, a, 0, "a")
	fromB := insert(t, b, 0, "b") // concurrent, so both characters carry clock 1
	apply(t, a, fromB)
	apply(t, b, fromA)
	remove(t, b, 0, 2) // two deletions, taking b's sequence to 3

	loaded, err := Load(2, b.Snapshot())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ops, err := loaded.Insert(0, "c")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := ops[0].validate(); err != nil {
		t.Fatalf("the operation issued after Load is invalid (%v): %+v", err, ops[0])
	}
	if err := a.Apply(ops...); err != nil {
		t.Fatalf("a peer rejected the operation issued after Load: %v", err)
	}
}

func TestSnapshotOfEmptyDocument(t *testing.T) {
	loaded, err := Load(5, New(1).Snapshot())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := loaded.String(); got != "" {
		t.Fatalf("String() = %q, want empty", got)
	}
	insert(t, loaded, 0, "x")
	if got := loaded.String(); got != "x" {
		t.Fatalf("String() = %q, want %q", got, "x")
	}
}

// snapshotBuilder assembles snapshots field by field so that every rejection in
// Load can be provoked directly, rather than by corrupting valid bytes and
// hoping the corruption lands where it is needed.
type snapshotBuilder struct {
	magic   []byte
	version byte
	sites   [][2]uint64 // site, sequence
	items   []snapshotItem
	dups    [][4]uint64 // deletion site, sequence, target site, target sequence
	// counts override the encoded lengths, to build a header that lies.
	itemCount, dupCount int
	trailing            []byte
}

type snapshotItem struct {
	site, seq, clock, originSite, originSeq, char, delSite, delSeq uint64
}

func (b snapshotBuilder) build() []byte {
	out := append([]byte{}, b.magic...)
	out = append(out, b.version)
	out = binary.AppendUvarint(out, uint64(len(b.sites)))
	for _, s := range b.sites {
		out = binary.AppendUvarint(out, s[0])
		out = binary.AppendUvarint(out, s[1])
	}
	count := b.itemCount
	if count == 0 {
		count = len(b.items)
	}
	out = binary.AppendUvarint(out, uint64(count))
	for _, it := range b.items {
		for _, v := range []uint64{it.site, it.seq, it.clock, it.originSite, it.originSeq, it.char, it.delSite, it.delSeq} {
			out = binary.AppendUvarint(out, v)
		}
	}
	count = b.dupCount
	if count == 0 {
		count = len(b.dups)
	}
	out = binary.AppendUvarint(out, uint64(count))
	for _, d := range b.dups {
		for _, v := range d {
			out = binary.AppendUvarint(out, v)
		}
	}
	return append(out, b.trailing...)
}

// wellFormed is a two-character document from site 1, the second deleted, used
// as the base every rejection test varies from.
func wellFormed() snapshotBuilder {
	return snapshotBuilder{
		magic:   []byte("crdt"),
		version: snapshotVersionV1,
		sites:   [][2]uint64{{1, 3}},
		items: []snapshotItem{
			{site: 1, seq: 1, clock: 1, char: 'a'},
			{site: 1, seq: 2, clock: 2, originSite: 1, originSeq: 1, char: 'b', delSite: 1, delSeq: 3},
		},
	}
}

func TestLoadAcceptsAHandBuiltSnapshot(t *testing.T) {
	d, err := Load(2, wellFormed().build())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := d.String(), "a"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got, want := d.Tombstones(), 1; got != want {
		t.Fatalf("Tombstones() = %d, want %d", got, want)
	}
}

func TestLoadRejectsMalformedSnapshots(t *testing.T) {
	tests := []struct {
		name  string
		alter func(*snapshotBuilder)
	}{
		{"foreign magic", func(b *snapshotBuilder) { b.magic = []byte("yjs!") }},
		{"future format version", func(b *snapshotBuilder) { b.version = snapshotVersion + 1 }},
		{"version zero", func(b *snapshotBuilder) { b.version = 0 }},
		{"site with no operations", func(b *snapshotBuilder) { b.sites = [][2]uint64{{1, 0}} }},
		{"more items than bytes", func(b *snapshotBuilder) { b.itemCount = 1 << 20 }},
		{"more duplicates than bytes", func(b *snapshotBuilder) { b.dupCount = 1 << 20 }},
		{"trailing bytes", func(b *snapshotBuilder) { b.trailing = []byte{0} }},
		{"character above the highest rune", func(b *snapshotBuilder) { b.items[0].char = 0x110000 }},
		{"surrogate character", func(b *snapshotBuilder) { b.items[0].char = 0xD800 }},
		{"clock below sequence", func(b *snapshotBuilder) { b.items[1].clock = 1 }},
		{"item with the root identity", func(b *snapshotBuilder) { b.items[0].site, b.items[0].seq = 0, 0 }},
		{"item the version vector does not cover", func(b *snapshotBuilder) { b.items[1].seq = 9 }},
		{"repeated identity", func(b *snapshotBuilder) { b.items[1].seq = b.items[0].seq }},
		{"origin that does not exist", func(b *snapshotBuilder) { b.items[1].originSeq = 7 }},
		{"origin that comes later", func(b *snapshotBuilder) {
			b.items[0].originSite, b.items[0].originSeq = 1, 2
		}},
		{"deletion the version vector does not cover", func(b *snapshotBuilder) { b.items[1].delSeq = 9 }},
		{"duplicate deletion of the root", func(b *snapshotBuilder) {
			b.dups = [][4]uint64{{0, 0, 1, 1}}
		}},
		{"duplicate deletion the version vector does not cover", func(b *snapshotBuilder) {
			b.dups = [][4]uint64{{1, 9, 1, 1}}
		}},
		{"duplicate deletion of a character that does not exist", func(b *snapshotBuilder) {
			b.dups = [][4]uint64{{1, 3, 1, 7}}
		}},
		{"duplicate deletion of the root character", func(b *snapshotBuilder) {
			b.sites = [][2]uint64{{1, 4}}
			b.dups = [][4]uint64{{1, 4, 0, 0}}
		}},
		{"duplicate deletion of a character still visible", func(b *snapshotBuilder) {
			b.sites = [][2]uint64{{1, 4}}
			b.dups = [][4]uint64{{1, 4, 1, 1}}
		}},
		{"duplicate deletion below the one the character kept", func(b *snapshotBuilder) {
			// The item keeps the lower operation, so a duplicate below it is a
			// state the tie-break could not have produced.
			b.sites = [][2]uint64{{1, 4}}
			b.items[1].delSeq = 4
			b.dups = [][4]uint64{{1, 3, 1, 2}}
		}},
		{"the same site listed twice", func(b *snapshotBuilder) {
			b.sites = [][2]uint64{{1, 3}, {1, 3}}
		}},
		{"a history with a gap", func(b *snapshotBuilder) {
			// Four operations promised, three accounted for.
			b.sites = [][2]uint64{{1, 4}}
		}},
		{"a deletion no character claims", func(b *snapshotBuilder) {
			b.items[1].delSite, b.items[1].delSeq = 0, 0
		}},
		{"two characters claiming one deletion", func(b *snapshotBuilder) {
			b.sites = [][2]uint64{{1, 4}}
			b.items = append(b.items, snapshotItem{
				site: 1, seq: 4, clock: 4, originSite: 1, originSeq: 2,
				char: 'c', delSite: 1, delSeq: 3,
			})
		}},
		{"a character with a sequence number of zero", func(b *snapshotBuilder) {
			b.items[0].site, b.items[0].seq = 1, 0
		}},
		{"a deletion with a sequence number of zero", func(b *snapshotBuilder) {
			b.items[1].delSite, b.items[1].delSeq = 1, 0
		}},
		{"an origin with a sequence number of zero", func(b *snapshotBuilder) {
			b.items[1].originSite, b.items[1].originSeq = 1, 0
		}},
		{"an origin that is a deletion", func(b *snapshotBuilder) {
			// Operation 3 is the deletion of the second character; it made no
			// character, so nothing can have been inserted after it.
			b.sites = [][2]uint64{{1, 4}}
			b.items = append(b.items, snapshotItem{
				site: 1, seq: 4, clock: 4, originSite: 1, originSeq: 3, char: 'c',
			})
		}},
		{"characters in an order integration could not produce", func(b *snapshotBuilder) {
			// Both hang off the root, so the higher clock has to come first.
			b.sites = [][2]uint64{{1, 2}, {2, 1}}
			b.items = []snapshotItem{
				{site: 2, seq: 1, clock: 1, char: 'a'},
				{site: 1, seq: 1, clock: 9, char: 'b'},
				{site: 1, seq: 2, clock: 10, originSite: 1, originSeq: 1, char: 'c'},
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := wellFormed()
			tt.alter(&b)
			if _, err := Load(2, b.build()); !errors.Is(err, ErrMalformed) {
				t.Fatalf("Load() = %v, want ErrMalformed", err)
			}
		})
	}
}

// Every proper prefix of a snapshot is truncated. None may be accepted, and none
// may panic.
func TestLoadRejectsTruncatedSnapshots(t *testing.T) {
	d := New(1)
	insert(t, d, 0, "truncate me")
	remove(t, d, 0, 3)
	data := d.Snapshot()
	for n := range len(data) {
		if _, err := Load(2, data[:n]); err == nil {
			t.Fatalf("Load(%d of %d bytes) succeeded, want an error", n, len(data))
		}
	}
}

// Concurrent deletions of one character are recorded outside the character
// itself, so they need their own round trip.
func TestSnapshotCarriesDuplicateDeletions(t *testing.T) {
	a, b := New(1), New(2)
	seed := insert(t, a, 0, "xyz")
	apply(t, b, seed)
	delA := remove(t, a, 1, 1)
	delB := remove(t, b, 1, 1)
	apply(t, a, delB)
	apply(t, b, delA)

	loaded, err := Load(3, a.Snapshot())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(loaded.Snapshot()) != string(a.Snapshot()) {
		t.Fatal("the losing deletion did not survive the round trip")
	}
	fresh := New(4)
	apply(t, fresh, loaded.OpsSince(nil))
	if !fresh.Version().Equal(a.Version()) {
		t.Fatalf("Version() = %v, want %v", fresh.Version(), a.Version())
	}
}

// runBuilder assembles snapshots field by field, so that every
// rejection in the run reader can be provoked directly.
type runBuilder struct {
	// purgedBelow is version 7's floor: the clock below which characters were
	// discarded. Zero for a fixture that purged nothing.
	purgedBelow uint64
	// asVersion writes an older format instead of the current one, so that the
	// readers for versions this package still accepts keep being exercised after
	// the current version moves on. Zero means the current version.
	asVersion byte
	sites     [][2]uint64
	floor     [][2]uint64 // the collection floor: site, sequence
	gone      [][2]uint64 // what collection took away: site, count
	// counts override the encoded lengths of the two, to build a header that lies.
	floorCount, goneCount int
	runs                  []encodedRun
	count                 int // overrides the encoded number of runs
	tail                  []byte
}

type encodedRun struct {
	site, seq, clock, originSite, originSeq uint64
	text                                    []rune
	length                                  uint64 // overrides the encoded length
	dels                                    [][4]uint64
	delCount                                int    // overrides the encoded number of deletions
	purged                                  bool   // version 9: its characters were discarded
	purgedFlag                              uint64 // overrides the encoded flag
}

func (b runBuilder) build() []byte {
	out := append([]byte{}, "crdt"...)
	// The version the writer would choose for this document, so that a fixture
	// is comparable byte for byte with what Snapshot produces: version 9 only
	// when there is a purge floor to write, and version 8 otherwise. See
	// Doc.formatVersion.
	version := byte(snapshotVersionV8)
	if b.purgedBelow != 0 {
		version = snapshotVersion
	}
	if b.asVersion != 0 {
		version = b.asVersion
	}
	lastDel := map[SiteID]uint64{}
	lastRun := map[SiteID]uint64{}
	lastOrigin := map[SiteID]uint64{}
	out = append(out, version)
	out = binary.AppendUvarint(out, uint64(len(b.sites)))
	for _, s := range b.sites {
		out = binary.AppendUvarint(out, s[0])
		out = binary.AppendUvarint(out, s[1])
	}
	if version >= snapshotVersionV6 {
		// Version 6: the collection floor and the per-site tallies of what
		// collection took away. Nothing here has ever been collected, so both
		// are empty, which is what a document that never calls Collect writes.
		nFloor := b.floorCount
		if nFloor == 0 {
			nFloor = len(b.floor)
		}
		out = binary.AppendUvarint(out, uint64(nFloor))
		for _, f := range b.floor {
			out = binary.AppendUvarint(out, f[0])
			out = binary.AppendUvarint(out, f[1])
		}
		nGone := b.goneCount
		if nGone == 0 {
			nGone = len(b.gone)
		}
		out = binary.AppendUvarint(out, uint64(nGone))
		for _, g := range b.gone {
			out = binary.AppendUvarint(out, g[0])
			out = binary.AppendUvarint(out, g[1])
		}
	}
	if version >= snapshotVersion {
		// Version 9: the purge floor. A fixture that purged nothing writes zero,
		// which is what a document that never called Purge writes.
		out = binary.AppendUvarint(out, b.purgedBelow)
	}
	n := b.count
	if n == 0 {
		n = len(b.runs)
	}
	out = binary.AppendUvarint(out, uint64(n))

	// A case names a run's sequence, clock and origin outright, because that is
	// what a reader of the test wants to see; the encodings are applied here.
	// Version 4 writes the two sequence numbers as steps and the clock as the
	// distance above the run's own sequence. Version 5 writes each field in a
	// column of its own, which is the same bytes in a different order; version
	// 8 writes the deletion fields in four columns rather than one, and every
	// column in groups rather than one value at a time.
	//
	// A case that asks for a clock below the sequence — which no honest replica
	// can produce — still encodes: the subtraction wraps, and the enormous
	// distance is refused against the ceiling instead of against the sequence.
	// The case still fails to load, which is what it asserts.
	var runSites, seqs, clocks, oSites, oSeqs, lengths, text, delCounts []uint64
	var delGaps, delSpans, delSites, delSeqs, purged []uint64
	for _, r := range b.runs {
		runSites = append(runSites, r.site)
		flag := boolByte(r.purged)
		if r.purgedFlag != 0 {
			flag = r.purgedFlag
		}
		purged = append(purged, flag)
		seqs = append(seqs, zigzag(int64(r.seq)-int64(lastRun[SiteID(r.site)])))
		lastRun[SiteID(r.site)] = r.seq
		clocks = append(clocks, r.clock-r.seq)
		oSites = append(oSites, r.originSite)
		oSeqs = append(oSeqs, zigzag(int64(r.originSeq)-int64(lastOrigin[SiteID(r.originSite)])))
		lastOrigin[SiteID(r.originSite)] = r.originSeq
		length := r.length
		if length == 0 {
			length = uint64(len(r.text))
		}
		lengths = append(lengths, length)
		for _, ch := range r.text {
			text = append(text, uint64(ch))
		}
		dn := r.delCount
		if dn == 0 {
			dn = len(r.dels)
		}
		delCounts = append(delCounts, uint64(dn))
		for _, d := range r.dels {
			delGaps = append(delGaps, d[0])
			delSpans = append(delSpans, d[1])
			delSites = append(delSites, d[2])
			site := SiteID(d[2])
			delSeqs = append(delSeqs, zigzag(int64(d[3])-int64(lastDel[site])))
			lastDel[site] = d[3]
		}
	}
	if version >= snapshotVersionV8 {
		// Version 8: twelve columns, each an encoding byte and then its groups.
		cols := [][]uint64{runSites, seqs, clocks, oSites, oSeqs,
			lengths, text, delCounts, delGaps, delSpans, delSites, delSeqs}
		if version >= snapshotVersion {
			// Version 9's addition, and the reader asks for it by version, so a
			// fixture that did not write it would be short a column rather than
			// describing an older format.
			cols = append(cols, purged)
		}
		for _, col := range cols {
			out = appendColumn(out, groupColumn(col))
		}
	} else {
		// Versions 5 and 6: nine plain columns, the deletion fields written as
		// one stream of gap, span, site and step over and over.
		var delFields []uint64
		for i := range delGaps {
			delFields = append(delFields, delGaps[i], delSpans[i], delSites[i], delSeqs[i])
		}
		for _, col := range [][]uint64{runSites, seqs, clocks, oSites, oSeqs,
			lengths, text, delCounts, delFields} {
			var enc []byte
			for _, v := range col {
				enc = binary.AppendUvarint(enc, v)
			}
			out = binary.AppendUvarint(out, uint64(len(enc)))
			out = append(out, enc...)
		}
	}

	out = binary.AppendUvarint(out, 0) // no duplicate deletions
	return append(out, b.tail...)
}

// groupColumn writes a column the way version 8 does, and is deliberately not
// the encoder the package uses: a fixture that shares its encoder with the code
// under test agrees with it about everything, including its mistakes. What pins
// this one to the real format is TestABuiltRunIsWhatARealDocumentWrites, which
// puts its bytes beside a document that was actually typed.
func groupColumn(vs []uint64) []byte {
	var out []byte
	var lit []uint64
	flushLit := func() {
		if len(lit) == 0 {
			return
		}
		out = binary.AppendUvarint(out, zigzag(-int64(len(lit))))
		for _, v := range lit {
			out = binary.AppendUvarint(out, v)
		}
		lit = lit[:0]
	}
	for i := 0; i < len(vs); {
		j := i
		for j+1 < len(vs) && vs[j+1] == vs[i] {
			j++
		}
		if n := j - i + 1; n >= runThreshold {
			flushLit()
			out = binary.AppendUvarint(out, zigzag(int64(n)))
			out = binary.AppendUvarint(out, vs[i])
		} else {
			lit = append(lit, vs[i:j+1]...)
		}
		i = j + 1
	}
	flushLit()
	return out
}

// appendColumn frames one column: its length, its encoding byte, its groups.
func appendColumn(out, groups []byte) []byte {
	out = binary.AppendUvarint(out, uint64(len(groups)+1))
	out = append(out, columnGroups)
	return append(out, groups...)
}

// wellFormedRun is a four-character run from site 1 with its third character
// deleted, the shape every rejection below varies from.
func wellFormedRun() runBuilder {
	return runBuilder{
		sites: [][2]uint64{{1, 5}},
		runs: []encodedRun{{
			site: 1, seq: 1, clock: 1, text: []rune("abcd"),
			dels: [][4]uint64{{2, 1, 1, 5}}, // gap 2, span 1, deleted by 5@1
		}},
	}
}

func TestLoadAcceptsAHandBuiltRun(t *testing.T) {
	d, err := Load(2, wellFormedRun().build())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := d.String(), "abd"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got, want := d.Tombstones(), 1; got != want {
		t.Fatalf("Tombstones() = %d, want %d", got, want)
	}
	// It has to re-encode to exactly the bytes it was given.
	if string(d.Snapshot()) != string(wellFormedRun().build()) {
		t.Fatal("re-encoding a hand-built run did not reproduce it")
	}
}

func TestLoadRejectsMalformedRuns(t *testing.T) {
	tests := []struct {
		name  string
		alter func(*runBuilder)
	}{
		{"a run of no characters", func(b *runBuilder) { b.runs[0].text = nil; b.runs[0].dels = nil }},
		{"more characters than bytes", func(b *runBuilder) { b.runs[0].length = 1 << 20 }},
		{"more runs than bytes", func(b *runBuilder) { b.count = 1 << 20 }},
		{"clock below the sequence", func(b *runBuilder) { b.runs[0].clock = 0; b.runs[0].seq = 1 }},
		{"an identity with a sequence of zero", func(b *runBuilder) { b.runs[0].seq = 0 }},
		{"an origin with a sequence of zero", func(b *runBuilder) { b.runs[0].originSite = 1 }},
		{"a character above the highest rune", func(b *runBuilder) { b.runs[0].text = []rune{'a', 0, 'c'}; b.runs[0].text[1] = rune(0x110000) }},
		{"a surrogate character", func(b *runBuilder) { b.runs[0].text[1] = rune(0xD800) }},
		{"more deletions than bytes", func(b *runBuilder) { b.runs[0].delCount = 1 << 20 }},
		{"a deletion of no characters", func(b *runBuilder) { b.runs[0].dels[0][1] = 0 }},
		{"a deletion past the end of the run", func(b *runBuilder) { b.runs[0].dels[0][1] = 9 }},
		{"a deletion starting past the end", func(b *runBuilder) { b.runs[0].dels[0][0] = 9 }},
		{"a deletion with no identity", func(b *runBuilder) { b.runs[0].dels[0][2], b.runs[0].dels[0][3] = 0, 0 }},
		{"a deletion with a sequence of zero", func(b *runBuilder) { b.runs[0].dels[0][3] = 0 }},
		// The two below are failure modes version 3 introduced by writing the
		// sequence number as a step: a step is signed, so it can land where no
		// operation is. A case still names the sequence it wants; the builder
		// turns it into the step that reaches it.
		{"a deletion whose step lands below the first sequence", func(b *runBuilder) {
			b.runs[0].dels = [][4]uint64{{2, 1, 1, 5}, {0, 1, 1, 0}}
		}},
		{"a deletion above the clock ceiling", func(b *runBuilder) {
			b.runs[0].dels[0][3] = MaxClock + 1
		}},
		{"deletions that overlap", func(b *runBuilder) {
			b.sites = [][2]uint64{{1, 6}}
			b.runs[0].dels = [][4]uint64{{2, 1, 1, 5}, {0, 2, 1, 6}}
		}},
		{"an origin that does not exist", func(b *runBuilder) { b.runs[0].originSite, b.runs[0].originSeq = 9, 9 }},
		{"a run the version vector does not cover", func(b *runBuilder) { b.sites = [][2]uint64{{1, 2}} }},
		{"trailing bytes", func(b *runBuilder) { b.tail = []byte{0} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := wellFormedRun()
			tt.alter(&b)
			if _, err := Load(2, b.build()); !errors.Is(err, ErrMalformed) {
				t.Fatalf("Load() = %v, want ErrMalformed", err)
			}
		})
	}
}

// Version 1 wrote one record per character, and documents stored by an older
// build must still open — including refusing the ones it should refuse.
func TestLoadRejectsTruncatedVersionOneSnapshots(t *testing.T) {
	data := wellFormed().build()
	if _, err := Load(2, data); err != nil {
		t.Fatalf("the version 1 fixture does not load: %v", err)
	}
	for n := range len(data) {
		if _, err := Load(2, data[:n]); err == nil {
			t.Fatalf("Load(%d of %d bytes) succeeded, want an error", n, len(data))
		}
	}
}

// A document stored by an older build has to open. This snapshot was produced by
// crdt v0.4.0, which wrote one record per character, and is kept verbatim: the
// test is worth nothing if it is regenerated by the code it is meant to check.
//
// It carries the awkward parts on purpose — characters outside the basic plane,
// a stretch deleted concurrently by two replicas, and a second site's work.
func TestLoadReadsASnapshotWrittenByVersionOne(t *testing.T) {
	encoded, err := os.ReadFile("testdata/v1-snapshot.base64")
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	if got, want := raw[4], byte(snapshotVersionV1); got != want {
		t.Fatalf("the fixture claims format version %d, want %d", got, want)
	}

	d, err := Load(9, raw)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	const want = "written v0.4.0 — héllo 世界 🌍 and more"
	if got := d.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got, wantN := d.Tombstones(), 3; got != wantN {
		t.Fatalf("Tombstones() = %d, want %d", got, wantN)
	}

	// It must be usable, not merely readable: the history replays, and writing it
	// out again produces the current format, which reloads to the same thing.
	peer := New(8)
	apply(t, peer, d.OpsSince(nil))
	if got := peer.String(); got != want {
		t.Fatalf("replaying the loaded history gave %q, want %q", got, want)
	}
	fresh := d.Snapshot()
	// Version 8, not the current one: version 9 is written only by a document
	// that has purged, so that a build which merely understands it never hands
	// an older reader bytes it cannot parse. See Doc.formatVersion.
	if fresh[4] != snapshotVersionV8 {
		t.Fatalf("re-encoding wrote format version %d, want %d", fresh[4], snapshotVersionV8)
	}
	again, err := Load(7, fresh)
	if err != nil {
		t.Fatalf("reloading the re-encoded snapshot: %v", err)
	}
	if got := again.String(); got != want {
		t.Fatalf("the re-encoded snapshot reads %q, want %q", got, want)
	}
	if len(fresh) >= len(raw) {
		t.Fatalf("re-encoding did not shrink it: %d bytes against %d", len(fresh), len(raw))
	}
	t.Logf("the same document: %d bytes in version %d, %d in version %d", len(raw), raw[4], len(fresh), fresh[4])
}

// A document stored by a build that wrote version 3 has to open. Version 4
// changed the run header: its own sequence number and its origin's are steps
// now, and its clock is the distance above its own sequence.
//
// The fixture was produced by the build that still wrote version 3, and carries
// the same awkward parts as the version 2 one. Two versions back and one version
// back both have to read, which is the property a chain of format changes has to
// keep and the one it is easiest to lose.
func TestLoadReadsASnapshotWrittenByVersionThree(t *testing.T) {
	encoded, err := os.ReadFile("testdata/v3-snapshot.base64")
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	if got, want := raw[4], byte(snapshotVersionV3); got != want {
		t.Fatalf("the fixture claims format version %d, want %d", got, want)
	}

	d, err := Load(9, raw)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	const want = "written.14.0 — héllo界 🌍 and more plus"
	if got := d.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got, wantN := d.Tombstones(), 5; got != wantN {
		t.Fatalf("Tombstones() = %d, want %d", got, wantN)
	}

	peer := New(8)
	apply(t, peer, d.OpsSince(nil))
	if got := peer.String(); got != want {
		t.Fatalf("replaying the loaded history gave %q, want %q", got, want)
	}
	fresh := d.Snapshot()
	// Version 8, not the current one: version 9 is written only by a document
	// that has purged, so that a build which merely understands it never hands
	// an older reader bytes it cannot parse. See Doc.formatVersion.
	if fresh[4] != snapshotVersionV8 {
		t.Fatalf("re-encoding wrote format version %d, want %d", fresh[4], snapshotVersionV8)
	}
	again, err := Load(7, fresh)
	if err != nil {
		t.Fatalf("reloading the re-encoded snapshot: %v", err)
	}
	if got := again.String(); got != want {
		t.Fatalf("the re-encoded snapshot reads %q, want %q", got, want)
	}
	if got, wantN := again.Tombstones(), 5; got != wantN {
		t.Fatalf("the re-encoded snapshot has %d tombstones, want %d", got, wantN)
	}
	t.Logf("the same document: %d bytes in version %d, %d in version %d", len(raw), raw[4], len(fresh), fresh[4])
}

// A document stored by a build that wrote version 2 has to open. Version 3
// changed one field — a deletion's sequence number, from the number itself to a
// step from the last one that site used — and a format change is only safe if
// what came before it still reads.
//
// The fixture was produced by the build that still wrote version 2 and is kept
// verbatim, for the reason the version 1 one is: a fixture regenerated by the
// code it is meant to check proves nothing. It carries the awkward parts on
// purpose — characters outside the basic plane, a stretch two replicas deleted
// concurrently, a second site's work, and a run holding more than one deleted
// stretch, which is the case the step encoding is about.
func TestLoadReadsASnapshotWrittenByVersionTwo(t *testing.T) {
	encoded, err := os.ReadFile("testdata/v2-snapshot.base64")
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	if got, want := raw[4], byte(snapshotVersionV2); got != want {
		t.Fatalf("the fixture claims format version %d, want %d", got, want)
	}

	d, err := Load(9, raw)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	const want = "written.13.0 — héllo界 🌍 and more plus"
	if got := d.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got, wantN := d.Tombstones(), 5; got != wantN {
		t.Fatalf("Tombstones() = %d, want %d", got, wantN)
	}

	// Usable, not merely readable: the history replays into a fresh replica, and
	// re-encoding writes the current version, which reloads to the same text and
	// the same tombstones. The tombstones matter here more than anywhere else —
	// they are what the changed field names.
	peer := New(8)
	apply(t, peer, d.OpsSince(nil))
	if got := peer.String(); got != want {
		t.Fatalf("replaying the loaded history gave %q, want %q", got, want)
	}
	fresh := d.Snapshot()
	// Version 8, not the current one: version 9 is written only by a document
	// that has purged, so that a build which merely understands it never hands
	// an older reader bytes it cannot parse. See Doc.formatVersion.
	if fresh[4] != snapshotVersionV8 {
		t.Fatalf("re-encoding wrote format version %d, want %d", fresh[4], snapshotVersionV8)
	}
	again, err := Load(7, fresh)
	if err != nil {
		t.Fatalf("reloading the re-encoded snapshot: %v", err)
	}
	if got := again.String(); got != want {
		t.Fatalf("the re-encoded snapshot reads %q, want %q", got, want)
	}
	if got, wantN := again.Tombstones(), 5; got != wantN {
		t.Fatalf("the re-encoded snapshot has %d tombstones, want %d", got, wantN)
	}
	t.Logf("the same document: %d bytes in version %d, %d in version %d", len(raw), raw[4], len(fresh), fresh[4])
}

// Truncating a version 2 snapshot anywhere must fail rather than produce a
// document, exactly as truncating a version 1 one must.
func TestLoadRejectsTruncatedVersionTwoSnapshots(t *testing.T) {
	encoded, err := os.ReadFile("testdata/v2-snapshot.base64")
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	for n := range len(raw) {
		if _, err := Load(2, raw[:n]); err == nil {
			t.Fatalf("Load(%d of %d bytes) succeeded, want an error", n, len(raw))
		}
	}
}

// columnsFit reports whether a snapshot holds exactly n length-prefixed
// columns, by walking n of them and requiring that what follows is an empty
// duplicate-deletion table and the end of the bytes.
//
// Exactly, rather than at least: a column is length-prefixed, so a walk that
// stops counting when it runs out reads the duplicate count as one more empty
// column and answers one too many. Requiring the end to line up is what makes
// the answer mean something -- these fixtures carry no duplicates, so anything
// but a zero and an empty buffer says the walk landed somewhere else.
func columnsFit(t *testing.T, good []byte, n int) bool {
	t.Helper()
	r := &reader{buf: good[columnsStart(t, good):]}
	for range n {
		size, ok := r.uvarint()
		if !ok {
			return false
		}
		if _, ok := r.bytes(int(size)); !ok {
			return false
		}
	}
	nDups, ok := r.uvarint()
	return ok && nDups == 0 && len(r.buf) == 0
}

// A snapshot is a row of length-prefixed columns, and each of the ways that can
// be wrong has to be refused rather than half-read.
//
// These are failure modes the format did not have before columns: interleaved
// fields end where the runs end, but a column carries its own length and can
// claim more bytes than exist, or fewer than the runs need, or more than they
// use. The last is the one worth stating: bytes left over in a column describe
// runs the count did not claim, which would make two byte strings for one
// document.
//
// Both column counts are exercised, because there are two: version 8 writes
// twelve and version 9 a thirteenth, and a document is written in whichever it
// needs. Running these against one of them only would leave the other's column
// bookkeeping unchecked -- and the header walk these all depend on has one more
// field to step over in version 9, which is precisely the mistake that has been
// made here before.
func TestLoadRejectsMalformedColumns(t *testing.T) {
	for _, fixture := range []struct {
		name string
		b    runBuilder
	}{
		{"version 8, twelve columns", wellFormedRun()},
		{"version 9, thirteen columns", purgedRun()},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			rejectsMalformedColumns(t, fixture.b.build())
		})
	}
}

func rejectsMalformedColumns(t *testing.T, good []byte) {
	t.Helper()
	if _, err := Load(2, good); err != nil {
		t.Fatalf("the fixture does not load: %v", err)
	}
	colsAt := columnsStart(t, good)

	tests := []struct {
		name  string
		alter func([]byte) []byte
	}{
		{"a column claiming more bytes than the snapshot holds", func(b []byte) []byte {
			out := append([]byte{}, b[:colsAt]...)
			out = binary.AppendUvarint(out, 1<<20)
			return append(out, b[colsAt+1:]...)
		}},
		{"a column cut short", func(b []byte) []byte {
			// Drop the last byte of the last column, so its length overruns.
			return b[:len(b)-2]
		}},
		{"a column of no bytes at all", func(b []byte) []byte {
			// Not even the encoding byte. A column is at least that, so this is
			// a missing column rather than an empty one.
			out := append([]byte{}, b[:colsAt]...)
			out = binary.AppendUvarint(out, 0)
			return append(out, b[colsAt+1:]...)
		}},
		{"a column that runs out before the runs do", func(b []byte) []byte {
			// The run count still says one, and the column of sites holds no
			// values: the field the first run needs is simply not there.
			// Nothing about the length is wrong, which is what makes this a
			// different refusal from the ones around it.
			head, cols, tail := splitColumns(t, b)
			cols[0] = []byte{columnGroups}
			return joinColumns(head, cols, tail)
		}},
		{"a column with values to spare", func(b []byte) []byte {
			// Two sites where there is one run: the column is longer than the
			// count consumes, and what is left over is a run nobody claimed.
			head, cols, tail := splitColumns(t, b)
			cols[0] = append([]byte{columnGroups}, groupColumn([]uint64{1, 2})...)
			return joinColumns(head, cols, tail)
		}},
		{"an encoding nothing defines", func(b []byte) []byte {
			head, cols, tail := splitColumns(t, b)
			cols[0][0] = columnDeflated + 1
			return joinColumns(head, cols, tail)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Load(2, tt.alter(append([]byte{}, good...))); !errors.Is(err, ErrMalformed) {
				t.Fatalf("Load() = %v, want ErrMalformed", err)
			}
		})
	}
}

// columnsStart walks a snapshot's header byte by byte and says where the
// columns begin.
//
// Walking it is not optional and it is not a formality: a byte counted wrong
// lands every alteration above in the wrong field, and the loader then refuses
// for a reason none of the cases is about — so the tests keep passing while
// testing nothing. It happened here once, to a walk that read the collection
// tally and the purge floor as column lengths and every column after them from
// the wrong offset, and the case still passed, because landing anywhere in a
// snapshot produces some rejection. Every field the header grows has to be
// stepped over here.
//
// Which fields are there depends on the version — version 6 added the
// collection floor and the tallies, version 9 the purge floor — so the version
// in the bytes decides, and a fixture written in one version cannot be walked
// by a header written for another.
func columnsStart(t *testing.T, snap []byte) int {
	t.Helper()
	version := snap[len(snapshotMagic)]
	r := &reader{buf: snap[len(snapshotMagic)+1:]}
	nSites, _ := r.uvarint()
	for range nSites {
		r.uvarint()
		r.uvarint()
	}
	if version >= snapshotVersionV6 {
		nFloor, _ := r.uvarint()
		for range nFloor {
			r.uvarint()
			r.uvarint()
		}
		nGone, _ := r.uvarint()
		for range nGone {
			r.uvarint()
			r.uvarint()
		}
	}
	if version >= snapshotVersion {
		r.uvarint() // version 9: the purge floor
	}
	r.uvarint() // the run count
	return len(snap) - len(r.buf)
}

// snapshotColumns is how many columns a snapshot of this version holds, which
// version 9 grows by one. splitColumns has to know, or it reads the duplicate
// deletion table as a thirteenth column and puts the snapshot back wrong.
func snapshotColumns(snap []byte) int {
	c := columns{withPurged: snap[len(snapshotMagic)] >= snapshotVersion}
	return len(c.all())
}

// splitColumns takes a version 8 snapshot apart into what comes before the
// columns, the twelve column payloads, and what comes after them, so that a
// test can replace one column without counting bytes around it.
func splitColumns(t *testing.T, snap []byte) ([]byte, [][]byte, []byte) {
	t.Helper()
	at := columnsStart(t, snap)
	r := &reader{buf: snap[at:]}
	var cols [][]byte
	for range snapshotColumns(snap) {
		n, ok := r.uvarint()
		if !ok {
			t.Fatalf("the snapshot has no length for column %d", len(cols))
		}
		buf, ok := r.bytes(int(n))
		if !ok {
			t.Fatalf("column %d claims %d bytes and has fewer", len(cols), n)
		}
		cols = append(cols, append([]byte{}, buf...))
	}

	// What is left has to be exactly the duplicate-deletion table, ending where
	// the snapshot ends. This is what says the walk above landed on a column
	// boundary rather than near one, and nothing else here would notice: a
	// header field stepped over wrongly puts every column length one byte out,
	// the columns still come apart into plausible slices, and the alteration a
	// test then makes lands in some other field -- where the loader refuses
	// anyway, for a reason the test is not about. Removing the purge floor from
	// the walk above passed every case in this file before this was added.
	left := &reader{buf: r.buf}
	n, ok := left.uvarint()
	if !ok || n > uint64(len(left.buf)) {
		t.Fatalf("the columns did not end on the duplicate-deletion table: %d bytes left over",
			len(r.buf))
	}
	for range n {
		for range 4 {
			if _, ok := left.uvarint(); !ok {
				t.Fatal("the duplicate-deletion table runs short, so the columns ended in the wrong place")
			}
		}
	}
	if len(left.buf) != 0 {
		t.Fatalf("the split left %d bytes past the duplicate-deletion table", len(left.buf))
	}
	return snap[:at], cols, r.buf
}

// joinColumns puts back what splitColumns took apart.
func joinColumns(head []byte, cols [][]byte, tail []byte) []byte {
	out := append([]byte{}, head...)
	for _, c := range cols {
		out = binary.AppendUvarint(out, uint64(len(c)))
		out = append(out, c...)
	}
	return append(out, tail...)
}

// Truncating a version 3 snapshot anywhere must fail, as truncating a version 1
// or a version 2 one must. It is also the only thing that exercises reading a
// run header from a version that wrote it in full rather than in columns and
// stopped mid-field.
func TestLoadRejectsTruncatedVersionThreeSnapshots(t *testing.T) {
	encoded, err := os.ReadFile("testdata/v3-snapshot.base64")
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		t.Fatalf("decoding the fixture: %v", err)
	}
	for n := range len(raw) {
		if _, err := Load(2, raw[:n]); err == nil {
			t.Fatalf("Load(%d of %d bytes) succeeded, want an error", n, len(raw))
		}
	}
}

// A deletion whose fields run out part way through. The count says there is a
// stretch to read and the gap is there to be read, so the refusal has to come
// from the field that is missing rather than from a length.
func TestLoadRejectsADeletionCutOffMidField(t *testing.T) {
	good := wellFormedRun().build()
	if _, err := Load(2, good); err != nil {
		t.Fatalf("the fixture does not load: %v", err)
	}
	head, cols, tail := splitColumns(t, good)
	// The column of spans, emptied. Its gap is still there, and its site and
	// its sequence step after it.
	cols[9] = []byte{columnGroups}
	if _, err := Load(2, joinColumns(head, cols, tail)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("Load() = %v, want ErrMalformed", err)
	}
}

// A version 5 snapshot — the format before collection was written down — still
// loads, and says the same thing as the same document written today. Without
// this the readers for it stop being exercised the moment the current version
// moves on, which is how a format that claims to accept an older one quietly
// stops doing so.
func TestLoadStillAcceptsVersionFive(t *testing.T) {
	old := wellFormedRun()
	old.asVersion = snapshotVersionV5
	raw := old.build()
	if raw[4] != snapshotVersionV5 {
		t.Fatalf("the fixture wrote version %d, want %d", raw[4], snapshotVersionV5)
	}
	was, err := Load(2, raw)
	if err != nil {
		t.Fatalf("a version 5 snapshot did not load: %v", err)
	}
	now, err := Load(2, wellFormedRun().build())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if was.String() != now.String() {
		t.Fatalf("version 5 reads %q, the current version %q", was.String(), now.String())
	}
	if was.Tombstones() != now.Tombstones() {
		t.Fatalf("version 5 keeps %d tombstones, the current version %d",
			was.Tombstones(), now.Tombstones())
	}
	// Re-encoding an old snapshot writes version 8: nothing was purged in it, so
	// it has nothing version 9 exists to say. See Doc.formatVersion.
	if fresh := was.Snapshot(); fresh[4] != snapshotVersionV8 {
		t.Fatalf("re-encoding wrote version %d, want %d", fresh[4], snapshotVersionV8)
	}
}

// The version 6 header is a trust boundary like every other field here. A floor
// or a tally that no document could have produced is refused rather than
// believed, because both are what the accounting below them is measured against.
func TestLoadRejectsAMalformedCollectionHeader(t *testing.T) {
	for _, c := range []struct {
		name   string
		break_ func(b *runBuilder)
	}{
		{"a floor at sequence zero", func(b *runBuilder) { b.floor = [][2]uint64{{1, 0}} }},
		{"a floor above the clock ceiling", func(b *runBuilder) { b.floor = [][2]uint64{{1, MaxClock + 1}} }},
		{"a floor naming operations the document has not seen", func(b *runBuilder) { b.floor = [][2]uint64{{1, 6}} }},
		{"a site in the floor twice", func(b *runBuilder) { b.floor = [][2]uint64{{1, 2}, {1, 3}} }},
		{"more floor entries than there are bytes", func(b *runBuilder) {
			b.floor = [][2]uint64{{1, 2}}
			b.floorCount = 1 << 20
		}},
		{"a tally of nothing", func(b *runBuilder) { b.gone = [][2]uint64{{1, 0}} }},
		{"a tally larger than the site ever issued", func(b *runBuilder) { b.gone = [][2]uint64{{1, 6}} }},
		{"a site tallied twice", func(b *runBuilder) {
			b.floor = [][2]uint64{{1, 3}}
			b.gone = [][2]uint64{{1, 1}, {1, 2}}
		}},
		{"more tallies than there are bytes", func(b *runBuilder) {
			b.gone = [][2]uint64{{1, 1}}
			b.goneCount = 1 << 20
		}},
		{"a tally the runs do not account for", func(b *runBuilder) {
			b.floor = [][2]uint64{{1, 2}}
			b.gone = [][2]uint64{{1, 1}}
		}},
		{"a tally the floor does not cover", func(b *runBuilder) { b.gone = [][2]uint64{{1, 1}} }},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := wellFormedRun()
			c.break_(&b)
			if _, err := Load(2, b.build()); !errors.Is(err, ErrMalformed) {
				t.Fatalf("Load = %v, want ErrMalformed", err)
			}
		})
	}
}

// The long branch of sortIDs orders exactly as the short one does. It has to:
// the encodings depend on that order, and a document crosses the threshold
// between them as it accumulates duplicate deletions.
func TestBothSortsAgree(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	for _, n := range []int{1, 2, 31, 32, 33, 200, 5000} {
		ids := make([]ID, n)
		for i := range ids {
			ids[i] = ID{Site: SiteID(1 + r.IntN(9)), Seq: 1 + r.Uint64()%50}
		}
		mine := append([]ID(nil), ids...)
		sortIDs(mine)

		// The same list, ordered by the branch this one did not take: an
		// insertion sort spelled out here rather than borrowed, so the two are
		// not the same code agreeing with itself.
		theirs := append([]ID(nil), ids...)
		for i := 1; i < len(theirs); i++ {
			for j := i; j > 0 && idLess(theirs[j], theirs[j-1]); j-- {
				theirs[j], theirs[j-1] = theirs[j-1], theirs[j]
			}
		}
		for i := range mine {
			if mine[i] != theirs[i] {
				t.Fatalf("%d ids: the two sorts disagree at %d: %v against %v", n, i, mine[i], theirs[i])
			}
		}
	}
}

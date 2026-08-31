package crdt

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
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
	purged                                  bool   // version 7: its characters were discarded
	purgedFlag                              uint64 // overrides the encoded flag
}

func (b runBuilder) build() []byte {
	out := append([]byte{}, "crdt"...)
	version := byte(snapshotVersion)
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
	if version >= snapshotVersion {
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
		// Version 7: the purge floor. A fixture that purged nothing writes zero,
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
	// column of its own, which is the same bytes in a different order.
	//
	// A case that asks for a clock below the sequence — which no honest replica
	// can produce — still encodes: the subtraction wraps, and the enormous
	// distance is refused against the ceiling instead of against the sequence.
	// The case still fails to load, which is what it asserts.
	var runSites, seqs, clocks, oSites, oSeqs, lengths, text, delCounts, delFields, purged []byte
	for _, r := range b.runs {
		runSites = binary.AppendUvarint(runSites, r.site)
		flag := uint64(boolByte(r.purged))
		if r.purgedFlag != 0 {
			flag = r.purgedFlag
		}
		purged = binary.AppendUvarint(purged, flag)
		seqs = binary.AppendUvarint(seqs, zigzag(int64(r.seq)-int64(lastRun[SiteID(r.site)])))
		lastRun[SiteID(r.site)] = r.seq
		clocks = binary.AppendUvarint(clocks, r.clock-r.seq)
		oSites = binary.AppendUvarint(oSites, r.originSite)
		oSeqs = binary.AppendUvarint(oSeqs, zigzag(int64(r.originSeq)-int64(lastOrigin[SiteID(r.originSite)])))
		lastOrigin[SiteID(r.originSite)] = r.originSeq
		length := r.length
		if length == 0 {
			length = uint64(len(r.text))
		}
		lengths = binary.AppendUvarint(lengths, length)
		for _, ch := range r.text {
			text = binary.AppendUvarint(text, uint64(ch))
		}
		dn := r.delCount
		if dn == 0 {
			dn = len(r.dels)
		}
		delCounts = binary.AppendUvarint(delCounts, uint64(dn))
		for _, d := range r.dels {
			delFields = binary.AppendUvarint(delFields, d[0])
			delFields = binary.AppendUvarint(delFields, d[1])
			delFields = binary.AppendUvarint(delFields, d[2])
			site := SiteID(d[2])
			delFields = binary.AppendUvarint(delFields, zigzag(int64(d[3])-int64(lastDel[site])))
			lastDel[site] = d[3]
		}
	}
	// Version 5 writes each field in a column of its own, length-prefixed.
	//
	// There is no branch here for the versions before it. This builder only
	// ever produced the current format — nothing has ever set b.ver — and an
	// interleaved branch that has never run is a branch that is wrong. The
	// older formats are covered by fixtures written by the builds that
	// produced them, which is a stronger check than a builder imitating them.
	cols := [][]byte{runSites, seqs, clocks, oSites, oSeqs, lengths, text, delCounts, delFields}
	if version >= snapshotVersion {
		// Version 7's addition, and the reader asks for it by version, so a
		// fixture that did not write it would be short a column rather than
		// describing an older format.
		cols = append(cols, purged)
	}
	for _, col := range cols {
		out = binary.AppendUvarint(out, uint64(len(col)))
		out = append(out, col...)
	}

	out = binary.AppendUvarint(out, 0) // no duplicate deletions
	return append(out, b.tail...)
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
	if fresh[4] != snapshotVersion {
		t.Fatalf("re-encoding wrote format version %d, want %d", fresh[4], snapshotVersion)
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
	if fresh[4] != snapshotVersion {
		t.Fatalf("re-encoding wrote format version %d, want %d", fresh[4], snapshotVersion)
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
	if fresh[4] != snapshotVersion {
		t.Fatalf("re-encoding wrote format version %d, want %d", fresh[4], snapshotVersion)
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

// A version 5 snapshot is nine length-prefixed columns, and each of the ways
// that can be wrong has to be refused rather than half-read.
//
// These are failure modes the format did not have before: interleaved fields
// end where the runs end, but a column carries its own length and can claim
// more bytes than exist, or fewer than the runs need, or more than they use.
// The last is the one worth stating: bytes left over in a column describe runs
// the count did not claim, which would make two byte strings for one document.
func TestLoadRejectsMalformedColumns(t *testing.T) {
	good := wellFormedRun().build()
	if _, err := Load(2, good); err != nil {
		t.Fatalf("the fixture does not load: %v", err)
	}

	// Where the columns start: past the magic, the version, the version-vector
	// table and the run count.
	head := len(snapshotMagic) + 1
	r := &reader{buf: good[head:]}
	nSites, _ := r.uvarint()
	for range nSites {
		r.uvarint()
		r.uvarint()
	}
	// Version 6 puts the collection floor and the per-site tallies here, both
	// empty for this fixture. Walking past them is not optional: a byte counted
	// wrong lands every alteration below in the wrong field, and the loader then
	// refuses for a reason none of these cases is about.
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
	r.uvarint() // version 7: the purge floor
	r.uvarint() // the run count
	colsAt := len(good) - len(r.buf)

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
		{"a column that runs out before the runs do", func(b []byte) []byte {
			// The run count still says one, and the column of sites is empty:
			// the field the first run needs is simply not there. Nothing about
			// the length is wrong, which is what makes this a different
			// refusal from the three around it.
			out := append([]byte{}, b[:colsAt]...)
			out = binary.AppendUvarint(out, 0) // an empty sites column
			return append(out, b[colsAt+2:]...)
		}},
		{"a column with bytes to spare", func(b []byte) []byte {
			// One more site than there are runs: the column is longer than the
			// count consumes, and what is left over is a run nobody claimed.
			out := append([]byte{}, b[:colsAt]...)
			out = binary.AppendUvarint(out, 2) // the sites column, now two bytes
			out = append(out, b[colsAt+1], 9)  // its byte, and one nobody reads
			return append(out, b[colsAt+2:]...)
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
// stretch to read and the column has room for a byte of it, so the refusal has
// to come from the field that is missing rather than from the length.
func TestLoadRejectsADeletionCutOffMidField(t *testing.T) {
	b := wellFormedRun()
	good := b.build()
	if _, err := Load(2, good); err != nil {
		t.Fatalf("the fixture does not load: %v", err)
	}
	// The deletion-fields column is the last one; keep one byte of it.
	head := len(snapshotMagic) + 1
	r := &reader{buf: good[head:]}
	nSites, _ := r.uvarint()
	for range nSites {
		r.uvarint()
		r.uvarint()
	}
	r.uvarint()
	at := len(good) - len(r.buf)
	for range 8 { // the eight columns before the deletion fields
		n, _ := r.uvarint()
		r.bytes(int(n))
	}
	at = len(good) - len(r.buf)
	cut := append([]byte{}, good[:at]...)
	cut = binary.AppendUvarint(cut, 1) // one byte of deletion fields
	cut = append(cut, good[at+1])      // the gap, and nothing after it
	cut = binary.AppendUvarint(cut, 0) // no duplicate deletions
	if _, err := Load(2, cut); !errors.Is(err, ErrMalformed) {
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
	// Re-encoding an old snapshot writes the current version, and nothing was
	// collected in it, so its floor is empty.
	if fresh := was.Snapshot(); fresh[4] != snapshotVersion {
		t.Fatalf("re-encoding wrote version %d, want %d", fresh[4], snapshotVersion)
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

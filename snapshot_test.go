package crdt

import (
	"encoding/binary"
	"errors"
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
		version: snapshotVersion,
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

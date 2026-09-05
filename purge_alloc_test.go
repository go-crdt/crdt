package crdt

import (
	"bytes"
	"errors"
	"runtime"
	"strings"
	"testing"
)

// A purged run says how many characters it stands for and carries none of them.
// Reading one must therefore cost what it holds — a block, an origin, the
// deletions that explain it — and not what it says, because what it says is one
// uvarint and a version vector entry away from any number at all.
//
// The snapshot below is ninety-odd bytes and stands for a million characters,
// every one of them deleted and discarded. It is not malformed: it is
// byte-for-byte what Purge writes for a document somebody really typed and
// really deleted, so refusing it is not an option — reading it cheaply is.
func TestLoadDoesNotBuildAPurgedRunToThrowItAway(t *testing.T) {
	const length = 1 << 20
	b := purgedRun()
	b.sites = [][2]uint64{{1, 2 * length}} // length insertions, then length deletions
	b.purgedBelow = length                 // the clock of the last character discarded
	b.runs[0].length = length
	b.runs[0].dels = [][4]uint64{{0, length, 1, length + 1}} // gap 0, all of them, from length+1@1
	in := b.build()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	d, err := Load(2, in)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// The document it produces is the one the slow path produced.
	if got := d.String(); got != "" {
		t.Fatalf("String() = %q, want the empty string", got)
	}
	if got, want := d.Tombstones(), length; got != want {
		t.Fatalf("Tombstones() = %d, want %d", got, want)
	}
	if got, want := d.PurgedBelow(), uint64(length); got != want {
		t.Fatalf("PurgedBelow() = %d, want %d", got, want)
	}
	if !bytes.Equal(d.Snapshot(), in) {
		t.Fatal("re-encoding the loaded document did not reproduce the snapshot it came from")
	}

	// And what it cost to read is measured in bytes of input, not in characters
	// the input names. Allocation rather than time: the number does not move
	// with what else this machine is doing.
	got := after.TotalAlloc - before.TotalAlloc
	const ceiling = 1 << 20 // eleven thousand times the input, and 250x under the fault
	t.Logf("a %d-byte snapshot naming %d purged characters cost %d bytes to read",
		len(in), length, got)
	if got > ceiling {
		t.Fatalf("reading a %d-byte snapshot allocated %d bytes (%.1f MiB), want at most %d: "+
			"a purged run is being materialised character by character before being thrown away",
			len(in), got, float64(got)/(1<<20), ceiling)
	}
}

// The fixture above is not a forgery, and this is how that is known: a document
// really typed, really deleted and really purged writes the same bytes. So no
// ceiling on a purged run's length can tell the two apart -- there are not two.
// What can be told apart is what reading one costs, which is what the test
// above measures.
func TestAPurgedDocumentSnapshotsToAlmostNothing(t *testing.T) {
	const length = 20000
	real := New(1)
	if _, err := real.Insert(0, strings.Repeat("a", length)); err != nil {
		t.Fatal(err)
	}
	if _, err := real.Delete(0, length); err != nil {
		t.Fatal(err)
	}
	if got := real.Purge(); got != length {
		t.Fatalf("Purge() discarded %d characters, want %d", got, length)
	}
	b := purgedRun()
	b.sites = [][2]uint64{{1, 2 * length}}
	b.purgedBelow = length
	b.runs[0].length = length
	b.runs[0].dels = [][4]uint64{{0, length, 1, length + 1}}
	if !bytes.Equal(b.build(), real.Snapshot()) {
		t.Fatalf("the fixture is %d bytes and what Purge writes is %d, and they differ",
			len(b.build()), len(real.Snapshot()))
	}
	t.Logf("a real %d-character document, typed and deleted and purged, is %d bytes",
		length, len(real.Snapshot()))
}

// A delRange holds its offsets in thirty-two bits, and block.size reads the
// last one as the run's length. A run longer than that is described by records
// whose arithmetic has already wrapped -- and they can be made to add up: two
// records of 2^31 cover 2^32 characters as far as the sum is concerned, while
// the second one's end truncates to zero. Before the length was refused
// outright the reader was saved from this by running out of memory first, which
// is not a check.
func TestLoadRefusesAPurgedRunLongerThanItsDeletionsCanDescribe(t *testing.T) {
	const length = 1 << 32
	const half = length / 2
	wide := purgedRun()
	wide.sites = [][2]uint64{{1, 2 * length}}
	wide.purgedBelow = length
	wide.runs[0].length = length
	wide.runs[0].dels = [][4]uint64{
		{0, half, 1, length + 1},
		{0, half, 1, length + 1 + half},
	}
	if _, err := Load(2, wide.build()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a purged run of 2^32 characters loaded with %v, want ErrMalformed", err)
	}
}

// Two purged runs may not claim the same operations. Each stands for a stretch
// of identities nothing else in the snapshot mentions, so overlapping stretches
// are two runs saying they are the same characters.
func TestLoadRefusesTwoPurgedRunsClaimingTheSameOperations(t *testing.T) {
	twice := purgedRun()
	twice.sites = [][2]uint64{{1, 16}}
	twice.runs = append(twice.runs, encodedRun{
		site: 1, seq: 3, clock: 3, originSite: 1, originSeq: 4,
		length: 4, purged: true,
		dels: [][4]uint64{{0, 4, 1, 9}},
	})
	if _, err := Load(2, twice.build()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("two purged runs over one stretch of operations loaded with %v, want ErrMalformed", err)
	}
}

// A character may not claim an identity a purged run claims, in either order.
// The run does not carry that character, so nothing else notices: both cases
// below spend exactly the seven operations site 1 promises, one of them twice,
// which leaves 4@1 promised and accounted for by nothing at all. Counting
// cannot see that. Putting the stretches over the identities seen on their own,
// once, at the end, can -- and has to be at the end, because whichever of the
// two came first was claimed when the other did not yet exist.
func TestLoadRefusesACharacterAndAPurgedRunOverOneOperation(t *testing.T) {
	// The purged run first: 1@1 through 3@1 discarded, deleted by 5@1 through
	// 7@1, and then 2@1 again as a character of its own.
	runFirst := runBuilder{
		sites:       [][2]uint64{{1, 7}},
		purgedBelow: 3,
		runs: []encodedRun{
			{
				site: 1, seq: 1, clock: 1,
				length: 3, purged: true,
				dels: [][4]uint64{{0, 3, 1, 5}},
			},
			{site: 1, seq: 2, clock: 5, originSite: 1, originSeq: 3, text: []rune("x")},
		},
	}
	if _, err := Load(2, runFirst.build()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a character inside a purged run's stretch loaded with %v, want ErrMalformed", err)
	}

	// And the character first, which is the order the counting cannot be made to
	// notice as it happens: 3@1 is read, and the run that swallows it arrives
	// after.
	charFirst := runBuilder{
		sites:       [][2]uint64{{1, 7}},
		purgedBelow: 6,
		runs: []encodedRun{
			{site: 1, seq: 3, clock: 3, text: []rune("x")},
			{
				site: 1, seq: 1, clock: 4, originSite: 1, originSeq: 3,
				length: 3, purged: true,
				dels: [][4]uint64{{0, 3, 1, 5}},
			},
		},
	}
	if _, err := Load(2, charFirst.build()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a purged run covering a character already read loaded with %v, want ErrMalformed", err)
	}

	// The control: the same seven operations with the character one place
	// further on, where the run does not reach. It loads, so what the two
	// refusals catch is the collision and not the shape of the fixture.
	clear := charFirst
	clear.runs = append([]encodedRun{}, charFirst.runs...)
	clear.runs[0].seq, clear.runs[0].clock = 4, 4
	clear.runs[1].originSeq = 4
	if _, err := Load(2, clear.build()); err != nil {
		t.Fatalf("the snapshot they vary from does not load: %v", err)
	}
}

// A purged run is placed by integrating its first character, and that character
// gets the check every other one gets: it has to land at the end of the
// document, because that is what says the order the snapshot states is the
// order integration produces.
//
// Here it does not. The run says it was inserted after 1@1, which is the first
// of two characters, so integration puts it between them -- and everything else
// about the snapshot is impeccable: its deletions cover it, its block is its
// own, and the six operations site 1 promises are spent exactly once each. Only
// the position is a lie, and only this check refuses it.
func TestLoadRefusesAPurgedRunThatDidNotLandAtTheEnd(t *testing.T) {
	midway := runBuilder{
		sites:       [][2]uint64{{1, 6}},
		purgedBelow: 4,
		runs: []encodedRun{
			{site: 1, seq: 1, clock: 1, text: []rune("ab")},
			{
				// A clock of 4 rather than 3, so that this run starts a block
				// of its own instead of continuing the one before it -- which
				// is what makes the position the only thing under test.
				site: 1, seq: 3, clock: 4, originSite: 1, originSeq: 1,
				length: 2, purged: true,
				dels: [][4]uint64{{0, 2, 1, 5}},
			},
		},
	}
	if _, err := Load(2, midway.build()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a purged run inserted into the middle of a run loaded with %v, want ErrMalformed", err)
	}

	// The control: the same run inserted after the second character, which is
	// where integration does put it.
	end := midway
	end.runs = append([]encodedRun{}, midway.runs...)
	end.runs[1].originSeq = 2
	if _, err := Load(2, end.build()); err != nil {
		t.Fatalf("the snapshot it varies from does not load: %v", err)
	}
}

// A purged run stands for characters at clocks its first character's does not
// reach, and the document's clock has to clear the last of them. It is not a
// bookkeeping detail: the run keeps its length, so integration still compares
// against every clock it covers, and a replica that reloads with its clock too
// low mints operations that sort inside a run they arrived after.
//
// Reading the run one character at a time raised the clock as a side effect of
// reading the last one. Reading it in one go has to say so.
func TestAReloadedPurgedRunLiftsTheDocumentClock(t *testing.T) {
	// Site 1's characters have to sit far above its own sequence numbers for
	// this to be visible at all, and that is what a second site is for: 200
	// characters from site 2 put site 1's four at clocks 201 to 204, while the
	// highest sequence number anywhere is 200.
	peer := New(2)
	ops, err := peer.Insert(0, strings.Repeat("x", 200))
	if err != nil {
		t.Fatal(err)
	}
	mine := New(1)
	if err := mine.Apply(ops...); err != nil {
		t.Fatal(err)
	}
	if _, err := mine.Insert(mine.Len(), "abcd"); err != nil {
		t.Fatal(err)
	}
	if _, err := mine.Delete(200, 4); err != nil {
		t.Fatal(err)
	}
	if got := mine.Purge(); got != 4 {
		t.Fatalf("Purge() discarded %d characters, want 4", got)
	}

	// What the last discarded character's clock was, taken from the document
	// that still remembers, so the number below is not one this test invented.
	const last = 204
	if got := mine.PurgedBelow(); got != last {
		t.Fatalf("the purge floor is %d, want %d", got, last)
	}

	back, err := Load(3, mine.Snapshot())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	next, err := back.Insert(0, "z")
	if err != nil {
		t.Fatal(err)
	}
	if next[0].Clock <= last {
		t.Fatalf("the first operation after reloading has clock %d, and the run it "+
			"was read after ends at %d: the reloaded replica mints inside a run it holds",
			next[0].Clock, last)
	}
}

// A snapshot may not claim more characters than a document can hold.
//
// Reading a purged run costs its bytes now, which is the fix above. What a
// length can still make expensive is everything sized FROM it, and the nearest
// of those is the encoder: [Doc.Snapshot] reserves 5+2*d.total before it writes
// a byte. Measured on this branch before the document-wide bound, an 83-byte
// snapshot claiming 2^32-1 purged characters read in about three kilobytes and
// then wrote itself back by reserving eight gigabytes — the bomb moved rather
// than went. On a 32-bit target the same arithmetic wraps negative and panics
// inside makeslice, out of bytes a peer sent, and this repository builds and
// vets for 386, arm and mips.
//
// The bound is the document's, not the run's, so several runs cannot do what
// one may not.
func TestLoadRefusesADocumentLongerThanOneCanBe(t *testing.T) {
	t.Run("one run past the bound", func(t *testing.T) {
		const n = maxDocumentLength + 1
		f := purgedRun()
		f.runs[0].length = n
		f.runs[0].dels = [][4]uint64{{0, n, 1, n + 1}}
		f.sites = [][2]uint64{{1, 2 * n}}
		f.purgedBelow = n + 1
		raw := f.build()
		if _, err := Load(2, raw); !errors.Is(err, ErrMalformed) {
			t.Fatalf("a %d-byte snapshot claiming %d characters loaded with %v, want ErrMalformed", len(raw), n, err)
		}
	})

	// Two runs, neither past the bound on its own, together past it: the bound
	// is the document's. Both are impeccable otherwise — the second continues
	// the first's sequence and names its last character as its origin — which
	// is what makes this about the total and nothing else.
	t.Run("two runs that together pass it", func(t *testing.T) {
		const a, b = maxDocumentLength - 1, 2
		f := twoPurgedRuns(a, b)
		raw := f.build()
		if _, err := Load(2, raw); !errors.Is(err, ErrMalformed) {
			t.Fatalf("a %d-byte snapshot claiming %d+%d characters loaded with %v, want ErrMalformed", len(raw), a, b, err)
		}
	})
}

// twoPurgedRuns is two consecutive purged runs of a and b characters from one
// site: inserts take sequence numbers 1..a+b, the deletions that follow take
// a+b+1..2(a+b), and the second run's origin is the first run's last character.
func twoPurgedRuns(a, b uint64) runBuilder {
	f := purgedRun()
	f.runs[0].length = a
	f.runs[0].dels = [][4]uint64{{0, a, 1, a + b + 1}}
	f.runs = append(f.runs, encodedRun{
		site: 1, seq: a + 1, clock: a + 1,
		originSite: 1, originSeq: a,
		length: b, purged: true,
		dels: [][4]uint64{{0, b, 1, a + b + 1 + a}},
	})
	f.sites = [][2]uint64{{1, 2 * (a + b)}}
	f.purgedBelow = a + b + 1
	return f
}

// And the bound is not so tight that it refuses what it should carry: a purged
// run of exactly the bound loads, and cheaply.
//
// It is loaded and not re-encoded here on purpose. Writing it back reserves
// 5+2*maxDocumentLength bytes -- that reservation is the whole reason the bound
// has the value it has, and making a unit test reserve a gigabyte to watch it
// succeed would be measuring the machine. That the reservation stays inside an
// int is asserted where it belongs, at compile time, beside the constant.
func TestADocumentAtTheBoundStillLoads(t *testing.T) {
	f := purgedRun()
	const n = maxDocumentLength
	f.runs[0].length = n
	f.runs[0].dels = [][4]uint64{{0, n, 1, n + 1}}
	f.sites = [][2]uint64{{1, 2 * n}}
	f.purgedBelow = n + 1
	raw := f.build()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	d, err := Load(2, raw)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("a purged run of exactly the bound was refused: %v", err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Fatalf("reading a %d-byte snapshot at the bound allocated %d bytes", len(raw), grew)
	}
	if d.Len() != 0 {
		t.Fatalf("a wholly purged document shows %d characters", d.Len())
	}
}

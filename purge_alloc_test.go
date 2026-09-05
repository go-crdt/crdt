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

// Nor may a character claim an identity a purged run has already claimed. The
// run does not carry that character, so nothing else would notice: the counts
// add up, and the document is left holding two blocks that say they are the
// same operation.
func TestLoadRefusesACharacterInsideAPurgedRunsStretch(t *testing.T) {
	inside := purgedRun()
	// 2@1 is the second character of the purged run, and here it is again.
	inside.runs = append(inside.runs, encodedRun{
		site: 1, seq: 2, clock: 2, originSite: 1, originSeq: 1,
		text: []rune("x"),
	})
	if _, err := Load(2, inside.build()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a character inside a purged run's stretch loaded with %v, want ErrMalformed", err)
	}
}

// And the same collision the other way round, which is the one the counting
// cannot see. The character is claimed first, on its own; the purged run that
// covers it is claimed after, when there is nothing left to compare it with.
// Site 1 promises seven operations and the snapshot spends seven -- one of them
// twice, so 4@1 is promised and accounted for by nothing at all.
func TestLoadRefusesAPurgedRunCoveringACharacterAlreadyRead(t *testing.T) {
	after := runBuilder{
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
	if _, err := Load(2, after.build()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a purged run covering a character already read loaded with %v, want ErrMalformed", err)
	}

	// The control: the same seven operations with the character one place
	// further on, where the run does not reach. It loads, so what the refusal
	// above catches is the collision and not the shape of the fixture.
	clear := after
	clear.runs = append([]encodedRun{}, after.runs...)
	clear.runs[0].seq, clear.runs[0].clock = 4, 4
	clear.runs[1].originSeq = 4
	if _, err := Load(2, clear.build()); err != nil {
		t.Fatalf("the snapshot it varies from does not load: %v", err)
	}
}

// A purged run is placed by integrating its first character, and that character
// gets the checks any other gets: its origin has to be a character the document
// holds.
func TestLoadRefusesAPurgedRunWithNoOrigin(t *testing.T) {
	orphan := purgedRun()
	orphan.runs[0].originSite = 1
	orphan.runs[0].originSeq = 99
	if _, err := Load(2, orphan.build()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a purged run inserted after a character that does not exist loaded with %v, want ErrMalformed", err)
	}
}

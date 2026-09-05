package crdt

import (
	"bytes"
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

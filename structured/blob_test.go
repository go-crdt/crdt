package structured

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/go-crdt/crdt"
)

func mustPut(t *testing.T, b *Blobs, name string, data []byte) []crdt.PartOps {
	t.Helper()
	ops, err := b.PutWith(name, data, FixedChunks(16))
	if err != nil {
		t.Fatal(err)
	}
	return ops
}

// filler makes bytes that compress badly and differ from one another, so a chunk
// of one file is not accidentally a chunk of another.
func filler(seed byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = seed + byte(i*7)
	}
	return out
}

func TestAFileGoesInAndComesOut(t *testing.T) {
	b := NewBlobs(1)
	data := filler(1, 100)
	ops := mustPut(t, b, "figure.png", data)

	// One operation per chunk, and the manifest last, which is the order they
	// have to be sent in.
	if len(ops) != 100/16+1+1 {
		t.Fatalf("a 100-byte file in 16-byte chunks was %d batches", len(ops))
	}
	if ops[len(ops)-1].Part != manifestPart {
		t.Fatal("the manifest is not the last batch")
	}
	got, ok := b.Get("figure.png")
	if !ok || !bytes.Equal(got, data) {
		t.Fatalf("the file came back as %d bytes (%v)", len(got), ok)
	}
	if size, ok := b.Size("figure.png"); !ok || size != 100 {
		t.Fatalf("the size reads %d (%v), want 100", size, ok)
	}
	if b.Missing("figure.png") != 0 {
		t.Fatal("a whole file reports missing chunks")
	}
	if names := b.Names(); len(names) != 1 || names[0] != "figure.png" {
		t.Fatalf("the store holds %v", names)
	}
}

// The point of content addressing. The same bytes are stored once, whoever puts
// them and under whatever name.
func TestTheSameBytesAreStoredOnce(t *testing.T) {
	b := NewBlobs(1)
	data := filler(2, 64)
	mustPut(t, b, "one.png", data)
	stored := b.Stored()

	// Again, under another name: no chunk is written.
	ops := mustPut(t, b, "two.png", data)
	if len(ops) != 1 {
		t.Fatalf("storing the same bytes again took %d batches, want just the manifest", len(ops))
	}
	if b.Stored() != stored {
		t.Fatalf("the store grew from %d chunks to %d", stored, b.Stored())
	}
	got, ok := b.Get("two.png")
	if !ok || !bytes.Equal(got, data) {
		t.Fatal("the second name does not read the same bytes")
	}
}

// A file that shares most of its chunks with one already stored costs only what
// is new — which is what a chunker cutting on the content buys, and what this
// type has to not get in the way of.
func TestOnlyWhatChangedIsWritten(t *testing.T) {
	b := NewBlobs(1)
	first := filler(3, 64)
	mustPut(t, b, "v1", first)
	stored := b.Stored()

	// The same file with its last chunk different. With fixed chunks the
	// boundaries do not move, so exactly one chunk is new.
	second := append([]byte{}, first...)
	for i := 48; i < 64; i++ {
		second[i] ^= 0xFF
	}
	ops, err := b.PutWith("v2", second, FixedChunks(16))
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 {
		t.Fatalf("a file differing in one chunk took %d batches, want the chunk and the manifest", len(ops))
	}
	if b.Stored() != stored+1 {
		t.Fatalf("the store went from %d chunks to %d, want one more", stored, b.Stored())
	}
	got, _ := b.Get("v2")
	if !bytes.Equal(got, second) {
		t.Fatal("the second version does not read back")
	}
}

// Two replicas storing the same file at once. There is no conflict to settle:
// the same bytes are the same key and the same value.
func TestTwoReplicasStoreTheSameFile(t *testing.T) {
	a, b := NewBlobs(1), NewBlobs(2)
	data := filler(4, 64)
	fromA := mustPut(t, a, "shared.png", data)
	fromB := mustPut(t, b, "shared.png", data)

	if err := a.Apply(fromB...); err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(fromA...); err != nil {
		t.Fatal(err)
	}
	for who, store := range map[string]*Blobs{"a": a, "b": b} {
		got, ok := store.Get("shared.png")
		if !ok || !bytes.Equal(got, data) {
			t.Fatalf("%s does not hold the file", who)
		}
		if store.Stored() != 4 {
			t.Fatalf("%s holds %d chunks for a four-chunk file", who, store.Stored())
		}
	}
	if !bytes.Equal(a.Snapshot(), b.Snapshot()) {
		t.Fatal("the two replicas do not agree byte for byte")
	}
}

// The property that makes a transfer resumable: a peer given a prefix of the
// batches holds what has arrived and says what has not.
func TestAPartialTransfer(t *testing.T) {
	a := NewBlobs(1)
	data := filler(5, 100)
	ops := mustPut(t, a, "big.bin", data)

	b := NewBlobs(2)
	// Everything but the last two chunks, and the manifest, which is sent last
	// and so arrives after them.
	if err := b.Apply(ops[:len(ops)-3]...); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Get("big.bin"); ok {
		t.Fatal("a file with no manifest read back")
	}
	if err := b.Apply(ops[len(ops)-1]); err != nil {
		t.Fatal(err)
	}
	// Now the manifest is here and two chunks are not.
	if size, ok := b.Size("big.bin"); !ok || size != 100 {
		t.Fatalf("the size is not known before the chunks are: %d (%v)", size, ok)
	}
	if got := b.Missing("big.bin"); got != 2 {
		t.Fatalf("%d chunks are reported missing, want 2", got)
	}
	if _, ok := b.Get("big.bin"); ok {
		t.Fatal("half a file read back as a file")
	}
	// The rest arrives.
	if err := b.Apply(ops[len(ops)-3 : len(ops)-1]...); err != nil {
		t.Fatal(err)
	}
	if b.Missing("big.bin") != 0 {
		t.Fatal("chunks are still reported missing")
	}
	got, ok := b.Get("big.bin")
	if !ok || !bytes.Equal(got, data) {
		t.Fatal("the file did not come good")
	}
}

// A chunk whose bytes are not what its key says are not the bytes handed back.
func TestAPoisonedChunkIsRefused(t *testing.T) {
	b := NewBlobs(1)
	data := filler(6, 64)
	mustPut(t, b, "figure.png", data)

	// A peer writes something else under a key that is in use.
	victim := b.chunks.Keys()[0]
	if _, err := b.chunks.Set(victim, []byte("not what the key says")); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Get("figure.png"); ok {
		t.Fatal("a file with a poisoned chunk read back")
	}
	if b.Missing("figure.png") != 1 {
		t.Fatalf("%d chunks are reported missing, want the poisoned one", b.Missing("figure.png"))
	}
	// And it repairs when the real bytes come back.
	if _, err := b.PutWith("figure.png", data, FixedChunks(16)); err != nil {
		t.Fatal(err)
	}
	got, ok := b.Get("figure.png")
	if !ok || !bytes.Equal(got, data) {
		t.Fatal("putting the file again did not repair it")
	}
}

// Replacing a file is one value, so two replicas replacing it at once give one
// whole file rather than a mixture of the two.
func TestTwoReplicasReplaceTheSameFile(t *testing.T) {
	a := NewBlobs(1)
	mustPut(t, a, "f", filler(7, 64))
	b, err := LoadBlobs(2, a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	fromA, fromB := filler(8, 64), filler(9, 48)
	mustPut(t, a, "f", fromA)
	mustPut(t, b, "f", fromB)

	if err := a.Apply(b.OpsSince(a.Version())...); err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(a.OpsSince(b.Version())...); err != nil {
		t.Fatal(err)
	}
	got, ok := a.Get("f")
	if !ok {
		t.Fatal("the file is gone")
	}
	if !bytes.Equal(got, fromA) && !bytes.Equal(got, fromB) {
		t.Fatalf("the file is neither of the two written, %d bytes", len(got))
	}
	other, _ := b.Get("f")
	if !bytes.Equal(got, other) {
		t.Fatal("the replicas hold different files")
	}
}

func TestRemovingAName(t *testing.T) {
	b := NewBlobs(1)
	data := filler(10, 32)
	mustPut(t, b, "keep", data)
	mustPut(t, b, "drop", data) // the same bytes, so the chunks are shared
	if _, err := b.Remove("drop"); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Get("drop"); ok {
		t.Fatal("a removed name still reads")
	}
	if got, ok := b.Get("keep"); !ok || !bytes.Equal(got, data) {
		t.Fatal("removing one name took the other's chunks with it")
	}
	if names := b.Names(); len(names) != 1 || names[0] != "keep" {
		t.Fatalf("the store holds %v", names)
	}
}

func TestSweepingUnreferencedChunks(t *testing.T) {
	b := NewBlobs(1)
	keep, drop := filler(11, 32), filler(12, 32)
	mustPut(t, b, "keep", keep)
	mustPut(t, b, "drop", drop)
	if b.Stored() != 4 {
		t.Fatalf("the store holds %d chunks, want 4", b.Stored())
	}
	if _, err := b.Remove("drop"); err != nil {
		t.Fatal(err)
	}

	ops, err := b.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || len(ops[0].Map) != 2 {
		t.Fatalf("the sweep was %v", ops)
	}
	if b.Stored() != 2 {
		t.Fatalf("the store holds %d chunks after sweeping, want 2", b.Stored())
	}
	if got, ok := b.Get("keep"); !ok || !bytes.Equal(got, keep) {
		t.Fatal("the sweep took a chunk that was still referred to")
	}
	// Nothing left to sweep is no operation at all.
	again, err := b.Sweep()
	if err != nil || again != nil {
		t.Fatalf("sweeping a swept store gave %v, %v", again, err)
	}
}

// The hazard the sweep documents, and what it costs. A peer puts a file whose
// chunks this replica already had, so it writes no chunk operations for them —
// and a sweep here, before that manifest arrives, leaves the peer naming chunks
// nobody holds. It is visible and it repairs.
func TestSweepingUnderAConcurrentPut(t *testing.T) {
	a := NewBlobs(1)
	data := filler(13, 32)
	mustPut(t, a, "here", data)
	b, err := LoadBlobs(2, a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}

	// b names the same bytes under another name, writing no chunks for them.
	fromB := mustPut(t, b, "mine", data)
	if len(fromB) != 1 {
		t.Fatalf("the peer wrote %d batches, want only the manifest", len(fromB))
	}
	// Meanwhile a drops its own name and sweeps.
	if _, err := a.Remove("here"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Sweep(); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(fromB...); err != nil {
		t.Fatal(err)
	}

	// The name is here, its length is known, and its chunks are not.
	if size, ok := a.Size("mine"); !ok || size != 32 {
		t.Fatalf("the name did not arrive: %d (%v)", size, ok)
	}
	if got := a.Missing("mine"); got != 2 {
		t.Fatalf("%d chunks are reported missing, want 2", got)
	}
	if _, ok := a.Get("mine"); ok {
		t.Fatal("a file whose chunks were swept read back")
	}
	// Putting it again restores exactly what went.
	if _, err := a.PutWith("mine", data, FixedChunks(16)); err != nil {
		t.Fatal(err)
	}
	got, ok := a.Get("mine")
	if !ok || !bytes.Equal(got, data) {
		t.Fatal("putting the file again did not restore it")
	}
}

func TestAFileOfNoBytes(t *testing.T) {
	b := NewBlobs(1)
	ops, err := b.Put("empty", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("an empty file took %d batches, want just the manifest", len(ops))
	}
	got, ok := b.Get("empty")
	if !ok || len(got) != 0 {
		t.Fatalf("the empty file read as %d bytes (%v)", len(got), ok)
	}
	if size, ok := b.Size("empty"); !ok || size != 0 {
		t.Fatalf("its size reads %d (%v)", size, ok)
	}
	if b.Missing("empty") != 0 {
		t.Fatal("an empty file is missing chunks")
	}
}

func TestFixedChunksCuts(t *testing.T) {
	data := filler(14, 10)
	for _, c := range []struct{ size, want int }{{0, 1}, {-1, 1}, {20, 1}, {10, 1}, {4, 3}, {1, 10}} {
		if got := len(FixedChunks(c.size)(data)); got != c.want {
			t.Fatalf("FixedChunks(%d) made %d pieces, want %d", c.size, got, c.want)
		}
	}
	// The pieces are the data, in order and complete.
	var rejoined []byte
	for _, piece := range FixedChunks(3)(data) {
		rejoined = append(rejoined, piece...)
	}
	if !bytes.Equal(rejoined, data) {
		t.Fatal("the pieces do not rejoin into what was cut")
	}
	// And a default chunker is used when none is given.
	b := NewBlobs(1)
	if _, err := b.PutWith("d", filler(15, DefaultChunkSize+1), nil); err != nil {
		t.Fatal(err)
	}
	if b.Stored() != 2 {
		t.Fatalf("a file one byte over the default was %d chunks, want 2", b.Stored())
	}
}

func TestWhatBlobsRefuse(t *testing.T) {
	b := NewBlobs(1)
	if _, err := b.Put("", []byte("x")); err == nil {
		t.Fatal("a file with no name was accepted")
	}
	if _, err := b.Remove("never stored"); err == nil {
		t.Fatal("removing a name that was never stored was accepted")
	}
	if _, ok := b.Get("never stored"); ok {
		t.Fatal("a name that was never stored read back")
	}
	if _, ok := b.Size("never stored"); ok {
		t.Fatal("a name that was never stored has a size")
	}
	if b.Missing("never stored") != 0 {
		t.Fatal("a name that was never stored is missing chunks")
	}
	// A chunker that does not hand back what it was given.
	if _, err := b.PutWith("bad", []byte("abcdef"), func([]byte) [][]byte {
		return [][]byte{[]byte("abc")}
	}); err == nil {
		t.Fatal("a chunker that dropped half the file was accepted")
	}
	if _, ok := b.Get("bad"); ok {
		t.Fatal("the refused file was stored anyway")
	}
	if _, err := LoadBlobs(1, []byte("not a snapshot")); err == nil {
		t.Fatal("loading rubbish was accepted")
	}
	if b.Composite() == nil || b.Site() != 1 {
		t.Fatal("the store does not report what it is")
	}
	if b.Pending() != 0 {
		t.Fatal("a fresh store has operations parked")
	}
	if doc := crdt.NewComposite(4); BlobsOf(doc).Site() != 4 {
		t.Fatal("BlobsOf does not read the composite it was given")
	}
}

// A peer can write any bytes as a manifest. Whatever it writes, a file either
// reads back whole or does not read back.
func TestRubbishManifests(t *testing.T) {
	b := NewBlobs(1)
	good := filler(16, 32)
	mustPut(t, b, "good", good)

	hash := sha256.Sum256([]byte("nothing stored under this"))
	for _, rubbish := range [][]byte{
		{},                            // nothing at all
		{0xFF},                        // a length that never ends
		binary.AppendUvarint(nil, 10), // a length and no count
		append(binary.AppendUvarint(binary.AppendUvarint(nil, 10), 1), 1, 2, 3),    // a hash cut short
		append(binary.AppendUvarint(binary.AppendUvarint(nil, 0), 1), hash[:]...),  // no bytes but a chunk
		binary.AppendUvarint(binary.AppendUvarint(nil, 10), 0),                     // bytes but no chunks
		append(binary.AppendUvarint(binary.AppendUvarint(nil, 10), 1), hash[:]...), // a chunk nobody holds
		append(binary.AppendUvarint(binary.AppendUvarint(nil, 99), 2), bytes.Repeat(hash[:], 2)...),
		// A manifest naming a chunk that *is* held, and lying about how long the
		// file is. Every chunk arrives, verifies, and still does not add up.
		append(binary.AppendUvarint(binary.AppendUvarint(nil, 999), 1), firstDigestOf(b, good)...),
	} {
		if _, err := b.manifest.Set("hand-written", rubbish); err != nil {
			t.Fatal(err)
		}
		if _, ok := b.Get("hand-written"); ok {
			t.Fatalf("%v read back as a file", rubbish)
		}
		if got, ok := b.Get("good"); !ok || !bytes.Equal(got, good) {
			t.Fatalf("%v cost the real file", rubbish)
		}
		// And a sweep is not confused into dropping what the real file needs.
		if _, err := b.Sweep(); err != nil {
			t.Fatal(err)
		}
		if got, ok := b.Get("good"); !ok || !bytes.Equal(got, good) {
			t.Fatalf("sweeping with %v as a manifest took the real file's chunks", rubbish)
		}
	}
}

// firstDigestOf returns the digest of the first chunk of some bytes, as the
// manifest stores it: a real chunk this store holds.
func firstDigestOf(b *Blobs, data []byte) []byte {
	sum := sha256.Sum256(FixedChunks(16)(data)[0])
	return sum[:]
}

// With no clock left, nothing is written and nothing is half-written.
func TestBlobsWithNoClockLeft(t *testing.T) {
	b := NewBlobs(1)
	data := filler(17, 64)
	mustPut(t, b, "before", data)
	mustPut(t, b, "second", data)

	top := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock,
		Key: "seed", Value: []byte("x")}
	if err := b.Apply(crdt.PartOps{Part: chunksPart, Map: []crdt.MapOp{top}}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.PutWith("after", filler(18, 64), FixedChunks(16)); err == nil {
		t.Fatal("storing with no clock left was accepted")
	}
	if _, ok := b.Get("after"); ok {
		t.Fatal("the refused file is readable")
	}
	// The manifest part has its own clock and is not exhausted, so a name can
	// still be taken away.
	if _, err := b.Remove("before"); err != nil {
		t.Fatal(err)
	}
	// Once it is exhausted too, neither removing nor sweeping can happen.
	if err := b.Apply(crdt.PartOps{Part: manifestPart, Map: []crdt.MapOp{top}}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.PutWith("x", nil, FixedChunks(16)); err == nil {
		t.Fatal("writing a manifest with no clock left was accepted")
	}
	if _, err := b.Remove("second"); err == nil {
		t.Fatal("removing a name with no clock left was accepted")
	}
	if _, ok := b.Size("second"); !ok {
		t.Fatal("a refused removal took the name anyway")
	}
	if _, err := b.Sweep(); err == nil {
		t.Fatal("sweeping with no clock left was accepted")
	}
}

// Storing a file whose manifest cannot be written leaves the chunks and no name,
// which is the one place this is not all-or-nothing.
func TestAManifestThatCannotBeWritten(t *testing.T) {
	b := NewBlobs(1)
	top := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock,
		Key: "seed", Value: []byte("x")}
	if err := b.Apply(crdt.PartOps{Part: manifestPart, Map: []crdt.MapOp{top}}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.PutWith("f", filler(19, 32), FixedChunks(16)); err == nil {
		t.Fatal("a file whose manifest could not be written was reported as stored")
	}
	if len(b.Names()) != 1 {
		t.Fatalf("the store holds %v, want only the peer's key", b.Names())
	}
	// The chunks are here and nothing refers to them, which is exactly what a
	// sweep is for — and the sweep still works, because the chunks part has
	// clock left.
	if b.Stored() != 2 {
		t.Fatalf("the store holds %d chunks", b.Stored())
	}
	if _, err := b.Sweep(); err != nil {
		t.Fatal(err)
	}
	if b.Stored() != 0 {
		t.Fatalf("%d chunks survived the sweep", b.Stored())
	}
}

// Many replicas, many files, delivered in different orders, all agreeing.
func TestRandomisedBlobsConverge(t *testing.T) {
	for seed := range uint64(20) {
		t.Run(fmt.Sprint("seed ", seed), func(t *testing.T) {
			const replicas = 4
			stores := make([]*Blobs, replicas)
			for i := range stores {
				stores[i] = NewBlobs(crdt.SiteID(i + 1))
			}
			pending := make([][]crdt.PartOps, replicas)
			// Deliberately overlapping content, so the same chunks are written
			// by more than one replica.
			for i, store := range stores {
				for f := range 3 {
					name := fmt.Sprint("f", (int(seed)+i+f)%4)
					data := filler(byte((int(seed)+f)%5), 20+f*17)
					ops, err := store.PutWith(name, data, FixedChunks(16))
					if err != nil {
						t.Fatal(err)
					}
					pending[i] = append(pending[i], ops...)
				}
			}
			for i, store := range stores {
				var inbox []crdt.PartOps
				for j, ops := range pending {
					if j != i {
						inbox = append(inbox, ops...)
					}
				}
				// Reversed, which is the order that finds a manifest arriving
				// before the chunks it names.
				for k := len(inbox) - 1; k >= 0; k-- {
					if err := store.Apply(inbox[k]); err != nil {
						t.Fatal(err)
					}
				}
				if n := store.Pending(); n != 0 {
					t.Fatalf("replica %d left %d operations parked", i, n)
				}
			}
			want := stores[0].Snapshot()
			for i, store := range stores[1:] {
				if !bytes.Equal(store.Snapshot(), want) {
					t.Fatalf("replica %d does not agree byte for byte", i+1)
				}
			}
			// And every name reads back whole on every replica.
			for _, name := range stores[0].Names() {
				first, ok := stores[0].Get(name)
				if !ok {
					t.Fatalf("%q does not read back", name)
				}
				for i, store := range stores[1:] {
					got, ok := store.Get(name)
					if !ok || !bytes.Equal(got, first) {
						t.Fatalf("replica %d reads %q differently", i+1, name)
					}
				}
			}
		})
	}
}

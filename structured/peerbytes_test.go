package structured

import (
	"crypto/sha256"
	"encoding/binary"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-crdt/crdt"
)

// What a peer can send, and what it must not be able to do with it.
//
// Every test here was a defect first. They are kept because each is one line of
// somebody else's bytes away from happening again, and because what they assert
// is not visible in the type they protect: a tree that reads, a length that is
// checked before it is believed, a site that only its own replica can speak for.

// A record keyed by the root's own identity is not a node.
//
// `TreeID{}` is `{Site: 0, Seq: 0}` and encodes to "0.0", so a peer naming a
// record that made the root a node of the tree — with itself as its parent,
// because its parent field named nothing live. Nodes() then walked from the
// root into the root, and the process died of a stack overflow, which is not a
// panic and cannot be recovered from.
func TestARecordNamedAfterTheRootIsNotANode(t *testing.T) {
	tree := NewTree(1)
	real, _, err := tree.Insert(TreeID{}, TreeID{})
	if err != nil {
		t.Fatal(err)
	}
	op := crdt.MapOp{
		Kind:  crdt.MapSet,
		ID:    crdt.ID{Site: 9, Seq: 1},
		Clock: 1,
		Key:   fieldKey(encodeID(crdt.ID{}), treeParentField),
		Value: []byte("x"),
	}
	if err := tree.Apply(op); err != nil {
		t.Fatalf("an ordinary map operation was refused: %v", err)
	}

	done := make(chan []TreeID, 1)
	go func() { done <- tree.Nodes() }()
	select {
	case nodes := <-done:
		if len(nodes) != 1 || nodes[0] != real {
			t.Fatalf("Nodes = %v, want just the one node this replica made", nodes)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Nodes did not return; the root was walked into itself")
	}
	_ = tree.Children(TreeRoot)
	if _, ok := tree.Depth(real); !ok {
		t.Fatal("the real node lost its depth")
	}
}

// The same trick against the other types built on record keys: the start
// sentinel of a sequence, and the rows and columns of a sheet, which are
// sequences. A sentinel means a PLACE — the front, the top — so a caller handed
// one as a thing would be passing "the front" back where a row was meant.
func TestARecordNamedAfterASentinelIsNotAThing(t *testing.T) {
	rootKey := encodeID(crdt.ID{})

	t.Run("sequence", func(t *testing.T) {
		s := NewSequence(1)
		real, _, err := s.Insert(SeqStart, []byte("real"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.r.SetField(rootKey, seqRankField, []byte("m")); err != nil {
			t.Fatal(err)
		}
		items := s.Items()
		if len(items) != 1 || items[0] != real {
			t.Fatalf("Items = %v, want just %v", items, real)
		}
	})

	t.Run("sheet", func(t *testing.T) {
		sh := NewSheet(1)
		row, _, err := sh.AppendRow()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sh.rows.r.SetField(rootKey, seqRankField, []byte("m")); err != nil {
			t.Fatal(err)
		}
		rows := sh.Rows()
		if len(rows) != 1 || rows[0] != row {
			t.Fatalf("Rows = %v, want just %v", rows, row)
		}
	})

	t.Run("proposals", func(t *testing.T) {
		p := NewProposals(1)
		if _, err := p.recs.SetField(rootKey, propTitleField, []byte("t")); err != nil {
			t.Fatal(err)
		}
		if got := p.List(); got != nil {
			t.Fatalf("List = %v, want nothing", got)
		}
	})
}

// Only the canonical spelling of a number is a site's key.
//
// strconv.ParseUint accepts leading zeros, so "1", "01" and "0001" all name
// site 1. Accepting all three put three readings in the list, each claiming to
// be the same replica, and whichever carried the highest vector hid the one
// that replica actually wrote.
func TestOnlyOneKeySpeaksForASite(t *testing.T) {
	r := NewMultiRegister(1)
	if _, err := r.Set([]byte("mine")); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"01", "0001", "+1", " 1"} {
		if !writable(key) {
			continue
		}
		forged := encodeReading(map[crdt.SiteID]uint64{1: 99}, []byte("forged"), false)
		if _, err := r.m.Set(key, forged); err != nil {
			t.Fatal(err)
		}
	}
	readings := r.Readings()
	if len(readings) != 1 || readings[0].Site != 1 || string(readings[0].Value) != "mine" {
		t.Fatalf("Readings = %v, want only this replica's own writing", readings)
	}
}

// A length in a peer's value is checked against the bytes that are there before
// anything is allocated for it. The rule is ParsePartOps's, in this repository's
// own words: "a count larger than the remaining bytes allow is a corrupt
// header. Refuse it before allocating for it."
func TestALengthFromAPeerIsCheckedBeforeItIsBelieved(t *testing.T) {
	t.Run("a vector of no entries at all", func(t *testing.T) {
		// Two to five bytes used to ask for a map of any size: 2^22 cost 144 MiB
		// and ten milliseconds, and it is linear, so a peer chose the number.
		for _, n := range []uint64{1 << 20, 1 << 30, 1<<64 - 1} {
			in := binary.AppendUvarint(nil, n) // and nothing after it
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			if _, _, _, ok := decodeReading(in); ok {
				t.Fatalf("a vector claiming %d entries and carrying none was accepted", n)
			}
			runtime.ReadMemStats(&after)
			if grew := int64(after.HeapAlloc) - int64(before.HeapAlloc); grew > 1<<20 {
				t.Fatalf("refusing a vector of %d entries still grew the heap by %d bytes", n, grew)
			}
		}
	})

	t.Run("a manifest whose count wraps the multiplication", func(t *testing.T) {
		// count * sha256.Size is uint64 arithmetic. 1<<59 times 32 is exactly
		// 1<<64, which is zero, so a manifest carrying no digests satisfied
		// "len(rest) == count*32" and then asked for 1<<59 strings.
		count := uint64(1) << 59
		if count*sha256.Size != 0 {
			t.Skipf("the multiplication does not wrap here: %d", count*sha256.Size)
		}
		value := binary.AppendUvarint(nil, 1)      // a size, so the size/count rule passes
		value = binary.AppendUvarint(value, count) // and a count that wraps
		if _, _, ok := decodeManifest(value); ok {
			t.Fatalf("a %d-byte manifest claiming %d chunks was accepted", len(value), count)
		}
	})

	t.Run("a manifest whose size is a number the peer liked", func(t *testing.T) {
		// One real digest, and a length saying the file is an exabyte. Get used
		// to reserve that much before looking at a single chunk.
		digest := sha256.Sum256([]byte("a chunk nobody stored"))
		value := binary.AppendUvarint(nil, 1<<60)
		value = binary.AppendUvarint(value, 1)
		value = append(value, digest[:]...)

		b := NewBlobs(1)
		if _, err := b.manifest.Set("huge.bin", value); err != nil {
			t.Fatal(err)
		}
		if size, ok := b.Size("huge.bin"); !ok || size != 1<<60 {
			t.Fatalf("Size = %d, %v — the manifest is readable, which is the point of Size", size, ok)
		}
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		if _, ok := b.Get("huge.bin"); ok {
			t.Fatal("a file whose chunks nobody holds was handed back")
		}
		runtime.ReadMemStats(&after)
		if grew := int64(after.HeapAlloc) - int64(before.HeapAlloc); grew > 1<<20 {
			t.Fatalf("refusing it still grew the heap by %d bytes", grew)
		}
	})
}

// A file that really is stored still reads, so none of the above was bought by
// refusing the ordinary case.
func TestTheGuardsDoNotRefuseARealFile(t *testing.T) {
	b := NewBlobs(1)
	data := []byte(strings.Repeat("the quick brown fox ", 500))
	if _, err := b.Put("real.bin", data); err != nil {
		t.Fatal(err)
	}
	got, ok := b.Get("real.bin")
	if !ok || string(got) != string(data) {
		t.Fatalf("a real file of %d bytes read back as %d, ok=%v", len(data), len(got), ok)
	}
	if size, ok := b.Size("real.bin"); !ok || size != len(data) {
		t.Fatalf("Size = %d, %v", size, ok)
	}
	if n := b.Missing("real.bin"); n != 0 {
		t.Fatalf("%d chunks missing from a file just stored", n)
	}
}

// Text that no block covers is reachable rather than lost.
//
// A block begins at a marker, so a document written through this type begins
// with one. Another writer can put characters in front of it, and they belong
// to no block — they used to be returned by nothing at all.
func TestTextBeforeTheFirstBlockIsNotLost(t *testing.T) {
	b := NewBlocks(1)
	id, _, err := b.Insert(DocStart, "paragraph")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.InsertText(id, 0, "in a block"); err != nil {
		t.Fatal(err)
	}
	if got := b.Preamble(); got != "" {
		t.Fatalf("a document this package wrote has a preamble: %q", got)
	}

	// Another writer, reaching past this type to the text part.
	if _, err := b.RichText().Doc().Insert(0, "orphaned"); err != nil {
		t.Fatal(err)
	}
	if got := b.Preamble(); got != "orphaned" {
		t.Fatalf("Preamble = %q, want the characters no block covers", got)
	}
	if b.Len() != 1 {
		t.Fatalf("%d blocks, want 1 — the preamble is not a block", b.Len())
	}
	if got, _ := b.Text(id); got != "in a block" {
		t.Fatalf("the real block reads %q", got)
	}
	// Everything is accounted for: the preamble, the marker, and the block.
	whole := []rune(b.RichText().Text())
	if len(whole) != len("orphaned")+1+len("in a block") {
		t.Fatalf("the text is %d characters, which nothing adds up to", len(whole))
	}
}

// The walk that reads a tree cannot recurse forever, whatever shape it is given.
//
// Tree.shape breaks every ring before it returns, so no shape it produces can
// reach this — which is exactly why the walk is a function of its own: a guard
// nothing can reach is a guard nobody can check. Handed a ring directly, it
// stops. What it prevents is a stack overflow, which kills the process.
func TestTheWalkStopsOnAShapeThatLoops(t *testing.T) {
	a := TreeID{Site: 1, Seq: 1}
	b := TreeID{Site: 1, Seq: 2}
	cases := map[string]map[TreeID][]TreeID{
		"the root is its own child": {TreeRoot: {TreeRoot}},
		"two nodes point at each other": {
			TreeRoot: {a},
			a:        {b},
			b:        {a},
		},
		"a node is its own child": {TreeRoot: {a}, a: {a}},
	}
	for name, byParent := range cases {
		t.Run(name, func(t *testing.T) {
			done := make(chan []TreeID, 1)
			go func() { done <- walkShape(byParent, TreeRoot, len(byParent)) }()
			select {
			case out := <-done:
				seen := map[TreeID]bool{}
				for _, n := range out {
					if seen[n] {
						t.Fatalf("%v is in the walk twice: %v", n, out)
					}
					seen[n] = true
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the walk did not stop")
			}
		})
	}
}

// A document whose text carries no marker at all is all preamble.
func TestATextWithNoMarkerIsAllPreamble(t *testing.T) {
	b := NewBlocks(1)
	if _, err := b.RichText().Doc().Insert(0, "nobody opened a block"); err != nil {
		t.Fatal(err)
	}
	if got := b.Preamble(); got != "nobody opened a block" {
		t.Fatalf("Preamble = %q", got)
	}
	if b.Len() != 0 || b.List() != nil {
		t.Fatalf("%d blocks in a text with no marker", b.Len())
	}
}

// A vector whose entries run out part way is refused, wherever it runs out.
func TestAVectorThatRunsOutIsRefused(t *testing.T) {
	cases := map[string][]byte{
		"no site after the count":  {1, 0x80, 0x80},
		"no count after the site":  {1, 1, 0x80},
		"the second pair is short": {2, 1, 1, 2, 0x80},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, _, ok := decodeReading(in); ok {
				t.Fatalf("%x was accepted", in)
			}
		})
	}
}

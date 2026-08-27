package crdt

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// The index in tree.go is not observable: every answer it gives, the list gives
// too, more slowly. That is exactly what makes it testable — checkIndex walks
// the list and the tree and requires them to agree, and every test here builds
// a document some way and then asks.

// checkIndex verifies that the tree holds the same blocks as the list, in the
// same order, with correct summaries and the AVL invariant.
func checkIndex(t *testing.T, d *Doc) {
	t.Helper()
	d.flush()

	order := map[*block]int{d.head: 0}
	n := 1
	for b := d.head.next; b != nil; b = b.next {
		order[b] = n
		n++
	}
	if d.tree.up != nil {
		t.Fatal("the root of the tree has a parent")
	}

	var walk func(b *block) (count, vis, sup int, min *block, height uint8)
	walk = func(b *block) (int, int, int, *block, uint8) {
		count, vis, sup, min, height := 1, b.visibleFrom(0), countAliveSup(b), b, uint8(1)
		if n := countAllSup(b); int(b.nsup) != n {
			t.Fatalf("block %v claims %d supplementary characters, holds %d", b.id, b.nsup, n)
		}
		if l := b.left; l != nil {
			if l.up != b {
				t.Fatalf("block %v is the left child of %v but does not point back", l.id, b.id)
			}
			if order[l] >= order[b] {
				t.Fatalf("block %v is left of %v but after it in the document", l.id, b.id)
			}
			c, v, s, m, h := walk(l)
			count, vis, sup, height = count+c, vis+v, sup+s, max(height, h+1)
			if sortsLower(m, min) {
				min = m
			}
		}
		if r := b.right; r != nil {
			if r.up != b {
				t.Fatalf("block %v is the right child of %v but does not point back", r.id, b.id)
			}
			if order[r] <= order[b] {
				t.Fatalf("block %v is right of %v but before it in the document", r.id, b.id)
			}
			c, v, s, m, h := walk(r)
			count, vis, sup, height = count+c, vis+v, sup+s, max(height, h+1)
			if sortsLower(m, min) {
				min = m
			}
		}
		if int(b.subVis) != vis {
			t.Fatalf("block %v claims %d visible characters below it, holds %d", b.id, b.subVis, vis)
		}
		if int(b.subSup) != sup {
			t.Fatalf("block %v claims %d visible supplementary characters below it, holds %d", b.id, b.subSup, sup)
		}
		if b.subMin != min {
			t.Fatalf("block %v claims %v sorts lowest below it, %v does", b.id, b.subMin.id, min.id)
		}
		if b.height != height {
			t.Fatalf("block %v claims height %d, has %d", b.id, b.height, height)
		}
		if l, r := heightOf(b.left), heightOf(b.right); l > r+1 || r > l+1 {
			t.Fatalf("block %v is out of balance: %d against %d", b.id, l, r)
		}
		return count, vis, sup, min, height
	}
	count, vis, sup, _, _ := walk(d.tree)
	if count != len(order) {
		t.Fatalf("the tree holds %d blocks, the list %d", count, len(order))
	}
	if vis != d.visible {
		t.Fatalf("the tree holds %d visible characters, the document reports %d", vis, d.visible)
	}
	if sup != d.sup {
		t.Fatalf("the tree holds %d visible supplementary characters, the document reports %d", sup, d.sup)
	}
}

// countAliveSup and countAllSup count what the index summarises, without using
// anything the index uses to maintain it: the deletion records are read
// directly rather than through deadIndex, and the characters one at a time
// rather than a span at a time.
func countAliveSup(b *block) int {
	n := 0
	for i, r := range b.text {
		if r > 0xFFFF && !deletedByHand(b, i) {
			n++
		}
	}
	return n
}

func countAllSup(b *block) int {
	n := 0
	for _, r := range b.text {
		if r > 0xFFFF {
			n++
		}
	}
	return n
}

func deletedByHand(b *block, i int) bool {
	for _, r := range b.dels {
		if uint32(i) >= r.from && uint32(i) < r.to {
			return true
		}
	}
	return false
}

// fragment builds a document of n characters in n runs, by inserting single
// characters at positions that never continue the run before them. Every walk
// the index is there to shorten is then as long as the document.
func fragment(t *testing.T, n int) *Doc {
	t.Helper()
	d := New(1)
	for i := range n {
		// A position that jumps about, from an integer sequence rather than a
		// generator, so a failure repeats exactly.
		pos := (i * 7919) % (i + 1)
		if _, err := d.Insert(pos, "x"); err != nil {
			t.Fatalf("Insert(%d): %v", pos, err)
		}
	}
	return d
}

func TestIndexTracksScatteredInserts(t *testing.T) {
	d := fragment(t, 400)
	checkIndex(t, d)
	if d.Len() != 400 {
		t.Fatalf("Len() = %d, want 400", d.Len())
	}
}

// A position far from the last edit is what the index is for: the walk gives up
// and the descent has to land on the same character.
func TestSeekAgreesWithTheWalk(t *testing.T) {
	d := fragment(t, 300)
	text := []rune(d.String())
	for pos := range text {
		b, i := d.seek(pos)
		if got := b.text[i]; got != text[pos] {
			t.Fatalf("seek(%d) found %q, want %q", pos, got, text[pos])
		}
		if !b.aliveAt(i) {
			t.Fatalf("seek(%d) landed on a deleted character", pos)
		}
	}
}

// Inserting far from the mark, in both directions, has to give the same text as
// the same edits applied to a string.
func TestScatteredEditsAgainstAString(t *testing.T) {
	d := New(1)
	var want []rune
	for i := range 500 {
		pos := (i * 7919) % (len(want) + 1)
		ch := rune('a' + i%26)
		if _, err := d.Insert(pos, string(ch)); err != nil {
			t.Fatalf("Insert(%d): %v", pos, err)
		}
		want = append(want[:pos:pos], append([]rune{ch}, want[pos:]...)...)
		if i%50 == 0 && len(want) > 20 {
			at := (i * 104729) % (len(want) - 1)
			if _, err := d.Delete(at, 1); err != nil {
				t.Fatalf("Delete(%d): %v", at, err)
			}
			want = append(want[:at], want[at+1:]...)
		}
	}
	if got := d.String(); got != string(want) {
		t.Fatalf("the document reads %q, want %q", got, string(want))
	}
	checkIndex(t, d)
}

// A stretch of runs with nothing visible left in it lies between the character
// being deleted and the one before it, which is more than the walk back is
// allowed to cross.
func TestDeleteAfterALongTombstonedStretch(t *testing.T) {
	d := fragment(t, 200)
	if _, err := d.Delete(1, 100); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	checkIndex(t, d)
	if _, err := d.Delete(1, 1); err != nil {
		t.Fatalf("Delete after the stretch: %v", err)
	}
	if d.Len() != 99 {
		t.Fatalf("Len() = %d, want 99", d.Len())
	}
	checkIndex(t, d)
}

// sameOrigin builds operations that all name the document root, each sorting
// before every one before it, so that integrating each has to step over
// everything already there. It is what an unfriendly peer sends.
//
// One site per operation is the shortest way to say it: a site's own operations
// are ordered, so a single site cannot claim a hundred first characters. The
// sites start above the ones the tests edit as, so that nothing here collides
// with a replica's own operations.
func sameOrigin(n int) []Op {
	ops := make([]Op, n)
	for i := range ops {
		ops[i] = Op{
			Kind:  OpInsert,
			ID:    ID{Site: SiteID(i + 10), Seq: 1},
			Clock: uint64(n - i),
			Char:  rune('a' + i%26),
		}
	}
	return ops
}

// The whole point of the index: the walk over a run of characters that all sort
// after the new one is a descent, and the document still converges to what the
// order says it should.
func TestSameOriginFloodIntegratesInOrder(t *testing.T) {
	d := New(1)
	ops := sameOrigin(200)
	if err := d.Apply(ops...); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	checkIndex(t, d)
	// Descending clocks arrive in ascending document order: the highest clock
	// sorts first at a shared origin.
	want := make([]rune, len(ops))
	for i, op := range ops {
		want[i] = op.Char
	}
	if got := d.String(); got != string(want) {
		t.Fatalf("the document reads %q, want %q", got, string(want))
	}

	// The same operations delivered to a second replica in the opposite order
	// have to produce the same document; the fallback runs on both.
	peer := New(2)
	for i := len(ops) - 1; i >= 0; i-- {
		if err := peer.Apply(ops[i]); err != nil {
			t.Fatalf("Apply: %v", err)
		}
	}
	if peer.String() != d.String() {
		t.Fatal("two replicas given the same operations in opposite orders disagree")
	}
	checkIndex(t, peer)
}

// A character that sorts after everything already at its origin lands at the end
// of the document, which is the case where the descent finds nothing to stop on.
func TestSameOriginFloodEndingAtTheEnd(t *testing.T) {
	d := New(1)
	if err := d.Apply(sameOrigin(100)...); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// The lowest clock, and the lowest site of the ones holding it, so that
	// nothing in the document sorts after it: it belongs at the very end, which
	// is where the descent finds no block to stop on.
	last := Op{Kind: OpInsert, ID: ID{Site: 2, Seq: 1}, Clock: 1, Char: 'Z'}
	if err := d.Apply(last); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := d.String(); !strings.HasSuffix(got, "Z") {
		t.Fatalf("the lowest-sorting character is not last: %q", got[len(got)-5:])
	}
	checkIndex(t, d)
}

// A run in the middle of the document that a flood has to step over, so that the
// descent starts inside the tree rather than at its rightmost edge.
func TestSameOriginFloodInsideADocument(t *testing.T) {
	d := New(1)
	if _, err := d.Insert(0, "hello world"); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	ops := must(d.OpsSince(nil))
	origin := ops[4].ID // after "hello"

	peer := New(2)
	if err := peer.Apply(ops...); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	flood := sameOrigin(120)
	for i := range flood {
		flood[i].Origin = origin
		flood[i].Clock += uint64(len(ops))
	}
	if err := peer.Apply(flood...); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	checkIndex(t, peer)

	want := make([]rune, 0, len(flood))
	for _, op := range flood {
		want = append(want, op.Char)
	}
	if got, expect := peer.String(), "hello"+string(want)+" world"; got != expect {
		t.Fatalf("the document reads %q, want %q", got, expect)
	}

	// And the replica that made the text has to reach the same place.
	if err := d.Apply(flood...); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if d.String() != peer.String() {
		t.Fatal("the two replicas disagree")
	}
	checkIndex(t, d)
}

// Two replicas editing and exchanging operations, with the index checked after
// every step. It is worth more than it looks: the summaries are wrong for a
// while before anything an assertion on the text would notice goes wrong, and
// the local and remote paths reach the index differently.
func TestIndexUnderRandomisedEditing(t *testing.T) {
	for seed := range uint64(50) {
		rng := rand.New(rand.NewPCG(seed, 0x5eed))
		docs := []*Doc{New(1), New(2)}
		inbox := [][]Op{nil, nil}
		for step := range 100 {
			i := rng.IntN(len(docs))
			d := docs[i]
			var ops []Op
			var err error
			if d.Len() == 0 || rng.IntN(3) != 0 {
				ops, err = d.Insert(rng.IntN(d.Len()+1), "ab")
			} else {
				pos := rng.IntN(d.Len())
				ops, err = d.Delete(pos, 1+rng.IntN(d.Len()-pos))
			}
			if err != nil {
				t.Fatalf("seed %d, step %d: %v", seed, step, err)
			}
			inbox[1-i] = append(inbox[1-i], ops...)
			if rng.IntN(2) == 0 {
				if err := d.Apply(inbox[i]...); err != nil {
					t.Fatalf("seed %d, step %d: %v", seed, step, err)
				}
				inbox[i] = nil
			}
			checkIndex(t, d)
			if t.Failed() {
				t.Fatalf("seed %d, step %d, replica %d", seed, step, i)
			}
		}
	}
}

// Splitting a block splits its visible-character count between the two halves.
func TestIndexAfterSplits(t *testing.T) {
	d := New(1)
	if _, err := d.Insert(0, strings.Repeat("abcdefghij", 20)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	for i := range 50 {
		if _, err := d.Insert(i*3+1, "-"); err != nil {
			t.Fatalf("Insert: %v", err)
		}
		checkIndex(t, d)
	}
	if _, err := d.Delete(10, 40); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	checkIndex(t, d)
}

// A document rebuilt from a snapshot has to carry the same index as the one it
// was taken from, deleted characters included.
func TestIndexAfterLoad(t *testing.T) {
	d := fragment(t, 150)
	if _, err := d.Delete(20, 60); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	loaded, err := Load(2, d.Snapshot())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	checkIndex(t, loaded)
	if loaded.String() != d.String() {
		t.Fatal("the reloaded document does not read the same")
	}
}

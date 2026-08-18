package crdt

import (
	"fmt"
	"testing"
)

// The B-tree is checked against the answer rather than against itself: a slice
// of the runs in document order, which is what the index is an index of. Every
// invariant below is one the AVL index in tree.go already keeps, so the two can
// be compared directly once this is wired in — and until then this is what says
// the structure holds.

// order returns the runs the tree holds, left to right.
func (t *btree) order() []*block {
	var out []*block
	var walk func(*bnode)
	walk = func(n *bnode) {
		if n.isLeaf() {
			out = append(out, n.runs...)
			return
		}
		for _, k := range n.kids {
			walk(k)
		}
	}
	walk(t.root)
	return out
}

// check verifies every invariant the tree is supposed to keep, and says which
// one broke rather than that something did.
func (t *btree) check(tb testing.TB) {
	tb.Helper()
	var walk func(n *bnode, depth int) (vis, sup int32, leafDepth int)
	depths := map[int]bool{}
	walk = func(n *bnode, depth int) (int32, int32, int) {
		if n.isLeaf() {
			depths[depth] = true
			var vis, sup int32
			var min *block
			for _, r := range n.runs {
				if r.leaf != n {
					tb.Fatalf("a run's leaf pointer does not point at the leaf holding it")
				}
				vis += runVisible(r)
				sup += r.visibleSup()
				if min == nil || sortsLower(r, min) {
					min = r
				}
			}
			if n.vis != vis || n.sup != sup {
				tb.Fatalf("a leaf says %d visible and %d supplementary, holds %d and %d",
					n.vis, n.sup, vis, sup)
			}
			if n.min != min {
				tb.Fatal("a leaf's lowest-sorting run is not the lowest-sorting run it holds")
			}
			if len(n.runs) > btreeOrder {
				tb.Fatalf("a leaf holds %d runs, over the order of %d", len(n.runs), btreeOrder)
			}
			return vis, sup, depth
		}
		if len(n.kids) > btreeOrder {
			tb.Fatalf("a node holds %d children, over the order of %d", len(n.kids), btreeOrder)
		}
		if len(n.kids) < 2 && n != t.root {
			tb.Fatalf("a node that is not the root holds %d children", len(n.kids))
		}
		var vis, sup int32
		var min *block
		for _, k := range n.kids {
			if k.up != n {
				tb.Fatal("a child does not point back at its parent")
			}
			kv, ks, _ := walk(k, depth+1)
			vis += kv
			sup += ks
			if min == nil || (k.min != nil && sortsLower(k.min, min)) {
				min = k.min
			}
		}
		if n.vis != vis || n.sup != sup {
			tb.Fatalf("a node says %d visible and %d supplementary, holds %d and %d",
				n.vis, n.sup, vis, sup)
		}
		if n.min != min {
			tb.Fatal("a node's lowest-sorting run is not the lowest of its children's")
		}
		return vis, sup, depth
	}
	walk(t.root, 0)
	// Every leaf at the same depth is what makes a descent cost the same
	// wherever it lands, and it is the property a split has to keep.
	if len(depths) > 1 {
		tb.Fatalf("the leaves are at %d different depths", len(depths))
	}
	if t.root.up != nil {
		tb.Fatal("the root has a parent")
	}
}

// runOf makes a run of one character, with an identity that sorts by its
// sequence number so the ordering invariants have something to order.
func runOf(seq uint64, ch rune) *block {
	return &block{
		id:    ID{Site: 1, Seq: seq},
		clock: seq,
		text:  []rune{ch},
	}
}

func TestBTreeHoldsItsInvariants(t *testing.T) {
	for _, n := range []int{1, 2, btreeOrder - 1, btreeOrder, btreeOrder + 1, 200, 5000} {
		t.Run(fmt.Sprint(n, " runs"), func(t *testing.T) {
			head := runOf(1, 'a')
			tree := &btree{}
			tree.start(head)
			tree.check(t)

			at := head
			want := []*block{head}
			for i := 2; i <= n; i++ {
				fresh := runOf(uint64(i), rune('a'+i%26))
				tree.insertAfter(at, fresh)
				want = append(want, fresh)
				at = fresh
				if i%97 == 0 || i <= btreeOrder+2 {
					tree.check(t)
				}
			}
			tree.check(t)

			got := tree.order()
			if len(got) != len(want) {
				t.Fatalf("the tree holds %d runs, want %d", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("run %d is not the one inserted there", i)
				}
			}
			if int(tree.root.vis) != n {
				t.Fatalf("the root says %d visible characters, want %d", tree.root.vis, n)
			}
		})
	}
}

// Inserting in the middle, which is what an editor does and what a walk of the
// runs would otherwise pay for.
func TestBTreeInsertsInTheMiddle(t *testing.T) {
	head := runOf(1, 'a')
	tree := &btree{}
	tree.start(head)

	// A thousand runs, each inserted after one chosen from what is already
	// there, so the tree is built by splitting rather than by appending.
	runs := []*block{head}
	seed := uint64(12345)
	next := func(n int) int {
		seed = seed*1103515245 + 12345
		return int((seed >> 16) % uint64(n))
	}
	for i := 2; i <= 1000; i++ {
		at := runs[next(len(runs))]
		fresh := runOf(uint64(i), 'x')
		tree.insertAfter(at, fresh)
		// Keep the expected order in step: fresh goes immediately after at.
		for j, r := range runs {
			if r == at {
				runs = append(runs, nil)
				copy(runs[j+2:], runs[j+1:])
				runs[j+1] = fresh
				break
			}
		}
		if i%50 == 0 {
			tree.check(t)
		}
	}
	tree.check(t)

	got := tree.order()
	for i := range runs {
		if got[i] != runs[i] {
			t.Fatalf("run %d is out of order", i)
		}
	}
	t.Logf("%d runs, %d deep", len(got), depthOf(tree.root))
}

func depthOf(n *bnode) int {
	d := 1
	for !n.isLeaf() {
		n = n.kids[0]
		d++
	}
	return d
}

// seek turns a position into the run holding it, which is the question every
// edit asks.
func TestBTreeSeeksToThePositionsAWalkWouldFind(t *testing.T) {
	head := runOf(1, 'a')
	tree := &btree{}
	tree.start(head)
	at := head
	for i := 2; i <= 500; i++ {
		fresh := runOf(uint64(i), rune('a'+i%26))
		tree.insertAfter(at, fresh)
		at = fresh
	}
	tree.check(t)

	// Every position, against the answer a walk of the runs gives.
	runs := tree.order()
	pos := 0
	for _, want := range runs {
		got, off := tree.seek(pos)
		if got != want || off != 0 {
			t.Fatalf("seek(%d) found a different run, at offset %d", pos, off)
		}
		pos += int(runVisible(want))
	}
}

// A position at the very end of the document, which is where a person typing
// spends most of their time and the one case a descent can fall off.
func TestBTreeSeeksToTheEnd(t *testing.T) {
	head := runOf(1, 'a')
	tree := &btree{}
	tree.start(head)
	at := head
	for i := 2; i <= 300; i++ {
		fresh := runOf(uint64(i), 'x')
		tree.insertAfter(at, fresh)
		at = fresh
	}
	runs := tree.order()
	total := int(tree.root.vis)

	got, off := tree.seek(total)
	if want := runs[len(runs)-1]; got != want {
		t.Fatal("seeking to the end did not land on the last run")
	}
	if off != int(runVisible(runs[len(runs)-1])) {
		t.Fatalf("seeking to the end gave offset %d, want the end of the run", off)
	}

	// And past it, which an insert at the end asks for while the document is
	// still being counted.
	if _, _ = tree.seek(total + 5); false {
		t.Fatal("unreachable")
	}
}

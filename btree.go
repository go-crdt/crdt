package crdt

// A B-tree over the runs, in document order, holding them in its leaves.
//
// It answers what tree.go answers — turn a position into a run, turn a UTF-16
// offset into one, and find the lowest-sorting run of a subtree so integration
// steps over stretches whole — and it is here to replace it. The case is in
// docs/performance.md and it is a size: the AVL index is a node per run, and
// those fields take a run from Go's 112-byte size class to its 160-byte one.
//
// # What it must not cost
//
// A run keeps its address. The mark, the per-site index and every walk in
// text.go hold *block and compare them, so leaves hold pointers rather than
// runs by value. That gives up some of the locality a B-tree is usually chosen
// for and keeps the change to one that can be made without rewriting the rest
// of the package around it — and the size win, which is the reason, survives:
// a run drops left, right, up, subMin, subVis, subSup and height and gains one
// pointer back to the leaf that holds it.
//
// # Why not a treap, again
//
// The same reason tree.go gives. Operations arrive from peers that need not be
// honest, so the shape cannot depend on a priority a peer can compute: a tree
// balanced by chosen priorities is a list again. A B-tree's shape is decided by
// how full its nodes are, which no peer chooses.
//
// # Not yet wired in
//
// Nothing in the package uses this. It is built and tested on its own first,
// because the wiring is the part that can break a document silently and it
// should be done against a structure already known to hold.

// btreeOrder is how many children an internal node holds and how many runs a
// leaf holds, at most. Half that, rounded up, is the minimum for every node but
// the root.
//
// Runs are only ever added and never removed — a deleted character leaves a
// tombstone, which is still a character in a run — so nodes never merge and the
// minimum is only about what a split leaves behind. The number is a guess until
// it is measured against the real trace; 32 keeps a leaf inside a few cache
// lines of pointers and gives a depth of four at a hundred thousand runs.
const btreeOrder = 32

// bnode is a node of the tree. A leaf holds runs; an internal node holds
// children. Both hold the summaries of everything below them, which is what
// makes a position a descent rather than a walk.
type bnode struct {
	// runs when this is a leaf, kids when it is not. Exactly one is non-nil.
	runs []*block
	kids []*bnode

	up *bnode

	// vis is the visible characters below this node, sup the visible
	// supplementary ones — those written as two UTF-16 code units. Both are
	// int32 for the reason tree.go gives: two billion visible characters is
	// eight gigabytes of text before anything else.
	vis int32
	sup int32

	// min is the run below this node whose first character sorts lowest. It is
	// what lets the integration walk step over a whole subtree that holds
	// nothing sorting before the character being placed.
	min *block
}

func (n *bnode) isLeaf() bool { return n.kids == nil }

// btree holds the root. A document always has at least the head sentinel in it,
// so the root is never nil once started.
type btree struct {
	root *bnode
}

// visible reports how many visible characters a run holds. It is what the
// summaries are sums of.
func runVisible(b *block) int32 { return int32(b.visibleFrom(0)) }

// start makes a tree of one run: the sentinel every document begins with.
func (t *btree) start(head *block) {
	leaf := &bnode{runs: []*block{head}, min: head}
	head.leaf = leaf
	t.root = leaf
	leaf.resum()
}

// resum recomputes a node's summaries from what is directly below it.
func (n *bnode) resum() {
	n.vis, n.sup, n.min = 0, 0, nil
	if n.isLeaf() {
		for _, r := range n.runs {
			n.vis += runVisible(r)
			n.sup += r.visibleSup()
			if n.min == nil || sortsLower(r, n.min) {
				n.min = r
			}
		}
		return
	}
	for _, k := range n.kids {
		n.vis += k.vis
		n.sup += k.sup
		if n.min == nil || (k.min != nil && sortsLower(k.min, n.min)) {
			n.min = k.min
		}
	}
}

// resumUp recomputes every summary from a node to the root. It is what an edit
// to one run costs: the height of the tree rather than the length of the
// document, and the height is four where the AVL index's was seventeen.
func (t *btree) resumUp(n *bnode) {
	for ; n != nil; n = n.up {
		n.resum()
	}
}

// insertAfter puts fresh immediately after at, splitting nodes that overflow.
//
// Runs are only ever added, never removed — a deleted character leaves a
// tombstone, which is still a character in a run — so there is no merging here
// and no rebalancing beyond what a split does.
func (t *btree) insertAfter(at, fresh *block) {
	leaf := at.leaf
	i := 0
	for ; i < len(leaf.runs); i++ {
		if leaf.runs[i] == at {
			break
		}
	}
	leaf.runs = append(leaf.runs, nil)
	copy(leaf.runs[i+2:], leaf.runs[i+1:])
	leaf.runs[i+1] = fresh
	fresh.leaf = leaf
	t.splitIfFull(leaf)
	t.resumUp(fresh.leaf)
}

// splitIfFull divides a node that has outgrown the order, and its parent if
// that overflows in turn, up to a new root.
func (t *btree) splitIfFull(n *bnode) {
	for {
		if n.isLeaf() && len(n.runs) <= btreeOrder {
			return
		}
		if !n.isLeaf() && len(n.kids) <= btreeOrder {
			return
		}
		right := &bnode{up: n.up}
		if n.isLeaf() {
			half := len(n.runs) / 2
			right.runs = append([]*block(nil), n.runs[half:]...)
			n.runs = n.runs[:half:half]
			for _, r := range right.runs {
				r.leaf = right
			}
		} else {
			half := len(n.kids) / 2
			right.kids = append([]*bnode(nil), n.kids[half:]...)
			n.kids = n.kids[:half:half]
			for _, k := range right.kids {
				k.up = right
			}
		}
		n.resum()
		right.resum()

		parent := n.up
		if parent == nil {
			// A new root, which is the only way this tree gets taller.
			parent = &bnode{kids: []*bnode{n, right}}
			n.up, right.up = parent, parent
			t.root = parent
			parent.resum()
			return
		}
		at := 0
		for ; at < len(parent.kids); at++ {
			if parent.kids[at] == n {
				break
			}
		}
		parent.kids = append(parent.kids, nil)
		copy(parent.kids[at+2:], parent.kids[at+1:])
		parent.kids[at+1] = right
		parent.resum()
		n = parent
	}
}

// seek turns a position in visible characters into the run holding it and the
// offset within that run, which is what an edit needs and what a walk would
// otherwise have to count for.
func (t *btree) seek(pos int) (*block, int) {
	n := t.root
	for !n.isLeaf() {
		last := len(n.kids) - 1
		for i, k := range n.kids {
			// The last child takes whatever is left, which is how a position
			// at the very end of the document lands on the last run rather
			// than nowhere. Written as part of the same test so that there is
			// no branch here that only an edge case reaches.
			if int32(pos) < k.vis || i == last {
				n = k
				break
			}
			pos -= int(k.vis)
		}
	}
	last := len(n.runs) - 1
	r := n.runs[last]
	for i, x := range n.runs {
		if pos < int(runVisible(x)) || i == last {
			r = x
			break
		}
		pos -= int(runVisible(x))
	}
	return r, r.visibleOffset(0, pos)
}

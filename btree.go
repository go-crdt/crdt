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

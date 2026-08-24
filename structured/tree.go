package structured

import (
	"sort"

	"github.com/go-crdt/crdt"
)

// A Tree is a collaborative tree whose nodes can be moved: a file tree, the
// outline of a document, the grouping of a diagram, a thread of replies.
//
// # Why moving is the whole problem
//
// Every other structure here is a map of records, and a tree could be one too:
// a node is a record, and one of its fields names its parent. That much merges
// on its own, because a parent is a single value and [Register] already says
// how two replicas that write one at the same time agree.
//
// What does not merge on its own is the shape. Two replicas can each move a
// node under the other, and each move is fine by itself: A under B is a tree,
// B under A is a tree, both together are a ring floating free of the root.
// Nothing in the operations is wrong, and no amount of ordering them differently
// helps — the pair is what is wrong, and it only exists once they meet.
//
// The same is true of deleting: one replica removes a folder while another
// moves a file into it, and neither operation is in conflict with the other,
// but the file is now under a node that is not there.
//
// # How this one answers
//
// Both are answered when the tree is read rather than when an operation is
// applied, by rules that are a function of the state alone. Every replica
// holding the same state reads the same tree out of it, whatever order the
// operations arrived in, which is what convergence asks for:
//
//   - A node whose parent is not a live node is read as a child of the root.
//     So deleting a folder does not delete a file a peer concurrently moved
//     into it; the file resurfaces. That direction is deliberate: a tree that
//     loses work to a concurrent delete cannot be trusted with a project.
//
//   - A node that cannot reach the root is in a ring, or below one. The ring is
//     broken at the node whose parent was written last, by the same (clock,
//     site) order the map underneath resolves writes by — the move that arrived
//     into an already-moved tree is the one that gives way — and that node is
//     read as a child of the root.
//
// Neither rule discards an operation. The state keeps every move that was ever
// made, and a later move can put back what a ring detached; what the rules
// decide is only what the tree looks like now.
//
// # Where a node sits among its siblings
//
// A node carries its order as a rank rather than living in a list per parent;
// see rank.go for why that is a single field write and a list would not be.
// Siblings are read in order of (rank, identity), which is total, so two
// replicas read the same order.
type Tree struct {
	r *RecordMap
}

// The two fields every node has. They are ordinary record fields, so a node's
// own fields are set with [Tree.SetField] and can be anything not named here.
const (
	treeParentField = "\x00parent"
	treeRankField   = "\x00rank"
)

// TreeRoot is the parent of a node at the top of the tree. It is not a node and
// has no fields; it is the name of the place a top-level node hangs from.
var TreeRoot = TreeID{}

// A TreeID names a node.
type TreeID crdt.ID

// String returns the identity in the form the rest of this package prints one.
func (t TreeID) String() string { return crdt.ID(t).String() }

// key is the identity in the form the map is keyed by, which is the form
// [decodeID] reads back and not the one String prints.
func (t TreeID) key() string { return encodeID(crdt.ID(t)) }

// IsRoot reports whether t names the root rather than a node.
func (t TreeID) IsRoot() bool { return crdt.ID(t).IsRoot() }

// NewTree returns an empty tree this site can edit.
func NewTree(site crdt.SiteID) *Tree { return &Tree{r: NewRecordMap(site)} }

// TreeOf reads a map as a tree, for a map that is a part of a [crdt.Composite].
func TreeOf(m *crdt.Map) *Tree { return &Tree{r: RecordsOf(m)} }

// Map returns the map underneath, which is what is snapshotted and what
// operations are applied to.
func (t *Tree) Map() *crdt.Map { return t.r.Map() }

// Records returns the record map underneath, for setting and reading a node's
// own fields.
func (t *Tree) Records() *RecordMap { return t.r }

// Insert adds a node under parent, after the sibling named by after. A root
// TreeID for after puts it first.
//
// It returns the new node's identity and the operations that made it.
func (t *Tree) Insert(parent, after TreeID) (TreeID, []crdt.MapOp, error) {
	fresh, mint, err := mintID(t.Map())
	if err != nil {
		return TreeID{}, nil, err
	}
	ops, err := t.place(TreeID(fresh), parent, after)
	if err != nil {
		return TreeID{}, nil, err
	}
	return TreeID(fresh), append([]crdt.MapOp{mint}, ops...), nil
}

// Move puts node under parent, after the sibling named by after. A root TreeID
// for after puts it first.
//
// Moving a node under itself or under one of its own descendants is refused
// here, because a replica can see that it is about to make a ring and there is
// no reason to make one. It is only when two replicas each make a legal move
// that a ring can appear, and that is what reading the tree resolves.
func (t *Tree) Move(node, parent, after TreeID) ([]crdt.MapOp, error) {
	if node.IsRoot() {
		return nil, crdt.ErrInvalidOp
	}
	if !t.r.HasRecord(node.key()) {
		return nil, crdt.ErrInvalidOp
	}
	for at := parent; !at.IsRoot(); {
		if at == node {
			return nil, crdt.ErrInvalidOp
		}
		up, ok := t.Parent(at)
		if !ok {
			break
		}
		at = up
	}
	return t.place(node, parent, after)
}

// Remove deletes a node. Its children are read as children of the root, by the
// rule this type states: a concurrent move into a node being deleted keeps
// what was moved.
//
// RemoveSubtree is what deleting a folder and its contents means.
func (t *Tree) Remove(node TreeID) ([]crdt.MapOp, error) {
	if node.IsRoot() || !t.r.HasRecord(node.key()) {
		return nil, crdt.ErrInvalidOp
	}
	return t.r.DeleteRecord(node.key())
}

// RemoveSubtree deletes a node and everything under it, as the tree reads now.
func (t *Tree) RemoveSubtree(node TreeID) ([]crdt.MapOp, error) {
	if node.IsRoot() || !t.r.HasRecord(node.key()) {
		return nil, crdt.ErrInvalidOp
	}
	var ops []crdt.MapOp
	// Depth-first, deepest first, so that every node is still reachable when it
	// is reached.
	var walk func(TreeID) error
	walk = func(n TreeID) error {
		for _, kid := range t.Children(n) {
			if err := walk(kid); err != nil {
				return err
			}
		}
		got, err := t.r.DeleteRecord(n.key())
		if err != nil {
			return err
		}
		ops = append(ops, got...)
		return nil
	}
	if err := walk(node); err != nil {
		return nil, err
	}
	return ops, nil
}

// place writes a node's parent and rank, which is what both adding and moving
// come down to.
func (t *Tree) place(node, parent, after TreeID) ([]crdt.MapOp, error) {
	if !parent.IsRoot() && !t.r.HasRecord(parent.key()) {
		return nil, crdt.ErrInvalidOp
	}
	lo, hi := t.between(parent, after, node)
	setParent, err := t.r.SetField(node.key(), treeParentField, []byte(parent.key()))
	if err != nil {
		return nil, err
	}
	setRank, err := t.r.SetField(node.key(), treeRankField, []byte(rankBetween(lo, hi)))
	if err != nil {
		// The parent is written and the rank is not, which is the one place
		// this is not all-or-nothing. It happens only with a single clock tick
		// left, and what it leaves is still a tree: the node is where it was
		// asked to go, keeping the rank it had, so it is read somewhere among
		// its new siblings rather than nowhere. Going where you were sent and
		// sitting in the wrong place is nearer what the caller asked for than
		// not going at all.
		return nil, err
	}
	return []crdt.MapOp{setParent, setRank}, nil
}

// between returns the ranks either side of the gap after the sibling named by
// after, skipping the node being placed — which is already there when it is
// being moved within its own parent.
func (t *Tree) between(parent, after, moving TreeID) (lo, hi string) {
	kids := t.Children(parent)
	at := -1
	if !after.IsRoot() {
		for i, kid := range kids {
			if kid == after {
				at = i
				break
			}
		}
	}
	for i := at; i >= 0; i-- {
		if kids[i] != moving {
			lo = t.rankOf(kids[i])
			break
		}
	}
	for i := at + 1; i < len(kids); i++ {
		if kids[i] != moving {
			hi = t.rankOf(kids[i])
			break
		}
	}
	return lo, hi
}

// Parent returns the node's parent as the tree reads now, and whether the node
// exists. A node at the top of the tree has the root as its parent, which is
// reported as a root TreeID and true.
func (t *Tree) Parent(node TreeID) (TreeID, bool) {
	if node.IsRoot() || !t.r.HasRecord(node.key()) {
		return TreeID{}, false
	}
	return t.shape()[node], true
}

// Children returns the children of a node, in order. Passing the root returns
// the nodes at the top of the tree.
func (t *Tree) Children(parent TreeID) []TreeID {
	shape := t.shape()
	var kids []TreeID
	for node, up := range shape {
		if up == parent {
			kids = append(kids, node)
		}
	}
	t.sortSiblings(kids)
	return kids
}

// Nodes returns every node in the tree, in depth-first order from the root,
// which is the order a file tree or an outline is read in.
func (t *Tree) Nodes() []TreeID {
	shape := t.shape()
	byParent := make(map[TreeID][]TreeID, len(shape))
	for node, up := range shape {
		byParent[up] = append(byParent[up], node)
	}
	for _, kids := range byParent {
		t.sortSiblings(kids)
	}
	return walkShape(byParent, TreeRoot, len(shape))
}

// walkShape returns everything below root, depth first.
//
// It refuses to visit a node twice, and that guard is the whole reason it is a
// function of its own rather than a closure inside [Tree.Nodes]: [Tree.shape]
// breaks every ring before it returns, so no shape it produces can make this
// recurse forever, and a guard nothing can reach is a guard nobody can check.
// Here it can be handed a ring directly.
//
// What it prevents is not a wrong answer but a stack overflow, which is not a
// panic: it kills the process rather than raising something a caller could
// handle. That happened, from one map operation a peer sent.
func walkShape(byParent map[TreeID][]TreeID, root TreeID, size int) []TreeID {
	out := make([]TreeID, 0, size)
	// The root counts as seen, so a shape naming it as its own child skips
	// rather than descends. Each node is marked before it is appended, not
	// after it is entered: marking on entry stops the recursion and still lets
	// a ring put a node in the answer twice, which is a wrong tree rather than
	// a dead process — one bug in place of the other.
	seen := make(map[TreeID]bool, size)
	seen[root] = true
	var walk func(TreeID)
	walk = func(at TreeID) {
		for _, kid := range byParent[at] {
			if seen[kid] {
				continue
			}
			seen[kid] = true
			out = append(out, kid)
			walk(kid)
		}
	}
	walk(root)
	return out
}

// Depth returns how far a node is below the root, the top of the tree being
// depth one, and whether the node exists.
func (t *Tree) Depth(node TreeID) (int, bool) {
	if node.IsRoot() || !t.r.HasRecord(node.key()) {
		return 0, false
	}
	shape := t.shape()
	depth := 1
	// Bounded for the reason [Tree.Nodes] keeps a visited set: shape is
	// acyclic, and a loop that trusts that with no bound hangs rather than
	// answers if it ever stops being so.
	for at := shape[node]; !at.IsRoot() && depth <= len(shape); at = shape[at] {
		depth++
	}
	return depth, true
}

// sortSiblings puts nodes in the order they are read in: by rank, and by
// identity where two replicas minted the same rank at the same place.
func (t *Tree) sortSiblings(nodes []TreeID) {
	rank := make(map[TreeID]string, len(nodes))
	for _, n := range nodes {
		rank[n] = t.rankOf(n)
	}
	sort.Slice(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		if rank[a] != rank[b] {
			return rank[a] < rank[b]
		}
		return idLess(crdt.ID(a), crdt.ID(b))
	})
}

func (t *Tree) rankOf(node TreeID) string {
	value, _ := t.r.GetField(node.key(), treeRankField)
	return string(value)
}

// shape returns every node's parent as the tree reads now: the parent it was
// given, unless that would leave it outside the tree, in which case the root.
//
// This is where both of the rules in the type's documentation live. It is a
// function of the state and nothing else — no wall clock, no arrival order, no
// state of its own — so two replicas holding the same operations read the same
// shape out of them.
func (t *Tree) shape() map[TreeID]TreeID {
	nodes := t.r.Records()
	stated := make(map[TreeID]TreeID, len(nodes))
	live := make(map[TreeID]bool, len(nodes))
	for _, key := range nodes {
		// decodeThing rather than decodeID: a record named after the root is
		// not a node, and treating it as one made the root its own child.
		id, ok := decodeThing(key)
		if !ok {
			continue
		}
		live[TreeID(id)] = true
	}
	for node := range live {
		up := TreeRoot
		if value, ok := t.r.GetField(node.key(), treeParentField); ok {
			if id, ok := decodeID(string(value)); ok && live[TreeID(id)] {
				// A parent that is not a live node leaves the node at the top
				// of the tree, which is the first rule.
				up = TreeID(id)
			}
		}
		stated[node] = up
	}

	// The second rule. A node that cannot reach the root is in a ring or below
	// one; the ring is broken at whichever of its nodes had its parent written
	// last, and that node is read as a child of the root.
	//
	// Breaking one ring can free a whole subtree, so this repeats until every
	// node reaches the root. Each pass detaches at least one node and a
	// detached node is never re-attached here, so it stops.
	for {
		stuck := t.unreachable(stated)
		if len(stuck) == 0 {
			return stated
		}
		stated[t.latestMoved(stuck)] = TreeRoot
	}
}

// unreachable returns the nodes that cannot reach the root by following stated.
func (t *Tree) unreachable(stated map[TreeID]TreeID) []TreeID {
	reaches := make(map[TreeID]bool, len(stated))
	var stuck []TreeID
	for node := range stated {
		// Walk up, remembering the path, until the root is reached or a node is
		// seen twice — which is the ring.
		path := map[TreeID]bool{}
		at, ok := node, false
		for {
			if at.IsRoot() {
				ok = true
				break
			}
			if answer, known := reaches[at]; known {
				ok = answer
				break
			}
			if path[at] {
				break
			}
			path[at] = true
			at = stated[at]
		}
		for seen := range path {
			reaches[seen] = ok
		}
		if !ok {
			stuck = append(stuck, node)
		}
	}
	return stuck
}

// latestMoved returns whichever of the nodes had its parent written last, by
// the (clock, site) order the map resolves concurrent writes by.
func (t *Tree) latestMoved(nodes []TreeID) TreeID {
	best := nodes[0]
	bestClock, bestSite, _ := t.stampOf(best)
	for _, node := range nodes[1:] {
		clock, site, _ := t.stampOf(node)
		if movedLater(clock, site, node, bestClock, bestSite, best) {
			best, bestClock, bestSite = node, clock, site
		}
	}
	return best
}

// movedLater is that order, written on its own so that the tie it settles can
// be tested. Two live writes never share both a clock and a site, so the last
// key is only reached for a parent field with no stamp behind it — which a peer
// can produce and which still has to give the same answer on every replica.
func movedLater(clock uint64, site crdt.SiteID, node TreeID,
	bestClock uint64, bestSite crdt.SiteID, best TreeID) bool {
	if clock != bestClock {
		return clock > bestClock
	}
	if site != bestSite {
		return site > bestSite
	}
	return idLess(crdt.ID(best), crdt.ID(node))
}

func (t *Tree) stampOf(node TreeID) (uint64, crdt.SiteID, bool) {
	return t.Map().Stamp(fieldKey(node.key(), treeParentField))
}

// SetField sets one of a node's own fields.
func (t *Tree) SetField(node TreeID, field string, value []byte) (crdt.MapOp, error) {
	if node.IsRoot() || field == treeParentField || field == treeRankField {
		return crdt.MapOp{}, crdt.ErrInvalidPart
	}
	return t.r.SetField(node.key(), field, value)
}

// GetField reads one of a node's own fields.
func (t *Tree) GetField(node TreeID, field string) ([]byte, bool) {
	return t.r.GetField(node.key(), field)
}

// Apply takes operations from a peer.
func (t *Tree) Apply(ops ...crdt.MapOp) error { return t.Map().Apply(ops...) }

// Version returns what this replica has seen.
func (t *Tree) Version() crdt.VersionVector { return t.Map().Version() }

// OpsSince returns the operations a peer at vv has not seen.
func (t *Tree) OpsSince(vv crdt.VersionVector) []crdt.MapOp { return t.Map().OpsSince(vv) }

// Snapshot returns the tree as bytes.
func (t *Tree) Snapshot() []byte { return t.Map().Snapshot() }

// LoadTree reads a snapshot back, as the given site.
func LoadTree(site crdt.SiteID, snapshot []byte) (*Tree, error) {
	m, err := crdt.LoadMap(site, snapshot)
	if err != nil {
		return nil, err
	}
	return &Tree{r: RecordsOf(m)}, nil
}

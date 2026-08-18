package crdt

// The blocks are a list, which answers "what follows this" in one step and
// everything else by walking. Editing is local, so walking is usually the right
// answer, and the mark makes it cheap. It is the wrong answer twice.
//
// A position far from the last edit — a second cursor, a replace-all, a patch
// dropped into the middle of a document, or simply a peer's operation arriving
// between two keystrokes and clearing the mark — walks every run in between.
//
// Integration walks forward from the origin over every character that sorts
// after the new one, so a peer naming one origin over and over makes the walk
// as long as the document and integration quadratic. Operations arrive from
// peers that need not be honest.
//
// The runs are therefore also held, in document order, in the leaves of a
// B-tree whose nodes each carry two summaries of what is below them: how many
// visible characters, which turns a position into a descent, and which run
// sorts lowest, which turns that integration walk into one as well.
//
// The tree is an index and no more. Document order is the list, the wire and
// snapshot formats are per character, and nothing outside this file depends on
// the shape. That shape is fixed by AVL rather than by priorities a treap would
// draw, because the input is untrusted: a priority a peer can compute is a
// priority a peer can choose, and a tree balanced by chosen priorities is a
// list again.

// Walking is cheaper than descending for the first few blocks, and a walk is
// what locality asks for. Both walks therefore start out along the list and
// turn to the tree once they have gone further than a descent would have cost:
// the common case pays nothing for the index, and the pathological one pays the
// height of the tree twice over rather than the length of the document. The
// number is that height for a document of a few tens of thousands of runs, and
// a sweep of the real editing trace found nothing to choose between 4 and 16.
const scanBudget = 16

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
// # How it replaced the AVL
//
// Both were built, and both answered every question the package asks of an
// index, over a real editing history — 104 852 positions and 10 050
// integration walks — before either was removed. Substituting an index is the
// change that corrupts a document silently, and comparing two of them on a real
// trace is the only way to find out that it would without shipping it.

// btreeOrder is how many entries a node holds, at most — children in an
// internal node, runs in a leaf.
//
// Runs are only ever added and never removed — a deleted character leaves a
// tombstone, which is still a character in a run — so nodes never merge and the
// number is only about what a split leaves behind and what a scan costs.
//
// It was 32 as a guess and is 8 as a measurement. A descent scans a node's
// entries, so a wide node trades depth for scanning; on the real editing trace
// 8 and 16 were level with each other and both beat 4 and 32 by a tenth on the
// integration walk. 8 keeps a node's counts inside one cache line.
const btreeOrder = 8

// A bkey is what a character sorts by: its clock, then the site that wrote it,
// then that site's sequence number. It is held by value beside the entry it
// belongs to, rather than read through the run, because the integration walk
// scans keys and nothing else — chasing a pointer per entry to reach one is
// what a B-tree exists to avoid.
type bkey struct {
	clock uint64
	id    ID
}

func keyOf(b *block) bkey { return bkey{b.clock, b.id} }

// lower reports whether k sorts before other.
func (k bkey) lower(other bkey) bool { return before(k.clock, k.id, other.clock, other.id) }

// after reports whether k sorts after a character with this clock and identity —
// the test the integration walk steps over entries by. Within a run the clocks
// ascend, so its first character decides for the whole of it.
func (k bkey) after(clock uint64, id ID) bool { return before(clock, id, k.clock, k.id) }

// bnode is a node of the tree. A leaf holds runs; an internal node holds
// children. Either way it holds one entry per thing below it, and the summaries
// of that thing live here rather than in it.
//
// That is the whole difference between this and a tree of pointers, and it is
// the difference that shows up in a benchmark. A descent scans vis; the
// integration walk scans keys. Both are slices of plain values laid out end to
// end, so a node is read in one or two cache lines and nothing is dereferenced
// until the entry is chosen. Holding the same numbers one level down instead
// turns each of those scans into a dependent load per entry, and turned an
// integration walk a fifth slower than the tree this replaced.
type bnode struct {
	// runs when this is a leaf, kids when it is not. Exactly one is non-nil,
	// and both are as long as the three summaries below.
	runs []*block
	kids []*bnode

	// vis is the visible characters under each entry, sup how many of those are
	// supplementary — written as two UTF-16 code units rather than one, which
	// is what turns a UTF-16 offset into a descent instead of a walk. Both are
	// int32 because two billion visible characters is eight gigabytes of text
	// before anything else.
	vis []int32
	sup []int32
	// keys is the lowest-sorting character under each entry. It is what lets
	// the integration walk step over a whole subtree holding nothing that could
	// stop it.
	keys []bkey

	up *bnode
	// slot is this node's entry in up, so a change to one run reaches the root
	// without searching for itself at every level.
	slot int32
}

func (n *bnode) isLeaf() bool { return n.kids == nil }

// count is how many entries the node holds.
func (n *bnode) count() int { return len(n.vis) }

// btree holds the root. A document always has at least the head sentinel in it,
// so the root is never nil once started.
type btree struct {
	root *bnode
}

// visible reports how many visible characters a run holds. It is what the
// summaries are sums of, and it walks the run's deletion records — which is why
// the answer is stored beside the entry rather than asked for on every descent.
func runVisible(b *block) int32 { return int32(b.visibleFrom(0)) }

// sortsLower orders two blocks by their first characters.
func sortsLower(a, b *block) bool { return before(a.clock, a.id, b.clock, b.id) }

// start makes a tree of one run: the sentinel every document begins with.
func (t *btree) start(head *block) {
	leaf := &bnode{
		runs: append(make([]*block, 0, btreeOrder+1), head),
		vis:  append(make([]int32, 0, btreeOrder+1), runVisible(head)),
		sup:  append(make([]int32, 0, btreeOrder+1), head.visibleSup()),
		keys: append(make([]bkey, 0, btreeOrder+1), keyOf(head)),
	}
	head.leaf, head.slot = leaf, 0
	t.root = leaf
}

// total returns everything the tree holds. Only a test asks: the document keeps
// its own count, and this is what that count is checked against.
func (t *btree) total() (vis, sup int32) {
	for i := range t.root.vis {
		vis += t.root.vis[i]
		sup += t.root.sup[i]
	}
	return vis, sup
}

// sum adds up a node's entries, which is what its own entry one level up says.
func (n *bnode) sum() (vis, sup int32, key bkey) {
	for i := range n.vis {
		vis += n.vis[i]
		sup += n.sup[i]
		if i == 0 || n.keys[i].lower(key) {
			key = n.keys[i]
		}
	}
	return vis, sup, key
}

// bump adds a change to one run's counts to every summary above it.
//
// It is what an edit to a single character costs, and it is why there is no
// deferred-update machinery here. The AVL index this replaced held changes back
// and carried them up lazily because its depth was seventeen and paying that
// per character was not affordable. Five is, so the summaries are always
// readable and the three fields that held a pending change are gone with the
// tree that needed them.
func (t *btree) bump(b *block, vis, sup int32) {
	n, i := b.leaf, int(b.slot)
	for {
		n.vis[i] += vis
		n.sup[i] += sup
		p := n.up
		if p == nil {
			return
		}
		i, n = int(n.slot), p
	}
}

// insertAfter puts fresh immediately after at, splitting nodes that overflow.
//
// Runs are only ever added, never removed — a deleted character leaves a
// tombstone, which is still a character in a run — so there is no merging here
// and no rebalancing beyond what a split does.
func (t *btree) insertAfter(at, fresh *block) {
	leaf := at.leaf
	i := int(at.slot) + 1
	vis, sup, key := runVisible(fresh), fresh.visibleSup(), keyOf(fresh)

	leaf.runs = insertAt(leaf.runs, i, fresh)
	leaf.vis = insertAt(leaf.vis, i, vis)
	leaf.sup = insertAt(leaf.sup, i, sup)
	leaf.keys = insertAt(leaf.keys, i, key)
	for j := i; j < len(leaf.runs); j++ {
		leaf.runs[j].leaf, leaf.runs[j].slot = leaf, int32(j)
	}

	// Carry what fresh holds up to the root before splitting, not after. A
	// split recomputes both halves from what they hold, and a node it has
	// already made correct must not then be told about fresh a second time.
	for n := leaf; n.up != nil; n = n.up {
		p, j := n.up, int(n.slot)
		p.vis[j] += vis
		p.sup[j] += sup
		if key.lower(p.keys[j]) {
			p.keys[j] = key
		}
	}
	t.splitIfFull(leaf)
}

// insertAt puts v at index i, moving what was there and after it along.
//
// Every slice here is made with room for btreeOrder+1, and a node is split
// before it can hold more, so this append never reallocates. Sizing them to
// what they hold instead — which is what capping a split's halves to their own
// length does — made every insertion recopy all four, and doubled the
// allocations of an integration walk.
func insertAt[T any](s []T, i int, v T) []T {
	s = append(s, v)
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

// splitIfFull divides a node that has outgrown the order, and its parent if
// that overflows in turn, up to a new root.
func (t *btree) splitIfFull(n *bnode) {
	for n.count() > btreeOrder {
		half := n.count() / 2
		right := &bnode{
			up:   n.up,
			vis:  append(make([]int32, 0, btreeOrder+1), n.vis[half:]...),
			sup:  append(make([]int32, 0, btreeOrder+1), n.sup[half:]...),
			keys: append(make([]bkey, 0, btreeOrder+1), n.keys[half:]...),
		}
		n.vis, n.sup, n.keys = n.vis[:half], n.sup[:half], n.keys[:half]
		if n.isLeaf() {
			right.runs = append(make([]*block, 0, btreeOrder+1), n.runs[half:]...)
			n.runs = n.runs[:half]
			for j, r := range right.runs {
				r.leaf, r.slot = right, int32(j)
			}
		} else {
			right.kids = append(make([]*bnode, 0, btreeOrder+1), n.kids[half:]...)
			n.kids = n.kids[:half]
			for j, k := range right.kids {
				k.up, k.slot = right, int32(j)
			}
		}

		parent := n.up
		if parent == nil {
			// A new root, which is the only way this tree gets taller.
			parent = &bnode{
				kids: append(make([]*bnode, 0, btreeOrder+1), n, right),
				vis:  make([]int32, 2, btreeOrder+1),
				sup:  make([]int32, 2, btreeOrder+1),
				keys: make([]bkey, 2, btreeOrder+1),
			}
			n.up, n.slot = parent, 0
			right.up, right.slot = parent, 1
			t.root = parent
			parent.vis[0], parent.sup[0], parent.keys[0] = n.sum()
			parent.vis[1], parent.sup[1], parent.keys[1] = right.sum()
			return
		}
		j := int(n.slot)
		parent.kids = insertAt(parent.kids, j+1, right)
		parent.vis = insertAt(parent.vis, j+1, int32(0))
		parent.sup = insertAt(parent.sup, j+1, int32(0))
		parent.keys = insertAt(parent.keys, j+1, bkey{})
		for k := j; k < len(parent.kids); k++ {
			parent.kids[k].up, parent.kids[k].slot = parent, int32(k)
		}
		parent.vis[j], parent.sup[j], parent.keys[j] = n.sum()
		parent.vis[j+1], parent.sup[j+1], parent.keys[j+1] = right.sum()
		n = parent
	}
}

// seek turns a position in visible characters into the run holding it and the
// offset within that run, which is what an edit needs and what a walk would
// otherwise have to count for.
func (t *btree) seek(pos int) (*block, int) {
	n := t.root
	for {
		i := pick(n.vis, &pos)
		if n.isLeaf() {
			r := n.runs[i]
			return r, r.visibleOffset(0, pos)
		}
		n = n.kids[i]
	}
}

// pick chooses the entry holding visible character pos and leaves pos as the
// offset within it.
//
// The last entry takes whatever is left, which is how a position at the very
// end of the document lands on the last run rather than nowhere. It is written
// as part of the same test so that there is no branch here that only an edge
// case reaches.
func pick(vis []int32, pos *int) int {
	last := len(vis) - 1
	for i := 0; i < last; i++ {
		if *pos < int(vis[i]) {
			return i
		}
		*pos -= int(vis[i])
	}
	return last
}

// supBefore returns how many of the document's first pos visible characters are
// supplementary — written as two UTF-16 code units rather than one.
//
// It is the same descent seek makes, taking the supplementary count of every
// entry it steps over whole, so turning a UTF-16 offset into a position costs
// the depth of the tree rather than a walk of the document.
func (t *btree) supBefore(pos int) int {
	n, sup := t.root, 0
	for {
		i := 0
		for last := n.count() - 1; i < last; i++ {
			if pos < int(n.vis[i]) {
				break
			}
			pos -= int(n.vis[i])
			sup += int(n.sup[i])
		}
		if n.isLeaf() {
			return sup + int(n.runs[i].supBefore(pos))
		}
		n = n.kids[i]
	}
}

// runeAtUnit returns the position of the character holding UTF-16 offset u, and
// whether u falls between that character's two units.
//
// The descent is by units rather than by characters — an entry spans vis+sup of
// them — and both counts are carried down, because the answer is a character
// count and the question is a unit count.
func (t *btree) runeAtUnit(u int) (int, bool) {
	n, pos := t.root, 0
	for {
		i := 0
		for last := n.count() - 1; i < last; i++ {
			units := int(n.vis[i] + n.sup[i])
			// An offset landing exactly at the end of an entry is answered
			// inside it rather than at the start of the next, so that the walk
			// below always has an offset it can reach.
			if u <= units {
				break
			}
			u -= units
			pos += int(n.vis[i])
		}
		if n.isLeaf() {
			k, split := n.runs[i].runeAtUnit(u)
			return pos + k, split
		}
		n = n.kids[i]
	}
}

// lastOver returns the run the integration walk from at ends on: the last one
// whose characters all sort after a character with this clock and identity. The
// new character goes after it.
func (t *btree) lastOver(at *block, clock uint64, id ID) *block {
	stop := t.stopRun(at, clock, id)
	if stop == nil {
		return t.last()
	}
	return stop.prev
}

// stopRun returns the first run after at that does not sort after a character
// with this clock and identity, or nil if no run does.
//
// The candidates are the runs the walk would have visited, in the order it
// would have visited them: the rest of at's own leaf, then the later entries of
// each node on the way to the root. An entry whose lowest-sorting character
// still sorts after the one being placed holds nothing to stop on and is
// stepped over whole, which is what makes this the depth of the tree rather
// than the length of the document.
func (t *btree) stopRun(at *block, clock uint64, id ID) *block {
	n, from := at.leaf, int(at.slot)+1
	for {
		for i := from; i < len(n.keys); i++ {
			if !n.keys[i].after(clock, id) {
				if n.isLeaf() {
					return n.runs[i]
				}
				return descendStopIn(n.kids[i], clock, id)
			}
		}
		if n.up == nil {
			return nil
		}
		n, from = n.up, int(n.slot)+1
	}
}

// descendStopIn returns the first run of n in document order that does not sort
// after a character with this clock and identity. The subtree is known to hold
// one, which is what lets the scan take the first entry that could hold it and
// never look at the rest.
func descendStopIn(n *bnode, clock uint64, id ID) *block {
	for {
		for i := range n.keys {
			if !n.keys[i].after(clock, id) {
				if n.isLeaf() {
					return n.runs[i]
				}
				n = n.kids[i]
				break
			}
		}
	}
}

// last returns the last run in document order.
func (t *btree) last() *block {
	n := t.root
	for !n.isLeaf() {
		n = n.kids[len(n.kids)-1]
	}
	return n.runs[len(n.runs)-1]
}

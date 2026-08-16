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
// The same blocks are therefore also the nodes of an AVL tree in document
// order, each holding two summaries of its subtree: how many visible characters
// it contains, which turns a position into a descent, and which of its blocks
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

// subVisOf and heightOf read a summary through a child that may not be there,
// which is most of what the code below would otherwise spend its lines on.
func subVisOf(b *block) int32 {
	if b == nil {
		return 0
	}
	return b.subVis
}

func heightOf(b *block) uint8 {
	if b == nil {
		return 0
	}
	return b.height
}

// sortsLower orders two blocks by their first characters.
func sortsLower(a, b *block) bool { return before(a.clock, a.id.Site, b.clock, b.id.Site) }

// sortsAfter reports whether b's first character sorts after a character with
// this clock and site — the test the integration walk steps over runs by. Within
// a run the clocks ascend, so the first character decides for the whole of it.
func sortsAfter(b *block, clock uint64, site SiteID) bool {
	return before(clock, site, b.clock, b.id.Site)
}

// pullMin recomputes which block of b's subtree sorts lowest. A block's own key
// never changes — the clock and identity of its first character are fixed when
// it is created — so this is only ever needed where a subtree changed hands.
func (b *block) pullMin() {
	m := b
	if l := b.left; l != nil && sortsLower(l.subMin, m) {
		m = l.subMin
	}
	if r := b.right; r != nil && sortsLower(r.subMin, m) {
		m = r.subMin
	}
	b.subMin = m
}

func (b *block) fixHeight() { b.height = 1 + max(heightOf(b.left), heightOf(b.right)) }

// startIndex makes the sentinel the whole tree. Every other block joins it after
// one already there, so this is the only block that ever has to be put in.
func (d *Doc) startIndex() {
	d.head.subMin = d.head
	d.head.height = 1
	d.tree = d.head
}

// index puts fresh into the tree immediately after at in document order, which
// is where the list has just put it.
func (d *Doc) index(at, fresh *block) {
	fresh.subVis = int32(fresh.visibleFrom(0))
	fresh.subMin = fresh
	fresh.height = 1
	// Immediately after a block with a right subtree is the leftmost block of
	// that subtree, on its empty left; after one without is the empty right.
	parent := at
	if at.right == nil {
		at.right = fresh
	} else {
		parent = at.right
		for parent.left != nil {
			parent = parent.left
		}
		parent.left = fresh
	}
	fresh.up = parent
	d.repair(parent, fresh)
}

// repair walks from p to the root, where fresh has just been added below: every
// subtree on the way holds fresh's visible characters as well, may have fresh as
// its lowest-sorting block now, and may have grown too tall on one side.
//
// The counts have to be carried the whole way, but the balance rarely does: one
// added block changes the height of at most the subtrees below the first one
// whose height it left alone. Past that the walk touches nothing but the blocks
// on the path itself.
func (d *Doc) repair(p, fresh *block) {
	// Read once: a rotation on the way up can make fresh the root of a subtree,
	// and its count then stands for that whole subtree rather than for itself.
	delta := fresh.subVis
	balancing := true
	for p != nil {
		up := p.up // a rotation moves p, so the way out is taken before it happens
		p.subVis += delta
		if sortsLower(fresh, p.subMin) {
			p.subMin = fresh
		}
		if balancing {
			balancing = d.rebalance(p)
		}
		p = up
	}
}

// rebalance restores the AVL invariant at p, which is at most one rotation away
// from holding because one block was added below it, and reports whether the
// height of p's subtree changed.
func (d *Doc) rebalance(p *block) bool {
	l, r := heightOf(p.left), heightOf(p.right)
	switch {
	case l > r+1:
		if heightOf(p.left.left) < heightOf(p.left.right) {
			d.rotateLeft(p.left)
		}
		d.rotateRight(p)
	case r > l+1:
		if heightOf(p.right.right) < heightOf(p.right.left) {
			d.rotateRight(p.right)
		}
		d.rotateLeft(p)
	default:
		was := p.height
		p.height = 1 + max(l, r)
		return p.height != was
	}
	// A rotation gives the subtree back the height it had before the block was
	// added, so nothing above it can be out of balance.
	return false
}

// rotateRight makes x's left child the root of x's subtree.
func (d *Doc) rotateRight(x *block) {
	y := x.left
	x.left = y.right
	if y.right != nil {
		y.right.up = x
	}
	y.right = x
	d.reparent(x, y)
	x.up = y
	// A rotation moves whole subtrees, so the counts follow from what changed
	// hands: y ends up holding exactly what x held, and x keeps the rest.
	y.subVis, x.subVis = x.subVis, x.subVis-y.subVis+subVisOf(x.left)
	x.fixHeight()
	y.fixHeight()
	x.pullMin()
	y.pullMin()
}

// rotateLeft makes x's right child the root of x's subtree.
func (d *Doc) rotateLeft(x *block) {
	y := x.right
	x.right = y.left
	if y.left != nil {
		y.left.up = x
	}
	y.left = x
	d.reparent(x, y)
	x.up = y
	y.subVis, x.subVis = x.subVis, x.subVis-y.subVis+subVisOf(x.right)
	x.fixHeight()
	y.fixHeight()
	x.pullMin()
	y.pullMin()
}

// reparent hangs y where x hung.
func (d *Doc) reparent(x, y *block) {
	y.up = x.up
	switch {
	case x.up == nil:
		d.tree = y
	case x.up.left == x:
		x.up.left = y
	default:
		x.up.right = y
	}
}

// addVis records that b holds delta more visible characters than the tree
// believes.
//
// Typing appends character after character to one block, and carrying each of
// them to the root would cost the height of the tree per keystroke. The change
// is held against the block instead and carried up once, when the tree is next
// read.
//
// Restructuring the tree in the meantime is safe, and that is worth stating
// because it is not obvious. The state a held change leaves behind is exactly
// "every subtree containing b is short by delta", and both the rotations and
// the walk that inserts a block only ever add and subtract whole subtree
// counts. What is short stays short by the same amount, and lands on whichever
// blocks are the ancestors of b by the time the change is carried up.
func (d *Doc) addVis(b *block, delta int32) {
	if d.dirty != b {
		d.flush()
		d.dirty = b
	}
	d.dirtyVis += delta
}

// flush carries the held change to the root, leaving the tree readable.
func (d *Doc) flush() {
	if d.dirty == nil {
		return
	}
	for p := d.dirty; p != nil; p = p.up {
		p.subVis += d.dirtyVis
	}
	d.dirty, d.dirtyVis = nil, 0
}

// seek returns the block and offset of the visible character at index pos,
// which the caller must have checked is in range. It costs the height of the
// tree rather than the number of runs before pos.
func (d *Doc) seek(pos int) (*block, int) {
	d.flush()
	b := d.tree
	for {
		if l := b.left; l != nil {
			if int32(pos) < l.subVis {
				b = l
				continue
			}
			pos -= int(l.subVis)
		}
		own := int(b.subVis - subVisOf(b.left) - subVisOf(b.right))
		if pos < own {
			return b, b.visibleOffset(0, pos)
		}
		pos -= own
		b = b.right
	}
}

// lastOver returns the block the integration walk from at ends on: the last one
// whose characters all sort after a character with this clock and site. The new
// character goes after it.
func (d *Doc) lastOver(at *block, clock uint64, site SiteID) *block {
	stop := d.stopBlock(at, clock, site)
	if stop == nil {
		return d.lastBlock()
	}
	return stop.prev
}

// stopBlock returns the first block after at that does not sort after a
// character with this clock and site, or nil if no block does.
//
// The candidates are the blocks the walk would have visited, in the order it
// would have visited them: at's right subtree, then each ancestor holding at in
// its left subtree, then that ancestor's right subtree. A subtree whose lowest
// block still sorts after the character holds nothing to stop on and is stepped
// over whole, which is what makes this the height of the tree rather than the
// length of the document.
func (d *Doc) stopBlock(at *block, clock uint64, site SiteID) *block {
	if r := at.right; r != nil && !sortsAfter(r.subMin, clock, site) {
		return descendStop(r, clock, site)
	}
	for n := at; n.up != nil; n = n.up {
		p := n.up
		if p.right == n {
			continue // n is after p; p is behind the walk
		}
		if !sortsAfter(p, clock, site) {
			return p
		}
		if r := p.right; r != nil && !sortsAfter(r.subMin, clock, site) {
			return descendStop(r, clock, site)
		}
	}
	return nil
}

// descendStop returns the first block of t in document order that does not sort
// after a character with this clock and site. The subtree is known to hold one.
func descendStop(t *block, clock uint64, site SiteID) *block {
	for {
		if l := t.left; l != nil && !sortsAfter(l.subMin, clock, site) {
			t = l
			continue
		}
		if !sortsAfter(t, clock, site) {
			return t
		}
		t = t.right
	}
}

// lastBlock returns the last block in document order.
func (d *Doc) lastBlock() *block {
	b := d.tree
	for b.right != nil {
		b = b.right
	}
	return b
}

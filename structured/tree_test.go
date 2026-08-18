package structured

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"testing"

	"github.com/go-crdt/crdt"
)

// shapeOf draws the tree as indented lines, which is what a file tree or an
// outline is, and is the only readable way to say two replicas agree.
func shapeOf(t *Tree) string {
	var b strings.Builder
	var walk func(at TreeID, depth int)
	walk = func(at TreeID, depth int) {
		for _, kid := range t.Children(at) {
			name, ok := t.GetField(kid, "name")
			if !ok {
				name = []byte(kid.String())
			}
			fmt.Fprintf(&b, "%s%s\n", strings.Repeat("  ", depth), name)
			walk(kid, depth+1)
		}
	}
	walk(TreeRoot, 0)
	return b.String()
}

// named adds a node with a name, which is all the tests below need a node to
// carry.
func named(tb testing.TB, t *Tree, parent, after TreeID, name string) TreeID {
	tb.Helper()
	id, _, err := t.Insert(parent, after)
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := t.SetField(id, "name", []byte(name)); err != nil {
		tb.Fatal(err)
	}
	return id
}

// sync brings b up to date with a and a up to date with b, which is what two
// replicas meeting means.
func sync(tb testing.TB, a, b *Tree) {
	tb.Helper()
	fromA := a.OpsSince(b.Version())
	fromB := b.OpsSince(a.Version())
	if err := b.Apply(fromA...); err != nil {
		tb.Fatal(err)
	}
	if err := a.Apply(fromB...); err != nil {
		tb.Fatal(err)
	}
}

func TestATreeIsBuiltAndRead(t *testing.T) {
	tree := NewTree(1)
	src := named(t, tree, TreeRoot, TreeRoot, "src")
	named(t, tree, src, TreeRoot, "main.tex")
	docs := named(t, tree, TreeRoot, src, "docs")
	named(t, tree, docs, TreeRoot, "notes.md")

	want := "src\n  main.tex\ndocs\n  notes.md\n"
	if got := shapeOf(tree); got != want {
		t.Fatalf("the tree reads\n%s\nwant\n%s", got, want)
	}
	if n := len(tree.Nodes()); n != 4 {
		t.Fatalf("the tree holds %d nodes, want 4", n)
	}
	if depth, ok := tree.Depth(src); !ok || depth != 1 {
		t.Fatalf("src is at depth %d (%v), want 1", depth, ok)
	}
}

func TestSiblingsKeepTheirOrder(t *testing.T) {
	tree := NewTree(1)
	// Inserted out of order on purpose: c first, then a before it, then b
	// between them.
	c := named(t, tree, TreeRoot, TreeRoot, "c")
	a := named(t, tree, TreeRoot, TreeRoot, "a")
	named(t, tree, TreeRoot, a, "b")
	_ = c

	if got, want := shapeOf(tree), "a\nb\nc\n"; got != want {
		t.Fatalf("the tree reads\n%s\nwant\n%s", got, want)
	}
}

// Inserting at the same place over and over is what a rank has to survive:
// every insert splits the same gap, and the strings only get longer if they
// have to.
func TestAThousandInsertsAtTheSamePlace(t *testing.T) {
	tree := NewTree(1)
	first := named(t, tree, TreeRoot, TreeRoot, "first")
	for i := range 1000 {
		named(t, tree, TreeRoot, first, fmt.Sprint(i))
	}
	kids := tree.Children(TreeRoot)
	if len(kids) != 1001 {
		t.Fatalf("the root has %d children, want 1001", len(kids))
	}
	// Strictly ascending ranks, and the last insert immediately after first.
	last := ""
	for i, kid := range kids {
		rank := tree.rankOf(kid)
		if i > 0 && rank <= last {
			t.Fatalf("child %d has rank %q, not above %q", i, rank, last)
		}
		last = rank
	}
	if name, _ := tree.GetField(kids[1], "name"); string(name) != "999" {
		t.Fatalf("the child after first is %q, want the last one inserted", name)
	}
	if n := len(tree.rankOf(kids[len(kids)-1])); n > 12 {
		t.Fatalf("a rank grew to %d characters over a thousand inserts", n)
	}
}

func TestMovingANode(t *testing.T) {
	tree := NewTree(1)
	src := named(t, tree, TreeRoot, TreeRoot, "src")
	docs := named(t, tree, TreeRoot, src, "docs")
	file := named(t, tree, src, TreeRoot, "notes.md")

	if _, err := tree.Move(file, docs, TreeRoot); err != nil {
		t.Fatal(err)
	}
	if got, want := shapeOf(tree), "src\ndocs\n  notes.md\n"; got != want {
		t.Fatalf("the tree reads\n%s\nwant\n%s", got, want)
	}
	if up, ok := tree.Parent(file); !ok || up != docs {
		t.Fatal("the moved node did not change parent")
	}
}

func TestMovingWithinTheSameParent(t *testing.T) {
	tree := NewTree(1)
	a := named(t, tree, TreeRoot, TreeRoot, "a")
	b := named(t, tree, TreeRoot, a, "b")
	c := named(t, tree, TreeRoot, b, "c")

	// c to the front, then a to the end.
	if _, err := tree.Move(c, TreeRoot, TreeRoot); err != nil {
		t.Fatal(err)
	}
	if got, want := shapeOf(tree), "c\na\nb\n"; got != want {
		t.Fatalf("after moving c first the tree reads\n%s\nwant\n%s", got, want)
	}
	if _, err := tree.Move(a, TreeRoot, b); err != nil {
		t.Fatal(err)
	}
	if got, want := shapeOf(tree), "c\nb\na\n"; got != want {
		t.Fatalf("after moving a last the tree reads\n%s\nwant\n%s", got, want)
	}
}

func TestAMoveThatWouldMakeARingIsRefused(t *testing.T) {
	tree := NewTree(1)
	a := named(t, tree, TreeRoot, TreeRoot, "a")
	b := named(t, tree, a, TreeRoot, "b")
	c := named(t, tree, b, TreeRoot, "c")

	for _, into := range []TreeID{a, b, c} {
		if _, err := tree.Move(a, into, TreeRoot); err == nil {
			t.Fatalf("moving a under %v was accepted", into)
		}
	}
	if got, want := shapeOf(tree), "a\n  b\n    c\n"; got != want {
		t.Fatalf("a refused move changed the tree to\n%s", got)
	}
}

// The reason this type exists. Two replicas each make a move that is perfectly
// legal on its own, and together they are a ring.
func TestTwoReplicasMakeARingAndBothReadTheSameTree(t *testing.T) {
	a := NewTree(1)
	x := named(t, a, TreeRoot, TreeRoot, "x")
	y := named(t, a, TreeRoot, x, "y")

	b, err := LoadTree(2, a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}

	// a puts y under x; b puts x under y. Neither has heard the other.
	if _, err := a.Move(y, x, TreeRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Move(x, y, TreeRoot); err != nil {
		t.Fatal(err)
	}
	sync(t, a, b)

	if shapeOf(a) != shapeOf(b) {
		t.Fatalf("the two replicas read different trees:\n%s\nand\n%s", shapeOf(a), shapeOf(b))
	}
	// Neither node is lost, and neither is below the other twice over.
	if n := len(a.Nodes()); n != 2 {
		t.Fatalf("the tree holds %d nodes after the ring, want 2\n%s", n, shapeOf(a))
	}
	// One of the two moves gave way, so one node is at the top.
	if len(a.Children(TreeRoot)) != 1 {
		t.Fatalf("the ring was not broken:\n%s", shapeOf(a))
	}
}

// A ring made of three, which the two-node case does not exercise: breaking it
// at one node has to free the other two.
func TestARingOfThree(t *testing.T) {
	a := NewTree(1)
	x := named(t, a, TreeRoot, TreeRoot, "x")
	y := named(t, a, TreeRoot, x, "y")
	z := named(t, a, TreeRoot, y, "z")

	b, err := LoadTree(2, a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadTree(3, a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.Move(y, x, TreeRoot); err != nil { // y under x
		t.Fatal(err)
	}
	if _, err := b.Move(z, y, TreeRoot); err != nil { // z under y
		t.Fatal(err)
	}
	if _, err := c.Move(x, z, TreeRoot); err != nil { // x under z
		t.Fatal(err)
	}
	sync(t, a, b)
	sync(t, a, c)
	sync(t, b, c)
	sync(t, a, b)

	if shapeOf(a) != shapeOf(b) || shapeOf(b) != shapeOf(c) {
		t.Fatalf("three replicas read three trees:\n%s\n%s\n%s", shapeOf(a), shapeOf(b), shapeOf(c))
	}
	if n := len(a.Nodes()); n != 3 {
		t.Fatalf("the tree holds %d nodes, want 3\n%s", n, shapeOf(a))
	}
}

// Deleting a folder while a peer moves a file into it. The file survives, which
// is the direction this type chooses and says it chooses.
func TestAFileMovedIntoADeletedFolderSurvives(t *testing.T) {
	a := NewTree(1)
	folder := named(t, a, TreeRoot, TreeRoot, "folder")
	file := named(t, a, TreeRoot, folder, "file")

	b, err := LoadTree(2, a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Remove(folder); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Move(file, folder, TreeRoot); err != nil {
		t.Fatal(err)
	}
	sync(t, a, b)

	if shapeOf(a) != shapeOf(b) {
		t.Fatalf("the replicas disagree:\n%s\nand\n%s", shapeOf(a), shapeOf(b))
	}
	if got, want := shapeOf(a), "file\n"; got != want {
		t.Fatalf("the tree reads\n%s\nwant\n%s", got, want)
	}
}

func TestRemovingASubtree(t *testing.T) {
	tree := NewTree(1)
	keep := named(t, tree, TreeRoot, TreeRoot, "keep")
	drop := named(t, tree, TreeRoot, keep, "drop")
	inner := named(t, tree, drop, TreeRoot, "inner")
	named(t, tree, inner, TreeRoot, "deep")

	if _, err := tree.RemoveSubtree(drop); err != nil {
		t.Fatal(err)
	}
	if got, want := shapeOf(tree), "keep\n"; got != want {
		t.Fatalf("the tree reads\n%s\nwant\n%s", got, want)
	}
	if n := len(tree.Nodes()); n != 1 {
		t.Fatalf("%d nodes are left, want 1", n)
	}
}

func TestWhatIsRefused(t *testing.T) {
	tree := NewTree(1)
	node := named(t, tree, TreeRoot, TreeRoot, "node")
	gone := TreeID{Site: 9, Seq: 9}

	if _, err := tree.Move(TreeRoot, TreeRoot, TreeRoot); err == nil {
		t.Fatal("moving the root was accepted")
	}
	if _, err := tree.Move(gone, TreeRoot, TreeRoot); err == nil {
		t.Fatal("moving a node that does not exist was accepted")
	}
	if _, err := tree.Move(node, gone, TreeRoot); err == nil {
		t.Fatal("moving a node under one that does not exist was accepted")
	}
	if _, _, err := tree.Insert(gone, TreeRoot); err == nil {
		t.Fatal("inserting under a node that does not exist was accepted")
	}
	if _, err := tree.Remove(TreeRoot); err == nil {
		t.Fatal("removing the root was accepted")
	}
	if _, err := tree.Remove(gone); err == nil {
		t.Fatal("removing a node that does not exist was accepted")
	}
	if _, err := tree.RemoveSubtree(TreeRoot); err == nil {
		t.Fatal("removing the root's subtree was accepted")
	}
	if _, err := tree.RemoveSubtree(gone); err == nil {
		t.Fatal("removing a missing node's subtree was accepted")
	}
	if _, err := tree.SetField(TreeRoot, "name", nil); err == nil {
		t.Fatal("setting a field on the root was accepted")
	}
	for _, field := range []string{treeParentField, treeRankField} {
		if _, err := tree.SetField(node, field, nil); err == nil {
			t.Fatalf("writing %q directly was accepted", field)
		}
	}
	if _, ok := tree.Parent(TreeRoot); ok {
		t.Fatal("the root reports a parent")
	}
	if _, ok := tree.Parent(gone); ok {
		t.Fatal("a node that does not exist reports a parent")
	}
	if _, ok := tree.Depth(TreeRoot); ok {
		t.Fatal("the root reports a depth")
	}
	if _, ok := tree.Depth(gone); ok {
		t.Fatal("a node that does not exist reports a depth")
	}
}

func TestLoadingRefusesRubbishTree(t *testing.T) {
	if _, err := LoadTree(1, []byte("not a snapshot")); err == nil {
		t.Fatal("loading rubbish was accepted")
	}
}

func TestTreeOfReadsAMapInPlace(t *testing.T) {
	m := crdt.NewMap(1)
	tree := TreeOf(m)
	named(t, tree, TreeRoot, TreeRoot, "a")
	if len(TreeOf(m).Nodes()) != 1 {
		t.Fatal("a second view of the same map does not see the node")
	}
	if tree.Records() == nil || tree.Map() != m {
		t.Fatal("the tree does not report what it is built on")
	}
}

// A peer can write anything into the map. Whatever it writes, the tree must
// still be a tree: every node reachable from the root, no node twice.
func TestRubbishInTheMapStillReadsAsATree(t *testing.T) {
	tree := NewTree(1)
	a := named(t, tree, TreeRoot, TreeRoot, "a")
	b := named(t, tree, a, TreeRoot, "b")

	for _, write := range []struct{ key, value string }{
		{fieldKey(a.key(), treeParentField), "not an identity"},
		{fieldKey(b.key(), treeParentField), b.key()}, // its own parent
		{"not a record key", "x"},
		// A parent field under a name no node was ever minted with. It makes
		// that node exist, because a record is its fields and this is one —
		// the same for a Document or a Diagram. What matters is that it reads
		// as a node of the tree rather than as something outside it.
		{fieldKey("7.7", treeParentField), a.key()},
		{fieldKey(b.key(), treeRankField), "\x01 not a rank"},
	} {
		if _, err := tree.Map().Set(write.key, []byte(write.value)); err != nil {
			t.Fatal(err)
		}
		seen := map[TreeID]bool{}
		for _, node := range tree.Nodes() {
			if seen[node] {
				t.Fatalf("after %q the tree reads a node twice", write.key)
			}
			seen[node] = true
		}
		if len(seen) != len(tree.Records().Records()) {
			t.Fatalf("after %q the tree reads %d of %d records",
				write.key, len(seen), len(tree.Records().Records()))
		}
		// And a second replica holding the same bytes reads the same tree,
		// which is the guarantee that has to survive anything a peer writes.
		other, err := LoadTree(2, tree.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		if shapeOf(other) != shapeOf(tree) {
			t.Fatalf("after %q two replicas read different trees:\n%s\nand\n%s",
				write.key, shapeOf(tree), shapeOf(other))
		}
	}
}

// The whole point, checked the only way that settles it: many replicas, many
// random moves, delivered in different orders, all reading the same tree.
func TestRandomisedMovesConverge(t *testing.T) {
	for seed := range uint64(40) {
		t.Run(fmt.Sprint("seed ", seed), func(t *testing.T) {
			base := NewTree(1)
			var nodes []TreeID
			for i := range 8 {
				parent := TreeRoot
				if len(nodes) > 0 && i%2 == 0 {
					parent = nodes[i%len(nodes)]
				}
				nodes = append(nodes, named(t, base, parent, TreeRoot, fmt.Sprint("n", i)))
			}
			snapshot := base.Snapshot()

			const replicas = 4
			trees := make([]*Tree, replicas)
			for i := range trees {
				tree, err := LoadTree(crdt.SiteID(i+2), snapshot)
				if err != nil {
					t.Fatal(err)
				}
				trees[i] = tree
			}

			// Each replica makes moves nobody else has seen.
			rng := rand.New(rand.NewPCG(seed, 7))
			pending := make([][]crdt.MapOp, replicas)
			for round := range 6 {
				for i, tree := range trees {
					node := nodes[rng.IntN(len(nodes))]
					parent := TreeRoot
					if rng.IntN(2) == 0 {
						parent = nodes[rng.IntN(len(nodes))]
					}
					after := TreeRoot
					if kids := tree.Children(parent); len(kids) > 0 && rng.IntN(2) == 0 {
						after = kids[rng.IntN(len(kids))]
					}
					ops, err := tree.Move(node, parent, after)
					if err != nil {
						continue // a move that would make a ring on its own
					}
					pending[i] = append(pending[i], ops...)
				}
				_ = round
			}

			// Delivered to everyone, each replica in its own order.
			for i, tree := range trees {
				var inbox []crdt.MapOp
				for j, ops := range pending {
					if j != i {
						inbox = append(inbox, ops...)
					}
				}
				rng.Shuffle(len(inbox), func(a, b int) { inbox[a], inbox[b] = inbox[b], inbox[a] })
				if err := tree.Apply(inbox...); err != nil {
					t.Fatal(err)
				}
			}

			want := shapeOf(trees[0])
			for i, tree := range trees[1:] {
				if got := shapeOf(tree); got != want {
					t.Fatalf("replica %d reads\n%s\nreplica 0 reads\n%s", i+1, got, want)
				}
			}
			// Still a tree: every node once, all reachable.
			seen := map[TreeID]bool{}
			for _, node := range trees[0].Nodes() {
				if seen[node] {
					t.Fatalf("a node is read twice:\n%s", want)
				}
				seen[node] = true
			}
			if len(seen) != len(nodes) {
				t.Fatalf("%d of %d nodes are readable:\n%s", len(seen), len(nodes), want)
			}
		})
	}
}

func TestRankBetweenIsAlwaysStrictlyBetween(t *testing.T) {
	// Repeatedly splitting the same gap, from both ends, is what an editor
	// does to a rank and what has to keep terminating.
	ranks := []string{rankBetween("", "")}
	for range 500 {
		ranks = append(ranks, rankBetween("", ranks[0]))
		sort.Strings(ranks)
		ranks = append(ranks, rankBetween(ranks[len(ranks)-1], ""))
		mid := len(ranks) / 2
		ranks = append(ranks, rankBetween(ranks[mid], ranks[mid+1]))
		sort.Strings(ranks)
	}
	for i := 1; i < len(ranks); i++ {
		if ranks[i-1] >= ranks[i] {
			t.Fatalf("%q and %q are not in order", ranks[i-1], ranks[i])
		}
	}
	// A gap the wrong way round is answered rather than refused.
	if got := rankBetween("z", "a"); got == "" || got >= "a" {
		t.Fatalf("rankBetween(\"z\", \"a\") gave %q, want something below a", got)
	}
	// A rank containing something outside the alphabet can only have come from
	// a peer writing one by hand, and there is no string between "" and a byte
	// below the alphabet's first digit. What is promised there is not
	// betweenness but that an answer comes back at all — the order siblings are
	// read in is (rank, identity), so two replicas still agree on it.
	if got := rankBetween("", "\x01"); got == "" {
		t.Fatal("rankBetween gave nothing back for a rank outside the alphabet")
	}
}

// Two replicas inserting at the same place at the same time mint the same rank,
// so the order falls to the identity — and both replicas read it the same way
// round.
func TestConcurrentInsertsAtTheSamePlace(t *testing.T) {
	a := NewTree(1)
	anchor := named(t, a, TreeRoot, TreeRoot, "anchor")
	b, err := LoadTree(2, a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}

	fromA := named(t, a, TreeRoot, anchor, "a")
	fromB := named(t, b, TreeRoot, anchor, "b")
	if a.rankOf(fromA) != b.rankOf(fromB) {
		t.Fatalf("the two inserts minted %q and %q; this test needs them equal",
			a.rankOf(fromA), b.rankOf(fromB))
	}
	sync(t, a, b)

	if shapeOf(a) != shapeOf(b) {
		t.Fatalf("the replicas disagree:\n%s\nand\n%s", shapeOf(a), shapeOf(b))
	}
	if got, want := shapeOf(a), "anchor\na\nb\n"; got != want {
		t.Fatalf("the tree reads\n%s\nwant\n%s", got, want)
	}
}

func TestDepthBelowTheTop(t *testing.T) {
	tree := NewTree(1)
	one := named(t, tree, TreeRoot, TreeRoot, "one")
	two := named(t, tree, one, TreeRoot, "two")
	three := named(t, tree, two, TreeRoot, "three")
	for want, node := range map[int]TreeID{1: one, 2: two, 3: three} {
		if got, ok := tree.Depth(node); !ok || got != want {
			t.Fatalf("depth %d, want %d", got, want)
		}
	}
}

// A record whose name is not an identity is not a node. It can only come from a
// peer writing one, and it must not become part of the tree.
func TestARecordThatIsNotANodeIsNotReadAsOne(t *testing.T) {
	tree := NewTree(1)
	named(t, tree, TreeRoot, TreeRoot, "real")
	if _, err := tree.Map().Set(fieldKey("not-an-identity", "name"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if n := len(tree.Nodes()); n != 1 {
		t.Fatalf("the tree reads %d nodes, want 1", n)
	}
}

// The order two moves are compared in, including the tie that only a parent
// field with no stamp behind it can reach.
func TestWhichMoveHappenedLater(t *testing.T) {
	low, high := TreeID{Site: 1, Seq: 1}, TreeID{Site: 1, Seq: 2}
	for _, c := range []struct {
		what             string
		clock, bestClock uint64
		site, bestSite   crdt.SiteID
		node, best       TreeID
		want             bool
	}{
		{"a higher clock wins", 2, 1, 1, 1, low, high, true},
		{"a lower clock loses", 1, 2, 1, 1, low, high, false},
		{"the same clock falls to the site", 1, 1, 2, 1, low, high, true},
		{"and the lower site loses", 1, 1, 1, 2, low, high, false},
		{"with neither stamped it falls to the identity", 0, 0, 0, 0, high, low, true},
		{"and the lower identity loses", 0, 0, 0, 0, low, high, false},
	} {
		if got := movedLater(c.clock, c.site, c.node, c.bestClock, c.bestSite, c.best); got != c.want {
			t.Fatalf("%s: got %v, want %v", c.what, got, c.want)
		}
	}
}

// With no clock left, nothing can be written, and every entry point says so
// rather than half-making a change.
func TestATreeWithNoClockLeft(t *testing.T) {
	tree := NewTree(1)
	parent := named(t, tree, TreeRoot, TreeRoot, "parent")
	child := named(t, tree, parent, TreeRoot, "child")

	top := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock,
		Key: fieldKey("other", "g"), Value: []byte("x")}
	if err := tree.Apply(top); err != nil {
		t.Fatal(err)
	}

	if _, _, err := tree.Insert(TreeRoot, TreeRoot); err == nil {
		t.Fatal("inserting with no clock left was accepted")
	}
	if _, err := tree.Move(child, TreeRoot, TreeRoot); err == nil {
		t.Fatal("moving with no clock left was accepted")
	}
	if _, err := tree.Remove(child); err == nil {
		t.Fatal("removing with no clock left was accepted")
	}
	if _, err := tree.RemoveSubtree(parent); err == nil {
		t.Fatal("removing a subtree with no clock left was accepted")
	}
	// And the tree is unchanged: nothing was half-written.
	if got, want := shapeOf(tree), "parent\n  child\n"; got != want {
		t.Fatalf("the tree reads\n%s\nwant\n%s", got, want)
	}
}

// One clock tick left: the parent is written and the rank is not. It is the one
// operation here that is not all-or-nothing, and what it leaves has to still be
// a tree.
func TestAMoveWithOneClockTickLeft(t *testing.T) {
	tree := NewTree(1)
	parent := named(t, tree, TreeRoot, TreeRoot, "parent")
	child := named(t, tree, TreeRoot, parent, "child")

	top := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock - 1,
		Key: fieldKey("other", "g"), Value: []byte("x")}
	if err := tree.Apply(top); err != nil {
		t.Fatal(err)
	}

	if _, err := tree.Move(child, parent, TreeRoot); err == nil {
		t.Fatal("a move that could not write both fields was reported as done")
	}
	if got, want := shapeOf(tree), "parent\n  child\n"; got != want {
		t.Fatalf("the half-made move left\n%s\nwant\n%s", got, want)
	}
	if up, ok := tree.Parent(child); !ok || up != parent {
		t.Fatal("the node did not go where it was sent")
	}
}

// A peer given exactly the operations an edit returned, and nothing else, has
// to be able to apply them. Sending what OpsSince returns hides a gap in the
// sequence — the operations park, waiting for one that was never handed over,
// and the tree silently stops arriving.
func TestTheOperationsReturnedAreEnoughOnTheirOwn(t *testing.T) {
	a := NewTree(1)
	b := NewTree(2)

	parent, ops, err := a.Insert(TreeRoot, TreeRoot)
	if err != nil {
		t.Fatal(err)
	}
	child, more, err := a.Insert(parent, TreeRoot)
	if err != nil {
		t.Fatal(err)
	}
	ops = append(ops, more...)
	moved, err := a.Move(child, TreeRoot, parent)
	if err != nil {
		t.Fatal(err)
	}
	ops = append(ops, moved...)

	if err := b.Apply(ops...); err != nil {
		t.Fatal(err)
	}
	if n := b.Map().Pending(); n != 0 {
		t.Fatalf("%d operations are parked waiting for one that was not returned", n)
	}
	if shapeOf(b) != shapeOf(a) {
		t.Fatalf("the peer reads\n%s\nthe writer reads\n%s", shapeOf(b), shapeOf(a))
	}
	if len(b.Nodes()) != 2 {
		t.Fatalf("the peer holds %d nodes, want 2", len(b.Nodes()))
	}
}

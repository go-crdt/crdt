package structured

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/go-crdt/crdt"
)

// The four laws were proved over four document types — a sheet, a diagram, the
// isometric document and a block document — and the package has ten more.
//
// Each of those has convergence tests of its own, and they are not the same
// thing. A test written for a type exercises what its author thought of; this
// harness exercises what nobody thought of, because the schedule is random, the
// network is unreliable, and the oracle is byte-equal snapshots rather than
// equal readings — two replicas can agree on every value and disagree about
// which write produced it, which is a divergence that only shows up later.
//
// So every type that can be driven by a replica is driven by one here. What
// follows is one editor apiece; the laws themselves are in property_test.go and
// are not repeated.

// loadComposite turns a snapshot back into a replica. Every editor below is a
// [crdt.Composite] underneath — the map-backed types are given a composite of
// one part to live in — so one helper serves all of them, and the round trip
// the harness asserts is the composite's own.
func loadComposite(bind func(*crdt.Composite) editor) func(*testing.T, crdt.SiteID, []byte) editor {
	return func(t *testing.T, site crdt.SiteID, snap []byte) editor {
		t.Helper()
		doc, err := crdt.LoadComposite(site, snap)
		if err != nil {
			t.Fatalf("LoadComposite: %v", err)
		}
		return bind(doc)
	}
}

// A part name per map-backed type. These types are a [crdt.Map], not a
// [crdt.Composite], so each is given a composite of one part to live in — which
// is also how a caller would hold one beside anything else.
func mapIn(doc *crdt.Composite, name string) (*crdt.Map, crdt.Part) {
	m, err := doc.Map(name)
	if err != nil {
		panic(err) // a constant, valid name
	}
	return m, crdt.Part{Kind: crdt.PartMap, Name: name}
}

// batch wraps map operations as the composite batch the harness broadcasts.
func batch(part crdt.Part, ops []crdt.MapOp, err error) ([]crdt.PartOps, error) {
	if err != nil {
		return nil, err
	}
	if len(ops) == 0 {
		return nil, nil
	}
	return []crdt.PartOps{{Part: part, Map: ops}}, nil
}

func one(op crdt.MapOp, err error) ([]crdt.MapOp, error) {
	if err != nil {
		return nil, err
	}
	return []crdt.MapOp{op}, nil
}

// --- Tree ---------------------------------------------------------------------

type treeReplica struct {
	doc  *crdt.Composite
	part crdt.Part
	tree *Tree
}

func newTreeReplica(site crdt.SiteID) editor { return bindTree(crdt.NewComposite(site)) }

func bindTree(doc *crdt.Composite) editor {
	m, part := mapIn(doc, "tree")
	return &treeReplica{doc: doc, part: part, tree: TreeOf(m)}
}

func (r *treeReplica) Apply(b ...crdt.PartOps) error  { return r.doc.Apply(b...) }
func (r *treeReplica) Snapshot() []byte               { return r.doc.Snapshot() }
func (r *treeReplica) Version() crdt.CompositeVersion { return r.doc.Version() }
func (r *treeReplica) Pending() int                   { return r.doc.Pending() }
func (r *treeReplica) OpsSince(v crdt.CompositeVersion) []crdt.PartOps {
	return r.doc.OpsSince(v)
}

// edit makes one random change. Nodes are picked from what this replica can see,
// so once the network has delivered anything the replicas are moving each
// other's nodes — which is the case a tree has to survive, and the one that
// makes a ring.
func (r *treeReplica) edit(t *testing.T, rng *rand.Rand) []crdt.PartOps {
	t.Helper()
	nodes := r.tree.Nodes()
	pick := func() TreeID {
		if len(nodes) == 0 || rng.IntN(4) == 0 {
			return TreeID{} // the root
		}
		return nodes[rng.IntN(len(nodes))]
	}
	var ops []crdt.MapOp
	var err error
	switch rng.IntN(6) {
	case 0, 1:
		_, ops, err = r.tree.Insert(pick(), TreeID{})
	case 2, 3:
		if len(nodes) == 0 {
			return nil
		}
		node := nodes[rng.IntN(len(nodes))]
		parent := pick()
		if !legalParent(r.tree, node, parent) {
			return nil // a cycle; the library refuses it and so does the harness
		}
		ops, err = r.tree.Move(node, parent, TreeID{})
	case 4:
		if len(nodes) == 0 {
			return nil
		}
		ops, err = one(r.tree.SetField(nodes[rng.IntN(len(nodes))],
			fmt.Sprintf("f%d", rng.IntN(3)), []byte(fmt.Sprint(rng.IntN(9)))))
	default:
		if len(nodes) == 0 {
			return nil
		}
		ops, err = r.tree.Remove(nodes[rng.IntN(len(nodes))])
	}
	out, err := batch(r.part, ops, err)
	if err != nil {
		t.Fatalf("tree edit: %v", err)
	}
	return out
}

// legalParent reports whether node may be moved under parent — that is, whether
// parent is neither node nor anything below it. [Tree.Move] refuses a cycle, and
// a harness that proposed one would be testing the refusal rather than the merge.
func legalParent(tree *Tree, node, parent TreeID) bool {
	for at := parent; !at.IsRoot(); {
		if at == node {
			return false
		}
		up, ok := tree.Parent(at)
		if !ok {
			return true
		}
		at = up
	}
	return true
}

// --- Sequence -----------------------------------------------------------------

type sequenceReplica struct {
	doc  *crdt.Composite
	part crdt.Part
	seq  *Sequence
}

func newSequenceReplica(site crdt.SiteID) editor { return bindSequence(crdt.NewComposite(site)) }

func bindSequence(doc *crdt.Composite) editor {
	m, part := mapIn(doc, "sequence")
	return &sequenceReplica{doc: doc, part: part, seq: SequenceOf(m)}
}

func (r *sequenceReplica) Apply(b ...crdt.PartOps) error  { return r.doc.Apply(b...) }
func (r *sequenceReplica) Snapshot() []byte               { return r.doc.Snapshot() }
func (r *sequenceReplica) Version() crdt.CompositeVersion { return r.doc.Version() }
func (r *sequenceReplica) Pending() int                   { return r.doc.Pending() }
func (r *sequenceReplica) OpsSince(v crdt.CompositeVersion) []crdt.PartOps {
	return r.doc.OpsSince(v)
}

func (r *sequenceReplica) edit(t *testing.T, rng *rand.Rand) []crdt.PartOps {
	t.Helper()
	items := r.seq.Items()
	pick := func() ItemID {
		if len(items) == 0 || rng.IntN(5) == 0 {
			return SeqStart
		}
		return items[rng.IntN(len(items))]
	}
	var ops []crdt.MapOp
	var err error
	switch rng.IntN(5) {
	case 0, 1:
		_, ops, err = r.seq.Insert(pick(), []byte(fmt.Sprint(rng.IntN(50))))
	case 2, 3:
		if len(items) == 0 {
			return nil
		}
		item := items[rng.IntN(len(items))]
		after := pick()
		if item == after {
			return nil // ErrNoChange by design
		}
		ops, err = one(r.seq.Move(item, after))
	default:
		if len(items) == 0 {
			return nil
		}
		ops, err = r.seq.Remove(items[rng.IntN(len(items))])
	}
	out, err := batch(r.part, ops, err)
	if err != nil {
		t.Fatalf("sequence edit: %v", err)
	}
	return out
}

// --- Counter ------------------------------------------------------------------

type counterReplica struct {
	doc  *crdt.Composite
	part crdt.Part
	c    *Counter
}

func newCounterReplica(site crdt.SiteID) editor { return bindCounter(crdt.NewComposite(site)) }

func bindCounter(doc *crdt.Composite) editor {
	m, part := mapIn(doc, "counter")
	return &counterReplica{doc: doc, part: part, c: CounterOf(m)}
}

func (r *counterReplica) Apply(b ...crdt.PartOps) error  { return r.doc.Apply(b...) }
func (r *counterReplica) Snapshot() []byte               { return r.doc.Snapshot() }
func (r *counterReplica) Version() crdt.CompositeVersion { return r.doc.Version() }
func (r *counterReplica) Pending() int                   { return r.doc.Pending() }
func (r *counterReplica) OpsSince(v crdt.CompositeVersion) []crdt.PartOps {
	return r.doc.OpsSince(v)
}

func (r *counterReplica) edit(t *testing.T, rng *rand.Rand) []crdt.PartOps {
	t.Helper()
	delta := int64(rng.IntN(21) - 10)
	if delta == 0 {
		return nil // Add(0) is ErrNoChange by design
	}
	ops, err := one(r.c.Add(delta))
	out, err := batch(r.part, ops, err)
	if err != nil {
		t.Fatalf("counter edit: %v", err)
	}
	return out
}

// --- Set ----------------------------------------------------------------------

type setReplica struct {
	doc  *crdt.Composite
	part crdt.Part
	s    *Set
}

func newSetReplica(site crdt.SiteID) editor { return bindSet(crdt.NewComposite(site)) }

func bindSet(doc *crdt.Composite) editor {
	m, part := mapIn(doc, "set")
	return &setReplica{doc: doc, part: part, s: SetOf(m)}
}

func (r *setReplica) Apply(b ...crdt.PartOps) error  { return r.doc.Apply(b...) }
func (r *setReplica) Snapshot() []byte               { return r.doc.Snapshot() }
func (r *setReplica) Version() crdt.CompositeVersion { return r.doc.Version() }
func (r *setReplica) Pending() int                   { return r.doc.Pending() }
func (r *setReplica) OpsSince(v crdt.CompositeVersion) []crdt.PartOps {
	return r.doc.OpsSince(v)
}

// A tiny pool of names, so the replicas collide on the same one — which is the
// only interesting case for a set that resolves add against remove.
var setNames = []string{"urgent", "draft", "blocked", "review"}

func (r *setReplica) edit(t *testing.T, rng *rand.Rand) []crdt.PartOps {
	t.Helper()
	name := setNames[rng.IntN(len(setNames))]
	var ops []crdt.MapOp
	var err error
	if rng.IntN(3) == 0 {
		ops, err = r.s.Remove(name)
		if err == ErrNoChange {
			return nil
		}
	} else {
		ops, err = r.s.Add(name)
	}
	out, err := batch(r.part, ops, err)
	if err != nil {
		t.Fatalf("set edit: %v", err)
	}
	return out
}

// --- MultiRegister ------------------------------------------------------------

type multiReplica struct {
	doc  *crdt.Composite
	part crdt.Part
	m    *MultiRegister
}

func newMultiReplica(site crdt.SiteID) editor { return bindMulti(crdt.NewComposite(site)) }

func bindMulti(doc *crdt.Composite) editor {
	m, part := mapIn(doc, "multi")
	return &multiReplica{doc: doc, part: part, m: MultiRegisterOf(m)}
}

func (r *multiReplica) Apply(b ...crdt.PartOps) error  { return r.doc.Apply(b...) }
func (r *multiReplica) Snapshot() []byte               { return r.doc.Snapshot() }
func (r *multiReplica) Version() crdt.CompositeVersion { return r.doc.Version() }
func (r *multiReplica) Pending() int                   { return r.doc.Pending() }
func (r *multiReplica) OpsSince(v crdt.CompositeVersion) []crdt.PartOps {
	return r.doc.OpsSince(v)
}

func (r *multiReplica) edit(t *testing.T, rng *rand.Rand) []crdt.PartOps {
	t.Helper()
	var ops []crdt.MapOp
	var err error
	if rng.IntN(5) == 0 {
		ops, err = one(r.m.Clear())
	} else {
		ops, err = one(r.m.Set([]byte(fmt.Sprintf("v%d", rng.IntN(6)))))
	}
	out, err := batch(r.part, ops, err)
	if err != nil {
		t.Fatalf("multi-register edit: %v", err)
	}
	return out
}

// --- RichText -----------------------------------------------------------------

type richReplica struct{ r *RichText }

func newRichReplica(site crdt.SiteID) editor { return bindRichReplica(crdt.NewComposite(site)) }

func bindRichReplica(doc *crdt.Composite) editor { return &richReplica{r: RichTextOf(doc)} }

func (r *richReplica) Apply(b ...crdt.PartOps) error  { return r.r.Apply(b...) }
func (r *richReplica) Snapshot() []byte               { return r.r.Snapshot() }
func (r *richReplica) Version() crdt.CompositeVersion { return r.r.Version() }
func (r *richReplica) Pending() int                   { return r.r.Pending() }
func (r *richReplica) OpsSince(v crdt.CompositeVersion) []crdt.PartOps {
	return r.r.OpsSince(v)
}

var markNames = []string{"bold", "italic", "link"}

func (r *richReplica) edit(t *testing.T, rng *rand.Rand) []crdt.PartOps {
	t.Helper()
	n := r.r.Len()
	var b crdt.PartOps
	var err error
	switch {
	case n < 2 || rng.IntN(3) == 0:
		b, err = r.r.Insert(rng.IntN(n+1), string(rune('a'+rng.IntN(26))))
	case rng.IntN(4) == 0:
		at := rng.IntN(n)
		b, err = r.r.Delete(at, 1+rng.IntN(n-at))
	default:
		from := rng.IntN(n - 1)
		to := 1 + from + rng.IntN(n-from-1)
		name := markNames[rng.IntN(len(markNames))]
		if rng.IntN(3) == 0 {
			b, err = r.r.Unmark(from, to, name)
		} else {
			b, err = r.r.Mark(from, to, name,
				[]byte(fmt.Sprintf("v%d", rng.IntN(3))), Expand(rng.IntN(4)))
		}
	}
	if err != nil {
		t.Fatalf("rich text edit: %v", err)
	}
	return []crdt.PartOps{b}
}

// --- Ink ----------------------------------------------------------------------

type inkReplica struct {
	ink     *Ink
	strokes []StrokeID
}

func newInkReplica(site crdt.SiteID) editor { return bindInkReplica(crdt.NewComposite(site)) }

func bindInkReplica(doc *crdt.Composite) editor { return &inkReplica{ink: InkOf(doc)} }

func (r *inkReplica) Apply(b ...crdt.PartOps) error  { return r.ink.Apply(b...) }
func (r *inkReplica) Snapshot() []byte               { return r.ink.Snapshot() }
func (r *inkReplica) Version() crdt.CompositeVersion { return r.ink.Version() }
func (r *inkReplica) Pending() int                   { return r.ink.Pending() }
func (r *inkReplica) OpsSince(v crdt.CompositeVersion) []crdt.PartOps {
	return r.ink.OpsSince(v)
}

func (r *inkReplica) edit(t *testing.T, rng *rand.Rand) []crdt.PartOps {
	t.Helper()
	live := r.ink.Strokes().Items()
	switch {
	case len(live) == 0 || rng.IntN(3) == 0:
		stroke, ops, err := r.ink.Begin()
		if err != nil {
			t.Fatalf("ink begin: %v", err)
		}
		r.strokes = append(r.strokes, stroke)
		return ops
	case rng.IntN(5) == 0:
		b, err := r.ink.Erase(live[rng.IntN(len(live))])
		if err != nil {
			t.Fatalf("ink erase: %v", err)
		}
		return []crdt.PartOps{b}
	default:
		pts := make([]Point, 1+rng.IntN(3))
		for i := range pts {
			pts[i] = Point{X: float32(rng.IntN(100)), Y: float32(rng.IntN(100)), Pressure: 1}
		}
		b, err := r.ink.Extend(live[rng.IntN(len(live))], pts...)
		if err != nil {
			t.Fatalf("ink extend: %v", err)
		}
		return []crdt.PartOps{b}
	}
}

// --- Blobs --------------------------------------------------------------------

type blobReplica struct{ b *Blobs }

func newBlobReplica(site crdt.SiteID) editor { return bindBlobReplica(crdt.NewComposite(site)) }

func bindBlobReplica(doc *crdt.Composite) editor { return &blobReplica{b: BlobsOf(doc)} }

func (r *blobReplica) Apply(b ...crdt.PartOps) error  { return r.b.Apply(b...) }
func (r *blobReplica) Snapshot() []byte               { return r.b.Snapshot() }
func (r *blobReplica) Version() crdt.CompositeVersion { return r.b.Version() }
func (r *blobReplica) Pending() int                   { return r.b.Pending() }
func (r *blobReplica) OpsSince(v crdt.CompositeVersion) []crdt.PartOps {
	return r.b.OpsSince(v)
}

// A small pool of names and of contents, so two replicas store the same bytes
// under the same name — the case where dedup has to be exactly right.
var blobNames = []string{"a.png", "b.png", "c.png"}

func (r *blobReplica) edit(t *testing.T, rng *rand.Rand) []crdt.PartOps {
	t.Helper()
	name := blobNames[rng.IntN(len(blobNames))]
	if rng.IntN(6) == 0 {
		if _, ok := r.b.Size(name); !ok {
			return nil
		}
		b, err := r.b.Remove(name)
		if err != nil {
			t.Fatalf("blob remove: %v", err)
		}
		return []crdt.PartOps{b}
	}
	data := make([]byte, 1+rng.IntN(40))
	for i := range data {
		data[i] = byte('a' + rng.IntN(4))
	}
	b, err := r.b.Put(name, data)
	if err != nil {
		t.Fatalf("blob put: %v", err)
	}
	return b
}

// --- Proposals ----------------------------------------------------------------

type proposalReplica struct {
	p     *Proposals
	site  crdt.SiteID
	seq   int
	known []ProposalID
}

func newProposalReplica(site crdt.SiteID) editor {
	return bindProposalReplica(crdt.NewComposite(site))
}

func bindProposalReplica(doc *crdt.Composite) editor {
	p := ProposalsOf(doc)
	// A part to propose changes to.
	if _, err := p.Composite().Text("text"); err != nil {
		panic(err)
	}
	return &proposalReplica{p: p, site: doc.Site()}
}

func (r *proposalReplica) Apply(b ...crdt.PartOps) error  { return r.p.Apply(b...) }
func (r *proposalReplica) Snapshot() []byte               { return r.p.Snapshot() }
func (r *proposalReplica) Version() crdt.CompositeVersion { return r.p.Version() }
func (r *proposalReplica) Pending() int                   { return r.p.Pending() }
func (r *proposalReplica) OpsSince(v crdt.CompositeVersion) []crdt.PartOps {
	return r.p.OpsSince(v)
}

func (r *proposalReplica) edit(t *testing.T, rng *rand.Rand) []crdt.PartOps {
	t.Helper()
	text, err := r.p.Composite().Text("text")
	if err != nil {
		t.Fatal(err)
	}
	open := r.p.Open()
	switch {
	case len(open) > 0 && rng.IntN(3) == 0:
		id := open[rng.IntN(len(open))].ID
		if rng.IntN(2) == 0 {
			b, err := r.p.Accept(id)
			if err != nil {
				t.Fatalf("accept: %v", err)
			}
			return b
		}
		b, err := r.p.Withdraw(id)
		if err != nil {
			t.Fatalf("withdraw: %v", err)
		}
		return []crdt.PartOps{b}
	case rng.IntN(3) == 0:
		// A draft is a replica: give it a site nothing else uses.
		r.seq++
		draft, err := r.p.Draft(crdt.SiteID(1000*uint64(r.site) + uint64(r.seq)))
		if err != nil {
			t.Fatalf("draft: %v", err)
		}
		drafted, err := draft.Composite().Text("text")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := drafted.Insert(rng.IntN(drafted.Len()+1), string(rune('A'+rng.IntN(26)))); err != nil {
			t.Fatalf("draft edit: %v", err)
		}
		id, b, err := r.p.Put(fmt.Sprintf("p%d.%d", r.site, r.seq), draft)
		if err != nil {
			t.Fatalf("put: %v", err)
		}
		r.known = append(r.known, id)
		return b
	default:
		ops, err := text.Insert(rng.IntN(text.Len()+1), string(rune('a'+rng.IntN(26))))
		if err != nil {
			t.Fatalf("text edit: %v", err)
		}
		return []crdt.PartOps{{Part: crdt.Part{Kind: crdt.PartText, Name: "text"}, Text: ops}}
	}
}

// --- Undo ---------------------------------------------------------------------

// An undo is a new edit, not a restored state, so it has to converge like any
// other — and it is made against a document the other replicas are editing
// underneath it, which is exactly where a stack of states would come apart.
type undoReplica struct {
	doc  *crdt.Composite
	text *crdt.Doc
	undo *Undo
}

func newUndoReplica(site crdt.SiteID) editor { return bindUndoReplica(crdt.NewComposite(site)) }

// An undo history is this replica's own record of what it did, not part of the
// document, so a replica rebuilt from a snapshot has nothing to undo — which is
// the honest answer and what the harness's reload round trip asserts about.
func bindUndoReplica(doc *crdt.Composite) editor {
	text, err := doc.Text("text")
	if err != nil {
		panic(err)
	}
	return &undoReplica{doc: doc, text: text, undo: NewUndo(text)}
}

func (r *undoReplica) Apply(b ...crdt.PartOps) error  { return r.doc.Apply(b...) }
func (r *undoReplica) Snapshot() []byte               { return r.doc.Snapshot() }
func (r *undoReplica) Version() crdt.CompositeVersion { return r.doc.Version() }
func (r *undoReplica) Pending() int                   { return r.doc.Pending() }
func (r *undoReplica) OpsSince(v crdt.CompositeVersion) []crdt.PartOps {
	return r.doc.OpsSince(v)
}

func (r *undoReplica) edit(t *testing.T, rng *rand.Rand) []crdt.PartOps {
	t.Helper()
	var ops []crdt.Op
	var err error
	n := r.text.Len()
	switch {
	case r.undo.CanUndo() && rng.IntN(4) == 0:
		ops, err = r.undo.Undo()
	case r.undo.CanRedo() && rng.IntN(5) == 0:
		ops, err = r.undo.Redo()
	case n > 0 && rng.IntN(4) == 0:
		at := rng.IntN(n)
		ops, err = r.undo.Delete(at, 1+rng.IntN(n-at))
	default:
		ops, err = r.undo.Insert(rng.IntN(n+1), string(rune('a'+rng.IntN(26))))
	}
	if err != nil {
		t.Fatalf("undo edit: %v", err)
	}
	if len(ops) == 0 {
		return nil
	}
	return []crdt.PartOps{{Part: crdt.Part{Kind: crdt.PartText, Name: "text"}, Text: ops}}
}

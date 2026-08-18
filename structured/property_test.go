package structured

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/go-crdt/crdt"
)

// These are the acceptance gate for the whole package. A structured document is
// only worth anything if replicas that saw the same operations agree, whatever
// order those operations arrived in — and because [Sheet] and [Diagram] are two
// wrappers over one [crdt.Composite], proving it for both over one harness is the
// mutualization made concrete: the same merge, exercised through two shapes.
//
// The oracle is byte-equal [crdt.Composite] snapshots, which is strictly stronger
// than equal values: two replicas can agree on every cell yet disagree about
// which write produced it, and the snapshot catches that where a value
// comparison would not.
//
// Randomness is seeded explicitly, so a failure names the seed and reproduces
// exactly, on any platform, js/wasm included.

// A doc is what the harness needs of a replica: to apply peer operations, to
// state what it holds, and to be compared. [Sheet], [Diagram] and [Document] are
// all docs, which is the point.
type doc interface {
	Apply(...crdt.PartOps) error
	Snapshot() []byte
	Version() crdt.CompositeVersion
	Pending() int
}

// editor drives one replica: it makes a random local edit and returns the
// batches to broadcast, or nil when the state offered nothing to do.
type editor interface {
	doc
	edit(t *testing.T, rng *rand.Rand) []crdt.PartOps
}

// network is an unreliable transport: batches sit in per-replica inboxes and are
// delivered late, out of order, and more than once.
type network struct {
	inbox [][]crdt.PartOps
}

func newNetwork(n int) *network { return &network{inbox: make([][]crdt.PartOps, n)} }

func (nw *network) broadcast(from int, batches []crdt.PartOps) {
	for i := range nw.inbox {
		if i != from {
			nw.inbox[i] = append(nw.inbox[i], batches...)
		}
	}
}

// deliver hands replica i a random, shuffled, occasionally-duplicated slice of
// its inbox. Anything not delivered stays queued.
func (nw *network) deliver(t *testing.T, rng *rand.Rand, docs []editor, i int) {
	t.Helper()
	queued := nw.inbox[i]
	if len(queued) == 0 {
		return
	}
	n := 1 + rng.IntN(len(queued))
	batch := append([]crdt.PartOps{}, queued[:n]...)
	nw.inbox[i] = queued[n:]
	rng.Shuffle(len(batch), func(a, b int) { batch[a], batch[b] = batch[b], batch[a] })
	if rng.IntN(4) == 0 {
		batch = append(batch, batch[rng.IntN(len(batch))]) // a duplicate must change nothing
	}
	if err := docs[i].Apply(batch...); err != nil {
		t.Fatalf("replica %d: Apply: %v", i, err)
	}
}

func (nw *network) settle(t *testing.T, rng *rand.Rand, docs []editor) {
	t.Helper()
	for i := range docs {
		for len(nw.inbox[i]) > 0 {
			nw.deliver(t, rng, docs, i)
		}
	}
}

// converged reports whether every replica holds byte-identical state, nothing
// left waiting, and equal versions. It is returned rather than asserted so the
// control-run below can prove it discriminates.
func converged(docs []editor) bool {
	want := docs[0].Snapshot()
	for _, d := range docs {
		if !bytes.Equal(d.Snapshot(), want) {
			return false
		}
		if d.Pending() != 0 {
			return false
		}
		if !d.Version().Equal(docs[0].Version()) {
			return false
		}
	}
	return true
}

func assertConverged(t *testing.T, seed uint64, docs []editor) {
	t.Helper()
	if !converged(docs) {
		for i, d := range docs {
			t.Logf("seed %d: replica %d pending=%d snapshot=%x", seed, i, d.Pending(), d.Snapshot())
		}
		t.Fatalf("seed %d: replicas did not converge", seed)
	}
}

// runSession drives a randomised editing session and returns the settled
// replicas. mk builds a replica for a given site.
func runSession(t *testing.T, seed uint64, replicas int, mk func(crdt.SiteID) editor) []editor {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 0x5eed))
	docs := make([]editor, replicas)
	for i := range docs {
		docs[i] = mk(crdt.SiteID(i + 1))
	}
	nw := newNetwork(replicas)
	for range 14 {
		for i := range docs {
			for range 1 + rng.IntN(3) {
				if batches := docs[i].edit(t, rng); len(batches) > 0 {
					nw.broadcast(i, batches)
				}
			}
			if rng.IntN(2) == 0 {
				nw.deliver(t, rng, docs, i)
			}
		}
	}
	nw.settle(t, rng, docs)
	return docs
}

// --- the four CRDT laws, over both document types ------------------------------

// TestConvergence: replicas edit concurrently while the network delivers late,
// out of order, and with duplicates. Once delivery catches up, every replica
// must hold byte-identical state.
func TestConvergence(t *testing.T) {
	for _, tc := range docTypes() {
		t.Run(tc.name, func(t *testing.T) {
			for seed := range uint64(200) {
				docs := runSession(t, seed, 2+int(seed%3), tc.mk)
				assertConverged(t, seed, docs)
			}
		})
	}
}

// TestCommutativity applies one fixed set of operations in many different orders,
// one operation at a time, and requires byte-identical state every time — the
// pending buffer, not the batch, doing the reordering. This is convergence with
// the edit schedule held constant, which isolates order-independence.
func TestCommutativity(t *testing.T) {
	for _, tc := range docTypes() {
		t.Run(tc.name, func(t *testing.T) {
			source := runSession(t, 42, 3, tc.mk)
			ops := flatten(source[0].(interface {
				OpsSince(crdt.CompositeVersion) []crdt.PartOps
			}).OpsSince(nil))
			if len(ops) < 20 {
				t.Fatalf("only %d operations to permute; fixture too small", len(ops))
			}
			rng := rand.New(rand.NewPCG(7, 7))
			var want []byte
			for trial := range 200 {
				shuffled := append([]crdt.PartOps{}, ops...)
				rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
				d := tc.mk(99)
				for _, b := range shuffled {
					if err := d.Apply(b); err != nil {
						t.Fatalf("trial %d: Apply: %v", trial, err)
					}
				}
				if d.Pending() != 0 {
					t.Fatalf("trial %d: %d operations never became applicable", trial, d.Pending())
				}
				if trial == 0 {
					want = d.Snapshot()
					continue
				}
				if !bytes.Equal(d.Snapshot(), want) {
					t.Fatalf("trial %d: state differs from the first ordering", trial)
				}
			}
		})
	}
}

// TestIdempotence re-delivers operations a replica already holds and requires the
// state to be unchanged: applying an operation twice is a no-op.
func TestIdempotence(t *testing.T) {
	for _, tc := range docTypes() {
		t.Run(tc.name, func(t *testing.T) {
			docs := runSession(t, 5, 3, tc.mk)
			assertConverged(t, 5, docs)
			ops := flatten(docs[0].(interface {
				OpsSince(crdt.CompositeVersion) []crdt.PartOps
			}).OpsSince(nil))
			before := docs[0].Snapshot()
			// Re-apply the whole history, then a shuffled second copy.
			for _, b := range ops {
				if err := docs[0].Apply(b); err != nil {
					t.Fatal(err)
				}
			}
			rng := rand.New(rand.NewPCG(1, 2))
			rng.Shuffle(len(ops), func(a, b int) { ops[a], ops[b] = ops[b], ops[a] })
			if err := docs[0].Apply(ops...); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(docs[0].Snapshot(), before) {
				t.Fatalf("re-applying operations changed the state")
			}
		})
	}
}

// TestAssociativity feeds one operation set to two fresh replicas grouped into
// batches two different ways. Merge is associative, so the grouping cannot
// matter: both must reach byte-identical state.
func TestAssociativity(t *testing.T) {
	for _, tc := range docTypes() {
		t.Run(tc.name, func(t *testing.T) {
			source := runSession(t, 99, 3, tc.mk)
			ops := flatten(source[0].(interface {
				OpsSince(crdt.CompositeVersion) []crdt.PartOps
			}).OpsSince(nil))
			rng := rand.New(rand.NewPCG(3, 4))
			rng.Shuffle(len(ops), func(a, b int) { ops[a], ops[b] = ops[b], ops[a] })

			left := tc.mk(101)  // delivered in a few big groups
			right := tc.mk(102) // delivered one at a time
			at := 0
			for at < len(ops) {
				step := 1 + rng.IntN(5)
				if at+step > len(ops) {
					step = len(ops) - at
				}
				if err := left.Apply(ops[at : at+step]...); err != nil {
					t.Fatal(err)
				}
				at += step
			}
			for _, b := range ops {
				if err := right.Apply(b); err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Equal(left.Snapshot(), right.Snapshot()) {
				t.Fatalf("regrouping the same operations produced different state")
			}
		})
	}
}

// TestSnapshotRoundTripKeepsConverging is the late-joiner property: a replica
// that joins by loading a snapshot is indistinguishable from one that replayed
// the history, and keeps converging as editing continues.
func TestSnapshotRoundTripKeepsConverging(t *testing.T) {
	for _, tc := range docTypes() {
		t.Run(tc.name, func(t *testing.T) {
			for seed := range uint64(60) {
				docs := runSession(t, seed, 3, tc.mk)
				assertConverged(t, seed, docs)
				joined := tc.load(t, crdt.SiteID(99), docs[0].Snapshot())
				if !bytes.Equal(joined.Snapshot(), docs[0].Snapshot()) {
					t.Fatalf("seed %d: a snapshot did not reload to itself", seed)
				}
			}
		})
	}
}

// --- control-run: the harness's own oracle must detect divergence -------------

// TestHarnessDetectsDivergence is run BEFORE the properties above are trusted: a
// convergence oracle that never fails proves nothing. It confirms that byte-equal
// snapshots discriminate — that two replicas given different operations are
// reported as NOT converged, and identical ones as converged — so a green result
// from the property tests is evidence rather than an accident.
func TestHarnessDetectsDivergence(t *testing.T) {
	for _, tc := range docTypes() {
		t.Run(tc.name, func(t *testing.T) {
			source := runSession(t, 17, 2, tc.mk)
			ops := flatten(source[0].(interface {
				OpsSince(crdt.CompositeVersion) []crdt.PartOps
			}).OpsSince(nil))
			if len(ops) < 4 {
				t.Fatal("fixture too small to withhold an operation")
			}
			full := tc.mk(1)
			short := tc.mk(2)
			if err := full.Apply(ops...); err != nil {
				t.Fatal(err)
			}
			if err := short.Apply(ops[:len(ops)-1]...); err != nil { // withhold one
				t.Fatal(err)
			}
			// Negative control: one operation apart, the oracle must say NOT converged.
			if converged([]editor{full, short}) {
				t.Fatal("the convergence oracle failed to notice a withheld operation")
			}
			// Positive control: give it the missing operation, and it must agree.
			if err := short.Apply(ops[len(ops)-1]); err != nil {
				t.Fatal(err)
			}
			if !converged([]editor{full, short}) {
				t.Fatal("the convergence oracle failed to notice identical state")
			}
		})
	}
}

// flatten concatenates OpsSince batches into a slice of single-part batches, so
// the harness can shuffle and regroup at operation granularity.
func flatten(batches []crdt.PartOps) []crdt.PartOps {
	var out []crdt.PartOps
	for _, b := range batches {
		switch {
		case len(b.Text) > 0:
			for _, op := range b.Text {
				out = append(out, crdt.PartOps{Part: b.Part, Text: []crdt.Op{op}})
			}
		case len(b.List) > 0:
			for _, op := range b.List {
				out = append(out, crdt.PartOps{Part: b.Part, List: []crdt.ListOp{op}})
			}
		case len(b.Map) > 0:
			for _, op := range b.Map {
				out = append(out, crdt.PartOps{Part: b.Part, Map: []crdt.MapOp{op}})
			}
		}
	}
	return out
}

// --- replica drivers -----------------------------------------------------------

type docType struct {
	name string
	mk   func(crdt.SiteID) editor
	load func(*testing.T, crdt.SiteID, []byte) editor
}

func docTypes() []docType {
	return []docType{
		{
			name: "sheet",
			mk:   func(s crdt.SiteID) editor { return &sheetReplica{s: NewSheet(s)} },
			load: func(t *testing.T, s crdt.SiteID, snap []byte) editor {
				sh, err := LoadSheet(s, snap)
				if err != nil {
					t.Fatalf("LoadSheet: %v", err)
				}
				return &sheetReplica{s: sh}
			},
		},
		{
			name: "diagram",
			mk:   func(s crdt.SiteID) editor { return &diagramReplica{d: NewDiagram(s)} },
			load: func(t *testing.T, s crdt.SiteID, snap []byte) editor {
				dg, err := LoadDiagram(s, snap)
				if err != nil {
					t.Fatalf("LoadDiagram: %v", err)
				}
				return &diagramReplica{d: dg}
			},
		},
		{
			name: "document",
			mk:   func(s crdt.SiteID) editor { return &documentReplica{d: NewDocument(s)} },
			load: func(t *testing.T, s crdt.SiteID, snap []byte) editor {
				doc, err := LoadDocument(s, snap)
				if err != nil {
					t.Fatalf("LoadDocument: %v", err)
				}
				return &documentReplica{d: doc}
			},
		},
	}
}

// documentReplica drives a [Document] through all five families and the generic
// field surface, so the four laws are exercised over the full isometric model:
// zones, text and layers merging beside nodes and connectors, with concurrent
// writes to shared, caller-chosen ids.
type documentReplica struct{ d *Document }

func (r *documentReplica) Apply(b ...crdt.PartOps) error  { return r.d.Apply(b...) }
func (r *documentReplica) Snapshot() []byte               { return r.d.Snapshot() }
func (r *documentReplica) Version() crdt.CompositeVersion { return r.d.Version() }
func (r *documentReplica) Pending() int                   { return r.d.Pending() }
func (r *documentReplica) OpsSince(v crdt.CompositeVersion) []crdt.PartOps {
	return r.d.OpsSince(v)
}

// docFields is a small fixed pool of field names so that replicas collide on the
// same entity and field — which is what makes a concurrent edit a concurrent edit
// rather than two edits that never meet. The families are the document's own five.
var docFields = []string{"x", "y", "label", "color", "visible", "order"}

func (r *documentReplica) edit(t *testing.T, rng *rand.Rand) []crdt.PartOps {
	t.Helper()
	d := r.d
	one := func(b crdt.PartOps, err error) []crdt.PartOps {
		if err != nil {
			t.Fatalf("document edit: %v", err)
		}
		return []crdt.PartOps{b}
	}
	fam := families[rng.IntN(len(families))]
	id := fmt.Sprintf("e%d", rng.IntN(6)) // a shared six-entity id pool
	switch rng.IntN(6) {
	case 0:
		return one(d.Add(fam, id))
	case 1:
		field := docFields[rng.IntN(len(docFields))]
		return one(d.Set(fam, id, field, EncodeInt(int32(rng.IntN(20))-10)))
	case 2:
		field := docFields[rng.IntN(len(docFields))]
		return one(d.Set(fam, id, field, []byte(fmt.Sprintf("v%d", rng.IntN(50)))))
	case 3:
		field := docFields[rng.IntN(len(docFields))]
		return one(d.Set(fam, id, field, EncodeBool(rng.IntN(2) == 0)))
	case 4:
		field := docFields[rng.IntN(len(docFields))]
		return one(d.DeleteField(fam, id, field))
	default:
		if !d.Has(fam, id) {
			return nil
		}
		return one(d.Remove(fam, id))
	}
}

type sheetReplica struct{ s *Sheet }

func (r *sheetReplica) Apply(b ...crdt.PartOps) error  { return r.s.Apply(b...) }
func (r *sheetReplica) Snapshot() []byte               { return r.s.Snapshot() }
func (r *sheetReplica) Version() crdt.CompositeVersion { return r.s.Version() }
func (r *sheetReplica) Pending() int                   { return r.s.Pending() }
func (r *sheetReplica) OpsSince(v crdt.CompositeVersion) []crdt.PartOps {
	return r.s.OpsSince(v)
}

func (r *sheetReplica) edit(t *testing.T, rng *rand.Rand) []crdt.PartOps {
	t.Helper()
	s := r.s
	must := func(b crdt.PartOps, err error) []crdt.PartOps {
		if err != nil {
			t.Fatalf("sheet edit: %v", err)
		}
		return []crdt.PartOps{b}
	}
	switch rng.IntN(6) {
	case 0:
		_, b, err := s.AppendRow()
		return must(b, err)
	case 1:
		_, b, err := s.AppendCol()
		return must(b, err)
	case 2:
		rows, cols := s.Rows(), s.Cols()
		if len(rows) == 0 || len(cols) == 0 {
			return nil
		}
		row, col := rows[rng.IntN(len(rows))], cols[rng.IntN(len(cols))]
		var cell Cell
		if rng.IntN(3) == 0 && len(rows) > 1 {
			// A formula referencing another cell by stable identity.
			ref := CellRef{Row: rows[rng.IntN(len(rows))], Col: col}
			cell = Formula(fmt.Sprintf("=%d", rng.IntN(100)), ref)
		} else {
			cell = Literal(fmt.Sprintf("v%d", rng.IntN(1000)))
		}
		return must(s.SetCell(row, col, cell))
	case 3:
		if s.RowCount() == 0 {
			return nil
		}
		return must(s.DeleteRow(rng.IntN(s.RowCount())))
	case 4:
		if s.ColCount() == 0 {
			return nil
		}
		return must(s.DeleteCol(rng.IntN(s.ColCount())))
	default:
		rows, cols := s.Rows(), s.Cols()
		if len(rows) == 0 || len(cols) == 0 {
			return nil
		}
		row, col := rows[rng.IntN(len(rows))], cols[rng.IntN(len(cols))]
		return must(s.ClearCell(row, col))
	}
}

type diagramReplica struct {
	d     *Diagram
	nodes []NodeID // identities this replica has minted or seen, to edit and reference
	conns []ConnID
}

func (r *diagramReplica) Apply(b ...crdt.PartOps) error  { return r.d.Apply(b...) }
func (r *diagramReplica) Snapshot() []byte               { return r.d.Snapshot() }
func (r *diagramReplica) Version() crdt.CompositeVersion { return r.d.Version() }
func (r *diagramReplica) Pending() int                   { return r.d.Pending() }
func (r *diagramReplica) OpsSince(v crdt.CompositeVersion) []crdt.PartOps {
	return r.d.OpsSince(v)
}

func (r *diagramReplica) edit(t *testing.T, rng *rand.Rand) []crdt.PartOps {
	t.Helper()
	d := r.d
	one := func(b crdt.PartOps, err error) []crdt.PartOps {
		if err != nil {
			t.Fatalf("diagram edit: %v", err)
		}
		return []crdt.PartOps{b}
	}
	live := d.Nodes()
	switch rng.IntN(7) {
	case 0:
		n, b, err := d.AddNode()
		if err != nil {
			t.Fatalf("AddNode: %v", err)
		}
		r.nodes = append(r.nodes, n)
		return []crdt.PartOps{b}
	case 1:
		if len(live) == 0 {
			return nil
		}
		return one(d.SetNodePosition(live[rng.IntN(len(live))], int32(rng.IntN(50))-25, int32(rng.IntN(50))-25))
	case 2:
		if len(live) == 0 {
			return nil
		}
		return one(d.SetNodeLabel(live[rng.IntN(len(live))], fmt.Sprintf("n%d", rng.IntN(100))))
	case 3:
		if len(live) == 0 {
			return nil
		}
		return one(d.SetNodeColour(live[rng.IntN(len(live))], fmt.Sprintf("#%03x", rng.IntN(4096))))
	case 4:
		if len(live) < 2 {
			return nil
		}
		from, to := live[rng.IntN(len(live))], live[rng.IntN(len(live))]
		c, bs, err := d.AddConn(from, to)
		if err != nil {
			t.Fatalf("AddConn: %v", err)
		}
		r.conns = append(r.conns, c)
		return bs
	case 5:
		if len(live) == 0 {
			return nil
		}
		return one(d.RemoveNode(live[rng.IntN(len(live))]))
	default:
		conns := d.Conns()
		if len(conns) == 0 {
			return nil
		}
		return one(d.RemoveConn(conns[rng.IntN(len(conns))]))
	}
}

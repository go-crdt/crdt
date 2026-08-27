package structured

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-crdt/crdt"
)

// A diagram worked on the way one is: nodes added, placed, labelled, moved
// about, and half of them thought better of.
func workedOnDiagram(t *testing.T, nodes int) (*Diagram, []NodeID) {
	t.Helper()
	d := NewDiagram(1)
	var made []NodeID
	for i := 0; i < nodes; i++ {
		n, _, err := d.AddNode()
		if err != nil {
			t.Fatal(err)
		}
		made = append(made, n)
		if _, err := d.SetNodeLabel(n, fmt.Sprintf("node %d", i)); err != nil {
			t.Fatal(err)
		}
		if _, err := d.SetNodeColour(n, "blue"); err != nil {
			t.Fatal(err)
		}
		for k := 0; k < 5; k++ {
			if _, err := d.SetNodePosition(n, int32(i*10+k), int32(k*3)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := 0; i < len(made); i += 2 {
		if _, err := d.RemoveNode(made[i]); err != nil {
			t.Fatal(err)
		}
	}
	return d, made
}

// Sweeping and then collecting is what makes a diagram smaller, and neither
// half does it alone: removing a node leaves its properties behind, sweeping
// turns them into tombstones, and collecting is what gives the bytes back.
func TestSweepingAndCollectingADiagram(t *testing.T) {
	d, made := workedOnDiagram(t, 200)
	live := len(d.Nodes())
	if live != len(made)/2 {
		t.Fatalf("%d nodes left, want %d", live, len(made)/2)
	}
	before := len(d.Snapshot())

	if _, err := d.Sweep(); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	swept := len(d.Snapshot())
	if len(d.Nodes()) != live {
		t.Fatalf("sweeping changed the diagram: %d nodes, want %d", len(d.Nodes()), live)
	}

	n := d.Collect(d.Version())
	after := len(d.Snapshot())
	if n == 0 {
		t.Fatal("sweeping made tombstones and collecting took none of them")
	}
	if len(d.Nodes()) != live {
		t.Fatalf("collecting changed the diagram: %d nodes, want %d", len(d.Nodes()), live)
	}
	if after >= before {
		t.Fatalf("the diagram did not shrink: %d bytes became %d", before, after)
	}
	t.Logf("%d nodes, %d removed: %d bytes -> %d swept -> %d collected (%.2fx)",
		live, len(made)-live, before, swept, after, float64(before)/float64(after))

	// Every node that is still there says what it said.
	for i := 1; i < len(made); i += 2 {
		if !d.HasNode(made[i]) {
			t.Fatalf("node %d went missing", i)
		}
		label, ok := d.NodeLabel(made[i])
		if !ok || label != fmt.Sprintf("node %d", i) {
			t.Fatalf("node %d reads %q", i, label)
		}
	}

	back, err := LoadDiagram(2, d.Snapshot())
	if err != nil {
		t.Fatalf("a collected diagram did not reload: %v", err)
	}
	if len(back.Nodes()) != live {
		t.Fatalf("the reloaded diagram holds %d nodes, want %d", len(back.Nodes()), live)
	}
}

// Sweeping a diagram with nothing to sweep says so rather than making an empty
// operation.
func TestSweepingADiagramWithNothingToSweep(t *testing.T) {
	d := NewDiagram(1)
	n, _, err := d.AddNode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetNodeLabel(n, "kept"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Sweep(); err != ErrNoChange {
		t.Fatalf("Sweep = %v, want ErrNoChange", err)
	}
	// A connector whose node is gone is swept with the node's own properties.
	other, _, err := d.AddNode()
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := d.AddConn(n, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetConnField(c, "style", []byte("dashed")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RemoveConn(c); err != nil {
		t.Fatal(err)
	}
	ops, err := d.Sweep()
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(ops) == 0 {
		t.Fatal("a removed connector's properties were not swept")
	}
	if _, held := d.ConnField(c, "style"); held {
		t.Fatal("the swept connector still has a style")
	}
}

// A swept and collected diagram still merges with a replica that did neither,
// which is the whole difference between collecting and rewriting.
func TestACollectedDiagramStillMerges(t *testing.T) {
	a, made := workedOnDiagram(t, 40)
	b, err := LoadDiagram(2, a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Sweep(); err != nil {
		t.Fatal(err)
	}
	// b has everything a holds, so what a collects against is honest.
	if n := a.Collect(a.Version()); n == 0 {
		t.Fatal("nothing was collected")
	}

	// b, which collected nothing, carries on working.
	n, ops, err := b.AddNode()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(ops); err != nil {
		t.Fatalf("a collected diagram refused a peer's node: %v", err)
	}
	moved, err := b.SetNodePosition(made[1], 99, 99)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(moved); err != nil {
		t.Fatalf("a collected diagram refused a peer's move: %v", err)
	}
	if a.Pending() != 0 {
		t.Fatalf("%d operations from the peer were stranded", a.Pending())
	}
	if !a.HasNode(n) {
		t.Fatal("the peer's new node is not here")
	}
	x, y, ok := a.NodePosition(made[1])
	if !ok || x != 99 || y != 99 {
		t.Fatalf("the peer's move reads %d,%d ok=%v", x, y, ok)
	}
}

// Collecting a composite leaves alone every part the caller did not vouch for.
func TestCollectingACompositeSkipsPartsNobodyVouchedFor(t *testing.T) {
	d, _ := workedOnDiagram(t, 20)
	if _, err := d.Sweep(); err != nil {
		t.Fatal(err)
	}
	before := len(d.Snapshot())
	// An empty version names no part, so nothing may be collected against it.
	if n := d.Collect(crdt.CompositeVersion{}); n != 0 {
		t.Fatalf("collected %d against a version naming no part", n)
	}
	if len(d.Snapshot()) != before {
		t.Fatal("a composite collected against nothing changed size")
	}
}

// Rich text keeps its marks against the identities of the characters they
// describe, which is why there is no rewrite for a composite. Collection is the
// case that is safe, and safe is not the same as obviously safe: a mark on text
// that was deleted names characters collection takes away, and the document has
// to go on reading and merging as if nothing had happened.
func TestCollectingUnderRichTextMarks(t *testing.T) {
	r := NewRichText(1)
	if _, err := r.Insert(0, "the brown fox"); err != nil {
		t.Fatal(err)
	}
	// Typed in afterwards, so it is a run of its own and can die whole — which
	// is the only shape a text has anything to collect in.
	if _, err := r.Insert(4, "quick "); err != nil {
		t.Fatal(err)
	}
	// Bold over "quick ", which is then deleted outright; italic over "brown",
	// which stays.
	if _, err := r.Mark(4, 10, "bold", nil, ExpandNone); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Mark(10, 15, "italic", nil, ExpandNone); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Delete(4, 6); err != nil {
		t.Fatal(err)
	}
	want := r.Text()
	peer, err := LoadRichText(2, r.Snapshot())
	if err != nil {
		t.Fatal(err)
	}

	n := r.Composite().Collect(r.Version())
	if n == 0 {
		t.Fatal("nothing was collected, so this proves nothing")
	}
	if got := r.Text(); got != want {
		t.Fatalf("collecting changed the text: %q, want %q", got, want)
	}
	// The surviving mark still describes the text it described.
	if marks := r.MarksAt(6); len(marks) == 0 {
		t.Fatal("collecting took the surviving mark with it")
	}
	back, err := LoadRichText(3, r.Snapshot())
	if err != nil {
		t.Fatalf("a collected rich text did not reload: %v", err)
	}
	if back.Text() != want {
		t.Fatalf("the reloaded document reads %q, want %q", back.Text(), want)
	}

	// And a peer that collected nothing still merges into it.
	ops, err := peer.Insert(peer.Len(), " jumps")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Apply(ops); err != nil {
		t.Fatalf("a collected rich text refused a peer's work: %v", err)
	}
	if r.Pending() != 0 {
		t.Fatalf("%d operations from the peer were stranded", r.Pending())
	}
	if r.Text() != want+" jumps" {
		t.Fatalf("after merging it reads %q, want %q", r.Text(), want+" jumps")
	}
}

// A sweep keeps what is still there: a connector that was removed goes, the one
// beside it stays, and the diagram underneath is the same document either way.
func TestASweepKeepsWhatIsStillThere(t *testing.T) {
	d := NewDiagram(1)
	a, _, err := d.AddNode()
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := d.AddNode()
	if err != nil {
		t.Fatal(err)
	}
	kept, _, err := d.AddConn(a, b)
	if err != nil {
		t.Fatal(err)
	}
	going, _, err := d.AddConn(b, a)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []ConnID{kept, going} {
		if _, err := d.SetConnField(c, "style", []byte("solid")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.RemoveConn(going); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Sweep(); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if _, held := d.ConnField(kept, "style"); !held {
		t.Fatal("the sweep took a connector that is still there")
	}
	if _, held := d.ConnField(going, "style"); held {
		t.Fatal("the sweep left a removed connector's style behind")
	}
	// The parts are reachable for a caller that wants them.
	if d.Composite() == nil {
		t.Fatal("a diagram has no document")
	}
	if got := len(d.Composite().Parts()); got == 0 {
		t.Fatal("a diagram's document has no parts")
	}
}

// A sweep that runs out of sequence numbers says so rather than reporting a
// partial one as done.
func TestASweepReportsAnExhaustedClock(t *testing.T) {
	for _, which := range []struct {
		name string
		part crdt.Part
		set  func(d *Diagram) error
	}{
		{"node properties", crdt.Part{Kind: crdt.PartMap, Name: "nodes"}, nil},
		{"connector properties", crdt.Part{Kind: crdt.PartMap, Name: "conns"}, nil},
	} {
		t.Run(which.name, func(t *testing.T) {
			d := NewDiagram(1)
			a, _, err := d.AddNode()
			if err != nil {
				t.Fatal(err)
			}
			b, _, err := d.AddNode()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.SetNodeLabel(a, "doomed"); err != nil {
				t.Fatal(err)
			}
			c, _, err := d.AddConn(a, b)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.SetConnField(c, "style", []byte("solid")); err != nil {
				t.Fatal(err)
			}
			if _, err := d.RemoveNode(a); err != nil {
				t.Fatal(err)
			}
			if _, err := d.RemoveConn(c); err != nil {
				t.Fatal(err)
			}
			// Push the part's clock to the ceiling, so the next write it is
			// asked for cannot be made.
			m, err := d.Composite().Map(which.part.Name)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.Apply(crdt.MapOp{
				Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1},
				Clock: crdt.MaxClock, Key: "filler",
			}); err != nil {
				t.Fatalf("filling the clock: %v", err)
			}
			if _, err := d.Sweep(); !errors.Is(err, crdt.ErrExhausted) {
				t.Fatalf("Sweep with no sequence numbers left = %v, want ErrExhausted", err)
			}
		})
	}
}

// A proposal is the document's history replayed with a change laid over it, so
// a document that has collected can be neither drafted from nor previewed.
//
// This is worth a test rather than a note, because the alternative is worse than
// an error: a draft quietly missing what everybody had already agreed was gone
// would look right until somebody proposed against it.
func TestADocumentThatHasCollectedCannotBeDraftedFrom(t *testing.T) {
	p := NewProposals(1)
	body, err := p.Composite().Text("body")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := body.Insert(0, "AAA"); err != nil {
		t.Fatal(err)
	}
	// A second site writes, so the first run is one of its own and can die
	// whole.
	peer := crdt.NewComposite(2)
	if err := peer.Apply(must(p.Composite().OpsSince(nil))...); err != nil {
		t.Fatal(err)
	}
	peerBody, err := peer.Text("body")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := peerBody.Insert(peerBody.Len(), "BBB")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Composite().Apply(crdt.PartOps{
		Part: crdt.Part{Kind: crdt.PartText, Name: "body"}, Text: theirs,
	}); err != nil {
		t.Fatal(err)
	}

	// Draftable and previewable while it has given nothing back.
	draft, err := p.Draft(3)
	if err != nil {
		t.Fatalf("Draft before collecting: %v", err)
	}
	draftBody, err := draft.Composite().Text("body")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := draftBody.Insert(0, "X"); err != nil {
		t.Fatal(err)
	}
	id, _, err := p.Put("a change", draft)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := p.Preview(id, 4); err != nil {
		t.Fatalf("Preview before collecting: %v", err)
	}

	if _, err := body.Delete(0, 3); err != nil {
		t.Fatal(err)
	}
	if n := p.Composite().Collect(p.Composite().Version()); n == 0 {
		t.Fatal("nothing was collected, so nothing below is being tested")
	}

	if _, err := p.Draft(5); !errors.Is(err, crdt.ErrCollected) {
		t.Fatalf("Draft from a collected document = %v, want ErrCollected", err)
	}
	if _, err := p.Preview(id, 6); !errors.Is(err, crdt.ErrCollected) {
		t.Fatalf("Preview of a collected document = %v, want ErrCollected", err)
	}
}

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

// A diagram gives back the property records of nodes that are gone: sweeping
// turns them into tombstones, collecting takes them away.
func TestSweepingAndCollectingADiagramsNodes(t *testing.T) {
	d, made := workedOnDiagram(t, 40)
	live := len(d.Nodes())
	if _, err := d.Sweep(); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	floors := crdt.CompositeClocks{}
	for part := range d.Version() {
		// Nothing more is sent in this test, so no clock is still to come.
		floors[part] = ^uint64(0)
	}
	n := d.Collect(d.Version(), floors)
	if n == 0 {
		t.Fatal("sweeping made tombstones and collecting took none of them")
	}
	if len(d.Nodes()) != live {
		t.Fatalf("collecting changed the diagram: %d nodes, want %d", len(d.Nodes()), live)
	}
	for i := 1; i < len(made); i += 2 {
		label, ok := d.NodeLabel(made[i])
		if !ok || label != fmt.Sprintf("node %d", i) {
			t.Fatalf("node %d reads %q after collecting", i, label)
		}
	}
	back, err := LoadDiagram(2, d.Snapshot())
	if err != nil {
		t.Fatalf("a collected diagram did not reload: %v", err)
	}
	if len(back.Nodes()) != live {
		t.Fatalf("the reloaded diagram holds %d nodes, want %d", len(back.Nodes()), live)
	}
	t.Logf("%d nodes, %d removed, %d property records given back", live, len(made)-live, n)
}

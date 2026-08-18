package structured

import (
	"errors"
	"reflect"
	"testing"

	"github.com/go-crdt/crdt"
)

func applyDiagram(t *testing.T, d *Diagram, batches ...crdt.PartOps) {
	t.Helper()
	if err := d.Apply(batches...); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

// sharedNode builds two synchronised replicas holding one node.
func sharedNode(t *testing.T) (a, b *Diagram, n NodeID) {
	t.Helper()
	a, b = NewDiagram(1), NewDiagram(2)
	n, batch, err := a.AddNode()
	if err != nil {
		t.Fatal(err)
	}
	applyDiagram(t, b, batch)
	return a, b, n
}

// Two replicas move the same node at once: the position field is one register,
// so last-writer-wins picks one and both agree on it.
func TestDiagramConcurrentMoveConverges(t *testing.T) {
	a, b, n := sharedNode(t)
	fromA, err := a.SetNodePosition(n, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	fromB, err := b.SetNodePosition(n, 9, 9)
	if err != nil {
		t.Fatal(err)
	}
	applyDiagram(t, a, fromB)
	applyDiagram(t, b, fromA)

	xa, ya, _ := a.NodePosition(n)
	xb, yb, _ := b.NodePosition(n)
	if xa != xb || ya != yb {
		t.Fatalf("diverged: a=(%d,%d) b=(%d,%d)", xa, ya, xb, yb)
	}
	// Both minted clock 1, so the higher site — 2 — wins: (9,9).
	if xa != 9 || ya != 9 {
		t.Fatalf("winner = (%d,%d), want (9,9)", xa, ya)
	}
	if !reflect.DeepEqual(a.Snapshot(), b.Snapshot()) {
		t.Fatal("diverged")
	}
}

// One replica moves a node while another recolours it: two different fields, so
// both edits survive.
func TestDiagramConcurrentMoveAndRecolourBothSurvive(t *testing.T) {
	a, b, n := sharedNode(t)
	move, err := a.SetNodePosition(n, 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	recolour, err := b.SetNodeColour(n, "#f00")
	if err != nil {
		t.Fatal(err)
	}
	applyDiagram(t, a, recolour)
	applyDiagram(t, b, move)

	for _, d := range []*Diagram{a, b} {
		x, y, ok := d.NodePosition(n)
		colour, ok2 := d.NodeColour(n)
		if !ok || x != 3 || y != 4 || !ok2 || colour != "#f00" {
			t.Fatalf("a concurrent move+recolour lost one: pos=(%d,%d),%v colour=%q,%v", x, y, ok, colour, ok2)
		}
	}
	if !reflect.DeepEqual(a.Snapshot(), b.Snapshot()) {
		t.Fatal("diverged")
	}
}

func TestDiagramNodeLifecycle(t *testing.T) {
	d := NewDiagram(1)
	if d.Site() != 1 {
		t.Fatalf("Site = %d", d.Site())
	}
	n1, _, _ := d.AddNode()
	n2, _, _ := d.AddNode()
	if got, want := d.Nodes(), []NodeID{n1, n2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Nodes = %v, want %v", got, want)
	}
	if !d.HasNode(n1) {
		t.Fatal("HasNode(n1) = false")
	}
	if _, err := d.SetNodePosition(n1, -5, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetNodeLabel(n1, "start"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetNodeColour(n1, "blue"); err != nil {
		t.Fatal(err)
	}
	if x, y, ok := d.NodePosition(n1); !ok || x != -5 || y != 7 {
		t.Fatalf("NodePosition = %d,%d,%v", x, y, ok)
	}
	if l, ok := d.NodeLabel(n1); !ok || l != "start" {
		t.Fatalf("NodeLabel = %q,%v", l, ok)
	}
	if c, ok := d.NodeColour(n1); !ok || c != "blue" {
		t.Fatalf("NodeColour = %q,%v", c, ok)
	}
	// Unset fields of the other node read absent.
	if _, _, ok := d.NodePosition(n2); ok {
		t.Fatal("NodePosition of unset node = ok")
	}
	if _, ok := d.NodeLabel(n2); ok {
		t.Fatal("NodeLabel of unset node = ok")
	}
	if _, ok := d.NodeColour(n2); ok {
		t.Fatal("NodeColour of unset node = ok")
	}
	// Remove n1; it is gone from membership, and removing it again is refused.
	if _, err := d.RemoveNode(n1); err != nil {
		t.Fatal(err)
	}
	if d.HasNode(n1) {
		t.Fatal("HasNode(n1) after removal = true")
	}
	if got, want := d.Nodes(), []NodeID{n2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Nodes after removal = %v, want %v", got, want)
	}
	if _, err := d.RemoveNode(n1); !errors.Is(err, ErrUnknownNode) {
		t.Fatalf("RemoveNode(already removed) = %v, want ErrUnknownNode", err)
	}
	if _, err := d.RemoveNode(NodeID{Site: 42, Seq: 42}); !errors.Is(err, ErrUnknownNode) {
		t.Fatalf("RemoveNode(never added) = %v, want ErrUnknownNode", err)
	}
}

func TestDiagramConnectors(t *testing.T) {
	d := NewDiagram(1)
	n1, _, _ := d.AddNode()
	n2, _, _ := d.AddNode()
	n3, _, _ := d.AddNode()
	c, batches, err := d.AddConn(n1, n2)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 {
		t.Fatalf("AddConn returned %d batches, want 2 (identity + endpoints)", len(batches))
	}
	if got, want := d.Conns(), []ConnID{c}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Conns = %v, want %v", got, want)
	}
	if !d.HasConn(c) {
		t.Fatal("HasConn = false")
	}
	from, to, ok := d.ConnEndpoints(c)
	if !ok || from != n1 || to != n2 {
		t.Fatalf("ConnEndpoints = %v,%v,%v, want %v,%v", from, to, ok, n1, n2)
	}
	// Re-point it.
	if _, err := d.SetConnEndpoints(c, n1, n3); err != nil {
		t.Fatal(err)
	}
	if from, to, ok := d.ConnEndpoints(c); !ok || from != n1 || to != n3 {
		t.Fatalf("after re-point, ConnEndpoints = %v,%v,%v", from, to, ok)
	}
	// Remove it; gone from membership, and refused a second time.
	if _, err := d.RemoveConn(c); err != nil {
		t.Fatal(err)
	}
	if d.HasConn(c) {
		t.Fatal("HasConn after removal = true")
	}
	if _, err := d.RemoveConn(c); !errors.Is(err, ErrUnknownConn) {
		t.Fatalf("RemoveConn(removed) = %v, want ErrUnknownConn", err)
	}
	if _, err := d.SetConnEndpoints(ConnID{Site: 42, Seq: 1}, n1, n2); !errors.Is(err, ErrUnknownConn) {
		t.Fatalf("SetConnEndpoints(unknown) = %v, want ErrUnknownConn", err)
	}
}

func TestDiagramConnEndpointsAbsentOrForeign(t *testing.T) {
	d := NewDiagram(1)
	n1, _, _ := d.AddNode()
	c, _, err := d.AddConn(n1, n1)
	if err != nil {
		t.Fatal(err)
	}
	// A connector with only one endpoint set reads as not-ok.
	half := NewDiagram(2)
	nn, batch, _ := half.AddNode()
	applyDiagram(t, d, batch)
	cc, batches, _ := half.AddConn(nn, nn)
	// Deliver only the identity and a single from-field, withholding the to-field.
	applyDiagram(t, d, batches[0])
	fromOnly := crdt.MapOp{
		Kind: crdt.MapSet, ID: crdt.ID{Site: 7, Seq: 1}, Clock: 1,
		Key: fieldKey(encodeID(crdt.ID(cc)), fieldFrom), Value: []byte(encodeID(crdt.ID(nn))),
	}
	applyDiagram(t, d, crdt.PartOps{Part: connPropsPart, Map: []crdt.MapOp{fromOnly}})
	if _, _, ok := d.ConnEndpoints(cc); ok {
		t.Fatal("ConnEndpoints with a missing endpoint = ok")
	}
	// A connector whose endpoint bytes are not an identity reads as not-ok.
	rec := encodeID(crdt.ID(c))
	garbage := crdt.MapOp{
		Kind: crdt.MapSet, ID: crdt.ID{Site: 7, Seq: 2}, Clock: 2,
		Key: fieldKey(rec, fieldFrom), Value: []byte("not-an-id"),
	}
	applyDiagram(t, d, crdt.PartOps{Part: connPropsPart, Map: []crdt.MapOp{garbage}})
	if _, _, ok := d.ConnEndpoints(c); ok {
		t.Fatal("ConnEndpoints with foreign endpoint bytes = ok")
	}
}

func TestDiagramNodePositionForeignBytes(t *testing.T) {
	d := NewDiagram(1)
	n, _, _ := d.AddNode()
	foreign := crdt.MapOp{
		Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: 1,
		Key: fieldKey(encodeID(crdt.ID(n)), fieldPos), Value: []byte{0xFF},
	}
	applyDiagram(t, d, crdt.PartOps{Part: nodePropsPart, Map: []crdt.MapOp{foreign}})
	if x, y, ok := d.NodePosition(n); ok {
		t.Fatalf("NodePosition on foreign bytes = %d,%d,ok", x, y)
	}
}

func TestDiagramSnapshotRoundTrip(t *testing.T) {
	d := NewDiagram(1)
	n, _, _ := d.AddNode()
	if _, err := d.SetNodeLabel(n, "x"); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadDiagram(3, d.Snapshot())
	if err != nil {
		t.Fatalf("LoadDiagram: %v", err)
	}
	if !reflect.DeepEqual(loaded.Snapshot(), d.Snapshot()) {
		t.Fatal("a diagram snapshot did not reload to itself")
	}
	if _, err := LoadDiagram(1, []byte("garbage")); err == nil {
		t.Fatal("LoadDiagram accepted garbage")
	}
}

// Every editing path refuses rather than wrapping the clock once the relevant
// part has been driven to the ceiling by a peer.
func TestDiagramEditsRefuseWhenClockExhausted(t *testing.T) {
	maxList := func() crdt.ListOp {
		return crdt.ListOp{Kind: crdt.OpInsert, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock, Value: []byte{axisMark}}
	}

	// nodes list exhausted: AddNode refused.
	d := NewDiagram(1)
	applyDiagram(t, d, crdt.PartOps{Part: nodesPart, List: []crdt.ListOp{maxList()}})
	if _, _, err := d.AddNode(); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("AddNode = %v, want ErrExhausted", err)
	}

	// A present node, then nodes list exhausted: RemoveNode refused.
	d = NewDiagram(1)
	n, _, _ := d.AddNode()
	applyDiagram(t, d, crdt.PartOps{Part: nodesPart, List: []crdt.ListOp{{Kind: crdt.OpInsert, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock, Value: []byte{axisMark}}}})
	if _, err := d.RemoveNode(n); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("RemoveNode = %v, want ErrExhausted", err)
	}

	// nodeProps map exhausted: SetNodeLabel refused.
	d = NewDiagram(1)
	n, _, _ = d.AddNode()
	applyDiagram(t, d, crdt.PartOps{Part: nodePropsPart, Map: []crdt.MapOp{{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock, Key: "seed"}}})
	if _, err := d.SetNodeLabel(n, "x"); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("SetNodeLabel = %v, want ErrExhausted", err)
	}

	// conns list exhausted: AddConn refused at the identity step.
	d = NewDiagram(1)
	n1, _, _ := d.AddNode()
	n2, _, _ := d.AddNode()
	applyDiagram(t, d, crdt.PartOps{Part: connsPart, List: []crdt.ListOp{maxList()}})
	if _, _, err := d.AddConn(n1, n2); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("AddConn (list) = %v, want ErrExhausted", err)
	}

	// connProps map exhausted: AddConn refused at the endpoints step (identity
	// minted, then no room for the fields).
	d = NewDiagram(1)
	n1, _, _ = d.AddNode()
	n2, _, _ = d.AddNode()
	applyDiagram(t, d, crdt.PartOps{Part: connPropsPart, Map: []crdt.MapOp{{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock, Key: "seed"}}})
	if _, _, err := d.AddConn(n1, n2); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("AddConn (endpoints) = %v, want ErrExhausted", err)
	}

	// connProps with room for exactly one write: the from-field lands, the
	// to-field is refused — the second endpoint's own error path.
	d = NewDiagram(1)
	n1, _, _ = d.AddNode()
	n2, _, _ = d.AddNode()
	c, _, err := d.AddConn(n1, n2)
	if err != nil {
		t.Fatal(err)
	}
	applyDiagram(t, d, crdt.PartOps{Part: connPropsPart, Map: []crdt.MapOp{{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock - 1, Key: "seed"}}})
	if _, err := d.SetConnEndpoints(c, n1, n2); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("SetConnEndpoints (to-field) = %v, want ErrExhausted", err)
	}

	// conns list exhausted with a present connector: RemoveConn refused.
	d = NewDiagram(1)
	n1, _, _ = d.AddNode()
	n2, _, _ = d.AddNode()
	c, _, err = d.AddConn(n1, n2)
	if err != nil {
		t.Fatal(err)
	}
	applyDiagram(t, d, crdt.PartOps{Part: connsPart, List: []crdt.ListOp{{Kind: crdt.OpInsert, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock, Value: []byte{axisMark}}}})
	if _, err := d.RemoveConn(c); !errors.Is(err, crdt.ErrExhausted) {
		t.Fatalf("RemoveConn = %v, want ErrExhausted", err)
	}
}

package structured

import (
	"encoding/binary"
	"errors"
	"math"

	"github.com/go-crdt/crdt"
)

// A Diagram's parts. A node's identity and a connector's identity are ordered
// lists — used here only as a reload-safe source of stable identities and as the
// set of what exists, the order ignored — while a node's and a connector's
// fields are maps. A list part and a map part may share a name, because
// [crdt.Composite] identifies a part by its kind and name together, so "nodes"
// the list and "nodes" the map are two parts, not one.
var (
	nodesPart     = crdt.Part{Kind: crdt.PartList, Name: "nodes"}
	nodePropsPart = crdt.Part{Kind: crdt.PartMap, Name: "nodes"}
	connsPart     = crdt.Part{Kind: crdt.PartList, Name: "conns"}
	connPropsPart = crdt.Part{Kind: crdt.PartMap, Name: "conns"}
)

// The fields a node and a connector carry. Each is an independent LWW-register,
// so a concurrent change to two of them never conflicts.
const (
	fieldPos    = "pos"
	fieldLabel  = "label"
	fieldColour = "colour"
	fieldFrom   = "from"
	fieldTo     = "to"
)

// ErrUnknownNode reports an operation naming a node this replica does not hold as
// present — one never added, or already removed.
var ErrUnknownNode = errors.New("structured: unknown node")

// ErrUnknownConn reports an operation naming a connector this replica does not
// hold as present.
var ErrUnknownConn = errors.New("structured: unknown connector")

// A Diagram is a collaborative diagram of nodes and connectors — an isometric
// diagram is the case it was shaped for. A node is a record of fields (a grid
// position, a label, a colour); a connector is a record whose fields are the
// identities of the two nodes it joins. Editing it produces operations that any
// number of replicas may exchange, offline and in any order, converging to the
// same diagram.
//
// It is one [crdt.Composite] — the same core a [Sheet] is — which is the point
// of this package: a spreadsheet and a diagram are two thin addressings of one
// merging substrate, not two implementations. Each node and connector field
// merges on its own, so one replica may move a node while another recolours it
// and both edits survive.
//
// Membership is the node and connector lists: [Diagram.Nodes] and
// [Diagram.Conns] say what exists. A field read is by identity and independent of
// membership, so it still answers for a removed node — a view renders what
// [Diagram.Nodes] returns and needs no more.
//
// A Diagram is not safe for concurrent use. Construct one with [NewDiagram] or
// [LoadDiagram].
type Diagram struct {
	doc       *crdt.Composite
	nodes     *crdt.List
	conns     *crdt.List
	nodeProps *RecordMap
	connProps *RecordMap
}

// bindDiagram returns the four parts of a composite, creating any a fresh
// document lacks. The names are constant and valid, so the errors are impossible
// and discarded.
func bindDiagram(doc *crdt.Composite) *Diagram {
	nodes, _ := doc.List(nodesPart.Name)
	conns, _ := doc.List(connsPart.Name)
	nodeMap, _ := doc.Map(nodePropsPart.Name)
	connMap, _ := doc.Map(connPropsPart.Name)
	return &Diagram{
		doc:       doc,
		nodes:     nodes,
		conns:     conns,
		nodeProps: RecordsOf(nodeMap),
		connProps: RecordsOf(connMap),
	}
}

// NewDiagram returns an empty diagram that issues operations as site. Every
// replica editing a diagram concurrently must pass a distinct [crdt.SiteID].
func NewDiagram(site crdt.SiteID) *Diagram { return bindDiagram(crdt.NewComposite(site)) }

// LoadDiagram rebuilds a diagram from a snapshot, to be edited as site.
func LoadDiagram(site crdt.SiteID, snapshot []byte) (*Diagram, error) {
	doc, err := crdt.LoadComposite(site, snapshot)
	if err != nil {
		return nil, err
	}
	return bindDiagram(doc), nil
}

// Site returns the replica identity this diagram issues operations as.
func (d *Diagram) Site() crdt.SiteID { return d.doc.Site() }

// Snapshot encodes the whole diagram, for a joining peer or for persistence.
func (d *Diagram) Snapshot() []byte { return d.doc.Snapshot() }

// Version returns what this replica holds, to hand a peer that will send back
// what it is missing; see [Diagram.OpsSince].
func (d *Diagram) Version() crdt.CompositeVersion { return d.doc.Version() }

// OpsSince returns the operations this replica holds that v does not, batched by
// part. Pass a nil version for everything.
func (d *Diagram) OpsSince(v crdt.CompositeVersion) []crdt.PartOps { return d.doc.OpsSince(v) }

// Apply integrates batches of operations from peers, tolerating duplicates and
// reordering.
func (d *Diagram) Apply(batches ...crdt.PartOps) error { return d.doc.Apply(batches...) }

// Pending reports how many received operations are still waiting, across every
// part, for the operations they depend on.
func (d *Diagram) Pending() int { return d.doc.Pending() }

// AddNode adds a node and returns its identity and the operation to broadcast.
// The node has no fields yet.
func (d *Diagram) AddNode() (NodeID, crdt.PartOps, error) {
	ops, err := d.nodes.Insert(d.nodes.Len(), []byte{axisMark})
	if err != nil {
		return NodeID{}, crdt.PartOps{}, err
	}
	return NodeID(ops[0].ID), crdt.PartOps{Part: nodesPart, List: ops}, nil
}

// Nodes returns the identities of the nodes present. Two replicas holding the
// same operations return the same slice.
func (d *Diagram) Nodes() []NodeID {
	out := make([]NodeID, 0, d.nodes.Len())
	for i := range d.nodes.Len() {
		id, _ := d.nodes.Anchor(i)
		out = append(out, NodeID(id))
	}
	return out
}

// HasNode reports whether a node is present.
func (d *Diagram) HasNode(n NodeID) bool { return d.nodes.Visible(crdt.ID(n)) }

// RemoveNode removes a node's identity and returns the operation to broadcast.
// Its fields are left in place, unreferenced by any present node, exactly as a
// removed row's cells are in a [Sheet]. A node not present is [ErrUnknownNode].
func (d *Diagram) RemoveNode(n NodeID) (crdt.PartOps, error) {
	if !d.nodes.Visible(crdt.ID(n)) {
		return crdt.PartOps{}, ErrUnknownNode
	}
	pos, _ := d.nodes.Position(crdt.ID(n)) // present, so the position is exact
	ops, err := d.nodes.Delete(pos, 1)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: nodesPart, List: ops}, nil
}

// SetNodePosition writes a node's grid position and returns the operation to
// broadcast. Coordinates are int32 so that every replica, on any architecture,
// stores and compares them identically.
func (d *Diagram) SetNodePosition(n NodeID, x, y int32) (crdt.PartOps, error) {
	return d.setNodeField(n, fieldPos, encodePos(x, y))
}

// SetNodeLabel writes a node's label and returns the operation to broadcast.
func (d *Diagram) SetNodeLabel(n NodeID, label string) (crdt.PartOps, error) {
	return d.setNodeField(n, fieldLabel, []byte(label))
}

// SetNodeColour writes a node's colour and returns the operation to broadcast.
func (d *Diagram) SetNodeColour(n NodeID, colour string) (crdt.PartOps, error) {
	return d.setNodeField(n, fieldColour, []byte(colour))
}

func (d *Diagram) setNodeField(n NodeID, field string, value []byte) (crdt.PartOps, error) {
	op, err := d.nodeProps.SetField(encodeID(crdt.ID(n)), field, value)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: nodePropsPart, Map: []crdt.MapOp{op}}, nil
}

// NodePosition returns a node's grid position and whether it is set.
func (d *Diagram) NodePosition(n NodeID) (x, y int32, ok bool) {
	value, held := d.nodeProps.GetField(encodeID(crdt.ID(n)), fieldPos)
	if !held {
		return 0, 0, false
	}
	return decodePos(value)
}

// NodeLabel returns a node's label and whether it is set.
func (d *Diagram) NodeLabel(n NodeID) (string, bool) {
	value, ok := d.nodeProps.GetField(encodeID(crdt.ID(n)), fieldLabel)
	if !ok {
		return "", false
	}
	return string(value), true
}

// NodeColour returns a node's colour and whether it is set.
func (d *Diagram) NodeColour(n NodeID) (string, bool) {
	value, ok := d.nodeProps.GetField(encodeID(crdt.ID(n)), fieldColour)
	if !ok {
		return "", false
	}
	return string(value), true
}

// SetNodeField writes an arbitrary field of a node and returns the operation to
// broadcast. It is the generic form of [Diagram.SetNodePosition],
// [Diagram.SetNodeLabel] and [Diagram.SetNodeColour], for the scalar fields a
// caller's schema carries beyond those three — a shape, an icon, a layer. Each
// field is its own LWW-register, so a concurrent write to two of them never
// conflicts. The value is opaque bytes; a caller storing a number should encode
// it identically on every architecture, as [EncodeInt] does. Writing the field a
// typed setter uses ("pos", "label", "colour") reaches the very same register.
func (d *Diagram) SetNodeField(n NodeID, field string, value []byte) (crdt.PartOps, error) {
	return d.setNodeField(n, field, value)
}

// NodeField returns the value of an arbitrary node field and whether it is set.
// The value is a copy.
func (d *Diagram) NodeField(n NodeID, field string) ([]byte, bool) {
	return d.nodeProps.GetField(encodeID(crdt.ID(n)), field)
}

// SetConnField writes an arbitrary field of a connector and returns the operation
// to broadcast — the generic form of [Diagram.SetConnEndpoints] for the fields a
// connector carries beyond its endpoints: a label, a style, an arrow, a colour, a
// width. Like [Diagram.SetNodeField] each field is its own register.
func (d *Diagram) SetConnField(c ConnID, field string, value []byte) (crdt.PartOps, error) {
	op, err := d.connProps.SetField(encodeID(crdt.ID(c)), field, value)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: connPropsPart, Map: []crdt.MapOp{op}}, nil
}

// ConnField returns the value of an arbitrary connector field and whether it is
// set. The value is a copy.
func (d *Diagram) ConnField(c ConnID, field string) ([]byte, bool) {
	return d.connProps.GetField(encodeID(crdt.ID(c)), field)
}

// AddConn adds a connector from one node to another and returns its identity and
// the operations to broadcast: the connector's own identity, then its endpoints.
// The endpoints are stored by identity and are not required to name nodes this
// replica already holds — the node may arrive later, or be concurrently removed.
func (d *Diagram) AddConn(from, to NodeID) (ConnID, []crdt.PartOps, error) {
	ops, err := d.conns.Insert(d.conns.Len(), []byte{axisMark})
	if err != nil {
		return ConnID{}, nil, err
	}
	conn := ConnID(ops[0].ID)
	batches := []crdt.PartOps{{Part: connsPart, List: ops}}
	endpoints, err := d.setConnEndpoints(conn, from, to)
	if err != nil {
		return ConnID{}, nil, err
	}
	return conn, append(batches, endpoints), nil
}

// SetConnEndpoints rewrites which nodes a connector joins and returns the
// operation to broadcast. The connector must be present.
func (d *Diagram) SetConnEndpoints(c ConnID, from, to NodeID) (crdt.PartOps, error) {
	if !d.conns.Visible(crdt.ID(c)) {
		return crdt.PartOps{}, ErrUnknownConn
	}
	return d.setConnEndpoints(c, from, to)
}

func (d *Diagram) setConnEndpoints(c ConnID, from, to NodeID) (crdt.PartOps, error) {
	rec := encodeID(crdt.ID(c))
	fromOp, err := d.connProps.SetField(rec, fieldFrom, []byte(encodeID(crdt.ID(from))))
	if err != nil {
		return crdt.PartOps{}, err
	}
	toOp, err := d.connProps.SetField(rec, fieldTo, []byte(encodeID(crdt.ID(to))))
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: connPropsPart, Map: []crdt.MapOp{fromOp, toOp}}, nil
}

// ConnEndpoints returns the nodes a connector joins and whether both are set.
func (d *Diagram) ConnEndpoints(c ConnID) (from, to NodeID, ok bool) {
	rec := encodeID(crdt.ID(c))
	fromValue, ok1 := d.connProps.GetField(rec, fieldFrom)
	toValue, ok2 := d.connProps.GetField(rec, fieldTo)
	if !ok1 || !ok2 {
		return NodeID{}, NodeID{}, false
	}
	fromID, ok3 := decodeID(string(fromValue))
	toID, ok4 := decodeID(string(toValue))
	if !ok3 || !ok4 {
		return NodeID{}, NodeID{}, false
	}
	return NodeID(fromID), NodeID(toID), true
}

// Conns returns the identities of the connectors present.
func (d *Diagram) Conns() []ConnID {
	out := make([]ConnID, 0, d.conns.Len())
	for i := range d.conns.Len() {
		id, _ := d.conns.Anchor(i)
		out = append(out, ConnID(id))
	}
	return out
}

// HasConn reports whether a connector is present.
func (d *Diagram) HasConn(c ConnID) bool { return d.conns.Visible(crdt.ID(c)) }

// RemoveConn removes a connector's identity and returns the operation to
// broadcast. Its fields are left in place, as a removed node's are. A connector
// not present is [ErrUnknownConn].
func (d *Diagram) RemoveConn(c ConnID) (crdt.PartOps, error) {
	if !d.conns.Visible(crdt.ID(c)) {
		return crdt.PartOps{}, ErrUnknownConn
	}
	pos, _ := d.conns.Position(crdt.ID(c))
	ops, err := d.conns.Delete(pos, 1)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return crdt.PartOps{Part: connsPart, List: ops}, nil
}

// encodePos renders a grid position as two signed varints. int32 keeps it
// identical on a 32-bit target (js/wasm) and a 64-bit one.
func encodePos(x, y int32) []byte {
	out := binary.AppendVarint(nil, int64(x))
	return binary.AppendVarint(out, int64(y))
}

// decodePos reverses [encodePos], reporting failure for bytes it did not
// produce — a value outside int32 among them, since a peer's operation carries
// the bytes and a wider value would store differently on a 32-bit replica.
func decodePos(data []byte) (x, y int32, ok bool) {
	xv, n1 := binary.Varint(data)
	if n1 <= 0 {
		return 0, 0, false
	}
	yv, n2 := binary.Varint(data[n1:])
	if n2 <= 0 || n1+n2 != len(data) {
		return 0, 0, false
	}
	if xv < math.MinInt32 || xv > math.MaxInt32 || yv < math.MinInt32 || yv > math.MaxInt32 {
		return 0, 0, false
	}
	return int32(xv), int32(yv), true
}

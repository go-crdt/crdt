package structured

import (
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/go-crdt/crdt"
)

// A Document is a collaborative document made of several families of entities,
// each entity addressed by a stable identity the caller chooses. It is the shape
// an isometric diagram takes: nodes, connectors, zones, free text and layers all
// living in one merging substrate, every family a map of records and every field
// an independent LWW-register.
//
// # One composite, one boundary
//
// Like [Sheet] and [Diagram] a Document is exactly one [crdt.Composite]: every
// family is a [RecordMap] over a distinct map part of that one composite, so the
// whole document has a single [Document.Snapshot], a single [Document.Version],
// a single [Document.OpsSince] and a single [Document.Apply] covering all five
// families at once. There is nothing to bundle and no second transport to keep in
// step — the convergence, commutativity, idempotence and associativity the crdt
// package proves for its parts are inherited whole, because there is only ever one
// set of parts doing the merging.
//
// # Caller-chosen identities
//
// A [Diagram] mints a node's identity from its RGA, so two replicas that each
// "add a node" get two distinct nodes — which is right when the two are genuinely
// different. A Document instead lets the caller name an entity, because a toolkit
// that already has its own stable id for a shape needs the two replicas that
// create "the shape the user just dropped" to converge on one entity, not two.
// The id is the record key, so [Document.Add] of the same id on two replicas is
// the same write to the same key: they converge, with no local, non-replicated
// id table to keep. An id is arbitrary, non-empty UTF-8.
//
// # What exists
//
// An entity exists exactly while it holds at least one live field — a field the
// caller set, or the presence marker [Document.Add] writes so that a freshly
// created entity with no fields yet still shows in [Document.IDs]. Reading a
// field is by identity and independent of membership, exactly as it is for a
// [Diagram]: a view renders what [Document.IDs] returns.
//
// A Document holds no widget and no geometry engine. Field values are opaque
// bytes; [EncodeInt], [DecodeInt], [EncodeBool] and [DecodeBool] are offered so
// that the non-string fields a diagram carries are encoded identically on every
// architecture, js/wasm included, but nothing here interprets a field.
//
// A Document is not safe for concurrent use. Construct one with [NewDocument] or
// [LoadDocument].
type Document struct {
	doc  *crdt.Composite
	fams map[Family]*RecordMap
}

// A Family names one of the five kinds of entity a [Document] holds. Its value is
// also the name of the [crdt.Composite] map part the family lives in, so the set
// of families is the document's structural layout, fixed and the same on every
// replica; the fields inside a record are the caller's own schema, not this
// package's.
type Family string

const (
	// Nodes is the family of diagram nodes.
	Nodes Family = "node"
	// Connectors is the family of edges joining nodes.
	Connectors Family = "conn"
	// Zones is the family of rectangular regions.
	Zones Family = "zone"
	// TextBoxes is the family of free-standing text entities. It is not named
	// "Text" so as not to be mistaken for a [crdt.PartText]; a Document holds no
	// text part.
	TextBoxes Family = "text"
	// Layers is the family of layers entities are assigned to.
	Layers Family = "layer"
)

// families is the fixed set of families a Document binds, in a deterministic
// order. It is the one place the five are enumerated.
var families = []Family{Nodes, Connectors, Zones, TextBoxes, Layers}

// valid reports whether f is one of the five families a Document knows. A value
// a caller invented is refused rather than silently creating a sixth part that no
// other replica would bind.
func (f Family) valid() bool {
	switch f {
	case Nodes, Connectors, Zones, TextBoxes, Layers:
		return true
	default:
		return false
	}
}

// The two reserved field names a Document stores against a record, kept in a
// namespace disjoint from the caller's fields so neither can be mistaken for the
// other. A caller field named x is stored as userFieldPrefix+x, and the presence
// marker is presenceField; the two prefixes differ in their first byte, so no
// caller field can ever collide with the marker whatever the caller names it.
const (
	userFieldPrefix = "f"
	presenceField   = "p"
)

// ErrUnknownFamily reports an operation naming a family a [Document] does not
// hold. The five valid families are the constants above.
var ErrUnknownFamily = errors.New("structured: unknown family")

// ErrUnknownEntity reports an operation naming an entity this replica does not
// hold as present — one never added, or already removed.
var ErrUnknownEntity = errors.New("structured: unknown entity")

// ErrInvalidID reports an entity identity that cannot key a record: the empty
// string, or bytes that are not valid UTF-8. An id crosses into JavaScript, where
// a string is UTF-16, so it is held to the same rule a [crdt.Map] key is.
var ErrInvalidID = errors.New("structured: invalid entity id")

// bindDocument returns a Document over doc, binding each family's map part —
// creating any a fresh composite lacks. The names are constant and valid, so the
// errors [crdt.Composite.Map] returns for an invalid name cannot happen and are
// discarded, exactly as [bind] and [bindDiagram] discard them.
func bindDocument(doc *crdt.Composite) *Document {
	fams := make(map[Family]*RecordMap, len(families))
	for _, f := range families {
		m, _ := doc.Map(string(f))
		fams[f] = RecordsOf(m)
	}
	return &Document{doc: doc, fams: fams}
}

// NewDocument returns an empty document that issues operations as site. Every
// replica editing a document concurrently must pass a distinct [crdt.SiteID].
func NewDocument(site crdt.SiteID) *Document { return bindDocument(crdt.NewComposite(site)) }

// LoadDocument rebuilds a document from a snapshot, to be edited as site.
func LoadDocument(site crdt.SiteID, snapshot []byte) (*Document, error) {
	doc, err := crdt.LoadComposite(site, snapshot)
	if err != nil {
		return nil, err
	}
	return bindDocument(doc), nil
}

// Site returns the replica identity this document issues operations as.
func (d *Document) Site() crdt.SiteID { return d.doc.Site() }

// Snapshot encodes the whole document — all five families — for a joining peer or
// for persistence. Two replicas holding the same operations produce identical
// bytes.
func (d *Document) Snapshot() []byte { return d.doc.Snapshot() }

// Version returns what this replica holds, to hand a peer that will send back
// what it is missing; see [Document.OpsSince].
func (d *Document) Version() crdt.CompositeVersion { return d.doc.Version() }

// OpsSince returns the operations this replica holds that v does not, batched by
// part and covering all five families. Pass a nil version for everything.
func (d *Document) OpsSince(v crdt.CompositeVersion) []crdt.PartOps { return d.doc.OpsSince(v) }

// Apply integrates batches of operations from peers, tolerating duplicates and
// reordering, across every family at once.
func (d *Document) Apply(batches ...crdt.PartOps) error { return d.doc.Apply(batches...) }

// Pending reports how many received operations are still waiting, across every
// family, for the operations they depend on.
func (d *Document) Pending() int { return d.doc.Pending() }

// records returns the record map of a family, or an error naming the family as
// unknown, and validates the id in the one place every entity operation passes.
func (d *Document) records(fam Family, id string) (*RecordMap, error) {
	if !fam.valid() {
		return nil, ErrUnknownFamily
	}
	if id == "" || !validUTF8(id) {
		return nil, ErrInvalidID
	}
	return d.fams[fam], nil
}

// partOf wraps a record map's operation as a batch addressed to the family's map
// part, so the caller broadcasts it as it does a [Diagram]'s.
func partOf(fam Family, ops ...crdt.MapOp) crdt.PartOps {
	return crdt.PartOps{Part: crdt.Part{Kind: crdt.PartMap, Name: string(fam)}, Map: ops}
}

// Add creates an entity in a family under the caller's id and returns the
// operation to broadcast. It writes a presence marker, so an entity with no
// fields yet is still present. Adding an id that already exists rewrites the
// marker and changes nothing observable, which is what lets two replicas both
// "create X" and converge.
func (d *Document) Add(fam Family, id string) (crdt.PartOps, error) {
	rm, err := d.records(fam, id)
	if err != nil {
		return crdt.PartOps{}, err
	}
	op, err := rm.SetField(id, presenceField, nil)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return partOf(fam, op), nil
}

// Has reports whether an entity is present — whether it holds any live field. An
// unknown family or an invalid id is not present rather than an error, so a view
// may ask freely.
func (d *Document) Has(fam Family, id string) bool {
	if !fam.valid() || id == "" {
		return false
	}
	return d.fams[fam].HasRecord(id)
}

// IDs returns the identities present in a family, ascending, so two replicas
// holding the same operations return the same slice. An unknown family has none.
func (d *Document) IDs(fam Family) []string {
	if !fam.valid() {
		return nil
	}
	return d.fams[fam].Records()
}

// Set writes value to one field of one entity and returns the operation to
// broadcast. The entity is created by the write if it did not exist, on the same
// terms as [Add], so a field may be set without a prior Add. value is opaque; use
// the encoders for a non-string field.
func (d *Document) Set(fam Family, id, field string, value []byte) (crdt.PartOps, error) {
	rm, err := d.records(fam, id)
	if err != nil {
		return crdt.PartOps{}, err
	}
	op, err := rm.SetField(id, userFieldPrefix+field, value)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return partOf(fam, op), nil
}

// Field returns the value of one field and whether it is set. The value is a
// copy. An unknown family, an invalid id or an unset field reads as absent.
func (d *Document) Field(fam Family, id, field string) ([]byte, bool) {
	if !fam.valid() || id == "" {
		return nil, false
	}
	return d.fams[fam].GetField(id, userFieldPrefix+field)
}

// Fields returns the field names an entity holds, ascending and with the internal
// namespace stripped, so the caller sees exactly the names it set. The presence
// marker and any foreign key a peer injected are not among them.
func (d *Document) Fields(fam Family, id string) []string {
	if !fam.valid() || id == "" {
		return nil
	}
	var out []string
	for _, f := range d.fams[fam].Fields(id) {
		if name, ok := strings.CutPrefix(f, userFieldPrefix); ok {
			out = append(out, name)
		}
	}
	return out
}

// DeleteField removes one field of one entity and returns the operation to
// broadcast. Removing a field an entity does not hold is a no-op, not an error.
// Removing an entity's last field — the presence marker included — makes it cease
// to exist; use [Document.Remove] to clear an entity whole.
func (d *Document) DeleteField(fam Family, id, field string) (crdt.PartOps, error) {
	rm, err := d.records(fam, id)
	if err != nil {
		return crdt.PartOps{}, err
	}
	op, err := rm.DeleteField(id, userFieldPrefix+field)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return partOf(fam, op), nil
}

// Remove clears an entity — every field it holds, presence marker included — and
// returns the operations to broadcast, ascending so the batch is deterministic.
// An entity not present is [ErrUnknownEntity]. The concurrent case, a Remove
// racing a field write, is resolved per field by (clock, site) exactly as it is
// for a [RecordMap]: a write the Remove did not see re-establishes the entity.
func (d *Document) Remove(fam Family, id string) (crdt.PartOps, error) {
	rm, err := d.records(fam, id)
	if err != nil {
		return crdt.PartOps{}, err
	}
	if !rm.HasRecord(id) {
		return crdt.PartOps{}, ErrUnknownEntity
	}
	ops, err := rm.DeleteRecord(id)
	if err != nil {
		return crdt.PartOps{}, err
	}
	return partOf(fam, ops...), nil
}

// validUTF8 reports whether s is valid UTF-8. It is the one check an id shares
// with a [crdt.Map] key, named here so [Document.records] reads plainly.
func validUTF8(s string) bool { return utf8.ValidString(s) }

// EncodeInt renders a signed integer as one varint, so that every replica, on any
// architecture, stores and compares an integer field identically. It is int32 for
// the same reason a [Diagram]'s position is: a 32-bit target — js/wasm — and a
// 64-bit one must agree byte for byte.
func EncodeInt(v int32) []byte { return binary.AppendVarint(nil, int64(v)) }

// DecodeInt reverses [EncodeInt], reporting failure for bytes it did not produce:
// an empty slice, a truncated or trailing varint, or a value outside int32, since
// a peer's operation carries the bytes and a wider value would store differently
// on a 32-bit replica.
func DecodeInt(data []byte) (int32, bool) {
	v, n := binary.Varint(data)
	if n <= 0 || n != len(data) {
		return 0, false
	}
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, false
	}
	return int32(v), true
}

// EncodeBool renders a boolean as a single byte, 0 or 1.
func EncodeBool(v bool) []byte {
	if v {
		return []byte{1}
	}
	return []byte{0}
}

// DecodeBool reverses [EncodeBool], reporting failure for anything but a single 0
// or 1 byte — a peer may send other bytes, and a boolean has exactly two values.
func DecodeBool(data []byte) (bool, bool) {
	if len(data) != 1 {
		return false, false
	}
	switch data[0] {
	case 0:
		return false, true
	case 1:
		return true, true
	default:
		return false, false
	}
}

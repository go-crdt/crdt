package structured

import (
	"sort"

	"github.com/go-crdt/crdt"
)

// A RecordMap is a map of records built over a single [crdt.Map]: an observed-
// remove map whose values are themselves records, each a set of named fields,
// and each field an independent LWW-register.
//
// # Why the fields merge independently
//
// A record is not stored as one opaque value. Were it, two replicas that
// concurrently changed two different fields of the same record — one moved a
// node, the other recoloured it — would collide, and last-writer-wins would
// discard one of the two edits wholesale. Storing each field under its own map
// key makes the two edits touch different keys, so both survive and only a
// genuine same-field conflict is a conflict, resolved by the (clock, site) order.
//
// # Add and remove
//
// A record exists exactly while it has a live field. [RecordMap.DeleteRecord]
// removes the fields it can see, which is the whole record as this replica knows
// it. The policy for the race that leaves — a delete concurrent with a write to
// the same record — is the map's own: it is resolved per field by (clock, site),
// so a field written after the delete it did not see re-establishes the record,
// and one written before loses. That is last-writer-wins at the granularity of a
// field, stated once here rather than arbitrated case by case.
//
// A RecordMap is not safe for concurrent use. The zero RecordMap is unusable —
// construct one with [NewRecordMap], or view an existing map with [RecordsOf].
type RecordMap struct{ m *crdt.Map }

// NewRecordMap returns an empty record map that issues operations as site.
func NewRecordMap(site crdt.SiteID) *RecordMap { return &RecordMap{m: crdt.NewMap(site)} }

// RecordsOf views an existing map as a record map. The two share their storage,
// so an operation applied through one is seen by the other; it is how a document
// keeps a part's records beside its other parts.
func RecordsOf(m *crdt.Map) *RecordMap { return &RecordMap{m: m} }

// Map returns the underlying map, whose Version, OpsSince, Apply and Snapshot
// are the record map's transport and persistence.
func (r *RecordMap) Map() *crdt.Map { return r.m }

// SetField writes value to one field of one record and returns the operation
// describing it, already applied. The record is created by the write if it did
// not exist.
func (r *RecordMap) SetField(rec, field string, value []byte) (crdt.MapOp, error) {
	return r.m.Set(fieldKey(rec, field), value)
}

// GetField returns the value of one field and whether it is set. The value is a
// copy.
func (r *RecordMap) GetField(rec, field string) ([]byte, bool) {
	return r.m.Get(fieldKey(rec, field))
}

// DeleteField removes one field and returns the operation describing it. The
// record ceases to exist when its last field is removed.
func (r *RecordMap) DeleteField(rec, field string) (crdt.MapOp, error) {
	return r.m.Delete(fieldKey(rec, field))
}

// Fields returns the live field names of one record, ascending, so that two
// replicas holding the same operations return the same slice.
func (r *RecordMap) Fields(rec string) []string {
	var out []string
	for _, key := range r.m.Keys() {
		gotRec, field, ok := splitFieldKey(key)
		if ok && gotRec == rec {
			out = append(out, field)
		}
	}
	sort.Strings(out)
	return out
}

// Records returns the identities of every record with at least one live field,
// ascending. A key an operation injected that this map's own writes could not
// have produced is not a record and is skipped.
func (r *RecordMap) Records() []string {
	seen := map[string]struct{}{}
	for _, key := range r.m.Keys() {
		rec, _, ok := splitFieldKey(key)
		if !ok {
			continue
		}
		seen[rec] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for rec := range seen {
		out = append(out, rec)
	}
	sort.Strings(out)
	return out
}

// DeleteRecord removes every live field of one record and returns the operations
// describing it, in ascending field order so the batch is deterministic. It
// removes what this replica can see; see [RecordMap] for the concurrent case.
func (r *RecordMap) DeleteRecord(rec string) ([]crdt.MapOp, error) {
	fields := r.Fields(rec)
	ops := make([]crdt.MapOp, 0, len(fields))
	for _, field := range fields {
		op, err := r.m.Delete(fieldKey(rec, field))
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, nil
}

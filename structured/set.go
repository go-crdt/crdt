package structured

import (
	"sort"
	"unicode/utf8"

	"github.com/go-crdt/crdt"
)

// A Set is a collection of names any number of replicas may add to and take
// from at once: the labels on a card, the people in a conversation, the layers
// that are showing, the tags on a document.
//
// # Why a map of flags is not one
//
// The obvious way to share a set is a [crdt.Map] keyed by the name, holding a
// flag or nothing at all. It converges, and the case it converges badly on is
// the one that happens: Ada adds "urgent" while Grace, who has never seen it,
// takes it away. Both wrote the same key, so one of the two writes wins by the
// (clock, site) order — and which one is nothing to do with what either of them
// knew. Grace can remove a label she has never been shown.
//
// # How this one works
//
// Every addition mints a tag of its own, and a name is in the set while it has
// at least one tag. Removing a name takes away the tags this replica can see,
// which is the removal that was actually asked for: these ones, the ones the
// person was looking at.
//
// A tag nobody has seen is untouched by that, so an addition concurrent with a
// removal survives it. This is usually stated as a policy — "add wins" — and it
// is better read as the absence of one. A removal says what it observed. There
// is no rule here about an addition it did not observe, because there is nothing
// to base one on: neither replica knew about the other, and inventing a winner
// would be inventing knowledge.
//
// It is a [RecordMap] with the fields used as tags and never written twice, so
// the merging is the map's, unchanged: a record exists while it has a live
// field, and a field written after a deletion it did not see re-establishes it.
//
// # What it costs
//
// One map entry per addition that has not been removed, and — because a tag is
// an identity, and an identity here is the identity of an operation — two
// operations per addition rather than one. Adding a name that is already there
// mints another tag rather than doing nothing, and it has to: a removal still
// on its way would otherwise take the name away, having seen every tag it had.
type Set struct {
	r *RecordMap
}

// NewSet returns an empty set this site can edit.
func NewSet(site crdt.SiteID) *Set { return &Set{r: NewRecordMap(site)} }

// SetOf reads a map as a set, for a map that is a part of a [crdt.Composite].
func SetOf(m *crdt.Map) *Set { return &Set{r: RecordsOf(m)} }

// LoadSet rebuilds a set from a snapshot, to be edited as site.
func LoadSet(site crdt.SiteID, snapshot []byte) (*Set, error) {
	m, err := crdt.LoadMap(site, snapshot)
	if err != nil {
		return nil, err
	}
	return &Set{r: RecordsOf(m)}, nil
}

// Map returns the map underneath, which is what is snapshotted and what
// operations are applied to.
func (s *Set) Map() *crdt.Map { return s.r.Map() }

// Records returns the record map underneath, for a caller that wants to read
// the tags themselves.
func (s *Set) Records() *RecordMap { return s.r }

// Site returns the replica this set edits as.
func (s *Set) Site() crdt.SiteID { return s.r.Map().Site() }

// Add puts a name in the set.
//
// It is two operations: one to mint the tag, which has to be an identity no
// replica can mint twice, and one to write it. Both must reach a peer, or the
// name arrives with a tag that replica could mint again after a reload.
//
// Adding a name that is already there is not nothing. It mints another tag,
// because a removal still on its way has seen every tag the name had, and
// would otherwise take it away.
func (s *Set) Add(name string) ([]crdt.MapOp, error) {
	if !validName(name) {
		return nil, crdt.ErrInvalidOp
	}
	tag, mint, err := mintID(s.Map())
	if err != nil {
		return nil, err
	}
	set, err := s.r.SetField(name, encodeID(tag), nil)
	if err != nil {
		// The tag was minted and not written, which leaves the set exactly as
		// it was: an identity nothing refers to. Nothing has to be undone.
		return nil, err
	}
	return []crdt.MapOp{mint, set}, nil
}

// Remove takes a name out of the set, by taking away the tags this replica can
// see. A tag added concurrently, which this replica has not seen, is not one of
// them and the name stays.
//
// A name that is not in the set is [ErrNoChange] rather than a silent nothing,
// so a caller never has to decide whether an empty batch is safe to send.
func (s *Set) Remove(name string) ([]crdt.MapOp, error) {
	if !validName(name) {
		return nil, crdt.ErrInvalidOp
	}
	if !s.r.HasRecord(name) {
		return nil, ErrNoChange
	}
	return s.r.DeleteRecord(name)
}

// Contains reports whether a name is in the set.
func (s *Set) Contains(name string) bool { return validName(name) && s.r.HasRecord(name) }

// Names returns everything in the set, in order, so that two replicas holding
// the same set list it the same way.
func (s *Set) Names() []string { return s.r.Records() }

// Len returns how many names are in the set.
func (s *Set) Len() int { return len(s.r.Records()) }

// Adders returns the replicas whose additions of a name are still standing, in
// order and without repetition. It is what "who put this label on" asks, and it
// is free: a tag is the identity of the operation that minted it, and an
// identity carries its site.
//
// A name held only by tags this version cannot read returns nothing, which is
// the same answer as a name nobody added.
func (s *Set) Adders(name string) []crdt.SiteID {
	if !validName(name) {
		return nil
	}
	seen := map[crdt.SiteID]struct{}{}
	for _, tag := range s.r.Fields(name) {
		id, ok := decodeID(tag)
		if !ok {
			// A field this type did not write. A map holds whatever key an
			// applied operation names, so this is a peer's, not a fault — and
			// it still counts towards the name being in the set, because what
			// makes a name present is having a field at all.
			continue
		}
		seen[id.Site] = struct{}{}
	}
	out := make([]crdt.SiteID, 0, len(seen))
	for site := range seen {
		out = append(out, site)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	if len(out) == 0 {
		return nil
	}
	return out
}

// Tags returns how many additions of a name are still standing. Two is two
// replicas that added it without seeing each other, or one replica that added
// it twice — and it is what a removal will have to take away.
func (s *Set) Tags(name string) int {
	if !validName(name) {
		return 0
	}
	return len(s.r.Fields(name))
}

// validName refuses the empty name and one that is not text, which are the two
// a [crdt.Map] key cannot be.
func validName(name string) bool { return name != "" && utf8.ValidString(name) }

// Apply integrates operations from peers, tolerating duplicates and reordering.
func (s *Set) Apply(ops ...crdt.MapOp) error { return s.Map().Apply(ops...) }

// Version returns what this replica holds, to hand a peer that will send back
// what it is missing; see [Set.OpsSince].
func (s *Set) Version() crdt.VersionVector { return s.Map().Version() }

// OpsSince returns the operations this replica holds that vv does not.
func (s *Set) OpsSince(vv crdt.VersionVector) []crdt.MapOp { return s.Map().OpsSince(vv) }

// Snapshot encodes the whole set, for a joining peer or for persistence.
func (s *Set) Snapshot() []byte { return s.Map().Snapshot() }

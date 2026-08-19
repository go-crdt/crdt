package structured

import (
	"sort"

	"github.com/go-crdt/crdt"
)

// A Sequence is an ordered collection whose items can be moved: the slides of a
// talk, the columns of a board, the order of a bibliography, the layers of a
// drawing, the rows of a list a person drags about.
//
// # Why not a list
//
// [crdt.List] is an RGA, and it is the right structure for text: it decides
// where a new character goes against every other character being typed at the
// same moment, and it does so per character. What it has no operation for is
// moving something that is already in it. Written with the operations it does
// have, a move is a delete and an insert — two operations, and a second replica
// moving the same item at the same time splits them, so the item ends up in
// both places or in neither.
//
// # How this one works
//
// An item carries where it sits as a value, the way a node of a [Tree] carries
// where it sits among its siblings: a rank, with another always available
// between any two, so moving an item is a single write of a single field. Two
// replicas that move the same item at once are then two writes to one field,
// which is a conflict [crdt.Map] already knows how to settle and settles the
// same way on both.
//
// Items are read in order of (rank, identity). The identity is there because
// two replicas inserting at the same place at the same moment mint the same
// rank, and an order that stopped at the rank would not be an order.
//
// # What it gives up against a list
//
// The RGA's per-character judgement, which an opaque item does not need, and
// with it the property that an insert never has to be told about its
// neighbours. This has to read the ranks either side of where an item is going,
// which is one descent of a sorted slice rather than a walk of the collection.
type Sequence struct {
	r *RecordMap
}

// The two fields every item has, beside whatever the caller puts on it.
const (
	seqRankField  = "\x00rank"
	seqValueField = "\x00value"
)

// SeqStart is the place before the first item: passing it to [Sequence.Insert]
// or [Sequence.Move] puts an item at the front.
var SeqStart = ItemID{}

// An ItemID names an item. It is stable across a reload and unique across
// replicas, because it is the identity of the operation that created the item.
type ItemID crdt.ID

// String returns the identity in the form the rest of this package prints one.
func (i ItemID) String() string { return crdt.ID(i).String() }

// key is the identity in the form the map is keyed by.
func (i ItemID) key() string { return encodeID(crdt.ID(i)) }

// IsStart reports whether i names the place before the first item rather than
// an item.
func (i ItemID) IsStart() bool { return crdt.ID(i).IsRoot() }

// NewSequence returns an empty sequence this site can edit.
func NewSequence(site crdt.SiteID) *Sequence { return &Sequence{r: NewRecordMap(site)} }

// SequenceOf reads a map as a sequence, for a map that is a part of a
// [crdt.Composite].
func SequenceOf(m *crdt.Map) *Sequence { return &Sequence{r: RecordsOf(m)} }

// Map returns the map underneath, which is what is snapshotted and what
// operations are applied to.
func (s *Sequence) Map() *crdt.Map { return s.r.Map() }

// Records returns the record map underneath, for reading an item's own fields.
func (s *Sequence) Records() *RecordMap { return s.r }

// Insert puts a new item holding value after the item named by after, or at the
// front for [SeqStart].
//
// A nil value writes no value at all, which is one operation fewer and is what
// a sequence whose items are identities rather than contents wants — the axes
// of a [Sheet], where the item is the row and the row has no value of its own.
// It is not the same as an empty value, which is written.
func (s *Sequence) Insert(after ItemID, value []byte) (ItemID, []crdt.MapOp, error) {
	if !after.IsStart() && !s.r.HasRecord(after.key()) {
		return ItemID{}, nil, crdt.ErrInvalidOp
	}
	fresh, mint, err := mintID(s.Map())
	if err != nil {
		return ItemID{}, nil, err
	}
	item := ItemID(fresh)
	setRank, err := s.rank(item, after)
	if err != nil {
		return ItemID{}, nil, err
	}
	if value == nil {
		return item, []crdt.MapOp{mint, setRank}, nil
	}
	setValue, err := s.r.SetField(item.key(), seqValueField, value)
	if err != nil {
		// The item exists with a place and no value, which reads as an item
		// holding nothing rather than as no item at all. It happens only with
		// the site's last clock tick, and [Sequence.Set] puts it right.
		return ItemID{}, nil, err
	}
	return item, []crdt.MapOp{mint, setRank, setValue}, nil
}

// Move puts an item after the item named by after, or at the front for
// [SeqStart]. It is one operation: an item's place is one field.
func (s *Sequence) Move(item, after ItemID) (crdt.MapOp, error) {
	if item.IsStart() || !s.r.HasRecord(item.key()) {
		return crdt.MapOp{}, crdt.ErrInvalidOp
	}
	if item == after {
		return crdt.MapOp{}, ErrNoChange
	}
	if !after.IsStart() && !s.r.HasRecord(after.key()) {
		return crdt.MapOp{}, crdt.ErrInvalidOp
	}
	return s.rank(item, after)
}

// rank writes the place an item sits at: between the item named by after and
// whatever follows it, skipping the item being moved, which is already there
// when it is moving within the sequence.
func (s *Sequence) rank(item, after ItemID) (crdt.MapOp, error) {
	items := s.Items()
	at := -1
	if !after.IsStart() {
		for i, other := range items {
			if other == after {
				at = i
				break
			}
		}
	}
	var lo, hi string
	for i := at; i >= 0; i-- {
		if items[i] != item {
			lo = s.rankOf(items[i])
			break
		}
	}
	for i := at + 1; i < len(items); i++ {
		if items[i] != item {
			hi = s.rankOf(items[i])
			break
		}
	}
	return s.r.SetField(item.key(), seqRankField, []byte(rankBetween(lo, hi)))
}

// Set replaces what an item holds.
func (s *Sequence) Set(item ItemID, value []byte) (crdt.MapOp, error) {
	if item.IsStart() || !s.r.HasRecord(item.key()) {
		return crdt.MapOp{}, crdt.ErrInvalidOp
	}
	return s.r.SetField(item.key(), seqValueField, value)
}

// Remove takes an item out.
func (s *Sequence) Remove(item ItemID) ([]crdt.MapOp, error) {
	if item.IsStart() || !s.r.HasRecord(item.key()) {
		return nil, crdt.ErrInvalidOp
	}
	return s.r.DeleteRecord(item.key())
}

// Items returns every item, in order.
func (s *Sequence) Items() []ItemID {
	records := s.r.Records()
	items := make([]ItemID, 0, len(records))
	for _, key := range records {
		id, ok := decodeID(key)
		if !ok {
			// A record whose name is not an identity is not an item. Only a
			// peer writing into the map by hand can make one.
			continue
		}
		items = append(items, ItemID(id))
	}
	rank := make(map[ItemID]string, len(items))
	for _, item := range items {
		rank[item] = s.rankOf(item)
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if rank[a] != rank[b] {
			return rank[a] < rank[b]
		}
		// Two replicas inserting at the same place at the same moment mint the
		// same rank, so the identity is what makes this an order at all.
		return idLess(crdt.ID(a), crdt.ID(b))
	})
	return items
}

// Values returns what every item holds, in order.
func (s *Sequence) Values() [][]byte {
	items := s.Items()
	out := make([][]byte, 0, len(items))
	for _, item := range items {
		value, _ := s.Value(item)
		out = append(out, value)
	}
	return out
}

// Value returns what an item holds, and whether the item exists.
func (s *Sequence) Value(item ItemID) ([]byte, bool) {
	if item.IsStart() || !s.r.HasRecord(item.key()) {
		return nil, false
	}
	value, _ := s.r.GetField(item.key(), seqValueField)
	return value, true
}

// At returns the item at a position, and whether there is one there.
func (s *Sequence) At(pos int) (ItemID, bool) {
	items := s.Items()
	if pos < 0 || pos >= len(items) {
		return ItemID{}, false
	}
	return items[pos], true
}

// IndexOf returns where an item sits, or -1 if it is not there.
func (s *Sequence) IndexOf(item ItemID) int {
	for i, other := range s.Items() {
		if other == item {
			return i
		}
	}
	return -1
}

// Len returns how many items there are.
func (s *Sequence) Len() int { return len(s.Items()) }

func (s *Sequence) rankOf(item ItemID) string {
	value, _ := s.r.GetField(item.key(), seqRankField)
	return string(value)
}

// SetField sets one of an item's own fields, beside what it holds.
func (s *Sequence) SetField(item ItemID, field string, value []byte) (crdt.MapOp, error) {
	if item.IsStart() || field == seqRankField || field == seqValueField {
		return crdt.MapOp{}, crdt.ErrInvalidPart
	}
	return s.r.SetField(item.key(), field, value)
}

// GetField reads one of an item's own fields.
func (s *Sequence) GetField(item ItemID, field string) ([]byte, bool) {
	return s.r.GetField(item.key(), field)
}

// Apply takes operations from a peer.
func (s *Sequence) Apply(ops ...crdt.MapOp) error { return s.Map().Apply(ops...) }

// Version returns what this replica has seen.
func (s *Sequence) Version() crdt.VersionVector { return s.Map().Version() }

// OpsSince returns the operations a peer at vv has not seen.
func (s *Sequence) OpsSince(vv crdt.VersionVector) []crdt.MapOp { return s.Map().OpsSince(vv) }

// Snapshot returns the sequence as bytes.
func (s *Sequence) Snapshot() []byte { return s.Map().Snapshot() }

// LoadSequence reads a snapshot back, as the given site.
func LoadSequence(site crdt.SiteID, snapshot []byte) (*Sequence, error) {
	m, err := crdt.LoadMap(site, snapshot)
	if err != nil {
		return nil, err
	}
	return &Sequence{r: RecordsOf(m)}, nil
}

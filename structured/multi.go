package structured

import (
	"encoding/binary"
	"sort"
	"strconv"

	"github.com/go-crdt/crdt"
)

// A MultiRegister is a value that two replicas are allowed to disagree about,
// where the disagreement is the answer rather than something to be settled.
//
// # What a register cannot say
//
// [Register] resolves every concurrent write by the (clock, site) order, which
// is exactly right when a losing write is of no interest: a cursor position, a
// window size, a colour somebody picked. It is wrong when it is. Two people
// rename the same file at the same moment and one of the names is gone, with
// nothing anywhere recording that there was ever a second one — not in the
// state, not in the operations, not in anything a reader could show. The
// register did not choose badly; it has no way of saying that a choice was made.
//
// # How this one works
//
// Each replica writes only its own key, as a [Counter] does, so no two replicas
// ever write the same one and the map underneath is doing no merging at all.
// What a replica writes is its value together with a version vector: how many
// times it has written, and how many times it had seen every other replica
// write when it did.
//
// A value is live when no other value's vector strictly dominates it. Two
// replicas that wrote without seeing each other have vectors neither of which
// dominates the other, so both values are live and both are read. A replica
// that writes having seen the other dominates it, and its value is the only one
// read.
//
// That last sentence is also the whole of resolving a conflict: choosing one of
// the values is writing it, and writing it dominates everything the writer could
// see, including the value they chose. There is no separate operation for
// settling, and none is needed — see [MultiRegister.Set].
//
// # What it costs
//
// One key per replica that has ever written, and a vector of one entry per
// replica that has ever written inside each of those. That is the same shape a
// [Counter] has, for the same reason, and it is the price of being able to say
// that two writes did not see each other — a Lamport clock cannot, because it
// gives a total order, and the question is which pairs are unordered.
type MultiRegister struct {
	m *crdt.Map
}

// NewMultiRegister returns an empty register this site can write.
func NewMultiRegister(site crdt.SiteID) *MultiRegister {
	return &MultiRegister{m: crdt.NewMap(site)}
}

// MultiRegisterOf reads a map as a multi-value register, for a map that is a
// part of a [crdt.Composite].
func MultiRegisterOf(m *crdt.Map) *MultiRegister { return &MultiRegister{m: m} }

// LoadMultiRegister rebuilds one from a snapshot, to be written as site.
func LoadMultiRegister(site crdt.SiteID, snapshot []byte) (*MultiRegister, error) {
	m, err := crdt.LoadMap(site, snapshot)
	if err != nil {
		return nil, err
	}
	return &MultiRegister{m: m}, nil
}

// Map returns the map underneath, which is what is snapshotted and what
// operations are applied to.
func (r *MultiRegister) Map() *crdt.Map { return r.m }

// Site returns the replica this register writes as.
func (r *MultiRegister) Site() crdt.SiteID { return r.m.Site() }

// A Reading is one replica's writing of the register, as it stands now.
type Reading struct {
	// Site is the replica that wrote it.
	Site crdt.SiteID
	// Value is what that replica wrote, or nil if it cleared the register.
	Value []byte
	// Cleared is true when that replica took the value away rather than
	// writing one. A clear that nobody has seen is as live as a value nobody
	// has seen, which is what makes "somebody deleted this while I was
	// renaming it" a thing a reader can be shown.
	Cleared bool
}

// Set writes value, and in doing so settles every disagreement this replica can
// see: the write's vector dominates every reading it was made against, so the
// register reads as this one value until somebody who has not seen it writes
// again.
//
// Choosing between the values of a conflict is therefore not a separate
// operation. Write the one that was chosen.
func (r *MultiRegister) Set(value []byte) (crdt.MapOp, error) { return r.write(value, false) }

// Clear takes the value away, on the same terms as [MultiRegister.Set]: it is a
// writing of its own, it dominates everything this replica has seen, and a
// concurrent write that has not seen it stays live beside it.
func (r *MultiRegister) Clear() (crdt.MapOp, error) { return r.write(nil, true) }

func (r *MultiRegister) write(value []byte, cleared bool) (crdt.MapOp, error) {
	seen := r.frontier()
	me := r.m.Site()
	seen[me]++ // this write, which is one more than the writer had made
	return r.m.Set(strconv.FormatUint(uint64(me), 10), encodeReading(seen, value, cleared))
}

// frontier returns everything this replica has seen: for each site, the highest
// number of writes any reading credits it with.
//
// It is the componentwise maximum rather than this replica's own record,
// because a reading arrives carrying what its writer had seen, so a replica
// learns what a third one has done from the second without ever hearing from it
// directly.
func (r *MultiRegister) frontier() map[crdt.SiteID]uint64 {
	out := map[crdt.SiteID]uint64{}
	for _, entry := range r.readings() {
		for site, n := range entry.vector {
			if n > out[site] {
				out[site] = n
			}
		}
	}
	return out
}

// an entry as stored: who wrote it, what they had seen, and what they wrote.
type entry struct {
	Reading
	vector map[crdt.SiteID]uint64
}

// readings returns every replica's writing, decodable ones only, in site order.
func (r *MultiRegister) readings() []entry {
	keys := r.m.Keys()
	out := make([]entry, 0, len(keys))
	for _, key := range keys {
		site, err := strconv.ParseUint(key, 10, 64)
		if err != nil || strconv.FormatUint(site, 10) != key {
			// A key no replica of this type wrote. A map holds whatever key an
			// applied operation names, so this is a peer's, not a fault.
			//
			// The second half of that test is what makes a site's key its own:
			// ParseUint accepts leading zeros, so "1", "01" and "0001" all
			// name site 1, and a reading is supposed to be one per replica.
			// Accepting all three would put three of them in the list, each
			// claiming to be the same replica, and whichever carried the
			// highest vector would hide the one that replica actually wrote.
			// Only the canonical spelling is a key this type writes.
			continue
		}
		raw, _ := r.m.Get(key)
		vector, value, cleared, ok := decodeReading(raw)
		if !ok {
			// Bytes this version cannot read: a later one wrote them, or a peer
			// wrote them by hand. Skipped rather than guessed at.
			continue
		}
		out = append(out, entry{
			Reading: Reading{Site: crdt.SiteID(site), Value: value, Cleared: cleared},
			vector:  vector,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Site < out[j].Site })
	return out
}

// Readings returns every writing of the register that nothing has superseded,
// in site order.
//
// One reading is the ordinary case. More than one is a disagreement: two or
// more replicas wrote without seeing each other, and there is no fact anywhere
// that says which of them is the answer. Nothing here invents one.
func (r *MultiRegister) Readings() []Reading {
	all := r.readings()
	out := make([]Reading, 0, len(all))
	for i, e := range all {
		if dominated(e, all, i) {
			continue
		}
		out = append(out, e.Reading)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// dominated reports whether some other reading saw this one and went past it.
func dominated(e entry, all []entry, self int) bool {
	for j, other := range all {
		if j == self {
			continue
		}
		if dominates(other.vector, e.vector) {
			return true
		}
	}
	return false
}

// dominates reports whether a saw everything b saw and at least one thing more.
// Two vectors of which neither dominates the other were written concurrently,
// which is the question this type exists to answer.
func dominates(a, b map[crdt.SiteID]uint64) bool {
	more := false
	for site, n := range b {
		switch {
		case a[site] < n:
			return false
		case a[site] > n:
			more = true
		}
	}
	if more {
		return true
	}
	// Equal on everything b names; a dominates if it names something else.
	for site, n := range a {
		if n > b[site] {
			return true
		}
	}
	return false
}

// Values returns the values of the live readings, in site order, leaving out
// any replica that cleared the register.
//
// It is what a reader that has no way of showing a conflict should use with
// [MultiRegister.Conflicted]: one value, or a list to be offered.
func (r *MultiRegister) Values() [][]byte {
	var out [][]byte
	for _, reading := range r.Readings() {
		if reading.Cleared {
			continue
		}
		out = append(out, reading.Value)
	}
	return out
}

// Value returns the one value the register holds, and whether it holds exactly
// one. A register nobody has written, one everybody agrees is cleared, and one
// two replicas disagree about all report false — so a caller that ignores the
// second return reads no value rather than an arbitrary one.
func (r *MultiRegister) Value() ([]byte, bool) {
	readings := r.Readings()
	if len(readings) != 1 || readings[0].Cleared {
		return nil, false
	}
	return readings[0].Value, true
}

// Conflicted reports whether more than one writing is live: two replicas wrote
// without seeing each other, and both writings stand.
func (r *MultiRegister) Conflicted() bool { return len(r.Readings()) > 1 }

// Apply integrates operations from peers, tolerating duplicates and reordering.
func (r *MultiRegister) Apply(ops ...crdt.MapOp) error { return r.m.Apply(ops...) }

// Version returns what this replica holds, to hand a peer that will send back
// what it is missing; see [MultiRegister.OpsSince].
func (r *MultiRegister) Version() crdt.VersionVector { return r.m.Version() }

// OpsSince returns the operations this replica holds that vv does not.
func (r *MultiRegister) OpsSince(vv crdt.VersionVector) []crdt.MapOp { return r.m.OpsSince(vv) }

// Snapshot encodes the whole register, for a joining peer or for persistence.
func (r *MultiRegister) Snapshot() []byte { return r.m.Snapshot() }

// The encoding of one writing: the vector, then whether there is a value, then
// the value. Sites go in ascending order, so two replicas that computed the
// same vector encode the same bytes — which is what lets a snapshot be compared
// byte for byte.
func encodeReading(vector map[crdt.SiteID]uint64, value []byte, cleared bool) []byte {
	sites := make([]crdt.SiteID, 0, len(vector))
	for site := range vector {
		sites = append(sites, site)
	}
	sort.Slice(sites, func(i, j int) bool { return sites[i] < sites[j] })

	out := binary.AppendUvarint(nil, uint64(len(sites)))
	for _, site := range sites {
		out = binary.AppendUvarint(out, uint64(site))
		out = binary.AppendUvarint(out, vector[site])
	}
	if cleared {
		return append(out, 0)
	}
	out = append(out, 1)
	return append(out, value...)
}

func decodeReading(in []byte) (vector map[crdt.SiteID]uint64, value []byte, cleared bool, ok bool) {
	n, used := binary.Uvarint(in)
	if used <= 0 {
		return nil, nil, false, false
	}
	in = in[used:]
	// A count larger than the remaining bytes allow is a corrupt header, and it
	// is refused before anything is allocated for it — the rule ParsePartOps
	// states and decodeCell keeps. Each entry is two varints of at least one
	// byte each, so a vector of n entries needs at least 2n bytes; without this
	// a five-byte value asked for a map of any size the peer liked, and one
	// found by fuzzing spent three minutes in this line.
	if n > uint64(len(in))/2 {
		return nil, nil, false, false
	}
	vector = make(map[crdt.SiteID]uint64, n)
	var last uint64
	for i := uint64(0); i < n; i++ {
		site, used := binary.Uvarint(in)
		if used <= 0 {
			return nil, nil, false, false
		}
		in = in[used:]
		count, used := binary.Uvarint(in)
		if used <= 0 {
			return nil, nil, false, false
		}
		in = in[used:]
		if i > 0 && site <= last {
			// Sites out of order, or one named twice: not something this type
			// writes, and accepting it would make two encodings of one vector.
			return nil, nil, false, false
		}
		last = site
		vector[crdt.SiteID(site)] = count
	}
	if len(in) == 0 {
		return nil, nil, false, false
	}
	switch in[0] {
	case 0:
		if len(in) != 1 {
			return nil, nil, false, false
		}
		return vector, nil, true, true
	case 1:
		return vector, append([]byte(nil), in[1:]...), false, true
	}
	return nil, nil, false, false
}

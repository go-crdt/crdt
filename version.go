package crdt

import (
	"encoding/binary"
	"sort"
)

// A VersionVector records, per site, the highest sequence number a replica has
// applied. Because a site's sequence numbers have no gaps and [Doc] refuses to
// apply an operation until its predecessor has landed, the vector describes a
// replica's state exactly: it holds operation Seq from Site if and only if
// Seq <= v[Site].
//
// The nil vector is valid and reads as "nothing applied".
type VersionVector map[SiteID]uint64

// Get returns the highest sequence number applied for site, or zero.
func (v VersionVector) Get(site SiteID) uint64 { return v[site] }

// Includes reports whether the operation named by id has been applied.
func (v VersionVector) Includes(id ID) bool { return id.Seq <= v[id.Site] }

// Clone returns an independent copy. The clone of a nil vector is empty but
// non-nil, so it can be written to.
func (v VersionVector) Clone() VersionVector {
	out := make(VersionVector, len(v))
	for site, seq := range v {
		out[site] = seq
	}
	return out
}

// Equal reports whether v and other describe the same set of operations. Sites
// recorded with a zero sequence number count as absent, so a nil vector equals
// an empty one.
func (v VersionVector) Equal(other VersionVector) bool {
	for site, seq := range v {
		if seq != other[site] {
			return false
		}
	}
	for site, seq := range other {
		if seq != v[site] {
			return false
		}
	}
	return true
}

// promises reports whether the vector describes any operation at all. A site
// recorded with a zero sequence number counts as absent, as it does for Equal,
// so this is not len(v) != 0.
func (v VersionVector) promises() bool {
	for _, seq := range v {
		if seq != 0 {
			return true
		}
	}
	return false
}

// covers reports whether v accounts for every operation other describes.
//
// It is the question a replica asks before working out what a peer is missing:
// if the peer covers what this replica holds, there is nothing to send, and the
// answer costs a walk of the vector rather than of the history. [Composite]
// asks it once per part, of which there may be hundreds.
func (v VersionVector) covers(other VersionVector) bool {
	for site, seq := range other {
		if v[site] < seq {
			return false
		}
	}
	return true
}

// sites returns the sites with a non-zero sequence number, ascending, so that
// anything derived from a vector — a snapshot, a comparison — is deterministic
// rather than dependent on Go's map iteration order.
func (v VersionVector) sites() []SiteID {
	out := make([]SiteID, 0, len(v))
	for site, seq := range v {
		if seq != 0 {
			out = append(out, site)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// MarshalBinary encodes the vector. Entries are written in ascending site
// order, so the same state always produces the same bytes and a caller may
// compare or cache them.
//
// A replica sends this to be told what it has missed; see [Doc.OpsSince].
func (v VersionVector) MarshalBinary() ([]byte, error) {
	sites := v.sites()
	out := binary.AppendUvarint(nil, uint64(len(sites)))
	for _, site := range sites {
		out = binary.AppendUvarint(out, uint64(site))
		out = binary.AppendUvarint(out, v[site])
	}
	return out, nil
}

// UnmarshalBinary decodes a vector written by MarshalBinary. A site listed
// twice, a sequence number of zero, and trailing bytes are all rejected: each
// would leave what the vector means dependent on decoding order.
func (v *VersionVector) UnmarshalBinary(data []byte) error {
	count, used := uvarint(data)
	if used <= 0 {
		return ErrMalformed
	}
	rest := data[used:]
	// Each entry is at least two bytes, so a count larger than the remaining
	// bytes allow is a corrupt header — refuse it before allocating for it.
	if count > uint64(len(rest)) {
		return ErrMalformed
	}
	out := make(VersionVector, count)
	for range count {
		site, used := uvarint(rest)
		if used <= 0 {
			return ErrMalformed
		}
		rest = rest[used:]
		// A sequence number above the clock ceiling names an operation no replica
		// could have issued — the refusal every snapshot loader here already
		// makes, and this decoder did not. See [MaxClock].
		seq, used := uvarint(rest)
		if used <= 0 || seq == 0 || seq > MaxClock {
			return ErrMalformed
		}
		rest = rest[used:]
		if _, dup := out[SiteID(site)]; dup {
			return ErrMalformed
		}
		out[SiteID(site)] = seq
	}
	if len(rest) != 0 {
		return ErrMalformed
	}
	*v = out
	return nil
}

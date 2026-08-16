package crdt

import "strconv"

// SiteID identifies a replica. It is chosen by the caller and must be distinct
// for every replica that concurrently edits a document; two replicas sharing a
// SiteID can mint the same [ID] for different characters, which breaks
// convergence.
//
// The package never generates one itself: a random or clock-derived identifier
// is unavailable, or not reproducible, under js/wasm. See [DeriveSiteID].
type SiteID uint64

// ID names a single operation, and — for an insertion — the character that
// operation created. It is unique across replicas because Site is unique and
// Seq counts that site's own operations.
//
// The zero ID is the document root: the virtual character that precedes all
// content. It is a valid insertion origin and is never the ID of a real
// operation, because Seq starts at one.
type ID struct {
	Site SiteID
	Seq  uint64
}

// IsRoot reports whether id names the virtual character at the start of every
// document rather than a real operation.
//
// The test is on Seq alone, because Seq counts from one: no operation ever
// carries zero, whatever its site. A decoder that only compared against the zero
// ID would let a sequence number of zero paired with a non-zero site through as
// if it named something real.
func (id ID) IsRoot() bool { return id.Seq == 0 }

// wellFormed reports whether id is either a real operation or exactly the root.
// The root has no site, so a zero sequence number paired with a site is a
// contradiction and comes only from corrupt or hostile input.
func (id ID) wellFormed() bool { return id.Seq != 0 || id.Site == 0 }

// String renders the ID as "seq@site", the notation used in the CRDT
// literature. The root prints as "root".
func (id ID) String() string {
	if id.IsRoot() {
		return "root"
	}
	return strconv.FormatUint(id.Seq, 10) + "@" + strconv.FormatUint(uint64(id.Site), 10)
}

// DeriveSiteID hashes b into a SiteID with FNV-1a. It gives callers a
// deterministic way to turn an identifier they already hold — a session token, a
// user ID, a tab identifier — into a replica identity, on any platform,
// including js/wasm where the usual sources of randomness are absent.
//
// Distinctness is the caller's responsibility: distinct b almost always yields
// distinct SiteIDs, but a hash cannot promise it.
func DeriveSiteID(b []byte) SiteID {
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)
	h := uint64(offset)
	for _, c := range b {
		h ^= uint64(c)
		h *= prime
	}
	return SiteID(h)
}

// before reports whether an operation with Lamport timestamp aClock from site
// aSite sorts before one with bClock/bSite in the RGA total order: ascending
// Lamport timestamp, ties broken by ascending site.
//
// The order is total because a site never issues two operations with the same
// timestamp, so a tie implies two distinct sites.
func before(aClock uint64, aSite SiteID, bClock uint64, bSite SiteID) bool {
	if aClock != bClock {
		return aClock < bClock
	}
	return aSite < bSite
}

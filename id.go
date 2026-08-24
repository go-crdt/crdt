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
//
// # Across instances that have never spoken
//
// That responsibility has a sharp edge the moment more than one instance is
// involved. A site identity has to be unique across every replica that will
// ever meet, not merely across the ones one server hands out — two operations
// claiming one identity is the thing this package rests on not happening, and
// no merge can recover from it.
//
// So b must carry the instance. Derive from something SCOPED — an
// eduPersonPrincipalName, a subject-id, an OIDC issuer and subject together, a
// URL — and never from a bare local identifier: two instances that each have a
// user "42" would derive the same site from it, on purpose, because a hash is
// a function and that is what a function does. There is a test of both.
//
// Measured on scoped identifiers of the shape a SAML assertion carries: twenty
// million of them over four thousand scopes, no collisions, which is what a
// uniform 64-bit hash gives at that size. See federation_test.go.
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

// before reports whether the operation identified by a, with Lamport timestamp
// aClock, sorts before the one identified by b in the RGA total order:
// ascending timestamp, then ascending site, then ascending sequence number.
//
// The last of those three looks redundant and is not. A site never issues two
// operations with the same timestamp — its clock advances at least once per
// operation — so for anything this package mints the sequence number is never
// reached, and the order is exactly what it always was. But that is a claim
// about a site's whole history, and an arriving operation is one operation: no
// receiver can check it, because nothing in an operation carries the rest of
// them. A peer could therefore hand over two operations from one site sharing a
// timestamp, and (clock, site) then failed to be an order at all — two distinct
// operations compared equal in both directions.
//
// What that cost was not a wrong ordering but an unreadable document. Both walks
// that place a character use this comparison, and where it is ambiguous they
// need not agree: integration put such a pair one way round, the scan [Load]
// re-derives put it the other, and the snapshot was then refused as one no
// replica could have produced. A server that accepted the batch, broadcast it
// and persisted it could not afterwards open its own file — a peer's bytes
// deciding whether a document survives a restart. [List] had it by the same
// route. [Map] did not, and for a reason worth keeping in mind: a site's
// operations are integrated in sequence order whatever order they arrive in, so
// its ties always resolved the same way on every replica.
//
// The sequence number closes it because (site, seq) is unique by construction,
// so the three together are a strict total order on operations, derived from
// what every operation already carries.
func before(aClock uint64, a ID, bClock uint64, b ID) bool {
	if aClock != bClock {
		return aClock < bClock
	}
	if a.Site != b.Site {
		return a.Site < b.Site
	}
	return a.Seq < b.Seq
}

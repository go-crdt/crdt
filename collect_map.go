package crdt

import (
	"encoding/binary"
	"sort"
)

// Collect drops the tombstones nothing can still be confused by.
//
// A map keeps one record per key rather than one per edit, so it has far less
// past to give back than a text or a list: what accumulates is not the history
// of a key but the keys that were deleted. A diagram whose nodes come and go
// carries every node it ever held, as a record saying that node is gone.
//
// A tombstone is kept for one reason, which [Map.integrate] states: it is what
// stops an older set resurrecting a key somebody has since deleted. So it may go
// once no replica can still send a write that would lose to it.
//
// That takes two things, and for a long time this asked for only one of them.
//
// The first is a version every replica has delivered, exactly as elsewhere; see
// [Doc.Collect] for what that means and who can know it. The second is a clock,
// and it is not the same question. A version says which operations everybody
// holds. It says nothing about the clocks of the operations still in flight: a
// site that has seen nothing writes at clock one, however far along everyone
// else is. So a write from a site that had not seen the deletion can arrive
// afterwards carrying a clock that beats it — and a replica that dropped the
// tombstone has nothing left to make that comparison against, while one that
// kept it brings the key back. Two replicas, the same operations, different
// documents. See TestCollectingLosesAComparisonALaterWriteNeeded.
//
// So below is a clock floor: a promise that no operation with a clock at or
// under it can still arrive. A tombstone goes when its own clock is at or under
// that floor, which is what makes every write that can still come strictly
// later than it — and a write that is strictly later beats it, comes back, and
// wants no comparison. What needed the tombstone was a write at or below its
// clock, and the floor is the promise there will not be one.
//
// A replica cannot work the floor out alone, for the same reason it cannot work
// out the version: it does not know who is out there. [Map.LastClocks] is what
// it can offer somebody who does — a server, which everything passes through —
// and the arithmetic is a minimum over every site that could still send.
//
// # What Yjs does instead
//
// It does not do this at all, and the difference is worth stating because it is
// the whole reason a floor is needed here and nowhere in that codebase.
//
// Yjs collects a deleted item by replacing its *content* and keeping the item:
// Item.gc, called with parentGCd false, sets this.content = new
// ContentDeleted(this.length) and leaves the item in the store with its id, its
// origins and its key. The other branch, for an item whose parent is going too,
// is replaceStruct(store, this, new GC(this.id, this.length)) — which also keeps
// the id. Either way the identity survives, so an operation arriving late still
// finds something to lose to, and no precondition beyond causal delivery is
// needed. (Read in yjs 13.6.32, src/structs/Item.js and
// src/utils/Transaction.js.)
//
// What that reclaims is the content. A map's tombstone has none: the record is
// the space, and giving it back means giving back the identity. So this is an
// economy Yjs does not attempt, and the floor is its price rather than the
// repair of a mistake. A diagram whose nodes come and go is what makes the
// price worth paying; see [Diagram.Collect].
//
// # Why this one needs no format version and no floor
//
// A map already gives back a sequence number without its operation: a second
// write to a key overwrites the first, so the first is gone and
// [Map.OpsSince] reports the gap as [MapSuperseded] rather than pretending the
// operation is still here. Collecting a tombstone frees a sequence number the
// same way, and the same span covers it. Nothing on the wire changes, no peer
// has to be re-seeded, and a map's loader has no completeness ledger to relax
// because a map never had a complete history to hold it to.
//
// # What it costs
//
// [Map.Stamp] can no longer say when a collected key was deleted; it answers as
// it does for a key that never existed. That is the whole of what is lost from
// reading.
//
// What is lost from *misuse* is worse than elsewhere, which is why the guard
// below exists. Given a version some replica has not delivered, or a floor no
// caller could promise, that replica's write arrives naming a key whose
// tombstone is gone, finds no record to lose to, and the key comes back. So a
// map remembers the highest clock it collected under and refuses a write at or
// below it for a key it does not hold, with [ErrStranded]. Given both of the
// things above, no such operation can arrive at all and the guard never fires;
// when it does, it is naming the mistake, and [Composite.Apply] passes it on
// rather than dropping it.
//
// The guard catches half of that misuse. The other half arrives from the
// opposite direction and nothing here can see it: the replica that has not
// delivered the deletion is also the one that will ask for it. [Map.OpsSince]
// answers for a collected stretch with [MapSuperseded], because under correct
// use those sequence numbers are accounted for — so that replica's version
// vector advances over the deletion without it ever learning what the operation
// did, and it goes on holding a value every other replica removed. Its version
// vector then equals theirs, which is what makes it permanent: there is nothing
// left for a catch-up to send. Found this way in a server that collected against
// the participants it had a connection to rather than every participant it had
// ever had: two participants, one deletion, and the one that was away came back
// still holding the key, with a version equal to the server's.
//
// Collect reports how many tombstones it dropped.
func (m *Map) Collect(stable VersionVector, below uint64) int {
	// The floor and not the highest clock actually dropped, which is what this
	// counted before. What is dropped depends on what this replica happened to
	// hold: one that had already given a record back, or never had it, drops
	// fewer and would remember a lower clock. That is replica-relative state,
	// and it is in the snapshot — so two replicas that had applied the same
	// operations wrote different bytes, which is the one thing a snapshot
	// promises not to do. The floor is the same number for everybody asked with
	// it, and it is at least as high as anything it allows to go.
	if below > MaxClock {
		// A caller saying nothing at all can still arrive says it with the
		// largest number there is. Nothing above MaxClock names an operation
		// any replica could have issued, and the snapshot will not carry one.
		below = MaxClock
	}
	if below > m.collectedBelow {
		m.collectedBelow = below
	}
	dropped := 0
	for key, rec := range m.records {
		if !rec.dead || !stable.Includes(rec.id) || rec.clock > below {
			continue
		}
		delete(m.records, key)
		dropped++
	}
	return dropped
}

// CollectedBelow reports the floor this map has been collected against, and
// zero if it has never been collected.
//
// It is the floor it was asked with rather than the highest clock it actually
// dropped, and the difference matters: what is dropped depends on what this
// replica happened to hold, so remembering that would put replica-relative
// state in the snapshot and two replicas that had applied the same operations
// would write different bytes.
//
// It is a clock rather than a version, unlike [Doc.Floor] and [List.Floor],
// because what a map has to recognise is not an operation it dropped but a
// write that would have lost to one. That comparison is by clock, so the guard
// is too.
func (m *Map) CollectedBelow() uint64 { return m.collectedBelow }

// resurrects reports whether an operation would bring back a key whose
// tombstone was collected.
//
// A write at or below the clock a tombstone was collected under, naming a key
// this replica does not hold, is either that or an operation that could not
// have been issued: under correct use nothing below that clock can still
// arrive, because collecting asked for a version every replica had delivered.
func (m *Map) resurrects(op MapOp) bool {
	if m.collectedBelow == 0 || op.Kind == MapSuperseded {
		return false
	}
	if op.Clock > m.collectedBelow {
		return false
	}
	_, held := m.records[op.Key]
	return !held
}

// Collect drops what every map part of this document can spare, given what
// every replica has delivered, and reports how many tombstones went.
//
// Only the maps. A text and a list had one too and it was withdrawn — see the
// note in text.go — so a document made of them gives nothing back for now. A
// map is unaffected because it re-points nothing, which is exactly what the
// other two got wrong.
//
// A part not named in stable is left alone. That is not a nicety: collecting a
// part against a version nobody vouched for is exactly the mistake the other
// [Doc.Collect] guards against, and the guard cannot see a part the caller
// forgot.
//
// There is deliberately no rewrite for a [Composite] — see [Doc.Rewritten] —
// because rewriting mints new identities and the structured layer keeps rich
// text marks, tree parents and sequence positions against the identities of the
// characters they describe. Collecting a map keeps every identity it does not
// drop, and drops only records that are already invisible and already agreed to
// be gone.
func (c *Composite) Collect(stable CompositeVersion, below CompositeClocks) int {
	n := 0
	for name, m := range c.maps {
		part := Part{Kind: PartMap, Name: name}
		held, named := stable[part]
		floor, promised := below[part]
		if !named || !promised {
			// A part nobody promised anything about keeps everything. Each map
			// has a clock of its own, so one floor cannot stand for another's:
			// a part left out is left alone rather than collected against
			// somebody else's arithmetic.
			continue
		}
		n += m.Collect(held, floor)
	}
	return n
}

// CompositeClocks is a clock floor for each part of a document: a promise that
// no operation with a clock at or under it can still arrive at that part. It is
// the second half of what [Composite.Collect] needs, and a part it does not name
// is a part nothing is given back from.
//
// One entry per part and not one number, because each map carries a Lamport
// clock of its own and they do not run together.
type CompositeClocks map[Part]uint64

// collected reports whether any map part has given something back, which is
// what makes this document's bytes something a newcomer replaying its history
// cannot reproduce: the clock a map collected under is in its snapshot and in
// no operation.
func (c *Composite) collected() bool {
	for _, m := range c.maps {
		if m.collectedBelow > 0 {
			return true
		}
	}
	return false
}

// LastClocks reports, for each site, the clock of the last operation this
// replica integrated from it.
//
// A site issues in increasing clock order and a transport delivers one site's
// operations in order, so whatever has not arrived yet from that site carries a
// clock above the one reported here. That is the whole of what a replica can say
// about what it has not seen, and it is what a collection floor is built from —
// by somebody who knows which sites there are to take the minimum over. A
// replica cannot: a site it has never heard from is missing from this map
// entirely, and that site is exactly the one whose write is still on its way.
//
// The value is a copy.
func (m *Map) LastClocks() map[SiteID]uint64 {
	out := make(map[SiteID]uint64, len(m.lastClock))
	for site, clock := range m.lastClock {
		out[site] = clock
	}
	return out
}

// MarshalBinary encodes the floors, sorted by part so that two callers holding
// the same clocks produce the same bytes.
//
// A clock above [MaxClock] is refused for the reason a sequence number is: it
// is not something this package can have produced, so carrying it would be
// carrying somebody else's mistake onto the wire.
func (c CompositeClocks) MarshalBinary() ([]byte, error) {
	parts := make([]Part, 0, len(c))
	for p := range c {
		if !p.valid() {
			return nil, ErrInvalidPart
		}
		if c[p] > MaxClock {
			return nil, ErrInvalidOp
		}
		parts = append(parts, p)
	}
	sort.Slice(parts, func(i, j int) bool { return partLess(parts[i], parts[j]) })
	out := binary.AppendUvarint(nil, uint64(len(parts)))
	for _, p := range parts {
		out = append(out, byte(p.Kind))
		out = appendKey(out, p.Name)
		out = binary.AppendUvarint(out, c[p])
	}
	return out, nil
}

// UnmarshalBinary reads what MarshalBinary wrote, and refuses anything else:
// these arrive from a peer, so nothing about them is trusted.
func (c *CompositeClocks) UnmarshalBinary(in []byte) error {
	r := &reader{buf: in}
	n, ok := r.uvarint()
	if !ok || n > uint64(len(r.buf)) {
		return ErrMalformed
	}
	out := CompositeClocks{}
	var last Part
	for i := uint64(0); i < n; i++ {
		kind, ok := r.bytes(1)
		if !ok {
			return ErrMalformed
		}
		name, ok := r.sized()
		if !ok {
			return ErrMalformed
		}
		p := Part{Kind: PartKind(kind[0]), Name: string(name)}
		if !p.valid() {
			return ErrMalformed
		}
		if i > 0 && !partLess(last, p) {
			// Out of order, or one part named twice: not something
			// MarshalBinary writes, and accepting it would make two encodings
			// of one set of floors.
			return ErrMalformed
		}
		last = p
		clock, ok := r.uvarint()
		if !ok || clock > MaxClock {
			return ErrMalformed
		}
		out[p] = clock
	}
	if len(r.buf) != 0 {
		return ErrMalformed
	}
	*c = out
	return nil
}

// Clocks reports how far each map part of this document has counted.
//
// It is what a participant tells a server so the server can build a floor to
// collect against: this replica writes above these, on every part, whatever it
// does next. A text and a list are left out, having nothing to give back.
func (c *Composite) Clocks() CompositeClocks {
	out := CompositeClocks{}
	for name, m := range c.maps {
		out[Part{Kind: PartMap, Name: name}] = m.Clock()
	}
	return out
}

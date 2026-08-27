package crdt

// Collect drops the tombstones nothing can still be confused by.
//
// A map keeps one record per key rather than one per edit, so it has far less
// past to give back than a text or a list: what accumulates is not the history
// of a key but the keys that were deleted. A diagram whose nodes come and go
// carries every node it ever held, as a record saying that node is gone.
//
// A tombstone is kept for one reason, which [Map.integrate] states: it is what
// stops an older set resurrecting a key somebody has since deleted. So it may go
// once no replica can still send a write that would lose to it — which is when
// the deletion is one every replica has delivered, exactly as elsewhere. Pass a
// version every replica has; see [Doc.Collect] for what that means and who can
// know it.
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
// below exists. Given a version some replica has not delivered, that replica's
// write arrives naming a key whose tombstone is gone, finds no record to lose
// to, and the key comes back — silently, and on that replica only. So a map
// remembers the highest clock it collected under and refuses a write at or
// below it for a key it does not hold, with [ErrStranded]. Under correct use no
// such operation can arrive at all, so the guard never fires; when it does, it
// is naming the mistake.
//
// Collect reports how many tombstones it dropped.
func (m *Map) Collect(stable VersionVector) int {
	dropped := 0
	for key, rec := range m.records {
		if !rec.dead || !stable.Includes(rec.id) {
			continue
		}
		delete(m.records, key)
		if rec.clock > m.collectedBelow {
			m.collectedBelow = rec.clock
		}
		dropped++
	}
	return dropped
}

// CollectedBelow reports the highest clock this replica has collected a
// tombstone under, and zero if it has collected none.
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

// Collect drops what every part of this document can spare, given what every
// replica has delivered, and reports how many characters, elements and
// tombstones went.
//
// A part not named in stable is left alone. That is not a nicety: collecting a
// part against a version nobody vouched for is exactly the mistake the other
// [Doc.Collect] guards against, and the guard cannot see a part the caller
// forgot.
//
// # Why this exists here and a rewrite does not
//
// There is deliberately no rewrite for a [Composite] — see [Doc.Rewritten] —
// because rewriting mints new identities, and the structured layer keeps rich
// text marks, tree parents and sequence positions against the identities of the
// characters they describe. A rewrite would silently empty every one of those.
//
// Collection is the opposite: it keeps every identity it does not drop, and
// drops only what is already invisible and already agreed to be gone. A mark on
// a character that is collected was a mark on text nobody could see, and it
// stays exactly as inert as it was. So a composite may be collected where it
// may not be rewritten, and that difference is the whole reason this one is
// offered.
func (c *Composite) Collect(stable CompositeVersion) int {
	n := 0
	for name, d := range c.texts {
		if held, named := stable[Part{Kind: PartText, Name: name}]; named {
			n += d.Collect(held)
		}
	}
	for name, l := range c.lists {
		if held, named := stable[Part{Kind: PartList, Name: name}]; named {
			n += l.Collect(held)
		}
	}
	for name, m := range c.maps {
		if held, named := stable[Part{Kind: PartMap, Name: name}]; named {
			n += m.Collect(held)
		}
	}
	return n
}

// CanReplay reports whether [Composite.OpsSince] would hand back a complete
// history from v, for every part of this document.
//
// It is false when any text or list part has collected below what v holds; see
// [Doc.CanReplay]. A peer it is false for has to be sent a snapshot rather than
// a difference.
//
// A map part never makes it false. A map gives back a sequence number without
// its operation as a matter of course — a second write to a key overwrites the
// first — so the span that stands in for one collected tombstone is the same
// span that already stood in for an overwritten value, and a peer applying it
// catches up either way.
func (c *Composite) CanReplay(v CompositeVersion) bool {
	for name, d := range c.texts {
		if !d.CanReplay(v[Part{Kind: PartText, Name: name}]) {
			return false
		}
	}
	for name, l := range c.lists {
		if !l.CanReplay(v[Part{Kind: PartList, Name: name}]) {
			return false
		}
	}
	return true
}

// collected reports whether any part has given something back, which is what
// makes this document's bytes something a newcomer replaying its history cannot
// reproduce: what was collected is in the snapshot and in no operation.
func (c *Composite) collected() bool {
	for _, d := range c.texts {
		if len(d.floor) > 0 {
			return true
		}
	}
	for _, l := range c.lists {
		if len(l.floor) > 0 {
			return true
		}
	}
	for _, m := range c.maps {
		if m.collectedBelow > 0 {
			return true
		}
	}
	return false
}

package crdt

// Collect drops the tombstones nothing can name any more, on the same terms as
// [Doc.Collect] and for the same reasons — see that method for what the version
// handed in has to be, and what is lost by handing in the wrong one.
//
// A list is simpler than a text to collect, because it is simpler to begin with:
// every element carries its own origin, where a character's is implicit in the
// run it belongs to. There is no rule here that an element goes only together
// with its neighbours, so an element goes when its own deletion is one every
// replica has, and the survivors that named it are re-pointed at the nearest
// element still alive before it.
//
// A deletion that duplicates another's — two replicas removing the same element
// concurrently — is recorded against that element, and goes with it. It has to
// be stable as well, or a replica could still be about to send one, and would
// find nothing to record it against.
//
// Collect reports how many elements it dropped.
func (l *List) Collect(stable VersionVector) int {
	// The duplicate deletions are filed by the deletion, so the question "is
	// everything that removed this element stable" needs them the other way
	// round.
	dupsOf := map[ID][]ID{}
	for delID, target := range l.dupDeletes {
		dupsOf[target] = append(dupsOf[target], delID)
	}

	drop := map[ID]bool{}
	for _, e := range l.elements {
		if e.present() || !stable.Includes(e.delID) {
			continue
		}
		stableToo := true
		for _, delID := range dupsOf[e.id] {
			if !stable.Includes(delID) {
				stableToo = false
				break
			}
		}
		if stableToo {
			drop[e.id] = true
		}
	}
	if len(drop) == 0 {
		return 0
	}

	// One walk in order does both halves: it separates what stays from what
	// goes, and records for everything going the last element before it that is
	// staying, which is what a survivor naming it must be re-pointed at.
	instead := map[ID]ID{}
	lastKept := ID{}
	kept := make([]*element, 0, len(l.elements)-len(drop))
	for _, e := range l.elements {
		if drop[e.id] {
			instead[e.id] = lastKept
			l.collectedOne(e.id.Site)
			l.collectedOne(e.delID.Site)
			for _, delID := range dupsOf[e.id] {
				l.collectedOne(delID.Site)
				delete(l.dupDeletes, delID)
			}
			continue
		}
		kept = append(kept, e)
		lastKept = e.id
	}
	for _, e := range kept {
		if to, gone := instead[e.origin]; gone {
			e.origin = to
		}
	}

	l.elements = kept
	l.byID = make(map[ID]int, len(kept))
	for i, e := range kept {
		l.byID[e.id] = i
	}
	l.floor = l.floor.join(stable)
	return len(drop)
}

// Floor reports the version below which this replica can no longer say what the
// list held. It is empty for a list that has never collected.
func (l *List) Floor() VersionVector { return l.floor.Clone() }

// collectedOne records that one more of a site's operations is no longer here to
// be written down; see [Doc.Collect] for what a loader does with the tally.
func (l *List) collectedOne(site SiteID) {
	if l.collected == nil {
		l.collected = map[SiteID]uint64{}
	}
	l.collected[site]++
}

// readable reports whether v is at or above everything collection has taken
// away.
func (l *List) readable(v VersionVector) bool {
	for site, seq := range l.floor {
		if v[site] < seq {
			return false
		}
	}
	return true
}

// strandedBy reports whether an operation is waiting for an element this replica
// has seen and no longer holds.
func (l *List) strandedBy(op ListOp) bool {
	if len(l.floor) == 0 {
		return false
	}
	named := op.Origin
	if op.Kind != OpInsert {
		named = op.Target
	}
	if named.IsRoot() || !l.floor.Includes(named) {
		return false
	}
	return !l.known(named)
}

// CanReplay reports whether [List.OpsSince] would hand back a complete history
// from v.
//
// It is false when v is below [List.Floor]: collection dropped operations that v
// has not seen, so what OpsSince returns has holes in its sequence numbers, and
// a replica applying them parks everything after the first hole instead of
// catching up — silently, since nothing in the batch is wrong on its own. A peer
// below the floor has to be sent a snapshot rather than a difference.
//
// It is true for every version of a replica that has never collected, which is
// every replica until somebody asks.
func (l *List) CanReplay(v VersionVector) bool { return l.readable(v) }

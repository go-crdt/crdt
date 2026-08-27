package crdt

// Collecting the tombstones nothing can still name.
//
// A deletion hides a character and does not forget it, because a replica that
// forgot could not tell a character arriving late from one it had already seen.
// That is why a document's snapshot grows with every edit and never shrinks,
// and it is the cost [Doc.Collect] exists to bound.
//
// A tombstone may go when three things are true at once:
//
//  1. Every character of the run is deleted. Runs are collected whole, never in
//     part: a character's origin is the character before it in its own run, so
//     collecting a prefix would leave the survivor behind it naming something
//     that is gone.
//  2. Every one of those deletions is dominated by stable — a version every
//     replica has delivered. No replica still has the character visible, and an
//     insertion names as its origin a character that was visible to whoever
//     issued it, so no operation that could name this run can still be written.
//     Anything already written naming it is, by the same argument, already here.
//  3. Every survivor that named a character of it is re-pointed at the nearest
//     character still alive before it. Without this nothing is ever collected:
//     a run appended to a document names the last character of the run before
//     it, so an entirely deleted run is essentially always named by its
//     successor. Measured on a document written and revised the ordinary way,
//     332 runs of 667 were entirely deleted and stable, and every one of them
//     was named by a survivor.
//
// Re-pointing is what makes the result representable as well: an origin left
// pointing at a collected character is one [Doc.Load] must reject, since placing
// a character needs the character it follows.
//
// # What it costs
//
// The operations are gone, and with them the ability to say what the document
// said before they were applied. [Doc.TextAt] and its neighbours refuse below
// [Doc.Floor] rather than answer with characters missing — a wrong answer about
// the past being worse than none. The version vector is untouched: the replica
// still knows it has seen these operations, so a duplicate arriving late is
// still recognised and dropped rather than applied a second time.
//
// # Who may call it
//
// stable has to be a version every replica has delivered *and* whose operations
// this replica holds. A server that fans operations out and collects
// acknowledgements knows one; a replica on its own does not, and passing a
// version that some replica has not reached will strand that replica's work —
// its operations will name origins that are gone, and it will be told so by
// [ErrStranded] rather than left to park them for ever.
//
// Collect reports how many characters it dropped.
func (d *Doc) Collect(stable VersionVector) int {
	d.flush()

	// Candidates first, on the two conditions that look only at the run itself.
	candidate := map[*block]bool{}
	for b := d.head.next; b != nil; b = b.next {
		if collectible(b, stable) {
			candidate[b] = true
		}
	}
	if len(candidate) == 0 {
		return 0
	}

	dropped := d.dropRuns(candidate)
	d.floor = d.floor.join(stable)
	return dropped
}

// Floor reports the version below which this replica can no longer say what the
// document held: everything [Doc.Collect] has been given, joined. It is empty
// for a document that has never collected, which is every document until
// somebody asks.
func (d *Doc) Floor() VersionVector { return d.floor.Clone() }

// collectible reports whether every character of b is deleted and every one of
// those deletions is one stable has.
func collectible(b *block, stable VersionVector) bool {
	covered := 0
	for _, del := range b.dels {
		for i := del.from; i < del.to; i++ {
			if !stable.Includes(del.continuesTo(i)) {
				return false
			}
		}
		covered += int(del.to - del.from)
	}
	return covered == len(b.text)
}

// dropRuns unlinks the given runs and rebuilds what indexed them.
//
// The index is rebuilt rather than repaired. Blocks have never been removed
// from it — the tree only ever grows and rotates — and a deletion that has to
// carry subMin, subVis and subSup correctly through a rebalance is the kind of
// code whose mistakes are silent. Rebuilding uses the one path every document
// is already built by.
func (d *Doc) dropRuns(drop map[*block]bool) int {
	// One walk in document order does both halves of the work: it separates the
	// runs that stay from the ones that go, and it records, for every character
	// about to go, the last character before it that is staying. That is what a
	// survivor naming it must be re-pointed at.
	chars := 0
	kept := make([]*block, 0, 16)
	instead := map[ID]ID{}
	lastKept := ID{}
	for b := d.head.next; b != nil; b = b.next {
		if drop[b] {
			chars += len(b.text)
			for _, del := range b.dels {
				for i := del.from; i < del.to; i++ {
					d.collectedOne(del.continuesTo(i).Site)
				}
			}
			for i := range b.text {
				instead[b.idAt(i)] = lastKept
				d.collectedOne(b.idAt(i).Site)
			}
			continue
		}
		kept = append(kept, b)
		lastKept = b.idAt(len(b.text) - 1)
	}
	for _, b := range kept {
		if to, gone := instead[b.originID]; gone {
			b.originID = to
		}
	}

	// The sentinel is a tree node too, and startIndex only makes it the root:
	// left behind, its old children point into the runs just dropped, and the
	// first rebalance walks into a cycle.
	d.head.next = nil
	d.head.left, d.head.right, d.head.up = nil, nil, nil
	d.head.subVis, d.head.subSup = 0, 0
	d.bySite = map[SiteID][]*block{}
	d.startIndex()
	at := d.head
	var prev *block = d.head
	for _, b := range kept {
		b.left, b.right, b.up, b.subMin = nil, nil, nil, nil
		b.next, b.prev = nil, prev
		prev.next = b
		prev = b
		d.indexAdd(b)
		d.index(at, b)
		at = b
	}

	d.total -= chars
	d.mark, d.markPos, d.markIdx = nil, 0, 0
	d.dirty, d.dirtyVis, d.dirtySup = nil, 0, 0
	return chars
}

// collectedOne records that one more of a site's operations is no longer here to
// be written down, which is what keeps a snapshot's accounting exact: a loader
// counts what it reads and compares it against what the version vector
// promises, and these are the difference.
func (d *Doc) collectedOne(site SiteID) {
	if d.collected == nil {
		d.collected = map[SiteID]uint64{}
	}
	d.collected[site]++
}

// join returns the element-wise maximum of two versions, which is the version
// that has seen everything either of them has.
func (v VersionVector) join(other VersionVector) VersionVector {
	out := v.Clone()
	for site, seq := range other {
		if seq > out[site] {
			out[site] = seq
		}
	}
	return out
}

package crdt

// Discarding what a document no longer says.
//
// A deletion hides a character and does not forget it, because a replica that
// forgot could not tell a character arriving late from one it had already seen.
// The identity has to stay. What does not have to stay is the character itself:
// once it is deleted, nobody can read it, and the only thing still asking about
// it is the past.
//
// So a purged run keeps its identity, its length, its origin and the operations
// that deleted it, and drops the characters. Nothing is re-pointed and nothing
// moves: a survivor that named one of these characters still finds it, because
// it is still there — which is the whole difference between this and the
// collection that was withdrawn in v0.35.0 for leaving two replicas holding
// different documents.
//
// This is the shape Yjs uses. `Item.gc` replaces a deleted item in place with
// `GC(this.id, this.length)` and re-points nothing, and Yjs runs it
// unconditionally rather than waiting for every replica to have seen the
// deletion. That it converges under concurrency was checked against Yjs itself
// before this was written: two hundred random histories over five peers with gc
// on, and all of them agree.
//
// # What it costs
//
// The past. [Doc.TextAt] and [Doc.ChangesSince] cannot rebuild a state in which
// a purged character was still visible, so they refuse below
// [Doc.PurgedBelow] rather than answer with characters missing — a wrong answer
// about the past being worse than none.
//
// Nothing else. There is no version to supply and no replica to wait for: a
// purged run answers to its identity exactly as it did, so an operation naming
// one finds it, and a peer catching up is sent the run with its characters
// missing rather than not sent it at all.
//
// Purge reports how many characters it discarded.
func (d *Doc) Purge() int {
	d.flush()
	n := 0
	for b := d.head.next; b != nil; b = b.next {
		if b.gone || len(b.text) == 0 || !allDeleted(b) {
			continue
		}
		n += len(b.text)
		if last := b.clockAt(len(b.text) - 1); last > d.purgedBelow {
			d.purgedBelow = last
		}
		b.gone = true
		b.text = nil
		b.nsup = 0
	}
	return n
}

// PurgedBelow reports the highest clock this replica has discarded a character
// under, and zero for a document nobody has purged.
//
// It is what [Doc.TextAt] and [Doc.ChangesSince] refuse below. A clock rather
// than a version, because what they cannot rebuild is a moment, and a moment is
// what a clock names.
func (d *Doc) PurgedBelow() uint64 { return d.purgedBelow }

// readable reports whether v is late enough that nothing purged was still
// visible in it.
func (d *Doc) readable(v VersionVector) bool {
	if d.purgedBelow == 0 {
		return true
	}
	for b := d.head.next; b != nil; b = b.next {
		if !b.gone {
			continue
		}
		for i := range b.size() {
			// A purged character was visible at v if v had its insertion and
			// not the deletion that removed it.
			if v.Includes(b.idAt(i)) && !v.Includes(b.delIDAt(i)) {
				return false
			}
		}
	}
	return true
}

// allDeleted reports whether every character of b has been deleted.
func allDeleted(b *block) bool {
	covered := 0
	for _, del := range b.dels {
		covered += int(del.to - del.from)
	}
	return covered == len(b.text)
}

// purged reports whether any text part has discarded characters, which is what
// makes this document's bytes something a newcomer replaying its history cannot
// reproduce: what a purge took is in no operation, and the floor it took them
// under is in the snapshot and in no operation either.
//
// The same shape as [Composite.collected], and true for the same reason.
func (c *Composite) purged() bool {
	for _, d := range c.texts {
		if d.purgedBelow > 0 {
			return true
		}
	}
	return false
}

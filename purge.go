package crdt

import "errors"

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
// A peer that is behind it. A purged run is not in [Doc.OpsSince] at all —
// neither its insertions nor the deletions that explain them, because both are
// read from characters that are gone — so a peer missing any of them is sent a
// history with a hole in it and parks everything that followed. That is what
// [Doc.CanServe] is for: ask before serving, and send a [Doc.Snapshot] instead
// of operations to a version it refuses.
//
// The past, in the same breath and for the same reason. [Doc.TextAt],
// [Doc.LenAt] and [Doc.ChangesSince] read the characters, so a version in which
// a purged character was still visible reads back without it. None of the three
// can refuse — they return no error, and giving them one would break every
// caller — so the refusal is the caller's to make, against the same question:
// a version [Doc.CanServe] accepts is one whose text this replica can still
// rebuild, because accepting it means every purged character was already
// deleted at that version.
//
// Nothing else. A purged run answers to its identity exactly as it did, so an
// operation naming one still finds it, and an ordinary edit from a peer that
// never purged applies unchanged.
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
// It is a clock rather than a version because what a purge takes is a moment,
// and a moment is what a clock names. It says that this replica has given
// something up; it does not say which peers that costs, which is a question
// about a version vector and is [Doc.CanServe]'s to answer.
func (d *Doc) PurgedBelow() uint64 { return d.purgedBelow }

// ErrPurged reports a version this replica can no longer answer for, because a
// purge discarded characters that answering it would need.
//
// Beside [ErrStranded], and for the reason written there: an operation that can
// never be applied is returned rather than left waiting, because parking it is
// the silent version of the same failure. This is the same doctrine one step
// earlier — refusing to send what would park at the far end — and it is the
// shape the field settled on independently. Loro names two of these:
//
//	ImportUpdatesThatDependsOnOutdatedVersion
//	SwitchToVersionBeforeShallowRoot
//
// A caller that gets this sends a [Doc.Snapshot] instead of operations. That is
// not a fallback but the whole design: a snapshot carries a purged document
// exactly, which is what [Doc.Purge] keeping every identity buys.
var ErrPurged = errors.New("crdt: a purge discarded characters this version still needs")

// CanServe reports whether this replica can still answer for a peer at v,
// returning nil when it can and [ErrPurged] when a purge has taken what
// answering would need. Ask it before [Doc.OpsSince], and send a
// [Doc.Snapshot] instead when it refuses.
//
// A document that has purged nothing accepts every version, including one that
// has seen nothing at all.
//
// It answers for reading the past as well as for serving a peer, and the two
// are one question rather than two that happen to agree: it accepts v only when
// every purged character was both written and deleted as of v, and a character
// deleted as of v is one no reading of v would have shown.
//
// # It is not enough for v to be past the deletions
//
// The obvious condition — nothing purged was still visible at v — is the one
// for reading and not the one for serving, and it accepts the worst peer there
// is. A version that never saw a purged run at all was never shown those
// characters, so it passes; and it is precisely the version that must be
// refused, because everything the purge took is what it is owed. Measured on a
// forty-edit document, purged: the weaker condition accepted the empty version
// vector, and the 798 operations sent to a peer at it all parked.
func (d *Doc) CanServe(v VersionVector) error {
	if d.purgedBelow == 0 {
		return nil
	}
	for b := d.head.next; b != nil; b = b.next {
		if !b.gone {
			continue
		}
		for i := range b.size() {
			// Both halves: the insertion, which the purge took out of
			// [Doc.OpsSince] along with the characters, and the deletion, which
			// went with it and without which the peer would show a character
			// this replica can no longer produce.
			if !v.Includes(b.idAt(i)) || !v.Includes(b.delIDAt(i)) {
				return ErrPurged
			}
		}
	}
	return nil
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

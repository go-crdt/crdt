package crdt

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
)

// What two replicas compare when a version vector cannot tell them apart.
//
// A version vector summarises how many operations each site made, and two
// replicas whose vectors match conclude that they are finished with each other.
// That conclusion is wrong whenever one site has put its name on two different
// operations — equivocation — and no amount of signing changes it: a signature
// says who produced a batch, not that a name is unique. Kleppmann sets it out in
// "Making CRDTs Byzantine Fault Tolerant" (PaPoC '22, §2.3.1), and collab
// measures it happening across a federation link, where both replicas report the
// same vector and hold different text and neither ever asks for anything again.
//
// A digest is the thing they can compare that does not rely on names being
// honest. Two replicas that hold the same document produce the same digest; two
// that do not, do not.
//
// # Of state, not of history
//
// This document keeps no operations. [Doc.OpsSince] SYNTHESISES them by walking
// blocks and reading characters, so there is no history here to hash-link the way
// Automerge hash-links its stored changes. What there is, is the document — and
// hashing that turns out to be the right answer rather than a consolation:
//
//   - It survives [Doc.Purge]. A purge discards only runs whose every character
//     is already deleted, so what a reader sees does not move, and neither does
//     this. Two replicas that purged differently still agree. A digest over
//     history could not: the operations are gone.
//   - It survives [Map.Collect], which drops only records that are already dead.
//   - It catches the forgery that matters. A digest over IDENTITIES alone would
//     compare equal on exactly the attack it exists for, because a forged
//     operation wears an identity the replica already holds. So what is hashed is
//     what a reader sees, together with the identity carrying it.
//
// # Why it is a walk in document order, and not a tree
//
// The obvious construction summarises subtrees, the way [tree.go] already
// summarises visible counts. It cannot be used: two replicas holding the same
// document need not hold the same AVL, so their subtree summaries would differ
// where the documents agree. The escape is either a combiner that ignores
// order — an incremental multiset hash — or a walk in the order the LIST gives,
// which is document order and is the same on every replica that has converged.
//
// The walk is right, and it is also nearly five times cheaper. Measured against
// a combiner of per-character hashes: 948 µs against 4.56 ms over a hundred
// thousand characters, because one hash over a stream costs far less than a
// hundred thousand hashes and an addition each. It needs no invented
// construction, and it is about twice what [Doc.Snapshot] costs over the same
// blocks — which is what a fresh join already pays. See
// TestWhatADigestWalkCostsAgainstASnapshot for the table and for the two
// corrections that produced it.
//
// That price is affordable at the rate replicas need to compare and at no other:
// collab measures one join per participant per session against one
// acknowledgement per participant per KEYSTROKE. So this is for the moment a
// replica concludes it is caught up, and not for every message.
//
// # What it does not do
//
// It does not attribute, prevent or repair. It says two replicas differ, which is
// the half of the failure that otherwise has no remedy at all, since a replica
// that believes it is finished never asks again.

// A Digest fingerprints what a replica holds: equal digests for replicas holding
// the same document, different ones otherwise.
//
// It is not a version and cannot be ordered or subtracted. Two replicas learn
// from it that they differ, not which of them is behind — that is what the
// version vector is for, and the two are exchanged together.
type Digest [32]byte

// String renders a digest as hex, which is how an operator sees one: a mismatch
// is worth logging, and a 32-byte array is not worth reading as decimal.
func (d Digest) String() string { return hex.EncodeToString(d[:]) }

// Domain separation, so that a text part and a list part holding the same bytes
// cannot produce the same digest, and so that a part's digest cannot be mistaken
// for a whole composite's.
const (
	digestText      byte = 1
	digestList      byte = 2
	digestMap       byte = 3
	digestComposite byte = 4
)

// writeUint64 and writeBytes frame what goes into a digest. Lengths are written
// before the bytes they measure, because two adjacent values a peer chooses must
// not be able to encode the same way as one value and a different neighbour.
func writeUint64(h hash.Hash, v uint64) {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	_, _ = h.Write(buf[:])
}

func writeBytes(h hash.Hash, b []byte) {
	writeUint64(h, uint64(len(b)))
	_, _ = h.Write(b)
}

func writeID(h hash.Hash, id ID) {
	writeUint64(h, uint64(id.Site))
	writeUint64(h, id.Seq)
}

// Digest fingerprints the visible text and the identity of every character in
// it, in document order.
//
// No flush first, deliberately: [Doc.flush] settles the index summaries in
// tree.go, and this reads the blocks themselves, which are never behind.
func (d *Doc) Digest() Digest {
	h := sha256.New()
	_, _ = h.Write([]byte{digestText})
	// One write of the three fields together rather than three writes of eight
	// bytes, which is the whole of the hot loop here. The bytes are the same
	// either way -- site, then sequence, then the character, little-endian --
	// and the difference is measured: 2.62 ms against 945 µs over a hundred
	// thousand characters, nearly three times, all of it call overhead. The
	// helpers below are kept for the other two kinds, where a value's length
	// has to be framed anyway and the loop is not the cost.
	var buf [24]byte
	for b := d.head.next; b != nil; b = b.next {
		if b.gone {
			// Purged: every character of it was already deleted, so none of it
			// was visible, and there is nothing here a reader could see.
			continue
		}
		cursor := delCursor{b: b}
		for i, r := range b.text {
			if !cursor.at(i).IsRoot() {
				continue
			}
			id := b.idAt(i)
			binary.LittleEndian.PutUint64(buf[0:], uint64(id.Site))
			binary.LittleEndian.PutUint64(buf[8:], id.Seq)
			binary.LittleEndian.PutUint64(buf[16:], uint64(r))
			_, _ = h.Write(buf[:])
		}
	}
	var out Digest
	h.Sum(out[:0])
	return out
}

// Digest fingerprints the values present in the list, in list order, each with
// the identity that carries it.
func (l *List) Digest() Digest {
	h := sha256.New()
	_, _ = h.Write([]byte{digestList})
	for _, e := range l.elements {
		if !e.present() {
			continue
		}
		writeID(h, e.id)
		writeBytes(h, e.value)
	}
	var out Digest
	h.Sum(out[:0])
	return out
}

// Digest fingerprints the live keys of the map and the value each holds, in key
// order, with the identity of the write that put it there.
//
// Key order rather than the map's own, which has none: a Go map is deliberately
// unordered, so a digest that walked it would differ between two replicas
// holding the same keys, and between two runs of the same one.
func (m *Map) Digest() Digest {
	h := sha256.New()
	_, _ = h.Write([]byte{digestMap})
	for _, key := range m.Keys() {
		rec := m.records[key]
		writeBytes(h, []byte(key))
		writeID(h, rec.id)
		writeBytes(h, rec.value)
	}
	var out Digest
	h.Sum(out[:0])
	return out
}

// Digest fingerprints every part of the composite, in the order [Parts] gives,
// which is by kind and then by name and is the same on every replica that has
// applied the same operations.
//
// A part a caller reached for and left empty is not in it, for the same reason
// it is not in [Composite.Parts]: one replica having touched a name is not a
// difference in the document.
func (c *Composite) Digest() Digest {
	h := sha256.New()
	_, _ = h.Write([]byte{digestComposite})
	for _, part := range c.Parts() {
		_, _ = h.Write([]byte{byte(part.Kind)})
		writeBytes(h, []byte(part.Name))
		var sub Digest
		switch part.Kind {
		case PartText:
			sub = c.texts[part.Name].Digest()
		case PartList:
			sub = c.lists[part.Name].Digest()
		default:
			sub = c.maps[part.Name].Digest()
		}
		_, _ = h.Write(sub[:])
	}
	var out Digest
	h.Sum(out[:0])
	return out
}

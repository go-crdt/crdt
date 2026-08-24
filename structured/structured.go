// Package structured turns the replicated primitives of the crdt package into a
// shared substrate for co-editing structured documents. One core expresses
// both a spreadsheet and an isometric diagram — and any other structured
// document — rather than each growing a merge engine of its own.
//
// # One core, many document types
//
// Nothing here re-implements a CRDT. The merge logic lives entirely in the crdt
// package, and this layer only composes three of its structures:
//
//   - [crdt.List] is an RGA: an ordered sequence whose every element carries a
//     stable identity that survives concurrent insertion and deletion elsewhere.
//     With its order ignored it is a reload-safe source of stable identities and
//     an existence set, which is what a diagram's nodes and connectors are. It
//     is not what an axis of a spreadsheet is, because an RGA has no operation
//     for moving something already in it; see [Sequence].
//   - [crdt.Map] is a last-writer-wins key-value map. Each key is, on its own, an
//     LWW-register: a single value merged by a (Lamport clock, site) total order
//     with no tie left to chance and no wall clock read. [Register] names that
//     degenerate case, and [RecordMap] composes many such registers into records
//     whose fields merge independently.
//   - [crdt.Composite] holds those parts under one name, one snapshot and one
//     version, so a whole document is one thing to persist and to authorise.
//
// Two things a map and a list do not express are built here rather than added
// to the crdt package, because neither needs a new merge rule — only a way of
// using the ones that exist:
//
//   - [Counter] is a number several replicas add to at once. A register cannot
//     be one, because "add one" is not a value and writing a value cannot say
//     it. Keying the map by site, so that a replica writes only its own total,
//     makes concurrent additions concurrent writes to different keys.
//   - [Tree] is a tree whose nodes move. A parent is a single value and merges
//     on its own; what does not is the shape two legal moves make between them,
//     which is a ring. Tree resolves that when the tree is read, by rules that
//     are a function of the state alone.
//   - [RichText] is text that carries formatting. Written into the sequence —
//     a bold-on character, a bold-off character — two replicas bolding
//     overlapping stretches produce interleaved markers and read differently;
//     written per character it converges and costs a write per letter. A mark
//     is one operation naming two boundaries instead, and the formatting is
//     worked out when the text is read.
//   - [Undo] puts back what this replica did, and only that. It is not a stack
//     of states — restoring one would throw away what everybody else has done
//     since, and travel to them as an instruction to do the same. An undo here
//     is a new edit, made now, that has the effect of the old one not having
//     happened, and it reaches a peer as an ordinary edit.
//   - [Ink] is what is drawn by hand. A stroke is a path that arrives a point at
//     a time, so held as one value every point sent rewrites the whole path and
//     the person watching sees the line redrawn rather than extended. The points
//     are a sequence of their own here, appended to, each saying which stroke it
//     belongs to — one stream rather than one per stroke, because a part cannot
//     be taken out of a composite and a whiteboard would otherwise spend more on
//     saying what it has than on what was drawn.
//   - [Blobs] holds the files a document refers to but is not made of. A file
//     as one map value is one operation the size of the file: it cannot be sent
//     as it is read, resumed if the connection drops, or recognised as one a
//     peer already has. Cut into chunks stored under the hash of their own
//     bytes, it is as many operations as it has chunks, and the same chunk
//     written by two replicas is the same key and the same value — so there is
//     nothing to merge and nothing is stored twice.
//   - [Proposals] are changes to a document that are not part of it yet: a
//     suggested edit, a change put up for review. Held as a second copy of the
//     document and reconciled later, accepting one means applying a difference
//     between two texts, and applying a difference mints new characters — so
//     every comment, mark and cursor anchored to the characters it replaced
//     would be left pointing at nothing. A proposal is a replica that has not
//     synced instead: its operations are the document's own, against the
//     document's own identities, written down where reviewers can read them.
//     Accepting is applying them, which merges with whatever happened
//     meanwhile because that is what a replica coming back from offline does;
//     turning one down costs nothing, because they were never applied.
//   - [Set] is a collection of names replicas add to and take from at once —
//     the labels on a card, the people in a conversation, the layers that are
//     showing. A map keyed by the name converges on the case that happens
//     badly: one replica adds a label while another, which has never seen it,
//     takes it away, and one of the two writes wins by an order that has
//     nothing to do with what either knew. Every addition mints a tag of its
//     own instead, and a removal takes away the tags it can see — so an
//     addition nobody had seen is untouched by it, not as a policy but for
//     want of anything to base one on.
//   - [MultiRegister] is a value two replicas are allowed to disagree about.
//     A [Register] settles every concurrent write by the (clock, site) order,
//     which is right when the losing write is of no interest and wrong when it
//     is: two people rename the same thing at once and one of the names is gone,
//     with nothing anywhere recording that there was a second. This keeps a
//     version vector beside each replica's own value, so a value written without
//     seeing another is not superseded by it and both are read. Choosing between
//     them is writing the one chosen — a write dominates everything its writer
//     could see — so there is no operation for settling and none is needed.
//   - [Blocks] is a document made of blocks — paragraphs, headings, list items,
//     quotes, code — each holding formatted text and nested to a depth. Written
//     as a rich text per block it converges and does not scale: a part cannot be
//     taken out of a composite and a version carries one entry per part, so a
//     thousand blocks is a thousand entries exchanged on every sync whether the
//     document still holds them or not. The blocks are markers in one text
//     instead, keyed to one map, which is three parts however many blocks there
//     are. The marker is a character rather than a boundary between two, because
//     "the end of this paragraph" and "the start of the next" are the same
//     offset and are not the same place — a character has two sides and a
//     boundary has one.
//   - [Sequence] is an ordered collection whose items move. [crdt.List] is an
//     RGA and has no operation for moving something already in it; written as a
//     delete and an insert, a move is two operations that a concurrent move
//     splits, leaving the item in both places or in neither. An item carries
//     where it sits as a rank instead, so a move is one field write. It is what
//     the rows and columns of a [Sheet] are, which is what makes a row something
//     a person can drag.
//
// [Sheet] and [Diagram] are thin wrappers over the same [crdt.Composite]. That
// they share it is the point: the convergence, commutativity, idempotence and
// associativity the crdt package proves for its parts are inherited by every
// document type built here, because there is only ever one set of parts doing
// the merging.
//
// # Determinism
//
// Like the crdt package, this one never reads the wall clock and never draws a
// random number, so a document compiled to js/wasm behaves exactly as it does
// on a server. Replica identity is the caller's [crdt.SiteID]; stable element
// identities are minted by the RGA, which is why they are reload-safe and never
// reused, even across a deletion.
//
// # What is replicated and what is not
//
// The raw content of a cell — a literal or a formula's source text together with
// the stable identities it references — is replicated. A formula's computed
// value is not: it is derived locally from content every replica already agrees
// on, so replicating it would be replicating a function of the state rather than
// the state. This package is the data layer; it holds no formula engine and no
// widget.
package structured

import (
	"strconv"
	"strings"

	"github.com/go-crdt/crdt"
)

// encodeID renders a stable identity as a UTF-8 token "site.seq", so it can be
// part of a [crdt.Map] key, which must be valid UTF-8. Both fields are decimal,
// so the single '.' is unambiguous and the encoding is injective.
func encodeID(id crdt.ID) string {
	return strconv.FormatUint(uint64(id.Site), 10) + "." + strconv.FormatUint(id.Seq, 10)
}

// decodeID reverses [encodeID]. It reports failure rather than trusting its
// input, because a selection published by a peer reaches it; see the awareness
// wiring.
func decodeID(s string) (crdt.ID, bool) {
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		return crdt.ID{}, false
	}
	site, err1 := strconv.ParseUint(s[:dot], 10, 64)
	seq, err2 := strconv.ParseUint(s[dot+1:], 10, 64)
	if err1 != nil || err2 != nil {
		return crdt.ID{}, false
	}
	return crdt.ID{Site: crdt.SiteID(site), Seq: seq}, true
}

// decodeThing decodes a record key into the identity of a thing.
//
// It differs from [decodeID] in one way, and the difference is the whole point:
// it refuses the root identity. Every type here uses that identity as a
// sentinel meaning a PLACE rather than a thing — the top of a [Tree], the front
// of a [Sequence] — and a record named after it is not something any replica of
// this package writes.
//
// A peer can write one by hand, and one did: a record keyed "0.0" made a tree
// whose root was its own child, and reading it recursed until the process died
// of a stack overflow — which is not a panic and cannot be recovered from. The
// root is not a thing, so a record named after it is not one either.
//
// [decodeID] keeps its meaning, because the root is a perfectly good field
// VALUE: a node at the top of a tree stores the root as its parent.
func decodeThing(key string) (crdt.ID, bool) {
	id, ok := decodeID(key)
	if !ok || id.IsRoot() {
		return crdt.ID{}, false
	}
	return id, true
}

// cellKey is the [crdt.Map] key a cell is stored under: the row and column
// identities together, which is what keeps a cell's address stable when a
// concurrent edit inserts or removes some other row or column. Because neither
// identity contains a ':', the join is unambiguous and injective.
func cellKey(row RowID, col ColID) string {
	return encodeID(crdt.ID(row)) + ":" + encodeID(crdt.ID(col))
}

// decodeCellKey reverses [cellKey]. Like [decodeID] it refuses malformed input,
// because a cell coordinate travels in an awareness selection.
func decodeCellKey(s string) (RowID, ColID, bool) {
	colon := strings.IndexByte(s, ':')
	if colon < 0 {
		return RowID{}, ColID{}, false
	}
	row, ok1 := decodeID(s[:colon])
	col, ok2 := decodeID(s[colon+1:])
	if !ok1 || !ok2 {
		return RowID{}, ColID{}, false
	}
	return RowID(row), ColID(col), true
}

// fieldKey is the [crdt.Map] key one field of one record is stored under. The
// record identity is length-prefixed so that the split back into record and
// field is exact whatever either contains — a record identity holds a '.', and a
// field name is arbitrary, so neither a separator search nor a fixed width would
// do. The length is decimal and the identity valid UTF-8, so the key is too.
func fieldKey(rec, field string) string {
	return strconv.Itoa(len(rec)) + "." + rec + field
}

// splitFieldKey reverses [fieldKey], reporting failure for a key that was not
// written by it — which a peer may inject, since a [crdt.Map] holds whatever key
// an applied operation names.
func splitFieldKey(key string) (rec, field string, ok bool) {
	dot := strings.IndexByte(key, '.')
	if dot <= 0 {
		return "", "", false
	}
	n, err := strconv.Atoi(key[:dot])
	if err != nil || n < 0 {
		return "", "", false
	}
	rest := key[dot+1:]
	if n > len(rest) {
		return "", "", false
	}
	return rest[:n], rest[n:], true
}

// mintKey is the one key an identity is drawn from. Its value is never read;
// what matters is the identity of the operation that wrote it, which is unique
// to the replica and to the write, and stable across a reload.
const mintKey = "\x00mint"

// mintID allocates an identity for a new thing, and returns the operation that
// did it — which has to reach every peer, or the identity is one this replica
// could mint again after a reload.
func mintID(m *crdt.Map) (crdt.ID, crdt.MapOp, error) {
	op, err := m.Set(mintKey, []byte{1})
	if err != nil {
		return crdt.ID{}, crdt.MapOp{}, err
	}
	return op.ID, op, nil
}

// idLess is the total order on identities. It settles two things a concurrent
// edit gave the same rank, which is what makes reading them an order at all.
func idLess(a, b crdt.ID) bool {
	if a.Site != b.Site {
		return a.Site < b.Site
	}
	return a.Seq < b.Seq
}

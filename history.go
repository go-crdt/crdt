package crdt

import "strings"

// Reading a document as it stood, and what has happened to it since.
//
// # Why this costs nothing to keep
//
// A document that merges without a server has to remember more than what it
// currently says. Every character carries the identity of the operation that
// made it, and every deletion carries the identity of the operation that made
// *it* — because that is what lets two replicas that have seen different halves
// of the story agree about the whole of it.
//
// So the history is already here. A version vector says which operations a
// replica had seen; a character was there if its own operation was among them,
// and it was visible if the operation that removed it was not. That is the whole
// of it: no log is kept, nothing is stored twice, and asking costs one walk.
//
// # What it cannot do
//
// This is a property of a sequence, not of the package. [Doc] and [List] keep
// every character and every element they ever held, tombstones included, so both
// can be read at any version a replica has reached. [Map] keeps one record per
// key: a second write to a key overwrites the first, and the first is gone. A
// map — and so anything built on one — can say when its current value was
// written, and cannot say what the value was before that.

// TextAt returns the text as it stood at version v: every character whose
// insertion v had seen, less every character whose deletion v had seen.
//
// A version this replica has not reached is not refused. Operations it has not
// seen simply are not in the document to be counted, so the answer is the text
// as of everything the two have in common — which is what a replica can honestly
// say about a version it does not hold.
func (d *Doc) TextAt(v VersionVector) string {
	var b strings.Builder
	for blk := d.head.next; blk != nil; blk = blk.next {
		for i, ch := range blk.text {
			if !v.Includes(blk.idAt(i)) {
				continue // not written yet, as of v
			}
			if del := blk.delIDAt(i); !del.IsRoot() && v.Includes(del) {
				continue // already taken out, as of v
			}
			b.WriteRune(ch)
		}
	}
	return b.String()
}

// LenAt returns how many characters the text held at version v, without
// building it.
func (d *Doc) LenAt(v VersionVector) int {
	n := 0
	for blk := d.head.next; blk != nil; blk = blk.next {
		for i := range blk.text {
			if !v.Includes(blk.idAt(i)) {
				continue
			}
			if del := blk.delIDAt(i); !del.IsRoot() && v.Includes(del) {
				continue
			}
			n++
		}
	}
	return n
}

// ChangesSince returns the edits that turn the text as it stood at v into the
// text as it stands now, in order, with offsets into the text being edited as
// each is applied — the same shape [Doc.ApplyChanges] reports, so a caller that
// can replay one can replay the other.
//
// It is not a diff. Two texts can be turned into one another in many ways and a
// diff picks one; this reports what actually happened, because every character
// says whether it arrived since v and every deletion says whether it did.
// Text that was written and then removed, both since v, is in neither the old
// text nor the new one and is reported in neither.
func (d *Doc) ChangesSince(v VersionVector) []Change {
	var out []Change
	pos := 0         // where we are in the text as it stood at v
	var added []rune // characters arrived since, not yet reported
	removed := 0     // characters gone since, not yet reported
	flush := func() {
		if len(added) == 0 && removed == 0 {
			return
		}
		out = append(out, Change{Pos: pos, Removed: removed, Text: string(added)})
		pos += len(added)
		added, removed = nil, 0
	}
	for blk := d.head.next; blk != nil; blk = blk.next {
		for i, ch := range blk.text {
			was := v.Includes(blk.idAt(i))
			del := blk.delIDAt(i)
			goneThen := was && !del.IsRoot() && v.Includes(del)
			goneNow := !del.IsRoot()
			switch {
			case !was && goneNow:
				// Written and removed, both since v: it was never in either
				// text, so it is in neither report.
			case !was:
				added = append(added, ch) // arrived since
			case goneThen:
				// Already gone at v, and still gone: in neither text.
			case goneNow:
				removed++ // was there at v, taken out since
			default:
				// In both texts: whatever was gathered ends here.
				flush()
				pos++
			}
		}
	}
	flush()
	return out
}

// ValuesAt returns the elements the list held at version v, in order: every
// element whose insertion v had seen, less every element whose deletion v had
// seen. It reads the list the way [Doc.TextAt] reads the text, and for the same
// reason — an element carries the identity of what made it and of what removed
// it, so the list is its own history.
func (l *List) ValuesAt(v VersionVector) [][]byte {
	out := [][]byte{}
	for _, e := range l.elements {
		if !v.Includes(e.id) {
			continue
		}
		if !e.delID.IsRoot() && v.Includes(e.delID) {
			continue
		}
		out = append(out, append([]byte(nil), e.value...))
	}
	return out
}

// LenAt returns how many elements the list held at version v, without building
// them.
func (l *List) LenAt(v VersionVector) int {
	n := 0
	for _, e := range l.elements {
		if !v.Includes(e.id) {
			continue
		}
		if !e.delID.IsRoot() && v.Includes(e.delID) {
			continue
		}
		n++
	}
	return n
}

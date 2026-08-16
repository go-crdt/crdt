package crdt

// An editor needs three things from a document that a plain "give me the text"
// does not provide: somewhere stable to hang a comment, a way back from that
// place to a position on screen, and who wrote what.
//
// All three are already in the document. Every character carries the identity of
// the operation that created it, that identity never changes, and the site is
// part of it. What was missing was a way to ask.

// Anchor returns the identity of the character at visible offset pos.
//
// The identity does not move. Insertions and deletions elsewhere change which
// offset the character sits at, and never change what it is, so an anchor is
// what a comment, a mark or a selection should be stored as — an offset stored
// instead would point somewhere else the moment anyone edits above it.
//
// pos may equal [Doc.Len], which anchors to the end of the document and returns
// the zero ID: the position after every character there is, and the one thing
// insertions at the end do not move.
func (d *Doc) Anchor(pos int) (ID, error) {
	if pos < 0 || pos > d.visible {
		return ID{}, ErrOutOfRange
	}
	if pos == d.visible {
		return ID{}, nil
	}
	b, i := d.visibleAt(pos)
	return b.idAt(i), nil
}

// Position returns where the character an anchor names sits now.
//
// A deleted character still has a place — the offset it would occupy, which is
// where the text around it closed up — and that is returned too, because a
// comment on a deleted sentence belongs where the sentence was rather than
// nowhere. Use [Doc.Visible] to tell the two apart.
//
// The zero ID anchors to the end of the document. ok is false only for an
// identity this document has never seen, which means the anchor came from
// somewhere else or the operations that would explain it have not arrived.
func (d *Doc) Position(anchor ID) (pos int, ok bool) {
	if anchor.IsRoot() {
		return d.visible, true
	}
	b, i, known := d.lookupChar(anchor)
	if !known {
		return 0, false
	}
	return d.visiblePos(b, i), true
}

// Visible reports whether the character an anchor names is still in the text.
func (d *Doc) Visible(anchor ID) bool {
	if anchor.IsRoot() {
		return true
	}
	b, i, known := d.lookupChar(anchor)
	return known && b.aliveAt(i)
}

// visiblePos returns the visible offset of character i of b — the offset of the
// character itself when it is visible, and the offset the text closed up to when
// it is not.
//
// It climbs the index rather than walking the document: the visible characters
// before a block are those in the subtrees hanging to its left on the way to the
// root, which is the same descent [Doc.seek] makes, read upwards.
func (d *Doc) visiblePos(b *block, i int) int {
	d.flush()
	n := b.visibleUpto(i)
	if b.aliveAt(i) {
		n-- // the character itself is at the offset, not after it
	}
	n += int(subVisOf(b.left))
	for child, p := b, b.up; p != nil; child, p = p, p.up {
		if p.right == child {
			n += int(p.subVis - child.subVis)
		}
	}
	return n
}

// An AuthorRun is a stretch of the visible text one replica wrote.
type AuthorRun struct {
	// Pos is the visible offset the stretch starts at.
	Pos int
	// Len is how many characters it covers.
	Len int
	// Site is the replica that wrote them.
	Site SiteID
}

// Author returns the replica that wrote the character at visible offset pos.
func (d *Doc) Author(pos int) (SiteID, error) {
	if pos < 0 || pos >= d.visible {
		return 0, ErrOutOfRange
	}
	b, _ := d.visibleAt(pos)
	return b.id.Site, nil
}

// AuthorRuns splits the visible text into stretches by who wrote them, in order.
// It is what colouring a document by author needs, and it costs one pass rather
// than one lookup per character.
//
// Adjacent stretches by the same replica are joined, so the result depends on
// the text rather than on how the document happens to be stored: two replicas
// holding the same document return the same runs.
func (d *Doc) AuthorRuns() []AuthorRun {
	var out []AuthorRun
	pos := 0
	for b := d.head.next; b != nil; b = b.next {
		visible := b.visibleFrom(0)
		if visible == 0 {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Site == b.id.Site {
			out[n-1].Len += visible
		} else {
			out = append(out, AuthorRun{Pos: pos, Len: visible, Site: b.id.Site})
		}
		pos += visible
	}
	return out
}

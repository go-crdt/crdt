package structured

import (
	"github.com/go-crdt/crdt"
)

// An Undo puts back what this replica did, and only what this replica did.
//
// # Why a stack of edits is not enough
//
// Undo in a document one person is editing is a stack of states: keep the old
// one, put it back. That is wrong here, and quietly so. Between making an edit
// and undoing it, other people have been editing too — their work is in the
// document, and restoring a remembered state would throw it away. Worse, an
// undo that replaced the document would travel to them as "the document is now
// this", and take their work away on their screens as well.
//
// So an undo is not a restoration. It is a new edit, made now, that has the
// effect of the old one not having happened — and it travels as an ordinary
// edit, which is why a peer needs no code to receive one.
//
// # What that means for what it can promise
//
//   - Undoing an insertion removes those characters, wherever they have since
//     moved to, and however much has been typed around them. They are named by
//     identity, not by where they were.
//   - Undoing a removal puts the text back after the character it followed. If
//     somebody has typed at that spot in the meantime, both replicas agree
//     about the order, because the sequence decides it and not this.
//   - Undoing something a peer has already undone by other means does nothing,
//     rather than doing it twice.
//
// What it does not promise is that the document returns to a state it was in.
// It cannot, because that state was never the shared one.
//
// # What it records
//
// Edits go through this rather than through the document, because inverting one
// afterwards is not possible from what the document keeps: a removal has to
// know the text that went, and a document that has removed it no longer says.
// [Undo.Insert] and [Undo.Delete] take that note as they go.
//
// An Undo is not safe for concurrent use.
type Undo struct {
	doc  *crdt.Doc
	done []step // what Undo would put back, oldest first
	redo []step // what Redo would do again, oldest first
	open *step  // the group being gathered by Begin, if any
}

// A step is one thing Undo puts back: everything gathered between a Begin and
// its Commit, or a single edit when there was no Begin.
type step struct {
	edits []edit
}

// An edit is one insertion or one removal, with what it takes to invert it.
type edit struct {
	// ids are the characters an insertion made. Empty for a removal.
	ids []crdt.ID
	// text is what a removal took out. Empty for an insertion.
	text string
	// after is the character the removed text followed, so it can go back where
	// it was rather than where that offset now is. The zero ID means the start
	// of the document — which cannot move.
	after crdt.ID
}

// NewUndo watches a document. Only the edits made through it are undoable,
// which is what makes an undo this replica's own and not everybody's.
func NewUndo(doc *crdt.Doc) *Undo { return &Undo{doc: doc} }

// Doc returns the document being edited.
func (u *Undo) Doc() *crdt.Doc { return u.doc }

// Insert puts text in at a visible offset and returns the operations to
// broadcast, as [crdt.Doc.Insert] does.
func (u *Undo) Insert(pos int, text string) ([]crdt.Op, error) {
	ops, err := u.doc.Insert(pos, text)
	if err != nil {
		return nil, err
	}
	ids := make([]crdt.ID, 0, len(ops))
	for _, op := range ops {
		ids = append(ids, op.ID)
	}
	u.record(edit{ids: ids})
	return ops, nil
}

// Delete removes count characters from a visible offset and returns the
// operations to broadcast.
func (u *Undo) Delete(pos, count int) ([]crdt.Op, error) {
	// What is about to go, and what it follows — read before the removal,
	// because afterwards the document no longer says either.
	gone, after, err := u.textAt(pos, count)
	if err != nil {
		return nil, err
	}
	ops, err := u.doc.Delete(pos, count)
	if err != nil {
		return nil, err
	}
	u.record(edit{text: gone, after: after})
	return ops, nil
}

// textAt returns the count characters at pos and the identity of the character
// before them, which is where they go back to.
func (u *Undo) textAt(pos, count int) (string, crdt.ID, error) {
	if pos < 0 || count < 0 || pos+count > u.doc.Len() {
		return "", crdt.ID{}, crdt.ErrOutOfRange
	}
	runes := []rune(u.doc.String())
	var after crdt.ID
	if pos > 0 {
		// In range: the bounds above are the ones Anchor checks.
		after, _ = u.doc.Anchor(pos - 1)
	}
	return string(runes[pos : pos+count]), after, nil
}

// record files an edit under the open group, or as a step of its own, and drops
// anything Redo was holding: a new edit is a new future.
func (u *Undo) record(e edit) {
	if u.open != nil {
		u.open.edits = append(u.open.edits, e)
	} else {
		u.done = append(u.done, step{edits: []edit{e}})
	}
	u.redo = nil
}

// Begin starts a group: everything until [Undo.Commit] is put back by one Undo.
// It is what makes typing a word undo as a word rather than a letter at a time,
// and the caller decides where a word ends because only the caller knows.
//
// Begin inside a group does nothing, so a caller need not track whether one is
// open.
func (u *Undo) Begin() {
	if u.open == nil {
		u.open = &step{}
	}
}

// Commit closes the group. A group that turned out to be empty leaves nothing
// to undo, so pressing undo afterwards reaches past it to the edit before.
func (u *Undo) Commit() {
	if u.open == nil {
		return
	}
	if len(u.open.edits) > 0 {
		u.done = append(u.done, *u.open)
	}
	u.open = nil
}

// CanUndo and CanRedo report whether there is anything to put back or do again.
func (u *Undo) CanUndo() bool { return len(u.done) > 0 }

func (u *Undo) CanRedo() bool { return len(u.redo) > 0 }

// Undo puts back the last edit, or the last group, and returns the operations
// to broadcast. It returns [ErrNoChange] when there is nothing to put back.
//
// An open group is closed first, so a caller that began one and then asked for
// an undo gets what it has just done rather than what came before it.
func (u *Undo) Undo() ([]crdt.Op, error) {
	u.Commit()
	if len(u.done) == 0 {
		return nil, ErrNoChange
	}
	last := u.done[len(u.done)-1]
	ops, mirror, err := u.invert(last)
	if err != nil {
		return nil, err
	}
	u.done = u.done[:len(u.done)-1]
	u.redo = append(u.redo, mirror)
	return ops, nil
}

// Redo does again what the last Undo put back. It returns [ErrNoChange] when
// there is nothing, which includes after any new edit: making one says what the
// document is to become, and what Redo was holding is no longer part of it.
func (u *Undo) Redo() ([]crdt.Op, error) {
	if len(u.redo) == 0 {
		return nil, ErrNoChange
	}
	last := u.redo[len(u.redo)-1]
	ops, mirror, err := u.invert(last)
	if err != nil {
		return nil, err
	}
	u.redo = u.redo[:len(u.redo)-1]
	u.done = append(u.done, mirror)
	return ops, nil
}

// invert applies the opposite of a step and returns both the operations that
// did it and the step that would put *that* back — which is what Redo needs,
// and what makes undo and redo the same walk in opposite directions.
//
// The edits of a step are inverted last first, so that one made on top of
// another comes off first.
func (u *Undo) invert(s step) ([]crdt.Op, step, error) {
	var ops []crdt.Op
	var mirror step
	for i := len(s.edits) - 1; i >= 0; i-- {
		got, back, err := u.invertOne(s.edits[i])
		if err != nil {
			return nil, step{}, err
		}
		ops = append(ops, got...)
		mirror.edits = append(mirror.edits, back)
	}
	return ops, mirror, nil
}

// invertOne undoes a single insertion or removal.
func (u *Undo) invertOne(e edit) ([]crdt.Op, edit, error) {
	if len(e.ids) > 0 {
		return u.removeAgain(e)
	}
	return u.putBack(e)
}

// removeAgain takes out the characters an insertion made, wherever they are
// now. Each is found by its identity, so anything typed around them since is
// left alone; one a peer has already removed is skipped rather than removed
// twice.
func (u *Undo) removeAgain(e edit) ([]crdt.Op, edit, error) {
	var ops []crdt.Op
	var text []rune
	var after crdt.ID
	runes := []rune(u.doc.String())
	// Highest offset first: removing a character moves everything after it.
	for i := len(e.ids) - 1; i >= 0; i-- {
		id := e.ids[i]
		if !u.doc.Visible(id) {
			continue // already gone, by a peer or by another undo
		}
		pos, _ := u.doc.Position(id) // visible, so it has one
		if i == 0 {
			// Where the text goes back to, if this is undone in turn.
			after = crdt.ID{}
			if pos > 0 {
				after, _ = u.doc.Anchor(pos - 1)
			}
		}
		text = append([]rune{runes[pos]}, text...)
		got, err := u.doc.Delete(pos, 1)
		if err != nil {
			return nil, edit{}, err
		}
		ops = append(ops, got...)
	}
	return ops, edit{text: string(text), after: after}, nil
}

// putBack re-inserts what a removal took out, after the character it followed.
// The text is new — the old characters are gone for good, which is what a
// sequence that only ever grows means — so anything anchored to them, a comment
// or a mark, does not follow it back.
func (u *Undo) putBack(e edit) ([]crdt.Op, edit, error) {
	pos := 0
	if !e.after.IsRoot() {
		// Known: the identity came from this document, and a character is never
		// forgotten — a removed one keeps the place the text closed up to, which
		// is where the text belongs.
		at, _ := u.doc.Position(e.after)
		pos = at + 1
		if pos > u.doc.Len() {
			pos = u.doc.Len()
		}
	}
	ops, err := u.doc.Insert(pos, e.text)
	if err != nil {
		return nil, edit{}, err
	}
	ids := make([]crdt.ID, 0, len(ops))
	for _, op := range ops {
		ids = append(ids, op.ID)
	}
	return ops, edit{ids: ids}, nil
}

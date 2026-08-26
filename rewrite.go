package crdt

// Rewriting a replica trades its past for its size.
//
// A replica remembers every operation ever applied to it, including the ones
// that removed things: a deletion hides a character, it does not forget it,
// because a replica that forgot could not tell a late arrival apart from a
// character it had already seen. That memory is what makes merging work without
// a server, and it is also what makes a heavily revised document larger than
// its text.
//
// A rewrite builds a new replica holding the same content and none of the
// history. What that buys is exactly what was deleted and nothing else: a
// document nothing was ever removed from rewrites to the same size, while one
// that was emptied rewrites to nothing. Measured on a text of 40 000 edits: no
// deletions, 1.0x; a third of it deleted, 1.6x; all of it, four orders of
// magnitude.
//
// What it costs is every identity. The new replica mints its own, so:
//
//   - Operations from the old replica no longer apply to the new one. They are
//     not rejected and they do not corrupt it; they anchor to characters it has
//     never heard of, so they park as pending and stay there. Any replica still
//     holding the old identities has to be replaced by the rewrite, not merged
//     with it.
//   - Anything anchored to a character is left pointing at nothing. This is the
//     same trap [Proposals] exists to avoid, and it is why there is no rewrite
//     for a [Composite]: rich text marks, tree parents and sequence positions
//     are stored against the identities of the characters they describe, and a
//     composite cannot tell a part that carries such anchors from one that does
//     not. Rewrite the parts you know are plain, or rebuild the anchors
//     yourself.
//
// So a rewrite belongs where a document is quiescent and about to be archived,
// or where a single writer is compacting its own copy. It does not belong in a
// live session.
//
// Rewritten returns a new document with this one's text, minted at site. Pass a
// site the old replica never used: reusing one would let two different
// characters carry the same identity, which is the one thing a replica may not
// allow.
func (d *Doc) Rewritten(site SiteID) (*Doc, error) {
	return d.rewriteInto(New(site))
}

func (d *Doc) rewriteInto(fresh *Doc) (*Doc, error) {
	if text := d.String(); text != "" {
		if _, err := fresh.Insert(0, text); err != nil {
			return nil, err
		}
	}
	return fresh, nil
}

// Rewritten returns a new list with this one's values, minted at site. It trades
// the list's past for its size on the terms described on [Doc.Rewritten].
func (l *List) Rewritten(site SiteID) (*List, error) {
	return l.rewriteInto(NewList(site))
}

func (l *List) rewriteInto(fresh *List) (*List, error) {
	if values := l.Values(); len(values) > 0 {
		if _, err := fresh.Insert(0, values...); err != nil {
			return nil, err
		}
	}
	return fresh, nil
}

// Rewritten returns a new map with this one's entries, minted at site. A map
// keeps one record per key rather than a record per edit, so it has far less
// past to trade than a text does; this exists so a composite's parts can all be
// rewritten the same way, on the terms described on [Doc.Rewritten].
func (m *Map) Rewritten(site SiteID) (*Map, error) {
	return m.rewriteInto(NewMap(site))
}

func (m *Map) rewriteInto(fresh *Map) (*Map, error) {
	// Keys reports the live keys and Get answers for every one of them, so the
	// lookup below cannot miss.
	for _, key := range m.Keys() {
		value, _ := m.Get(key)
		if _, err := fresh.Set(key, value); err != nil {
			return nil, err
		}
	}
	return fresh, nil
}

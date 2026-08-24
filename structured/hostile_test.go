package structured

import (
	"testing"
	"unicode/utf8"

	"github.com/go-crdt/crdt"
)

// A replicated map holds whatever key an applied operation names, and whatever
// bytes it carries. Nothing in this package decides what a peer sends: an
// operation that decodes is applied, and every value in it is then read by
// whichever type owns that part.
//
// So each of these types has a decoder standing between a peer's bytes and its
// own arithmetic, and what those decoders must never do is panic, hang, or
// return something the code around them then indexes with. Six of them had no
// fuzzing at all when this file was written — decodeMark, decodeReading,
// decodePoint, decodeManifest, decodeTally, decodePos — and a decoder is only
// half the surface: what the reader does with what it decoded is the other half.
//
// Each target below therefore writes arbitrary bytes into a live document and
// then calls every reader that type has, rather than calling the decoder alone.
// The property is not "the value is rejected" — a peer is allowed to write
// nonsense — it is that the document still reads, as some document, without
// falling over.

// writable reports whether a map would accept the key at all. A key that is not
// valid UTF-8 or is empty is refused by [crdt.Map] itself, so feeding one here
// tests the map's own guard rather than the type's decoder — which map_test.go
// already does.
func writable(key string) bool { return key != "" && utf8.ValidString(key) }

// FuzzHostileMarks: formatting is worked out when the text is read, from bytes
// a peer wrote. Spans and MarksAt walk them.
func FuzzHostileMarks(f *testing.F) {
	f.Add("1.1", []byte{1, 0, 4, 'b', 'o', 'l', 'd', 0, 2, 0, 3, 0})
	f.Add("x", []byte{})
	f.Add("1.1", []byte{2, 255, 255})
	f.Fuzz(func(t *testing.T, key string, value []byte) {
		if !writable(key) {
			t.Skip()
		}
		r := NewRichText(1)
		if _, err := r.Insert(0, "hello world"); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Mark(0, 5, "bold", nil, ExpandEnd); err != nil {
			t.Fatal(err)
		}
		if _, err := r.marks.Set(key, value); err != nil {
			t.Fatal(err)
		}
		_ = r.Text()
		_ = r.Len()
		for i := range r.Len() + 1 {
			_ = r.MarksAt(i)
		}
		spans := r.Spans()
		// Whatever the marks say, the spans still cover the text exactly once
		// and in order — that is what a renderer is entitled to.
		want := len([]rune(r.Text()))
		got, at := 0, 0
		for _, s := range spans {
			if s.Pos != at {
				t.Fatalf("span at %d, expected %d: spans do not tile the text", s.Pos, at)
			}
			n := len([]rune(s.Text))
			at += n
			got += n
		}
		if got != want {
			t.Fatalf("spans cover %d characters of %d", got, want)
		}
	})
}

// FuzzHostileReadings: a multi-value register reads a version vector out of
// every entry and compares them, so a malformed one must not be comparable to
// anything in a way that loses the real values.
func FuzzHostileReadings(f *testing.F) {
	f.Add("2", []byte{1, 2, 1, 1, 'x'})
	f.Add("2", []byte{255})
	f.Add("notasite", []byte{0, 1})
	f.Fuzz(func(t *testing.T, key string, value []byte) {
		if !writable(key) {
			t.Skip()
		}
		r := NewMultiRegister(1)
		if _, err := r.Set([]byte("mine")); err != nil {
			t.Fatal(err)
		}
		if _, err := r.m.Set(key, value); err != nil {
			t.Fatal(err)
		}
		readings := r.Readings()
		_ = r.Values()
		_, _ = r.Value()
		_ = r.Conflicted()
		// This replica's own writing is well formed and was written knowing
		// nothing else, so nothing a peer can write makes it disappear without
		// dominating it — and a malformed entry is not decodable, so it cannot.
		mine := false
		for _, reading := range readings {
			if reading.Site == 1 && string(reading.Value) == "mine" {
				mine = true
			}
		}
		if !mine && !dominatedByAnyDecodable(r) {
			t.Fatalf("a peer's bytes took this replica's own value away: %v", readings)
		}
	})
}

// dominatedByAnyDecodable reports whether some decodable entry really does
// dominate this replica's own — the only legitimate reason for it to be gone.
func dominatedByAnyDecodable(r *MultiRegister) bool {
	all := r.readings()
	var mine entry
	found := false
	for _, e := range all {
		if e.Site == 1 {
			mine, found = e, true
		}
	}
	if !found {
		return true // it was overwritten, which is the caller's own doing
	}
	for i, e := range all {
		if all[i].Site != 1 && dominates(e.vector, mine.vector) {
			return true
		}
	}
	return false
}

// FuzzHostilePoints: a stroke's points are a list a peer appends to, and each
// carries the identity of the stroke it belongs to.
func FuzzHostilePoints(f *testing.F) {
	f.Add([]byte{1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	f.Add([]byte{0})
	f.Add([]byte{255, 255, 255})
	f.Fuzz(func(t *testing.T, value []byte) {
		if len(value) == 0 {
			t.Skip() // a list refuses an empty value; list_test.go covers that
		}
		ink := NewInk(1)
		stroke, _, err := ink.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ink.Extend(stroke, Point{X: 1, Y: 2, Pressure: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := ink.points.Insert(ink.points.Len(), value); err != nil {
			t.Fatal(err)
		}
		_ = ink.Paths()
		_ = ink.Points(stroke)
		_ = ink.Strokes().Items()
	})
}

// FuzzHostileManifest: a file is a name pointing at a list of chunk digests, and
// reading it means looking every one of them up.
func FuzzHostileManifest(f *testing.F) {
	f.Add("a.png", []byte{4, 1, 'x'})
	f.Add("a.png", []byte{255, 255, 255, 255, 255, 255, 255, 255, 255, 255})
	f.Add("a.png", []byte{})
	f.Fuzz(func(t *testing.T, name string, value []byte) {
		if !writable(name) {
			t.Skip()
		}
		b := NewBlobs(1)
		if _, err := b.Put("real.png", []byte("some bytes")); err != nil {
			t.Fatal(err)
		}
		if _, err := b.manifest.Set(name, value); err != nil {
			t.Fatal(err)
		}
		_ = b.Names()
		_ = b.Stored()
		for _, n := range b.Names() {
			_, _ = b.Get(n)
			_, _ = b.Size(n)
			_ = b.Missing(n)
		}
		// The file this replica really stored is still readable whatever a peer
		// says about some other name.
		if got, ok := b.Get("real.png"); name != "real.png" && (!ok || string(got) != "some bytes") {
			t.Fatalf("a peer's manifest for %q made real.png unreadable: %q %v", name, got, ok)
		}
	})
}

// FuzzHostileTally: a counter sums two numbers out of every site's entry.
func FuzzHostileTally(f *testing.F) {
	f.Add("2", []byte{1, 1})
	f.Add("2", []byte{255, 255, 255, 255, 255, 255, 255, 255, 255, 255})
	f.Add("nonsense", []byte{})
	f.Fuzz(func(t *testing.T, key string, value []byte) {
		if !writable(key) {
			t.Skip()
		}
		c := NewCounter(1)
		if _, err := c.Add(7); err != nil {
			t.Fatal(err)
		}
		if _, err := c.m.Set(key, value); err != nil {
			t.Fatal(err)
		}
		added, removed, total := c.Added(), c.Removed(), c.Value()
		if added-removed != total {
			t.Fatalf("Value %d is not Added %d less Removed %d", total, added, removed)
		}
	})
}

// FuzzHostileTree: a node's parent is a key and its place is a rank, both bytes
// a peer chooses. Reading has to terminate and to produce a tree.
func FuzzHostileTree(f *testing.F) {
	f.Add("1.1", "\x00parent", []byte("1.2"))
	f.Add("1.1", "\x00rank", []byte{255, 255})
	f.Add("9.9", "\x00parent", []byte("9.9"))
	f.Fuzz(func(t *testing.T, rec, field string, value []byte) {
		if !writable(rec) || !writable(field) {
			t.Skip()
		}
		tree := NewTree(1)
		a, _, err := tree.Insert(TreeID{}, TreeID{})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := tree.Insert(a, TreeID{}); err != nil {
			t.Fatal(err)
		}
		if _, err := tree.r.SetField(rec, field, value); err != nil {
			t.Fatal(err)
		}
		nodes := tree.Nodes()
		// Every node is reachable from the root by walking parents, in at most
		// as many steps as there are nodes. A shape that fails this is a ring
		// the reader did not break, and the caller would loop on it.
		for _, n := range nodes {
			at, steps := n, 0
			for !at.IsRoot() {
				up, ok := tree.Parent(at)
				if !ok {
					break
				}
				at = up
				steps++
				if steps > len(nodes)+1 {
					t.Fatalf("node %v is in a ring the reader did not break", n)
				}
			}
		}
		_ = tree.Children(TreeID{})
		for _, n := range nodes {
			_ = tree.Children(n)
			_, _ = tree.Depth(n)
		}
	})
}

// FuzzHostileBlocks: a block is a marker in the text and a record in a map, and
// a peer controls both.
func FuzzHostileBlocks(f *testing.F) {
	f.Add("1.1", "\x00depth", []byte{255, 255, 255, 255, 255, 255, 255, 255, 255, 255}, "abc")
	f.Add("1.1", "\x00type", []byte("heading"), "�")
	f.Fuzz(func(t *testing.T, rec, field string, value []byte, text string) {
		if !writable(rec) || !writable(field) || !utf8.ValidString(text) {
			t.Skip()
		}
		b := NewBlocks(1)
		first, _, err := b.Insert(DocStart, "paragraph")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := b.InsertText(first, 0, "hello"); err != nil {
			t.Fatal(err)
		}
		// Straight into the text part, markers and all, as another version of
		// this code would.
		if _, err := b.RichText().Doc().Insert(0, text); err != nil {
			t.Fatal(err)
		}
		if _, err := b.recs.SetField(rec, field, value); err != nil {
			t.Fatal(err)
		}
		blocks := b.List()
		if got := b.Len(); got != len(blocks) {
			t.Fatalf("Len says %d blocks and List returns %d", got, len(blocks))
		}
		// The blocks tile the text: every character is in exactly one of them,
		// and the markers are in none.
		total := 0
		for _, blk := range blocks {
			total += len([]rune(blk.Text))
			_ = b.Spans(blk.ID)
			_, _ = b.Text(blk.ID)
		}
		markers := 0
		for _, r := range b.RichText().Text() {
			if r == BlockMark {
				markers++
			}
		}
		// Every character is in a block, is a marker, or is in the preamble —
		// the text before the first marker, which no writer in this package
		// can produce and which Preamble is where it goes rather than nowhere.
		preamble := len([]rune(b.Preamble()))
		if total+markers+preamble != len([]rune(b.RichText().Text())) {
			t.Fatalf("blocks cover %d characters plus %d markers and %d of preamble, of %d",
				total, markers, preamble, len([]rune(b.RichText().Text())))
		}
		_ = b.Outline()
	})
}

// FuzzHostileSheet: a cell is bytes, and a row or column identity is a key.
func FuzzHostileSheet(f *testing.F) {
	f.Add("1.1:1.2", []byte{0, 1, 'x'})
	f.Add("nonsense", []byte{})
	f.Fuzz(func(t *testing.T, key string, value []byte) {
		if !writable(key) {
			t.Skip()
		}
		s := NewSheet(1)
		row, _, err := s.AppendRow()
		if err != nil {
			t.Fatal(err)
		}
		col, _, err := s.AppendCol()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetCell(row, col, Literal("42")); err != nil {
			t.Fatal(err)
		}
		if key == cellKey(row, col) {
			t.Skip() // overwriting the real cell is the caller's own doing
		}
		if _, err := s.cells.Set(key, value); err != nil {
			t.Fatal(err)
		}
		for _, r := range s.Rows() {
			for _, c := range s.Cols() {
				_, _ = s.GetCell(r, c)
			}
		}
		if cell, ok := s.GetCell(row, col); !ok || cell.Text != "42" {
			t.Fatalf("a peer's cell key %q made a real cell unreadable: %+v %v", key, cell, ok)
		}
	})
}

// FuzzHostileProposals: a proposal carries a whole batch of operations and a
// version, both as bytes in a map value.
func FuzzHostileProposals(f *testing.F) {
	f.Add("1.1", "\x00ops", []byte{1, 1, 1, 't', 0})
	f.Add("1.1", "\x00base", []byte{255, 255})
	f.Fuzz(func(t *testing.T, rec, field string, value []byte) {
		if !writable(rec) || !writable(field) {
			t.Skip()
		}
		p := NewProposals(1)
		text, err := p.Composite().Text("text")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := text.Insert(0, "hello"); err != nil {
			t.Fatal(err)
		}
		if _, err := p.recs.SetField(rec, field, value); err != nil {
			t.Fatal(err)
		}
		before := text.String()
		list := p.List()
		_ = p.Open()
		for _, proposal := range list {
			// Reading a proposal must not change the document.
			if _, err := p.Preview(proposal.ID, 99); err != nil {
				t.Fatalf("previewing a proposal this replica lists: %v", err)
			}
		}
		if got := text.String(); got != before {
			t.Fatalf("reading proposals changed the document from %q to %q", before, got)
		}
	})
}

// The decoders themselves, so a crash found in one is reported against the
// decoder rather than against whichever reader happened to reach it.
func FuzzDecodeMark(f *testing.F) {
	f.Add([]byte{1, 0, 4, 'b', 'o', 'l', 'd', 0, 2, 0, 3, 0})
	f.Fuzz(func(t *testing.T, value []byte) { _, _ = decodeMark(value) })
}

func FuzzDecodeReading(f *testing.F) {
	f.Add([]byte{1, 2, 1, 1, 'x'})
	f.Fuzz(func(t *testing.T, value []byte) {
		vector, _, _, ok := decodeReading(value)
		if !ok {
			return
		}
		// A decodable vector re-encodes to the same bytes: the encoding is
		// canonical, which is what lets a snapshot be compared byte for byte.
		again := encodeReading(vector, nil, true)
		if _, _, _, ok := decodeReading(again); !ok {
			t.Fatalf("a vector decoded from %x re-encodes to something undecodable", value)
		}
	})
}

func FuzzDecodePoint(f *testing.F) {
	f.Add([]byte{1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, value []byte) { _, _, _ = decodePoint(value) })
}

func FuzzDecodeManifest(f *testing.F) {
	f.Add([]byte{4, 1, 'x'})
	f.Fuzz(func(t *testing.T, value []byte) {
		total, keys, ok := decodeManifest(value)
		if ok && total < 0 {
			t.Fatalf("a manifest decoded to a negative length %d with %d keys", total, len(keys))
		}
	})
}

func FuzzDecodeTally(f *testing.F) {
	f.Add([]byte{1, 1})
	f.Fuzz(func(t *testing.T, value []byte) { _, _, _ = decodeTally(value) })
}

func FuzzDecodePos(f *testing.F) {
	f.Add([]byte{0, 0, 0, 1, 0, 0, 0, 2})
	f.Fuzz(func(t *testing.T, value []byte) { _, _, _ = decodePos(value) })
}

// A map operation a peer sends can name any key at all, and every type here
// keys its own map some particular way. This walks the whole family with one
// hostile key and value, and asks only that the type still reads.
func TestEveryTypeSurvivesAKeyItDidNotWrite(t *testing.T) {
	hostile := []struct{ key, value string }{
		{"", "x"},
		{"\x00", "x"},
		{"\x00rank", ""},
		{"1.1", ""},
		{"..", "\x00\x00"},
		{"999999999999999999999.1", "x"},
		{"1.1:1.1", "\xff\xfe"},
		{"\x00mint", "\xff"},
	}
	for _, h := range hostile {
		if !writable(h.key) {
			continue
		}
		t.Run(h.key, func(t *testing.T) {
			// Each of these owns one map; writing straight into it is what an
			// applied operation from a peer does.
			tree := NewTree(1)
			if _, _, err := tree.Insert(TreeID{}, TreeID{}); err != nil {
				t.Fatal(err)
			}
			if _, err := tree.Map().Set(h.key, []byte(h.value)); err != nil {
				t.Fatal(err)
			}
			_ = tree.Nodes()
			_ = tree.Children(TreeID{})

			seq := NewSequence(1)
			if _, _, err := seq.Insert(SeqStart, []byte("a")); err != nil {
				t.Fatal(err)
			}
			if _, err := seq.Map().Set(h.key, []byte(h.value)); err != nil {
				t.Fatal(err)
			}
			_ = seq.Items()

			set := NewSet(1)
			if _, err := set.Add("a"); err != nil {
				t.Fatal(err)
			}
			if _, err := set.Map().Set(h.key, []byte(h.value)); err != nil {
				t.Fatal(err)
			}
			_ = set.Names()
			_ = set.Len()

			c := NewCounter(1)
			if _, err := c.Add(1); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Map().Set(h.key, []byte(h.value)); err != nil {
				t.Fatal(err)
			}
			_ = c.Value()

			m := NewMultiRegister(1)
			if _, err := m.Set([]byte("a")); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Map().Set(h.key, []byte(h.value)); err != nil {
				t.Fatal(err)
			}
			_ = m.Readings()
		})
	}
}

// A composite a peer sends can name a part this type does not know, and the
// type still has to read.
func TestATypeReadsBesideAPartItDoesNotKnow(t *testing.T) {
	b := NewBlocks(1)
	id, _, err := b.Insert(DocStart, "paragraph")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.InsertText(id, 0, "hello"); err != nil {
		t.Fatal(err)
	}
	stranger := crdt.Part{Kind: crdt.PartList, Name: "something/else"}
	op := crdt.ListOp{Kind: crdt.OpInsert, ID: crdt.ID{Site: 9, Seq: 1}, Clock: 1, Value: []byte("x")}
	if err := b.Apply(crdt.PartOps{Part: stranger, List: []crdt.ListOp{op}}); err != nil {
		t.Fatalf("a part this type does not know was refused: %v", err)
	}
	if got := b.Len(); got != 1 {
		t.Fatalf("%d blocks after a stranger's part arrived", got)
	}
	if txt, _ := b.Text(id); txt != "hello" {
		t.Fatalf("the block reads %q", txt)
	}
}

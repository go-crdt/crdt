package structured

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-crdt/crdt"
)

func mustOpen(t *testing.T, b *Blocks, after BlockID, typ string) BlockID {
	t.Helper()
	id, _, err := b.Insert(after, typ)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustType(t *testing.T, b *Blocks, id BlockID, offset int, s string) {
	t.Helper()
	if _, err := b.InsertText(id, offset, s); err != nil {
		t.Fatal(err)
	}
}

// read renders a document as text, which is the only readable way to say two
// replicas agree about one.
func read(b *Blocks) string {
	out := ""
	for _, blk := range b.List() {
		out += fmt.Sprintf("%s%s: %q\n", strings.Repeat("  ", blk.Depth), blk.Type, blk.Text)
	}
	return out
}

func syncBlocks(t *testing.T, a, b *Blocks) {
	t.Helper()
	fromA := must(a.OpsSince(b.Version()))
	fromB := must(b.OpsSince(a.Version()))
	if err := b.Apply(fromA...); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(fromB...); err != nil {
		t.Fatal(err)
	}
	if a.Pending() != 0 || b.Pending() != 0 {
		t.Fatalf("operations held back: a=%d b=%d", a.Pending(), b.Pending())
	}
}

// A document is blocks, each of a type, each holding text.
func TestADocumentIsBlocksOfText(t *testing.T) {
	doc := NewBlocks(1)
	if doc.Len() != 0 || doc.List() != nil {
		t.Fatalf("a fresh document holds %d blocks", doc.Len())
	}

	title := mustOpen(t, doc, DocStart, "heading")
	mustType(t, doc, title, 0, "On rivers")
	body := mustOpen(t, doc, title, "paragraph")
	mustType(t, doc, body, 0, "They run downhill.")

	want := "heading: \"On rivers\"\nparagraph: \"They run downhill.\"\n"
	if got := read(doc); got != want {
		t.Fatalf("document reads\n%swant\n%s", got, want)
	}
	if got := doc.Plain("\n"); got != "On rivers\nThey run downhill." {
		t.Fatalf("plain text is %q", got)
	}
	if got, ok := doc.Text(title); !ok || got != "On rivers" {
		t.Fatalf("Text(title) = %q, %v", got, ok)
	}
	if blk, ok := doc.Block(body); !ok || blk.Type != "paragraph" || blk.Text != "They run downhill." {
		t.Fatalf("Block(body) = %+v, %v", blk, ok)
	}
	if ids := doc.IDs(); len(ids) != 2 || ids[0] != title || ids[1] != body {
		t.Fatalf("IDs = %v, want [%v %v]", ids, title, body)
	}
}

// The seam between two blocks has two sides, and two people can use them at the
// same moment without arbitration. This is the whole reason a block begins at a
// character rather than at a boundary between two of them: "the end of this
// paragraph" and "the start of the next" are the same offset, and they are not
// the same place.
func TestTwoPeopleEditTheSameSeamAndMeanDifferentThings(t *testing.T) {
	a := NewBlocks(1)
	first := mustOpen(t, a, DocStart, "paragraph")
	mustType(t, a, first, 0, "one")
	second := mustOpen(t, a, first, "paragraph")
	mustType(t, a, second, 0, "two")

	b := NewBlocks(2)
	if err := b.Apply(must(a.OpsSince(nil))...); err != nil {
		t.Fatal(err)
	}

	// Neither can see the other. One is finishing the first paragraph; the
	// other is starting the second.
	if _, err := a.InsertText(first, 3, "!"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.InsertText(second, 0, "…"); err != nil {
		t.Fatal(err)
	}
	syncBlocks(t, a, b)

	want := "paragraph: \"one!\"\nparagraph: \"…two\"\n"
	if got := read(a); got != want {
		t.Fatalf("a reads\n%swant\n%s", got, want)
	}
	if got := read(b); got != want {
		t.Fatalf("b reads\n%swant\n%s", got, want)
	}
}

// Two people typing at the same side of the same seam converge too — the
// ordinary case, which the sequence has always settled.
func TestTwoPeopleTypeAtTheSameSideOfASeam(t *testing.T) {
	a := NewBlocks(1)
	one := mustOpen(t, a, DocStart, "paragraph")
	mustType(t, a, one, 0, "x")
	two := mustOpen(t, a, one, "paragraph")

	b := NewBlocks(2)
	if err := b.Apply(must(a.OpsSince(nil))...); err != nil {
		t.Fatal(err)
	}
	if _, err := a.InsertText(two, 0, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.InsertText(two, 0, "b"); err != nil {
		t.Fatal(err)
	}
	syncBlocks(t, a, b)

	if read(a) != read(b) {
		t.Fatalf("replicas disagree\n%s\n%s", read(a), read(b))
	}
	if got, _ := a.Text(two); got != "ab" && got != "ba" {
		t.Fatalf("second block holds %q, want both characters", got)
	}
}

// Pressing return in the middle of a paragraph cuts it in two.
func TestSplitCutsABlockInTwo(t *testing.T) {
	doc := NewBlocks(1)
	p := mustOpen(t, doc, DocStart, "paragraph")
	mustType(t, doc, p, 0, "before after")
	if _, err := doc.SetDepth(p, 2); err != nil {
		t.Fatal(err)
	}

	rest, _, err := doc.Split(p, 7, "paragraph")
	if err != nil {
		t.Fatal(err)
	}
	want := "    paragraph: \"before \"\n    paragraph: \"after\"\n"
	if got := read(doc); got != want {
		t.Fatalf("split reads\n%swant\n%s", got, want)
	}
	if blk, _ := doc.Block(rest); blk.Depth != 2 {
		t.Fatalf("the new block sits at depth %d, want the depth it came from", blk.Depth)
	}

	// At either end: an empty block after, and an empty block before.
	if _, _, err := doc.Split(rest, 5, "paragraph"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := doc.Split(p, 0, "paragraph"); err != nil {
		t.Fatal(err)
	}
	want = "    paragraph: \"\"\n    paragraph: \"before \"\n    paragraph: \"after\"\n    paragraph: \"\"\n"
	if got := read(doc); got != want {
		t.Fatalf("splitting at the edges reads\n%swant\n%s", got, want)
	}
}

// Backspace at the start of a block joins it to the one above.
func TestMergeJoinsABlockToTheOneAbove(t *testing.T) {
	doc := NewBlocks(1)
	one := mustOpen(t, doc, DocStart, "paragraph")
	mustType(t, doc, one, 0, "one ")
	two := mustOpen(t, doc, one, "quote")
	mustType(t, doc, two, 0, "two")

	if _, err := doc.Merge(two); err != nil {
		t.Fatal(err)
	}
	if got, want := read(doc), "paragraph: \"one two\"\n"; got != want {
		t.Fatalf("merged reads %q, want %q", got, want)
	}
	if _, ok := doc.Block(two); ok {
		t.Fatal("the merged block is still there")
	}
	// The record went with it, so nothing is left saying it was a quote.
	if doc.Records().HasRecord(two.key()) {
		t.Fatal("the merged block's record survives it")
	}

	// The first block has nothing above it to join.
	if _, err := doc.Merge(one); !errors.Is(err, crdt.ErrOutOfRange) {
		t.Fatalf("merging the first block = %v, want out of range", err)
	}
}

// Removing a block takes its text with it.
func TestRemoveTakesTheTextWithIt(t *testing.T) {
	doc := NewBlocks(1)
	one := mustOpen(t, doc, DocStart, "paragraph")
	mustType(t, doc, one, 0, "keep")
	two := mustOpen(t, doc, one, "paragraph")
	mustType(t, doc, two, 0, "drop")
	three := mustOpen(t, doc, two, "paragraph")
	mustType(t, doc, three, 0, "keep too")

	if _, err := doc.Remove(two); err != nil {
		t.Fatal(err)
	}
	if got, want := read(doc), "paragraph: \"keep\"\nparagraph: \"keep too\"\n"; got != want {
		t.Fatalf("after removing reads %q, want %q", got, want)
	}
	if got := doc.Plain("|"); got != "keep|keep too" {
		t.Fatalf("plain text is %q", got)
	}
}

// A block's type and its nesting are single fields, so two people changing them
// at once is a conflict the map already settles — and the loser can say it
// again.
func TestTypeAndDepthAreOneFieldEach(t *testing.T) {
	a := NewBlocks(1)
	p := mustOpen(t, a, DocStart, "paragraph")
	mustType(t, a, p, 0, "text")

	b := NewBlocks(2)
	if err := b.Apply(must(a.OpsSince(nil))...); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetType(p, "heading"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SetType(p, "quote"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetDepth(p, 1); err != nil {
		t.Fatal(err)
	}
	syncBlocks(t, a, b)
	if read(a) != read(b) {
		t.Fatalf("replicas disagree\n%s\n%s", read(a), read(b))
	}
	blk, _ := a.Block(p)
	if blk.Type != "heading" && blk.Type != "quote" {
		t.Fatalf("the block became %q, which neither replica asked for", blk.Type)
	}
	if blk.Depth != 1 {
		t.Fatalf("depth is %d, want 1", blk.Depth)
	}

	// The empty type takes it off.
	if _, err := a.SetType(p, ""); err != nil {
		t.Fatal(err)
	}
	if blk, _ := a.Block(p); blk.Type != "" {
		t.Fatalf("the type is still %q", blk.Type)
	}
}

// A caller's own fields sit beside the two this type keeps, and cannot collide
// with them.
func TestABlockCarriesTheCallersFields(t *testing.T) {
	doc := NewBlocks(1)
	code := mustOpen(t, doc, DocStart, "code")
	if _, err := doc.SetField(code, "language", []byte("go")); err != nil {
		t.Fatal(err)
	}
	if got, ok := doc.Field(code, "language"); !ok || string(got) != "go" {
		t.Fatalf("language = %q, %v", got, ok)
	}
	if _, ok := doc.Field(code, "missing"); ok {
		t.Fatal("a field nobody wrote reads as written")
	}

	// The reserved prefix is refused, both writing and reading, so a caller
	// cannot reach the type or the depth by the back door.
	for _, bad := range []string{"", blockTypeField, blockDepthField, "\x00anything"} {
		if _, err := doc.SetField(code, bad, []byte("x")); !errors.Is(err, crdt.ErrInvalidOp) {
			t.Fatalf("SetField(%q) = %v, want invalid", bad, err)
		}
		if _, ok := doc.Field(code, bad); ok {
			t.Fatalf("Field(%q) reads something", bad)
		}
	}
	if _, err := doc.SetField(code, "\xff\xfe", []byte("x")); !errors.Is(err, crdt.ErrInvalidOp) {
		t.Fatal("a field name that is not text was accepted")
	}
}

// Formatting runs over the text, so a mark can cover two blocks — a comment on
// a pair of paragraphs, a sentence emphasised across a break.
func TestAMarkCanSpanBlocks(t *testing.T) {
	doc := NewBlocks(1)
	one := mustOpen(t, doc, DocStart, "paragraph")
	mustType(t, doc, one, 0, "first")
	two := mustOpen(t, doc, one, "paragraph")
	mustType(t, doc, two, 0, "second")

	if _, err := doc.Mark(one, 2, two, 3, "comment", []byte("look"), ExpandNone); err != nil {
		t.Fatal(err)
	}

	if got := spansOf(doc, one); got != `"fi"/"rst"[comment=look]` {
		t.Fatalf("first block spans %s", got)
	}
	if got := spansOf(doc, two); got != `"sec"[comment=look]/"ond"` {
		t.Fatalf("second block spans %s", got)
	}
	if m := doc.MarksAt(one, 3); string(m["comment"]) != "look" {
		t.Fatalf("MarksAt(one,3) = %v", m)
	}
	if m := doc.MarksAt(one, 0); m != nil {
		t.Fatalf("MarksAt(one,0) = %v, want nothing", m)
	}

	if _, err := doc.Unmark(one, 2, two, 3, "comment"); err != nil {
		t.Fatal(err)
	}
	if got := spansOf(doc, one); got != `"first"` {
		t.Fatalf("after unmarking, first block spans %s", got)
	}
}

// spansOf renders one block's formatting, joined so that the marker characters
// between blocks cannot show through as a break in a run of plain text.
func spansOf(b *Blocks, id BlockID) string {
	out := make([]string, 0, 4)
	for _, s := range b.Spans(id) {
		text := fmt.Sprintf("%q", s.Text)
		if len(s.Marks) > 0 {
			names := make([]string, 0, len(s.Marks))
			for name, value := range s.Marks {
				names = append(names, fmt.Sprintf("%s=%s", name, value))
			}
			text += "[" + strings.Join(names, ",") + "]"
		}
		out = append(out, text)
	}
	return strings.Join(out, "/")
}

// An empty block has no spans, and neither has a block nobody has heard of.
func TestSpansOfNothing(t *testing.T) {
	doc := NewBlocks(1)
	empty := mustOpen(t, doc, DocStart, "paragraph")
	if got := doc.Spans(empty); got != nil {
		t.Fatalf("an empty block has spans %v", got)
	}
	if got := doc.Spans(BlockID{Site: 9, Seq: 9}); got != nil {
		t.Fatalf("an unknown block has spans %v", got)
	}
	if got := doc.MarksAt(BlockID{Site: 9, Seq: 9}, 0); got != nil {
		t.Fatalf("an unknown block has marks %v", got)
	}
	if got := doc.MarksAt(empty, 0); got != nil {
		t.Fatalf("an empty block has marks at 0: %v", got)
	}
	if got := doc.MarksAt(empty, -1); got != nil {
		t.Fatalf("a negative offset reads marks: %v", got)
	}
}

// Nesting is read as an outline: a block hangs under the nearest block before
// it that sits shallower.
func TestNestingReadsAsAnOutline(t *testing.T) {
	doc := NewBlocks(1)
	depths := []int{0, 1, 1, 2, 0, 3}
	ids := make([]BlockID, 0, len(depths))
	after := DocStart
	for i, d := range depths {
		id := mustOpen(t, doc, after, "item")
		mustType(t, doc, id, 0, fmt.Sprint(i))
		if _, err := doc.SetDepth(id, d); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		after = id
	}

	want := []BlockID{DocStart, ids[0], ids[0], ids[2], DocStart, ids[4]}
	for i, o := range doc.Outline() {
		if o.Parent != want[i] {
			t.Fatalf("block %d (depth %d) hangs under %v, want %v", i, o.Depth, o.Parent, want[i])
		}
	}
	if got := doc.Children(DocStart); len(got) != 2 || got[0] != ids[0] || got[1] != ids[4] {
		t.Fatalf("top level holds %v", got)
	}
	if got := doc.Children(ids[0]); len(got) != 2 || got[0] != ids[1] || got[1] != ids[2] {
		t.Fatalf("children of the first item are %v", got)
	}
	// A jump of three levels: the block that opened them stands in for the ones
	// with nothing of their own in them.
	if got := doc.Children(ids[4]); len(got) != 1 || got[0] != ids[5] {
		t.Fatalf("children of the fifth item are %v", got)
	}
	if got := doc.Children(ids[5]); got != nil {
		t.Fatalf("a leaf has children %v", got)
	}
}

// The marker is not something a caller can write, because a caller that could
// would be writing a block boundary into the middle of a word.
func TestTheMarkerCannotBeTyped(t *testing.T) {
	doc := NewBlocks(1)
	p := mustOpen(t, doc, DocStart, "paragraph")
	if _, err := doc.InsertText(p, 0, "a"+string(BlockMark)+"b"); !errors.Is(err, ErrReservedRune) {
		t.Fatalf("typing a marker = %v, want refused", err)
	}
	if got, _ := doc.Text(p); got != "" {
		t.Fatalf("the refused text was written anyway: %q", got)
	}
}

// A marker a peer wrote with nothing said about it is a block nobody has typed,
// not an error: what a document reads as is a function of the state it is in.
func TestAMarkerWithNoRecordIsAnUntypedBlock(t *testing.T) {
	doc := NewBlocks(1)
	p := mustOpen(t, doc, DocStart, "paragraph")
	mustType(t, doc, p, 0, "written")

	// Straight into the text part, which is what a peer running another
	// version of this code would reach. The offset is into the whole text,
	// markers included — which is what [Blocks.At] exists to convert.
	at, err := doc.At(p, 4)
	if err != nil {
		t.Fatal(err)
	}
	if at != 5 {
		t.Fatalf("offset 4 of the first block is %d in the text, want 5", at)
	}
	if _, err := doc.RichText().Doc().Insert(at, string(BlockMark)); err != nil {
		t.Fatal(err)
	}
	want := "paragraph: \"writ\"\n: \"ten\"\n"
	if got := read(doc); got != want {
		t.Fatalf("a bare marker reads\n%swant\n%s", got, want)
	}
	if doc.Len() != 2 {
		t.Fatalf("a bare marker made %d blocks", doc.Len())
	}
}

// A deletion inside a block cannot reach the marker of the next one, so no
// deletion turns two blocks into one by accident. Joining them is Merge, which
// is a different thing to ask for.
func TestADeletionCannotReachPastItsBlock(t *testing.T) {
	doc := NewBlocks(1)
	one := mustOpen(t, doc, DocStart, "paragraph")
	mustType(t, doc, one, 0, "abc")
	two := mustOpen(t, doc, one, "paragraph")
	mustType(t, doc, two, 0, "def")

	if _, err := doc.DeleteText(one, 1, 5); !errors.Is(err, crdt.ErrOutOfRange) {
		t.Fatalf("deleting past the block = %v, want out of range", err)
	}
	if _, err := doc.DeleteText(one, 1, 2); err != nil {
		t.Fatal(err)
	}
	if got, want := read(doc), "paragraph: \"a\"\nparagraph: \"def\"\n"; got != want {
		t.Fatalf("after deleting reads %q, want %q", got, want)
	}
}

// However many blocks a document holds, it is three parts — so what a replica
// says it has, on every sync, does not grow with the document.
//
// The comparison is the shape this type exists to replace: one rich text per
// block, each its own pair of parts.
func TestADocumentIsThreePartsHoweverManyBlocks(t *testing.T) {
	const blocks = 1000

	doc := NewBlocks(1)
	after := DocStart
	for i := range blocks {
		id := mustOpen(t, doc, after, "paragraph")
		mustType(t, doc, id, 0, fmt.Sprintf("block %d", i))
		after = id
	}
	if doc.Len() != blocks {
		t.Fatalf("built %d blocks, want %d", doc.Len(), blocks)
	}

	// A part apiece, which is what a rich text per block costs.
	perBlock := crdt.NewComposite(1)
	for i := range blocks {
		text, err := perBlock.Text(fmt.Sprintf("block:%d/text", i))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := text.Insert(0, fmt.Sprintf("block %d", i)); err != nil {
			t.Fatal(err)
		}
		if _, err := perBlock.Map(fmt.Sprintf("block:%d/marks", i)); err != nil {
			t.Fatal(err)
		}
	}

	// A part nothing has been written to promises nothing and is not in the
	// version at all, so an unmarked document is two parts rather than three.
	if got := len(doc.Version()); got != 2 {
		t.Fatalf("an unmarked document is %d parts, want 2", got)
	}
	if _, err := doc.Mark(after, 0, after, 5, "bold", nil, ExpandEnd); err != nil {
		t.Fatal(err)
	}

	mine, err := doc.Version().MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := perBlock.Version().MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(doc.Version()); got != 3 {
		t.Fatalf("a %d-block document is %d parts, want 3", blocks, got)
	}
	t.Logf("%d blocks: version %d bytes over 3 parts, against %d bytes over %d parts one part per block",
		blocks, len(mine), len(theirs), len(perBlock.Version()))
	// The numbers in this type's own documentation. They are asserted rather
	// than only logged, so the claim fails here if it stops being true.
	if len(mine) > 100 {
		t.Fatalf("version of a %d-block document is %d bytes; it is not supposed to grow with the document",
			blocks, len(mine))
	}
	if len(theirs) < 10000 {
		t.Fatalf("a part per block is %d bytes, which is not the problem this type exists for", len(theirs))
	}
}

// A document survives being written down and read back, blocks, formatting and
// all.
func TestADocumentSurvivesASnapshot(t *testing.T) {
	doc := NewBlocks(1)
	h := mustOpen(t, doc, DocStart, "heading")
	mustType(t, doc, h, 0, "Title")
	p := mustOpen(t, doc, h, "paragraph")
	mustType(t, doc, p, 0, "Body text")
	if _, err := doc.SetDepth(p, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.SetField(p, "note", []byte("n")); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Mark(p, 0, p, 4, "bold", nil, ExpandEnd); err != nil {
		t.Fatal(err)
	}

	back, err := LoadBlocks(2, doc.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := read(back), read(doc); got != want {
		t.Fatalf("reloaded reads\n%swant\n%s", got, want)
	}
	if got, ok := back.Field(p, "note"); !ok || string(got) != "n" {
		t.Fatalf("the field did not survive: %q %v", got, ok)
	}
	if got, want := spansOf(back, p), spansOf(doc, p); got != want {
		t.Fatalf("formatting reloaded as %s, want %s", got, want)
	}
	if back.Site() != 2 {
		t.Fatalf("reloaded as site %d", back.Site())
	}
	if _, err := LoadBlocks(2, []byte("not a snapshot")); err == nil {
		t.Fatal("a snapshot that is not one loaded")
	}
}

// Everything a caller can get wrong, and what it is told.
func TestWhatABlockDocumentRefuses(t *testing.T) {
	doc := NewBlocks(1)
	p := mustOpen(t, doc, DocStart, "paragraph")
	mustType(t, doc, p, 0, "abc")
	gone := BlockID{Site: 7, Seq: 7}

	if _, ok := doc.Block(gone); ok {
		t.Fatal("a block nobody has heard of reads")
	}
	if _, ok := doc.Text(gone); ok {
		t.Fatal("a block nobody has heard of holds text")
	}
	if _, _, err := doc.Insert(gone, "x"); !errors.Is(err, crdt.ErrInvalidOp) {
		t.Fatalf("Insert after a stranger = %v", err)
	}
	if _, _, err := doc.Split(gone, 0, "x"); !errors.Is(err, crdt.ErrInvalidOp) {
		t.Fatalf("Split of a stranger = %v", err)
	}
	if _, err := doc.Merge(gone); !errors.Is(err, crdt.ErrInvalidOp) {
		t.Fatalf("Merge of a stranger = %v", err)
	}
	if _, err := doc.Remove(gone); !errors.Is(err, crdt.ErrInvalidOp) {
		t.Fatalf("Remove of a stranger = %v", err)
	}
	if _, err := doc.SetType(gone, "x"); !errors.Is(err, crdt.ErrInvalidOp) {
		t.Fatalf("SetType of a stranger = %v", err)
	}
	if _, err := doc.SetType(gone, ""); !errors.Is(err, crdt.ErrInvalidOp) {
		t.Fatalf("clearing the type of a stranger = %v", err)
	}
	if _, err := doc.SetDepth(gone, 1); !errors.Is(err, crdt.ErrInvalidOp) {
		t.Fatalf("SetDepth of a stranger = %v", err)
	}
	if _, err := doc.SetField(gone, "f", nil); !errors.Is(err, crdt.ErrInvalidOp) {
		t.Fatalf("SetField of a stranger = %v", err)
	}
	if _, err := doc.InsertText(gone, 0, "x"); !errors.Is(err, crdt.ErrInvalidOp) {
		t.Fatalf("InsertText into a stranger = %v", err)
	}
	if _, err := doc.DeleteText(gone, 0, 1); !errors.Is(err, crdt.ErrInvalidOp) {
		t.Fatalf("DeleteText from a stranger = %v", err)
	}
	if _, err := doc.At(gone, 0); !errors.Is(err, crdt.ErrInvalidOp) {
		t.Fatalf("At in a stranger = %v", err)
	}
	if _, err := doc.Mark(gone, 0, p, 1, "m", nil, ExpandNone); !errors.Is(err, crdt.ErrInvalidOp) {
		t.Fatalf("Mark from a stranger = %v", err)
	}
	if _, err := doc.Mark(p, 0, gone, 1, "m", nil, ExpandNone); !errors.Is(err, crdt.ErrInvalidOp) {
		t.Fatalf("Mark to a stranger = %v", err)
	}
	if _, err := doc.Unmark(gone, 0, p, 1, "m"); !errors.Is(err, crdt.ErrInvalidOp) {
		t.Fatalf("Unmark from a stranger = %v", err)
	}

	// Offsets outside the block.
	if _, err := doc.At(p, 4); !errors.Is(err, crdt.ErrOutOfRange) {
		t.Fatalf("At past the end = %v", err)
	}
	if _, err := doc.At(p, -1); !errors.Is(err, crdt.ErrOutOfRange) {
		t.Fatalf("At before the start = %v", err)
	}
	if got, err := doc.At(p, 3); err != nil || got != 4 {
		t.Fatalf("At the end of the block = %d, %v; want the place after its last character", got, err)
	}
	if _, err := doc.InsertText(p, 4, "x"); !errors.Is(err, crdt.ErrOutOfRange) {
		t.Fatalf("InsertText past the end = %v", err)
	}
	if _, err := doc.DeleteText(p, -1, 1); !errors.Is(err, crdt.ErrOutOfRange) {
		t.Fatalf("DeleteText before the start = %v", err)
	}
	if _, err := doc.DeleteText(p, 0, -1); !errors.Is(err, crdt.ErrOutOfRange) {
		t.Fatalf("DeleteText of a negative count = %v", err)
	}
	if _, _, err := doc.Split(p, 4, "x"); !errors.Is(err, crdt.ErrOutOfRange) {
		t.Fatalf("Split past the end = %v", err)
	}
	if _, err := doc.SetDepth(p, -1); !errors.Is(err, crdt.ErrOutOfRange) {
		t.Fatalf("SetDepth below the top level = %v", err)
	}
	if _, err := doc.Mark(p, 2, p, 1, "m", nil, ExpandNone); !errors.Is(err, crdt.ErrOutOfRange) {
		t.Fatalf("a mark that ends before it starts = %v", err)
	}

	// DocStart names a place, not a block.
	if !DocStart.IsStart() || DocStart.String() == "" {
		t.Fatalf("DocStart = %v", DocStart)
	}
	if p.IsStart() {
		t.Fatal("a real block reads as the start of the document")
	}
}

// A block document is a composite like any other, so it can share one with
// whatever else a page holds.
func TestABlockDocumentSharesACompositeWithTheRest(t *testing.T) {
	doc := crdt.NewComposite(1)
	blocks := BlocksOf(doc)
	p := mustOpen(t, blocks, DocStart, "paragraph")
	mustType(t, blocks, p, 0, "shared")

	chat, err := doc.List("chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Insert(0, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	// Three: the text, the blocks and the chat. The marks part promises
	// nothing until something is marked, and a part promising nothing is not in
	// a version.
	if got := len(doc.Version()); got != 3 {
		t.Fatalf("a document with a chat beside it is %d parts, want 3", got)
	}
	if _, err := blocks.Mark(p, 0, p, 3, "bold", nil, ExpandEnd); err != nil {
		t.Fatal(err)
	}
	if got := len(doc.Version()); got != 4 {
		t.Fatalf("once something is marked it is %d parts, want 4", got)
	}
	if blocks.Composite() != doc {
		t.Fatal("Composite returns something else")
	}
	if got, _ := blocks.Text(p); got != "shared" {
		t.Fatalf("the block reads %q", got)
	}
	if blocks.Records() == nil || blocks.RichText() == nil {
		t.Fatal("the parts underneath are not reachable")
	}
}

// Whatever order the operations of an editing session arrive in, every replica
// reads the same document. This is the property the whole package rests on, and
// blocks add a second structure over the same text, so it is asserted here too.
func TestEveryOrderOfArrivalReadsTheSame(t *testing.T) {
	// One session's worth of editing, recorded as operations.
	source := NewBlocks(1)
	one := mustOpen(t, source, DocStart, "heading")
	mustType(t, source, one, 0, "Title")
	two := mustOpen(t, source, one, "paragraph")
	mustType(t, source, two, 0, "Some body text")
	three, _, err := source.Split(two, 5, "paragraph")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.SetDepth(three, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Mark(two, 0, three, 4, "bold", nil, ExpandEnd); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Merge(three); err != nil {
		t.Fatal(err)
	}
	four := mustOpen(t, source, one, "quote")
	mustType(t, source, four, 0, "quoted")
	if _, err := source.Remove(four); err != nil {
		t.Fatal(err)
	}

	ops := must(source.OpsSince(nil))
	want := read(source)

	// Every rotation of the batch, which is enough to exercise arrival in an
	// order no replica chose while keeping each part's own operations in the
	// order the part requires.
	for shift := range len(ops) {
		replica := NewBlocks(2)
		rotated := make([]crdt.PartOps, 0, len(ops))
		rotated = append(rotated, ops[shift:]...)
		rotated = append(rotated, ops[:shift]...)
		for _, batch := range rotated {
			if err := replica.Apply(batch); err != nil {
				t.Fatalf("shift %d: %v", shift, err)
			}
		}
		if replica.Pending() != 0 {
			t.Fatalf("shift %d: %d operations held back", shift, replica.Pending())
		}
		if got := read(replica); got != want {
			t.Fatalf("shift %d reads\n%swant\n%s", shift, got, want)
		}
	}
}

// Applying the same operations twice changes nothing, which is what lets a
// replica re-send what it is not sure arrived.
func TestApplyingTwiceChangesNothing(t *testing.T) {
	source := NewBlocks(1)
	p := mustOpen(t, source, DocStart, "paragraph")
	mustType(t, source, p, 0, "once")

	replica := NewBlocks(2)
	ops := must(source.OpsSince(nil))
	if err := replica.Apply(ops...); err != nil {
		t.Fatal(err)
	}
	first := read(replica)
	if err := replica.Apply(ops...); err != nil {
		t.Fatal(err)
	}
	if got := read(replica); got != first {
		t.Fatalf("a second application reads\n%swant\n%s", got, first)
	}
}

// A block of no type at no depth writes nothing but the marker, and takes
// nothing but the marker away.
func TestAnUntypedBlockIsJustAMarker(t *testing.T) {
	doc := NewBlocks(1)
	bare, ops, err := doc.Insert(DocStart, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Part != textPart {
		t.Fatalf("an untyped block took %d batches, want just the text", len(ops))
	}
	mustType(t, doc, bare, 0, "text")

	gone, err := doc.Remove(bare)
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 1 || gone[0].Part != textPart {
		t.Fatalf("removing it took %d batches, want just the text", len(gone))
	}
	if doc.Len() != 0 {
		t.Fatalf("%d blocks left", doc.Len())
	}
}

// A depth this version cannot read is no depth, rather than a document that
// will not open: a peer writes what it likes into the map, and what a document
// reads as has to be a function of the state it is in.
func TestADepthThatCannotBeReadIsNoDepth(t *testing.T) {
	doc := NewBlocks(1)
	p := mustOpen(t, doc, DocStart, "paragraph")
	if _, err := doc.SetDepth(p, 3); err != nil {
		t.Fatal(err)
	}
	if blk, _ := doc.Block(p); blk.Depth != 3 {
		t.Fatalf("depth is %d, want 3", blk.Depth)
	}
	// Straight into the record, as a peer running other code would.
	if _, err := doc.Records().SetField(p.key(), blockDepthField, nil); err != nil {
		t.Fatal(err)
	}
	if blk, _ := doc.Block(p); blk.Depth != 0 {
		t.Fatalf("an unreadable depth reads as %d, want 0", blk.Depth)
	}
	if got := doc.depthOf(p); got != 0 {
		t.Fatalf("depthOf an unreadable depth = %d", got)
	}
	// And a block nobody has written a depth for at all.
	other := mustOpen(t, doc, p, "paragraph")
	if got := doc.depthOf(other); got != 0 {
		t.Fatalf("depthOf a block with no depth = %d", got)
	}
}

// Neighbouring stretches of text are one span when they read the same, and two
// when they do not — whether they differ by carrying a different value under
// one name, or by carrying different names altogether.
func TestSpansJoinOnlyWhatReadsAlike(t *testing.T) {
	doc := NewBlocks(1)
	p := mustOpen(t, doc, DocStart, "paragraph")
	mustType(t, doc, p, 0, "abcdef")

	// Same name, different values: two spans.
	if _, err := doc.Mark(p, 0, p, 2, "colour", []byte("red"), ExpandNone); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Mark(p, 2, p, 4, "colour", []byte("blue"), ExpandNone); err != nil {
		t.Fatal(err)
	}
	// Different names over the same width: two more.
	if _, err := doc.Mark(p, 4, p, 5, "bold", nil, ExpandNone); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Mark(p, 5, p, 6, "italic", nil, ExpandNone); err != nil {
		t.Fatal(err)
	}
	want := `"ab"[colour=red]/"cd"[colour=blue]/"e"[bold=]/"f"[italic=]`
	if got := spansOf(doc, p); got != want {
		t.Fatalf("spans are %s, want %s", got, want)
	}

	// The same value over two adjacent stretches reads as one span, which is
	// what keeps the answer a function of the text rather than of how it was
	// marked.
	q := mustOpen(t, doc, p, "paragraph")
	mustType(t, doc, q, 0, "ghij")
	if _, err := doc.Mark(q, 0, q, 2, "bold", nil, ExpandNone); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Mark(q, 2, q, 4, "bold", nil, ExpandNone); err != nil {
		t.Fatal(err)
	}
	if got := spansOf(doc, q); got != `"ghij"[bold=]` {
		t.Fatalf("two marks of one name over one stretch read as %s", got)
	}
}

// With no clock left a replica writes nothing, and says so, rather than wrapping
// round and writing an operation that would lose to one already sent.
func TestABlockDocumentWithNoClockLeft(t *testing.T) {
	doc := NewBlocks(1)
	p := mustOpen(t, doc, DocStart, "paragraph")
	mustType(t, doc, p, 0, "abc")
	deep := mustOpen(t, doc, p, "paragraph")
	if _, err := doc.SetDepth(deep, 1); err != nil {
		t.Fatal(err)
	}

	// The blocks map first; the text still has clock of its own.
	topMap := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1},
		Clock: crdt.MaxClock, Key: "seed", Value: []byte("x")}
	if err := doc.Apply(crdt.PartOps{Part: blocksPart, Map: []crdt.MapOp{topMap}}); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.SetType(p, "heading"); err == nil {
		t.Fatal("typing a block with no clock left was accepted")
	}
	if _, err := doc.SetType(p, ""); err == nil {
		t.Fatal("clearing a type with no clock left was accepted")
	}
	if _, err := doc.SetDepth(p, 2); err == nil {
		t.Fatal("indenting with no clock left was accepted")
	}
	if _, err := doc.SetField(p, "note", []byte("n")); err == nil {
		t.Fatal("a field written with no clock left was accepted")
	}
	// The record cannot be forgotten, so neither can happen — and because the
	// record goes first, neither writes anything either.
	was := read(doc)
	if _, err := doc.Remove(p); err == nil {
		t.Fatal("removing with no clock left was accepted")
	}
	if _, err := doc.Merge(deep); err == nil {
		t.Fatal("merging with no clock left was accepted")
	}
	if got := read(doc); got != was {
		t.Fatalf("a refused removal changed the document to\n%swas\n%s", got, was)
	}

	// A new block's marker is written and its type cannot be, so the block is
	// there and untyped. It is said rather than hidden, and SetType puts it
	// right once there is clock again.
	before := doc.Len()
	if _, _, err := doc.Insert(p, "quote"); err == nil {
		t.Fatal("opening a typed block with no clock left was accepted")
	}
	if doc.Len() != before+1 {
		t.Fatalf("the refused block left %d blocks, want %d", doc.Len(), before+1)
	}
	// The same for a block that inherits a depth it cannot write.
	if _, _, err := doc.Insert(deep, ""); err == nil {
		t.Fatal("opening a nested block with no clock left was accepted")
	}

	// Then the text, after which nothing can be written at all.
	topText := crdt.Op{Kind: crdt.OpInsert, ID: crdt.ID{Site: 9, Seq: 1},
		Clock: crdt.MaxClock, Char: 'x'}
	if err := doc.Apply(crdt.PartOps{Part: textPart, Text: []crdt.Op{topText}}); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.InsertText(p, 0, "x"); err == nil {
		t.Fatal("typing with no clock left was accepted")
	}
	if _, err := doc.DeleteText(p, 0, 1); err == nil {
		t.Fatal("deleting with no clock left was accepted")
	}
	if _, _, err := doc.Insert(p, ""); err == nil {
		t.Fatal("opening a block with no clock left was accepted")
	}
	if _, _, err := doc.Split(p, 1, ""); err == nil {
		t.Fatal("splitting with no clock left was accepted")
	}
	if _, err := doc.Remove(p); err == nil {
		t.Fatal("removing with no clock left was accepted")
	}
	if _, err := doc.Merge(deep); err == nil {
		t.Fatal("merging with no clock left was accepted")
	}
}

// The other way round: the record can be forgotten and the text cannot. What
// is left is a block that has lost its type, which is a document somebody can
// read and go on editing — and it is what the order in close is chosen for.
func TestARemovalThatOnlyGetsHalfwayLosesTheTypeAndNotTheText(t *testing.T) {
	doc := NewBlocks(1)
	p := mustOpen(t, doc, DocStart, "quote")
	mustType(t, doc, p, 0, "words")

	topText := crdt.Op{Kind: crdt.OpInsert, ID: crdt.ID{Site: 9, Seq: 1},
		Clock: crdt.MaxClock, Char: 'x'}
	if err := doc.Apply(crdt.PartOps{Part: textPart, Text: []crdt.Op{topText}}); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Remove(p); err == nil {
		t.Fatal("removing with no clock left in the text was accepted")
	}
	if got, want := read(doc), ": \"words\"\n"; got != want {
		t.Fatalf("what is left reads %q, want %q — the text, and no type", got, want)
	}
}

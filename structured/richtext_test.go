package structured

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/go-crdt/crdt"
)

// draw renders the formatting as a line under the text, which is the only
// readable way to say two replicas agree about it.
//
//	the quick fox
//	    bbbbb
func draw(r *RichText) string {
	text := r.Text()
	if text == "" {
		return ""
	}
	rows := map[string][]byte{}
	for pos := range []rune(text) {
		for name := range r.MarksAt(pos) {
			if rows[name] == nil {
				rows[name] = []byte(strings.Repeat(" ", len([]rune(text))))
			}
			rows[name][pos] = name[0]
		}
	}
	out := text
	for _, name := range sortedNames(rows) {
		out += "\n" + string(rows[name])
	}
	return out
}

func sortedNames(m map[string][]byte) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	for i := range names {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	return names
}

func mustInsert(t *testing.T, r *RichText, pos int, s string) {
	t.Helper()
	if _, err := r.Insert(pos, s); err != nil {
		t.Fatal(err)
	}
}

func mustMark(t *testing.T, r *RichText, from, to int, name string, value []byte, expand Expand) {
	t.Helper()
	if _, err := r.Mark(from, to, name, value, expand); err != nil {
		t.Fatal(err)
	}
}

func syncRich(t *testing.T, a, b *RichText) {
	t.Helper()
	fromA := a.OpsSince(b.Version())
	fromB := b.OpsSince(a.Version())
	if err := b.Apply(fromA...); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(fromB...); err != nil {
		t.Fatal(err)
	}
}

func TestMarkingAStretch(t *testing.T) {
	r := NewRichText(1)
	mustInsert(t, r, 0, "the quick fox")
	mustMark(t, r, 4, 9, "bold", nil, ExpandEnd)

	if got, want := draw(r), "the quick fox\n    bbbbb    "; got != want {
		t.Fatalf("the text reads\n%s\nwant\n%s", got, want)
	}
	spans := r.Spans()
	if len(spans) != 3 {
		t.Fatalf("the text is %d spans, want 3: %#v", len(spans), spans)
	}
	if spans[1].Text != "quick" || spans[1].Marks["bold"] != nil {
		t.Fatalf("the middle span is %q with %v", spans[1].Text, spans[1].Marks)
	}
	if spans[0].Marks != nil || spans[2].Marks != nil {
		t.Fatal("the text either side of the mark carries it")
	}
}

// A mark carrying a value, which is what a colour or the target of a link is.
func TestAMarkCarriesAValue(t *testing.T) {
	r := NewRichText(1)
	mustInsert(t, r, 0, "see the paper")
	mustMark(t, r, 8, 13, "link", []byte("https://example.invalid"), ExpandNone)

	if got := string(r.MarksAt(8)["link"]); got != "https://example.invalid" {
		t.Fatalf("the link reads %q", got)
	}
	if _, ok := r.MarksAt(7)["link"]; ok {
		t.Fatal("the space before the link is part of it")
	}
}

// The reason an anchor has a side. Typing at the end of a bold word continues
// it; typing at the end of a link does not become part of the link.
func TestWhatGrowsAndWhatDoesNot(t *testing.T) {
	r := NewRichText(1)
	mustInsert(t, r, 0, "ab cd")
	mustMark(t, r, 0, 2, "bold", nil, ExpandEnd)
	mustMark(t, r, 3, 5, "link", []byte("x"), ExpandNone)

	// Type at the end of each.
	mustInsert(t, r, 2, "X") // "abX cd"
	mustInsert(t, r, 6, "Y") // "abX cdY"
	if _, ok := r.MarksAt(2)["bold"]; !ok {
		t.Fatalf("typing at the end of bold did not continue it:\n%s", draw(r))
	}
	if _, ok := r.MarksAt(6)["link"]; ok {
		t.Fatalf("typing at the end of a link joined it:\n%s", draw(r))
	}

	// And at the start of each, where neither grows.
	mustInsert(t, r, 0, "Z") // "ZabX cdY"
	if _, ok := r.MarksAt(0)["bold"]; ok {
		t.Fatalf("typing before bold joined it:\n%s", draw(r))
	}
}

func TestAMarkThatGrowsAtTheStart(t *testing.T) {
	r := NewRichText(1)
	mustInsert(t, r, 0, "abc")
	mustMark(t, r, 1, 3, "grow", nil, ExpandBoth)
	mustInsert(t, r, 1, "X") // between a and b, at the mark's leading edge
	if _, ok := r.MarksAt(1)["grow"]; !ok {
		t.Fatalf("a mark that grows at the start did not take what was typed there:\n%s", draw(r))
	}
	// And at the very start of the document, where there is no character before.
	mustMark(t, r, 0, 1, "head", nil, ExpandStart)
	mustInsert(t, r, 0, "Y")
	if _, ok := r.MarksAt(0)["head"]; !ok {
		t.Fatalf("a mark growing at the start of the document did not:\n%s", draw(r))
	}
}

func TestAMarkAtTheEndOfTheDocument(t *testing.T) {
	r := NewRichText(1)
	mustInsert(t, r, 0, "abc")
	mustMark(t, r, 1, 3, "grow", nil, ExpandEnd)
	mustMark(t, r, 1, 3, "stay", nil, ExpandNone)
	mustInsert(t, r, 3, "Z")
	if _, ok := r.MarksAt(3)["grow"]; !ok {
		t.Fatalf("a mark growing at the end of the document did not:\n%s", draw(r))
	}
	if _, ok := r.MarksAt(3)["stay"]; ok {
		t.Fatalf("a mark that does not grow took what was typed at the end:\n%s", draw(r))
	}
}

func TestUnmarking(t *testing.T) {
	r := NewRichText(1)
	mustInsert(t, r, 0, "abcdef")
	mustMark(t, r, 0, 6, "bold", nil, ExpandEnd)
	if _, err := r.Unmark(2, 4, "bold"); err != nil {
		t.Fatal(err)
	}
	if got, want := draw(r), "abcdef\nbb  bb"; got != want {
		t.Fatalf("the text reads\n%s\nwant\n%s", got, want)
	}
	// And a later mark can put it back, because nothing was discarded.
	mustMark(t, r, 2, 4, "bold", nil, ExpandEnd)
	if got, want := draw(r), "abcdef\nbbbbbb"; got != want {
		t.Fatalf("re-marking gave\n%s\nwant\n%s", got, want)
	}
}

// Two replicas bolding overlapping stretches. Written as markers in the text
// this is where the two come apart; as two marks read at the end it is one
// answer on both.
func TestOverlappingMarksFromTwoReplicas(t *testing.T) {
	a := NewRichText(1)
	mustInsert(t, a, 0, "abcdefgh")
	b, err := LoadRichText(2, a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	mustMark(t, a, 0, 5, "bold", nil, ExpandEnd)
	mustMark(t, b, 3, 8, "bold", nil, ExpandEnd)
	syncRich(t, a, b)

	if draw(a) != draw(b) {
		t.Fatalf("the replicas disagree:\n%s\nand\n%s", draw(a), draw(b))
	}
	if got, want := draw(a), "abcdefgh\nbbbbbbbb"; got != want {
		t.Fatalf("the text reads\n%s\nwant\n%s", got, want)
	}
}

// One replica bolds while the other takes bold away from part of it. The later
// of the two wins where they disagree, and both replicas read the same one.
func TestAMarkAndAnUnmarkThatDisagree(t *testing.T) {
	a := NewRichText(1)
	mustInsert(t, a, 0, "abcdef")
	mustMark(t, a, 0, 6, "bold", nil, ExpandEnd)
	b, err := LoadRichText(2, a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}

	// Concurrent: a re-bolds the whole thing, b unbolds the middle.
	mustMark(t, a, 0, 6, "bold", nil, ExpandEnd)
	if _, err := b.Unmark(2, 4, "bold"); err != nil {
		t.Fatal(err)
	}
	syncRich(t, a, b)

	if draw(a) != draw(b) {
		t.Fatalf("the replicas disagree:\n%s\nand\n%s", draw(a), draw(b))
	}
	// Whichever won, it is one answer and the ends are still bold.
	if _, ok := a.MarksAt(0)["bold"]; !ok {
		t.Fatalf("the start lost its mark:\n%s", draw(a))
	}
	if _, ok := a.MarksAt(5)["bold"]; !ok {
		t.Fatalf("the end lost its mark:\n%s", draw(a))
	}
}

// A mark whose text is deleted covers nothing, and does not reappear.
func TestDeletingTheMarkedText(t *testing.T) {
	r := NewRichText(1)
	mustInsert(t, r, 0, "keep BOLD keep")
	mustMark(t, r, 5, 9, "bold", nil, ExpandNone)
	if _, err := r.Delete(5, 4); err != nil {
		t.Fatal(err)
	}
	if got, want := r.Text(), "keep  keep"; got != want {
		t.Fatalf("the text reads %q, want %q", got, want)
	}
	for pos := range []rune(r.Text()) {
		if _, ok := r.MarksAt(pos)["bold"]; ok {
			t.Fatalf("the mark survived its text at %d:\n%s", pos, draw(r))
		}
	}
}

func TestWhatRichTextRefuses(t *testing.T) {
	r := NewRichText(1)
	mustInsert(t, r, 0, "abc")
	for _, c := range []struct {
		what     string
		from, to int
		name     string
	}{
		{"an empty name", 0, 1, ""},
		{"a negative start", -1, 1, "bold"},
		{"an end past the text", 0, 4, "bold"},
		{"an empty range", 1, 1, "bold"},
		{"a range the wrong way round", 2, 1, "bold"},
	} {
		if _, err := r.Mark(c.from, c.to, c.name, nil, ExpandNone); err == nil {
			t.Fatalf("marking with %s was accepted", c.what)
		}
		if _, err := r.Unmark(c.from, c.to, c.name); err == nil {
			t.Fatalf("unmarking with %s was accepted", c.what)
		}
	}
	if _, err := r.Insert(9, "x"); err == nil {
		t.Fatal("inserting past the end was accepted")
	}
	if _, err := r.Delete(0, 9); err == nil {
		t.Fatal("deleting past the end was accepted")
	}
	if r.MarksAt(-1) != nil || r.MarksAt(3) != nil {
		t.Fatal("a position outside the text has formatting")
	}
	if _, err := LoadRichText(1, []byte("not a snapshot")); err == nil {
		t.Fatal("loading rubbish was accepted")
	}
	if NewRichText(1).Spans() != nil {
		t.Fatal("an empty text has spans")
	}
}

// A peer can write anything into the marks map. Whatever it writes, the text
// still reads, and it reads the same on a second replica.
func TestRubbishInTheMarksMap(t *testing.T) {
	r := NewRichText(1)
	mustInsert(t, r, 0, "abcdef")
	mustMark(t, r, 0, 3, "bold", nil, ExpandEnd)

	good := encodeMark(mark{kind: markAdd, name: "x", start: anchor{kind: atStart}, end: anchor{kind: atEnd}})
	for _, rubbish := range [][]byte{
		{},                                     // nothing at all
		{markAdd},                              // no expand
		{9, 0, 1, 'x', 0, 0, 1},                // a kind that is neither add nor remove
		{markAdd, 0, 0xFF},                     // a name length that never ends
		{markAdd, 0, 0},                        // an empty name
		{markAdd, 0, 1, 'x'},                   // no value after the name
		{markAdd, 0, 1, 'x', 0},                // no anchors
		{markAdd, 0, 1, 'x', 0, 7},             // an anchor kind that is none of the four
		{markAdd, 0, 1, 'x', 0, beforeChar},    // an anchor with no identity
		{markAdd, 0, 1, 'x', 0, beforeChar, 1}, // an identity with no sequence
		{markAdd, 0, 1, 'x', 0, beforeChar, 1, 0}, // a sequence of zero, which no site issues
		append(append([]byte{}, good...), 'j'),    // something left over
		encodeMark(mark{kind: markAdd, name: "gone", // an identity from another document
			start: anchor{kind: beforeChar, id: crdt.ID{Site: 9, Seq: 9}},
			end:   anchor{kind: atEnd}}),
	} {
		if _, err := r.marks.Set(fmt.Sprint("hand-written-", len(rubbish)), rubbish); err != nil {
			t.Fatal(err)
		}
		if got, want := r.Text(), "abcdef"; got != want {
			t.Fatalf("the text reads %q", got)
		}
		if _, ok := r.MarksAt(0)["bold"]; !ok {
			t.Fatalf("%v cost the real mark", rubbish)
		}
		other, err := LoadRichText(2, r.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		if draw(other) != draw(r) {
			t.Fatalf("after %v two replicas read differently:\n%s\nand\n%s", rubbish, draw(r), draw(other))
		}
	}
}

// The whole point, checked the only way that settles it: replicas typing and
// marking without seeing each other, delivered in different orders, all reading
// the same text with the same formatting.
func TestRandomisedEditingAndMarkingConverges(t *testing.T) {
	names := []string{"bold", "italic", "link"}
	expands := []Expand{ExpandEnd, ExpandBoth, ExpandNone}

	for seed := range uint64(40) {
		t.Run(fmt.Sprint("seed ", seed), func(t *testing.T) {
			base := NewRichText(1)
			mustInsert(t, base, 0, "the quick brown fox jumps")
			snapshot := base.Snapshot()

			const replicas = 4
			docs := make([]*RichText, replicas)
			for i := range docs {
				doc, err := LoadRichText(crdt.SiteID(i+2), snapshot)
				if err != nil {
					t.Fatal(err)
				}
				docs[i] = doc
			}

			rng := rand.New(rand.NewPCG(seed, 3))
			pending := make([][]crdt.PartOps, replicas)
			for range 6 {
				for i, doc := range docs {
					var ops crdt.PartOps
					var err error
					switch rng.IntN(4) {
					case 0:
						ops, err = doc.Insert(rng.IntN(doc.Len()+1), string(rune('A'+rng.IntN(26))))
					case 1:
						if doc.Len() < 2 {
							continue
						}
						at := rng.IntN(doc.Len() - 1)
						ops, err = doc.Delete(at, 1)
					case 2:
						if doc.Len() < 2 {
							continue
						}
						from := rng.IntN(doc.Len() - 1)
						to := from + 1 + rng.IntN(doc.Len()-from-1)
						which := rng.IntN(len(names))
						ops, err = doc.Mark(from, to, names[which], nil, expands[which])
					default:
						if doc.Len() < 2 {
							continue
						}
						from := rng.IntN(doc.Len() - 1)
						to := from + 1 + rng.IntN(doc.Len()-from-1)
						ops, err = doc.Unmark(from, to, names[rng.IntN(len(names))])
					}
					if err != nil {
						continue
					}
					pending[i] = append(pending[i], ops)
				}
			}

			for i, doc := range docs {
				var inbox []crdt.PartOps
				for j, ops := range pending {
					if j != i {
						inbox = append(inbox, ops...)
					}
				}
				rng.Shuffle(len(inbox), func(a, b int) { inbox[a], inbox[b] = inbox[b], inbox[a] })
				if err := doc.Apply(inbox...); err != nil {
					t.Fatal(err)
				}
				if n := doc.Pending(); n != 0 {
					t.Fatalf("replica %d left %d operations parked", i, n)
				}
			}

			want := draw(docs[0])
			for i, doc := range docs[1:] {
				if got := draw(doc); got != want {
					t.Fatalf("replica %d reads\n%s\nreplica 0 reads\n%s", i+1, got, want)
				}
			}
			// The spans cover the text exactly once, in order.
			at := 0
			for _, span := range docs[0].Spans() {
				if span.Pos != at {
					t.Fatalf("a span starts at %d, want %d", span.Pos, at)
				}
				at += len([]rune(span.Text))
			}
			if at != docs[0].Len() {
				t.Fatalf("the spans cover %d characters of %d", at, docs[0].Len())
			}
		})
	}
}

func TestRichTextReportsWhatItIsBuiltOn(t *testing.T) {
	doc := crdt.NewComposite(3)
	r := RichTextOf(doc)
	mustInsert(t, r, 0, "abc")
	if r.Composite() != doc {
		t.Fatal("the rich text does not report the composite it is built on")
	}
	if r.Site() != 3 {
		t.Fatalf("it edits as site %d, want 3", r.Site())
	}
	if r.Doc() == nil || r.Doc().String() != "abc" {
		t.Fatal("the text part is not the one holding the characters")
	}
	// A second view of the same composite sees the same text, which is what
	// makes a rich text one part of a larger document rather than a document.
	if RichTextOf(doc).Text() != "abc" {
		t.Fatal("a second view does not see the text")
	}
}

// With no clock left in the marks part, marking refuses rather than writing an
// identity it cannot then use.
func TestMarkingWithNoClockLeft(t *testing.T) {
	r := NewRichText(1)
	mustInsert(t, r, 0, "abcdef")
	mustMark(t, r, 0, 3, "bold", nil, ExpandEnd)
	before := draw(r)

	top := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock,
		Key: "seed", Value: []byte("x")}
	if err := r.Apply(crdt.PartOps{Part: marksPart, Map: []crdt.MapOp{top}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Mark(3, 6, "bold", nil, ExpandEnd); err == nil {
		t.Fatal("marking with no clock left was accepted")
	}
	if _, err := r.Unmark(0, 3, "bold"); err == nil {
		t.Fatal("unmarking with no clock left was accepted")
	}
	if got := draw(r); got != before {
		t.Fatalf("a refused mark changed the formatting:\n%s\nwas\n%s", got, before)
	}
}

// One clock tick left: the identity is minted and the mark itself is not, so
// nothing is formatted by half.
func TestMarkingWithOneClockTickLeft(t *testing.T) {
	r := NewRichText(1)
	mustInsert(t, r, 0, "abcdef")
	top := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock - 1,
		Key: "seed", Value: []byte("x")}
	if err := r.Apply(crdt.PartOps{Part: marksPart, Map: []crdt.MapOp{top}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Mark(0, 3, "bold", nil, ExpandEnd); err == nil {
		t.Fatal("a mark that could not be written was reported as made")
	}
	for pos := range 6 {
		if r.MarksAt(pos) != nil {
			t.Fatalf("the text is formatted at %d after a mark that failed", pos)
		}
	}
}

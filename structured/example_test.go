package structured_test

import (
	"fmt"

	"github.com/go-crdt/crdt/structured"
)

// A spreadsheet and a diagram are two thin wrappers over one collaborative core.
// Here two replicas of a sheet edit two different cells while disconnected, then
// exchange operations and converge — the same merge a Diagram would use.
func ExampleSheet() {
	ada, grace := structured.NewSheet(1), structured.NewSheet(2)

	// Ada lays out a row and a column and shares them.
	row, r1, _ := ada.AppendRow()
	col, r2, _ := ada.AppendCol()
	grace.Apply(r1, r2)

	// Offline, each writes a different cell.
	fromAda, _ := ada.SetCell(row, col, structured.Literal("42"))
	other, _, _ := grace.AppendRow()
	fromGrace, _ := grace.SetCell(other, col, structured.Literal("7"))

	// They swap operations, in either order, and agree.
	ada.Apply(fromGrace)
	grace.Apply(fromAda)

	cell, _ := grace.GetCell(row, col)
	fmt.Println(cell.Text, len(grace.Rows()))
	// Output: 42 2
}

// A Document is the five-family isometric core: nodes, connectors, zones, text
// and layers under one composite. Here two replicas, disconnected, each create
// the entity the user dropped under the same caller-chosen id and edit different
// fields of it; they exchange operations and converge on one entity carrying
// both edits.
func ExampleDocument() {
	ada, grace := structured.NewDocument(1), structured.NewDocument(2)

	// Both name the same node — a caller-chosen id, so the two creations converge.
	fromAda, _ := ada.Add(structured.Nodes, "server")
	fromGrace, _ := grace.Add(structured.Nodes, "server")

	// Offline, each writes a different field.
	adaX, _ := ada.Set(structured.Nodes, "server", "x", structured.EncodeInt(5))
	graceLabel, _ := grace.Set(structured.Nodes, "server", "label", []byte("web"))

	// They swap every operation, in either order, and agree.
	ada.Apply(fromGrace, graceLabel)
	grace.Apply(fromAda, adaX)

	x, _ := grace.Field(structured.Nodes, "server", "x")
	n, _ := structured.DecodeInt(x)
	label, _ := grace.Field(structured.Nodes, "server", "label")
	fmt.Println(grace.IDs(structured.Nodes), n, string(label))
	// Output: [server] 5 web
}

// A Blocks is a document made of blocks — headings, paragraphs, list items —
// however many of them, in three parts. Here two replicas edit the same seam at
// the same moment: one is finishing a paragraph and the other is starting the
// next, which are the same offset and are not the same place. Both edits land
// where they were meant, with nothing arbitrated.
func ExampleBlocks() {
	ada, grace := structured.NewBlocks(1), structured.NewBlocks(2)

	// Ada writes a heading and a paragraph, and shares them.
	title, first, _ := ada.Insert(structured.DocStart, "heading")
	second, _ := ada.InsertText(title, 0, "Rivers")
	body, third, _ := ada.Insert(title, "paragraph")
	fourth, _ := ada.InsertText(body, 0, "They run downhill")
	grace.Apply(append(append(first, second), append(third, fourth)...)...)

	// Offline: Ada finishes the heading, Grace starts the paragraph.
	fromAda, _ := ada.InsertText(title, 6, " and seas")
	fromGrace, _ := grace.InsertText(body, 0, "Mostly, ")

	ada.Apply(fromGrace)
	grace.Apply(fromAda)

	for _, block := range grace.List() {
		fmt.Printf("%s: %s\n", block.Type, block.Text)
	}
	fmt.Println(len(grace.Version()), "parts")
	// Output:
	// heading: Rivers and seas
	// paragraph: Mostly, They run downhill
	// 2 parts
}

// A MultiRegister keeps a disagreement instead of settling it. Here two
// replicas rename the same thing while disconnected: an ordinary register would
// throw one name away with nothing left saying it existed, and this one hands
// both back. Choosing between them is writing the one chosen.
func ExampleMultiRegister() {
	ada, grace := structured.NewMultiRegister(1), structured.NewMultiRegister(2)

	// Offline, each names the file.
	fromAda, _ := ada.Set([]byte("notes.txt"))
	fromGrace, _ := grace.Set([]byte("readme.txt"))

	ada.Apply(fromGrace)
	grace.Apply(fromAda)

	fmt.Println(ada.Conflicted())
	for _, name := range ada.Values() {
		fmt.Printf("%s\n", name)
	}

	// Ada picks one. That write saw both, so it supersedes both.
	chosen, _ := ada.Set([]byte("readme.txt"))
	grace.Apply(chosen)
	name, only := grace.Value()
	fmt.Printf("%s %v\n", name, only)
	// Output:
	// true
	// notes.txt
	// readme.txt
	// readme.txt true
}

// A Set is a collection of names, where a removal takes away what it saw and
// nothing else. Here two replicas edit the labels on a card at the same moment:
// Grace clears the labels she has been shown while Ada adds one Grace has never
// seen, and the new label survives — which a map keyed by the label would have
// settled by an order that has nothing to do with what either knew.
func ExampleSet() {
	ada, grace := structured.NewSet(1), structured.NewSet(2)

	shared, _ := ada.Add("draft")
	grace.Apply(shared...)

	// Offline: Ada labels it urgent, Grace clears what she can see.
	added, _ := ada.Add("urgent")
	removed, _ := grace.Remove("draft")

	ada.Apply(removed...)
	grace.Apply(added...)

	fmt.Println(grace.Names())
	fmt.Println(grace.Adders("urgent"))
	// Output:
	// [urgent]
	// [1]
}

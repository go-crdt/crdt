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

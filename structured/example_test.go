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

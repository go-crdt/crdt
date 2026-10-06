package structured

import (
	"testing"

	"github.com/go-crdt/crdt"
)

// [Blocks.SetType] documents that "the empty string takes the type off", and
// takes it off by DELETING the field rather than by writing an empty value
// into it. Nothing tested that: removing the branch leaves the whole suite
// green, because a block with an empty type and a block with no type read the
// same through [Blocks.Block].
//
// They do not merge the same. A delete and a concurrent SetType("heading") are
// a removal against a write; an empty SET and the same concurrent write are two
// writes, which the map resolves by its own rule. The visible outcome of
// clearing a type while somebody else sets one therefore depends on which of
// the two this emits, and that is what this pins.
func TestTheEmptyTypeTakesTheTypeOffRatherThanWritingAnEmptyOne(t *testing.T) {
	b := NewBlocks(1)
	id, _, err := b.Insert(BlockID{}, "para")
	if err != nil {
		t.Fatal(err)
	}

	// The control: an ordinary type is a write, so this test is about which
	// operation is emitted and not about every operation being a delete.
	set, err := b.SetType(id, "heading")
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Map) != 1 || set.Map[0].Kind != crdt.MapSet {
		t.Fatalf("SetType(%q) emitted %d op(s), first kind %v, want one MapSet", "heading", len(set.Map), kindOf(set))
	}

	cleared, err := b.SetType(id, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.Map) != 1 || cleared.Map[0].Kind != crdt.MapDelete {
		t.Fatalf(`SetType("") emitted %d op(s), first kind %v, want one MapDelete: `+
			"an empty value written into the field would read the same here and "+
			"merge differently against a concurrent write", len(cleared.Map), kindOf(cleared))
	}
	if blk, ok := b.Block(id); !ok || blk.Type != "" {
		t.Fatalf("after clearing, Block.Type = %q (ok=%v)", blk.Type, ok)
	}
}

func kindOf(op crdt.PartOps) any {
	if len(op.Map) == 0 {
		return "none"
	}
	return op.Map[0].Kind
}

package crdt

import (
	"testing"
)

// What a call integrates is not what it was given, and this is the difference
// that matters: an operation parked earlier is released later, and it was in no
// batch anybody sent.
func TestApplyAbsorbedReportsWhatWasReleased(t *testing.T) {
	// Two operations from a replica of its own, where the second cannot be
	// applied until the first has been.
	mine := NewComposite(3)
	text, err := mine.Text("body")
	if err != nil {
		t.Fatal(err)
	}
	first, err := text.Insert(0, "a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := text.Insert(1, "b")
	if err != nil {
		t.Fatal(err)
	}
	part := Part{Kind: PartText, Name: "body"}

	theirs := NewComposite(4)
	// The second one arrives first and can only be parked.
	got, err := theirs.ApplyAbsorbed(PartOps{Part: part, Text: second})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a batch that could only be parked reported %d parts absorbed", len(got))
	}
	if theirs.Pending() != len(second) {
		t.Fatalf("%d operations are waiting, want %d", theirs.Pending(), len(second))
	}

	// Then the one it was waiting for. Both are absorbed, and the release has
	// to be in there — it is in no batch anybody sent.
	got, err = theirs.ApplyAbsorbed(PartOps{Part: part, Text: first})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("absorbed %d parts, want 1", len(got))
	}
	if want := len(first) + len(second); len(got[0].Text) != want {
		t.Fatalf("absorbed %d operations, want %d — the released ones are the point",
			len(got[0].Text), want)
	}
	if theirs.Pending() != 0 {
		t.Fatalf("%d operations are still waiting", theirs.Pending())
	}
	body, err := theirs.Text("body")
	if err != nil {
		t.Fatal(err)
	}
	if body.String() != "ab" {
		t.Fatalf("the document reads %q", body.String())
	}
}

// Nothing learned is nothing reported, which is what lets a relay between two
// replicas stop.
func TestApplyAbsorbedReportsNothingForWhatIsAlreadyHeld(t *testing.T) {
	mine := NewComposite(3)
	text, _ := mine.Text("body")
	ops, err := text.Insert(0, "hello")
	if err != nil {
		t.Fatal(err)
	}
	part := Part{Kind: PartText, Name: "body"}

	theirs := NewComposite(4)
	got, err := theirs.ApplyAbsorbed(PartOps{Part: part, Text: ops})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Text) != len(ops) {
		t.Fatalf("the first time absorbed %v", got)
	}
	// The same batch again teaches it nothing.
	got, err = theirs.ApplyAbsorbed(PartOps{Part: part, Text: ops})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("the second time absorbed %d parts, want none", len(got))
	}
}

// Lists and maps answer the same question, and a malformed batch is refused
// before anything is applied.
func TestApplyAbsorbedAcrossTheParts(t *testing.T) {
	mine := NewComposite(3)
	list, _ := mine.List("chat")
	listOps, err := list.Insert(0, []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	cells, _ := mine.Map("cells")
	mapOp, err := cells.Set("a", []byte("1"))
	if err != nil {
		t.Fatal(err)
	}
	mapOps := []MapOp{mapOp}

	theirs := NewComposite(4)
	got, err := theirs.ApplyAbsorbed(
		PartOps{Part: Part{Kind: PartList, Name: "chat"}, List: listOps},
		PartOps{Part: Part{Kind: PartMap, Name: "cells"}, Map: mapOps},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("absorbed %d parts, want 2", len(got))
	}
	if len(got[0].List) != len(listOps) || len(got[1].Map) != len(mapOps) {
		t.Fatalf("absorbed %v", got)
	}
	// Again, and it learns nothing.
	got, err = theirs.ApplyAbsorbed(
		PartOps{Part: Part{Kind: PartList, Name: "chat"}, List: listOps},
		PartOps{Part: Part{Kind: PartMap, Name: "cells"}, Map: mapOps},
	)
	if err != nil || len(got) != 0 {
		t.Fatalf("the second time absorbed %v, %v", got, err)
	}
	// And a batch that is not a batch is refused whole.
	if _, err := theirs.ApplyAbsorbed(PartOps{Part: Part{Kind: PartText, Name: ""}}); err == nil {
		t.Fatal("a malformed batch was accepted")
	}
}

// The three parts answer directly too, which is what a caller holding one of
// them uses.
func TestApplyAbsorbedOnEachPart(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		mine := New(3)
		ops, err := mine.Insert(0, "hi")
		if err != nil {
			t.Fatal(err)
		}
		theirs := New(4)
		got, err := theirs.ApplyAbsorbed(ops...)
		if err != nil || len(got) != len(ops) {
			t.Fatalf("absorbed %d of %d, %v", len(got), len(ops), err)
		}
		if again, _ := theirs.ApplyAbsorbed(ops...); len(again) != 0 {
			t.Fatalf("absorbed %d the second time", len(again))
		}
		if _, err := theirs.ApplyAbsorbed(Op{}); err == nil {
			t.Fatal("a malformed operation was accepted")
		}
	})
	t.Run("list", func(t *testing.T) {
		mine := NewList(3)
		ops, err := mine.Insert(0, []byte("x"))
		if err != nil {
			t.Fatal(err)
		}
		theirs := NewList(4)
		got, err := theirs.ApplyAbsorbed(ops...)
		if err != nil || len(got) != len(ops) {
			t.Fatalf("absorbed %d of %d, %v", len(got), len(ops), err)
		}
		if again, _ := theirs.ApplyAbsorbed(ops...); len(again) != 0 {
			t.Fatalf("absorbed %d the second time", len(again))
		}
		if _, err := theirs.ApplyAbsorbed(ListOp{}); err == nil {
			t.Fatal("a malformed operation was accepted")
		}
	})
	t.Run("map", func(t *testing.T) {
		mine := NewMap(3)
		op, err := mine.Set("k", []byte("v"))
		if err != nil {
			t.Fatal(err)
		}
		ops := []MapOp{op}
		theirs := NewMap(4)
		got, err := theirs.ApplyAbsorbed(ops...)
		if err != nil || len(got) != len(ops) {
			t.Fatalf("absorbed %d of %d, %v", len(got), len(ops), err)
		}
		if again, _ := theirs.ApplyAbsorbed(ops...); len(again) != 0 {
			t.Fatalf("absorbed %d the second time", len(again))
		}
		if _, err := theirs.ApplyAbsorbed(MapOp{}); err == nil {
			t.Fatal("a malformed operation was accepted")
		}
	})
}

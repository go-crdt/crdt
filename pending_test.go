package crdt

import (
	"runtime"
	"testing"
)

// What a peer can make a replica hold, and how it gives it back.
//
// An operation arriving before the one it depends on is parked, which is right.
// Nothing bounds the pile, which is not — so DropPending is the lever, and what
// follows is the argument that using it is safe, checked rather than believed.

// never returns an operation that can never apply: it waits on a sequence
// number its site never issues.
func never(seq uint64) Op {
	return Op{
		Kind:   OpInsert,
		ID:     ID{Site: 9, Seq: seq + 1},
		Origin: ID{Site: 9, Seq: seq},
		Clock:  seq + 1,
		Char:   'x',
	}
}

func TestWhatAPeerCanMakeAReplicaHold(t *testing.T) {
	const n = 100000
	d := New(1)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := range n {
		if err := d.Apply(never(uint64(i + 1))); err != nil {
			t.Fatalf("operation %d refused: %v", i, err)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("%d operations that can never apply: heap +%d KiB, %d bytes each",
		n, grew/1024, grew/n)

	if d.Pending() != n {
		t.Fatalf("Pending = %d, want %d", d.Pending(), n)
	}
	if d.Len() != 0 {
		t.Fatalf("the document holds %d characters; none of these applied", d.Len())
	}
	if v := d.Version(); len(v) != 0 {
		t.Fatalf("the version promises %v; a parked operation is not held", v)
	}

	if dropped := d.DropPending(); dropped != n {
		t.Fatalf("DropPending returned %d, want %d", dropped, n)
	}
	if d.Pending() != 0 {
		t.Fatalf("%d still pending after dropping", d.Pending())
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if now := int64(after.HeapAlloc) - int64(before.HeapAlloc); now > grew/2 {
		t.Fatalf("dropping %d operations gave back %d KiB of %d KiB",
			n, (grew-now)/1024, grew/1024)
	}
}

// Dropping loses nothing, because a parked operation is not in the version
// vector: a peer asked what this replica is missing sends it again.
func TestDroppingWhatIsParkedLosesNothing(t *testing.T) {
	ada := New(1)
	if _, err := ada.Insert(0, "hello world"); err != nil {
		t.Fatal(err)
	}
	ops := ada.OpsSince(nil)

	// Grace receives them back to front, so every one but the last parks.
	grace := New(2)
	for i := len(ops) - 1; i > 0; i-- {
		if err := grace.Apply(ops[i]); err != nil {
			t.Fatal(err)
		}
	}
	if grace.Pending() == 0 {
		t.Fatal("nothing parked; this fixture does not test what it says")
	}
	if grace.String() != "" {
		t.Fatalf("grace reads %q before anything applied", grace.String())
	}

	dropped := grace.DropPending()
	if dropped != len(ops)-1 {
		t.Fatalf("dropped %d of %d", dropped, len(ops)-1)
	}

	// Ada owes her all of them, the dropped ones included, because none was
	// ever in grace's version.
	owed := ada.OpsSince(grace.Version())
	if len(owed) != len(ops) {
		t.Fatalf("ada owes %d operations of %d", len(owed), len(ops))
	}
	if err := grace.Apply(owed...); err != nil {
		t.Fatal(err)
	}
	if grace.String() != ada.String() {
		t.Fatalf("after re-syncing grace reads %q, want %q", grace.String(), ada.String())
	}
	if grace.Pending() != 0 {
		t.Fatalf("%d parked after a complete sync", grace.Pending())
	}
}

// Dropping leaves what was already applied exactly as it was.
func TestDroppingKeepsWhatWasApplied(t *testing.T) {
	d := New(1)
	if _, err := d.Insert(0, "kept"); err != nil {
		t.Fatal(err)
	}
	before, version := d.String(), d.Version()
	for i := range 5 {
		if err := d.Apply(never(uint64(i + 1))); err != nil {
			t.Fatal(err)
		}
	}
	if d.DropPending() != 5 {
		t.Fatal("the five that can never apply were not dropped")
	}
	if d.String() != before {
		t.Fatalf("the text reads %q, want %q", d.String(), before)
	}
	if !d.Version().Equal(version) {
		t.Fatalf("the version changed to %v from %v", d.Version(), version)
	}
	if d.DropPending() != 0 {
		t.Fatal("dropping twice found something the second time")
	}
	// And the document still takes operations afterwards.
	if _, err := d.Insert(4, " on"); err != nil {
		t.Fatal(err)
	}
	if d.String() != "kept on" {
		t.Fatalf("after dropping, the document reads %q", d.String())
	}
}

// The same lever on the other three structures.
func TestDropPendingOnEveryStructure(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		l := NewList(1)
		op := ListOp{Kind: OpInsert, ID: ID{Site: 9, Seq: 2},
			Origin: ID{Site: 9, Seq: 1}, Clock: 2, Value: []byte("x")}
		if err := l.Apply(op); err != nil {
			t.Fatal(err)
		}
		if l.Pending() != 1 || l.DropPending() != 1 || l.Pending() != 0 {
			t.Fatalf("list: pending did not drop")
		}
		if l.Len() != 0 {
			t.Fatalf("the list holds %d values", l.Len())
		}
	})

	t.Run("map", func(t *testing.T) {
		m := NewMap(1)
		op := MapOp{Kind: MapSet, ID: ID{Site: 9, Seq: 2}, Clock: 2,
			Key: "k", Value: []byte("v")}
		if err := m.Apply(op); err != nil {
			t.Fatal(err)
		}
		if m.Pending() != 1 || m.DropPending() != 1 || m.Pending() != 0 {
			t.Fatalf("map: pending did not drop")
		}
	})

	t.Run("composite", func(t *testing.T) {
		c := NewComposite(1)
		text := Part{Kind: PartText, Name: "t"}
		list := Part{Kind: PartList, Name: "l"}
		mp := Part{Kind: PartMap, Name: "m"}
		batches := []PartOps{
			{Part: text, Text: []Op{never(1)}},
			{Part: list, List: []ListOp{{Kind: OpInsert, ID: ID{Site: 9, Seq: 2},
				Origin: ID{Site: 9, Seq: 1}, Clock: 2, Value: []byte("x")}}},
			{Part: mp, Map: []MapOp{{Kind: MapSet, ID: ID{Site: 9, Seq: 2},
				Clock: 2, Key: "k", Value: []byte("v")}}},
		}
		if err := c.Apply(batches...); err != nil {
			t.Fatal(err)
		}
		if c.Pending() != 3 {
			t.Fatalf("composite: Pending = %d, want 3", c.Pending())
		}
		if dropped := c.DropPending(); dropped != 3 {
			t.Fatalf("composite: dropped %d, want 3", dropped)
		}
		if c.Pending() != 0 {
			t.Fatalf("composite: %d still pending", c.Pending())
		}
	})
}

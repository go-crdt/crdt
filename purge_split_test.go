package crdt

import "testing"

// A concurrent insertion whose origin is a character in the INTERIOR of a
// purged run must land where a replica that never purged puts it. Purge keeps a
// run's identity and length but drops its characters; the placement walk used
// to measure a run by len(text), zero for a purged run, so it stepped over the
// whole run — carrying the insertion PAST a later, higher-clocked character it
// should sort before. Same operations, two texts, for ever.
//
// The shape needs a visible character after the run so the misplacement shows:
// with only the inserted character visible, where it sorts against an invisible
// purged run cannot be observed.
func TestAnInsertionInsideAPurgedRunConverges(t *testing.T) {
	ins := func(d *Doc, at int, s string) []Op {
		t.Helper()
		ops, err := d.Insert(at, s)
		if err != nil {
			t.Fatal(err)
		}
		return ops
	}
	apply := func(d *Doc, ops []Op) {
		t.Helper()
		if err := d.Apply(ops...); err != nil {
			t.Fatal(err)
		}
	}

	writer := New(1)
	ins(writer, 0, "abcd") // 1@1..1@4, clocks 1..4

	// Site 3 sees abcd, has its clock lifted by an unrelated edit, then types
	// 'e' after 'd' with a clock (6) above X's.
	site3 := New(3)
	apply(site3, writer.OpsSince(nil))
	bump := New(5)
	apply(bump, writer.OpsSince(nil))
	q := ins(bump, 0, "q") // clock 5
	apply(site3, q)
	e := ins(site3, 5, "e") // after 'd', clock 6

	// Site 2, concurrently and without q or e, types X after 'c' — origin is
	// the interior character 1@3 — with clock 5, below e's.
	site2 := New(2)
	apply(site2, writer.OpsSince(nil))
	x := ins(site2, 3, "X")

	// Everyone but site 2 sees q and e; then the writer deletes abcd and purges.
	apply(writer, q)
	apply(writer, e)
	del, err := writer.Delete(1, 4) // "qabcde" -> delete abcd
	if err != nil {
		t.Fatal(err)
	}
	control := New(4)
	apply(control, writer.OpsSince(nil))
	_ = del
	if writer.Purge() == 0 {
		t.Fatal("nothing was purged; the scenario is not the one described")
	}
	if writer.String() != "qe" || control.String() != "qe" {
		t.Fatalf("setup: purged %q control %q, want %q", writer.String(), control.String(), "qe")
	}

	// X arrives at the purged writer and at the unpurged control.
	apply(writer, x)
	apply(control, x)
	if writer.Pending() != 0 || control.Pending() != 0 {
		t.Fatalf("pending: writer=%d control=%d", writer.Pending(), control.Pending())
	}
	if writer.String() != control.String() {
		t.Fatalf("divergence: purged replica %q, unpurged control %q, same operations", writer.String(), control.String())
	}
	if writer.String() != "qXe" {
		t.Fatalf("both read %q, want %q (X keeps its place between c and e)", writer.String(), "qXe")
	}

	// A replica loaded from the purged snapshot must agree too, or the
	// divergence would be permanent.
	back, err := Load(7, writer.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if back.String() != control.String() {
		t.Fatalf("a replica reloaded from the purged snapshot reads %q, control %q", back.String(), control.String())
	}
	check(t, writer, "purged writer")
	check(t, control, "control")
	check(t, back, "reloaded")
}

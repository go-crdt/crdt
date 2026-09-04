package crdt

import (
	"errors"
	"testing"
)

// collectedPair is three map replicas that agree, have written and deleted
// keys, and have collected: the tombstones that carried the highest clocks are
// gone, which is exactly the state a snapshot understates.
func collectedPair(t *testing.T) (a, b *Map, floor uint64) {
	t.Helper()
	a, b = NewMap(1), NewMap(2)
	c := NewMap(3)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	set := func(m *Map, k, v string) MapOp {
		t.Helper()
		op, err := m.Set(k, []byte(v))
		must(err)
		return op
	}
	del := func(m *Map, k string) MapOp {
		t.Helper()
		op, err := m.Delete(k)
		must(err)
		return op
	}
	all := []*Map{a, b, c}
	tell := func(op MapOp) {
		for _, m := range all {
			must(m.Apply(op))
		}
	}
	tell(set(c, "c1", "1"))
	tell(set(a, "live", "1"))
	for i := range 4 {
		tell(set(a, "p", string(rune('a'+i))))
		tell(set(b, "q", string(rune('a'+i))))
	}
	for _, op := range []MapOp{del(a, "p"), del(b, "q"), del(c, "c1")} {
		tell(op)
	}
	floor = ^uint64(0)
	for _, m := range all {
		for _, site := range []SiteID{1, 2, 3} {
			clk, ok := m.LastClocks()[site]
			if !ok {
				t.Fatalf("replica %d never heard from site %d", m.Site(), site)
			}
			floor = min(floor, clk)
		}
	}
	stable := VersionVector{}
	for site, seq := range a.Version() {
		stable[site] = min(seq, b.Version()[site], c.Version()[site])
	}
	if a.Collect(stable, floor) == 0 || b.Collect(stable, floor) == 0 || c.Collect(stable, floor) == 0 {
		t.Fatal("nothing was collected, so this proves nothing")
	}
	if string(a.Snapshot()) != string(b.Snapshot()) {
		t.Fatal("after collecting the two replicas do not agree")
	}
	return a, b, floor
}

// A replica that restarts from a collected snapshot must not mint below the
// floor it was collected under. The tombstones that carried the highest clocks
// are exactly what Collect dropped, so the records alone understate the clock;
// collectedBelow is a clock the replica had certainly seen, and its next write
// has to clear it — or a peer refuses that write as a resurrection while the
// writer itself holds it.
func TestAMapReloadedAfterCollectMintsAboveTheFloor(t *testing.T) {
	a, b, floor := collectedPair(t)

	back, err := LoadMap(1, a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if back.Clock() < floor {
		t.Fatalf("reloaded clock %d is below the floor %d it was collected under", back.Clock(), floor)
	}
	fresh, err := back.Set("fresh", []byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Clock <= floor {
		t.Fatalf("the reloaded replica minted clock %d against a floor of %d", fresh.Clock, floor)
	}
	peer, err := LoadMap(2, b.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Apply(fresh); err != nil {
		t.Fatalf("an honest write by a replica that restarted from a collected snapshot was refused: %v", err)
	}
	if _, held := peer.Get("fresh"); !held {
		t.Fatal("applied and yet not held")
	}
	// The same key on both sides, after a round trip through the snapshot: the
	// reload must not change what the replica would have written.
	if got, _ := back.Get("fresh"); string(got) != "v" {
		t.Fatalf("the writer holds %q", got)
	}
}

// ApplyAbsorbed is the relay path a server takes, and it must refuse what
// Apply refuses: a map write below the collect floor is a resurrection, and a
// relay that swallowed it would apply half the batch and tell nobody.
func TestApplyAbsorbedReturnsErrStranded(t *testing.T) {
	a, _, floor := collectedPair(t)
	c := NewComposite(7)
	if _, err := c.Map("m"); err != nil {
		t.Fatal(err)
	}
	// Give the composite's map part the collected history of a, by loading a
	// composite whose map part is a's snapshot.
	part := c.mapPart("m")
	loaded, err := LoadMap(7, a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	*part = *loaded

	// A write from a site nobody has heard of, stamped below the floor, on a
	// key that was collected: the one thing a peer must refuse.
	stranded := MapOp{Kind: MapSet, ID: ID{Site: 99, Seq: 1}, Clock: floor, Key: "p", Value: []byte("back from the dead")}
	batch := PartOps{Part: Part{Kind: PartMap, Name: "m"}, Map: []MapOp{stranded}}

	if err := c.Apply(batch); !errors.Is(err, ErrStranded) {
		t.Fatalf("Apply: %v, want ErrStranded (the control: Apply has always refused this)", err)
	}
	_, err = c.ApplyAbsorbed(batch)
	if !errors.Is(err, ErrStranded) {
		t.Fatalf("ApplyAbsorbed: %v, want ErrStranded — a relay must not swallow what a client is told", err)
	}
	if _, held := part.Get("p"); held {
		t.Fatal("refused and yet held")
	}
}

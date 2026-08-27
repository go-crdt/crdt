package crdt

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
)

// A map gives back its tombstones and nothing else, because a tombstone is all
// it keeps that the live keys do not need.
func TestCollectAMapDropsItsTombstones(t *testing.T) {
	m := NewMap(1)
	for i := 0; i < 400; i++ {
		if _, err := m.Set(fmt.Sprintf("key-%d", i), []byte("value")); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 400; i += 2 {
		if _, err := m.Delete(fmt.Sprintf("key-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	before, live := len(m.Snapshot()), m.Len()
	if m.Tombstones() != 200 {
		t.Fatalf("%d tombstones, want 200", m.Tombstones())
	}
	n := m.Collect(m.Version())
	if n != 200 {
		t.Fatalf("collected %d tombstones, want 200", n)
	}
	if m.Len() != live {
		t.Fatalf("collecting changed the live keys: %d, want %d", m.Len(), live)
	}
	if m.Tombstones() != 0 {
		t.Fatalf("%d tombstones left", m.Tombstones())
	}
	after := len(m.Snapshot())
	if after >= before {
		t.Fatalf("the snapshot did not shrink: %d bytes became %d", before, after)
	}
	t.Logf("%d bytes became %d (%.2fx)", before, after, float64(before)/float64(after))

	// It reloads, and the clock it collected under survives with it.
	back, err := LoadMap(2, m.Snapshot())
	if err != nil {
		t.Fatalf("a collected map did not reload: %v", err)
	}
	if back.Len() != live {
		t.Fatalf("the reloaded map holds %d keys, want %d", back.Len(), live)
	}
	if back.CollectedBelow() != m.CollectedBelow() {
		t.Fatalf("the reloaded map collected below %d, want %d",
			back.CollectedBelow(), m.CollectedBelow())
	}
	if m.CollectedBelow() == 0 {
		t.Fatal("a map that collected 200 tombstones remembers no clock")
	}
}

// A map that has never collected is unchanged by asking, and a version that has
// seen nothing collects nothing.
func TestCollectingAMapWithNothingToTake(t *testing.T) {
	m := NewMap(1)
	for i := 0; i < 20; i++ {
		if _, err := m.Set(fmt.Sprintf("k%d", i), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	before := len(m.Snapshot())
	if n := m.Collect(m.Version()); n != 0 {
		t.Fatalf("collected %d from a map nothing was deleted from", n)
	}
	if len(m.Snapshot()) != before || m.CollectedBelow() != 0 {
		t.Fatal("collecting nothing changed something")
	}
	if _, err := m.Delete("k0"); err != nil {
		t.Fatal(err)
	}
	if n := m.Collect(VersionVector{}); n != 0 {
		t.Fatalf("collected %d against a version that has seen nothing", n)
	}
}

// The claim: a replica that has collected and one that has not still agree,
// whatever they do next.
func TestCollectedAndUncollectedMapsStillAgree(t *testing.T) {
	for seed := uint64(1); seed <= 200; seed++ {
		r := rand.New(rand.NewPCG(seed, 13))
		a, b := NewMap(1), NewMap(2)
		for round := 0; round < 80; round++ {
			from, to := a, b
			if r.IntN(2) == 0 {
				from, to = b, a
			}
			key := fmt.Sprintf("k%d", r.IntN(20))
			var op MapOp
			var err error
			if r.IntN(3) == 0 {
				op, err = from.Delete(key)
			} else {
				op, err = from.Set(key, []byte(fmt.Sprintf("v%d", r.IntN(100))))
			}
			if err != nil {
				continue // deleting a key that is not there is not an edit
			}
			if err := to.Apply(op); err != nil {
				t.Fatalf("seed %d: apply: %v", seed, err)
			}
		}
		if !sameKeys(a, b) {
			t.Fatalf("seed %d: the two disagreed before anything was collected", seed)
		}
		collected := a.Collect(a.Version())
		if !sameKeys(a, b) {
			t.Fatalf("seed %d: collecting %d tombstones changed the map", seed, collected)
		}

		// And they carry on.
		for round := 0; round < 30; round++ {
			key := fmt.Sprintf("k%d", r.IntN(20))
			op, err := a.Set(key, []byte("after"))
			if err != nil {
				t.Fatal(err)
			}
			if err := b.Apply(op); err != nil {
				t.Fatalf("seed %d: b refused a's work: %v", seed, err)
			}
			op, err = b.Set(fmt.Sprintf("k%d", r.IntN(20)), []byte("theirs"))
			if err != nil {
				t.Fatal(err)
			}
			if err := a.Apply(op); err != nil {
				t.Fatalf("seed %d: a refused b's work: %v", seed, err)
			}
		}
		if !sameKeys(a, b) {
			t.Fatalf("seed %d: after collecting %d tombstones the replicas disagree", seed, collected)
		}
	}
}

func sameKeys(a, b *Map) bool {
	ka, kb := a.Keys(), b.Keys()
	if len(ka) != len(kb) {
		return false
	}
	for i := range ka {
		if ka[i] != kb[i] {
			return false
		}
		va, _ := a.Get(ka[i])
		vb, _ := b.Get(kb[i])
		if string(va) != string(vb) {
			return false
		}
	}
	return true
}

// The guard: collecting against a version some replica has not delivered would
// let that replica's write bring a deleted key back, silently and on that
// replica alone. It is refused instead.
func TestAWriteThatWouldResurrectACollectedKeyIsRefused(t *testing.T) {
	a, b := NewMap(1), NewMap(2)
	first, err := a.Set("k", []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(first); err != nil {
		t.Fatal(err)
	}
	// b writes without having heard that a is about to delete the key.
	late, err := b.Set("k", []byte("two"))
	if err != nil {
		t.Fatal(err)
	}
	gone, err := a.Delete("k")
	if err != nil {
		t.Fatal(err)
	}
	_ = gone
	// The precondition broken deliberately: b has not delivered the deletion.
	if n := a.Collect(a.Version()); n != 1 {
		t.Fatalf("collected %d tombstones, want 1", n)
	}
	if err := a.Apply(late); !errors.Is(err, ErrStranded) {
		t.Fatalf("a write that would bring back a collected key = %v, want ErrStranded", err)
	}
	if _, held := a.Get("k"); held {
		t.Fatal("the key came back anyway")
	}
	// A replica that never collected takes the same write and resolves it the
	// ordinary way, which is what the guard is standing in for.
	fresh := NewMap(3)
	if err := fresh.Apply(first); err != nil {
		t.Fatal(err)
	}
	if err := fresh.Apply(late); err != nil {
		t.Fatalf("a replica that never collected refused the same write: %v", err)
	}
}

// A write above the collected clock is ordinary work and is not refused, even
// for a key this replica does not hold.
func TestAWriteAboveTheCollectedClockIsOrdinary(t *testing.T) {
	a, b := NewMap(1), NewMap(2)
	first, err := a.Set("k", []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(first); err != nil {
		t.Fatal(err)
	}
	gone, err := a.Delete("k")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(gone); err != nil {
		t.Fatal(err)
	}
	// Now b has delivered the deletion, so collecting is legitimate.
	if n := a.Collect(a.Version()); n != 1 {
		t.Fatalf("collected %d tombstones, want 1", n)
	}
	// And b's next write, made knowing the key was deleted, is ordinary work.
	next, err := b.Set("k", []byte("again"))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(next); err != nil {
		t.Fatalf("an ordinary write after a collection was refused: %v", err)
	}
	if v, held := a.Get("k"); !held || string(v) != "again" {
		t.Fatalf("the key reads %q, held=%v", v, held)
	}
	// A brand new key too.
	other, err := b.Set("fresh", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(other); err != nil {
		t.Fatalf("a new key was refused: %v", err)
	}
}

// A version 1 map snapshot still loads and comes back having collected nothing.
func TestLoadMapStillAcceptsVersionOne(t *testing.T) {
	raw := encodeMapSnapshot(uint64(1), uint64(1), uint64(1), uint64(1),
		"k", uint64(1), uint64(1), uint64(1), []byte{1}, uint64(1), []byte("v"))
	// Rebuild it at version 1: the same body without the collected clock.
	old := append([]byte{}, mapMagic[:]...)
	old = append(old, mapVersionV1)
	old = append(old, raw[len(mapMagic)+2:]...)
	m, err := LoadMap(2, old)
	if err != nil {
		t.Fatalf("a version 1 map snapshot did not load: %v", err)
	}
	if m.CollectedBelow() != 0 {
		t.Fatalf("a map loaded from version 1 came back having collected below %d", m.CollectedBelow())
	}
	if fresh := m.Snapshot(); fresh[len(mapMagic)] != mapVersion {
		t.Fatalf("re-encoding wrote version %d, want %d", fresh[len(mapMagic)], mapVersion)
	}
}

// A composite can say whether a peer can be caught up with a difference, and it
// is the text and list parts that decide: a map part never makes it false,
// because a map gives back a sequence number without its operation as a matter
// of course and the span that stands in for one collected tombstone is the same
// span that already stood in for an overwritten value.
func TestACompositeSaysWhetherAPeerCanBeCaughtUp(t *testing.T) {
	c := NewComposite(1)
	body, err := c.Text("body")
	if err != nil {
		t.Fatal(err)
	}
	notes, err := c.List("notes")
	if err != nil {
		t.Fatal(err)
	}
	props, err := c.Map("props")
	if err != nil {
		t.Fatal(err)
	}
	peer := NewComposite(2)
	exchange := func(batches []PartOps) {
		t.Helper()
		if err := peer.Apply(batches...); err != nil {
			t.Fatal(err)
		}
	}
	ops, err := body.Insert(0, "AAA")
	if err != nil {
		t.Fatal(err)
	}
	exchange([]PartOps{{Part: Part{Kind: PartText, Name: "body"}, Text: ops}})
	lops, err := notes.Insert(0, []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	exchange([]PartOps{{Part: Part{Kind: PartList, Name: "notes"}, List: lops}})
	mop, err := props.Set("k", []byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	exchange([]PartOps{{Part: Part{Kind: PartMap, Name: "props"}, Map: []MapOp{mop}}})

	behind := peer.Version()
	if !c.CanReplay(behind) {
		t.Fatal("a document that has collected nothing said a peer could not be caught up")
	}
	if c.collected() {
		t.Fatal("a document that has collected nothing says it has")
	}

	// A map that collects does not change the answer.
	if _, err := props.Delete("k"); err != nil {
		t.Fatal(err)
	}
	if n := props.Collect(props.Version()); n != 1 {
		t.Fatalf("collected %d map tombstones, want 1", n)
	}
	if !c.CanReplay(behind) {
		t.Fatal("collecting a map tombstone stopped a peer being caught up")
	}
	if !c.collected() {
		t.Fatal("a document whose map collected says it has not")
	}

	// A text that collects does. The peer writes the second run, so the first
	// one is a run of its own and can die whole — a site typing straight on
	// extends the run it is already in.
	peerBody, err := peer.Text("body")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := peerBody.Insert(peerBody.Len(), "BBB")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Apply(PartOps{Part: Part{Kind: PartText, Name: "body"}, Text: theirs}); err != nil {
		t.Fatal(err)
	}
	if _, err := body.Delete(0, 3); err != nil {
		t.Fatal(err)
	}
	if n := body.Collect(body.Version()); n != 3 {
		t.Fatalf("collected %d characters, want 3", n)
	}
	if c.CanReplay(behind) {
		t.Fatal("a document whose text collected still offers a peer a difference")
	}
	if !c.CanReplay(c.Version()) {
		t.Fatal("a document refuses to catch up a peer that is level with it")
	}
	if !c.collected() {
		t.Fatal("a document whose text collected says it has not")
	}

	// And a list that collects does too, on its own.
	fresh := NewComposite(3)
	items, err := fresh.List("items")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := items.Insert(0, []byte("a")); err != nil {
		t.Fatal(err)
	}
	early := fresh.Version()
	if _, err := items.Insert(items.Len(), []byte("b")); err != nil {
		t.Fatal(err)
	}
	if _, err := items.Delete(0, 1); err != nil {
		t.Fatal(err)
	}
	if n := items.Collect(items.Version()); n != 1 {
		t.Fatalf("collected %d elements, want 1", n)
	}
	if fresh.CanReplay(early) {
		t.Fatal("a document whose list collected still offers a peer a difference")
	}
	if !fresh.collected() {
		t.Fatal("a document whose list collected says it has not")
	}
}

package crdt

import (
	"bytes"
	"testing"
)

// A site never issues two operations sharing a Lamport timestamp: its clock
// advances at least once per operation. Nothing can check that of an operation
// it is handed, though, because the claim is about a site's whole history and an
// arriving operation is one of them — so a peer can send such a pair, and every
// replica has to survive it.
//
// It used not to. The comparison that places a character called the two equal,
// which made it not an order at all, and the two walks that use it were then
// free to disagree: integration put the pair one way and the scan Load
// re-derives put it the other, so the document could not reload its own
// snapshot. Persisting is what a server does when the last participant leaves,
// so a peer's bytes decided whether a document survived a restart.

// tiedInsertions returns three insertions from one site where the last two share
// a timestamp, which is the smallest history that used to break.
func tiedInsertions() []Op {
	return []Op{
		{Kind: OpInsert, ID: ID{Site: 1, Seq: 1}, Clock: 32, Char: 'a'},
		{Kind: OpInsert, ID: ID{Site: 1, Seq: 2}, Clock: 48, Origin: ID{Site: 1, Seq: 1}, Char: 'b'},
		{Kind: OpInsert, ID: ID{Site: 1, Seq: 3}, Clock: 48, Origin: ID{Site: 1, Seq: 1}, Char: 'c'},
	}
}

func TestADocumentCanReloadWhatATiedHistoryProduced(t *testing.T) {
	d := New(99)
	if err := d.Apply(tiedInsertions()...); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	text := d.String()
	snapshot := d.Snapshot()
	back, err := Load(1, snapshot)
	if err != nil {
		t.Fatalf("the document cannot reload its own snapshot: %v", err)
	}
	if got := back.String(); got != text {
		t.Fatalf("reloaded %q, want %q", got, text)
	}
	// And the reload is the same document, not merely the same text.
	if !bytes.Equal(back.Snapshot(), snapshot) {
		t.Fatal("the reloaded document does not encode to what it was loaded from")
	}
}

func TestAListCanReloadWhatATiedHistoryProduced(t *testing.T) {
	l := NewList(99)
	ops := []ListOp{
		{Kind: OpInsert, ID: ID{Site: 1, Seq: 1}, Clock: 32, Value: []byte("a")},
		{Kind: OpInsert, ID: ID{Site: 1, Seq: 2}, Clock: 48, Origin: ID{Site: 1, Seq: 1}, Value: []byte("b")},
		{Kind: OpInsert, ID: ID{Site: 1, Seq: 3}, Clock: 48, Origin: ID{Site: 1, Seq: 1}, Value: []byte("c")},
	}
	if err := l.Apply(ops...); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	snapshot := l.Snapshot()
	back, err := LoadList(1, snapshot)
	if err != nil {
		t.Fatalf("the list cannot reload its own snapshot: %v", err)
	}
	if !bytes.Equal(back.Snapshot(), snapshot) {
		t.Fatal("the reloaded list does not encode to what it was loaded from")
	}
}

// Convergence is the other half, and the half a reload test would not catch:
// every replica must place a tied pair the same way, whatever order the
// operations reach it in and however they are grouped.
func TestATiedHistoryConvergesInEveryOrder(t *testing.T) {
	ops := tiedInsertions()
	var want []byte
	permute(ops, func(p []Op) {
		d := New(99)
		for _, op := range p {
			if err := d.Apply(op); err != nil {
				t.Fatalf("Apply: %v", err)
			}
		}
		if d.Pending() != 0 {
			t.Fatalf("%d operations never became applicable", d.Pending())
		}
		got := d.Snapshot()
		if want == nil {
			want = got
			return
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("delivery order %v produced a different document; text %q",
				[]ID{p[0].ID, p[1].ID, p[2].ID}, d.String())
		}
	})

	lops := []ListOp{
		{Kind: OpInsert, ID: ID{Site: 1, Seq: 1}, Clock: 32, Value: []byte("a")},
		{Kind: OpInsert, ID: ID{Site: 1, Seq: 2}, Clock: 48, Origin: ID{Site: 1, Seq: 1}, Value: []byte("b")},
		{Kind: OpInsert, ID: ID{Site: 1, Seq: 3}, Clock: 48, Origin: ID{Site: 1, Seq: 1}, Value: []byte("c")},
	}
	var lwant []byte
	permute(lops, func(p []ListOp) {
		l := NewList(99)
		for _, op := range p {
			if err := l.Apply(op); err != nil {
				t.Fatalf("Apply: %v", err)
			}
		}
		if l.Pending() != 0 {
			t.Fatalf("%d list operations never became applicable", l.Pending())
		}
		got := l.Snapshot()
		if lwant == nil {
			lwant = got
			return
		}
		if !bytes.Equal(got, lwant) {
			t.Fatal("a delivery order produced a different list")
		}
	})
}

// The map never had this, because a site's operations integrate in sequence
// order whatever order they arrive in — so its ties already resolved the same
// way everywhere. What changes for it is which one wins: the later sequence
// number now does, where before an operation that tied simply lost. That is
// last-writer-wins meaning what it says, and it is still the same answer on
// every replica.
func TestAMapResolvesATiedPairByTheLaterOperation(t *testing.T) {
	first := MapOp{Kind: MapSet, ID: ID{Site: 1, Seq: 1}, Clock: 7, Key: "k", Value: []byte("first")}
	second := MapOp{Kind: MapSet, ID: ID{Site: 1, Seq: 2}, Clock: 7, Key: "k", Value: []byte("second")}

	forward, backward := NewMap(99), NewMap(99)
	if err := forward.Apply(first, second); err != nil {
		t.Fatal(err)
	}
	if err := backward.Apply(second, first); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(forward.Snapshot(), backward.Snapshot()) {
		t.Fatal("the two delivery orders disagree")
	}
	got, ok := forward.Get("k")
	if !ok || !bytes.Equal(got, []byte("second")) {
		t.Errorf("the key holds %q, want the later operation's %q", got, "second")
	}
}

// A tie can also fall inside a run rather than between two of them, which is
// the comparison the walk makes most and the one that does not build the
// character's identity unless it has to. Both ways of settling it have to be
// reached: a run one site typed, and an operation landing in the middle of it
// carrying the clock of the character already there.
func TestATieInsideARunIsSettledBothWays(t *testing.T) {
	// "ab" typed by site 1 as one run: clocks 5 and 6, sequence numbers 1 and 2.
	run := []Op{
		{Kind: OpInsert, ID: ID{Site: 1, Seq: 1}, Clock: 5, Char: 'a'},
		{Kind: OpInsert, ID: ID{Site: 1, Seq: 2}, Clock: 6, Origin: ID{Site: 1, Seq: 1}, Char: 'b'},
	}
	// Site 2 ties with 'b' on the clock, and the sites settle it. Among
	// characters sharing an origin the one sorting higher comes first, so site 2
	// lands ahead of site 1's 'b'.
	bySite := New(99)
	if err := bySite.Apply(append(append([]Op{}, run...),
		Op{Kind: OpInsert, ID: ID{Site: 2, Seq: 1}, Clock: 6, Origin: ID{Site: 1, Seq: 1}, Char: 'X'})...); err != nil {
		t.Fatal(err)
	}
	if got, want := bySite.String(), "aXb"; got != want {
		t.Fatalf("a tie settled by site gave %q, want %q", got, want)
	}

	// The same site ties with its own character, which only a peer's bytes can
	// produce: the sequence numbers settle it, by the same rule — 3 sorts above
	// 2, so it lands ahead of it.
	bySeq := New(99)
	if err := bySeq.Apply(append(append([]Op{}, run...),
		Op{Kind: OpInsert, ID: ID{Site: 1, Seq: 3}, Clock: 6, Origin: ID{Site: 1, Seq: 1}, Char: 'Y'})...); err != nil {
		t.Fatal(err)
	}
	if got, want := bySeq.String(), "aYb"; got != want {
		t.Fatalf("a tie settled by sequence number gave %q, want %q", got, want)
	}
	// And both survive the round trip that used to be what broke.
	for _, d := range []*Doc{bySite, bySeq} {
		snapshot := d.Snapshot()
		back, err := Load(1, snapshot)
		if err != nil {
			t.Fatalf("cannot reload %q: %v", d.String(), err)
		}
		if !bytes.Equal(back.Snapshot(), snapshot) {
			t.Fatalf("reloading %q did not give back the same document", d.String())
		}
	}
}

// Nothing an honest replica produces reaches the sequence number at all, so no
// existing document changes its ordering — which is why this needed no format
// change and no migration. A site's clock advances at least once per operation,
// so within one site the clocks are strictly increasing.
func TestAnHonestHistoryNeverTies(t *testing.T) {
	d := New(1)
	if _, err := d.Insert(0, "a paragraph of text, typed"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Delete(3, 5); err != nil {
		t.Fatal(err)
	}
	seen := map[uint64]ID{}
	for _, op := range d.OpsSince(nil) {
		if prev, dup := seen[op.Clock]; dup && prev.Site == op.ID.Site {
			t.Fatalf("site %d issued %v and %v with the same clock %d",
				op.ID.Site, prev, op.ID, op.Clock)
		}
		seen[op.Clock] = op.ID
	}
}

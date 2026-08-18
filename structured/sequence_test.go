package structured

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/go-crdt/crdt"
)

// readSeq is the sequence as a line, which is the only readable way to say two
// replicas agree.
func readSeq(s *Sequence) string {
	var parts []string
	for _, value := range s.Values() {
		parts = append(parts, string(value))
	}
	return strings.Join(parts, " ")
}

func add(tb testing.TB, s *Sequence, after ItemID, value string) ItemID {
	tb.Helper()
	item, _, err := s.Insert(after, []byte(value))
	if err != nil {
		tb.Fatal(err)
	}
	return item
}

func syncSeq(tb testing.TB, a, b *Sequence) {
	tb.Helper()
	fromA := a.OpsSince(b.Version())
	fromB := b.OpsSince(a.Version())
	if err := b.Apply(fromA...); err != nil {
		tb.Fatal(err)
	}
	if err := a.Apply(fromB...); err != nil {
		tb.Fatal(err)
	}
}

func TestASequenceIsBuiltAndRead(t *testing.T) {
	s := NewSequence(1)
	if s.Len() != 0 {
		t.Fatalf("an empty sequence has %d items", s.Len())
	}
	a := add(t, s, SeqStart, "a")
	b := add(t, s, a, "b")
	add(t, s, b, "c")
	// And one at the very front.
	add(t, s, SeqStart, "first")

	if got, want := readSeq(s), "first a b c"; got != want {
		t.Fatalf("the sequence reads %q, want %q", got, want)
	}
	if s.Len() != 4 {
		t.Fatalf("the sequence holds %d items, want 4", s.Len())
	}
	if item, ok := s.At(2); !ok || item != b {
		t.Fatal("At did not find the item at position 2")
	}
	if got := s.IndexOf(b); got != 2 {
		t.Fatalf("IndexOf gave %d, want 2", got)
	}
	if value, ok := s.Value(b); !ok || string(value) != "b" {
		t.Fatalf("the item holds %q", value)
	}
	if b.String() == "" || b.String() != crdt.ID(b).String() {
		t.Fatalf("an item prints as %q", b.String())
	}
}

// The thing a sequence is for. A list has no operation for this, and writing it
// as a delete and an insert is what a concurrent move splits.
func TestMovingAnItem(t *testing.T) {
	s := NewSequence(1)
	a := add(t, s, SeqStart, "a")
	b := add(t, s, a, "b")
	c := add(t, s, b, "c")

	if _, err := s.Move(c, SeqStart); err != nil {
		t.Fatal(err)
	}
	if got, want := readSeq(s), "c a b"; got != want {
		t.Fatalf("after moving c first the sequence reads %q, want %q", got, want)
	}
	if _, err := s.Move(a, b); err != nil {
		t.Fatal(err)
	}
	if got, want := readSeq(s), "c b a"; got != want {
		t.Fatalf("after moving a last the sequence reads %q, want %q", got, want)
	}
	// A move is one operation, which is the whole point.
	op, err := s.Move(b, SeqStart)
	if err != nil {
		t.Fatal(err)
	}
	if op.Kind != crdt.MapSet {
		t.Fatal("a move is not a single write")
	}
	if got, want := readSeq(s), "b c a"; got != want {
		t.Fatalf("the sequence reads %q, want %q", got, want)
	}
}

// Two replicas moving the same item at once. Written as a delete and an insert
// this leaves the item twice or not at all; as one field write it is a conflict
// the map already settles, and both replicas settle it the same way.
func TestTwoReplicasMoveTheSameItem(t *testing.T) {
	a := NewSequence(1)
	one := add(t, a, SeqStart, "one")
	two := add(t, a, one, "two")
	three := add(t, a, two, "three")

	b, err := LoadSequence(2, a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Move(one, three); err != nil { // one to the end
		t.Fatal(err)
	}
	if _, err := b.Move(one, two); err != nil { // one into the middle
		t.Fatal(err)
	}
	syncSeq(t, a, b)

	if readSeq(a) != readSeq(b) {
		t.Fatalf("the replicas disagree: %q and %q", readSeq(a), readSeq(b))
	}
	if a.Len() != 3 {
		t.Fatalf("the sequence holds %d items after the concurrent move, want 3: %q", a.Len(), readSeq(a))
	}
	seen := map[ItemID]bool{}
	for _, item := range a.Items() {
		if seen[item] {
			t.Fatal("an item is read twice")
		}
		seen[item] = true
	}
}

// Two replicas inserting at the same place mint the same rank, and the identity
// is what makes the result an order.
func TestConcurrentInsertsAtTheSamePlaceInASequence(t *testing.T) {
	a := NewSequence(1)
	anchor := add(t, a, SeqStart, "anchor")
	b, err := LoadSequence(2, a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	fromA := add(t, a, anchor, "fromA")
	fromB := add(t, b, anchor, "fromB")
	if a.rankOf(fromA) != b.rankOf(fromB) {
		t.Fatalf("the two inserts minted %q and %q; this test needs them equal",
			a.rankOf(fromA), b.rankOf(fromB))
	}
	syncSeq(t, a, b)

	if readSeq(a) != readSeq(b) {
		t.Fatalf("the replicas disagree: %q and %q", readSeq(a), readSeq(b))
	}
	if got, want := readSeq(a), "anchor fromA fromB"; got != want {
		t.Fatalf("the sequence reads %q, want %q", got, want)
	}
}

func TestSettingAndRemoving(t *testing.T) {
	s := NewSequence(1)
	a := add(t, s, SeqStart, "a")
	b := add(t, s, a, "b")

	if _, err := s.Set(a, []byte("A")); err != nil {
		t.Fatal(err)
	}
	if got, want := readSeq(s), "A b"; got != want {
		t.Fatalf("the sequence reads %q, want %q", got, want)
	}
	if _, err := s.Remove(a); err != nil {
		t.Fatal(err)
	}
	if got, want := readSeq(s), "b"; got != want {
		t.Fatalf("after removing the sequence reads %q, want %q", got, want)
	}
	if _, ok := s.Value(a); ok {
		t.Fatal("a removed item still holds a value")
	}
	if s.IndexOf(a) != -1 {
		t.Fatal("a removed item still has a position")
	}
	_ = b
}

func TestAnItemCarriesItsOwnFields(t *testing.T) {
	s := NewSequence(1)
	a := add(t, s, SeqStart, "slide")
	if _, err := s.SetField(a, "notes", []byte("say this")); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.GetField(a, "notes"); !ok || string(got) != "say this" {
		t.Fatalf("the field reads %q", got)
	}
	// And moving the item does not disturb it.
	if _, err := s.Move(a, SeqStart); err != nil && !errors.Is(err, ErrNoChange) {
		t.Fatal(err)
	}
	if got, _ := s.GetField(a, "notes"); string(got) != "say this" {
		t.Fatal("moving the item lost a field")
	}
}

func TestWhatASequenceRefuses(t *testing.T) {
	s := NewSequence(1)
	item := add(t, s, SeqStart, "a")
	gone := ItemID{Site: 9, Seq: 9}

	if _, _, err := s.Insert(gone, nil); err == nil {
		t.Fatal("inserting after an item that does not exist was accepted")
	}
	if _, err := s.Move(SeqStart, SeqStart); err == nil {
		t.Fatal("moving the start was accepted")
	}
	if _, err := s.Move(gone, SeqStart); err == nil {
		t.Fatal("moving an item that does not exist was accepted")
	}
	if _, err := s.Move(item, gone); err == nil {
		t.Fatal("moving after an item that does not exist was accepted")
	}
	if _, err := s.Move(item, item); !errors.Is(err, ErrNoChange) {
		t.Fatalf("moving an item after itself gave %v, want ErrNoChange", err)
	}
	if _, err := s.Set(SeqStart, nil); err == nil {
		t.Fatal("setting the start was accepted")
	}
	if _, err := s.Set(gone, nil); err == nil {
		t.Fatal("setting an item that does not exist was accepted")
	}
	if _, err := s.Remove(SeqStart); err == nil {
		t.Fatal("removing the start was accepted")
	}
	if _, err := s.Remove(gone); err == nil {
		t.Fatal("removing an item that does not exist was accepted")
	}
	if _, err := s.SetField(SeqStart, "f", nil); err == nil {
		t.Fatal("setting a field on the start was accepted")
	}
	for _, field := range []string{seqRankField, seqValueField} {
		if _, err := s.SetField(item, field, nil); err == nil {
			t.Fatalf("writing %q directly was accepted", field)
		}
	}
	if _, ok := s.At(-1); ok {
		t.Fatal("a negative position found an item")
	}
	if _, ok := s.At(99); ok {
		t.Fatal("a position past the end found an item")
	}
	if _, ok := s.Value(SeqStart); ok {
		t.Fatal("the start holds a value")
	}
}

// A peer given exactly the operations an edit returned has to be able to apply
// them, with nothing left parked waiting for one that was never handed over.
func TestASequenceReturnsEnoughOnItsOwn(t *testing.T) {
	a, b := NewSequence(1), NewSequence(2)
	var ops []crdt.MapOp

	first, made, err := a.Insert(SeqStart, []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	ops = append(ops, made...)
	second, made, err := a.Insert(first, []byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	ops = append(ops, made...)
	moved, err := a.Move(second, SeqStart)
	if err != nil {
		t.Fatal(err)
	}
	ops = append(ops, moved)

	if err := b.Apply(ops...); err != nil {
		t.Fatal(err)
	}
	if n := b.Map().Pending(); n != 0 {
		t.Fatalf("%d operations are parked waiting for one that was not returned", n)
	}
	if readSeq(b) != readSeq(a) {
		t.Fatalf("the peer reads %q, the writer %q", readSeq(b), readSeq(a))
	}
}

func TestASequenceSnapshotRoundTrips(t *testing.T) {
	a := NewSequence(1)
	x := add(t, a, SeqStart, "x")
	add(t, a, x, "y")

	b, err := LoadSequence(2, a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if readSeq(b) != readSeq(a) {
		t.Fatalf("the reloaded sequence reads %q, want %q", readSeq(b), readSeq(a))
	}
	if _, err := LoadSequence(1, []byte("not a snapshot")); err == nil {
		t.Fatal("loading rubbish was accepted")
	}
}

func TestSequenceOfReadsAMapInPlace(t *testing.T) {
	m := crdt.NewMap(1)
	s := SequenceOf(m)
	add(t, s, SeqStart, "a")
	if SequenceOf(m).Len() != 1 {
		t.Fatal("a second view of the same map does not see the item")
	}
	if s.Records() == nil || s.Map() != m {
		t.Fatal("the sequence does not report what it is built on")
	}
}

// A record whose name is not an identity is not an item, whatever a peer writes.
func TestARecordThatIsNotAnItem(t *testing.T) {
	s := NewSequence(1)
	add(t, s, SeqStart, "real")
	if _, err := s.Map().Set(fieldKey("not-an-identity", "x"), []byte("y")); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 1 {
		t.Fatalf("the sequence reads %d items, want 1", s.Len())
	}
}

// With no clock left nothing can be written, and every entry point says so.
func TestASequenceWithNoClockLeft(t *testing.T) {
	s := NewSequence(1)
	item := add(t, s, SeqStart, "a")
	top := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock,
		Key: fieldKey("other", "g"), Value: []byte("x")}
	if err := s.Apply(top); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Insert(SeqStart, nil); err == nil {
		t.Fatal("inserting with no clock left was accepted")
	}
	if _, err := s.Move(item, SeqStart); err == nil {
		t.Fatal("moving with no clock left was accepted")
	}
	if _, err := s.Set(item, nil); err == nil {
		t.Fatal("setting with no clock left was accepted")
	}
	if _, err := s.Remove(item); err == nil {
		t.Fatal("removing with no clock left was accepted")
	}
	if got, want := readSeq(s), "a"; got != want {
		t.Fatalf("the sequence reads %q, want %q", got, want)
	}
}

// Two clock ticks left: the identity and the place are written and the value is
// not, which is the one place an insert is not all-or-nothing.
func TestAnInsertWithTwoClockTicksLeft(t *testing.T) {
	s := NewSequence(1)
	top := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock - 2,
		Key: fieldKey("other", "g"), Value: []byte("x")}
	if err := s.Apply(top); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Insert(SeqStart, []byte("v")); err == nil {
		t.Fatal("an insert that could not write the value was reported as done")
	}
	// What is left is an item holding nothing, not half an item.
	if s.Len() != 1 {
		t.Fatalf("the sequence holds %d items, want 1", s.Len())
	}
	if value, ok := s.Value(s.Items()[0]); !ok || len(value) != 0 {
		t.Fatalf("the item holds %q, want nothing", value)
	}
}

// One clock tick left: the identity is minted and the place is not.
func TestAnInsertWithOneClockTickLeft(t *testing.T) {
	s := NewSequence(1)
	top := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1}, Clock: crdt.MaxClock - 1,
		Key: fieldKey("other", "g"), Value: []byte("x")}
	if err := s.Apply(top); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Insert(SeqStart, []byte("v")); err == nil {
		t.Fatal("an insert that could not write a place was reported as done")
	}
	if s.Len() != 0 {
		t.Fatalf("the sequence holds %d items, want none", s.Len())
	}
}

// Many replicas, many moves, delivered in different orders, all reading the
// same sequence with every item present exactly once.
func TestRandomisedMovesConvergeInASequence(t *testing.T) {
	for seed := range uint64(40) {
		t.Run(fmt.Sprint("seed ", seed), func(t *testing.T) {
			base := NewSequence(1)
			var items []ItemID
			after := SeqStart
			for i := range 8 {
				after = add(t, base, after, fmt.Sprint("i", i))
				items = append(items, after)
			}
			snapshot := base.Snapshot()

			const replicas = 4
			seqs := make([]*Sequence, replicas)
			for i := range seqs {
				s, err := LoadSequence(crdt.SiteID(i+2), snapshot)
				if err != nil {
					t.Fatal(err)
				}
				seqs[i] = s
			}

			rng := rand.New(rand.NewPCG(seed, 11))
			pending := make([][]crdt.MapOp, replicas)
			for range 6 {
				for i, s := range seqs {
					item := items[rng.IntN(len(items))]
					dest := SeqStart
					if rng.IntN(2) == 0 {
						dest = items[rng.IntN(len(items))]
					}
					op, err := s.Move(item, dest)
					if err != nil {
						continue // moving an item after itself
					}
					pending[i] = append(pending[i], op)
				}
			}

			for i, s := range seqs {
				var inbox []crdt.MapOp
				for j, ops := range pending {
					if j != i {
						inbox = append(inbox, ops...)
					}
				}
				rng.Shuffle(len(inbox), func(a, b int) { inbox[a], inbox[b] = inbox[b], inbox[a] })
				if err := s.Apply(inbox...); err != nil {
					t.Fatal(err)
				}
			}

			want := readSeq(seqs[0])
			for i, s := range seqs[1:] {
				if got := readSeq(s); got != want {
					t.Fatalf("replica %d reads %q, replica 0 reads %q", i+1, got, want)
				}
			}
			if n := seqs[0].Len(); n != len(items) {
				t.Fatalf("%d of %d items are readable: %q", n, len(items), want)
			}
		})
	}
}

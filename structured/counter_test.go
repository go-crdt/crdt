package structured

import (
	"errors"
	"testing"

	"github.com/go-crdt/crdt"
)

// The thing a counter is for: two replicas adding at the same time, and both
// additions surviving. A register cannot do this, which is the whole reason the
// type exists, so the test says so directly.
func TestConcurrentAdditionsBothCount(t *testing.T) {
	a, b := NewCounter(1), NewCounter(2)

	opA, err := a.Add(1)
	if err != nil {
		t.Fatal(err)
	}
	opB, err := b.Add(1)
	if err != nil {
		t.Fatal(err)
	}
	// Neither has heard the other: both went from nothing to one.
	if a.Value() != 1 || b.Value() != 1 {
		t.Fatalf("before exchanging, a is %d and b is %d, want 1 and 1", a.Value(), b.Value())
	}

	if err := a.Apply(opB); err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(opA); err != nil {
		t.Fatal(err)
	}
	if a.Value() != 2 {
		t.Fatalf("a holds %d after both additions, want 2", a.Value())
	}
	if b.Value() != 2 {
		t.Fatalf("b holds %d after both additions, want 2", b.Value())
	}
}

// A register, for contrast, loses one of them. This is not a test of the
// register; it is the measurement that justifies the counter, kept beside it so
// that the justification cannot quietly stop being true.
func TestARegisterLosesOneOfThem(t *testing.T) {
	a, b := NewRegister(1), NewRegister(2)
	opA, err := a.Set([]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	opB, err := b.Set([]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(opB); err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(opA); err != nil {
		t.Fatal(err)
	}
	got, _ := a.Get()
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("the register holds %v, want the one value that won", got)
	}
}

func TestAddingAndTakingAway(t *testing.T) {
	c := NewCounter(1)
	if c.Value() != 0 {
		t.Fatalf("an empty counter holds %d, want 0", c.Value())
	}
	for _, delta := range []int64{5, -2, 10, -1} {
		if _, err := c.Add(delta); err != nil {
			t.Fatal(err)
		}
	}
	if c.Value() != 12 {
		t.Fatalf("the counter holds %d, want 12", c.Value())
	}
	if c.Added() != 15 {
		t.Fatalf("%d was added, want 15", c.Added())
	}
	if c.Removed() != 3 {
		t.Fatalf("%d was taken away, want 3", c.Removed())
	}
}

func TestAddingNothingIsNotAnOperation(t *testing.T) {
	c := NewCounter(1)
	if _, err := c.Add(0); !errors.Is(err, ErrNoChange) {
		t.Fatalf("adding zero returned %v, want ErrNoChange", err)
	}
	if len(c.Map().Keys()) != 0 {
		t.Fatal("adding zero wrote a key")
	}
}

func TestTheTotalGoesNegative(t *testing.T) {
	c := NewCounter(1)
	if _, err := c.Add(-4); err != nil {
		t.Fatal(err)
	}
	if c.Value() != -4 {
		t.Fatalf("the counter holds %d, want -4", c.Value())
	}
}

// Delivery order must not matter, and neither must how the operations are
// batched.
func TestOrderOfDeliveryDoesNotMatter(t *testing.T) {
	a, b, c := NewCounter(1), NewCounter(2), NewCounter(3)
	var ops []crdt.MapOp
	for _, step := range []struct {
		who   *Counter
		delta int64
	}{{a, 3}, {b, -1}, {c, 7}, {a, -2}, {b, 4}, {c, -5}} {
		op, err := step.who.Add(step.delta)
		if err != nil {
			t.Fatal(err)
		}
		ops = append(ops, op)
	}
	want := int64(3 - 1 + 7 - 2 + 4 - 5)

	// Every replica, told everything, in the reverse order, one at a time.
	for _, replica := range []*Counter{a, b, c} {
		for i := len(ops) - 1; i >= 0; i-- {
			if err := replica.Apply(ops[i]); err != nil {
				t.Fatal(err)
			}
		}
		if replica.Value() != want {
			t.Fatalf("a replica holds %d, want %d", replica.Value(), want)
		}
	}
}

func TestASnapshotCarriesTheTotal(t *testing.T) {
	a := NewCounter(1)
	if _, err := a.Add(9); err != nil {
		t.Fatal(err)
	}
	b, err := LoadCounter(2, a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if b.Value() != 9 {
		t.Fatalf("the reloaded counter holds %d, want 9", b.Value())
	}
	if b.Site() != 2 {
		t.Fatalf("the reloaded counter is site %d, want 2", b.Site())
	}
	// And it can be added to as the new site, without disturbing what site one
	// contributed.
	if _, err := b.Add(1); err != nil {
		t.Fatal(err)
	}
	if b.Value() != 10 {
		t.Fatalf("the counter holds %d, want 10", b.Value())
	}
}

func TestLoadingRefusesRubbish(t *testing.T) {
	if _, err := LoadCounter(1, []byte("not a snapshot")); err == nil {
		t.Fatal("loading rubbish was accepted")
	}
}

// A peer can write anything into the map underneath. A value the counter cannot
// read is skipped rather than guessed at, and the rest of the total is still
// right.
func TestAKeyThatCannotBeReadIsSkipped(t *testing.T) {
	c := NewCounter(1)
	if _, err := c.Add(6); err != nil {
		t.Fatal(err)
	}
	for _, rubbish := range [][]byte{
		{},                      // no first number
		{0xFF},                  // a first number that never ends
		{1, 0xFF},               // a second number that never ends
		{1, 1, 9},               // something left over after both
		encodeTally(1<<62+1, 0), // a first half that would read as negative
		encodeTally(0, 1<<62+1), // and a second
	} {
		if _, err := c.Map().Set("99", rubbish); err != nil {
			t.Fatal(err)
		}
		if c.Value() != 6 {
			t.Fatalf("with %v under another site's key the total is %d, want 6", rubbish, c.Value())
		}
	}
}

// A replica that cannot read its own key starts that key again rather than
// adding to a number it had to guess.
func TestARubbishKeyOfItsOwnIsStartedAgain(t *testing.T) {
	c := NewCounter(1)
	if _, err := c.Map().Set("1", []byte{0xFF}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Add(3); err != nil {
		t.Fatal(err)
	}
	if c.Value() != 3 {
		t.Fatalf("the counter holds %d, want 3", c.Value())
	}
}

func TestCounterOfReadsAMapInPlace(t *testing.T) {
	m := crdt.NewMap(1)
	c := CounterOf(m)
	if _, err := c.Add(2); err != nil {
		t.Fatal(err)
	}
	if CounterOf(m).Value() != 2 {
		t.Fatal("a second view of the same map does not see the addition")
	}
}

func TestVersionAndOpsSinceCatchAPeerUp(t *testing.T) {
	a, b := NewCounter(1), NewCounter(2)
	behind := b.Version()
	for _, delta := range []int64{1, 2, 3} {
		if _, err := a.Add(delta); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Apply(a.OpsSince(behind)...); err != nil {
		t.Fatal(err)
	}
	if b.Value() != 6 {
		t.Fatalf("the caught-up replica holds %d, want 6", b.Value())
	}
	if len(a.OpsSince(b.Version())) != 0 {
		t.Fatal("the replica is caught up and is still being sent operations")
	}
}

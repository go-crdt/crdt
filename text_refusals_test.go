package crdt

import (
	"errors"
	"testing"
)

// A duplicate operation that is not an insert never reports a colliding
// identity.
//
// collides answers one question -- "two replicas minted the same (site,
// sequence) for different characters" -- and it is asked of every operation
// that arrives twice. Its own documentation says it answers false whenever it
// cannot answer, because the alternative is to call a replica's identity
// broken on evidence it does not have.
//
// The refusal at the top of it is what makes that true, and a mutation sweep
// walked through it on 2026-10-08. Deleted, a superseded run is carried into
// the comparison below, where its ID names a character this replica holds and
// its Char field is the zero value no character has. Measured on this fixture:
//
//	collides(a superseded run) = false   as written
//	collides(a superseded run) = true    with the refusal gone
//
// and true there is Apply returning ErrCollidingID. The case is not exotic: a
// superseded run describes operations the sender no longer holds, so it covers
// sequence numbers the receiver may well still have, and a peer that resends
// one would be told its identity clashes and have its session torn down.
func TestADuplicateSupersededRunIsNotACollidingIdentity(t *testing.T) {
	peer := New(2)
	ops, err := peer.Insert(0, "abc")
	if err != nil {
		t.Fatal(err)
	}
	d := New(1)
	if err := d.Apply(ops...); err != nil {
		t.Fatal(err)
	}

	// A run covering what was just applied: the sender no longer holds those
	// operations, this replica does.
	// Its clock is its own sequence number: a superseded run reports no Lamport
	// time, because the operations it stands for are not here. See Op.validate.
	last := ops[len(ops)-1].ID
	sup := Op{Kind: OpSuperseded, ID: last, Clock: last.Seq, Span: 3}
	if !d.vv.Includes(sup.ID) {
		t.Fatalf("the fixture does not hold %v, so Apply would not reach collides at all", sup.ID)
	}

	if err := d.Apply(sup); err != nil {
		t.Fatalf("a superseded run over operations this replica holds gave %v; "+
			"ErrCollidingID here says a peer's identity is broken on the evidence of a field it does not use", err)
	}
	if got := d.String(); got != "abc" {
		t.Errorf("the document reads %q, want %q", got, "abc")
	}

	// The control, and it is the reason this test is not vacuous: an insert
	// arriving twice with a DIFFERENT character is exactly what collides is for,
	// and it must still be caught.
	clash := ops[0]
	clash.Char = 'Z'
	if err := d.Apply(clash); !errors.Is(err, ErrCollidingID) {
		t.Errorf("two different characters under one identity gave %v, want ErrCollidingID", err)
	}
}

// The walk from the mark gives up when its budget runs out, instead of walking
// the whole document.
//
// visibleAt starts at the last local edit and steps run by run, because editing
// is local and the position wanted is nearly always a few characters away. When
// it is not, the index is cheaper, and the budget is what decides. Both
// directions carry the same refusal and a mutation sweep walked through both on
// 2026-10-08, for a reason the shape explains: with the position always in
// range -- visibleAt's caller guarantees it -- removing the budget never gives
// a WRONG answer. It gives the right one by the expensive route, and nothing a
// test asserts about the answer can see that.
//
// What can see it is that the budget is a parameter. Called with none left,
// these must report that they are giving up, and that is the whole contract:
// visibleAt reads a false here as "descend the index instead". Deleted, they
// walk on and answer, and visibleAt never descends the index at all.
func TestTheWalkFromTheMarkGivesUpWhenItsBudgetIsSpent(t *testing.T) {
	// Each insertion at the front starts a run of its own, so this is a
	// document of many blocks rather than one long one.
	d := New(1)
	const blocks = 8
	for range blocks {
		if _, err := d.Insert(0, "x"); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(d.String()); got != blocks {
		t.Fatalf("the fixture holds %d characters, want %d", got, blocks)
	}

	first := d.head.next
	if first == nil || first.next == nil {
		t.Fatal("the fixture is one block, so neither walk ever steps and this test is vacuous")
	}

	// The control: with budget to spend, the walk finds the last character.
	if _, _, ok := forward(first, 0, 0, blocks-1, scanBudget); !ok {
		t.Fatal("the walk could not reach the end with its budget, so the refusal below is not what is being measured")
	}
	if _, _, ok := forward(first, 0, 0, blocks-1, 0); ok {
		t.Error("forward answered a position in a later run with no budget left, " +
			"so visibleAt will never fall back to the index however far the position is")
	}

	// And the same question towards the start, from the last block.
	last := first
	for last.next != nil {
		last = last.next
	}
	if _, _, ok := backward(last, last.size()-1, blocks-1, 0, scanBudget); !ok {
		t.Fatal("the backward walk could not reach the start with its budget")
	}
	if _, _, ok := backward(last, last.size()-1, blocks-1, 0, 0); ok {
		t.Error("backward answered a position in an earlier run with no budget left")
	}
}

// Three more refusals in text.go survived the same sweep and are deliberately
// not pinned, each for a reason that was measured rather than argued.
//
//	text.go:875, "a superseded run is ready". Deleting it falls through to a
//	lookup of op.Target, and a superseded run's target is the zero ID, which
//	IsRoot -- so lookupChar answers "found" and ready returns true anyway.
//	Probed on an empty document and a filled one: true both ways, before and
//	after. The refusal says what it means rather than relying on that.
//
//	text.go:228, isolate's "this record is already one character" path, and
//	text.go:1045, duplicatesInOrder's "nothing has arrived since" path. Both
//	are cost, and both reach the same value by the longer route. Run under
//	`mutate -expect-pass`, which succeeds only when the suite still passes:
//	it does, for both.
//
// The Includes half of the gate above is in the same category. Dropping only
// `!d.vv.Includes(op.ID)` leaves the whole suite green and this test with it,
// because an identity this replica does not hold has no character for
// lookupChar to find and the !ok underneath answers first. What carries the
// gate is the kind.

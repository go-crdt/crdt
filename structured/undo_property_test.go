package structured

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/go-crdt/crdt"
)

// The claim of [Undo], stated as a property over three replicas that edit at
// once and hear of each other's work late and out of order: an undo takes back
// this replica's last group and nothing of anybody else's, and when everything
// has been delivered the replicas read the same text.
//
// Each site writes in its own alphabet, so a character says who made it. The
// model keeps, per site, how many of its own characters should be in the
// document, and moves it only by what that site did itself: a group typed, a
// character removed, an undo or a redo of its own. No site's count is ever moved
// by another site's undo. If one were, the final text would not match.
//
// The specification is the one on [Undo]: an undo is a new edit that names the
// old one's effect by identity, not a restoration of an earlier state. This test
// checks that property directly instead of assuming it from the construction.
// (The selective-undo literature was not opened for this change, so none is
// cited.)

var siteAlphabets = [3]string{"abcdefgh", "ABCDEFGH", "01234567"}

// ownRunes counts the characters of s that belong to site i's alphabet.
func ownRunes(s string, i int) int {
	n := 0
	for _, r := range s {
		if strings.ContainsRune(siteAlphabets[i], r) {
			n++
		}
	}
	return n
}

type undoSite struct {
	u     *Undo
	undo  []int // for each undo step, how many of its own characters it would add (+) or take (-)
	redo  []int
	model int // how many of its own characters the document should hold
}

type undoNet struct {
	t     *testing.T
	sites [3]*undoSite
	// inbox[to] holds batches not yet delivered to that site.
	inbox [3][][]crdt.Op
	rng   *rand.Rand
}

func newUndoNet(t *testing.T, seed uint64) *undoNet {
	n := &undoNet{t: t, rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))}
	for i := range n.sites {
		n.sites[i] = &undoSite{u: NewUndo(crdt.New(crdt.SiteID(i + 1)))}
	}
	return n
}

func (n *undoNet) broadcast(from int, ops []crdt.Op) {
	if len(ops) == 0 {
		return
	}
	for to := range n.sites {
		if to != from {
			n.inbox[to] = append(n.inbox[to], ops)
		}
	}
}

// deliverSome hands one pending batch, picked at random, to a site: batches
// overtake each other, which the document must absorb.
func (n *undoNet) deliverSome(to int) {
	q := n.inbox[to]
	if len(q) == 0 {
		return
	}
	k := n.rng.IntN(len(q))
	if err := n.sites[to].u.Doc().Apply(q[k]...); err != nil {
		n.t.Fatalf("apply at site %d: %v", to+1, err)
	}
	n.inbox[to] = append(q[:k:k], q[k+1:]...)
}

func (n *undoNet) deliverAll() {
	for to := range n.sites {
		for len(n.inbox[to]) > 0 {
			n.deliverSome(to)
		}
	}
}

func (n *undoNet) typeGroup(i int) {
	s := n.sites[i]
	s.u.Begin()
	total := 0
	for range 1 + n.rng.IntN(3) {
		text := ""
		for range 1 + n.rng.IntN(3) {
			a := siteAlphabets[i]
			text += string(a[n.rng.IntN(len(a))])
		}
		ops, err := s.u.Insert(n.rng.IntN(s.u.Doc().Len()+1), text)
		if err != nil {
			n.t.Fatal(err)
		}
		n.broadcast(i, ops)
		total += len(text)
	}
	s.u.Commit()
	s.undo = append(s.undo, -total) // undoing it takes `total` characters
	s.redo = nil
	s.model += total
}

// removeOwn removes one character this site wrote, if its replica shows one.
func (n *undoNet) removeOwn(i int) {
	s := n.sites[i]
	var at []int
	for p, r := range []rune(s.u.Doc().String()) {
		if strings.ContainsRune(siteAlphabets[i], r) {
			at = append(at, p)
		}
	}
	if len(at) == 0 {
		return
	}
	ops, err := s.u.Delete(at[n.rng.IntN(len(at))], 1)
	if err != nil {
		n.t.Fatal(err)
	}
	n.broadcast(i, ops)
	s.undo = append(s.undo, +1)
	s.redo = nil
	s.model--
}

func (n *undoNet) undoOne(i int) {
	s := n.sites[i]
	if len(s.undo) == 0 {
		return
	}
	ops, err := s.u.Undo()
	if err != nil {
		n.t.Fatal(err)
	}
	n.broadcast(i, ops)
	d := s.undo[len(s.undo)-1]
	s.undo = s.undo[:len(s.undo)-1]
	s.redo = append(s.redo, -d)
	s.model += d
}

func (n *undoNet) redoOne(i int) {
	s := n.sites[i]
	if len(s.redo) == 0 {
		return
	}
	ops, err := s.u.Redo()
	if err != nil {
		n.t.Fatal(err)
	}
	n.broadcast(i, ops)
	d := s.redo[len(s.redo)-1]
	s.redo = s.redo[:len(s.redo)-1]
	s.undo = append(s.undo, -d)
	s.model += d
}

func TestUndoIsOnlyThisSitesAcrossThreeReplicas(t *testing.T) {
	for seed := uint64(1); seed <= 300; seed++ {
		n := newUndoNet(t, seed)
		var trace []string
		step := func(what string) { trace = append(trace, what) }
		for range 60 {
			i := n.rng.IntN(3)
			switch n.rng.IntN(10) {
			case 0, 1, 2:
				step("type")
				n.typeGroup(i)
			case 3:
				step("remove")
				n.removeOwn(i)
			case 4, 5:
				step("undo")
				n.undoOne(i)
			case 6:
				step("redo")
				n.redoOne(i)
			default:
				n.deliverSome(n.rng.IntN(3))
			}
		}
		n.deliverAll()

		want := n.sites[0].u.Doc().String()
		for i, s := range n.sites {
			if got := s.u.Doc().String(); got != want {
				t.Fatalf("seed %d: replicas disagree: site %d reads %q, site 1 reads %q", seed, i+1, got, want)
			}
		}
		for i, s := range n.sites {
			if got := ownRunes(want, i); got != s.model {
				t.Fatalf("seed %d: site %d has %d of its characters in %q, want %d: an undo or redo touched work that was not its own", seed, i+1, got, want, s.model)
			}
		}
	}
}

// Taking a character out and then undoing that must not leave the character
// beyond the reach of the group that typed it: undoing the group afterwards has
// to take it out again.
func TestUndoingAGroupAfterUndoingARemovalInIt(t *testing.T) {
	u := NewUndo(crdt.New(1))
	if _, err := u.Insert(0, "ab"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Delete(1, 1); err != nil { // "a"
		t.Fatal(err)
	}
	if _, err := u.Undo(); err != nil { // "ab" again
		t.Fatal(err)
	}
	if s := u.Doc().String(); s != "ab" {
		t.Fatalf("after undoing the removal: %q", s)
	}
	if _, err := u.Undo(); err != nil { // the typing
		t.Fatal(err)
	}
	if s := u.Doc().String(); s != "" {
		t.Fatalf("after undoing the typing: %q, want it all taken back", s)
	}
}

// Doing a group again puts its pieces back in the order they were typed in, even
// though every one of them is a new character: a piece placed after another
// follows the new one, not the one that is gone.
func TestRedoingAGroupKeepsItsPiecesInOrder(t *testing.T) {
	u := NewUndo(crdt.New(1))
	u.Begin()
	if _, err := u.Insert(0, "one "); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Insert(4, "two"); err != nil {
		t.Fatal(err)
	}
	u.Commit()
	for round := range 3 {
		if _, err := u.Undo(); err != nil {
			t.Fatal(err)
		}
		if s := u.Doc().String(); s != "" {
			t.Fatalf("round %d: after undo %q", round, s)
		}
		if _, err := u.Redo(); err != nil {
			t.Fatal(err)
		}
		if s := u.Doc().String(); s != "one two" {
			t.Fatalf("round %d: after redo %q, want the group back in order", round, s)
		}
	}
}

// A group whose pieces were put back as new characters, while a peer had typed
// next to them, comes back in order and the replicas agree: the removal that
// follows a character the undo replaced follows the new one.
func TestRedoingAGroupBesideAPeersTyping(t *testing.T) {
	u, theirs, send := pair(t)
	step := func(want string, ops []crdt.Op, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		send(t, ops, theirs)
		if u.Doc().String() != theirs.String() || u.Doc().String() != want {
			t.Fatalf("want %q on both, mine %q theirs %q", want, u.Doc().String(), theirs.String())
		}
	}
	u.Begin()
	a, err := u.Insert(0, "one ")
	if err != nil {
		t.Fatal(err)
	}
	b, err := u.Insert(4, "two")
	if err != nil {
		t.Fatal(err)
	}
	u.Commit()
	send(t, append(a, b...), theirs)
	bang, err := theirs.Insert(7, "!")
	if err != nil {
		t.Fatal(err)
	}
	send(t, bang, u.Doc())

	ops, err := u.Delete(0, 4)
	step("two!", ops, err)
	ops, err = u.Undo() // "one " again, as new characters
	step("one two!", ops, err)
	ops, err = u.Undo() // the typing
	step("!", ops, err)
	ops, err = u.Redo() // the typing again, in order, before the peer's "!"
	step("one two!", ops, err)
}

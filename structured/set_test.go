package structured

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/go-crdt/crdt"
)

func mustAdd(t *testing.T, s *Set, name string) []crdt.MapOp {
	t.Helper()
	ops, err := s.Add(name)
	if err != nil {
		t.Fatal(err)
	}
	return ops
}

func mustRemove(t *testing.T, s *Set, name string) []crdt.MapOp {
	t.Helper()
	ops, err := s.Remove(name)
	if err != nil {
		t.Fatal(err)
	}
	return ops
}

func members(s *Set) string { return strings.Join(s.Names(), " ") }

// The ordinary case, with nothing about this type showing.
func TestASetHoldsNames(t *testing.T) {
	s := NewSet(1)
	if s.Len() != 0 || len(s.Names()) != 0 || s.Contains("urgent") {
		t.Fatalf("a fresh set holds %q", members(s))
	}
	mustAdd(t, s, "urgent")
	mustAdd(t, s, "draft")
	if got, want := members(s), "draft urgent"; got != want {
		t.Fatalf("set holds %q, want %q", got, want)
	}
	if !s.Contains("urgent") || s.Len() != 2 {
		t.Fatalf("Contains/Len disagree with %q", members(s))
	}
	mustRemove(t, s, "urgent")
	if got, want := members(s), "draft"; got != want {
		t.Fatalf("after removing, set holds %q, want %q", got, want)
	}
	if s.Contains("urgent") {
		t.Fatal("a removed name is still there")
	}
	if s.Site() != 1 || s.Map() == nil || s.Records() == nil {
		t.Fatalf("Site = %d", s.Site())
	}
}

// The case a map of flags gets wrong: somebody takes away a label they have
// never been shown, and the label stays, because the removal says what it
// observed and there was nothing to observe.
func TestARemovalCannotTakeAwayWhatItNeverSaw(t *testing.T) {
	ada, grace := NewSet(1), NewSet(2)
	shared := mustAdd(t, ada, "draft")
	if err := grace.Apply(shared...); err != nil {
		t.Fatal(err)
	}

	// Ada labels it urgent. Grace, who cannot see that, clears the labels she
	// has — which is "draft", and only that.
	added := mustAdd(t, ada, "urgent")
	removed := mustRemove(t, grace, "draft")

	if err := ada.Apply(removed...); err != nil {
		t.Fatal(err)
	}
	if err := grace.Apply(added...); err != nil {
		t.Fatal(err)
	}
	if got, want := members(ada), "urgent"; got != want {
		t.Fatalf("ada holds %q, want %q", got, want)
	}
	if got, want := members(grace), "urgent"; got != want {
		t.Fatalf("grace holds %q, want %q", got, want)
	}

	// And the same name, added and removed at once: the addition stands,
	// because the removal saw no tag of it.
	alsoAdded := mustAdd(t, ada, "blocked")
	if err := grace.Apply(alsoAdded...); err != nil {
		t.Fatal(err)
	}
	graceRemoves := mustRemove(t, grace, "blocked")
	adaAddsAgain := mustAdd(t, ada, "blocked") // a second tag Grace never saw
	if err := ada.Apply(graceRemoves...); err != nil {
		t.Fatal(err)
	}
	if err := grace.Apply(adaAddsAgain...); err != nil {
		t.Fatal(err)
	}
	if !ada.Contains("blocked") || !grace.Contains("blocked") {
		t.Fatalf("the concurrent addition was taken away: %q / %q", members(ada), members(grace))
	}
	if got := ada.Tags("blocked"); got != 1 {
		t.Fatalf("blocked has %d tags, want the one the removal did not see", got)
	}
}

// A removal that has seen everything takes the name away, on every replica.
func TestARemovalThatSawEverythingTakesItAway(t *testing.T) {
	ada, grace := NewSet(1), NewSet(2)
	first := mustAdd(t, ada, "urgent")
	if err := grace.Apply(first...); err != nil {
		t.Fatal(err)
	}
	second := mustAdd(t, grace, "urgent") // two replicas, two tags
	if err := ada.Apply(second...); err != nil {
		t.Fatal(err)
	}
	if got := ada.Tags("urgent"); got != 2 {
		t.Fatalf("urgent has %d tags, want 2", got)
	}

	gone := mustRemove(t, ada, "urgent")
	if len(gone) != 2 {
		t.Fatalf("removing took %d operations, want one per tag", len(gone))
	}
	if err := grace.Apply(gone...); err != nil {
		t.Fatal(err)
	}
	if ada.Contains("urgent") || grace.Contains("urgent") {
		t.Fatalf("the name survived a removal that saw both tags: %q / %q",
			members(ada), members(grace))
	}
}

// Adding a name that is already there is not nothing, and the test is what
// happens when a removal that saw the first tag arrives afterwards.
func TestAddingWhatIsAlreadyThereIsNotNothing(t *testing.T) {
	ada, grace := NewSet(1), NewSet(2)
	first := mustAdd(t, ada, "urgent")
	if err := grace.Apply(first...); err != nil {
		t.Fatal(err)
	}

	// Grace removes it. Ada, not having seen that, presses the button again.
	gone := mustRemove(t, grace, "urgent")
	again := mustAdd(t, ada, "urgent")
	if got := ada.Tags("urgent"); got != 2 {
		t.Fatalf("adding again left %d tags, want 2", got)
	}
	if err := ada.Apply(gone...); err != nil {
		t.Fatal(err)
	}
	if err := grace.Apply(again...); err != nil {
		t.Fatal(err)
	}
	if !ada.Contains("urgent") || !grace.Contains("urgent") {
		t.Fatalf("pressing the button again did nothing: %q / %q", members(ada), members(grace))
	}
	if got := ada.Tags("urgent"); got != 1 {
		t.Fatalf("%d tags left, want only the one the removal did not see", got)
	}
}

// A tag is the identity of the operation that minted it, so the set already
// knows who put a name in it.
func TestWhoAddedAName(t *testing.T) {
	ada, grace, third := NewSet(1), NewSet(2), NewSet(3)
	fromAda := mustAdd(t, ada, "urgent")
	fromGrace := mustAdd(t, grace, "urgent")
	fromAdaAgain := mustAdd(t, ada, "urgent")

	if err := third.Apply(append(append(fromAda, fromGrace...), fromAdaAgain...)...); err != nil {
		t.Fatal(err)
	}
	got := third.Adders("urgent")
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("Adders = %v, want [1 2] with no repetition", got)
	}
	if n := third.Tags("urgent"); n != 3 {
		t.Fatalf("Tags = %d, want one per addition", n)
	}
	if got := third.Adders("nobody added this"); got != nil {
		t.Fatalf("Adders of an absent name = %v", got)
	}
}

// A set survives being written down and read back.
func TestASetSurvivesASnapshot(t *testing.T) {
	s := NewSet(1)
	mustAdd(t, s, "urgent")
	mustAdd(t, s, "draft")
	mustRemove(t, s, "draft")
	mustAdd(t, s, "réunion") // a name that is not ASCII

	back, err := LoadSet(2, s.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := members(back), members(s); got != want {
		t.Fatalf("reloaded holds %q, want %q", got, want)
	}
	if back.Site() != 2 {
		t.Fatalf("reloaded as site %d", back.Site())
	}
	// And a removal made after the reload still takes the reloaded tag away.
	if _, err := back.Remove("urgent"); err != nil {
		t.Fatal(err)
	}
	if back.Contains("urgent") {
		t.Fatal("a tag from a snapshot cannot be removed")
	}
	if _, err := LoadSet(1, []byte("not a snapshot")); err == nil {
		t.Fatal("a snapshot that is not one loaded")
	}
}

// A set is a map, so it can be a part of a document beside everything else it
// holds, and syncs the way every part here does.
func TestASetIsAPartOfADocument(t *testing.T) {
	doc := crdt.NewComposite(1)
	part, err := doc.Map("labels")
	if err != nil {
		t.Fatal(err)
	}
	labels := SetOf(part)
	mustAdd(t, labels, "urgent")

	peer := crdt.NewComposite(2)
	peerPart, err := peer.Map("labels")
	if err != nil {
		t.Fatal(err)
	}
	peerLabels := SetOf(peerPart)

	owed := labels.OpsSince(peerLabels.Version())
	if len(owed) != 2 {
		t.Fatalf("%d operations owed, want the mint and the tag", len(owed))
	}
	if err := peerLabels.Apply(owed...); err != nil {
		t.Fatal(err)
	}
	if !peerLabels.Contains("urgent") {
		t.Fatalf("the peer holds %q", members(peerLabels))
	}
	if got := labels.OpsSince(peerLabels.Version()); len(got) != 0 {
		t.Fatalf("%d operations still owed after a full exchange", len(got))
	}
}

// Everything a caller can get wrong, and what it is told.
func TestWhatASetRefuses(t *testing.T) {
	s := NewSet(1)
	for _, bad := range []string{"", "\xff\xfe"} {
		if _, err := s.Add(bad); !errors.Is(err, crdt.ErrInvalidOp) {
			t.Fatalf("Add(%q) = %v, want invalid", bad, err)
		}
		if _, err := s.Remove(bad); !errors.Is(err, crdt.ErrInvalidOp) {
			t.Fatalf("Remove(%q) = %v, want invalid", bad, err)
		}
		if s.Contains(bad) {
			t.Fatalf("Contains(%q) is true", bad)
		}
		if got := s.Adders(bad); got != nil {
			t.Fatalf("Adders(%q) = %v", bad, got)
		}
		if got := s.Tags(bad); got != 0 {
			t.Fatalf("Tags(%q) = %d", bad, got)
		}
	}
	// Removing a name that is not there says so rather than sending nothing.
	if _, err := s.Remove("absent"); !errors.Is(err, ErrNoChange) {
		t.Fatalf("removing an absent name = %v, want no change", err)
	}
}

// A map holds whatever key an applied operation names. A tag this version
// cannot read still makes the name present — what makes a name present is
// having a tag at all — and it is simply not counted among the adders.
func TestATagThisVersionCannotReadStillHoldsTheName(t *testing.T) {
	s := NewSet(1)
	if _, err := s.Records().SetField("urgent", "not-an-identity", nil); err != nil {
		t.Fatal(err)
	}
	if !s.Contains("urgent") {
		t.Fatal("a name held by an unreadable tag is not in the set")
	}
	if got := s.Adders("urgent"); got != nil {
		t.Fatalf("Adders = %v, want nobody this version can name", got)
	}
	if got := s.Tags("urgent"); got != 1 {
		t.Fatalf("Tags = %d, want 1", got)
	}
	// And removing it works, because a removal takes away the fields it sees
	// whatever they say.
	if _, err := s.Remove("urgent"); err != nil {
		t.Fatal(err)
	}
	if s.Contains("urgent") {
		t.Fatal("an unreadable tag cannot be removed")
	}
}

// With no clock left a replica writes nothing and says so.
func TestASetWithNoClockLeft(t *testing.T) {
	s := NewSet(1)
	mustAdd(t, s, "urgent")
	top := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1},
		Clock: crdt.MaxClock, Key: "seed", Value: []byte("x")}
	if err := s.Apply(top); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("another"); err == nil {
		t.Fatal("adding with no clock left was accepted")
	}
	if _, err := s.Remove("urgent"); err == nil {
		t.Fatal("removing with no clock left was accepted")
	}
	// The set is exactly as it was.
	if got, want := members(s), "urgent"; got != want {
		t.Fatalf("after two refusals the set holds %q, want %q", got, want)
	}
}

// One tick left is not enough for an addition, because an addition is two
// writes. The tag is minted and cannot be written, which leaves an identity
// nothing refers to — the set is exactly as it was, and nothing has to be
// undone.
func TestASetWithOneTickLeft(t *testing.T) {
	s := NewSet(1)
	mustAdd(t, s, "urgent")
	nearlyTop := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1},
		Clock: crdt.MaxClock - 1, Key: "seed", Value: []byte("x")}
	if err := s.Apply(nearlyTop); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("another"); err == nil {
		t.Fatal("an addition with one tick left was accepted")
	}
	if got, want := members(s), "urgent"; got != want {
		t.Fatalf("the half-written addition left %q, want %q", got, want)
	}
	if s.Contains("another") {
		t.Fatal("the name that could not be tagged is in the set")
	}
}

// Whatever order the operations arrive in, and however often, every replica
// holds the same set — asserted on byte-equal snapshots, which is stronger than
// an equal listing.
func TestASetConverges(t *testing.T) {
	names := []string{"urgent", "draft", "blocked", "review"}
	for seed := range uint64(200) {
		rng := rand.New(rand.NewPCG(seed, seed))
		const replicas = 4
		sets := make([]*Set, replicas)
		for i := range sets {
			sets[i] = NewSet(crdt.SiteID(i + 1))
		}
		inbox := make([][]crdt.MapOp, replicas)

		for range 40 {
			i := rng.IntN(replicas)
			if n := len(inbox[i]); n > 0 {
				take := 1 + rng.IntN(n)
				ops := inbox[i][:take]
				inbox[i] = inbox[i][take:]
				if err := sets[i].Apply(ops...); err != nil {
					t.Fatalf("seed %d: %v", seed, err)
				}
				if rng.IntN(3) == 0 {
					if err := sets[i].Apply(ops...); err != nil {
						t.Fatalf("seed %d: duplicate delivery: %v", seed, err)
					}
				}
			}
			name := names[rng.IntN(len(names))]
			var ops []crdt.MapOp
			var err error
			if rng.IntN(3) == 0 {
				ops, err = sets[i].Remove(name)
				if errors.Is(err, ErrNoChange) {
					continue
				}
			} else {
				ops, err = sets[i].Add(name)
			}
			if err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
			for j := range sets {
				if j != i {
					inbox[j] = append(inbox[j], ops...)
				}
			}
		}

		for i := range sets {
			rest := inbox[i]
			rng.Shuffle(len(rest), func(a, b int) { rest[a], rest[b] = rest[b], rest[a] })
			if err := sets[i].Apply(rest...); err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
		}
		want := sets[0].Snapshot()
		for i, s := range sets[1:] {
			if got := s.Snapshot(); !bytes.Equal(got, want) {
				t.Fatalf("seed %d: replica %d holds %q against %q",
					seed, i+2, members(s), members(sets[0]))
			}
		}
	}
}

// The property that makes this a set rather than a race: a name added by
// somebody who could not see the removal is in the set afterwards, whatever
// order the operations are delivered in.
func TestAnAdditionOutlivesAConcurrentRemovalInEveryOrder(t *testing.T) {
	// Build the fixture once: Ada adds, Grace removes what she saw, Ada adds
	// again without having seen the removal.
	ada, grace := NewSet(1), NewSet(2)
	first := mustAdd(t, ada, "urgent")
	if err := grace.Apply(first...); err != nil {
		t.Fatal(err)
	}
	removal := mustRemove(t, grace, "urgent")
	second := mustAdd(t, ada, "urgent")

	var ops []crdt.MapOp
	ops = append(ops, first...)
	ops = append(ops, removal...)
	ops = append(ops, second...)

	rng := rand.New(rand.NewPCG(11, 11))
	for trial := range 200 {
		shuffled := append([]crdt.MapOp(nil), ops...)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		s := NewSet(9)
		for _, op := range shuffled {
			if err := s.Apply(op); err != nil {
				t.Fatalf("trial %d: %v", trial, err)
			}
		}
		if !s.Contains("urgent") {
			t.Fatalf("trial %d: order %v lost the addition", trial, orderOf(shuffled))
		}
		if got := s.Tags("urgent"); got != 1 {
			t.Fatalf("trial %d: %d tags, want the one the removal did not see", trial, got)
		}
	}
}

func orderOf(ops []crdt.MapOp) []string {
	out := make([]string, 0, len(ops))
	for _, op := range ops {
		out = append(out, fmt.Sprintf("%d.%d/%d", op.ID.Site, op.ID.Seq, op.Kind))
	}
	return out
}

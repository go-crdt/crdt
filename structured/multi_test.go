package structured

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/go-crdt/crdt"
)

func mustSet(t *testing.T, r *MultiRegister, value string) crdt.MapOp {
	t.Helper()
	op, err := r.Set([]byte(value))
	if err != nil {
		t.Fatal(err)
	}
	return op
}

// live renders what a register reads as, which is the only readable way to say
// two replicas agree about a disagreement.
func live(r *MultiRegister) string {
	parts := make([]string, 0, 4)
	for _, reading := range r.Readings() {
		if reading.Cleared {
			parts = append(parts, fmt.Sprintf("%d:cleared", reading.Site))
			continue
		}
		parts = append(parts, fmt.Sprintf("%d:%s", reading.Site, reading.Value))
	}
	return strings.Join(parts, " ")
}

// The ordinary case: one replica writing reads as one value, and nothing about
// this type shows.
func TestOneWriterReadsAsOneValue(t *testing.T) {
	r := NewMultiRegister(1)
	if got, ok := r.Value(); ok {
		t.Fatalf("an unwritten register holds %q", got)
	}
	if r.Conflicted() || r.Readings() != nil || r.Values() != nil {
		t.Fatalf("an unwritten register reads as %q", live(r))
	}

	mustSet(t, r, "first")
	if got, ok := r.Value(); !ok || string(got) != "first" {
		t.Fatalf("Value = %q, %v", got, ok)
	}
	mustSet(t, r, "second")
	if got, ok := r.Value(); !ok || string(got) != "second" {
		t.Fatalf("after a second write Value = %q, %v", got, ok)
	}
	if r.Conflicted() {
		t.Fatalf("one replica writing twice conflicts: %q", live(r))
	}
	if got := r.Values(); len(got) != 1 || string(got[0]) != "second" {
		t.Fatalf("Values = %q", got)
	}
	if r.Site() != 1 || r.Map() == nil {
		t.Fatalf("Site = %d", r.Site())
	}
}

// Two replicas that wrote without seeing each other are both read, and both
// replicas read the same pair. This is the whole point: a register would have
// thrown one of them away with nothing left saying it existed.
func TestTwoWritesThatDidNotSeeEachOtherBothStand(t *testing.T) {
	a, b := NewMultiRegister(1), NewMultiRegister(2)
	fromA := mustSet(t, a, "notes.txt")
	fromB := mustSet(t, b, "readme.txt")

	if err := a.Apply(fromB); err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(fromA); err != nil {
		t.Fatal(err)
	}
	want := "1:notes.txt 2:readme.txt"
	if got := live(a); got != want {
		t.Fatalf("a reads %q, want %q", got, want)
	}
	if got := live(b); got != want {
		t.Fatalf("b reads %q, want %q", got, want)
	}
	if !a.Conflicted() || !b.Conflicted() {
		t.Fatal("a disagreement does not report as one")
	}
	if _, ok := a.Value(); ok {
		t.Fatal("a disagreement reads as a single value")
	}
	if got := a.Values(); len(got) != 2 {
		t.Fatalf("Values = %q, want both", got)
	}
}

// Choosing between the values of a disagreement is writing the one chosen.
// There is no separate operation, because a write already dominates everything
// its writer could see.
func TestChoosingIsJustWriting(t *testing.T) {
	a, b := NewMultiRegister(1), NewMultiRegister(2)
	fromA := mustSet(t, a, "one")
	fromB := mustSet(t, b, "two")
	if err := a.Apply(fromB); err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(fromA); err != nil {
		t.Fatal(err)
	}
	if !a.Conflicted() {
		t.Fatal("the two writes did not conflict")
	}

	// Ada picks Grace's value. Nothing but a write.
	chosen := mustSet(t, a, "two")
	if a.Conflicted() {
		t.Fatalf("choosing left a conflict: %q", live(a))
	}
	if got, ok := a.Value(); !ok || string(got) != "two" {
		t.Fatalf("after choosing, Value = %q, %v", got, ok)
	}
	if err := b.Apply(chosen); err != nil {
		t.Fatal(err)
	}
	if b.Conflicted() {
		t.Fatalf("the other replica still disagrees: %q", live(b))
	}
	if got, ok := b.Value(); !ok || string(got) != "two" {
		t.Fatalf("b reads %q, %v", got, ok)
	}
}

// A third replica that saw only one of two concurrent writes supersedes that
// one and not the other — which is the difference between a vector and a clock,
// and the reason this type carries one.
func TestSeeingOneOfTwoSupersedesOnlyThatOne(t *testing.T) {
	a, b, c := NewMultiRegister(1), NewMultiRegister(2), NewMultiRegister(3)
	fromA := mustSet(t, a, "a")
	fromB := mustSet(t, b, "b")

	if err := c.Apply(fromA); err != nil {
		t.Fatal(err)
	}
	fromC := mustSet(t, c, "c")

	for _, r := range []*MultiRegister{a, b, c} {
		if err := r.Apply(fromA, fromB, fromC); err != nil {
			t.Fatal(err)
		}
	}
	want := "2:b 3:c"
	for i, r := range []*MultiRegister{a, b, c} {
		if got := live(r); got != want {
			t.Fatalf("replica %d reads %q, want %q", i+1, got, want)
		}
	}
}

// A replica learns what a third one has done from the second, without ever
// hearing from it directly: a writing carries what its writer had seen.
func TestKnowledgeTravelsThroughTheReplicaThatSawIt(t *testing.T) {
	a, b, c := NewMultiRegister(1), NewMultiRegister(2), NewMultiRegister(3)
	fromA := mustSet(t, a, "a")
	if err := b.Apply(fromA); err != nil {
		t.Fatal(err)
	}
	fromB := mustSet(t, b, "b")

	// c hears only b, and has never seen a's operation.
	if err := c.Apply(fromB); err != nil {
		t.Fatal(err)
	}
	fromC := mustSet(t, c, "c")

	// Now a's operation finally reaches c. It is old news, and c says so.
	if err := c.Apply(fromA); err != nil {
		t.Fatal(err)
	}
	if got := live(c); got != "3:c" {
		t.Fatalf("c reads %q, want the one value it wrote knowing everything", got)
	}
	if err := a.Apply(fromB, fromC); err != nil {
		t.Fatal(err)
	}
	if got := live(a); got != "3:c" {
		t.Fatalf("a reads %q", got)
	}
}

// Somebody deleting a value while somebody else changes it is a disagreement
// like any other, and one a reader can be shown — which is what a register
// cannot do at all.
func TestAClearAndAWriteCanBothStand(t *testing.T) {
	a, b := NewMultiRegister(1), NewMultiRegister(2)
	fromA := mustSet(t, a, "keep")
	if err := b.Apply(fromA); err != nil {
		t.Fatal(err)
	}

	gone, err := a.Clear()
	if err != nil {
		t.Fatal(err)
	}
	fromB := mustSet(t, b, "changed")
	if err := a.Apply(fromB); err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(gone); err != nil {
		t.Fatal(err)
	}

	want := "1:cleared 2:changed"
	if got := live(a); got != want {
		t.Fatalf("a reads %q, want %q", got, want)
	}
	if got := live(b); got != want {
		t.Fatalf("b reads %q, want %q", got, want)
	}
	// Values leaves the clearing out, so a caller that can only show values
	// shows the one there is — and Conflicted still says there was a choice.
	if got := a.Values(); len(got) != 1 || string(got[0]) != "changed" {
		t.Fatalf("Values = %q, want just the value", got)
	}
	if !a.Conflicted() {
		t.Fatal("a clear against a write does not report as a disagreement")
	}
	if _, ok := a.Value(); ok {
		t.Fatal("a clear against a write reads as one value")
	}

	// And a clear everybody has seen is simply no value.
	settled, err := b.Clear()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(settled); err != nil {
		t.Fatal(err)
	}
	if got := live(a); got != "2:cleared" {
		t.Fatalf("after both cleared, a reads %q", got)
	}
	if got, ok := a.Value(); ok {
		t.Fatalf("a cleared register holds %q", got)
	}
	if a.Conflicted() || a.Values() != nil {
		t.Fatalf("a cleared register conflicts: %q", live(a))
	}
}

// A register survives being written down and read back, disagreement included.
func TestAMultiRegisterSurvivesASnapshot(t *testing.T) {
	a, b := NewMultiRegister(1), NewMultiRegister(2)
	fromA := mustSet(t, a, "one")
	fromB := mustSet(t, b, "two")
	if err := a.Apply(fromB); err != nil {
		t.Fatal(err)
	}

	back, err := LoadMultiRegister(3, a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := live(back), live(a); got != want {
		t.Fatalf("reloaded reads %q, want %q", got, want)
	}
	if back.Site() != 3 {
		t.Fatalf("reloaded as site %d", back.Site())
	}
	// And a third replica writing on top of a reloaded disagreement settles it.
	fromBack := mustSet(t, back, "three")
	if err := b.Apply(fromA, fromBack); err != nil {
		t.Fatal(err)
	}
	if got := live(b); got != "3:three" {
		t.Fatalf("b reads %q", got)
	}
	if _, err := LoadMultiRegister(1, []byte("not a snapshot")); err == nil {
		t.Fatal("a snapshot that is not one loaded")
	}
}

// A map holds whatever an applied operation names, so everything read here has
// to be read defensively: what cannot be understood is left out rather than
// guessed at, and the register still reads.
func TestWhatAPeerCanWriteAndWhatItIsWorth(t *testing.T) {
	r := NewMultiRegister(1)
	mustSet(t, r, "mine")

	// Each of these is written by a replica that really is the site its key
	// names, and applied here as an operation. Writing them through r.Map()
	// instead would make them this replica's own writing under somebody else's
	// key — which is refused, and refused for a reason, but it is not what a
	// peer does and it would not test what this test is named for.
	bad := map[string][]byte{
		"not-a-site": encodeReading(map[crdt.SiteID]uint64{9: 1}, []byte("x"), false),
		"9":          nil,                // no vector at all
		"10":         {1},                // a vector of one entry, and nothing else
		"11":         {1, 5},             // a site, and no count
		"12":         {0, 2},             // no sites, and a flag that is not one
		"13":         {0, 0, 0},          // cleared, with bytes after it
		"14":         {2, 1, 1, 1, 1, 1}, // the same site twice
		"15":         {2, 5, 1, 1, 1, 1}, // sites out of order
		"16":         {0},                // no vector, and no flag
		// Well formed, and from a replica this one has never heard of: it stands,
		// because neither saw the other.
		"1000000000": encodeReading(map[crdt.SiteID]uint64{1000000000: 1}, nil, true),
		// Well formed, and saying it saw nothing and wrote nothing — which
		// everything dominates, so it is not live.
		"1000000001": encodeReading(nil, []byte("nowhere"), false),
	}
	for key, value := range bad {
		site := uint64(7) // for a key that names no site at all
		if n, err := strconv.ParseUint(key, 10, 64); err == nil {
			site = n
		}
		peer := NewMultiRegister(crdt.SiteID(site))
		op, err := peer.Map().Set(key, value)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Apply(op); err != nil {
			t.Fatal(err)
		}
	}
	// Everything unreadable is left out; the one well-formed stranger stands
	// beside this replica's own writing, because neither saw the other.
	if got := live(r); got != "1:mine 1000000000:cleared" {
		t.Fatalf("with a peer's rubbish applied, the register reads %q", got)
	}
	if got, ok := r.Value(); ok {
		t.Fatalf("Value = %q with two live readings", got)
	}

	// Writing again settles it, rubbish and all.
	mustSet(t, r, "still mine")
	if got := live(r); got != "1:still mine" {
		t.Fatalf("after writing, the register reads %q", got)
	}
}

// The encoding is canonical: one vector has one spelling, so two replicas that
// computed the same vector hold the same bytes and a snapshot can be compared
// byte for byte.
func TestTheEncodingIsCanonical(t *testing.T) {
	vector := map[crdt.SiteID]uint64{7: 2, 1: 5, 300: 1}
	first := encodeReading(vector, []byte("v"), false)
	// The same vector built in another order encodes the same, because a Go map
	// has no order and this one is sorted before it is written.
	other := map[crdt.SiteID]uint64{300: 1, 1: 5, 7: 2}
	if second := encodeReading(other, []byte("v"), false); !bytes.Equal(first, second) {
		t.Fatalf("one vector encoded two ways: %x and %x", first, second)
	}
	got, value, cleared, ok := decodeReading(first)
	if !ok || cleared || string(value) != "v" || len(got) != 3 ||
		got[7] != 2 || got[1] != 5 || got[300] != 1 {
		t.Fatalf("round trip = %v, %q, %v, %v", got, value, cleared, ok)
	}
	empty, value, cleared, ok := decodeReading(encodeReading(nil, nil, false))
	if !ok || cleared || len(empty) != 0 || value != nil {
		t.Fatalf("an empty vector round trips as %v, %q, %v, %v", empty, value, cleared, ok)
	}
}

// dominates is the whole of the merge, so it is asserted directly as well as
// through the replicas.
func TestWhatDominatesWhat(t *testing.T) {
	v := func(pairs ...uint64) map[crdt.SiteID]uint64 {
		out := map[crdt.SiteID]uint64{}
		for i := 0; i < len(pairs); i += 2 {
			out[crdt.SiteID(pairs[i])] = pairs[i+1]
		}
		return out
	}
	cases := []struct {
		name string
		a, b map[crdt.SiteID]uint64
		want bool
	}{
		{"further along the same line", v(1, 2), v(1, 1), true},
		{"behind on the same line", v(1, 1), v(1, 2), false},
		{"the same", v(1, 1), v(1, 1), false},
		{"neither saw the other", v(1, 1), v(2, 1), false},
		{"saw everything and more", v(1, 1, 2, 1), v(1, 1), true},
		{"saw everything and nothing more", v(1, 1), v(1, 1, 2, 1), false},
		{"ahead here and behind there", v(1, 2, 2, 1), v(1, 1, 2, 2), false},
		{"nothing dominates nothing", nil, nil, false},
		{"something dominates nothing", v(1, 1), nil, true},
		{"nothing dominates nothing at all", nil, v(1, 1), false},
	}
	for _, c := range cases {
		if got := dominates(c.a, c.b); got != c.want {
			t.Errorf("%s: dominates(%v, %v) = %v, want %v", c.name, c.a, c.b, got, c.want)
		}
	}
}

// With no clock left a replica writes nothing and says so.
func TestAMultiRegisterWithNoClockLeft(t *testing.T) {
	r := NewMultiRegister(1)
	mustSet(t, r, "value")
	top := crdt.MapOp{Kind: crdt.MapSet, ID: crdt.ID{Site: 9, Seq: 1},
		Clock: crdt.MaxClock, Key: "seed", Value: []byte("x")}
	if err := r.Apply(top); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Set([]byte("another")); err == nil {
		t.Fatal("writing with no clock left was accepted")
	}
	if _, err := r.Clear(); err == nil {
		t.Fatal("clearing with no clock left was accepted")
	}
}

// Whatever order the operations arrive in, and however often, every replica
// reads the same disagreement — asserted on byte-equal snapshots, which is
// stronger than an equal reading.
func TestAMultiRegisterConverges(t *testing.T) {
	for seed := range uint64(200) {
		rng := rand.New(rand.NewPCG(seed, seed))
		const replicas = 4
		regs := make([]*MultiRegister, replicas)
		for i := range regs {
			regs[i] = NewMultiRegister(crdt.SiteID(i + 1))
		}
		inbox := make([][]crdt.MapOp, replicas)

		for round := range 30 {
			i := rng.IntN(replicas)
			// Deliver a random part of the inbox, sometimes twice.
			if n := len(inbox[i]); n > 0 {
				take := 1 + rng.IntN(n)
				ops := inbox[i][:take]
				inbox[i] = inbox[i][take:]
				if err := regs[i].Apply(ops...); err != nil {
					t.Fatalf("seed %d: %v", seed, err)
				}
				if rng.IntN(3) == 0 {
					if err := regs[i].Apply(ops...); err != nil {
						t.Fatalf("seed %d: duplicate delivery: %v", seed, err)
					}
				}
			}
			var op crdt.MapOp
			var err error
			if rng.IntN(5) == 0 {
				op, err = regs[i].Clear()
			} else {
				op, err = regs[i].Set([]byte(fmt.Sprintf("r%d.%d", i, round)))
			}
			if err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
			for j := range regs {
				if j != i {
					inbox[j] = append(inbox[j], op)
				}
			}
		}

		// Deliver everything that is left, in a shuffled order.
		for i := range regs {
			rest := inbox[i]
			rng.Shuffle(len(rest), func(a, b int) { rest[a], rest[b] = rest[b], rest[a] })
			if err := regs[i].Apply(rest...); err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
		}
		want := regs[0].Snapshot()
		for i, r := range regs[1:] {
			if got := r.Snapshot(); !bytes.Equal(got, want) {
				t.Fatalf("seed %d: replica %d holds a different state\n%s\nagainst\n%s",
					seed, i+2, live(r), live(regs[0]))
			}
		}
		// And the reading is one value or a disagreement, never nothing at all,
		// because every replica wrote at least once.
		if len(regs[0].Readings()) == 0 {
			t.Fatalf("seed %d: nothing is live after %d writes", seed, 30)
		}
	}
}

// A disagreement is read in a settled order whatever order the writes arrived
// in, so two replicas showing it to two people show the same list.
func TestADisagreementIsReadInASettledOrder(t *testing.T) {
	var ops []crdt.MapOp
	for site := 1; site <= 5; site++ {
		r := NewMultiRegister(crdt.SiteID(site))
		ops = append(ops, mustSet(t, r, fmt.Sprint("v", site)))
	}
	want := "1:v1 2:v2 3:v3 4:v4 5:v5"

	rng := rand.New(rand.NewPCG(3, 3))
	for trial := range 50 {
		shuffled := append([]crdt.MapOp(nil), ops...)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		r := NewMultiRegister(9)
		if err := r.Apply(shuffled...); err != nil {
			t.Fatal(err)
		}
		if got := live(r); got != want {
			t.Fatalf("trial %d reads %q, want %q", trial, got, want)
		}
		values := r.Values()
		sorted := sort.SliceIsSorted(values, func(i, j int) bool {
			return bytes.Compare(values[i], values[j]) < 0
		})
		if !sorted {
			t.Fatalf("trial %d: values are %q, which is not the site order", values, values)
		}
	}
}

// A multi-value register is a map, so it can be a part of a document beside
// everything else it holds — and it syncs the way every part here does, by
// telling a peer what it has and taking back what it is missing.
func TestAMultiRegisterIsAPartOfADocument(t *testing.T) {
	ada, grace := crdt.NewComposite(1), crdt.NewComposite(2)
	part := crdt.Part{Kind: crdt.PartMap, Name: "title"}

	adaMap, err := ada.Map(part.Name)
	if err != nil {
		t.Fatal(err)
	}
	graceMap, err := grace.Map(part.Name)
	if err != nil {
		t.Fatal(err)
	}
	adaTitle, graceTitle := MultiRegisterOf(adaMap), MultiRegisterOf(graceMap)

	// Neither can see the other, and each names the document.
	if _, err := adaTitle.Set([]byte("On rivers")); err != nil {
		t.Fatal(err)
	}
	if _, err := graceTitle.Set([]byte("Rivers")); err != nil {
		t.Fatal(err)
	}

	// Sync by version vector rather than by carrying the operations about.
	fromAda := adaTitle.OpsSince(graceTitle.Version())
	fromGrace := graceTitle.OpsSince(adaTitle.Version())
	if len(fromAda) == 0 || len(fromGrace) == 0 {
		t.Fatalf("nothing to send: %d and %d", len(fromAda), len(fromGrace))
	}
	if err := graceTitle.Apply(fromAda...); err != nil {
		t.Fatal(err)
	}
	if err := adaTitle.Apply(fromGrace...); err != nil {
		t.Fatal(err)
	}

	want := "1:On rivers 2:Rivers"
	if got := live(adaTitle); got != want {
		t.Fatalf("ada reads %q, want %q", got, want)
	}
	if got := live(graceTitle); got != want {
		t.Fatalf("grace reads %q, want %q", got, want)
	}
	// Nothing is left to send once both have everything.
	if got := adaTitle.OpsSince(graceTitle.Version()); len(got) != 0 {
		t.Fatalf("%d operations still owed after a full exchange", len(got))
	}
}

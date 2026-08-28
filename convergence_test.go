package crdt

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// The convergence tests are the package's acceptance gate. A text CRDT is only
// worth anything if replicas that saw the same operations agree, whatever order
// those operations arrived in, so the property is exercised against randomised
// edit and delivery schedules rather than a handful of chosen cases.
//
// Randomness is seeded explicitly: a failure names the seed that produced it and
// the run reproduces exactly, on any platform, including js/wasm.

// alphabet is deliberately mixed-width: a bug in rune handling shows up as a
// divergence rather than as a silently mangled byte.
var alphabet = []rune("abcde \n\té世🌍")

// simulation is a set of replicas plus an unreliable network: messages sit in
// per-replica inboxes and are delivered late, out of order, and more than once.
type simulation struct {
	t     *testing.T
	rng   *rand.Rand
	docs  []*Doc
	inbox [][]Op
}

func newSimulation(t *testing.T, seed uint64, replicas int) *simulation {
	t.Helper()
	s := &simulation{
		t:     t,
		rng:   rand.New(rand.NewPCG(seed, 0x5eed)),
		docs:  make([]*Doc, replicas),
		inbox: make([][]Op, replicas),
	}
	for i := range s.docs {
		s.docs[i] = New(SiteID(i + 1))
	}
	return s
}

// edit performs one random local change on a replica and queues the result for
// everyone else.
func (s *simulation) edit(i int) {
	s.t.Helper()
	d := s.docs[i]
	var ops []Op
	var err error
	switch {
	case d.Len() == 0 || s.rng.IntN(3) != 0:
		text := make([]rune, 1+s.rng.IntN(4))
		for j := range text {
			text[j] = alphabet[s.rng.IntN(len(alphabet))]
		}
		ops, err = d.Insert(s.rng.IntN(d.Len()+1), string(text))
	default:
		pos := s.rng.IntN(d.Len())
		ops, err = d.Delete(pos, 1+s.rng.IntN(d.Len()-pos))
	}
	if err != nil {
		s.t.Fatalf("replica %d: %v", i, err)
	}
	s.broadcast(i, ops)
}

func (s *simulation) broadcast(from int, ops []Op) {
	for i := range s.docs {
		if i != from {
			s.inbox[i] = append(s.inbox[i], ops...)
		}
	}
}

// deliver hands a replica a random slice of its inbox, shuffled, occasionally
// duplicating an operation. Anything not delivered stays queued.
func (s *simulation) deliver(i int) {
	s.t.Helper()
	queued := s.inbox[i]
	if len(queued) == 0 {
		return
	}
	n := 1 + s.rng.IntN(len(queued))
	batch := append([]Op{}, queued[:n]...)
	s.inbox[i] = queued[n:]
	s.rng.Shuffle(len(batch), func(a, b int) { batch[a], batch[b] = batch[b], batch[a] })
	if s.rng.IntN(4) == 0 {
		batch = append(batch, batch[s.rng.IntN(len(batch))])
	}
	if err := s.docs[i].Apply(batch...); err != nil {
		s.t.Fatalf("replica %d: Apply: %v", i, err)
	}
}

// settle delivers everything still queued, which is what "eventually" means.
func (s *simulation) settle() {
	s.t.Helper()
	for i := range s.docs {
		for len(s.inbox[i]) > 0 {
			s.deliver(i)
		}
	}
}

// assertConverged is the property under test: identical text, identical state,
// nothing left waiting.
func (s *simulation) assertConverged(seed uint64) {
	s.t.Helper()
	want := s.docs[0].String()
	wantSnapshot := string(s.docs[0].Snapshot())
	for i, d := range s.docs {
		if got := d.String(); got != want {
			s.t.Fatalf("seed %d: replica %d text = %q, replica 0 text = %q", seed, i, got, want)
		}
		if got := string(d.Snapshot()); got != wantSnapshot {
			s.t.Fatalf("seed %d: replica %d agrees on the text but not on the state", seed, i)
		}
		if got := d.Pending(); got != 0 {
			s.t.Fatalf("seed %d: replica %d still holds %d undeliverable operations", seed, i, got)
		}
		if !d.Version().Equal(s.docs[0].Version()) {
			s.t.Fatalf("seed %d: replica %d version = %v, replica 0 version = %v",
				seed, i, d.Version(), s.docs[0].Version())
		}
	}
}

// TestConvergence runs many randomised sessions: replicas edit concurrently while
// the network delivers late, out of order, and with duplicates. Once delivery
// catches up, every replica must hold the same document.
func TestConvergence(t *testing.T) {
	for seed := range uint64(300) {
		s := newSimulation(t, seed, 2+int(seed%3))
		for range 12 {
			for i := range s.docs {
				for range 1 + s.rng.IntN(3) {
					s.edit(i)
				}
				if s.rng.IntN(2) == 0 {
					s.deliver(i)
				}
			}
		}
		s.settle()
		s.assertConverged(seed)
	}
}

// TestConvergenceAcrossSnapshot checks that a replica which joins late by
// loading a snapshot is indistinguishable from one that replayed the history:
// it must keep converging with the others as editing continues.
func TestConvergenceAcrossSnapshot(t *testing.T) {
	for seed := range uint64(100) {
		s := newSimulation(t, seed, 3)
		for range 6 {
			for i := range s.docs {
				s.edit(i)
			}
		}
		s.settle()
		s.assertConverged(seed)

		// A fourth replica joins from replica 0's snapshot and joins the session.
		joined, err := Load(SiteID(len(s.docs)+1), s.docs[0].Snapshot())
		if err != nil {
			t.Fatalf("seed %d: Load: %v", seed, err)
		}
		s.docs = append(s.docs, joined)
		s.inbox = append(s.inbox, nil)

		for range 6 {
			for i := range s.docs {
				s.edit(i)
				if s.rng.IntN(2) == 0 {
					s.deliver(i)
				}
			}
		}
		s.settle()
		s.assertConverged(seed)
	}
}

// TestOrderIndependence applies one fixed set of operations in many different
// orders. Every ordering must produce byte-identical state — the strongest form
// of the convergence property, with the edit schedule held constant.
func TestOrderIndependence(t *testing.T) {
	source := newSimulation(t, 42, 3)
	for range 8 {
		for i := range source.docs {
			source.edit(i)
		}
		source.settle()
	}
	all := source.docs[0].OpsSince(nil)
	if len(all) < 20 {
		t.Fatalf("only %d operations to permute; the fixture is too small to be a test", len(all))
	}

	want := ""
	wantSnapshot := ""
	rng := rand.New(rand.NewPCG(7, 7))
	for trial := range 200 {
		shuffled := append([]Op{}, all...)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		d := New(99)
		// Feed them one at a time: the pending buffer, not the batch, has to do
		// the reordering work.
		for _, op := range shuffled {
			if err := d.Apply(op); err != nil {
				t.Fatalf("trial %d: Apply: %v", trial, err)
			}
		}
		if d.Pending() != 0 {
			t.Fatalf("trial %d: %d operations never became applicable", trial, d.Pending())
		}
		if trial == 0 {
			want, wantSnapshot = d.String(), string(d.Snapshot())
			continue
		}
		if got := d.String(); got != want {
			t.Fatalf("trial %d: text = %q, want %q", trial, got, want)
		}
		if got := string(d.Snapshot()); got != wantSnapshot {
			t.Fatalf("trial %d: state differs from the first ordering", trial)
		}
	}
	if want != source.docs[0].String() {
		t.Fatalf("replaying the history gives %q, want %q", want, source.docs[0].String())
	}
}

// TestConcurrentTypingConverges covers the case CRDT papers single out: two
// people typing whole words at the same position at the same time. Both replicas
// must reach the same text, every character must survive, and — the property the
// Lamport clock exists to protect — neither person's run may be chopped up by
// the other's. A character typed after its neighbour always carries a higher
// clock than anything the typist had seen, so a concurrent insertion cannot sort
// between the two.
func TestConcurrentTypingConverges(t *testing.T) {
	for seed := range uint64(50) {
		rng := rand.New(rand.NewPCG(seed, 3))
		a, b := New(1), New(2)
		seedOps := insert(t, a, 0, "start end")
		apply(t, b, seedOps)

		var fromA, fromB []Op
		for i, r := range "ALPHA" {
			fromA = append(fromA, insert(t, a, 6+i, string(r))...)
		}
		for i, r := range "beta" {
			fromB = append(fromB, insert(t, b, 6+i, string(r))...)
		}
		rng.Shuffle(len(fromB), func(i, j int) { fromB[i], fromB[j] = fromB[j], fromB[i] })
		apply(t, a, fromB)
		apply(t, b, fromA)

		if a.String() != b.String() {
			t.Fatalf("seed %d: diverged: %q vs %q", seed, a.String(), b.String())
		}
		if got, want := a.Len(), len("start end")+len("ALPHA")+len("beta"); got != want {
			t.Fatalf("seed %d: Len() = %d, want %d: characters were lost", seed, got, want)
		}
		for _, run := range []string{"ALPHA", "beta"} {
			if !strings.Contains(a.String(), run) {
				t.Fatalf("seed %d: %q was split apart in %q: a concurrent insertion "+
					"sorted between two characters typed one after the other",
					seed, run, a.String())
			}
		}
	}
}

// TestRunSurvivesManyConcurrentWriters raises the pressure: several replicas each
// type a run of their own at the same position, all concurrently. Every run must
// still come out whole, and every replica must agree on the order they ended up
// in.
func TestRunSurvivesManyConcurrentWriters(t *testing.T) {
	const writers = 5
	for seed := range uint64(50) {
		rng := rand.New(rand.NewPCG(seed, 11))
		docs := make([]*Doc, writers)
		for i := range docs {
			docs[i] = New(SiteID(i + 1))
		}
		seedOps := insert(t, docs[0], 0, "<>")
		for _, d := range docs[1:] {
			apply(t, d, seedOps)
		}

		runs := []string{"aaaa", "BBBB", "cccc", "DDDD", "eeee"}
		batches := make([][]Op, writers)
		for i, d := range docs {
			// Each writer types its run one character at a time, so every
			// character but the first is a descendant of the one before it.
			for j, r := range runs[i] {
				batches[i] = append(batches[i], insert(t, d, 1+j, string(r))...)
			}
		}
		for i, d := range docs {
			for j, batch := range batches {
				if i == j {
					continue
				}
				shuffled := append([]Op{}, batch...)
				rng.Shuffle(len(shuffled), func(a, b int) {
					shuffled[a], shuffled[b] = shuffled[b], shuffled[a]
				})
				apply(t, d, shuffled)
			}
		}
		want := docs[0].String()
		for i, d := range docs {
			if got := d.String(); got != want {
				t.Fatalf("seed %d: replica %d = %q, replica 0 = %q", seed, i, got, want)
			}
		}
		for _, run := range runs {
			if !strings.Contains(want, run) {
				t.Fatalf("seed %d: %q was split apart in %q", seed, run, want)
			}
		}
	}
}

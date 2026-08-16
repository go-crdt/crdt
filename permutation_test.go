package crdt

import (
	"math/rand/v2"
	"testing"
)

// Randomised delivery samples the space of orderings; it does not cover it. This
// file closes the gap for small histories by enumerating *every* permutation of
// the operations, which is where an ordering bug in the integration rule shows
// up first: the counterexamples in the CRDT literature are all four to eight
// operations long.

// permute calls fn with every ordering of ops (Heap's algorithm), reusing one
// slice, so fn must not retain it. It is written over the element type because
// every replicated structure in the package needs it.
func permute[T any](ops []T, fn func([]T)) {
	n := len(ops)
	work := append([]T{}, ops...)
	counters := make([]int, n)
	fn(work)
	for i := 0; i < n; {
		if counters[i] >= i {
			counters[i] = 0
			i++
			continue
		}
		j := 0
		if i%2 == 1 {
			j = counters[i]
		}
		work[i], work[j] = work[j], work[i]
		fn(work)
		counters[i]++
		i = 0
	}
}

func TestPermuteCoversEveryOrdering(t *testing.T) {
	ops := make([]Op, 4)
	for i := range ops {
		ops[i] = Op{Kind: OpDelete, ID: ID{Site: SiteID(i + 1), Seq: 1}, Clock: 1, Target: ID{Seq: 1}}
	}
	seen := map[[4]SiteID]bool{}
	count := 0
	permute(ops, func(p []Op) {
		count++
		var key [4]SiteID
		for i, op := range p {
			key[i] = op.ID.Site
		}
		seen[key] = true
	})
	if count != 24 || len(seen) != 24 {
		t.Fatalf("permute produced %d orderings, %d of them distinct; want 24 and 24", count, len(seen))
	}
}

// history builds a small concurrent edit history and returns every operation in
// it. Replicas edit blind, then exchange everything, then edit blind again, so
// the operations form concurrent branches on top of a shared prefix — the shape
// that makes an integration rule fail.
func history(t *testing.T, rng *rand.Rand, sites, editsPerPhase int) []Op {
	t.Helper()
	docs := make([]*Doc, sites)
	for i := range docs {
		docs[i] = New(SiteID(i + 1))
	}
	var all []Op
	for phase := range 2 {
		phaseOps := make([][]Op, sites)
		for i, d := range docs {
			for range editsPerPhase {
				var ops []Op
				var err error
				if d.Len() > 0 && rng.IntN(4) == 0 {
					ops, err = d.Delete(rng.IntN(d.Len()), 1)
				} else {
					ops, err = d.Insert(rng.IntN(d.Len()+1), string(rune('a'+rng.IntN(4))))
				}
				if err != nil {
					t.Fatalf("phase %d, site %d: %v", phase, i, err)
				}
				phaseOps[i] = append(phaseOps[i], ops...)
				all = append(all, ops...)
			}
		}
		for i, d := range docs {
			for j, ops := range phaseOps {
				if i != j {
					apply(t, d, ops)
				}
			}
		}
	}
	return all
}

// TestEveryOrderingConverges is the exhaustive form of the convergence property:
// for each small history, applying its operations in every possible order must
// produce byte-identical state.
func TestEveryOrderingConverges(t *testing.T) {
	rng := rand.New(rand.NewPCG(2026, 8))
	for trial := range 40 {
		ops := history(t, rng, 3, 1) // three sites, two phases: six operations
		if len(ops) != 6 {
			t.Fatalf("trial %d: history has %d operations, want 6", trial, len(ops))
		}
		want := ""
		permute(ops, func(p []Op) {
			d := New(99)
			for _, op := range p {
				if err := d.Apply(op); err != nil {
					t.Fatalf("trial %d: Apply: %v", trial, err)
				}
			}
			if d.Pending() != 0 {
				t.Fatalf("trial %d: %d operations never became applicable", trial, d.Pending())
			}
			got := string(d.Snapshot())
			if want == "" {
				want = got
				return
			}
			if got != want {
				var order []ID
				for _, op := range p {
					order = append(order, op.ID)
				}
				t.Fatalf("trial %d: delivery order %v produced different state; text %q",
					trial, order, d.String())
			}
		})
	}
}

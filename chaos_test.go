package crdt

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// A hundred people on one file, and a network doing its worst.
//
// The property tests deliver late, out of order and twice. That is a bad
// network. This is a broken one: replicas that cannot reach each other for a
// while, batches that are never delivered at all, a replica that stops and
// comes back from a snapshot, a replica that throws away what it was holding
// back, and edits that land on the same characters at the same moment from
// dozens of places.
//
// The claim is the one the whole thing rests on, and it is checked two ways:
// once the network is allowed to heal, every replica holds a byte-identical
// snapshot, and each of them agrees with itself — the index against the list it
// indexes, the counters against the blocks they count.
//
// Byte-identical rather than equal-reading is the point. Two replicas can show
// the same text and disagree about which write produced it, and that difference
// surfaces later, as a divergence nobody can explain.

// chaosSize lets this be cranked past what CI should spend. The default is what
// runs everywhere.
func chaosSize(t *testing.T, name string, or int) int {
	t.Helper()
	if raw := os.Getenv(name); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			t.Fatalf("%s=%q is not a size", name, raw)
		}
		return n
	}
	return or
}

// The parts a document is made of here: one of each kind, because the three
// merge differently and a composite is where they meet.
var (
	chaosText = Part{Kind: PartText, Name: "file:main.tex"}
	chaosList = Part{Kind: PartList, Name: "chat"}
	chaosMap  = Part{Kind: PartMap, Name: "cells"}
)

// a replica, and what the chaos does to it.
type chaosReplica struct {
	site SiteID
	doc  *Composite
	// inbox is what has been sent to it and not yet delivered. The network
	// decides when, in what order, and whether at all.
	inbox []PartOps
	// down is a replica nobody can reach and which makes no edits.
	down bool
	// restarts and drops are counted so the test can say the chaos happened
	// rather than assume it.
	restarts, drops int
}

func (r *chaosReplica) text(t *testing.T) *Doc {
	t.Helper()
	d, err := r.doc.Text(chaosText.Name)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// edit makes one change of one of the three kinds.
func (r *chaosReplica) edit(t *testing.T, rng *rand.Rand) []PartOps {
	t.Helper()
	switch rng.IntN(10) {
	case 0, 1:
		l, err := r.doc.List(chaosList.Name)
		if err != nil {
			t.Fatal(err)
		}
		n := l.Len()
		if n > 0 && rng.IntN(4) == 0 {
			ops, err := l.Delete(rng.IntN(n), 1)
			if err != nil {
				t.Fatal(err)
			}
			return []PartOps{{Part: chaosList, List: ops}}
		}
		ops, err := l.Insert(rng.IntN(n+1), []byte(fmt.Sprintf("%d says %d", r.site, rng.IntN(1000))))
		if err != nil {
			t.Fatal(err)
		}
		return []PartOps{{Part: chaosList, List: ops}}
	case 2, 3:
		m, err := r.doc.Map(chaosMap.Name)
		if err != nil {
			t.Fatal(err)
		}
		// A small pool of keys, so a hundred replicas collide on them.
		key := fmt.Sprintf("cell:%d", rng.IntN(12))
		if rng.IntN(5) == 0 {
			op, err := m.Delete(key)
			if err != nil {
				t.Fatal(err)
			}
			return []PartOps{{Part: chaosMap, Map: []MapOp{op}}}
		}
		op, err := m.Set(key, []byte(strconv.Itoa(rng.IntN(1000))))
		if err != nil {
			t.Fatal(err)
		}
		return []PartOps{{Part: chaosMap, Map: []MapOp{op}}}
	default:
		d := r.text(t)
		n := d.Len()
		// Everybody types near the front, so the same characters are contended.
		at := rng.IntN(min(n, 24) + 1)
		if span := min(n-at, 4); n > 8 && span > 0 && rng.IntN(5) == 0 {
			ops, err := d.Delete(at, 1+rng.IntN(span))
			if err != nil {
				t.Fatal(err)
			}
			return []PartOps{{Part: chaosText, Text: ops}}
		}
		ops, err := d.Insert(at, string(rune('a'+rng.IntN(26))))
		if err != nil {
			t.Fatal(err)
		}
		return []PartOps{{Part: chaosText, Text: ops}}
	}
}

func TestAHundredClientsAndABrokenNetwork(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos takes longer than -short allows")
	}
	replicas := chaosSize(t, "CRDT_CHAOS_REPLICAS", 100)
	rounds := chaosSize(t, "CRDT_CHAOS_ROUNDS", 120)
	seeds := chaosSize(t, "CRDT_CHAOS_SEEDS", 3)

	for seed := range uint64(seeds) {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			runChaos(t, seed, replicas, rounds)
		})
	}
}

func runChaos(t *testing.T, seed uint64, replicas, rounds int) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 0xc4a05))
	start := time.Now()

	all := make([]*chaosReplica, replicas)
	for i := range all {
		all[i] = &chaosReplica{site: SiteID(i + 1), doc: NewComposite(SiteID(i + 1))}
	}

	// A partition is a split of the replicas into groups that cannot reach each
	// other. It lasts a few rounds and then heals.
	group := make([]int, replicas) // everybody in group 0 to begin with
	partitionUntil := 0

	var edits, delivered, lost, duplicated, gossips int
	for round := range rounds {
		if round >= partitionUntil && rng.IntN(6) == 0 {
			// Split into two or three groups for a while.
			groups := 2 + rng.IntN(2)
			for i := range group {
				group[i] = rng.IntN(groups)
			}
			partitionUntil = round + 3 + rng.IntN(8)
		} else if round == partitionUntil {
			for i := range group {
				group[i] = 0
			}
		}

		for i, r := range all {
			// A replica goes down, and comes back.
			if r.down {
				if rng.IntN(8) == 0 {
					r.down = false
				}
				continue
			}
			if rng.IntN(200) == 0 {
				r.down = true
				continue
			}

			// It edits, once or a few times.
			for range 1 + rng.IntN(3) {
				batches := r.edit(t, rng)
				edits++
				for j, other := range all {
					if j == i || other.down || group[j] != group[i] {
						continue
					}
					if rng.IntN(20) == 0 {
						lost++ // this one simply never arrives
						continue
					}
					other.inbox = append(other.inbox, batches...)
				}
			}

			// Some of what it was sent arrives, shuffled, sometimes twice.
			if n := len(r.inbox); n > 0 && rng.IntN(2) == 0 {
				take := 1 + rng.IntN(n)
				batch := append([]PartOps(nil), r.inbox[:take]...)
				r.inbox = r.inbox[take:]
				rng.Shuffle(len(batch), func(a, b int) { batch[a], batch[b] = batch[b], batch[a] })
				if rng.IntN(4) == 0 {
					batch = append(batch, batch[rng.IntN(len(batch))])
					duplicated++
				}
				if err := r.doc.Apply(batch...); err != nil {
					t.Fatalf("seed %d round %d: replica %d: Apply: %v", seed, round, r.site, err)
				}
				delivered += take
			}

			// It throws away what it is holding back. Safe, because a parked
			// operation is not in the version vector: whoever has it sends it
			// again. This is here to prove that under chaos rather than in a
			// test written to suit it.
			if r.doc.Pending() > 0 && rng.IntN(30) == 0 {
				r.drops += r.doc.DropPending()
			}

			// It stops and comes back from a snapshot, as a process restart
			// would. Anything in flight to it is lost with the inbox.
			if rng.IntN(150) == 0 {
				back, err := LoadComposite(r.site, r.doc.Snapshot())
				if err != nil {
					t.Fatalf("seed %d round %d: replica %d: reload: %v", seed, round, r.site, err)
				}
				r.doc = back
				r.inbox = nil
				r.restarts++
			}

			// Two replicas that can reach each other reconcile directly, which
			// is what heals everything the network lost.
			if rng.IntN(3) == 0 {
				j := rng.IntN(replicas)
				if j != i && !all[j].down && group[j] == group[i] {
					if err := r.doc.Apply(all[j].doc.OpsSince(r.doc.Version())...); err != nil {
						t.Fatalf("seed %d round %d: gossip %d<-%d: %v", seed, round, r.site, all[j].site, err)
					}
					gossips++
				}
			}
		}
	}

	// The network heals: everybody is up, nobody is partitioned, and every
	// replica reconciles with every other until nothing moves.
	for _, r := range all {
		r.down = false
		r.inbox = nil
	}
	for pass := 0; ; pass++ {
		if pass > replicas+8 {
			t.Fatalf("seed %d: %d passes and still not settled", seed, pass)
		}
		moved := false
		for i, r := range all {
			for j, other := range all {
				if i == j {
					continue
				}
				owed := other.doc.OpsSince(r.doc.Version())
				if len(owed) == 0 {
					continue
				}
				if err := r.doc.Apply(owed...); err != nil {
					t.Fatalf("seed %d: healing %d<-%d: %v", seed, r.site, other.site, err)
				}
				moved = true
			}
		}
		if !moved {
			break
		}
	}

	// --- what all of that has to have left behind
	want := all[0].doc.Snapshot()
	for _, r := range all[1:] {
		if got := r.doc.Snapshot(); !bytes.Equal(got, want) {
			t.Fatalf("seed %d: replica %d holds a different document: %d bytes against %d",
				seed, r.site, len(got), len(want))
		}
	}
	for _, r := range all {
		if n := r.doc.Pending(); n != 0 {
			t.Fatalf("seed %d: replica %d still holds %d operations back", seed, r.site, n)
		}
		if !r.doc.Version().Equal(all[0].doc.Version()) {
			t.Fatalf("seed %d: replica %d promises something different", seed, r.site)
		}
		check(t, r.text(t), fmt.Sprintf("seed %d, replica %d", seed, r.site))
		if t.Failed() {
			t.FailNow()
		}
	}

	// And the chaos has to have actually happened, or this proves nothing.
	restarts, drops := 0, 0
	for _, r := range all {
		restarts += r.restarts
		drops += r.drops
	}
	d := all[0].text(t)
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	t.Logf("%d replicas, %d rounds in %s: %d edits, %d batches delivered, %d lost, "+
		"%d duplicated, %d reconciliations, %d restarts, %d operations dropped while parked",
		replicas, rounds, time.Since(start).Round(time.Millisecond),
		edits, delivered, lost, duplicated, gossips, restarts, drops)
	t.Logf("the document they agree on: %d characters, %d tombstones, %d bytes as a snapshot",
		d.Len(), d.Tombstones(), len(want))
	if lost == 0 || duplicated == 0 || restarts == 0 || gossips == 0 {
		t.Errorf("the network was not broken enough: lost=%d duplicated=%d restarts=%d gossips=%d",
			lost, duplicated, restarts, gossips)
	}
	if d.Len() == 0 {
		t.Error("everybody agrees on an empty document, which proves nothing")
	}
}

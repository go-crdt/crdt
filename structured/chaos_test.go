package structured

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"testing"

	"github.com/go-crdt/crdt"
)

// The property tests drive two to four replicas over a network that reorders and
// duplicates. That leaves a gap: no structured type has ever seen a partition, a
// replica reloaded from its own snapshot mid-session, or a participant that joins
// after the editing started. This harness closes it, over every type at once.
//
// Sizes come from the environment so continuous integration stays quick and a
// deliberate run can be made very large.

func chaosSize(env string, def int) int {
	if s := os.Getenv(env); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// chaosSize0 is chaosSize for a knob that may legitimately be switched off.
func chaosSize0(env string, def int) int {
	if s := os.Getenv(env); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			return n
		}
	}
	return def
}

// chaosNet is the property-test network plus a holdback: a partitioned replica's
// edits are invisible to everyone until it rejoins, at which point they all
// arrive at once.
type chaosNet struct {
	*network
	held [][]crdt.PartOps
	cut  []bool
	// log is every batch that has reached the network, kept so a replica built
	// from a snapshot can be caught up. A snapshot does not carry the
	// operations a replica has parked — they have had no effect on its state,
	// so they are not in its version vector — and [crdt.List.DropPending] spells
	// out why that is safe: a peer asked what this replica is missing sends it
	// again. A transport that never re-sends leaves the new replica short of
	// them for good, which is a fault in the harness rather than in the merge.
	log []crdt.PartOps
}

func newChaosNet(n int) *chaosNet {
	return &chaosNet{network: newNetwork(n), held: make([][]crdt.PartOps, n), cut: make([]bool, n)}
}

// resync is what a peer does for a replica that has just been built from a
// snapshot: it sends everything again. Anything already applied is a duplicate
// and changes nothing.
func (nw *chaosNet) resync(i int) {
	nw.inbox[i] = append(nw.inbox[i], nw.log...)
}

func (nw *chaosNet) send(from int, batches []crdt.PartOps) {
	if nw.cut[from] {
		nw.held[from] = append(nw.held[from], batches...)
		return
	}
	nw.log = append(nw.log, batches...)
	nw.broadcast(from, batches)
}

// heal readmits a replica: everything it wrote while cut off is broadcast now.
func (nw *chaosNet) heal(i int) {
	nw.cut[i] = false
	if len(nw.held[i]) > 0 {
		nw.log = append(nw.log, nw.held[i]...)
		nw.broadcast(i, nw.held[i])
		nw.held[i] = nil
	}
}

// grow makes room for a replica that joins mid-session.
func (nw *chaosNet) grow(from int) int {
	// The joiner starts from a peer's snapshot, so it must also inherit what that
	// peer has not applied yet; otherwise those batches reach nobody and the
	// session could not converge for reasons that are the harness's fault.
	nw.inbox = append(nw.inbox, append([]crdt.PartOps{}, nw.inbox[from]...))
	nw.held = append(nw.held, nil)
	nw.cut = append(nw.cut, false)
	return len(nw.inbox) - 1
}

func (nw *chaosNet) settleAll(t *testing.T, rng *rand.Rand, docs []editor) {
	t.Helper()
	for i := range docs {
		nw.heal(i)
	}
	// Healing broadcasts, which fills inboxes, so drain until nothing moves.
	for {
		moved := false
		for i := range docs {
			if len(nw.inbox[i]) > 0 {
				nw.deliver(t, rng, docs, i)
				moved = true
			}
		}
		if !moved {
			return
		}
	}
}

// TestChaosStructured puts every structured type through partitions, churn and
// late joiners at a scale the property tests never reach, and demands
// byte-identical state once the network settles.
func TestChaosStructured(t *testing.T) {
	replicas := chaosSize("CRDT_STRUCTURED_CHAOS_REPLICAS", 12)
	rounds := chaosSize("CRDT_STRUCTURED_CHAOS_ROUNDS", 40)
	seeds := chaosSize("CRDT_STRUCTURED_CHAOS_SEEDS", 2)
	// Restarts and late joiners are on unless set to zero, which switches that
	// source of disorder off so a failure can be attributed to one of them
	// rather than guessed at. The rates themselves are fixed, and drawn from a
	// stream of their own, so switching one off moves no other draw.
	churn := chaosSize0("CRDT_STRUCTURED_CHAOS_CHURN", 6)
	join := chaosSize0("CRDT_STRUCTURED_CHAOS_JOIN", 10)

	for _, dt := range docTypes() {
		t.Run(dt.name, func(t *testing.T) {
			t.Parallel()
			// A seed is a subtest of its own: one that fails must not hide
			// how the others fared, which is what a rate is read from.
			for seed := range uint64(seeds) {
				t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
					chaosSession(t, dt, seed, replicas, rounds, churn, join)
				})
			}
		})
	}
}

func chaosSession(t *testing.T, dt docType, seed uint64, replicas, rounds, churn, join int) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 0xc4a05))
	// Disorder draws from its own stream, so switching a source off leaves every
	// other draw where it was and a failure can be attributed rather than
	// guessed at.
	dis := rand.New(rand.NewPCG(seed, 0xd1502))
	docs := make([]editor, replicas)
	for i := range docs {
		docs[i] = dt.mk(crdt.SiteID(i + 1))
	}
	nw := newChaosNet(replicas)
	site := crdt.SiteID(replicas + 1)

	for round := range rounds {
		// Cut a few replicas off, and readmit some of those already cut.
		for i := range docs {
			healRoll, cutRoll := dis.IntN(3) == 0, dis.IntN(12) == 0
			switch {
			case nw.cut[i] && healRoll:
				nw.heal(i)
			case !nw.cut[i] && cutRoll:
				nw.cut[i] = true
			}
		}

		for i := range docs {
			for range 1 + rng.IntN(3) {
				if batches := docs[i].edit(t, rng); len(batches) > 0 {
					nw.send(i, batches)
				}
			}
			if !nw.cut[i] && rng.IntN(2) == 0 {
				nw.deliver(t, rng, docs, i)
			}
		}

		// Churn: a replica dies and comes back from its own snapshot. Its queued
		// inbox survives the restart, as a real client's would not — which is the
		// harsher case, since the reloaded state must accept them all.
		churnRoll, churnAt := dis.IntN(6) == 0, dis.IntN(len(docs))
		if churn > 0 && replicas > 1 && churnRoll {
			i := churnAt % len(docs)
			if !nw.cut[i] {
				// A reopened document takes a fresh site, as Automerge gives a
				// document a new actor on every open: the operations it wrote
				// before the restart are in the snapshot under the old one, and
				// a site it has already used must never be issued twice.
				docs[i] = dt.load(t, site, docs[i].Snapshot())
				site++
				nw.resync(i)
			}
		}

		// A participant joins late, seeded from a live peer.
		joinRoll, joinFrom := dis.IntN(4) == 0, dis.IntN(len(docs))
		if join > 0 && round > 0 && round < rounds-5 && joinRoll && len(docs) < 4*replicas {
			from := joinFrom % len(docs)
			docs = append(docs, dt.load(t, site, docs[from].Snapshot()))
			nw.resync(nw.grow(from))
			site++
		}
	}

	nw.settleAll(t, rng, docs)

	want := docs[0].Snapshot()
	for i, d := range docs {
		if d.Pending() != 0 {
			t.Fatalf("%s seed %d: replica %d still has %d operations pending", dt.name, seed, i, d.Pending())
		}
		if !bytes.Equal(d.Snapshot(), want) {
			t.Fatalf("%s seed %d: replica %d diverged (%d bytes vs %d)", dt.name, seed, i, len(d.Snapshot()), len(want))
		}
		if !d.Version().Equal(docs[0].Version()) {
			t.Fatalf("%s seed %d: replica %d has a different version", dt.name, seed, i)
		}
	}
	if testing.Verbose() {
		fmt.Printf("%-12s seed %d: %d replicas converged on %d bytes\n", dt.name, seed, len(docs), len(want))
	}
}

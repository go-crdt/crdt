package crdt

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"strconv"
	"strings"
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

// a client, and what the chaos does to it.
//
// A client is not simply up or down. It arrives, sometimes long after the
// session started; it loses its connection and goes on typing, because that is
// the whole reason for a CRDT; it comes back and has to be reconciled in both
// directions; and sometimes it closes the tab and never returns, leaving its
// work in the document.
type chaosReplica struct {
	site SiteID
	doc  *Composite
	// inbox is what has been sent to it and not yet delivered. The network
	// decides when, in what order, and whether at all.
	inbox []PartOps
	// state is where this client is in its life.
	state clientState
	// what happened to it, counted so the test can say the chaos happened
	// rather than assume it.
	restarts, drops, offlineEdits, textDeletes int
}

type clientState int

const (
	// connected: sending, receiving, editing.
	connected clientState = iota
	// disconnected: still editing, and nobody can hear it. Its work piles up
	// locally and lands when it comes back — which is the case this whole
	// package exists for.
	disconnected
	// gone: it closed the tab. What it wrote stays in the document; it will
	// never be reconciled again, so it is not asked to agree at the end.
	gone
	// unborn: a client that has not joined yet. It has no document.
	unborn
)

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
			r.textDeletes++
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
	// How many of them edit in a round, and how many peers an edit is told to.
	// Every replica holds the whole document, so at ten thousand clients the
	// document has to stay a document rather than become ten thousand copies of
	// everything everybody ever typed.
	editors := chaosSize(t, "CRDT_CHAOS_EDITORS", min(replicas, 100))
	fanout := chaosSize(t, "CRDT_CHAOS_FANOUT", min(replicas-1, 8))
	churn := chaosSize(t, "CRDT_CHAOS_CHURN", max(1, replicas/50))
	// How often everybody gives back what everybody has certainly seen. Only
	// the maps give anything back — see the note in text.go — and this is here
	// so that the next attempt at the other two has something to fail against
	// rather than a harness that has never collected anything.
	collectEvery := chaosSize(t, "CRDT_CHAOS_COLLECT_EVERY", 15)

	for seed := range uint64(seeds) {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			runChaos(t, seed, replicas, rounds, editors, fanout, churn, collectEvery)
		})
	}
}

func runChaos(t *testing.T, seed uint64, replicas, rounds, editors, fanout, churn, collectEvery int) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 0xc4a05))
	start := time.Now()

	// The hub. Ten thousand people on one file do not each hold a connection
	// to the other nine thousand nine hundred and ninety-nine: they go through
	// a server, which is what collab is. It arbitrates nothing — it applies
	// what it is sent and hands back what the asker lacks — so it is a replica
	// like any other, and it is included in the agreement at the end.
	hub := &chaosReplica{site: SiteID(replicas + 1), doc: NewComposite(SiteID(replicas + 1)), state: connected}

	// A quarter of them are there from the beginning; the rest arrive during
	// the session, which is what a shared document looks like.
	all := make([]*chaosReplica, replicas)
	present := max(1, replicas/4)
	for i := range all {
		all[i] = &chaosReplica{site: SiteID(i + 1), state: unborn}
	}
	for i := range present {
		all[i].doc = NewComposite(all[i].site)
		all[i].state = connected
	}

	group := make([]int, replicas)
	partitionUntil := 0

	var edits, delivered, lost, duplicated, gossips int
	var joins, disconnects, reconnects, departures int
	var collections, collected int

	for round := range rounds {
		if round >= partitionUntil && rng.IntN(6) == 0 {
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

		// --- clients arriving, leaving, dropping out and coming back
		//
		// Proportional to how many there are: a session of ten thousand in
		// which three of them join per round would be a session of three
		// hundred with nine thousand seven hundred spectators who never
		// arrived, which is not what is being tested.
		for range churn {
			i := rng.IntN(replicas)
			r := all[i]
			switch r.state {
			case unborn:
				// A late joiner is welcomed with a snapshot from the hub,
				// which is how a real one joins: it does not replay history.
				doc, err := LoadComposite(r.site, hub.doc.Snapshot())
				if err != nil {
					t.Fatalf("seed %d round %d: client %d joining: %v", seed, round, r.site, err)
				}
				r.doc, r.state = doc, connected
				joins++
			case connected:
				if rng.IntN(3) == 0 {
					r.state = disconnected
					r.inbox = nil // whatever was in flight to it is lost
					disconnects++
				} else if rng.IntN(25) == 0 {
					r.state = gone
					r.inbox = nil
					departures++
				}
			case disconnected:
				// It comes back and reconciles with the hub both ways: what it
				// missed, and what it wrote while nobody could hear it.
				if err := r.doc.Apply(hub.doc.OpsSince(r.doc.Version())...); err != nil {
					t.Fatalf("seed %d round %d: client %d catching up: %v", seed, round, r.site, err)
				}
				if err := hub.doc.Apply(r.doc.OpsSince(hub.doc.Version())...); err != nil {
					t.Fatalf("seed %d round %d: client %d pushing back: %v", seed, round, r.site, err)
				}
				r.state = connected
				reconnects++
			}
		}

		// --- the ones doing the work
		for _, i := range rng.Perm(replicas)[:editors] {
			r := all[i]
			if r.state == unborn || r.state == gone {
				continue
			}

			for range 1 + rng.IntN(3) {
				batches := r.edit(t, rng)
				edits++
				if r.state == disconnected {
					// Nobody hears it. This is the work that lands later.
					r.offlineEdits++
					continue
				}
				// To the hub, unless the message is lost or this client is on
				// the wrong side of a partition.
				if group[i] == 0 && rng.IntN(20) != 0 {
					hub.inbox = append(hub.inbox, batches...)
				} else {
					lost++
				}
				// And to a handful of peers directly, which is what keeps this
				// from being a test of one server.
				for range fanout {
					j := rng.IntN(replicas)
					other := all[j]
					if j == i || other.state != connected || group[j] != group[i] {
						continue
					}
					if rng.IntN(20) == 0 {
						lost++
						continue
					}
					other.inbox = append(other.inbox, batches...)
				}
			}

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
					t.Fatalf("seed %d round %d: client %d: Apply: %v", seed, round, r.site, err)
				}
				delivered += take
			}

			if r.doc.Pending() > 0 && rng.IntN(30) == 0 {
				r.drops += r.doc.DropPending()
			}

			// It stops and comes back from a snapshot, as a process restart
			// would. Anything in flight to it is lost with the inbox.
			if rng.IntN(150) == 0 {
				back, err := LoadComposite(r.site, r.doc.Snapshot())
				if err != nil {
					t.Fatalf("seed %d round %d: client %d reload: %v", seed, round, r.site, err)
				}
				r.doc = back
				r.inbox = nil
				r.restarts++
			}

			if r.state == connected && rng.IntN(3) == 0 {
				j := rng.IntN(replicas)
				if j != i && all[j].state == connected && group[j] == group[i] {
					if err := r.doc.Apply(all[j].doc.OpsSince(r.doc.Version())...); err != nil {
						t.Fatalf("seed %d round %d: gossip %d<-%d: %v", seed, round, r.site, all[j].site, err)
					}
					gossips++
				}
			}
		}

		// The hub takes everything it was sent, in whatever order it arrived.
		if n := len(hub.inbox); n > 0 {
			batch := hub.inbox
			hub.inbox = nil
			rng.Shuffle(len(batch), func(a, b int) { batch[a], batch[b] = batch[b], batch[a] })
			if err := hub.doc.Apply(batch...); err != nil {
				t.Fatalf("seed %d round %d: the hub: %v", seed, round, err)
			}
			delivered += n
		}

		// And clients pull from it, which is how everyone sees everyone.
		for range editors {
			r := all[rng.IntN(replicas)]
			if r.state != connected || r.doc == nil {
				continue
			}
			if err := r.doc.Apply(hub.doc.OpsSince(r.doc.Version())...); err != nil {
				t.Fatalf("seed %d round %d: client %d pulling: %v", seed, round, r.site, err)
			}
			gossips++
		}

		// The ones that are only reading still catch up.
		for range editors {
			r := all[rng.IntN(replicas)]
			if r.state != connected || len(r.inbox) == 0 {
				continue
			}
			batch := r.inbox
			r.inbox = nil
			rng.Shuffle(len(batch), func(a, b int) { batch[a], batch[b] = batch[b], batch[a] })
			if err := r.doc.Apply(batch...); err != nil {
				t.Fatalf("seed %d round %d: client %d catching up: %v", seed, round, r.site, err)
			}
			delivered += len(batch)
		}

		// --- everybody gives back what everybody has certainly seen
		//
		// The meet is taken over every replica that will be asked to agree at
		// the end. A floor at the meet is at or below every one of their
		// versions, so none of them can be below it. A replica that has not
		// joined is left out: it is welcomed with a snapshot and inherits
		// whatever the hub has given back with it.
		if collectEvery > 0 && round%collectEvery == collectEvery-1 {
			if stable, ok := chaosMeet(all, hub); ok {
				collections++
				for _, r := range all {
					if r.state == unborn {
						continue
					}
					collected += r.doc.Collect(stable)
				}
				collected += hub.doc.Collect(stable)
			}
		}
	}

	// --- the network heals, and everybody who is still here comes back
	var here []*chaosReplica
	hub.state = connected
	hub.inbox = nil
	here = append(here, hub)
	for _, r := range all {
		if r.state == unborn {
			continue // never joined; it has no document to agree with
		}
		r.state = connected
		r.inbox = nil
		here = append(here, r)
	}
	if len(here) < 2 {
		t.Fatalf("seed %d: only %d clients ever joined", seed, len(here))
	}

	// Healing through one collector rather than every pair: everybody sends to
	// it, then it sends to everybody. Two sweeps of n, not n squared — the
	// difference between ten thousand clients settling and not finishing. It is
	// enough: after the first sweep the collector holds everything anybody
	// holds, and after the second everyone holds what the collector does.
	collector := here[0]
	for _, r := range here[1:] {
		if err := collector.doc.Apply(r.doc.OpsSince(collector.doc.Version())...); err != nil {
			t.Fatalf("seed %d: collecting from %d: %v", seed, r.site, err)
		}
	}
	for _, r := range here[1:] {
		if err := r.doc.Apply(collector.doc.OpsSince(r.doc.Version())...); err != nil {
			t.Fatalf("seed %d: handing back to %d: %v", seed, r.site, err)
		}
	}

	// --- what all of that has to have left behind
	// The bytes when nobody collected, the contents when somebody did.
	//
	// A snapshot is canonical for a replica that has only ever been told
	// things, which is why this compares bytes at all. A map that collects
	// remembers the highest clock it collected under, and two replicas reach
	// that moment holding different records — one of them was disconnected and
	// had not heard a deletion yet — so they collect a little differently and
	// remember a different clock. What they hold is the same; the bookkeeping
	// of what they gave back is not, and that is what this stops comparing.
	if collectEvery > 0 {
		want := chaosContents(t, here[0])
		for _, r := range here[1:] {
			if got := chaosContents(t, r); got != want {
				t.Fatalf("seed %d: replica %d holds a different document: %d against %d",
					seed, r.site, len(got), len(want))
			}
		}
	} else {
		want := here[0].doc.Snapshot()
		for _, r := range here[1:] {
			if got := r.doc.Snapshot(); !bytes.Equal(got, want) {
				t.Fatalf("seed %d: replica %d holds a different document: %d bytes against %d",
					seed, r.site, len(got), len(want))
			}
		}
	}
	for _, r := range here {
		if n := r.doc.Pending(); n != 0 {
			t.Fatalf("seed %d: replica %d still holds %d operations back", seed, r.site, n)
		}
		if !r.doc.Version().Equal(here[0].doc.Version()) {
			t.Fatalf("seed %d: replica %d promises something different", seed, r.site)
		}
		check(t, r.text(t), fmt.Sprintf("seed %d, replica %d", seed, r.site))
		if t.Failed() {
			t.FailNow()
		}
	}

	// And the chaos has to have actually happened, or this proves nothing.
	restarts, drops, offline, deletes := 0, 0, 0, 0
	for _, r := range all {
		restarts += r.restarts
		drops += r.drops
		offline += r.offlineEdits
		deletes += r.textDeletes
	}
	d := here[0].text(t)
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	t.Logf("%d replicas, %d rounds in %s: %d edits, %d batches delivered, %d lost, "+
		"%d duplicated, %d reconciliations, %d restarts, %d operations dropped while parked",
		replicas, rounds, time.Since(start).Round(time.Millisecond),
		edits, delivered, lost, duplicated, gossips, restarts, drops)
	if collectEvery > 0 {
		if collections == 0 {
			t.Fatal("collection was asked for and never happened, so nothing about it was tested")
		}
		t.Logf("collected %d times, giving back %d records", collections, collected)
	}
	version, err := here[0].doc.Version().MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("clients: %d joined late, %d disconnections, %d reconnections, %d left for good, "+
		"%d edits made while disconnected; %d agreeing at the end",
		joins, disconnects, reconnects, departures, offline, len(here))
	t.Logf("the document they agree on: %d characters, %d tombstones, %d bytes as a snapshot, "+
		"%d bytes of version over %d sites; heap %d MiB; %d text deletions were made",
		d.Len(), d.Tombstones(), len(here[0].doc.Snapshot()), len(version), len(here[0].doc.Version()[chaosText]),
		mem.HeapAlloc>>20, deletes)
	if lost == 0 || duplicated == 0 || restarts == 0 || gossips == 0 {
		t.Errorf("the network was not broken enough: lost=%d duplicated=%d restarts=%d gossips=%d",
			lost, duplicated, restarts, gossips)
	}
	if joins == 0 || disconnects == 0 || reconnects == 0 || departures == 0 || offline == 0 {
		t.Errorf("the clients did not churn: joins=%d disconnects=%d reconnects=%d "+
			"departures=%d offline edits=%d",
			joins, disconnects, reconnects, departures, offline)
	}
	if d.Len() == 0 {
		t.Error("everybody agrees on an empty document, which proves nothing")
	}
	if deletes == 0 || d.Tombstones() == 0 {
		t.Errorf("nobody deleted anything: %d deletions, %d tombstones. A document "+
			"only ever added to is the easy half", deletes, d.Tombstones())
	}
}

// chaosMeet is what every replica that will be asked to agree has certainly
// seen: the element-wise minimum of their versions.
//
// A replica that has not joined is left out, because it holds nothing and the
// meet with nothing is nothing. It reports false when the answer is empty,
// which is a room where somebody has just arrived and nothing can be collected
// yet.
func chaosMeet(all []*chaosReplica, hub *chaosReplica) (CompositeVersion, bool) {
	out := hub.doc.Version()
	if len(out) == 0 {
		return nil, false
	}
	for _, r := range all {
		if r.state == unborn {
			continue
		}
		theirs := r.doc.Version()
		next := CompositeVersion{}
		for part, mine := range out {
			other, known := theirs[part]
			if !known {
				continue
			}
			shared := VersionVector{}
			for site, seq := range mine {
				if o := other[site]; o < seq {
					seq = o
				}
				if seq > 0 {
					shared[site] = seq
				}
			}
			if len(shared) > 0 {
				next[part] = shared
			}
		}
		out = next
		if len(out) == 0 {
			return nil, false
		}
	}
	return out, true
}

// chaosContents is everything a replica says, in one string: the text, the list
// and the map. It is what two replicas have to agree about once collecting has
// stopped their bytes from agreeing.
func chaosContents(t *testing.T, r *chaosReplica) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(r.text(t).String())
	b.WriteByte(0)
	l, err := r.doc.List(chaosList.Name)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range l.Values() {
		b.Write(v)
		b.WriteByte(1)
	}
	b.WriteByte(0)
	m, err := r.doc.Map(chaosMap.Name)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range m.Keys() {
		b.WriteString(k)
		b.WriteByte(2)
		v, _ := m.Get(k)
		b.Write(v)
		b.WriteByte(1)
	}
	return b.String()
}

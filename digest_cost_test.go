package crdt

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"testing"
	"time"
)

// What a digest of state would cost, measured against something this package
// already does at the same moment.
//
// go-crdt/crdt#123 wants a digest two replicas can compare, so that neither
// concludes it is caught up while holding different text. collab's
// TestHowOftenAReplicaWouldCompare measures the rate: an acknowledgement per
// participant per keystroke, against one join per participant per session. So
// the question here is only whether a WALK is affordable at the join rate, and
// the honest way to answer it is a ratio against Doc.Snapshot, which is what a
// fresh join already pays over the same blocks.
//
// A ratio, and not two absolute numbers, because this was measured on a machine
// with other work on it. Load inflates both arms; it does not change which is
// bigger. The arms are interleaved and their ORDER IS REVERSED half the time,
// so a drift over the run would show up as the two orders disagreeing.
//
// The digest is order-independent on purpose. Two replicas holding the same
// document need not hold the same AVL shape, and a Merkle combination of
// children would then differ where the documents agree -- so the combiner adds
// per-element hashes into a wide accumulator rather than chaining them. A real
// implementation wants a studied multiset hash (LtHash) rather than this
// stand-in; what is being measured is the walk, which is the part that scales.
//
// What it found, on an Apple M4 Max with other work on the machine:
//
//	visible chars   digest   snapshot   digest/snapshot   rejected combiner
//	        1 000   9.3 µs     4.7 µs         1.96x              44.7 µs
//	       10 000    92 µs      48 µs         1.88-1.93x          448 µs
//	      100 000   948 µs     444 µs         2.08-2.20x         4.56 ms
//
// So a walk costs about twice the snapshot a fresh join already pays over the
// same blocks. At the join rate collab measured -- one per participant per
// session -- that is affordable, and now known rather than guessed. At the
// acknowledgement rate it measured -- one per participant per KEYSTROKE -- it is
// not, and no implementation of this walk would make it so. That is the decision
// this file exists to support: compare where a replica concludes it is caught
// up, not on every acknowledgement.
//
// The last column is the construction that was rejected, kept measured rather
// than remembered: hashing each character separately and combining the results
// with an order-independent sum costs 4.8 times the walk, at every size. It was
// reached for to escape the AVL's shape, and is not needed, because the LIST is
// already in document order and every converged replica walks it the same way.
//
// Two other things this measurement decided, both of them corrections:
//
// An earlier accumulator used a big.Int and cost 20 to 27 times a snapshot --
// nearly all of it allocating and reducing per character rather than walking.
// Four lanes of wrapping addition is the 4.8x above.
//
// And the first version of Doc.Digest wrote its three fields in three calls of
// eight bytes, which cost 2.62 ms where one call of twenty-four costs 948 µs.
// Same bytes, same digest, nearly three times the time, and it was in code
// written the same hour as this table.
//
// What remains is one SHA-256 per visible character, and that is a floor rather
// than an inefficiency: a digest per RUN would be far cheaper and would not be
// canonical, because two replicas holding the same document need not have split
// their runs in the same places.

// digestOfVisible walks the document and combines a hash per visible character
// with its identity, independently of the order they are visited in.
func digestOfVisible(d *Doc) [4]uint64 {
	var acc [4]uint64
	var buf [24]byte
	for b := d.head.next; b != nil; b = b.next {
		if b.gone {
			continue
		}
		cursor := delCursor{b: b}
		for i, r := range b.text {
			if !cursor.at(i).IsRoot() {
				continue // deleted: not part of what a reader sees
			}
			id := b.idAt(i)
			binary.LittleEndian.PutUint64(buf[0:], uint64(id.Site))
			binary.LittleEndian.PutUint64(buf[8:], id.Seq)
			binary.LittleEndian.PutUint64(buf[16:], uint64(r))
			sum := sha256.Sum256(buf[:])
			// Four lanes of wrapping addition: order-independent, allocation
			// free, and the shape LtHash uses. A big.Int accumulator was tried
			// first and cost 20 to 27 times a Snapshot, nearly all of it in
			// allocating and reducing per character rather than in the walk.
			for lane := range acc {
				acc[lane] += binary.LittleEndian.Uint64(sum[lane*8:])
			}
		}
	}
	return acc
}

// digestOfVisible is the construction that was REJECTED, kept so that the
// reason stays measurable rather than remembered: it hashes each character
// separately and combines the results with wrapping addition, which is
// independent of the order it visits them in.
//
// That independence is what it was reached for, and it is not needed: the list
// is already canonical, because two replicas that have converged hold the same
// visible characters in the same order. What it costs is measured below.
func docOfSize(t *testing.T, n int) *Doc {
	t.Helper()
	d := New(1)
	runes := make([]rune, n)
	for i := range runes {
		runes[i] = rune('a' + i%26)
	}
	if _, err := d.Insert(0, string(runes)); err != nil {
		t.Fatal(err)
	}
	return d
}

func median(xs []time.Duration) time.Duration {
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	return xs[len(xs)/2]
}

// TestWhatADigestWalkCostsAgainstASnapshot reports the ratio, and fails only if
// the measurement contradicts itself between the two orders.
func TestWhatADigestWalkCostsAgainstASnapshot(t *testing.T) {
	const rounds = 11
	for _, n := range []int{1_000, 10_000, 100_000} {
		d := docOfSize(t, n)

		// Both orders, so a drift across the run cannot be read as an effect.
		ratio := map[string]float64{}
		for _, order := range []string{"digest first", "snapshot first"} {
			var digest, snap, combined []time.Duration
			for range rounds {
				timeCombined := func() {
					start := time.Now()
					got := digestOfVisible(d)
					combined = append(combined, time.Since(start))
					if got == ([4]uint64{}) {
						t.Fatal("empty digest")
					}
				}
				timeDigest := func() {
					start := time.Now()
					got := d.Digest()
					digest = append(digest, time.Since(start))
					if got == (Digest{}) {
						t.Fatal("empty digest")
					}
				}
				timeSnap := func() {
					start := time.Now()
					got := d.Snapshot()
					snap = append(snap, time.Since(start))
					if len(got) == 0 {
						t.Fatal("empty snapshot")
					}
				}
				if order == "digest first" {
					timeDigest()
					timeSnap()
					timeCombined()
				} else {
					timeCombined()
					timeSnap()
					timeDigest()
				}
			}
			md, ms, mc := median(digest), median(snap), median(combined)
			ratio[order] = float64(md) / float64(ms)
			t.Logf("%7d chars, %-14s: digest %8v, snapshot %8v, digest/snapshot %.2fx, rejected combiner %8v (%.1fx the digest)",
				n, order, md, ms, ratio[order], mc, float64(mc)/float64(md))
		}

		a, b := ratio["digest first"], ratio["snapshot first"]
		hi, lo := a, b
		if hi < lo {
			hi, lo = lo, hi
		}
		// The two orders must agree about the ratio. They need not agree
		// closely -- this machine is busy -- but a factor of two apart would
		// mean the run drifted more than the effect being measured, and the
		// number would not be worth reporting.
		if hi > 2*lo {
			t.Errorf("%d chars: the two orders disagree (%.2fx vs %.2fx); the run drifted more than the effect", n, a, b)
		}
	}
}

// TestADigestOverVisibleTextSurvivesAPurge is a property of [Doc.Purge] first and
// of the digest second: Purge discards only runs whose every character is already
// deleted, so what a READER sees is unchanged, and so is anything computed from it.
//
// It is here because it is what makes a digest of state possible at all. A digest
// over history could not survive a purge -- the operations are gone, and purge.go
// says so -- and a digest over identities would compare equal on the very forgery
// it exists to catch, since that forgery wears identities we already hold. Visible
// text is the one thing that is both stable under Purge and different when two
// replicas disagree.
func TestADigestOverVisibleTextSurvivesAPurge(t *testing.T) {
	d := New(1)
	// Two inserts, the second INSIDE the first, so the document holds more than
	// one run and one of them can be deleted whole. Deleting a stretch inside a
	// single run leaves that run partly alive, and Purge takes nothing -- which
	// is what the first version of this fixture did, and why the check below
	// that Purge discarded something is not decoration.
	if _, err := d.Insert(0, "keepthat"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Insert(4, "GONE"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Delete(4, 8); err != nil { // exactly the inserted run
		t.Fatal(err)
	}
	before := digestOfVisible(d)
	text := d.String()

	if n := d.Purge(); n == 0 {
		t.Fatal("Purge discarded nothing; the fixture does not exercise it")
	}
	if got := d.String(); got != text {
		t.Fatalf("Purge changed the visible text: %q, want %q", got, text)
	}
	if after := digestOfVisible(d); after != before {
		t.Errorf("the digest moved across a Purge:\n before %x\n after  %x", before, after)
	}
}

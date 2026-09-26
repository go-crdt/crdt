package crdt

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"
)

// What a digest of state costs, measured against something this package already
// does at the same moment.
//
// go-crdt/crdt#123 asks for a digest two replicas can compare, so that neither
// concludes it is caught up while holding different text. collab measured the
// RATE (TestHowOftenAReplicaWouldCompare): one acknowledgement per participant
// per keystroke, against one join per participant per session. This measures the
// COST against [Doc.Snapshot], which walks the same blocks at the moment a fresh
// join already pays for it.
//
// What it found, on an Apple M4 Max with other work on the machine (ns/op):
//
//	visible chars    digest   snapshot   digest/snapshot   rejected combiner
//	        1 000     9 559      5 225        1.83x                 45 833
//	       10 000    97 255     48 561        2.00x                455 847
//	      100 000   945 541    458 415        2.06x              4 534 268
//
// So a walk costs about twice the snapshot a fresh join already pays over the
// same blocks. At the join rate that is affordable, and now known rather than
// guessed. At the acknowledgement rate it is not, and no implementation of this
// walk would make it so. That is the decision this file supports: compare where
// a replica concludes it is caught up, not on every message.
//
// The last column is the construction that was REJECTED, kept measured rather
// than remembered. It hashes each character separately and combines the results
// with wrapping addition, which is independent of the order it visits them in --
// and that independence is what it was reached for, to escape the shape of the
// AVL index. It is not needed, because the LIST is already in document order and
// every converged replica walks it the same way. It costs 4.8 times the walk, at
// every size.
//
// Two other things the measuring decided, both of them corrections:
//
// An earlier accumulator used a big.Int and cost 20 to 27 times a snapshot,
// nearly all of it allocating and reducing per character rather than walking.
// Four lanes of wrapping addition is the last column above.
//
// And the first version of [Doc.Digest] wrote its three fields in three calls of
// eight bytes, which cost 2.62 ms where one call of twenty-four costs 946 µs.
// Same bytes, same digest, nearly three times the time -- in code written the
// same hour as this table.

// digestOfVisible is that rejected construction, kept so the reason stays
// measurable.
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
				continue
			}
			id := b.idAt(i)
			binary.LittleEndian.PutUint64(buf[0:], uint64(id.Site))
			binary.LittleEndian.PutUint64(buf[8:], id.Seq)
			binary.LittleEndian.PutUint64(buf[16:], uint64(r))
			sum := sha256.Sum256(buf[:])
			for lane := range acc {
				acc[lane] += binary.LittleEndian.Uint64(sum[lane*8:])
			}
		}
	}
	return acc
}

func docOfSize(t testing.TB, n int) *Doc {
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

// BenchmarkDigest prices the walk against the snapshot and against the rejected
// combiner.
//
// A benchmark and not a test, which was learned from CI. The first version timed
// each arm with time.Now() and compared medians. On Windows, whose clock ticks
// about every 15.6 ms, every sample under that read as exactly 0s -- so the
// ratios came out +Inf and NaN, and the test failed for a reason that had
// nothing to do with the code. A measurement finer than its clock is not a slow
// measurement, it is an absent one, and testing's own loop is what repeats an
// arm until it is long enough to see.
//
// Run it with:
//
//	go test -run xxx -bench Digest
func BenchmarkDigest(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 100_000} {
		d := docOfSize(b, n)
		b.Run(fmt.Sprint(n, "/digest"), func(b *testing.B) {
			for range b.N {
				if d.Digest() == (Digest{}) {
					b.Fatal("empty digest")
				}
			}
		})
		b.Run(fmt.Sprint(n, "/snapshot"), func(b *testing.B) {
			for range b.N {
				if len(d.Snapshot()) == 0 {
					b.Fatal("empty snapshot")
				}
			}
		})
		b.Run(fmt.Sprint(n, "/rejected-combiner"), func(b *testing.B) {
			for range b.N {
				if digestOfVisible(d) == ([4]uint64{}) {
					b.Fatal("empty digest")
				}
			}
		})
	}
}

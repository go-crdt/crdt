package crdt

import (
	"fmt"
	"testing"
	"time"
)

// What actually grows with the number of clients.
//
// A replica's memory is a function of the document, not of how many people are
// editing it — ten thousand clients each hold one document, and that document
// is the same size whoever holds it.
//
// One thing is not. A version vector carries an entry per site that has ever
// written, it is exchanged on every sync, and OpsSince walks it. So this is
// measured rather than assumed, and the numbers are recorded in
// docs/performance.md so that a change to them is noticed rather than
// discovered by somebody with ten thousand users.
//
// Measured on an Apple M4 Max, Go 1.26.4:
//
//	  sites   version   per site   OpsSince, nothing owed   snapshot
//	    100     201 B      2.0 B                   0.97 us      1 017 B
//	  1 000   2 876 B      2.9 B                  11.04 us     12 647 B
//	 10 000  29 876 B      3.0 B                  95.67 us    129 649 B
//	100 000 383 495 B      3.8 B                 821.16 us  1 550 509 B
//
// It is linear, at about ten nanoseconds a site, which is what a map lookup
// costs. What that means for a deployment is worth saying plainly: a server
// answering ten thousand clients "you are up to date" once a second spends
// about a second of one core doing it, and each of those answers reads a
// thirty-kilobyte version. That is the cost of asking "what am I missing" with
// per-site vectors; it is not a defect, and it is what a protocol above this
// would batch or shard.
func TestWhatGrowsWithTheNumberOfClients(t *testing.T) {
	for _, sites := range []int{100, 1000, 10000, 100000} {
		d := New(1)
		// Every site writes one character, which is the cheapest way to be in
		// the version.
		for s := range sites {
			op := Op{
				Kind:   OpInsert,
				ID:     ID{Site: SiteID(s + 2), Seq: 1},
				Origin: ID{},
				Clock:  uint64(s + 1),
				Char:   'x',
			}
			if err := d.Apply(op); err != nil {
				t.Fatalf("%d sites: %v", sites, err)
			}
		}
		v := d.Version()
		encoded, err := v.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}

		// What a server pays per client that asks "what am I missing" when the
		// answer is "nothing".
		const calls = 200
		start := time.Now()
		for range calls {
			if owed := must(d.OpsSince(v)); len(owed) != 0 {
				t.Fatalf("%d owed when nothing should be", len(owed))
			}
		}
		each := time.Since(start) / calls

		// And the snapshot, which a joiner is welcomed with.
		snap := d.Snapshot()
		t.Logf("%7d sites: version %8d bytes (%.1f each), OpsSince with nothing owed %8s, snapshot %d bytes",
			sites, len(encoded), float64(len(encoded))/float64(sites), each, len(snap))
	}
}

// And the same question of a composite, which is what a real document is: the
// version is per part, so a document of p parts and s sites carries p*s entries.
func TestAVersionIsPerPartAndPerSite(t *testing.T) {
	for _, parts := range []int{1, 4, 16} {
		c := NewComposite(1)
		for p := range parts {
			d, err := c.Text(fmt.Sprintf("part%d", p))
			if err != nil {
				t.Fatal(err)
			}
			for s := range 1000 {
				op := Op{Kind: OpInsert, ID: ID{Site: SiteID(s + 2), Seq: 1},
					Clock: uint64(s + 1), Char: 'x'}
				if err := d.Apply(op); err != nil {
					t.Fatal(err)
				}
			}
		}
		encoded, err := c.Version().MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%2d parts x 1000 sites: version %d bytes", parts, len(encoded))
	}
}

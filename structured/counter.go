package structured

import (
	"encoding/binary"
	"errors"
	"strconv"

	"github.com/go-crdt/crdt"
)

// ErrNoChange is returned by an operation that would change nothing, so that a
// caller never has to decide whether a zero-valued operation is safe to send.
var ErrNoChange = errors.New("structured: the operation would change nothing")

// A Counter is a number any number of replicas may add to at once, offline, in
// any delivery order, and every replica ends up holding the same total.
//
// # Why a register is not one
//
// The obvious way to share a number is a last-writer-wins register: read it,
// add one, write it back. That loses. Two replicas holding 7 both write 8, one
// of the two writes wins, and a vote is missed — the outcome does not depend on
// what either replica meant, only on which write sorted higher. The mistake is
// not in the register; it is that "add one" is not a value, and writing a value
// cannot express it.
//
// # How this one works
//
// A counter is a map keyed by site, and a replica writes only its own key. Its
// key holds everything that replica has ever added and everything it has ever
// taken away, as two numbers that only ever grow. The total is the sum of the
// first minus the sum of the second, over every key.
//
// Nothing ever conflicts, because no two replicas write the same key, and a
// replica's own key is a number it computes from what it alone has done. That
// is the whole of it: the [crdt.Map] underneath is doing no merging at all, and
// the counter is correct because concurrent additions are concurrent writes to
// different keys.
//
// # Why two numbers rather than one
//
// A single signed total per site would be shorter and would work. Two are kept
// because each of them only ever increases, which is what makes a site's key
// safe to read back from any snapshot — an older copy of it can only be behind,
// never wrong in the other direction — and because a tally of what was added
// against what was taken away is what a vote, a stock level or a budget is
// actually asking for. [Counter.Added] and [Counter.Removed] return them.
type Counter struct {
	m *crdt.Map
}

// NewCounter returns a counter this site can add to.
func NewCounter(site crdt.SiteID) *Counter { return &Counter{m: crdt.NewMap(site)} }

// CounterOf reads a map as a counter, for a map that is a part of a
// [crdt.Composite].
func CounterOf(m *crdt.Map) *Counter { return &Counter{m: m} }

// Map returns the map underneath, which is what is snapshotted and what
// operations are applied to.
func (c *Counter) Map() *crdt.Map { return c.m }

// Site returns the replica this counter adds as.
func (c *Counter) Site() crdt.SiteID { return c.m.Site() }

// Value returns the total: everything every replica has added, less everything
// every replica has taken away.
func (c *Counter) Value() int64 {
	added, removed := c.tally()
	return added - removed
}

// Added returns everything every replica has ever added, ignoring what was
// taken away. Removed returns the other half.
func (c *Counter) Added() int64 { added, _ := c.tally(); return added }

// Removed returns everything every replica has ever taken away.
func (c *Counter) Removed() int64 { _, removed := c.tally(); return removed }

func (c *Counter) tally() (added, removed int64) {
	for _, key := range c.m.Keys() {
		// Keys returns the live keys, so Get answers for every one of them.
		value, _ := c.m.Get(key)
		up, down, ok := decodeTally(value)
		if !ok {
			// A key this replica cannot read is a key a future version wrote,
			// or one a peer wrote by hand. It is skipped rather than guessed
			// at: a counter that invents a number is worse than one that is
			// behind.
			continue
		}
		added += up
		removed += down
	}
	return added, removed
}

// Add moves the counter by delta, which may be negative. Adding zero is not an
// operation and returns [ErrNoChange].
func (c *Counter) Add(delta int64) (crdt.MapOp, error) {
	if delta == 0 {
		return crdt.MapOp{}, ErrNoChange
	}
	key := strconv.FormatUint(uint64(c.m.Site()), 10)
	var up, down int64
	if value, ok := c.m.Get(key); ok {
		// A key this replica cannot read is one it is about to replace, and
		// replacing it with a number derived from a guess would make the total
		// wrong on every other replica too. Its own key is the one key it is
		// entitled to overwrite, so what it cannot read it starts again.
		up, down, _ = decodeTally(value)
	}
	if delta > 0 {
		up += delta
	} else {
		down -= delta
	}
	return c.m.Set(key, encodeTally(up, down))
}

// encodeTally and decodeTally write the two halves of one site's contribution.
// Both only ever grow, so both are unsigned.
func encodeTally(up, down int64) []byte {
	out := binary.AppendUvarint(nil, uint64(up))
	return binary.AppendUvarint(out, uint64(down))
}

func decodeTally(value []byte) (up, down int64, ok bool) {
	u, n := binary.Uvarint(value)
	if n <= 0 {
		return 0, 0, false
	}
	d, m := binary.Uvarint(value[n:])
	if m <= 0 || n+m != len(value) {
		return 0, 0, false
	}
	// Both halves are read back as int64 and must not have wrapped: a peer can
	// send any bytes, and a negative half would make the total nonsense.
	if u > 1<<62 || d > 1<<62 {
		return 0, 0, false
	}
	return int64(u), int64(d), true
}

// Apply takes operations from a peer.
func (c *Counter) Apply(ops ...crdt.MapOp) error { return c.m.Apply(ops...) }

// Version returns what this replica has seen.
func (c *Counter) Version() crdt.VersionVector { return c.m.Version() }

// OpsSince returns the operations a peer at vv has not seen.
func (c *Counter) OpsSince(vv crdt.VersionVector) []crdt.MapOp { return c.m.OpsSince(vv) }

// Snapshot returns the counter as bytes.
func (c *Counter) Snapshot() []byte { return c.m.Snapshot() }

// LoadCounter reads a snapshot back, as the given site.
func LoadCounter(site crdt.SiteID, snapshot []byte) (*Counter, error) {
	m, err := crdt.LoadMap(site, snapshot)
	if err != nil {
		return nil, err
	}
	return &Counter{m: m}, nil
}

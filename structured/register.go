package structured

import "github.com/go-crdt/crdt"

// A Register is a single last-writer-wins value: any number of replicas may
// write it at once, offline, in any delivery order, and every replica ends up
// holding the same value. Writes are ordered by the (Lamport clock, site) total
// order the crdt package defines — a maximum, so it does not depend on the order
// the writes arrive in — and the tie a shared clock would leave is broken by
// site, deterministically, without a wall clock ever being read.
//
// It is the degenerate keyed map: one key, and nothing to merge that [crdt.Map]
// does not already merge. The whole of a Register is that restriction, so it
// carries no merge logic of its own and inherits the map's convergence,
// idempotence and wire format unchanged.
//
// A Register is not safe for concurrent use. The zero Register is unusable —
// construct one with [NewRegister] or [LoadRegister].
type Register struct{ m *crdt.Map }

// registerKey is the single key a Register keeps its value under. It is empty
// because there is nothing to distinguish: a valid, and the shortest, UTF-8 map
// key.
const registerKey = ""

// NewRegister returns an empty register that issues operations as site. Every
// replica writing to a register concurrently must pass a distinct site.
func NewRegister(site crdt.SiteID) *Register { return &Register{m: crdt.NewMap(site)} }

// Get returns the current value and whether one is set. A cleared register reads
// as absent. The value is a copy.
func (r *Register) Get() ([]byte, bool) { return r.m.Get(registerKey) }

// Set writes value and returns the operation describing it, already applied;
// send it to every peer. The register copies value.
func (r *Register) Set(value []byte) (crdt.MapOp, error) { return r.m.Set(registerKey, value) }

// Clear removes the value and returns the operation describing it. Like a map
// deletion it keeps the write's clock, so a concurrent Set resolves against it
// by the same order rather than always winning.
func (r *Register) Clear() (crdt.MapOp, error) { return r.m.Delete(registerKey) }

// Apply integrates operations from peers, tolerating duplicates and reordering.
func (r *Register) Apply(ops ...crdt.MapOp) error { return r.m.Apply(ops...) }

// Version returns what this replica holds, to hand a peer that will send back
// what it is missing; see [Register.OpsSince].
func (r *Register) Version() crdt.VersionVector { return r.m.Version() }

// OpsSince returns the operations this replica holds that vv does not.
func (r *Register) OpsSince(vv crdt.VersionVector) []crdt.MapOp { return r.m.OpsSince(vv) }

// Snapshot encodes the whole register, for a joining peer or for persistence.
func (r *Register) Snapshot() []byte { return r.m.Snapshot() }

// LoadRegister rebuilds a register from a snapshot, to be written as site.
func LoadRegister(site crdt.SiteID, snapshot []byte) (*Register, error) {
	m, err := crdt.LoadMap(site, snapshot)
	if err != nil {
		return nil, err
	}
	return &Register{m: m}, nil
}

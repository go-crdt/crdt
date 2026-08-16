// Package crdt implements a conflict-free replicated data type for plain text:
// a replicated character sequence that any number of replicas may edit
// concurrently, offline, and in any delivery order, and that is guaranteed to
// converge to the same text on every replica.
//
// The sequence is an RGA (Replicated Growable Array). Every character carries a
// unique [ID] and the ID of the character it was inserted after, so insertions
// are placed relative to content rather than to an index that concurrent edits
// would invalidate. Deletions are tombstones, so an insertion may still refer to
// a character another replica has already removed.
//
// # Determinism
//
// The package never reads the wall clock and never draws random numbers, so the
// same [Doc] compiled to js/wasm behaves exactly as it does on a server. Replica
// identity is injected by the caller as a [SiteID]; see [DeriveSiteID] for a
// deterministic way to obtain one from bytes the caller already has.
//
// # Two counters
//
// Each operation carries two numbers, and they are not the same thing:
//
//   - [ID].Seq is a per-site counter that increases by exactly one per operation
//     the site issues. It gives the operation its identity and lets a
//     [VersionVector] describe, exactly, which operations a replica holds.
//   - [Op].Clock is a Lamport timestamp, bumped past every clock a replica has
//     seen. It orders concurrent insertions at the same position, and it is what
//     makes RGA integration convergent.
//
// Folding the two into one counter would create gaps in a site's own sequence,
// and a version vector cannot describe a sequence with gaps.
//
// # Delivery
//
// [Doc.Apply] tolerates duplicates, and it tolerates operations that arrive
// before the operations they depend on: an operation that is not yet ready is
// buffered and integrated as soon as its dependencies land. Callers therefore do
// not need an ordered transport, only an eventually-complete one.
//
// # A replicated map
//
// [Map] is the same machinery applied to a key-value map, for what an editor
// keeps beside its text — a spreadsheet of cells, a table of settings. The last
// write to a key wins, under the same (clock, site) order, and a deleted key
// keeps its clock so that an older write arriving later cannot resurrect it. It
// shares [ID], [VersionVector], [ErrMalformed] and the wire conventions with
// [Doc] and nothing else, so neither structure can disturb the other.
package crdt

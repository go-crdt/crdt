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
// # Where a concurrent word lands, which convergence does not decide
//
// Converging on the same text is not the same as converging on the text a
// person would have written, and RGA is known to leave one case open. When two
// people type at the same place, and one of them typed twice there without the
// two runs being contiguous — typed a word, moved the cursor BACK, typed
// another — the other person's word may end up BETWEEN those two runs.
//
// "Hello!", User 1 typing " reader" and then " dear" at the same anchor, User 2
// concurrently typing " Alice" there, merges to one of:
//
//	Hello dear reader Alice!
//	Hello dear Alice reader!    <- the other person's word, split into yours
//	Hello Alice dear reader!
//
// Every replica agrees on which, and which one it is comes from the tie-break
// between equal Lamport clocks rather than from anything either person did. It
// is not a defect of this implementation: Kleppmann, Gomes, Mulligan &
// Beresford proved RGA free of the SEVERE anomaly — two concurrent words jumbled
// character by character, which Logoot and LSEQ do exhibit — and showed this
// lesser one remains ("Interleaving anomalies in collaborative text editors",
// PaPoC '19, §3).
//
// More precisely, and this is the distinction that names what is above: RGA is
// proved free of FORWARD interleaving, where each person types left to right. It
// exhibits BACKWARD interleaving, which is what moving the cursor back and typing
// again produces, and which is not exotic — hitting backspace to fix a typo does
// it, and so does prepending rows to a list or a spreadsheet.
//
// That 2019 paper proposes a fix, and the fix does not work. Weidner & Kleppmann
// report it as two flaws ("The Art of the Fugue: Minimizing Interleaving in
// Collaborative Text Editing", §3.2): the non-interleaving property it defines
// "cannot be satisfied by any algorithm", and the algorithm it proposes "is
// incorrect — it does not converge". So there is nothing here to weigh up and
// decline.
//
// What does work is a different algorithm, not a patch to this one. Fugue and
// FugueMax are proved to interleave "only in the rare situations where some
// interleaving is inevitable", FugueMax satisfying their maximal non-interleaving
// property, with performance their paper compares to Yjs on a real editing trace.
// Adopting one would replace how this package orders concurrent insertions at a
// shared anchor, which is its centre rather than a setting.
//
// TestWhereAConcurrentWordLandsAmongTwoOfYourOwn holds what is actually
// promised: every merge order agrees, and the result is one of those three. A
// fourth would be the severe anomaly and a real defect.
//
// # Determinism
//
// The package never reads the wall clock and never draws random numbers, so the
// same [Doc] compiled to js/wasm behaves exactly as it does on a server. Replica
// identity is injected by the caller as a [SiteID]; see [DeriveSiteID] for a
// deterministic way to obtain one from bytes the caller already has.
//
// TestTheSourceNeverReadsTheClockOrDrawsRandomNumbers holds this one, and it
// reads the source rather than exercising the code, because what the sentence
// forbids is an absence. A time.Now() added to a new file tomorrow would break
// it with every test still passing — the convergence suite least of all, since
// two replicas that both read the clock can still agree with each other and
// disagree only with a replay of themselves.
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
//
// # One document of many parts
//
// [Composite] holds named parts, each a [Doc], a [List] or a [Map], so that a
// text, the comments on it and a sheet of cells are one snapshot, one version
// and one thing to authorize rather than five. It adds no merge rule: each part
// keeps its own counters and converges exactly as it does standing alone. A part
// is identified by its name and its kind together, and exists because operations
// for it exist, so two replicas that reach for the same part are already holding
// it and nothing is exchanged to create one.
package crdt

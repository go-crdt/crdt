# Design

## Why a CRDT, and why in Go

The alternative is operational transformation, as ShareDB and Google Docs use.
OT needs a server that is authoritative over ordering, a transform function that
is famously hard to get right for every pair of operation types, and it does not
survive going offline. A CRDT needs none of those: merging is a pure function of
the operations, so the same code decides the outcome everywhere.

That is worth more here than usual, because this package compiles to `js/wasm`.
A browser client and the server run **the same merge implementation**, not two
implementations that have to agree. A JavaScript Yjs client paired with a Go
server cannot make that claim.

## Why not an existing library

Checked before writing anything (2026-08-15):

- **`github.com/amoghyermalkar123/ygo`** — a YATA/Yjs port, and the closest thing
  to a Go text CRDT that exists. Measured rather than read: 74.3% statement
  coverage overall (`marker` 7.1%, `blockstore` 38.2%), no property test for
  convergence, last commit May 2025, single author. It pulls in `zap`,
  `lumberjack` — file-based log rotation, in a library meant for the browser —
  and `templ`. Apache-2.0, so its code cannot be taken into a BSD-3 project
  either. Useful as a reference for YATA; not a base.
- **`github.com/cshekharsharma/go-crdt`** — RGA with an out-of-order buffer.
  Early, small, a reference for the RGA path.
- **Automerge / Yjs** — mature, but Rust and JavaScript. `automerge-go` is a CGO
  binding to `automerge-rs`, which fails CGO=0 and cannot be shared into wasm.

So: built here, informed by the YATA and RGA literature.

## The algorithm

RGA — a replicated growable array. Every character has a unique [ID] and names
the character it was inserted **after**, so an insertion is positioned relative
to content, not to an index a concurrent edit would invalidate. Deletion is a
tombstone, because a concurrent insertion may still name a character another
replica has already removed.

Integration, in full:

```
find the origin character
walk forward while the character there sorts after the new one
insert
```

The walk is why the ordering is a *total* order over all operations. It is safe
to stop at the first character that sorts before the new one because everything a
replica inserted after some character carries a higher Lamport clock than that
character — and so does everything inserted after *that* — so a lower clock can
only belong to something outside the region the insertion can land in.

### Two counters, deliberately

Each operation carries both:

| | meaning | used for |
|---|---|---|
| `ID.Seq` | per-site counter, +1 per operation, never a gap | identity, version vectors, exact deduplication |
| `Op.Clock` | Lamport timestamp, raised past everything seen | ordering concurrent insertions |

Folding them into one counter is the obvious economy and it is wrong. A Lamport
clock jumps when it hears from a peer, which leaves gaps in a site's own numbers,
and a version vector cannot describe a sequence with gaps — a replica could then
not tell "operation 7 has not arrived" from "operation 7 does not exist", and
reordered delivery within one site would be mistaken for a duplicate.

Keeping `Seq` contiguous has a second effect worth naming: `Apply` will not
integrate an operation until its predecessor from the same site has landed, so
delivery is causally ordered per site whatever the transport does.

### What the ordering rule actually decides

Under that causal readiness rule, convergence turns out **not** to depend on the
Lamport clock: an experiment that disabled the clock comparison entirely still
passed 300 randomised sessions and every permutation of 40 small histories, at
four sites and eight operations. What the clock decides is *which* order
concurrent insertions take, and that is user-visible: whoever had seen more of
the document when they typed is placed first, rather than whoever holds the lower
site number. `TestClockOutranksSite` pins exactly that, and fails without the
clock; `TestConcurrentInsertAtSamePosition` pins the tie-break.

This is recorded because it is the kind of thing that looks like dead weight to a
later reader with a profiler.

### Interleaving

RGA keeps a run of characters typed one after another contiguous, even when
someone else is typing at the same position, because each character's clock
exceeds anything its typist had seen. It does not give the stronger guarantee
Fugue proves for insertions made in other patterns. `Doc` is a small surface —
`Insert`, `Delete`, `Apply`, `Snapshot` — precisely so a Fugue or YATA
integration rule can replace the current one without disturbing anything that
imports it. Version 0.1 ships RGA because it is the variant whose correctness can
be demonstrated rather than argued.

## Snapshots

A snapshot is every character in document order, alive or tombstoned, plus the
version vector. Two properties make it more than a dump:

- **Canonical.** Two replicas holding the same operations produce byte-identical
  snapshots, which is why the test suite compares snapshots rather than text when
  it checks convergence — agreeing on the text is weaker than agreeing on the
  state.
- **Complete.** The whole history is recoverable: `OpsSince` on a loaded document
  returns what it would have returned on the original, so a replica restored from
  a snapshot can still serve a peer that has been away for a month.

A deletion's Lamport clock is the one thing not kept — it never affects ordering
— so replayed deletions carry their sequence number as their clock.

### Loading is a trust boundary

Snapshots arrive over a network, and most of the work in `Load` is refusing
states no replica could have reached. Fuzzing found every one of these; each was
a real way to make a document unable to reproduce itself:

- A sequence number of zero paired with a non-zero site read as a real operation
  rather than as the root, which smuggled in a deletion that replay would then
  skip as already-applied. `IsRoot` now tests the sequence number alone.
- Two characters claiming the same deletion, or a version vector promising more
  operations than the snapshot accounts for. `Load` now insists that every
  operation the vector promises appears exactly once.
- A document order that integration could not have produced. The order is not a
  matter of choice, so `Load` re-walks the integration scan and rejects a
  snapshot that disagrees with it.
- A site listed twice in the version vector, a concurrent deletion aimed at the
  root sentinel, and a concurrent deletion of a character still visible.

## Awareness

Cursors and selections are not part of the document. They are never persisted,
they are dropped when a peer leaves, and merging them needs nothing stronger than
last-writer-wins per peer — so `crdt/awareness` carries a counter of its own
rather than borrowing the document's, and a departure keeps that counter so an
update still in flight cannot resurrect a peer who has gone.

## Next

1. `Collab`, a gRPC service over
   [`grpc-transports/websocket`](https://github.com/grpc-transports/websocket):
   a per-document hub that fans out operations, snapshots late joiners, and
   persists. No transform, because the CRDT converges.
2. An end-to-end proof with two `js/wasm` clients editing concurrently through
   that server, asserting programmatically that both converge.
3. Run-length blocks — see [performance.md](performance.md).

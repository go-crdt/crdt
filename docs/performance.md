# Performance

Measured, not estimated. Apple M4 Max, Go 1.26.4, `darwin/arm64`, on a document
of 10 000 characters:

| Benchmark | Result |
|---|---|
| `InsertAtEnd` | 231 ns/op |
| `InsertAtStart` | 315 ns/op |
| `ApplyRemote` (10 000 operations) | 823 µs |
| `String` | 34.7 µs |
| `Snapshot` | 94.5 µs, **11.0 bytes/char** |
| `Load` | 1.48 ms |
| memory | **107.7 bytes/char** |

Reproduce with `go test -run '^$' -bench . -benchmem`.

## The position mark

Finding a rune offset means walking the list, and the first measurement made the
cost plain: inserting at the end of a 10 000-character document took **68 052 ns**
against 218 ns at the start. The whole difference was the walk.

Someone typing asks for very nearly the same position on every keystroke, so
`Doc` remembers the character its last local insertion produced and resumes from
there when the position wanted is at or after it. Any other change — a deletion,
a peer's operation — drops the mark.

**68 052 ns → 231 ns, a factor of 294.** The shortcut is covered by a test that
runs every edit twice, once with the mark and once with it cleared, and requires
the same document both times; removing the invalidation makes the convergence
suite fail immediately.

## What 107.7 bytes per character means

A character costs a 64-byte list item plus its entry in the identity map. A 1 MB
source file would therefore need roughly 100 MB, which is the number that decides
the next piece of work.

The answer for RGA text is **not** tombstone garbage collection. A tombstone
cannot simply be dropped: a concurrent insertion may still name it as its origin,
and removing it means rewriting those origins on every replica identically, plus
keeping a mapping for operations still in flight that were made before the
collection. Yjs sidesteps all of that by keeping the identity and dropping only
the content — which wins nothing here, because the content of a character-level
item is four bytes of a sixty-four byte struct.

The answer is **run-length blocks**: one item per *run* of characters typed
consecutively by one site, split only where a concurrent insertion or deletion
actually lands. Typing produces long runs, so the item count falls by orders of
magnitude on real documents, and tombstones collapse with them. It is how Yjs and
`ygo` are built, and it is version 0.2 — behind the same `Doc` API, with the same
convergence suite as the gate.

`ApplyRemote` and `Load` are dominated by the same per-character allocation and
will follow the item count down.

## Complexity

| Operation | Cost |
|---|---|
| `Insert` at the mark | O(1) amortised |
| `Insert` elsewhere | O(distance from the mark, or from the start) |
| `Apply` an insertion | O(1) plus the integration scan |
| `Apply` a deletion | O(1) |
| `String`, `Snapshot`, `OpsSince` | O(characters, alive and tombstoned) |

The integration scan is bounded by the number of insertions made concurrently at
the same position, which is small in practice and adversarial in theory: a peer
sending operations that all name the same origin can make it quadratic. That is a
property of RGA's list representation shared with `Apply` and `Load` alike, and it
goes away with the indexed structure that run-length blocks bring.

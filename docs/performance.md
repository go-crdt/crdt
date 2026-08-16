# Performance

Measured, not estimated. Apple M4 Max, Go 1.26.4, `darwin/arm64`, on a document
of 10 000 characters:

| Benchmark | Result | Was |
|---|---|---|
| `InsertAtEnd` | **64.8 ns/op** | 231 ns |
| `InsertAtStart` | **58.0 ns/op** | 315 ns |
| `ApplyRemote` (10 000 operations) | **441 µs** | 823 µs |
| `String` | 31.0 µs | 34.7 µs |
| `Snapshot` | 75.2 µs, **11.0 bytes/char** | 94.5 µs |
| `Load` | **1.02 ms** | 1.48 ms |
| memory | **73.1 bytes/char** | 107.7 |

Reproduce with `go test -run '^$' -bench . -benchmem`. The "was" column is the
0.1.0 release, for the two changes described below.

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

## Indexing characters by sequence number rather than by identity

Integration has to find a character from the identity an operation names. The
obvious structure is `map[ID]*item`, and it was measured costing about as much
again as the character it points at — a 16-byte key and an 8-byte value carry
roughly 48 bytes of map overhead against a 64-byte character.

It is not needed. A site's sequence numbers start at one and have no gaps, and
`Apply` refuses an operation until its predecessor has landed, so **position in a
slice is the sequence number**: `chars[site][n]` is the character made by that
site's operation `n+1`, or nil where that operation was a deletion and made none.
Lookup stays O(1) and the per-character overhead falls to one pointer.

**107.7 → 73.1 bytes/char, and the write path got faster too** — inserting at the
end went from 231 ns to 65 ns, because a map insert per character was a larger
cost than the list work around it. Applying a peer's operations nearly halved.

## What 73 bytes per character means

A character costs a 64-byte list entry plus a pointer in its site's index. A 1 MB
source file therefore needs roughly 73 MB, which is still the number that decides
the next piece of work.

The answer is **not** tombstone garbage collection. A tombstone cannot simply be
dropped: a concurrent insertion may still name it as its origin, and removing it
means rewriting those origins on every replica identically, plus keeping a mapping
for operations still in flight that were made before the collection. Yjs sidesteps
all of that by keeping the identity and dropping only the content — which wins
nothing here, because the content of a character-level entry is four bytes of a
sixty-four byte struct.

The answer is **run-length blocks**: one entry per *run* of characters typed
consecutively by one site, split only where a concurrent insertion or deletion
actually lands. Within such a run the identities, Lamport clocks and origins all
follow from the first character's, so a thousand-character run costs one entry and
its text rather than a thousand entries. Typing produces long runs, so the entry
count falls by orders of magnitude on real documents, and tombstones collapse with
them. It is how Yjs and `ygo` are built, and the sequence-number index above is
the structure it needs — a run is a contiguous range of one site's sequence
numbers, which is exactly what that index addresses.

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

# Performance

## On the trace everybody publishes against

The sequential editing traces at [josephg/editing-traces](https://github.com/josephg/editing-traces)
are the common yardstick for text CRDTs — Yjs, Automerge and diamond-types all
report against them. `automerge-paper` is the canonical one: **259 778
single-character edits** recorded from someone actually writing a paper, ending
in 104 852 characters with 77 463 tombstones.

Apple M4 Max, Go 1.26.4, `darwin/arm64`:

| | Result |
|---|---|
| replay the whole history locally | **24.4 ms** — 94 ns/edit |
| apply the same history as a peer's operations | **55.9 ms** — 215 ns/operation |
| deliver it **back to front**, nothing applicable until the last operation | **0.24 s** |
| memory held afterwards | **3.9 MiB** — 22.7 bytes per character including tombstones |

Reproduce it, and check the result against the recorded final text:

```sh
curl -sLO https://raw.githubusercontent.com/josephg/editing-traces/master/sequential_traces/automerge-paper.json.gz
CRDT_TRACE=automerge-paper.json.gz go test -run TestEditingTrace -v
CRDT_TRACE=automerge-paper.json.gz go test -run '^$' -bench EditingTrace -benchtime 5x
```

The replay is a correctness test before it is a benchmark: a quarter of a million
edits at positions a real person chose, with the answer known in advance, plus a
snapshot round trip and a full reload. CI runs it on every change.

## Measured against other implementations

The numbers above are ours. Setting them beside the figures other projects
publish would compare four machines, so the other implementations were run here
instead: same machine, same trace, same protocol, on 2026-08-16.

Every implementation is driven the way its own published benchmark drives it,
and every run rebuilds the document and compares it against the `endContent`
recorded in the trace before any time is reported — a replay that produces the
wrong text fails rather than printing a number. Verification is outside the
clock. The harness is in [`docs/comparison/`](comparison/): one file with one
timing loop and one adapter per library, so the measurement cannot differ
between them.

Apple M4 Max (16 cores, 128 GiB), macOS 26.6.1, Go 1.26.4 `darwin/arm64`,
Node v26.4.0. Ten replays of the whole 259 778-edit trace per implementation;
the median is quoted and the full spread is in the last column.

| Implementation | Runs on | Replay (median) | ns/edit | × ours | min–max |
|---|---|---|---|---|---|
| diamond-types 1.0.2 | Rust → WebAssembly | **18.4 ms** | 71 | **0.76×** | 17.7–23.8 ms |
| **go-crdt/crdt 0.4.0** | Go | **24.1 ms** | 93 | 1.00× | 23.9–24.1 ms |
| loro-crdt 1.14.1 | Rust → WebAssembly | 692 ms | 2 662 | 28.7× | 573–1007 ms |
| yjs 13.6.32 | JavaScript | 3 079 ms | 11 851 | 127.6× | 3068–3141 ms |
| @automerge/automerge-wasm 1.0.0-preview.0 | Rust → WebAssembly | 8 262 ms | 31 803 | 342.5× | 8235–8497 ms |
| @automerge/automerge 3.4.1 | Rust → WebAssembly, JS wrapper | 26 492 ms | 101 980 | 1098× | 25 660–30 672 ms |

**diamond-types is faster than we are**, by a third, and that is the result. It
is the fastest text CRDT anyone has published and it stays that way here. The
honest reading of this table is that we are in its range — same order of
magnitude, same trace, same machine — and ahead of everything else measured.

### Document size, where we do badly

Encoding the replayed document:

| Implementation | Encoded document |
|---|---|
| diamond-types 1.0.2 | 109 KB |
| @automerge/automerge 3.4.1 | 129 KB |
| yjs 13.6.32 (V2 encoding) | 160 KB |
| loro-crdt 1.14.1 | 251 KB |
| yjs 13.6.32 (V1 encoding) | 311 KB |
| **go-crdt/crdt 0.4.0** | **2 663 KB** |

Ours is between eight and twenty-four times larger than anyone else's. The
others compress the sequence — run-length identities, variable-length integers,
columnar layout — and `Snapshot` does not; it writes the state out plainly. This
is the clearest thing this comparison found to fix, and nothing about the format
prevents fixing it.

### Memory

Only where it can be read honestly. Yjs is JavaScript, so `heapUsed` after a
forced collection is its document; Automerge, Loro and diamond-types keep theirs
in WebAssembly linear memory, which `process.memoryUsage()` does not see, so
there is no figure for them here rather than a bad one.

| Implementation | Held after replay | Per visible character |
|---|---|---|
| **go-crdt/crdt 0.4.0** | **4 034 KiB** | **39.4 B** |
| yjs 13.6.32, `gc: true` (default) | 5 678 KiB | 55.5 B |
| yjs 13.6.32, `gc: false` | 7 573 KiB | 74.0 B |

Per *visible* character, because that is the only count the two agree on: the
document ends with 104 852 characters, and we additionally hold 77 463
tombstones, which is where the 22.7 B/char quoted earlier comes from. Yjs's
default `gc: true` discards deleted content, so the `gc: false` row is the
closer comparison — and we are below both.

Ours varied by not one byte across three runs; Yjs's readings spanned 5678–5997
KiB (`gc: true`) and 7255–7573 KiB (`gc: false`).

### What this does not say

- **These libraries do not all do the same job.** Automerge maintains a whole
  JSON document with history, rich-text marks and branch metadata; Yjs and Loro
  carry several shared types. This is a text CRDT. A trace of single-character
  text edits is the workload they all publish against, but it flatters the
  implementation that does least.
- **Yjs is JavaScript** and the rest are compiled. That it is 128× slower than
  compiled code on a tight per-character loop is not a defect in Yjs.
- The Yjs figure here is faster than the 5 714 ms dmonad publishes for the same
  benchmark, on newer hardware and without the update observer his harness
  registers. Our encoded size for Yjs, 159 929 bytes, matches his published
  `docSize` exactly, which is the check that this harness reproduces his.
- Automerge measured **slower** than the 14 326 ms dmonad publishes, on faster
  hardware. He pins `@automerge/automerge@^2.1.10` and this is 3.4.1. That
  difference was not investigated, and no claim of a regression is made from one
  measurement. The `automerge-wasm` row is the same trace against Automerge's
  Rust core with the JavaScript document wrapper removed, which puts about two
  thirds of the cost in the wrapper.
- Wrapping the whole Yjs replay in a single `doc.transact` makes it *slower*,
  9 406 ms against 3 079 ms, so the per-edit form used here is both what Yjs's
  own benchmark does and the faster of the two.
- One `Automerge.change` per edit, rather than one for the trace, costs about
  101 µs/edit over the first 20 000 edits — Automerge's own benchmark batches,
  and this is why.

Reproduce all of it:

```sh
cd docs/comparison && npm install
CRDT_TRACE=…/automerge-paper.json.gz node --expose-gc bench.js yjs --runs 10
```

## Synthetic benchmarks

On a document of 10 000 characters:

| Benchmark | 0.1.0 | 0.2.0 | 0.3.0 | 0.4.0 |
|---|---|---|---|---|
| `InsertAtEnd` | 231 ns | 65 ns | 34.8 ns | **32.9 ns** |
| `ApplyRemote` (10 000 operations) | 823 µs | 441 µs | 302 µs | **279 µs** |
| `Load` | 1.48 ms | 1.02 ms | 830 µs | **756 µs** |
| `String` | 34.7 µs | 31.0 µs | 27.4 µs | **23.7 µs** |
| memory, one run | 107.7 B/char | 73.1 | 4.19 | **4.20** |

`go test -run '^$' -bench . -benchmem`.

## What changed, and why

### The position mark, and then a list that walks both ways

Finding a rune offset means walking the document. The first measurement was
blunt: inserting at the end of a 10 000-character document took **68 052 ns**
against 218 ns at the start, and the whole difference was the walk. Remembering
where the last local edit ended took that to 231 ns.

That mark only helped forwards, which the real trace exposed immediately: a
replay that should have taken milliseconds took **2.79 seconds**, because half
the edits move the cursor back a little and every one of those walked from the
start of the document. Blocks now carry a back pointer and the walk goes
whichever way it must, and a deletion re-establishes the mark instead of dropping
it.

**2.79 s → 50 ms on the real trace, a factor of 56**, and it is the single change
that put this in the same range as the fastest implementations rather than an
order of magnitude behind them.

### Characters indexed by sequence number, not by identity

Integration finds a character from the identity an operation names. That was a
`map[ID]*item`, and the map cost about as much again as the character it pointed
at. It was never needed: a site's sequence numbers start at one and have no gaps,
and `Apply` refuses an operation until its predecessor has landed, so **position
in a slice is the sequence number**.

107.7 → 73.1 bytes per character, and the write path halved — a map insert per
character cost more than all the list work around it.

### Run-length blocks

A character used to be a struct of its own: identity, clock, origin pointer,
rune, deletion, next. Everything but the rune is derivable. A block is a run of
characters one site typed consecutively, and for character *k* of that run the
identity is `{site, seq+k}`, the clock is `clock+k`, and the origin is character
*k-1*. A thousand characters typed in a row cost one header and their text.

Blocks are only ever split — by an insertion landing inside one — and extended,
by the next character of the same run. There is no merging to do: a new character
can never bridge two existing blocks, because that would need the sequence number
the right-hand block already holds. Integration walks runs rather than
characters, because within a run the clocks ascend, so whichever way the "sorts
after the new character" test goes at the first character of a run holds for the
whole run.

**73.1 → 4.19 bytes per character** on a document typed in one run, and the
index shrank with it: it now holds one entry per run rather than one per
character.

### Waiting operations filed under what they are waiting for

A peer that sends a long history back to front used to be quadratic: every
arrival rescanned everything still parked. Delivering the real trace in reverse —
337 000 operations, none applicable until the last — took **210 seconds**.

Operations now wait in a map keyed by the single operation each needs, so
integrating one wakes exactly its dependents. **210 s → 0.30 s.**

### Deletions stored as stretches

A block used to keep one deletion identity per character as soon as any
character in it died — 2289 KiB of the 4015 the real trace occupied, and half of
those entries described characters that were still visible.

A record now covers a whole stretch: characters `[from, to)` removed by one
site's operations `seq, seq+1, …`, so the operation that removed character
`from+k` is `seq+k`. Backspacing over a word is one record rather than one per
letter. The contiguity is enforced, not assumed — a deletion joins the record
beside it only when its identity continues that record's sequence.

Measuring first was worth it: the trace's 77 463 tombstones collapse to 50 276
records, 1.5 characters each, because most are scattered corrections rather than
long runs. That put the honest expectation at 2289 → 1178 KiB rather than the
larger figure a guess would have promised.

The speed-up was the surprise. **54.6 ms → 24.4 ms on the real trace**, because
everything that walks a block — the text, the positional search, counting what is
visible — now steps over stretches instead of testing characters one at a time.

One simplification fell out and is worth stating, because it looks like a missing
case: a deletion can only ever *extend* the record ending where it lands. It can
never fill a gap between two records, or lead into one, because a site's
operations are applied in sequence order — a record whose identities follow this
deletion's cannot already exist.

## Where the memory goes now

Measured on the real trace: 10 824 blocks holding 182 315 characters, of which
77 463 are tombstones, in 3.9 MiB.

| | |
|---|---|
| block headers | ~1050 KiB |
| text | 712 KiB |
| deletion records | ~1180 KiB |

The remaining lever is the block headers, at 104 bytes each. An order-statistic
tree over the sequence would replace both the linked list and the per-site slice
index, make a cold positional lookup logarithmic rather than linear in runs, and
remove the adversarial quadratic described below — worth measuring before
committing to.

## Complexity

| Operation | Cost |
|---|---|
| `Insert` or `Delete` near the last edit | O(distance in characters), which is what locality makes small |
| `Insert` or `Delete` cold | O(runs), not O(characters) |
| `Apply` an insertion | O(runs scanned from the origin) |
| `Apply` a deletion | O(log runs of the target's site), then O(records in the run) |
| an operation arriving before its dependencies | O(1) to park, O(1) to wake |
| `String`, `Snapshot`, `OpsSince` | O(characters, alive and tombstoned) |

The integration scan is bounded by the number of runs inserted concurrently at
the same position, which is small in practice and adversarial in theory: a peer
sending operations that all name the same origin can make it quadratic. The
answer there is an order-statistic tree over the sequence, which would also make
a cold positional lookup logarithmic rather than linear in runs.

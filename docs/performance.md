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
| replay the whole history locally | **18.4 ms** — 71 ns/edit |
| apply the same history as a peer's operations | **52.9 ms** — 204 ns/operation |
| deliver it **back to front**, nothing applicable until the last operation | **0.25 s** |
| memory held afterwards | **4.3 MiB** — 24.6 bytes per character including tombstones |
| the document encoded | **620 KB** — 5.9 bytes per visible character |

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

Our row is 0.4.0. The index over runs described below arrived after this table
was measured and takes the replay to 18.4 ms, on the Go benchmark that put 0.4.0
at 23.4 ms in the same session — level with diamond-types on this trace, by a
measurement that agrees with this one to within 3%. The table stands as it was
taken; the other implementations have not been re-run.

### Document size, where we do badly

Encoding the replayed document:

| Implementation | Encoded document |
|---|---|
| diamond-types 1.0.2 | 109 KB |
| @automerge/automerge 3.4.1 | 129 KB |
| yjs 13.6.32 (V2 encoding) | 160 KB |
| loro-crdt 1.14.1 | 251 KB |
| yjs 13.6.32 (V1 encoding) | 311 KB |
| **go-crdt/crdt 0.15.0** | **478 KB** |
| *go-crdt/crdt 0.14.0* | *527 KB* |
| *go-crdt/crdt 0.5.0* | *620 KB* |
| *go-crdt/crdt 0.4.0, when this was measured* | *2 663 KB* |

This is what the comparison was for. At 0.4.0 ours was between eight and
twenty-four times larger than anyone else's, because `Snapshot` wrote one record
per character while everyone else writes runs. Version 2 of the format writes
runs too, which took it to 620 KB.

### What the gap actually was

This page used to say the rest was the text: "ours is 182 KB of characters
written plainly, which already exceeds their whole document." That was true and
it was not the answer. If the text is 182 KB of 620, something else is 438 KB,
and 438 KB is four times diamond-types' entire document — so compressing the
text would have left most of the gap standing.

`TestSnapshotBudget` accounts for every byte `Snapshot` writes, by the field
that wrote it. On the same document, under version 2:

| Field | Bytes | Share |
|---|---|---|
| deletions | 299 244 | 48.3% |
| the text | 182 315 | 29.4% |
| run identities | 42 444 | 6.8% |
| origins | 42 382 | 6.8% |
| clocks | 31 620 | 5.1% |
| run and field lengths | 21 857 | 3.5% |

The deletions were the largest thing in the file, larger than the text, and on
their own nearly three times diamond-types' whole document. Half of that — 148 KB
— was one field: the sequence number of the operation that did the deleting,
written in full at three bytes each, 50 276 times.

Version 3 writes it as a signed step from the last sequence number that site
deleted with. A person deleting text works through it rather than jumping about,
so the step is usually one byte. Measured before it was built, on this trace:

| That field, written as | Bytes |
|---|---|
| the number itself (version 2) | 148 385 |
| **a step from the previous deletion (version 3)** | **55 828** |
| a step from the run's own sequence | 117 771 |

which is what took the document from 620 KB to 527 KB.

### Then the same question, of what was left

With the deletions halved, the three fields under them were 116 KB between
them, and the same measurement answered the same way:

| Field | In full | As a step | |
|---|---|---|---|
| run identities | 42 444 | **31 273** | a step from the last run by that site |
| origins | 42 382 | **25 484** | a step from the last origin with that site |
| clocks | 31 620 | **10 824** | the distance above the run's own sequence |

The clocks are the interesting one: 10 824 bytes is one byte per run, and it is
not a coincidence. A clock can never fall below its own sequence number — a
site's clock advances at least once per operation it issues — so the distance
between them is small, unsigned, and needs no sign bit. Writing it that way also
means a snapshot can no longer express a clock the loader would have had to
refuse: the invariant moved out of a check and into the format.

Version 4 writes all three. 527 KB to 478 KB, and 620 KB to 478 KB since the
runs were introduced — a 23% cut in two steps, both of them measured first.

What is left is mostly the text: 182 KB of it, 38% of the file, written plainly
because this package has no dependencies and a compressor is a dependency. The
deletions are still 43%, but three quarters of that is now the offsets and spans
themselves rather than the identities attached to them.

### The document size gap is an encoding, not a compressor

At version 4 a real document encodes to 478 KB where diamond-types takes 109 KB,
and this page used to put that down to the text being written plainly. It is not
the text. Compressing the snapshot as it stands takes it to 148 KB — still a
third bigger than their whole document — because the fields are interleaved: a
compressor looking for repetition sees a run's identity beside its text beside
its deletions.

Written one field at a time, each column is a stream of similar numbers, and a
compressor is very good at those. The same document, same fields, same step
encodings, laid out in columns and compressed per column:

| Column | Raw | Compressed |
|---|---|---|
| run sites | 10 824 | **13** |
| clocks | 10 824 | **13** |
| origin sites | 10 824 | **14** |
| deletion counts | 10 850 | 3 978 |
| run lengths | 11 005 | 6 732 |
| origin sequences | 14 660 | 10 824 |
| run sequences | 20 449 | 16 720 |
| deletion fields | 206 687 | **18 181** |
| the text | 182 315 | 40 228 |
| **total** | **478 438** | **96 703** |

Against diamond-types' 111 616 bytes that is **87%**. The deletion fields are the
sharpest: 206 KB to 18, because a column of small gaps and spans repeats itself
in a way the same numbers scattered between other fields do not.

#### Which compressor, measured rather than assumed

Every one of these is pure Go. Same columns, best setting of each:

| | Total | |
|---|---|---|
| **brotli** (`andybalholm/brotli`, q11) | **96 703** | |
| lzma (`ulikunitz/xz`) | 109 149 | +13% |
| zstd (`klauspost/compress`) | 109 201 | +13% |
| xz | 109 564 | +13% |
| flate (standard library) | 112 211 | +16% |

brotli wins on every column, including the small-varint streams where it would
be least expected. Two settings were measured rather than guessed: the window
must be at least 2^20 — at 2^16 it costs 2.4 KB, and past 2^20 nothing changes —
and compressing **per column beats one stream**, 96 703 against 97 811, so the
column boundaries are worth more than the redundancy across them.

That is the one dependency worth taking: it buys 14% against the next best,
where zstd's advantage was one column.

#### And what it costs, which decides the setting

brotli's top quality is very slow. The ladder, per column, on the same 478 KB:

| Quality | Per column | vs diamond-types | Encode |
|---|---|---|---|
| q4 | 112 215 | 101% | 5 ms |
| **q5** | **108 266** | **97%** | **7 ms** |
| **q9** | **106 169** | **95%** | **14 ms** |
| q10 | 98 686 | 88% | 167 ms |
| q11 | 96 703 | 87% | 425 ms |

Quality 5 is already ahead of them, at seven milliseconds. The cliff is between
9 and 10: fourteen milliseconds becomes a hundred and sixty-seven, for seven
kilobytes. So q9 is the setting to default to — ahead of diamond-types, and
costing about what zstd costs — and q11 is for an archive that is written once.

Decompression does not have this problem: 2 ms, 292 MB/s, and that is the side
that runs whenever a document is opened.

This is also the honest answer to whether one of our own compressors should be
used instead. `go-compressions/deflate` accelerates match extension with SIMD,
but DEFLATE's density is what it is — 112 KB here — so the SIMD buys time on a
format that cannot reach the number. Where a SIMD brotli encoder would pay is
exactly the q10 and q11 rows, and what it would buy there is the 167 or 425
milliseconds rather than any bytes. q9 does not need it.

#### What is not decided by this

`Snapshot` says the same state is the same bytes, and every format change so far
has kept that. A compressor's output is deterministic for a build and not
promised across versions of the library, so compression belongs beside the
format rather than inside it: the canonical form stays the columnar bytes, and
whoever stores or sends them compresses. A plain whole-blob compression of the
columnar layout gives 97 811 rather than 96 703, which is the price of keeping
the boundary in the right place.

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
tombstones, which is where the per-stored-character figure at the top of this
page comes from. Yjs's
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

| Benchmark | 0.1.0 | 0.2.0 | 0.3.0 | 0.4.0 | 0.5.0 |
|---|---|---|---|---|---|
| `InsertAtEnd` | 231 ns | 65 ns | 34.8 ns | 32.9 ns | **33.7 ns** |
| `ApplyRemote` (10 000 operations) | 823 µs | 441 µs | 302 µs | 279 µs | **295 µs** |
| `Load` | 1.48 ms | 1.02 ms | 830 µs | 756 µs | **613 µs** |
| `String` | 34.7 µs | 31.0 µs | 27.4 µs | 23.7 µs | **23.6 µs** |
| memory, one run | 107.7 B/char | 73.1 | 4.19 | 4.20 | **4.19** |
| `ScatteredInsert` | | | | 12.3 µs | **0.37 µs** |
| `SameOriginFlood` (5000 operations) | | | | 27.3 ms | **1.07 ms** |

`go test -run '^$' -bench . -benchmem`. The last two arrived with 0.5.0; their
0.4.0 column is the same benchmark run against the release before it.

The 0.5.0 column holds two changes and a busy machine, so it was taken beside the
commit before it in the same session, which read 32.9 ns, 291 µs, 607 µs, 24.0 µs
and 4.19 B/char. `Load` is the run-length snapshot format; what the index costs
here is under a nanosecond of `InsertAtEnd` and 4 µs of `ApplyRemote`.

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

### The snapshot format writes runs

`Snapshot` wrote one record per character: identity, clock, origin, the
character, and its deletion — eight numbers each, twenty-five bytes per visible
character of a real document. Comparing against other implementations made it
plain that nobody else pays that.

Version 2 writes a run at a time — one header for a stretch one site typed
consecutively, then its text, then the stretches of it that have been deleted —
because everything about the characters in a run follows from the first one.
**2 663 KB → 620 KB on the real trace, 25.4 → 5.9 bytes per visible character**,
and writing it is twelve times faster.

Version 1 is still read, so a document stored by an older build opens; the test
for that uses a snapshot produced by the v0.4.0 release rather than one this
build wrote, because a fixture regenerated by the code it checks proves nothing.

Two things had to be true for this to work, and one of them was not free. The
runs written have to be the same on every replica holding the same operations,
or a snapshot stops being a convergence check. The blocks themselves already
are — two that continue one another are never adjacent, because a character
bridging them would need the sequence number the right-hand one already holds —
but their *deletion records* are not: cutting a block divides a record, and
replacing one character's deletion when two replicas delete it at once cuts
another. Writing them joined hides that. The randomised convergence suite found
this immediately, by comparing encoded state rather than text.

### An index over the runs

The list answers "what follows this" in a step and everything else by walking,
which the mark makes cheap only while the next position is near the last one. Two
things it is not cheap for, and the second is not a matter of taste:

- a position far from the last edit — a second cursor, a replace-all, a patch
  dropped into the middle, or simply a peer's operation arriving between two
  keystrokes and clearing the mark — walks every run in between;
- integration walks forward from the origin over everything that sorts after the
  new character, so a peer naming one origin over and over makes the walk the
  length of the document. `collab`'s server integrates what its peers send, and
  peers need not be honest.

The blocks are now the nodes of an AVL tree in document order as well as links in
the list, each carrying two summaries of its subtree: the visible characters it
holds, which turns a position into a descent, and the block of it that sorts
lowest, which turns that integration walk into one too. Measured back to back
against the release before it, on the same machine in the same session:

| | 0.4.0 | 0.5.0 | |
|---|---|---|---|
| the real trace, replayed | 23.4 ms | **18.4 ms** | −21% |
| 2000 inserts at scattered positions in a 12 000-run document | 12.3 µs each | **0.37 µs** | 33× |
| 5000 operations at one origin | 27.3 ms | **1.07 ms** | 26× |
| 20 000 operations at one origin | 370 ms | **4.6 ms** | 80× |
| the real trace, applied as a peer's operations | 50.9 ms | 52.9 ms | +4% |
| memory held on the real trace | 22.66 B/char | 24.56 B/char | +8% |
| `InsertAtStart` | 93.2 ns | 140 ns | +50% |

The flood is the one to read twice. Its cost per operation was 2.8 µs at 2500
operations, 5.6 at 5000, 10.2 at 10 000 and 18.5 at 20 000 — doubling with the
document, which is the quadratic stated below. It is now 193 ns, 214, 231 and
231: flat, because a subtree whose lowest-sorting block still sorts after the new
character holds nothing to stop on and is stepped over whole.

The trace got faster rather than slower because real editing is not as local as
the mark assumes: a fifth of the replay was walking.

Both walks keep their heads. Each starts along the list and turns to the tree
only after sixteen runs, which is about what a descent costs, so typing pays
nothing for the index and never sees it.

Three numbers went the wrong way, and all three are the same number. The index
costs 32 bytes a run — three pointers, a count and a height, which takes a block
header from Go's 112-byte size class to its 144-byte one — and on the trace that
is 338 KiB. `InsertAtStart` inserts one character per run at one end, so it pays
a walk to the root of the tree per character and nothing else amortises it; it is
the index's worst case by construction. Applying a whole history allocates
everything from nothing, and a profile of it is dominated by the allocator rather
than by anything in `tree.go` — the 4% is that 8% of memory, arriving as
collector work.

A B-tree holding runs in its leaves, as diamond-types does, would pay for itself
here: no per-run node, a depth of four rather than seventeen, and iteration from
the leaves. It costs the pointer identity of a block, which the mark, the
per-site index and every walk in `text.go` are written in terms of.

#### The arithmetic, since it was measured rather than guessed at

A block is **152 bytes**, and Go's size classes here are sixteen bytes apart, so
it is allocated in the **160-byte class**: eight bytes of every block are paid
for and unused. `TestBlockFitsItsSizeClass` pins this so it cannot drift.

The tail is what decides it. `subMin` ends at 136; `subVis` and `subSup` fill the
word to 144 exactly; `nsup` and `height` spill past it and cost a whole further
word. Five bytes of summary buy sixteen bytes of allocation, on every block.

Shaving that tail was looked at and rejected, which is worth recording so the
next person does not look at it again:

- `height` is an AVL height, computed from its children. It is structural, not a
  priority that could be derived from the block's identity and dropped.
- `nsup` could become a single bit — every use of it outside `split` is a test
  against zero — but that alone leaves the block at 148 bytes, which is the same
  size class. Nothing is saved.
- Reaching 144 means capping `subVis` or `subSup` so that `height` can live in
  the spare bits. A subtree's visible count is already `int32` for a stated
  reason: two billion characters is eight gigabytes of text. Capping it lower
  trades a bounded, honest limit for one that fails as silent corruption.

So the block cannot lose a size class without giving something up, and what it
would save is about 4% of what a real document holds.

#### What the other answer is worth

The B-tree removes the per-run node rather than shrinking it, and that is a
different order of answer. `TestWhatTheBTreeWouldBuy` measures it the same way,
by declaring the shapes and asking Go:

| A run | Allocated | |
|---|---|---|
| today | 160 B | |
| with no per-run index node | **112 B** | 30% less |
| and iterating from the leaves | **96 B** | 40% less |

The second line drops `left`, `right`, `up`, `subMin`, `subVis`, `subSup` and
`height`; the third drops `next` and `prev` as well, since a B-tree iterates from
its leaves and the backwards walk those exist for is what the index replaced.

On the real trace that is **676 KiB of the 4 541 the document holds, about 15%**
— against the 4% shaving the tail would have bought, for a change that gives up
nothing. The B-tree's own internal nodes are not free, but at any sensible
branching factor they are a few percent of the leaves.

That is the case for spending a project on it. It costs the pointer identity of a
block, which the mark, the per-site index and every walk in `text.go` are written
in terms of, and it should be built against these two tests: they fail if the
prize disappears.

### A second summary, for UTF-16 offsets

Addressing the document in UTF-16 code units means turning a count of units into
a count of characters, and the obvious way to do that is to walk the document
counting. That is O(n) per keystroke, so the index carries a second summary
instead — the visible characters of a subtree that take two code units, beside
the count of visible characters it already carried — and a conversion becomes a
descent.

The cost was measured before it was decided to keep the summary, because one
nobody needs is still paid for by everybody. Both builds were run alternately in
one session on a machine under a load average of nine, so that whatever the
machine was doing landed on both; `benchstat`, n=14 on the trace and n=12 on the
synthetic benchmarks.

| | before | after | |
|---|---|---|---|
| the real trace, replayed | 20.60 ms ± 3% | **20.74 ms ± 1%** | ~ (p=0.27) |
| the real trace, as a peer's operations | 100.2 ms ± 14% | **104.2 ms ± 14%** | ~ (p=0.76) |
| `InsertAtEnd` | 49.10 ns ± 6% | **48.88 ns ± 16%** | ~ (p=0.68) |
| `ApplyRemote` | 410.4 µs ± 7% | **407.1 µs ± 10%** | ~ (p=0.93) |
| `InsertAtStart` | 199.0 ns ± 2% | **211.9 ns ± 9%** | ~ (p=0.07) |
| `ScatteredInsert` | 1.791 µs ± 7% | **1.903 µs ± 3%** | ~ (p=0.07) |
| `SameOriginFlood` | 1.656 ms ± 9% | **1.702 ms ± 6%** | ~ (p=0.44) |

Nothing moved by a distinguishable amount, and the trace — which is ASCII, and
so should not have moved at all — did not. The two rows that come closest,
`InsertAtStart` and `ScatteredInsert` at about +6% with p=0.07, are the two
benchmarks that allocate one block per character, which is where the memory
below is charged rather than the arithmetic.

The memory is exact, and it is the real cost:

| | before | after | |
|---|---|---|---|
| a block header | 144 B | **160 B** | +16 B |
| held on the real trace | 4372 KiB | **4541 KiB** | +3.9% |
| per stored character, real trace | 24.56 B | **25.51 B** | +3.9% |
| `MemoryPerCharacter`, 10 000 characters in one run | 4.19 B/char | **4.19 B/char** | — |

The summary is two `int32`s: the visible supplementary characters below a block,
and the supplementary characters the block itself holds, deleted ones included.
Sixteen bytes rather than eight, because the header was 141 bytes in Go's
144-byte size class with three bytes spare — *any* field, of any width, takes it
to the 160-byte class. A single `bool` would have cost exactly what these two
counters cost, which is why they are two counters and not a compromise.

The second of them is what keeps the work off documents with no emoji in them. A
block that holds no supplementary character answers every question about UTF-16
units without reading a character, so splitting one or indexing one stays
arithmetic instead of becoming a scan. And a document with none anywhere
converts in constant time without touching the index at all, because its rune
and UTF-16 offsets are equal by construction: `InsertAtEndUTF16` measures 44.9 ns
against `InsertAtEnd`'s 48.9, and 46.5 ns on the same document with one emoji
added, which is the descent.

## Where the memory goes now

Measured on the real trace: 10 824 blocks holding 182 315 characters, of which
77 463 are tombstones, in 4.4 MiB.

| | |
|---|---|
| block headers | ~1690 KiB |
| text | 712 KiB |
| deletion records | ~1180 KiB |

The remaining lever is still the block headers, now 160 bytes each.

## Complexity

| Operation | Cost |
|---|---|
| `Insert` or `Delete` near the last edit | O(distance in characters), which is what locality makes small |
| `Insert` or `Delete` cold | O(log runs) |
| `Apply` an insertion | O(log runs) |
| `Apply` a deletion | O(log runs of the target's site), then O(records in the run) |
| an operation arriving before its dependencies | O(1) to park, O(1) to wake |
| `String`, `Snapshot`, `OpsSince` | O(characters, alive and tombstoned) |

Both logarithms are the tree in `tree.go`, reached after sixteen runs of walking
the list. Nothing an untrusted peer sends can make either of them worse: the tree
is an AVL tree, balanced by height rather than by priorities a peer could
compute, and a peer that could choose the priorities could choose a list.

Adding a run to the tree costs a walk to its root, which typing amortises over a
whole run of characters — the change a keystroke makes is held against the block
and carried up when the tree is next read — but which one character per run does
not.

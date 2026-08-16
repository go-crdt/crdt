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

## Telling a view what changed

An editor cannot be handed the new text. Replacing the whole buffer throws away
the selection, the scroll position, the folded regions and every decoration, and
it would do so on each keystroke anyone else makes. It has to be told the edits.

[Doc.ApplyChanges] is [Doc.Apply] and also reports them, against the text as it
stands after the edits before them, so applying them in order to a copy brings
the copy up to date. That is the property the randomised test asserts: a copy
that only ever applies reported changes holds what the document holds, through
two hundred sessions of shuffled and duplicated delivery.

They are coalesced, because a peer typing a word produces one operation per
character and a view would rather hear about the word. Accumulating that word
naively — appending to the change's string per character — copies the whole word
each time, and measured **fifty-seven times** the cost of applying the operations
at all; the text is built in a buffer and sealed once instead, which brings the
overhead to a fifth over [Doc.Apply].

Finding where each edit landed costs a walk up the index per operation, so
[Doc.Apply] does not collect anything and does not pay.

## Anchors, and authorship

An editor needs somewhere stable to hang a comment. An offset is not that: the
moment anyone edits above it, it points somewhere else. What does not move is the
identity of the character itself, which every character already carries and which
nothing ever changes — so [Doc.Anchor] hands it out and [Doc.Position] converts
back, climbing the index rather than walking the document.

A deleted character still reports a position, the offset the text closed up to.
That is deliberate: a comment on a deleted sentence belongs where the sentence
was, not nowhere, and [Doc.Visible] is how a caller tells the two apart. The end
of the document anchors to the zero ID, the one place insertions at the end do
not move.

Authorship falls out of the same fact. The site is part of every character's
identity, so [Doc.AuthorRuns] splits the visible text by who wrote it in one
pass, joining stretches by the same replica so that the answer describes the text
rather than how this replica happens to have split its blocks — two replicas
holding the same document return the same runs.

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

## Counting in UTF-16

The document counts characters. CodeMirror, the DOM, the Language Server
Protocol and every index into a JavaScript string count UTF-16 code units, in
which a character above U+FFFF — an emoji, an extended CJK ideograph, most
mathematical alphanumerics — is two units rather than one. This is not an
encoding detail that stops at the edge: it is a browser sending a cursor offset,
and the offset naming a different position at each end.

The failure is silent and it is permanent. A document holding one emoji before
the cursor takes every later insertion one place to the left of where the user
put it, produces no error, and leaves nothing behind that a later read could use
to notice. The intended consumer is a LaTeX and Markdown editor, so the content
that triggers it — `𝔸`, `∫`, `𝒮`, an emoji in a comment — is the content it is
for.

So `Doc` addresses the same three operations both ways. `Len`, `Insert` and
`Delete` count runes and are unchanged; `LenUTF16`, `InsertUTF16` and
`DeleteUTF16` count code units, and `UTF16Offset` and `RuneOffset` convert
between the two. Nothing else grows a second form: `Anchor`, `Position` and
`Author` speak of the document as it stands, so the conversions compose with
them exactly.

`Change` is the exception worth naming, because composing there looks safe and
is not. Its offsets are against the text as it stood after the changes before
it, so converting one of them against the finished document is right only for
the last. A caller patching its own copy has the intermediate text in hand and
converts there.

### An offset that splits a character is refused

A UTF-16 offset can land between the two code units of one character. There are
three things to do about it — round down, round up, or refuse — and the third is
the only one that keeps a promise.

Such an offset names a position that does not exist. Half of an emoji is not
somewhere a caret can be, and no user put it there: it comes from a bug, or from
an offset computed against a different string. Rounding it silently moves the
edit somewhere the caller did not ask for and leaves nothing to say so, which is
the same class of failure this whole surface exists to remove — and this package
already refuses invalid UTF-8 rather than substituting replacement characters,
for the same reason.

The control instrument settles it. JavaScript will do the operation, and
`"a😀b".slice(0, 2) + "X"` is a string containing a lone high surrogate: not
text, not valid UTF-8, and nothing this package can hold. An offset whose own
definition can only be honoured by producing a broken string has already lost
the information needed to honour it. `testdata/utf16-control.json` records those
results as code units, and the test asserts they decode to a replacement
character — the argument is checked, not asserted.

Refusing costs the tolerant caller nothing, because rounding down needs no
second API. An offset that splits a character is always exactly one past that
character's first unit, so `RuneOffset(pos-1)` is the position of the character
it landed inside, and it cannot fail.

## A list of values

`List` replicates a sequence of opaque values by the same algorithm as the text,
because the things built around a document — its comments, its record of
changes, the messages beside it — are sequences with the same requirements.

They are two types rather than one generic one, and that is a judgement about
shape rather than about taste. A document holds hundreds of thousands of
characters, which is what earns run-length blocks, an order-statistic tree and a
snapshot format written a run at a time; a list holds tens or hundreds of values,
where a slice is both faster to walk and obviously correct, and where an element
is a byte slice the caller encodes rather than a rune this package understands.
Sharing the code would mean carrying the document's machinery for no gain.

What is shared is what should be: identities, version vectors, the Lamport
ordering, the ledger that makes a snapshot prove it accounts for its own history,
and the discipline — convergence demonstrated against randomised delivery *and*
against every permutation of small histories, replicas compared on encoded state
rather than on what they show, every decoder fuzzed.

## A replicated map

Not everything beside a document is a sequence. A spreadsheet is a map of cells:
written and cleared, never woven in between its neighbours. So `Map` is
last-writer-wins per key, ordered by the same `(clock, site)` total order the
text uses. Convergence is immediate — the winner is a maximum, and a maximum does
not depend on the order it is taken in — which moves all of the difficulty to two
other places.

### A deleted key keeps its clock

A deletion leaves a record behind. Dropping the key would leave nothing for an
older write arriving later to lose against, so that write would take effect,
resurrecting the key on the replica that heard it late and not on the one that
heard it early — permanently, and with nothing left behind that a later read
could use to notice. It is the classic mistake in a last-writer-wins map, and the
tombstone is equally what makes a delete and a concurrent set to the same key
resolve identically everywhere.

`Delete` writes a tombstone whether or not this replica holds the key, for the
same reason: what a deletion means cannot depend on what the deleting replica
happens to have heard.

### What a replica forgets, and how it says so

One record per key means the value a write put there is gone as soon as a later
write replaces it. The **operation** is not gone, and may not be: sequence
numbers are contiguous per site, which is what lets a version vector describe a
replica exactly, and `Apply` never skips one — an operation arriving before its
predecessor waits rather than being dropped, because the number it would skip
could be a key nobody would ever hear about again.

So a peer catching up has to be told that the number was used. `OpsSince` says it
with a `MapSuperseded` operation, which names no key and carries no value. That
is sound only because the operation which superseded it travels in the same
result: it is either the record now held for that key, which `OpsSince` sends
whenever the peer lacks it, or something the peer already has. Induction on the
`(clock, site)` order, which strictly increases at every step, closes it. A caller
that filters what `OpsSince` returns breaks the map.

A superseded operation covers a **run** of consecutive sequence numbers rather
than one. That is not only an economy: the numbers a replica has forgotten are
exactly the gaps between the ones it still holds, so a cell rewritten ten
thousand times sends one record and one run rather than ten thousand operations,
and catching a peer up costs what the state costs rather than what the history
did. Fuzzing found the version of this that did not: a snapshot whose version
vector promises a history far longer than the records describing it — no replica
sends one, but a decoder cannot tell — and `OpsSince` sat there naming every
number in it.

### Loading is a trust boundary, with one check the text has and this cannot

`Load` insists that every operation the version vector promises is accounted for
exactly once. `LoadMap` cannot: the superseded ones are accounted for by nothing,
which is the whole point of the section above. What it does insist on is that no
record claims an operation the vector does not promise, that no operation is
claimed by two records, that keys ascend strictly — the encoding is canonical, so
there is only one order, and a key given twice would leave which record applies
up to decoding order — and that a record's clock is at least its own sequence
number. What is left over is the superseded set, which `OpsSince` derives from
exactly that difference rather than storing it.

## Awareness

Cursors and selections are not part of the document. They are never persisted,
they are dropped when a peer leaves, and merging them needs nothing stronger than
last-writer-wins per peer — so `crdt/awareness` carries a counter of its own
rather than borrowing the document's, and a departure keeps that counter so an
update still in flight cannot resurrect a peer who has gone.

Its offsets stay in runes, and that is a decision rather than an omission. Both
ends have to agree what an offset means and an `Update` has nowhere to say:
adding a unit to the encoding changes the wire format for every peer, and not
adding one leaves a browser publishing UTF-16 and a server reading runes, with
no error anywhere — the document's failure moved somewhere nothing can detect
it.

What makes runes safe here rather than merely incumbent is that nothing in
awareness is authoritative. A cursor is advisory, stale before it is drawn,
clamped rather than trusted, and replaced by the next keystroke. An offset a
character out draws a caret a character out until the next update arrives; it
can never edit anything and it is never stored. A peer counting in UTF-16
converts at its own edge, where it has to clamp in any case.

## Next

1. `Collab`, a gRPC service over
   [`grpc-transports/websocket`](https://github.com/grpc-transports/websocket):
   a per-document hub that fans out operations, snapshots late joiners, and
   persists. No transform, because the CRDT converges.
2. An end-to-end proof with two `js/wasm` clients editing concurrently through
   that server, asserting programmatically that both converge.
3. Run-length blocks — see [performance.md](performance.md).

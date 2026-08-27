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

### The ordering has three keys, and the third is not decoration

Operations sort by ascending Lamport timestamp, then ascending site, then
ascending sequence number. The first two are the RGA rule everyone writes down.
The third was missing, and the justification for leaving it out was written in
the code: *a site never issues two operations with the same timestamp, so a tie
implies two distinct sites.*

That is true of every operation this package mints — a site's clock advances at
least once per operation. But it is a claim about a site's **whole history**, and
an arriving operation is one operation: nothing in it carries the rest, so no
receiver can check it, and nothing else can either. A peer could therefore hand
over two operations from one site sharing a timestamp, and `(clock, site)` was
then not an order at all — two distinct operations compared equal in both
directions.

What that cost was not a wrong ordering but **an unreadable document**. Both
walks that place a character use this comparison, and where it is ambiguous they
need not agree: integration put such a pair one way round, the scan `Load`
re-derives put it the other, and the snapshot was refused as one no replica could
have produced. Three operations were enough. A server that accepted the batch,
broadcast it, and persisted it when the last participant left could not
afterwards open its own file — a peer's bytes deciding whether a document
survives a restart.

The sequence number closes it because `(site, seq)` is unique by construction, so
the three keys are a strict total order on operations, derived from what every
operation already carries. `List` had the defect by the same route. `Map` did
not, for a reason worth keeping in mind: a site's operations integrate in
sequence order whatever order they arrive in, so its ties already resolved the
same way everywhere. What changes there is only which one wins — the later
sequence number now does, where an operation that tied simply lost.

Nothing an honest replica produces reaches the third key, so no existing document
changes its ordering: no format change and no migration.

Adding a key did cost, and not where it looked like it would. The comparison
itself is unchanged on the path that matters, since it takes a clock tie to reach
the new part — but building the compared character's identity eagerly, in order
to pass it in, cost **+17.9% on `InsertAtEnd`** and +3.0% on `ApplyRemote`
(benchstat, p ≤ 0.03). The walk's innermost comparison now derives that sequence
number only if it gets that far, which puts every benchmark back inside the
noise.

### The clock has a ceiling

A Lamport clock is raised past every clock a replica is told about, so the number
a peer writes on an operation is a number this replica adopts. Left unbounded,
that is enough to break a replica permanently, and it takes one operation — a
corrupted varint will do, no malice required. A clock at the top of the range
leaves the receiver's clock there, its next edit wraps to zero, and every
operation it issues from then on carries a timestamp below its own sequence
number: an operation its own validator rejects, and one that loses every tie it
takes part in. The peer goes on typing and nothing it types is ever seen again.

So `crdt.MaxClock` (2⁶²) is part of what makes an operation valid, checked
wherever an operation or a snapshot arrives — text, list, map, and the awareness
registry, which has a counter of the same shape. A sequence number is bounded by
the same constant, since a clock is never below the sequence number beside it, so
a version vector cannot promise operations that could not exist either. Reaching
the ceiling honestly would take four quintillion operations, one per nanosecond
for a century and a half.

**What this does not fix, stated plainly.** A peer may still claim a clock *at*
the ceiling, and a replica that adopts it can issue nothing further. There is no
local test that separates a legitimately high clock from an absurd one — a peer
returning from a long spell offline genuinely has a high clock. What the ceiling
buys is the difference between silent, permanent corruption and a refusal that
names itself: the edit reports `ErrExhausted` having changed nothing, and an edit
asks for room once for all of it, so it never happens by halves.

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


### And what the other two kinds need is not the same thing

`Composite.ApplyChanges` reports one `PartChange` per part that actually moved,
in the canonical part order. What each kind fills in is different, and the
difference was settled by reading the consumer rather than by symmetry:

| kind | reported | why |
|---|---|---|
| text | the edits, in order | an editor cannot re-read a document per keystroke and keep a cursor |
| map | the keys that changed, ascending | the view reads back the keys it is told about |
| list | nothing but the part | the views written against one read it back whole |

The list is the interesting one, because reporting positions would have been the
symmetric thing to do. A list here holds tens or hundreds of values, not the
hundreds of thousands a document holds — the same fact that makes it a slice
rather than a tree — and a view that re-reads it is not paying much. Naming
positions would be a second protocol to keep correct, fuzzed and covered for a
caller that does not exist. It is a field left empty rather than a kind left out,
so it can be filled in later without breaking anyone.

A key is named rather than its new value, which is also what keeps the report
honest when one batch writes a key twice: the key appears once, and reading it
gives the winner. Two batches for one part fold into one account of it — a view
is told about a key once, however the operations were grouped.

Only what happened is reported, everywhere: an operation already applied, one
still waiting for the operation its site issued before it, and a write that lost
to one already held all change nothing and say nothing. When a waiting one lands,
that call reports it. Watching costs the walk up the index per text operation,
and nothing at all for the parts nobody is watching — `Apply` takes a path that
does not so much as copy a version vector.

### What waiting costs, and how to stop paying it

An operation arriving before the one it depends on waits rather than being
dropped, because the number it would skip could be a key nobody would ever hear
about again. Nothing bounds how much waits.

Measured: operations that can never apply — each waiting on a sequence number
its site never issues — cost about **140 bytes apiece**, held for as long as the
document is, on a document that stays empty. That is around eight times what
they cost on the wire, and a peer chooses how many to send.

`DropPending` is the lever, and it is safe for one reason: a parked operation
has had no effect on the state, so it is **not in the version vector**. A peer
asked what this replica is missing sends it again. Dropping and re-syncing loses
nothing and diverges from nobody.

There is deliberately no cap in here. A cap has to decide what to do when it is
reached, and the only two answers are to drop — which is a policy, and the
caller's — or to refuse an `Apply` for reasons that have nothing to do with what
it was handed.

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

### Canonical goes all the way down, or it is not canonical

Every encoding here is canonical — the same state is the same bytes — and that
is a claim about *arriving* bytes as much as about produced ones. It was not,
until a fuzzer wrote a `1` as `{0x81, 0x00}`. `binary.Uvarint` accepts a varint
longer than its value needs, so a peer could hand over a snapshot that decoded
to a document already held and yet did not match its bytes: two encodings, one
state, which is exactly what the promise says cannot happen.

Every decoder in the package inherited that from one shared reader, so all four
formats had it. A varint's last byte carries the value's highest bits, so a zero
there says the encoding is longer than the value needs, and
`binary.AppendUvarint` never writes one except for zero itself, which is a
single byte. Refusing it therefore cannot reject anything this package writes —
which is asserted over the boundary values rather than assumed, because a rule
that only rejects is half tested.

### What a replica keeps, it keeps a copy of

An operation handed to `Apply` belongs to whoever handed it over: it may have
been decoded into a buffer a transport reuses for the next message. An operation
handed *back* by an edit belongs to the caller too, who broadcasts it and may
hold it. And a snapshot passed to a loader is the caller's to reuse or
overwrite the moment the call returns.

So nothing a replica keeps may alias any of the three. `List` did, on two of
them — `Apply` stored the caller's slice, and the operations `Insert` returned
shared their bytes with the elements just inserted — and the consequence is not
repairable by anything downstream: the value changes with no operation having
been applied, so the replica silently disagrees with every peer that received
the same operation, and no later operation says what it should have been.

### Trading the past for size, and what that costs

A replica remembers every operation it ever applied, deletions included, because
a replica that forgot could not tell a character arriving late from one it had
already seen. That memory is what lets two copies merge with no server, and it
is also why a heavily revised document is larger than its text.

`Rewritten` builds a new replica holding the same content and none of the past.
What it recovers is exactly what was deleted, and nothing else — which is worth
saying with numbers, because the intuition that documents grow without bound is
half wrong. On a text of 40 000 edits:

| deleted | with its past | rewritten | smaller by |
| --- | --- | --- | --- |
| nothing | 2226.6 KB | 2226.6 KB | 1.0x |
| a ninth | 2062.6 KB | 1781.3 KB | 1.2x |
| a third | 1816.5 KB | 1113.4 KB | 1.6x |
| all of it | 1386.8 KB | 0.1 KB | 17110x |

Live content is irreducible; only the dead part can be given back. A document
nobody deletes from has nothing to gain, and the common case — steady writing
with some revision — gains about a factor of two, not the order of magnitude the
emptied case suggests.

The price is every identity. The new replica mints its own, so an old copy's
operations anchor to characters the new one never had: they are not rejected and
they corrupt nothing, they park as pending and stay there. A rewrite therefore
*replaces* the replicas that preceded it rather than joining them, which is only
safe where a document is quiescent — being archived, or compacted by its single
writer.

And it is why there is no `Rewritten` on `Composite`. This is the trap
`Proposals` exists to avoid, one level down: rich text marks, tree parents and
sequence positions are stored against the identities of the characters they
describe, and a composite cannot tell a part carrying such anchors from a part
of plain text. Offering one would silently empty the mark table of every
formatted document it touched. Rewrite the parts known to be plain, or rebuild
the anchors deliberately.

Reclaiming space *without* breaking identities means collecting tombstones once
no replica can still refer to them, and that is not a smaller version of this.
In RGA an insertion anchors to the element preceding it, tombstones included, so
a tombstone is load-bearing until every replica that might anchor to it is
accounted for — which needs causal stability, and causal stability needs to know
the set of replicas. That is a different design, not a tuning of this one.

### Collecting what nothing can name any more

A rewrite gives back what was deleted at the price of every identity. Collection
gives back the same thing at a much smaller price, and the two are worth stating
side by side: on the same revised document, a rewrite is 2.2x smaller and
collection 2.07x — but a collected replica still merges with the replicas it
came from, and a rewritten one does not.

`Collect` takes a version every replica has delivered and drops the runs that
are entirely deleted at or below it. Three things make that safe:

- **A run goes whole or not at all.** A character's origin is the character
  before it in its own run, so collecting a prefix would leave the survivor
  behind it naming something that is gone.
- **Every deletion in it is below the given version.** An insertion names as its
  origin a character that was visible to whoever issued it; once no replica has
  the character visible, no operation naming it can still be written, and
  anything already written naming it has by the same argument already arrived.
- **Survivors that named it are re-pointed** at the nearest character still alive
  before it. This is not an optimisation. Without it nothing is ever collected:
  a run appended to a document names the last character of the run before it, so
  an entirely deleted run is almost always named by its successor. Measured on a
  document written and revised the ordinary way, 332 runs of 667 were entirely
  deleted and stable — and every single one was named by a survivor.

That the re-pointing does not move any text is the claim the design rests on, so
it is tested rather than argued: four hundred random histories, collected on one
replica and not on the other, then eighty concurrent edits each delivered in a
shuffled order, and the two must agree.

#### What it costs

**The past.** `TextAt`, `LenAt` and `ChangesSince` return `ErrCollected` below
`Floor` instead of a text with characters missing from it — a wrong answer about
the past being worse than none, since nothing downstream could tell the two
apart. A document that never collects has an empty floor and is unaffected.

**A format version.** A collected document has gaps where operations used to be,
and the snapshot's central integrity check counts a site's operations against
what the version vector promises. Version 6 writes the floor and, per site, how
many operations collection took away, so that check stays *exact* rather than
being relaxed to let a gap through. Versions 1 through 5 still load.

**A precondition somebody has to meet.** The version handed to `Collect` must be
one every replica has delivered. A server that fans operations out and collects
acknowledgements knows such a version; a replica on its own does not. Given a
version some replica has not reached, that replica's work arrives naming
characters that are gone — and is refused with `ErrStranded` rather than parked
for ever, because parking it is the silent version of the same failure.

What is deliberately still not done is collecting a tombstone that a survivor
*does* name by re-pointing across a replica that might yet appear. That is the
same trade as a rewrite, and it belongs to whoever decides a replica is gone.

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

The wire format is shared in the same way. `ListOp` carries `MarshalBinary` and
`UnmarshalBinary` as `Op` and `MapOp` do, and `AppendListOps`/`ParseListOps` are
`AppendOps`/`ParseOps` with a length-prefixed value where the text writes a rune.
What differs is the one thing the type differs by: a decoded value is copied,
because a list element is bytes rather than a rune and the buffer it was decoded
into belongs to a transport that reuses it.

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

That missing check is not free, and the clock ceiling is what stands in its
place. Because the text's loader demands its whole history back, a text snapshot
cannot promise a sequence number larger than the operations it carries. A map
snapshot can promise any number at all, which without a bound would let crafted
bytes hand a replica a counter one step from wrapping — the same defect as the
paragraph above, arriving by a different road. Both roads are now closed by the
same constant.

## A document of named parts

An editor holds a text, the comments on it, a record of changes, a chat and a
sheet of cells. Persisting those as five snapshots means five moments at which
they were saved and no instant at which the five agree — a session restored from
them can hold a comment on a sentence the text beside it does not have. `Composite`
holds them as named parts: one snapshot, one version, one thing to authorize.

It adds no merge rule. Everything below is about identity, about cost when there
are hundreds of parts, and about what its decoder has to refuse.

### Each part keeps its own counters

A part is an ordinary `Doc`, `List` or `Map`, with its own site counter, Lamport
clock and version vector. One counter for the whole document was considered and
rejected twice over. It would make `Doc.Version` describe operations a standalone
`Doc` knows nothing about, damaging three clean types for a container's
convenience — and it would not buy cross-part causality anyway, because
contiguous sequence numbers only order operations issued by the *same* site, so a
comment Bob writes on text Ada typed is unprotected either way.

The version is therefore per part: `map[Part]VersionVector`.

### A part is (name, kind), and exists because its operations exist

Two replicas that have never spoken may each reach for `"notes"`, one as a list
and one as a map. There is no operation to exchange that would tell them so.
Identifying a part by name alone makes that a conflict needing a tie-break, and
whichever way the tie-break falls one replica finds its writes gone; identifying
it by name *and* kind makes it two parts, which is convergent, needs no
arbitration, and needs no rule anybody has to know.

That is also why there is no part-creation operation. `c.List("comments")` on two
replicas at once must converge with nothing exchanged, and it does, because
reaching for a part is not an event.

### An empty part is not merely cheap — it does not exist

The consequence is forced rather than chosen. A replica that reached for `"chat"`
and typed nothing must produce the same snapshot bytes as one that never heard
the word, or two replicas holding exactly the same operations would disagree
about their state and the snapshot would stop being a convergence check. So a
part holding no operations is in no snapshot, in no version, and not among
`Parts()`. A part holding only tombstones has had operations and stays.

### What it costs in the number of parts

The consumer this was written for gives every comment its own map part, so that a
`resolved` flag flips with one `Set` rather than a delete and a reinsert. That
means hundreds of parts of eight keys each. Measured on 1 text part of 2 000
characters, 3 lists of 100 values and 300 maps of 8 keys, edited by three sites
whose identities are `DeriveSiteID` hashes — Apple M4 Max, Go 1.26.4,
`darwin/arm64`, `BenchmarkComposite*`:

| | | |
|---|---|---|
| `Snapshot` | 277 µs | 113 068 bytes |
| `LoadComposite` | 615 µs | |
| `OpsSince(nil)` — the whole history | 690 µs | |
| `OpsSince(v)` — a peer missing one part | 21 µs | |
| `Version().MarshalBinary()` | 110 µs | 16 052 bytes |

The last two are the ones that matter in a live session. `OpsSince` compares the
peer's vector against each part's before touching a history, so a peer that has
missed one comment does not pay for the other two hundred and ninety-nine; and
neither it nor `Version` asks `Parts()` for its parts in order, because sorting
three hundred names costs more than the answer. Sorting is left to the batches
actually being sent, of which there is usually one. That alone took `OpsSince`
from 71 µs to 21 µs.

A version travels on every join, so its encoding writes the sites once in a table
and each part's entries name an index into it. A `SiteID` is a whole uint64 —
`DeriveSiteID` uses the range — so it is ten bytes as a varint where an index into
a table of three is one. Measured: 16 052 bytes against 23 926 for the same
version with the identities repeated, a third less. What is left is mostly the
names themselves, and interning those would mean a version could not be read
without the snapshot that defines them, which is not a trade worth making for a
message this size.

### Operations on the wire

A version says where a peer is; `AppendPartOps` and `ParsePartOps` carry what it
is missing. They mirror `AppendOps` and `AppendMapOps` — validate everything
before writing a byte, count the batches and count the operations in each, refuse
trailing bytes and refuse a count larger than the remaining bytes could hold —
and they are what a gRPC service puts in a field: `Composite.OpsSince` returns
`[]PartOps`, this hands it over whole, and `Composite.Apply` takes it back.

The kind byte in front of a batch is what makes the encoding unable to express
the batch `PartOps.validate` refuses. Exactly one of `Text`, `List` and `Map` may
be populated, and the kind decides which decoder reads the operations back, so
there is nowhere to put a second slice rather than a rule saying there must not
be one.

Measured on the same document as the table above — same machine, `BenchmarkAppendPartOps`:

| | batches | operations | bytes | per operation |
|---|---|---|---|---|
| text | 1 | 2 002 | 55 663 | 27.8 |
| lists | 3 | 306 | 10 017 | 32.7 |
| maps | 300 | 3 000 | 92 100 | 30.7 |
| **whole history** | **304** | **5 308** | **157 782** | **29.7** |

Encoding it costs 102 µs and decoding it 255 µs. A map batch is 307 bytes, of
which 46 are the envelope — the kind, the 44-character part name and its length.
That is the shape a consumer with one map part per comment pays, and the reason a
batch names its part once rather than once per operation: written per operation
instead, those 44 characters would be most of the message.

What the table also says is where the bytes go, and it is not the content. An
operation carries its site as a whole `SiteID`, and its origin or target carries
another, so a text insertion spends about twenty of its twenty-eight bytes
restating two identities `DeriveSiteID` hashed into the full uint64 range. The
version encoding already solved this for itself with a site table, and the same
table would take roughly a third off this message. It is not done here because
these four functions mirror `AppendOps` and `AppendMapOps` deliberately — the same
shape, the same guarantees, one thing to learn — and interning sites would apply
to those two as well or to none. It is a format change, worth its own decision.

### Loading is a trust boundary, three times over

Each part's bytes go to that part's own loader, so the clock ceiling, the version
vector's promise and everything `Load`, `LoadList` and `LoadMap` refuse are
enforced there and not re-derived here. A composite has no clock of its own — it
mints nothing — so `MaxClock` reaches it only through a part, except in
`CompositeVersion.UnmarshalBinary`, which a part never sees and which holds the
ceiling itself.

What this decoder adds is what only it can see: that parts ascend strictly by
kind and then name, that a name is non-empty valid UTF-8, and that no part is
empty. The last is not tidiness. An empty part is one no snapshot carries, so
accepting one would mean re-encoding gave back different bytes.

The hazard the list loader had — a ledger sharing its version vector with the
list it was checking — has the same shape here and appears in two places. Each
part's loader is handed a window on the caller's buffer, which the caller may
overwrite; all three copy what they keep, and the round trip is asserted against a
buffer that is scribbled over after loading. And `Version` hands out copies of
every vector, because handing out the live ones would put the position a server
measures a client against under the control of the thing being measured.

One thing this loader deliberately does not insist on is that a part's bytes are
the *current* encoding. A text part written by an older build is still read by
`Load` and written back in the current form, so a snapshot this package accepts is
normalised on load while one it produced reloads to itself byte for byte.
Refusing the older form would buy an exact fixed point on arbitrary input at the
price of the migration that form exists for.

### A name

Names are arbitrary UTF-8 carrying structure — `file:src/main.tex`,
`comment:9f3c…`, `chat`. Invalid UTF-8 is refused, for the reason `Map` refuses
it in a key: a name crosses into JavaScript, where a string is UTF-16 and bytes
that are not text cannot survive the trip. The empty name is refused too, and
that is a decision rather than an omission — the name is the only thing telling
one part from another, `""` is what a caller passes when the name it meant to
compute never got computed, and two unrelated bugs would then share one part with
nothing downstream able to notice.

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

## The structured layer

`crdt/structured` is where documents are built, and the first thing to say about
it is that it re-implements nothing. There are three merging structures — a
text, a list, a map — and every type below is a way of using them. Convergence,
commutativity, idempotence and associativity are proved once, of the parts, and
inherited by everything composed from them; the package's own acceptance gate
runs those four laws plus a snapshot round trip over four of the document
types at once, against byte-equal snapshots rather than equal readings, because
two replicas can agree on every value and disagree about which write produced it.

What follows is why each type is shaped the way it is. In every case the shape
is forced by something the obvious version gets wrong.

### Identity, and records whose fields merge apart

A record stored as one opaque value loses an edit: two replicas that change two
different fields collide, and last-writer-wins discards one of them wholesale.
`RecordMap` gives each field its own map key, so only a genuine same-field
conflict is one. `Register` is the other degenerate case — one key, nothing to
merge that the map does not already merge.

Stable identities come from operations. A type that needs one writes a map key
whose value nobody reads, and takes the identity of the write; it is unique
across replicas, survives a reload, and is never reused. **The operation that
minted it has to be in what the caller sends** — a type that returned only the
writes that followed it left peers parking everything and the structure silently
stopped arriving.

### Things that move

`crdt.List` is an RGA. It decides where a new element goes against every other
element arriving at the same moment, per element, which is exactly right for
text — and it has no operation for moving something already in it. Written with
the operations it does have, a move is a delete and an insert: two operations,
which a concurrent move of the same item splits, leaving the item in both places
or in neither.

So `Sequence` and `Tree` carry position as a **rank**, a string with another
always available between any two. A move is one field write, and two replicas
moving the same item are two writes to one field, which the map already settles.
Order is (rank, identity), because two replicas inserting at the same place mint
the same rank and an order that stopped at the rank would not be one.

A `Tree` has a second problem a `Sequence` does not: a parent merges on its own,
but the *shape* two legal concurrent moves make between them is a ring. It is
resolved when the tree is read, by rules that are a function of the state alone.
A node whose parent is not a live node reads as a child of the root, so deleting
a folder does not delete a file a peer concurrently moved into it — the file
resurfaces, and that direction is deliberate. A node that cannot reach the root
is in a ring, broken at the node whose parent was written last by the map's own
order. No operation is discarded, so a later move can restore what a ring
detached.

### Three things a register cannot be

- **A count.** Read it, add one, write it back: two replicas holding 7 both write
  8 and a vote is lost. The mistake is not in the register — "add one" is not a
  value, and writing a value cannot say it. `Counter` keys the map by site, so
  concurrent additions are concurrent writes to *different* keys and nothing
  conflicts at all.
- **A disagreement.** Two people rename the same thing at once and one of the
  names is gone, with nothing anywhere recording that there was a second.
  `MultiRegister` keeps a version vector beside each replica's value and reads
  the ones nothing dominates. Choosing between them is writing the one chosen —
  a write dominates everything its writer could see — so there is no operation
  for settling. A vector rather than a clock, because a Lamport clock gives a
  total order and the question is which pairs are *unordered*.
- **A set.** Keyed by the name, one replica adds a label while another, which has
  never seen it, takes it away; one write wins by an order that has nothing to do
  with what either knew. In `Set` every addition mints a tag and a removal takes
  away the tags it can see. An addition nobody had seen is untouched — not as a
  policy but for want of anything to base one on.

### Documents

**Formatting.** Written into the sequence, two replicas bolding overlapping
stretches produce interleaved on and off markers and read differently; written
per character it converges and costs a write per letter, forever. A `RichText`
mark is one operation naming two boundaries, and the formatting is worked out
when the text is read. A boundary is a character and a side of it, which is
exactly the bold-continues / link-does-not distinction.

**Blocks.** A rich text per block converges and does not scale, for a reason
that has nothing to do with merging: a part cannot be taken out of a composite
and a version carries one entry per part, so a thousand blocks is a thousand
entries exchanged on every sync — whether the document still holds them or not.
`Blocks` is one text, one marks map and one blocks map: **three parts however
many blocks there are**, measured at 35 bytes of version against 18894.

A block begins at a marker character rather than at a boundary between two of
them, and that is the whole of why it works. "The end of this paragraph" and
"the start of the next" are the same offset and are not the same place; a
boundary faces one way, a character has two sides, so each intention is its own
insertion at its own place and the sequence keeps them apart with nothing
arbitrated. Nesting is a depth rather than a parent, because the order is
already decided by the text and a parent pointer is a second statement about it
that can contradict the first.

**Handwriting.** A stroke held as one value has its whole path rewritten by every
point, so the person watching sees the line redrawn rather than extended. `Ink`
appends points to a stream of their own, each saying which stroke it belongs to —
one stream rather than one part per stroke, for the reason `Blocks` gives.

**Files.** A file as one map value is one operation the size of the file: it
cannot be sent as it is read, resumed if the connection drops, or recognised as
one a peer already has. `Blobs` cuts it into chunks stored under the hash of
their own bytes, so the same chunk written by two replicas is the same key *and*
the same value — nothing to merge and nothing stored twice. Dedup must verify
rather than check presence, or a poisoned chunk can never be repaired by
re-putting the file.

`Sheet` and `Diagram` are thin wrappers over the same composite; that they share
it is the point.

### Three things that are not state

- **Undo** is not a stack of states. Restoring one throws away what everybody else
  has done since, and travels to them as an instruction to do the same. An undo
  here is a new edit, made now, whose effect is that the old one did not happen,
  and it reaches a peer as an ordinary edit. Edits go *through* the manager,
  because inverting one afterwards is impossible from what the document keeps: a
  reported change carries the text that was inserted, never the text that was
  removed.
- **History** costs nothing, because it is already kept. A document that merges
  without a server carries the identity of every operation and of every deletion,
  so "was this character there, and was it visible" is a question a version
  vector already answers. `TextAt`, `LenAt` and `ChangesSince` ask it. A map keeps
  one record per key, so a map-backed type can say when its current value was
  written and not what preceded it — said in the documentation rather than hidden.
- **A proposal** is a replica that has not synced. Two copies of a text can be
  compared, and what a comparison produces is a difference; applying a difference
  mints new characters, so every comment and cursor anchored to the ones it
  replaced is left pointing at nothing. `Proposals` records the document's own
  operations instead, made against the document's own identities. Accepting is
  `Apply`, which merges with whatever happened meanwhile because that is what a
  replica returning from offline does; turning one down costs nothing, because
  the operations were never applied.

## Next

The three things this section used to list are done:

1. **`Collab`** exists — a per-document gRPC hub over
   [`grpc-transports/websocket`](https://github.com/grpc-transports/websocket)
   that fans out operations, snapshots late joiners and persists, with no
   transform because the CRDT converges.
2. **The end-to-end proof** is `TestWasmConverges` in that repository: two
   `js/wasm` clients editing through the server, asserting convergence
   programmatically.
3. **Run-length blocks** shipped, at 73.1 → 4.19 bytes per character — see
   [performance.md](performance.md).

What is open now is smaller and is written down where it belongs rather than
here: the types this layer has not grown are named in the package documentation,
and each of them is named together with the reason it has not been built.

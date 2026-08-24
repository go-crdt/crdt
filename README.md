<p align="center"><img src="https://raw.githubusercontent.com/go-crdt/brand/main/social/go-crdt.png" alt="go-crdt/crdt" width="720"></p>

# crdt — collaborative text editing in pure Go

[![CI](https://github.com/go-crdt/crdt/actions/workflows/ci.yml/badge.svg)](https://github.com/go-crdt/crdt/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-crdt/crdt.svg)](https://pkg.go.dev/github.com/go-crdt/crdt)
[![coverage](https://img.shields.io/badge/coverage-100%25-brightgreen)](https://github.com/go-crdt/crdt/actions/workflows/ci.yml)
[![license](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)


`github.com/go-crdt/crdt` is a pure-Go, **CGO=0** conflict-free replicated data type
for plain text: any number of replicas may edit the same document at once,
offline, over an unreliable transport, and every replica ends up with the same
text. No server arbitrates, and no operation is ever transformed.

It compiles to `js/wasm`, so **the same merge logic runs on the server and in
every browser tab** — one implementation, one source of truth, no second
codebase in JavaScript to keep in step. The whole test suite, convergence
properties included, runs under `js/wasm` in CI.

Zero dependencies.

## Packages

| Package | Purpose |
|---|---|
| `crdt` | the replicated text document (`Doc`), a replicated sequence of values (`List`), a replicated map (`Map`) and a document of named parts (`Composite`) — operations, version vectors, snapshots |
| `crdt/awareness` | ephemeral presence — who is here and where their cursor is |
| `crdt/structured` | a shared substrate for co-editing **structured documents** over one core — a block document (`Blocks`), formatted text (`RichText`), a value replicas may disagree about (`MultiRegister`), a set of names (`Set`), changes put up for review (`Proposals`), a spreadsheet (`Sheet`), an isometric diagram (`Diagram`), a movable tree (`Tree`), a movable sequence (`Sequence`), a counter (`Counter`), chunked files (`Blobs`), handwriting (`Ink`) and undo (`Undo`), plus the `Register`, `RecordMap` and `Cell` pieces they compose |

## Using it

```go
ada, grace := crdt.New(1), crdt.New(2)

opening, _ := ada.Insert(0, "the quick fox")
grace.Apply(opening...)

// Both edit at once, neither having seen the other.
fromAda, _ := ada.Insert(10, "brown ")
fromGrace, _ := grace.Insert(13, " jumps")

ada.Apply(fromGrace...)
grace.Apply(fromAda...)

fmt.Println(ada)   // the quick brown fox jumps
fmt.Println(grace) // the quick brown fox jumps
```

A replica joining late loads a snapshot instead of the whole history; one coming
back from offline hands over its version vector and is sent only what it missed:

```go
client, err := crdt.Load(siteID, snapshot)   // join
missed := server.OpsSince(client.Version())  // catch up
```

## A list, for what sits beside the text

The things built around a document are sequences too — the comments on it, the
record of who changed what, the messages beside it — and they need the same
guarantees. `List` is the same algorithm over values the caller encodes:

```go
comments := crdt.NewList(site)
ops, _ := comments.Insert(0, encoded)   // values are opaque []byte
comments.Apply(fromPeers...)
anchor, _ := comments.Anchor(pos)       // a handle that survives other edits
```

It is a separate type rather than a generic one, deliberately: a document holds
hundreds of thousands of characters and earns run-length storage and an index
over runs, while a list holds tens or hundreds of values, where a slice is both
faster and obviously correct.

## A map, for the cells beside it

Not everything beside a document is a sequence. A spreadsheet is a map of cells,
a table of settings is a map of settings: written and cleared, never woven in
between their neighbours. `Map` merges those by last writer wins per key, under
the same `(clock, site)` order the text uses, over values the caller encodes:

```go
sheet := crdt.NewMap(site)
op, _ := sheet.Set("B7", []byte("42"))  // send op to every peer
value, ok := sheet.Get("B7")            // a copy; writing to it changes nothing
op, _ = sheet.Delete("B7")
sheet.Keys()                            // sorted, so every replica agrees
```

A deleted key keeps its clock. Dropping it would let an older write arriving
afterwards bring the key back on the replica that heard it late and not on the
one that heard it early, permanently — the classic mistake in a last-writer-wins
map, and the thing here most worth reading [docs/design.md](docs/design.md) for.

## One document, many parts

An editor does not hold one structure. It holds the text, the comments on it,
the record of who changed what, the messages beside it and a sheet of cells —
and persisting those separately means five snapshots saved at five moments, five
things to authorize, and no instant at which the set of them is consistent.
`Composite` is those parts under one name:

```go
doc := crdt.NewComposite(site)

text, _ := doc.Text("file:src/main.tex")   // a *Doc,  created on first use
chat, _ := doc.List("chat")                // a *List
cells, _ := doc.Map("cells")               // a *Map

snapshot := doc.Snapshot()                 // one thing to persist
missed := server.OpsSince(client.Version())
```

A part is identified by its **name and its kind together**, and it exists
because operations for it exist — so there is no operation to create one, and
two replicas that reach for `"chat"` at the same moment are already holding the
same part. It follows that a part which exists and holds nothing is
indistinguishable from one that was never created, and that has to be true
rather than convenient: an empty part is in no snapshot and no version, or two
replicas holding the same operations would disagree about their state.

Each part keeps its own site counter, clock and version vector, so a `Doc` inside
a composite behaves exactly as one standing alone, and the version is per part.
On a document of three hundred parts — one small map per comment, which is how a
`resolved` flag flips with one write instead of a delete and a reinsert — the
version encodes to 16 KB with the site identities shared in one table rather than
repeated in every part, a third less than writing them out.

## Keeping a view in step

A view of the text — an editor, a preview — has to be told what changed, not
what the text now is. Handed only the new text it would have to replace
everything, and replacing everything throws away the selection, the scroll
position, the folded regions and the decorations, on every keystroke anybody
else makes.

`ApplyChanges` is `Apply` and also reports the edits, in the order they have to
be made, coalesced: a peer typing a word is one change, not one per letter.
`Apply` does not pay for that.

## Anchoring, and who wrote what

An offset names a place; an anchor names a character, and keeps naming it however
the document moves around it. That is what a comment, a mark or a stored
selection should be:

```go
changes, _ := doc.ApplyChanges(ops...) // what a view has to do to catch up
anchor, _ := doc.Anchor(pos)     // the identity of the character there
pos, ok := doc.Position(anchor)  // where it is now — or where it was, if deleted
doc.Visible(anchor)              // whether it is still in the text
doc.AuthorRuns()                 // the visible text split by who wrote each stretch
```

Every character already carried the identity of the operation that created it,
and the site is part of that identity, so none of this costs the document
anything to store.

## Counting the way a browser counts

The document counts characters. CodeMirror, the DOM, the Language Server
Protocol and every index into a JavaScript string count UTF-16 code units, in
which an emoji, an extended CJK ideograph or a `𝔸` is **two** units and one
character. An editor handing its cursor offset to `Insert` therefore edits in
the wrong place as soon as the document holds one of them — silently, and with
nothing left behind that a later read could notice.

So the same operations are addressed both ways, and a caller who counts in
UTF-16 never converts by hand:

```go
doc.LenUTF16()                 // what JavaScript's String.length would report
doc.InsertUTF16(pos, "text")   // pos in code units
doc.DeleteUTF16(pos, n)        // pos and n in code units
doc.UTF16Offset(runePos)       // and the conversion, both ways
doc.RuneOffset(utf16Pos)
```

An offset landing between the two units of one character is refused rather than
rounded; [docs/design.md](docs/design.md) says why, and how to round it in one
step if that is what you want. A document holding no such character converts in
constant time, so nothing pays for this until it has an emoji in it. The answers
are checked against node's, not against ours.

## What it guarantees

- **Convergence.** Replicas holding the same operations hold the same document,
  whatever order those operations arrived in. Proven by randomised sessions with
  late, reordered and duplicated delivery, *and* by enumerating every permutation
  of small histories.
- **Idempotence and reordering.** Duplicates are ignored; an operation arriving
  before what it depends on is buffered until that lands. A transport needs to be
  eventually complete, not ordered.
- **Determinism.** No wall clock, no randomness. Replica identity is injected by
  the caller, so a document behaves identically on a server and in a browser.
- **Intent.** A run of characters typed one after another is never chopped up by
  someone else's concurrent insertion.
- **Nothing is trusted.** Every decoder is fuzzed; a snapshot that no replica
  could have produced is rejected rather than loaded; and operations arranged to
  make integration walk the whole document cost a descent of an index instead.

## Status

Version 0.27: the text, list and map CRDTs, a composite document that holds them
as named parts, the wire and snapshot formats, awareness, and the surface an
editor needs — reported changes, anchors, authorship, UTF-16 addressing, undo,
and reading a document as it stood at any version.
Pure Go, CGO=0, **100% statement coverage** on both packages, race-clean, six-arch
CI, and the full suite green under `js/wasm`.

A real editing history — 259 778 edits from the trace text CRDTs are commonly
measured on — replays in **18.4 ms** and matches the recorded text exactly; the
same history delivered back to front, nothing applicable until the last
operation, settles in 0.25 s, and the document encodes to 620 KB. See [docs/performance.md](docs/performance.md).

See [docs/design.md](docs/design.md) for how it works and why, and
[docs/performance.md](docs/performance.md) for what it costs and what changes
next.

## License

BSD-3-Clause — see [LICENSE](LICENSE). Copyright the go-crdt authors.

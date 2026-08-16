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
| `crdt` | the replicated text document: `Doc`, operations, version vectors, snapshots |
| `crdt/awareness` | ephemeral presence — who is here and where their cursor is |

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
  could have produced is rejected rather than loaded.

## Status

Version 0.5: the sequence CRDT, the wire and snapshot formats, and awareness.
Pure Go, CGO=0, **100% statement coverage** on both packages, race-clean, six-arch
CI, and the full suite green under `js/wasm`.

A real editing history — 259 778 edits from the trace text CRDTs are commonly
measured on — replays in **24.4 ms** and matches the recorded text exactly; the
same history delivered back to front, nothing applicable until the last
operation, settles in 0.24 s, and the document encodes to 620 KB. See [docs/performance.md](docs/performance.md).

See [docs/design.md](docs/design.md) for how it works and why, and
[docs/performance.md](docs/performance.md) for what it costs and what changes
next.

## License

BSD-3-Clause — see [LICENSE](LICENSE). Copyright the go-crdt authors.

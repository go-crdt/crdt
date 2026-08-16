# Comparison harness

Replays the `automerge-paper` editing trace against Yjs, Automerge, Loro and
diamond-types, so that their numbers and ours come from one machine, one trace
and one protocol. The results are in [../performance.md](../performance.md).

```sh
curl -sLO https://raw.githubusercontent.com/josephg/editing-traces/master/sequential_traces/automerge-paper.json.gz
npm install
export CRDT_TRACE=$PWD/automerge-paper.json.gz

for impl in diamond-types loro yjs automerge automerge-wasm; do
  node --expose-gc bench.js "$impl" --runs 10
done

node --expose-gc bench.js yjs --mem        # memory, JavaScript heap only
node --expose-gc bench.js yjs-nogc --mem
```

And ours, from the repository root:

```sh
CRDT_TRACE=$PWD/docs/comparison/automerge-paper.json.gz \
  go test -run '^$' -bench 'EditingTrace$' -benchtime 10x -count 5 .
```

Every run reconstructs the document and compares it against the `endContent`
recorded in the trace before reporting a time; a replay that produces the wrong
text fails instead of printing a number. Verification is outside the clock.

Each implementation is driven the way its own published benchmark drives it —
see the comment at the top of `bench.js`. The variants (`yjs-transact`,
`yjs-nogc`, `automerge-per-edit`) are there to show what those choices cost, not
to pick a flattering one.

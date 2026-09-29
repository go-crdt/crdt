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

## Comparing two implementations, rather than two moments

Running `bench.js fugue` and then `bench.js yjs` gives each library a different
stretch of wall-clock time. If the machine's load moves in between — and on a
workstation it does — the movement is charged to whichever library happened to be
running, and neither result says so.

`paired.js` asks every arm for one replay each, in rotation, with each process
staying warm between its replays. Every round is then a paired sample, and the
median of the per-round *ratios* survives a load that drifts in a way the ratio
of two medians does not.

```sh
export CRDT_TRACE=$PWD/automerge-paper.json.gz
node paired.js --rounds 20 --arms ours,fugue,yjs --out interleaved.jsonl
node paired.js --rounds 15 --arms ours,fugue,yjs --block --out block.jsonl
```

The first arm is the baseline every ratio is taken against. `ours` is the Go
worker in `../../trace_test.go`, gated on `CRDT_SERVE` and compiled by
`paired.js` before the session starts — a test rather than a program of its own
so that the loader and the replay are the ones the benchmark and the correctness
test use.

`--block` is the control: the same processes and the same code, each arm doing
all of its replays consecutively. Run it beside the interleaved form and the only
difference between the two is when the replays happened. On a machine whose load
is steady they agree; a block is not wrong, it is unprotected.

**The one-minute load average cannot label a measurement.** It is an exponential
average, so it lags by about a minute in both directions: the first round of a
run started beside twelve busy loops read `load1` 10.7 — a value the uncontended
control had spent its whole run inside — while every arm was already 20–30%
slower than its uncontended figure. Each line of the `--out` file records
what `load1` said, so a reader can see it — not so a result can be justified by
it. What contention does to a comparison is in
[../performance.md](../performance.md), and it is not a constant factor: it does
not fall on every implementation alike.

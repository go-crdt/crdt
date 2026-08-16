// Shared harness for replaying the automerge-paper editing trace against
// several CRDT implementations, so that every one of them is measured the same
// way on the same machine.
//
// The trace is github.com/josephg/editing-traces/sequential_traces/automerge-paper.json.gz:
// 259 778 single-character edits, ending in 104 852 characters. Every patch is
// [position, deleteCount, insertedText]; positions are plain character offsets
// and the whole trace is ASCII, so UTF-16 code units, code points and bytes all
// coincide and no index conversion can favour one library over another.

'use strict'

const fs = require('fs')
const zlib = require('zlib')

// loadTrace reads the trace named by CRDT_TRACE (or the first CLI argument) and
// returns the flat list of patches together with the text they must produce.
// Parsing is deliberately outside every measurement, exactly as it is in the Go
// test we compare against.
function loadTrace () {
  const path = process.env.CRDT_TRACE || process.argv[2]
  if (!path) {
    throw new Error('set CRDT_TRACE to the trace file (.json or .json.gz)')
  }
  let buf = fs.readFileSync(path)
  if (path.endsWith('.gz')) buf = zlib.gunzipSync(buf)
  const raw = JSON.parse(buf.toString('utf8'))
  if (raw.startContent !== '') {
    throw new Error('this harness assumes the trace starts from an empty document')
  }
  const patches = []
  for (const txn of raw.txns) {
    for (const p of txn.patches) {
      if (p.length !== 3) throw new Error(`a patch has ${p.length} fields, want 3`)
      patches.push(p)
    }
  }
  return { patches, endContent: raw.endContent }
}

// stats reports the shape of a set of timings rather than a single number, so
// that a lucky run cannot be mistaken for a result.
function stats (samples) {
  // Timings arrive as BigInt nanoseconds from process.hrtime.bigint(); a
  // document replay is milliseconds, so Number holds it exactly and sorts.
  const s = samples.map(Number).sort((a, b) => a - b)
  const mid = s.length >> 1
  return {
    runs: s.length,
    min: s[0],
    median: s.length % 2 ? s[mid] : (s[mid - 1] + s[mid]) / 2,
    max: s[s.length - 1]
  }
}

function ms (ns) { return Number(ns) / 1e6 }

// heapUsed collects until the heap stops shrinking, then reads it. A single
// global.gc() routinely leaves a megabyte or two of freshly dead objects behind,
// which is the difference between a memory figure and a guess.
function heapUsed () {
  if (typeof global.gc !== 'function') {
    throw new Error('run node with --expose-gc for the memory measurement')
  }
  let last = Infinity
  for (let i = 0; i < 10; i++) {
    global.gc()
    const used = process.memoryUsage().heapUsed
    if (used >= last - 4096) return used
    last = used
  }
  return process.memoryUsage().heapUsed
}

// report prints one implementation's results in a fixed, greppable form.
function report (name, timings, extra) {
  const t = stats(timings)
  const edits = extra.edits
  console.log(JSON.stringify({
    implementation: name,
    edits,
    runs: t.runs,
    first_run_ms: +ms(timings[0]).toFixed(2),
    min_ms: +ms(t.min).toFixed(2),
    median_ms: +ms(t.median).toFixed(2),
    max_ms: +ms(t.max).toFixed(2),
    median_ns_per_edit: +(Number(t.median) / edits).toFixed(1),
    ...extra
  }, null, 0))
}

module.exports = { loadTrace, stats, ms, heapUsed, report }

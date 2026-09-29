// Asks several implementations for one replay each, in rotation, instead of
// running each one as its own block of wall-clock time.
//
//   node paired.js [--rounds N] [--arms a,b,c] [--block] [--out FILE]
//
// Why this exists. A comparison that runs `bench.js yjs` and then
// `bench.js fugue` gives each library a different stretch of machine. If the
// load moves in between — and on a workstation it does — the movement is charged
// to whichever library was running, and nothing in either result says so. That
// is not hypothetical: it is how the Fugue row in ../performance.md came out 14%
// too high, and how it was found.
//
// Interleaved, every round is a paired sample: the ratio within a round is taken
// against a replay that met the same machine seconds earlier, and the median of
// those ratios survives a load that drifts. Each process stays warm between its
// replays, so nothing here trades the pairing against a cold run.
//
// The order rotates every round, so no implementation always speaks first and a
// rhythm in what the machine is doing cannot settle on one of them.
//
// --block is the control: the same processes, the same code, each implementation
// doing all of its replays consecutively. Run it beside the interleaved form and
// the difference between them is the scheduling and nothing else. On a machine
// whose load is steady the two agree, which is the useful thing to know — a
// block is not wrong, it is unprotected.
'use strict'

const { spawn, spawnSync } = require('child_process')
const os = require('os')
const fs = require('fs')
const path = require('path')

const args = process.argv.slice(2)
const flag = (f, d) => {
  const i = args.indexOf(f)
  return i === -1 ? d : args[i + 1]
}
const ROUNDS = Number(flag('--rounds', 15))
const ARMS = String(flag('--arms', 'ours,fugue,yjs')).split(',')
const BLOCK = args.includes('--block')
const OUT = flag('--out', null)
// --repeats ours=8 makes that arm average eight replays per sample. Comparing a
// 20 ms sample against a 200 ms one under contention measures the difference in
// duration as well as the difference in implementation; this is how to take that
// difference away and see what is left.
const REPEATS = Object.fromEntries(
  String(flag('--repeats', '')).split(',').filter(Boolean).map(p => {
    const [a, n] = p.split('=')
    return [a, Number(n)]
  }))

const trace = process.env.CRDT_TRACE
if (!trace) {
  console.error('set CRDT_TRACE to the trace file (.json or .json.gz)')
  process.exit(2)
}
const repo = path.resolve(__dirname, '..', '..')

// Our own arm is the gated worker in trace_test.go rather than a program of its
// own, so that the loader and the replay are the ones the benchmark and the
// correctness test use. A second copy of them could drift, and a comparison
// whose two sides are timed differently measures the harness.
//
// It is compiled and then driven directly, because  holds a test's
// output until the test has finished: run through the go command, the worker's
// first line would arrive after the last replay it was asked for. Compiling
// first also keeps the build out of the session the timings come from.
function buildOurs () {
  const bin = path.join(os.tmpdir(), `crdt-paired-${process.pid}.test`)
  const r = spawnSync('go', ['test', '-c', '-o', bin, '.'], { cwd: repo, stdio: ['ignore', 'inherit', 'inherit'] })
  if (r.status !== 0) {
    console.error('compiling the Go worker failed')
    process.exit(1)
  }
  process.on('exit', () => { try { fs.unlinkSync(bin) } catch {} })
  return bin
}
const ours = () => ({
  name: 'ours',
  cmd: buildOurs(),
  args: ['-test.run', 'TestServeTraceReplays', '-test.timeout', '0'],
  cwd: repo,
  env: { CRDT_SERVE: '1' }
})
const js = name => ({
  name,
  cmd: process.execPath,
  args: ['--expose-gc', path.join(__dirname, 'bench.js'), name, '--serve'],
  cwd: __dirname,
  env: {}
})

const specs = ARMS.map(a => (a === 'ours' ? ours() : js(a)))

function start (s) {
  const p = spawn(s.cmd, s.args, {
    cwd: s.cwd,
    env: { ...process.env, CRDT_TRACE: trace, ...s.env }
  })
  p.stderr.on('data', d => process.stderr.write(`[${s.name}] ${d}`))
  const w = { ...s, proc: p, buf: '', waiters: [] }
  p.stdout.setEncoding('utf8')
  p.stdout.on('data', chunk => {
    w.buf += chunk
    let i
    while ((i = w.buf.indexOf('\n')) !== -1) {
      const line = w.buf.slice(0, i).trim()
      w.buf = w.buf.slice(i + 1)
      // `go test` prints its own PASS and ok lines on the same stream. Only the
      // JSON lines are ours, and a reply that is not JSON must not be mistaken
      // for one — a dropped line would silently pair the wrong two replays.
      if (!line.startsWith('{')) continue
      const waiter = w.waiters.shift()
      if (waiter) waiter(JSON.parse(line))
    }
  })
  p.on('exit', code => {
    if (code !== 0) {
      console.error(`${s.name} exited ${code}`)
      process.exit(1)
    }
  })
  return w
}

const next = w => new Promise(res => w.waiters.push(res))

async function main () {
  const workers = specs.map(start)
  const ready = await Promise.all(workers.map(next))
  const edits = ready.map(r => r.edits)
  if (new Set(edits).size !== 1) {
    console.error(`the arms loaded different traces: ${edits.join(', ')} edits`)
    process.exit(1)
  }
  console.error(`ready: ${workers.map(w => w.name).join(', ')}, ${edits[0]} edits each`)

  const plan = []
  if (BLOCK) {
    for (const w of workers) for (let r = 0; r < ROUNDS; r++) plan.push([r, w, 0])
  } else {
    for (let r = 0; r < ROUNDS; r++) {
      for (let pos = 0; pos < workers.length; pos++) {
        plan.push([r, workers[(pos + r) % workers.length], pos])
      }
    }
  }

  const out = OUT ? fs.createWriteStream(OUT) : null
  const rounds = new Map()
  for (const [r, w, pos] of plan) {
    // The one-minute load average is recorded beside every timing. It is a
    // lagging average, so it does not prove the machine was quiet — it is here
    // so a reader can see what it said, not so a result can be labelled with it.
    const load1 = +os.loadavg()[0].toFixed(2)
    w.proc.stdin.write(`${REPEATS[w.name] || ''}\n`)
    const res = await next(w)
    if (out) out.write(JSON.stringify({ round: r, impl: w.name, pos, ms: res.ms, replays: res.replays, load1, t: Date.now() }) + '\n')
    if (!rounds.has(r)) rounds.set(r, {})
    rounds.get(r)[w.name] = res.ms
    process.stderr.write(`round ${r} ${w.name.padEnd(6)} ${res.ms.toFixed(2).padStart(9)} ms  load1 ${load1}\n`)
  }
  if (out) out.end()
  for (const w of workers) w.proc.stdin.end()

  const median = xs => {
    const s = [...xs].sort((a, b) => a - b)
    const m = s.length >> 1
    return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2
  }
  const base = ARMS[0]
  const report = { schedule: BLOCK ? 'block' : 'interleaved', rounds: ROUNDS, arms: {} }
  for (const a of ARMS) {
    const xs = [...rounds.values()].map(r => r[a]).filter(x => x !== undefined)
    const ratios = [...rounds.values()]
      .filter(r => r[a] !== undefined && r[base] !== undefined)
      .map(r => r[a] / r[base])
    report.arms[a] = {
      median_ms: +median(xs).toFixed(2),
      min_ms: +Math.min(...xs).toFixed(2),
      max_ms: +Math.max(...xs).toFixed(2),
      // The median of the per-round ratios, not the ratio of the two medians:
      // pairing is the whole point, and taking it first is what keeps a drift
      // out of the answer.
      [`median_ratio_to_${base}`]: +median(ratios).toFixed(2),
      [`ratio_spread_to_${base}`]: `${Math.min(...ratios).toFixed(2)}–${Math.max(...ratios).toFixed(2)}`
    }
  }
  console.log(JSON.stringify(report, null, 2))
}

main()

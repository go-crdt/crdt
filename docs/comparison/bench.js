// Replays the automerge-paper editing trace against Yjs, Automerge and
// diamond-types, so their numbers and ours come from the same machine, the same
// trace and the same protocol.
//
//   node --expose-gc bench.js <impl> [--runs N] [--mem] [--limit N]
//
// One file rather than one per library on purpose: the timing loop, the
// verification and the memory reading are then provably identical for every
// implementation, and only the adapter differs.
//
// Every implementation is driven the way its own published benchmark drives it:
//
//   yjs           per-edit calls on a Y.Text, no wrapping transaction, as in
//                 dmonad/crdt-benchmarks js-lib/b4.js
//   automerge     the whole trace inside a single Automerge.change using
//                 Automerge.splice, as in automerge/automerge
//                 rust/edit-trace/automerge-js.js
//   diamond-types Doc.ins / Doc.del, the API josephg/diamond-types exposes
//
// The variants (yjs-transact, yjs-nogc, automerge-per-edit) exist to show what
// those choices cost, not to pick a flattering one.

'use strict'

const fs = require('fs')
const path = require('path')
const { loadTrace, stats, ms, heapUsed } = require('./common')

// Several of these packages block `require('pkg/package.json')` through their
// "exports" map, so read the manifest off disk instead of asking the loader.
function pkgVersion (name) {
  const p = path.join(__dirname, 'node_modules', name, 'package.json')
  return JSON.parse(fs.readFileSync(p, 'utf8')).version
}

const impls = {
  // ---- Yjs -----------------------------------------------------------------
  yjs: {
    version: () => pkgVersion('yjs'),
    create () {
      const Y = require('yjs')
      const doc = new Y.Doc() // gc: true, the default every Yjs user gets
      return { Y, doc, text: doc.getText('text') }
    },
    replay (c, patches) {
      const text = c.text
      for (let i = 0; i < patches.length; i++) {
        const p = patches[i]
        if (p[1] > 0) text.delete(p[0], p[1])
        if (p[2]) text.insert(p[0], p[2])
      }
      return c
    },
    text: c => c.text.toString(),
    // V2 is the encoding dmonad/crdt-benchmarks reports as docSize; V1 is what
    // most Yjs deployments send on the wire. Both are recorded.
    bytes: c => c.Y.encodeStateAsUpdateV2(c.doc).byteLength,
    altBytes: c => c.Y.encodeStateAsUpdate(c.doc).byteLength,
    free: c => c.doc.destroy(),
    jsHeap: true
  },

  // The same, with every edit inside one transaction. Yjs's own benchmark does
  // not do this; it is here because it is the obvious way to make Yjs look
  // faster and the difference should be visible rather than quietly chosen.
  'yjs-transact': {
    version: () => pkgVersion('yjs'),
    create: () => impls.yjs.create(),
    replay (c, patches) {
      c.doc.transact(() => { impls.yjs.replay(c, patches) })
      return c
    },
    text: c => c.text.toString(),
    // V2 is the encoding dmonad/crdt-benchmarks reports as docSize; V1 is what
    // most Yjs deployments send on the wire. Both are recorded.
    bytes: c => c.Y.encodeStateAsUpdateV2(c.doc).byteLength,
    altBytes: c => c.Y.encodeStateAsUpdate(c.doc).byteLength,
    free: c => c.doc.destroy(),
    jsHeap: true
  },

  // gc: false keeps deleted content, which is what our implementation does with
  // its tombstones. Without this the memory columns are not comparing the same
  // thing.
  'yjs-nogc': {
    version: () => pkgVersion('yjs'),
    create () {
      const Y = require('yjs')
      const doc = new Y.Doc({ gc: false })
      return { Y, doc, text: doc.getText('text') }
    },
    replay: (c, patches) => impls.yjs.replay(c, patches),
    text: c => c.text.toString(),
    // V2 is the encoding dmonad/crdt-benchmarks reports as docSize; V1 is what
    // most Yjs deployments send on the wire. Both are recorded.
    bytes: c => c.Y.encodeStateAsUpdateV2(c.doc).byteLength,
    altBytes: c => c.Y.encodeStateAsUpdate(c.doc).byteLength,
    free: c => c.doc.destroy(),
    jsHeap: true
  },

  // ---- Automerge -----------------------------------------------------------
  automerge: {
    version: () => pkgVersion('@automerge/automerge'),
    create () {
      const A = require('@automerge/automerge')
      return { A, doc: A.from({ text: '' }) }
    },
    replay (c, patches) {
      const A = c.A
      c.doc = A.change(c.doc, d => {
        for (let i = 0; i < patches.length; i++) {
          const p = patches[i]
          A.splice(d, ['text'], p[0], p[1], p[2])
        }
      })
      return c
    },
    text: c => c.doc.text,
    bytes: c => c.A.save(c.doc).byteLength,
    free: () => {},
    jsHeap: false // the document lives in WebAssembly memory, not the V8 heap
  },

  // One Automerge.change per edit. Automerge's own benchmark batches, and this
  // shows why; it is far too slow for the whole trace, so use --limit.
  'automerge-per-edit': {
    version: () => pkgVersion('@automerge/automerge'),
    create: () => impls.automerge.create(),
    replay (c, patches) {
      const A = c.A
      for (let i = 0; i < patches.length; i++) {
        const p = patches[i]
        c.doc = A.change(c.doc, d => { A.splice(d, ['text'], p[0], p[1], p[2]) })
      }
      return c
    },
    text: c => c.doc.text,
    bytes: c => c.A.save(c.doc).byteLength,
    free: () => {},
    jsHeap: false
  },

  // Automerge's Rust core driven directly, bypassing the JavaScript document
  // wrapper, as in automerge/automerge rust/edit-trace/automerge-wasm.js. This
  // is not the API an Automerge user writes against; it is here to show how much
  // of the cost above is the wrapper and how much is the core.
  'automerge-wasm': {
    version: () => pkgVersion('@automerge/automerge-wasm'),
    create () {
      const W = require('@automerge/automerge-wasm')
      const doc = W.create()
      return { W, doc, text: doc.putObject('_root', 'text', '') }
    },
    replay (c, patches) {
      const doc = c.doc; const text = c.text
      for (let i = 0; i < patches.length; i++) {
        const p = patches[i]
        doc.splice(text, p[0], p[1], p[2])
      }
      return c
    },
    text: c => c.doc.text(c.text),
    bytes: c => c.doc.save().byteLength,
    free: c => c.doc.free(),
    jsHeap: false
  },

  // ---- Loro ----------------------------------------------------------------
  // Not asked for, but dmonad/crdt-benchmarks reports Loro as the fastest entry
  // in its table, so leaving it out would be choosing the opposition.
  loro: {
    version: () => pkgVersion('loro-crdt'),
    create () {
      const { LoroDoc } = require('loro-crdt')
      const doc = new LoroDoc()
      return { doc, text: doc.getText('text') }
    },
    replay (c, patches) {
      const text = c.text
      for (let i = 0; i < patches.length; i++) {
        const p = patches[i]
        if (p[1] > 0) text.delete(p[0], p[1])
        if (p[2]) text.insert(p[0], p[2])
      }
      return c
    },
    text: c => c.text.toString(),
    bytes: c => c.doc.export({ mode: 'snapshot' }).byteLength,
    free: () => {},
    jsHeap: false
  },

  // ---- diamond-types -------------------------------------------------------
  'diamond-types': {
    version: () => pkgVersion('diamond-types-node'),
    create () {
      const { Doc } = require('diamond-types-node')
      return { doc: new Doc('bench') }
    },
    replay (c, patches) {
      const doc = c.doc
      for (let i = 0; i < patches.length; i++) {
        const p = patches[i]
        if (p[1] > 0) doc.del(p[0], p[1])
        if (p[2]) doc.ins(p[0], p[2])
      }
      return c
    },
    text: c => c.doc.get(),
    bytes: c => c.doc.toBytes().byteLength,
    free: c => c.doc.free(),
    jsHeap: false // WebAssembly memory again
  }
}

function main () {
  const args = process.argv.slice(2)
  const name = args[0]
  const impl = impls[name]
  if (!impl) {
    console.error(`usage: node --expose-gc bench.js <${Object.keys(impls).join('|')}> [--runs N] [--mem] [--limit N]`)
    process.exit(2)
  }
  const flag = (f, d) => {
    const i = args.indexOf(f)
    return i === -1 ? d : Number(args[i + 1])
  }
  const runs = flag('--runs', 10)
  const limit = flag('--limit', 0)
  const memMode = args.includes('--mem')

  let { patches, endContent } = loadTrace()
  let expected = endContent
  if (limit > 0) {
    // A prefix of the trace still has a known answer: replay it once with the
    // implementation itself and require every later run to reproduce that. The
    // full trace is checked against the recorded text instead, which is
    // stronger, so a prefix run is only ever used for the slow variants.
    patches = patches.slice(0, limit)
    expected = null
  }

  const meta = {
    implementation: name,
    library_version: impl.version(),
    node: process.version,
    edits: patches.length
  }

  if (memMode) {
    if (!impl.jsHeap) {
      console.log(JSON.stringify({
        ...meta,
        memory: null,
        note: 'document lives in WebAssembly linear memory; process.memoryUsage().heapUsed cannot see it, so no figure is reported'
      }))
      return
    }
    const before = heapUsed()
    const c = impl.replay(impl.create(), patches)
    const after = heapUsed()
    const text = impl.text(c)
    if (expected !== null && text !== expected) throw new Error('replayed text does not match the recorded final text')
    const held = after - before
    console.log(JSON.stringify({
      ...meta,
      chars: text.length,
      held_bytes: held,
      held_kib: +(held / 1024).toFixed(0),
      bytes_per_char: +(held / text.length).toFixed(2),
      serialized_bytes: impl.bytes(c)
    }))
    global.__keep = c // the reading above must not be of a collected document
    return
  }

  const timings = []
  let text = null
  let serialized = 0
  let alt = null
  for (let r = 0; r < runs; r++) {
    const c = impl.create()
    const t0 = process.hrtime.bigint()
    impl.replay(c, patches)
    const t1 = process.hrtime.bigint()
    timings.push(t1 - t0) // verification is deliberately outside the clock
    text = impl.text(c)
    if (expected === null) expected = text
    if (text !== expected) throw new Error(`run ${r}: replayed text does not match`)
    serialized = impl.bytes(c)
    if (impl.altBytes) alt = impl.altBytes(c)
    impl.free(c)
  }

  const t = stats(timings)
  console.log(JSON.stringify({
    ...meta,
    runs,
    verified: true,
    final_chars: text.length,
    first_run_ms: +ms(timings[0]).toFixed(2),
    min_ms: +ms(t.min).toFixed(2),
    median_ms: +ms(t.median).toFixed(2),
    max_ms: +ms(t.max).toFixed(2),
    median_ns_per_edit: +(Number(t.median) / patches.length).toFixed(1),
    serialized_bytes: serialized,
    ...(alt === null ? {} : { serialized_bytes_alt: alt })
  }))
}

main()

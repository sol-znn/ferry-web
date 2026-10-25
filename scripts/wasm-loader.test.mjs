// Run the actual loader with synthetic runtime/stream boundaries. No wallet,
// chain, hosted build or remote fetch is involved.
import assert from 'node:assert/strict'
import {dirname, resolve} from 'node:path'
import test from 'node:test'
import {fileURLToPath, pathToFileURL} from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const flush = () => new Promise((done) => setImmediate(done))
let serial = 0

async function fixture(t, options = {}) {
  const saved = Object.fromEntries(['window', 'document', 'fetch', '__WASM_VERSION__'].map((key) =>
    [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
  const originalInstantiate = WebAssembly.instantiate
  const window = new EventTarget()
  const scripts = []
  const requests = []
  let runs = 0
  const never = () => new Promise(() => {})
  globalThis.window = window
  globalThis.__WASM_VERSION__ = 'synthetic-loader-fixture'
  globalThis.document = {
    createElement: () => {
      const script = {removed: false, remove() { this.removed = true }}
      scripts.push(script)
      return script
    },
    head: {appendChild(script) {
      if (options.script === 'pending') return
      queueMicrotask(() => {
        if (options.script === 'error') { script.onerror?.(); return }
        window.Go = class {
          importObject = {}
          run() {
            runs++
            if (options.run) return options.run(window)
            window.ferryWasm = {ready: true, call: async () => '{}'}
            return never()
          }
        }
        script.onload?.()
      })
    }},
  }
  globalThis.fetch = (url, init) => {
    requests.push({url, init})
    return options.fetch?.(url, init) ?? Promise.resolve(new Response(new Uint8Array([0, 97, 115, 109])))
  }
  WebAssembly.instantiate = options.instantiate ?? (async () => ({instance: {exports: {}}, module: {}}))
  const loader = await import(`${pathToFileURL(resolve(root, 'ui/src/core/wasm.ts')).href}?fixture=${serial++}`)
  t.after(() => {
    WebAssembly.instantiate = originalInstantiate
    for (const [key, descriptor] of Object.entries(saved)) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor)
      else delete globalThis[key]
    }
  })
  return {loader, window, scripts, requests, runs: () => runs}
}

async function observed(predicate) {
  for (let attempt = 0; attempt < 30 && !predicate(); attempt++) await flush()
  assert.ok(predicate(), 'expected load boundary was reached')
}

test('successful loader cleans its script and starts one callable engine', async (t) => {
  const {loader, scripts, requests, runs} = await fixture(t)
  await Promise.all([loader.startWasm(), loader.startWasm()])
  assert.equal(loader.loadState().phase, 'ready')
  assert.equal(scripts.length, 1)
  assert.equal(scripts[0].removed, true)
  assert.equal(scripts[0].onload, null)
  assert.equal(requests.length, 1)
  assert.equal(runs(), 1)
  assert.deepEqual(await loader.wasmCall('synthetic'), {})
})

test('runtime script deadline removes handlers and cannot accept a stale ready API', async (t) => {
  t.mock.timers.enable({apis: ['setTimeout', 'setInterval']})
  const {loader, scripts, window, requests} = await fixture(t, {script: 'pending'})
  window.ferryWasm = {ready: true, call: async () => '{}'}
  const rejected = assert.rejects(loader.startWasm(), /runtime shim timed out/)
  t.mock.timers.tick(loader.LOAD_LIMITS.scriptMs)
  await rejected
  assert.equal(loader.loadState().phase, 'failed')
  assert.equal(window.ferryWasm, undefined)
  assert.equal(scripts[0].removed, true)
  assert.equal(scripts[0].onload, null)
  assert.equal(requests.length, 0)
})

test('fetch deadline aborts a stalled header request', async (t) => {
  t.mock.timers.enable({apis: ['setTimeout', 'setInterval']})
  const {loader, requests, runs} = await fixture(t, {fetch: () => new Promise(() => {})})
  const rejected = assert.rejects(loader.startWasm(), /Downloading .* timed out/)
  await observed(() => requests.length === 1)
  t.mock.timers.tick(loader.LOAD_LIMITS.fetchMs)
  await rejected
  assert.equal(requests[0].init.signal.aborted, true)
  assert.equal(runs(), 0)
  assert.equal(loader.loadState().phase, 'failed')
})

test('fetch deadline cancels a stalled response body and never starts the engine', async (t) => {
  t.mock.timers.enable({apis: ['setTimeout', 'setInterval']})
  let cancelled = false
  const {loader, runs} = await fixture(t, {fetch: async () => new Response(new ReadableStream({
    start(controller) { controller.enqueue(new Uint8Array([1])) },
    cancel() { cancelled = true },
  }))})
  const rejected = assert.rejects(loader.startWasm(), /Downloading .* timed out/)
  await observed(() => loader.loadState().loadedBytes === 1)
  t.mock.timers.tick(loader.LOAD_LIMITS.fetchMs)
  await rejected
  await flush()
  assert.equal(cancelled, true)
  assert.equal(runs(), 0)
})

test('oversized declared content is rejected before reading the body', async (t) => {
  let reads = 0
  const {loader, runs} = await fixture(t, {fetch: async () => ({
    ok: true,
    headers: new Headers({'content-length': String(33 * 1024 * 1024)}),
    body: {getReader() { reads++; throw new Error('must not read an oversized body') }},
  })})
  await assert.rejects(loader.startWasm(), /exceeds the 32 MiB limit/)
  assert.equal(reads, 0)
  assert.equal(runs(), 0)
})

test('streamed content is bounded even when Content-Length lies', async (t) => {
  let cancelled = false
  let chunks = 0
  const {loader, runs} = await fixture(t, {fetch: async () => new Response(new ReadableStream({
    pull(controller) { chunks++; controller.enqueue(new Uint8Array(1024 * 1024)) },
    cancel() { cancelled = true },
  }), {headers: {'content-length': '1'}})})
  await assert.rejects(loader.startWasm(), /exceeds the 32 MiB limit/)
  assert.equal(cancelled, true)
  assert.ok(chunks <= 34)
  assert.equal(runs(), 0)
  assert.equal(loader.loadState().phase, 'failed')
})

test('compile deadline does not start a later-resolving instance', async (t) => {
  t.mock.timers.enable({apis: ['setTimeout', 'setInterval']})
  let finish
  const {loader, runs} = await fixture(t, {instantiate: () => new Promise((done) => { finish = done })})
  const rejected = assert.rejects(loader.startWasm(), /Compiling the signing engine timed out/)
  await observed(() => typeof finish === 'function')
  t.mock.timers.tick(loader.LOAD_LIMITS.compileMs)
  await rejected
  finish({instance: {exports: {}}, module: {}})
  await flush()
  assert.equal(runs(), 0)
  assert.equal(loader.loadState().phase, 'failed')
})

test('a partial ready object without a callable API times out', async (t) => {
  t.mock.timers.enable({apis: ['setTimeout', 'setInterval']})
  const {loader, window, runs} = await fixture(t, {run: (w) => {
    w.ferryWasm = {ready: true}
    return new Promise(() => {})
  }})
  const rejected = assert.rejects(loader.startWasm(), /did not start within 20 seconds/)
  await observed(() => runs() === 1)
  t.mock.timers.tick(loader.LOAD_LIMITS.readyMs)
  await rejected
  assert.equal(window.ferryWasm, undefined)
  assert.equal(loader.loadState().phase, 'failed')
})

test('runtime rejection fails readiness immediately and revokes a ready API', async (t) => {
  let stop
  const {loader, window} = await fixture(t, {run: (w) => {
    w.ferryWasm = {ready: true, call: async () => '{}'}
    return new Promise((_, reject) => { stop = reject })
  }})
  await loader.startWasm()
  stop(new Error('synthetic runtime crash'))
  await flush()
  assert.equal(loader.loadState().phase, 'failed')
  assert.equal(window.ferryWasm, undefined)
  await assert.rejects(loader.wasmCall('synthetic'), /synthetic runtime crash/)
})

test('a clean runtime exit cannot leave a ready-looking API usable', async (t) => {
  const {loader, window} = await fixture(t, {run: (w) => {
    w.ferryWasm = {ready: true, call: async () => '{}'}
    return Promise.resolve()
  }})
  await assert.rejects(loader.startWasm(), /signing engine exited/)
  assert.equal(window.ferryWasm, undefined)
  assert.equal(loader.loadState().phase, 'failed')
})

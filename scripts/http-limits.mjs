// Exercise the shipped browser HTTP implementation with synthetic streams.
// No network, wallet, or chain access is permitted.
// node scripts/http-limits.mjs [path/to/ferry.wasm]
import assert from 'node:assert/strict'
import {execFileSync} from 'node:child_process'
import {readFile} from 'node:fs/promises'
import {dirname, resolve} from 'node:path'
import {fileURLToPath} from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const wasm = process.argv[2] ?? resolve(root, 'ui/public/ferry.wasm')
const maxBody = 8 << 20
const chunkSize = 64 << 10
const storage = new Map()
globalThis.window = globalThis
globalThis.localStorage = {
  get length() { return storage.size },
  key: (i) => [...storage.keys()][i] ?? null,
  getItem: (key) => storage.get(key) ?? null,
  setItem: (key, value) => storage.set(key, String(value)),
  removeItem: (key) => storage.delete(key),
}
let responseFactory
let lastSignal
globalThis.fetch = async (url, options) => {
  assert.equal(url, 'https://fixture.invalid/blocks/tip/height')
  lastSignal = options.signal
  return responseFactory()
}
const goroot = execFileSync('go', ['env', 'GOROOT'], {encoding: 'utf8'}).trim()
const shim = await readFile(resolve(goroot, 'lib/wasm/wasm_exec.js'), 'utf8')
new Function(shim)()
const go = new globalThis.Go()
const {instance} = await WebAssembly.instantiate(await readFile(wasm), go.importObject)
void go.run(instance)
await new Promise((done) => setTimeout(done, 50))
assert.equal(globalThis.ferryWasm?.ready, true)
const config = async () => JSON.parse(await globalThis.ferryWasm.call('config', JSON.stringify({
  settings: {network: 'regtest', btcEsplora: 'https://fixture.invalid'},
})))

function streamFixture(length, contentLength) {
  let produced = 0
  let cancelled = 0
  const body = new ReadableStream({
    pull(controller) {
      if (produced === length) { controller.close(); return }
      const count = Math.min(chunkSize, length - produced)
      const chunk = new Uint8Array(count).fill(32)
      if (produced === 0) chunk.set(new TextEncoder().encode('100'))
      produced += count
      controller.enqueue(chunk)
    },
    cancel() { cancelled++ },
  })
  const response = new Response(body, {
    headers: contentLength === undefined ? {} : {'content-length': String(contentLength)},
  })
  response.arrayBuffer = () => { throw new Error('unbounded body buffering is forbidden') }
  return {response, produced: () => produced, cancelled: () => cancelled}
}

let checks = 0
async function check(name, work) {
  await work()
  checks++
  console.log(`PASS ${name}`)
}

await check('a complete streamed body at the byte limit is accepted', async () => {
  const fixture = streamFixture(maxBody)
  responseFactory = () => fixture.response
  const result = await config()
  assert.equal(result.tipHeight, 100)
  assert.equal(result.chainError, undefined)
  assert.equal(fixture.cancelled(), 0)
  assert.equal(fixture.response.body.locked, false)
})

for (const declared of [undefined, 1]) {
  await check(`oversized stream with Content-Length=${declared ?? 'absent'} is cancelled and rejected`, async () => {
    const fixture = streamFixture(maxBody + chunkSize * 10, declared)
    responseFactory = () => fixture.response
    const result = await config()
    assert.match(result.chainError, /byte limit/)
    assert.equal(result.tipHeight, undefined)
    assert.equal(fixture.cancelled(), 1)
    assert.equal(lastSignal.aborted, true)
    assert.equal(fixture.response.body.locked, false)
    // ReadableStream can prefetch one chunk while the previous read crosses
    // the JS/WASM boundary. It must not buffer the rest of this large body.
    assert.ok(fixture.produced() <= maxBody + chunkSize * 2)
  })
}

await check('declared oversize cancels the body before downloading it', async () => {
  const fixture = streamFixture(maxBody * 2, maxBody + 1)
  responseFactory = () => fixture.response
  const result = await config()
  assert.match(result.chainError, /byte limit/)
  assert.equal(fixture.cancelled(), 1)
  assert.equal(lastSignal.aborted, true)
  assert.ok(fixture.produced() <= chunkSize)
})

await check('a stream read failure aborts and releases its reader', async () => {
  const response = new Response(new ReadableStream({
    pull() { throw new Error('synthetic stream failure') },
  }))
  responseFactory = () => response
  const result = await config()
  assert.match(result.chainError, /synthetic stream failure/)
  assert.equal(lastSignal.aborted, true)
  assert.equal(response.body.locked, false)
})

await check('cancellation still bounds a runtime without AbortController', async () => {
  const controller = globalThis.AbortController
  const navigatorDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'navigator')
  // This probe isolates HTTP cancellation. Native Node locks require their
  // own AbortController to bound queue waits and are tested separately.
  Object.defineProperty(globalThis, 'navigator', {value: {}, configurable: true})
  globalThis.AbortController = undefined
  try {
    const fixture = streamFixture(maxBody + chunkSize * 10)
    responseFactory = () => fixture.response
    const result = await config()
    assert.match(result.chainError, /byte limit/)
    assert.equal(fixture.cancelled(), 1)
    assert.equal(fixture.response.body.locked, false)
  } finally {
    globalThis.AbortController = controller
    if (navigatorDescriptor) Object.defineProperty(globalThis, 'navigator', navigatorDescriptor)
    else delete globalThis.navigator
  }
})

await check('a response without a body does not throw at the bridge', async () => {
  responseFactory = () => new Response(null, {status: 204})
  const result = await config()
  assert.equal(result.error, undefined)
  assert.equal(result.tipHeight, undefined)
  assert.match(result.chainError, /parse|invalid|empty/i)
})

console.log(`${checks} HTTP body-bound checks passed.`)
process.exit(0)

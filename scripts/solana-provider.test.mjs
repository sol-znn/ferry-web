// Offline checks against the bundled browser SDK. All keys, providers and RPC
// replies are disposable fixtures; this does not qualify a live wallet or chain.
import assert from 'node:assert/strict'
import {createPublicKey, verify, webcrypto} from 'node:crypto'
import {createRequire} from 'node:module'
import {dirname, resolve} from 'node:path'
import {fileURLToPath, pathToFileURL} from 'node:url'
import vm from 'node:vm'
import test from 'node:test'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const ui = resolve(root, 'ui')
const require = createRequire(resolve(ui, 'package.json'))
const {build} = await import(pathToFileURL(require.resolve('vite')).href)
const entry = resolve(ui, 'solana-provider.fixture.ts')
const result = await build({
  root: ui,
  configFile: false,
  logLevel: 'error',
  // Library mode keeps process.env references unless they are defined explicitly.
  define: {
    __FERRY_ENV__: JSON.stringify('dev'),
    'process.env.NODE_ENV': JSON.stringify('production'),
  },
  plugins: [{
    name: 'solana-provider-fixture',
    resolveId(id) { if (id === entry) return '\0' + entry },
    load(id) {
      if (id === '\0' + entry) return [
        'export * from ' + JSON.stringify(resolve(ui, 'src/core/solana.ts')),
        'export {Connection, Keypair, Transaction, PublicKey} from "@solana/web3.js"',
      ].join('\n')
    },
  }],
  build: {
    write: false,
    minify: false,
    target: 'es2022',
    lib: {entry, name: 'FerrySolanaFixture', formats: ['iife']},
    rollupOptions: {output: {inlineDynamicImports: true}},
  },
})
const outputs = Array.isArray(result) ? result : [result]
const chunks = outputs.flatMap((output) => output.output).filter((o) => o.type === 'chunk')
assert.equal(chunks.length, 1, 'the browser fixture must be one self-contained IIFE')
const bundle = new vm.Script(chunks[0].code)
const endpoint = 'https://fixture.invalid/solana'
const blockhash = '11111111111111111111111111111111'
const alphabet = '123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz'
function base58(bytes) {
  let n = 0n
  for (const b of bytes) n = n * 256n + BigInt(b)
  let text = ''
  while (n) { text = alphabet[Number(n % 58n)] + text; n /= 58n }
  for (const b of bytes) { if (b) break; text = '1' + text }
  return text
}
function signatureValid(message, signature, publicKey) {
  const key = createPublicKey({
    key: Buffer.concat([Buffer.from('302a300506032b6570032100', 'hex'), Buffer.from(publicKey)]),
    format: 'der',
    type: 'spki',
  })
  return verify(null, Buffer.from(message), key, Buffer.from(signature))
}
function harness({fallbackCrypto = false} = {}) {
  const requests = [], confirmations = [], storage = new Map()
  let rpcError = null, malformed = false
  const sandbox = {
    Uint8Array, Uint16Array, Uint32Array, Int32Array, ArrayBuffer, DataView,
    TextEncoder, TextDecoder, URL, URLSearchParams, Headers, Request, Response,
    atob, btoa, setTimeout, clearTimeout, setInterval, clearInterval, queueMicrotask,
    crypto: fallbackCrypto ? {getRandomValues: webcrypto.getRandomValues.bind(webcrypto)} : webcrypto,
    console,
    localStorage: {
      getItem: (key) => storage.get(key) ?? null,
      setItem: (key, value) => storage.set(key, String(value)),
      removeItem: (key) => storage.delete(key),
    },
    WebSocket: class { constructor() { throw new Error('unexpected websocket in offline fixture') } },
    fetch: async (url, options) => {
      assert.equal(url, endpoint, 'no live RPC is allowed')
      const req = JSON.parse(options.body)
      assert.match(req.id, /^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$/i)
      assert.equal(req.jsonrpc, '2.0')
      requests.push(req)
      if (malformed) return new Response('{invalid')
      if (rpcError) return Response.json({jsonrpc: '2.0', id: req.id, error: rpcError})
      let value
      if (req.method === 'getBalance') value = {context: {slot: 10}, value: 2500000000}
      else if (req.method === 'getLatestBlockhash') value = {
        context: {slot: 10}, value: {blockhash, lastValidBlockHeight: 500},
      }
      else if (req.method === 'sendTransaction') {
        const wire = Buffer.from(req.params[0], 'base64')
        assert.equal(wire[0], 1)
        value = base58(wire.subarray(1, 65))
      } else throw new Error('unexpected RPC method: ' + req.method)
      return Response.json({jsonrpc: '2.0', id: req.id, result: value})
    },
  }
  sandbox.window = sandbox
  sandbox.self = sandbox
  vm.createContext(sandbox)
  assert.equal(vm.runInContext('typeof Buffer', sandbox), 'undefined')
  bundle.runInContext(sandbox, {timeout: 10000})
  const api = sandbox.FerrySolanaFixture
  api.Connection.prototype.confirmTransaction = async function(strategy, commitment) {
    confirmations.push({strategy: {...strategy}, commitment})
    return {context: {slot: 11}, value: {err: null}}
  }
  const pair = api.Keypair.fromSeed(new Uint8Array(32).fill(7))
  const address = pair.publicKey.toBase58()
  const instruction = {
    programId: blockhash,
    accounts: [{pubkey: address, isSigner: true, isWritable: true}],
    data: 'AQID',
  }
  return {
    api, sandbox, requests, confirmations, storage, pair, address, instruction,
    failRpc(value) { rpcError = value },
    malformRpc() { malformed = true },
    local() {
      storage.set('ferry.dev.solanaKey', JSON.stringify(Array.from(pair.secretKey)))
      assert.equal(api.useLocalKey(), address)
    },
    async inject(methods) {
      sandbox.phantom = {solana: {
        isPhantom: true,
        publicKey: pair.publicKey,
        async connect() { return {publicKey: pair.publicKey} },
        ...methods,
      }}
      assert.equal(await api.connectPhantom(), address)
    },
  }
}

test('the lock removes vulnerable chains and scopes the reviewed RPC override', () => {
  const lock = require('./package-lock.json')
  assert.equal(lock.packages['node_modules/jayson'].version, '5.0.0')
  for (const name of ['stream-json', 'braces', 'fast-glob', 'micromatch']) {
    assert.equal(Object.keys(lock.packages).some((p) => p.endsWith('/' + name)), false, name)
  }
  for (const [path, pkg] of Object.entries(lock.packages)) {
    if (/(?:^|\/)node_modules\/uuid$/.test(path)) {
      const [major, minor, patch] = pkg.version.split('.').map(Number)
      assert.ok(major > 11 || (major === 11 && (minor > 1 || (minor === 1 && patch >= 1))), path)
    }
  }
  assert.deepEqual(require('./package.json').overrides, {'@solana/web3.js': {jayson: '5.0.0'}})
})

test('the actual browser SDK reads a balance without a global Buffer', async () => {
  const h = harness()
  assert.equal(await h.api.balance(endpoint, h.address), 2500000000)
  assert.equal(h.requests.length, 1)
  assert.equal(h.requests[0].params[1].commitment, 'confirmed')
})

test('RPC IDs also work when only getRandomValues is available', async () => {
  const h = harness({fallbackCrypto: true})
  assert.equal(await h.api.balance(endpoint, h.address), 2500000000)
  assert.equal(h.requests.length, 1)
})

test('JSON-RPC errors are surfaced without issuing another request', async () => {
  const h = harness()
  h.failRpc({code: -32000, message: 'fixture RPC failure'})
  await assert.rejects(h.api.balance(endpoint, h.address), /fixture RPC failure/)
  assert.equal(h.requests.length, 1)
})

test('malformed JSON is rejected by the browser RPC client', async () => {
  const h = harness()
  h.malformRpc()
  await assert.rejects(h.api.balance(endpoint, h.address))
  assert.equal(h.requests.length, 1)
})

test('the local fixture key restores and produces a valid board proof', async () => {
  const h = harness()
  h.local()
  const message = 'ferry offline dependency fixture'
  const signature = Buffer.from(await h.api.signMessage(message), 'hex')
  assert.equal(signature.length, 64)
  assert.ok(signatureValid(new TextEncoder().encode(message), signature, h.pair.publicKey.toBytes()))
})

test('local signing preserves the exact legacy transaction wire format', async () => {
  const h = harness()
  h.local()
  const signature = await h.api.send(endpoint, h.instruction)
  const sent = h.requests.filter((r) => r.method === 'sendTransaction')
  assert.equal(sent.length, 1)
  const wire = Buffer.from(sent[0].params[0], 'base64')
  const expectedMessage = Buffer.from([
    1, 0, 1, 2, ...h.pair.publicKey.toBytes(), ...new Uint8Array(32),
    ...new Uint8Array(32), 1, 1, 1, 0, 3, 1, 2, 3,
  ])
  assert.deepEqual(wire.subarray(65), expectedMessage)
  assert.ok(signatureValid(expectedMessage, wire.subarray(1, 65), h.pair.publicKey.toBytes()))
  assert.equal(signature, base58(wire.subarray(1, 65)))
  assert.equal(sent[0].params[1].preflightCommitment, 'confirmed')
  assert.equal(h.confirmations.length, 1)
  assert.deepEqual(h.confirmations[0], {
    strategy: {signature, blockhash, lastValidBlockHeight: 500}, commitment: 'confirmed',
  })
})

test('an injected sign-only wallet is called once and submits through the chosen RPC', async () => {
  const h = harness()
  let calls = 0
  await h.inject({async signTransaction(tx) { calls++; tx.sign(h.pair); return tx }})
  await h.api.send(endpoint, h.instruction)
  assert.equal(calls, 1)
  assert.equal(h.api.wallet.sendsThroughItsOwnRpc, false)
  assert.equal(h.requests.filter((r) => r.method === 'sendTransaction').length, 1)
})

test('a sign-and-send-only provider retains its separate submission path', async () => {
  const h = harness()
  let calls = 0
  await h.inject({async signAndSendTransaction(tx) {
    calls++
    tx.sign(h.pair)
    return {signature: base58(tx.serialize().subarray(1, 65))}
  }})
  await h.api.send(endpoint, h.instruction)
  assert.equal(calls, 1)
  assert.equal(h.api.wallet.sendsThroughItsOwnRpc, true)
  assert.equal(h.requests.filter((r) => r.method === 'sendTransaction').length, 0)
  assert.equal(h.confirmations.length, 1)
})

test('a wrong required signer fails before an RPC or signing request', async () => {
  const h = harness()
  let calls = 0
  await h.inject({async signTransaction(tx) { calls++; return tx }})
  const ix = {...h.instruction, accounts: [{pubkey: blockhash, isSigner: true, isWritable: true}]}
  await assert.rejects(h.api.send(endpoint, ix), /this step has to be signed by/)
  assert.equal(calls, 0)
  assert.equal(h.requests.length, 0)
})

test('a declined signing request causes no broadcast or signing retry', async () => {
  const h = harness()
  let calls = 0
  await h.inject({async signTransaction() { calls++; throw new Error('fixture declined') }})
  await assert.rejects(h.api.send(endpoint, h.instruction), /fixture declined/)
  assert.equal(calls, 1)
  assert.equal(h.requests.filter((r) => r.method === 'sendTransaction').length, 0)
  assert.equal(h.confirmations.length, 0)
})

test('a returned transaction with a different message is not broadcast', async () => {
  const h = harness()
  await h.inject({async signTransaction(tx) {
    const other = h.api.Transaction.from(tx.serialize({requireAllSignatures: false, verifySignatures: false}))
    other.recentBlockhash = h.address
    other.sign(h.pair)
    return other
  }})
  await assert.rejects(h.api.send(endpoint, h.instruction), /different transaction/)
  assert.equal(h.requests.filter((r) => r.method === 'sendTransaction').length, 0)
})

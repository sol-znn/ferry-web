// A minimal Esplora REST API over a Bitcoin Core regtest node.
//
// ferry-web speaks Esplora and nothing else — see "Why Esplora only" in the
// README — which is right for a page and leaves local development with no chain
// to point at, since a regtest node offers Core RPC and no Esplora.
//
// This is that missing adapter and only that: the six endpoints
// wasm/chain/esplora.go actually calls, backed by the regtest node's RPC, with
// the CORS headers a browser demands. A development tool — no auth, no
// pagination, no caching policy, its index in memory. Do not put it in front of
// anything real.
//
//   GET  /blocks/tip/height        -> "155"
//   GET  /block-height/<h>         -> the block hash at that height
//   GET  /address/{addr}/utxo      -> [{txid, vout, value, status}]
//   GET  /tx/{txid}/hex            -> "0200000001..."
//   GET  /tx/{txid}/status         -> {confirmed, block_height, block_hash, ...}
//   GET  /tx/{txid}/outspend/{n}   -> {spent, txid, vin, status}
//   GET  /fee-estimates            -> {"1": 2, ...}   (sat/vB)
//   POST /tx  (body: raw hex)      -> txid
//
// Two of those — address UTXOs and outspends — are exactly what Core has no
// index for. Rather than require an address index or watch-only descriptors,
// this walks the chain once, keeps its own index in memory, then follows the
// tip. On regtest that is a few hundred tiny blocks. The mempool is a separate
// layer on top, which is what makes unconfirmed funding visible the way a real
// Esplora shows it.
//
// Usage:
//   node scripts/regtest-esplora.mjs
//   node scripts/regtest-esplora.mjs --port 3002 --rpc http://127.0.0.1:18443
//
// Then set the Esplora base URL in Ferry's Node settings to
// http://127.0.0.1:3002 and the network to regtest.

import http from 'node:http'

// --- configuration ---------------------------------------------------------

function arg(name, fallback) {
  const i = process.argv.indexOf(`--${name}`)
  return i !== -1 && process.argv[i + 1] ? process.argv[i + 1] : fallback
}

const PORT = Number(arg('port', process.env.ESPLORA_PORT ?? 3002))
const RPC_URL = arg('rpc', process.env.BITCOIN_RPC ?? 'http://127.0.0.1:18443')
const RPC_USER = arg('rpcuser', process.env.BITCOIN_RPC_USER ?? 'ferry')
const RPC_PASS = arg('rpcpass', process.env.BITCOIN_RPC_PASS ?? 'ferryregtestpass')

// Regtest has no fee market, so estimatesmartfee returns nothing and Esplora's
// map would be empty. These are the sat/vB values handed out instead: above the
// 1 sat/vB relay floor, low enough that a test never burns a meaningful amount.
const FEE_ESTIMATES = Object.fromEntries(
  [1, 2, 3, 4, 5, 6, 10, 20, 144, 504, 1008].map((t) => [String(t), t <= 6 ? 2 : 1]),
)

// --- Bitcoin Core RPC ------------------------------------------------------

const auth = 'Basic ' + Buffer.from(`${RPC_USER}:${RPC_PASS}`).toString('base64')
let rpcId = 0

async function rpc(method, params = []) {
  const res = await fetch(RPC_URL, {
    method: 'POST',
    headers: {Authorization: auth, 'Content-Type': 'application/json'},
    body: JSON.stringify({jsonrpc: '1.0', id: ++rpcId, method, params}),
  })
  // Core answers 500 with a JSON-RPC error body for ordinary failures (an
  // unknown txid, a rejected transaction), so the body is parsed before the
  // status is judged — the message inside it is the useful part.
  const text = await res.text()
  let body
  try {
    body = JSON.parse(text)
  } catch {
    throw new Error(`${method}: HTTP ${res.status}: ${text.slice(0, 300)}`)
  }
  if (body.error) {
    const err = new Error(body.error.message ?? String(body.error.code))
    err.code = body.error.code
    throw err
  }
  return body.result
}

// --- the index -------------------------------------------------------------
//
// Confirmed state is accumulated block by block and never recomputed. Mempool
// state is a separate layer, rebuilt from the node's view on each sync, because
// entries leave it for reasons (confirmation, eviction, replacement) that no
// amount of watching the tip would tell us about.

const key = (txid, vout) => `${txid}:${vout}`
const sats = (btc) => Math.round(btc * 1e8)

/** Confirmed: outpoint -> {value, addr, status}. */
let outs = new Map()
/** Confirmed: address -> Set of outpoints. */
let byAddr = new Map()
/** Confirmed: outpoint -> {txid, vin, status} of the transaction spending it. */
let spent = new Map()
/** height -> block hash, kept so a regtest reorg or wipe is noticed. */
let hashAt = new Map()
let scanned = 0

/** Mempool layer, rebuilt each sync from the transactions the node holds. */
let memTx = new Map() // txid -> decoded tx
let memOuts = new Map()
let memByAddr = new Map()
let memSpent = new Map()

function addOut(outMap, addrMap, txid, vout, value, addr, status) {
  outMap.set(key(txid, vout), {value, addr, status})
  if (!addrMap.has(addr)) addrMap.set(addr, new Set())
  addrMap.get(addr).add(key(txid, vout))
}

/** Index one decoded transaction into the given layer. */
function indexTx(tx, status, outMap, addrMap, spentMap) {
  tx.vin.forEach((vin, i) => {
    if (!vin.txid) return // coinbase
    spentMap.set(key(vin.txid, vin.vout), {txid: tx.txid, vin: i, status})
  })
  for (const vout of tx.vout) {
    const addr = vout.scriptPubKey?.address
    if (!addr) continue // bare multisig, OP_RETURN, anything unaddressable
    addOut(outMap, addrMap, tx.txid, vout.n, sats(vout.value), addr, status)
  }
}

function resetConfirmed() {
  outs = new Map()
  byAddr = new Map()
  spent = new Map()
  hashAt = new Map()
  scanned = 0
}

async function syncBlocks() {
  const tip = await rpc('getblockcount')

  // A regtest datadir gets wiped and rebuilt routinely, and blocks get
  // invalidated by hand. Either leaves this index describing a chain that no
  // longer exists, so the hash at the last scanned height is rechecked and the
  // whole index dropped if it moved. Rescanning a regtest chain costs less than
  // reasoning about a partial rollback.
  if (scanned > 0) {
    const stored = hashAt.get(scanned)
    let current = null
    try {
      current = await rpc('getblockhash', [scanned])
    } catch {
      current = null // chain is shorter than it was
    }
    if (!stored || stored !== current) {
      console.log(`[esplora] chain changed under us at height ${scanned}; reindexing`)
      resetConfirmed()
    }
  }

  if (scanned >= tip) return tip
  const from = scanned + 1
  for (let h = from; h <= tip; h++) {
    const hash = await rpc('getblockhash', [h])
    const block = await rpc('getblock', [hash, 2])
    const status = {
      confirmed: true,
      block_height: h,
      block_hash: hash,
      block_time: block.time,
    }
    for (const tx of block.tx) indexTx(tx, status, outs, byAddr, spent)
    hashAt.set(h, hash)
    scanned = h
  }
  if (tip >= from) {
    console.log(`[esplora] indexed blocks ${from}..${tip}`)
  }
  return tip
}

const UNCONFIRMED = {confirmed: false}

async function syncMempool() {
  const ids = await rpc('getrawmempool')
  const wanted = new Set(ids)
  for (const txid of memTx.keys()) if (!wanted.has(txid)) memTx.delete(txid)
  for (const txid of ids) {
    if (memTx.has(txid)) continue
    try {
      memTx.set(txid, await rpc('getrawtransaction', [txid, true]))
    } catch {
      // Raced with a block or an eviction; it will be picked up as confirmed
      // on the next sync, or it is gone. Either way there is nothing to index.
    }
  }
  memOuts = new Map()
  memByAddr = new Map()
  memSpent = new Map()
  for (const tx of memTx.values()) {
    indexTx(tx, UNCONFIRMED, memOuts, memByAddr, memSpent)
  }
}

let syncing = null
async function sync() {
  // Requests arrive in bursts (the UI refreshes several swaps at once) and the
  // scan is not reentrant, so concurrent callers share one in-flight pass.
  if (syncing) return syncing
  syncing = (async () => {
    try {
      await syncBlocks()
      await syncMempool()
    } finally {
      syncing = null
    }
  })()
  return syncing
}

// --- queries ---------------------------------------------------------------

function addressUTXOs(addr) {
  const result = []
  const seen = new Set()
  const collect = (addrMap, outMap) => {
    for (const k of addrMap.get(addr) ?? []) {
      if (seen.has(k)) continue
      seen.add(k)
      if (spent.has(k) || memSpent.has(k)) continue // spent, confirmed or not
      const out = outMap.get(k)
      if (!out) continue
      const [txid, vout] = [k.slice(0, 64), Number(k.slice(65))]
      result.push({txid, vout, value: out.value, status: out.status})
    }
  }
  collect(byAddr, outs)
  collect(memByAddr, memOuts)
  return result
}

function outspend(txid, vout) {
  const hit = spent.get(key(txid, vout)) ?? memSpent.get(key(txid, vout))
  if (!hit) return {spent: false}
  return {spent: true, txid: hit.txid, vin: hit.vin, status: hit.status}
}

// --- HTTP ------------------------------------------------------------------

function cors(res) {
  res.setHeader('Access-Control-Allow-Origin', '*')
  res.setHeader('Access-Control-Allow-Methods', 'GET, POST, OPTIONS')
  res.setHeader('Access-Control-Allow-Headers', 'Content-Type')
  res.setHeader('Access-Control-Max-Age', '600')
  // Chrome gates requests from a page to a more-private network behind this.
  // Both ends are loopback here, but the header costs nothing and removes a
  // browser-version-dependent failure that looks like an unexplained CORS error.
  res.setHeader('Access-Control-Allow-Private-Network', 'true')
}

function send(res, status, body, type = 'text/plain') {
  cors(res)
  res.setHeader('Content-Type', type)
  res.statusCode = status
  res.end(body)
}

const json = (res, status, value) => send(res, status, JSON.stringify(value), 'application/json')

function readBody(req) {
  return new Promise((resolve, reject) => {
    let data = ''
    req.on('data', (c) => (data += c))
    req.on('end', () => resolve(data))
    req.on('error', reject)
  })
}

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, `http://${req.headers.host}`)
  const path = url.pathname.replace(/\/+$/, '') || '/'

  if (req.method === 'OPTIONS') {
    cors(res)
    res.statusCode = 204
    return res.end()
  }

  try {
    if (req.method === 'POST' && path === '/tx') {
      const hex = (await readBody(req)).trim()
      let txid
      try {
        txid = await rpc('sendrawtransaction', [hex])
      } catch (e) {
        // Esplora reports a rejection as 400 with the reason as the body, and
        // ferry-web shows that body verbatim. Core's message is the reason.
        return send(res, 400, `sendrawtransaction: ${e.message}`)
      }
      await syncMempool()
      console.log(`[esplora] broadcast ${txid}`)
      return send(res, 200, txid)
    }

    if (req.method !== 'GET') return send(res, 405, 'method not allowed')

    let m

    if (path === '/blocks/tip/height') {
      const tip = await syncBlocks()
      return send(res, 200, String(tip))
    }

    if (path === '/fee-estimates') {
      return json(res, 200, FEE_ESTIMATES)
    }

    // The block hash at a height. Two parties compare this to prove they are on
    // the same chain rather than merely on two chains both called "regtest" --
    // which, on regtest, is the normal situation and the one worth catching.
    if ((m = path.match(/^\/block-height\/(\d+)$/))) {
      try {
        return send(res, 200, await rpc('getblockhash', [Number(m[1])]))
      } catch (e) {
        return send(res, 404, e.message)
      }
    }

    if ((m = path.match(/^\/address\/([^/]+)\/utxo$/))) {
      await sync()
      return json(res, 200, addressUTXOs(m[1]))
    }

    if ((m = path.match(/^\/tx\/([0-9a-fA-F]{64})\/hex$/))) {
      try {
        return send(res, 200, await rpc('getrawtransaction', [m[1]]))
      } catch (e) {
        return send(res, 404, e.message)
      }
    }

    // Whether a transaction is in a block, and which. ferry's confirmation
    // counter is (tip - block_height + 1), so without this the funding on a
    // card sits at 0/6 however many blocks are mined -- which is what a local
    // run looked like until this was added. Core answers it directly; no index
    // of ours is involved.
    if ((m = path.match(/^\/tx\/([0-9a-fA-F]{64})\/status$/))) {
      try {
        const tx = await rpc('getrawtransaction', [m[1], true])
        if (!tx.blockhash) return json(res, 200, {confirmed: false})
        const block = await rpc('getblockheader', [tx.blockhash])
        return json(res, 200, {
          confirmed: true,
          block_height: block.height,
          block_hash: tx.blockhash,
          block_time: block.time,
        })
      } catch (e) {
        return send(res, 404, e.message)
      }
    }

    if ((m = path.match(/^\/tx\/([0-9a-fA-F]{64})\/outspend\/(\d+)$/))) {
      await sync()
      return json(res, 200, outspend(m[1], Number(m[2])))
    }

    // Not part of the six, but the two things worth being able to ask a shim.
    if (path === '/' || path === '/_shim/status') {
      await sync()
      return json(res, 200, {
        shim: 'regtest-esplora',
        rpc: RPC_URL,
        scannedHeight: scanned,
        indexedOutputs: outs.size,
        indexedSpends: spent.size,
        mempoolTx: memTx.size,
      })
    }

    return send(res, 404, `no such endpoint: ${path}`)
  } catch (e) {
    console.error(`[esplora] ${req.method} ${path}: ${e.message}`)
    return send(res, 502, e.message)
  }
})

const tip = await rpc('getblockcount').catch((e) => {
  console.error(`[esplora] cannot reach the regtest node at ${RPC_URL}: ${e.message}`)
  process.exit(1)
})
const info = await rpc('getblockchaininfo')
if (info.chain !== 'regtest') {
  // No development tool should be able to wander onto a real node.
  console.error(`[esplora] refusing to serve chain "${info.chain}" — this shim is regtest only`)
  process.exit(1)
}

await sync()
server.listen(PORT, '127.0.0.1', () => {
  console.log(`[esplora] regtest Esplora shim on http://127.0.0.1:${PORT}`)
  console.log(`[esplora] backing node ${RPC_URL} (chain=${info.chain} height=${tip})`)
  console.log(`[esplora] set this as the Esplora base URL in Ferry's Node settings`)
})

// A CLI wallet the development build can reach.
//
//   node --env-file=../zenon-faucet/.env scripts/cli-wallet.mjs
//
// Development only, and the one file in this tree that reaches outside it: the
// Zenon signing client is imported from ../zenon-faucet rather than copied, the
// way wasm/sol/ was copied from solzen. That is a deliberate, reversible choice
// and not the shipped app's business — nothing here is bundled, imported by the
// page, or reachable from a browser that has not been told this URL. Vendoring
// the six files it uses would make the tree airtight again if that matters more
// than the duplication.
//
// Two signers behind one loopback HTTP surface:
//
//   Zenon    a real ed25519 key from go-zenon's public devnet mnemonic. It
//            signs the account block the page builds -- the same bytes Syrius
//            would be handed -- mines the PoW the node asks for, and publishes.
//   Bitcoin  bitcoin-cli against the regtest node, for funding a contract
//            address and for mining the confirmations that make it visible.
//
// Nothing here is a wallet extension and nothing here is a stub: the blocks are
// signed with a key that owns the coins, and a swap settled through this really
// settled.
import {createServer} from 'node:http'
import {execFileSync} from 'node:child_process'

const FAUCET = process.env.ZNN_CLIENT ?? new URL('../../zenon-faucet', import.meta.url).pathname.replace(/^\//, '')
const {ZenonClient} = await import(`file:///${FAUCET}/src/znn/client.ts`)
const {BlockPublisher} = await import(`file:///${FAUCET}/src/znn/block.ts`)
const {Account} = await import(`file:///${FAUCET}/src/znn/wallet.ts`)
const prim = await import(`file:///${FAUCET}/src/znn/primitives.ts`)

const RPC = process.env.ZNN_RPC ?? 'http://127.0.0.1:35997'
const CHAIN_ID = Number(process.env.ZNN_CHAIN_ID ?? 69)
const PORT = Number(process.env.PORT ?? 8787)

const BITCOIN_CLI = 'N:/Bitcoin/daemon/bitcoin-cli.exe'
const BTC_ARGS = [
  '-datadir=C:\\dev\\zenon\\atomicswap\\.regtest',
  '-conf=C:\\dev\\zenon\\atomicswap\\.regtest\\bitcoin.conf',
  '-rpcwallet=test',
]
const btc = (...args) =>
  execFileSync(BITCOIN_CLI, [...BTC_ARGS, ...args], {encoding: 'utf8'}).trim()

const mnemonic = process.env.FAUCET_MNEMONIC
if (!mnemonic) throw new Error('FAUCET_MNEMONIC is not set — pass --env-file')

const client = new ZenonClient(RPC)
// Two accounts so a swap can have two sides. Index 3 is the one the faucet
// funded; index 0 is go-zenon's own genesis spender.
const accounts = new Map()
for (const index of [0, 3]) {
  const a = await Account.fromMnemonic(mnemonic, index)
  accounts.set(index, {account: a, publisher: new BlockPublisher(client, a, CHAIN_ID)})
}
const pick = (index) => {
  const got = accounts.get(Number(index ?? 3))
  if (!got) throw new Error(`no account at index ${index}`)
  return got
}

const log = (...a) => console.log(new Date().toISOString().slice(11, 19), ...a)

const routes = {
  'GET /health': async () => ({
    ok: true,
    znn: (await client.frontierMomentum()).height,
    btc: JSON.parse(btc('getblockchaininfo')).blocks,
  }),

  // What a wallet must be able to say about itself: which chain it signs for and
  // which node it publishes through. Ferry refuses to build a block without
  // both, and it is right to — a block signed for chain 1 and published to chain
  // 69 is a valid signature over the wrong thing.
  'GET /znn/info': async () => ({chainId: CHAIN_ID, rpcUrl: RPC}),

  'GET /znn/accounts': async () => {
    const out = []
    for (const [index, {account}] of accounts) {
      const info = await client.accountInfo(account.address)
      out.push({
        index,
        address: account.address,
        height: info.accountHeight,
        balances: Object.fromEntries(
          Object.entries(info.balanceInfoMap ?? {}).map(([z, b]) => [b.token?.symbol ?? z, b.balance]),
        ),
      })
    }
    return out
  },

  // The page hands over the block it built for Syrius. Only the four fields
  // that carry intent are taken from it — recipient, amount, token, call data —
  // because everything else (height, previousHash, momentum, plasma, nonce) is
  // this wallet's business and is rebuilt against the chain as it is right now.
  // A wallet that signed a stranger's height would publish a block the node
  // rejects, which is exactly what Syrius does not do either.
  'POST /znn/send': async (body) => {
    const {block, index} = body
    if (!block?.toAddress) throw new Error('no toAddress in the block')
    const {publisher, account} = pick(index)
    const spec = {
      toAddress: block.toAddress,
      amount: BigInt(block.amount || '0'),
      tokenStandard: block.tokenStandard || prim.ZNN_ZTS,
      data: block.data ? Buffer.from(block.data, 'base64') : undefined,
    }
    log('znn send', account.address, '→', spec.toAddress, spec.amount.toString(), spec.tokenStandard)
    const hash = await publisher.send(spec, {onMining: () => {}})
    log('  published', hash)
    return {hash, block: {hash}, from: account.address}
  },

  'GET /znn/htlc': async (_body, url) => {
    const id = url.searchParams.get('id')
    return (await client.htlcById(id)) ?? {missing: true, id}
  },

  'POST /btc/fund': async ({address, sats}) => {
    const amount = (Number(sats) / 1e8).toFixed(8)
    const txid = btc('sendtoaddress', address, amount)
    log('btc fund', address, amount, txid)
    return {txid, amount}
  },

  'POST /btc/mine': async ({blocks = 1, address}) => {
    const to = address || btc('getnewaddress')
    const hashes = JSON.parse(btc('generatetoaddress', String(blocks), to))
    return {mined: hashes.length, height: JSON.parse(btc('getblockchaininfo')).blocks}
  },

  'POST /btc/send-raw': async ({hex}) => ({txid: btc('sendrawtransaction', hex)}),

  'GET /btc/info': async () => {
    const chain = JSON.parse(btc('getblockchaininfo'))
    return {blocks: chain.blocks, balance: Number(btc('getbalance'))}
  },
}

createServer((req, res) => {
  const url = new URL(req.url, 'http://127.0.0.1')
  const key = `${req.method} ${url.pathname}`
  res.setHeader('access-control-allow-origin', '*')
  res.setHeader('access-control-allow-headers', 'content-type')
  res.setHeader('access-control-allow-methods', 'GET,POST,OPTIONS')
  if (req.method === 'OPTIONS') return res.writeHead(204).end()

  const chunks = []
  req.on('data', (c) => chunks.push(c))
  req.on('end', async () => {
    const handler = routes[key]
    if (!handler) {
      res.writeHead(404, {'content-type': 'application/json'})
      return res.end(JSON.stringify({error: `no route ${key}`, routes: Object.keys(routes)}))
    }
    try {
      const body = chunks.length ? JSON.parse(Buffer.concat(chunks).toString()) : {}
      const out = await handler(body, url)
      res.writeHead(200, {'content-type': 'application/json'})
      res.end(JSON.stringify(out, (_k, v) => (typeof v === 'bigint' ? v.toString() : v)))
    } catch (e) {
      log('ERROR', key, e.message)
      res.writeHead(500, {'content-type': 'application/json'})
      res.end(JSON.stringify({error: String(e.message ?? e)}))
    }
  })
}).listen(PORT, '127.0.0.1', () => {
  log(`cli wallet on http://127.0.0.1:${PORT}`)
  for (const [i, {account}] of accounts) log(`  znn[${i}] ${account.address}`)
})

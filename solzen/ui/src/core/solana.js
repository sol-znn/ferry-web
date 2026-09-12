// The Solana wallet, and the one place a transaction is assembled.
//
// The engine hands over an instruction -- program id, accounts, data -- and
// nothing else. Everything below turns that into a transaction and gets it
// signed. Splitting it here is what keeps the page's wallet plumbing away from
// the rules that decide whether a swap is safe: a bug in this file can lose a
// fee or fail to send, and cannot change what the instruction says.

import {
  Connection,
  Keypair,
  PublicKey,
  Transaction,
  TransactionInstruction,
  LAMPORTS_PER_SOL,
} from '@solana/web3.js'
import { reactive } from 'vue'

const LOCAL_KEY = 'solzen.localSolanaKey'
// Remembering that an extension was connected is what lets a reload reconnect
// without a popup. It holds no key and no address -- only that the user once
// pressed Connect in this browser.
const TRUSTED_KEY = 'solzen.walletTrusted'

export const PHANTOM_INSTALL_URL = 'https://phantom.app/download'

export const wallet = reactive({
  // 'none' | 'injected' | 'local'
  kind: 'none',
  address: '',
  name: '',
  error: '',
  connecting: false,
  // True when the extension can only sign-and-send, which means it submits
  // through whatever cluster it is pointed at rather than through the node
  // named in Nodes. Surfaced because against a local validator that is the
  // difference between a swap step landing and vanishing.
  sendsThroughItsOwnRpc: false,
})

// What this browser has to offer, kept reactive because extensions inject on
// their own schedule and a button rendered before Phantom arrives would
// otherwise never appear.
export const detected = reactive({ phantom: false, other: '' })

function usable(p) {
  return p && typeof p.connect === 'function' ? p : null
}

// Phantom exposes itself twice: on window.phantom.solana, and on window.solana
// when it is the only wallet installed. Prefer the namespaced one -- window.solana
// is contested ground, and several wallets claim it.
export function phantom() {
  const ns = usable(globalThis.phantom?.solana)
  if (ns?.isPhantom) return ns
  const bare = usable(globalThis.solana)
  if (bare?.isPhantom) return bare
  return ns ?? null
}

export function hasPhantom() {
  return phantom() !== null
}

function nameOf(p) {
  if (p?.isPhantom) return 'Phantom'
  if (p?.isSolflare) return 'Solflare'
  if (p?.isBackpack) return 'Backpack'
  return 'wallet extension'
}

// Every injected provider this page can drive, Phantom first. Detecting by
// shape rather than by name means a wallet that arrived after this was written
// still works.
function candidates() {
  const seen = new Set()
  const out = []
  const add = (p) => {
    const q = usable(p)
    if (!q || seen.has(q)) return
    seen.add(q)
    out.push(q)
  }
  add(phantom())
  add(globalThis.solflare)
  add(globalThis.backpack?.solana ?? globalThis.backpack)
  add(globalThis.solana)
  return out
}

// Extensions are not there when the page's first frame renders. Phantom
// announces itself with an event; a few short retries cover the ones that do
// not. Both stop as soon as something is found.
export function watchForWallets() {
  const scan = () => {
    detected.phantom = hasPhantom()
    const other = candidates().find((p) => !p.isPhantom)
    detected.other = other ? nameOf(other) : ''
  }
  scan()
  globalThis.addEventListener?.('phantom#initialized', scan, { once: true })
  let tries = 0
  const timer = setInterval(() => {
    scan()
    if (detected.phantom || ++tries > 10) clearInterval(timer)
  }, 300)
}

let active = null
const watched = new WeakSet()

// A wallet is driven from the extension, not from this page: the user can
// switch accounts or lock it mid-swap. Either way the page must stop believing
// it can sign for the address it is showing.
function watchProvider(p) {
  if (!p || watched.has(p) || typeof p.on !== 'function') return
  watched.add(p)
  p.on('accountChanged', (key) => {
    if (!key) return clear()
    wallet.address = key.toString()
    wallet.error = ''
  })
  p.on('disconnect', () => {
    if (wallet.kind === 'injected') clear()
  })
}

function remember(yes) {
  try {
    if (yes) localStorage.setItem(TRUSTED_KEY, '1')
    else localStorage.removeItem(TRUSTED_KEY)
  } catch {
    /* storage may be unavailable; the connection still works for this session */
  }
}

function remembered() {
  try {
    return localStorage.getItem(TRUSTED_KEY) === '1'
  } catch {
    return false
  }
}

async function connectProvider(p, opts = {}) {
  if (!p) throw new Error('no Solana wallet extension was found in this browser')
  wallet.connecting = true
  try {
    const res = await p.connect(opts.onlyIfTrusted ? { onlyIfTrusted: true } : undefined)
    const key = res?.publicKey ?? p.publicKey
    if (!key) throw new Error('the wallet connected but did not return an address')
    active = p
    watchProvider(p)
    wallet.kind = 'injected'
    wallet.address = key.toString()
    wallet.name = nameOf(p)
    wallet.error = ''
    wallet.sendsThroughItsOwnRpc = typeof p.signTransaction !== 'function'
    remember(true)
    return wallet.address
  } finally {
    wallet.connecting = false
  }
}

export async function connectPhantom() {
  const p = phantom()
  if (!p) throw new Error('Phantom was not found in this browser; install it from ' + PHANTOM_INSTALL_URL)
  return connectProvider(p)
}

export async function connectInjected() {
  return connectProvider(candidates()[0])
}

// Reconnect on load if the user has connected here before. Phantom remembers
// which origins it trusts, and onlyIfTrusted asks it to reconnect without
// showing a popup -- it throws when the origin is not trusted, which is an
// ordinary outcome and not something to put on screen.
export async function reconnectIfTrusted() {
  if (!remembered()) return false
  const p = phantom() ?? candidates()[0]
  if (!p) return false
  try {
    await connectProvider(p, { onlyIfTrusted: true })
    return true
  } catch {
    remember(false)
    return false
  }
}

// A keypair kept in this browser, for driving a local test chain without
// installing an extension.
//
// It is not how the swap is meant to be used and the UI says so. It exists
// because the whole point of a local validator is that it can be reset, and
// asking someone to point a real wallet at a chain that is wiped every morning
// is a bad trade.
export function useLocalKey() {
  let secret = null
  try {
    const stored = localStorage.getItem(LOCAL_KEY)
    if (stored) secret = Uint8Array.from(JSON.parse(stored))
  } catch {
    /* storage may be unavailable; a fresh key is the right fallback */
  }
  const kp = secret ? Keypair.fromSecretKey(secret) : Keypair.generate()
  try {
    localStorage.setItem(LOCAL_KEY, JSON.stringify(Array.from(kp.secretKey)))
  } catch {
    /* ignore: the key still works for this session */
  }
  wallet.kind = 'local'
  wallet.address = kp.publicKey.toBase58()
  wallet.name = 'a key in this browser'
  wallet.error = ''
  wallet.sendsThroughItsOwnRpc = false
  localKeypair = kp
  return wallet.address
}

// clear drops what this page believes about the wallet. The extension is not
// told, because this also runs when the extension is the one that hung up.
function clear() {
  wallet.kind = 'none'
  wallet.address = ''
  wallet.name = ''
  wallet.sendsThroughItsOwnRpc = false
  active = null
  localKeypair = null
}

export function disconnect() {
  const p = active
  clear()
  remember(false)
  // Ask the extension to forget the session too, so the next Connect is a
  // deliberate act rather than a silent reconnect.
  try {
    p?.disconnect?.()
  } catch {
    /* the page's own state is what matters here */
  }
}

let localKeypair = null

export function connection(url) {
  return new Connection(url, 'confirmed')
}

// send turns an engine instruction into a confirmed transaction.
//
// The wallet is asked to sign whatever the engine produced, unmodified. The
// page adds no instruction of its own -- not a memo, not a compute-budget
// bump -- so what the user approves in their wallet is exactly the swap step
// they pressed.
export async function send(url, ix) {
  if (!wallet.address) throw new Error('connect a Solana wallet first')
  const conn = connection(url)
  const instruction = new TransactionInstruction({
    programId: new PublicKey(ix.programId),
    keys: ix.accounts.map((a) => ({
      pubkey: new PublicKey(a.pubkey),
      isSigner: !!a.isSigner,
      isWritable: !!a.isWritable,
    })),
    data: base64ToBytes(ix.data),
  })

  // The engine decides who has to sign; only create has a signer at all, and it
  // is the escrow's initiator, fixed when the swap was made. If the connected
  // account is somebody else -- an account switched in the extension, most
  // likely -- the wallet cannot produce that signature, and the failure it
  // produces instead names a missing signature rather than the reason for it.
  const mustSign = ix.accounts.filter((a) => a.isSigner).map((a) => a.pubkey)
  const wrong = mustSign.find((k) => k !== wallet.address)
  if (wrong) {
    throw new Error(
      `this step has to be signed by ${wrong}, and the connected wallet is ${wallet.address}. ` +
        'Switch to that account in your wallet, or connect it here',
    )
  }

  const { blockhash, lastValidBlockHeight } = await conn.getLatestBlockhash('confirmed')
  const tx = new Transaction({ feePayer: new PublicKey(wallet.address), blockhash, lastValidBlockHeight })
  tx.add(instruction)

  let signature
  if (wallet.kind === 'injected') {
    const p = active ?? phantom()
    if (!p) throw new Error('the wallet is no longer connected; connect it again')
    if (typeof p.signTransaction === 'function') {
      // Sign in the extension, submit from here. signAndSendTransaction would
      // submit through whichever cluster the extension is pointed at, and this
      // page is pointed at the node named in Nodes -- against a local validator
      // those are never the same chain, and the blockhash above is this one's.
      const signed = await p.signTransaction(tx)
      // What comes back is only conventionally the transaction that went in.
      // The wallet is a separate program with its own code, and this page is
      // about to broadcast whatever it hands over -- so the message is
      // compared before it is sent. A signature over a message we did not
      // build is not this swap's step, whatever it is.
      if (!sameBytes(tx.serializeMessage(), signed.serializeMessage())) {
        throw new Error(
          'the wallet returned a different transaction than the one it was asked to sign; ' +
            'nothing has been broadcast. Do not retry until you know why',
        )
      }
      signature = await conn.sendRawTransaction(signed.serialize(), { preflightCommitment: 'confirmed' })
    } else {
      const res = await p.signAndSendTransaction(tx)
      signature = res?.signature ?? res
    }
  } else if (wallet.kind === 'local') {
    if (!localKeypair) throw new Error('the local key is gone; reconnect it')
    tx.sign(localKeypair)
    signature = await conn.sendRawTransaction(tx.serialize(), { preflightCommitment: 'confirmed' })
  } else {
    throw new Error('connect a Solana wallet first')
  }

  await conn.confirmTransaction({ signature, blockhash, lastValidBlockHeight }, 'confirmed')
  return signature
}

// Airdrops only work on a test chain, which is the only place this button is
// offered.
export async function airdrop(url, sol = 2) {
  const conn = connection(url)
  const sig = await conn.requestAirdrop(new PublicKey(wallet.address), sol * LAMPORTS_PER_SOL)
  const bh = await conn.getLatestBlockhash('confirmed')
  await conn.confirmTransaction({ signature: sig, ...bh }, 'confirmed')
  return sig
}

export async function balance(url, address) {
  const conn = connection(url)
  return conn.getBalance(new PublicKey(address || wallet.address), 'confirmed')
}

// Byte comparison that does not assume what serializeMessage returns. web3.js
// hands back a Buffer today, whose .equals would do; a Uint8Array has no such
// method, and a security check is a poor place to depend on which one arrives.
function sameBytes(a, b) {
  if (a.length !== b.length) return false
  let diff = 0
  for (let i = 0; i < a.length; i++) diff |= a[i] ^ b[i]
  return diff === 0
}

function base64ToBytes(b64) {
  const bin = atob(b64)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

export const lamportsToSol = (n) => (n / LAMPORTS_PER_SOL).toLocaleString('en-US', { maximumFractionDigits: 9 })

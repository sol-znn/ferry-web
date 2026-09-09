/**
 * The Syrius browser extension, as this app talks to it.
 *
 * The extension injects a promise-returning provider at `window.zenon` from a
 * MAIN-world content script at `document_start`, so it is present before this
 * app's own scripts run and is not subject to this page's CSP.
 *
 * `sendAccountBlock` is what makes the Zenon leg possible from a page at all: an
 * HTLC call is an ordinary send to an embedded contract carrying ABI-encoded
 * data, and this method takes a block with a `data` field — so `htlc.Create`,
 * `htlc.Unlock` and `htlc.Reclaim` are all expressible without the extension
 * knowing what an HTLC is. `sendTransaction`, a plain amount-to-address send, is
 * deliberately unused; every send this app needs carries data.
 *
 * The connection costs one prompt per origin and one per block signed. The read
 * methods are answered from the extension's session state and never prompt,
 * which is what lets this app restore a connection on load and re-read the
 * wallet after it changes account — see `readAccess`.
 */

/** Where to get the extension. */
export const SYRIUS_EXTENSION_URL = 'https://github.com/sol-znn/syrius-extension/releases'

/** The account block shape znn-ts-sdk's `AccountBlockTemplate.fromJson` reads. */
export interface ZenonAccountBlock {
  version: number
  chainIdentifier: number
  blockType: number
  hash: string
  previousHash: string
  height: number
  momentumAcknowledged: {hash: string; height: number}
  address: string
  toAddress: string
  amount: string
  tokenStandard: string
  fromBlockHash: string
  /** Base64. This is the field an embedded-contract call lives in. */
  data: string
  fusedPlasma: number
  difficulty: number
  nonce: string
  publicKey: string
  signature: string
}

/** The wallet's account of itself: the three settings a block depends on. */
export interface ZenonWalletAccess {
  address: string
  chainId: number
  nodeUrl: string
}

/** What `sendAccountBlock` answers with, as far as this app reads it. */
export interface ZenonSendResult {
  /** The published block's hash. For an `htlc.Create`, this is the HTLC id. */
  hash: string
  /**
   * The whole block as published — the wallet's `AccountBlockTemplate.toJson`
   * after it has filled in the account, the public key, the chain position and
   * the signature.
   *
   * Carried back into Go and compared field by field against the block this page
   * proposed. That comparison is the only way to learn the wallet signed with an
   * account other than the one it reported: the extension re-reads its selected
   * account at signing time, so somebody who switches it while the approval
   * window is open signs with the new one and nothing tells this page so.
   */
  signed?: Record<string, unknown>
}

/** A change the wallet announces about itself. */
export interface ZenonWalletChange {
  address?: string
  chainId?: number
  nodeUrl?: string
  /** The wallet revoked this origin, or locked. The connection is gone. */
  disconnected?: boolean
}

/** The provider object, as much of it as this app uses. */
interface ZenonProvider {
  isSyriusExtension?: boolean
  isZenon?: boolean
  version?: number
  accounts?: string[]
  chainId?: number | string | null
  connect(): Promise<string[]>
  disconnect(): Promise<boolean>
  getAccounts(): Promise<string[]>
  getChainId(): Promise<number | string | null>
  getNodeUrl(): Promise<string | null>
  sendAccountBlock(block: ZenonAccountBlock): Promise<{hash?: string; block?: unknown}>
  /**
   * Sign a plain message with the selected account's key.
   *
   * NOT IN THE EXTENSION YET. Declared optional and probed for at every call
   * site, so a build without it degrades to "cannot prove this address" rather
   * than to a TypeError -- and so the day it ships, nothing here needs changing
   * but the deletion of this paragraph.
   *
   * What the module expects, and the whole of the contract (wasm/board.go,
   * ProofZNN): the statement's raw UTF-8 bytes signed with the account's ed25519
   * key, and the 32-byte public key alongside it, because an ed25519 signature
   * carries no recoverable key the way a Bitcoin one does. Go then derives the
   * z1 address from that key and checks it against the address being claimed --
   * so a wallet cannot claim an account it does not hold, whatever it reports.
   *
   * The return shape is a guess at a convention that does not exist yet, and it
   * is read defensively in `signMessage` below: a string is taken as the
   * signature, an object is read for `signature`/`sig` and `publicKey`/`pubKey`.
   */
  signMessage?(
    message: string,
  ): Promise<string | {signature?: string; sig?: string; publicKey?: string; pubKey?: string}>
  on(event: string, handler: (data: unknown) => void): unknown
  removeListener(event: string, handler: (data: unknown) => void): unknown
}

declare global {
  interface Window {
    zenon?: Partial<ZenonProvider>
  }
}

/**
 * The provider, if it is there and is the version this file speaks. Methods are
 * checked for rather than a version compared: a wallet that only sets the old
 * `isSyriusExtension` flag has no `sendAccountBlock` to call, and finding that
 * out as "not a function" at the moment of a send is finding it out too late.
 */
function provider(): ZenonProvider | null {
  if (typeof window === 'undefined') return null
  const p = window.zenon
  if (!p || typeof p.sendAccountBlock !== 'function' || typeof p.connect !== 'function') {
    return null
  }
  return p as ZenonProvider
}

/** Whether a usable provider is on the page right now. */
export function zenonExtension(): boolean {
  return provider() !== null
}

/**
 * How long to wait for the extension to inject itself. A MAIN-world content
 * script at `document_start` runs before this app does, so in practice the
 * provider is already there; this covers an extension enabled while the page was
 * open, and settles on the `zenon#initialized` event rather than only polling.
 */
export function waitForExtension(ms = 3000): Promise<boolean> {
  if (zenonExtension()) return Promise.resolve(true)
  if (typeof window === 'undefined') return Promise.resolve(false)

  return new Promise((resolve) => {
    const done = (found: boolean) => {
      window.removeEventListener('zenon#initialized', announced)
      window.clearInterval(poll)
      window.clearTimeout(timer)
      resolve(found)
    }
    const announced = () => {
      if (zenonExtension()) done(true)
    }
    // Polled as well as listened for: the event fires once, and a provider that
    // arrived between the check above and this listener would never re-fire it.
    const poll = window.setInterval(() => {
      if (zenonExtension()) done(true)
    }, 100)
    const timer = window.setTimeout(() => done(false), ms)
    window.addEventListener('zenon#initialized', announced)
  })
}

/**
 * How long to wait for a person.
 *
 * The provider bounds only its transport and deliberately does not time out a
 * request waiting on somebody — they may be looking for their password. So the
 * deadline is this app's, set against what the person is actually doing: unlock
 * a wallet, read a block, press a button, with a proof-of-work search in the
 * middle if their account has no fused plasma.
 *
 * Expiring is not the same as failing. A block whose approval window is still
 * open when this fires may still be published, which is why the message says to
 * check the chain rather than saying it failed.
 */
const SEND_TIMEOUT_MS = 10 * 60 * 1000

/**
 * How long to give Syrius to put its window on screen.
 *
 * A deadline on the EXTENSION, not on the person, and the distinction is the
 * whole of it. A flat two-minute timeout answered both questions with one number
 * and got both wrong: far too long to sit watching a "Waiting for Syrius…"
 * button when nothing is ever going to open, and far too short for somebody who
 * did get a window and has gone to find their password.
 *
 * So the clock runs only until the wallet visibly does something. After that
 * there is no deadline: the request is waiting on a human, and rejecting a
 * promise they cannot see achieves nothing.
 */
const CONNECT_ACTIVATION_MS = 30 * 1000

/**
 * The ceiling once the wallet HAS opened its window. Not a second guess at how
 * long somebody needs — it is the same figure a block waiting to be signed gets,
 * because a promise that can never settle leaves the card disabled behind a
 * "Waiting for Syrius…" button with no way out but a reload.
 */
const CONNECT_PATIENCE_MS = SEND_TIMEOUT_MS

/** The read-only methods are answered from session state, or not at all. */
const READ_TIMEOUT_MS = 15 * 1000

/**
 * The block as something `postMessage` will accept.
 *
 * The provider hands its params to `window.postMessage`, which is structured
 * clone — and a Vue reactive object is a `Proxy`, which structured clone refuses
 * outright with "could not be cloned", naming no field. Every block this app
 * sends is held in a `ref`, so without this the send could not leave the page.
 *
 * A JSON round trip rather than a blunt `toRaw`, because the message does not
 * stop at the provider: the content script hands it to
 * `chrome.runtime.sendMessage`, which serialises it as JSON anyway. So anything
 * a round trip drops was never going to reach the wallet, and a value that
 * cannot survive the trip throws here, where the error names the request.
 */
function plain<T>(params: T): T {
  if (params === undefined) return params
  return JSON.parse(JSON.stringify(params)) as T
}

/**
 * The provider's rejection as an `Error`. It rejects with `{code, message}` — a
 * plain object — so anything reading `err.message` gets `undefined` and anything
 * stringifying one gets "[object Object]". The codes are EIP-1193's: 4001 user
 * rejected, 4100 not connected, 4200 unsupported, 4900 no answer, -32603
 * internal.
 */
function walletError(err: unknown, fallback: string): Error {
  if (err instanceof Error) return err
  if (err && typeof err === 'object') {
    const e = err as {code?: number; message?: string}
    if (e.code === 4001) {
      return new Error('You declined the request in Syrius. Nothing was sent.')
    }
    if (e.code === 4100) {
      return new Error(
        'Syrius does not have this page connected. Press Connect and approve it in the wallet.',
      )
    }
    if (e.message) return new Error(e.message)
  }
  return new Error(fallback)
}

/** Reject if the wallet has not answered in `ms`. */
function withTimeout<T>(work: Promise<T>, ms: number, what: string): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = window.setTimeout(() => reject(new Error(what)), ms)
    work.then(
      (value) => {
        window.clearTimeout(timer)
        resolve(value)
      },
      (err) => {
        window.clearTimeout(timer)
        reject(err)
      },
    )
  })
}

/**
 * Wait for a request that should open a wallet window, on a clock that stops the
 * moment one does.
 *
 * The signal is focus. Syrius approves in a window of its own, so the page losing
 * focus is the only evidence that anything appeared — there is no event for "I
 * have asked the user", and a request never going to be answered looks exactly
 * like one being read carefully. A heuristic, and allowed to be: being wrong in
 * the generous direction means waiting on somebody who is not there.
 *
 * Both a listener and a poll, for the same reason `waitForExtension` has both:
 * `blur` may already have fired, and `document.hasFocus()` is the state rather
 * than the edge.
 */
function untilActive<T>(work: Promise<T>, ms: number, what: string): Promise<T> {
  if (typeof window === 'undefined') return work
  return new Promise<T>((resolve, reject) => {
    let timer: number | undefined = window.setTimeout(() => {
      cleanup()
      reject(new Error(what))
    }, ms)

    // Something took the focus off this page: the wallet is on screen, and from
    // here on the only thing being waited on is a person.
    const activated = () => {
      if (timer === undefined) return
      window.clearTimeout(timer)
      timer = undefined
      stopWatching()
    }
    const onBlur = () => activated()
    const poll = window.setInterval(() => {
      if (!document.hasFocus()) activated()
    }, 250)
    function stopWatching() {
      window.clearInterval(poll)
      window.removeEventListener('blur', onBlur)
    }
    function cleanup() {
      if (timer !== undefined) window.clearTimeout(timer)
      timer = undefined
      stopWatching()
    }
    window.addEventListener('blur', onBlur)

    work.then(
      (value) => {
        cleanup()
        resolve(value)
      },
      (err) => {
        cleanup()
        reject(err)
      },
    )
  })
}

function requireProvider(): ZenonProvider {
  const p = provider()
  if (!p) {
    throw new Error(
      'No Syrius wallet on this page. Install or enable the extension and reload — it injects ' +
        'itself when the page loads, so a wallet enabled after this page opened is not there yet.',
    )
  }
  return p
}

/**
 * The wallet's chain id, as a number. The extension stores it as a string in
 * some builds and a number in others; comparing them without normalising is how
 * a swap gets refused for no reason.
 */
function asChainId(value: unknown): number {
  return Number(value ?? 0) || 0
}

/** Read the address, chain id and node URL together. */
async function readTriple(p: ZenonProvider, address: string): Promise<ZenonWalletAccess> {
  const [chainId, nodeUrl] = await withTimeout(
    Promise.all([p.getChainId(), p.getNodeUrl()]),
    READ_TIMEOUT_MS,
    'The wallet did not report its chain and node. It may have locked — open Syrius and try again.',
  )
  return {address, chainId: asChainId(chainId), nodeUrl: nodeUrl ?? ''}
}

/**
 * The wallet's state without asking anybody anything. Returns null when this
 * page is not connected or the wallet is locked, which the extension reports the
 * same way: an empty account list. Neither opens a window, so this is safe to
 * call on load and after any announced change — which is what keeps a person
 * from pressing Connect again after every reload.
 */
export async function readAccess(): Promise<ZenonWalletAccess | null> {
  const p = provider()
  if (!p) return null
  try {
    const accounts = await withTimeout(p.getAccounts(), READ_TIMEOUT_MS, 'no answer')
    const address = accounts?.[0]
    if (!address) return null
    return await readTriple(p, address)
  } catch {
    // A read that could not run is not a connection. Reported as absence rather
    // than as an error: nothing was asked of the user, so there is nothing to
    // tell them about yet.
    return null
  }
}

/**
 * Ask for the selected address, the wallet's chain id and its node URL. Opens
 * the connect prompt only for an origin the wallet has not connected before, or
 * one whose wallet is locked.
 */
export async function requestAccess(): Promise<ZenonWalletAccess> {
  const p = requireProvider()
  let accounts: string[]
  try {
    // Two independent clocks, and only the outer one can be stopped. The inner
    // is the absolute ceiling; the outer is the 30 seconds the extension has to
    // put something on screen, cleared the moment it does.
    accounts = await untilActive(
      withTimeout(
        p.connect(),
        CONNECT_PATIENCE_MS,
        'Syrius opened its window but never came back with an answer. Nothing was signed. Close ' +
          'the wallet window and press Connect again.',
      ),
      CONNECT_ACTIVATION_MS,
      'Syrius never opened its window. Nothing was sent to it and nothing was signed. The ' +
        'provider is on this page but did not put anything on screen within 30 seconds — open ' +
        'Syrius from the toolbar (an extension whose background worker has gone to sleep wakes ' +
        'up when you do), check this site is not blocked in its connected-sites list, then press ' +
        'Connect again.',
    )
  } catch (err) {
    throw walletError(err, 'Syrius refused the connect request.')
  }

  const address = accounts?.[0]
  if (!address) {
    throw new Error('The wallet granted access but reported no address.')
  }
  return readTriple(p, address)
}

/**
 * Tell the wallet to forget this page. This revokes something real: the
 * extension drops the origin from its connected-sites list, so the next
 * `connect` prompts again.
 */
export async function revokeAccess(): Promise<void> {
  const p = provider()
  if (!p || typeof p.disconnect !== 'function') return
  try {
    await withTimeout(p.disconnect(), READ_TIMEOUT_MS, 'no answer')
  } catch {
    // Forgetting is what the page wanted; whether the wallet also forgot is not
    // worth an error in the user's face.
  }
}

/**
 * Hand the wallet a complete account block to sign and publish. It arrives with
 * its chain position and plasma fields empty; the extension fills those in from
 * its own node and overwrites the address and public key with the account it has
 * selected. What this page decides — and all it decides — is `toAddress`,
 * `amount`, `tokenStandard` and `data`.
 */
export async function sendAccountBlock(block: ZenonAccountBlock): Promise<ZenonSendResult> {
  const p = requireProvider()
  let result: {hash?: string; block?: unknown}
  try {
    result = await withTimeout(
      p.sendAccountBlock(plain(block)),
      SEND_TIMEOUT_MS,
      'Syrius did not answer within ten minutes. It may still have published: check the chain ' +
        'with "Find it" before trying again, because signing a second time locks a second ' +
        'lot of ZNN.',
    )
  } catch (err) {
    throw walletError(err, 'The wallet did not publish the block.')
  }

  const hash = typeof result?.hash === 'string' ? result.hash : ''
  if (!hash) {
    throw new Error(
      'The wallet says the block was sent but reported no transaction hash. Find the HTLC ' +
        'id with "Find it", or copy it out of the wallet history.',
    )
  }
  const signed =
    result.block && typeof result.block === 'object'
      ? (result.block as Record<string, unknown>)
      : undefined
  return {hash, signed}
}

/** A signed statement, in the shape wasm/board.go verifies. */
export interface ZenonSignedMessage {
  /** 64-byte ed25519 signature, hex. */
  sig: string
  /** 32-byte ed25519 public key, hex. Go derives the address from this. */
  pubKey: string
}

/** Whether this wallet can sign a message at all — see the provider interface. */
export function canSignMessage(): boolean {
  return typeof provider()?.signMessage === 'function'
}

/**
 * Ask the wallet to sign a statement binding an address to this browser's board
 * key.
 *
 * `znn_sign` over the wire: the request desktop Syrius already serves over
 * WalletConnect, and the one an older extension build does not have. Everything
 * downstream of this exists and is tested -- the statement, the proof record,
 * the badge, and the ed25519 verification against a derived z1 address in
 * wasm/board.go.
 *
 * So this asks, and says so plainly when the answer is that it cannot. It does
 * not fall back to anything -- an unproven address shown as proven would be
 * worse than no badge -- and since a proof is what lets anybody post or take,
 * a build without it cannot trade a Zenon leg on the board.
 *
 * The response is read defensively because the shape is not settled: a bare
 * string is taken as the signature, an object is read for the two fields under
 * either of the two names each is plausibly given. Anything else is reported as
 * an answer this build cannot use, naming what it wanted.
 */
export async function signMessage(message: string): Promise<ZenonSignedMessage> {
  const p = requireProvider()
  if (typeof p.signMessage !== 'function') {
    throw new Error(
      'This Syrius build cannot sign a message, so it cannot prove it holds this address. The ' +
        'extension signs account blocks and nothing else — and the board only trades addresses ' +
        'a wallet has signed for, so a Zenon leg needs a build with signMessage (znn_sign). ' +
        'Reading the board works either way.',
    )
  }
  let answer: Awaited<ReturnType<NonNullable<ZenonProvider['signMessage']>>>
  try {
    answer = await untilActive(
      withTimeout(
        p.signMessage(message),
        CONNECT_PATIENCE_MS,
        'Syrius opened its window but never came back with a signature. Nothing was signed.',
      ),
      CONNECT_ACTIVATION_MS,
      'Syrius never opened its window, so nothing was signed. Open it from the toolbar and try ' +
        'again.',
    )
  } catch (err) {
    throw walletError(err, 'Syrius refused the signing request.')
  }

  if (typeof answer === 'string') {
    // A signature with no key is not usable: Go derives the address from the
    // key, and without one there is nothing to check the claim against.
    throw new Error(
      'The wallet returned a signature but no public key. An ed25519 signature does not carry ' +
        'one the way a Bitcoin signature does, and without it the address in this post cannot ' +
        'be checked against the key that signed.',
    )
  }
  const sig = answer?.signature ?? answer?.sig ?? ''
  const pubKey = answer?.publicKey ?? answer?.pubKey ?? ''
  if (!sig || !pubKey) {
    throw new Error(
      'The wallet answered in a shape this build does not recognise. It wants a signature and ' +
        'the 32-byte public key that made it, both hex.',
    )
  }
  return {sig, pubKey}
}

/**
 * Address, chain and node changes the wallet announces without being asked.
 * These carry the new value, so a change can be applied rather than only
 * noticed. `disconnect` is included: the wallet can revoke this origin from its
 * own settings screen, and a page that keeps offering to sign afterwards is
 * offering something that will be refused. Returns the unsubscribe function.
 */
export function onWalletChange(handler: (change: ZenonWalletChange) => void): () => void {
  const p = provider()
  if (!p) return () => {}

  const onAccounts = (data: unknown) => {
    const accounts = Array.isArray(data) ? (data as string[]) : []
    const address = accounts[0] ?? ''
    handler(address ? {address} : {disconnected: true})
  }
  const onChain = (data: unknown) => handler({chainId: asChainId(data)})
  const onNode = (data: unknown) => handler({nodeUrl: typeof data === 'string' ? data : ''})
  const onDisconnect = () => handler({disconnected: true})

  p.on('accountsChanged', onAccounts)
  p.on('chainChanged', onChain)
  p.on('nodeChanged', onNode)
  p.on('disconnect', onDisconnect)

  return () => {
    p.removeListener('accountsChanged', onAccounts)
    p.removeListener('chainChanged', onChain)
    p.removeListener('nodeChanged', onNode)
    p.removeListener('disconnect', onDisconnect)
  }
}

import {computed, onMounted, onUnmounted, ref} from 'vue'
import {
  NETWORK_OF_CHAIN,
  PROVIDER_EVENTS,
  UNISAT_CHAIN,
  addressFitsNetwork,
  unisat,
  waitForProvider,
  walletError,
} from '@/core/unisat'
import {useSettings, type Network} from '@/core/composables/useSettings'

/**
 * The connected wallet, as one shared thing.
 *
 * Module-level rather than per-component because there is one wallet and several
 * places that care about it: the create form wants an address to pay out to, the
 * funding prompt wants to send to a contract, the header wants to say whether
 * any of that is available.
 *
 * What this connector may do is bounded by what a swap needs, and both are
 * things any website may ask a wallet for: read an address, and make an ordinary
 * send. The swap's own key is generated in the page and never offered to the
 * wallet, so connecting one changes nothing about custody.
 */

/**
 * That the user connected before, not the connection itself. A reload should not
 * need a second prompt for a wallet that is already unlocked, and getAccounts()
 * reconnects silently -- but calling it on a page the user never connected would
 * silently reveal their address to a site they only looked at.
 */
const REMEMBER_KEY = 'ferry.unisat.connected'

const address = ref('')
const publicKey = ref('')
const balance = ref<{confirmed: number; unconfirmed: number; total: number} | null>(null)
const chain = ref('')
const busy = ref(false)
const error = ref('')
let listening = false

/**
 * Whether a provider is on the page.
 *
 * A ref rather than a computed over `window.unisat`, and that distinction is the
 * whole bug it fixes: `window.unisat` is a plain property an extension assigns
 * whenever its content script happens to run, so a computed over it has no
 * reactive dependency to invalidate. Vue evaluates it once, caches "no wallet",
 * and never looks again -- telling somebody they have no wallet installed while
 * it sits in their toolbar.
 *
 * So detection is a value that gets SET when the provider turns up. It is only
 * ever a hint: the connect button does not wait for it, because the
 * authoritative test for whether there is a wallet is asking the wallet.
 */
const detected = ref(Boolean(unisat()))

const available = computed(() => detected.value)
const connected = computed(() => Boolean(address.value))

/** Which of this app's networks the wallet is on, or '' if it is one we have no name for. */
const walletNetwork = computed<Network | ''>(() => NETWORK_OF_CHAIN[chain.value] ?? '')

/**
 * How long to keep looking for a provider that has not appeared yet.
 *
 * Extensions inject at document_start or document_idle depending on the
 * browser's mood, and a cold profile can be slower. Ten seconds covers every
 * case observed and then stops, because a page that polls forever for a wallet
 * nobody has installed is burning a timer for nothing.
 *
 * Stopping is safe because the poll is not the only way a provider is noticed:
 * the extension's own announcement is listened for, the tab coming back to the
 * front restarts the search, and the connect button asks the wallet directly.
 */
const PROVIDER_POLL_MS = 250
const PROVIDER_POLL_LIMIT = 40

let polling: ReturnType<typeof setInterval> | undefined
let polls = 0

function stopPolling() {
  if (polling) clearInterval(polling)
  polling = undefined
}

/** A provider has turned up. Do the two things that were skipped without one. */
function adoptProvider() {
  detected.value = true
  stopPolling()
  listen()
  void resume()
}

/** Watch for a provider that is injected after this page has already rendered. */
function watchForProvider() {
  if (unisat()) {
    // Already there: still needs its listeners attached and a silent reconnect.
    adoptProvider()
    return
  }
  if (polling) return
  polls = 0
  polling = setInterval(() => {
    polls += 1
    if (unisat()) adoptProvider()
    else if (polls >= PROVIDER_POLL_LIMIT) stopPolling()
  }, PROVIDER_POLL_MS)
}

/**
 * Attach the page-level triggers that find a wallet without a reload.
 *
 * Three, because the ways a provider arrives late differ. The extension
 * announcing itself covers an injection that lost the race with this page; the
 * tab regaining focus covers the two things a user actually does -- installing
 * the extension in another tab, and unlocking it in its own popup -- neither of
 * which fires anything here. Both restart the search, which is idempotent.
 *
 * Attached once, at module scope, because the state they write is module state.
 */
let hooked = false

function hookProviderTriggers() {
  if (hooked || typeof window === 'undefined') return
  hooked = true
  const look = () => {
    if (unisat()) adoptProvider()
    else watchForProvider()
  }
  for (const ev of PROVIDER_EVENTS) {
    window.addEventListener(ev, look)
    document.addEventListener(ev, look)
  }
  window.addEventListener('focus', look)
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') look()
  })
}

function remember(on: boolean) {
  try {
    if (on) localStorage.setItem(REMEMBER_KEY, '1')
    else localStorage.removeItem(REMEMBER_KEY)
  } catch {
    // A browser that refuses storage costs a prompt on reload, nothing more.
  }
}

function wasConnected(): boolean {
  try {
    return localStorage.getItem(REMEMBER_KEY) === '1'
  } catch {
    return false
  }
}

async function readChain(w = unisat()) {
  if (!w) return
  try {
    if (w.getChain) {
      chain.value = (await w.getChain()).enum
      return
    }
    // Older builds only have getNetwork, which cannot tell signet from testnet.
    // Mapping it to the coarse answer is better than reporting nothing: a
    // mainnet/testnet mix-up is the one this needs to catch.
    const net = await w.getNetwork?.()
    chain.value = net === 'livenet' ? 'BITCOIN_MAINNET' : net ? 'BITCOIN_TESTNET' : ''
  } catch {
    chain.value = ''
  }
}

async function readBalance(w = unisat()) {
  if (!w) return
  try {
    balance.value = await w.getBalance()
  } catch {
    balance.value = null
  }
}

async function adopt(accounts: string[]) {
  const w = unisat()
  address.value = accounts[0] ?? ''
  if (!address.value) {
    publicKey.value = ''
    balance.value = null
    return
  }
  try {
    publicKey.value = (await w?.getPublicKey()) ?? ''
  } catch {
    publicKey.value = ''
  }
  await Promise.all([readChain(w), readBalance(w)])
}

/** Ask for access. This is the only call that shows a prompt. */
async function connect(): Promise<boolean> {
  // Looked up fresh, never read off `detected`, and WAITED for rather than
  // sampled. The old version asked once, at the instant of the click, and told
  // anybody whose extension had not finished injecting to reload the page — for
  // a wallet that was about to arrive a few hundred milliseconds later. So the
  // button holds for a moment instead: `busy` is set first, so the wait shows
  // as a working button rather than as a dead one.
  error.value = ''
  busy.value = true
  try {
    const w = (await waitForProvider()) ?? unisat()
    if (!w) {
      detected.value = false
      error.value =
        'No Bitcoin wallet answered. If UniSat is installed, open it, unlock it, and press ' +
        'Connect again — a locked extension does not always answer a page. Otherwise install ' +
        'UniSat and press Connect; there is no need to reload this page.'
      return false
    }
    detected.value = true
    // The provider turned up after mount, so nothing has attached its listeners
    // yet. Doing it here means a connect is never the thing that leaves this
    // page blind to an account or network change afterwards.
    listen()
    await adopt(await w.requestAccounts())
    remember(true)
    return connected.value
  } catch (e) {
    error.value = walletError(e)
    return false
  } finally {
    busy.value = false
  }
}

/** Forget the wallet here. The extension has no disconnect; this is ours. */
function disconnect() {
  address.value = ''
  publicKey.value = ''
  balance.value = null
  chain.value = ''
  error.value = ''
  remember(false)
}

/** Reconnect without a prompt, for a page the user already connected. */
async function resume() {
  const w = unisat()
  if (!w || !wasConnected() || connected.value) return
  try {
    await adopt(await w.getAccounts())
  } catch {
    // An extension that is present but locked answers with an error. There is
    // nothing to say about it: the connect button is right there.
  }
}

/** Move the wallet onto the network this app is set to. */
async function switchTo(network: Network): Promise<boolean> {
  const w = unisat()
  const target = UNISAT_CHAIN[network]
  error.value = ''
  if (!w || !target) {
    error.value = `UniSat has no ${network} chain, so this swap's Bitcoin has to be handled by hand.`
    return false
  }
  busy.value = true
  try {
    if (w.switchChain) await w.switchChain(target)
    else await w.switchNetwork?.(network === 'mainnet' ? 'livenet' : 'testnet')
    await adopt(await w.getAccounts())
    return true
  } catch (e) {
    error.value = walletError(e)
    return false
  } finally {
    busy.value = false
  }
}

/**
 * Send to an address, which for a swap means funding a contract. The network is
 * checked twice before the wallet is asked, because this is the one call here
 * that moves money and the two ways it goes wrong are both silent -- a wallet on
 * the wrong chain, and an address from the wrong chain -- and neither is
 * recoverable once the send is signed.
 */
async function send(to: string, satoshis: number, feeRate?: number): Promise<string> {
  const w = unisat()
  error.value = ''
  if (!w || !connected.value) throw new Error('No UniSat wallet is connected.')

  const {stored} = useSettings()
  const network = stored.value.network as Network
  if (walletNetwork.value && walletNetwork.value !== network) {
    throw new Error(
      `UniSat is on ${walletNetwork.value} and this swap is on ${network}. ` +
        `Switch the wallet before funding.`,
    )
  }
  if (!addressFitsNetwork(to, network)) {
    throw new Error(`${to} is not a ${network} address; nothing was sent.`)
  }

  busy.value = true
  try {
    const txid = await w.sendBitcoin(to, satoshis, feeRate ? {feeRate} : undefined)
    void readBalance(w)
    return txid
  } finally {
    busy.value = false
  }
}

/**
 * Sign a fixed sentence, to prove this wallet holds the address it reports.
 *
 * The one call here that is not part of a swap. It exists for the board, where
 * an offer is signed by a key of this app's own and a wallet signature is what
 * ties that key to an address a counterparty can look up -- see wasm/board.go.
 * Once per identity, not once per post.
 *
 * "ecdsa" rather than the wallet's default, because the module verifies by
 * recovering the public key from the signature, and only the compact form
 * carries one.
 *
 * It signs and returns; it does not decide whether the signature is any good.
 * That is Go's, against a statement Go also produced -- a page that judged its
 * own proof would be judging the half of the exchange that is not in question.
 */
async function signMessage(message: string): Promise<string> {
  const w = unisat()
  error.value = ''
  if (!w || !connected.value) throw new Error('No UniSat wallet is connected.')
  if (!w.signMessage) {
    throw new Error(
      'This UniSat build cannot sign a message, so it cannot prove it holds this address. ' +
        'Update the extension, or post without a Bitcoin proof — the post is still signed, and ' +
        'still only yours to edit.',
    )
  }
  busy.value = true
  try {
    return await w.signMessage(message, 'ecdsa')
  } finally {
    busy.value = false
  }
}

/**
 * Follow the wallet rather than snapshotting it. Switching account or network is
 * a two-click operation the user will do without thinking about this page, and a
 * stale address here is an address a payout gets sent to. Attached once,
 * globally, because the state they write is global.
 */
function listen() {
  const w = unisat()
  if (!w || listening || !w.on) return
  listening = true
  w.on('accountsChanged', (...args: unknown[]) => {
    const accounts = (args[0] as string[]) ?? []
    // An empty list means the wallet was locked or this site's access revoked.
    // Dropping the remembered flag stops the next load prompting about a wallet
    // the user just walked away from.
    if (!accounts.length) disconnect()
    else void adopt(accounts)
  })
  const onChain = () => {
    void readChain()
    void readBalance()
  }
  w.on('chainChanged', onChain)
  w.on('networkChanged', onChain)
}

export function useUnisat() {
  onMounted(() => {
    // watchForProvider attaches the listeners and resumes once there is
    // something to attach them to, whether that is now or in three seconds;
    // hookProviderTriggers covers the wallet that arrives later than that.
    hookProviderTriggers()
    watchForProvider()
  })
  // Nothing is torn down: the listeners are process-wide and shared by every
  // component that calls this, so removing them when one unmounts would blind
  // the others.
  onUnmounted(() => {})

  return {
    available,
    connected,
    address,
    publicKey,
    balance,
    chain,
    walletNetwork,
    busy,
    error,
    connect,
    disconnect,
    switchTo,
    send,
    signMessage,
  }
}

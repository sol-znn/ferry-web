import {computed, getCurrentInstance, ref} from 'vue'
import {api} from '@/core/api'
import {useSettings} from '@/core/composables/useSettings'
import {useSolana} from '@/core/composables/useSolana'
import {useUnisat} from '@/core/composables/useUnisat'
import {useZenonWallet} from '@/core/composables/useZenonWallet'
import {signMessage as syriusSign} from '@/core/zenon-wallet'
import type {BoardIdentity, ProofScheme} from '@/types'

/**
 * Who this browser is on the board, and which wallets have vouched for it.
 *
 * The identity is a key the module mints on first use. It is not an account and
 * there is nothing to sign up for: it exists so that a post can be signed, and
 * therefore so that only its author can edit or withdraw it. That property costs
 * nothing and is on by default.
 *
 * A BINDING is the separate thing: a wallet signature saying "the key that signs
 * those posts is held by whoever holds this address". One signature per
 * identity, not one per post -- a board where every keystroke opened a wallet
 * popup is a board nobody would edit. The delegation is spelled out in the
 * statement being signed, so nobody is agreeing to something implicit.
 *
 * It is required rather than decorative: nobody posts or takes without one on
 * every chain the offer settles on. See core/chains.ts for how "every chain the
 * offer settles on" is worked out, and useChainProofs for the question the
 * board's gates actually ask.
 *
 * Module-level, because there is one identity and several places that care about
 * it: the post form attaches proofs, a row shows a badge, and this panel is where
 * they are managed.
 */

/**
 * The two wallet composables, captured while a component is setting up.
 *
 * `useUnisat` registers `onMounted`, so calling it from a click handler would
 * warn and would attach a lifecycle hook to nothing. The bind functions below
 * run from clicks, so the handles are taken once, here, on the first call made
 * during a setup — which every component that offers a Bind button makes.
 *
 * Guarded rather than assumed, because this composable is also called from
 * `useBoard.start()`, which runs from a mounted hook and only ever wants the
 * identity itself.
 */
let unisat: ReturnType<typeof useUnisat> | null = null
let zenon: ReturnType<typeof useZenonWallet> | null = null
let solana: ReturnType<typeof useSolana> | null = null

const identity = ref<BoardIdentity | null>(null)
const busy = ref(false)
const error = ref('')
/** What the last successful bind proved, so the panel can say so out loud. */
const lastBound = ref<ProofScheme | ''>('')

const pubKey = computed(() => identity.value?.pubKey ?? '')

/** The bound address for a scheme, or '' — what the post form fills in from. */
function boundAddress(scheme: ProofScheme): string {
  return identity.value?.bindings.find((b) => b.scheme === scheme)?.address ?? ''
}

/**
 * How long a post may run on this instance, and how long one runs by default.
 *
 * From the module, not from a constant here: the default differs by build --
 * fifteen minutes on development, a day on production -- and a form holding its
 * own copy would offer a span the module then refused to mean. The fallback is
 * only for the moment before the identity has loaded.
 */
const limits = computed(
  () => identity.value?.limits ?? {defaultTtl: 24 * 3600, minTtl: 600, maxTtl: 7 * 24 * 3600},
)

const btcBound = computed(() => boundAddress('btc-ecdsa'))
const znnBound = computed(() => boundAddress('znn-ed25519'))
const solBound = computed(() => boundAddress('sol-ed25519'))

/**
 * Whether the connected Syrius extension can sign a message at all.
 *
 * Reads `useZenonWallet`'s own `canSign`, which is a `ref` kept live by the same
 * detection cycle that tracks whether the extension is there at all -- not a
 * fresh probe taken here, which would have the exact bug `detected` in
 * useUnisat.ts exists to avoid: a plain read of `window.zenon.signMessage`
 * cached the instant this computed first ran and never looked again.
 *
 * Falls back to a one-shot read only for the moment before any component has
 * called `useBoardIdentity()` during setup, so this never throws on a
 * not-yet-assigned `zenon`; in practice that moment is not observable, because
 * every component offering a Bind button makes that call before its template
 * can read this.
 */
const zenonCanSign = computed(() => zenon?.canSign.value ?? false)

async function load() {
  try {
    identity.value = (await api.boardIdentity()).identity
    error.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

/**
 * Prove a Bitcoin address.
 *
 * Three steps and each belongs where it is: Go writes the statement, because the
 * statement is what the signature commits to and a second copy of it here would
 * fail every proof invisibly; UniSat signs it; Go verifies, by recovering the key
 * and deriving every address form it writes. This function does no checking of
 * its own, which is the point -- a page that judged its own proof would be
 * judging the half that is not in question.
 */
async function bindBitcoin(): Promise<boolean> {
  const {body: settings} = useSettings()
  error.value = ''
  lastBound.value = ''
  if (!unisat) {
    error.value = 'The wallet connector is not ready on this page yet.'
    return false
  }
  const {connected, address, connect, signMessage} = unisat

  // Connect first when nothing is connected, rather than refusing.
  //
  // The board has no wallet strip of its own -- the swaps page is where a
  // wallet is normally connected, and somebody who came straight to the board
  // has never been offered one. A button disabled until they find that other
  // page is a dead end with no sign pointing out of it, so the button does the
  // connecting. It is the same prompt they would have got there, asked at the
  // moment they asked for the thing that needs it.
  busy.value = true
  try {
    if (!connected.value) {
      if (!(await connect())) {
        // connect() puts its own sentence in useUnisat's error; a declined
        // prompt sets none, because closing a window on purpose is not a fault.
        error.value =
          unisat.error.value ||
          'No Bitcoin wallet was connected, so there is no address to prove yet.'
        return false
      }
    }
    if (!address.value) {
      error.value = 'The wallet connected but reported no address.'
      return false
    }
    // Deliberately no network check. A signed message is a signature over a
    // hash by a key -- no chain appears in it -- and the module compares the
    // recovered key against every network's spelling of the address, so a
    // wallet on mainnet proves a mainnet address perfectly well on a regtest
    // board. Refusing here was worse than pointless: UniSat has no regtest
    // chain at all, so on the development instance it refused the only proof
    // anybody could possibly make.
    const {statement} = await api.boardStatement(address.value)
    const sig = await signMessage(statement)
    identity.value = (
      await api.boardBind({scheme: 'btc-ecdsa', address: address.value, sig}, settings.value)
    ).identity
    lastBound.value = 'btc-ecdsa'
    return true
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    return false
  } finally {
    busy.value = false
  }
}

/**
 * Prove a Zenon address.
 *
 * Everything this rests on exists and is tested -- the statement, the ed25519
 * verification, the address derived from the signing key in
 * wasm/znn/address.go, the badge. What it needs from the wallet is a
 * `signMessage`, which older Syrius builds do not have; those come back with
 * the sentence in `zenon-wallet.ts` naming what is missing rather than with a
 * button that was never pressable.
 *
 * It deliberately does not fall back to anything. A page could ask Syrius to
 * publish a self-send carrying the board key and read the claim back off the
 * chain, which would work and would cost plasma and an on-chain footprint for
 * every identity; or it could accept the address unproven and badge it anyway,
 * which is worse than no badge. Saying so is better than either, and it is
 * honest about the cost: since a proof is now what lets anybody act, a build
 * that cannot sign cannot trade a Zenon leg here at all.
 */
async function bindZenon(): Promise<boolean> {
  const {body: settings} = useSettings()
  error.value = ''
  lastBound.value = ''
  if (!zenon) {
    error.value = 'The wallet connector is not ready on this page yet.'
    return false
  }
  const {connected, address, connect} = zenon

  // Connect first rather than refusing — see bindBitcoin for why the button
  // does this instead of pointing at a wallet strip that is on another page.
  busy.value = true
  try {
    if (!connected.value) {
      await connect()
    }
    if (!address.value) {
      error.value = zenon.error.value || 'Syrius connected but reported no address.'
      return false
    }
    const {statement} = await api.boardStatement(address.value)
    const {sig, pubKey: signer} = await syriusSign(statement)
    identity.value = (
      await api.boardBind(
        {scheme: 'znn-ed25519', address: address.value, pubKey: signer, sig},
        settings.value,
      )
    ).identity
    lastBound.value = 'znn-ed25519'
    return true
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    return false
  } finally {
    busy.value = false
  }
}

/**
 * Prove a Solana address.
 *
 * The shortest of the three, and the difference is worth stating: a Solana
 * address IS its public key, so there is no key to carry alongside the signature
 * and no derivation for Go to check. Whatever verifies against the address in the
 * proof was made by whoever holds it.
 *
 * Phantom, Solflare and Backpack all expose `signMessage`. One that does not
 * comes back with the sentence in core/solana.ts naming what is missing, rather
 * than with a button that was never pressable.
 */
async function bindSolana(): Promise<boolean> {
  const {body: settings} = useSettings()
  error.value = ''
  lastBound.value = ''
  if (!solana) {
    error.value = 'The wallet connector is not ready on this page yet.'
    return false
  }
  const {connected, address, connect, signMessage} = solana

  // Connect first rather than refusing -- see bindBitcoin for why the button
  // does this instead of pointing at a wallet strip that is on another page.
  busy.value = true
  try {
    if (!connected.value) {
      if (!(await connect())) {
        error.value =
          solana.error.value ||
          'No Solana wallet was connected, so there is no address to prove yet.'
        return false
      }
    }
    if (!address.value) {
      error.value = 'The wallet connected but reported no address.'
      return false
    }
    const {statement} = await api.boardStatement(address.value)
    const sig = await signMessage(statement)
    identity.value = (
      await api.boardBind({scheme: 'sol-ed25519', address: address.value, sig}, settings.value)
    ).identity
    lastBound.value = 'sol-ed25519'
    return true
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    return false
  } finally {
    busy.value = false
  }
}

/**
 * Drop a binding.
 *
 * It affects the next post and nothing before it. Posts already on a relay carry
 * their own signed copy of the proof, and rewriting those is precisely what the
 * design prevents -- so this is not a recall. Withdrawing the posts is.
 */
async function unbind(scheme: ProofScheme) {
  error.value = ''
  busy.value = true
  try {
    identity.value = (await api.boardUnbind(scheme)).identity
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

export function useBoardIdentity() {
  // Only during setup — see the note on `unisat` above.
  if (getCurrentInstance()) {
    unisat ??= useUnisat()
    zenon ??= useZenonWallet()
    solana ??= useSolana()
  }
  return {
    identity,
    pubKey,
    limits,
    btcBound,
    znnBound,
    solBound,
    zenonCanSign,
    busy,
    error,
    lastBound,
    load,
    bindBitcoin,
    bindZenon,
    bindSolana,
    unbind,
  }
}

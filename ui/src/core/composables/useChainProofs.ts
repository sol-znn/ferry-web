import {computed} from 'vue'
import {useBoardIdentity} from '@/core/composables/useBoardIdentity'
import {
  CHAIN_IDS,
  PAIRS,
  chainList,
  missingChains,
  postablePairs,
  type ChainId,
} from '@/core/chains'

/**
 * Which chains this browser has proven an address on, as the one question the
 * board asks before letting anybody act.
 *
 * The board's gate used to be "both wallets connected". A connected wallet hands
 * over an address and nothing else, so the gate answered "can this person be
 * paid" and stopped there — which was the whole requirement while every offer
 * had the same two legs.
 *
 * It is now "an address PROVEN on every chain this offer settles on", and both
 * halves of that matter:
 *
 * Proven, because connecting is not a claim. A wallet reports whatever address
 * it likes and a page pastes it into a post; a proof is a signature over a
 * statement naming this board key and that address, verified in Go against a key
 * the address is derived from. It costs one popup per chain, once, and it is what
 * makes the address on an offer worth reading.
 *
 * Per chain, because the requirement belongs to the trade rather than to the
 * app. A Bitcoin-against-Zenon offer needs both; an offer between two Zenon
 * tokens needs Zenon and one proof covers both its legs; a SOL-against-BTC offer
 * needs neither a Zenon wallet nor a Zenon node. See chains.ts — the requirement
 * is derived from the post, so a pair this file has never heard of gates itself
 * correctly.
 *
 * Reading only. Proving is `useBoardIdentity`'s job and the buttons that do it
 * live in `BoardIdentity`; this is the question, not another answer to it.
 *
 * NOTE for callers rendering long lists: this reads the identity, which pulls in
 * the wallet composables when it is called during setup — and `useUnisat`
 * registers a mounted hook. Two hundred rows asking this for themselves would
 * attach two hundred of them to one question, so a page calls it once and passes
 * `proven` down. Everything a row then needs is a pure function in chains.ts.
 */
export function useChainProofs() {
  const {btcBound, znnBound, solBound} = useBoardIdentity()

  /** The proven address per chain, or '' — the address that goes into a post or
   *  a take. Never the wallet's currently selected one: Go refuses to publish an
   *  address it holds no proof for, so an unproven address here would be an
   *  offer that fails at the last step instead of a button that says why. */
  const addresses = computed<Record<ChainId, string>>(() => ({
    btc: btcBound.value,
    znn: znnBound.value,
    sol: solBound.value,
  }))

  const proven = computed<ChainId[]>(() => CHAIN_IDS.filter((id) => Boolean(addresses.value[id])))

  function addressFor(chain: ChainId): string {
    return addresses.value[chain] ?? ''
  }

  /** The proven addresses for exactly the chains a trade settles on, in the
   *  shape a publish or a take sends. Only those chains: an address filed under
   *  one the trade does not touch is a claim nobody reading it can act on, and
   *  Go drops it rather than publishing it. */
  function addrsFor(need: readonly ChainId[]): Partial<Record<ChainId, string>> {
    const out: Partial<Record<ChainId, string>> = {}
    for (const c of need) {
      const addr = addressFor(c)
      if (addr) out[c] = addr
    }
    return out
  }

  /** What is still unproven of what this trade needs. */
  function missingFor(need: readonly ChainId[]): ChainId[] {
    return missingChains(need, proven.value)
  }

  function readyFor(need: readonly ChainId[]): boolean {
    return missingFor(need).length === 0
  }

  /** Why the board is not letting you act, in a sentence that names the chains
   *  rather than saying "prove your addresses" to somebody who has proven one. */
  function messageFor(need: readonly ChainId[]): string {
    const missing = missingFor(need)
    if (!missing.length) return ''
    const one = missing.length === 1
    return (
      `Prove your ${chainList(missing)} address${one ? '' : 'es'} first — this offer settles on ` +
      `${chainList(need)}, and a proof is what says the address your half gets paid to is ` +
      `yours. The button${one ? ' is' : 's are'} under your board key.`
    )
  }

  /** Whether anything at all can be posted with the proofs held. With one pair
   *  this is "both proven"; with two it is "either pair's chains proven", which
   *  is the point of asking it this way. */
  const canPostAny = computed(() => postablePairs(proven.value).length > 0)

  /** The shortest true answer to "what do I have to prove before I can post
   *  anything": whatever the nearest pair is still missing. Naming every
   *  unproven chain would overstate it the moment a second pair exists — a
   *  ZNN↔ZNN offer would be postable while the sentence still asked for
   *  Bitcoin. */
  const postBlockers = computed<ChainId[]>(() => {
    if (canPostAny.value) return []
    return (
      PAIRS.map((p) => missingChains(p.chains, proven.value)).reduce(
        (best, m) => (best === null || m.length < best.length ? m : best),
        null as ChainId[] | null,
      ) ?? []
    )
  })

  return {
    proven,
    addresses,
    addressFor,
    addrsFor,
    missingFor,
    readyFor,
    messageFor,
    canPostAny,
    postBlockers,
  }
}

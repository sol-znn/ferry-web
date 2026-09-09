import type {BoardPost, ProofScheme} from '@/types'

/**
 * Which chains exist here, which one an offer touches, and how a chain is named
 * to somebody reading a button.
 *
 * This file exists because the board's rule is per chain rather than per app:
 * you may act on an offer once you have PROVEN an address on every chain that
 * offer settles on. Today every offer is Bitcoin against Zenon and the rule
 * reads as "both" — which is exactly why it needed writing down as a list. The
 * moment a second Zenon token trades against a first, or a third chain arrives,
 * "both" becomes wrong in a way that is invisible: a ZNN↔ZNN offer would demand
 * a Bitcoin proof for a leg that does not exist, and a new chain would demand
 * nothing at all.
 *
 * So the requirement is DERIVED from the offer rather than asserted beside it.
 * `chainsForPost` reads the post and says which chains it settles on; every gate
 * in the UI and the refusal in Go are written over that answer. Adding a chain
 * is a row in `CHAINS` plus a proof scheme in Go; adding a pair is a row in
 * `PAIRS`. Neither is a rule change.
 *
 * Nothing here is trusted with anything. It decides which button is disabled and
 * which sentence names what is missing — the enforcement lives in
 * `handleBoardPublish` and `handleBoardTake`, which refuse to sign a post or a
 * take carrying an address this browser has not proven.
 */

export type ChainId = 'btc' | 'znn'

export interface Chain {
  id: ChainId
  /** How a button says it: "Prove Bitcoin address". A chain, not a wallet —
   *  UniSat and Syrius are how you prove one, not what is being proven. */
  label: string
  /** The proof this chain's address is claimed with. One per chain: the scheme
   *  is a property of the signature format, and a chain that gained a second
   *  would be two rows here rather than a list. */
  scheme: ProofScheme
}

export const CHAINS: readonly Chain[] = [
  {id: 'btc', label: 'Bitcoin', scheme: 'btc-ecdsa'},
  {id: 'znn', label: 'Zenon', scheme: 'znn-ed25519'},
]

export const CHAIN_IDS: readonly ChainId[] = CHAINS.map((c) => c.id)

export function chainFor(id: ChainId): Chain {
  const found = CHAINS.find((c) => c.id === id)
  if (!found) throw new Error(`${id} is not a chain this build knows`)
  return found
}

export function chainLabel(id: ChainId): string {
  return chainFor(id).label
}

/** "Bitcoin", "Bitcoin and Zenon", "Bitcoin, Zenon and …" — a list as somebody
 *  would say it, because these go into sentences rather than into a legend. */
export function chainList(ids: readonly ChainId[]): string {
  const names = ids.map(chainLabel)
  if (names.length <= 1) return names[0] ?? ''
  return `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`
}

/**
 * Which chains an offer settles on.
 *
 * Read off the terms rather than off a field naming a pair, because the terms
 * are what a relay hands over and what Go re-checks — a `pair: "btc-znn"` tag
 * would be a third thing that could disagree with the two amounts it summarises,
 * and posts already on relays do not carry one.
 *
 * A post naming nothing recognisable comes back as every chain rather than
 * none: this answer gates buttons, and a gate that opens for a post it does not
 * understand is the wrong way for it to fail.
 */
export function chainsForPost(post: BoardPost): ChainId[] {
  const out: ChainId[] = []
  if (post.amountSats > 0 || post.btcAddr) out.push('btc')
  if (post.zenonAmt?.trim() || post.znnAddr) out.push('znn')
  return out.length ? out : [...CHAIN_IDS]
}

/** What is still unproven of what an offer needs. Ordered as `CHAINS` is, so two
 *  sentences on the same page never name the same pair in a different order. */
export function missingChains(need: readonly ChainId[], proven: readonly ChainId[]): ChainId[] {
  return CHAIN_IDS.filter((id) => need.includes(id) && !proven.includes(id))
}

/** A trade this build can post. One entry today; the gates are written over the
 *  list rather than over its contents, so a second pair is a row here. */
export interface Pair {
  id: string
  label: string
  chains: readonly ChainId[]
}

export const PAIRS: readonly Pair[] = [{id: 'btc-znn', label: 'BTC ⇄ ZNN', chains: ['btc', 'znn']}]

/** The pairs somebody holding these proofs may post. Empty means the Post button
 *  has nothing to offer yet, which is a sentence rather than a dead control. */
export function postablePairs(proven: readonly ChainId[]): Pair[] {
  return PAIRS.filter((p) => p.chains.every((c) => proven.includes(c)))
}

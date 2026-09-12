import type {BoardPost, PostLeg, ProofScheme} from '@/types'

/**
 * Which chains exist here, which ones a trade touches, and how a chain is named
 * to somebody reading a button.
 *
 * This file exists because the board's rule is per chain rather than per app:
 * you may act on an offer once you have PROVEN an address on every chain that
 * offer settles on. v1 had one pair, so the rule read as "both" — which is
 * exactly why it needed writing down as a list. With four pairs, "both" is wrong
 * in a way that is invisible: a ZTS↔ZTS offer would demand a Bitcoin proof for a
 * leg that does not exist, and a Solana one would demand nothing at all.
 *
 * So the requirement is DERIVED from the offer rather than asserted beside it.
 * `chainsForPost` reads the post and says which chains it settles on; every gate
 * in the UI and the refusal in Go are written over that answer.
 *
 * Nothing here is trusted with anything. It decides which button is disabled and
 * which sentence names what is missing — the enforcement lives in
 * `handleBoardPublish` and `handleBoardTake`, which refuse to sign a post or a
 * take carrying an address this browser has not proven.
 */

export type ChainId = 'btc' | 'znn' | 'sol'

export interface Chain {
  id: ChainId
  /** How a button says it: "Prove Bitcoin address". A chain, not a wallet —
   *  UniSat, Syrius and Phantom are how you prove one, not what is proven. */
  label: string
  /** What a person types amounts in. Not always the base unit: Solana is typed
   *  in SOL and held in lamports, which is the conversion Go owns. */
  unit: string
  /** True where a leg names a token as well as an amount. */
  tokenised: boolean
  /** The proof this chain's address is claimed with. One per chain: the scheme
   *  is a property of the signature format. */
  scheme: ProofScheme
  /** Which wallet this build drives for it, for a sentence that names one. */
  wallet: string
}

export const CHAINS: readonly Chain[] = [
  {id: 'btc', label: 'Bitcoin', unit: 'sat', tokenised: false, scheme: 'btc-ecdsa', wallet: 'UniSat'},
  {id: 'znn', label: 'Zenon', unit: '', tokenised: true, scheme: 'znn-ed25519', wallet: 'Syrius'},
  {id: 'sol', label: 'Solana', unit: 'SOL', tokenised: false, scheme: 'sol-ed25519', wallet: 'Phantom'},
]

export const CHAIN_IDS: readonly ChainId[] = CHAINS.map((c) => c.id)

export function chainFor(id: ChainId): Chain {
  const found = CHAINS.find((c) => c.id === id)
  if (!found) throw new Error(`${id} is not a chain this build knows`)
  return found
}

export function chainLabel(id: ChainId): string {
  return CHAINS.find((c) => c.id === id)?.label ?? id
}

export function schemeFor(id: ChainId): ProofScheme {
  return chainFor(id).scheme
}

/** The chain a proof scheme is about — the inverse of `schemeFor`. */
export function chainForScheme(scheme: ProofScheme): ChainId | '' {
  return CHAINS.find((c) => c.scheme === scheme)?.id ?? ''
}

/** "Bitcoin", "Bitcoin and Zenon", "Bitcoin, Zenon and Solana" — a list as
 *  somebody would say it, because these go into sentences. */
export function chainList(ids: readonly ChainId[]): string {
  const names = ids.map(chainLabel)
  if (names.length <= 1) return names[0] ?? ''
  return `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`
}

/** Deduplicate and order a set of chains, so two sentences on the same page
 *  never name the same pair in a different order. */
export function orderChains(ids: readonly ChainId[]): ChainId[] {
  return CHAIN_IDS.filter((id) => ids.includes(id))
}

/** Which chains a trade settles on. A ZTS↔ZTS offer answers `['znn']`, which is
 *  correct: one proof covers both its legs. */
export function chainsForPost(post: BoardPost): ChainId[] {
  return orderChains([post.give?.chain, post.want?.chain].filter(Boolean) as ChainId[])
}

/** What is still unproven of what a trade needs. */
export function missingChains(need: readonly ChainId[], proven: readonly ChainId[]): ChainId[] {
  return CHAIN_IDS.filter((id) => need.includes(id) && !proven.includes(id))
}

/** A trade this build can post. Mirrors `Pairs` in wasm/chains.go, which is what
 *  actually refuses one; this list decides which buttons exist. */
export interface Pair {
  id: string
  label: string
  a: ChainId
  b: ChainId
  chains: ChainId[]
}

export const PAIRS: readonly Pair[] = [
  {id: 'btc-znn', label: 'BTC ⇄ ZTS', a: 'btc', b: 'znn', chains: ['btc', 'znn']},
  {id: 'znn-znn', label: 'ZTS ⇄ ZTS', a: 'znn', b: 'znn', chains: ['znn']},
  {id: 'sol-znn', label: 'SOL ⇄ ZTS', a: 'sol', b: 'znn', chains: ['sol', 'znn']},
  {id: 'sol-btc', label: 'SOL ⇄ BTC', a: 'sol', b: 'btc', chains: ['sol', 'btc']},
]

export function pairFor(id: string): Pair | undefined {
  return PAIRS.find((p) => p.id === id)
}

/** The stable name of a trade, in the order PAIRS declares it. */
export function pairId(a: ChainId, b: ChainId): string {
  const found = PAIRS.find((p) => (p.a === a && p.b === b) || (p.a === b && p.b === a))
  return found ? found.id : `${a}-${b}`
}

export function pairLabel(a: ChainId, b: ChainId): string {
  return pairFor(pairId(a, b))?.label ?? `${chainLabel(a)} ⇄ ${chainLabel(b)}`
}

/** The pairs somebody holding these proofs may post. Empty means the Post button
 *  has nothing to offer yet, which is a sentence rather than a dead control. */
export function postablePairs(proven: readonly ChainId[]): Pair[] {
  return PAIRS.filter((p) => p.chains.every((c) => proven.includes(c)))
}

/** The two well-known Zenon tokens, so a ZTS↔ZTS form has something to offer
 *  before anybody types a standard. Any other ZTS is typed in full. */
export const KNOWN_TOKENS: readonly {zts: string; name: string}[] = [
  {zts: 'zts1znnxxxxxxxxxxxxx9z4ulx', name: 'ZNN'},
  {zts: 'zts1qsrxxxxxxxxxxxxxmrhjll', name: 'QSR'},
]

/** How a ZTS reads in a row. Long standards are shortened rather than wrapped:
 *  a board of 26-character identifiers is a board nobody reads. */
export function tokenName(zts?: string): string {
  const t = (zts ?? '').trim() || KNOWN_TOKENS[0].zts
  const known = KNOWN_TOKENS.find((k) => k.zts === t)
  if (known) return known.name
  return t.length > 12 ? `${t.slice(0, 8)}…${t.slice(-4)}` : t
}

/** One leg of an offer, as a row says it: "10 ZNN", "400000 sat", "1.5 SOL". */
export function legLabel(leg?: PostLeg | {chain: ChainId; token?: string; amount: string}): string {
  if (!leg) return ''
  const amount = (leg.amount ?? '').trim()
  switch (leg.chain) {
    case 'btc':
      return `${amount} sat`
    case 'sol':
      return `${amount} SOL`
    case 'znn':
      return `${amount} ${tokenName(leg.token)}`
  }
  return amount
}

/** The unit a leg's amount is typed in, for a field's suffix. */
export function unitFor(chain: ChainId, token?: string): string {
  return chain === 'znn' ? tokenName(token) : chainFor(chain).unit
}

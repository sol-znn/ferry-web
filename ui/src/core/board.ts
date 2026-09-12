import type {BoardPost, Listing, PostLeg, ProofScheme} from '@/types'
import {chainsForPost, legLabel, pairId, unitFor, type ChainId} from '@/core/chains'

/**
 * The board's arithmetic and its sorting, kept out of the components.
 *
 * None of it is trusted with anything. Go decides what a post SAYS — it verifies
 * the signature, re-checks the proofs and refuses a post whose terms do not
 * cohere — and this file decides how a verified post is ordered and displayed.
 * The split matters because everything here is derived: a rate is a division,
 * not a term, and nothing downstream may ever treat one as agreed.
 *
 * v1 could speak in one direction — "buying ZNN" or "selling it" — because every
 * offer had the same two legs. With four pairs there is no asset every row has
 * in common, so a row says what the AUTHOR gives and gets, and the reader's own
 * side is the mirror of it. That is one flip rather than a vocabulary, and it is
 * done once here.
 */

/** What the viewer would fund if they took this post: the author's `want`. */
export function takerGives(post: BoardPost): PostLeg {
  return post.want
}

/** And what they would receive: the author's `give`. */
export function takerGets(post: BoardPost): PostLeg {
  return post.give
}

/** The pair a post trades, as the filter bar names it. */
export function pairOf(post: BoardPost): string {
  return pairId(post.give.chain, post.want.chain)
}

/**
 * The rate, as units of `want` per unit of `give`.
 *
 * Derived rather than carried. A rate in the post would be a third number that
 * could disagree with the two it is computed from, and the two are what the
 * trade is actually in — so this is the only place it exists, and it is never
 * sent anywhere.
 *
 * NaN for a post whose amounts cannot be divided, which the callers show as an
 * em dash rather than as a number. A post like that would already have been
 * refused by Go's validation; this is what stops one slipping through as
 * `Infinity` and sorting to the top.
 *
 * Comparable only WITHIN a pair, which is why the board refuses to sort by rate
 * until one is chosen: "0.004" means satoshi per ZNN in one row and SOL per ZNN
 * in the next, and a column that sorts those together is worse than no column.
 */
export function rate(post: BoardPost): number {
  const give = Number(post.give.amount)
  const want = Number(post.want.amount)
  if (!Number.isFinite(give) || !Number.isFinite(want) || give <= 0 || want <= 0) return NaN
  return want / give
}

/** The same rate the other way up. */
export function inverseRate(post: BoardPost): number {
  const r = rate(post)
  return Number.isFinite(r) && r > 0 ? 1 / r : NaN
}

/** What a rate is IN, for the label beside it: "ZNN per sat". */
export function rateUnits(post: BoardPost): string {
  return `${unitFor(post.want.chain, post.want.token)} per ${unitFor(post.give.chain, post.give.token)}`
}

/** A rate as a string, at a precision that does not imply more than it knows. */
export function formatRate(value: number): string {
  if (!Number.isFinite(value)) return '—'
  if (value >= 1000) return Math.round(value).toLocaleString('en-US')
  if (value >= 1) return value.toFixed(2)
  return value.toPrecision(3)
}

/** Both halves of a trade as one line: "400000 sat → 10 ZNN". */
export function tradeLabel(post: BoardPost): string {
  return `${legLabel(post.give)} → ${legLabel(post.want)}`
}

/** The size band as a sentence, or '' when the post trades one amount only.
 *  Always about the leg the author FUNDS: a band on both halves would be a band
 *  on the rate, which Go refuses. */
export function bandLabel(post: BoardPost): string {
  const {min, max, chain, token} = post.give
  const unit = unitFor(chain, token)
  if (!min && !max) return ''
  if (min && max) return `${min} – ${max} ${unit}`
  if (min) return `from ${min} ${unit}`
  return `up to ${max} ${unit}`
}

/** Amounts compare as numbers, not as strings: "10", "10.0" and "010.00" are one
 *  size typed three ways. NaN for anything that will not parse, which every
 *  caller treats as "no opinion" rather than as zero. */
export function amountOf(value?: string): number {
  const n = Number((value ?? '').trim())
  return Number.isFinite(n) ? n : NaN
}

/** Whether an amount is one this post will trade. Used to stop a take being sent
 *  for a size the author already said they will not do — which would otherwise
 *  be discovered in conversation, one round trip later. */
export function fitsBand(post: BoardPost, amount: number): boolean {
  if (!Number.isFinite(amount) || amount <= 0) return false
  const min = amountOf(post.give.min)
  const max = amountOf(post.give.max)
  const headline = amountOf(post.give.amount)
  if (!Number.isFinite(min) && !Number.isFinite(max)) return amount === headline
  if (Number.isFinite(min) && amount < min) return false
  if (Number.isFinite(max) && amount > max) return false
  return true
}

/** Seconds left before a post expires. Negative once it has. */
export function secondsLeft(post: BoardPost, now = Date.now()): number {
  return post.expiresAt - Math.floor(now / 1000)
}

/** "6h 12m" / "4m" / "expired" — the countdown a row carries. Deliberately
 *  coarse above an hour: a board where every row ticks by the second is a board
 *  that is hard to read. */
export function timeLeft(post: BoardPost, now = Date.now()): string {
  const left = secondsLeft(post, now)
  if (left <= 0) return 'expired'
  const mins = Math.floor(left / 60)
  if (mins < 60) return `${mins}m`
  const hours = Math.floor(mins / 60)
  if (hours < 48) return `${hours}h ${mins % 60}m`
  return `${Math.floor(hours / 24)}d`
}

/** Under an hour left. The row says so, because an offer that will be gone
 *  before a swap can be set up is worth knowing about before you take it. */
export const EXPIRING_SOON_SECONDS = 3600

/** `a1b2…9f8e` — a board key short enough to sit in a row. It is the only name
 *  anybody has here, so it is shown rather than hidden behind an avatar. */
export function shortKey(pubkey: string): string {
  return pubkey.length > 16 ? `${pubkey.slice(0, 6)}…${pubkey.slice(-4)}` : pubkey
}

export function hasProof(listing: Listing, scheme: ProofScheme): boolean {
  return Boolean(listing.verified?.includes(scheme))
}

/** Every address a post advertises, as pairs, for the row's detail panel. */
export function postAddresses(post: BoardPost): {chain: ChainId; addr: string}[] {
  return chainsForPost(post)
    .map((chain) => ({chain, addr: post.addrs?.[chain] ?? ''}))
    .filter((a) => Boolean(a.addr))
}

// ---------- filtering and sorting ----------

export type SortKey = 'rate' | 'newest' | 'expiring' | 'size'

export interface BoardFilters {
  /** Which trade the viewer wants. 'any' shows every pair. */
  pair: string | 'any'
  /** Which chain the viewer would FUND, within that pair. 'any' shows both
   *  sides. It replaces v1's buy/sell, which named an asset that no longer
   *  appears in every row. */
  giving: ChainId | 'any'
  /** Only posts whose author has proven a wallet address. */
  verifiedOnly: boolean
  /** Free text over the note, the author key and the advertised addresses. */
  search: string
  sort: SortKey
}

export const DEFAULT_FILTERS: BoardFilters = {
  pair: 'any',
  giving: 'any',
  verifiedOnly: false,
  search: '',
  sort: 'newest',
}

/** Apply the filter bar. Expiry and status are NOT decided here — see useBoard,
 *  which drops dead posts before anything gets this far. */
export function applyFilters(listings: Listing[], f: BoardFilters): Listing[] {
  const needle = f.search.trim().toLowerCase()
  return listings.filter((l) => {
    if (f.pair !== 'any' && pairOf(l.post) !== f.pair) return false
    // The viewer funds the author's `want`, so a filter on what the VIEWER
    // gives is a filter on that leg's chain.
    if (f.giving !== 'any' && l.post.want.chain !== f.giving) return false
    if (f.verifiedOnly && !(l.verified?.length ?? 0)) return false
    if (needle) {
      const addrs = postAddresses(l.post)
        .map((a) => a.addr)
        .join(' ')
      const hay = `${l.post.note ?? ''} ${l.author} ${addrs}`
      if (!hay.toLowerCase().includes(needle)) return false
    }
    return true
  })
}

/**
 * Order the board.
 *
 * "Best rate" is the only sort that has to know what it is comparing, and it is
 * the one people reach for. Two things make it meaningless across a mixed list:
 * a rate in one pair is in different units from a rate in another, and within one
 * pair the two directions want opposite ends of the same number — a viewer
 * funding satoshi wants the most tokens per satoshi, one funding tokens wants
 * the fewest.
 *
 * So it sorts by rate only once BOTH are chosen, and falls back to newest
 * otherwise rather than to a number that means something different in adjacent
 * rows.
 */
export function sortListings(listings: Listing[], f: BoardFilters): Listing[] {
  const out = [...listings]
  switch (f.sort) {
    case 'rate': {
      if (f.pair === 'any' || f.giving === 'any') {
        return sortListings(out, {...f, sort: 'newest'})
      }
      out.sort((a, b) => {
        const ra = rate(a.post)
        const rb = rate(b.post)
        // A post whose rate cannot be computed sorts last either way rather
        // than winning by being NaN.
        if (!Number.isFinite(ra)) return 1
        if (!Number.isFinite(rb)) return -1
        // The viewer funds `want`, so a bigger `give` per unit of `want` is a
        // better deal for them — which is a SMALLER want-per-give rate.
        return ra - rb
      })
      break
    }
    case 'newest':
      out.sort((a, b) => b.post.createdAt - a.post.createdAt)
      break
    case 'expiring':
      out.sort((a, b) => a.post.expiresAt - b.post.expiresAt)
      break
    case 'size':
      // Only meaningful inside one pair, for the same reason as rate. Across a
      // mixed board this orders numbers in different units, which is why the
      // control offering it is disabled until a pair is chosen.
      out.sort((a, b) => amountOf(b.post.give.amount) - amountOf(a.post.give.amount))
      break
  }
  return out
}

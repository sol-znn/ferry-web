import type {BoardPost, Leg, Listing, ProofScheme} from '@/types'

/**
 * The board's arithmetic and its sorting, kept out of the components.
 *
 * None of it is trusted with anything. Go decides what a post SAYS -- it
 * verifies the signature, re-checks the proofs and refuses a post whose terms do
 * not cohere -- and this file decides how a verified post is ordered and
 * displayed. The split matters because everything here is derived: a rate is a
 * division, not a term, and nothing downstream may ever treat one as agreed.
 */

/** What the viewer does, rather than what the author does.
 *
 *  Every author-facing field on a post is written from the author's side, and a
 *  reader who forgets to flip one reads every offer backwards. So the flip
 *  happens once, here, and the components only ever see the viewer's side. */
export type Direction = 'buy-znn' | 'sell-znn'

/** Which way a post looks to somebody reading it. The author's `side` is what
 *  THEY do with Bitcoin, so a reader taking a post where the author sends BTC is
 *  a reader who receives BTC and pays ZNN — selling ZNN. */
export function directionFor(post: BoardPost): Direction {
  return post.side === 'send' ? 'sell-znn' : 'buy-znn'
}

/** The leg the viewer takes if they take this post. */
export function takerLeg(post: BoardPost): Leg {
  return post.side === 'send' ? 'receive' : 'send'
}

/**
 * The rate, in ZNN per BTC.
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
 */
export function rate(post: BoardPost): number {
  const znn = Number(post.zenonAmt)
  const btc = post.amountSats / 1e8
  if (!Number.isFinite(znn) || !Number.isFinite(btc) || btc <= 0 || znn <= 0) return NaN
  return znn / btc
}

/** The same rate the other way up: satoshis per ZNN, which is the number small
 *  trades are easier to think in. */
export function satsPerZnn(post: BoardPost): number {
  const znn = Number(post.zenonAmt)
  if (!Number.isFinite(znn) || znn <= 0) return NaN
  return post.amountSats / znn
}

/** A rate as a string, at a precision that does not imply more than it knows. */
export function formatRate(value: number): string {
  if (!Number.isFinite(value)) return '—'
  if (value >= 1000) return Math.round(value).toLocaleString('en-US')
  if (value >= 1) return value.toFixed(2)
  return value.toPrecision(3)
}

/** The size band as a sentence, or '' when the post trades one amount only. */
export function bandLabel(post: BoardPost): string {
  const min = post.minSats ?? 0
  const max = post.maxSats ?? 0
  if (!min && !max) return ''
  const btc = (n: number) => (n / 1e8).toFixed(8).replace(/0+$/, '').replace(/\.$/, '')
  if (min && max) return `${btc(min)} – ${btc(max)} BTC`
  if (min) return `from ${btc(min)} BTC`
  return `up to ${btc(max)} BTC`
}

/** Whether an amount is one this post will trade. Used to stop a take being
 *  sent for a size the author already said they will not do — which would
 *  otherwise be discovered in conversation, one round trip later. */
export function fitsBand(post: BoardPost, sats: number): boolean {
  if (!Number.isFinite(sats) || sats <= 0) return false
  if (!post.minSats && !post.maxSats) return sats === post.amountSats
  if (post.minSats && sats < post.minSats) return false
  if (post.maxSats && sats > post.maxSats) return false
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

// ---------- filtering and sorting ----------

export type SortKey = 'rate' | 'newest' | 'expiring' | 'size'

export interface BoardFilters {
  /** Which direction the VIEWER wants. 'any' shows both sides. */
  direction: Direction | 'any'
  /** Minimum and maximum Bitcoin size, in satoshis. Zero means unbounded. */
  minSats: number
  maxSats: number
  /** Only posts whose author has proven a wallet address. */
  verifiedOnly: boolean
  /** Free text over the note and the author key. */
  search: string
  sort: SortKey
}

export const DEFAULT_FILTERS: BoardFilters = {
  direction: 'any',
  minSats: 0,
  maxSats: 0,
  verifiedOnly: false,
  search: '',
  sort: 'rate',
}

/**
 * Whether a size filter overlaps what a post will actually trade.
 *
 * Overlap rather than containment, which is the difference between a useful
 * filter and one that hides everything: a post offering 0.001–0.1 BTC should
 * appear for somebody looking to do 0.01, even though neither end of its band
 * equals theirs.
 */
function sizeOverlaps(post: BoardPost, min: number, max: number): boolean {
  const low = post.minSats || post.amountSats
  const high = post.maxSats || post.amountSats
  if (min > 0 && high < min) return false
  if (max > 0 && low > max) return false
  return true
}

/** Apply the filter bar. Expiry and status are NOT decided here — see
 *  useBoard, which drops dead posts before anything gets this far. */
export function applyFilters(listings: Listing[], f: BoardFilters): Listing[] {
  const needle = f.search.trim().toLowerCase()
  return listings.filter((l) => {
    if (f.direction !== 'any' && directionFor(l.post) !== f.direction) return false
    if (!sizeOverlaps(l.post, f.minSats, f.maxSats)) return false
    if (f.verifiedOnly && !(l.verified?.length ?? 0)) return false
    if (needle) {
      const hay = `${l.post.note ?? ''} ${l.author} ${l.post.btcAddr ?? ''} ${l.post.znnAddr ?? ''}`
      if (!hay.toLowerCase().includes(needle)) return false
    }
    return true
  })
}

/**
 * Order the board.
 *
 * "Best rate" is the only sort that has to know which way round it is, and it is
 * the one people reach for: a viewer buying ZNN wants the most ZNN per BTC, and
 * one selling wants the fewest. Sorting both the same way would put the worst
 * offers at the top for half the users, silently.
 *
 * With no direction chosen there is no "best" to compute, because the two halves
 * of the list are not comparable — so a mixed board sorts by newest instead
 * rather than by a number that means opposite things in adjacent rows.
 */
export function sortListings(listings: Listing[], f: BoardFilters): Listing[] {
  const out = [...listings]
  switch (f.sort) {
    case 'rate': {
      if (f.direction === 'any') return sortListings(out, {...f, sort: 'newest'})
      const better = f.direction === 'buy-znn' ? -1 : 1
      out.sort((a, b) => {
        const ra = rate(a.post)
        const rb = rate(b.post)
        // A post whose rate cannot be computed sorts last either way rather
        // than winning by being NaN.
        if (!Number.isFinite(ra)) return 1
        if (!Number.isFinite(rb)) return -1
        return (ra - rb) * better
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
      out.sort((a, b) => b.post.amountSats - a.post.amountSats)
      break
  }
  return out
}

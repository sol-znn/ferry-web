import type {LegView, Swap} from '@/types'
import {chainLabel} from '@/core/chains'

/**
 * Where a swap is, and what to do about it. Both answers live in one file
 * because they are the same walk down the same facts, and a card that says
 * "Fund your leg" while its track lights up "Claim" is worse than either alone.
 *
 * v1 walked the Bitcoin leg and read the Zenon one off the side. This walks the
 * two legs by what they DO for this user — the leg you fund, and the leg you
 * claim — which is the same walk with the chains taken out of it, and is why one
 * function now covers four pairs.
 */

/** The five moves of a swap, in the words a card can afford to print. */
export const STAGES = ['Set up', 'Fund', 'Their leg', 'Claim', 'Done'] as const

/** Whether a leg is described well enough to be acted on at all. */
function describable(l: LegView | undefined): boolean {
  if (!l) return false
  if (l.chain === 'btc') return Boolean(l.btc?.contractAddr)
  return Boolean(l.selfAddr && l.peerAddr)
}

/** Index into STAGES. Mirrors nextStep below, branch for branch. */
export function stage(sw: Swap): number {
  if (sw.settled) return 4
  if (!describable(sw.out) && !describable(sw.in)) return 0
  if (sw.out?.done || sw.in?.done) return 3
  if (!sw.out?.funded) return 1
  if (!sw.in?.funded || !sw.in?.verified) return 2
  return 3
}

/**
 * What the user has to do next, in a few words. It replaces reading the state
 * name and working it out, and it is the one line the card always shows.
 */
export function nextStep(sw: Swap): string {
  const out = sw.out
  const inn = sw.in
  if (!out || !inn) return 'This record is incomplete'

  if (sw.settled) return sw.archived ? 'Settled' : 'Settled — archive it'

  // Setting up. A Bitcoin leg needs a contract before anything can happen, and
  // which side owes what depends on who funds it.
  if (!describable(out)) {
    if (out.chain === 'btc') return 'Waiting for their pubkey hash so the contract can be built'
    return `Add their ${chainLabel(out.chain)} address so your leg can be created`
  }
  if (!describable(inn)) {
    if (inn.chain === 'btc') return 'Send them your pubkey hash, then wait for their contract'
    return `Waiting for their ${chainLabel(inn.chain)} address`
  }

  // A leg that stopped moving because THIS user took it back is the end of the
  // swap, not the start of a claim. Checked first: `done` covers both exits,
  // and reading a refund as a claim tells the person who just reclaimed their
  // own money that a preimage they invented is now public, and points them at
  // a counterparty leg that was never funded.
  if (out.reclaimed && !inn.funded) {
    return `You took your ${chainLabel(out.chain)} back — they never funded theirs`
  }

  // Your leg has been claimed but theirs has not: the counterparty holds the
  // preimage and the rest is theirs. Said before the funding branches, because
  // it outlives them.
  if (out.done && !inn.done) {
    return inn.expired
      ? `Their ${chainLabel(inn.chain)} leg expired — reclaim is theirs, nothing is owed to you`
      : `Claim your ${chainLabel(inn.chain)} — the preimage is public now`
  }
  if (inn.done && !out.done) {
    return 'Waiting for them to claim your leg'
  }

  if (out.expired && out.funded) {
    return `Deadline passed — take your ${chainLabel(out.chain)} back`
  }

  if (!out.funded) {
    // The initiator funds first; the participant waits for a verified leg.
    if (sw.outIsInitiators) return `Fund your ${chainLabel(out.chain)} leg`
    if (!inn.funded) return `Waiting for their ${chainLabel(inn.chain)} leg`
    if (!inn.verified) return `Check their ${chainLabel(inn.chain)} leg, then fund yours`
    return `Fund your ${chainLabel(out.chain)} leg`
  }

  if (!inn.funded) return `Waiting for their ${chainLabel(inn.chain)} leg`
  if (!inn.verified) return `Their ${chainLabel(inn.chain)} leg has not passed verification`
  if (!sw.secretHex) return 'Waiting on the preimage'
  return `Claim your ${chainLabel(inn.chain)}`
}

/**
 * Whether the next step is this user's move or the counterparty's. A card
 * waiting on somebody else should not look like a card demanding something.
 *
 * Walked out of the swap rather than read off nextStep's wording: matching the
 * word "Waiting" would break silently the first time one of those sentences is
 * reworded, into a card that shouts about a thing you cannot do.
 */
export function waitingOnThem(sw: Swap): boolean {
  const out = sw.out
  const inn = sw.in
  if (!out || !inn || sw.settled) return false

  if (!describable(out)) return out.chain === 'btc'
  if (!describable(inn)) return inn.chain !== 'btc'

  if (out.done && !inn.done) return Boolean(inn.expired)
  if (inn.done && !out.done) return true

  if (out.expired && out.funded) return false
  if (!out.funded) return !sw.outIsInitiators && (!inn.funded || !inn.verified)
  if (!inn.funded) return true
  // A leg that exists and failed verification is this user's problem to act on,
  // not something to wait out.
  if (!inn.verified) return false
  return !sw.secretHex
}

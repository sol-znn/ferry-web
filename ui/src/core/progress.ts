import type {Swap} from '@/types'

/**
 * Where a swap is, and what to do about it. Both answers live in one file
 * because they are the same walk down the same facts, and a card that says
 * "Fund the contract" while its track lights up "Claim" is worse than either
 * alone.
 */

/** The five moves of a swap, in the words a card can afford to print. */
export const STAGES = ['Set up', 'Fund', 'Zenon leg', 'Claim', 'Done'] as const

/** Index into STAGES. Mirrors nextStep below, branch for branch. */
export function stage(sw: Swap): number {
  if (!sw.contractAddr) return 0
  // `settled` rather than `!active` — a swap no longer leaves the active list
  // on its own, so "gone from the page" stopped being a usable stand-in for
  // "done". The distinction is the participant who funded the Bitcoin leg and
  // has yet to collect their ZNN: still claiming, not finished.
  if (sw.state === 'redeemed') return sw.settled ? 4 : 3
  if (sw.state === 'refunded' || (sw.state === 'expired' && sw.archived)) return 4
  if (!sw.funding) return 1
  // Held for a short funding: the contract has money in it, but not the
  // agreed amount, and the redeem is withheld until a single output covers it.
  // That is still the funding step, whatever the card's other facts say.
  if (sw.redeemHeldForShortFunding) return 1
  if (sw.refundable) return 3
  if (sw.leg === 'receive') return sw.secretHex ? 3 : 2
  if (!sw.zenon?.htlcId || !sw.zenon.verified) return 2
  return 3
}

/**
 * What the user has to do next, in a few words. It replaces reading the state
 * name and working it out, and it is the one line the card always shows.
 */
export function nextStep(sw: Swap): string {
  if (!sw.contractAddr) {
    // The receiving side owes the pubkey hash before the other side can build
    // anything, so this is their move and not a wait. Saying "waiting for their
    // contract" here described the step AFTER the one outstanding, and put a
    // "their move" label on the swap's very first hand-off.
    return sw.leg === 'send'
      ? 'Waiting for their pubkey hash'
      : 'Send them your pubkey hash, then wait for their contract'
  }
  if (sw.state === 'redeemed') {
    // The claim this is about is the Zenon one, and it may already be done: a
    // contract redeemed by the counterparty leaves this user collecting ZNN,
    // and once they have collected it there is nothing here but the filing.
    if (!sw.settled) return 'Claim your ZNN on Zenon, then archive'
    return sw.archived ? 'Settled' : 'Settled — archive it'
  }
  if (sw.state === 'refunded') return sw.archived ? 'Refunded' : 'Refunded — archive it'
  if (!sw.funding) {
    return sw.leg === 'send' ? 'Fund the contract' : 'Waiting for them to fund it'
  }
  // The button this would name is withheld, so naming it would send the user
  // looking for something that is not there. What they are waiting for is a
  // single output that covers the agreed amount; several short ones do not add.
  if (sw.redeemHeldForShortFunding) return 'Funded short — waiting for a payment of the full amount'
  if (sw.refundable) return 'Timelock passed — refund available'
  if (sw.leg === 'receive') {
    return sw.secretHex ? 'Redeem the Bitcoin' : 'Waiting on the preimage'
  }
  if (!sw.zenon?.htlcId) return 'Waiting on their Zenon HTLC'
  if (!sw.zenon?.verified) return 'Their Zenon HTLC has not passed verification'
  // Already unlocked: the ZNN is this user's and the preimage they needed to
  // publish is public. Everything after this belongs to the counterparty, who
  // reads it off Zenon and takes the Bitcoin. Saying "unlock the HTLC" here —
  // which is what this said until the unlock was recorded — told somebody who
  // had just finished their last move to go and do it again.
  if (sw.zenon.unlockHash) return 'Waiting for them to take the Bitcoin — your ZNN is collected'
  return 'Unlock the Zenon HTLC to take your ZNN'
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
  // Before a contract exists, only the funding side is genuinely waiting. The
  // receiving side is holding the value that unblocks it.
  if (!sw.contractAddr) return sw.leg === 'send'
  if (sw.state === 'redeemed' || sw.state === 'refunded') return false
  if (!sw.funding) return sw.leg === 'receive'
  // Kept in step with nextStep: a held short funding is theirs to put right.
  if (sw.redeemHeldForShortFunding) return true
  if (sw.refundable) return false
  // An HTLC that exists and failed verification is this user's problem to act
  // on, not something to wait out — so only a missing one counts as waiting.
  if (sw.leg === 'receive') return !sw.secretHex
  // Unlocked: this side is done and the next move is theirs. Kept in step with
  // nextStep above, which is the entire reason these two live in one file.
  return !sw.zenon?.htlcId || Boolean(sw.zenon.unlockHash)
}

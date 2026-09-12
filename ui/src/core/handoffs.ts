import type {Swap} from '@/types'

/**
 * What this browser knows that the other side needs, and cannot work out alone.
 *
 * A swap is two half-records that have to agree, and every disagreement between
 * them is somebody waiting. Each value below is public, each is checked by the
 * side receiving it, and each is knowable by exactly one browser until it is
 * sent. So whoever has one publishes it, and neither side is ever the only one
 * holding a fact about the swap.
 *
 * The preimage is not on this list and there is no entry for it. It is not a
 * hand-off at all: revealing it hands the counterparty both legs, so it has no
 * field on a session message and no path that would send or accept one. It
 * travels by being spent — into a Bitcoin witness, a Zenon unlock or a Solana
 * redeem — where the other side reads it off the chain that already enforced it.
 *
 * Which side owes which value is not cosmetic. Go refuses each of these on the
 * wrong leg, so a browser that published everything it held would fill the
 * counterparty's transcript with REFUSED lines about values they never asked
 * for — indistinguishable, from the reading end, from malformed data.
 *
 * The value is carried here only to tell one hand-off from the next: what
 * travels is read out of the stored swap in Go (fillFromSwap in
 * wasm/session_api.go), so a message describes what this browser holds rather
 * than what this page happens to be rendering.
 */

export type HandoffType = 'pkh' | 'contract' | 'zenon' | 'solana' | 'funded'

export interface Handoff {
  type: HandoffType
  /** Identifies this hand-off, so a changed value is a new send and an
   *  unchanged one is not sent twice. Never published — see above. */
  value: string
  /** What the transcript says when it goes. */
  describe: string
}

/** Everything this side owes the other right now, in the order it comes due. */
export function owed(sw: Swap): Handoff[] {
  const out: Handoff[] = []

  // The pubkey hash, from the side RECEIVING Bitcoin. It names the key allowed
  // to take the redeem branch, and the funder cannot build the contract without
  // it. It travels one way because the funder's own hash is already inside the
  // contract they send back.
  if (sw.in?.chain === 'btc' && sw.in.btc?.key?.pkhHex) {
    out.push({type: 'pkh', value: sw.in.btc.key.pkhHex, describe: 'Sent your pubkey hash'})
  }

  // The contract, from the side that built it. The receiver audits it — checks
  // it really is redeemable by them, and that its locktime sits on the safe side
  // of their own leg — and will not act until they have.
  if (sw.out?.chain === 'btc' && sw.out.btc?.contractHex) {
    out.push({type: 'contract', value: sw.out.btc.contractHex, describe: 'Sent the contract'})
  }

  // The Zenon HTLC id, from whoever created that leg, once THIS browser has
  // checked it against a node.
  //
  // Verified rather than merely recorded: a create is unreadable for a momentum
  // or two after the wallet publishes it, so an id sent in that window arrives at
  // a counterparty whose node cannot see it either, and their verification
  // refuses it — a red REFUSED line about an HTLC that is simply young.
  if (sw.out?.chain === 'znn' && sw.out.znn?.htlcId && sw.out.znn.verified) {
    out.push({type: 'zenon', value: sw.out.znn.htlcId, describe: 'Sent your Zenon HTLC id'})
  }

  // The Solana escrow's coordinates. Unlike an HTLC id these are knowable before
  // anything is funded — both sides derive the same PDA from the program and the
  // swap id — so this goes as soon as the leg exists. What it prevents is the
  // silent failure: two sides watching two different accounts, each reporting
  // the other's leg as simply not funded yet.
  if (sw.out?.chain === 'sol' && sw.out.sol?.swapId) {
    out.push({
      type: 'solana',
      value: `${sw.out.sol.programId ?? ''}:${sw.out.sol.swapId}`,
      describe: 'Sent your Solana escrow details',
    })
  }

  // The funding, from the side that paid a Bitcoin contract, once a node has
  // actually listed the output. A broadcast is not funding — it can be dropped
  // or replaced — and announcing one would tell the other side to start their
  // leg against a payment that may never have happened.
  //
  // Bitcoin only: the other two chains announce themselves. A Zenon entry exists
  // by having an id, and a Solana escrow by being at an address both sides
  // already derived, so on those "it is funded" is something the counterparty
  // reads rather than something to be told.
  if (sw.out?.chain === 'btc' && sw.out.btc?.funding) {
    out.push({
      type: 'funded',
      value: `${sw.out.btc.funding.txid}:${sw.out.btc.funding.vout}`,
      describe: 'Told them the contract is funded',
    })
  }

  return out
}

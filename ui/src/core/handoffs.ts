import type {Swap} from '@/types'

/**
 * What this browser knows that the other side needs, and cannot work out alone.
 *
 * A swap is two half-records that have to agree, and every disagreement between
 * them is somebody waiting. The four values below are the whole of what has to
 * cross: each is public, each is checked by the side receiving it, and each is
 * knowable by exactly one browser until it is sent. So whoever has one publishes
 * it, and neither side is ever the only one holding a fact about the swap.
 *
 * The preimage is not on this list and there is no fifth entry. It is not a
 * hand-off at all: revealing it hands the counterparty both legs, so it has no
 * field on a session message and no path that would send or accept one. It
 * travels by being spent -- into a Bitcoin witness or a Zenon unlock -- where
 * the other side reads it off the chain that already enforced it.
 *
 * Which side owes which value is not cosmetic. Go refuses each of these on the
 * wrong leg, so a browser that published everything it held would fill the
 * counterparty's transcript with REFUSED lines about values they never asked
 * for -- indistinguishable, from the reading end, from malformed data.
 *
 * The value is carried here only to tell one hand-off from the next: what
 * travels is read out of the stored swap in Go (fillFromSwap in
 * wasm/session_api.go), so a message describes what this browser holds rather
 * than what this page happens to be rendering.
 */

export type HandoffType = 'pkh' | 'contract' | 'zenon' | 'funded'

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

  // The pubkey hash, from the receiving side only. It names the key allowed to
  // take the redeem branch, and the funder cannot build the contract without
  // it. It travels one way because the funder's own hash is already inside the
  // contract they send back.
  if (sw.leg === 'receive' && sw.key?.pkhHex) {
    out.push({type: 'pkh', value: sw.key.pkhHex, describe: 'Sent your pubkey hash'})
  }

  // The contract, from the side that built it. The receiver audits it — checks
  // it really is redeemable by them, and that its locktime sits on the safe
  // side of the Zenon leg — and will not act until they have.
  if (sw.leg === 'send' && sw.contractHex) {
    out.push({type: 'contract', value: sw.contractHex, describe: 'Sent the contract'})
  }

  // The Zenon HTLC id, from whoever created that leg, once THIS browser has
  // checked it against a node.
  //
  // Verified rather than merely recorded: a create is unreadable for a momentum
  // or two after the wallet publishes it, so an id sent in that window arrives at
  // a counterparty whose node cannot see it either, and their verification
  // refuses it -- a red REFUSED line about an HTLC that is simply young.
  if (sw.zenonHtlcIsOurs && sw.zenon?.htlcId && sw.zenon.verified) {
    out.push({type: 'zenon', value: sw.zenon.htlcId, describe: 'Sent your Zenon HTLC id'})
  }

  // The funding, from the side that paid it, once a node has actually listed
  // the output. A broadcast is not funding — it can be dropped or replaced —
  // and announcing one would tell the other side to start their leg against a
  // payment that may never have happened.
  if (sw.leg === 'send' && sw.funding) {
    out.push({
      type: 'funded',
      value: `${sw.funding.txid}:${sw.funding.vout}`,
      describe: 'Told them the contract is funded',
    })
  }

  return out
}

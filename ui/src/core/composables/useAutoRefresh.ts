import {api} from '@/core/api'
import {useAutoMode} from './useAutoMode'
import {useFerry} from './useFerry'
import {useSettings} from './useSettings'
import type {Swap} from '@/types'

/**
 * Press Refresh from chain for the user, on a timer.
 *
 * Everything a swap waits for happens somewhere this page cannot see, and until
 * something asks a node the card goes on saying what it said an hour ago. The
 * button has always been there; the problem is that needing to press it is
 * knowledge the user does not have -- a swap that has silently moved on and one
 * that is genuinely stuck look identical from the sofa.
 *
 * Two conditions are checked each round, because there are two ways a swap sits
 * still:
 *
 *   - The Bitcoin side, through Refresh. It finds the funding, counts its depth,
 *     pre-signs the refund the moment there is an output to spend, and reads a
 *     counterparty's preimage out of their redeem.
 *   - The Zenon side, when a verdict is pending. A create is unreadable for a
 *     momentum or two after a wallet publishes it. A REFUSED verdict is not
 *     retried: that is an answer.
 *
 * What it does not do is act. Nothing here signs, spends, funds or unlocks --
 * this only makes sure the card offering those is describing the chain as it is
 * now.
 */

/**
 * Long enough that a page left open overnight is not a load on somebody's
 * Esplora, short enough that a confirmation shows up while you are still looking
 * at the card.
 */
const INTERVAL_MS = 60_000

let timer: number | null = null
/** A round still in flight. A slow or unreachable node must not let ticks
 *  stack up into a queue of duplicate requests. */
let running = false

const {swaps, view, loadSwaps} = useFerry()
const {body: settings, stored, hasZenon} = useSettings()
const {isOn} = useAutoMode()

/**
 * Whether a node can say anything about this swap at all. A swap on another
 * network is excluded for the same reason every chain-touching button on its
 * card is disabled: the lookup would go to the wrong chain. A swap with no
 * contract has nothing on chain to look at.
 */
function onChain(sw: Swap): boolean {
  // A leg that is describable enough for a node to be asked about: a Bitcoin
  // contract that exists, an entry or an escrow either side has named.
  const readable = (l: Swap['out'] | undefined) =>
    Boolean(l && (l.btc?.contractAddr || l.znn?.htlcId || l.sol?.escrow || l.funded))
  return (readable(sw.out) || readable(sw.in)) && sw.network === stored.value.network
}

/**
 * Whether this swap is worth asking a node about on the heartbeat. A settled
 * swap has nothing left to learn minute by minute, and putting the finished
 * ones on a timer would poll a set that only grows. History gets one pass when
 * it is opened -- see {@link syncHistoryOnce}.
 *
 * Settled is checked as well as active, and not instead of it: a finished swap
 * now stays on the active page until the user archives it, so `active` alone
 * stopped meaning "still has something to learn" the moment that changed.
 */
function due(sw: Swap): boolean {
  return sw.active && !sw.settled && onChain(sw)
}

/**
 * A Zenon leg that was written down but never got an answer from a node.
 *
 * A create is unreadable for a momentum or two after the wallet publishes it, so
 * the first answer is "not on the chain yet" -- and until this, nothing but a
 * Verify press ever asked again, leaving an HTLC that became perfectly good
 * thirty seconds later wearing "awaiting confirmation".
 *
 * Only pending. A refusal is an answer. A verified leg is not re-read either,
 * because the other reason to go back to Zenon -- the preimage a counterparty's
 * unlock revealed -- is already part of Refresh above.
 */
function verifyDue(sw: Swap): Swap['out'][] {
  return [sw.out, sw.in].filter(
    (l): l is Swap['out'] =>
      Boolean(l?.znn?.htlcId) && !l?.znn?.verified && Boolean(l?.znn?.verifyPending),
  )
}

/**
 * Ask both chains about one swap, and report whether they said anything new.
 *
 * Both in one call, because a swap is one thing and its two halves are only
 * separately interesting to the code. `refresh` covers the Bitcoin side -- the
 * funding, its depth, the pre-signed refund, and a counterparty's redeem -- and
 * also goes looking on Zenon for the unlock that revealed a preimage there.
 *
 * Each step is attempted on its own and a failure is swallowed. Nobody is
 * waiting on an answer here, so a node that blinks should cost the next swap
 * nothing and must not paint an error over a page somebody is reading.
 */
async function syncSwap(sw: Swap): Promise<boolean> {
  let latest: Swap | null = null
  try {
    latest = await api.refresh(sw.id, settings.value)
  } catch {
    // See above.
  }
  const now = latest ?? sw
  if (hasZenon.value) {
    // Both legs, because a ZTS-against-ZTS swap has two of them and either can
    // be the one still waiting on a verdict.
    for (const leg of verifyDue(now)) {
      try {
        // A refusal comes back in the result rather than as a throw, and is as
        // much of an answer as a pass: either way the leg is no longer pending
        // and the card can stop saying it is waiting.
        const r = await api.verifyZenon(sw.id, leg.znn?.htlcId ?? '', settings.value, leg.dir)
        latest = r.swap ?? latest
      } catch {
        // Likewise. A verdict that cannot be reached is still pending.
      }
    }
  }
  return Boolean(latest) && JSON.stringify(latest) !== JSON.stringify(sw)
}

async function tick() {
  if (running) return
  const work = swaps.value.filter(due)
  // A background tab is somebody else's browser doing nothing useful with a
  // stranger's node. Whatever it would have found is found on the tick after
  // they come back, which is the first moment it could matter to them.
  //
  // Unless autopilot is armed on one of these swaps, which is the case where
  // that reasoning inverts: arming it is somebody saying they are NOT going to
  // sit and watch, and every step it takes is unlocked by something a node says.
  // A driver that only runs while its tab is in front is a driver that runs
  // exactly when it was not needed -- and a Zenon leg left unverified because
  // the tab was in the background is one whose id never reaches the
  // counterparty, since that hand-off waits on the verdict. Narrowed to the
  // swaps actually being driven, so a halted swap or one armed months ago does
  // not put a hidden tab on a node forever.
  if (document.hidden && !work.some((sw) => isOn(sw.id))) return
  running = true
  try {
    if (!work.length) return
    let changed = false
    for (const sw of work) {
      if (await syncSwap(sw)) changed = true
    }
    // Only when a node actually said something new, and once for the whole round
    // rather than per swap. Reloading regardless would replace the list every
    // minute, and every component holding something a user is part-way through
    // would have to survive a churn that carried no news. Into whichever view is
    // on screen, so this cannot swap History out from under someone.
    if (changed) await loadSwaps(view.value)
  } finally {
    running = false
  }
}

/**
 * One pass over the finished list, when somebody opens it.
 *
 * History is the one view nothing ever refreshes -- the heartbeat skips it by
 * design, see {@link due} -- so a card there shows whatever the chain last said
 * before the swap left the active list. A refund that confirmed afterwards never
 * reached the card at all, and the page that is supposed to be the record of
 * what happened was the one furthest behind.
 *
 * Once, on open, rather than on a timer: the finished list only grows, and
 * polling an unbounded set to learn things that change at most once per swap is
 * the wrong trade.
 *
 * Shares `running` with the heartbeat, so arriving on History mid-tick queues
 * nothing and doubles no request.
 */
async function syncHistoryOnce() {
  if (running) return
  running = true
  try {
    // Not filtered any harder than this. A settled-looking swap is exactly the
    // one whose card may be wrong — `refundTx` is a refund PRE-SIGNED at
    // funding time and not a refund that went anywhere, so reading it as
    // "resolved" would skip the swaps this pass exists for.
    const work = swaps.value.filter((sw) => !sw.active && onChain(sw))
    if (!work.length) return
    let changed = false
    for (const sw of work) {
      if (await syncSwap(sw)) changed = true
    }
    if (changed) await loadSwaps(view.value)
  } finally {
    running = false
  }
}

/**
 * How hard to chase one piece of news before falling back to the heartbeat.
 *
 * A counterparty saying they have just done something is a claim about a chain
 * this browser's node may not have caught up with, so the first look very often
 * finds nothing and giving up would turn a message that should have saved a
 * minute into one that saved nothing.
 *
 * Four seconds is faster than either chain settles and slow enough not to hammer
 * a node; ten attempts is a little under a minute, where the heartbeat takes
 * over anyway.
 */
const BURST_INTERVAL_MS = 4_000
const BURST_ATTEMPTS = 10

/** Swaps with a burst already running, so two announcements arriving together
 *  chase the same news once rather than twice. */
const bursting = new Set<string>()

const sleep = (ms: number) => new Promise((done) => setTimeout(done, ms))

/**
 * Go and look now, because the counterparty says there is something to see.
 *
 * This is the whole point of announcing an action over the session: the other
 * side is not told WHAT happened and specifically is told no value -- it is told
 * that a chain moved, and goes and reads the chain itself, through the same
 * calls and the same checks as always. A message can make this browser look; it
 * cannot make it believe anything.
 *
 * It stops at the first round that changes something, and anything still
 * outstanding after that is the heartbeat's job.
 */
async function syncNow(id: string, attempts = BURST_ATTEMPTS) {
  if (bursting.has(id)) return
  bursting.add(id)
  try {
    for (let attempt = 0; attempt < attempts; attempt++) {
      // Re-read each round: the list may have been reloaded underneath, and a
      // swap that has been archived or settled in the meantime is not worth
      // chasing any further.
      const sw = swaps.value.find((s) => s.id === id)
      if (!sw || !due(sw)) return
      if (await syncSwap(sw)) {
        await loadSwaps(view.value)
        return
      }
      if (attempt < attempts - 1) await sleep(BURST_INTERVAL_MS)
    }
  } finally {
    bursting.delete(id)
  }
}

export function startAutoRefresh(intervalMs = INTERVAL_MS) {
  if (timer !== null) return
  timer = window.setInterval(() => void tick(), intervalMs)
}

export function stopAutoRefresh() {
  if (timer === null) return
  window.clearInterval(timer)
  timer = null
}

export function useAutoRefresh() {
  return {startAutoRefresh, stopAutoRefresh, syncNow, syncHistoryOnce}
}

export {syncNow, syncHistoryOnce}

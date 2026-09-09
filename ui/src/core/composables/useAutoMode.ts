import {computed, ref} from 'vue'
import {isDev} from '@/core/env'

/**
 * Autopilot, armed per swap.
 *
 * Once the terms are agreed and the counterparty's contract audited, almost
 * every remaining click is a person relaying a decision already made. The cost
 * of that is not the clicking: it is that a swap only advances while somebody is
 * watching it, and both sides are waiting on each other to look.
 *
 * Auto Mode removes the watching. Armed, this browser performs the outstanding
 * step as soon as it becomes possible, and stops exactly where a wallet has to
 * sign. What it does NOT do is the whole safety argument:
 *
 *   - It never skips a check. Every value arriving over a session still goes
 *     through the handler that audits or verifies it, and every action through
 *     the Go call that re-checks its preconditions. It presses buttons; it does
 *     not grant anything.
 *   - It never signs. Funding and every Zenon call are signed in the user's own
 *     wallet, in its own window. Autopilot gets you to that window; only you get
 *     past it.
 *   - It never refunds. Abandoning a swap is a decision about whether to keep
 *     waiting, and the one moment where acting automatically could take the
 *     wrong branch -- a refund raced against a counterparty's redeem.
 *
 * Arming it is therefore the approval, which is why it is per swap and never
 * global: approval is about this trade, not about trading.
 */

/** Per instance, for the same reason the settings are: a dev build and a
 *  production build on one origin must not share a decision about a real swap. */
const STORAGE_KEY = isDev ? 'ferry.dev.auto' : 'ferry.auto'

function read(): Record<string, boolean> {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    const v: unknown = JSON.parse(raw || '{}')
    if (!v || typeof v !== 'object') return {}
    return v as Record<string, boolean>
  } catch {
    return {}
  }
}

/** Which swaps are armed. Persisted, because a swap outlives a page: autopilot
 *  that forgot itself on reload would be autopilot you had to sit and watch. */
const armed = ref<Record<string, boolean>>(read())

/**
 * Why autopilot stopped, per swap. The circuit breaker: anything an automatic
 * step can do wrong it can do wrong repeatedly and quickly, so the first failure
 * disarms this swap and says so on the card. Held in memory rather than stored,
 * because a reload is a person deciding to try again.
 */
const halted = ref<Record<string, string>>({})

/**
 * Steps already attempted, as `swapId:action`, for the life of this page.
 *
 * Belt to the circuit breaker's braces, aimed at a different failure: a halt
 * catches a step that threw, this catches one that returned happily and left the
 * swap looking exactly as it did -- a broadcast a node has not listed yet, a
 * wallet that reported success before the block was readable. Without it the
 * driver would see the same outstanding step next tick and do it again, which
 * for a spend means a second transaction.
 *
 * At most once per swap per action per page load. If the first attempt did not
 * take, the user is better placed than a loop to decide what to do.
 */
const attempted = new Set<string>()

function save() {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(armed.value))
  } catch {
    // A browser refusing storage costs autopilot its memory across reloads and
    // nothing else. It is not worth a message on a swap card.
  }
}

/** Whether this swap should be driven right now. */
function isOn(id: string): boolean {
  return armed.value[id] === true && !halted.value[id]
}

/** Whether the user has armed it, regardless of whether it has since stopped —
 *  which is what a toggle has to reflect, so that a halted swap does not look
 *  like one nobody ever armed. */
function isArmed(id: string): boolean {
  return armed.value[id] === true
}

function reason(id: string): string {
  return halted.value[id] ?? ''
}

function set(id: string, on: boolean) {
  armed.value = {...armed.value, [id]: on}
  // Turning it on is always a fresh start: it clears the halt and forgets what
  // was tried, because the user has now looked at the thing that stopped it.
  if (on) {
    delete halted.value[id]
    halted.value = {...halted.value}
    for (const key of [...attempted]) {
      if (key.startsWith(`${id}:`)) attempted.delete(key)
    }
  }
  save()
}

/** Stop driving this swap and say why. The toggle stays on, so the card can
 *  show that autopilot was armed AND that it has stopped, which are two
 *  different things and both worth knowing. */
function halt(id: string, why: string) {
  halted.value = {...halted.value, [id]: why}
}

/**
 * Claim one step, or refuse. Returns false when it has already been attempted,
 * which is what makes every caller a one-shot without each of them keeping its
 * own bookkeeping.
 */
function claim(id: string, action: string): boolean {
  const key = `${id}:${action}`
  if (attempted.has(key)) return false
  attempted.add(key)
  return true
}

/** Swaps armed at all, so a page can say autopilot is running somewhere. */
const anyArmed = computed(() => Object.values(armed.value).some(Boolean))

export function useAutoMode() {
  return {armed, halted, anyArmed, isOn, isArmed, reason, set, halt, claim}
}

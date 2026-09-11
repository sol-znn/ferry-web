/**
 * Whether the printed znn-cli create may be printed, as a small state machine
 * with no Vue in it, so its lifecycle can be tested under Node.
 *
 * The wallet button is behind Go's gate twice over: when the block is built
 * and again before it is handed to the wallet. A command copied into a
 * terminal passes through no gate at all, so the text is the gate -- and the
 * text is only as safe as the freshest answer behind it. Three things this
 * guarantees, each of which a plain "remember the last answer" got wrong:
 *
 *  - Asking revokes. The moment a check starts, the previous authorisation is
 *    gone; a check that is pending, hung, or never answers leaves the command
 *    withheld rather than still printed on the strength of an earlier pass.
 *  - Only the latest answer counts. A check that started earlier and finishes
 *    later cannot overwrite the verdict of the one that started after it.
 *  - A pass expires. Authorisation is good for `ttlMs` and no longer, so a
 *    caller whose re-checks stop arriving -- a refresh that failed and fired
 *    nothing, a throttled timer -- ends up withheld rather than stale-open.
 *
 * `check` is the fail-closed engine call (api.fundingCheck): resolves when the
 * funding is present, full, mined, unspent and the chain was readable; rejects
 * with the reason otherwise.
 */
export interface CliCreateGate {
  /** Ask again. Withheld from the moment this is called until the answer lands. */
  run(): Promise<void>
  /** Withdraw any authorisation now, with an optional reason to show. */
  revoke(reason?: string): void
  /** The current verdict, judged at `now`. */
  state(): {allowed: boolean; blocker: string}
}

export const CLI_CREATE_TTL_MS = 45_000

export function createCliCreateGate(opts: {
  check: () => Promise<unknown>
  ttlMs?: number
  now?: () => number
}): CliCreateGate {
  const ttl = opts.ttlMs ?? CLI_CREATE_TTL_MS
  const now = opts.now ?? (() => Date.now())
  let seq = 0
  let validUntil = 0
  let blocker = ''

  return {
    async run() {
      const mine = ++seq
      validUntil = 0
      blocker = ''
      try {
        await opts.check()
        if (mine !== seq) return
        validUntil = now() + ttl
      } catch (err) {
        if (mine !== seq) return
        validUntil = 0
        blocker = err instanceof Error ? err.message : String(err)
      }
    },
    revoke(reason = '') {
      seq++
      validUntil = 0
      blocker = reason
    },
    state() {
      return {allowed: validUntil > 0 && now() < validUntil, blocker}
    },
  }
}

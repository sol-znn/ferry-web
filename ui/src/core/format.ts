/** Satoshis with thousands separators. The unit is always shown: an amount
 *  without one is how a swap gets funded off by a factor of 100,000,000. */
export function sats(n: number | undefined): string {
  return typeof n === 'number' ? `${n.toLocaleString('en-US')} sat` : '—'
}

export function height(n: number | undefined): string {
  return typeof n === 'number' ? n.toLocaleString('en-US') : '—'
}

/** "2026-09-03T14:22:07Z" → "2026-09-03 14:22:07", which is what the event log
 *  and the locktime both want: sortable, unambiguous, no locale surprises. */
export function stamp(iso: string | undefined): string {
  if (!iso) return '—'
  return iso.replace('T', ' ').replace(/\..*/, '').replace('Z', '')
}

/** Whether a locktime that has already passed. Purely for display. */
export function isPast(iso: string | undefined): boolean {
  if (!iso) return false
  const t = Date.parse(iso)
  return Number.isFinite(t) && t < Date.now()
}

/** The same amount in BTC. Sats are the unit the protocol works in and the one
 *  a swap is agreed in; BTC is the one most people can picture, so both are
 *  shown wherever an amount is entered or funded. */
export function btc(n: number | undefined): string {
  if (typeof n !== 'number' || !Number.isFinite(n)) return '—'
  const s = (n / 1e8).toFixed(8).replace(/0+$/, '').replace(/\.$/, '')
  return `${s} BTC`
}

/** "in 47 h" / "6 min ago" — a locktime in the terms people reason about.
 *  Always shown beside the exact stamp, never instead of it: "in 2 days" is
 *  what you understand, and the timestamp is what you plan around. */
export function until(iso: string | undefined): string {
  if (!iso) return ''
  const t = Date.parse(iso)
  if (!Number.isFinite(t)) return ''
  const mins = Math.round((t - Date.now()) / 60000)
  const m = Math.abs(mins)
  const span =
    m < 60 ? `${m} min` : m < 2880 ? `${Math.round(m / 60)} h` : `${Math.round(m / 1440)} days`
  return mins < 0 ? `${span} ago` : `in ${span}`
}

/** The ZTS the Go side fills in when a swap does not name a token.
 *  Mirrors znn.ZnnTokenStandard in wasm/znn. Exported because "blank" and this
 *  are one term, and anything comparing two tokens has to know that. */
export const ZNN_ZTS = 'zts1znnxxxxxxxxxxxxx9z4ulx'

/** A token standard as something a person can read. ZNN is spelled out because
 *  it is what almost every swap trades; anything else keeps its zts so nobody
 *  is told a friendly name for a token they have not verified. */
export function tokenName(std: string | undefined): string {
  if (!std || std === ZNN_ZTS) return 'ZNN'
  return `${std.slice(0, 9)}…${std.slice(-4)}`
}

import type {LegView, Swap} from '@/types'

/** Where the printed commands come from. */
export const ZNN_CLI_URL = 'https://github.com/zenon-network/znn_cli_dart'

/**
 * znn-cli caps `htlc.create` at 1..24 hours. Nothing on chain does, so this only
 * decides which command is worth printing, never whether a swap is possible: the
 * Syrius extension signs any expiry.
 */
export const ZNN_CLI_MAX_HOURS = 24

/** The two ports a go-zenon node serves the same JSON-RPC on. */
const RPC_HTTP_PORT = '35997'
const RPC_WS_PORT = '35998'

/**
 * The znn-cli `-u` value for a node this app is pointed at.
 *
 * This app takes either endpoint; znn-cli takes only the WebSocket, and "wss
 * instead of https, and one port up" is the kind of translation people get wrong
 * once and then debug for an hour. Returns '' when there is nothing sensible to
 * say, rather than a guess — an invented node URL in a command somebody pastes
 * into a terminal is worse than a placeholder they can see is a placeholder.
 */
export function cliNodeURL(appURL: string | undefined): string {
  const raw = (appURL ?? '').trim()
  if (!raw) return ''
  let u: URL
  try {
    u = new URL(raw)
  } catch {
    return ''
  }
  if (u.protocol === 'ws:' || u.protocol === 'wss:') return u.toString().replace(/\/$/, '')

  const secure = u.protocol === 'https:'
  const port = u.port === RPC_HTTP_PORT || u.port === '' ? RPC_WS_PORT : u.port
  return `${secure ? 'wss' : 'ws'}://${u.hostname}:${port}`
}

/** How the printed commands should name the user's own signing account. */
export interface CommandContext {
  /** The Zenon node this browser is set to, translated to znn-cli's transport. */
  nodeURL?: string
}

/**
 * The Zenon leg as znn-cli commands, for anyone driving it from a terminal
 * rather than through the Syrius extension. Creating and unlocking an HTLC are
 * signed operations and ferry never holds Zenon keys, so either way the signing
 * happens somewhere else.
 *
 * Everything the app knows is filled in. What is left as a placeholder is
 * exactly what only the user can supply — which keystore, which account index,
 * the passphrase — and those are marked as placeholders rather than looking like
 * values. A printed command that is nearly right is a command somebody runs
 * without reading.
 */
export function znnCommands(sw: Swap, leg: LegView, ctx: CommandContext = {}): string {
  const lines: string[] = []
  const other = leg.dir === 'out' ? sw.in : sw.out
  const token = leg.token || 'ZNN'
  const hours = leg.znn?.expirationHours
  const amount = leg.amount || '<amount>'
  const peer = leg.peerAddr || '<counterparty z1 address>'
  const self = leg.selfAddr
  const ours = leg.isInitiators ? "initiator's" : "participant's"

  // -k names the keystore (its file name, or the address it holds), -i the
  // account index inside it, -p the passphrase and -u the node.
  const node = ctx.nodeURL ? ` -u ${ctx.nodeURL}` : ' -u <wss://your-node:35998>'
  const cliFlags = ` -k ${self || '<your keystore>'} -i <account index> -p <passphrase>${node}`

  lines.push(`# znn-cli — ${ZNN_CLI_URL}`)
  lines.push('')

  if (leg.dir === 'out') {
    // This user funds this leg, so this user creates the entry.
    lines.push('# You fund this leg, so you create the HTLC. Your keys stay in your wallet.')
    if (!hours) {
      lines.push('# (the expiry is filled in once the other leg has a deadline)')
    }
    lines.push(
      '# htlc.create <recipient> <token> <amount> <hours> <hashtype> <hashlock>' +
        " — hashType 1 = SHA-256, which is what every other leg's preimage commits to.",
    )
    lines.push(
      `# This is the ${ours} leg, so it must expire ` +
        `${leg.isInitiators ? 'AFTER' : 'BEFORE'} the ${other?.chain ?? 'other'} leg.`,
    )
    if (hours && hours > ZNN_CLI_MAX_HOURS) {
      lines.push(
        `# znn-cli cannot express this leg: it caps htlc.create at ${ZNN_CLI_MAX_HOURS}h and this` +
          ` needs ${hours}h. Use the Syrius extension, which has no cap.`,
      )
    } else {
      lines.push(
        `znn-cli htlc.create ${peer} ${token} ${amount} ${hours || '<hours>'} 1` +
          ` ${sw.secretHashHex}${cliFlags}`,
      )
    }
    lines.push('')
    lines.push('# It will print an id. Paste that into "HTLC id" above and verify it —')
    lines.push('# verifying your own HTLC is how you catch a typo before they act on it.')
    lines.push('')
    if (sw.secretArrivesOn === 'znn') {
      lines.push('# When they unlock it, the preimage becomes visible on Zenon. Unlocking DELETES')
      lines.push('# the entry, so ferry reads the preimage out of their unlock transaction on')
      lines.push('# Refresh; if that cannot reach your node, paste it in below by hand.')
    } else {
      lines.push('# You already hold the preimage. Claim their leg with it once you have checked')
      lines.push('# it; that publishes the preimage, which is how they unlock this HTLC.')
    }
    lines.push('')
    lines.push('# If the swap stalls, reclaim after expiry:')
    lines.push(`znn-cli htlc.reclaim ${leg.znn?.htlcId || '<your htlc id>'}${cliFlags}`)
  } else {
    // The counterparty funds this leg, so they create the entry.
    const id = leg.znn?.htlcId || '<their htlc id>'
    const preimage = sw.secretHex || '<preimage>'
    lines.push('# The counterparty creates this HTLC. Verify it above before acting, then:')
    lines.push(`znn-cli htlc.unlock ${id} ${preimage}${cliFlags}`)
    lines.push('')
    // The step people miss. Unlocking moves the funds to your address as an
    // unreceived block; on Zenon that is not the same as having them.
    lines.push('# Unlocking sends the tokens to you as an unreceived block. Collect them after two')
    lines.push('# momentums, or they sit there looking like the unlock did not work:')
    lines.push(`znn-cli receiveAll${cliFlags}`)
    lines.push('')
    if (sw.secretArrivesOn) {
      lines.push('# You do NOT hold the preimage yet. It appears on the leg you funded when they')
      lines.push('# claim it; ferry extracts it automatically on Refresh, then unlock with it.')
    } else {
      lines.push('# You hold the preimage. Unlocking reveals it, which is how they claim the leg')
      lines.push('# you funded. Do it well before their HTLC expires.')
    }
  }
  return lines.join('\n')
}

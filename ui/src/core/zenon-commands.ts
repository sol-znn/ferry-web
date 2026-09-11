import type {Swap} from '@/types'

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

/**
 * Text that is not a command, as comment lines. Every physical line gets its
 * own `# `, because this block is copied into a terminal and a newline inside
 * a message -- an error body relayed from a node this browser was pointed at,
 * say -- would otherwise end the comment and start a command. Carriage returns
 * count as line breaks for the same reason.
 */
export function comment(text: string): string[] {
  return text.split(/\r\n|\r|\n/).map((l) => `# ${l}`)
}

/** How the printed commands should name the user's own signing account. */
export interface CommandContext {
  /** The Zenon node this browser is set to, translated to znn-cli's transport. */
  nodeURL?: string
  /**
   * For a create that answers the counterparty's Bitcoin funding: whether a
   * fail-closed check of that funding against the chain passed JUST NOW. Not
   * the swap's cached `fundingCommitted`, which is only as current as the last
   * refresh and survives a refresh that could not read the chain. Absent means
   * not checked, and not checked means withheld -- the command is the one path
   * the engine cannot gate, so the text has to be.
   */
  createAllowed?: boolean
  /** Why it is not allowed, when the check refused; shown in the command's place. */
  createBlocker?: string
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
export function znnCommands(sw: Swap, ctx: CommandContext = {}): string {
  const lines: string[] = []
  const token = sw.zenon?.tokenStandard || 'ZNN'
  const hours = sw.zenon?.expirationHours
  const amount = sw.zenon?.amountDisplay || '<amount>'
  const peer = sw.zenon?.peerAddress || '<counterparty z1 address>'
  const self = sw.zenon?.selfAddress
  const ours = sw.btcLegIsInitiators ? "participant's" : "initiator's"

  // -k names the keystore (its file name, or the address it holds), -i the
  // account index inside it, -p the passphrase and -u the node.
  const node = ctx.nodeURL ? ` -u ${ctx.nodeURL}` : ' -u <wss://your-node:35998>'
  const cliFlags = ` -k ${self || '<your keystore>'} -i <account index> -p <passphrase>${node}`

  lines.push(`# znn-cli — ${ZNN_CLI_URL}`)
  lines.push('')

  if (sw.zenonHtlcIsOurs) {
    // This user sends ZNN, so this user creates the Zenon HTLC.
    lines.push('# You send ZNN, so you create the Zenon HTLC. Your keys stay in your wallet.')
    if (!hours) lines.push('# (the expiry is filled in once the Bitcoin locktime is known)')
    lines.push(
      `# htlc.create <recipient> <token> <amount> <hours> <hashtype> <hashlock>` +
        ` — hashType 1 = SHA-256, which is what Bitcoin's OP_SHA256 requires.`,
    )
    lines.push(
      `# This is the ${ours} leg, so it must expire ` +
        `${sw.btcLegIsInitiators ? 'BEFORE' : 'AFTER'} the Bitcoin contract.`,
    )
    // The same gate the wallet button is behind, applied to the one path the
    // engine cannot stop: a command run in a terminal. Where this leg answers
    // the counterparty's Bitcoin funding, the command is printed only when the
    // caller says a live, fail-closed check of that funding passed just now;
    // the swap's own cached flag is not consulted, because it is only as
    // current as the last refresh. Printed as a comment rather than a command,
    // because a command that is nearly right is a command somebody runs
    // without reading -- and this one, run now, hands the counterparty the ZNN
    // for a payment they can still take back.
    const waitsOnBtc = sw.btcLegIsInitiators
    if (waitsOnBtc && ctx.createAllowed !== true) {
      lines.push(
        ...comment(
          `NOT YET: ${
            ctx.createBlocker ||
            sw.fundingCommitBlocker ||
            'their Bitcoin funding has not been checked against the chain just now'
          }.`,
        ),
      )
      lines.push('# Your HTLC answers their Bitcoin payment. A payment they can still replace is')
      lines.push('# one they can take back after you lock ZNN, and they already hold the secret.')
      lines.push('# The create command is withheld until the card says the funding is confirmed')
      lines.push('# and covers the agreed amount; Refresh keeps checking.')
    } else if (hours && hours > ZNN_CLI_MAX_HOURS) {
      lines.push(
        `# znn-cli cannot express this leg: it caps htlc.create at ${ZNN_CLI_MAX_HOURS}h and this` +
          ` needs ${hours}h. Use the Syrius extension, which has no cap.`,
      )
    } else {
      lines.push(
        `znn-cli htlc.create ${peer} ${token} ${amount} ${hours || '<hours>'} 1` +
          ` ${sw.secretHashHex}${cliFlags}`,
      )
      lines.push('')
      lines.push('# It will print an id. Paste that into "Zenon HTLC id" above and verify it —')
      lines.push('# verifying your own HTLC is how you catch a typo before they act on it.')
    }
    lines.push('')
    if (sw.secretArrivesOnZenon) {
      lines.push('# When they unlock it, the preimage becomes visible on Zenon. Unlocking DELETES')
      lines.push('# the entry, so ferry reads the preimage out of their unlock transaction on')
      lines.push('# Refresh; if that cannot reach your node, paste it in below by hand.')
    } else {
      lines.push('# You already hold the preimage. Redeem their Bitcoin contract with it once you')
      lines.push('# have audited it; that publishes the preimage on Bitcoin, which is how they')
      lines.push('# unlock this HTLC.')
    }
    lines.push('')
    lines.push('# If the swap stalls, reclaim after expiry:')
    lines.push(`znn-cli htlc.reclaim ${sw.zenon?.htlcId || '<your htlc id>'}${cliFlags}`)
  } else {
    // The counterparty sends ZNN, so they create the Zenon HTLC.
    const id = sw.zenon?.htlcId || '<their htlc id>'
    const preimage = sw.secretHex || '<preimage>'
    lines.push('# The counterparty creates the Zenon HTLC. Verify it above before acting, then:')
    lines.push(`znn-cli htlc.unlock ${id} ${preimage}${cliFlags}`)
    lines.push('')
    // The step people miss. Unlocking moves the funds to your address as an
    // unreceived block; on Zenon that is not the same as having them.
    lines.push('# Unlocking sends the ZNN to you as an unreceived block. Collect it after two')
    lines.push('# momentums, or it sits there looking like the unlock did not work:')
    lines.push(`znn-cli receiveAll${cliFlags}`)
    lines.push('')
    if (sw.btcLegIsInitiators) {
      lines.push('# You hold the preimage. Unlocking reveals it on Zenon, which is how they claim')
      lines.push('# the Bitcoin you locked. Do it well before their HTLC expires.')
    } else {
      lines.push('# You do NOT hold the preimage yet. It appears on Bitcoin when they redeem your')
      lines.push('# contract; ferry extracts it automatically on Refresh, then unlock with it.')
    }
  }
  return lines.join('\n')
}

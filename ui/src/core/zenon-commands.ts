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
    if (sw.btcLegIsInitiators) {
      // This leg answers the counterparty's Bitcoin funding, and the create
      // is NOT printed as a command for it, whatever the swap record says. The
      // wallet button is behind Go's gate twice over -- the funding is read
      // off the chain when the block is built and again before it is signed
      // -- but a command copied into a terminal passes through no gate at
      // all, and every attempt to gate the text on a remembered answer has a
      // moment where the answer is stale. So the terms are printed, as
      // comments, and the command is not: somebody who must use znn-cli for
      // this leg composes it after checking the funding themselves, at the
      // moment they run it.
      lines.push(
        ...comment(
          'No create command is printed for this leg. It answers their Bitcoin payment, ' +
            'and locking ZNN against a payment that can still be replaced or reclaimed ' +
            'hands them the ZNN: they already hold the secret. Use the wallet button ' +
            'above, which checks the funding against the chain when the block is built ' +
            'and again before it is signed.',
        ),
      )
      lines.push('#')
      lines.push(
        ...comment(
          'If you must use znn-cli, check on your own node, immediately before running ' +
            `it, that the contract output is unspent, mined, holds ${sw.amountSats} sat, ` +
            'and that the Bitcoin locktime is far enough away to fit this leg before it. ' +
            'Then htlc.create takes, in order:',
        ),
      )
      // Each term through comment(), whole: these values come from the offer,
      // the form and the chain, and a line break inside one would otherwise
      // end the comment and start a command.
      for (const term of [
        `  recipient  ${peer}`,
        `  token      ${token}`,
        `  amount     ${amount}`,
        `  hours      ${hours || '<hours>'}` +
          (hours && hours > ZNN_CLI_MAX_HOURS
            ? ` (over znn-cli's ${ZNN_CLI_MAX_HOURS}h cap: use the wallet)`
            : ''),
        '  hashtype   1  (SHA-256, what Bitcoin OP_SHA256 requires)',
        `  hashlock   ${sw.secretHashHex}`,
      ]) {
        lines.push(...comment(term))
      }
      if (sw.fundingCommitBlocker) {
        lines.push('#')
        lines.push(...comment(`As of the last refresh: ${sw.fundingCommitBlocker}.`))
      }
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

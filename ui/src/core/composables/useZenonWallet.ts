import {computed, onScopeDispose, readonly, ref} from 'vue'

import {
  canSignMessage,
  onWalletChange,
  readAccess,
  requestAccess,
  revokeAccess,
  sendAccountBlock,
  waitForExtension,
  zenonExtension,
  type ZenonAccountBlock,
  type ZenonSendResult,
  type ZenonWalletAccess,
} from '@/core/zenon-wallet'

/**
 * The connected Syrius extension, as one piece of state shared by every card.
 *
 * Module-level rather than per-component, for the same reason `useUnisat` is: a
 * connection is a property of the browser, not of whichever card asked for it.
 *
 * Nothing here is persisted by this app, and nothing needs to be. The extension
 * remembers connected origins itself and answers `getAccounts` from that without
 * prompting, so the state below is restored by asking the wallet rather than by
 * trusting something this page wrote down -- which matters, because the address
 * decides who can reclaim an HTLC.
 */

const available = ref(zenonExtension())
/**
 * Whether the connected extension can sign a plain message.
 *
 * A `ref` updated at the same moments `available` is, never a `computed` over
 * `canSignMessage()` directly -- and that distinction is the whole bug it
 * avoids. `canSignMessage()` reads a plain property off `window.zenon`, which a
 * `computed` has no reactive dependency to invalidate on: it would evaluate once,
 * cache whatever the extension supported at that instant, and never look again --
 * telling somebody their freshly updated extension still cannot sign a message
 * while it sits right there in the toolbar. See `detected` in useUnisat.ts, which
 * names the same fix for the same reason on the Bitcoin side.
 */
const canSign = ref(canSignMessage())
const address = ref('')
const chainId = ref(0)
const nodeUrl = ref('')
const connecting = ref(false)
const sending = ref(false)
const error = ref('')

/**
 * Bumped whenever the wallet tells us something about itself has changed.
 *
 * A prepared block is built for one account on one chain. If either moves, that
 * block is a proposal about a wallet that no longer exists, so anything holding
 * one watches this and throws it away. A counter rather than a flag because what
 * matters is whether it has changed since I looked, which is a comparison.
 *
 * Bumped on every announced change even when the new state reads back
 * successfully: discarding the prepared block is the safety property, not making
 * the user press Connect again is the convenience, and only the first is
 * load-bearing.
 */
const epoch = ref(0)

/** Whether the module has subscribed to the wallet yet. */
let watching = false

/**
 * Guards against an out-of-order re-read.
 *
 * Two changes in quick succession start two reads, and the slower one can
 * answer last while describing the earlier state. Only the newest read is
 * allowed to write.
 */
let readToken = 0

/**
 * Adopt a wallet state, and say whether it was actually different.
 *
 * The return value drives `epoch`, and the distinction is the whole point:
 * `epoch` answers "has the wallet moved since I looked", which is a question
 * about state, not about traffic. An event announcing that the selected account
 * is still the selected account has moved nothing -- and bumping on the event
 * instead discards a block that was about to be signed, with a message saying
 * the wallet changed, which is untrue and unactionable.
 */
function apply(access: ZenonWalletAccess | null): boolean {
  const next = access ?? {address: '', chainId: 0, nodeUrl: ''}
  const moved =
    next.address !== address.value ||
    next.chainId !== chainId.value ||
    next.nodeUrl !== nodeUrl.value
  address.value = next.address
  chainId.value = next.chainId
  nodeUrl.value = next.nodeUrl
  return moved
}

/**
 * Re-read the whole triple after the wallet says something moved.
 *
 * The three are only ever taken together, from one read. Applying a lone
 * `addressChanged` would leave an identity assembled from two different moments,
 * which was observed live: a new account beside a node URL captured before the
 * user went into the settings screen, with the gate then reporting that the
 * wallet did not say which node it publishes through while the real and
 * unchanged problem was the wrong chain.
 *
 * `getAccounts`, `getChainId` and `getNodeUrl` are read-only and never prompt,
 * so all three can be re-read at any moment -- which is what lets the rule stay
 * while the Connect prompt goes.
 */
async function refresh(): Promise<void> {
  const token = ++readToken
  const access = await readAccess()
  if (token !== readToken) return
  if (apply(access)) epoch.value++
  // Re-checked on every announced change, not only when the provider first
  // turns up: an extension can update itself and re-announce without a page
  // reload, and `canSign` is meant to catch exactly that moment.
  canSign.value = canSignMessage()
  if (!access) {
    error.value =
      'Syrius is no longer connected to this page — it was locked, or this site was removed ' +
      'from its connected sites. Press Connect to reconnect.'
  }
}

function startWatching() {
  if (watching || typeof window === 'undefined') return
  watching = true

  // The provider is injected at document_start into the page's own world, so it
  // is normally already here. The wait covers an extension enabled while this
  // page was open. Once it is there, a connection this origin already has is
  // restored without prompting.
  void waitForExtension().then(async (found) => {
    available.value = found
    canSign.value = found && canSignMessage()
    if (found) {
      await refresh()
      // Nothing was connected. That is the ordinary first-visit state and is
      // not something to report as an error.
      if (!address.value) error.value = ''
    }
  })

  // Deliberately not bumped here. An announcement is not a change, and the
  // re-read below is what decides — see `apply`. A wallet that re-announces
  // its unchanged state, which this one does, would otherwise discard a
  // prepared block every time it spoke.
  onWalletChange((change) => {
    if (change.disconnected) {
      if (apply(null)) epoch.value++
      error.value =
        'Syrius disconnected this page. Press Connect to reconnect — any block prepared before ' +
        'now has been discarded.'
      return
    }
    error.value = ''
    void refresh()
  })
}

export function useZenonWallet() {
  startWatching()

  const connected = computed(() => Boolean(address.value))

  async function connect() {
    error.value = ''
    connecting.value = true
    try {
      available.value = await waitForExtension()
      canSign.value = available.value && canSignMessage()
      const access = await requestAccess()
      readToken++
      if (apply(access)) epoch.value++
      return access
    } catch (err) {
      error.value = err instanceof Error ? err.message : String(err)
      throw err
    } finally {
      connecting.value = false
    }
  }

  /**
   * Disconnect this page from the wallet. This revokes something real: the
   * extension keeps a connected-sites list, and `disconnect` removes this origin
   * from it, which is what makes the next Connect prompt again rather than
   * resolve silently.
   */
  async function forget() {
    readToken++
    if (apply(null)) epoch.value++
    error.value = ''
    await revokeAccess()
  }

  /**
   * The wallet's own account of itself, as the shape every call takes.
   *
   * Read at the moment of the call rather than captured, because the point of
   * every check built on it is that it may have changed since the last one.
   */
  function identity() {
    return {address: address.value, chainId: chainId.value, nodeUrl: nodeUrl.value}
  }

  async function send(block: ZenonAccountBlock): Promise<ZenonSendResult> {
    error.value = ''
    sending.value = true
    try {
      return await sendAccountBlock(block)
    } catch (err) {
      error.value = err instanceof Error ? err.message : String(err)
      throw err
    } finally {
      sending.value = false
    }
  }

  onScopeDispose(() => {
    // Deliberately nothing. The state outlives this component by design, and
    // the subscription is one per module rather than one per card.
  })

  return {
    available: readonly(available),
    canSign: readonly(canSign),
    address: readonly(address),
    chainId: readonly(chainId),
    nodeUrl: readonly(nodeUrl),
    connected,
    connecting: readonly(connecting),
    sending: readonly(sending),
    error: readonly(error),
    epoch: readonly(epoch),
    identity,
    connect,
    forget,
    send,
  }
}

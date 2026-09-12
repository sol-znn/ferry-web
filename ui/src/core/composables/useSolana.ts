import {computed, onMounted, ref} from 'vue'
import {
  PHANTOM_INSTALL_URL,
  airdrop as doAirdrop,
  balance as readBalance,
  connectInjected,
  connectPhantom,
  detected,
  disconnect as dropWallet,
  send as sendInstruction,
  signMessage as signWithWallet,
  useLocalKey,
  wallet,
} from '@/core/solana'
import {useSettings} from '@/core/composables/useSettings'
import {isDev} from '@/core/env'
import type {SolInstruction} from '@/types'

/**
 * The Solana wallet, as the page asks about it.
 *
 * A thin reactive shell over core/solana.ts, the same division useUnisat and
 * useZenonWallet make: the module below holds the provider and knows how to sign;
 * this holds the busy flag and the sentence a button shows. The state itself is
 * module-level because there is one wallet and several places that care —
 * the header strip, the swap card, and the board's Prove button.
 *
 * Reconnecting on mount is what makes a reload not a disconnection. Phantom
 * remembers which origins it trusts and reconnects without a popup; when it does
 * not, the failure is ordinary and silent, because closing a window on purpose
 * is not a fault.
 */

const busy = ref(false)
const error = ref('')
const lamports = ref<number | null>(null)
let started = false

export function useSolana() {
  const {stored, body: settings} = useSettings()

  onMounted(() => {
    if (started) return
    started = true
    void reconnect()
  })

  const connected = computed(() => wallet.kind !== 'none')
  const address = computed(() => wallet.address)
  const name = computed(() => wallet.name)
  const available = computed(() => detected.phantom || Boolean(detected.other))
  /** The name the connect button should carry: whatever is actually installed,
   *  and Phantom when nothing has announced itself yet — pressing it asks
   *  directly, which is the only authoritative answer. */
  const walletName = computed(() =>
    detected.phantom ? 'Phantom' : detected.other || 'Phantom',
  )
  /** The RPC endpoint calls will actually use. */
  const rpc = computed(() => stored.value.solRpc || defaultRpc(stored.value.network))

  async function reconnect(): Promise<boolean> {
    const {reconnectIfTrusted} = await import('@/core/solana')
    const ok = await reconnectIfTrusted()
    if (ok) void refreshBalance()
    return ok
  }

  async function connect(): Promise<boolean> {
    busy.value = true
    error.value = ''
    try {
      if (detected.phantom) await connectPhantom()
      else await connectInjected()
      void refreshBalance()
      return true
    } catch (e) {
      // A declined prompt is not a fault and gets no sentence; anything else
      // does, because the button otherwise goes quiet for no visible reason.
      const msg = e instanceof Error ? e.message : String(e)
      error.value = /reject|denied|cancel/i.test(msg) ? '' : msg
      return false
    } finally {
      busy.value = false
    }
  }

  /** A keypair in this browser, offered only on the development instance: the
   *  whole point of a local validator is that it gets wiped, and pointing a real
   *  wallet at a chain like that is a bad trade. */
  function useThrowawayKey(): boolean {
    if (!isDev) return false
    error.value = ''
    useLocalKey()
    void refreshBalance()
    return true
  }

  function disconnect() {
    dropWallet()
    lamports.value = null
  }

  async function refreshBalance() {
    if (!wallet.address) return
    try {
      lamports.value = await readBalance(rpc.value)
    } catch {
      // A balance is a courtesy. A node that will not answer is not a reason to
      // put a red sentence under a working wallet.
      lamports.value = null
    }
  }

  async function airdrop(sol = 2): Promise<boolean> {
    busy.value = true
    error.value = ''
    try {
      await doAirdrop(rpc.value, sol)
      await refreshBalance()
      return true
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
      return false
    } finally {
      busy.value = false
    }
  }

  /** Sign a statement, for a board proof. */
  async function signMessage(message: string): Promise<string> {
    return signWithWallet(message)
  }

  /** Sign and submit one engine instruction, returning its signature. */
  async function send(ix: SolInstruction): Promise<string> {
    const sig = await sendInstruction(rpc.value, ix)
    void refreshBalance()
    return sig
  }

  return {
    wallet,
    detected,
    available,
    walletName,
    connected,
    address,
    name,
    lamports,
    rpc,
    busy,
    error,
    settings,
    connect,
    reconnect,
    useThrowawayKey,
    disconnect,
    refreshBalance,
    airdrop,
    signMessage,
    send,
    installUrl: PHANTOM_INSTALL_URL,
  }
}

/** Mirrors DefaultSolanaURL in wasm/swap.go, repeated here only so the settings
 *  dialog can show the URL a blank field will use. */
export function defaultRpc(network: string): string {
  switch (network) {
    case 'mainnet':
      return 'https://api.mainnet-beta.solana.com'
    case 'testnet':
    case 'signet':
      return 'https://api.devnet.solana.com'
    case 'regtest':
      return 'http://127.0.0.1:8899'
  }
  return ''
}

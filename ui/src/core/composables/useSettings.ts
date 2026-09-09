import {computed, ref} from 'vue'
import type {Settings} from '@/types'
import {DEFAULT_RELAYS} from '@/core/nostr'
import {DEFAULT_SETTINGS, isDev} from '@/core/env'

/**
 * Which network, and which nodes. Sent with every call that reaches a chain.
 *
 * It matters more than it reads. Funding detection, HTLC verification and tip
 * heights are only as trustworthy as the node that answers them, and a lying
 * node can tell you an unsafe HTLC is fine or that a contract is unfunded when
 * it is not. Pointing this at your own Esplora and your own Zenon node is the
 * difference between verifying a swap and being told about one.
 */
/**
 * Per instance, so a development build and a production build deployed to the
 * same origin do not share a configuration -- the same reason their swaps do not
 * share a storage namespace. Without it, opening the dev build once would leave
 * the production build pointed at regtest and a loopback Esplora, and it would
 * look like it had always been that way.
 */
const STORAGE_KEY = isDev ? 'ferry.dev.settings' : 'ferry.settings'

/** Networks a swap can run on, in the order the picker shows them. */
export const NETWORKS = ['mainnet', 'testnet', 'signet', 'regtest'] as const
export type Network = (typeof NETWORKS)[number]

/**
 * What each network falls back to when no Esplora URL is set. These mirror
 * DefaultEsploraURL in the Go module, which is what actually applies -- they are
 * repeated here only so the settings dialog can show the URL a blank field will
 * use.
 */
export const DEFAULT_ESPLORA: Record<Network, string> = {
  mainnet: 'https://blockstream.info/api',
  testnet: 'https://blockstream.info/testnet/api',
  signet: 'https://mempool.space/signet/api',
  regtest: 'http://127.0.0.1:3002',
}

/**
 * What this instance starts with when nothing has been saved yet.
 *
 * On production there is still no default Zenon node, deliberately: baking one
 * in would make every user trust an endpoint they never chose for the one check
 * where a dishonest answer costs money. Leaving it blank makes the choice
 * visible -- the header says the Zenon side is off, and verification refuses
 * rather than quietly asking a stranger.
 *
 * Development seeds both nodes, because both are on loopback and there is no
 * stranger in `http://127.0.0.1`.
 */
const EMPTY: Settings = DEFAULT_SETTINGS

function read(): Settings {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    // Nothing saved is not the same as something saved and empty. A browser
    // that has never opened this instance gets its defaults; one that has been
    // configured — including one deliberately configured back to blanks — keeps
    // what it was given.
    if (raw === null) return {...EMPTY}
    const v: unknown = JSON.parse(raw || '{}')
    if (!v || typeof v !== 'object') return {...EMPTY}
    const s = v as Partial<Settings>
    return {
      network: (NETWORKS as readonly string[]).includes(s.network ?? '')
        ? (s.network as Network)
        : EMPTY.network,
      btcEsplora: s.btcEsplora ?? '',
      znnUrl: s.znnUrl ?? '',
      relays: s.relays ?? '',
    }
  } catch {
    return {...EMPTY}
  }
}

const stored = ref<Settings>(read())

/**
 * What actually gets sent: only the fields that are set, so an untouched setup
 * sends just the network and the Go side applies its own default for the rest.
 */
const body = computed<Settings>(() => {
  const out: Settings = {network: stored.value.network}
  if (stored.value.btcEsplora) out.btcEsplora = stored.value.btcEsplora
  if (stored.value.znnUrl) out.znnUrl = stored.value.znnUrl
  // `relays` is deliberately absent. The module refuses request fields it does
  // not know, and it has no business knowing which relay carries a conversation
  // it has already sealed.
  return out
})

/** The Esplora instance that will actually be used, for display. */
const effectiveEsplora = computed(
  () => stored.value.btcEsplora || DEFAULT_ESPLORA[stored.value.network as Network] || '',
)

const hasZenon = computed(() => Boolean(stored.value.znnUrl))

function save(next: Settings) {
  stored.value = next
  localStorage.setItem(STORAGE_KEY, JSON.stringify(next))
}

/**
 * The relays this browser will actually use, as a list.
 *
 * Here rather than beside either of the two features that need one, because a
 * session and the board are the same choice made once: whose infrastructure this
 * browser talks to. Two copies of the parsing would be two ways for a typo in
 * the settings field to matter on one screen and not the other.
 *
 * A blank field falls back to the suggested list, which is the whole reason the
 * field can be left blank.
 */
function relayList(): string[] {
  const custom = (stored.value.relays ?? '')
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean)
  return custom.length ? custom : DEFAULT_RELAYS
}

/** Re-read from storage — another tab may have changed it. */
function reload() {
  stored.value = read()
}

export function useSettings() {
  return {stored, body, effectiveEsplora, hasZenon, relayList, save, reload}
}

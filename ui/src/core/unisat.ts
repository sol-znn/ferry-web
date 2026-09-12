import type {Network} from '@/core/composables/useSettings'
import {isDev} from '@/core/env'

/**
 * The UniSat wallet extension, as this app uses it.
 *
 * A swap has exactly two places where a Bitcoin wallet is involved and both are
 * ordinary: an address to be paid out to, and a plain send to the contract
 * address. Neither needs anything a wallet would not do for any website, which
 * is why this connector does not weaken the property the rest of the app rests
 * on -- the swap's own key is generated in the page and never leaves it.
 *
 * So the surface used here is deliberately the small one:
 *
 *   requestAccounts   the connect prompt, and the address to be paid at
 *   getAccounts       the same, without a prompt, for a page that reloads
 *   getChain          which chain the wallet is on, to catch a mismatch
 *   sendBitcoin       fund the contract, which is a normal send to a P2SH
 *   signMessage       prove an address on the board, once per identity
 *
 * Nothing here signs a swap transaction, imports a key or exports one. A wallet
 * that refuses every method above costs the user two copy-pastes, not a swap.
 *
 * signMessage is the newest of the five and the only one a swap does not use. It
 * signs a fixed sentence naming this browser's board key and the address being
 * claimed -- see wasm/board.go -- and it authorises nothing: it moves no coins,
 * commits to no transaction, and is refused by the module unless the recovered
 * key really does hold the address. The magic prefix Bitcoin puts in front of a
 * signed message is what makes that safe to sign at all, because it guarantees
 * the bytes cannot also be a sighash.
 */

/** What the extension injects. Only the parts this app calls are described. */
export interface UnisatProvider {
  requestAccounts(): Promise<string[]>
  getAccounts(): Promise<string[]>
  getPublicKey(): Promise<string>
  getBalance(): Promise<{confirmed: number; unconfirmed: number; total: number}>
  getChain?(): Promise<{enum: string; name: string; network: string}>
  switchChain?(chain: string): Promise<{enum: string; name: string; network: string}>
  getNetwork?(): Promise<string>
  switchNetwork?(network: string): Promise<string>
  sendBitcoin(toAddress: string, satoshis: number, options?: {feeRate?: number}): Promise<string>
  /**
   * Sign a message with the selected address's key.
   *
   * "ecdsa" is asked for explicitly rather than left to the wallet's default. It
   * is the 65-byte recoverable signature `bitcoin-cli signmessage` produces,
   * which the module verifies by recovering the key and deriving every address
   * form it writes. "bip322-simple" is the other option UniSat offers and this
   * app does not take it: it is a serialized transaction rather than a compact
   * signature, and verifying one properly means running a script engine over a
   * virtual input for a gain of nothing here.
   */
  signMessage?(message: string, type?: 'ecdsa' | 'bip322-simple'): Promise<string>
  on?(event: string, handler: (...args: unknown[]) => void): void
  removeListener?(event: string, handler: (...args: unknown[]) => void): void
}

declare global {
  interface Window {
    unisat?: UnisatProvider
    /**
     * Where the development build's testkit puts a stub, because it cannot put
     * one where the extension does: UniSat defines `window.unisat` as
     * non-writable and non-configurable, so a page that wants to stand in for it
     * has nowhere to stand. Read only on the development instance, where the
     * whole expression folds away at build time -- see core/testkit.ts.
     */
    __ferryUnisat?: UnisatProvider
  }
}

export function unisat(): UnisatProvider | undefined {
  if (typeof window === 'undefined') return undefined
  if (isDev && window.__ferryUnisat) return window.__ferryUnisat
  return window.unisat
}

/**
 * The events an extension fires when it finishes injecting itself.
 *
 * A content script runs whenever the browser gets round to it, sometimes after
 * this page has rendered. The convention every injected wallet follows is to
 * announce the fact, so listening turns "reload the page" into "the button
 * works".
 *
 * Listened for on both window and document because extensions have historically
 * dispatched on either. The poll in useUnisat stays regardless: an announcement
 * fired before this page attaches its listener is one no listener can catch.
 */
export const PROVIDER_EVENTS = ['unisat#initialized', 'ethereum#initialized'] as const

/**
 * Wait for a provider to turn up, up to `ms`. This is what the connect button
 * awaits, and the whole of the fix for "install UniSat, press Connect, be told
 * there is no wallet".
 */
export function waitForProvider(ms = 3000): Promise<UnisatProvider | undefined> {
  const found = unisat()
  if (found) return Promise.resolve(found)
  return new Promise((resolve) => {
    let done = false
    const finish = () => {
      if (done) return
      const w = unisat()
      if (!w) return
      done = true
      cleanup()
      resolve(w)
    }
    const timer = setInterval(finish, 100)
    const stop = setTimeout(() => {
      if (done) return
      done = true
      cleanup()
      resolve(unisat())
    }, ms)
    function cleanup() {
      clearInterval(timer)
      clearTimeout(stop)
      for (const ev of PROVIDER_EVENTS) {
        window.removeEventListener(ev, finish)
        document.removeEventListener(ev, finish)
      }
    }
    for (const ev of PROVIDER_EVENTS) {
      window.addEventListener(ev, finish)
      document.addEventListener(ev, finish)
    }
  })
}

/**
 * Which UniSat chain each of this app's networks corresponds to.
 *
 * regtest is absent on purpose rather than by omission: UniSat has no regtest
 * chain, so a developer running against a local node keeps typing addresses,
 * and the UI says so instead of offering a button that cannot work.
 */
export const UNISAT_CHAIN: Partial<Record<Network, string>> = {
  mainnet: 'BITCOIN_MAINNET',
  testnet: 'BITCOIN_TESTNET',
  signet: 'BITCOIN_SIGNET',
}

/** The reverse map, for reading what the wallet reports back. */
export const NETWORK_OF_CHAIN: Record<string, Network> = {
  BITCOIN_MAINNET: 'mainnet',
  BITCOIN_TESTNET: 'testnet',
  BITCOIN_TESTNET4: 'testnet',
  BITCOIN_SIGNET: 'signet',
}

/**
 * Which networks an address could belong to, from its prefix alone. A
 * cross-check on what the wallet says its chain is, not a substitute: testnet and
 * signet share the `tb1`/`m`/`n`/`2` prefixes and cannot be told apart this way.
 * It exists to catch the one mistake that costs real money -- a mainnet address
 * arriving while the app is pointed at a test network, or the reverse.
 */
export function networksForAddress(addr: string): Network[] {
  const a = addr.trim()
  if (!a) return []
  const lower = a.toLowerCase()
  if (lower.startsWith('bcrt1')) return ['regtest']
  if (lower.startsWith('tb1')) return ['testnet', 'signet']
  if (lower.startsWith('bc1')) return ['mainnet']
  // Base58: version byte decides, and the prefix character is enough for it.
  if (/^[13]/.test(a)) return ['mainnet']
  if (/^[mn2]/.test(a)) return ['testnet', 'signet', 'regtest']
  return []
}

/** Whether an address can belong to `network`, as far as its prefix can say. */
export function addressFitsNetwork(addr: string, network: Network): boolean {
  const fits = networksForAddress(addr)
  return fits.length === 0 || fits.includes(network)
}

/** `bc1qpq…x7qv0` — an address short enough to sit in a button. */
export function shortAddr(addr: string): string {
  return addr.length > 16 ? `${addr.slice(0, 8)}…${addr.slice(-5)}` : addr
}

/**
 * Turn whatever the extension threw into something worth reading.
 *
 * A rejected prompt is the common case and is not an error worth a red box:
 * the user closed a window on purpose. UniSat reports it as code 4001, the
 * convention every injected wallet follows.
 */
export function walletError(e: unknown): string {
  const err = e as {code?: number; message?: string} | undefined
  if (err?.code === 4001) return ''
  return err?.message || String(e)
}

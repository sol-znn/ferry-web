import {hasPhantom, useLocalKey, wallet as solWallet} from '@/core/solana'
import {useSettings} from '@/core/composables/useSettings'
import type {Settings} from '@/types'

/**
 * A driver's seat for the development instance, and nothing else.
 *
 * Everything a person does to this app by hand costs a browser automation
 * driver a screenshot, a guess at a coordinate and a click that may have landed
 * somewhere else. Three of those steps stand between an empty tab and the part
 * of the app worth exercising -- the nodes have to be named, three wallets have
 * to be connected, and a dialog has to be answered -- and none of them is what
 * is being tested when the thing under test is the create flow, the board or a
 * leg panel.
 *
 * So this puts the same three things behind function calls. It is installed on
 * the development build only, and it is deliberately thin: it seeds
 * configuration, it installs stub wallet providers, and it clicks buttons by
 * their label. It does not reach inside a component, it does not set a flag any
 * component reads, and it changes no rule -- the gate that asks about the terms
 * still asks, the wallet step still refuses to advance until every chain has an
 * address, and the module still checks every value. What the driver gets is a
 * shorter route to the screen, not a different app once it is there.
 *
 * Two things it will never do, both on purpose:
 *
 *   It cannot sign. The stubs refuse `sendBitcoin`, `sendAccountBlock` and
 *   `signMessage` with a sentence rather than returning a plausible-looking
 *   hash, because a fake txid written into a swap record is worse than no swap
 *   at all -- it is a card that says a leg is funded when nothing is.
 *
 *   It is not on production. `installTestkit` is called from main.ts behind
 *   `isDev`, which is a build-time constant, so this module is dropped from the
 *   production bundle entirely rather than merely left unreachable. Grep dist/
 *   for `__ferry` after a build; the string is not there.
 *
 * Used from a browser console or an automation driver:
 *
 *   __ferry.settings({znnUrl: 'ws://127.0.0.1:35998', solProgram: '...'})
 *   __ferry.wallets()          // stub UniSat + Syrius, then reload
 *   __ferry.solanaKey()        // the throwaway key the dev build already offers
 *   __ferry.click('Connect UniSat')
 *   __ferry.state()
 *   __ferry.reset()
 */

/**
 * Addresses with no relationship to anything, the same ones scripts/smoke.mjs
 * uses. They are valid on their chains, which is the only property that matters
 * here: an address the module rejects would fail a form for a reason the test
 * was not asking about. Where the coins would land is irrelevant, because
 * nothing these stubs touch can move any.
 */
export const FIXTURES = {
  btc: {
    regtest: 'bcrt1q0rymrte6drs2nvjn73mqsl2meud7nv0dy6tgn4',
    mainnet: 'bc1qpqxt3fljkrthfd5mup5sku0s5sy0xlss5x7qv0',
  },
  znn: 'z1qqjnwjjpnue8xmmpanz6csze6tcmtzzdtfsww7',
  znnPeer: 'z1qzal6c5s9rjnnxd2z7dvdhjxpmmj4fmw56a0mz',
  zts: {znn: 'zts1znnxxxxxxxxxxxxx9z4ulx', qsr: 'zts1qsrxxxxxxxxxxxxxmrhjll'},
  solProgram: 'Ha1cRuMYS3vtDrRxx1RcLNRHzcJUeYbYAJBqLsFwFvbe',
} as const

/**
 * What the stubs are configured with, kept in storage rather than in memory.
 *
 * A provider that appears halfway through a page's life is the awkward case in
 * every one of these connectors -- they poll for it, they listen for its
 * announcement, and they give up after a while. A real extension does not have
 * that problem because it is there before the first frame, so the stubs are
 * installed the same way: written down once, then put on `window` by
 * `installTestkit` at startup, before Vue mounts. `wallets()` therefore asks for
 * a reload, and says so in what it returns.
 */
const CONFIG_KEY = 'ferry.dev.testkit'

interface StubConfig {
  btc?: string
  znn?: string
  /**
   * Base URL of a CLI wallet daemon. When set, the Zenon provider stops being a
   * stub: `sendAccountBlock` posts the block there, where a real ed25519 key
   * signs it, mines the plasma the node asks for and publishes it. What comes
   * back is a hash off a chain, not a fixture.
   */
  cli?: string
  /**
   * Which of the daemon's keys this instance is. A ZTS⇄ZTS swap has a Zenon leg
   * on both sides, so the two browsers have to be two accounts — one key
   * creating an HTLC and unlocking it is a swap with itself, and the rules that
   * only bite between strangers never fire. Defaults to 3, the funded account.
   */
  znnIndex?: number
  /** What the CLI wallet signs for, read from the daemon. Ferry refuses to build
   *  a block unless the wallet can say both — a signature for chain 1 published
   *  to chain 69 is a valid signature over the wrong thing. */
  chainId?: number
  nodeUrl?: string
  /** What the UniSat stub reports for getChain, so a network mismatch can be
   *  provoked on purpose as well as avoided by accident. */
  btcChain?: string
}

function readConfig(): StubConfig | null {
  try {
    const raw = localStorage.getItem(CONFIG_KEY)
    if (!raw) return null
    const v: unknown = JSON.parse(raw)
    return v && typeof v === 'object' ? (v as StubConfig) : null
  } catch {
    return null
  }
}

/**
 * What the UniSat stub says it is on.
 *
 * UniSat names a chain per network and has no name for regtest — so on the
 * development instance the stub reports a name the app's own map does not know,
 * which reads as "no opinion" rather than as mainnet. Claiming mainnet on a
 * regtest build lit the mismatch warning and offered to switch a wallet that
 * does not exist.
 */
const BTC_CHAIN_OF_NETWORK: Record<string, string> = {
  mainnet: 'BITCOIN_MAINNET',
  testnet: 'BITCOIN_TESTNET',
  signet: 'BITCOIN_SIGNET',
  regtest: 'BITCOIN_REGTEST',
}
const DEFAULT_BTC_CHAIN = 'BITCOIN_MAINNET'

function refuse(what: string): Promise<never> {
  return Promise.reject(
    new Error(
      `the testkit's ${what} stub cannot sign or send. Connect a real wallet for anything that ` +
        'moves money — this stub exists to get past the wallet step, not through it.',
    ),
  )
}

/** A stub of the UniSat provider, covering the five methods core/unisat.ts calls. */
function unisatStub(addr: string, chain: string) {
  const chainInfo = {
    enum: chain,
    name: chain,
    network: chain.includes('MAINNET') ? 'livenet' : 'testnet',
  }
  return {
    requestAccounts: () => Promise.resolve([addr]),
    getAccounts: () => Promise.resolve([addr]),
    // Not a real key for this address, and nothing in the app derives anything
    // from it: the one call that would — a board proof — is refused below.
    getPublicKey: () => Promise.resolve('02'.padEnd(66, '0')),
    getBalance: () => Promise.resolve({confirmed: 0, unconfirmed: 0, total: 0}),
    getChain: () => Promise.resolve(chainInfo),
    switchChain: () => Promise.resolve(chainInfo),
    sendBitcoin: () => refuse('UniSat'),
    signMessage: () => refuse('UniSat'),
    on: () => undefined,
    removeListener: () => undefined,
  }
}

/** A stub of the Syrius provider, in the shape core/zenon-wallet.ts probes for:
 *  `connect` and `sendAccountBlock` are what it checks before believing there
 *  is a wallet at all. */
function zenonStub(addr: string, cli?: string, chainId = 1, nodeUrl = '', znnIndex = 3) {
  /**
   * The one method that moves anything.
   *
   * Without a daemon it refuses, loudly. With one it hands the block over to a
   * key that owns coins on the devnet — and takes back a hash, which for an
   * `htlc.Create` is also the HTLC's id. The page cannot tell the difference
   * between this and Syrius, which is the point: the block that gets signed is
   * the block the app built.
   */
  const send = cli
    ? async (block: unknown) => {
        const res = await fetch(`${cli}/znn/send`, {
          method: 'POST',
          headers: {'content-type': 'application/json'},
          body: JSON.stringify({block, index: znnIndex}),
        })
        const out = (await res.json()) as {hash?: string; error?: string}
        if (!res.ok || out.error) throw new Error(out.error ?? `wallet daemon said ${res.status}`)
        return {hash: out.hash, block: {hash: out.hash}}
      }
    : () => refuse('Syrius')

  return {
    isSyriusExtension: true,
    isZenon: true,
    version: 2,
    accounts: [addr],
    chainId,
    connect: () => Promise.resolve([addr]),
    disconnect: () => Promise.resolve(true),
    getAccounts: () => Promise.resolve([addr]),
    getChainId: () => Promise.resolve(chainId),
    getNodeUrl: () => Promise.resolve(nodeUrl || null),
    sendAccountBlock: send,
    signMessage: () => refuse('Syrius'),
    on: () => undefined,
    removeListener: () => undefined,
  }
}

declare global {
  interface Window {
    __ferry?: ReturnType<typeof api>
  }
}

/** Whether a modal is genuinely up — open, not merely still in the document. */
function modalOpen(): boolean {
  return document.querySelector('[role="dialog"][data-state="open"]') !== null
}

/**
 * Whether a control is one a person could actually press right now.
 *
 * Three rules, and the third one is the whole lesson of writing this.
 *
 * A dialog is unmounted when its close animation ends, and **a browser does not
 * animate a tab that is not in front**. A driver working in a background tab
 * therefore leaves every dialog it closes sitting in the document forever, in
 * `data-state="closed"`, with the rest of the page still marked `aria-hidden`
 * and `<body>` still holding `pointer-events: none`. Nothing is wrong with the
 * app — bring the tab to the front and it finishes in a heartbeat — but a driver
 * that reads `aria-hidden` literally concludes that the only thing on screen is
 * the dialog it just dismissed, and then waits for a page that is, as far as it
 * can tell, not there.
 *
 * So: a closed dialog's own controls never count, and `aria-hidden` disqualifies
 * a control only while a modal is really open. Everything else is the ordinary
 * test — not disabled, and a box with area.
 */
function clickable(el: HTMLElement): boolean {
  if (el.hasAttribute('disabled') || el.getAttribute('aria-disabled') === 'true') return false
  if (el.closest('[inert], [hidden]')) return false
  // A dialog on its way out, and only a dialog: `data-state="closed"` is also
  // what every select trigger on the page says while its popup is shut, so
  // excluding the attribute outright hid every dropdown in the app.
  if (el.closest('[role="dialog"][data-state="closed"], [role="alertdialog"][data-state="closed"]'))
    return false
  if (modalOpen() && el.closest('[aria-hidden="true"]')) return false
  const box = el.getBoundingClientRect()
  return box.width > 0 && box.height > 0
}

/**
 * Every control a person could press right now, in document order.
 *
 * Options are in the list because this app's selects are not `<select>`: the
 * design system builds them out of a button and a popup of `[role="option"]`,
 * and picking one is a click like any other.
 */
function reachable(): HTMLElement[] {
  const all = Array.from(
    document.querySelectorAll<HTMLElement>(
      'button, a, [role="button"], [role="option"], [role="menuitem"]',
    ),
  )
  return all.filter(clickable)
}

/**
 * How long a click or a wait keeps looking. Generous because a driver often
 * works in a tab that is not in front, where the browser throttles the polling
 * timer to about a second — three seconds of wall clock can be three attempts.
 */
const DEFAULT_TIMEOUT = 8000

/**
 * The control a visible label points at.
 *
 * `for`/`id` first, because that is the association the app actually writes and
 * the one a screen reader follows; a label wrapping its control is the fallback.
 * Matching is on a substring so a caller can say "Your Solana address" without
 * copying the units and hint text that ride along in some labels.
 */
const forId = (l: Element) => l.getAttribute('for')

/**
 * The event sequence a mouse actually produces.
 *
 * `.click()` alone dispatches one `click` and nothing else, which is enough for
 * a plain button and not enough for anything the design system builds: its
 * selects open on `pointerdown` and commit a choice on `pointerup`, so a driver
 * that only clicks opens no popup and picks no option — while reporting that it
 * pressed exactly what was asked for.
 */
/**
 * Wait for focus to move, or for a moment to pass.
 *
 * The listbox moves DOM focus onto an option, both when it opens and on every
 * arrow key, so `focusin` is the signal that the highlight has actually moved.
 * Listening for it rather than sleeping matters for the same reason it did in
 * `find`: in a background tab the timer that a sleep is made of runs about once
 * a second, and a select with four options would cost four seconds to walk. The
 * timer here is only the escape hatch for a control that moves no focus at all.
 */
function focusMoved(timeout = 1000): Promise<void> {
  return new Promise((resolve) => {
    const done = () => {
      document.removeEventListener('focusin', done)
      clearTimeout(timer)
      resolve()
    }
    const timer = setTimeout(done, timeout)
    document.addEventListener('focusin', done)
  })
}

/** A keypress on whatever the popup has focused. */
function key(name: string): void {
  const target = document.activeElement ?? document.body
  for (const type of ['keydown', 'keyup'] as const) {
    target.dispatchEvent(
      new KeyboardEvent(type, {key: name, code: name, bubbles: true, cancelable: true}),
    )
  }
}

function press(el: HTMLElement): void {
  for (const type of ['pointerdown', 'mousedown', 'pointerup', 'mouseup'] as const) {
    el.dispatchEvent(
      new PointerEvent(type, {bubbles: true, cancelable: true, button: 0, isPrimary: true}),
    )
  }
  el.click()
}

function labelled(text: string): HTMLElement {
  const wanted = text.trim().toLowerCase()
  const labels = Array.from(document.querySelectorAll('label'))
  const hit = labels.find((l) => (l.textContent ?? '').trim().toLowerCase().includes(wanted))
  if (!hit) {
    const seen = labels.map((l) => (l.textContent ?? '').trim()).filter(Boolean)
    throw new Error(`no label matching ${JSON.stringify(text)}. On screen: ${seen.join(' | ')}`)
  }
  // `for` first, then the field the label sits with. A plain `button` inside the
  // label is deliberately NOT a candidate: several labels here carry an InfoTip,
  // which is a button, and picking it means opening a tooltip and reporting that
  // the select never opened.
  const REAL = 'input, textarea, select, [role="combobox"]'
  const sibling = hit.nextElementSibling
  const el =
    (forId(hit) ? document.getElementById(forId(hit) as string) : null) ??
    hit.querySelector<HTMLElement>(REAL) ??
    (sibling instanceof HTMLElement && sibling.matches(REAL) ? sibling : null) ??
    hit.parentElement?.querySelector<HTMLElement>(REAL) ??
    null
  if (!el) throw new Error(`label ${JSON.stringify(text)} points at no control`)
  return el as HTMLElement
}

/** The same, narrowed to something that holds text. */
function field(text: string): HTMLInputElement | HTMLTextAreaElement {
  const el = labelled(text)
  if (!(el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement)) {
    throw new Error(`${JSON.stringify(text)} is not a text field — use choose() for a select`)
  }
  return el
}

const labelOf = (el: HTMLElement) => (el.textContent ?? '').trim().replace(/\s+/g, ' ')

/**
 * Wait for a reachable control whose label matches, and hand it back.
 *
 * Exact match wins over a substring, because a dialog's "Decline" and a page's
 * "Decline all offers" are two different answers and document order is not a
 * good way to choose between them.
 *
 * It watches rather than polls, and the reason is the same background tab that
 * shaped `clickable`. A browser throttles `setTimeout` in a tab that is not in
 * front to roughly one call a second, so a poll loop that reads as "check every
 * 50ms" actually checks once a second — and a driver pressing six buttons then
 * spends the better part of a minute waiting on renders that finished
 * immediately. A MutationObserver is not throttled: it fires when the DOM
 * changes, which is exactly when a control might have appeared or become
 * enabled. Attributes are watched too, because `disabled`, `data-state` and
 * `aria-hidden` flipping is how most controls here become reachable without any
 * node being added.
 */
function find(text: string, timeout: number): Promise<HTMLElement> {
  const wanted = text.trim().toLowerCase()
  const attempt = (): HTMLElement | null => {
    const here = reachable()
    return (
      here.find((el) => labelOf(el).toLowerCase() === wanted) ??
      here.find((el) => labelOf(el).toLowerCase().includes(wanted)) ??
      null
    )
  }

  const now = attempt()
  if (now) return Promise.resolve(now)

  return new Promise((resolve, reject) => {
    let settled = false
    const stop = () => {
      settled = true
      observer.disconnect()
      clearTimeout(timer)
    }
    const observer = new MutationObserver(() => {
      if (settled) return
      const hit = attempt()
      if (hit) {
        stop()
        resolve(hit)
      }
    })
    const timer = setTimeout(() => {
      if (settled) return
      stop()
      const seen = reachable().map(labelOf).filter(Boolean)
      reject(
        new Error(
          `no control matching ${JSON.stringify(text)} after ${timeout}ms. ` +
            `Reachable: ${seen.join(' | ') || '(nothing — a modal may still be closing)'}`,
        ),
      )
    }, timeout)
    observer.observe(document.documentElement, {
      subtree: true,
      childList: true,
      attributes: true,
      attributeFilter: [
        'disabled',
        'aria-disabled',
        'aria-hidden',
        'data-state',
        'hidden',
        'style',
      ],
    })
  })
}

/**
 * Put the stubs on the window, if any are configured.
 *
 * They go on `__ferryUnisat` and `__ferryZenon` rather than on the names the
 * extensions use, and that is not a matter of taste. Both extensions define
 * their global as non-writable and non-configurable, so on a browser that has
 * either one installed — the browser somebody is most likely to be testing in —
 * assigning to `window.unisat` throws and `defineProperty` is refused. The two
 * connectors read the testkit's name first, on the development build only.
 *
 * Called before Vue mounts so the providers are present for the first frame,
 * and again from `wallets()` for the case where a driver would rather not
 * reload — the announcement events cover the connectors that listen for one.
 */
function installStubs(): StubConfig | null {
  const cfg = readConfig()
  if (!cfg) return null
  if (cfg.btc) {
    window.__ferryUnisat = unisatStub(cfg.btc, cfg.btcChain ?? DEFAULT_BTC_CHAIN)
    // What a returning user's browser holds: the flag that lets the app call
    // getAccounts without a prompt. Set here so the stub connects the way a
    // remembered wallet does, rather than needing a click the first time.
    try {
      localStorage.setItem('ferry.unisat.connected', '1')
    } catch {
      /* a browser that will not remember still connects on a click */
    }
  }
  if (cfg.znn) {
    window.__ferryZenon = zenonStub(cfg.znn, cfg.cli, cfg.chainId, cfg.nodeUrl, cfg.znnIndex)
    window.dispatchEvent(new Event('zenon#initialized'))
  }
  return cfg
}

function api() {
  const {stored, save} = useSettings()

  return {
    /**
     * Read or patch the nodes and network, the same values the Nodes dialog
     * writes. A patch is merged, so naming one field leaves the rest alone.
     */
    settings(patch?: Partial<Settings>): Settings {
      if (patch) save({...stored.value, ...patch})
      return {...stored.value}
    },

    /**
     * Install the stub wallets. Returns what was configured and whether a
     * reload is wanted — it is, for the reason in CONFIG_KEY's note above.
     */
    async wallets(opts: StubConfig = {}): Promise<{config: StubConfig; reload: string}> {
      const network = stored.value.network
      // With a daemon, the Zenon address is not a fixture: it is whichever
      // account that wallet actually holds the key for, because a swap paying
      // an address nobody can spend from is a swap that proves nothing.
      let znn = opts.znn
      let chainId = opts.chainId
      let nodeUrl = opts.nodeUrl
      const znnIndex = opts.znnIndex ?? 3
      if (opts.cli) {
        if (!znn) {
          const res = await fetch(`${opts.cli}/znn/accounts`)
          const list = (await res.json()) as {index: number; address: string}[]
          const at = list.find((a) => a.index === znnIndex)
          if (!at) {
            throw new Error(
              `the wallet daemon holds no account at index ${znnIndex} — it has ` +
                `${list.map((a) => a.index).join(', ')}`,
            )
          }
          znn = at.address
        }
        const info = (await (await fetch(`${opts.cli}/znn/info`)).json()) as {
          chainId: number
          rpcUrl: string
        }
        chainId = chainId ?? info.chainId
        nodeUrl = nodeUrl ?? info.rpcUrl
      }
      const cfg: StubConfig = {
        btc: opts.btc ?? (network === 'regtest' ? FIXTURES.btc.regtest : FIXTURES.btc.mainnet),
        znn: znn ?? FIXTURES.znn,
        btcChain: opts.btcChain ?? BTC_CHAIN_OF_NETWORK[network] ?? DEFAULT_BTC_CHAIN,
        ...(opts.cli ? {cli: opts.cli, znnIndex} : {}),
        ...(chainId === undefined ? {} : {chainId}),
        ...(nodeUrl === undefined ? {} : {nodeUrl}),
      }
      localStorage.setItem(CONFIG_KEY, JSON.stringify(cfg))
      installStubs()
      return {
        config: cfg,
        reload: 'call location.reload() — providers are read once, before the first frame',
      }
    },

    /**
     * The CLI wallet daemon, for the things a page cannot do for itself:
     * paying a Bitcoin contract address and mining the confirmation that makes
     * the payment visible. Both are `bitcoin-cli` on a regtest node.
     */
    cli: {
      async call(path: string, body?: unknown): Promise<unknown> {
        const base = readConfig()?.cli
        if (!base) throw new Error('no CLI wallet configured — pass {cli} to wallets()')
        const res = await fetch(base + path, {
          method: body === undefined ? 'GET' : 'POST',
          ...(body === undefined
            ? {}
            : {headers: {'content-type': 'application/json'}, body: JSON.stringify(body)}),
        })
        const out: unknown = await res.json()
        if (!res.ok) throw new Error(JSON.stringify(out))
        return out
      },
      health() {
        return this.call('/health')
      },
      fund(address: string, sats: number | string) {
        return this.call('/btc/fund', {address, sats: String(sats)})
      },
      mine(blocks = 1) {
        return this.call('/btc/mine', {blocks})
      },
      htlc(id: string) {
        return this.call(`/znn/htlc?id=${encodeURIComponent(id)}`)
      },
    },

    /** The throwaway Solana key the dev build already offers behind a button,
     *  as a call. Unlike the two stubs this one is real and can sign. */
    solanaKey(): string {
      return useLocalKey()
    },

    /**
     * Click the control whose visible text matches, once there is one.
     *
     * Buttons are what this app is driven by and their labels are stable in a
     * way coordinates are not — a resized window moves every pixel and renames
     * nothing.
     *
     * It waits rather than fires, and that is not politeness: a dialog closing
     * leaves the rest of the page marked `aria-hidden` until its animation ends,
     * so a click sent on a fixed delay lands either on a control that is not
     * there yet or on one the app currently considers unreachable. Polling for
     * the control to become clickable makes the caller's script a list of what
     * it wants to press, with no sleeps in it and no guesses about how long a
     * transition takes. Rejects with what WAS reachable, which is a better
     * failure than a click into empty space.
     */
    async click(text: string, opts: {timeout?: number} = {}): Promise<string> {
      const hit = await find(text, opts.timeout ?? DEFAULT_TIMEOUT)
      // An option is not a button: it commits on pointerup, so it needs the
      // whole sequence. A plain control is left with the plain click, which is
      // what every other button on the page already answers to.
      if (hit.matches('[role="option"], [role="menuitem"]')) press(hit)
      else hit.click()
      // Let Vue flush. Its scheduler runs on the microtask queue, so yielding
      // to that is enough and — unlike a timer or a frame — is not throttled in
      // a tab that is not in front. Anything slower than a render is picked up
      // by the next call's observer.
      await Promise.resolve()
      return labelOf(hit)
    },

    /**
     * Type a value into the field a label names.
     *
     * Vue listens for `input`, so setting `.value` alone updates the pixels and
     * not the model — the form would then submit what it had before, which is
     * the kind of green test that is worse than none. The event is dispatched
     * with the native setter so the framework sees a real change.
     */
    fill(label: string, value: string): string {
      const el = field(label)
      const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement : HTMLInputElement
      const setter = Object.getOwnPropertyDescriptor(proto.prototype, 'value')?.set
      setter?.call(el, value)
      el.dispatchEvent(new Event('input', {bubbles: true}))
      el.dispatchEvent(new Event('change', {bubbles: true}))
      return el.value
    },

    /**
     * Open the select a label names and pick the option that matches.
     *
     * Opened with a pointer sequence and chosen with the keyboard, which is not
     * a mixture for its own sake. These selects commit a choice from a trusted
     * pointer event, and a synthetic one — however complete the sequence — moves
     * the highlight and selects nothing, while reporting that the option was
     * pressed. The keyboard path has no such requirement: arrow to the option
     * the listbox says is focused, then Enter, which is also how somebody
     * without a mouse uses this control.
     */
    async choose(label: string, option: string): Promise<string> {
      const trigger = labelled(label)
      // Opened from the keyboard, not the mouse. A pointer sequence opens the
      // popup on `pointerdown` and the `click` that completes it can toggle the
      // same popup straight back shut — which shows up as an intermittent
      // "Options:" with nothing after it. Enter on a focused trigger has no
      // such second half. The pointer path stays as the fallback for a control
      // that ignores the keyboard.
      trigger.focus()
      key('Enter')
      const opened = await find(option, 1000).then(
        () => true,
        () => false,
      )
      if (!opened) {
        press(trigger)
        await find(option, DEFAULT_TIMEOUT)
      }

      const wanted = option.trim().toLowerCase()
      const options = () => Array.from(document.querySelectorAll<HTMLElement>('[role="option"]'))
      const focused = () => {
        const active = document.activeElement
        return active instanceof HTMLElement && active.matches('[role="option"]') ? active : null
      }
      const onIt = () => {
        const f = focused()
        return f !== null && labelOf(f).toLowerCase().includes(wanted)
      }

      // The listbox moves focus onto an option itself, and not in the tick it
      // was opened in — so wait for that before sending a key, or every arrow
      // goes to the body and the highlight never moves.
      if (!focused()) await focusMoved()

      // Down through the list, then back up: whichever end it opened on, the
      // option is reachable within one pass. Each press is followed by the
      // focus move it causes, which is what makes this quick rather than a walk
      // through four throttled sleeps.
      const count = options().length
      for (const step of ['ArrowDown', 'ArrowUp'] as const) {
        for (let i = 0; i <= count && !onIt(); i++) {
          key(step)
          await focusMoved()
        }
        if (onIt()) break
      }
      if (!onIt()) {
        throw new Error(
          `${JSON.stringify(option)} is not reachable in ${JSON.stringify(label)}. ` +
            `Options: ${options().map(labelOf).join(' | ')}`,
        )
      }
      key('Enter')
      // Closing hands focus back to the trigger, so waiting for that move is
      // both the signal the choice landed and cheaper than a throttled sleep.
      await focusMoved(300)
      return labelOf(trigger)
    },

    /**
     * Wait for a control to become reachable without pressing it — the way to
     * assert that a screen arrived, rather than sleeping and hoping.
     */
    async waitFor(text: string, opts: {timeout?: number} = {}): Promise<string> {
      return labelOf(await find(text, opts.timeout ?? DEFAULT_TIMEOUT))
    },

    /** Every control a person could press right now. What to read when a click
     *  failed and the question is what the page thinks it is showing. */
    reachable(): string[] {
      return reachable().map(labelOf).filter(Boolean)
    },

    /** What this browser currently is, in one object: enough to assert against
     *  without a screenshot. */
    state() {
      const keys: string[] = []
      for (let i = 0; i < localStorage.length; i++) {
        const k = localStorage.key(i)
        if (k?.startsWith('ferry.')) keys.push(k)
      }
      return {
        route: location.hash,
        settings: {...stored.value},
        stubs: readConfig(),
        providers: {
          unisat: Boolean(window.__ferryUnisat ?? window.unisat),
          zenon: Boolean(window.__ferryZenon ?? window.zenon),
          stubbed: {
            unisat: Boolean(window.__ferryUnisat),
            zenon: Boolean(window.__ferryZenon),
          },
          phantom: hasPhantom(),
        },
        solana: {kind: solWallet.kind, address: solWallet.address},
        storage: keys.sort(),
      }
    },

    /**
     * Put this instance back to a first run: every `ferry.` key this build
     * owns, the stub configuration included. Settings are cleared too unless
     * asked otherwise, since the usual reason to reset is to see what somebody
     * arriving for the first time sees.
     */
    reset(opts: {keepSettings?: boolean} = {}): string[] {
      const kept = opts.keepSettings ? {...stored.value} : null
      const dropped: string[] = []
      for (let i = localStorage.length - 1; i >= 0; i--) {
        const k = localStorage.key(i)
        if (!k?.startsWith('ferry.')) continue
        localStorage.removeItem(k)
        dropped.push(k)
      }
      delete window.__ferryUnisat
      delete window.__ferryZenon
      if (kept) save(kept)
      return dropped.sort()
    },

    FIXTURES,
  }
}

/**
 * Install the harness. Called from main.ts on the development build only, and
 * before the app mounts, so a configured stub provider is on the page for the
 * first frame the way a real extension would be.
 */
export function installTestkit(): void {
  installStubs()
  window.__ferry = api()
}

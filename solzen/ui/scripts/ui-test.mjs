// Settles a swap by clicking the actual page, in two browsers.
//
// Everything else here tests the engine. This tests the thing a person
// touches -- and it uses two separate browser profiles rather than two tabs,
// because storage is per profile and two tabs would be one participant holding
// the swap twice. That is not a testing detail; it is what makes the
// counterparty a counterparty, and it is the same instruction a real pair of
// users gets.
//
//   npm run dev            # in another terminal
//   node scripts/ui-test.mjs [sol2znn|znn2sol] [--pow] [--phantom]
//
// By default plasma is fused to both swap addresses, so every Zenon block
// publishes at once and a run takes a few minutes. `--pow` fuses only the
// sending side's, leaving the receiving side to mine its unlock in the browser
// the way a user with no QSR would -- about four minutes of hashing, and the
// only way to exercise that path end to end.
//
// The Solana side uses the page's "key in this browser" mode by default: a real
// extension cannot be installed into a throwaway profile from here. `--phantom`
// runs the same swap through the page's wallet connector instead, against a
// provider shaped like Phantom's whose key lives in this script rather than in
// the page. That is not Phantom -- its popup, its cluster setting and its
// simulation warnings are only reachable by hand -- but it is the page's own
// connect, sign and submit path, which is otherwise never exercised.

import { execFileSync, spawnSync } from 'node:child_process'
import { createPrivateKey, sign as signRaw } from 'node:crypto'
import { existsSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { Keypair } from '@solana/web3.js'
import puppeteer from 'puppeteer-core'

const here = dirname(fileURLToPath(import.meta.url))
const root = join(here, '../..')
const URL_ = process.env.SOLZEN_UI ?? 'http://127.0.0.1:5188/'
const args = process.argv.slice(2)
const MINE_IN_BROWSER = args.includes('--pow')
const USE_PHANTOM = args.includes('--phantom')
const DIRECTION = args.find((a) => !a.startsWith('--')) ?? 'sol2znn'
const exe = process.platform === 'win32' ? '.exe' : ''
const walletBin = join(root, 'bin', `devnet-wallet${exe}`)

const CHROME = [
  process.env.CHROME_PATH,
  'C:/Program Files/Google/Chrome/Application/chrome.exe',
  'C:/Program Files (x86)/Google/Chrome/Application/chrome.exe',
  '/usr/bin/google-chrome',
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
].find((p) => p && existsSync(p))
if (!CHROME) {
  console.error('no Chrome found; set CHROME_PATH')
  process.exit(2)
}

let failures = 0
const step = (s) => console.log(`\n=== ${s}`)
function check(name, ok, detail = '') {
  console.log(`${ok ? '  ok  ' : ' FAIL '} ${name}${detail ? ` -- ${detail}` : ''}`)
  if (!ok) failures++
}

function wallet(...args) {
  const r = spawnSync(walletBin, args, { encoding: 'utf8' })
  if (r.status !== 0) throw new Error(`devnet-wallet ${args.join(' ')}: ${r.stderr || r.stdout}`)
  return r.stdout.trim()
}

// --- driving the page ------------------------------------------------------
//
// Selection is by visible text rather than by test ids. A test id can go on
// drifting while the button it names stops saying what it does; matching the
// words a user reads means the test fails when the page stops making sense.

// Matching is case-insensitive throughout, because innerText applies CSS
// text-transform: several labels here are uppercased in the stylesheet and
// sentence case in the source, and a test that cares about the difference is
// testing the stylesheet.
async function clickText(page, text, tag = 'button') {
  const handle = await page.evaluateHandle(
    (t, g) => [...document.querySelectorAll(g)].find((e) => e.textContent.trim().toLowerCase().includes(t.toLowerCase())),
    text,
    tag,
  )
  const el = handle.asElement()
  if (!el) throw new Error(`no <${tag}> saying "${text}"`)
  await el.click()
}

async function fillLabelled(page, labelText, value) {
  const ok = await evaluate(
    page,
    (t, v) => {
      const label = [...document.querySelectorAll('label')].find((l) => l.textContent.includes(t))
      const field = label?.querySelector('input, textarea')
      if (!field) return false
      const setter = Object.getOwnPropertyDescriptor(
        field instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype,
        'value',
      ).set
      setter.call(field, v)
      field.dispatchEvent(new Event('input', { bubbles: true }))
      field.dispatchEvent(new Event('blur', { bubbles: true }))
      return true
    },
    labelText,
    value,
  )
  if (!ok) throw new Error(`no field labelled "${labelText}"`)
}

// Every read of a page goes through here so that a page whose JavaScript has
// stopped responding is reported as that, rather than as a hang. Screenshots go
// through a different CDP path than script evaluation, so one succeeding while
// the other times out is the signature of a blocked main thread -- which is a
// bug in the page, and worth saying so.
async function evaluate(page, fn, ...args) {
  let timer
  const timeout = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error('the page did not answer within 30s -- its main thread is blocked')), 30000)
  })
  try {
    return await Promise.race([page.evaluate(fn, ...args), timeout])
  } finally {
    clearTimeout(timer)
  }
}

const text = (page) => evaluate(page, () => document.body.innerText)
const has = async (page, needle) => (await text(page)).toLowerCase().includes(needle.toLowerCase())

async function waitForText(page, needle, { timeout = 120000 } = {}) {
  const started = Date.now()
  for (;;) {
    if (await has(page, needle)) return
    if (Date.now() - started > timeout) {
      throw new Error(`timed out waiting for "${needle}"\n---\n${(await text(page)).slice(0, 2500)}`)
    }
    await new Promise((r) => setTimeout(r, 1000))
  }
}

// The page only reads the chains when asked, which is deliberate: a swap is a
// thing you check, not a thing that animates. So the test presses Refresh the
// way a person would.
async function refreshUntil(page, predicate, describe, { tries = 40 } = {}) {
  for (let i = 0; i < tries; i++) {
    await clickText(page, 'Refresh from chains')
    await new Promise((r) => setTimeout(r, 1500))
    if (await predicate()) return
  }
  throw new Error(`refreshed ${tries} times without ${describe}\n---\n${(await text(page)).slice(0, 2500)}`)
}

// Actions are found by what they mean rather than by their label. The label is
// what the assertions read; the kind is what the page is driven through, and
// conflating the two makes a test that passes on a page saying the wrong thing
// in the right words.
const offers = (page, kind) =>
  evaluate(page, (k) => !!document.querySelector(`[data-action="${k}"][data-ready="true"]`), kind)

async function doAction(page, kind) {
  const clicked = await evaluate(page, (k) => {
    const btn = document.querySelector(`[data-action="${k}"][data-ready="true"] button`)
    if (!btn) return false
    btn.click()
    return true
  }, kind)
  if (!clicked) throw new Error(`the page is not offering "${kind}"\n---\n${(await text(page)).slice(0, 2000)}`)
}

// A Zenon operation publishes a block and then waits for the contract to
// process it; a Solana one waits for confirmation. Either way the button is
// disabled and relabelled until it finishes, so waiting for it to come back is
// waiting for the operation.
async function waitIdle(page, { timeout = 900000, label = '' } = {}) {
  const started = Date.now()
  for (;;) {
    const busy = await evaluate(page, () =>
      [...document.querySelectorAll('button')].some((b) => b.textContent.includes('working')),
    )
    if (!busy) {
      const seconds = Math.round((Date.now() - started) / 1000)
      if (label && seconds > 5) console.log(`       ${label} took ${seconds}s`)
      return
    }
    if (Date.now() - started > timeout) throw new Error('an action did not finish')
    await new Promise((r) => setTimeout(r, 1000))
  }
}

// An ed25519 private key in the form node's crypto wants, from the 32-byte seed
// a Solana keypair is built on. The prefix is the PKCS#8 header for ed25519 --
// fixed bytes, so the whole conversion is a concatenation.
const ED25519_PKCS8_PREFIX = Buffer.from('302e020100300506032b657004220420', 'hex')

// A provider shaped like Phantom's, installed before the page's own scripts run.
//
// The key is held here, out of the page's reach, which is the property that
// matters: the page has to ask something else to sign, exactly as it would with
// a real extension. What it proves is the page's half -- detection, connect,
// signTransaction, and submitting the signed transaction to the node the swap
// is against rather than to a cluster the extension chose.
async function installPhantomStub(page, keypair) {
  const key = createPrivateKey({
    key: Buffer.concat([ED25519_PKCS8_PREFIX, Buffer.from(keypair.secretKey.slice(0, 32))]),
    format: 'der',
    type: 'pkcs8',
  })
  await page.exposeFunction('__solzenStubSign', (messageB64) =>
    signRaw(null, Buffer.from(messageB64, 'base64'), key).toString('base64'),
  )
  await page.evaluateOnNewDocument((address) => {
    const listeners = {}
    const emit = (event, ...rest) => (listeners[event] ?? []).forEach((fn) => fn(...rest))
    const publicKey = () => ({ toString: () => address, toBase58: () => address })
    let connected = false
    window.phantom = {
      solana: {
        isPhantom: true,
        get publicKey() {
          return connected ? publicKey() : null
        },
        async connect(opts) {
          // Phantom remembers which origins it trusts, across reloads, and
          // onlyIfTrusted is the page asking to reconnect without a popup.
          // Refusing that on a first visit is what Phantom does, and the page
          // has to treat the refusal as ordinary rather than as an error worth
          // showing. sessionStorage is what makes trust outlive a reload here.
          const trusted = (() => {
            try {
              return sessionStorage.getItem('stub.trusted') === '1'
            } catch {
              return false
            }
          })()
          if (opts?.onlyIfTrusted && !trusted) {
            throw Object.assign(new Error('the origin is not trusted'), { code: 4001 })
          }
          connected = true
          try {
            sessionStorage.setItem('stub.trusted', '1')
          } catch {
            /* the run still works; only the reload check needs this */
          }
          emit('connect', publicKey())
          return { publicKey: publicKey() }
        },
        async disconnect() {
          connected = false
          try {
            sessionStorage.removeItem('stub.trusted')
          } catch {
            /* nothing to forget */
          }
          emit('disconnect')
        },
        on(event, fn) {
          ;(listeners[event] ??= []).push(fn)
        },
        off(event, fn) {
          listeners[event] = (listeners[event] ?? []).filter((f) => f !== fn)
        },
        // Sign only. A wallet that also submits would send this to its own
        // cluster; the page is supposed to prefer this call for that reason.
        async signTransaction(tx) {
          const message = tx.serializeMessage()
          const signature = await window.__solzenStubSign(
            btoa(String.fromCharCode(...new Uint8Array(message))),
          )
          tx.addSignature(tx.feePayer, Uint8Array.from(atob(signature), (c) => c.charCodeAt(0)))
          return tx
        },
      },
    }
    // The two things a person does in the extension rather than in the page.
    // A real wallet raises these from its own UI, which a test cannot reach.
    window.__stubSwitchAccount = (next) => {
      address = next
      emit('accountChanged', publicKey())
    }
    window.__stubHangUp = () => {
      connected = false
      emit('disconnect')
    }
    window.dispatchEvent(new Event('phantom#initialized'))
  }, keypair.publicKey.toBase58())
}

// watchForMining reports whether the page put up its mining panel.
//
// The panel is bound to the engine's own answer about whether this block needs
// work, so seeing it is seeing the page decide to mine -- not an inference from
// how long something took. It is polled rather than awaited because the action
// it belongs to is already in flight.
async function watchForMining(page, { timeout = 30000 } = {}) {
  const until = Date.now() + timeout
  while (Date.now() < until) {
    if (await has(page, 'Mining plasma')) return true
    // Once the action is over the panel is gone, and a slow poll can miss a
    // short mine entirely; a quick one costs nothing next to a block.
    await new Promise((r) => setTimeout(r, 250))
  }
  return false
}

async function openBrowser(name, phantomKey = null) {
  const dir = mkdtempSync(join(tmpdir(), `solzen-${name}-`))
  const browser = await puppeteer.launch({
    executablePath: CHROME,
    headless: 'new',
    userDataDir: dir,
    // Mining plasma takes minutes, and CDP's default 180s ceiling applies to
    // every evaluate that overlaps it. The page yields the main thread while it
    // mines -- that is the whole point of the pause in znn/pow.go -- but a call
    // still queues behind whatever slice is running.
    protocolTimeout: 900_000,
    args: ['--no-first-run', '--no-default-browser-check', '--disable-features=Translate'],
  })
  const page = await browser.newPage()
  await page.setViewport({ width: 1280, height: 1400 })
  page.on('pageerror', (e) => console.log(`  [${name}] page error: ${e.message}`))
  if (phantomKey) await installPhantomStub(page, phantomKey)
  await page.goto(URL_, { waitUntil: 'domcontentloaded' })
  await waitForText(page, 'Two contracts, one secret', { timeout: 60000 })
  const handle = { browser, page, dir, name }
  openBrowsers.push(handle)
  return handle
}

async function connectLocalKey(page) {
  await clickText(page, 'Use a key in this browser')
  await page.waitForFunction(() => document.body.innerText.includes('a key in this browser'), { timeout: 15000 })
  return fundAndReadAddress(page)
}

// The connector's own path: the page has to notice the provider on its own --
// it is injected before the page loads and announces itself with an event, the
// way Phantom does -- and the button only exists once it has.
async function connectStubbedPhantom(page) {
  await page.waitForFunction(() => document.body.innerText.includes('Connect Phantom'), { timeout: 15000 })
  await clickText(page, 'Connect Phantom')
  await page.waitForFunction(
    () => /Phantom/.test(document.body.innerText) && document.body.innerText.includes('disconnect'),
    { timeout: 15000 },
  )

  // A reload must not cost a second popup. The page remembers that this browser
  // has connected here and asks the wallet to reconnect silently; nothing is
  // clicked below, so a page that came back disconnected fails here. Everything
  // after this point is also, therefore, a reconnected wallet signing.
  await page.reload({ waitUntil: 'domcontentloaded' })
  await waitForText(page, 'Two contracts, one secret', { timeout: 60000 })
  await page.waitForFunction(() => document.body.innerText.includes('disconnect'), { timeout: 30000 })
  check('the wallet reconnects after a reload without being asked again', true)

  return fundAndReadAddress(page)
}

async function fundAndReadAddress(page) {
  await clickText(page, 'airdrop')
  await page.waitForFunction(() => /\d+\.\d+ SOL/.test(document.body.innerText), { timeout: 30000 })
  return evaluate(page, () => {
    const m = document.body.innerText.match(/([1-9A-HJ-NP-Za-km-z]{4,8})…([1-9A-HJ-NP-Za-km-z]{4})/)
    return m ? m[0] : ''
  })
}

async function copyValueUnder(page, labelText) {
  return evaluate(page, (t) => {
    const block = [...document.querySelectorAll('div')].find(
      (d) => d.children.length === 2 && d.firstElementChild?.textContent.trim().toLowerCase() === t.toLowerCase(),
    )
    return block?.querySelector('code')?.textContent.trim() ?? ''
  }, labelText)
}

// --- the run ---------------------------------------------------------------

async function main() {
  step('building the wallet stand-in')
  execFileSync('go', ['build', '-o', walletBin, './cmd/devnet-wallet'], { cwd: join(root, 'wasm'), stdio: 'inherit' })

  const initiatorSendsSol = DIRECTION === 'sol2znn'
  step(`opening two browser profiles for a ${DIRECTION} swap`)
  const makerKey = USE_PHANTOM ? Keypair.generate() : null
  const takerKey = USE_PHANTOM ? Keypair.generate() : null
  const maker = await openBrowser('maker', makerKey)
  const taker = await openBrowser('taker', takerKey)
  check('both pages load the engine and render', true)

  const znnFunded = wallet('address', '-index', '1')
  const znnEmpty = wallet('address', '-index', '2')
  const znnAddr = initiatorSendsSol
    ? { maker: znnEmpty, taker: znnFunded }
    : { maker: znnFunded, taker: znnEmpty }

  const connectWallet = USE_PHANTOM ? connectStubbedPhantom : connectLocalKey
  step(USE_PHANTOM ? 'connecting the wallet connector to a Phantom-shaped provider' : 'connecting a Solana key in each browser')
  const makerSol = await connectWallet(maker.page)
  const takerSol = await connectWallet(taker.page)
  check('each browser has its own Solana address', !!makerSol && !!takerSol && makerSol !== takerSol,
    `${makerSol} / ${takerSol}`)
  if (USE_PHANTOM) {
    check(
      'the page shows the address the provider handed it',
      makerSol.startsWith(makerKey.publicKey.toBase58().slice(0, 4)),
      `${makerSol} for ${makerKey.publicKey.toBase58()}`,
    )
    // A wallet that could only sign-and-send would be submitting through its own
    // cluster, and the page says so in the header. Saying nothing is the claim
    // that it signs here and submits to the node in Nodes.
    check(
      'the page does not warn about submitting through the wallet',
      !(await has(maker.page, 'submits through its own node')),
    )
  }

  step('the maker writes an offer')
  await clickText(maker.page, 'New swap')
  await clickText(maker.page, initiatorSendsSol ? 'I send SOL, I receive ZNN' : 'I send ZNN, I receive SOL')
  await fillLabelled(maker.page, 'SOL amount', '0.3')
  await fillLabelled(maker.page, 'Zenon amount', '6')
  await fillLabelled(maker.page, 'Your Zenon address', znnAddr.maker)
  await clickText(maker.page, 'Create the offer')
  await waitForText(maker.page, 'Send this offer to the counterparty')
  const offer = await copyValueUnder(maker.page, 'Send this offer to the counterparty')
  check('the offer is produced and shown', offer.startsWith('solzenoffer1:'), `${offer.slice(0, 28)}…`)

  step('the taker accepts it')
  await clickText(taker.page, 'New swap')
  await clickText(taker.page, 'Take an offer')
  await taker.page.evaluate((v) => {
    const ta = document.querySelector('textarea')
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set
    setter.call(ta, v)
    ta.dispatchEvent(new Event('input', { bubbles: true }))
    ta.dispatchEvent(new Event('blur', { bubbles: true }))
  }, offer)
  await fillLabelled(taker.page, 'Your Zenon address', znnAddr.taker)
  await clickText(taker.page, 'Accept')
  await waitForText(taker.page, 'Send this acceptance back')
  const accept = await copyValueUnder(taker.page, 'Send this acceptance back')
  check('the taker produces an acceptance', accept.startsWith('solzenaccept1:'))

  step('the maker applies it')
  await maker.page.evaluate((v) => {
    const ta = [...document.querySelectorAll('textarea')].pop()
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set
    setter.call(ta, v)
    ta.dispatchEvent(new Event('input', { bubbles: true }))
  }, accept)
  await clickText(maker.page, 'Apply')
  await new Promise((r) => setTimeout(r, 2000))
  check('the maker no longer asks for an acceptance',
    !(await has(maker.page, 'Paste the counterparty’s acceptance')))

  // The ZNN side is funded from a wallet, which is the whole point: an ordinary
  // transfer to an ordinary address, no HTLC support required of it.
  const znnSender = initiatorSendsSol ? taker : maker
  // Taken from the panel that asks for it, not from the first z1q address on
  // the page: several are shown, and the wrong one is a payment to the
  // counterparty's wallet with no swap attached to it.
  const swapAddress = await evaluate(znnSender.page, () => {
    const panel = [...document.querySelectorAll('section')].find((s) =>
      s.querySelector('h3')?.textContent.includes("This swap's Zenon address"),
    )
    return panel?.querySelector('code')?.textContent.trim() ?? ''
  })
  check('the page shows a per-swap Zenon address to pay', /^z1[0-9a-z]{38}$/.test(swapAddress), swapAddress)

  step('paying that address from a wallet, and fusing plasma to it')
  wallet('fuse', '-index', '1', '-to', swapAddress, '-amount', '60')
  wallet('fuse', '-index', '1', '-to', znnEmpty, '-amount', '60')
  if (!MINE_IN_BROWSER) {
    // The receiving side's swap key holds nothing, but publishing its unlock is
    // still a Zenon block. Fusing to it is what the page tells that user to do;
    // `--pow` skips this to exercise the alternative.
    const receiverSwapAddress = await evaluate((initiatorSendsSol ? maker : taker).page, () => {
      const panel = [...document.querySelectorAll('section')].find((s) =>
        s.querySelector('h3')?.textContent.includes("This swap's Zenon address"),
      )
      return panel?.querySelector('code')?.textContent.trim() ?? ''
    })
    if (receiverSwapAddress) wallet('fuse', '-index', '1', '-to', receiverSwapAddress, '-amount', '60')
  }
  wallet('send', '-index', '1', '-to', swapAddress, '-amount', '6', '-token', 'ZNN')

  await refreshUntil(znnSender.page, () => offers(znnSender.page, 'znn.receive'), 'offering to receive the payment')
  check('the page notices the payment and asks to receive it', true)
  await doAction(znnSender.page, 'znn.receive')
  await waitIdle(znnSender.page)
  await refreshUntil(znnSender.page, () => has(znnSender.page, 'fused'), 'reporting on plasma')
  check('the page reports fused plasma, so no proof of work is needed',
    await has(znnSender.page, 'its blocks publish immediately'))

  step('the initiator funds the long leg')
  const initiator = maker
  const fundKind = initiatorSendsSol ? 'sol.create' : 'znn.create'
  await refreshUntil(initiator.page, () => offers(initiator.page, fundKind), `offering ${fundKind}`)
  await doAction(initiator.page, fundKind)
  await waitIdle(initiator.page, { label: fundKind })

  step('the participant verifies it and funds the short leg')
  await refreshUntil(taker.page, () => has(taker.page, 'funded and verified'), 'verifying the initiator leg')
  check('the taker sees the initiator leg verified against the terms', true)
  const takerFundKind = initiatorSendsSol ? 'znn.create' : 'sol.create'
  await refreshUntil(taker.page, () => offers(taker.page, takerFundKind), `offering ${takerFundKind}`)
  await doAction(taker.page, takerFundKind)
  await waitIdle(taker.page, { label: takerFundKind })

  step('the initiator claims, publishing the secret')
  const initiatorClaim = initiatorSendsSol ? 'znn.unlock' : 'sol.redeem'
  await refreshUntil(maker.page, () => offers(maker.page, initiatorClaim), `offering ${initiatorClaim}`)

  // Under --pow this is the block nobody fused plasma for, and mining it in the
  // tab is the only thing that run exists to exercise. Without these two checks
  // the mode is indistinguishable from an ordinary run that happened to be
  // fused -- which is exactly how it passed in three minutes once.
  if (MINE_IN_BROWSER && initiatorClaim === 'znn.unlock') {
    check('the claiming side has no plasma, and the page says what that costs',
      await has(maker.page, 'proof of work in this tab'))
  }
  const claimStarted = Date.now()
  await doAction(maker.page, initiatorClaim)
  const watching = MINE_IN_BROWSER && initiatorClaim === 'znn.unlock'
  const mined = watching ? await watchForMining(maker.page) : null
  await waitIdle(maker.page, { label: initiatorClaim })
  if (watching) {
    check('and it actually mined, in the tab, rather than publishing at once', mined,
      `${Math.round((Date.now() - claimStarted) / 1000)}s for a block the page priced at minutes`)
  }

  step('the participant picks the secret up and claims the other leg')
  const takerClaim = initiatorSendsSol ? 'sol.redeem' : 'znn.unlock'
  await refreshUntil(taker.page, () => offers(taker.page, takerClaim), `offering ${takerClaim}`)
  check('the taker was handed the secret by the chain, not by the counterparty', true)
  await doAction(taker.page, takerClaim)
  await waitIdle(taker.page, { label: takerClaim })

  step('both pages report the swap settled')
  const settled = 'settled -- both legs claimed against one secret'
  await refreshUntil(maker.page, () => has(maker.page, settled), 'reporting the swap settled', { tries: 30 })
  await refreshUntil(taker.page, () => has(taker.page, settled), 'reporting the swap settled', { tries: 30 })
  check('the maker’s page says settled', true)
  check('the taker’s page says settled', true)

  // The wallet mode is in the filename because the header is the one part of
  // the page that differs between them, and a screenshot of a settled swap is
  // only evidence of what it says it is.
  const shot = USE_PHANTOM ? 'ui-phantom' : 'ui'
  await maker.page.screenshot({ path: join(root, 'docs', `${shot}-maker.png`), fullPage: true })
  await taker.page.screenshot({ path: join(root, 'docs', `${shot}-taker.png`), fullPage: true })
  console.log('  screenshots written to docs/')

  // Last, because it changes what the header says: the two things that happen in
  // the extension rather than on the page. A page that ignored them would go on
  // showing an address the wallet has moved off, and sign for it.
  if (USE_PHANTOM) {
    step('the wallet is driven from the extension instead of the page')
    const other = Keypair.generate().publicKey.toBase58()
    await maker.page.evaluate((a) => window.__stubSwitchAccount(a), other)
    await maker.page.waitForFunction((a) => document.body.innerText.includes(a.slice(0, 6)), { timeout: 15000 }, other)
    check('the page follows the wallet to a new account', true, `${other.slice(0, 6)}…`)

    await maker.page.evaluate(() => window.__stubHangUp())
    await maker.page.waitForFunction(() => document.body.innerText.includes('Connect Phantom'), { timeout: 15000 })
    check('and offers to connect again when the wallet hangs up', true)
  }

  for (const b of [maker, taker]) {
    await b.browser.close()
    rmSync(b.dir, { recursive: true, force: true })
  }
  console.log(`\n${failures === 0 ? 'all checks passed' : `${failures} check(s) failed`}`)
  process.exit(failures === 0 ? 0 : 1)
}

let openBrowsers = []

main().catch(async (e) => {
  console.error(`\n${e.stack ?? e}`)
  for (const b of openBrowsers) {
    try {
      const path = join(root, 'docs', `failure-${b.name}.png`)
      await b.page.screenshot({ path, fullPage: true })
      console.error(`  wrote ${path}`)
    } catch (err) {
      console.error(`  could not screenshot ${b.name}: ${err.message}`)
    }
  }
  process.exit(1)
})

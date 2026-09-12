// Leaves a swap to expire, and takes both legs back.
//
// This is the path swap-test.mjs does not walk. Nobody claims, both timelocks
// pass, and each side refunds its own leg. `program:test` covers the Solana
// refund branch as a contract call; what is covered here is the thing a person
// would actually meet -- the engine noticing an expiry, refusing to claim after
// it, offering the refund only to the side whose leg it is, refusing it to that
// side while the leg is still live, and the money arriving back.
//
//   node scripts/timeout-test.mjs [sol2znn|znn2sol|abandon]
//
// The two directions are different tests, because refunding is per chain:
// sol2znn expires a Zenon HTLC held by the participant and a Solana escrow held
// by the initiator, and znn2sol swaps which side holds which. Between them,
// both chains' refund paths run for both roles.
//
// `abandon` is the third way a swap ends with nobody paid, and needs no clock at
// all: the ZNN is sent to this swap's address and the swap is dropped before the
// HTLC that would have committed it. Nothing is locked, and the money has to be
// reachable anyway.
//
// ## Why this builds its own engine
//
// A real swap's deadlines are 24 and 48 hours out, and `MinLegGapSeconds`
// requires at least four hours between them, so waiting out a real one is a
// four-hour test at best. This builds `wasm/` with `-tags fastclock`, which
// selects `limits_fastclock.go` over `limits.go`: the same two constants, small
// enough to sit through.
//
// Both gate whether a swap may be *created*. Neither is consulted by a refund,
// by the expiry arithmetic, or by any of the on-chain calls -- so the code under
// test below is the code that ships, file for file, and the difference between
// them is a compiler flag rather than a patched copy. The run asserts through
// the `env` call that it got the engine it asked for, so that claim is checked
// rather than trusted.

import { execFileSync, spawnSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  Connection,
  Keypair,
  PublicKey,
  Transaction,
  TransactionInstruction,
  LAMPORTS_PER_SOL,
  sendAndConfirmTransaction,
} from '@solana/web3.js'

const here = dirname(fileURLToPath(import.meta.url))
const root = join(here, '../..')
const wasmDir = join(root, 'wasm')

const DIRECTION = process.argv[2] ?? 'sol2znn'
if (!['sol2znn', 'znn2sol', 'abandon', 'mismatch'].includes(DIRECTION)) {
  console.error(`unknown mode ${DIRECTION}; expected sol2znn, znn2sol, abandon or mismatch`)
  process.exit(2)
}
const MISMATCH = DIRECTION === 'mismatch'
// The third mode is the other way a swap ends without settling, and the only one
// that needs no clock at all: money is sent to a swap address and the swap is
// then abandoned before the HTLC is made. It runs against the shipped engine.
const ABANDON = DIRECTION === 'abandon'

const SOLANA_URL = process.env.SOLZEN_SOLANA_URL ?? 'http://127.0.0.1:8899'
const ZENON_URL = process.env.SOLZEN_ZENON_URL ?? 'http://127.0.0.1:35997'

// The two deadlines, in seconds from when the swap is made. They have to be far
// enough out that both legs are funded while both are still live -- funding the
// Zenon side means a transfer, a receive and a contract call, each of them a
// block -- and close enough that the run ends. The gap between them is what the
// participant is protected by, and it is checked against the patched minimum
// below rather than being assumed.
const SHORT_SECONDS = Number(process.env.SOLZEN_SHORT_SECONDS ?? 900)
const LONG_SECONDS = Number(process.env.SOLZEN_LONG_SECONDS ?? 1200)

// What the fastclock build requires, copied from wasm/limits_fastclock.go. The
// run asserts the engine agrees before it uses it, so a value that drifts here
// is caught rather than quietly weakening the swap under test.
const FASTCLOCK_MIN_GAP = 120
const FASTCLOCK_MIN_REMAINING = 60

const FUSE_QSR = '60'
const SOL_AMOUNT = '0.35'
const ZNN_AMOUNT = '5'

const connection = new Connection(SOLANA_URL, 'confirmed')
const exe = process.platform === 'win32' ? '.exe' : ''
const walletBin = join(root, 'bin', `devnet-wallet${exe}`)
let solzenBin = ''
const started = Date.now()

let failures = 0
const elapsed = () => {
  const s = Math.round((Date.now() - started) / 1000)
  return `${String(Math.floor(s / 60)).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}`
}
const step = (s) => console.log(`\n=== [${elapsed()}] ${s}`)
function check(name, ok, detail = '') {
  console.log(`${ok ? '  ok  ' : ' FAIL '} ${name}${detail ? ` -- ${detail}` : ''}`)
  if (!ok) failures++
}

// --------------------------------------------------------------- the engine

// buildShippedEngine builds wasm/ as it stands. The abandon and mismatch modes
// need no shortened clocks, so they get no tag either -- the point of those
// tests is weakened if they run against a build nobody ships.
function buildShippedEngine() {
  return buildEngine(`solzen${exe}`, [])
}

// buildFastclockEngine builds the same files with the short minimums.
function buildFastclockEngine() {
  return buildEngine(`solzen-fastclock${exe}`, ['-tags', 'fastclock'])
}

function buildEngine(name, tags) {
  const bin = join(root, 'bin', name)
  execFileSync('go', ['build', ...tags, '-o', bin, '.'], { cwd: wasmDir, stdio: 'inherit' })
  execFileSync('go', ['build', '-o', walletBin, './cmd/devnet-wallet'], { cwd: wasmDir, stdio: 'inherit' })
  return { bin }
}

function call(store, method, params) {
  const request = JSON.stringify(params === undefined ? { method } : { method, params })
  const r = spawnSync(solzenBin, ['-store', store, request], {
    encoding: 'utf8',
    env: { ...process.env, SOLZEN_SOLANA_URL: SOLANA_URL, SOLZEN_ZENON_URL: ZENON_URL },
    maxBuffer: 32 * 1024 * 1024,
  })
  if (r.error) throw r.error
  let parsed
  try {
    parsed = JSON.parse(r.stdout)
  } catch {
    throw new Error(`${method}: the engine printed something that is not JSON:\n${r.stdout}\n${r.stderr}`)
  }
  if (parsed.error) throw new Error(`${method}: ${parsed.error}`)
  return parsed.result
}

// refuses runs a call that is supposed to fail and hands back the reason. A
// test that only checks the happy path of a refund is a test that would pass
// with every guard removed.
function refuses(fn) {
  try {
    fn()
    return null
  } catch (e) {
    return String(e.message ?? e)
  }
}

function wallet(...args) {
  const r = spawnSync(walletBin, args, {
    encoding: 'utf8',
    env: { ...process.env, SOLZEN_ZENON_URL: ZENON_URL },
  })
  if (r.status !== 0) throw new Error(`devnet-wallet ${args.join(' ')}: ${r.stderr || r.stdout}`)
  return r.stdout.trim()
}

// ------------------------------------------------------------------ solana

const programId = new PublicKey(
  Keypair.fromSecretKey(
    Uint8Array.from(JSON.parse(readFileSync(join(root, 'program/target/deploy/solzen_htlc-keypair.json'), 'utf8'))),
  ).publicKey,
)

async function fundSol(kp, sol) {
  const sig = await connection.requestAirdrop(kp.publicKey, sol * LAMPORTS_PER_SOL)
  const bh = await connection.getLatestBlockhash()
  await connection.confirmTransaction({ signature: sig, ...bh }, 'confirmed')
}

const feesPaid = { initiator: 0, participant: 0 }

async function sendSolanaAction(store, swapId, kind, payer, who) {
  const ix = call(store, 'sol.instruction', { id: swapId, kind })
  const instruction = new TransactionInstruction({
    programId: new PublicKey(ix.programId),
    keys: ix.accounts.map((a) => ({
      pubkey: new PublicKey(a.pubkey),
      isSigner: !!a.isSigner,
      isWritable: !!a.isWritable,
    })),
    data: Buffer.from(ix.data, 'base64'),
  })
  const sig = await sendAndConfirmTransaction(connection, new Transaction().add(instruction), [payer], {
    commitment: 'confirmed',
  })
  call(store, 'sol.recordTx', { id: swapId, kind, signature: sig })
  const tx = await connection.getTransaction(sig, { commitment: 'confirmed', maxSupportedTransactionVersion: 0 })
  feesPaid[who] += tx?.meta?.fee ?? 0
  return sig
}

// ------------------------------------------------------------------- helpers

const actionKinds = (status) => status.actions.map((a) => a.kind)
const actionOf = (status, kind) => status.actions.find((a) => a.kind === kind)

function primary(status) {
  return status.actions.find((a) => a.primary) ?? status.actions[0]
}

function show(who, status) {
  const line = (l, s) =>
    `${l}: ${s.settled ? 'settled' : s.refunded ? 'refunded' : s.funded ? (s.verified ? 'funded+verified' : 'funded BUT WRONG') : '-'}` +
    (s.funded && !s.settled && !s.refunded ? `(${s.expired ? 'expired' : `${Math.max(0, s.secondsLeft ?? 0)}s left`})` : '')
  console.log(
    `  [${who}] ${line('sol', status.sol)}  ${line('znn', status.znn)}  next=${primary(status)?.kind ?? '-'}`,
  )
  for (const w of status.warnings ?? []) console.log(`  [${who}] warning: ${w}`)
}

async function until(what, fn, { tries = 60, delay = 3000 } = {}) {
  for (let i = 0; i < tries; i++) {
    const v = await fn()
    if (v) return v
    await new Promise((r) => setTimeout(r, delay))
  }
  throw new Error(`timed out waiting for ${what}`)
}

// waitForExpiry polls the chain's own view rather than this machine's clock.
// Both deadlines are enforced against a chain clock -- Solana's cluster time and
// Zenon's momentum time -- and neither is this process's `Date.now()`.
async function waitForExpiry(store, swapId, leg, label) {
  const until_ = Date.now() + 40 * 60 * 1000
  for (;;) {
    const { status } = call(store, 'swap.refresh', { id: swapId })
    const l = leg === 'sol' ? status.sol : status.znn
    if (l.expired) return status
    if (Date.now() > until_) throw new Error(`${label} never expired`)
    const left = Math.max(0, l.secondsLeft ?? 0)
    console.log(`       ${label}: ${left}s left`)
    await new Promise((r) => setTimeout(r, Math.min(30000, Math.max(5000, left * 1000 * 0.4))))
  }
}

// ---------------------------------------------------------------------- run

async function main() {
  if (ABANDON) return abandonRun()
  if (MISMATCH) return mismatchRun()
  step('building an engine whose clocks are short enough to watch')
  const engine = buildFastclockEngine()
  solzenBin = engine.bin
  console.log(`  -tags fastclock  MinLegGapSeconds=${FASTCLOCK_MIN_GAP}s  MinRemainingSeconds=${FASTCLOCK_MIN_REMAINING}s`)
  const probe = mkdtempSync(join(tmpdir(), 'solzen-probe-'))
  const limits = call(probe, 'env').limits
  rmSync(probe, { recursive: true, force: true })
  check('the engine under test reports the fastclock minimums',
    limits.minLegGapSeconds === FASTCLOCK_MIN_GAP && limits.minRemainingSeconds === FASTCLOCK_MIN_REMAINING,
    `gap=${limits.minLegGapSeconds}s remaining=${limits.minRemainingSeconds}s`)
  check('and the shipped defaults are otherwise untouched',
    limits.defaultLongSeconds === 48 * 3600 && limits.defaultShortSeconds === 24 * 3600)
  check('the deadlines this run uses satisfy that minimum',
    LONG_SECONDS - SHORT_SECONDS >= FASTCLOCK_MIN_GAP,
    `${LONG_SECONDS - SHORT_SECONDS}s apart`)

  const initiatorSendsSol = DIRECTION === 'sol2znn'
  const stores = {
    initiator: mkdtempSync(join(tmpdir(), 'solzen-initiator-')),
    participant: mkdtempSync(join(tmpdir(), 'solzen-participant-')),
  }

  // Which chain each side's own leg is on. The refund is always of your own
  // leg, so this is what decides which call each side makes at the end.
  const myLeg = { initiator: initiatorSendsSol ? 'sol' : 'znn', participant: initiatorSendsSol ? 'znn' : 'sol' }

  step(`a ${DIRECTION} swap that nobody will claim`)
  console.log(`  short leg (the participant's) expires in ${SHORT_SECONDS}s`)
  console.log(`  long leg  (the initiator's)   expires in ${LONG_SECONDS}s`)

  const config = { solanaUrl: SOLANA_URL, zenonUrl: ZENON_URL, solProgram: programId.toBase58() }
  call(stores.initiator, 'config.set', config)
  call(stores.participant, 'config.set', config)

  const solKeys = { initiator: Keypair.generate(), participant: Keypair.generate() }
  await Promise.all([fundSol(solKeys.initiator, 3), fundSol(solKeys.participant, 3)])

  const znnFunded = wallet('address', '-index', '1')
  const znnEmpty = wallet('address', '-index', '2')
  const znnIndexOf = { [znnFunded]: '1', [znnEmpty]: '2' }
  const znnAddr = initiatorSendsSol
    ? { initiator: znnEmpty, participant: znnFunded }
    : { initiator: znnFunded, participant: znnEmpty }

  const created = call(stores.initiator, 'swap.create', {
    sendsSol: initiatorSendsSol,
    solAddress: solKeys.initiator.publicKey.toBase58(),
    znnAddress: znnAddr.initiator,
    solAmount: SOL_AMOUNT,
    znnAmount: ZNN_AMOUNT,
    znnToken: 'ZNN',
    longSeconds: LONG_SECONDS,
    shortSeconds: SHORT_SECONDS,
  })
  const swapId = created.swap.id
  console.log(`  swap ${swapId}`)

  const accepted = call(stores.participant, 'swap.accept', {
    offer: created.offer,
    solAddress: solKeys.participant.publicKey.toBase58(),
    znnAddress: znnAddr.participant,
  })
  const applied = call(stores.initiator, 'swap.applyAccept', { id: swapId, accept: accepted.accept })
  check('the participant accepted a swap whose legs expire in minutes', !!applied.swap.terms.znnExpiry)

  // ------------------------------------------------------------ funding
  const znnSenderStore = initiatorSendsSol ? stores.participant : stores.initiator
  const swapAddress = (initiatorSendsSol ? accepted : applied).znnSwapAddress

  step(`funding the swap address ${swapAddress} from a wallet`)
  wallet('fuse', '-index', '1', '-to', swapAddress, '-amount', FUSE_QSR)
  wallet('fuse', '-index', '1', '-to', znnEmpty, '-amount', FUSE_QSR)
  wallet('send', '-index', '1', '-to', swapAddress, '-amount', ZNN_AMOUNT, '-token', 'ZNN')

  await until('the swap address to see the transfer', () => {
    const { status } = call(znnSenderStore, 'swap.refresh', { id: swapId })
    return status.swapAddress?.pending > 0 || status.swapAddress?.sufficient
  })
  {
    const { status } = call(znnSenderStore, 'swap.refresh', { id: swapId })
    if (status.swapAddress.pending > 0) call(znnSenderStore, 'znn.act', { id: swapId, kind: 'znn.receive' })
  }
  await until('the balance to settle', () => {
    const { status } = call(znnSenderStore, 'swap.refresh', { id: swapId })
    return status.swapAddress?.sufficient
  })

  step('both legs are funded, and then left alone')
  if (initiatorSendsSol) {
    await sendSolanaAction(stores.initiator, swapId, 'sol.create', solKeys.initiator, 'initiator')
  } else {
    call(stores.initiator, 'znn.act', { id: swapId, kind: 'znn.create' })
  }
  await until('the participant to verify the initiator’s leg', () => {
    const { status } = call(stores.participant, 'swap.refresh', { id: swapId })
    const leg = initiatorSendsSol ? status.sol : status.znn
    return leg.funded && leg.verified
  })
  if (initiatorSendsSol) {
    call(stores.participant, 'znn.act', { id: swapId, kind: 'znn.create' })
  } else {
    await sendSolanaAction(stores.participant, swapId, 'sol.create', solKeys.participant, 'participant')
  }
  const bothFunded = await until('the initiator to verify the participant’s leg', () => {
    const { status } = call(stores.initiator, 'swap.refresh', { id: swapId })
    const leg = initiatorSendsSol ? status.znn : status.sol
    return leg.funded && leg.verified ? status : null
  })
  show('initiator', bothFunded)
  const shortLeft = (initiatorSendsSol ? bothFunded.znn : bothFunded.sol).secondsLeft ?? 0
  check('both legs were funded while both were still live', shortLeft > 0,
    `${shortLeft}s left on the short leg when it was verified`)
  if (shortLeft < 60) {
    console.log('       (that is uncomfortably close -- raise SOLZEN_SHORT_SECONDS if this run is flaky)')
  }

  // --------------------------------------------- refunds before the time
  //
  // Both sides hold a funded leg that is still live. Neither should be able to
  // take it back. Each chain's contract enforces that itself; what is checked
  // here is that the engine refuses first, on both sides, so that learning it
  // costs a request rather than a Solana fee or a Zenon mine.
  step('a refund before the timelock is refused')
  for (const who of ['initiator', 'participant']) {
    const { status } = call(stores[who], 'swap.refresh', { id: swapId })
    const kind = myLeg[who] === 'sol' ? 'sol.refund' : 'znn.reclaim'
    check(`${who} is not offered a refund yet`, !actionKinds(status).includes(kind), actionKinds(status).join(', '))
    if (myLeg[who] === 'sol') {
      const why = refuses(() => call(stores[who], 'sol.instruction', { id: swapId, kind: 'sol.refund' }))
      check(`${who}'s escrow refuses to build an early refund`, /not yet/.test(why ?? ''), why ?? 'it built one')
    } else {
      // The contract declines an early reclaim too, but only after the block is
      // published -- which on Zenon is paid for in plasma, or in minutes of
      // proof of work, either way. So the engine compares the entry's expiry
      // against the frontier momentum first (ISSUES.md §3), the way the Solana
      // branch above compares against the cluster clock. What has to hold
      // either way is that the money does not move.
      const before = call(stores[who], 'swap.refresh', { id: swapId }).status.znn
      const why = refuses(() => call(stores[who], 'znn.act', { id: swapId, kind: 'znn.reclaim' }))
      const after = call(stores[who], 'swap.refresh', { id: swapId }).status.znn
      check(`${who}'s HTLC refuses to build an early reclaim`, /not yet/.test(why ?? ''),
        why ? String(why).split('\n')[0].slice(0, 120) : 'it built and published one')
      check(`${who}'s HTLC survives an early reclaim`,
        after.funded && !after.refunded && after.amount === before.amount,
        `still holding ${after.amount}`)
    }
  }

  // ------------------------------------------------ the short leg expires
  step("waiting for the participant's leg to expire")
  await waitForExpiry(stores.participant, swapId, myLeg.participant, "the participant's leg")

  {
    const { status } = call(stores.initiator, 'swap.refresh', { id: swapId })
    show('initiator', status)
    const claim = actionOf(status, initiatorSendsSol ? 'znn.unlock' : 'sol.redeem')
    check('the initiator can no longer claim the expired leg', claim ? claim.ready === false : true,
      claim?.blocked ?? 'the claim is not offered at all')
    if (claim) check('and is told why', /too late/.test(claim.blocked ?? ''), claim.blocked)
  }

  step('the participant takes their own leg back')
  drain(znnIndexOf[znnAddr.participant])
  const beforeShort = await balances(solKeys, znnIndexOf, znnAddr)
  const feesBeforeShort = { ...feesPaid }
  {
    const { status } = call(stores.participant, 'swap.refresh', { id: swapId })
    show('participant', status)
    const kind = myLeg.participant === 'sol' ? 'sol.refund' : 'znn.reclaim'
    const action = actionOf(status, kind)
    check(`the participant is offered ${kind}`, !!action && action.ready === true,
      action ? action.blocked || action.label : 'not offered')
    check('and it is the step the page would lead with', primary(status)?.kind === kind, primary(status)?.kind)

    if (kind === 'sol.refund') {
      const sig = await sendSolanaAction(stores.participant, swapId, 'sol.refund', solKeys.participant, 'participant')
      console.log(`  refunded ${sig.slice(0, 16)}...`)
    } else {
      await bringZenonHome(stores.participant, swapId, znnIndexOf[znnAddr.participant])
    }
  }
  {
    const { status } = call(stores.participant, 'swap.refresh', { id: swapId })
    const leg = myLeg.participant === 'sol' ? status.sol : status.znn
    check('the participant’s leg reads as refunded, not settled', leg.refunded && !leg.settled,
      `refunded=${leg.refunded} settled=${leg.settled}`)
    const after = await balances(solKeys, znnIndexOf, znnAddr)
    if (myLeg.participant === 'sol') {
      const back = after.sol.participant - beforeShort.sol.participant + (feesPaid.participant - feesBeforeShort.participant)
      check(`the participant got their ${SOL_AMOUNT} SOL back, plus the escrow's rent`,
        back >= Number(SOL_AMOUNT) * LAMPORTS_PER_SOL,
        `${back / LAMPORTS_PER_SOL} SOL including rent, after their own fees`)
    } else {
      const back = after.znn.participant - beforeShort.znn.participant
      check(`the participant got their ${ZNN_AMOUNT} ZNN back`, Math.abs(back - Number(ZNN_AMOUNT)) < 1e-8, `${back} ZNN`)
    }
  }

  // ------------------------------------------------- the long leg expires
  step("waiting for the initiator's leg to expire")
  {
    const { status } = call(stores.initiator, 'swap.refresh', { id: swapId })
    const mine = myLeg.initiator === 'sol' ? status.sol : status.znn
    check('the initiator’s leg outlived the participant’s', !mine.expired,
      `${mine.secondsLeft}s left after the other leg expired`)
  }
  await waitForExpiry(stores.initiator, swapId, myLeg.initiator, "the initiator's leg")

  step('the initiator takes theirs back too')
  drain(znnIndexOf[znnAddr.initiator])
  const beforeLong = await balances(solKeys, znnIndexOf, znnAddr)
  const feesBeforeLong = { ...feesPaid }
  {
    const { status } = call(stores.initiator, 'swap.refresh', { id: swapId })
    show('initiator', status)
    const kind = myLeg.initiator === 'sol' ? 'sol.refund' : 'znn.reclaim'
    const action = actionOf(status, kind)
    check(`the initiator is offered ${kind}`, !!action && action.ready === true,
      action ? action.blocked || action.label : 'not offered')

    if (kind === 'sol.refund') {
      const sig = await sendSolanaAction(stores.initiator, swapId, 'sol.refund', solKeys.initiator, 'initiator')
      console.log(`  refunded ${sig.slice(0, 16)}...`)
    } else {
      await bringZenonHome(stores.initiator, swapId, znnIndexOf[znnAddr.initiator])
    }
  }

  // ------------------------------------------------------------- outcome
  step('nobody was paid, and both sides say so')
  for (const who of ['initiator', 'participant']) {
    const st = await until(`${who} to see both legs closed`, () => {
      const { status } = call(stores[who], 'swap.refresh', { id: swapId })
      return status.sol.refunded && status.znn.refunded ? status : null
    })
    show(who, st)
    check(`${who} records the swap as refunded, not settled`,
      !st.sol.settled && !st.znn.settled && (st.outcome ?? '').length > 0, st.outcome)
  }
  {
    // swap.get answers with the record itself, not the refresh envelope.
    const record = call(stores.participant, 'swap.get', { id: swapId }).swap
    check('the participant never learned the secret', !record.secret,
      'nobody claimed, so nothing was ever published')
  }
  const after = await balances(solKeys, znnIndexOf, znnAddr)
  if (myLeg.initiator === 'sol') {
    const back = after.sol.initiator - beforeLong.sol.initiator + (feesPaid.initiator - feesBeforeLong.initiator)
    check(`the initiator got their ${SOL_AMOUNT} SOL back, plus the escrow's rent`,
      back >= Number(SOL_AMOUNT) * LAMPORTS_PER_SOL,
      `${back / LAMPORTS_PER_SOL} SOL including rent, after their own fees`)
  } else {
    const back = after.znn.initiator - beforeLong.znn.initiator
    check(`the initiator got their ${ZNN_AMOUNT} ZNN back`, Math.abs(back - Number(ZNN_AMOUNT)) < 1e-8, `${back} ZNN`)
  }

  for (const dir of Object.values(stores)) rmSync(dir, { recursive: true, force: true })
  console.log(`\n[${elapsed()}] ${failures === 0 ? 'all checks passed' : `${failures} check(s) failed`}`)
  process.exit(failures === 0 ? 0 : 1)
}

// abandonRun covers the other way a swap ends with nobody paid: the taker pays
// this swap's Zenon address -- an ordinary transfer, the step that happens in
// their own wallet -- and then stops, before the HTLC that would commit it.
//
// Nothing is locked at that point, which is exactly why it is worth a test: the
// money is sitting at an address whose only key is one this page generated, and
// the claim the page makes about it ("you can send it back to yourself at any
// point before the HTLC is made") is the claim being checked here.
async function abandonRun() {
  step('building the engine as it ships -- this path needs no shortened clocks')
  const engine = buildShippedEngine()
  solzenBin = engine.bin

  const stores = {
    initiator: mkdtempSync(join(tmpdir(), 'solzen-initiator-')),
    participant: mkdtempSync(join(tmpdir(), 'solzen-participant-')),
  }
  const config = { solanaUrl: SOLANA_URL, zenonUrl: ZENON_URL, solProgram: programId.toBase58() }
  call(stores.initiator, 'config.set', config)
  call(stores.participant, 'config.set', config)

  const solKeys = { initiator: Keypair.generate(), participant: Keypair.generate() }
  await Promise.all([fundSol(solKeys.initiator, 1), fundSol(solKeys.participant, 1)])
  const znnFunded = wallet('address', '-index', '1')
  const znnEmpty = wallet('address', '-index', '2')

  step('a swap in which the taker will send ZNN, and then think better of it')
  const created = call(stores.initiator, 'swap.create', {
    sendsSol: true,
    solAddress: solKeys.initiator.publicKey.toBase58(),
    znnAddress: znnEmpty,
    solAmount: SOL_AMOUNT,
    znnAmount: ZNN_AMOUNT,
    znnToken: 'ZNN',
  })
  const swapId = created.swap.id
  const accepted = call(stores.participant, 'swap.accept', {
    offer: created.offer,
    solAddress: solKeys.participant.publicKey.toBase58(),
    znnAddress: znnFunded,
  })
  call(stores.initiator, 'swap.applyAccept', { id: swapId, accept: accepted.accept })
  const swapAddress = accepted.znnSwapAddress
  check('the deadlines are the shipped defaults, not a test’s',
    accepted.swap.terms.znnExpiry - accepted.swap.terms.solTimelock < -4 * 3600,
    `${Math.round((accepted.swap.terms.solTimelock - accepted.swap.terms.znnExpiry) / 3600)}h apart`)

  {
    const { status } = call(stores.participant, 'swap.refresh', { id: swapId })
    const ask = actionOf(status, 'znn.awaitFunding')
    check('the page asks for an ordinary transfer to this swap’s address', !!ask, ask?.label ?? actionKinds(status).join(', '))
    check('and names the address it is asking for', (ask?.detail ?? '').includes(swapAddress), swapAddress)
  }

  step(`paying ${swapAddress}, and then abandoning the swap`)
  wallet('fuse', '-index', '1', '-to', swapAddress, '-amount', FUSE_QSR)
  wallet('send', '-index', '1', '-to', swapAddress, '-amount', ZNN_AMOUNT, '-token', 'ZNN')
  await until('the swap address to see the transfer', () => {
    const { status } = call(stores.participant, 'swap.refresh', { id: swapId })
    return status.swapAddress?.pending > 0 || status.swapAddress?.sufficient
  })
  {
    const { status } = call(stores.participant, 'swap.refresh', { id: swapId })
    if (status.swapAddress.pending > 0) call(stores.participant, 'znn.act', { id: swapId, kind: 'znn.receive' })
  }
  await until('the balance to settle', () => {
    const { status } = call(stores.participant, 'swap.refresh', { id: swapId })
    return status.swapAddress?.sufficient
  })

  drain('1')
  const before = Number(wallet('balance', '-index', '1').match(/ZNN\s+([\d.]+)/)?.[1] ?? 0)
  {
    const { status } = call(stores.participant, 'swap.refresh', { id: swapId })
    show('participant', status)
    check('nothing is locked yet -- there is no HTLC', !status.znn.funded)
    const sweep = actionOf(status, 'znn.sweep')
    check('the money can be sent back without the counterparty', !!sweep && sweep.ready === true,
      sweep?.label ?? actionKinds(status).join(', '))
    check('and the page offers making the HTLC as well, since either is still open',
      actionKinds(status).includes('znn.create'))
  }

  step('taking it back')
  const s = call(stores.participant, 'znn.act', { id: swapId, kind: 'znn.sweep' })
  console.log(`  swept ${s.tx} confirmed=${s.confirmed}`)
  // The wallet's own receive, which Syrius does by itself. A sweep is an
  // ordinary send and is not receivable until it is in a momentum, so asking
  // once, immediately, is asking too early. This used to pass by accident:
  // znn.act spent ninety seconds waiting for a paired block that only this
  // wallet could publish, and the block landed during the wait (ISSUES.md §11).
  await until('the swept funds to reach the wallet', () => wallet('receive', '-index', '1') !== 'received 0')
  const after = Number(wallet('balance', '-index', '1').match(/ZNN\s+([\d.]+)/)?.[1] ?? 0)
  check(`the ${ZNN_AMOUNT} ZNN came back to the wallet that sent it`,
    Math.abs(after - before - Number(ZNN_AMOUNT)) < 1e-8, `${(after - before).toFixed(8)} ZNN`)
  {
    const { status } = call(stores.participant, 'swap.refresh', { id: swapId })
    check('the swap address is empty', status.swapAddress?.balance === '0', status.swapAddress?.balance)
    check('and no HTLC was ever created', !status.znn.funded && !status.znn.settled && !status.znn.refunded)
    // The address only exists as long as its key does. A swept swap that threw
    // the key away would strand anything sent to it afterwards.
    const record = call(stores.participant, 'swap.get', { id: swapId }).swap
    check('the swap kept its key, so the address stays reachable', !!record.znnSwapSeed)
    check('and it is the key that address was derived from', record.terms.znnSender === swapAddress,
      record.terms.znnSender)
  }

  for (const dir of Object.values(stores)) rmSync(dir, { recursive: true, force: true })
  console.log(`\n[${elapsed()}] ${failures === 0 ? 'all checks passed' : `${failures} check(s) failed`}`)
  process.exit(failures === 0 ? 0 : 1)
}

// mismatchRun funds a leg that does not match what was agreed, and checks that
// the counterparty is stopped.
//
// This is the branch every other test avoids by being honest. It is also the one
// that matters most: a swap is safe because the participant refuses to commit
// against a leg that is not the agreed one, and nothing else in this repo ever
// produces such a leg. Here the initiator's own record is edited before their
// page builds the escrow -- which is exactly what a counterparty running modified
// code would do, and is invisible from the outside until the escrow is read.
async function mismatchRun() {
  step('building the engine as it ships -- a lie has to be caught by the real one')
  const engine = buildShippedEngine()
  solzenBin = engine.bin

  const stores = {
    initiator: mkdtempSync(join(tmpdir(), 'solzen-initiator-')),
    participant: mkdtempSync(join(tmpdir(), 'solzen-participant-')),
  }
  const config = { solanaUrl: SOLANA_URL, zenonUrl: ZENON_URL, solProgram: programId.toBase58() }
  call(stores.initiator, 'config.set', config)
  call(stores.participant, 'config.set', config)

  const solKeys = { initiator: Keypair.generate(), participant: Keypair.generate() }
  await Promise.all([fundSol(solKeys.initiator, 3), fundSol(solKeys.participant, 1)])
  const znnFunded = wallet('address', '-index', '1')
  const znnEmpty = wallet('address', '-index', '2')

  step('a swap agreed honestly, in which the initiator will then underpay')
  const created = call(stores.initiator, 'swap.create', {
    sendsSol: true,
    solAddress: solKeys.initiator.publicKey.toBase58(),
    znnAddress: znnEmpty,
    solAmount: SOL_AMOUNT,
    znnAmount: ZNN_AMOUNT,
    znnToken: 'ZNN',
  })
  const swapId = created.swap.id
  const accepted = call(stores.participant, 'swap.accept', {
    offer: created.offer,
    solAddress: solKeys.participant.publicKey.toBase58(),
    znnAddress: znnFunded,
  })
  const applied = call(stores.initiator, 'swap.applyAccept', { id: swapId, accept: accepted.accept })
  const agreedLamports = applied.swap.terms.solLamports
  const swapAddress = accepted.znnSwapAddress

  // The tamper. The store is one JSON file per swap, which is what makes this
  // possible without a second implementation of the instruction encoder: the
  // engine will build a correct escrow, for terms that are no longer the agreed
  // ones. The participant's copy is untouched, and is the only one that counts.
  step('the initiator edits their own copy of the terms')
  const recordPath = join(stores.initiator, `solzen.swap.${swapId}.json`)
  const record = JSON.parse(readFileSync(recordPath, 'utf8'))
  record.terms.solLamports = Math.floor(agreedLamports / 2)
  writeFileSync(recordPath, JSON.stringify(record))
  const tampered = call(stores.initiator, 'swap.get', { id: swapId }).swap
  check('their engine now believes a smaller amount was agreed',
    tampered.terms.solLamports === Math.floor(agreedLamports / 2),
    `${tampered.terms.solLamports} instead of ${agreedLamports}`)
  check('and the participant still holds the real terms',
    call(stores.participant, 'swap.get', { id: swapId }).swap.terms.solLamports === agreedLamports)

  step('the initiator funds that, and it lands on chain like any other escrow')
  const sig = await sendSolanaAction(stores.initiator, swapId, 'sol.create', solKeys.initiator, 'initiator')
  console.log(`  escrow funded ${sig.slice(0, 16)}...`)

  const seen = await until('the participant to see the escrow', () => {
    const { status } = call(stores.participant, 'swap.refresh', { id: swapId })
    return status.sol.funded ? status : null
  })
  show('participant', seen)
  check('the participant sees a funded leg', seen.sol.funded)
  check('and refuses to call it verified', seen.sol.verified === false)
  // The problem has to name both numbers. "does not match" alone would leave the
  // participant to go and read a chain to find out how badly.
  const problems = (seen.sol.problems ?? []).join('; ')
  const underpaid = String(Math.floor(agreedLamports / 2) / LAMPORTS_PER_SOL)
  check('saying both what it holds and what was agreed',
    problems.includes(underpaid) && problems.includes(SOL_AMOUNT), problems)
  check('and warns in as many words that this must not be funded against',
    (seen.warnings ?? []).some((w) => w.includes('does not match') && w.includes('Do not fund yours')),
    (seen.warnings ?? []).join(' | '))

  // The warning alone is not the protection. The protection is that the step
  // which cannot be undone -- creating the HTLC -- is not offered as ready. The
  // step before it, paying this swap's own address, deliberately stays available:
  // it moves the user's money to an address the user controls and can sweep back.
  step('the irreversible step is blocked, and the reversible one is not')
  wallet('fuse', '-index', '1', '-to', swapAddress, '-amount', FUSE_QSR)
  wallet('send', '-index', '1', '-to', swapAddress, '-amount', ZNN_AMOUNT, '-token', 'ZNN')
  await until('the swap address to see the transfer', () => {
    const { status } = call(stores.participant, 'swap.refresh', { id: swapId })
    return status.swapAddress?.pending > 0 || status.swapAddress?.sufficient
  })
  {
    const { status } = call(stores.participant, 'swap.refresh', { id: swapId })
    if (status.swapAddress.pending > 0) call(stores.participant, 'znn.act', { id: swapId, kind: 'znn.receive' })
  }
  const funded = await until('the swap address to hold the amount', () => {
    const { status } = call(stores.participant, 'swap.refresh', { id: swapId })
    return status.swapAddress?.sufficient ? status : null
  })
  show('participant', funded)
  const create = actionOf(funded, 'znn.create')
  check('the HTLC step is offered but not ready', !!create && create.ready === false,
    create ? create.blocked : 'not offered at all')
  check('and says which of the two reasons it is',
    (create?.blocked ?? '').includes('does not match'), create?.blocked)
  // And the same refusal in the executor, which is now safe to prove by calling
  // it: `znn.act znn.create` re-reads the escrow and refuses before it builds
  // anything, so this costs a request rather than the participant's money. It
  // used not to -- the guard was only in the plan, and a caller that skipped the
  // plan got the HTLC (ISSUES.md §8).
  const refused = refuses(() => call(stores.participant, 'znn.act', { id: swapId, kind: 'znn.create' }))
  check('and the engine refuses it too, not just the page',
    /does not match the agreement/.test(refused ?? ''), refused ?? 'it built and published one')
  const still = call(stores.participant, 'swap.refresh', { id: swapId }).status
  check('nothing was published by asking', still.swapAddress.sufficient && !still.znn.funded,
    `swap address still holds ${still.swapAddress.balance}`)

  step('and the money the participant already moved is still theirs')
  drain('1')
  const before = Number(wallet('balance', '-index', '1').match(/ZNN\s+([\d.]+)/)?.[1] ?? 0)
  const sweep = actionOf(funded, 'znn.sweep')
  check('a sweep back to their own wallet is offered', !!sweep && sweep.ready === true,
    sweep?.label ?? 'not offered')
  const s = call(stores.participant, 'znn.act', { id: swapId, kind: 'znn.sweep' })
  console.log(`  swept ${s.tx}`)
  await until('the swept funds to reach the wallet', () => wallet('receive', '-index', '1') !== 'received 0')
  const after = Number(wallet('balance', '-index', '1').match(/ZNN\s+([\d.]+)/)?.[1] ?? 0)
  check(`the ${ZNN_AMOUNT} ZNN came back`, Math.abs(after - before - Number(ZNN_AMOUNT)) < 1e-8,
    `${(after - before).toFixed(8)} ZNN`)

  for (const dir of Object.values(stores)) rmSync(dir, { recursive: true, force: true })
  console.log(`\n[${elapsed()}] ${failures === 0 ? 'all checks passed' : `${failures} check(s) failed`}`)
  process.exit(failures === 0 ? 0 : 1)
}

// bringZenonHome reclaims a Zenon leg and checks that the money is actually home.
//
// Reclaiming hands the money to nobody: the contract sends it to the address
// that created the HTLC, which is this swap's own address, and on Zenon an
// incoming transfer is not spendable until the receiving account publishes a
// block for it. So taking a Zenon leg back is three blocks -- reclaim, receive,
// sweep -- and it used to be three separate actions, with the money sitting at
// an address only one browser can reach in between two of them. It is one
// action now (ISSUES.md §7), and what this asserts is that all three blocks
// were published, and that nothing was left behind.
async function bringZenonHome(store, swapId, homeIndex) {
  const r = call(store, 'znn.act', { id: swapId, kind: 'znn.reclaim' })
  console.log(`  reclaimed ${r.tx} confirmed=${r.confirmed} in ${r.seconds}s`)
  for (const s of r.steps ?? []) console.log(`    ${s.kind.padEnd(12)} ${s.tx}  ${s.note}`)

  const kinds = (r.steps ?? []).map((s) => s.kind).join(', ')
  check('one reclaim published all three blocks',
    kinds === 'znn.reclaim, znn.receive, znn.sweep', kinds || 'no steps reported')
  check('and none of them was left owing', !r.incomplete, r.incomplete ?? 'nothing left to do')

  const done = await until('the leg to read as refunded with the address emptied', () => {
    const { status } = call(store, 'swap.refresh', { id: swapId })
    return status.znn.refunded && status.swapAddress?.balance === '0' ? status : null
  })
  check('the HTLC reads as reclaimed and this swap holds nothing',
    done.znn.refunded && done.swapAddress.balance === '0' && done.swapAddress.pending === 0,
    `${done.znn.note ?? ''} -- balance ${done.swapAddress.balance}, ${done.swapAddress.pending} unreceived`)
  check('so the page has no further step to offer for it',
    !actionOf(done, 'znn.receive') && !actionOf(done, 'znn.sweep'), actionKinds(done).join(', '))

  // The wallet's own receive, which Syrius does by itself. A sweep is an
  // ordinary send, so it is not receivable until it is in a momentum -- asking
  // once, immediately, is asking too early.
  await until('the swept funds to reach the wallet', () => wallet('receive', '-index', homeIndex) !== 'received 0')
}

// drain receives whatever is already waiting for an account, so that a run's
// before/after window measures that run. Zenon balances only move when the
// receiving account publishes a block, and a transfer an aborted run left
// unreceived otherwise lands inside the next run's measurement.
function drain(index) {
  wallet('receive', '-index', index)
}

async function balances(solKeys, znnIndexOf, znnAddr) {
  const out = { sol: {}, znn: {} }
  for (const who of ['initiator', 'participant']) {
    out.sol[who] = await connection.getBalance(solKeys[who].publicKey, 'confirmed')
    const text = wallet('balance', '-index', znnIndexOf[znnAddr[who]])
    const m = text.match(/ZNN\s+([\d.]+)/)
    out.znn[who] = m ? Number(m[1]) : 0
  }
  return out
}

main().catch((e) => {
  console.error(`\n${e.stack ?? e}`)
  process.exit(1)
})

// Settles a whole swap, both legs, against the two live local chains.
//
// This is the test that decides whether any of this works. The program tests
// prove the Solana contract keeps its promises; the Go tests prove the encoders
// agree with themselves. Only this one puts a real escrow on Solana and a real
// HTLC on Zenon, commits both to one secret, and shows the secret crossing from
// the chain where it was published to the party who needed it.
//
// It drives the same engine the browser runs -- wasm/api.go, built for the host
// instead of for wasm -- through two separate stores, because a swap has two
// participants and one store per participant is what makes them two.
//
//   node scripts/swap-test.mjs [direction]
//
//     sol2znn  (default)  the initiator sends SOL and is paid ZNN
//     znn2sol             the initiator sends ZNN and is paid SOL
//
// The two directions are not the same test. They differ in which chain the
// secret becomes public on, and that is the mechanism the whole scheme rests
// on, so both are worth running.

import { execFileSync, spawnSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
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
if (!['sol2znn', 'znn2sol'].includes(DIRECTION)) {
  console.error(`unknown direction ${DIRECTION}; expected sol2znn or znn2sol`)
  process.exit(2)
}

const SOLANA_URL = process.env.SOLZEN_SOLANA_URL ?? 'http://127.0.0.1:8899'
const ZENON_URL = process.env.SOLZEN_ZENON_URL ?? 'http://127.0.0.1:35997'

// Enough QSR fused to the swap address that its blocks publish immediately.
// Without it each one is a couple of minutes of proof of work -- which works,
// and is tested separately, but would make this run take twenty minutes.
const FUSE_QSR = '60'

const SOL_AMOUNT = '0.4'
const ZNN_AMOUNT = '7'

const connection = new Connection(SOLANA_URL, 'confirmed')

let failures = 0
const step = (s) => console.log(`\n=== ${s}`)
function check(name, ok, detail = '') {
  console.log(`${ok ? '  ok  ' : ' FAIL '} ${name}${detail ? ` -- ${detail}` : ''}`)
  if (!ok) failures++
}

// ---------------------------------------------------------------- binaries

const exe = process.platform === 'win32' ? '.exe' : ''
const solzenBin = join(root, 'bin', `solzen${exe}`)
const walletBin = join(root, 'bin', `devnet-wallet${exe}`)

function build() {
  step('building the engine')
  execFileSync('go', ['build', '-o', solzenBin, '.'], { cwd: wasmDir, stdio: 'inherit' })
  execFileSync('go', ['build', '-o', walletBin, './cmd/devnet-wallet'], { cwd: wasmDir, stdio: 'inherit' })
  console.log(`  built ${solzenBin}`)
}

// call runs one API request against one participant's store, and is the only
// way this script touches swap state. Everything a participant knows, it knows
// through the same call table the page uses.
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

// sendSolanaAction is the seam the browser has too: the engine says what the
// instruction is, and something that holds a key turns it into a transaction.
// Here that is a generated keypair; in the page it is the user's wallet, and
// the engine cannot tell the difference because it never sees either.
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
  // The fee is the signer's, not the swap's. Tracking it is what lets the
  // final assertion be "the escrow paid exactly the agreed amount" rather than
  // "roughly the agreed amount", which would hide a rounding bug.
  const tx = await connection.getTransaction(sig, { commitment: 'confirmed', maxSupportedTransactionVersion: 0 })
  feesPaid[who] += tx?.meta?.fee ?? 0
  return sig
}

// ------------------------------------------------------------------- helpers

const actionKinds = (status) => status.actions.map((a) => a.kind)

function primary(status) {
  return status.actions.find((a) => a.primary) ?? status.actions[0]
}

function show(who, status) {
  const line = (l, s) =>
    `${l}: ${s.settled ? 'settled' : s.refunded ? 'refunded' : s.funded ? (s.verified ? 'funded+verified' : 'funded BUT WRONG') : '-'}`
  console.log(
    `  [${who}] ${line('sol', status.sol)}  ${line('znn', status.znn)}  ` +
      `secret=${status.secretKnown ? 'yes' : 'no'}  next=${primary(status)?.kind ?? '-'}`,
  )
  for (const w of status.warnings ?? []) console.log(`  [${who}] warning: ${w}`)
}

// A Zenon HTLC only exists once the contract has produced its paired receive
// block, and a Solana transaction is only visible once it is confirmed. Both
// are a momentum or a slot away, so every hand-off polls rather than assuming.
async function until(what, fn, { tries = 40, delay = 3000 } = {}) {
  for (let i = 0; i < tries; i++) {
    const v = await fn()
    if (v) return v
    await new Promise((r) => setTimeout(r, delay))
  }
  throw new Error(`timed out waiting for ${what}`)
}

// ---------------------------------------------------------------------- run

async function main() {
  build()

  const initiatorSendsSol = DIRECTION === 'sol2znn'
  const stores = {
    initiator: mkdtempSync(join(tmpdir(), 'solzen-initiator-')),
    participant: mkdtempSync(join(tmpdir(), 'solzen-participant-')),
  }

  step(`a swap in which the initiator sends ${initiatorSendsSol ? 'SOL and is paid ZNN' : 'ZNN and is paid SOL'}`)
  console.log(`  program  ${programId.toBase58()}`)
  console.log(`  solana   ${SOLANA_URL}`)
  console.log(`  zenon    ${ZENON_URL}`)

  const config = { solanaUrl: SOLANA_URL, zenonUrl: ZENON_URL, solProgram: programId.toBase58() }
  call(stores.initiator, 'config.set', config)
  call(stores.participant, 'config.set', config)

  const checks = call(stores.initiator, 'config.check')
  check('both nodes answer', checks.solana.ok && checks.zenon.ok, JSON.stringify(checks.solana.program))

  // Solana wallets. Each side generates one; only the SOL sender needs a
  // balance, but both pay their own fees.
  const solKeys = { initiator: Keypair.generate(), participant: Keypair.generate() }
  await Promise.all([fundSol(solKeys.initiator, 3), fundSol(solKeys.participant, 3)])

  // Zenon wallet addresses. These are ordinary devnet accounts standing in for
  // two people's Syrius wallets: index 1 holds the funds, index 2 is empty and
  // is where the ZNN receiver will be paid.
  const znnFunded = wallet('address', '-index', '1')
  const znnEmpty = wallet('address', '-index', '2')
  const znnIndexOf = { [znnFunded]: '1', [znnEmpty]: '2' }
  const znnAddr = initiatorSendsSol
    ? { initiator: znnEmpty, participant: znnFunded } // initiator is paid ZNN
    : { initiator: znnFunded, participant: znnEmpty } // initiator sends ZNN
  console.log(`  znn: initiator ${znnAddr.initiator}`)
  console.log(`       participant ${znnAddr.participant}`)

  // ------------------------------------------------------- terms
  step('agreeing terms')
  const created = call(stores.initiator, 'swap.create', {
    sendsSol: initiatorSendsSol,
    solAddress: solKeys.initiator.publicKey.toBase58(),
    znnAddress: znnAddr.initiator,
    solAmount: SOL_AMOUNT,
    znnAmount: ZNN_AMOUNT,
    znnToken: 'ZNN',
  })
  const swapId = created.swap.id
  const previewed = call(stores.participant, 'swap.preview', { text: created.offer })
  check('the offer carries public terms only -- no secret, no key',
    !('secret' in previewed.terms) && !JSON.stringify(previewed.terms).includes(created.swap.secret))
  console.log(`  swap ${swapId}`)

  const accepted = call(stores.participant, 'swap.accept', {
    offer: created.offer,
    solAddress: solKeys.participant.publicKey.toBase58(),
    znnAddress: znnAddr.participant,
  })
  check('the participant derives the opposite role', accepted.swap.role.sendsSol === !initiatorSendsSol)
  check('the participant has no secret', !accepted.swap.secret)

  const applied = call(stores.initiator, 'swap.applyAccept', { id: swapId, accept: accepted.accept })
  check('both sides now hold identical terms', JSON.stringify(applied.swap.terms) === JSON.stringify(accepted.swap.terms))

  const tampered = accepted.accept.slice(0, -8) + 'AAAAAAAA'
  let refused = false
  try {
    call(stores.initiator, 'swap.applyAccept', { id: swapId, accept: tampered })
  } catch {
    refused = true
  }
  check('a damaged acceptance is refused', refused)

  // Whichever side sends ZNN funds its swap address, exactly as a user would
  // from Syrius: an ordinary transfer, plus a plasma fusion so the address's
  // own blocks publish without minutes of proof of work.
  const znnSenderStore = initiatorSendsSol ? stores.participant : stores.initiator
  const znnSenderSwap = initiatorSendsSol ? accepted : applied
  const swapAddress = znnSenderSwap.znnSwapAddress
  check('the swap address is the HTLC creator in the terms', swapAddress === applied.swap.terms.znnSender)

  step(`funding the swap address ${swapAddress} from a wallet`)
  wallet('fuse', '-index', '1', '-to', swapAddress, '-amount', FUSE_QSR)
  // The ZNN payout lands in an account that has never produced a block, and on
  // Zenon receiving is itself a block. Fusing plasma to it keeps that from
  // being half a minute of proof of work in the middle of the test.
  wallet('fuse', '-index', '1', '-to', znnEmpty, '-amount', FUSE_QSR)
  wallet('send', '-index', '1', '-to', swapAddress, '-amount', ZNN_AMOUNT, '-token', 'ZNN')
  console.log('  sent; waiting for it to show as receivable')

  await until('the swap address to see the transfer', () => {
    const { status } = call(znnSenderStore, 'swap.refresh', { id: swapId })
    return status.swapAddress?.pending > 0 || status.swapAddress?.sufficient
  })
  {
    const { status } = call(znnSenderStore, 'swap.refresh', { id: swapId })
    check('plasma was fused, so no proof of work is needed', status.swapAddress.plasmaFused === true)
    if (status.swapAddress.pending > 0) {
      check('the next step is to receive it', actionKinds(status).includes('znn.receive'))
      call(znnSenderStore, 'znn.act', { id: swapId, kind: 'znn.receive' })
    }
  }
  await until('the balance to settle', () => {
    const { status } = call(znnSenderStore, 'swap.refresh', { id: swapId })
    return status.swapAddress?.sufficient
  })
  check('the swap address holds the agreed amount', true)

  // ------------------------------------------------- the initiator funds
  step('the initiator funds the long leg')
  {
    const { status } = call(stores.participant, 'swap.refresh', { id: swapId })
    const fundAction = status.actions.find((a) => a.kind === 'sol.create' || a.kind === 'znn.create')
    check('the participant is told not to fund first', fundAction ? !fundAction.ready : true,
      fundAction?.blocked ?? 'no funding action offered')
  }

  if (initiatorSendsSol) {
    const sig = await sendSolanaAction(stores.initiator, swapId, 'sol.create', solKeys.initiator, 'initiator')
    console.log(`  escrow funded ${sig.slice(0, 16)}...`)
  } else {
    const r = call(stores.initiator, 'znn.act', { id: swapId, kind: 'znn.create' })
    console.log(`  htlc ${r.htlcId} confirmed=${r.confirmed}`)
  }

  await until('the participant to see and verify the initiator’s leg', () => {
    const { status } = call(stores.participant, 'swap.refresh', { id: swapId })
    const leg = initiatorSendsSol ? status.sol : status.znn
    return leg.funded && leg.verified
  })
  {
    const { status } = call(stores.participant, 'swap.refresh', { id: swapId })
    show('participant', status)
    const leg = initiatorSendsSol ? status.sol : status.znn
    check("the participant verified the initiator's leg against the terms", leg.verified, joinp(leg.problems))
    check('and is now told to fund theirs', primary(status).ready === true, primary(status).kind)
  }

  // ------------------------------------------------ the participant funds
  step('the participant funds the short leg')
  if (initiatorSendsSol) {
    const r = call(stores.participant, 'znn.act', { id: swapId, kind: 'znn.create' })
    console.log(`  htlc ${r.htlcId} confirmed=${r.confirmed}`)
  } else {
    const sig = await sendSolanaAction(stores.participant, swapId, 'sol.create', solKeys.participant, 'participant')
    console.log(`  escrow funded ${sig.slice(0, 16)}...`)
  }

  await until('the initiator to see and verify the participant’s leg', () => {
    const { status } = call(stores.initiator, 'swap.refresh', { id: swapId })
    const leg = initiatorSendsSol ? status.znn : status.sol
    return leg.funded && leg.verified
  })
  {
    const { status } = call(stores.initiator, 'swap.refresh', { id: swapId })
    show('initiator', status)
    const leg = initiatorSendsSol ? status.znn : status.sol
    check("the initiator verified the participant's leg", leg.verified, joinp(leg.problems))
    check('the Zenon HTLC id was found without anyone pasting it', !!call(stores.initiator, 'swap.get', { id: swapId }).swap.htlcId)
  }

  // ------------------------------------------------- the initiator claims
  step('the initiator claims, which publishes the secret')
  // Receive anything already waiting for the ZNN payout account first. Zenon
  // balances only move when the receiving account publishes a block, so an
  // unreceived transfer left by an earlier run would otherwise land inside this
  // run's before/after window and be counted as this swap's.
  wallet('receive', '-index', znnIndexOf[znnAddr[initiatorSendsSol ? 'initiator' : 'participant']])
  const balancesBefore = await balances(solKeys, znnIndexOf, znnAddr)
  const feesBefore = { ...feesPaid }
  if (initiatorSendsSol) {
    const r = call(stores.initiator, 'znn.act', { id: swapId, kind: 'znn.unlock' })
    console.log(`  unlocked ${r.tx} confirmed=${r.confirmed}`)
  } else {
    const sig = await sendSolanaAction(stores.initiator, swapId, 'sol.redeem', solKeys.initiator, 'initiator')
    console.log(`  redeemed ${sig.slice(0, 16)}...`)
  }

  // ------------------------------- the participant picks the secret up
  step(`the participant reads the secret off ${initiatorSendsSol ? 'Zenon' : 'Solana'}`)
  await until('the participant to find the secret', () => {
    const { status } = call(stores.participant, 'swap.refresh', { id: swapId })
    return status.secretKnown
  })
  {
    const { swap, status } = call(stores.participant, 'swap.refresh', { id: swapId })
    show('participant', status)
    check('the participant now knows the secret', status.secretKnown, status.secretFrom)
    check('and it is the initiator’s secret, not a lookalike',
      swap.swap.secret === applied.swap.secret)
    check('it was found on the chain the initiator claimed on',
      (status.secretFrom ?? '').includes(initiatorSendsSol ? 'Zenon' : 'Solana'), status.secretFrom)
  }

  // ------------------------------------------------ the participant claims
  step('the participant claims the other leg')
  if (initiatorSendsSol) {
    const sig = await sendSolanaAction(stores.participant, swapId, 'sol.redeem', solKeys.participant, 'participant')
    console.log(`  redeemed ${sig.slice(0, 16)}...`)
  } else {
    const r = call(stores.participant, 'znn.act', { id: swapId, kind: 'znn.unlock' })
    console.log(`  unlocked ${r.tx} confirmed=${r.confirmed}`)
  }

  // ------------------------------------------------------------- outcome
  step('both sides settle')
  // The ZNN payout arrives as an unreceived block in the recipient's own
  // wallet. Syrius does this automatically; here the stand-in does it.
  wallet('receive', '-index', znnIndexOf[znnAddr[initiatorSendsSol ? 'initiator' : 'participant']])
  for (const who of ['initiator', 'participant']) {
    const st = await until(`${who} to see both legs settled`, () => {
      const { status } = call(stores[who], 'swap.refresh', { id: swapId })
      return status.sol.settled && status.znn.settled ? status : null
    })
    show(who, st)
    check(`${who} records the swap as settled`, st.outcome?.startsWith('settled') === true, st.outcome)
  }

  const after = await balances(solKeys, znnIndexOf, znnAddr)
  const solPaidTo = initiatorSendsSol ? 'participant' : 'initiator'
  const znnPaidTo = initiatorSendsSol ? 'initiator' : 'participant'
  const feesAfter = { ...feesPaid }
  const solGain = after.sol[solPaidTo] - balancesBefore.sol[solPaidTo] + (feesAfter[solPaidTo] - feesBefore[solPaidTo])
  const znnGain = after.znn[znnPaidTo] - balancesBefore.znn[znnPaidTo]
  check(`the escrow paid the SOL receiver exactly ${SOL_AMOUNT} SOL`,
    solGain === Number(SOL_AMOUNT) * LAMPORTS_PER_SOL,
    `${solGain / LAMPORTS_PER_SOL} SOL, after ${feesAfter[solPaidTo] - feesBefore[solPaidTo]} lamports of their own fees`)
  check(`the ZNN receiver gained exactly ${ZNN_AMOUNT} ZNN`,
    Math.abs(znnGain - Number(ZNN_AMOUNT)) < 1e-8, `${znnGain} ZNN`)

  for (const dir of Object.values(stores)) rmSync(dir, { recursive: true, force: true })
  console.log(`\n${failures === 0 ? 'all checks passed' : `${failures} check(s) failed`}`)
  process.exit(failures === 0 ? 0 : 1)
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

const joinp = (p) => (p && p.length ? p.join('; ') : '')

main().catch((e) => {
  console.error(`\n${e.stack ?? e}`)
  process.exit(1)
})

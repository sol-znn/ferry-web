// Exercises the deployed solzen-htlc program against the local validator.
//
// The instruction encoding here is written out by hand rather than taken from
// wasm/sol/htlc.go, and that is the point: this is a second, independent
// reading of program/src/lib.rs. If the Go encoder and the Rust program ever
// drift, one of them still agrees with this file and the disagreement has a
// name.
//
// What it checks is the contract's promises, not its happy path: that a wrong
// preimage cannot spend, that a refund before the timelock cannot spend, that a
// redeem after expiry cannot spend, that the receiver is paid exactly the
// agreed amount and the initiator gets the rent back, and that the escrow is
// gone afterwards.
//
//   node scripts/program-test.mjs [rpc-url]

import { readFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import {
  Connection,
  Keypair,
  PublicKey,
  SystemProgram,
  Transaction,
  TransactionInstruction,
  LAMPORTS_PER_SOL,
  sendAndConfirmTransaction,
} from '@solana/web3.js'

const here = dirname(fileURLToPath(import.meta.url))
const root = join(here, '../..')
const RPC = process.argv[2] ?? 'http://127.0.0.1:8899'

const PDA_PREFIX = Buffer.from('htlc')
const ESCROW_LEN = 146

const programId = new PublicKey(
  Keypair.fromSecretKey(
    Uint8Array.from(JSON.parse(readFileSync(join(root, 'program/target/deploy/solzen_htlc-keypair.json'), 'utf8'))),
  ).publicKey,
)

const connection = new Connection(RPC, 'confirmed')

let failures = 0
function check(name, ok, detail = '') {
  console.log(`${ok ? '  ok  ' : ' FAIL '} ${name}${detail ? ` -- ${detail}` : ''}`)
  if (!ok) failures++
}

function escrowAddress(swapId) {
  return PublicKey.findProgramAddressSync([PDA_PREFIX, swapId], programId)
}

function createIx({ swapId, initiator, receiver, amount, hashlock, timelock }) {
  const [escrow] = escrowAddress(swapId)
  const data = Buffer.alloc(113)
  let o = 0
  data.writeUInt8(0, o); o += 1
  swapId.copy(data, o); o += 32
  receiver.toBuffer().copy(data, o); o += 32
  data.writeBigUInt64LE(BigInt(amount), o); o += 8
  hashlock.copy(data, o); o += 32
  data.writeBigInt64LE(BigInt(timelock), o)
  return new TransactionInstruction({
    programId,
    keys: [
      { pubkey: initiator, isSigner: true, isWritable: true },
      { pubkey: escrow, isSigner: false, isWritable: true },
      { pubkey: SystemProgram.programId, isSigner: false, isWritable: false },
    ],
    data,
  })
}

function redeemIx({ swapId, receiver, initiator, preimage }) {
  const [escrow] = escrowAddress(swapId)
  const data = Buffer.concat([Buffer.from([1, preimage.length]), preimage])
  return new TransactionInstruction({
    programId,
    keys: [
      { pubkey: escrow, isSigner: false, isWritable: true },
      { pubkey: receiver, isSigner: false, isWritable: true },
      { pubkey: initiator, isSigner: false, isWritable: true },
    ],
    data,
  })
}

function refundIx({ swapId, initiator, escrow: escrowOverride }) {
  const [derived] = escrowAddress(swapId)
  return new TransactionInstruction({
    programId,
    keys: [
      { pubkey: escrowOverride ?? derived, isSigner: false, isWritable: true },
      { pubkey: initiator, isSigner: false, isWritable: true },
    ],
    data: Buffer.from([2]),
  })
}

// A redeem naming an escrow account other than the one the swap id derives to.
// Used for the substitution cases, which are the ones that cost money: every
// other refusal in this program is a bad instruction, and these are a good
// instruction pointed at the wrong account.
function redeemIxAt({ escrow, receiver, initiator, preimage }) {
  const data = Buffer.concat([Buffer.from([1, preimage.length]), preimage])
  return new TransactionInstruction({
    programId,
    keys: [
      { pubkey: escrow, isSigner: false, isWritable: true },
      { pubkey: receiver, isSigner: false, isWritable: true },
      { pubkey: initiator, isSigner: false, isWritable: true },
    ],
    data,
  })
}

function decodeEscrow(data) {
  return {
    tag: data.readUInt8(0),
    swapId: data.subarray(1, 33),
    initiator: new PublicKey(data.subarray(33, 65)),
    receiver: new PublicKey(data.subarray(65, 97)),
    amount: data.readBigUInt64LE(97),
    hashlock: data.subarray(105, 137),
    timelock: data.readBigInt64LE(137),
    bump: data.readUInt8(145),
  }
}

async function send(ixs, signers) {
  const tx = new Transaction().add(...ixs)
  return sendAndConfirmTransaction(connection, tx, signers, {
    commitment: 'confirmed',
    skipPreflight: false,
  })
}

async function expectFail(name, ixs, signers) {
  try {
    await send(ixs, signers)
    check(name, false, 'the transaction succeeded')
  } catch (e) {
    check(name, true, firstLine(String(e.message ?? e)))
  }
}

function firstLine(s) {
  const m = s.split('\n')[0]
  return m.length > 90 ? `${m.slice(0, 90)}...` : m
}

async function fund(kp, sol) {
  const sig = await connection.requestAirdrop(kp.publicKey, sol * LAMPORTS_PER_SOL)
  const bh = await connection.getLatestBlockhash()
  await connection.confirmTransaction({ signature: sig, ...bh }, 'confirmed')
}

// The program compares its timelock against the cluster clock, which on a
// freshly started validator is not the machine's clock. Every deadline below is
// therefore built from this, not from Date.now().
async function chainNow() {
  const slot = await connection.getSlot('confirmed')
  const t = await connection.getBlockTime(slot)
  if (t == null) throw new Error('the validator has no block time yet')
  return t
}

const sha256 = (b) => createHash('sha256').update(b).digest()
const randomId = () => Buffer.from(crypto.getRandomValues(new Uint8Array(32)))

async function main() {
  console.log(`program ${programId.toBase58()}`)
  console.log(`rpc     ${RPC}`)
  console.log(`clock   ${new Date((await chainNow()) * 1000).toISOString()} (cluster)\n`)

  const alice = Keypair.generate() // initiator: locks the SOL
  const bob = Keypair.generate() // receiver: paid on a correct preimage
  const bystander = Keypair.generate() // pays fees, is party to nothing
  await Promise.all([fund(alice, 5), fund(bob, 1), fund(bystander, 1)])

  const preimage = Buffer.from(crypto.getRandomValues(new Uint8Array(32)))
  const hashlock = sha256(preimage)
  const amount = 0.25 * LAMPORTS_PER_SOL

  // ---------------------------------------------------------------- redeem
  {
    const swapId = randomId()
    const [escrow] = escrowAddress(swapId)
    const timelock = (await chainNow()) + 3600

    await send([createIx({ swapId, initiator: alice.publicKey, receiver: bob.publicKey, amount, hashlock, timelock })], [alice])

    const acc = await connection.getAccountInfo(escrow, 'confirmed')
    check('create opens an escrow owned by the program', acc?.owner.equals(programId) === true)
    check('the escrow is the documented length', acc?.data.length === ESCROW_LEN, `${acc?.data.length} bytes`)
    const state = decodeEscrow(acc.data)
    check('it records the parties and the amount',
      state.initiator.equals(alice.publicKey) && state.receiver.equals(bob.publicKey) && state.amount === BigInt(amount))
    check('it records the hashlock and timelock',
      state.hashlock.equals(hashlock) && state.timelock === BigInt(timelock))
    check('the escrow holds the amount plus its own rent', acc.lamports > amount,
      `${acc.lamports} lamports, rent ${acc.lamports - amount}`)
    const rent = acc.lamports - amount

    await expectFail('a wrong preimage cannot redeem',
      [redeemIx({ swapId, receiver: bob.publicKey, initiator: alice.publicKey, preimage: sha256(Buffer.from('not it')) })],
      [bystander])

    await expectFail('a redeem paying the wrong receiver is refused',
      [redeemIx({ swapId, receiver: bystander.publicKey, initiator: alice.publicKey, preimage })],
      [bystander])

    const bobBefore = await connection.getBalance(bob.publicKey, 'confirmed')
    const aliceBefore = await connection.getBalance(alice.publicKey, 'confirmed')

    // Signed by a third party who is not in the escrow at all: the preimage is
    // the authorization, so neither Alice nor Bob has to be online for the swap
    // to settle.
    const sig = await send([redeemIx({ swapId, receiver: bob.publicKey, initiator: alice.publicKey, preimage })], [bystander])

    const bobAfter = await connection.getBalance(bob.publicKey, 'confirmed')
    const aliceAfter = await connection.getBalance(alice.publicKey, 'confirmed')
    check('a third party can redeem with the preimage', true, sig.slice(0, 16) + '...')
    check('the receiver is paid exactly the agreed amount', bobAfter - bobBefore === amount,
      `${(bobAfter - bobBefore) / LAMPORTS_PER_SOL} SOL`)
    check('the rent deposit goes back to the initiator', aliceAfter - aliceBefore === rent,
      `${aliceAfter - aliceBefore} lamports`)
    check('the escrow account is gone', (await connection.getAccountInfo(escrow, 'confirmed')) === null)

    // The whole point of publishing the preimage on chain: the counterparty
    // reads it back out of the transaction that spent the escrow.
    const tx = await connection.getTransaction(sig, { commitment: 'confirmed', maxSupportedTransactionVersion: 0 })
    const ix = tx.transaction.message.compiledInstructions ?? tx.transaction.message.instructions
    const found = ix.map((i) => Buffer.from(i.data)).find((d) => d[0] === 1)
    check('the preimage is recoverable from the chain', found?.subarray(2).equals(preimage) === true)

    await expectFail('the same escrow cannot be redeemed twice',
      [redeemIx({ swapId, receiver: bob.publicKey, initiator: alice.publicKey, preimage })], [bystander])
  }

  // ---------------------------------------------------------------- refund
  {
    const swapId = randomId()
    const [escrow] = escrowAddress(swapId)
    const timelock = (await chainNow()) + 12

    await expectFail('a timelock in the past is refused at creation',
      [createIx({ swapId: randomId(), initiator: alice.publicKey, receiver: bob.publicKey, amount, hashlock, timelock: (await chainNow()) - 1 })],
      [alice])

    await send([createIx({ swapId, initiator: alice.publicKey, receiver: bob.publicKey, amount, hashlock, timelock })], [alice])
    const funded = await connection.getAccountInfo(escrow, 'confirmed')

    await expectFail('a refund before the timelock is refused',
      [refundIx({ swapId, initiator: alice.publicKey })], [bystander])

    process.stdout.write(`       waiting for the cluster clock to pass the timelock`)
    while ((await chainNow()) < timelock) {
      process.stdout.write('.')
      await new Promise((r) => setTimeout(r, 1000))
    }
    console.log('')

    await expectFail('a redeem after the timelock is refused, even with the preimage',
      [redeemIx({ swapId, receiver: bob.publicKey, initiator: alice.publicKey, preimage })], [bystander])

    // Asked here, where the timelock has already passed, so the only thing
    // left to refuse it is the initiator not being the escrow's. Asked before
    // the wait it would fail on the clock and prove nothing.
    await expectFail('a refund cannot be redirected to another account',
      [refundIx({ swapId, initiator: bystander.publicKey })], [bystander])

    const aliceBefore = await connection.getBalance(alice.publicKey, 'confirmed')
    await send([refundIx({ swapId, initiator: alice.publicKey })], [bystander])
    const aliceAfter = await connection.getBalance(alice.publicKey, 'confirmed')

    check('a third party can refund after the timelock', true)
    check('the initiator gets the amount and the rent back',
      aliceAfter - aliceBefore === funded.lamports, `${aliceAfter - aliceBefore} of ${funded.lamports} lamports`)
    check('the escrow account is gone', (await connection.getAccountInfo(escrow, 'confirmed')) === null)
  }

  // ------------------------------------------------- substituted accounts
  //
  // The refusals above are all about bad instructions. These are about good
  // instructions pointed at the wrong account, which is the family that
  // actually loses money in escrow programs and the one `load` exists for: it
  // re-derives the PDA from the swap id inside the account's own data, so an
  // escrow cannot be passed off as another.
  {
    const swapIdA = randomId()
    const swapIdB = randomId()
    const [escrowA] = escrowAddress(swapIdA)
    const [escrowB] = escrowAddress(swapIdB)
    const timelock = (await chainNow()) + 3600

    const preimageB = Buffer.from(crypto.getRandomValues(new Uint8Array(32)))

    // Two live escrows: A pays bob, B pays the bystander and commits to a
    // hashlock whose preimage the attacker holds.
    await send([createIx({ swapId: swapIdA, initiator: alice.publicKey, receiver: bob.publicKey, amount, hashlock, timelock })], [alice])
    await send([createIx({ swapId: swapIdB, initiator: bystander.publicKey, receiver: bystander.publicKey, amount: 0.05 * LAMPORTS_PER_SOL, hashlock: sha256(preimageB), timelock })], [bystander])

    // The whole point of re-deriving: B's preimage opens B, and naming A's
    // accounts beside it must not make it open A.
    await expectFail('a preimage for one escrow cannot spend another',
      [redeemIxAt({ escrow: escrowA, receiver: bystander.publicKey, initiator: alice.publicKey, preimage: preimageB })],
      [bystander])

    // An account this program does not own is not an escrow, whatever its
    // bytes happen to say. bob's own wallet account stands in for one.
    await expectFail('an account this program does not own is not an escrow',
      [refundIx({ swapId: swapIdA, initiator: alice.publicKey, escrow: bob.publicKey })], [bystander])

    await expectFail('the same swap id cannot be created twice',
      [createIx({ swapId: swapIdA, initiator: alice.publicKey, receiver: bob.publicKey, amount, hashlock, timelock })],
      [alice])

    await expectFail('a zero amount is refused at creation',
      [createIx({ swapId: randomId(), initiator: alice.publicKey, receiver: bob.publicKey, amount: 0, hashlock, timelock })],
      [alice])

    await expectFail('an empty preimage is refused',
      [redeemIxAt({ escrow: escrowA, receiver: bob.publicKey, initiator: alice.publicKey, preimage: Buffer.alloc(0) })],
      [bystander])

    // Both escrows still hold what they were given: none of the above moved
    // anything, which is the assertion that matters.
    const a = await connection.getAccountInfo(escrowA, 'confirmed')
    const b = await connection.getAccountInfo(escrowB, 'confirmed')
    check('nothing above moved any money', a !== null && b !== null,
      `${a?.lamports ?? 0} and ${b?.lamports ?? 0} lamports still held`)

    // And the legitimate spends still work, so the checks above are refusing
    // the right thing rather than everything.
    await send([redeemIx({ swapId: swapIdA, receiver: bob.publicKey, initiator: alice.publicKey, preimage })], [bystander])
    await send([redeemIxAt({ escrow: escrowB, receiver: bystander.publicKey, initiator: bystander.publicKey, preimage: preimageB })], [bystander])
    check('both escrows still redeem with their own preimage',
      (await connection.getAccountInfo(escrowA, 'confirmed')) === null &&
      (await connection.getAccountInfo(escrowB, 'confirmed')) === null)
  }

  // --------------------------------------------------------- prefunded PDA
  //
  // The escrow address is a pure function of a swap id that travels in the
  // offer string, so anyone who has seen an offer can compute it and pay it.
  // create_account fails outright on an account that already holds lamports,
  // so a stranger's payment would kill the trade; the program opens the
  // account the long way when it has to.
  //
  // The price of that grief is not one lamport, which is worth knowing: the
  // runtime refuses to leave any account below rent exemption, so the smallest
  // balance a stranger can park here is the rent-exempt minimum for a
  // zero-length account. Still cheap, still refundable to nobody, and still
  // enough to end a swap that has not been funded yet.
  {
    const swapId = randomId()
    const [escrow] = escrowAddress(swapId)
    const timelock = (await chainNow()) + 3600
    const dust = await connection.getMinimumBalanceForRentExemption(0)

    await send([SystemProgram.transfer({ fromPubkey: bystander.publicKey, toPubkey: escrow, lamports: dust })], [bystander])
    const griefed = await connection.getAccountInfo(escrow, 'confirmed')
    check('a stranger can pay the escrow address before it exists',
      griefed?.lamports === dust, `${dust} lamports, the least the runtime allows`)

    await send([createIx({ swapId, initiator: alice.publicKey, receiver: bob.publicKey, amount, hashlock, timelock })], [alice])
    const acc = await connection.getAccountInfo(escrow, 'confirmed')
    check('and the swap can still be funded', acc?.owner.equals(programId) === true)
    check('with the amount plus its rent, whoever contributed the balance',
      acc !== null && acc.lamports >= amount, `${acc?.lamports ?? 0} lamports`)

    const bobBefore = await connection.getBalance(bob.publicKey, 'confirmed')
    await send([redeemIx({ swapId, receiver: bob.publicKey, initiator: alice.publicKey, preimage })], [bystander])
    const bobAfter = await connection.getBalance(bob.publicKey, 'confirmed')
    check('and it pays the receiver exactly the agreed amount', bobAfter - bobBefore === amount,
      `${(bobAfter - bobBefore) / LAMPORTS_PER_SOL} SOL`)
  }

  console.log(`\n${failures === 0 ? 'all checks passed' : `${failures} check(s) failed`}`)
  process.exit(failures === 0 ? 0 : 1)
}

main().catch((e) => {
  console.error(e)
  process.exit(1)
})

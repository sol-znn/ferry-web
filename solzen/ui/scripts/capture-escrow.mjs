// Opens one escrow on the local validator and prints it, so the Go tests can
// pin their decoder against bytes the deployed program actually wrote.
//
// Run it after a deploy that changes the account layout, and paste the output
// into wasm/sol/htlc_test.go. A fixture written by hand would only prove the
// decoder agrees with whoever wrote it.
//
//   node scripts/capture-escrow.mjs

import { readFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  Connection, Keypair, PublicKey, SystemProgram, Transaction,
  TransactionInstruction, LAMPORTS_PER_SOL, sendAndConfirmTransaction,
} from '@solana/web3.js'

const root = join(dirname(fileURLToPath(import.meta.url)), '../..')
const RPC = process.env.SOLZEN_SOLANA_URL ?? 'http://127.0.0.1:8899'

const programId = new PublicKey(
  Keypair.fromSecretKey(
    Uint8Array.from(JSON.parse(readFileSync(join(root, 'program/target/deploy/solzen_htlc-keypair.json'), 'utf8'))),
  ).publicKey,
)
const conn = new Connection(RPC, 'confirmed')

const initiator = Keypair.generate()
const receiver = Keypair.generate()
const sig = await conn.requestAirdrop(initiator.publicKey, 3 * LAMPORTS_PER_SOL)
const bh = await conn.getLatestBlockhash()
await conn.confirmTransaction({ signature: sig, ...bh }, 'confirmed')

// Deliberately legible values, so a test reading them back is checking
// arithmetic rather than echoing a blob.
const swapId = Buffer.alloc(32)
for (let i = 0; i < 32; i++) swapId[i] = i
const preimage = Buffer.alloc(32, 's')
const hashlock = createHash('sha256').update(preimage).digest()
const amount = 250_000_000
const timelock = (await conn.getBlockTime(await conn.getSlot('confirmed'))) + 7200

const [escrow] = PublicKey.findProgramAddressSync([Buffer.from('htlc'), swapId], programId)
const data = Buffer.alloc(113)
let o = 0
data.writeUInt8(0, o); o += 1
swapId.copy(data, o); o += 32
receiver.publicKey.toBuffer().copy(data, o); o += 32
data.writeBigUInt64LE(BigInt(amount), o); o += 8
hashlock.copy(data, o); o += 32
data.writeBigInt64LE(BigInt(timelock), o)

await sendAndConfirmTransaction(
  conn,
  new Transaction().add(new TransactionInstruction({
    programId,
    data,
    keys: [
      { pubkey: initiator.publicKey, isSigner: true, isWritable: true },
      { pubkey: escrow, isSigner: false, isWritable: true },
      { pubkey: SystemProgram.programId, isSigner: false, isWritable: false },
    ],
  })),
  [initiator],
  { commitment: 'confirmed' },
)

const acc = await conn.getAccountInfo(escrow, 'confirmed')
console.log(JSON.stringify({
  base64: acc.data.toString('base64'),
  lamports: acc.lamports,
  amount,
  timelock,
  initiator: initiator.publicKey.toBase58(),
  receiver: receiver.publicKey.toBase58(),
  hashlock: hashlock.toString('hex'),
  preimage: preimage.toString('hex'),
  escrow: escrow.toBase58(),
  programId: programId.toBase58(),
}, null, 2))

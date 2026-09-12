// The shapes the WebAssembly module returns, mirroring wasm/api.go, wasm/swap.go
// and wasm/boardpost.go.
//
// Every []byte on the Go side is rendered as hex here, never base64 — nothing
// else in this system speaks base64, and a field named `…Hex` that is not hex is
// the kind of thing that gets noticed only when a swap is already funded.

import type {ChainId} from '@/core/chains'

export type {ChainId}

export type Role = 'initiator' | 'participant'

/** Which way one leg moves for this user: `out` is the leg they fund, `in` the
 *  leg the counterparty funds. */
export type Dir = 'out' | 'in'

export type State = 'draft' | 'awaiting_funding' | 'funded' | 'settled' | 'refunded' | 'expired'

export interface FundingOutput {
  txid: string
  vout: number
  value: number
  /** False while the payment is still only in a mempool. */
  confirmed?: boolean
  blockHeight?: number
  /**
   * How many blocks are on top of it as of the last refresh, counting its own —
   * so a freshly mined funding is 1. Counted up to six and then pinned there.
   */
  confirmations?: number
}

/**
 * A payment to a contract this browser has sent but not yet seen on chain.
 * Not funding, and never to be treated as such: only a refresh that finds the
 * output at the contract address can say a contract holds money. What it is for
 * is stopping the card offering to send a second payment.
 */
export interface FundingBroadcast {
  txid: string
  at: string
}

export interface SpendResult {
  rawHex: string
  txid: string
  fee: number
  vsize: number
  value: number
}

/** The public half of a Bitcoin leg's ephemeral keypair. The private key comes
 *  back only from the recovery call, never in a list response. */
export interface KeyView {
  pubHex: string
  pkhHex: string
}

/** A Bitcoin P2SH contract, as the page reads it. */
export interface BtcLegView {
  key?: KeyView
  counterpartyPkhHex?: string
  contractHex?: string
  contractAddr?: string
  funding?: FundingOutput
  fundingBroadcast?: FundingBroadcast
  refundTx?: SpendResult
  redeemTx?: SpendResult
  claimed?: boolean
  reclaimed?: boolean
  /** A contract funded for less than was agreed. */
  fundingShort?: boolean
  /** This side funded it, money is in it, nothing spent it, the clock passed. */
  refundable?: boolean
}

/** One entry in go-zenon's `htlc` embedded contract. */
export interface ZnnLegView {
  htlcId?: string
  /** The token the entry actually holds. A fact, never an expectation — the
   *  agreed one is `Leg.token`, and substituting it is exactly the attack. */
  observedToken?: string
  observedHashLocked?: string
  /** The duration this leg should be created with: hours for znn-cli, seconds
   *  for the block a wallet signs. */
  expirationHours?: number
  expirationSeconds?: number
  hashType: number
  keyMaxSize: number
  verified: boolean
  verifyError?: string
  /** Not checked against the chain YET — not checked and found wanting. */
  verifyPending?: boolean
  /** The transaction THIS browser published to unlock this leg. An unlock
   *  deletes the entry, so this is the only record that it happened. */
  unlockHash?: string
  /** The mirror for the side that did not publish it: the counterparty's unlock
   *  was FOUND on the chain. Never inferred from merely holding a preimage. */
  unlockSeen?: boolean
  reclaimHash?: string
}

/** One escrow account of the ferry-htlc Solana program. */
export interface SolLegView {
  /** The deployment this leg lives on. A TERM of the trade rather than a
   *  setting: two deployments of the same source are two different contracts. */
  programId?: string
  /** The 32-byte PDA seed, hex. Both sides derive the same escrow from it. */
  swapId?: string
  escrow?: string
  lamports?: number
  funded?: boolean
  observedAmount?: number
  observedLamports?: number
  observedInitiator?: string
  observedReceiver?: string
  observedTimelock?: number
  verified: boolean
  verifyError?: string
  verifyPending?: boolean
  createSig?: string
  redeemSig?: string
  refundSig?: string
  redeemed?: boolean
  refunded?: boolean
}

/** One half of a swap. */
export interface LegView {
  chain: ChainId
  dir: Dir
  /** "10 ZNN", "400000 sat", "1.5 SOL" — built in Go so two screens agree. */
  label: string
  token?: string
  tokenName?: string
  /** The agreed size as a person types it, in the chain's own unit. */
  amount: string
  /** The same figure in base units, once it is known. */
  base?: string
  unit?: string
  expiry?: number
  expiryAt?: string
  /** This user's address on this chain; the counterparty's. */
  selfAddr?: string
  peerAddr?: string
  /** Whether this leg takes the long timelock. */
  isInitiators: boolean

  /** The three questions every card asks of a leg, answered the same way
   *  whatever chain it is on. */
  funded: boolean
  verified: boolean
  done: boolean
  /** Which way a done leg finished: taken back by whoever funded it, rather
   *  than claimed by whoever was owed it. */
  reclaimed?: boolean
  expired?: boolean

  btc?: BtcLegView
  znn?: ZnnLegView
  sol?: SolLegView
}

export interface SwapEvent {
  at: string
  message: string
}

export interface Swap {
  id: string
  createdAt: string
  network: string
  role: Role
  state: State

  /** The stable name of the trade, and every chain it settles on. The board's
   *  proof rule is written over the second. */
  pair: string
  chains: ChainId[]

  /** `out` is the leg this user funds; `in` the one they receive. */
  out: LegView
  in: LegView

  secretHashHex: string
  secretHex?: string

  /** Active is whether the swap is still on the Swaps page; archived is whether
   *  the user filed it away by hand. Settled is the separate question: over,
   *  with nothing left to do but file it. */
  active: boolean
  archived: boolean
  settled: boolean

  outIsInitiators: boolean
  /** The chain this user learns the preimage on, or absent when they made it. */
  secretArrivesOn?: ChainId

  events: SwapEvent[]
}

export interface Config {
  network: string
  backend: string
  znn: boolean
  sol: boolean
  /** The Solana program a new leg would be created on, from the module. */
  solProgram?: string
  /**
   * Which instance the signing module was built as, 'dev' or 'prod'. Reported
   * by the module rather than read from the page, so the header can check that
   * the two halves of this build agree.
   */
  buildEnv: string
  /** Where swaps are kept — browser storage, named so the status line can say so. */
  swapDir: string
  secretSize: number
  /** Whether each side is on a URL this browser chose or on the built-in default. */
  usingOwnBtc: boolean
  usingOwnZnn: boolean
  usingOwnSol: boolean
  /** The trades this build will let somebody agree to, and what each chain is
   *  called. Sent from the module so the create form and the board's gates read
   *  one list rather than a second copy of it in JavaScript. */
  pairs?: {id: string; label: string; chains: ChainId[]; a: ChainId; b: ChainId}[]
  chains?: {id: ChainId; label: string; unit: string; tokenised: boolean; scheme: ProofScheme}[]
  activeSwaps?: number
  historySwaps?: number
  tipHeight?: number
  chainError?: string
  znnHeight?: number
  znnError?: string
  solVersion?: string
  solTime?: number
  solError?: string
}

/** This browser's network and node choices, sent with every call. */
export interface Settings {
  network: string
  btcEsplora?: string
  znnUrl?: string
  solRpc?: string
  /** Which deployment of the swap program new Solana legs use. A setting here
   *  and a term on a swap: a leg carries the program it was made for. */
  solProgram?: string
  /**
   * Nostr relays for the session feature, whitespace- or comma-separated. Not
   * sent to the module: sessions are sealed before they reach a relay.
   */
  relays?: string
}

/** One half of a proposal, as it travels in a `swapoffer2:` string. */
export interface OfferLeg {
  chain: ChainId
  token?: string
  /** As typed, in the chain's unit. */
  amount: string
  /** The SENDER's address on this chain. */
  addr?: string
  /** Bitcoin only: the sender's pubkey hash. */
  pkh?: string
  expiry?: number
  /** Solana only. */
  program?: string
  swapId?: string
}

/** The public half of a swap. Mirrors the Offer struct in wasm/swap.go — which
 *  deliberately has no field for a secret or a key, so there is no path that
 *  puts one in a chat window. */
export interface OfferBody {
  v: number
  network: string
  /** The sender's role; the receiver takes the other one. */
  fromRole: Role
  secretHash: string
  /** What the SENDER funds, and what they receive. The receiver mirrors them. */
  give: OfferLeg
  take: OfferLeg
  note?: string
}

export interface DecodedOffer {
  decoded: OfferBody
  /** Which legs the receiver should take, worked out in Go so no UI has to. */
  yourOut: OfferLeg
  yourIn: OfferLeg
  yourRole: Role
  pair: string
}

/** What the recovery download contains: everything needed to spend a Bitcoin
 *  contract without this app, which is the point of it. A Zenon leg is reclaimed
 *  from the wallet that created it and a Solana escrow refunds to the account
 *  that funded it, so neither needs a key exported from here. */
export interface RecoveryFile {
  swapId: string
  network: string
  pair: string
  dir: Dir
  contractHex: string
  contractAddr: string
  lockTime: number
  lockTimeUTC: string
  privateKeyWIF: string
  destAddr?: string
  funding?: FundingOutput
  secretHex?: string
  presignedRefundHex?: string
  presignedRefundTxid?: string
  otherLeg?: Record<string, unknown>
  README: string[]
}

/** One way out of a contract, priced. Mirrors branchCost in wasm/estimate.go. */
export interface BranchCost {
  vsize: number
  fee: number
  /** What lands at the destination. Negative when the amount is smaller than
   *  its own unlock fee, which is a thing worth showing rather than clamping. */
  net: number
  viable: boolean
}

/**
 * What it costs to get back out of a Bitcoin contract — the arithmetic that
 * decides whether an agreed amount can actually be unlocked.
 */
export interface SpendCost {
  amountSats: number
  dustLimit: number

  feeRate: number
  /** 'you' | 'node' | 'fallback' — whether this reflects the live fee market. */
  feeRateFrom: string

  destAssumed: boolean
  contractAssumed: boolean

  redeem: BranchCost
  refund: BranchCost

  minRelayable: number
  minAtFeeRate: number
  recommended: number
  headroomRate: number
  maxFeeRate: number

  verdict: 'ok' | 'tight' | 'unspendable'
  summary: string
}

/**
 * One party's claim about which chains they are on. Mirrors wasm/chainid.go.
 *
 * The anchor hash is the load-bearing field. A network name settles nothing —
 * everybody's regtest is their own — so the real check is the hash of a block
 * both sides should already have.
 */
export interface ChainFingerprint {
  network: string
  btcHeight?: number
  btcAnchorHeight?: number
  btcAnchorHash?: string
  btcError?: string
  znnHeight?: number
  znnAnchorHeight?: number
  znnAnchorHash?: string
  znnError?: string
}

/** What came of comparing two fingerprints. */
export interface ChainAgreement {
  ok: boolean
  sameChain: boolean
  problems?: string[]
  unchecked?: string[]
}

/** A session room, as one code derives it. Mirrors wasm/session.go. */
export interface SessionRoom {
  code: string
  display: string
  pubKey: string
  roomId: string
  kind: number
}

/**
 * One thing a side has to say. Every field is public swap data.
 *
 * There is deliberately no field for the preimage. Revealing it before claiming
 * hands the counterparty both legs, so it has no field here, no code path that
 * would send one, and nothing that would accept one.
 */
export interface SessionMessage {
  type:
    | 'hello'
    | 'ping'
    | 'bye'
    | 'offer'
    /** Where the sender wants each half paid, sent before any swap exists. */
    | 'addresses'
    | 'pkh'
    | 'contract'
    | 'zenon'
    | 'solana'
    | 'funded'
    | 'note'
    | 'resync'
    | 'moved'
  from?: Role
  /** The chain this hand-off is about, so a receiver applies it to the right
   *  half of the trade rather than to whichever half it guessed. */
  leg?: ChainId
  /** Random per session. Tells their message from our own coming back. */
  peerId?: string
  chain?: ChainFingerprint
  offer?: string
  pkhHex?: string
  contractHex?: string
  htlcId?: string
  zenonAddr?: string
  btcAddr?: string
  solAddr?: string
  solProgram?: string
  solSwapId?: string
  fundingTxid?: string
  note?: string
  sentAt?: number
}

/** One offline rescue attempt: the saved file, plus what to change about it. */
export interface RebuildRequest {
  file: string
  destAddr?: string
  feeRate?: number
  secretHex?: string
}

export interface RebuildResult {
  action: 'redeem' | 'refund'
  swapId: string
  network: string
  contractAddr: string
  spending: string
  destAddr: string
  feeRate: number

  rawHex: string
  txid: string
  fee: number
  vsize: number
  value: number

  validFrom?: string
  notYet?: string
  presignedRefundHex?: string
}

/**
 * The three things a wallet can be asked to do on a Zenon leg. Named rather
 * than inferred, because each is refused from the wrong side and a name in the
 * request is what lets Go say WHICH wrong thing was asked for.
 */
export type WalletAction = 'create' | 'unlock' | 'reclaim'

/** The three things a wallet can be asked to do on a Solana leg. */
export type SolAction = 'create' | 'redeem' | 'refund'

/** One Solana instruction, as Go builds it. The page assembles a transaction
 *  around it and hands that to a wallet; nothing about the instruction itself is
 *  decided in JavaScript. */
export interface SolInstruction {
  programId: string
  accounts: {pubkey: string; isSigner?: boolean; isWritable?: boolean}[]
  /** base64 — what survives JSON without a byte-array dance. */
  data: string
}

/** The account block znn-ts-sdk's `AccountBlockTemplate.fromJson` reads. */
export interface WalletAccountBlock {
  version: number
  chainIdentifier: number
  blockType: number
  hash: string
  previousHash: string
  height: number
  momentumAcknowledged: {hash: string; height: number}
  address: string
  toAddress: string
  amount: string
  tokenStandard: string
  fromBlockHash: string
  /** Base64-encoded ABI call data. This is what makes it an HTLC call. */
  data: string
  fusedPlasma: number
  difficulty: number
  nonce: string
  publicKey: string
  signature: string
}

/**
 * The verdict on whether the wallet and this page are looking at the same chain,
 * with the same account selected. `ok` is the gate: false blocks the send, and
 * there is no override.
 */
export interface WalletSync {
  ok: boolean

  address: string
  nodeUrl?: string
  sameNodeUrl: boolean

  chainIdentifier: number
  walletChainIdentifier?: number
  reportedChainId?: number

  sameChain: boolean
  anchorHeight?: number
  anchorHash?: string

  height?: number
  walletHeight?: number

  problems?: string[]
  unchecked?: string[]
  warnings?: string[]
}

/** One proposed HTLC call: the block, and what it says in words. */
export interface WalletBlockPlan {
  action: WalletAction
  block: WalletAccountBlock
  summary: string
  warnings?: string[]

  signer: string
  sync?: WalletSync

  hashLocked?: string
  expirationTime?: number
  expiresAt?: string
  amountDisplay?: string
  tokenSymbol?: string
  htlcId?: string
}

// ---------- the board ----------
//
// Mirrors wasm/board.go and wasm/boardpost.go. The board is public by
// construction, so unlike a session none of it is encrypted — the exception is
// a Take, which carries a session code and is sealed to one recipient.

/** How an address was proven to belong to a board key. */
export type ProofScheme = 'btc-ecdsa' | 'znn-ed25519' | 'sol-ed25519'

/** One address, proven to belong to whoever holds a board key. */
export interface WalletProof {
  scheme: ProofScheme
  address: string
  /** Only for `znn-ed25519`: an ed25519 signature does not carry its own key,
   *  a Bitcoin compact signature does, and a Solana address IS the key. */
  pubKey?: string
  /** base64 for Bitcoin — what every wallet returns — and hex for the two
   *  ed25519 schemes. */
  sig: string
  /** When THIS browser checked it. Cleared before publishing: a reader
   *  re-verifies rather than believing a stranger's timestamp. */
  verifiedAt?: string
}

export type PostStatus = 'open' | 'taken' | 'done' | 'void'

/** One half of an advertised trade. */
export interface PostLeg {
  chain: ChainId
  /** The ZTS on a Zenon leg. Blank means ZNN. */
  token?: string
  /** The headline size, in the chain's own unit. */
  amount: string
  /** A size band the author will trade within, on the leg they FUND. A band on
   *  both halves would be a band on the rate, which is a different offer. */
  min?: string
  max?: string
}

/** An offer as it appears on the board. Deliberately not an OfferBody: that one
 *  pins the terms of a swap that already exists, and this is the advertisement
 *  before there is one. */
export interface BoardPost {
  v: number
  /** The `d` tag — the addressable slot, and so the identity of the offer
   *  rather than of one version of it. */
  id: string
  network: string
  /** What the AUTHOR funds, and what they receive. A taker takes the mirror. */
  give: PostLeg
  want: PostLeg
  role: Role
  lockHours?: number
  /** The author's advertised addresses, keyed by chain. */
  addrs?: Partial<Record<ChainId, string>>
  proofs?: WalletProof[]
  status: PostStatus
  note?: string
  createdAt: number
  expiresAt: number
  /** Self-reported, unverifiable, and shown labelled as the claim it is. */
  completed?: number
}

/** A post as a reader gets it: the terms, who wrote them, and what survived
 *  checking. */
export interface Listing {
  post: BoardPost
  /** The x-only key that signed it, and the only durable identity here. */
  author: string
  eventId: string
  publishedAt: number
  /** Schemes whose proofs checked out here, against this reader's network. */
  verified?: ProofScheme[]
  problems?: string[]
  mine: boolean
  expired: boolean
}

/** This browser's board key, and what it has been bound to. */
export interface BoardIdentity {
  pubKey: string
  bindings: WalletProof[]
  createdAt: string
  kinds: {post: number; take: number; delete: number; presence: number; tag: string}
  limits: {defaultTtl: number; minTtl: number; maxTtl: number}
  presence: {beatSeconds: number; staleSeconds: number}
}

/** A verified presence beat: whose it was, and when they were last at a
 *  keyboard. Not a boolean, deliberately — see wasm/board.go. */
export interface Seen {
  author: string
  seenAt: number
  staleAfter: number
}

/** One of this browser's own posts, as last published. */
export interface MyPost {
  post: BoardPost
  swapId?: string
  code?: string
  taker?: string
  publishedAt: string
}

/** A reader saying "this one, and here is where to meet". The code is the whole
 *  payload and the reason a take is sealed rather than posted in the open. */
export interface Take {
  v: number
  postId: string
  code: string
  /** The size the taker wants, in the unit of the leg its author funds. */
  amount?: string
  /** Where the TAKER wants each half paid. Sealed with the rest of the take, so
   *  a taker answering one stranger tells only that stranger where their money
   *  goes. */
  addrs?: Partial<Record<ChainId, string>>
  note?: string
  sentAt?: number
}

/** A take, opened, with the key that sent it. */
export interface InboundTake {
  take: Take
  /** The taker's board key — the author's only handle on them. */
  from: string
  eventId: string
  at: number
}

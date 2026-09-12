// The shapes the WebAssembly module returns, mirroring wasm/api.go and
// wasm/swap.go.
//
// Every []byte on the Go side is rendered as hex here, never base64 — nothing
// else in this system speaks base64, and a field named `…Hex` that is not hex
// is the kind of thing that gets noticed only when a swap is already funded.

export type Role = 'initiator' | 'participant'

/** Which side of the Bitcoin leg this user is on. */
export type Leg = 'send' | 'receive'

export type State = 'draft' | 'awaiting_funding' | 'funded' | 'redeemed' | 'refunded' | 'expired'

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
 * A payment to the contract this browser has sent but not yet seen on chain.
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

/** The public half of the swap's ephemeral keypair. The private key comes back
 *  only from the recovery call, never in a list response. */
export interface KeyView {
  pubHex: string
  pkhHex: string
}

/** The other half of the trade. This app reads Zenon state but never writes it. */
export interface MissingZenonTerm {
  key: 'selfAddress' | 'peerAddress' | 'amount'
  reason: string
}

export interface ZenonLeg {
  htlcId?: string
  selfAddress?: string
  peerAddress?: string
  /** The token that was AGREED. Verification never overwrites it with the one
   *  the HTLC happens to hold — that substitution is exactly the attack. */
  tokenStandard?: string
  /** The token the entry actually holds, so a mismatch can name both halves. */
  observedToken?: string
  /** Base units, as the node's RPC returns them — what verification compares. */
  amount?: string
  /** The decimal amount a human types, which is what the CLIs expect. */
  amountDisplay?: string
  expirationHours?: number
  expirationSeconds?: number
  expirationTime?: number
  hashType: number
  keyMaxSize: number
  verified: boolean
  verifyError?: string
  /** Not checked against the chain YET — not checked and found wanting. */
  verifyPending?: boolean
  /**
   * The transaction this browser published to unlock the counterparty's HTLC. An
   * unlock deletes the entry, so afterwards no node can be asked whether this leg
   * has been collected -- a settled leg and an id that never existed answer
   * identically. This is the only record that it happened.
   */
  unlockHash?: string
  /**
   * The mirror of `unlockHash` for the side that did not publish it: the
   * counterparty's unlock of this leg was found on the chain. Set only where the
   * unlocking transaction itself was found, never inferred from holding a
   * preimage — one pasted in by hand hashes correctly with nothing observed.
   */
  unlockSeen?: boolean
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
  leg: Leg
  state: State

  amountSats: number
  destAddr?: string

  secretHashHex: string
  secretHex?: string
  contractHex?: string
  counterpartyPkhHex?: string

  contractAddr?: string
  key?: KeyView

  lockTime: number
  lockTimeAt: string
  refundable: boolean

  /** Active is whether the swap is still on the Swaps page; archived is whether
   *  the user filed it away by hand. Nothing files itself, so these are one
   *  question — both are kept because one picks a list and the other points the
   *  archive button. */
  active: boolean
  archived: boolean

  /** Over, with nothing left to do but file it. A settled swap is still active:
   *  it shows as a summary row with an Archive button until the user presses
   *  one. Computed in Go (Swap.Settled) because the case where a Bitcoin redeem
   *  does NOT end the swap is a rule, not a state name. */
  settled: boolean

  /** Which leg the Bitcoin contract is, which decides the timelock ordering
   *  and which side reveals the preimage where. Computed in Go rather than
   *  derived here, so the rule lives in exactly one place. */
  btcLegIsInitiators: boolean
  zenonHtlcIsOurs: boolean
  secretArrivesOnZenon: boolean
  /** A contract funded for less than was agreed. */
  fundingShort?: boolean
  /** The redeem is withheld: the contract is short AND this side's redeem
   *  would be the first publication of its secret, which is what opens the
   *  Zenon leg for the counterparty. Computed in Go (Swap.RedeemHeldForShortFunding)
   *  so the missing button and the engine's refusal are one rule. */
  redeemHeldForShortFunding?: boolean
  /** Nothing about the counterparty's Bitcoin funding stands in the way of
   *  this side creating its Zenon HTLC. False only for the participant in a
   *  Bitcoin-initiated swap while that funding is missing, short, unconfirmed
   *  or spent -- and then fundingCommitBlocker says which. Computed in Go
   *  (Swap.FundingCommitBlocker) so the card's gate and the engine's refusal
   *  are one rule. */
  fundingCommitted: boolean
  fundingCommitBlocker?: string
  /** Terms of the Zenon leg this swap never recorded, so no HTLC can verify
   *  against it: which field, and why. The card's repair form is built from
   *  this rather than from its own reading of the fields, so it and Go cannot
   *  disagree about what counts as missing (a zero amount does). */
  missingZenonTerms?: MissingZenonTerm[]

  funding?: FundingOutput
  refundTx?: SpendResult
  redeemTx?: SpendResult
  fundingBroadcast?: FundingBroadcast

  zenon: ZenonLeg
  events: SwapEvent[]
}

export interface Config {
  network: string
  backend: string
  znn: boolean
  /**
   * Which instance the signing module was built as, 'dev' or 'prod'. Reported
   * by the module rather than read from the page, so the header can check that
   * the two halves of this build agree. See core/env.ts and wasm/env.go.
   */
  buildEnv: string
  /** Where swaps are kept — browser storage, named so the status line can say so. */
  swapDir: string
  secretSize: number
  /** Whether each side is on a URL this browser chose or on the built-in default. */
  usingOwnBtc: boolean
  usingOwnZnn: boolean
  activeSwaps?: number
  historySwaps?: number
  tipHeight?: number
  chainError?: string
  znnHeight?: number
  znnError?: string
}

/** This browser's network and node choices, sent with every chain call. */
export interface Settings {
  network: string
  btcEsplora?: string
  znnUrl?: string
  /**
   * Nostr relays for the session feature, whitespace- or comma-separated. Not
   * sent to the module: sessions are sealed before they reach a relay. It lives
   * beside the node URLs because it is the same kind of choice -- whose
   * infrastructure this browser talks to.
   */
  relays?: string
}

/** The public half of a swap, as it travels in a `swapoffer1:` string.
 *  Mirrors the Offer struct in wasm/swap.go — which deliberately has no field
 *  for a secret or a key, so there is no path that puts one in a chat window. */
export interface OfferBody {
  v: number
  network: string
  /** The sender's role; the receiver takes the other one. */
  fromRole: Role
  secretHash: string
  /** The sender's Bitcoin pubkey hash. */
  pkh: string
  /** The sender's direction, so the receiver takes the opposite. */
  btcLeg: Leg
  amountSats: number
  lockTime?: number
  /** The sender's Zenon address — the receiver's counterparty address. */
  zenonAddr?: string
  zenonToken?: string
  zenonAmt?: string
  note?: string
}

export interface DecodedOffer {
  decoded: OfferBody
  /** Which side the receiver should take, worked out in Go so no UI has to. */
  yourLeg: Leg
  yourRole: Role
}

/** What the recovery download contains. Everything needed to spend the contract
 *  without this app — which is the point of it. */
export interface RecoveryFile {
  swapId: string
  network: string
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
 * What it costs to get back out of a contract — the arithmetic that decides
 * whether an agreed amount can actually be unlocked. Mirrors SpendCost in
 * wasm/estimate.go, where it is computed with the signer's own transaction
 * sizing rather than a second opinion about it.
 */
export interface SpendCost {
  amountSats: number
  dustLimit: number

  feeRate: number
  /** 'you' | 'node' | 'fallback' — whether this reflects the live fee market. */
  feeRateFrom: string

  /** Sized against a placeholder output, because no address was supplied. */
  destAssumed: boolean
  /** Sized against the contract template, because none was built yet. */
  contractAssumed: boolean

  redeem: BranchCost
  refund: BranchCost

  /** The smallest amount that can be unlocked at any relayable rate. */
  minRelayable: number
  /** The smallest amount that clears the dust limit at `feeRate`. */
  minAtFeeRate: number
  /** …with room for fees to move while the contract is locked. */
  recommended: number
  headroomRate: number

  /** The highest sat/vB the redeem can pay and still pay out. 0 = none works. */
  maxFeeRate: number

  verdict: 'ok' | 'tight' | 'unspendable'
  summary: string
}

/**
 * One party's claim about which chains they are on. Mirrors wasm/chainid.go.
 *
 * The anchor hash is the load-bearing field. A network name settles nothing —
 * everybody's regtest is their own, and two custom signets share a name — so
 * the real check is the hash of a block both sides should already have.
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

/** What came of comparing two fingerprints. Mirrors ChainAgreement in Go. */
export interface ChainAgreement {
  /** True only when nothing disagreed AND nothing went unchecked. */
  ok: boolean
  /** True only when a block hash was actually compared and matched. */
  sameChain: boolean
  /** Mismatches. A swap with any of these cannot complete. */
  problems?: string[]
  /** Checks that could not run — unknown, which is not the same as agreed. */
  unchecked?: string[]
}

/**
 * A session room, as one code derives it. Mirrors wasm/session.go. `code` is the
 * shared secret in its normalised form and is what every later call passes back;
 * `display` is the same value grouped for reading aloud. Neither ever leaves the
 * two browsers -- the relay only sees `pubKey`, which the code derives, and
 * ciphertext.
 */
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
 * There is deliberately no field for the preimage. Revealing it before redeeming
 * hands the counterparty both legs, so it has no field here, no code path that
 * would send one, and nothing that would accept one.
 *
 * `resync` and `moved` carry nothing at all, and both are requests rather than
 * values: `resync` asks the other side to publish everything about this swap
 * again, and `moved` says only that a chain was touched -- which is what lets it
 * be sent after an unlock, the one action whose result must never travel. The
 * receiver goes and reads the chain; the announcement only makes them look
 * sooner.
 */
export interface SessionMessage {
  type:
    | 'hello'
    | 'ping'
    | 'bye'
    | 'offer'
    /** Where the sender wants each half paid, sent before any swap exists. What
     *  a board accept sends, so the taker learns the author's CURRENT addresses
     *  rather than the ones their post was published with. */
    | 'addresses'
    | 'pkh'
    | 'contract'
    | 'zenon'
    | 'funded'
    | 'note'
    | 'resync'
    | 'moved'
  from?: Role
  /** Random per session. Tells their message from our own coming back. */
  peerId?: string
  /** Carried on a hello, so each side can prove they share both chains. */
  chain?: ChainFingerprint
  offer?: string
  pkhHex?: string
  contractHex?: string
  htlcId?: string
  /** On a `zenon` message, where the sender's HTLC pays. On an `addresses`
   *  message, simply where they want their Zenon paid. */
  zenonAddr?: string
  /** Where the sender wants their Bitcoin. Only on `addresses` — everywhere else
   *  the Bitcoin side is a pubkey hash, which is what a contract commits to. */
  btcAddr?: string
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
  /** Decided by the contract and the key, not by what was asked for. */
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

  /** When a refund becomes relayable; absent for a redeem. */
  validFrom?: string
  /** Set while that moment is still ahead, with the wait left. */
  notYet?: string
  presignedRefundHex?: string
}

/**
 * The three things a wallet can be asked to do on this swap's Zenon leg.
 *
 * They are named rather than inferred from the swap because each is refused
 * from the wrong side, and a name in the request is what lets Go say WHICH
 * wrong thing was asked for.
 */
export type WalletAction = 'create' | 'unlock' | 'reclaim'

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
 * with the same account selected.
 *
 * `ok` is the gate: false blocks the send, and there is no override. False for a
 * proven mismatch AND for a check that could not run -- an unreachable wallet
 * node is exactly what a wallet pointed at somebody else's chain looks like from
 * here.
 */
export interface WalletSync {
  ok: boolean

  address: string
  nodeUrl?: string
  /** Both sides named the same endpoint. Reported; never relied on. */
  sameNodeUrl: boolean

  chainIdentifier: number
  walletChainIdentifier?: number
  /** What the extension claimed, which can disagree with its own node. */
  reportedChainId?: number

  /**
   * True only when a momentum hash was read from both nodes and matched. This
   * is the one positive result: equal chain identifiers prove nothing, because
   * every go-zenon devnet is chain 69.
   */
  sameChain: boolean
  anchorHeight?: number
  anchorHash?: string

  height?: number
  walletHeight?: number

  /** Proven mismatches. */
  problems?: string[]
  /** Checks that could not run. These block too. */
  unchecked?: string[]
  /** True, worth saying, not disqualifying. */
  warnings?: string[]
}

/**
 * One proposed HTLC call: the block, and what it says in words. The extension
 * shows the raw block before signing, which is the right thing for it to show
 * and unreadable on its own; `summary` is what the page puts in front of it,
 * decoded by the code that built the bytes rather than by a second decoder.
 */
export interface WalletBlockPlan {
  action: WalletAction
  block: WalletAccountBlock
  summary: string
  /** True and worth knowing, but not a refusal — those come back as errors. */
  warnings?: string[]

  /** The account this plan was built for, to compare against the wallet's. */
  signer: string
  /** The verdict that allowed this plan to be built at all. */
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

/** How an address was proven to belong to a board key. Mirrors the constants in
 *  wasm/board.go, which are what the module actually checks against. */
export type ProofScheme = 'btc-ecdsa' | 'znn-ed25519'

/** One address, proven to belong to whoever holds a board key. */
export interface WalletProof {
  scheme: ProofScheme
  address: string
  /** Only for `znn-ed25519`: an ed25519 signature does not carry its own key,
   *  and a Bitcoin compact signature does. */
  pubKey?: string
  /** base64 for Bitcoin — what every wallet returns — and hex for Zenon. */
  sig: string
  /** When THIS browser checked it. Cleared before publishing: a reader
   *  re-verifies rather than believing a stranger's timestamp. */
  verifiedAt?: string
}

export type PostStatus = 'open' | 'taken' | 'done' | 'void'

/** An offer as it appears on the board. Deliberately not an OfferBody: that one
 *  pins the terms of a swap that already exists, and this is the advertisement
 *  before there is one. */
export interface BoardPost {
  v: number
  /** The `d` tag — the addressable slot, and so the identity of the offer
   *  rather than of one version of it. */
  id: string
  network: string
  /** What the AUTHOR does with Bitcoin. A taker takes the opposite. */
  side: Leg
  role: Role
  amountSats: number
  /** A size band the author will trade within. Absent means the amount is the
   *  amount. */
  minSats?: number
  maxSats?: number
  zenonAmt: string
  zenonToken?: string
  lockHours?: number
  btcAddr?: string
  znnAddr?: string
  proofs?: WalletProof[]
  status: PostStatus
  note?: string
  createdAt: number
  expiresAt: number
  /** Self-reported, unverifiable, and shown labelled as the claim it is. */
  completed?: number
}

/** A post as a reader gets it: the terms, who wrote them, and what survived
 *  checking. Anything in `problems` is worth reading — a post carrying a broken
 *  proof of somebody else's address is the most interesting thing on a board. */
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

/** This browser's board key, and what it has been bound to. The private half
 *  never crosses the boundary — same rule as a swap's KeyView. */
export interface BoardIdentity {
  pubKey: string
  bindings: WalletProof[]
  createdAt: string
  /** Which kinds and tag to subscribe on, from the module rather than from a
   *  second copy of the numbers here. A filter built from a stale constant is a
   *  board that silently shows nothing.
   *
   *  `tag` differs by instance — the development build posts to a board of its
   *  own, so a dev page pointed at mainnet for a moment cannot land test offers
   *  among real ones. See boardTag() in wasm/board.go. */
  kinds: {post: number; take: number; delete: number; presence: number; tag: string}
  /** How long a post may run, and how long one runs by default, in seconds.
   *  Reported rather than mirrored because the default differs by instance:
   *  fifteen minutes on dev, a day on prod. */
  limits: {defaultTtl: number; minTtl: number; maxTtl: number}
  /** How often to publish a presence beat, and how long one means "online",
   *  in seconds. From the module because ReadPresence applies the same window —
   *  a second copy here would be two halves disagreeing about who is online. */
  presence: {beatSeconds: number; staleSeconds: number}
}

/**
 * A verified presence beat: whose it was, and when they were last at a keyboard.
 *
 * Not a boolean, deliberately. Whether a key is online is a function of the
 * reader's clock and goes stale every second with nothing arriving, so the
 * module settles only the half that cannot be recomputed — that the beat is
 * genuine, is for this network, and is not stamped in the future — and the page
 * re-judges the rest on its own timer. Same division as `Listing.expired`.
 */
export interface Seen {
  /** The x-only board key that signed it. */
  author: string
  /** The author's own clock, in seconds. Checked for skew, not adjusted. */
  seenAt: number
  /** How many seconds `seenAt` still means "online" for. */
  staleAfter: number
}

/** One of this browser's own posts, as last published. */
export interface MyPost {
  post: BoardPost
  /** The swap this offer became. It is what lets a settled swap withdraw the
   *  post it came from. */
  swapId?: string
  /** The session code accepted for it, so a reload mid-trade can rejoin. */
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
  amountSats?: number
  /** Where the TAKER wants each half paid. Sealed with the rest of the take, so
   *  a taker answering one stranger tells only that stranger where their money
   *  goes — a post carries its author's addresses in the clear, but publishing a
   *  take in the open would put both sides' on a public relay. Absent on takes
   *  from builds that predate carrying them. */
  btcAddr?: string
  znnAddr?: string
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

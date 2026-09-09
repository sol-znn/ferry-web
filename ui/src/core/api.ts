import type {
  BoardIdentity,
  ChainAgreement,
  ChainFingerprint,
  Config,
  DecodedOffer,
  InboundTake,
  Listing,
  MyPost,
  ProofScheme,
  RebuildRequest,
  RebuildResult,
  RecoveryFile,
  Seen,
  SessionMessage,
  SessionRoom,
  Settings,
  SpendCost,
  Swap,
  WalletAction,
  WalletAccountBlock,
  WalletBlockPlan,
  WalletSync,
} from '@/types'
import type {NostrEvent} from './nostr'
import {wasmCall} from './wasm'

/**
 * The operations this app can perform: one method per entry in the Go call
 * table, each a call into the WebAssembly module running in this page rather
 * than a request to anything.
 *
 * There is no API token because there is no endpoint to protect, and no
 * per-request node override because there is no operator to override -- the same
 * fields are simply the user's settings, sent on every call that talks to a
 * chain.
 */
export const api = {
  config: (settings: Settings) => wasmCall<Config>('config', {settings}),

  list: (view: 'active' | 'history') => wasmCall<Swap[]>('list', {view}),

  create: (body: Record<string, unknown>, settings: Settings) =>
    wasmCall<Swap>('create', {...body, settings}),

  counterparty: (id: string, pkhHex: string) => wasmCall<Swap>('counterparty', {id, pkhHex}),

  audit: (id: string, contractHex: string) => wasmCall<Swap>('audit', {id, contractHex}),

  /**
   * Record that a payment to the contract has been broadcast.
   *
   * Reaches no node -- the whole point is that the chain has not heard about it
   * yet. It is what lets the card stop offering to send a second payment during
   * the gap between a wallet accepting a send and a node listing the output, and
   * it is stored on the swap so reloading does not reopen that gap.
   */
  fundingSent: (id: string, txid: string) => wasmCall<Swap>('fundingSent', {id, txid}),

  refresh: (id: string, settings: Settings) => wasmCall<Swap>('refresh', {id, settings}),

  redeem: (id: string, destAddr: string, settings: Settings) =>
    wasmCall<Swap>('redeem', {id, destAddr, settings}),

  refund: (id: string, destAddr: string, settings: Settings) =>
    wasmCall<Swap>('refund', {id, destAddr, settings}),

  verifyZenon: (id: string, htlcId: string, settings: Settings) =>
    wasmCall<{swap: Swap; error?: string}>('zenon', {id, htlcId, settings}),

  /**
   * Find this swap's Zenon HTLC without being told its id.
   *
   * The id is the hash of the transaction that created the entry, so it cannot be
   * computed -- but the creating address is known, every create is a send to the
   * HTLC contract, and a create's arguments carry the hashlock verbatim. Every
   * candidate goes through the same verification a hand-typed id gets, so this
   * saves the copy-paste and not one check.
   */
  findZenon: (id: string, settings: Settings) =>
    wasmCall<{swap: Swap; error?: string}>('zenonFind', {id, settings}),

  /**
   * Build the account block that performs one Zenon HTLC action, for a browser
   * wallet to sign.
   *
   * Nothing is signed or sent here: this returns bytes and a sentence describing
   * them. The signing account is passed IN rather than chosen, because the
   * extension signs with whichever account it has selected and cannot be told
   * otherwise -- so Go checks that account against the swap. For a create that
   * check is the difference between an HTLC this user can reclaim and one they
   * cannot.
   */
  /**
   * Whether the wallet and this page are on the same chain, with the right
   * account selected, before any block exists.
   *
   * Chain identifiers are compared, and then -- because equal identifiers prove
   * nothing, every go-zenon devnet being chain 69 -- the wallet's own node is
   * read and a momentum hash compared against this browser's. A check that could
   * not run counts as a failure, not as a pass.
   */
  walletSync: (
    wallet: {address: string; chainId?: number; nodeUrl?: string},
    settings: Settings,
    swap?: {id: string; action: WalletAction},
  ) => wasmCall<WalletSync>('walletSync', {...wallet, ...(swap ?? {}), settings}),

  walletBlock: (
    id: string,
    action: WalletAction,
    wallet: {address: string; chainId?: number; nodeUrl?: string},
    settings: Settings,
  ) =>
    wasmCall<WalletBlockPlan>('walletBlock', {
      id,
      action,
      from: wallet.address,
      chainId: wallet.chainId,
      nodeUrl: wallet.nodeUrl,
      settings,
    }),

  /**
   * Record what the wallet published, and check it.
   *
   * A create is fed straight back through the ordinary verification path: the
   * hash the wallet reports is the HTLC id, and an unchecked id is exactly as
   * untrustworthy whether it came from a counterparty or from one's own wallet.
   * It will usually not be on the chain yet, which comes back as pending rather
   * than as a failure.
   */
  walletSent: (
    id: string,
    action: WalletAction,
    hash: string,
    from: string,
    settings: Settings,
    /**
     * The block as proposed and the block as published. Go diffs them, which
     * is the only way to catch a wallet that signed with a different account
     * than the one it reported.
     */
    blocks?: {proposed: WalletAccountBlock; signed?: Record<string, unknown>},
  ) =>
    wasmCall<{swap: Swap; pending?: boolean; error?: string}>('walletSent', {
      id,
      action,
      hash,
      from,
      settings,
      ...(blocks ?? {}),
    }),

  setSecret: (id: string, secretHex: string) => wasmCall<Swap>('secret', {id, secretHex}),

  archive: (id: string, archived: boolean) => wasmCall<Swap>('archive', {id, archived}),

  /**
   * Remove a swap from this browser for good.
   *
   * Archive files a swap away; this destroys it, along with the only key that can
   * spend its contract, and there is no undo. Go refuses outright for a swap that
   * may still have money behind it, and `force` is the caller saying so a second
   * time after being told what is at stake. See handleDelete in wasm/api.go.
   */
  remove: (id: string, force = false) => wasmCall<{deleted: string}>('delete', {id, force}),

  offer: (id: string) => wasmCall<{offer: string}>('offer', {id}),

  decodeOffer: (offer: string) => wasmCall<DecodedOffer>('decodeOffer', {offer}),

  recovery: (id: string) => wasmCall<RecoveryFile>('recovery', {id}),

  /**
   * What unlocking a contract holding `amountSats` will cost the recipient. The
   * one call here that does not need a chain: pass a `feeRate` and it never
   * leaves the page, which is what lets the offline Recover page use it.
   */
  estimate: (
    req: {
      amountSats?: number
      destAddr?: string
      contractHex?: string
      /** Name a swap and it prices the contract that was really built. */
      id?: string
      feeRate?: number
    },
    settings?: Settings,
  ) => wasmCall<SpendCost>('estimate', settings ? {...req, settings} : req),

  /**
   * Mint a session code, or re-derive the room an existing one names. The same
   * call serves both sides, so there is no separate host path and no way for two
   * browsers to derive the room differently from the same code.
   */
  sessionNew: (code?: string) => wasmCall<SessionRoom>('sessionNew', code ? {code} : {}),

  /** Seal one message into a signed event ready to hand to a relay. */
  sessionSend: (code: string, message: SessionMessage, id?: string) =>
    wasmCall<{event: NostrEvent}>('sessionSend', id ? {code, message, id} : {code, message}),

  /** Verify an event came from this room, and decrypt it. */
  sessionOpen: (code: string, event: NostrEvent) =>
    wasmCall<{message: SessionMessage; eventId: string}>('sessionOpen', {code, event}),

  /**
   * Which chains this browser is on, and -- given a peer's answer -- whether
   * they are the same ones. The peer's numbers are never taken as fact: their
   * anchor HEIGHT is used to ask this browser's own node what hash it has there,
   * and it is that answer which is compared.
   */
  chainId: (settings: Settings, peer?: ChainFingerprint) =>
    wasmCall<{mine: ChainFingerprint; agreement?: ChainAgreement}>(
      'chainId',
      peer ? {settings, peer} : {settings},
    ),

  /**
   * The board.
   *
   * Every call here deals in events rather than swaps. The module signs what
   * this browser publishes and verifies what strangers publish; moving the
   * events is this app's job, exactly as it is for a session.
   */

  /** This browser's board key, minted on the first call. */
  boardIdentity: () => wasmCall<{identity: BoardIdentity}>('boardIdentity', {}),

  /**
   * The exact text a wallet has to sign to bind an address to the board key.
   *
   * A call rather than a string assembled here, because the statement is what
   * the signature commits to: a page that built one while Go verified another
   * would fail every proof, invisibly.
   */
  boardStatement: (address: string) => wasmCall<{statement: string}>('boardStatement', {address}),

  /** Verify a wallet signature and record the binding. Refuses rather than
   *  warns: a badge that appeared beside an unproven address would be doing the
   *  opposite of its job. */
  boardBind: (
    proof: {scheme: ProofScheme; address: string; pubKey?: string; sig: string},
    settings: Settings,
  ) => wasmCall<{identity: BoardIdentity}>('boardBind', {...proof, settings}),

  boardUnbind: (scheme: ProofScheme) =>
    wasmCall<{identity: BoardIdentity}>('boardUnbind', {scheme}),

  /**
   * Sign a post. Passing an `id` edits that post rather than making a second
   * one — a relay keys an addressable event by its `d` tag, so republishing
   * under the same id IS the edit.
   */
  boardPublish: (post: Record<string, unknown>, settings: Settings) =>
    wasmCall<{event: NostrEvent; post: MyPost}>('boardPublish', {...post, settings}),

  /**
   * Take a post down. Two events come back and both should be published: a void
   * replacement, which every relay honours because it is nothing more exotic
   * than an edit, and a NIP-09 deletion request for the ones that implement it.
   */
  boardWithdraw: (id: string, done = false) =>
    wasmCall<{events: NostrEvent[]; post: MyPost}>('boardWithdraw', {id, done}),

  boardMine: () => wasmCall<{posts: MyPost[]}>('boardMine', {}),

  /** Forget a post locally. Does not reach a relay — withdraw first. */
  boardForget: (id: string) => wasmCall<{forgot: string}>('boardForget', {id}),

  /** Record which swap and session a post turned into. This is the join that
   *  lets a settled swap withdraw the offer it came from. */
  boardLink: (id: string, link: {swapId?: string; code?: string; taker?: string}) =>
    wasmCall<{post: MyPost}>('boardLink', {id, ...link}),

  /** Verify one event off a relay and return what it says. Run on our own posts
   *  coming back too: a post that fails here would fail for everyone. */
  boardRead: (event: NostrEvent, settings: Settings) =>
    wasmCall<{listing: Listing}>('boardRead', {event, settings}),

  /**
   * Seal a session code to a post's author.
   *
   * The code is minted inside this call, so there is no moment where the page
   * holds one it might publish somewhere else. It comes back because the taker
   * has to join that room themselves.
   */
  boardTake: (req: {
    author: string
    postId: string
    amountSats?: number
    /** Where this taker wants each half paid. Both required — Go refuses a take
     *  that would reach an author with nowhere to pay. */
    btcAddr: string
    znnAddr: string
    note?: string
    code?: string
  }) => wasmCall<{event: NostrEvent; room: SessionRoom}>('boardTake', req),

  /** Open a take addressed to this browser, and derive the room it names. */
  boardReadTake: (event: NostrEvent) =>
    wasmCall<{take: InboundTake; room: SessionRoom}>('boardReadTake', {event}),

  /**
   * Sign one presence beat: "the key behind my posts is at a keyboard".
   *
   * Signing only. Whether there is anything worth being present for is the
   * page's question, and the page runs the timer — see the heartbeat in
   * useBoard for why the module does not start broadcasting on its own.
   */
  boardPresence: (settings: Settings) => wasmCall<{event: NostrEvent}>('boardPresence', {settings}),

  /** Verify one beat off a relay. Refuses a forged one, one for another
   *  network, and one stamped in the future — see ReadPresence. */
  boardReadPresence: (event: NostrEvent, settings: Settings) =>
    wasmCall<{seen: Seen}>('boardReadPresence', {event, settings}),

  /** `ferry recover`, offline: rebuild a spend from a saved recovery file. */
  rebuild: (req: RebuildRequest) => wasmCall<RebuildResult>('rebuild', req),

  /** Every swap in this browser, as one document to carry elsewhere. */
  exportAll: () => wasmCall<{swaps: Swap[]}>('export'),

  importAll: (data: string) =>
    wasmCall<{added: number; skipped: number; rejected: number}>('import', {data}),
}

/**
 * Hand the browser a file to save. With no server there is no
 * Content-Disposition header, so the download is assembled here from a blob.
 */
export function download(filename: string, contents: string, type = 'application/json') {
  const blob = new Blob([contents], {type})
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  // Revoked on the next tick rather than immediately: Safari has historically
  // cancelled an in-flight download whose object URL was released too early.
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

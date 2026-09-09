import {computed, ref, shallowRef, watch} from 'vue'
import {api} from '@/core/api'
import {RelayPool, type NostrEvent, type RelayStatus} from '@/core/nostr'
import {owed, type HandoffType} from '@/core/handoffs'
import {useFerry} from '@/core/composables/useFerry'
import {syncNow} from '@/core/composables/useAutoRefresh'
import {useSettings} from '@/core/composables/useSettings'
import type {ChainAgreement, Leg, Role, SessionMessage, SessionRoom} from '@/types'

/**
 * A live session between two browsers.
 *
 * One side makes a code, the other types it in, and the four hand-offs a swap
 * needs -- a pubkey hash, a contract, an HTLC id, a funding txid -- travel
 * between them instead of through a chat window.
 *
 * They travel on their own. Each is knowable by exactly one browser until it is
 * sent, so a session where somebody has to remember to press Send is one that
 * stalls on a value both sides could already have had. `publishOwed` watches the
 * attached swap and publishes whatever it has come to hold, once per value. The
 * Send buttons stay, because a relay can drop a message -- but nothing depends
 * on anybody pressing one.
 *
 * The preimage never travels this way, and that is not an omission: it has no
 * field on a SessionMessage and no code path that would send one. It moves by
 * being spent, and the other side reads it off the chain that enforced it. See
 * core/handoffs.ts.
 *
 * What makes that safe is that nothing here trusts a message. Each is applied
 * through the same call the paste box used, and those verify against what was
 * already agreed -- a contract through `audit`, a pubkey hash through
 * `counterparty`, an HTLC id through `zenon`. A session removes the typing, not
 * the checking, and a refusal leaves the swap exactly as it was.
 *
 * State is module-level because there is one session at a time and several places
 * care about it; per-component state would let two of them disagree about
 * whether a session is open.
 */

/** One line of what happened, for the panel. */
export interface SessionLine {
  id: string
  at: number
  /** 'in' from the counterparty, 'out' sent by us, 'sys' from this app. */
  dir: 'in' | 'out' | 'sys'
  text: string
  ok?: boolean
}

// The one shared swap list. A session's outgoing half is entirely a function of
// what is in it, so this module reads it rather than waiting to be told.
const {swaps} = useFerry()

const room = ref<SessionRoom | null>(null)
const lines = ref<SessionLine[]>([])
const error = ref('')
const busy = ref(false)
const relays = ref<{url: string; status: RelayStatus}[]>([])
// shallowRef: the pool holds sockets and a Set, none of which Vue should walk
// to make reactive.
const pool = shallowRef<RelayPool | null>(null)

/** The swap this session is attached to, once there is one. */
const swapId = ref('')
/** Which side this browser is, so its own messages can be ignored. */
const ourRole = ref<Role | ''>('')
/** An offer that arrived and is waiting to be decoded into the create form. */
const pendingOffer = ref('')

/**
 * A trade agreed on the board, waiting for the swaps page to be built from it.
 *
 * The board and the create form are two pages, and what passes between them is
 * a handful of terms two people have just settled on: which way round, how much
 * of each, and for how long. This carries them, and the swaps page fills its
 * form in from it on arrival.
 *
 * It lives beside `pendingOffer` because it is the same kind of thing and lands
 * in the same place -- and because the two are sequential rather than
 * alternatives. Taking an offer sets this, so the form shows the right trade the
 * moment the page opens; the counterparty's real `swapoffer1` arrives over the
 * session a moment later, sets `pendingOffer`, and OVERWRITES those terms with
 * the ones Go will hold the create to. The board's copy is a head start, never
 * an authority: nothing here has been signed by anybody, and `Offer.CheckCreate`
 * is what a swap is actually built against.
 */
export interface PendingTrade {
  /** The side THIS browser takes, already flipped where it needed flipping. */
  leg: Leg
  role: Role
  amountSats: number
  /** Scaled to `amountSats` when a size band meant the two disagree. */
  zenonAmount: string
  zenonToken: string
  /** Blank leaves the duration to Go, which derives it from the role. */
  lockHours: number
  /** Theirs, when the post advertised one. */
  zenonPeerAddress: string
  /** Which post this came from, and whose, for the banner on the form. */
  postId: string
  peer: string
}

const pendingTrade = ref<PendingTrade | null>(null)

/**
 * Where the other side wants each half paid, as last stated over this session.
 *
 * Separate from `pendingTrade` because it arrives by a different route and at a
 * different time: a trade is agreed on the board and travels with the user, an
 * address arrives from the counterparty whenever they say it. Kept rather than
 * consumed, so a form opened after the message landed still finds it.
 *
 * A head start and never an authority, same as everything else that fills this
 * form in without a signature behind it. `swapoffer1` is what pins a term.
 */
const peerAddresses = ref<{btc: string; znn: string} | null>(null)

/**
 * What has already gone over this session: `swapId:type` to the value that went.
 *
 * Keyed by the value rather than by a flag, because these are not one-shot
 * events: an HTLC id can be replaced after a failed create, and a contract
 * rebuilt against a corrected pubkey hash. Remembering what was sent, rather
 * than that something was, is what tells those apart from the dozen times a swap
 * is reloaded without changing.
 *
 * Reactive so a card can say "already sent" beside its own Send button, and
 * cleared with the room: a new session is a new conversation.
 */
const published = ref<Record<string, string>>({})
/** Bumped whenever an applied message changed a swap, so lists can reload. */
const changed = ref(0)

/**
 * Who else is in the room. A relay has no notion of membership, so presence is
 * built out of the only signal available: somebody said something. Each browser
 * announces itself with a `hello` on joining and answers a stranger's hello
 * once, which converges without a broadcast storm.
 */
const peers = ref<string[]>([])

/**
 * A relay stores events; it has no notion of a closed connection. Without
 * something saying so, the only way to learn a counterparty is gone is that they
 * stop saying anything at all -- which is silent forever, not "gone".
 *
 * So every browser sends a `ping` every {@link KEEPALIVE_MS}, making this map's
 * age comparable to an interval rather than to however long two people happen to
 * go without a real message; and `leave()` publishes a `bye` first, so a
 * deliberate departure is announced rather than inferred a keepalive late.
 */
const KEEPALIVE_MS = 15_000
/** Just over two missed keepalives, so one lost or delayed ping is not a false
 *  departure. */
const PEER_TIMEOUT_MS = 2.5 * KEEPALIVE_MS

/** When each peer was last heard from — any message counts, not only pings. */
const lastSeen = new Map<string, number>()
let keepaliveTimer: ReturnType<typeof setInterval> | null = null

/**
 * This browser's id within a session.
 *
 * Its job is to tell a message we sent from the same message arriving back off
 * the relays we published it to -- which the role field cannot do, because a
 * session usually starts before either side has a swap and therefore a role.
 *
 * Stable per browser rather than regenerated per join, and that is not a detail:
 * relays STORE what they are given and replay it to whoever subscribes next, so
 * a browser that reloads and rejoins is served its own earlier greetings. With a
 * fresh id it would meet its previous incarnation, count it as a second
 * participant, and re-run every check against it.
 *
 * Random, scoped to this app's storage, and only ever published inside a session
 * encrypted to a code -- so it identifies a browser to the one person already
 * holding the shared secret, and to nobody else.
 */
const PEER_ID_KEY = 'ferry.session.peer'

function loadPeerId(): string {
  try {
    const saved = localStorage.getItem(PEER_ID_KEY)
    if (saved) return saved
    const fresh = crypto.randomUUID().slice(0, 8)
    localStorage.setItem(PEER_ID_KEY, fresh)
    return fresh
  } catch {
    // Storage refused. A per-session id still works; it costs only the
    // self-replay filtering described above.
    return crypto.randomUUID().slice(0, 8)
  }
}

const myPeerId = ref(loadPeerId())

/** Whether the counterparty's chains were checked, and what came of it. */
const peerChain = ref<ChainAgreement | null>(null)

/** Peers already greeted back, so a hello cannot ping-pong forever. */
const greeted = new Set<string>()

/**
 * Peers whose chains have been checked, or are being checked right now. Claimed
 * synchronously, before the await: each side sends two hellos and they arrive
 * close enough together that both handlers would pass a guard reading state the
 * first one sets after its network round trip.
 */
const chainChecked = new Set<string>()

const peerPresent = computed(() => peers.value.length > 0)

/** Remove all trace of a peer known to be gone, whether they said so or timed
 *  out — the next hello from the same browser should be treated as new. */
function forgetPeer(id: string) {
  peers.value = peers.value.filter((p) => p !== id)
  lastSeen.delete(id)
  greeted.delete(id)
  chainChecked.delete(id)
}

/** Peers who have gone quiet for longer than a couple of missed keepalives. */
function dropStalePeers() {
  const cutoff = Date.now() - PEER_TIMEOUT_MS
  for (const id of peers.value) {
    const seen = lastSeen.get(id)
    if (seen === undefined || seen < cutoff) {
      forgetPeer(id)
      say('in', 'They appear to have left the session — no keepalive received.', false)
    }
  }
}

const active = computed(() => room.value !== null)
const connected = computed(() => relays.value.filter((r) => r.status === 'open').length)

function say(dir: SessionLine['dir'], text: string, ok?: boolean) {
  lines.value.push({id: `${Date.now()}-${lines.value.length}`, at: Date.now(), dir, text, ok})
}

function short(v: string | undefined): string {
  if (!v) return '(empty)'
  return v.length > 20 ? `${v.slice(0, 10)}…${v.slice(-6)}` : v
}

/**
 * Start or join a room. Both sides call this; the only difference is whether a
 * code was supplied. The module mints one when it is not, so there is no
 * separate host path and no way for the two sides to derive the room
 * differently.
 */
async function join(code?: string, attachTo?: string) {
  error.value = ''
  busy.value = true
  try {
    leave()
    const r = await api.sessionNew(code)
    room.value = r
    swapId.value = attachTo ?? ''
    lines.value = []
    peers.value = []
    peerChain.value = null
    published.value = {}
    greeted.clear()
    chainChecked.clear()
    say('sys', code ? `Joined session ${r.display}.` : `Session ${r.display} is open.`)

    const {relayList} = useSettings()
    const p = new RelayPool(relayList())
    pool.value = p
    p.open(
      // No `since`: a party who joins late must receive what was already said,
      // and a room only ever carries one conversation.
      [{kinds: [r.kind], authors: [r.pubKey], '#d': [r.roomId], limit: 100}],
      (ev) => void receive(ev),
      () => (relays.value = p.status),
    )
    relays.value = p.status
    // Announced after subscribing, never before: a hello published while we
    // are not yet listening is one whose reply we would miss.
    void announce()
    keepaliveTimer = setInterval(() => {
      void send({type: 'ping', peerId: myPeerId.value}, '', {quiet: true})
      dropStalePeers()
    }, KEEPALIVE_MS)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    room.value = null
  } finally {
    busy.value = false
  }
}

/**
 * Say we are here, and say which chains we are on. The fingerprint rides along
 * with the greeting because the two questions are asked at the same moment and
 * answered by the same round trip: is anybody there, and are they even on my
 * chain. A session where the second answer is no should stop before anybody
 * funds anything.
 */
async function announce() {
  const {body: settings} = useSettings()
  let chain
  try {
    chain = (await api.chainId(settings.value)).mine
  } catch {
    // A browser with no reachable node can still run a session; it simply
    // cannot prove which chain it is on, and the other side is told that.
  }
  await send({type: 'hello', peerId: myPeerId.value, chain}, '', {quiet: true})
}

/**
 * Best-effort goodbye, sealed against the room and pool captured at the moment
 * of leaving rather than the shared refs -- by the time the seal resolves,
 * `leave()` has cleared those and a `join()` may have replaced them.
 */
async function sendBye(targetRoom: SessionRoom, targetPool: RelayPool) {
  try {
    const {event} = await api.sessionSend(targetRoom.code, {type: 'bye', peerId: myPeerId.value})
    targetPool.publish(event)
  } catch {
    // Nothing to do — the peer-timeout in dropStalePeers is what covers this.
  }
}

function leave() {
  if (keepaliveTimer) {
    clearInterval(keepaliveTimer)
    keepaliveTimer = null
  }
  const r = room.value
  const p = pool.value
  room.value = null
  pool.value = null
  relays.value = []
  peers.value = []
  published.value = {}
  lastSeen.clear()
  greeted.clear()
  chainChecked.clear()
  if (r && p) {
    // The pool stays open just long enough to flush the bye — closing it
    // synchronously here would drop a message that has not been sent yet.
    void sendBye(r, p).finally(() => p.close())
  } else {
    p?.close()
  }
}

/** Attach the session to a swap, so incoming values have somewhere to go. */
function attach(id: string, role: Role) {
  ourRole.value = role
  if (swapId.value === id) return
  swapId.value = id
  say('sys', `Session attached to swap ${id}.`)
}

/**
 * Seal and publish one message.
 *
 * `quiet` is for the bookkeeping traffic, which would otherwise fill the
 * transcript with lines nobody asked for. A quiet message still reports a
 * FAILURE: a greeting that could not be published is the difference between
 * "they have not arrived" and "they cannot hear you".
 *
 * Returns whether it went, which is what lets publishOwed forget a hand-off it
 * failed to publish rather than record it as delivered.
 */
async function send(
  message: SessionMessage,
  describe: string,
  opts: {quiet?: boolean} = {},
): Promise<boolean> {
  if (!room.value || !pool.value) return false
  error.value = ''
  try {
    // Stamped on everything, not just hellos: a relay hands back what we
    // published to it, and this is what lets the receiving end tell that
    // message apart from the counterparty's.
    const stamped = {...message, peerId: myPeerId.value}
    const {event} = await api.sessionSend(room.value.code, stamped, swapId.value || undefined)
    pool.value.publish(event)
    if (!opts.quiet) say('out', describe, true)
    return true
  } catch (e) {
    const text = e instanceof Error ? e.message : String(e)
    error.value = text
    say('out', `${describe || 'Announcing yourself'} — not sent: ${text}`, false)
    return false
  }
}

/**
 * Publish everything this browser holds that the attached swap's counterparty
 * does not.
 *
 * Only the attached swap, which is the whole reason `attach` exists: publishing
 * one swap's contract into a room about a different swap hands the other side a
 * value they will rightly refuse.
 *
 * The key is claimed before the await rather than after. This runs from a watcher
 * on a list anything can reload, so a second round can start while the first is
 * still sealing an event, and marking it afterwards is how the same hand-off
 * goes out twice. A send that fails gives the key back.
 */
async function publishOwed() {
  if (!room.value || !swapId.value) return
  const sw = swaps.value.find((s) => s.id === swapId.value)
  if (!sw) return
  for (const handoff of owed(sw)) {
    const key = `${sw.id}:${handoff.type}`
    if (published.value[key] === handoff.value) continue
    published.value[key] = handoff.value
    if (!(await send({type: handoff.type}, handoff.describe))) delete published.value[key]
  }
}

/** Whether that exact value has already gone, so a card's Send button can say
 *  it would be a repeat rather than the first anybody has heard of it. */
function wasSent(id: string, type: HandoffType, value: string): boolean {
  return Boolean(value) && published.value[`${id}:${type}`] === value
}

/** Forget what this session has carried for one swap, so the next round
 *  publishes all of it again rather than skipping it as already delivered. */
function forgetPublished(id: string) {
  for (const key of Object.keys(published.value)) {
    if (key.startsWith(`${id}:`)) delete published.value[key]
  }
}

/**
 * Tell the counterparty a chain has moved, so they look now rather than in a
 * minute.
 *
 * It carries no value and makes no claim about what happened -- `what` is a
 * sentence for the transcript, not data. That is what makes it sendable after an
 * unlock, whose result must never travel in a message: the preimage stays where
 * it was published, and all this does is point the other browser at it sooner.
 * One announcement rather than a taxonomy to keep in step with the actions.
 *
 * Quiet about failure, because everything it accelerates happens anyway on the
 * heartbeat: a nudge that does not go costs a minute, not a swap.
 */
async function nudge(what: string) {
  if (!room.value || !swapId.value) return
  await send({type: 'moved', note: what}, what)
}

/**
 * Say everything again, and ask them to do the same.
 *
 * The automatic publishing above rests on each side remembering what it has
 * sent, and that memory lives in one tab's heap: a reload, a rejoined session or
 * a relay that accepted an event and dropped it leaves one side believing a
 * value was delivered that the other has never seen. Both cards then look
 * correct and the swap does not move, which is the worst shape a stall can take.
 *
 * There is no way to detect that from inside -- a message that never arrived
 * generates no evidence -- so the way out is not cleverer bookkeeping but a
 * button that makes the question moot. Everything that comes back goes through
 * the same verification, so re-sending a value they already had costs one line
 * in a transcript.
 *
 * A resync asks for hand-offs and never for another resync, which is what stops
 * two browsers politely re-syncing at each other forever.
 */
async function resync() {
  if (!room.value) return
  if (!swapId.value) {
    say('sys', 'This session is not attached to a swap, so there is nothing to re-sync.', false)
    return
  }
  forgetPublished(swapId.value)
  await send({type: 'resync'}, 'Asked them to send everything about this swap again')
  await publishOwed()
}

// Every way a hand-off can come into existence ends in one of these three
// changing: the swap list reloads with a contract on it, a session is opened, or
// a swap is attached to one. Watching the outcome rather than the causes is what
// keeps this from being a line somebody has to remember to add to the next
// action that changes a swap.
//
// Registered at module scope, alongside the state it watches: there is one
// session and one swap list, and a per-component watcher would mean one round of
// this per card on screen.
watch([swaps, swapId, active], () => void publishOwed())

/**
 * A hello arrived. Work out whether it is news, and whether we share a chain.
 *
 * Two independent things. Presence is bookkeeping. The chain check is the
 * substantive half -- it asks THIS browser's nodes to confirm the claim, so a
 * peer's numbers are only ever the question, never the answer.
 */
async function onHello(msg: SessionMessage) {
  const id = msg.peerId
  if (!id || id === myPeerId.value) return

  const isNew = !peers.value.includes(id)
  if (isNew) {
    peers.value = [...peers.value, id]
    say('in', 'Someone joined this session.', true)
  }

  // Greet back exactly once, so they see us without the two of us trading
  // hellos indefinitely.
  if (!greeted.has(id)) {
    greeted.add(id)
    void announce()
  }

  if (!msg.chain) {
    if (isNew) {
      say('in', 'They did not say which chains they are on, so that could not be checked.', false)
    }
    return
  }
  if (chainChecked.has(id)) return
  chainChecked.add(id)
  const {body: settings} = useSettings()
  try {
    const {agreement} = await api.chainId(settings.value, msg.chain)
    if (!agreement) return
    peerChain.value = agreement
    for (const problem of agreement.problems ?? []) say('in', `CHAIN MISMATCH: ${problem}`, false)
    for (const gap of agreement.unchecked ?? []) say('in', gap)
    if (agreement.sameChain && !(agreement.problems ?? []).length) {
      say('in', 'Same chains — a block your node and theirs both hold matches.', true)
    }
  } catch (e) {
    say('in', `Could not check their chains: ${e instanceof Error ? e.message : String(e)}`, false)
  }
}

/**
 * Run one verifying call and record what it decided. The check is the point, so
 * a refusal is reported as prominently as a success: a contract that failed the
 * audit is the most important thing that can appear in this panel.
 */
async function applyValue(what: string, fn: () => Promise<unknown>) {
  if (!swapId.value) {
    say('in', `Received ${what}, but this session is not attached to a swap yet.`, false)
    return
  }
  try {
    await fn()
    changed.value += 1
    say('in', `Accepted ${what} — it matches this swap.`, true)
  } catch (e) {
    say('in', `REFUSED ${what}: ${e instanceof Error ? e.message : String(e)}`, false)
  }
}

/**
 * Open one event and apply what is inside it. Messages this browser sent come
 * back off every relay it published to, and are dropped by role rather than by
 * event id -- the id would only catch the copy this tab sent, not one the same
 * user sent from another tab in the same room.
 */
async function receive(ev: NostrEvent) {
  if (!room.value) return
  const {body: settings} = useSettings()

  let msg: SessionMessage
  try {
    msg = (await api.sessionOpen(room.value.code, ev)).message
  } catch (e) {
    say('in', `A message could not be opened: ${e instanceof Error ? e.message : String(e)}`, false)
    return
  }
  // Our own message coming back off a relay we published it to. Filtered by
  // peer id rather than by role, because a session usually starts before either
  // side has a swap and therefore before either side has a role.
  if (msg.peerId && msg.peerId === myPeerId.value) return
  if (msg.from && msg.from === ourRole.value) return
  // Any message is proof of life, not only a ping — this is what keeps a busy
  // exchange of contracts and HTLC ids from being flagged as a stale peer
  // between pings.
  if (msg.peerId) lastSeen.set(msg.peerId, Date.now())

  switch (msg.type) {
    case 'hello':
      await onHello(msg)
      break
    case 'ping':
      // The lastSeen touch above is the entire point; there is nothing else
      // to do with one.
      break
    case 'bye':
      if (msg.peerId && peers.value.includes(msg.peerId)) {
        forgetPeer(msg.peerId)
        say('in', 'They left the session.', true)
      }
      break
    case 'offer':
      pendingOffer.value = msg.offer ?? ''
      say('in', 'They sent their offer — it is ready to decode into the new-swap form.')
      break
    case 'addresses':
      // Where they want each half paid, sent before any swap exists — a board
      // accept is the case that produces one. Recorded rather than applied:
      // there is usually no swap to apply it to yet, and even when there is, an
      // address is not a term this may pin. `swapoffer1` is what pins terms, it
      // is checked field by field on create, and it arrives later by the
      // ordinary path.
      //
      // So this is a head start on the one field the other side cannot fill in
      // for you, and it is overwritten by the signed offer exactly as the
      // board's own `pendingTrade` is.
      peerAddresses.value = {
        btc: (msg.btcAddr ?? '').trim(),
        znn: (msg.zenonAddr ?? '').trim(),
      }
      say('in', 'They sent where to pay them — it is filled into the new-swap form.')
      break
    case 'pkh':
      await applyValue(`their pubkey hash ${short(msg.pkhHex)}`, () =>
        api.counterparty(swapId.value, msg.pkhHex ?? ''),
      )
      break
    case 'contract':
      await applyValue(`their contract ${short(msg.contractHex)}`, () =>
        api.audit(swapId.value, msg.contractHex ?? ''),
      )
      break
    case 'zenon':
      await applyValue(`their Zenon HTLC ${short(msg.htlcId)}`, async () => {
        const r = await api.verifyZenon(swapId.value, msg.htlcId ?? '', settings.value)
        if (r.error) throw new Error(r.error)
        return r
      })
      break
    case 'moved':
      // Nothing in this message is believed, because nothing in it is a claim:
      // it says a chain moved and this browser goes and reads the chain. The
      // announcement decides when to look, never what is true.
      //
      // The burst is what makes it worth sending. A block published seconds ago
      // is not readable yet on either chain, so the first look usually finds
      // nothing; syncNow keeps looking for about a minute.
      say(
        'in',
        msg.note
          ? `They moved: ${msg.note}. Checking the chains.`
          : 'They moved — checking the chains.',
      )
      if (swapId.value) void syncNow(swapId.value)
      break
    case 'resync':
      // Answered with values, never with another resync — see resync() above.
      // The forget is what makes it mean anything: without it this side would
      // consult its own record of what it had already sent and helpfully send
      // nothing, which is precisely the belief being disputed.
      if (!swapId.value) {
        say('in', 'They asked to re-sync, but this session is not attached to a swap yet.', false)
        break
      }
      say('in', 'They asked for everything again — re-sending what this side holds.')
      forgetPublished(swapId.value)
      await publishOwed()
      break
    case 'funded':
      say('in', `They funded the contract — ${msg.fundingTxid}. Refreshing from chain.`)
      if (swapId.value) {
        try {
          await api.refresh(swapId.value, settings.value)
          changed.value += 1
        } catch {
          // A refresh failing here is not the message's fault, and the card's
          // own Refresh button reports it far better than a line in here would.
        }
      }
      break
    default:
      if (msg.note) say('in', msg.note)
  }
}

export function useSession() {
  return {
    room,
    lines,
    error,
    busy,
    relays,
    active,
    connected,
    swapId,
    ourRole,
    pendingOffer,
    pendingTrade,
    peerAddresses,
    changed,
    peers,
    peerPresent,
    peerChain,
    join,
    leave,
    attach,
    send,
    say,
    wasSent,
    resync,
    nudge,
  }
}

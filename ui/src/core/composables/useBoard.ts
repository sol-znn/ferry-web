import {computed, ref, shallowRef, watch} from 'vue'
import {api} from '@/core/api'
import {isDev} from '@/core/env'
import {RelayPool, type AcceptEvent, type NostrEvent, type RelayStatus} from '@/core/nostr'
import {useFerry} from '@/core/composables/useFerry'
import {useSession} from '@/core/composables/useSession'
import {useSettings} from '@/core/composables/useSettings'
import {useBoardIdentity} from '@/core/composables/useBoardIdentity'
import type {InboundTake, Listing, MyPost, SessionRoom} from '@/types'

/**
 * The board, live.
 *
 * One relay subscription, two streams coming down it -- everybody's posts, and
 * takes addressed to this browser -- and a small amount of bookkeeping to turn a
 * stream of events into a list somebody can read.
 *
 * The bookkeeping is where all the subtlety is, and it comes from one fact: a
 * relay hands over events, not state. An edit is a NEW event carrying the same
 * `d` tag, so a naive list shows both versions; a relay that was offline when a
 * post was withdrawn keeps serving the old one; and a post that expired an hour
 * ago is still on every relay that never implemented NIP-40. So this file keeps
 * the newest version per (author, post id), drops what has expired against the
 * READER's clock, and treats anything a relay says as a claim rather than a
 * fact. Go does the verifying; this decides what a verified claim replaces.
 *
 * Module-level state, like the session's, because there is one board.
 */

/** Nothing older than this is asked for.
 *
 *  A post lives at most a week, so a week is the whole of the board -- asking
 *  for more would be asking every relay to scan its history to send us things
 *  that are already dead. */
const HISTORY_SECONDS = 7 * 24 * 3600

/** How many posts to pull per relay. Generous: the filter is narrow (one kind,
 *  one tag, one network) so this is a small query, and a board that silently
 *  truncated would look like a quiet market rather than a capped one. */
const POST_LIMIT = 500

const listings = ref<Listing[]>([])
const takes = ref<InboundTake[]>([])
const mine = ref<MyPost[]>([])
const relays = ref<{url: string; status: RelayStatus}[]>([])
const pool = shallowRef<RelayPool | null>(null)
const error = ref('')
const busy = ref(false)
const loading = ref(false)
/** Bumped whenever the list changed, so a page can re-sort without watching an
 *  array of objects that is replaced wholesale on every event. */
const revision = ref(0)

/**
 * The newest version of each post, keyed by author and slot.
 *
 * The key is `${author}:${postId}` rather than the post id alone, and that is
 * not paranoia: the id is chosen by whoever publishes, so two people can pick
 * the same one, deliberately or otherwise. Keying by the author as well means
 * one person's post can never replace another's — which is the addressability
 * rule Nostr itself applies, restated here because a relay's copy is not the
 * only path an event takes to this list.
 */
const byKey = new Map<string, Listing>()

/**
 * When each board key was last known to be at a keyboard, in seconds.
 *
 * Keyed by author, holding a timestamp rather than a boolean, because online is
 * not a thing that arrives — it is a thing that lapses. A boolean written when a
 * beat landed would still say "online" an hour after the browser closed, since
 * nothing arrives to say otherwise; a timestamp compared against the reader's
 * clock goes stale on its own, which is the only way this can be right without a
 * server to tell it when somebody left.
 *
 * A plain object replaced wholesale rather than a reactive Map: a beat is at
 * most one write per author per minute, and the pattern matches `byKey`'s
 * rebuild — cheap, and it makes every row recompute together.
 */
const seenAt = ref<Record<string, number>>({})

/** How long a beat means "online", from the module. Zero until the identity has
 *  loaded, which reads as "nobody is online" rather than as "everybody is" —
 *  the safe direction for a window nobody has told us yet. */
const staleSeconds = ref(0)

/**
 * Takes that have been dealt with, remembered across reloads.
 *
 * A take is a REGULAR Nostr event, which is the whole reason this has to exist:
 * relays store one forever and replay it to whoever subscribes next, so a take
 * that was accepted or waved away in one session arrives again, looking exactly
 * as new as it did the first time. Dismissing it in memory only cleared it until
 * the next reload -- which is not dismissing it, it is hiding it for as long as
 * nobody presses F5.
 *
 * There is nothing to publish here: declining is not worth telling a stranger
 * about (see BoardInbox), and the sender's own copy is theirs. So this is one
 * browser's record of what it has finished with, and it lives beside the board's
 * other records rather than among the swaps.
 */
const HANDLED_KEY = isDev ? 'ferry.dev.board.takes.handled' : 'ferry.board.takes.handled'

/** Bounded, because relays keep takes indefinitely and this list would otherwise
 *  grow for the life of the browser. Oldest are dropped first; the worst a
 *  forgotten entry costs is one row to dismiss again. */
const HANDLED_LIMIT = 500

function loadHandled(): string[] {
  try {
    const raw = localStorage.getItem(HANDLED_KEY)
    const list: unknown = raw ? JSON.parse(raw) : []
    return Array.isArray(list) ? list.filter((v): v is string => typeof v === 'string') : []
  } catch {
    // A browser that will not keep this costs a re-dismissal, nothing more.
    return []
  }
}

const handled = ref<string[]>(loadHandled())

/** Mark one take as dealt with, here and for the next load. */
function markHandled(eventId: string) {
  if (!eventId || handled.value.includes(eventId)) return
  handled.value = [...handled.value, eventId].slice(-HANDLED_LIMIT)
  try {
    localStorage.setItem(HANDLED_KEY, JSON.stringify(handled.value))
  } catch {
    // Storage refused. The dismissal still holds for this session.
  }
}

const connected = computed(() => relays.value.filter((r) => r.status === 'open').length)

/** Posts still standing: open, not expired, and not this browser's own.
 *
 *  Own posts are excluded from the market and shown in their own section. A
 *  board where your own offer sits among the others is one where you eventually
 *  try to take it. */
const open = computed(() =>
  listings.value.filter((l) => !l.mine && !l.expired && l.post.status === 'open'),
)

/** Everything, including expired and withdrawn, for the "show all" toggle. A
 *  post that just vanished is a post the reader cannot ask about. */
const all = computed(() => listings.value)

/**
 * The takes worth showing: the ones that can still be acted on.
 *
 * Two things are dropped, and they are dropped for different reasons.
 *
 * Anything already dealt with — accepted, or waved away — is gone for good,
 * because `handled` outlives the reload that used to bring it back.
 *
 * Anything naming a post this browser no longer holds, or holds as finished, is
 * dropped as well: it cannot be accepted, so a row offering to is a row with a
 * disabled button and a badge saying why, arriving fresh from a relay on every
 * single load. That is the noise being complained about, and no click could ever
 * clear it because there is nothing left to act on.
 *
 * Filtered rather than marked handled on arrival, deliberately. `mine` is read
 * from storage before the pool opens, so it is normally there — but "normally"
 * is not a reason to write a permanent record. A filter that is wrong for a
 * moment corrects itself; a dismissal that is wrong is a take nobody can get
 * back.
 */
const inbox = computed(() =>
  takes.value.filter((t) => {
    if (handled.value.includes(t.eventId)) return false
    const post = mine.value.find((p) => p.post.id === t.take.postId)
    if (!post) return false
    return post.post.status === 'open' || post.post.status === 'taken'
  }),
)

function rebuild() {
  listings.value = [...byKey.values()]
  revision.value += 1
}

/**
 * Whether a board key is at a keyboard right now.
 *
 * Takes the clock rather than reading it, so a row recomputes on the same ticker
 * that already moves its countdown — a dot that read `Date.now()` itself would
 * be a dot that never changed, for the reason BoardPage's `nowMs` exists.
 *
 * Three states and not two. `unknown` is what a key with no beat at all gets,
 * and it is genuinely different from offline: this build has been publishing
 * beats since it shipped, but a post from a browser that predates it, or one
 * whose beats every relay on this list happens to have dropped, is a key nothing
 * is known about. Showing that as a confident grey "offline" would be inventing
 * a fact. The UI may still draw the two the same way — see PresenceDot, which
 * does — but the distinction is kept here so the tooltip can be honest.
 */
export type Presence = 'online' | 'offline' | 'unknown'

function presenceOf(author: string, nowMs: number): Presence {
  const last = seenAt.value[author]
  if (!last || !staleSeconds.value) return 'unknown'
  return Math.floor(nowMs / 1000) - last <= staleSeconds.value ? 'online' : 'offline'
}

/**
 * Take one verified listing into the list, or refuse it as older than what is
 * already there.
 *
 * `publishedAt` is the relay's copy of the author's own clock, which is not a
 * trustworthy ordering in general -- an author can write any number into it. It
 * is trustworthy for THIS comparison, because both versions were signed by the
 * same key: the only person who can use a forward-dated timestamp to suppress a
 * newer version of a post is the person who wrote both, and they could simply
 * not publish instead. It is the same rule relays apply to replaceable events.
 *
 * The tie-break is the other half of that rule, and it is why two people looking
 * at this board now see the same thing.
 *
 * Timestamps alone are a PARTIAL order: two versions of one post can share a
 * second, and the old comparison let whichever arrived second win. Relays answer
 * in whatever order they answer in, so two browsers merging the same pair of
 * events reached opposite conclusions -- one showing an offer, the other showing
 * it withdrawn -- with nothing in either session to suggest anything was wrong.
 *
 * NIP-01 settles the same tie for relays by keeping the event with the LOWEST
 * id, so that is what is mirrored here. Not an arbitrary pick of two equally
 * good rules: it is the one every relay is already applying, which makes this
 * list agree with what the relays will converge on rather than merely agreeing
 * with other copies of this app.
 *
 * Publishing avoids creating ties in the first place -- see SealPostAfter in
 * wasm/boardpost.go -- so this is the belt to that pair of braces. It still has
 * to be here: the events on relays today were signed before that existed.
 */
function accept(listing: Listing) {
  const key = `${listing.author}:${listing.post.id}`
  const held = byKey.get(key)
  if (held && !supersedes(listing, held)) return
  byKey.set(key, listing)
  rebuild()
}

/** Whether `next` should replace `held`: newer, or the same age with the lower
 *  event id. A total order, so every reader picks the same winner. */
function supersedes(next: Listing, held: Listing): boolean {
  if (next.publishedAt !== held.publishedAt) return next.publishedAt > held.publishedAt
  return next.eventId < held.eventId
}

/** Re-judge expiry without going back to a relay.
 *
 *  A post is retired by the reader's clock rather than by anything arriving, so
 *  without this a board left open overnight would keep showing yesterday's
 *  offers as live. Cheap enough to run on a timer: it touches a field, and the
 *  rest of the listing is untouched. */
function reap(now = Date.now()) {
  let changed = false
  for (const listing of byKey.values()) {
    const expired = listing.post.expiresAt <= Math.floor(now / 1000)
    if (expired !== listing.expired) {
      listing.expired = expired
      changed = true
    }
  }
  if (changed) rebuild()
}

let reaper: ReturnType<typeof setInterval> | null = null

async function receive(ev: NostrEvent, kinds: BoardKinds, acceptEvent: AcceptEvent) {
  const {body: settings} = useSettings()
  if (ev.kind === kinds.post) {
    try {
      const {listing} = await api.boardRead(ev, settings.value)
      if (!acceptEvent()) return
      // A board is per network, and the subscription already asks for `#n` —
      // but a relay filter is a REQUEST, not a guarantee. Relays are free to
      // send more than was asked for and they differ in which tags they
      // actually index, so a post for another chain reached one reader's list
      // and not another's purely on which relay answered first. `ReadPost`
      // records the mismatch rather than refusing it, because the same function
      // reads this browser's own posts back; the decision not to carry one
      // belongs here.
      //
      // Dropped at the door rather than filtered per view, so every list built
      // from `byKey` agrees. A filter on the open market alone would still have
      // let "include expired and withdrawn" disagree between two sessions.
      if (listing.post.network !== settings.value.network) return
      accept(listing)
    } catch {
      // A post that does not verify is not an error the reader can act on: it is
      // a stranger's broken or forged event, and the correct response is to not
      // show it. Reported nowhere, deliberately — a board that surfaced every
      // malformed event a relay carries would be a board of error messages.
    }
    return
  }
  if (ev.kind === kinds.presence) {
    try {
      const {seen} = await api.boardReadPresence(ev, settings.value)
      if (!acceptEvent()) return
      // Newest wins. Relays replay their stored copy on connect and a live beat
      // follows, so the same key arrives twice in the ordinary case — and an
      // older beat overwriting a newer one would show somebody who is here as
      // having left. The same rule `accept` applies to a post, for the same
      // reason: both were signed by the one key that could have written either.
      const held = seenAt.value[seen.author] ?? 0
      if (seen.seenAt <= held) return
      seenAt.value = {...seenAt.value, [seen.author]: seen.seenAt}
    } catch {
      // A beat that does not verify is a stranger's broken, forged, or
      // future-dated event, and the right response is to know nothing about that
      // key rather than to claim they are offline. Silent for the same reason a
      // bad post is — see above.
    }
    return
  }
  if (ev.kind === kinds.take) {
    // Already dealt with in an earlier session. Skipped before it is opened
    // rather than filtered after, because opening it is a call into the module
    // to decrypt something whose answer is already known.
    if (handled.value.includes(ev.id)) return
    try {
      const {take} = await api.boardReadTake(ev)
      if (!acceptEvent() || handled.value.includes(take.eventId)) return
      // Newest first: an inbox where the answer to "who wants this" is at the
      // bottom is one that gets scrolled past.
      takes.value = [take, ...takes.value.filter((t) => t.eventId !== take.eventId)]
    } catch {
      // Takes addressed to somebody else arrive here too — the `p` filter is a
      // request, and relays are free to send more. Not being able to open one is
      // the normal case, not a fault.
    }
    return
  }
}

interface BoardKinds {
  post: number
  take: number
  delete: number
  presence: number
  tag: string
}

/**
 * Open the board.
 *
 * Two filters on one subscription. The first asks for every post on this
 * network, which is what makes a board a board -- no author, no invitation, just
 * a tag and a kind. The second asks only for events addressed to this browser's
 * key, which is how a take arrives.
 *
 * The kinds come from the module rather than from constants here. A filter built
 * from a stale copy of a number is a board that connects, subscribes, and
 * silently shows nothing.
 */
async function start() {
  const {body: settings, relayList} = useSettings()
  const {identity, load} = useBoardIdentity()
  error.value = ''
  loading.value = true
  try {
    if (!identity.value) await load()
    const id = identity.value
    if (!id) throw new Error('This browser has no board key yet.')
    const kinds = id.kinds

    stop()
    staleSeconds.value = id.presence.staleSeconds
    await refreshMine()

    const nowSec = Math.floor(Date.now() / 1000)
    const since = nowSec - HISTORY_SECONDS
    const p = new RelayPool(relayList())
    pool.value = p
    p.open(
      [
        {
          kinds: [kinds.post],
          '#t': [kinds.tag],
          '#n': [settings.value.network],
          since,
          limit: POST_LIMIT,
        },
        // No `since` on takes. A take sent while this browser was closed is
        // exactly the one worth having, and there are never many.
        {kinds: [kinds.take], '#p': [id.pubKey], limit: 200},
        // Presence, and the `since` here is doing real work rather than being
        // copied from the filter above: a beat older than the staleness window
        // can only ever produce "offline", which is what a key with no beat
        // shows anyway. Asking for them would be asking every relay to send a
        // stored event per author so this page could compute the same answer it
        // already had. No limit for the opposite reason the posts have one —
        // this is one small event per author and truncating it would silently
        // grey out whoever fell off the end.
        {
          kinds: [kinds.presence],
          '#t': [kinds.tag],
          '#n': [settings.value.network],
          since: nowSec - id.presence.staleSeconds,
        },
      ],
      (ev, acceptEvent) => receive(ev, kinds, acceptEvent),
      () => (relays.value = p.status),
    )
    relays.value = p.status
    reaper = setInterval(() => reap(), 30_000)
    startHeartbeat(id.presence.beatSeconds)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

/**
 * Whether this browser has anything worth being present for.
 *
 * The gate on the heartbeat, and the reason there is one. A beat is a public,
 * signed statement that a key is at a keyboard, republished every minute for as
 * long as the tab is open — worth publishing when somebody may be deciding
 * whether to take your offer, and nobody's business the rest of the time. A page
 * that beat unconditionally would be broadcasting a browsing session.
 *
 * `taken` counts alongside `open`: a post mid-trade has a counterparty on the
 * other end of a session who very much cares whether you are still there.
 * Expired posts do not, whatever their status says — nobody can act on one.
 */
function hasLivePost(now = Date.now()): boolean {
  const nowSec = Math.floor(now / 1000)
  return mine.value.some(
    (p) => (p.post.status === 'open' || p.post.status === 'taken') && p.post.expiresAt > nowSec,
  )
}

let heart: ReturnType<typeof setInterval> | null = null

/**
 * Whether this tab is actually in front of somebody.
 *
 * Guarded rather than read directly: `document` is absent under the smoke
 * runner, which drives the same module under Node.
 */
function visible(): boolean {
  return typeof document === 'undefined' || document.visibilityState !== 'hidden'
}

/**
 * Publish one beat, if there is a reason to and somewhere to publish it.
 *
 * Silent while the tab is hidden, and that is what buys the short staleness
 * window rather than being a saving.
 *
 * A hidden tab's timers are throttled — Chrome to roughly one firing a minute —
 * so a heartbeat that kept going while hidden would arrive at a cadence nobody
 * controls, and the reader's window would have to be wide enough to tolerate the
 * worst of it. That is what held PresenceStale at three minutes: the floor came
 * from the throttled cadence, not from the beat interval, so shortening the beat
 * could not move it. Worse, a window set near that cadence makes the dot FLAP,
 * green or grey depending on whether a throttled firing happened to land inside
 * it, which is a worse answer than either.
 *
 * Going quiet instead makes the lapse deterministic and lets the window be three
 * beats of a tab that is genuinely being watched. It also narrows what the dot
 * claims to something a beat can honestly support: not "this browser is running
 * somewhere" but "the board is in front of them" — which is the question a taker
 * is actually asking. Switching back publishes immediately, so the cost of
 * alt-tabbing is a dot that goes grey and comes straight back.
 *
 * Failure is swallowed on purpose. A relay that blinks is the normal state of a
 * relay, and the cost of a missed beat is a dot that goes grey early and returns
 * on the next one — not worth an error banner over a list of live offers.
 */
async function beat() {
  const {body: settings} = useSettings()
  if (!pool.value || !visible() || !hasLivePost()) return
  try {
    const {event} = await api.boardPresence(settings.value)
    publishAll([event])
  } catch {
    // See above: a dropped beat is self-correcting.
  }
}

/** Coming back to the tab beats at once rather than waiting for the next tick.
 *  Without it, returning to a board left hidden would leave the author grey for
 *  up to a full interval while they are demonstrably right there. */
function onVisibility() {
  if (visible()) void beat()
}

/** Beat now, then on the module's interval.
 *
 *  Now rather than after the first tick, because the first tick is half a minute
 *  away: an author who has just posted would otherwise sit greyed out on
 *  everyone's board, at exactly the moment their offer is newest and most likely
 *  to be read. */
function startHeartbeat(beatSeconds: number) {
  stopHeartbeat()
  void beat()
  heart = setInterval(() => void beat(), Math.max(15, beatSeconds) * 1000)
  if (typeof document !== 'undefined') {
    document.addEventListener('visibilitychange', onVisibility)
  }
}

function stopHeartbeat() {
  if (heart) {
    clearInterval(heart)
    heart = null
  }
  if (typeof document !== 'undefined') {
    document.removeEventListener('visibilitychange', onVisibility)
  }
}

/** Close the subscription and forget the market.
 *
 *  Own posts survive: they are in the module's storage, not in this list, and
 *  they are what the "my posts" section is built from whether the board is open
 *  or not.
 *
 *  Presence goes with the market rather than surviving it. A `seenAt` kept
 *  across a network switch or a reconnect would be answering "is this person
 *  here" from beats collected against a board that is no longer on screen. */
function stop() {
  if (reaper) {
    clearInterval(reaper)
    reaper = null
  }
  stopHeartbeat()
  pool.value?.close()
  pool.value = null
  relays.value = []
  byKey.clear()
  listings.value = []
  takes.value = []
  seenAt.value = {}
}

function publishAll(events: NostrEvent[]) {
  const p = pool.value
  if (!p) throw new Error('The board is not connected to any relay, so nothing could be published.')
  for (const ev of events) p.publish(ev)
}

async function refreshMine() {
  try {
    mine.value = (await api.boardMine()).posts
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

/**
 * Publish a post, new or edited.
 *
 * The signed event goes straight back onto the same pool the board is reading,
 * so it comes back down as an ordinary event and through the same verification
 * every stranger's post gets. Nothing is inserted into the list locally, which
 * is deliberate: a post that appears because this browser put it there would
 * look published whether or not a single relay accepted it.
 */
async function publish(form: Record<string, unknown>): Promise<MyPost | null> {
  const {body: settings} = useSettings()
  error.value = ''
  busy.value = true
  try {
    const {event, post} = await api.boardPublish(form, settings.value)
    publishAll([event])
    await refreshMine()
    // The first post is what makes this browser worth beating for — until now
    // hasLivePost was false and the heartbeat was skipping every tick. Beating
    // here rather than waiting for the next one means the offer and its author's
    // green dot land on other people's boards together.
    void beat()
    return post
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    return null
  } finally {
    busy.value = false
  }
}

/**
 * Take a post down, as a withdrawal or as a finished trade.
 *
 * Two events, and both are published: a void replacement every relay honours
 * because it is only an edit, and a NIP-09 deletion request for those that
 * implement one. See handleBoardWithdraw.
 */
async function withdraw(id: string, done = false): Promise<boolean> {
  error.value = ''
  busy.value = true
  try {
    const {events} = await api.boardWithdraw(id, done)
    publishAll(events)
    await refreshMine()
    return true
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    return false
  } finally {
    busy.value = false
  }
}

/** Forget a post locally. Withdraw first — this reaches no relay. */
async function forget(id: string) {
  error.value = ''
  try {
    await api.boardForget(id)
    await refreshMine()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

/**
 * Take somebody's offer: mint a room, seal it to them, and join it.
 *
 * Joining before the event has been anywhere is the right order. A session that
 * exists only after the counterparty answers is one that misses their first
 * message; and the room is derived from a code this browser already holds, so
 * there is nothing to wait for.
 */
async function take(
  listing: Listing,
  opts: {amountSats?: number; note?: string; btcAddr: string; znnAddr: string},
): Promise<SessionRoom | null> {
  const session = useSession()
  const post = listing.post
  error.value = ''
  busy.value = true
  try {
    const amountSats = opts.amountSats && opts.amountSats > 0 ? opts.amountSats : post.amountSats
    // The taker's own addresses travel sealed inside the take, so the author
    // comes away holding somewhere to pay the moment they read it. Passed in
    // rather than read from the wallets here: this module has no business
    // reaching into an extension, and the page that offers the button is the
    // one that already knows both are connected.
    const {event, room} = await api.boardTake({
      author: listing.author,
      postId: post.id,
      ...opts,
      amountSats,
    })
    publishAll([event])
    await session.join(room.code)

    // The terms this side is taking, for the swaps page to build its form from.
    // Both the leg and the role are flipped: a post states what its AUTHOR does,
    // and a taker takes the other half of each.
    //
    // No lockHours. The post's figure is the maker's own contract duration,
    // which says nothing about what this side's should be — Go derives that from
    // the role, and the ordering that actually matters is enforced against the
    // real contract rather than against a number copied out of an advertisement.
    session.pendingTrade.value = {
      leg: otherLeg(post.side),
      role: otherRole(post.role),
      amountSats,
      zenonAmount: scaledZenon(post, amountSats),
      zenonToken: post.zenonToken ?? '',
      lockHours: 0,
      zenonPeerAddress: post.znnAddr ?? '',
      postId: post.id,
      peer: listing.author,
    }
    session.say('sys', `Waiting for the other side to accept your take on post ${post.id}.`)
    return room
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    return null
  } finally {
    busy.value = false
  }
}

/**
 * Accept one take: join its room, mark the post taken, and remember who.
 *
 * The post is moved to `taken` rather than withdrawn. A taker whose take was not
 * the one accepted then sees why, instead of watching the offer disappear and
 * being left to guess whether they were beaten to it or blocked.
 */
async function accept_(inbound: InboundTake): Promise<boolean> {
  const session = useSession()
  error.value = ''
  busy.value = true
  try {
    const post = mine.value.find((p) => p.post.id === inbound.take.postId)
    if (!post) throw new Error(`This browser has no post with id ${inbound.take.postId}.`)

    await session.join(inbound.take.code)
    await api.boardLink(post.post.id, {code: inbound.take.code, taker: inbound.from})
    const {event} = await api.boardPublish(
      {...postForm(post), id: post.post.id, status: 'taken'},
      useSettings().body.value,
    )
    publishAll([event])
    // Marked before the list is reloaded, so the row goes at the moment it stops
    // being a question. Without this an accepted take sat in the inbox offering
    // to be accepted again, and came back from the relay on every reload after.
    markHandled(inbound.eventId)
    await refreshMine()

    // The maker's own side, which needs no flipping: the post already states
    // what THEY do. The amount is whatever the taker asked for within the band,
    // and the ZNN scales with it.
    //
    // The taker's Zenon address comes straight off the take, which is the whole
    // point of it travelling there: this used to be blank and wait for the
    // session, so accepting landed the author on a form with a hole in it and
    // nothing to fill the hole with until the other side happened to send one.
    // A take from an older build still leaves it blank, and the session still
    // covers that.
    const amountSats =
      inbound.take.amountSats && inbound.take.amountSats > 0
        ? inbound.take.amountSats
        : post.post.amountSats
    session.pendingTrade.value = {
      leg: post.post.side,
      role: post.post.role,
      amountSats,
      zenonAmount: scaledZenon(post.post, amountSats),
      zenonToken: post.post.zenonToken ?? '',
      lockHours: post.post.lockHours ?? 0,
      zenonPeerAddress: inbound.take.znnAddr ?? '',
      postId: post.post.id,
      peer: inbound.from,
    }

    // And the author's own addresses back the other way, so the exchange is
    // complete the moment a take is accepted and neither side has to ask.
    //
    // Sent even though the post already carries them, because the post carries
    // the addresses it was PUBLISHED with: an author who switched wallets since
    // — or who is renewing a post from last week — would otherwise have the
    // taker building a swap against an address they no longer hold. This is the
    // current answer, from the wallet that is connected now.
    void session.send(
      {
        type: 'addresses',
        btcAddr: post.post.btcAddr ?? '',
        zenonAddr: post.post.znnAddr ?? '',
      },
      'where to pay you',
    )
    session.say('sys', `Accepted a take on post ${post.post.id}. Agree the swap in this session.`)
    return true
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    return false
  } finally {
    busy.value = false
  }
}

/**
 * Drop a take from the inbox, for good.
 *
 * Local only, and nothing is published: declining is not worth telling a
 * stranger about, and a refusal signed by your board key would be a permanent
 * public record of who you would not trade with.
 *
 * Written down rather than only removed from the array. A take is a regular
 * Nostr event, so the relay that stored it hands it back on the next
 * subscription — which made the old in-memory version undo itself on every
 * reload.
 */
function dismiss(eventId: string) {
  markHandled(eventId)
  takes.value = takes.value.filter((t) => t.eventId !== eventId)
}

/** Attach a post to the swap it became, so the watcher below can retire it. */
async function link(postId: string, swapId: string) {
  try {
    await api.boardLink(postId, {swapId})
    await refreshMine()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

/**
 * The Zenon side of a trade, for a Bitcoin amount that may not be the post's.
 *
 * A size band means the headline amounts are a ratio rather than a pair: a post
 * offering 0.001-0.1 BTC for 1200 ZNN is quoting a rate, and somebody taking
 * 0.05 of it is not owed 1200. So the ZNN scales with the Bitcoin, and the
 * fixed-amount case falls out of the same arithmetic unchanged.
 *
 * Eight decimal places because that is what ZNN has, trimmed so an exact figure
 * does not acquire a tail of zeros it did not have.
 */
function scaledZenon(post: MyPost['post'] | Listing['post'], amountSats: number): string {
  const quoted = Number(post.zenonAmt)
  if (!Number.isFinite(quoted) || quoted <= 0 || post.amountSats <= 0) return post.zenonAmt
  if (amountSats === post.amountSats) return post.zenonAmt
  const scaled = (quoted * amountSats) / post.amountSats
  if (!Number.isFinite(scaled) || scaled <= 0) return post.zenonAmt
  return String(Number(scaled.toFixed(8)))
}

/** The other side of a leg or a role. A board post states the AUTHOR's, so a
 *  taker takes the opposite of both; getting either backwards is a swap where
 *  both sides think they are sending the same coin. */
function otherLeg(leg: Listing['post']['side']) {
  return leg === 'send' ? ('receive' as const) : ('send' as const)
}
function otherRole(role: Listing['post']['role']) {
  return role === 'initiator' ? ('participant' as const) : ('initiator' as const)
}

/** A stored post, back in the shape the publish form sends. Used when this file
 *  republishes a post without a form in front of it — accepting a take, or
 *  retiring a finished one. */
function postForm(p: MyPost): Record<string, unknown> {
  const post = p.post
  return {
    side: post.side,
    role: post.role,
    amountSats: post.amountSats,
    minSats: post.minSats ?? 0,
    maxSats: post.maxSats ?? 0,
    zenonAmt: post.zenonAmt,
    zenonToken: post.zenonToken ?? '',
    lockHours: post.lockHours ?? 0,
    btcAddr: post.btcAddr ?? '',
    znnAddr: post.znnAddr ?? '',
    note: post.note ?? '',
    completed: post.completed ?? 0,
    // The remaining life, not the original span: republishing with the original
    // TTL would silently extend a post every time it was touched.
    ttlSeconds: Math.max(600, post.expiresAt - Math.floor(Date.now() / 1000)),
  }
}

/**
 * Retire a post whose swap has finished.
 *
 * This is the whole of "the board updates itself when the swap completes", and
 * it works because the two halves were joined earlier: `boardLink` wrote a swap
 * id onto the post when a take was accepted, and this watches the swap list this
 * app already keeps. No chain is polled for a board post, because a board post
 * is not on a chain.
 *
 * It runs only in the browser that made the post, because it is the only browser
 * that can: withdrawing needs the key that signed. A maker who closes the tab
 * mid-swap leaves the offer standing, and the day-long expiry is what covers
 * that -- which is most of why the default is a day.
 */
const {swaps} = useFerry()
const retiring = new Set<string>()

watch(
  [swaps, mine],
  () => {
    for (const post of mine.value) {
      if (!post.swapId || post.post.status === 'done' || post.post.status === 'void') continue
      const swap = swaps.value.find((s) => s.id === post.swapId)
      // `settled` is Go's verdict on whether a swap is over, derived from the
      // real rule rather than from a state name — see swapView.Settled. Archived
      // counts too: a user filing a swap away is a user saying it is finished.
      if (!swap?.settled && !swap?.archived) continue
      if (retiring.has(post.post.id)) continue
      // Only while there is somewhere to publish it. Withdrawing marks the
      // stored post done whether or not a relay heard, so running this with the
      // board closed would retire it locally, leave it standing everywhere else,
      // and never try again — the status it just wrote is what stops it.
      if (!pool.value) continue
      retiring.add(post.post.id)
      void withdraw(post.post.id, true).finally(() => retiring.delete(post.post.id))
    }
  },
  {deep: true},
)

export function useBoard() {
  return {
    listings,
    open,
    all,
    takes,
    inbox,
    mine,
    relays,
    connected,
    error,
    busy,
    loading,
    revision,
    seenAt,
    presenceOf,
    start,
    stop,
    reap,
    publish,
    withdraw,
    forget,
    take,
    accept: accept_,
    dismiss,
    link,
    refreshMine,
  }
}

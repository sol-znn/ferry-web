/**
 * A small Nostr relay pool: enough of the protocol to carry a swap session.
 *
 * Here rather than in the WebAssembly module because everything crossing it is
 * already sealed. The module signs and encrypts a message, this file moves an
 * opaque envelope, the module opens it at the other end. Nothing here can read a
 * session, and nothing in it decides anything about a swap.
 *
 * Why Nostr: a session needs two browsers with no server between them to find
 * each other, and every other option costs something this app will not spend --
 * WebRTC still needs a signalling server, a broker needs an account, and
 * anything self-hosted makes a static site depend on infrastructure someone has
 * to keep running.
 *
 * Several relays are used at once and the results merged. A relay that is down,
 * slow, or quietly dropping events is the normal state of one relay.
 */

/** A signed event, exactly as wasm/session.go produces it. */
export interface NostrEvent {
  id: string
  pubkey: string
  created_at: number
  kind: number
  tags: string[][]
  content: string
  sig: string
}

export interface NostrFilter {
  kinds?: number[]
  authors?: string[]
  /** The addressable slot. A session filters on this to name its room. */
  '#d'?: string[]
  /**
   * The board's index tag, and how a reader finds a board they have no author
   * for. Single-letter tags are the ones relays index; a filter on anything else
   * is one the relay answers by scanning, or refuses outright.
   */
  '#t'?: string[]
  /** Who an event is addressed at. How a take reaches the person it is for. */
  '#p'?: string[]
  /** The network a board post names, so a relay drops the other chains before
   *  sending them. */
  '#n'?: string[]
  since?: number
  limit?: number
}

/**
 * Relays this app suggests, and nothing more than a suggestion. All five are
 * long-running public relays that accept events from anyone, listed rather than
 * hard-coded so the settings dialog can show what a blank field will use.
 *
 * Five rather than one because a public relay being unreachable is its normal
 * state rather than an incident: the first live test of this feature reached two
 * of three, and the session worked precisely because those two carried it.
 */
export const DEFAULT_RELAYS = [
  'wss://relay.damus.io',
  'wss://nos.lol',
  'wss://relay.nostr.band',
  'wss://relay.primal.net',
  'wss://offchain.pub',
]

export type RelayStatus = 'connecting' | 'open' | 'closed'

/** Call after WASM verification and before applying the event. False means
 *  another copy was already accepted, or this pool has been closed. */
export type AcceptEvent = () => boolean

type EventHandler = (ev: NostrEvent, accept: AcceptEvent) => void | Promise<void>

interface Relay {
  url: string
  sock?: WebSocket
  status: RelayStatus
  /** Events queued while the socket was still opening. */
  queue: string[]
}

/**
 * One pool, one room.
 *
 * A pool is created per session and closed with it. It holds no state that
 * outlives the session, which is what makes leaving a session actually leave
 * it: there is no lingering subscription to a room the user has finished with.
 */
export class RelayPool {
  private relays: Relay[]
  private subId = `ferry-${Math.random().toString(36).slice(2, 10)}`
  private filters: NostrFilter[] = []
  private onEvent?: EventHandler
  private onStatus?: () => void
  /**
   * Ids accepted after verification by the consumer.
   *
   * Every relay that has an event sends it, so the same message arrives three
   * times. An id from a relay is only a claim until WASM verifies the event;
   * recording it before that would let a rejected copy suppress a valid one.
   */
  private seen = new Set<string>()
  private closed = false

  constructor(urls: string[]) {
    this.relays = urls.map((url) => ({url, status: 'closed', queue: []}))
  }

  /** Relay URLs and how each is doing, for display. */
  get status(): {url: string; status: RelayStatus}[] {
    return this.relays.map((r) => ({url: r.url, status: r.status}))
  }

  get connected(): number {
    return this.relays.filter((r) => r.status === 'open').length
  }

  /** Open every relay and subscribe. Safe to call once per pool. */
  open(filters: NostrFilter[], onEvent: EventHandler, onStatus?: () => void) {
    this.filters = filters
    this.onEvent = onEvent
    this.onStatus = onStatus
    for (const relay of this.relays) this.connect(relay)
  }

  /** Publish to every relay. One that accepts it is enough. */
  publish(event: NostrEvent) {
    this.send(JSON.stringify(['EVENT', event]))
  }

  close() {
    this.closed = true
    for (const relay of this.relays) {
      try {
        if (relay.sock && relay.status === 'open') {
          relay.sock.send(JSON.stringify(['CLOSE', this.subId]))
        }
        relay.sock?.close()
      } catch {
        // A socket that is already gone needs no closing.
      }
      relay.status = 'closed'
      relay.sock = undefined
    }
  }

  private send(payload: string) {
    for (const relay of this.relays) {
      if (relay.status === 'open' && relay.sock) {
        try {
          relay.sock.send(payload)
          continue
        } catch {
          // Fall through and queue it: the socket is on its way out and
          // reconnecting will flush what did not make it.
        }
      }
      // Bounded, because a pool left open against a relay that never comes back
      // would otherwise grow this array for the life of the page.
      if (relay.queue.length < 32) relay.queue.push(payload)
    }
  }

  private async deliver(event: NostrEvent) {
    const id = event.id
    if (this.closed || this.seen.has(id)) return
    try {
      await this.onEvent?.(event, () => {
        // Verification can overlap across relays. Claim the id synchronously
        // after it succeeds, so only one consumer applies the verified event.
        if (this.closed || this.seen.has(id)) return false
        this.seen.add(id)
        return true
      })
    } catch {
      // Consumers report actionable errors. A failed handler must not break
      // the connection or reserve an id it never accepted.
    }
  }

  private connect(relay: Relay) {
    if (this.closed) return
    relay.status = 'connecting'
    this.onStatus?.()

    let sock: WebSocket
    try {
      sock = new WebSocket(relay.url)
    } catch {
      relay.status = 'closed'
      this.onStatus?.()
      return
    }
    relay.sock = sock

    sock.onopen = () => {
      relay.status = 'open'
      this.onStatus?.()
      sock.send(JSON.stringify(['REQ', this.subId, ...this.filters]))
      for (const queued of relay.queue.splice(0)) sock.send(queued)
    }

    sock.onmessage = (ev: MessageEvent) => {
      if (typeof ev.data !== 'string') return
      let frame: unknown
      try {
        frame = JSON.parse(ev.data)
      } catch {
        return
      }
      if (!Array.isArray(frame) || frame[0] !== 'EVENT') return
      const event = frame[2] as NostrEvent | undefined
      // A relay is free to send anything. Only the shape is checked here —
      // whether it is genuine is the module's business, and it checks the
      // signature over a hash it recomputes rather than one this file passed on.
      if (typeof event?.id !== 'string' || !event.id || typeof event.content !== 'string') return
      void this.deliver(event)
    }

    const drop = () => {
      if (relay.status === 'closed') return
      relay.status = 'closed'
      this.onStatus?.()
      // Reconnect after a pause. A swap session is minutes long and a relay
      // that blinks should not end it, but a tight retry loop against a relay
      // that is refusing connections is a way to get an address blocked.
      if (!this.closed) setTimeout(() => this.connect(relay), 4000)
    }
    sock.onclose = drop
    sock.onerror = drop
  }
}

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

// Transport limits, not authenticity checks. A full board history fits in the
// queue; oversized/flooded feeds stop explicitly instead of exhausting the tab.
// Accepted IDs are never evicted while a pool is open: reaching the cap closes
// it, so old accepted messages cannot become acceptable again through eviction.
export const RELAY_LIMITS = Object.freeze({
  frameBytes: 256 * 1024,
  concurrentEvents: 4,
  queuedEvents: 2048,
  queuedBytes: 8 * 1024 * 1024,
  acceptedEvents: 10_000,
  relays: 16,
  outboundEvents: 32,
  outboundBytes: 2 * 1024 * 1024,
  connectMs: 20_000,
  handlerMs: 60_000,
})

const utf8 = new TextEncoder()
interface Delivery {
  event: NostrEvent
  /** Exact unverified copies may share work; differing copies of a claimed ID
   *  must still verify separately, so a forged copy cannot suppress a valid one. */
  key: string
  bytes: number
}

interface Relay {
  url: string
  sock?: WebSocket
  status: RelayStatus
  /** Events queued while the socket was still opening. */
  queue: string[]
  queueBytes: number
  reconnect?: ReturnType<typeof setTimeout>
  connectTimer?: ReturnType<typeof setTimeout>
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
  private onError?: (message: string) => void
  /**
   * Ids accepted after verification by the consumer.
   *
   * Every relay that has an event sends it, so the same message arrives three
   * times. An id from a relay is only a claim until WASM verifies the event;
   * recording it before that would let a rejected copy suppress a valid one.
   */
  private seen = new Set<string>()
  private closed = false
  private opened = false
  private lastError = ''
  private deliveries: Delivery[] = []
  private deliveryBytes = 0
  private pendingCopies = new Set<string>()
  private active = 0
  private handlerTimers = new Set<ReturnType<typeof setTimeout>>()

  constructor(urls: string[]) {
    if (urls.length > RELAY_LIMITS.relays) {
      throw new Error(`Use no more than ${RELAY_LIMITS.relays} relay URLs.`)
    }
    this.relays = urls.map((url) => ({url, status: 'closed', queue: [], queueBytes: 0}))
  }

  get error(): string {
    return this.lastError
  }

  /** Relay URLs and how each is doing, for display. */
  get status(): {url: string; status: RelayStatus}[] {
    return this.relays.map((r) => ({url: r.url, status: r.status}))
  }

  get connected(): number {
    return this.relays.filter((r) => r.status === 'open').length
  }

  /** Open every relay and subscribe. Safe to call once per pool. */
  open(
    filters: NostrFilter[],
    onEvent: EventHandler,
    onStatus?: () => void,
    onError?: (message: string) => void,
  ) {
    if (this.opened || this.closed) throw new Error('This relay pool is closed or already open.')
    this.opened = true
    this.filters = filters
    this.onEvent = onEvent
    this.onStatus = onStatus
    this.onError = onError
    for (const relay of this.relays) this.connect(relay)
  }

  /** Publish to every relay. One that accepts it is enough. */
  publish(event: NostrEvent) {
    if (this.closed)
      throw new Error(
        this.lastError || 'This relay connection is closed; reconnect before publishing.',
      )
    this.send(JSON.stringify(['EVENT', event]))
  }

  close() {
    if (this.closed) return
    this.closed = true
    this.deliveries = []
    this.deliveryBytes = 0
    this.pendingCopies.clear()
    this.seen.clear()
    for (const timer of this.handlerTimers) clearTimeout(timer)
    this.handlerTimers.clear()
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
      clearTimeout(relay.reconnect)
      clearTimeout(relay.connectTimer)
      relay.reconnect = undefined
      relay.connectTimer = undefined
      relay.queue = []
      relay.queueBytes = 0
      if (relay.sock) {
        relay.sock.onopen = null
        relay.sock.onmessage = null
        relay.sock.onclose = null
        relay.sock.onerror = null
      }
      relay.sock = undefined
    }
    this.onStatus?.()
  }

  private fail(message: string) {
    if (this.closed) return
    this.lastError = message
    this.close()
    this.onError?.(message)
  }

  private send(payload: string) {
    const bytes = utf8.encode(payload).byteLength
    if (bytes > RELAY_LIMITS.frameBytes) {
      const message =
        'Relay message is larger than 256 KiB. Shorten the message before reconnecting.'
      this.fail(message)
      throw new Error(message)
    }
    for (const relay of this.relays) {
      if (relay.status === 'open' && relay.sock) {
        if ((relay.sock.bufferedAmount || 0) + bytes > RELAY_LIMITS.outboundBytes) {
          const message =
            'A relay is not consuming outgoing messages. Reconnect with a responsive relay.'
          this.fail(message)
          throw new Error(message)
        }
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
      if (
        relay.queue.length >= RELAY_LIMITS.outboundEvents ||
        relay.queueBytes + bytes > RELAY_LIMITS.outboundBytes
      ) {
        const message =
          'Relay send queue is full. Check relay connectivity and reconnect before publishing again.'
        this.fail(message)
        throw new Error(message)
      }
      relay.queue.push(payload)
      relay.queueBytes += bytes
    }
  }

  private enqueue(event: NostrEvent, bytes: number) {
    if (this.closed || this.seen.has(event.id)) return
    let key: string
    try {
      key = JSON.stringify(event)
    } catch {
      return
    }
    if (this.pendingCopies.has(key)) return
    if (
      this.deliveries.length >= RELAY_LIMITS.queuedEvents ||
      this.deliveryBytes + bytes > RELAY_LIMITS.queuedBytes
    ) {
      this.fail(
        'Relay verification queue is full. Further deliveries stopped; reconnect using fewer or more reliable relays.',
      )
      return
    }
    this.pendingCopies.add(key)
    this.deliveries.push({event, key, bytes})
    this.deliveryBytes += bytes
    this.drain()
  }

  private drain() {
    while (!this.closed && this.active < RELAY_LIMITS.concurrentEvents && this.deliveries.length) {
      const delivery = this.deliveries.shift()
      if (!delivery) break
      this.deliveryBytes -= delivery.bytes
      if (this.seen.has(delivery.event.id)) {
        this.pendingCopies.delete(delivery.key)
        continue
      }
      this.active += 1
      void this.deliver(delivery)
    }
  }

  private async deliver({event, key}: Delivery) {
    const id = event.id
    const timer = setTimeout(() => {
      // A timed-out handler is not replaced with new work: its underlying
      // promise may still be running. Close the pool to retain the concurrency
      // bound and revoke all acceptance callbacks instead.
      this.fail(
        'Relay verification did not finish within 60 seconds. Check the signing engine and nodes, then reconnect.',
      )
    }, RELAY_LIMITS.handlerMs)
    this.handlerTimers.add(timer)
    try {
      await this.onEvent?.(event, () => {
        // Verification can overlap across relays. Claim the id synchronously
        // after it succeeds, so only one consumer applies the verified event.
        if (this.closed || this.seen.has(id)) return false
        if (this.seen.size >= RELAY_LIMITS.acceptedEvents) {
          this.fail(
            'This relay connection reached its 10,000 verified-event limit. Reconnect to start a new connection; no IDs were forgotten while it was open.',
          )
          return false
        }
        this.seen.add(id)
        return true
      })
    } catch {
      // Consumers report actionable errors. A failed handler must not break
      // the connection or reserve an id it never accepted.
    } finally {
      clearTimeout(timer)
      this.handlerTimers.delete(timer)
      this.pendingCopies.delete(key)
      this.active -= 1
      this.drain()
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
    relay.connectTimer = setTimeout(() => drop(), RELAY_LIMITS.connectMs)

    sock.onopen = () => {
      if (this.closed || relay.sock !== sock) return
      clearTimeout(relay.connectTimer)
      relay.connectTimer = undefined
      relay.status = 'open'
      this.onStatus?.()
      try {
        sock.send(JSON.stringify(['REQ', this.subId, ...this.filters]))
        while (relay.queue.length) {
          const queued = relay.queue[0]
          const bytes = utf8.encode(queued).byteLength
          if ((sock.bufferedAmount || 0) + bytes > RELAY_LIMITS.outboundBytes) {
            this.fail(
              'A relay is not consuming outgoing messages. Reconnect with a responsive relay.',
            )
            return
          }
          // Remove only after send accepts it. A reconnect must retain the
          // failed message and its byte count, rather than losing the tail.
          sock.send(queued)
          relay.queue.shift()
          relay.queueBytes -= bytes
        }
      } catch {
        drop()
      }
    }

    sock.onmessage = (ev: MessageEvent) => {
      if (this.closed || relay.sock !== sock) return
      if (typeof ev.data !== 'string') return
      // Count before JSON parsing. The string-length guard avoids allocating a
      // second copy of a huge frame just to measure its UTF-8 representation.
      if (
        ev.data.length > RELAY_LIMITS.frameBytes ||
        utf8.encode(ev.data).byteLength > RELAY_LIMITS.frameBytes
      ) {
        this.fail('A relay sent a message larger than 256 KiB. Reconnect using a different relay.')
        return
      }
      let frame: unknown
      try {
        frame = JSON.parse(ev.data)
      } catch {
        return
      }
      if (!Array.isArray(frame) || frame[0] !== 'EVENT' || frame[1] !== this.subId) return
      const event = frame[2] as NostrEvent | undefined
      // A relay is free to send anything. Only the shape is checked here —
      // whether it is genuine is the module's business, and it checks the
      // signature over a hash it recomputes rather than one this file passed on.
      if (
        typeof event?.id !== 'string' ||
        !/^[0-9a-f]{64}$/.test(event.id) ||
        typeof event.content !== 'string'
      )
        return
      this.enqueue(event, utf8.encode(ev.data).byteLength)
    }

    const drop = () => {
      if (this.closed || relay.sock !== sock || relay.status === 'closed') return
      clearTimeout(relay.connectTimer)
      relay.connectTimer = undefined
      sock.onopen = null
      sock.onmessage = null
      sock.onclose = null
      sock.onerror = null
      try {
        sock.close()
      } catch {
        /* already gone */
      }
      relay.sock = undefined
      relay.status = 'closed'
      this.onStatus?.()
      // Reconnect after a pause. A swap session is minutes long and a relay
      // that blinks should not end it, but a tight retry loop against a relay
      // that is refusing connections is a way to get an address blocked.
      if (!this.closed)
        relay.reconnect = setTimeout(() => {
          relay.reconnect = undefined
          this.connect(relay)
        }, 4000)
    }
    sock.onclose = drop
    sock.onerror = drop
  }
}

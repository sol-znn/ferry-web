// Offline delivery regressions for the real relay pool and its two consumers.
// Run with Node 24+: node --test scripts/relay-delivery.mjs
// Only the WASM API and unrelated application services are stubbed. Fixtures
// contain no keys or signed messages, and no network connection is opened.
import assert from 'node:assert/strict'
import {createRequire, registerHooks} from 'node:module'
import {dirname, resolve} from 'node:path'
import {after, test} from 'node:test'
import {fileURLToPath, pathToFileURL} from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const require = createRequire(resolve(root, 'ui/package.json'))
const {ref, watch} = await import(pathToFileURL(require.resolve('vue')).href)
const flush = () => new Promise((resolve) => setImmediate(resolve))
const deferred = () => {
  let resolve
  const promise = new Promise((done) => {
    resolve = done
  })
  return {promise, resolve}
}

const kinds = {post: 30001, take: 30002, presence: 30003, tag: 'fixture'}
const event = (id, kind = 30000) => ({
  id: id.toString(16).padStart(64, '0'),
  pubkey: '0'.repeat(64),
  created_at: Math.floor(Date.now() / 1000),
  kind,
  tags: [],
  content: `fixture ${id}`,
  sig: 'accepted fixture',
})
const rejected = (ev) => ({...ev, sig: 'rejected fixture'})
const check = async (ev) => {
  if (ev.sig !== 'accepted fixture') throw new Error('fixture rejected')
}
let verify = check

class Socket {
  static all = []
  sent = []
  readyState = 0

  constructor() {
    Socket.all.push(this)
    queueMicrotask(() => {
      if (this.readyState !== 0) return
      this.readyState = 1
      this.onopen?.()
    })
  }

  send(data) {
    this.sent.push(JSON.parse(data))
  }
  close() {
    this.readyState = 3
    this.onclose?.()
  }
  deliver(ev) {
    const subscription = this.sent.find((frame) => frame[0] === 'REQ')?.[1]
    assert.ok(subscription, 'the pool subscribed before delivery')
    this.onmessage?.({data: JSON.stringify(['EVENT', subscription, ev])})
  }
}

const saved = Object.fromEntries(
  ['WebSocket', 'localStorage', 'fetch'].map((key) => [
    key,
    Object.getOwnPropertyDescriptor(globalThis, key),
  ]),
)
const memory = new Map()
globalThis.WebSocket = Socket
globalThis.localStorage = {
  getItem: (key) => memory.get(key) ?? null,
  setItem: (key, value) => memory.set(key, String(value)),
}
globalThis.fetch = () => {
  throw new Error('network access is not part of this test')
}

const settings = {
  body: ref({network: 'regtest'}),
  relayList: () => ['wss://one.invalid', 'wss://two.invalid'],
}
const identity = {
  identity: ref({pubKey: '0'.repeat(64), kinds, presence: {beatSeconds: 60, staleSeconds: 180}}),
}
const api = {
  sessionNew: async () => ({
    code: 'fixture',
    display: 'fixture',
    roomId: 'fixture',
    pubKey: '0'.repeat(64),
    kind: 30000,
  }),
  sessionSend: async () => ({event: event(0)}),
  chainId: async () => ({mine: {}}),
  sessionOpen: async (_code, ev) => {
    await verify(ev)
    return {message: {type: 'note', note: ev.content}}
  },
  boardMine: async () => ({posts: []}),
  boardRead: async (ev) => {
    await verify(ev)
    return {
      listing: {
        eventId: ev.id,
        author: ev.pubkey,
        publishedAt: ev.created_at,
        post: {id: ev.id, network: 'regtest', expiresAt: ev.created_at + 600, status: 'open'},
        mine: false,
        expired: false,
      },
    }
  },
  boardReadPresence: async (ev) => {
    await verify(ev)
    return {seen: {author: ev.pubkey, seenAt: ev.created_at}}
  },
  boardReadTake: async (ev) => {
    await verify(ev)
    return {take: {eventId: ev.id, postId: 'fixture'}}
  },
}

// Resolve the app's aliases without bundling or rewriting its source. Vue and
// the relay/session/board modules are loaded as shipped; these service stubs
// make their receive paths usable without a wallet, node, or browser profile.
globalThis.__ferryRelayFixtures = {api, settings, identity, ferry: {swaps: ref([])}}
const stub = (source) => `data:text/javascript,${encodeURIComponent(source)}`
const modules = {
  '@/core/api': 'export const api = globalThis.__ferryRelayFixtures.api',
  '@/core/env': 'export const isDev = true',
  '@/core/composables/useSettings':
    'export const useSettings = () => globalThis.__ferryRelayFixtures.settings',
  '@/core/composables/useBoardIdentity':
    'export const useBoardIdentity = () => globalThis.__ferryRelayFixtures.identity',
  '@/core/composables/useFerry':
    'export const useFerry = () => globalThis.__ferryRelayFixtures.ferry',
  '@/core/composables/useAutoRefresh': 'export const syncNow = async () => {}',
}
const hooks = registerHooks({
  resolve(specifier, context, nextResolve) {
    if (modules[specifier]) return {url: stub(modules[specifier]), shortCircuit: true}
    if (specifier.startsWith('@/')) {
      return nextResolve(
        pathToFileURL(resolve(root, 'ui/src', `${specifier.slice(2)}.ts`)).href,
        context,
      )
    }
    return nextResolve(specifier, context)
  },
})
const {useSession} = await import(
  pathToFileURL(resolve(root, 'ui/src/core/composables/useSession.ts')).href
)
const {useBoard} = await import(
  pathToFileURL(resolve(root, 'ui/src/core/composables/useBoard.ts')).href
)
const {RelayPool, RELAY_LIMITS} = await import(
  pathToFileURL(resolve(root, 'ui/src/core/nostr.ts')).href,
)
const session = useSession()
const board = useBoard()

after(async () => {
  board.stop()
  session.leave()
  await flush()
  hooks.deregister()
  for (const [key, descriptor] of Object.entries(saved)) {
    if (descriptor) Object.defineProperty(globalThis, key, descriptor)
    else delete globalThis[key]
  }
  delete globalThis.__ferryRelayFixtures
})

async function startSession(t) {
  verify = check
  const before = Socket.all.length
  await session.join()
  await flush()
  assert.equal(session.error.value, '')
  t.after(async () => {
    session.leave()
    await flush()
  })
  return Socket.all.slice(before)
}

async function startBoard(t) {
  verify = check
  const before = Socket.all.length
  await board.start()
  await flush()
  assert.equal(board.error.value, '')
  t.after(() => board.stop())
  return Socket.all.slice(before)
}

const messages = (ev) => session.lines.value.filter((line) => line.text === ev.content)

test('a rejected session delivery does not consume a later accepted copy', async (t) => {
  const sockets = await startSession(t)
  const ev = event(1)
  sockets[0].deliver(rejected(ev))
  await flush()
  sockets[1].deliver(ev)
  await flush()
  assert.equal(messages(ev).length, 1)
})

test('an accepted copy can complete while another copy is still being checked', async (t) => {
  const sockets = await startSession(t)
  const pending = deferred()
  const ev = event(2)
  verify = async (incoming) => {
    if (incoming.sig !== ev.sig) await pending.promise
    await check(incoming)
  }
  sockets[0].deliver(rejected(ev))
  sockets[1].deliver(ev)
  await flush()
  const count = messages(ev).length
  pending.resolve()
  await flush()
  assert.equal(count, 1)
  assert.equal(messages(ev).length, 1)
})

test('concurrent accepted copies apply a session message once', async (t) => {
  const sockets = await startSession(t)
  const first = deferred()
  const second = deferred()
  const ev = event(3)
  let calls = 0
  verify = () => (++calls === 1 ? first.promise : second.promise)
  sockets[0].deliver(ev)
  sockets[1].deliver(ev)
  second.resolve()
  await flush()
  first.resolve()
  await flush()
  const verified = calls
  sockets[0].deliver(ev)
  await flush()
  assert.equal(messages(ev).length, 1)
  assert.equal(calls, verified, 'later replays need no additional verification')
})

test('a pending message cannot enter a replacement session', async (t) => {
  const sockets = await startSession(t)
  const pending = deferred()
  const goodbye = deferred()
  const send = api.sessionSend
  const ev = event(4)
  verify = () => pending.promise
  sockets[0].deliver(ev)
  api.sessionSend = async (_code, message) => {
    if (message.type === 'bye') await goodbye.promise
    return {event: event(0)}
  }
  t.after(() => {
    api.sessionSend = send
  })
  session.leave()
  await session.join()
  pending.resolve()
  await flush()
  const count = messages(ev).length
  goodbye.resolve()
  await flush()
  assert.equal(count, 0)
})

test('a retired socket cannot deliver into the current session', async (t) => {
  const sockets = await startSession(t)
  const goodbye = deferred()
  const send = api.sessionSend
  api.sessionSend = async (_code, message) => {
    if (message.type === 'bye') await goodbye.promise
    return {event: event(0)}
  }
  t.after(() => {
    api.sessionSend = send
  })
  session.leave()
  await session.join()
  await flush()
  const ev = event(5)
  sockets[0].deliver(ev)
  Socket.all.at(-1).deliver(ev)
  await flush()
  const count = messages(ev).length
  goodbye.resolve()
  await flush()
  assert.equal(count, 1)
  assert.equal(session.connected.value, 2, 'retired pool close cannot overwrite current relay status')
})

for (const [name, kind, count] of [
  ['post', kinds.post, () => board.listings.value.length],
  ['presence', kinds.presence, () => Object.keys(board.seenAt.value).length],
  ['take', kinds.take, () => board.takes.value.length],
]) {
  test(`a rejected board ${name} does not consume a later accepted copy`, async (t) => {
    const sockets = await startBoard(t)
    const ev = event(kind, kind)
    sockets[0].deliver(rejected(ev))
    await flush()
    assert.equal(count(), 0)
    sockets[1].deliver(ev)
    await flush()
    assert.equal(count(), 1)
  })
}

test('oversized relay frames are refused before JSON parsing and surface in the session', async (t) => {
  const sockets = await startSession(t)
  const parse = JSON.parse
  let parsed = false
  const oversized = ' '.repeat(RELAY_LIMITS.frameBytes + 1)
  JSON.parse = (text, ...args) => {
    if (text === oversized) parsed = true
    return parse(text, ...args)
  }
  try { sockets[0].onmessage?.({data: oversized}) } finally { JSON.parse = parse }
  assert.equal(parsed, false)
  assert.match(session.error.value, /larger than 256 KiB/)
  assert.equal(session.connected.value, 0)
})

async function rawPool(t, handler) {
  const before = Socket.all.length
  const pool = new RelayPool(['wss://fixture.invalid'])
  const errors = []
  pool.open([{kinds: [30000]}], handler, undefined, (message) => errors.push(message))
  await flush()
  const socket = Socket.all[before]
  t.after(() => pool.close())
  return {pool, socket, errors}
}

test('verification concurrency is bounded and exact pending copies share work', async (t) => {
  const pending = deferred()
  let active = 0
  let peak = 0
  let calls = 0
  let applied = 0
  const {socket} = await rawPool(t, async (_ev, accept) => {
    calls++
    peak = Math.max(peak, ++active)
    await pending.promise
    if (accept()) applied++
    active--
  })
  for (let id = 1; id <= 20; id++) {
    socket.deliver(event(id))
    socket.deliver(event(id))
  }
  assert.equal(calls, RELAY_LIMITS.concurrentEvents)
  pending.resolve()
  await flush()
  assert.equal(peak, RELAY_LIMITS.concurrentEvents)
  assert.equal(calls, 20)
  assert.equal(applied, 20)
})

test('a verification flood closes the pool without accepting pending events', async (t) => {
  const pending = deferred()
  let applied = 0
  let calls = 0
  const {pool, socket, errors} = await rawPool(t, async (_ev, accept) => {
    calls++
    await pending.promise
    if (accept()) applied++
  })
  for (let id = 1; id <= RELAY_LIMITS.queuedEvents + RELAY_LIMITS.concurrentEvents + 1; id++) {
    socket.deliver(event(id))
  }
  assert.equal(calls, RELAY_LIMITS.concurrentEvents)
  assert.equal(pool.connected, 0)
  assert.match(errors[0], /verification queue is full/)
  pending.resolve()
  await flush()
  assert.equal(applied, 0)
  assert.equal(calls, RELAY_LIMITS.concurrentEvents)
})

test('queue byte capacity is enforced before the event-count capacity', async (t) => {
  const pending = deferred()
  let applied = 0
  const {pool, socket, errors} = await rawPool(t, async (_ev, accept) => {
    await pending.promise
    if (accept()) applied++
  })
  for (let id = 1; id <= 100; id++) {
    socket.deliver({...event(id), content: 'x'.repeat(200 * 1024)})
  }
  assert.equal(pool.connected, 0)
  assert.match(errors[0], /verification queue is full/)
  pending.resolve()
  await flush()
  assert.equal(applied, 0)
})

test('UTF-8 frame size is bounded even when the string character count is smaller', async (t) => {
  const {pool, socket, errors} = await rawPool(t, () => assert.fail('oversized Unicode frame must not verify'))
  socket.deliver({...event(1), content: '\u20ac'.repeat(100_000)})
  assert.equal(pool.connected, 0)
  assert.match(errors[0], /larger than 256 KiB/)
})

test('relay count and offline outgoing queue have explicit bounds', async (t) => {
  assert.throws(() => new RelayPool(Array(RELAY_LIMITS.relays + 1).fill('wss://fixture.invalid')), /no more than/)
  const pool = new RelayPool(['wss://fixture.invalid'])
  t.after(() => pool.close())
  for (let id = 1; id <= RELAY_LIMITS.outboundEvents; id++) pool.publish(event(id))
  assert.throws(() => pool.publish(event(100)), /send queue is full/)
  assert.throws(() => pool.publish(event(1)), /send queue is full/)
})

test('a failed opening flush preserves unsent messages for the next connection', async (t) => {
  t.mock.timers.enable({apis: ['setTimeout']})
  const before = Socket.all.length
  const pool = new RelayPool(['wss://fixture.invalid'])
  t.after(() => pool.close())
  pool.publish(event(1))
  pool.publish(event(2))
  pool.open([{kinds: [30000]}], () => {})
  const first = Socket.all[before]
  const send = first.send.bind(first)
  first.send = (payload) => {
    if (JSON.parse(payload)[0] === 'EVENT') throw new Error('synthetic send failure')
    send(payload)
  }
  await flush()
  assert.equal(pool.connected, 0)
  t.mock.timers.tick(4000)
  await flush()
  const next = Socket.all[before + 1]
  assert.deepEqual(next.sent.filter((frame) => frame[0] === 'EVENT').map((frame) => frame[1].id),
    [event(1).id, event(2).id])
  assert.equal(pool.connected, 1)
})

test('accepted-ID capacity closes instead of forgetting IDs and allowing replay', async (t) => {
  let applied = 0
  const {pool, socket, errors} = await rawPool(t, (_ev, accept) => { if (accept()) applied++ })
  for (let id = 1; id <= RELAY_LIMITS.acceptedEvents; id++) {
    socket.deliver(event(id))
    if (id % 100 === 0) await flush()
  }
  await flush()
  assert.equal(applied, RELAY_LIMITS.acceptedEvents)
  socket.deliver(event(1))
  await flush()
  assert.equal(applied, RELAY_LIMITS.acceptedEvents)
  assert.equal(errors.length, 0)
  socket.deliver(event(RELAY_LIMITS.acceptedEvents + 1))
  await flush()
  assert.equal(pool.connected, 0)
  assert.match(errors[0], /verified-event limit/)
  socket.deliver(event(1))
  await flush()
  assert.equal(applied, RELAY_LIMITS.acceptedEvents)
})

test('close discards queued verification and cancels reconnects', async (t) => {
  t.mock.timers.enable({apis: ['setTimeout']})
  const pending = deferred()
  let calls = 0
  let applied = 0
  const {pool, socket} = await rawPool(t, async (_ev, accept) => {
    calls++
    await pending.promise
    if (accept()) applied++
  })
  for (let id = 1; id <= 20; id++) socket.deliver(event(id))
  socket.onclose?.()
  const count = Socket.all.length
  pool.close()
  t.mock.timers.tick(10_000)
  pending.resolve()
  await flush()
  assert.equal(Socket.all.length, count)
  assert.equal(calls, RELAY_LIMITS.concurrentEvents)
  assert.equal(applied, 0)
})

test('a handler that never settles closes on deadline without starting more handlers', async (t) => {
  t.mock.timers.enable({apis: ['setTimeout']})
  const pending = deferred()
  let calls = 0
  let applied = 0
  const {pool, socket, errors} = await rawPool(t, async (_ev, accept) => {
    calls++
    await pending.promise
    if (accept()) applied++
  })
  for (let id = 1; id <= 10; id++) socket.deliver(event(id))
  t.mock.timers.tick(RELAY_LIMITS.handlerMs)
  assert.match(errors[0], /did not finish within 60 seconds/)
  assert.equal(pool.connected, 0)
  assert.equal(calls, RELAY_LIMITS.concurrentEvents)
  pending.resolve()
  await flush()
  assert.equal(applied, 0)
})

test('concurrent accepted takes update the inbox once', async (t) => {
  const sockets = await startBoard(t)
  const pending = deferred()
  const ev = event(8, kinds.take)
  verify = () => pending.promise
  let updates = 0
  const stopWatch = watch(board.takes, () => updates++, {flush: 'sync'})
  t.after(stopWatch)
  sockets[0].deliver(ev)
  sockets[1].deliver(ev)
  pending.resolve()
  await flush()
  assert.equal(updates, 1)
  assert.equal(board.takes.value.length, 1)
  board.dismiss(ev.id)
  sockets[0].deliver(ev)
  await flush()
  assert.equal(board.takes.value.length, 0)
})

for (const [name, kind, count] of [
  ['post', kinds.post, () => board.listings.value.length],
  ['presence', kinds.presence, () => Object.keys(board.seenAt.value).length],
  ['take', kinds.take, () => board.takes.value.length],
]) {
  test(`a pending ${name} cannot enter a restarted board`, async (t) => {
    const sockets = await startBoard(t)
    const pending = deferred()
    verify = () => pending.promise
    sockets[0].deliver(event(9 + kind, kind))
    board.stop()
    await board.start()
    pending.resolve()
    await flush()
    assert.equal(count(), 0)
  })
}

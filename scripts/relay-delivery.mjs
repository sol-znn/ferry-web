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

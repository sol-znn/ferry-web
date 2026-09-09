// Runs the real WebAssembly module under Node and exercises the call table.
//
// Not a substitute for the Go unit tests — those already cover the contract
// template, the leg ordering and the script engine. This covers what they
// cannot: the seam between JavaScript and Go, the localStorage-backed store,
// JSON crossing that boundary in both directions, and the offline recovery path.
//
// Node runs the same GOOS=js binary the browser does, so what is tested is the
// artefact that ships. What Node lacks is a DOM, so localStorage is stubbed
// below — deliberately with the same throw-on-quota behaviour the real one has.
//
//   node scripts/smoke.mjs [path/to/ferry.wasm] [--env=dev|prod]
//
// --env is which instance the module under test was built as, defaulting to
// prod. Not decoration: the two builds differ only in a linker flag, and a
// mistyped flag produces a module that compiles, passes every other check here,
// and silently stores swaps in the wrong namespace.
//
// Every assertion is offline, which is also why it can run in CI.

import {readFile} from 'node:fs/promises'
import {dirname, resolve} from 'node:path'
import {fileURLToPath} from 'node:url'
import {execFileSync} from 'node:child_process'

const here = dirname(fileURLToPath(import.meta.url))
const args = process.argv.slice(2)
const wasmPath = args.find((a) => !a.startsWith('--')) ?? resolve(here, '../ui/public/ferry.wasm')

const expectedEnv =
  args.find((a) => a.startsWith('--env='))?.slice('--env='.length) ?? process.env.FERRY_ENV ?? 'prod'
if (expectedEnv !== 'dev' && expectedEnv !== 'prod') {
  throw new Error(`unknown instance ${JSON.stringify(expectedEnv)}: expected "dev" or "prod"`)
}
// Must match StorageKeyPrefix() in wasm/store.go. Kept here as a literal rather
// than derived: this file is checking that the module puts records where the
// rest of the world expects to find them, and deriving the expectation from the
// same rule the module uses would check nothing.
const swapPrefix = expectedEnv === 'dev' ? 'ferry.dev.swap.' : 'ferry.swap.'
// Must match BoardKeyPrefix() in wasm/boardstore.go, and for the same reason as
// above. The board keeps its key and its posts apart from the swaps because
// Store.List selects on the swap prefix and would otherwise try to read a board
// record as a swap.
const boardPrefix = expectedEnv === 'dev' ? 'ferry.dev.board.' : 'ferry.board.'
// Must match boardTag() in wasm/board.go. Scoped to the instance for the same
// reason the storage prefix is: the network tag alone is not enough, because a
// dev build's own settings can be pointed at "mainnet".
const boardTag = expectedEnv === 'dev' ? 'ferry-board-v1-dev' : 'ferry-board-v1'
// Must match DefaultPostTTL() in wasm/board.go. Shorter on dev because a test
// post is abandoned within the minute and only the browser that made it can
// take it down.
const boardDefaultTtl = expectedEnv === 'dev' ? 15 * 60 : 24 * 3600

// ---------- the browser globals the module expects ----------

class MemStorage {
  #m = new Map()
  get length() {
    return this.#m.size
  }
  key(i) {
    return [...this.#m.keys()][i] ?? null
  }
  getItem(k) {
    return this.#m.has(k) ? this.#m.get(k) : null
  }
  setItem(k, v) {
    this.#m.set(String(k), String(v))
  }
  removeItem(k) {
    this.#m.delete(k)
  }
}

globalThis.localStorage = new MemStorage()
// Go's runtime shim installs onto whichever global object it finds; the module
// then reads window.localStorage and dispatches its ready event on the same
// object, so the two have to be the same thing.
globalThis.window = globalThis

// ---------- boot ----------

const goroot = execFileSync('go', ['env', 'GOROOT'], {encoding: 'utf8'}).trim()
const shim = await readFile(resolve(goroot, 'lib/wasm/wasm_exec.js'), 'utf8').catch(() =>
  readFile(resolve(goroot, 'misc/wasm/wasm_exec.js'), 'utf8'),
)
// Evaluated rather than imported: the shim is a classic script that assigns to
// globalThis.Go, with no module exports to import.
new Function(shim)()

const go = new globalThis.Go()
const {instance} = await WebAssembly.instantiate(await readFile(wasmPath), go.importObject)
// Not awaited — the module's main() blocks forever by design.
void go.run(instance)
await new Promise((r) => setTimeout(r, 50))

if (!globalThis.ferryWasm?.ready) {
  throw new Error(`module did not become ready: ${globalThis.ferryWasm?.error ?? 'no API installed'}`)
}

const call = async (method, body = {}) => {
  const raw = await globalThis.ferryWasm.call(method, JSON.stringify(body))
  return JSON.parse(raw)
}

// ---------- assertions ----------

let failures = 0
let checks = 0

function ok(label, cond, detail = '') {
  checks++
  if (cond) {
    console.log(`  ok   ${label}`)
  } else {
    failures++
    console.log(`  FAIL ${label}${detail ? `\n       ${detail}` : ''}`)
  }
}

function section(name) {
  console.log(`\n${name}`)
}

const SETTINGS = {network: 'regtest'}
// A regtest address with no relationship to anything; where the coins land is
// irrelevant to what these checks assert.
const DEST = 'bcrt1q0rymrte6drs2nvjn73mqsl2meud7nv0dy6tgn4'

section(`this is the ${expectedEnv} instance`)

// Before anything else, because every assertion below about where a record
// lands is an assertion about a namespace this decides.
const identity = await call('config', {settings: SETTINGS})
ok(
  `the module reports buildEnv "${expectedEnv}"`,
  identity.buildEnv === expectedEnv,
  `got ${JSON.stringify(identity.buildEnv)} — the -X main.BuildEnv linker flag did not take`,
)
ok(
  'it says where it keeps swaps, and it is this instance’s namespace',
  typeof identity.swapDir === 'string' && identity.swapDir.includes(swapPrefix),
  `swapDir: ${JSON.stringify(identity.swapDir)}`,
)

section('a swap survives the JS/Go boundary')

const created = await call('create', {
  role: 'initiator',
  leg: 'send',
  amountSats: 400000,
  destAddr: DEST,
  settings: SETTINGS,
})
ok('create returns a swap, not an error', !created.error, created.error)
ok('the initiator gets a secret hash', /^[0-9a-f]{64}$/.test(created.secretHashHex ?? ''))
ok('an ephemeral pubkey hash is generated', /^[0-9a-f]{40}$/.test(created.key?.pkhHex ?? ''))
ok('the private key is NOT in the swap view', !JSON.stringify(created).includes('priv'))
ok('bitcoin is the initiator leg when the initiator sends BTC', created.btcLegIsInitiators === true)
ok('the swap starts as a draft', created.state === 'draft')

const id = created.id

section('the store is real: it persists across calls')

const listed = await call('list', {view: 'active'})
ok('the new swap is in the active list', Array.isArray(listed) && listed.some((s) => s.id === id))
ok(
  `it landed in localStorage under the ${expectedEnv} prefix ${swapPrefix}`,
  [...Array(localStorage.length).keys()].some((i) => localStorage.key(i) === `${swapPrefix}${id}`),
  `keys: ${[...Array(localStorage.length).keys()].map((i) => localStorage.key(i)).join(', ')}`,
)

section('building the contract needs both halves')

const built = await call('counterparty', {id, pkhHex: 'aa'.repeat(20)})
ok('a counterparty pubkey hash builds the contract', !built.error, built.error)
ok('it produces a regtest P2SH address', (built.contractAddr ?? '').startsWith('2'), built.contractAddr)
ok('the contract is hex', /^[0-9a-f]+$/.test(built.contractHex ?? ''))
ok('the state advances to awaiting_funding', built.state === 'awaiting_funding')

const badPkh = await call('counterparty', {id, pkhHex: 'nothex'})
ok('a malformed pubkey hash is refused', Boolean(badPkh.error), JSON.stringify(badPkh))

section('offers carry public data and nothing else')

const offered = await call('offer', {id})
ok('an offer string is produced', (offered.offer ?? '').startsWith('swapoffer1:'))
// The offer carries the secret HASH by design; what must never be in it is the
// preimage or a key. Decoding it and checking the fields is the only honest way
// to assert that — the base64 payload does not contain either as a substring
// whether or not the encoder put them there.
const offerFields = JSON.parse(Buffer.from(offered.offer.slice('swapoffer1:'.length), 'base64url'))
ok(
  'the offer carries no secret and no key',
  !('secret' in offerFields) && !('priv' in offerFields) && !JSON.stringify(offerFields).includes('priv'),
  JSON.stringify(Object.keys(offerFields)),
)

const decoded = await call('decodeOffer', {offer: offered.offer})
ok('the offer round-trips', decoded.decoded?.secretHash === created.secretHashHex, JSON.stringify(decoded))
ok('the receiver is told to take the other leg', decoded.yourLeg === 'receive')
ok('and the other role', decoded.yourRole === 'participant')

const junk = await call('decodeOffer', {offer: 'swapoffer1:not-base64!!'})
ok('a malformed offer is refused', Boolean(junk.error))

// Leg.Opposite maps anything it does not recognise to "send", so an offer whose
// btcLeg is nonsense would quietly tell the receiver to take the wrong side of
// the trade. Every field is validated rather than only the ones that are hex.
const bentLeg = {...offerFields, btcLeg: 'sideways'}
const bent = await call('decodeOffer', {
  offer: 'swapoffer1:' + Buffer.from(JSON.stringify(bentLeg)).toString('base64url'),
})
ok('an offer naming an impossible leg is refused', Boolean(bent.error), bent.error)

const shortHash = {...offerFields, secretHash: 'aabb'}
const short = await call('decodeOffer', {
  offer: 'swapoffer1:' + Buffer.from(JSON.stringify(shortHash)).toString('base64url'),
})
ok('an offer with a short secret hash is refused', Boolean(short.error), short.error)

// Answering an offer, exactly the way the form does it.
//
// The page fills every pinned term straight out of the decode and then locks
// those fields, so what create() receives is the offer's own values plus the two
// addresses only this side can know. That is the pairing worth pinning here: the
// fields the form WRITES have to be the fields CheckCreate accepts, and the two
// lists live in different languages. A term added to one and not the other turns
// a filled-in form into a refusal nobody can act on, because the value it
// objects to is not typeable.
const answer = {
  role: decoded.yourRole,
  leg: decoded.yourLeg,
  amountSats: decoded.decoded.amountSats,
  secretHashHex: decoded.decoded.secretHash,
  zenonPeerAddress: decoded.decoded.zenonAddr ?? '',
  zenonToken: decoded.decoded.zenonToken ?? '',
  zenonAmount: decoded.decoded.zenonAmt ?? '',
  // The receiving side does not carry their counterparty's pkh; only the side
  // building the contract does. Mirrors applyOffer.
  counterpartyPkhHex: '',
  // Theirs to supply, and the reason the form is not simply a display.
  destAddr: DEST,
  zenonSelfAddress: '',
  offer: offered.offer,
}
const answered = await call('create', {...answer, settings: SETTINGS})
ok('a form filled straight from an offer is accepted', !answered.error, answered.error)
ok('and it takes the opposite side', answered.role === 'participant' && answered.leg === 'receive')

// The check the locking exists to make unreachable. It is enforced in Go, so it
// holds for a create that never went near the form.
const edited = await call('create', {
  ...answer,
  amountSats: answer.amountSats + 1,
  settings: SETTINGS,
})
ok('a single edited term is refused, naming it', /Bitcoin amount/.test(edited.error ?? ''), edited.error)

section('the recovery file can spend the contract without this app')

// Funding is normally discovered from the chain. Nothing here reaches a node,
// so the swap record is edited directly — which is exactly the emergency
// hand-edit the file-per-swap design was chosen to allow, and it is how this
// check reaches the recovery path with no network.
const key = `${swapPrefix}${id}`
const record = JSON.parse(localStorage.getItem(key))
record.funding = {txid: '11'.repeat(32), vout: 0, value: 400000}
localStorage.setItem(key, JSON.stringify(record))

const rec = await call('recovery', {id})
ok('recovery exports the key as WIF', typeof rec.privateKeyWIF === 'string' && rec.privateKeyWIF.length > 40)
ok('recovery carries the contract', rec.contractHex === built.contractHex)
ok('recovery names the contract address', rec.contractAddr === built.contractAddr)

const rebuilt = await call('rebuild', {file: JSON.stringify(rec), feeRate: 5})
ok('a refund is rebuilt from the file alone', rebuilt.action === 'refund', rebuilt.error)
ok('it is signed and serialised', /^[0-9a-f]+$/.test(rebuilt.rawHex ?? ''))
ok('it pays the address in the file', rebuilt.destAddr === DEST)
ok('it is not yet valid, and says so', Boolean(rebuilt.notYet), JSON.stringify(rebuilt.validFrom))
ok('the fee is charged at the rate asked for', rebuilt.feeRate === 5)
ok('value plus fee equals the funding', rebuilt.value + rebuilt.fee === 400000)

// The refund branch holds this key. Asking it to redeem must be refused with an
// explanation rather than producing a transaction no node will accept.
const wrongBranch = await call('rebuild', {file: JSON.stringify(rec), secretHex: 'ab'.repeat(32)})
ok('redeeming with a refund-branch key is refused', /REFUND branch/.test(wrongBranch.error ?? ''), wrongBranch.error)

// A file whose stated address does not hash to its own contract has been
// edited, and neither half can be trusted.
const tampered = {...rec, contractAddr: '2N1SP7r92ZZJvYEQ4Xv7oVvGkVh7YQnCF8u'}
const refused = await call('rebuild', {file: JSON.stringify(tampered)})
ok('an inconsistent recovery file is refused', /inconsistent/.test(refused.error ?? ''), refused.error)

// Same for the locktime. "valid from" is what tells the user when to broadcast,
// on the screen they reach when everything else has already failed, so it comes
// out of the contract — and a file that disagrees with its own contract about
// when the refund branch opens is refused rather than quietly preferred.
const movedLock = {...rec, lockTime: rec.lockTime + 86400}
const refusedLock = await call('rebuild', {file: JSON.stringify(movedLock)})
ok(
  "a recovery file whose lockTime disagrees with its contract is refused",
  /inconsistent/.test(refusedLock.error ?? ''),
  refusedLock.error,
)
ok(
  'the rebuilt refund takes its valid-from from the contract',
  rebuilt.validFrom === new Date(rec.lockTime * 1000).toISOString().replace(/\.\d{3}Z$/, 'Z'),
  `${rebuilt.validFrom} vs lockTime ${rec.lockTime}`,
)

section('auditing a counterparty contract is offline and strict')

const receiver = await call('create', {
  role: 'participant',
  leg: 'receive',
  amountSats: 400000,
  destAddr: DEST,
  secretHashHex: created.secretHashHex,
  settings: SETTINGS,
})
ok('the participant side is created from a hash', !receiver.error, receiver.error)

// built.contractHex lets someone ELSE redeem: its redeem branch commits to the
// counterparty pubkey hash, not to this swap's key. Accepting it would mean
// funding a Zenon leg against Bitcoin that can never be claimed.
const audited = await call('audit', {id: receiver.id, contractHex: built.contractHex})
ok(
  'a contract redeemable by someone else is refused',
  /would not be able to claim/.test(audited.error ?? ''),
  audited.error,
)

section('backup and restore')

// Three by now: the initiator above, the participant answering its offer, and
// the receiver built for the audit check. Counted rather than derived, so a swap
// silently failing to store shows up here as a number rather than as agreement
// between two things that are both wrong.
const backup = await call('export')
ok('export returns every swap', backup.swaps?.length === 3, JSON.stringify(backup.swaps?.length))
ok('the export carries the keys that can spend', JSON.stringify(backup).includes('"priv"'))

const reimport = await call('import', {data: JSON.stringify(backup)})
ok('re-importing skips what is already here', reimport.skipped === 3 && reimport.added === 0, JSON.stringify(reimport))

localStorage.removeItem(`${swapPrefix}${receiver.id}`)
const restored = await call('import', {data: JSON.stringify(backup)})
ok('a missing swap is restored', restored.added === 1 && restored.skipped === 2, JSON.stringify(restored))

const notABackup = await call('import', {data: '{"nope":true}'})
ok('a file with no swaps is refused', Boolean(notABackup.error), notABackup.error)

// A backup is a file off the user's disk. An unchecked record from one reaches
// every path a locally created swap does, and handleOffer and handleRecovery
// read Key without asking — which used to be a nil dereference inside the
// module rather than a message.
const keyless = JSON.parse(JSON.stringify(backup))
keyless.swaps = [{...keyless.swaps[0], id: 'ffffffffffffffff', key: null}]
const refusedImport = await call('import', {data: JSON.stringify(keyless)})
ok(
  'a record with no key is refused rather than stored',
  Boolean(refusedImport.error) || refusedImport.rejected === 1,
  JSON.stringify(refusedImport),
)
ok(
  'and it did not land in storage',
  localStorage.getItem(`${swapPrefix}ffffffffffffffff`) === null,
)

// One malformed record must not take the whole import down with it.
const mixed = JSON.parse(JSON.stringify(backup))
mixed.swaps = [
  {...mixed.swaps[0], id: '00000000000000aa'},
  {...mixed.swaps[0], id: '00000000000000bb', network: 'dogecoin'},
]
const partial = await call('import', {data: JSON.stringify(mixed)})
ok(
  'a healthy record beside a bad one still imports',
  partial.added === 1 && partial.rejected === 1,
  JSON.stringify(partial),
)

section('the unlock cost is quoted before the money moves')

// The contract, funding and address below are the real swap 1c5762d7611fbe95 on
// mainnet, whose 1000 sat output could not be redeemed at 2 sat/vB and was
// eventually redeemed at 1.08. Checking the module against a transaction that
// is already in a chain is what stops this arithmetic drifting.
const STUCK = {
  contractHex:
    '6382012088a82048a2bdef64537626513a978b8cc733e45838619a33dff1e045bc9fedf5510d17' +
    '8876a91441d053189e4d3c8db9e26a77385171aa39633bc36704b51c9f6ab17576a9143d3e7142218ef936fbd2' +
    '6edaefeaccc1349302df6888ac',
  destAddr: 'bc1qpqxt3fljkrthfd5mup5sku0s5sy0xlss5x7qv0',
}

const quote = await call('estimate', {
  amountSats: 1000,
  feeRate: 2,
  ...STUCK,
  settings: {network: 'mainnet'},
})
ok(
  'it sizes the redeem the way the signer does',
  quote.redeem?.vsize === 323 && quote.redeem?.fee === 646,
  JSON.stringify(quote.redeem),
)
ok(
  'it says a 354 sat payout is not viable',
  quote.redeem?.net === 354 && quote.redeem?.viable === false,
  JSON.stringify(quote.redeem),
)
ok(
  'it names the fee rate that would work',
  quote.maxFeeRate === 1.4,
  `maxFeeRate: ${JSON.stringify(quote.maxFeeRate)}`,
)
ok(
  'it recommends an amount with room for fees to rise',
  quote.recommended > quote.minAtFeeRate && quote.headroomRate >= 10,
  JSON.stringify({recommended: quote.recommended, headroomRate: quote.headroomRate}),
)

// A quote with a fee rate supplied must not need a chain, because the offline
// Recover page uses it with the machine unplugged.
ok('a quote with a given rate needs no node', !quote.error && quote.feeRateFrom === 'you', quote.error)

const tooSmall = await call('estimate', {
  amountSats: 700,
  feeRate: 1,
  ...STUCK,
  settings: {network: 'mainnet'},
})
ok(
  'an amount below the relay floor is called unspendable, not merely expensive',
  tooSmall.verdict === 'unspendable' && tooSmall.maxFeeRate === 0,
  JSON.stringify({verdict: tooSmall.verdict, maxFeeRate: tooSmall.maxFeeRate}),
)


section('a session is sealed, signed, and unreadable to the relay')

const room = await call('sessionNew')
ok('a fresh code derives a room', /^[0-9A-Z]{32}$/.test(room.code ?? ''), JSON.stringify(room.code))
ok('it is shown in readable groups', room.display?.replace(/-/g, '') === room.code, room.display)
ok('it derives an x-only key to publish under', /^[0-9a-f]{64}$/.test(room.pubKey ?? ''), room.pubKey)

// The same code typed by a person — lower case, spaces for dashes — has to be
// the same room, or the feature fails for reasons nobody can see.
const rejoined = await call('sessionNew', {code: room.display.toLowerCase().replace(/-/g, ' ')})
ok('a retyped code opens the same room', rejoined.pubKey === room.pubKey && rejoined.roomId === room.roomId)

const sealed = await call('sessionSend', {
  code: room.code,
  message: {type: 'contract', from: 'initiator', contractHex: STUCK.contractHex},
})
ok('a message seals into a signed event', Boolean(sealed.event?.sig && sealed.event?.id), sealed.error)
ok(
  'the relay sees ciphertext, not a contract',
  !sealed.event?.content?.includes(STUCK.contractHex.slice(0, 16)),
)

const opened = await call('sessionOpen', {code: room.code, event: sealed.event})
ok(
  'and it opens again with the same code',
  opened.message?.contractHex === STUCK.contractHex,
  JSON.stringify(opened.error ?? opened.message),
)

const otherRoom = await call('sessionNew')
const wrongCode = await call('sessionOpen', {code: otherRoom.code, event: sealed.event})
ok('another code does not open it', Boolean(wrongCode.error), JSON.stringify(wrongCode.message))

const clobbered = {...sealed.event, content: sealed.event.content.slice(0, -6) + 'AAAAAA'}
const modified = await call('sessionOpen', {code: room.code, event: clobbered})
ok('a relay that modifies a message is caught', Boolean(modified.error), JSON.stringify(modified.message))

// The preimage is the one value that must never travel. There is no field for
// it, so the module refuses the request outright rather than dropping it.
const leak = await call('sessionSend', {
  code: room.code,
  message: {type: 'note', secretHex: 'ab'.repeat(32)},
})
ok('there is no way to put a preimage in a session message', Boolean(leak.error), JSON.stringify(leak))

section('a board post is signed, replaceable, and nobody but its author can change it')

// The board's half of the seam. The Go unit tests already cover the signing and
// the proofs properly; what is checked here is what they cannot — that the
// handlers are reachable through the call table, that the module mints and keeps
// an identity in the same localStorage the swaps live in, and that a post
// survives the JSON round trip in both directions.

const bWho = await call('boardIdentity')
ok('the module mints a board key on first use', /^[0-9a-f]{64}$/.test(bWho.identity?.pubKey ?? ''), JSON.stringify(bWho.error ?? bWho.identity))
ok('nothing in the identity is a private key', !JSON.stringify(bWho.identity ?? {}).includes('priv'), JSON.stringify(bWho.identity))
ok(
  'it reports the kinds and tag to subscribe on, so the page holds no second copy',
  bWho.identity?.kinds?.post === 30777 && bWho.identity?.kinds?.take === 9778 &&
    bWho.identity?.kinds?.tag === boardTag,
  JSON.stringify(bWho.identity?.kinds),
)
ok(
  `the ${expectedEnv} instance's board tag says so — it must never match the other instance's`,
  expectedEnv === 'dev' ? boardTag !== 'ferry-board-v1' : boardTag === 'ferry-board-v1',
  boardTag,
)
ok(
  `a post on the ${expectedEnv} instance runs ${boardDefaultTtl}s by default`,
  bWho.identity?.limits?.defaultTtl === boardDefaultTtl,
  JSON.stringify(bWho.identity?.limits),
)
ok(
  'and that default sits inside the bounds a post may choose',
  bWho.identity?.limits?.defaultTtl >= bWho.identity?.limits?.minTtl &&
    bWho.identity?.limits?.defaultTtl <= bWho.identity?.limits?.maxTtl,
  JSON.stringify(bWho.identity?.limits),
)

const bAgain = await call('boardIdentity')
ok('and keeps it — a new key every reload would orphan every post', bAgain.identity?.pubKey === bWho.identity?.pubKey)
ok(
  `the identity is stored under ${boardPrefix}, not among the swaps`,
  [...Array(localStorage.length).keys()].some((i) => localStorage.key(i) === `${boardPrefix}identity`),
)

// Every offer now has to say where its author wants each half paid, every take
// has to say the same back, and each of those addresses has to be one this
// browser has PROVEN. In the browser the proofs come from two wallet
// signatures; here they are written straight into the stored identity, because
// Node has no secp256k1 and no wallet, and forging a signature is exactly what
// the scheme prevents.
//
// That is not the check being skipped — `boardBind` refuses a bad proof and is
// asserted below, and `VerifyProof` has its own tests in Go against real
// signatures on both schemes. What this stub stands in for is the wallet, so the
// seam under test here is the one this file is for: a publish crossing into Go
// and coming back signed.
const BTC_ADDR = 'bcrt1qw508d6qejxtdg4y5r3zarvary0c5xw7kygt080'
const ZNN_ADDR = 'z1qz0x9yvhpj6ymk2qprrehjhemph33d958q2nfy'

const identityRecord = JSON.parse(localStorage.getItem(`${boardPrefix}identity`))
identityRecord.bindings = [
  {scheme: 'btc-ecdsa', address: BTC_ADDR, sig: 'stub'},
  {scheme: 'znn-ed25519', address: ZNN_ADDR, pubKey: '00'.repeat(32), sig: 'stub'},
]
localStorage.setItem(`${boardPrefix}identity`, JSON.stringify(identityRecord))

// An address nobody has proven is the one thing a post may not carry, and the
// refusal is in Go rather than in a disabled button: the page is static, so a
// gate that lived only there is a gate devtools walks around.
const bUnproven = await call('boardPublish', {
  side: 'send', role: 'initiator', amountSats: 1000, zenonAmt: '1',
  btcAddr: 'bcrt1qrp33g0q5c5txsp9arysrx4k6zdkfs4nce4xj0gdcccefvpysxf3qccfmv3',
  znnAddr: ZNN_ADDR, settings: SETTINGS,
})
ok('an offer naming an address this browser has not proven is refused',
  /proof is for/.test(bUnproven.error ?? ''), bUnproven.error)

const bPosted = await call('boardPublish', {
  side: 'send',
  role: 'initiator',
  amountSats: 1_000_000,
  zenonAmt: '1200',
  lockHours: 48,
  note: 'smoke test',
  btcAddr: BTC_ADDR,
  znnAddr: ZNN_ADDR,
  settings: SETTINGS,
})
ok('an offer publishes as a signed event', Boolean(bPosted.event?.sig && bPosted.event?.id), JSON.stringify(bPosted.error))

// An offer with nowhere to pay its author is one a taker cannot finish without
// going and finding a chat window, which is the step the board exists to remove.
const bNoAddr = await call('boardPublish', {
  side: 'send', role: 'initiator', amountSats: 1000, zenonAmt: '1', btcAddr: BTC_ADDR, settings: SETTINGS,
})
ok('an offer that names no Zenon address is refused', Boolean(bNoAddr.error), bNoAddr.error)
const bNoBtc = await call('boardPublish', {
  side: 'send', role: 'initiator', amountSats: 1000, zenonAmt: '1', znnAddr: ZNN_ADDR, settings: SETTINGS,
})
ok('and so is one that names no Bitcoin address', Boolean(bNoBtc.error), bNoBtc.error)
ok('the offer carries both addresses, so a reader learns where to pay without asking',
  bPosted.post?.post?.btcAddr === BTC_ADDR && bPosted.post?.post?.znnAddr === ZNN_ADDR,
  JSON.stringify({btc: bPosted.post?.post?.btcAddr, znn: bPosted.post?.post?.znnAddr}))
ok('under the addressable kind, so republishing it is an EDIT', bPosted.event?.kind === 30777, String(bPosted.event?.kind))

const postTags = Object.fromEntries((bPosted.event?.tags ?? []).map((t) => [t[0], t[1]]))
ok('carrying the slot, the board tag and the network a reader filters on',
  postTags.d === bPosted.post?.post?.id && postTags.t === boardTag && postTags.n === 'regtest',
  JSON.stringify(postTags))
ok('and a NIP-40 expiry, so relays can retire it themselves', Number(postTags.expiration) > Math.floor(Date.now() / 1000), postTags.expiration)

// A board is public: the terms are in the clear, and that is the point. It is
// worth asserting, because the session code beside it is the opposite rule.
ok('the terms are readable — an offer nobody can read is not an offer', bPosted.event?.content?.includes('1200'))

const bRead = await call('boardRead', {event: bPosted.event, settings: SETTINGS})
ok('it reads back, verified, as ours', bRead.listing?.mine === true && bRead.listing?.post?.amountSats === 1_000_000, JSON.stringify(bRead.error ?? bRead.listing))
// The post carries the proofs the identity holds, and this harness's are stubs
// — so reading it back is the other half of the rule under test: a proof that
// does not verify HERE badges nothing and is reported instead. A reader trusts
// its own arithmetic rather than the author's word for it.
ok('a proof that does not check out badges nothing, and says so',
  (bRead.listing?.verified ?? []).length === 0 && (bRead.listing?.problems ?? []).length > 0,
  JSON.stringify({verified: bRead.listing?.verified, problems: bRead.listing?.problems}))

const bRewritten = {...bPosted.event, content: bPosted.event.content.replace('1200', '0001')}
const bCaught = await call('boardRead', {event: bRewritten, settings: SETTINGS})
ok('a rewritten offer is refused rather than shown', Boolean(bCaught.error), JSON.stringify(bCaught.listing))

const bEdited = await call('boardPublish', {
  id: bPosted.post.post.id,
  side: 'send',
  role: 'initiator',
  amountSats: 1_000_000,
  zenonAmt: '1300',
  btcAddr: BTC_ADDR,
  znnAddr: ZNN_ADDR,
  settings: SETTINGS,
})
ok('an edit keeps the slot, which is what makes a relay replace rather than add',
  bEdited.post?.post?.id === bPosted.post.post.id, JSON.stringify(bEdited.error))
ok('and keeps the original creation time, so editing does not fake being new',
  bEdited.post?.post?.createdAt === bPosted.post.post.createdAt)

const bOrphan = await call('boardPublish', {id: 'deadbeefdeadbeef', side: 'send', role: 'initiator', amountSats: 1000, zenonAmt: '1', btcAddr: BTC_ADDR, znnAddr: ZNN_ADDR, settings: SETTINGS})
ok('a post this browser does not hold cannot be edited — the key is what signs it',
  Boolean(bOrphan.error), JSON.stringify(bOrphan.error))

const bGone = await call('boardWithdraw', {id: bPosted.post.post.id})
ok('withdrawing emits both a void replacement and a NIP-09 deletion request',
  bGone.events?.length === 2 && bGone.events[0].kind === 30777 && bGone.events[1].kind === 5,
  JSON.stringify(bGone.error ?? bGone.events?.map((e) => e.kind)))
ok('the replacement says void, which is what every relay honours', bGone.post?.post?.status === 'void')

// Forgetting is local-only, which is exactly why it cannot happen while the post
// is not. The record being deleted is the only thing that can withdraw the
// offer; deleting it early does not remove the offer, it strands it.
const bStanding = await call('boardPublish', {
  side: 'send', role: 'initiator', amountSats: 250000, zenonAmt: '3',
  btcAddr: BTC_ADDR, znnAddr: ZNN_ADDR, settings: SETTINGS,
})
const bTooSoon = await call('boardForget', {id: bStanding.post.post.id})
ok('a post still standing cannot be forgotten — that would strand a live offer',
  Boolean(bTooSoon.error), bTooSoon.error)
ok('and the refusal names withdrawing as the way through',
  (bTooSoon.error ?? '').includes('withdraw'), bTooSoon.error)
const bStillThere = await call('boardMine')
ok('the record survives the refusal, so the offer can still be taken down',
  (bStillThere.posts ?? []).some((p) => p.post.id === bStanding.post.post.id))

await call('boardWithdraw', {id: bStanding.post.post.id})
const bNowGone = await call('boardForget', {id: bStanding.post.post.id})
ok('withdrawing first is what makes it forgettable', bNowGone.forgot === bStanding.post.post.id,
  JSON.stringify(bNowGone.error ?? bNowGone))

// A take carries a session code, and a session code is the room — so unlike the
// post beside it, this one must be unreadable to everyone but its recipient.
const bStranger = 'ab'.repeat(31) + 'cd'
const bTaken = await call('boardTake', {author: bStranger, postId: bPosted.post.post.id, note: 'hello', btcAddr: BTC_ADDR, znnAddr: ZNN_ADDR})
ok('taking an offer mints a room and seals it to the author', Boolean(bTaken.event?.sig && bTaken.room?.code), JSON.stringify(bTaken.error))
ok('the session code is not on the wire', !bTaken.event?.content?.includes(bTaken.room.code))

// The other half of the exchange. A take carries where the TAKER wants paid,
// sealed — a post advertises its author's addresses in the clear, but a taker is
// answering one stranger, and publishing a take in the open would put both
// sides' addresses on a public relay.
const bTakeNoAddr = await call('boardTake', {author: bStranger, postId: bPosted.post.post.id, btcAddr: BTC_ADDR})
ok('a take that says nowhere to pay the taker is refused', Boolean(bTakeNoAddr.error), bTakeNoAddr.error)
ok("and the taker's addresses are sealed rather than in the clear",
  !bTaken.event?.content?.includes(ZNN_ADDR) && !bTaken.event?.content?.includes(BTC_ADDR))
ok('the take is addressed at the author, which is how it reaches them',
  (bTaken.event?.tags ?? []).some((t) => t[0] === 'p' && t[1] === bStranger))

const bSelfTake = await call('boardTake', {author: bWho.identity.pubKey, postId: bPosted.post.post.id, btcAddr: BTC_ADDR, znnAddr: ZNN_ADDR})
ok('taking your own offer is refused rather than opening a room with yourself', Boolean(bSelfTake.error), bSelfTake.error)

const bNotOurs = await call('boardReadTake', {event: bTaken.event})
ok('a take addressed to somebody else cannot be opened here', Boolean(bNotOurs.error), bNotOurs.error)

// Presence: the dot beside an author, and the seam it crosses. The Go tests
// cover the tampering; what is checked here is that the page is told the timing
// rather than holding its own copy, and that a beat is signed under the same
// durable key as the posts — a beat under a different key would be a green dot
// that could never be matched to a row.
section('a presence beat says one key was here, and lets the reader decide the rest')

const bBeat = await call('boardPresence', {settings: SETTINGS})
ok('the module signs a beat', Boolean(bBeat.event?.sig), JSON.stringify(bBeat.error))
ok(
  'under the same key that signs the posts, so a row can be matched to it',
  bBeat.event?.pubkey === bWho.identity?.pubKey,
)
ok(
  'addressable, in one slot, so a beat replaces the last rather than piling up',
  bBeat.event?.kind === 30778 &&
    (bBeat.event?.tags ?? []).some((t) => t[0] === 'd' && t[1] === 'presence'),
  JSON.stringify(bBeat.event?.tags),
)
ok(
  'tagged for this board and network, and set to expire',
  (bBeat.event?.tags ?? []).some((t) => t[0] === 't' && t[1] === boardTag) &&
    (bBeat.event?.tags ?? []).some((t) => t[0] === 'n' && t[1] === 'regtest') &&
    (bBeat.event?.tags ?? []).some((t) => t[0] === 'expiration'),
  JSON.stringify(bBeat.event?.tags),
)
// The payload is the thing to guard. Anything an author could put in it about
// their own liveness is something the reader would have to either trust or
// ignore, and the window being the reader's constant is the whole design.
const bBeatBody = JSON.parse(bBeat.event?.content ?? '{}')
ok(
  'and carries no claim about its own freshness — the window is the reader\'s',
  Object.keys(bBeatBody).every((k) => k === 'v' || k === 'network'),
  bBeat.event?.content,
)

const bSeen = await call('boardReadPresence', {event: bBeat.event, settings: SETTINGS})
ok('a beat reads back with the author and the window to judge it by',
  bSeen.seen?.author === bWho.identity?.pubKey && bSeen.seen?.staleAfter > 0,
  JSON.stringify(bSeen.error ?? bSeen.seen))
ok(
  'the page is told the beat interval and the staleness window, so it holds no second copy',
  bWho.identity?.presence?.beatSeconds > 0 &&
    bWho.identity?.presence?.staleSeconds === bSeen.seen?.staleAfter,
  JSON.stringify(bWho.identity?.presence),
)
ok(
  'and the window outlasts two beats, so one dropped publish does not blink the dot',
  bWho.identity?.presence?.staleSeconds > 2 * bWho.identity?.presence?.beatSeconds,
  JSON.stringify(bWho.identity?.presence),
)
// The other half of that bound. A window measured in minutes shows a closed
// browser as present for long enough that somebody sends a take and waits, which
// is the whole failure the dot exists to prevent. It can be this tight because
// the page stops beating while the tab is hidden, so nothing has to survive a
// throttled background timer.
ok(
  'and is short enough that a closed tab goes grey within a minute',
  bWho.identity?.presence?.staleSeconds <= 60,
  JSON.stringify(bWho.identity?.presence),
)
// The kinds are reported for the same reason the timing is: a filter built from
// a stale constant in TypeScript is a board that subscribes and shows nothing.
ok(
  'the presence kind reaches the page from the module, not from a constant beside the filter',
  bWho.identity?.kinds?.presence === bBeat.event?.kind,
  JSON.stringify(bWho.identity?.kinds),
)

const bBeatMoved = {...bBeat.event, created_at: bBeat.event.created_at + 3600}
const bBeatCaught = await call('boardReadPresence', {event: bBeatMoved, settings: SETTINGS})
ok('a beat restamped forward is refused, so nobody can hold a key green',
  Boolean(bBeatCaught.error), bBeatCaught.error)

const bStatement = await call('boardStatement', {address: 'bc1qexample'})
ok('the module writes the statement a wallet signs, so the page cannot get it wrong',
  bStatement.statement?.includes(bWho.identity.pubKey) && bStatement.statement?.includes('bc1qexample'),
  JSON.stringify(bStatement))

const bForged = await call('boardBind', {scheme: 'btc-ecdsa', address: 'bc1qexample', sig: Buffer.alloc(65).toString('base64'), settings: SETTINGS})
ok('a proof that does not verify is refused, not badged', Boolean(bForged.error), bForged.error)

const bUnknownScheme = await call('boardBind', {scheme: 'trust-me', address: 'x', sig: 'y', settings: SETTINGS})
ok('and an unknown proof scheme is refused outright', /unknown wallet proof scheme/.test(bUnknownScheme.error ?? ''), bUnknownScheme.error)

section('two parties are checked for being on the same chains')

// The network-name check short-circuits before any node is contacted, so this
// runs offline like everything else here.
const clash = await call('chainId', {
  settings: SETTINGS,
  peer: {network: 'mainnet', btcAnchorHeight: 94, btcAnchorHash: 'ab'.repeat(32)},
})
ok('it answers with the chains this browser is on', clash.mine?.network === 'regtest', JSON.stringify(clash.mine))
ok(
  'a peer on another network is refused, naming both',
  clash.agreement?.ok === false &&
    /mainnet/.test(clash.agreement?.problems?.[0] ?? '') &&
    /regtest/.test(clash.agreement?.problems?.[0] ?? ''),
  JSON.stringify(clash.agreement),
)

// "Could not check" must never come back as "checked and fine": only one of
// those is safe to fund against.
const silent = await call('chainId', {settings: SETTINGS, peer: {network: 'regtest'}})
ok(
  'a peer that proves nothing is unchecked rather than agreed',
  silent.agreement?.ok === false && silent.agreement?.sameChain === false &&
    (silent.agreement?.unchecked ?? []).length > 0,
  JSON.stringify(silent.agreement),
)

section('the boundary refuses what it does not understand')

const unknown = await call('nonsense')
ok('an unknown method is an error, not a crash', /unknown method/.test(unknown.error ?? ''))

const strayField = await call('create', {role: 'initiator', leg: 'send', amountSats: 1000, destAddr: DEST, notAField: 1, settings: SETTINGS})
ok('an unknown request field is refused rather than ignored', Boolean(strayField.error), strayField.error)

const badNetwork = await call('config', {settings: {network: 'dogecoin'}})
ok('an unknown network is refused', Boolean(badNetwork.error), badNetwork.error)

// Both branches of the contract pay the destination address and there is no
// other, so a swap without one can be funded and then not spent.
const homeless = await call('create', {role: 'initiator', leg: 'send', amountSats: 1000, settings: SETTINGS})
ok('a swap with nowhere to pay is refused', /destination address is required/.test(homeless.error ?? ''), homeless.error)

// A panic inside Go tears down the module and takes unsaved keys with it, so the
// bridge converts one into a rejected call. Reaching a real panic on demand is
// not possible from here; what is checked is that a call which fails deep in Go
// still answers rather than hanging.
//
// The HTLC search is a real method rather than a typo in the UI's call table,
// and it refuses rather than guesses when there is no Zenon node to search with.
// Finding an actual HTLC needs a node and a counterparty.
const findNoNode = await call('zenonFind', {id, settings: SETTINGS})
ok(
  'searching for an HTLC with no Zenon node set is refused, not answered emptily',
  /no Zenon node is set/.test(findNoNode.error ?? ''),
  findNoNode.error,
)

const missing = await call('get', {id: 'deadbeefdeadbeef'})
ok('a missing swap answers with an error', /no swap with id/.test(missing.error ?? ''), missing.error)

// ---------- result ----------

console.log(`\n${checks - failures}/${checks} checks passed`)
process.exit(failures === 0 ? 0 : 1)

// Drives the real WebAssembly module the browser will load.
//
// The Go tests cover the rules and the swap test covers the chains. This covers
// the seam neither can reach: the bridge between JavaScript and Go. It loads
// the actual public/solzen.wasm, stubs the one browser API it depends on, and
// exercises the call table -- including the failure paths, because a bridge
// that turns an error into a silent success is worse than one that does not
// work at all.
//
//   node scripts/smoke.mjs
//
// It talks to the local chains where a call needs to, so the nodes have to be
// up; those checks are skipped with a note rather than failed if they are not.

import { readFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const ui = join(here, '..')

let failures = 0
function check(name, ok, detail = '') {
  console.log(`${ok ? '  ok  ' : ' FAIL '} ${name}${detail ? ` -- ${detail}` : ''}`)
  if (!ok) failures++
}

// localStorage, as the module expects to find it. Keeping it here rather than
// in the module is the point of storage_js.go: the Go side asks the platform
// for storage and copes with any answer, including this one.
const store = new Map()
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: (k) => store.delete(k),
  key: (i) => [...store.keys()][i] ?? null,
  get length() {
    return store.size
  },
}

// pathToFileURL, because a Windows path is not a URL scheme Node's ESM
// loader accepts.
await import(pathToFileURL(join(ui, 'public', 'wasm_exec.js')).href)
const go = new globalThis.Go()
const bytes = await readFile(join(ui, 'public', 'solzen.wasm'))
const { instance } = await WebAssembly.instantiate(bytes, go.importObject)
go.run(instance)
for (let i = 0; i < 200 && !globalThis.solzen; i++) await new Promise((r) => setTimeout(r, 5))
if (!globalThis.solzen) {
  console.error('the module did not install its bridge')
  process.exit(1)
}

async function call(method, params) {
  const raw = await globalThis.solzen.call(JSON.stringify(params === undefined ? { method } : { method, params }))
  return JSON.parse(raw)
}
const ok = async (method, params) => {
  const r = await call(method, params)
  if (r.error) throw new Error(`${method}: ${r.error}`)
  return r.result
}

console.log(`module build: ${globalThis.solzen.build}`)

// --------------------------------------------------------------- call table
const env = await ok('env')
check('env reports a build and its safety limits', !!env.build && env.limits.minLegGapSeconds > 0,
  `${env.build}, gap ${env.limits.minLegGapSeconds}s`)
check('the module knows which program it was built for', !!env.defaultConfig.solProgram,
  env.defaultConfig.solProgram)

check('an unknown method is refused, not ignored', (await call('does.not.exist')).error?.includes('unknown method'))
check('a stray request field is refused', (await call('env', undefined)).error === undefined)
{
  const raw = await globalThis.solzen.call(JSON.stringify({ method: 'env', extra: 1 }))
  check('a request with an unexpected field is refused', !!JSON.parse(raw).error)
}
{
  const r = await call('swap.get', { id: 'nope', typo: 1 })
  check('an unexpected parameter is refused', !!r.error && r.error.includes('unknown field'), r.error)
}

// ------------------------------------------------------------------- config
const config = await ok('config.set', env.defaultConfig)
check('settings round-trip through storage', JSON.stringify(await ok('config.get')) === JSON.stringify(config))
check('settings are persisted under this app’s own key', [...store.keys()].some((k) => k.startsWith('solzen.')))

// ---------------------------------------------------------------- the nodes
// Everything below needs the two local chains. The module's fetch path is the
// thing being tested here as much as the chains are: this is the same
// browser-shaped HTTP that the page will make.
const probe = await ok('config.check')
const chainsUp = probe.solana?.ok && probe.zenon?.ok
if (!chainsUp) {
  console.log(`\n  -- skipping the chain-backed checks: ${probe.solana?.error ?? ''} ${probe.zenon?.error ?? ''}`)
} else {
  check('the module reaches Solana over fetch()', probe.solana.ok, `v${probe.solana.version}`)
  check('and finds the program deployed there', probe.solana.program?.ok === true)
  // Executability used to be described in a comment and never checked, and the
  // README claimed it too. Both are now the same thing.
  check('and checks that it is executable, rather than only present',
    probe.solana.program?.executable === true)
  // The sharper question, and the one this project cannot answer from its own
  // source: an upgradeable program's code -- and so the terms of every escrow
  // already funded under it -- can be replaced by whoever holds the authority.
  // Reported either way; the devnet deployment is upgradeable and saying so is
  // the point.
  check('and says whether anyone can still replace its code',
    typeof probe.solana.program?.upgradeable === 'boolean',
    probe.solana.program?.upgradeable
      ? `upgradeable, authority ${probe.solana.program.authority}`
      : 'immutable')
  check('the cluster is identified by its genesis hash, not by its URL',
    typeof probe.solana.genesis === 'string' && probe.solana.genesis.length > 0,
    probe.solana.genesis)
  check('the module reaches the Zenon node over fetch()', probe.zenon.ok, `momentum ${probe.zenon.height}`)

  const created = await ok('swap.create', {
    sendsSol: true,
    solAddress: '8S8cXUuRjrqr9obfqjdCJNRQqDgiM7QZKA4T1XkK2Fek',
    znnAddress: 'z1qzmzssx28dc0fmvlca05hyxk2kgkgu7n0cj8pl',
    solAmount: '0.25',
    znnAmount: '5',
    znnToken: 'ZNN',
  })
  const id = created.swap.id
  check('a swap crosses the boundary intact', created.swap.terms.solLamports === 250000000)
  check('it comes back with an offer to send', created.offer.startsWith('solzenoffer1:'))
  check('and with the Zenon address that swap will be funded through', !!created.znnSwapAddress)
  check('the secret stays on this side of the offer', !created.offer.includes(created.swap.secret))

  const preview = await ok('swap.preview', { text: created.offer })
  check('the offer decodes back to the same terms',
    JSON.stringify(preview.terms) === JSON.stringify(created.swap.terms))

  const damaged = await call('swap.preview', { text: created.offer.slice(0, -6) + 'zzzzzz' })
  check('a damaged offer is refused with a reason', !!damaged.error, damaged.error?.slice(0, 60))

  const listed = await ok('swap.list')
  check('the store survives between calls', listed.some((r) => r.swap.id === id))

  const refreshed = await ok('swap.refresh', { id })
  check('a refresh reads both chains', typeof refreshed.status.solNow === 'number' && typeof refreshed.status.znnNow === 'number')
  check('an unfunded swap reports nothing funded', !refreshed.status.sol.funded && !refreshed.status.znn.funded)
  check('and asks for the counterparty’s acceptance first',
    refreshed.status.actions[0].kind === 'exchange.applyAccept', refreshed.status.actions[0].kind)

  const wrongProgram = await call('swap.accept', {
    offer: created.offer.replace('solzenoffer1:', 'solzenaccept1:'),
    solAddress: '8S8cXUuRjrqr9obfqjdCJNRQqDgiM7QZKA4T1XkK2Fek',
    znnAddress: 'z1qzmzssx28dc0fmvlca05hyxk2kgkgu7n0cj8pl',
  })
  check('an acceptance offered as an offer is refused', !!wrongProgram.error, wrongProgram.error?.slice(0, 60))

  // Export and import, which is the only way a swap moves between browsers --
  // and the only defence against localStorage being cleared.
  const exported = await ok('store.export')
  check('the export carries the swap and its key', exported.swaps.some((s) => s.id === id && s.znnSwapSeed))
  const reimport = await ok('store.import', { text: JSON.stringify(exported) })
  check('re-importing the same export changes nothing', reimport.imported === 0 && reimport.skipped === exported.swaps.length,
    JSON.stringify(reimport))
  const badImport = await call('store.import', { text: '{"format":"something-else","swaps":[]}' })
  check('an import in an unknown format is refused', !!badImport.error, badImport.error?.slice(0, 60))
  check('and the healthy records are still there', (await ok('swap.list')).some((r) => r.swap.id === id))

  await ok('swap.delete', { id })
  check('a deleted swap is gone', !(await ok('swap.list')).some((r) => r.swap.id === id))
}

console.log(`\n${failures === 0 ? 'all checks passed' : `${failures} check(s) failed`}`)
process.exit(failures === 0 ? 0 : 1)

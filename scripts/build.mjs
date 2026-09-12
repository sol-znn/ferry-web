// The whole build, in one place and on one runtime.
//
//   node scripts/build.mjs            compile the module, then build the site
//   node scripts/build.mjs --wasm     compile the module only
//   node scripts/build.mjs --dev      build the development instance
//
// --dev is the only difference between the two deployments, and it is set here
// because here is the only point that feeds both halves of the artefact: the
// linker flag that tells the Go module which instance it is, and the Vite define
// that tells the page the same thing. A build cannot come out half development
// and half production. See wasm/env.go.
//
// Node rather than a shell script because this has to run identically on a
// Windows workstation and a Linux CI runner.
//
// Three steps, and the order matters. The output directory follows the instance
// — dist/ or dist-dev/ — so building one never overwrites the other:
//
//   1. Compile wasm/ for GOOS=js GOARCH=wasm into ui/public/ferry.wasm.
//   2. Copy Go's runtime shim, wasm_exec.js, out of THIS Go installation. It is
//      version-locked to the compiler: pairing a shim from one release with a
//      module from another fails at instantiation with an import mismatch that
//      reads like a corrupt download. Copying it here rather than committing it
//      is what makes that impossible.
//   3. Run Vite with FERRY_WASM_VERSION set to the module's content hash. Both
//      files are copied verbatim out of public/, so they get no content-hashed
//      filename of their own; the hash is appended to their URLs instead.

import {execFileSync} from 'node:child_process'
import {copyFileSync, existsSync, mkdirSync, readFileSync, statSync} from 'node:fs'
import {createHash} from 'node:crypto'
import {dirname, join, resolve} from 'node:path'
import {fileURLToPath} from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const outDir = join(root, 'ui', 'public')
const wasmPath = join(outDir, 'ferry.wasm')
const wasmOnly = process.argv.includes('--wasm')

// Which instance to build. The flag wins over the environment variable so a CI
// job can set FERRY_ENV once for a whole workflow and a person can still
// override it for one command; absent both, it is production, matching the
// default in wasm/env.go. Anything else is refused rather than normalised —
// `--env=develop` silently producing a production build is the mistake this
// whole mechanism exists to make impossible.
const envArg = process.argv.find((a) => a.startsWith('--env='))?.slice('--env='.length)
const ferryEnv = process.argv.includes('--dev') ? 'dev' : (envArg ?? process.env.FERRY_ENV ?? 'prod')
if (ferryEnv !== 'dev' && ferryEnv !== 'prod') {
  console.error(`unknown instance ${JSON.stringify(ferryEnv)}: expected "dev" or "prod"`)
  process.exit(1)
}

// `go` is a real executable and is spawned directly. Spawning it through a
// shell on Windows would re-split the arguments and break `-ldflags "-s -w"`
// into two flags, one of which `go build` does not have.
function run(cmd, args, opts = {}) {
  execFileSync(cmd, args, {stdio: 'inherit', ...opts})
}

function goEnv(name) {
  return execFileSync('go', ['env', name], {encoding: 'utf8'}).trim()
}

// The 32-byte public half of the program keypair, base58 — which is the
// program's address. Derived here rather than shelling out to the Solana CLI,
// which is not a build dependency of a static site.
const BASE58 = '123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz'
function base58(bytes) {
  let x = 0n
  for (const b of bytes) x = x * 256n + BigInt(b)
  let out = ''
  while (x > 0n) {
    out = BASE58[Number(x % 58n)] + out
    x /= 58n
  }
  for (const b of bytes) {
    if (b !== 0) break
    out = BASE58[0] + out
  }
  return out
}

function solanaProgramID() {
  if (process.env.FERRY_SOL_PROGRAM) return process.env.FERRY_SOL_PROGRAM
  const keypair = join(root, 'program/target/deploy/ferry_htlc-keypair.json')
  if (!existsSync(keypair)) {
    console.warn(
      'build: no Solana program keypair found, so the page starts with no program address. ' +
        'Deploy the program first, or set FERRY_SOL_PROGRAM. Node settings can name one at ' +
        'run time; Solana legs are refused until something does.',
    )
    return ''
  }
  const secret = Uint8Array.from(JSON.parse(readFileSync(keypair, 'utf8')))
  return base58(secret.slice(32))
}

// ---------- 1. the module ----------

try {
  goEnv('GOROOT')
} catch {
  console.error('go is not on PATH. Install Go 1.24 or newer: https://go.dev/dl/')
  process.exit(1)
}

mkdirSync(outDir, {recursive: true})

// The Solana program's address comes from its keypair, so it changes whenever
// the program is deployed somewhere new. Reading it from the file the deploy
// used is what keeps the page's default pointed at the program that is actually
// on the chain — and it stays a DEFAULT: Node settings can name any deployment,
// and an offer naming a different one is refused rather than quietly accepted.
const solProgram = solanaProgramID()

console.log(`compiling wasm/ for the browser (${ferryEnv} instance)...`)
run(
  'go',
  [
    'build',
    '-trimpath',
    '-ldflags',
    `-s -w -X main.BuildEnv=${ferryEnv} -X main.SolanaProgram=${solProgram}`,
    '-o',
    wasmPath,
    '.',
  ],
  {
    cwd: join(root, 'wasm'),
    // -s -w drop the symbol table and DWARF, which are dead weight in a browser
    // and about a fifth of the file. -trimpath keeps build machine paths out of
    // a binary that gets published. -X writes the instance into the module, so
    // the code that signs transactions knows which one it is rather than being
    // told by the page around it.
    env: {...process.env, GOOS: 'js', GOARCH: 'wasm'},
  },
)

// ---------- 2. the runtime shim ----------

const goroot = goEnv('GOROOT')
// Go 1.24 moved the shim from misc/wasm to lib/wasm. Look in both.
const shim = [join(goroot, 'lib', 'wasm', 'wasm_exec.js'), join(goroot, 'misc', 'wasm', 'wasm_exec.js')].find(
  (p) => existsSync(p),
)
if (!shim) {
  console.error(`could not find wasm_exec.js under ${goroot}`)
  process.exit(1)
}
copyFileSync(shim, join(outDir, 'wasm_exec.js'))

const bytes = readFileSync(wasmPath)
// The instance is part of what the hash identifies, because the two builds must
// not be able to share a cached module: they differ only in a linker flag, so
// switching between them changes the file by a few bytes and the URL has to
// change with it.
const version = createHash('sha256').update(bytes).digest('hex').slice(0, 12)
console.log(`ferry.wasm    ${(statSync(wasmPath).size / 1048576).toFixed(1)} MB  (${version})`)
console.log(`wasm_exec.js  from ${goroot}`)
console.log(`instance      ${ferryEnv}`)
console.log(`sol program   ${solProgram || '(unset — set one in Node settings)'}`)

if (wasmOnly) process.exit(0)

// ---------- 3. the site ----------

console.log('\nbuilding the site...')
// Vite's own entry point under this same Node, rather than `npx vite`. npx
// resolves to a .cmd on Windows that Node refuses to exec without a shell, and
// putting a shell back in is what broke the `go build` invocation above. This
// has no such wrapper and no PATH lookup at all.
const vite = join(root, 'ui', 'node_modules', 'vite', 'bin', 'vite.js')
if (!existsSync(vite)) {
  console.error('vite is not installed. Run `npm install --allow-git=all` in ui/ first.')
  process.exit(1)
}
run(process.execPath, [vite, 'build'], {
  cwd: join(root, 'ui'),
  env: {...process.env, FERRY_WASM_VERSION: version, FERRY_ENV: ferryEnv},
})

// Walks the boundary between the two ways a Zenon block gets paid for.
//
//   node scripts/plasma-test.mjs
//
// `ui:test --pow` is the end-to-end version of this and takes about nine
// minutes, most of it hashing. This asks the same question of the node
// directly, on one throwaway address, in about one -- so the pricing rule can
// be checked on every change rather than once a session.
//
// It exists because of issue #9: a --pow run published an htlc.Unlock without
// mining, and the suspicion was that the node had priced it at zero for an
// account with no plasma. It had not. The forensics are in ISSUES.md; what is
// left here is the assertion that would have refuted it in a minute, plus the
// rule underneath it -- that a block claiming fused plasma its account does not
// have is refused by the chain, not merely discouraged by the page.

import { execFileSync } from 'node:child_process'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const root = join(here, '../..')
const wasmDir = join(root, 'wasm')
const exe = process.platform === 'win32' ? '.exe' : ''
const walletBin = join(root, 'bin', `devnet-wallet${exe}`)
const probeBin = join(root, 'bin', `powpaths${exe}`)
const ZENON_URL = process.env.SOLZEN_ZENON_URL ?? 'http://127.0.0.1:35997'

console.log('\n=== building')
execFileSync('go', ['build', '-o', walletBin, './cmd/devnet-wallet'], { cwd: wasmDir, stdio: 'inherit' })
execFileSync('go', ['build', '-o', probeBin, './cmd/powpaths'], { cwd: wasmDir, stdio: 'inherit' })

// The probe fuses QSR from devnet account index 1, which every other script
// here also publishes from. Two at once build on the same account chain and
// one is rejected, so this has to be run in series with them -- the same rule
// ISSUES.md records for swap:test, ui:test and timeout:test.
try {
  execFileSync(probeBin, ['-url', ZENON_URL, '-wallet', walletBin], { stdio: 'inherit' })
} catch (err) {
  process.exit(err.status ?? 1)
}

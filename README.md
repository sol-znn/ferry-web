# Ferry v2 — cross-chain atomic swaps, as a static site

Trustless swaps between Bitcoin, Zenon (NoM) and Solana. No backend, no full
node — a directory of static files. Every rule runs as Go compiled to
WebAssembly inside the page; the UI is Vue.

| trade | one leg | the other |
| --- | --- | --- |
| **BTC ⇄ ZTS** | P2SH contract | `htlc` embedded contract |
| **ZTS ⇄ ZTS** | `htlc` entry | `htlc` entry, a **different** token |
| **SOL ⇄ ZTS** | `ferry-htlc` escrow | `htlc` entry |
| **SOL ⇄ BTC** | `ferry-htlc` escrow | P2SH contract |

One secret, two contracts: both settle or both refund. No counterparty,
operator or line of this software can take your coins.

## Why it works in a browser

- **Funding a contract is an ordinary payment.** A Bitcoin HTLC is a P2SH
  address. Hardware wallets, phones and exchange withdrawals all work.
- **Spending out of one needs no wallet either.** Bitcoin uses a per-swap key
  that can only move coins inside that one contract. `htlc.Unlock` and the
  Solana program's `redeem` may be called **by anyone** and pay the address
  recorded at creation — so either side can finish a stuck swap.
- **A contract call is just bytes a wallet will sign.** Ferry builds them;
  Syrius or Phantom signs.

## The model

Legs are named by what they do for **you**:

```
Out   the leg you fund.               You hold its refund path.
In    the leg the counterparty funds. You hold its claim.
```

- The **initiator's own funded leg takes the long deadline**, so `Out` is the
  long one exactly when you are the initiator.
- The **preimage surfaces on the leg you funded**, when the counterparty claims
  it. Ferry reads it off the chain, not out of a message.
- The **initiator funds first.** The participant's engine refuses to lock
  anything until the initiator's leg is on chain and verified.

## Quick start

```sh
cd ui
npm install --allow-git=all   # nom-ui is a git dependency; npm 12 needs the flag
npm run build                 # Go → wasm, then the site, into ../dist
npm run preview
```

Needs **Go 1.27+** and **Node 22+**. `dist/` is ~10 MB, of which 9.6 MB is the
wasm module (~3 MB gzipped, fetched once). `npm run build:dev` writes
`dist-dev/`: regtest, a **DEV** badge, its own storage namespace.

**Before your first swap**, open **Nodes**. Zenon has *no default* — verifying a
counterparty's HTLC is the one check where a dishonest answer costs money, so
you name that node yourself (`wss://…:35998`; a WebSocket handshake is not
CORS-preflighted). The **Solana program** address is a *term* of a swap, not a
setting: two deployments of one source are two contracts, so it rides on every
offer and a mismatch is refused.

## Doing a swap

**Chains, then wallets, then terms.** The wallet step is a gate: wallets supply
the addresses both ways out of the swap pay, and typing those by hand at the end
is the easiest place to paste somebody else's.

1. Exchange the `swapoffer2:` string — public data only, never the secret.
2. The initiator funds.
3. The participant verifies (hashlock, parties, token, amount, deadline
   ordering) against their own node, then funds.
4. The initiator claims, publishing the preimage.
5. The participant claims with it. Ferry finds it on chain by itself.

**Auto Mode** presses the buttons. It never signs (your wallet's window is where
it stops), never skips a check, and never refunds.

## The board

Offers on public Nostr relays, nobody hosting the list. Posts are addressable
events signed by a key your browser holds, so editing is republishing and only
you can withdraw. **Acting on an offer needs a proven address on every chain it
settles on** — one wallet signature per chain, derived from the offer: ZTS⇄ZTS
needs one, SOL⇄BTC needs no Zenon wallet. Taking mints a session code sealed to
the poster.

Nothing on the board is vetted. A signature proves a post was not altered; a
badge proves someone holds an address. The swap is what protects you.
See [docs/BOARD.md](docs/BOARD.md).

## Getting your money back

- **Bitcoin** — a refund is pre-signed the moment funding is seen and is in
  every recovery file. The **Recover page** rebuilds it at a fee rate you pick,
  offline, from the file alone.
- **Zenon** — `htlc.Reclaim`, offered once the deadline has passed.
- **Solana** — `refund` needs no signature from anyone after the timelock.
- **Export** takes every swap in the browser as one document.

## Layout

```
wasm/        the rules, compiled to WebAssembly (one file per chain: btcleg,
             znnleg, solleg; plus sol/ znn/ chain/ clients and api.go)
program/     the Solana HTLC program — two branches, both paying fixed addresses
ui/          Vue 3 + Vite + Tailwind 4 + nom-ui
scripts/     build, smoke test, regtest Esplora shim, CLI wallet
```

## Testing

```sh
cd wasm && go test ./...
cd ui   && npm test          # go test + wallet-provider + 140-check smoke
cd ui   && npm run typecheck && npm run lint
```

`npm run smoke` drives the actual `ferry.wasm` under Node. As a user would:
[docs/TESTING.md](docs/TESTING.md).

## Status

Proof of concept. Nothing has been used with real money on any mainnet.

A whole BTC⇄ZTS swap **has** settled end to end on regtest + devnet, through the
UI, with `scripts/cli-wallet.mjs` in place of the extensions. Known gaps:

1. **Swap records are not encrypted at rest** in `localStorage`. Any script on
   the origin can read them.
2. **No Solana program is deployed on a public chain.** `program/` builds and a
   deployment on a local validator has settled swaps; until an address is set in
   Nodes, a Solana leg is refused.
3. **No RBF or fee bumping.** The Recover page is the manual answer.
4. **Timeout paths are tested on the test chains only.** The refund drill has
   run on all three: the Bitcoin refund broadcast and confirmed after its
   locktime and refused as `non-final` before it, `htlc.Reclaim` taking a Zenon
   leg back, and a Solana `refund` closing the escrow and returning the rent.
   None of it has been run where money is real.
5. **Sessions have had no adversarial review.**
6. **~3 MB gzipped on a first visit**, which buys not rewriting Bitcoin signing
   in JavaScript.

## Documentation

| | |
| --- | --- |
| **`#/docs` in the app** | How a swap works, for the person doing one |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | How it is put together |
| [docs/SECURITY.md](docs/SECURITY.md) | The browser threat model — read this one |
| [docs/WALLETS.md](docs/WALLETS.md) | The three wallet boundaries |
| [docs/BOARD.md](docs/BOARD.md) | Posts, proofs, takes, presence |
| [docs/TESTING.md](docs/TESTING.md) | Testing a deployment, and the dev harness |
| [docs/DEPLOY.md](docs/DEPLOY.md) | The two instances and where they go |
| [HANDOFF.md](HANDOFF.md) | What changed from v1, and the live status |

The Bitcoin script is the `decred/atomicswap` template, kept so any tool that
understands it can audit or spend a contract Ferry produces.

## License

ISC — see [LICENSE](LICENSE).

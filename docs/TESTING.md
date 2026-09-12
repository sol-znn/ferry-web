# Testing it the way a user would

Unit tests already pass. What is left is what they cannot reach: real fee
markets, real node availability, real wallets, real counterparties.

| Stage | Proves | Costs |
| --- | --- | --- |
| 0. Preflight | the nodes answer from a browser | nothing |
| 1. Cold open | it works for somebody who has never seen it | nothing |
| 2. Rehearsal | whole swaps settle, all four pairs | nothing |
| 3. Production | it works where money is real | dust + fees |
| 4. Refund drill | the timeout path on each chain | fees |
| 5. Loss drills | recovery survives losing the browser | nothing |

A stage that cannot be run is a finding, not a skip.

## What a user supplies

- **Bitcoin** — any wallet; funding is an ordinary payment. UniSat adds
  one-click funding and signs board proofs.
- **Zenon** — the [Syrius **extension**][syrius], not the desktop app. Its own
  P2P swap screens cannot do this leg and are not used.
- **Solana** — Phantom, Solflare or Backpack, **and a deployed program**. Until
  its address is in Nodes, a Solana leg is refused.

## 0 — Preflight

```sh
npx wscat -c wss://node.zenonhub.io:35998 \
  -x '{"jsonrpc":"2.0","id":1,"method":"ledger.getFrontierMomentum","params":[]}'
curl -sS -X POST <sol-rpc> -H 'content-type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"getVersion"}'
curl -sSI https://blockstream.info/api/blocks/tip/height     # CORS
```

Prefer `wss://` for Zenon: a WebSocket handshake is not CORS-preflighted. A page
on `https://` cannot reach `http://` or `ws://` at all.

Then check the site itself: `ferry.wasm` returns 200 as `application/wasm`,
`/static/*` is immutable, no CSP violations in the console, the DEV badge
matches the instance, and the origin is not shared with anything else you
publish — storage is per origin and swap records are unencrypted.

## 1 — Cold open

In a browser that has never seen the site: the page explains itself before any
wallet is connected; Nodes shows Zenon blank with a warning; **New swap** asks
for chains first with nothing about amounts on screen; step two lists exactly
the wallets that pair needs and will not advance without them; the terms step is
in each leg's own unit; the board reads with nothing proven and Take is disabled
naming the chains you still need.

Check the refusals: a Bitcoin address in a Zenon field, a Zenon address in a
Solana field, a ZTS in the wrong slot, and a ZTS traded against itself.

## 2 — Rehearsal on test chains

**Two participants means two origins** — two tabs on one origin are one
participant holding the swap twice, because storage is per origin. A second
profile works; so does the cheaper trick, since `http://127.0.0.1:4176` and
`http://localhost:4176` are different origins to a browser and the same server
to you. Give the two instances different wallets as well as different storage:

```js
// initiator                      // participant, in the other origin
await __ferry.wallets({cli: 'http://127.0.0.1:8787', znnIndex: 3})
await __ferry.wallets({cli: 'http://127.0.0.1:8787', znnIndex: 0,
                       btc: '<a second regtest address>'})
```

Without `znnIndex` both sides sign as the same Zenon account, which a ZTS⇄ZTS
swap turns into one key creating an HTLC and unlocking it — every rule that only
bites between strangers goes untested.

Run each pair **in both directions**: which side initiates decides which leg is
long and where the preimage surfaces, and those are the two rules a bug hides
in. For each run: both sides create; the offer crosses; the initiator funds; the
participant's engine refuses to fund before the initiator's leg is on chain and
verified (try it); the participant funds; the initiator claims; **the preimage
appears on the participant's card by itself, without being told**; the
participant claims; both cards read settled.

Then the uncooperative counterparty: the initiator claims and closes the tab.
The participant must still finish.

**Auto Mode** performs no step today — `ZenonWallet`'s auto path is behind a
prop nothing passes, and the other two panels have none. Arming it only keeps a
background tab refreshing. If it is ever wired, what to check is that it stops
at every wallet window, never refunds, and halts with a sentence on the first
failure.

## 3 — The production run

The smallest amount worth unlocking on each chain, one run per pair, mainnet,
real wallets. Watch the quoted unlock cost before funding a Bitcoin leg: below
the dust limit the recipient receives nothing.

## 4 — The refund drill

Fund a leg with a short deadline, let it expire, take it back. The shortest
deadline the form accepts is **three hours**: a leg needs two hours left at the
moment it is funded, and funding is minutes after the form.

- **Bitcoin** — broadcast the pre-signed refund. Expect "non-final" for up to an
  hour: `nLockTime` is compared against median time past, which trails.
- **Zenon** — `htlc.Reclaim` from the creating account; refused before expiry,
  which is why the button appears only after.
- **Solana** — `refund` after the timelock; confirm the rent comes back. It
  should close the escrow outright — `getAccountInfo` on it answers `null`
  afterwards — and return the amount **and** the rent, so the funder is down
  only the transaction fee.

Only Bitcoin can be hurried. Regtest takes `setmocktime` and then twelve blocks,
because median time past is the median of the last eleven:

```sh
bitcoin-cli setmocktime $(( $(date +%s) + 21600 ))
for i in $(seq 12); do bitcoin-cli -generate 1; done
```

That moves the **chain's** clock and not the browser's, so the card still counts
the deadline in real time and its refund button stays hidden. The transaction is
the thing under test: POST the pre-signed hex to the Esplora shim directly, and
expect `non-final` before the locktime and a txid after it. The devnet and the
Solana test validator both run on wall time, so those two are a real wait.

Also confirm the Recover page rebuilds a Bitcoin refund at a higher fee rate,
offline, from the file alone.

## 5 — Loss drills

Clear site data on a browser holding a funded swap and recover it from the file
offline. Export and import into a second browser; confirm a stale record does
not clobber a newer one. Switch wallet accounts mid-swap and confirm the page
notices.

---

## A local run

```sh
bitcoind -regtest -daemon
node scripts/regtest-esplora.mjs          # http://127.0.0.1:3002
cd ../go-zenon && make devnet-up          # http://127.0.0.1:35997
docker build -t ferry-build program
docker run --rm -v "$PWD/program:/work" -v ferry-cache:/root -w /work ferry-build \
  --sbf-out-dir /work/target/deploy
solana-test-validator                     # http://127.0.0.1:8899
solana program deploy program/target/deploy/ferry_htlc.so
cd ui && npm run build:dev && npm run preview:dev
```

`regtest-esplora.mjs` serves the endpoints `wasm/chain/esplora.go` calls, backed
by the regtest node's RPC, with the CORS headers a browser requires. Funding is
`bitcoin-cli sendtoaddress`, confirmations are `bitcoin-cli -generate 1`.

Building the Solana program ends with `Error: Neither curl nor wget were found`
**after** it has written `target/deploy/ferry_htlc.so`. That is a post-build step
of `cargo-build-sbf`, not the build — check for the file rather than the exit
code.

`scripts/wallet-devnet.mjs` takes an `htlc.Create` block apart field by field
without a browser; `scripts/wallet-provider.mjs` is a stub Syrius provider, so
the wallet path has a test that needs no extension.

## The CLI wallet

`scripts/cli-wallet.mjs` is a wallet with no extension in it — a real ed25519
key for Zenon, `bitcoin-cli` for Bitcoin — behind one loopback surface the
development build can call. It exists because the two things stage 2 needs and a
browser driver cannot do are both wallet-shaped: approving a signature belongs
to the extension's own window, and paying a contract belongs to whoever holds
coins.

```sh
node --env-file=../zenon-faucet/.env scripts/cli-wallet.mjs   # 127.0.0.1:8787
```

It holds **two** accounts, index 3 (the funded one) and index 0 (go-zenon's
genesis spender), so a swap can have two sides. Index 3 is fused and publishes
in seconds; index 0 has no plasma and mines its own, which takes around three
minutes a block — long enough that a driver waiting on it should watch the
account height rather than a timeout.

```js
__ferry.settings({network: 'regtest', btcEsplora: 'http://127.0.0.1:3002',
                  znnUrl: 'http://127.0.0.1:35997'})
await __ferry.wallets({cli: 'http://127.0.0.1:8787'})   // then reload
await __ferry.cli.fund(contractAddress, 400000)
await __ferry.cli.mine(2)
```

With `cli`, the Zenon provider stops being a stub: `sendAccountBlock` posts the
block to a key that owns coins, which signs it, mines the plasma the node asks
for and publishes. It takes only the four fields carrying intent — recipient,
amount, token, call data — and rebuilds height, previous hash, momentum, plasma
and nonce against the chain as it is now, because a wallet that signed a
stranger's height would publish a block the node rejects.

It must also report **which chain it signs for and which node it publishes
through**. Ferry refuses to build a block without both, and is right to: a
signature for chain 1 published to chain 69 is a valid signature over the wrong
thing.

Development only. It is the one file here that imports from a sibling repo
(`../zenon-faucet` for the signing client) rather than vendoring it.

## The dev harness

The development build carries `window.__ferry`: stub wallets, the nodes settable
without the dialog, and clicking by label. Behind `isDev`, so the production
bundle does not contain it — `grep -r __ferry dist/` finds nothing.

```js
__ferry.settings({...})   __ferry.wallets({...})   __ferry.solanaKey()
await __ferry.click('New swap')
await __ferry.choose('Which way', 'I pay Zenon, I receive Bitcoin')
__ferry.fill('You pay', '1.5')
await __ferry.waitFor('Create the swap')
__ferry.reachable()   __ferry.state()   __ferry.reset()
```

The stubs refuse `sendBitcoin`, `sendAccountBlock` and `signMessage` unless a
CLI wallet is configured: a fake txid in a swap record is worse than no swap,
because it is a card claiming a leg is funded when nothing is.

They live on `__ferryUnisat` and `__ferryZenon`, not on the extensions' own
globals, which are non-writable and non-configurable when an extension is
installed.

**Three traps for anything else driving the DOM.**

- A browser does not run CSS animations in a background tab, so a dialog you
  close never unmounts: it stays in `data-state="closed"` with the page still
  `aria-hidden` and `<body>` at `pointer-events: none`. Nothing is wrong — bring
  the tab forward and it finishes — but a driver reading `aria-hidden` literally
  concludes the page is empty.
- The same tab throttles `setTimeout` to about once a second, so a "50ms" poll
  really polls once a second. `find` watches a MutationObserver and `choose`
  waits on `focusin`; six clicks went from ~40s to ~130ms.
- A synthetic click does not choose an option — these selects commit from a
  trusted pointer event. `choose` opens with Enter and picks with arrow keys.

[syrius]: https://github.com/sol-znn/syrius-extension/releases

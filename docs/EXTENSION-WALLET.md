# The Zenon leg through the Syrius extension

Ferry never holds a Zenon key. Creating and unlocking an HTLC are signed
operations, so the page builds the account block and the
[Syrius browser extension][syrius] signs and publishes it.

This document is the boundary: what crosses it, what each side decides, what is
checked either side of it, and what has actually been driven against a chain.

Target version: **v0.3.1 or later**.

---

## Why this works at all

Syrius's own HTLC screens cannot do a Bitcoin swap — they hardcode SHA3-256 and
cap expiry at 24h — and for a long time that read as "the extension cannot do
this leg". It is the wrong conclusion. Those screens are a feature; the extension
underneath them is a signer, and it will sign **any account block a page hands
it**.

An HTLC call is nothing but a send to an embedded contract carrying ABI-encoded
arguments. So:

- the page decides `toAddress`, `amount`, `tokenStandard` and `data`;
- the extension supplies the account, the public key, the chain position, plasma
  or proof-of-work, and the signature.

`htlc.Create`, `htlc.Unlock` and `htlc.Reclaim` are all expressible that way,
without the extension knowing what an HTLC is. `htlc.Unlock` in particular may be
called by anyone and pays the address recorded in the entry rather than the
caller — which is what lets a wallet holding nothing settle this leg.

---

## The provider

`window.zenon`, injected by a MAIN-world content script at `document_start`, so
it is present before this app's scripts run and no page CSP applies to it.
Promise-returning, with request ids.

| method | prompts? | what it does |
|---|---|---|
| `connect()` | first time per origin | grants this origin read access; resolves silently for an origin already connected and unlocked |
| `disconnect()` | no | drops the origin from the wallet's connected-sites list |
| `getAccounts()` | never | `[]` when not connected or locked |
| `getChainId()` | never | |
| `getNodeUrl()` | never | |
| `sendAccountBlock(block)` | every time | completes, signs and publishes; resolves `{hash, block}` |
| `signMessage(message)` | every time | signs the raw UTF-8 bytes with the account key; resolves `{message, address, publicKey, signature}`, the last two hex. Nothing is broadcast and nothing is spent |

`signMessage` is `znn_sign` over the wire — the same request desktop Syrius
serves over WalletConnect, and the same bytes and encoding, so a signature from
either verifies against the same code. It is what the board's Zenon proof is
made of, and an extension build without it cannot post or take there: see
[BOARD.md](BOARD.md#zenon-znn-ed25519). `canSignMessage()` in
`ui/src/core/zenon-wallet.ts` is how the page finds out, and the answer is kept
live rather than cached — an extension can be updated without a reload.

Events: `accountsChanged`, `chainChanged`, `nodeChanged`, `disconnect`.

Rejections are `{code, message}` — a plain object, not an `Error`, so
`ui/src/core/zenon-wallet.ts` translates them: 4001 declined, 4100 not connected,
4200 unsupported, 4900 no answer, -32603 internal.

`sendTransaction` — a plain amount-to-address send — is deliberately unused.
Every send this app needs carries data.

**One connect prompt per origin, one approval per block.** Everything else is
answered from the extension's session state, which is what lets the page restore
a connection on load and re-read the wallet after an account switch without
asking again.

### Two things the transport forces

**Blocks must be JSON round-tripped before they are sent.** The provider hands
params to `window.postMessage`, which is structured clone, and every block this
app sends is held in a Vue `ref` — a `Proxy`, which structured clone refuses
outright with "could not be cloned", naming no field. `plain()` in
`zenon-wallet.ts` does the round trip at the transport boundary rather than at
each call site: the content script forwards to `chrome.runtime.sendMessage`,
which serialises as JSON anyway, so anything a round trip drops was never going
to reach the wallet — and a value that cannot survive the trip throws where the
error names the request rather than arriving in the approval window as a field
quietly gone missing.

**Presence is advisory, never required.** The honest test of whether a wallet is
there is to ask it and see whether it answers, so `request()` is not gated on a
detection flag; the flag decides what the card *says*. Older builds announced
themselves by appending a `<script>` element from the content script, which this
app's `script-src 'self' 'wasm-unsafe-eval'` silently refused — making the wallet
undetectable on exactly the sites that take their own security seriously.
Relaxing the CSP was never on the table.

---

## ABI encoding

`wasm/go.mod` has no go-zenon dependency and must not gain one: go-zenon pulls
go-ethereum and friends, and [ARCHITECTURE.md](ARCHITECTURE.md) treats the module
size as a hard budget. The htlc ABI is three methods, so `wasm/znn/htlcabi.go`
hand-rolls it and pins the result to golden vectors from go-zenon's own encoder.

Rules, read out of `go-zenon/vm/abi`:

- method id = first 4 bytes of `SHA3-256("Name(type,type,…)")` — `crypto.Hash` is
  `sha3.New256`, standard SHA3, **not** Keccak — over the raw type strings from
  the ABI JSON (`address`, `int64`, `uint8`, `bytes`, `hash`);
- 32-byte words, Ethereum-style head/tail: static args inline, dynamic args
  (`bytes`) as an offset in the head and `[length, right-padded data]` in the
  tail;
- `address`, `hash`, `tokenStandard`: **left**-padded to 32 bytes;
- `int64`/`uint8`: `U256`, left-padded.

```
Create(address,int64,uint8,uint8,bytes)   5c7e7110   hashLocked, expirationTime, hashType, keyMaxSize, hashLock
Unlock(hash,bytes)                        d33791d3   id, preimage
Reclaim(hash)                             7e003c8d   id
```

All three match go-zenon's encoder byte for byte, which `wasm/znn/htlcabi_test.go`
holds.

`AccountBlockTemplate.fromJson` parses **every** field — `Hash.parse`,
`Address.parse` and `TokenStandard.parse` all run on whatever is there — so
`wasm/walletblock.go` emits a complete template with zero values rather than a
partial one.

---

## The sync gate

The extension is not an extension of this page. It has its own node URL, its own
chain identifier and its own selected account — three settings Ferry can read and
cannot set. Three mistakes live on that boundary, and all three cost money:

| | why it costs money |
|---|---|
| Wrong account | the signer of an `htlc.Create` becomes `timeLocked`, the **only** address the contract will pay a reclaim to |
| Wrong chain | an HTLC created where the counterparty is not looking — and if one side is mainnet, with real ZNN |
| Stale node | the expiry is compared against momentum time, so a lagging node produces a deadline the chain disagrees with |

### Equal chain identifiers prove nothing

Every go-zenon devnet anyone starts is chain 69. Two of them share that and
nothing else. So the check is the one `chainid.go` already uses on a
counterparty's claim, applied to the wallet's node: **read a momentum hash from
both nodes at an agreed height and compare it.** `sameChain` is set only when
that comparison actually ran and matched; it is the single positive result the
gate produces, and everything else is at best the absence of a contradiction.

The anchor is taken 60 momentums below whichever tip is *lower*, so a node that
is merely behind still holds the momentum being asked about — otherwise lagging
would be indistinguishable from a different chain.

### A check that could not run blocks, exactly like a failure

`walletSyncResult.Ok` is false for a proven mismatch **and** for anything that
could not be established. An unreachable wallet node is precisely what a wallet
pointed at somebody else's chain looks like from this side, so "could not tell"
and "fine" are kept apart. There is no override.

The practical consequence, and it is deliberate: a wallet whose node this page
cannot reach — an `http://` node without permissive CORS, or any non-`https` node
when Ferry is served over https — cannot be used. The fix named in the error is to
point both at the same node.

### Three checks, at three moments

1. **Before the block is built.** `runWalletSync` in `wasm/walletsync.go`, run
   both by the page (to display the verdict) and inside `handleWalletBlock` (to
   make it load-bearing). Refusing to *build* a block is the only refusal worth
   anything — once one exists, handing it to the wallet is a `postMessage` that
   Go does not mediate.
2. **Immediately before handing it over.** The page re-runs the same check and
   compares the wallet's current account against `plan.signer`. A prepared plan
   is discarded outright whenever the extension announces an address, chain or
   node change, or the swap's locktime moves.
3. **After it is published.** `diffSignedBlock` compares the block the wallet
   actually signed against the one this page proposed — `address`,
   `chainIdentifier`, `toAddress`, `amount`, `tokenStandard`, `data`. Height,
   plasma, nonce and signature are the wallet's to fill in and are not compared.

Check 3 exists because check 2 cannot be made airtight: the extension re-reads
its selected account at signing time, offers no way to pin one, and announces a
change only sometimes. Somebody who switches account while the approval window is
open signs with the new one, and nothing tells the page. So the gate prevents what
can be prevented and the diff *names* what could not — the difference between
being told in the next sentence that the reclaim key is somewhere else, and
finding out in two days when the swap stalls. On a mismatch the swap's
`selfAddress` is rewritten to the account that really signed, because recording
the intended one would be recording a key that cannot open anything.

Absence and emptiness are kept apart in that diff. A field the wallet did not
report is one it cannot be judged on; a `data` field reported as empty is an HTLC
call stripped of its arguments, which is a plain send of the same ZNN to the
contract — it keeps the money and creates nothing.

### What is tested

`wasm/walletsync_test.go` stands up two stub go-zenon nodes over `httptest`:

- two nodes both calling themselves chain 69 with different momentums → refused,
  named as different chains
- the same chain, wallet node 10 momentums behind → accepted
- the same chain, wallet node 10,000 momentums behind → refused for drift
- wallet node unreachable → refused as unchecked, not accepted
- wallet configured for chain 1 while its own node serves 69 → refused
- wallet reporting no node URL → refused

`wasm/walletsent_test.go` covers the recording rules below.
`scripts/wallet-provider.mjs` covers the provider translation layer against a
stub — the reply shape, the error shape and the event names — with no chain, no
browser and no extension. It runs in CI.

---

## Three defects only a live run could find

Each sat behind the previous one: the sync gate correctly refused every earlier
attempt at chain 1, so nothing had ever reached the send.

**The block could not leave the page.** `postMessage` threw "could not be
cloned" on the Vue `ref` holding it. Fixed by `plain()`, above. No block could
ever have been sent.

**A published create left no id on the swap.** `handleWalletSent` verified the
create immediately and, when verification came back pending — which is the
*normal* outcome, since an account block needs a momentum or two and the wallet
answers the instant it publishes — saved the swap without the id. A reload lost
it, and a card seeing no Zenon HTLC goes back to offering **Create the HTLC**:
pressing that locks a second lot of ZNN that only expiry returns. The id is now
recorded before verification is attempted, with `Verified` false and the pending
reason written where the card's badge reads it.

**The counterparty could not find the preimage an unlock published.**
`FindPreimage` searched the account blocks of the payout address, on the
assumption that only the entitled party unlocks. **Nobody is entitled to
unlock** — that is the whole property this feature rests on. Fixed in
`wasm/znn/ledger.go` with a second search that assumes nothing about the caller:
the contract's own chain is walked, the payout picks out candidates, and the
caller's transaction — reached through the receive's `fromBlockHash`, because the
receive does not carry the call's arguments — is where the preimage is read. The
address search is kept and tried first: one page and no guesswork when the payee
did unlock themselves. The hash is what decides in both.

A fourth, from the v0.2.0 protocol change: `sendAccountBlock` used to resolve
`{signedTransaction}` and now resolves `{hash, block}`. The extension's legacy
shim forwards the new shape under the old name, so it is not a faithful shim for
this one call — and the failure lands *after* the ZNN is locked. Anything still
reading `signedTransaction.hash` has this.

---

## What has been driven live

Against `go-zenon` devnet, through the extension, read back off the chain
independently of the page:

**`htlc.Create`** — published, and `embedded.htlc.getById` confirmed every field:
`timeLocked` is the account that signed (so the only one that can reclaim),
`hashLocked` is the counterparty and **not** the signer, the amount is the agreed
one in base units, `hashType` is **1** (SHA-256 — what `OP_SHA256` needs, and the
thing Syrius's own screens hardcode to SHA3), `keyMaxSize` is 32, `hashLock` is
the swap's secret hash exactly, and `expirationTime` lands on the correct side of
the Bitcoin locktime for the role. Both cards then verified it through the
ordinary path.

**`htlc.Unlock`** — published by an account that was `timeLocked`, **not**
`hashLocked`. Afterwards the entry was gone (`data non existent`), the ZNN was an
unreceived block from the contract to `hashLocked` rather than to the caller, and
the block's data carried method id `d33791d3` with a 32-byte window that SHA-256s
to the swap's hashlock. That is proxy-unlock working live, which is the property
the whole browser-driven Zenon leg rests on. The counterparty then recovered the
preimage from the chain with nothing sent between them.

**`htlc.Reclaim`** — not driven, on any version. It is refused before expiry by
design, so a live run means waiting out a real leg. This is README gap 3.

The sync gate caught a real misconfiguration on its first live run — the wallet
reported chain 1 while the devnet node reported 69 — and no block was built.

---

## Checking it by hand

### The provider alone, nothing signed

Paste into the dev instance's console:

```js
const z = window.zenon
console.log('provider', {
  version: z?.version,
  isSyrius: z?.isSyriusExtension,
  hasSendAccountBlock: typeof z?.sendAccountBlock === 'function',
})
// Must NOT open a window. [] before the first connect, the address after it.
console.log('accounts', await z.getAccounts())
console.log('chain   ', await z.getChainId())
console.log('node    ', await z.getNodeUrl())
```

Then `await z.connect()` — approve it once — **reload the page**, and run the
three reads again. They must answer with the address and open no window: that is
the silent restore, and it is the whole prompt reduction in one check.
`await z.disconnect()` puts it back to the first-visit state.

### Reaching the Create button in one paste

The wallet button only appears on a swap with a Bitcoin contract to order its
Zenon expiry against, which normally means two profiles and several hand-offs.
For testing the wallet path alone, paste this into the dev instance's console —
it builds both sides of a regtest swap through the module and leaves the
initiator's card holding an outstanding `htlc.Create`.

It is the **Zenon-initiated** ordering on purpose. The other one — Bitcoin
initiates, the participant creates the Zenon HTLC in answer — does not offer a
create until the initiator's Bitcoin funding is mined and covers the agreed
amount, and `walletBlock` refuses to build one before then: a payment still in
a mempool is one its sender can replace, and that sender already holds the
secret. With nothing paid on regtest, that card shows why it is waiting rather
than a button. The initiator's Zenon leg goes first by design, so it needs no
Bitcoin to exist:

```js
const S = {network: 'regtest', btcEsplora: 'http://127.0.0.1:3002', znnUrl: 'http://127.0.0.1:35997'}
const call = (m, b) => window.ferryWasm.call(m, JSON.stringify(b)).then(JSON.parse)
const PEER = 'z1qqvwzz2xq7q5gwk6uhcddgrpxlfcyzc8rsu82s' // any real address; the HTLC pays it

// No zenonSelfAddress here on purpose: whichever account the wallet has selected
// is adopted, with a warning that it becomes the only address that can reclaim.
const a = await call('create', {
  role: 'initiator', leg: 'receive', amountSats: 400000,
  destAddr: 'bcrt1qw508d6qejxtdg4y5r3zarvary0c5xw7kygt080',
  zenonPeerAddress: PEER, zenonAmount: '10', settings: S,
})
const b = await call('create', {
  role: 'participant', leg: 'send', amountSats: 400000,
  destAddr: 'bcrt1q0rymrte6drs2nvjn73mqsl2meud7nv0dy6tgn4',
  secretHashHex: a.secretHashHex, zenonSelfAddress: PEER, zenonAmount: '10', settings: S,
})
const built = await call('counterparty', {id: b.id, pkhHex: a.key.pkhHex})
await call('audit', {id: a.id, contractHex: built.contractHex})
location.reload()
```

Two cards appear. The **initiator's** — the one that receives BTC — has the
Zenon create. On it: connect, read the sync verdict, expand *"The block, exactly
as the wallet will receive it"* and compare it against what Syrius shows, approve,
and watch the card record the returned hash as the HTLC id. It will read pending
for a momentum or two, then verify.

The swap cannot go further without a funded regtest contract and a real
counterparty; the Zenon leg is the part under test.

---

## A trip hazard

**Both builds share `ui/public/ferry.wasm`.** `npm run smoke` expects the
production module, so it fails after `npm run dev` until `npm run wasm` is run
again. It surfaces as the recovery check failing with `Cannot set properties of
null`, because the module wrote `ferry.dev.swap.*` and the script looked for
`ferry.swap.*`. Use `npm run smoke:dev` against a development module.

[syrius]: https://github.com/sol-znn/syrius-extension/releases/tag/v0.3.1

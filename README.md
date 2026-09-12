# Ferry — Bitcoin ↔ Zenon atomic swaps, as a static site

Trustless cross-chain swaps between Bitcoin and Zenon (NoM), with **no backend**
and **no full node**. It is a directory of static files. Everything that can
move a coin runs in your browser, or in a wallet you already trust — no key
for either chain is ever held by this page.

```
BTC ──── HTLC on Bitcoin ────┐
                             ├──── one secret, both legs ────► atomic
ZNN ──── HTLC on Zenon ──────┘
```

Either both legs settle, or both refund. There is no point at which a
counterparty, an operator, or this software can take your coins.

The swap logic — contract construction, the strict template parser, key
generation, signing, local script-engine verification, the leg-ordering rules,
the Esplora and Zenon clients — is Go, compiled to WebAssembly and running
inside the page. The UI is Vue. There is nothing to deploy but files.

---

## Why this is usable and older swap tooling wasn't

Cross-chain atomic swaps have worked since 2017. They stayed a curiosity because
every implementation required each participant to **run a fully synced full node
with wallet RPC**, and to **hand-copy data back and forth** while manually
running `initiate` / `participate` / `audit` / `redeem` in the right order.

The observation the design rests on:

> **Funding an HTLC is just a payment to an ordinary address.**

The contract is a P2SH address. Paying it is indistinguishable, to the paying
wallet, from paying anyone else. So the funding leg needs _no_ wallet support:
hardware wallets, phones, desktop wallets, even an exchange withdrawal.

The hard half — _spending out of_ a contract, which needs a custom script
witness almost no wallet can build — is handled by an **ephemeral keypair
generated per swap**. That key can only ever move coins already inside that one
contract.

| Step                      | Who does it                   | Wallet support needed          |
| ------------------------- | ----------------------------- | ------------------------------ |
| Pay into the contract     | Your own wallet               | **None** — it is a normal send |
| Spend out of the contract | Ferry, with the throwaway key | **None** — no wallet involved  |

Result: a swap needs **a browser tab and the wallets you already have.**

The Zenon side reaches the same conclusion by the same route, and it took
longer to see. Syrius's own HTLC screens cannot do this leg — they hardcode
SHA3-256, which a Bitcoin `OP_SHA256` preimage cannot satisfy, and cap expiry
at 24h. But they do not have to: the extension will sign **any account block a
page hands it**, an HTLC call is nothing but a send to an embedded contract
carrying encoded arguments, and `htlc.Unlock` may be called by anyone and pays
the address in the entry rather than the caller. So Ferry builds the block and
the wallet signs it, exactly as on the Bitcoin side.

---

## Non-custodial, precisely

- Ferry never sees, requests or stores a key from your wallet.
- The per-swap key it holds satisfies one of two branches of one contract, and
  both branches pay an address **you supplied**. No code path pays elsewhere.
- Funding goes from your wallet straight to the contract. Money never passes
  through an address Ferry controls.
- **Your Zenon key never reaches this page either.** The Zenon leg's calls —
  `htlc.Create`, `htlc.Unlock`, `htlc.Reclaim` — are built here as bytes and
  handed to the [**Syrius browser extension**][syrius], which signs with its own
  key, mines its own plasma and publishes through its own node. Ferry sees the
  block it proposed and the hash that came back, and it compares the two.
  [znn-cli] commands are printed for anyone whose wallet is not that extension.
  See [docs/EXTENSION-WALLET.md](docs/EXTENSION-WALLET.md).
- Verification is not a formality. Ferry refuses an HTLC that commits to the
  wrong hash, pays the wrong party **or a party this swap never named**,
  **holds a token nobody agreed to**,
  underpays, is about to expire, or sits on the wrong side of the Bitcoin
  locktime for the role it is playing. Nor does it report a check it could not
  perform as one that passed: if the agreed amount cannot be converted into base
  units — the node did not answer for the token, the figure will not parse — the
  HTLC is refused rather than verified with that comparison quietly missing.
- Nothing leaves the page except a chain query to the node **you** chose, and a
  signed transaction when you press a button. There is no server to log
  anything, because there is no server.

**What running in a browser costs** — read [docs/SECURITY.md](docs/SECURITY.md)
before using this with real money. The short version: the residual risk is
availability rather than theft, but availability is a sharp edge. Browser
storage is erased by "clear site data", by a private window closing, and by
reinstalling the browser, with no warning. So a signed refund transaction is
produced the moment funding is seen, the UI pushes it at you until you save it,
and there is an export for the whole set.

---

## Quick start

```sh
cd ui
npm install --allow-git=all   # nom-ui is a git dependency; npm 12 needs the flag
npm run build                 # compiles Go to wasm, then bundles the site
npm run preview               # serve dist/ locally
```

`npm run build` needs **Go 1.27+** and **Node 22+** on PATH. Output is `dist/` —
about 10 MB, of which 9.5 MB is the WebAssembly module (2.9 MB gzipped, fetched
once and then cached).

Deploying is copying `dist/` somewhere. See [docs/DEPLOY.md](docs/DEPLOY.md) for
GitHub Pages, which the included workflow does on every push to `main`.

### Two instances

`npm run build:dev` produces a second build in `dist-dev/` that starts on
regtest with a local Esplora shim and a local Zenon node, carries a **DEV**
badge, and keeps its swaps and settings under names of their own so the two can
share an origin without sharing state. `npm run dev` serves it. The instance is
one build-time flag that reaches both the Go module and the page; see
[docs/DEPLOY.md](docs/DEPLOY.md#the-two-instances).

### Before your first swap

Open **Nodes** and pick:

- **Network** — mainnet, testnet, signet or regtest. A swap keeps the network it
  was created on.
- **Esplora URL** — blank uses a public instance. Your own keeps your addresses
  off a third party's logs. On `regtest` the blank default is
  `http://127.0.0.1:3002`, which is where
  [the local shim](#against-a-local-regtest-chain) serves; there is no public
  instance for a chain only you have.
- **Zenon node URL** — _there is no default, on purpose._ Verifying a
  counterparty's HTLC is the one check where a dishonest answer costs money, so
  the node has to be one you chose. Until you set one, the Zenon leg cannot be
  verified and the app says so.
- **Session relays** — optional, and only used by [live sessions](#live-sessions).
  Blank uses a few well-known public Nostr relays.

**Use the WebSocket endpoint for Zenon.** A go-zenon node serves the same
JSON-RPC twice — HTTP on **35997** and a WebSocket on **35998** — and the scheme
you type picks between them:

```
wss://node.zenonhub.io:35998     # the endpoint Syrius and znn-cli use
https://your-node:35997          # HTTP JSON-RPC, if that node sends CORS headers
```

Both work. The WebSocket is the one to reach for, because a WebSocket handshake
is not CORS-preflighted: it connects to public nodes that the HTTP endpoint
cannot, where the failure is an error a page is not even allowed to read. The
Esplora URL has no such escape — it is a `fetch()`, so a self-hosted instance
usually needs `Access-Control-Allow-Origin` adding to its reverse proxy.

Either way, a page on `https://` can only reach `https://` and `wss://`. Mixed
content is blocked before the request is sent, which is why the local
development flow serves the app over plain http.
[docs/TESTING.md](docs/TESTING.md#01--the-zenon-node-must-answer-json-rpc-over-wss-or-https)
has the commands that check yours before you need it.

---

## Doing a swap

**Either side can initiate.** The initiator is whoever generates the secret,
and their leg takes the longer timelock — whichever chain that lands on. Ferry
works out which leg is which, what expiry your Zenon HTLC needs, and where the
preimage will surface, so you never reason about the ordering yourself.

Two things follow from who initiates, and they are the whole difference between
the directions:

|                          | Bitcoin initiates                 | Zenon initiates                     |
| ------------------------ | --------------------------------- | ----------------------------------- |
| Long leg (48h)           | the Bitcoin contract              | the Zenon HTLC                      |
| Preimage surfaces on     | Zenon, when the initiator unlocks | Bitcoin, when the initiator redeems |
| The ZNN sender must      | nothing — Ferry extracts it       | nothing — Ferry extracts it         |
| Zenon leg creatable with | the extension, or znn-cli         | the extension (over znn-cli's 24h)  |

**Auto Mode** takes the rest of it off your hands. Armed on a swap — after you
have read the trade and seen its contract audited, which is what arming it means
— the card stops being a list of things to press: it funds the contract, builds
and hands over every Zenon call, and redeems the Bitcoin once the preimage is
known. Three things it does not do, and they are the whole of why it is safe to
offer:

- **It never signs.** Funding and every Zenon call go to your wallet, which opens
  its own window and shows you the whole transaction. Autopilot gets you to that
  window; only you get past it.
- **It never skips a check.** Everything arriving from the other side is audited
  or verified against your own nodes exactly as before. It presses buttons; it
  grants nothing.
- **It never refunds**, and it waits where a person would. Abandoning a swap is a
  decision about whether to keep waiting, so that button stays yours; and it will
  not fund out of turn, redeem against a funding still sitting in a mempool
  where the sender can replace it, or lock ZNN against one. The first thing that goes wrong stops all of
  it and says so on the card, rather than being retried.

It is armed per swap and never globally, because approval is about this trade,
not about trading.

**The page asks the chains for you.** Every minute, each swap still in progress
gets the same call the _Refresh from chain_ button makes, and a Zenon leg still
waiting on an answer gets re-read too. That is what finds a funding, counts its
confirmations, pre-signs the refund, settles an HTLC that was published a moment
ago and is not readable yet, and recovers a preimage out of the transaction that
revealed it. The buttons all remain — this only means that not pressing one is no
longer the difference between a swap that has moved on and a swap that looks
stuck. Nothing is ever signed, funded, unlocked or spent on your behalf.

**And each side says when it has moved.** In a session, funding a contract,
creating an HTLC, unlocking one, reclaiming, redeeming and refunding are all
announced the moment they succeed — and the announcement carries no value, only
"a chain moved". The other browser then goes and reads the chain, retrying every
few seconds for about a minute, because a block published seconds ago is not
readable yet on either chain. That is where most of the dead time in a swap was:
every step waits on the previous one being noticed, and noticing was on a timer.

It matters most for the step that has no other help. When the counterparty
unlocks your Zenon HTLC they publish the preimage you need to claim your Bitcoin
— and the unlock deletes the entry, so it cannot be read back off it. Ferry finds
the transaction that removed it and reads the preimage out of that, checked
against your own swap's hash. **This works whether or not they tell you.** It
needs no CLI, no block explorer, and not even their Zenon address: a preimage
identifies itself by hashing, so with no address to filter on the search simply
walks the HTLC contract's own chain instead. A counterparty who unlocks and then
goes quiet has not taken anything from you that a node cannot give back.

### Sending BTC, receiving ZNN

1. Create the swap. If you initiate, Ferry generates the secret and commits to
   its hash; if the other side does, paste their hash in.
2. Send the counterparty your `swapoffer1:` string. It carries public data only
   — never the secret, never a key.
3. Paste back the pubkey hash they return; Ferry builds the contract.
4. **Pay the contract address from any wallet.**
5. Refresh — Ferry sees the funding and pre-signs your refund. **Download the
   recovery file now.** The card will keep asking until you do.
6. Their Zenon HTLC id arrives over the session, or you press _Find it_, or you
   paste it. However it gets there, Ferry verifies the hashlock, both parties,
   the token, the amount, the expiry ordering and `hashType` against your own
   node before it will act on it.
7. If you hold the secret, unlock the Zenon HTLC to take the ZNN — that
   publishes the preimage, which is how they claim the BTC. If you do not, wait
   for them to redeem your contract; Refresh extracts the preimage from Bitcoin
   automatically and you unlock with it.

### Receiving BTC, sending ZNN

1. Decode their offer and create your swap. If you initiate, Ferry generates the
   secret; otherwise use the hash from the offer.
2. Send them your pubkey hash. They build and fund the contract.
3. Paste their contract hex and **audit** it. Ferry refuses it unless it is
   genuinely redeemable by your key, its locktime sits on the correct side of
   your Zenon leg, _and_ it is the canonical encoding of the template — the
   same terms written with a longer push opcode than they need would leave you
   a redeem that standard policy refuses while their refund still works. This
   check reaches no node — it works entirely offline.
4. Create your Zenon HTLC. With the Syrius extension that is a button; without
   it, switch on **Show CLI commands** at the foot of the card for the `znn-cli`
   line, already carrying `hashType 1` and the expiry Ferry computed from the
   audited locktime. The commands are off by default now that they are one of
   two ways rather than the only one.
5. If you hold the secret, redeem the BTC with it once the contract is funded.
   If you do not, they unlock your Zenon HTLC — which deletes the entry, so the
   preimage cannot be read off it afterwards. Ferry finds the transaction that
   removed it and reads the preimage out of that instead; the field to paste one
   in by hand is still there for when that search comes up empty.

---

## How much is worth swapping

A contract is emptied by one transaction with one input and one output, and that
transaction's fee comes **out of the contract**, not out of the unlocking
wallet. So the amount you agree is never the amount the recipient receives — and
below the 546 sat dust limit they receive nothing at all, because no node will
relay the output. The refund branch hits the same wall, so the coins are stuck
in both directions.

Unlocking is about **323 vB**, which puts the hard floor at roughly **870 sat**
at the 1 sat/vB network minimum. The practical floor is well above it: a
contract sits for the length of its timelock and the fee market a day from now
is not today's.

Every place an amount is chosen or shown quotes this — the create form, the
funding prompt, the swap card and the Recover page — using the signer's own
transaction sizing, so a fee quoted before the money moves is the fee charged
after it did. A spend that is refused for dust names the highest rate that
_would_ have worked, and the Recover page has a button that fills it in.

---

## Live sessions

Setting a swap up by hand means passing four long hex strings between two chat
windows: a pubkey hash, a contract, an HTLC id and a funding txid. A **session**
moves them for you — one side presses _Start a session_ and reads out the code,
the other types it in.

They move by themselves. Each of the four is knowable by exactly one browser
until it is sent, so whichever side comes to hold one publishes it, once, without
being asked: the card that was waiting stops waiting, and nobody has to know
which button would have unblocked it. The Send buttons stay on the cards, because
a relay can drop a message and repeating a hand-off is the commonest way out of a
stall — but they read _Send it again_, because by the time you can see one the
value has already gone.

**Re-sync with counterparty** is the way out of the one stall that looks like
nothing at all. Publishing-once rests on each side remembering what it has
already said, and that memory lives in a tab: a reload, a rejoined session or a
relay that accepted an event and dropped it can leave one side certain it
delivered a value the other has never seen. Neither card can tell — a message
that did not arrive leaves no evidence — so rather than guess, the button throws
away both sides' record and says everything again. Everything that comes back
goes through the same verification it always does, so a value the other side
already had costs one line in a transcript.

**Nothing that arrives over a session is trusted.** Every value is applied
through exactly the check it went through when it was pasted by hand: a contract
is audited against your own swap and its locktime ordering, a pubkey hash is
matched, an HTLC id is verified against your Zenon node. Anything that does not
match is refused, the swap is left as it was, and the refusal is the loudest
line in the transcript. A session removes the typing, not the checking.

The preimage is the one exception, and it is not an oversight: there is no field
for it on the wire format and no code path that would accept one. Revealing it
early hands the other side both legs of the swap — and once it is no longer
early, it is already public on a chain, which is a better place to read it from
than a message. A preimage that arrives over a session is one somebody could have
made up; one taken off the chain was accepted by the contract holding the money.
The page looks for it there, on a timer, and fills it in when it appears.

Messages travel over public [Nostr](https://nostr.com) relays, because they are
the only free, redundant, no-signup network a page with no backend can use.
Several are used at once, so one being down does not end a session. What a relay
holds is ciphertext published under a pseudonymous key; both the key and the
encryption are derived from the session code inside the browser, and the code
itself never leaves it — so a relay can carry a conversation it can neither read
nor join.

> Treat the code like a password. Anyone holding it can read the session and
> post into it. Use a fresh one per swap.

---

## The board

Sessions assume you have already found somebody. The **board** is where you find
them: offers posted to the same Nostr relays, with nobody hosting the list.

A post says which way you want to trade, how much, at what rate, and how long the
offer stands — a day by default. It is signed by a key your browser holds, which
is what makes it **yours**: nobody else can edit it, withdraw it, or publish one
under your name without forging a signature. Editing is republishing, revoking is
republishing as void, and both work because the posts are Nostr *addressable*
events — a relay keeps exactly one per key and slot.

Optionally you can prove which wallet is behind your posts. One signature, once,
over a sentence naming your board key and the address; every post after that
carries the badge. Bitcoin works today through UniSat's `signMessage`. **Zenon
does not yet** — the Syrius extension signs account blocks and nothing else — so
the check is written, tested and waiting on a wallet that can sign a message. An
unproven address costs you a badge and nothing more.

Reading the board needs nothing. **Posting an offer or taking one needs both
wallets connected**, because a swap settles to four addresses and two of them are
yours — so an offer carries where its author wants each half paid, a take carries
the same back, and accepting one sends the author's current addresses over the
session. A trade agreed here finishes here, with nothing to paste into a chat
window, which is the easiest place in a whole swap to be handed somebody else's
address. Connecting asks each wallet for an address; it signs nothing and creates
no account.

Taking an offer does not publish anything you would not want public. The board
advertises a key; your browser mints a fresh session code, encrypts it so only
that poster can read it, and joins the room — along with your own addresses, so
those reach one person rather than a public relay. Several people can take the
same offer and the poster picks one — which is also why a session code is never
on the board itself. It *is* the room.

Each row carries a dot saying whether its author has the board in front of them —
green for online, grey for offline. A browser with a live post and a visible tab
republishes a signed beat every twenty seconds, and one counts as online for
three of those, so a closed tab goes grey within a minute. Nothing is sent when
you close it: a dot that lapses on its own is the only kind that can be right
without a server keeping score. Beating stops while the tab is hidden — which is
what lets the window be that short — and never starts at all unless you have a
live post, so this broadcasts nothing while you are only reading the board. It
answers *will they see my take now*, and nothing else.

When the swap that came out of an offer settles, the offer withdraws itself. When
one is abandoned, it expires.

> **Nothing on the board is vetted, and this page does not pretend otherwise.** A
> signature proves a post was not altered. A badge proves somebody holds an
> address. The "swaps completed" number is typed by the person claiming it.
> Everything that actually protects you happens in the swap — both legs settle or
> both refund, and every term is re-checked against the offer before either side
> funds.

Full design, the wire format and the Syrius seam: [docs/BOARD.md](docs/BOARD.md).

---

## Getting your money back

Three paths, in increasing order of effort, all of them independent of this site
still existing:

1. **The pre-signed refund.** Produced the moment funding is seen and included
   in every recovery file. After the locktime, paste it into any broadcast form
   — `https://mempool.space/tx/push`, your own node's `sendrawtransaction`,
   anything. Nothing else is needed.
2. **The Recover page.** Load a recovery file and rebuild the spend at a fee
   rate you pick, to an address you pick, or as a redeem if the preimage turned
   up. It reads no stored swap, contacts no node, and needs no settings — so a
   saved copy of this site works on a machine that has never been online.
3. **The export.** Every swap in this browser as one JSON document, importable
   into any other browser. This is the answer to "how do I move to another
   machine".

See [docs/SECURITY.md](docs/SECURITY.md) for what each of those protects against.

---

## Layout

```
wasm/           the swap logic, compiled to WebAssembly
  htlc.go       contract construction and a strict template parser
  txbuild.go    redeem/refund building, signing, local script verification
  swap.go       the swap model, which leg belongs to which role, active vs done
  manager.go    create, audit, refresh, redeem, refund, verify
  keys.go       the ephemeral per-swap keypair
  store.go      one localStorage record per swap, plus export/import
  storage_js.go the browser binding for that store
  recover.go    offline rescue from a recovery file
  estimate.go   what emptying a contract costs, priced before it is funded
  session.go    session codes, and the sealed messages two browsers exchange
  session_api.go  the session handlers: sign, seal, open — never decide
  board.go      the board key, wallet proofs, and why a post is signed not owned
  boardpost.go  one offer: its wire form, its signing, and every check on reading
                — and the presence beat, which is the same shape with no payload
  boardstore.go the board's own records, kept apart from the swaps
  board_api.go  the board handlers: publish, read, withdraw, take, beat
  walletblock.go  a swap plus an action, as an account block for the extension
  walletsync.go   the gate: same chain, same account, a node that answers
  api.go        the call table the page talks to
  env.go        which instance this build is, set by a linker flag
  main_js.go    the JavaScript bridge
  httpx/        one HTTP call, over fetch() in the browser and net/http elsewhere
  chain/        the Esplora backend
  znn/          read-only Zenon JSON-RPC client, over HTTP or a WebSocket
    ledger.go   account-block reads: what became of an HTLC, and its preimage
    htlcabi.go  the three htlc calls, ABI-encoded without a go-zenon dependency
    address.go  an ed25519 key to the z1 address it spends from
    ws_js.go    JSON-RPC over the browser's WebSocket
ui/             Vue 3 + Vite + Tailwind 4 + nom-ui
scripts/
  build.mjs     compile the module, stage Go's shim, bundle the site
  smoke.mjs     runs the shipped module under Node and exercises the call table
  wallet-devnet.mjs    builds a whole swap against a live devnet and takes the
                       htlc.Create block apart, field by field
  wallet-provider.mjs  a stub Syrius provider, so the wallet path has a test
  regtest-esplora.mjs  an Esplora API over a regtest node, for local runs
dist/           build output: the entire deployable
dist-dev/       the same, built as the development instance
```

---

## Testing

```sh
cd wasm && go test ./...              # contract template, leg ordering, script engine
cd ui   && npm run smoke              # the shipped .wasm, driven under Node
cd ui   && npm run smoke:dev          # the same, for the development build
cd ui   && npm run typecheck && npm run lint
```

To test it the way a **user** would — against a deployed site, real wallets and
real chains, including the refund paths nothing here can reach — follow
[docs/TESTING.md](docs/TESTING.md). That is the document that decides whether
this is releasable; the ones below decide whether it is correct.

`go test` runs on your host platform, not on `js/wasm` — the build tags keep
every file except the browser bindings compilable there.

`npm run smoke` loads the actual `ferry.wasm` that will be deployed, stubs
`localStorage`, and asserts on the seam most likely to break: that the module is
the instance the build asked for and stores swaps under that instance's
namespace, that a swap survives the JS/Go boundary, that the store persists
between calls, that the offer round-trips and a malformed one is refused field by
field, that a recovery file rebuilds a signed refund with the right branch and
refuses the wrong one, that a recovery file inconsistent with its own contract —
in its address or its locktime — is refused, that a contract redeemable by
someone else fails audit, that export/import does not clobber a newer record and
refuses a malformed one without losing the healthy records beside it, and that
unknown methods and stray request fields are refused rather than ignored.

Both run in CI before anything is published.

### Against a local regtest chain

The app speaks Esplora and nothing else, and a Bitcoin Core regtest node offers
Core RPC and no Esplora — so there is nothing for a local build to point at.
`scripts/regtest-esplora.mjs` is that missing adapter: it serves the six
endpoints `wasm/chain/esplora.go` calls, backed by the regtest node's RPC, with
the CORS headers a browser requires.

```sh
node scripts/regtest-esplora.mjs      # http://127.0.0.1:3002, needs the regtest node up
```

It has no dependencies and holds its index in memory, walking the chain once and
then following the tip; the mempool is a layer on top, so unconfirmed funding is
visible the way a real Esplora shows it. It refuses to serve anything but
regtest. `http://127.0.0.1:3002` is already what **Node settings** falls back to
when the network is regtest, so with it running the field can be left blank.

The Zenon side needs a `go-zenon` devnet (`make devnet-up`), whose RPC already
sends permissive CORS headers, and either the Syrius extension or `znn-cli` for
the two Zenon operations this page deliberately cannot perform.

A whole swap needs four things running — the regtest node, this shim, the devnet,
and the site — and **two participants, which means two browser profiles**. Not
two tabs: storage is per origin, so two tabs are one participant with the swap
twice. A second profile (or a private window, or a second `--user-data-dir`) is
what makes the counterparty a counterparty. Everything that passes between them
— the offer, the pubkey hash, the contract hex, the HTLC id, and finally the
preimage — moves by copying it from one window and pasting it into the other,
which is exactly what two humans would be doing.

Funding comes from `bitcoin-cli sendtoaddress` to the contract address, because
that is the point: it is an ordinary send from an ordinary wallet. Confirmations
come from `bitcoin-cli -generate 1`. Nothing here needs to know either happened —
press **Refresh from chain** and the page finds them.

Full walkthrough: [docs/TESTING.md](docs/TESTING.md#appendix--a-local-devnet-run).

---

## Why Esplora only

Bitcoin Core RPC would be the private alternative to a public explorer, and it
does not survive running in a page: Core's JSON-RPC sends no CORS headers,
expects HTTP Basic credentials a web page cannot safely hold, and the call
funding detection needs — `scantxoutset` — walks the entire UTXO set on every
poll.

Privacy is recovered the other way: point **Node settings** at _your own_
Esplora instance. That is a URL change rather than a different protocol, and it
keeps your swap addresses off a third party's logs just as effectively.

---

## Status and known gaps

Proof of concept, but no longer an untried one. A full cross-chain swap has been
settled from the browser build: 400,000 sat against 10 ZNN, on a Bitcoin regtest
node and the `go-zenon` devnet, driven entirely by clicking the actual page in
two separate browser profiles — one per participant, which is what separate
`localStorage` makes them. Both legs settled against one secret hash, the funding
was seen while still unconfirmed, the refund was pre-signed on sight, and an HTLC
committing to a different hash was refused. The Zenon leg has been published
through the Syrius extension and read back off the chain independently.

What that run does **not** cover is a real fee market, a real mempool under
load, and the timeout paths — see gap 4. Nothing here has been used with real
money on mainnet.

### Two things that used to stop a release, and no longer do

Both were written down as outside this code — not defects, and not fixable by any
change here. Both turned out to be fixable, and the reason each looked immovable
is the part worth keeping: in both cases the thing that was missing existed
already, in a form nobody had recognised as the answer.

- ~~**There may be no public Zenon node this can reach.**~~ Closed by a
  WebSocket transport in `wasm/znn/client.go`: the scheme picks the transport,
  and `wss://` reaches the endpoint the ecosystem already publishes on 35998.
  It is the better one to reach for anyway, because a WebSocket handshake is
  not CORS-preflighted.
- ~~**The Zenon leg needs a Dart CLI, cloned and run from source.**~~ Closed by
  the [Syrius extension][syrius]. Its own HTLC screens genuinely cannot do this
  leg — they hardcode SHA3-256 and cap expiry at 24h — but those screens are not
  what gets used. The extension signs an arbitrary account block on request, and
  that is all an HTLC call is.
  [docs/EXTENSION-WALLET.md](docs/EXTENSION-WALLET.md) has the protocol, the
  checks either side of it, and the live devnet runs.

### Gaps in this code, in priority order

1. **Swap records are not encrypted at rest, and rest is `localStorage`.**
   Any script that runs on this origin can read them, which on a shared host
   like `<user>.github.io` means any page that user publishes there.
   [docs/SECURITY.md](docs/SECURITY.md) covers this properly. Passphrase
   encryption is the obvious next step.
2. **Live sessions have had no adversarial review.** The envelope is signed and
   AEAD-encrypted under a key derived from the session code, every value is
   re-checked by the handler that always checked it, and there is no field for a
   preimage — but the feature is new, and a relay is an untrusted party in a
   position nothing else in this app gives anybody. It is optional and off until
   somebody starts one, which is the right default for now.
3. **The extension path has been driven on devnet, not on mainnet, and the
   reclaim call has never been driven at all.** `htlc.Create` and `htlc.Unlock`
   have both been published from the page against `go-zenon` devnet and read
   back off the chain. `htlc.Reclaim` is refused before expiry by design, so
   exercising it live means waiting out a real 24h leg, which no run has done.
4. **Timeout paths are only partly tested.** Refund construction is verified by
   the script engine and refused before locktime, but no run has waited out a
   real expiry on either chain. A refund is only _relayable_ once the locktime
   is behind the chain's median time past, which trails real time by about an
   hour.
5. **There is still no RBF or fee bumping.** Amounts are now quoted against the
   fee market before funding and a refused spend names a rate that works, but a
   refund signed cheaply and then stranded by a spike near its deadline remains
   a real risk. The Recover page is the manual answer: rebuild at a higher rate
   and rebroadcast.
6. **The Esplora backend has been exercised end to end, but not by a test that
   lives here.** Every Go test runs against fixtures or the script engine, never
   a network, and `npm run smoke` stubs the chain. The backend's live coverage
   comes from driving the built page against a regtest Esplora by hand — see
   [Against a local regtest chain](#against-a-local-regtest-chain). Committing
   that run as a script, so CI can do it, is the gap.
7. **2.9 MB gzipped on a first visit.** That is the Go runtime, `btcutil`'s
   address handling and the script engine. It buys not rewriting Bitcoin
   signing in JavaScript, which is the right trade for this program, but it is
   a real cost. [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) has the breakdown
   and what was already done to halve it.

---

## Documentation

The first row is the only one a user of a deployed copy can reach, which is why
it exists: `dist/` is the site and nothing else, so none of the files below it
ship. It is the user's half — how a swap goes, what tooling the Zenon leg needs,
where the keys live, what to do when something is refused — and it is written
rather than rendered from these files, because these are addressed to somebody
reading the repository.

|                                                        |                                                                          |
| ------------------------------------------------------ | ------------------------------------------------------------------------ |
| **`#/docs` in the app**                                | How a swap works, for the person doing one. In the build, no clone needed |
| [docs/TESTING.md](docs/TESTING.md)                     | How to test a deployment as a user would, and what still blocks release   |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)           | How the app is put together, and what running in a browser forced         |
| [docs/SECURITY.md](docs/SECURITY.md)                   | The browser threat model — read this one                                  |
| [docs/EXTENSION-WALLET.md](docs/EXTENSION-WALLET.md)   | The Syrius extension boundary, the sync gate, and the live runs           |
| [docs/BOARD.md](docs/BOARD.md)                         | The offer board: addressable posts, wallet proofs, sealed takes, presence |
| [docs/REVIEW-2026-09.md](docs/REVIEW-2026-09.md)       | A full defect review, what was found and what was fixed                   |
| [docs/DEPLOY.md](docs/DEPLOY.md)                       | The two instances, GitHub Pages, other hosts, and running it offline      |

The Bitcoin contract script is the well-established atomic-swap template
popularised by `decred/atomicswap`, kept deliberately so any tool that
understands that template can audit or spend a contract Ferry produces.

## License

ISC — see [LICENSE](LICENSE).

[syrius]: https://github.com/sol-znn/syrius-extension/releases/tag/v0.3.1
[znn-cli]: https://github.com/zenon-network/znn_cli_dart

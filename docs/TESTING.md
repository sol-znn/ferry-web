# Testing Ferry the way a user would

A recipe for exercising a deployed Ferry the way a stranger with a Bitcoin
wallet and some ZNN would. It is written to be run against the **production
instance** — the real site, real chains, real wallets — because every other kind
of test this repository has already passes, and the remaining risk is entirely in
the parts a unit test cannot reach: real fee markets, real node availability,
real counterparties, and the tooling a user has to supply themselves.

For the developer loop, see [the appendix](#appendix--a-local-devnet-run).

Work through it in order. Stage 0 decides whether the rest is possible at all.

| Stage | What it proves | Costs |
|---|---|---|
| [0. Preflight](#stage-0--preflight) | A user can reach the nodes this needs | nothing |
| [1. Cold open](#stage-1--cold-open) | The site works for someone who has never seen it | nothing |
| [2. Rehearsal](#stage-2--rehearsal-on-signet) | A whole swap settles, two participants | nothing |
| [3. Production run](#stage-3--the-production-run) | It works where money is real | dust + fees |
| [4. Refund drill](#stage-4--the-refund-drill) | The path nothing has ever tested | fees |
| [5. Loss drills](#stage-5--loss-drills) | Recovery survives losing the browser | nothing |

Record results in [the sign-off sheet](#sign-off) at the bottom. A stage that
cannot be run is a finding, not a skip.

---

## Before you start: the two things a user must supply

Ferry holds no Zenon key and no Bitcoin key, so two pieces of the swap are the
user's own tooling.

### Your Bitcoin wallet — any of them

The funding leg is an ordinary payment to an ordinary P2SH address. Sparrow,
Ledger, Trezor, Electrum, BlueWallet, a Coinbase withdrawal — all fine, nothing
to install, no PSBT, no script support. Payouts go to a fresh receive address you
give Ferry, and legacy, P2SH, segwit and taproot all work.

This half genuinely is "a wallet you already have".

### Your Zenon wallet — the Syrius **extension**, not the desktop app

Install [syrius-extension v0.3.1][syrius] or later. It injects a provider at
`window.zenon` and will sign and publish an arbitrary account block on request,
which is all an HTLC call is — so create, unlock and reclaim each become one
button, with your key never leaving the extension.

> **Syrius's own P2P swap screens cannot do this leg.** They are a ZNN↔ZNN
> feature and they are wrong in three independent ways, any one of which is
> fatal:
>
> | What Ferry needs | What those screens do |
> |---|---|
> | `hashType 1` (SHA-256), because Bitcoin hashes with `OP_SHA256` | hardcode SHA3-256 |
> | an expiry of ~48h when the Zenon leg is the initiator's | default 8h, cap at 24h |
> | unlock an arbitrary HTLC by id and preimage | unlock only HTLCs from their own store |
>
> None of that matters, because Ferry does not use those screens. It builds the
> block itself and asks the extension to sign it. Ferry also refuses a SHA3 HTLC
> outright (`wasm/znn/client.go` checks `hashType`), so the mistake fails safe.

**Without the extension**, every action is still printed as a [znn-cli][znn-cli]
command — but `htlc.create` there accepts 1–24 hours and nothing else, which
rules out every swap where Zenon is the initiating side and takes the 48h leg.
The cap is znn-cli's, not the chain's.

---

## Stage 0 — Preflight

Ten minutes, no money, and it decides whether stages 2–4 are possible. **Every
check here is about reachability, not about Ferry.** If one fails, the swap will
fail later at a point where money is already committed.

### 0.1 — The Zenon node must answer JSON-RPC, over wss or https

This is the check most likely to stop the release, so do it first.

Ferry's Zenon client (`wasm/znn/client.go`) dispatches on the URL scheme:
`ws://`/`wss://` goes over a pooled browser WebSocket (`znn/ws_js.go`), anything
else over HTTP POST JSON-RPC to port **35997**. Syrius and `znn-cli` both use the
**WebSocket** endpoint on port **35998** — the same one Ferry speaks — and Syrius
will not accept a URL that is not `ws://` or `wss://`.

**Prefer wss.** It is the URL people already have, and a WebSocket handshake is
not CORS-preflighted, so it reaches public nodes an HTTP endpoint without CORS
headers cannot. Confirm it answers:

```sh
wscat -c wss://<your-zenon-node>:35998
> {"jsonrpc":"2.0","id":1,"method":"ledger.getFrontierMomentum","params":[]}
```

**Pass:** a JSON body with a `result` carrying `height` and `timestamp`. (Any
WebSocket client works; `wscat` is just convenient.)

The HTTPS JSON-RPC endpoint on 35997 still works, and is worth confirming too
if you expect users to set it — it needs CORS to be reachable from a browser:

```sh
curl -s -i -X POST -H 'Content-Type: application/json' -H 'Origin: https://example.com' \
  -d '{"jsonrpc":"2.0","id":1,"method":"ledger.getFrontierMomentum","params":[]}' \
  https://<your-zenon-node>:35997 | grep -i access-control-allow-origin
```

**Pass:** an `Access-Control-Allow-Origin` header. A stock go-zenon node sends
`*` (`node/defaults.go` sets `HTTPCors: ["*"]`), so a node that does not is one
behind a reverse proxy that dropped it.

Ways this still fails:

- **The node is `ws://` (not `wss://`) or `http://` (not `https://`).** The
  production site is served over `https://`, and a page on https cannot call a
  plaintext endpoint: the browser blocks it as mixed content before the request
  is sent. A local node is reachable only from a page that is itself on http —
  which is the development instance, not the production one.
- **The https endpoint has no CORS header**, and you are relying on it rather
  than wss. Switch to wss, which needs none.

Working public endpoints as of 2026-09: `wss://node.zenonhub.io:35998` and
`https://node.zenonhub.io:35997`. Re-run the check above against whichever node
you intend users to use before releasing — infrastructure changes.

### 0.2 — The extension and the page must agree

The extension has its own node, chain and account, and Ferry cannot set any of
them. Point the extension at the **same node** you set in Ferry.

**Pass:** the sync verdict on a swap card reads *"Same chain as this page — chain
N, momentum M matches on both nodes"*. Anything else is the gate working: it
names which of the three disagreed, and no block will be built until it does.

A wallet whose node this page cannot reach — an `http://` node without permissive
CORS, or any non-`https` node while Ferry is on https — cannot be used at all.
There is no override, deliberately: a node that cannot be read is
indistinguishable from a node on somebody else's chain.

### 0.3 — The Esplora instance must answer

```sh
curl -s https://blockstream.info/api/blocks/tip/height          # mainnet default
curl -s https://mempool.space/signet/api/blocks/tip/height      # signet default
```

**Pass:** a plausible block height. Both public instances send CORS headers. If
you are pointing at your own Esplora, run the `Origin:` variant from 0.1
against it too, and confirm it is on https.

### 0.4 — The site serves what it should

```sh
curl -sI https://<your-site>/ferry.wasm | grep -i 'content-type\|content-encoding'
```

**Pass:** `application/wasm`, and ideally `content-encoding: gzip` or `br`. The
loader compiles from bytes so a wrong MIME type still works, but 9.5 MB
uncompressed on every first visit is a bad first impression.

```sh
curl -sI https://<your-site>/ | grep -i 'x-frame-options\|content-security-policy'
```

**Pass:** `X-Frame-Options: DENY` or a `frame-ancestors 'none'` header. The page
carries its own CSP in a `<meta>` tag, but `frame-ancestors` is ignored in meta
form, so this one has to come from the host. See [DEPLOY.md](DEPLOY.md).

### 0.5 — The origin is not shared

Open the site and check the address bar. If it is `https://<user>.github.io/…`,
**stop and read [SECURITY.md](SECURITY.md).** Browser storage is scoped to the
origin, not the path, so every other page that user publishes on that domain can
read the swap keys this app stores. A custom domain, or a user site that hosts
nothing else, removes the problem entirely and is the single most valuable
deployment choice available.

### 0.6 — The build is the one you think it is

The header shows an amber **DEV** badge on the development instance and nothing
on production. Confirm you are on the one you mean to be testing. If the header
shows a red line saying the page and the signing module disagree about which
instance they are, the deployment is broken — do not test against it, rebuild.

---

## Stage 1 — Cold open

Test the first ninety seconds, in a browser profile that has never seen this
site. Use a fresh profile, not a private window — you want storage that
persists.

1. Open the site. **Pass:** "Loading the signing engine…" is replaced by the
   app, with nothing else on the page demanding a decision — the new-swap panel
   does not open itself, even with no swaps yet, so there is no dialog to
   answer before you have chosen to do anything. Time the load; on a cold cache
   this is a ~2.9 MB download.
2. Read the header without touching anything. **Pass:** three status chips —
   network, bitcoin (a tip height, green dot) and zenon reading `no node set`
   with an amber dot. Each chip's ⓘ explains itself on hover.
3. Open **Nodes**. **Pass:** the network is `mainnet`, the Esplora field is blank
   and shows the public default as its placeholder, and the Zenon field is blank.
   That last one is deliberate and the dialog explains why.
4. Set your Zenon node URL from 0.1 and save. **Pass:** within a few seconds the
   header changes from `no node set` to a Zenon height. If it shows a Zenon
   error instead, go back to 0.1 — the error text names the cause.
5. Open **Recover** without configuring anything. **Pass:** the page loads and
   asks for a file. It must not require settings, a node, or a stored swap.
6. Reload the page. **Pass:** your node settings survived, and the page is
   still just the list — empty, with no dialog and no panel open.

Then check the failure this app is most exposed to:

7. Open the site in a **private window**. **Pass:** either it works, or it says
   plainly that storage is unavailable and refuses to start. What it must never
   do is let you create a swap whose refund key cannot be saved.

Then the gate itself, which sits in front of the FORM rather than the Create
button, and asks fresh every time the panel is about to open rather than once
per browser:

8. Press **New swap**. **Pass:** the panel does not appear — a dialog does
   instead: experimental software, keys in this browser, nothing reversible —
   and it cannot be dismissed by clicking away, pressing escape, or a close
   button. The only ways out are the two answers, and neither has revealed an
   empty form yet.
9. Press **Decline**. **Pass:** the dialog closes and the panel stays shut — the
   button still reads **New swap**, not Close. Nothing was created and nothing
   partially exists.
10. Press **New swap** again, then **Accept**. **Pass:** only now does the panel
    appear, blank and ready to fill in — accepting reveals the form, it does not
    create anything by itself. Fill it in and press **Create swap**. **Pass:**
    it creates directly, with no second dialog in the way.
11. Close the panel and press **New swap** again, for a second swap. **Pass:**
    the terms dialog appears again. It is asked every time the panel opens, not
    remembered from the last swap.
12. Reload the page and press **New swap**. **Pass:** the dialog is still there.
    Nothing about reloading, or about how many swaps came before, skips it.

### The Zenon fields refuse the wrong kind of string

The bug this checks for: a Bitcoin address pasted into "Your Zenon address"
used to be accepted verbatim — trimmed and stored, nothing more — and sat
there looking normal until the Zenon leg was verified against a payee that was
never a real address. Accept the terms dialog, fill in a valid destination
address, and try each of these before pressing Create:

1. Paste a **Bitcoin address** (`bc1q…` / `tb1q…` / a regtest one) into **Your
   Zenon address**, then press **Create swap**. **Pass:** refused, naming the
   field and the address, e.g. `your Zenon address: "bc1q…" is not a Zenon
   address: its prefix is "bc", not "z"`. No swap appears in the list.
2. Same paste into **Their Zenon address**. **Pass:** refused the same way,
   naming *their* Zenon address instead of yours.
3. Paste a real **Zenon address** (`z1q…`, not a token) into the **Zenon
   token** field under Advanced. **Pass:** refused — `"z1q…" is not a Zenon
   token: its prefix is "z", not "zts"`. Both fields are bech32 and both start
   with the same letter, which is exactly the paste this has to catch.
4. Clear the Zenon fields back to blank and create the swap. **Pass:** it
   succeeds — these three fields stay optional, only a value that is given has
   to be well-formed.
5. Fill in a real `z1…` address on both Zenon fields and `zts1znnxxxxxxxxxxxxx9z4ulx`
   (ZNN's own standard) as the token, and create. **Pass:** it succeeds.

---

## Stage 2 — Rehearsal on signet

A complete swap, both legs, two participants, on chains where a mistake costs
nothing. **Do not skip this before stage 3.** Everything you will get wrong on
mainnet, you will get wrong here first, for free.

### What "two participants" means

Storage is per origin. Two tabs are one participant with the swap open twice —
they share the same `localStorage` and will overwrite each other. You need **two
browser profiles**, or two machines, or one of each. If two people are running
this, better still: that is the real test, because every handoff below is a
copy-paste between humans and the friction is part of what you are measuring.

Everything that passes between the sides — the offer, the pubkey hash, the
contract hex, the HTLC id, and finally the preimage — moves by copying it out of
one window and pasting it into the other.

### Setup

- **Bitcoin:** signet. Get coins from a signet faucet into a wallet you control.
- **Esplora:** leave blank; it defaults to `https://mempool.space/signet/api`.
- **Zenon:** there is no Zenon "signet". Use the same Zenon node both sides will
  use in stage 3, or a devnet — but note that using a devnet means the Zenon
  half of this rehearsal is not testing the node you will actually ship against,
  and you should repeat 0.1 for the real one.
- **Amounts:** small enough not to care, large enough to exceed dust and pay a
  fee. 100,000 sat against a token amount you agree between yourselves.

### Run it: Bitcoin initiates

Side A sends BTC and receives ZNN. Side B sends ZNN and receives BTC.

**A:**

1. **Nodes → network: signet.** Set the Zenon URL. Save.
2. **New swap → Start one.** Pick **Send BTC**, leave "who starts this swap"
   on **I am starting it**, set the amount, and give **your own BTC address**
   (a fresh one from your wallet — this is where your refund or your change of
   mind ends up, and both contract branches pay here and nowhere else). Add
   both Zenon addresses and the Zenon amount. The timelock lives under
   **advanced options** and stays on `auto`.
3. **Create swap.** **Pass:** a card appears with a 64-hex secret hash, in state
   `draft`.
4. Copy the offer string (**Offer string**) and send it to B. **Pass:** it starts
   `swapoffer1:` and — check this by eye, decoding it is a base64url blob —
   carries no key material.

**B:**

5. **New swap → Paste their offer**, paste it, **Decode and fill the form**.
   **Pass:** it switches to the form with a green banner naming your side, and
   the direction (**Receive BTC**), the role (**I am answering**), the amounts,
   their Zenon address and the secret hash are all filled in.
6. Add your own BTC address and your own Zenon address, then **Create swap**.
7. Copy your **pubkey hash** (40 hex) from the card and send it to A.

**A:**

8. Paste it and **Build contract**. **Pass:** a P2SH address (signet P2SH starts
   `2…`), the contract hex, and a locktime 48 hours out — A is the initiator, so
   A's leg is the long one.
9. **Pay the contract address from your own wallet.** An ordinary send. Note the
   fee rate you chose.
10. **Refresh from chain.** **Pass:** funding is seen *while still unconfirmed*,
    and a pre-signed refund appears. The card now nags you to save the recovery
    file.
11. **Download recovery file.** Do it now, not later. Open it and confirm it
    contains the contract, the locktime, and a WIF private key.

**B:**

12. Get the contract hex from A, paste it, **Audit contract**. **Pass:** it is
    accepted, and the card now shows the Zenon expiry you must use. **This check
    reaches no node** — try it with your network off to confirm.
    - Deliberately fail it once: change one hex character and audit again. It
      must be refused, naming the reason.
13. **Create the HTLC** in the Zenon panel. **Pass:** the sync verdict is green,
    the summary names the payee, the amount and the expiry, and expanding *"The
    block, exactly as the wallet will receive it"* shows a block whose fields
    match. Syrius opens its own window; read the block there before approving.
    - The expiry should be around 24 hours: the Bitcoin leg is the initiator's
      here, so the Zenon leg is the participant's and expires first.
    - Cross-check the printed command: switch on **Show CLI commands** and
      confirm the `znn-cli htlc.create` line carries hash type `1`, the agreed
      token and amount, A's Zenon address as the recipient, and the swap's
      secret hash.
14. **Pass:** the card records the transaction hash as the HTLC id without you
    copying anything, and shows it as pending for a momentum or two before it
    verifies. Reload the page here — the id must survive.

**A:**

15. Paste or find the HTLC id, **Verify HTLC**. **Pass:** it verifies — hashlock,
    parties, token, amount and expiry.
    - Deliberately fail it once: have B create a second HTLC with hash type `0`
      via `znn-cli`, and verify that id. It must be refused for the hash type.
      This is the check that stops the SHA3 mistake from becoming a loss.
16. Only now, unlock the Zenon HTLC to take your ZNN, with the preimage Ferry
    holds. **Pass:** it unlocks. Say nothing to B about the preimage — finding
    it is what step 18 tests.
17. Collect the payout. Unlocking sends the ZNN as an unreceived block; it is
    not in your balance until the wallet receives it (or `znn-cli receiveAll`).

**B:**

18. In this direction the preimage surfaces on **Zenon**, and unlocking deleted
    the entry — so nothing can read it back off the entry itself. Leave B's card
    alone and wait. **Pass:** within a minute or so the preimage appears on the
    card with no one having copied anything, because Ferry found the transaction
    that removed the entry and read it out of that. This is the check that A
    never has to tell B the secret: A said nothing in step 16, and the chain that
    enforced the unlock is where B got it.
    - Confirm it is the chain and not the session doing this: run the whole
      swap **with no session open at all**. It must still arrive. A preimage is
      the one value that has no field on a session message, so if it ever
      appears faster with a session open, something is sending it that should
      not be.
    - **Submit preimage** is still on the card for when the search comes up
      empty. Paste a wrong 64-hex string into it: it must be refused, because it
      does not hash to the swap's secret hash.
19. **Redeem with secret.** **Pass:** a transaction is built, signed, verified
    against the local script engine, and broadcast. Watch it confirm.

**Both:** the swap moves to `redeemed`, then **Archive** it and confirm it moves
to History.

### Then run it the other way

Repeat with **Zenon initiating**. This is a different code path and a different
set of instructions, and it is the direction where:

- the Zenon leg is the **long** one, around 48 hours — over `znn-cli`'s 24h cap,
  so the extension is the only tool that can create it. **Pass:** the card says
  so instead of printing a command that would be rejected;
- the preimage surfaces on **Bitcoin**, and Ferry extracts it automatically on
  **Refresh from chain** rather than asking you to paste it.

**Pass:** the preimage appears in the receiving side's card without anyone
copying it by hand.

### Then run it again with a session open

Everything above is the manual path: two windows, values carried across by hand.
Run it once more with a session, because the session's job is that nobody carries
anything — and a session that only half works looks exactly like one that works,
right up until a swap sits waiting on a value both browsers already had.

Start the session on A before creating the swap, join it from B, and then **touch
no Send button at any point**.

**Pass:** each of the four hand-offs appears in the other side's transcript
within a second or so of the browser holding it coming to hold it — B's pubkey
hash on creation, A's contract when it is built, A's funding once a refresh sees
the output, B's HTLC id once B's own node has verified it. Each arrives as
`Accepted …`, never `REFUSED`.

Then check the things that are easy to get wrong:

- **The HTLC id waits for a verdict.** B publishes the create through Syrius and
  the card says "awaiting confirmation". **Pass:** nothing is sent to A during
  that window. It goes only once B's node has verified it, so A's verification
  of the same id cannot fail for the one reason that is not A's problem.
- **Nothing is sent twice.** Leave both pages open for five minutes. **Pass:**
  no hand-off appears in either transcript a second time, and the Send buttons on
  the cards read _Send it again_.
- **A repeat is still possible.** Press one. **Pass:** it goes, and the other
  side accepts it again rather than treating it as a conflict.
- **Two swaps at once do not cross.** With the session attached to swap 1, open
  swap 2 in the same browser. **Pass:** swap 2's card offers to attach rather
  than to send, and nothing of swap 2's ever appears in swap 1's room.
- **The preimage never travels.** Grep both transcripts for the secret after the
  swap settles. **Pass:** it is in neither. It is on a chain, which is where the
  other side got it.
- **Each side announces that it moved.** Watch the other transcript while you
  fund, create, unlock, reclaim, redeem or refund. **Pass:** a `They moved: …`
  line appears within a second or so, followed by the swap updating — usually
  after a few seconds of retrying, because a block that new is not readable yet.
  The line must describe the action and carry no value.
- **Re-sync repairs a lost hand-off.** Reload one side mid-swap — which throws
  away its record of what it has sent — then press **Re-sync with counterparty**
  on the other. **Pass:** both transcripts fill with the whole set again, every
  line `Accepted`, and the swap is in the same state it was. Then press it on a
  swap where nothing is wrong: it must be harmless, not a second contract or a
  second HTLC.

### The uncooperative counterparty

The scenario the chain lookups exist for. You created the Zenon HTLC; they
unlocked it, which publishes the preimage you need to claim your Bitcoin, and
then they say nothing.

1. Run a swap to the point where they can unlock your Zenon HTLC.
2. Have them unlock it **with no session open at all**, and send you nothing.
3. Leave your card alone. **Pass:** the preimage appears on it, and _Redeem with
   secret_ becomes available. You did not run a CLI, open an explorer, or ask
   them for anything.
4. Do it again on a swap record whose **Zenon peer address was never filled in**
   — it is an optional field, and this is the shape that used to refuse to look.
   **Pass:** it still arrives. The search falls back to walking the HTLC
   contract's own chain, because a preimage identifies itself by hashing.

**Fail** is any outcome where the ZNN sender needs the ZNN receiver's
cooperation to finish. Everything after the unlock is public.

### The wallet boundary

The extension is a separate program, and these are the failures that only appear
when it is a real one.

1. **Switch account in Syrius mid-flow**, with a prepared block on screen.
   **Pass:** the prepared block is discarded and the card says the wallet moved,
   rather than handing over a block built for a different signer.
2. **Approve a create, then reload the page before it verifies.** **Pass:** the
   HTLC id is still on the card. It is recorded before verification is attempted,
   precisely so a pending verdict cannot lose it — and a card that has lost the
   id offers **Create the HTLC** again, which locks a second lot of ZNN.
3. **Decline a block in the wallet.** **Pass:** the card says you declined and
   that nothing was sent. Not a timeout, not a generic failure.
4. **Point the extension at a different chain than Ferry.** **Pass:** the sync
   gate refuses before any block is built, and names the two chain ids.
5. **Reload the extension, then reload the page.** **Pass:** no Connect prompt —
   the connection is restored silently from the extension's per-origin list.

### Then run one on Auto Mode

Autopilot is the feature with the most ways to be quietly wrong, so it gets its
own pass. Arm it on one side only, so the other side is a normal manual run and
you can see both halves.

1. Create the swap by hand, read the trade, and only then arm **Auto Mode** —
   the switch lives among the other buttons near the foot of the card, not a
   banner of its own; hover its ⓘ first and check the tooltip covers all of the
   below. **Pass:** everything from there happens without a click except in
   your wallet — UniSat opens for the funding, Syrius opens for each Zenon
   call, and the redeem happens with no wallet at all.
2. **It stops at the wallet, every time.** Nothing may be signed or published
   without a window you approved. If anything reaches a chain that you did not
   approve in a wallet, that is a release blocker, not a bug.
3. **Decline something in the wallet.** **Pass:** a warning line appears above
   the step banner — `Auto Mode stopped: …` — naming the reason, and the small
   "stopped" badge appears beside the switch. Nothing further happens on its
   own, in particular it must not reopen the wallet window. Switching the
   switch off and on again is what resumes it.
4. **It does not fund out of turn.** Run the direction where your Bitcoin
   contract is the _participant's_ leg. **Pass:** autopilot waits until the
   initiator's Zenon HTLC exists and has passed verification before it funds.
5. **It does not redeem against a mempool.** Fund from the other side and do not
   mine. **Pass:** the redeem waits for a confirmation, while the manual button
   beside it stays live.
6. **It never refunds.** Let a swap expire with Auto Mode armed. **Pass:** the
   refund button is offered and nothing presses it.

### While you are in there

- **CLI commands are off by default.** A fresh profile's swap card shows no
  `znn-cli` block and no button to reveal one. **Pass:** the switch beside Auto
  Mode, among the action buttons, brings them back, and the choice survives a
  reload and applies to every swap on the page, not just this one.
- **The card looks alive while it waits.** Any step where you are waiting on a
  chain or on them. **Pass:** the dots move, and a line under the step says what
  is being watched. It must not appear when the networks disagree — there the
  page is genuinely not watching anything.
- **The status is right after you unlock.** Unlock the counterparty's Zenon HTLC
  on the leg where you sent BTC. **Pass:** the step immediately changes from
  "Unlock the Zenon HTLC" to waiting for them to take the Bitcoin, and the card
  reads as their move. Telling somebody to unlock an HTLC they just unlocked is
  how a finished swap gets unlocked twice.

### And check the Zenon leg outlives the Bitcoin one

The shape that used to strand somebody: **you send BTC and you are the
initiator**, so you unlock the counterparty's Zenon HTLC to take your ZNN.

1. Get as far as their HTLC being verified on your card, then **do not unlock
   yet**.
2. Press **Move to History** on that swap. **Pass:** the Zenon panel and its
   Syrius button are still there. Archiving files a record away; it does not
   settle a leg, and the ZNN is still in an HTLC only your key opens.
3. Move it back, unlock it, and let the counterparty redeem your contract so the
   swap reaches `redeemed`. **Pass:** the unlock button is gone now — because
   you unlocked, which is recorded, and not because the Bitcoin side finished.

---

## Stage 3 — The production run

Mainnet, real coins, smallest amount that is not dust. Everything in stage 2,
once, for real. Do not attempt it until stage 2 has passed in both directions.

Differences that matter, and they are the whole point of running this stage:

1. **Fee estimation is one lookup at build time, with no RBF and no bumping**
   (README gap 5). Fund the contract at a fee rate that confirms comfortably —
   not the minimum your wallet offers. A redeem that sits unconfirmed while a
   timelock approaches is the failure mode with teeth.
2. **Check the fee Ferry chose for the redeem** before broadcasting, against
   `mempool.space`. Record it. If it is badly off, that is a finding.
3. **48 hours is real.** The initiator's leg genuinely runs for two days, and
   you must be available for the parts that need you. Do not start one on a
   Friday.
4. **Save the recovery file before funding is confirmed, not after.** Then
   assume the browser is gone — stage 5 tests that assumption.
5. **Watch the timelock ordering yourself, once.** Read the Bitcoin locktime off
   the card and the Zenon `expirationTime` off `znn-cli htlc.get <id>`, and
   confirm with your own eyes that the participant's leg expires first. Ferry
   checks this and refuses when it is wrong; the point of checking by hand once
   is to confirm the check is checking what you think.

**Pass:** both legs settle from one secret, the coins arrive at addresses you
control on both chains, and the fees were not a surprise.

---

## Stage 4 — The refund drill

**The timeout paths have never been run to completion** (README gap 4). This is
the most valuable stage in this document and the one most likely to be skipped
because it is slow.

Do it on **signet**, where waiting out a 48-hour locktime costs nothing but time.
Use a short timelock: set the Bitcoin timelock field explicitly rather than
`auto` — 2 hours is enough to test the mechanism, and `lockHours` accepts
anything from 1 upward.

1. Create a swap, fund the contract, download the recovery file, and then **have
   the counterparty do nothing at all.**
2. Before the locktime, press **Refund**. **Pass:** it is refused, and says the
   timelock has not passed.
3. Wait out the locktime. Now wait longer: a refund is only relayable once the
   locktime is behind the chain's **median time past**, which trails real time by
   roughly an hour. **Pass:** the card flips to "Timelock passed — refund
   available" and the refund broadcasts and confirms.
4. Repeat, but **do not use the app.** Take the pre-signed refund hex out of the
   recovery file you saved in step 1, paste it into
   `https://mempool.space/signet/tx/push`, and broadcast it. **Pass:** it
   confirms. This is the guarantee that Ferry disappearing does not cost anyone
   money, and it is the single most important assertion in this file.
5. Repeat once more, at a **higher fee rate**, through **Recover → Rebuild and
   sign**. **Pass:** the rebuilt transaction pays the rate you asked for and
   confirms. This is the manual answer to a refund stuck below the fee floor.
6. And the Zenon side: let a Zenon HTLC expire and reclaim it — the button on the
   card, or `znn-cli htlc.reclaim <id>`. **Pass:** the ZNN comes back. This is
   the one extension call no run has ever exercised (README gap 3), so do it
   through the button.

---

## Stage 5 — Loss drills

Browser storage is erased by "clear site data", by a private window closing, and
by reinstalling the browser, with no warning. These test that the loss is
survivable. None of them cost anything — run them against a signet swap.

1. **Export and import.** Export every swap to a file. In a *different browser
   profile*, import it. **Pass:** the swaps appear, with the keys that can spend,
   and re-importing does not duplicate or clobber a newer record.
2. **Clear site data,** then import your export. **Pass:** you are back.
3. **The offline machine.** Copy `dist/` to a USB stick. On a machine with no
   network, serve it (`python -m http.server`) and open `#/recover`. Load a
   recovery file. **Pass:** it rebuilds and signs a spend and prints raw hex,
   with no node, no settings and no stored swap. Carry the hex to a networked
   machine and broadcast it.
4. **Lose everything but the file.** With no export and no browser, use only the
   recovery file and step 4 of stage 4. **Pass:** the coins come back.

---

## Sign-off

| Stage | Result | Notes |
|---|---|---|
| 0.1 Zenon node reachable (wss or https+CORS) | | the release gate |
| 0.2 Extension and page on the same chain | | |
| 0.3 Esplora reachable | | |
| 0.4 MIME type, compression, headers | | |
| 0.5 Origin not shared | | |
| 0.6 Correct instance | | |
| 1 Cold open | | first-visit time: |
| 2 Rehearsal, Bitcoin initiates | | |
| 2 Rehearsal, Zenon initiates | | |
| 2 Wallet boundary | | |
| 3 Production run | | fee chosen vs. market: |
| 4.2 Refund refused before locktime | | |
| 4.3 Refund via the app | | |
| 4.4 **Pre-signed refund, app not involved** | | |
| 4.5 Rebuild at a higher fee | | |
| 4.6 Zenon reclaim after expiry | | |
| 5.1 Export / import | | |
| 5.3 Offline recovery | | |

---

## Blockers

Things this recipe will find that are not bugs in Ferry, but do stop a release.

1. **Swap records are not encrypted at rest** (README gap 1). On a shared origin
   this is the difference between "a stranger can read your swap keys" and not.
   Stage 0.5 is the mitigation available today.
2. **`htlc.Reclaim` has never been driven live** (README gap 3). Stage 4.6 is
   the only test of it anywhere, and it needs a real expiry to run.

---

## Appendix — a local devnet run

The developer loop: both chains local, both instances yours, nothing costs
anything. Four things have to be running, and then two browser profiles.

### 1. Bitcoin regtest and the Esplora shim

Ferry speaks the Esplora HTTP API and never Bitcoin Core RPC, and a regtest node
serves no Esplora — so this repo ships the adapter:

```sh
# with a Bitcoin Core regtest node already up:
node scripts/regtest-esplora.mjs      # serves http://127.0.0.1:3002
```

That is already Ferry's regtest default, so the Esplora field can stay blank. It
walks the chain once, follows the tip, and layers the mempool on top so
unconfirmed funding shows up the way a real Esplora shows it. It refuses to serve
anything but regtest.

Fund a contract with `bitcoin-cli sendtoaddress <address> <amount>` and confirm
it with `bitcoin-cli -generate 1` — the point being that funding is an ordinary
send from an ordinary wallet.

### 2. A Zenon devnet

From a `go-zenon` checkout, `make devnet-up`. Its RPC sends permissive CORS
headers and serves HTTP JSON-RPC on **35997** and a WebSocket on **35998**. Ferry
takes either; the extension and `znn-cli` take only the WebSocket.

```sh
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"ledger.getFrontierMomentum","params":[]}' \
  http://127.0.0.1:35997
```

**Pass:** a `result` with `height` and `timestamp`.

### 3. The development build

```sh
cd ui && npm run dev        # http://127.0.0.1:5173
```

Plain http on purpose: a page on https cannot reach a node on `127.0.0.1`, so the
local flow has to be http on both sides. The dev instance already defaults both
node URLs to loopback and stores its swaps under `ferry.dev.*`, so it cannot
collide with a production build on the same origin.

### 4. Node settings, in each profile

| Field | Value |
|---|---|
| Network | `regtest` |
| Esplora URL | blank (falls back to `http://127.0.0.1:3002`) |
| Zenon node URL | `http://127.0.0.1:35997`, or `ws://127.0.0.1:35998` |

Point the Syrius extension at the same node. The sync gate compares a momentum
hash from both, so "chain 69" on each side is not enough — every devnet is chain
69, and two of them share that and nothing else.

### Then run stage 2 against it

The swap flow is identical to [stage 2](#stage-2--rehearsal-on-signet); only the
chains and the amounts differ. Two profiles, not two tabs.

### Checking the wallet path without a whole swap

`npm run wallet:devnet` builds a two-sided swap through the module and takes the
`htlc.Create` block apart field by field — method id, payee, hash type, key size,
hashlock, amount in base units, token, and the expiry's ordering against the
Bitcoin locktime. It stops one step short of the extension, which is the manual
half. It exists because the three things it checks — the chain identifier, the
token decimals an agreed "10 ZNN" is converted with, and the momentum clock the
expiry is measured against — are exactly what an offline smoke test cannot
answer, and each is the kind of thing that is wrong by a factor of a hundred
million rather than slightly wrong.

`npm run wallet:provider` stands up a stub provider and drives the same path with
no extension installed at all, which is what runs in CI.

[syrius]: https://github.com/sol-znn/syrius-extension/releases/tag/v0.3.1
[znn-cli]: https://github.com/zenon-network/znn_cli_dart

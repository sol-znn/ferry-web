# ferry-web-v2 — handoff

What changed from `ferry-web`, and the live status.

## What it is

v1 did one pair: Bitcoin against Zenon. This does four — BTC⇄ZTS, ZTS⇄ZTS,
SOL⇄ZTS, SOL⇄BTC. It is an independent tree: the Solana code was copied in from
`solzen` rather than imported, the Go module is `github.com/zenon/ferry-web-v2/wasm`,
and every dependency is from a registry. (`scripts/cli-wallet.mjs`, a
development-only tool, is the single exception.)

## The one idea the rewrite rests on

v1 named the halves of a swap after their chains — "the Bitcoin leg", "the Zenon
leg" — which is unrepresentable for ZTS⇄ZTS. v2 names them after what they do
for **this user**:

```
Out   the leg this user funds.        They hold its refund path.
In    the leg the counterparty funds. This user holds its claim.
```

Every rule that mentioned a chain turns out to be about Out-versus-In, and is
shorter for it:

```go
// v1: a function of role AND direction, per chain
func (s *Swap) ZenonLegIsInitiators() bool { ... }

// v2: the initiator's own funded leg is the long one. That is all of it.
func (s *Swap) OutIsInitiators() bool { return s.Role == RoleInitiator }
```

Same for the preimage: it appears on the leg you funded, when the counterparty
claims it. `Swap.SecretArrivesOn()` returns a chain, and the three readers are
three functions rather than three branches.

## What changed, by area

**Wire formats** — all bumped; v1 strings are refused with a sentence, not
guessed at. `swapoffer1:` → **`swapoffer2:`**, carrying `give`/`take` legs
rather than a Bitcoin amount and a Zenon amount. Board posts and takes went to
v2 with `addrs` keyed by chain. Session messages gained `leg`, because a ZTS⇄ZTS
swap has two Zenon legs and an id applied to the wrong one is checked against
the wrong terms.

**Solana** — `wasm/sol/` and `program/` copied from solzen, unchanged from the
code driven end to end there. The **program address is a term, not a setting**:
two deployments of one source are two contracts, so it rides on every swap and
offer, and `CheckCreate` refuses a mismatch. The page assembles and submits; the
wallet only signs.

**The board** — proof scheme `sol-ed25519` added (a Solana address *is* the
public key, so there is no key to carry and no derivation to check). The gate
now varies: you may act on an offer once you have proven an address on every
chain that offer settles on. Filters are by pair and by which chain you would
fund, replacing buy/sell.

**The create flow** — three steps in the order the decisions come: chains,
wallets, terms. Step two is a gate because the wallets supply the addresses both
ways out of the swap pay; asked at the end, you discover a missing extension
after agreeing amounts and type addresses by hand.

**No wallet in the header** — the Solana strip was the only one with a permanent
seat, which read as a requirement the app does not have. Connecting now happens
where a wallet is wanted.

## Status

### Done

- Go engine: builds for host and `js/wasm`; `go vet` and `go test ./...` green.
- UI: `vue-tsc` and `eslint` clean; `node scripts/build.mjs` produces `dist/`.
- `node scripts/smoke.mjs` — **140/140** against the shipped `.wasm` under Node,
  for both instances.
- Wire formats, offer checking, board proofs, session hand-offs, per-leg panels,
  the stepped create form, the generalised board.
- **A dev harness**, `ui/src/core/testkit.ts`: stub wallets, nodes settable
  without the dialog, clicking by label. Behind `isDev`, so it is absent from
  the production bundle.
- **A CLI wallet**, `scripts/cli-wallet.mjs`: a real ed25519 key for Zenon and
  `bitcoin-cli` for Bitcoin, behind one loopback surface the dev build calls. It
  holds two Zenon accounts, and `__ferry.wallets({znnIndex})` picks which one an
  instance signs as — without that both browsers were the same account, which a
  ZTS⇄ZTS swap turns into one key trading with itself.

### Deployed

**https://ferry-web-v2.vercel.app** — project `ferry-web-v2`
(`prj_nFfbg5ior1xmZuL7OAUO7tHlW8Tm`). v1's projects are untouched.

### Verified in a browser

Every create path: four pairs × both directions × both roles, sixteen swaps
through the real form. Legs land on the chains the direction names, amounts are
in each leg's own unit, and the initiator's funded leg expires ~24h after the
participant's in all sixteen — `OutIsInitiators` holding in the shipped app.
Refusals fire with their own sentences: same token both legs, blank amount, zero
amount, own payout address cleared, participant with no secret hash, and a
Solana leg with no program (which refuses differently at the wallet step and at
the terms step). A `swapoffer1:` string is refused with the exact text from
`wasm/swap.go`, so the JS/Go seam is live in the shipped page.

**A whole BTC⇄ZTS swap settled** on regtest + devnet through the UI, with the
CLI wallet in place of the extensions:

- contract funded — 400,000 sat to `2N48Xv…G32A`, detected from Esplora, refund
  pre-signed automatically;
- the participant audited it offline and was told the expiry to use (86,130s);
- `htlc.Create` published — `cbc39ff5…efb6`, 100 QSR, hashlock and payee
  matching the terms;
- the initiator verified it, then unlocked it (`6a9bad54…`), paying itself and
  publishing the preimage;
- **the participant recovered that preimage by itself**, off the Zenon chain,
  with nothing passed between the cards;
- the participant redeemed the Bitcoin — `7279e7db…`, 399,354 sat after a 646
  sat fee, contract UTXO spent, both cards `settled`.

Two refusals fired correctly on the way: a wallet reporting chain 1 against a
chain-69 node, and funding the participant's leg before the initiator's was on
chain.

**A whole ZTS⇄ZTS swap settled** on devnet, and this one was genuinely
two-sided: two origins (`127.0.0.1:4176` and `localhost:4176` are separate
origins, so separate storage) driving two different Zenon accounts through the
one CLI wallet. 10 QSR against 1 ZNN, the initiator funding the QSR leg:

- the participant's engine refused to fund first — *"the counterparty's Zenon
  leg is not on chain yet. As the participant you fund second"* — which on this
  pair is the rule doing real work, because both legs are Zenon and only
  Out-versus-In distinguishes them;
- the initiator's leg published — `f6a229…9bf8`, deadline 48h;
- **the participant found it by scanning its own node**, nothing passed between
  the cards, and verified hashlock, parties, token, amount and expiry;
- the participant's leg published — `bf5ca7…c4be`, deadline 24h, so
  `OutIsInitiators` holds on the pair v1 could not express at all;
- the initiator unlocked it (`07644b7c…`), publishing the preimage;
- **the participant recovered that preimage by itself** and unlocked the QSR;
- both cards `settled`, and the chain agrees: index 0 is 1 ZNN lighter, index 3
  is 10 QSR lighter, both proceeds waiting as unreceived blocks.

**A SOL⇄ZTS swap settled**, once a program was deployed and two real bugs were
fixed — see below. 2 ZNN against 0.5 SOL, the initiator funding the Zenon leg,
so the preimage surfaced on **Solana** and the participant had to read it there:

- Zenon HTLC `9955ca…0622` published, 48h;
- the participant verified it, then locked 0.5 SOL (plus 0.0019 SOL rent) in
  escrow `6AhjiL…DprM`, which both sides now derive identically;
- the initiator's page verified that escrow against the PDA it derives itself —
  parties, amount, hashlock and deadline — then redeemed it (`4jeifXo…`);
- **the participant recovered the preimage off Solana by itself** and unlocked
  the Zenon leg;
- both cards `settled`.

**A SOL⇄BTC swap settled** — the pair with no Zenon leg in it, so nothing on
either side could lean on the chain the rest of the app was built around. 0.3
SOL against 300,000 sat, the initiator funding Solana, so the preimage crossed
on **Bitcoin**:

- the participant built the contract `2MsonE…KZ5G` from the pubkey hash in the
  offer — which took a third fix, below;
- the initiator locked 0.3 SOL, the participant paid the contract, and the
  initiator audited the contract offline before spending;
- the initiator redeemed the Bitcoin, putting the preimage in the witness;
- **the participant found it there by itself** and claimed the SOL;
- both cards `settled`.

### Found by testing, fixed

The numbering runs across both sections below; 1-3 came out of the rehearsals,
4-6 out of the refund drill.

1. **The taker minted its own Solana swap id.** `NewSwapForm.submit` passed
   `swapId` for the In leg and not the Out one, so a participant taking an offer
   generated a fresh id for the leg it was about to fund. The id is the PDA
   seed, so the two sides addressed **different escrow accounts**: the money
   would have gone somewhere the counterparty was not watching, and the swap
   could only have ended in a refund. The engine was already right —
   `manager.go` says "a joining side takes the one from the offer" — the form
   simply never handed it over. Both legs now take program and id from the
   decoded offer.
2. **`Buffer is not defined`, at the moment of funding a Solana leg.** The port
   wrapped instruction data in `Buffer.from(...)` to satisfy web3.js's
   TypeScript type; `Buffer` is a Node global and the browser bundle has none,
   so **no Solana leg could be funded from the page at all**. solzen passed the
   raw `Uint8Array`, which is what the library actually wants — restored, with
   the cast that makes the type agree.
3. **The pubkey hash in an offer was never applied to the leg the taker funds.**
   Same shape as the swap id, in a different file: `applyOffer` read `pkh` from
   the In leg only, the field rendered only when the In leg was Bitcoin, and
   `submit` passed it only then. So whenever the person taking an offer is the
   one funding Bitcoin, their card sat on *"Waiting for their pubkey hash. The
   contract commits to it, so it cannot be built without one"* — with the hash
   sitting unread in the offer they had just pasted. SOL⇄BTC in this direction
   could not be started at all. The engine takes a hash on either Bitcoin leg;
   only the form was one-sided.

   This is also half of the old finding about starting a swap by hand: the
   create form now offers the field whenever **this** user funds Bitcoin. The
   other half — a hash that arrives after the swap exists — is closed further
   down.

The first three were invisible to `go test`, to `vue-tsc` and to the 140-case
smoke run: two are fields a form forgets to pass, one a global that only fails
when a real transaction is built.

### The refund drill

Stage 4 of `docs/TESTING.md`, which had never been run on any chain. It has now
been run on all three. **Bitcoin, both ways round:**

- a contract funded with a 3-hour deadline; the regtest chain's median time past
  advanced beyond it with `setmocktime` plus twelve blocks; the **pre-signed
  refund broadcast and confirmed** — 249,420 sat back after a 580 sat fee;
- a second contract with a deadline still ahead of median time past: the same
  refund refused with **`non-final`**, the exact message TESTING.md predicts.

**Zenon is done**, waited out in real time — devnet momentums and the test
validator's clock both run on wall time, so neither of these two could be
hurried. HTLC `f8d006…ffb3`, 4 QSR, expiring `2026-09-08T21:59:30Z`:

- past the deadline the card turned to *"Deadline passed — take your Zenon
  back"* and offered the reclaim, which it had not before;
- **with the wallet deliberately switched to the wrong account** the reclaim was
  refused, by name: *"wrong account: this swap's Zenon leg belongs to
  z1qp3yph… and the wallet has z1qq9n7f… selected"* — stage 5's account-switch
  drill, proven on the highest-stakes action there is;
- switched back, `htlc.Reclaim` published as `e81b900b…3caa`, the entry is gone
  from the contract (`htlcById` answers `missing`), and the card reads
  `refunded` — *"You took your Zenon back — they never funded theirs"*, which is
  the sentence finding 4 added, now confirmed on a second chain.

**Solana is done** too, also waited out. Escrow
`EUmxgg5cN9fzKzBQeNZ13aCMGcNMsRkfneuSAfXMeoeH`, 0.25 SOL plus 0.00190704 rent,
unlocking at `2026-09-08T23:04:53Z`:

- past the timelock the card turned to *"Deadline passed — take your Solana
  back"* and the panel said why: *"its deadline passed … so its funder can take
  it back at any moment"*;
- `refund` published as `5ogBzxf…`, and the **escrow account is gone** —
  `getAccountInfo` answers `null`, so the program closed it rather than leaving
  a husk;
- **the rent came back with the amount**, which is the thing this stage exists
  to check: the funder is 251,902,040 lamports richer against an escrow holding
  251,907,040, so the whole 0.25 SOL and the whole 1,907,040 of rent returned
  and the only loss is the 5,000 lamport transaction fee.

That is stage 4 complete on all three chains.

### Found by the refund drill, fixed

4. **A refund read as a redeem.** After taking its own Bitcoin back, the card
   said *"Claim your Zenon — the preimage is public now"* and pointed at a leg
   the counterparty had never funded. `legDone` is true for both exits and
   `progress.ts` had only the one branch, so a leg reclaimed by its own funder
   was reported as one claimed by the counterparty — telling the user a secret
   they invented was public. The leg view now carries `reclaimed` beside `done`,
   and a one-sided refund settles the swap as `refunded` instead of leaving it
   `funded` for good.
5. **The deadline field did nothing.** "Your deadline (hours, optional)" was
   validated, written onto the leg, and overwritten on the next line by
   `planOutLeg` with the default the role implies. Every swap ran 48h or 24h
   whatever was typed. The requested duration is now kept on the leg and
   survives replanning — while the counterparty's expiry, once it is a fact,
   still wins, because the ordering between the legs is a safety rule and not a
   preference.
6. **The floor was wrong at both ends.** Below two hours a leg was accepted and
   then could not be built at all, and the card blamed a missing counterparty
   deadline. *At* two hours it was worse: the swap was created, and by the time
   the funding button was pressed the leg was under the floor — `"this swap's
   Solana deadline is 1h58m55s away, under the 2h0m0s minimum worth locking
   money behind"`. A deadline has to still clear the floor when the money moves,
   so the minimum is now three hours, with the sentence saying why.

`scripts/regtest-esplora.mjs` was missing `/tx/{txid}/status`, which is what
ferry's confirmation counter reads — so funding sat at `0 / 6` locally however
many blocks were mined. Added; the counter now moves, which is also what makes
the Bitcoin drill legible.

### Loss drills (stage 5), and the Recover page

- **Export and import across browsers.** The real Export button's file was
  imported into the second origin: *"imported 7 swap(s)"*. Re-importing the same
  file after editing one of those records locally gave *"imported 0 swap(s),
  skipped 7 already in this browser"*, and the local edit survived untouched —
  so a stale file cannot clobber a newer record, which is the rule the panel
  states.
- **The Recover page rebuilds a refund offline, from the file alone.** Fed the
  recovery file for a still-funded contract, it read the pre-signed refund out of
  it, quoted the payout, and rebuilt at 25 sat/vB: 7,250 sat of fee over 290
  vB, a new txid, and *"not valid for another 11h33m19s"* against the locktime.
  No node was involved.

- **Clear site data, then restore from the file.** `localStorage.clear()` on the
  second origin took it back to a first run — "Nothing in progress" — and the
  backup file alone brought all five records back: *"imported 5 swap(s)"*. The
  nodes and stub wallets do not come back with them, which is right: those are
  settings, not swap records.
- **Switching the wallet account mid-swap** is caught where it matters rather
  than on render. `ZenonWallet` re-reads the wallet immediately before handing a
  block over and refuses if the account moved — *"This block was built for … and
  the wallet now has … selected. Nothing was sent"*. The live proof rides on the
  Zenon reclaim: this browser's wallet has been switched to the account that did
  **not** create the HTLC, so the reclaim should be refused before it is
  switched back.

### Auto Mode does nothing

The card's toggle promised *"Performs the outstanding step as soon as it becomes
possible"*. It does not perform any step, on any chain.

`ZenonWallet` carries a complete and careful auto path — claim each step once,
build the block, stop at the wallet's own window, halt the swap on the first
failure — behind an `auto` prop. **Nothing in the app passes that prop.**
`SwapCard` computes `autoOn` and uses it only to render "Armed — watching this
swap"; the leg panels take `swap`, `leg` and `blocked` and no more; `BtcLeg` and
`SolLeg` have no auto path at all. The single real effect of arming it is in
`useAutoRefresh`, which keeps polling a swap whose tab is in the background.

Wiring it is `:auto="autoOn"` down through the panels to the action wallet and
**not** to the reclaim one — which is where "never refunds" stops being a
promise and becomes a missing binding. Left undone rather than half-done: on one
chain of three it would be harder to reason about than none. The description on
the card now says what it actually does, and says the promise was withdrawn.

### The three findings that were open, now closed

- **A pubkey hash can be pasted in, both before and after the swap exists.** The
  create form offers the field whenever this user funds Bitcoin; `BtcLeg.vue`
  now carries one beside the Callout that used to be a dead end, calling the
  `counterparty` endpoint that already existed and had only `useSession.ts` as a
  writer. Checked in the browser: a contract built from a hash typed by hand.
- **The mainnet Solana default is gone.** `api.mainnet-beta.solana.com` passes
  preflight and then answers a POST carrying a browser Origin with **403** —
  confirmed against the live endpoint, while `api.devnet.solana.com` answers
  200. A default that cannot work is worse than none, so mainnet now falls back
  to nothing and both the Nodes dialog and the Docs page say why. Devnet keeps
  its default.
  Removing that default exposed a second thing: with no endpoint set, the header
  chip said **`connecting…` for ever**, because there was no request to be
  waiting on. It now reads `no node set`, the same as Zenon's — checked on
  mainnet with every field blank.
- **The Docs page is no longer two-chain.** It already carried the Solana wallet
  card and the RPC-and-program section; only the sentence promising a public
  fallback needed correcting.

### Not done / next

1. **No Solana program is deployed on a public chain.** `program/` builds, and
   `Ga3KZR31qGVD6tjLzWB3nsvHXPFQSk1Wenjde6CmoUDh` is deployed on the local
   validator (solzen's `docker compose`, container `solzen-validator`) and has
   settled a swap. Devnet and mainnet still have nothing, so a Solana leg there
   is refused until Node settings names a deployment.
2. **Nothing has run on a live chain with real money.** The refund drill is done
   on all three test chains — see above — but stage 3, the production run, has
   not been attempted.
3. **Board proofs cannot be made by the CLI wallet** — the module verifies those
   signatures, correctly, so they need a real wallet.
4. **Auto Mode is a watcher, not a driver** — see above. Either wire it on all
   three chains or drop the toggle; it should not stay in between.

### Found while checking, not yet fixed

1. **Stale notes survive the leg they describe**, on two chains. A funded and
   verified Solana escrow still carries *"A create was submitted as … and the
   cluster has not confirmed it yet"* beneath a leg the same panel has since
   watched get redeemed. A reclaimed Zenon leg still carries the verification
   line it failed an hour earlier — *"expires in 1h40m10s … at least 2h0m0s must
   remain"* — beside the note saying it was reclaimed. Neither is harmful and
   both read as contradictions; a verdict should be cleared when the fact it was
   about stops being true.

# Handoff — solzen, 2026-09-06

Solana ⇄ Zenon atomic swaps, in ferry's model, driven from a static page.
`README.md` is the document to read for what it is and why; this one is what
state it is in, what is proven, and what is left.

**Short version:** it works, and everything green below was run against the code
as it currently stands. Swaps have settled in both directions through the engine
and through the page itself. What is left is the gap list, not the mechanism.

`ISSUES.md` is the working register of what is wrong, missing or worrying, and
is where anything found mid-session goes before it is worth promoting into the
README.

---

## This session — 2026-09-06

Worked the open register. Six entries in `ISSUES.md` were open at the start; five
are closed, one is deliberately held, and two more were found on the way and
fixed.

| # | Was | Now |
|---|---|---|
| §3 | `znn.reclaim` published the block and let the contract refuse it | the engine refuses first, against the entry's own expiry and the frontier momentum |
| §5 | Wallets found by injection, not by the Wallet Standard | **held**, and re-argued rather than re-deferred — see below |
| §6 | `timeout:test` built from a patched copy of `wasm/` | `-tags fastclock`, selecting `limits_fastclock.go` over `limits.go` |
| §7 | A reclaim left the money at an address only one browser could reach | one action publishes all three blocks and names each as it lands |
| §8 | The mismatched-leg refusal lived in the plan, not the executor | `znn.create` re-reads the escrow and refuses before it builds |
| §10 | A failed plasma probe rendered as "none — about 1 seconds of work" | a third state: unknown, dimmed, with the reason |
| §11 | *found here* — every plain send waited 90s for a paired block only the recipient can publish | the wait is taken for embedded-contract calls only |
| §12 | *found here* — the store could be exported but not imported from the page | an Import button beside Export |

**The one that matters is §7.** Reclaiming a Zenon HTLC pays the address that
created it, which is this swap's own address, and on Zenon that money is not
spendable until that address publishes a receive block for it. So taking a leg
back is reclaim, receive, sweep — and it used to be three buttons, of which the
first makes the leg read "refunded" and pays nobody. The two after it were the
easy ones to walk away from, with the only key that could finish the job sitting
in one browser's `localStorage`. `ZenonAct("znn.reclaim")` now does all three:
it waits for the contract's payment back to appear, receives it, waits for the
balance to land in a momentum, and forwards it to the nominated address. A step
that fails returns what was published in `steps` with `incomplete` naming what
is left, rather than throwing away the fact that money has moved.

**§5 is the one left open, and on purpose.** Discovering a Wallet Standard wallet
is a few lines; *driving* one is a second signing path — connect, account,
`solana:signTransaction`, chain identifiers — with no such wallet available here
to run it against. An untested second branch in the code that signs a swap is
worse than not finding a wallet nobody has asked for. The condition for doing it
is unchanged: if this connector is lifted into ferry-web, where the wallet list
is not "whatever the developer installed".

**Two guards moved from the plan into the engine** (§3 and §8), which is what the
last session's handoff said was worth doing. Both were reachable through the API
without going through the plan the page reads. The mismatch test can now prove
§8 by calling it, which it previously refused to do because doing so would have
locked the participant's money.


### Re-run after the changes

Everything below was run against the tree as it now stands, in series, on the
same local validator and go-zenon devnet.

| Run | Result |
|---|---|
| `go test ./...` | 29 tests, green — and green again under `-tags fastclock`, which is the other half of the build the timeout test uses |
| `smoke` | 27 checks, green, against a freshly built `solzen.wasm` |
| `swap:test` | 22 checks, green — a whole swap settled, both legs, one secret |
| `timeout:test` | 26 checks, green — with `SOLZEN_SHORT_SECONDS=420 SOLZEN_LONG_SECONDS=660` |
| `timeout:test znn2sol` | 26 checks, green — the initiator's reclaim, the other side of §7 |
| `timeout:test mismatch` | 12 checks, green — including the new one that calls `znn.act znn.create` and is refused |
| `timeout:test abandon` | 9 checks, green on the second run — the first found a real flaw in the test, below |
| `plasma:test` | green — the pricing rule is unchanged by any of this |
| `ui:test` | 12 checks, green — a whole swap clicked through in two Chrome profiles; screenshots refreshed in `docs/` |

**`abandon` failed the first time, and the failure was the point.** It swept and
then asked the test wallet to receive **once, immediately** — too early for a send
that is not in a momentum yet. It had always passed because `znn.act` used to
spend ninety seconds waiting for a paired block that a sweep never gets, and
that wait was exactly the pause the receive needed. Removing the wait (§11)
removed the pause, and the test now retries the way the others do. `ISSUES.md`
§11 has it written up as a shape worth recognising rather than an incident.

---

## Previous session — 2026-09-05

Re-ran everything, added a Phantom connector, and closed the gaps that could only
be closed by running something. Four scenarios that had never been executed now
are; one that was thought to be executed turns out not to have been.

| Run | Result |
|---|---|
| `go test ./...` | 27 tests, green |
| `program:test` | 20 checks, green |
| `swap:test` | 22 checks, green |
| `swap:test znn2sol` | 22 checks, green |
| `smoke` | green |
| `ui:test` | 12 checks, green — with the connector rewrite in place |
| `ui:test znn2sol` | 12 checks, green |
| `ui:test -- --phantom` | 18 checks, green — a whole swap signed outside the page |
| `ui:test -- --pow` | 14 checks, green on the re-run — the unlock mined for 217s. An earlier run of the same command passed in 28s without mining at all: `ISSUES.md` §9 |
| `timeout:test` | 24 checks, green — **new**, and the gap this handoff has been carrying |
| `timeout:test znn2sol` | 24 checks, green |
| `timeout:test abandon` | 11 checks, green |
| `timeout:test mismatch` | 10 checks, green — **new**, and the branch nothing had ever touched |

**Phantom connector.** The page had a generic injected-wallet path that connected
by shape. It now has a real one: Phantom detected by name and by the event it
announces itself with, a silent reconnect on reload, `accountChanged` and
`disconnect` followed rather than ignored, and an install link when no extension
is there. The important half is not the button — see `ISSUES.md` §1, which is a
bug the button's arrival exposed.

**Timeout paths.** `scripts/timeout-test.mjs` is new: it funds both legs, claims
neither, and takes both back after their timelocks. It builds the engine with
`-tags fastclock`, which swaps `limits.go` for `limits_fastclock.go` and lowers
the two creation-time minimums, because otherwise this is a four-hour test; every
other file is the one that ships, and the run fails if it did not get the build
it asked for. Two more modes came out of asking what else ends a swap
without settling — `abandon` (a swap address paid, then the swap dropped) and
`mismatch` (a leg funded that is not the one agreed), both against the engine
exactly as it ships.

**Two things this found that were not on anyone's list.** The refusal to fund
against a mismatched leg, and the refusal to reclaim before expiry, were both
enforced where the page reads and not where the engine acts — `ISSUES.md` §3 and
§8. Neither was reachable by clicking; both were reachable through the API.
Closed on 2026-09-06, and both are now asserted by calling them.

**And one that is worth more than the rest of this session.** `ui:test --pow` is
the only coverage the browser-mining path has, and it passed in under three
minutes against a page that said the block would cost four minutes of work. Run
again with an assertion that the mine actually happens, it mined for 217s — 110M
hashes at about 508k a second, which is the README's estimate almost exactly. So
the arithmetic is right and one run published a block without doing the work the
node asked for. That is §9 — and it was explained the next morning: the fast run
had fused plasma, because it was an ordinary run wearing a `--pow` label. The
register carries the forensics and the two assertions that stop it recurring.

---

## What is proven, and by what

Every row was re-run after the last code change.

| Claim | Evidence |
|---|---|
| The Solana program keeps its promises | `npm run program:test` — 20 checks: a wrong preimage cannot spend, a redeem naming the wrong receiver cannot spend, a refund before the timelock cannot spend, a redeem after it cannot either, the receiver is paid exactly the agreed amount, the rent goes back to the initiator, the escrow is closed, and the preimage is recoverable from the chain |
| The Go encoders agree with the Rust | `wasm/sol/htlc_test.go`, against an escrow captured off the chain by `npm run capture:escrow` |
| The Zenon signing is real | `wasm/znn/block_test.go` — `ComputeHash` reproduces the hash of a block the devnet accepted, that block's own signature verifies against it, every hashed field changes it, and the nonce this code mined satisfies the difficulty the chain recorded |
| The leg-ordering rules refuse what they should | `wasm/swap_test.go` |
| A whole swap settles, SOL initiating | `npm run swap:test` |
| A whole swap settles, ZNN initiating | `npm run swap:test znn2sol` |
| The shipped `.wasm` works over real `fetch()` | `npm run smoke` |
| A whole swap settles **by clicking the page**, in two browsers | `npm run ui:test` — screenshots in `docs/ui-maker.png`, `docs/ui-taker.png` |
| A swap nobody claims is taken back, on both chains, with nothing paid and no secret ever published | `npm run timeout:test` — 26 checks: a refund before the timelock is refused **by the engine on both chains**, the expired leg can no longer be claimed and says why, each side is offered only its own refund, taking the Zenon leg back publishes all three of its blocks in one action and leaves the swap address empty, and both amounts come back exactly — 5 ZNN, and 0.35 SOL plus the escrow's rent |
| The same, with the chains the other way round | `npm run timeout:test znn2sol` |
| Money sent to a swap address before the swap is abandoned comes back | `npm run timeout:test abandon` — against the engine exactly as it ships |
| A counterparty who funds a leg that is **not** the agreed one is caught, and the step that cannot be undone is refused | `npm run timeout:test mismatch` — the initiator edits their own copy of the terms and funds that; the participant's page reads the escrow, names both amounts, blocks the HTLC, has `znn.act znn.create` refused by the engine when it is called anyway, and sweeps its own money back |
| A whole swap settles **through the wallet connector**, signed outside the page | `npm run ui:test -- --phantom` — screenshots in `docs/ui-phantom-*.png` |
| A side with no plasma really can buy its block with proof of work, in the tab, without freezing it | `npm run ui:test -- --pow` — the page reports no plasma, puts up its mining panel, and the unlock costs 217s of hashing while the tab stays answerable |

A full `ui:test` run takes about four minutes; `swap:test` about the same, and a
`timeout:test` about twelve, most of it watching a clock. With `--pow` the UI run
takes ten, because the receiving side mines its unlock in the browser instead of
being handed plasma — **a `--pow` run that finishes in three minutes did not mine
anything**, which has happened, and is `ISSUES.md` §9.

---

## What is left

### 1. The gaps in `README.md` — "Status and known gaps"

Eight items in priority order. The two worth naming here because they change how
you would deploy this:

- **Swap records are not encrypted at rest.** They hold the per-swap Zenon key
  and, for the initiator, the secret, in `localStorage`. On a shared static host
  that means any page the same account publishes can read them.
- **Nothing verifies that the program at that address is this source.** The page
  checks the id is deployed and executable and cannot check what it contains. The
  Docker image pins both halves of the toolchain so a reproducible build is
  possible; nobody has done one.

### 2. Timeout paths — done through the engine, not through the page

`timeout:test` now funds both legs, claims neither, and takes both back, in both
directions, against both live chains. What it drives is the engine, which is what
the page drives; what nobody has watched is the same thing happening under a
mouse. That is a smaller gap than it was — every action the page would offer is
asserted by name and by whether it is ready — but it is not nothing.

Also still true: no swap with a *real* pair of deadlines has ever been left to
expire, because that is 48 hours. What has been run is the same code with the
creation-time minimums lowered, which since 2026-09-06 is a build tag rather than
a patched copy of the tree — `ISSUES.md` §6.

### 3. If `npm run ui:test` misbehaves

It is green, but it is a browser test driving two chains. Written down so nobody
loses the afternoon I did:

- **Most of a run is waiting, and it looks like a hang.** Every `devnet-wallet`
  call publishes a Zenon block and waits for the contract to process it; three
  fuses and a send run before the swap starts. Several minutes can pass under one
  unchanging log line. Check `Get-Process devnet-wallet` and look at **CPU**:
  near zero means waiting on a chain, climbing means mining. Neither is stuck.
- **Do not edit anything under `ui/` while a run is in flight.** Vite hot-reloads
  the page, the execution context is destroyed, and the run dies with
  `Execution context was destroyed`. This was the one genuine hang.
- **Kill leftover fixtures after interrupting a run.** Stopping the Node process
  leaves `devnet-wallet.exe` behind, still publishing from Zenon account index 1;
  a second process building on the same account chain is asking for a rejected
  block. `Get-Process devnet-wallet | Stop-Process -Force`.
- Page reads go through `evaluate()` in `ui/scripts/ui-test.mjs`, which fails
  after 30s with "its main thread is blocked" rather than hanging, and failures
  screenshot both browsers to `docs/failure-*.png`. Look at those before guessing.
- The tests share Zenon devnet accounts, so an unreceived payout left by an
  aborted run lands in the next run's balance window. `swap:test` now receives
  before it takes its "before" snapshot; if you add another balance assertion, do
  the same.

---

## Findings worth not re-discovering

**Taking a Zenon leg back is three steps, and only the first is called reclaim.**
The contract does not hand the money to anyone: it sends it to the address that
created the HTLC, which is this swap's own address — and on Zenon an incoming
transfer is not spendable until the receiving account publishes a block for it.
So the sequence is reclaim, then **receive**, then sweep. The first version of
`timeout-test.mjs` went straight from reclaim to sweep and died on "this swap's
address holds nothing to sweep", which reads like a lost payment and is actually
the middle step missing. Solana has no equivalent: a refund is one instruction and
the lamports are simply there.

Since 2026-09-06 the page does all three under one button (`zenonReclaimHome`,
`ISSUES.md` §7), because the two steps after the reclaim are the easy ones to walk
away from — the first one makes the leg read "refunded" and pays nobody. The
separate actions are still there and are still what a partly-finished recovery
falls back to.

**A wallet extension's own send goes to the wallet's chain, not to yours.** See
`ISSUES.md` §1 — it is the reason the page signs and submits in two separate
calls, and it is invisible on any network where both happen to be the same chain.

**Mining plasma freezes a browser tab, and the obvious fix makes it three times
slower.** Go compiled to WebAssembly runs on the page's one main thread. The
first browser run hit exactly that, with Chrome's debugging protocol timing out
behind it. Yielding with `time.Sleep` fixed the freeze and made the mine roughly
three times slower, because a sleep schedules a timer and Chrome clamps timers in
a page it considers hidden to about one a second. `wasm/znn/yield_js.go` uses a
MessageChannel instead: a macrotask, so the browser can paint, and not throttled.
Off the browser the same call compiles to nothing.

**Asking the node what a block costs fails if you ask about the wrong block.**
`embedded.plasma.getRequiredPoWForAccountBlock` works out base plasma for a send
to an embedded contract by *decoding the call*, so an empty payload addressed at
the htlc contract answers "method not found in the abi" — which, if the error is
swallowed, reads as "this account has no plasma". Probe with the real payload for
the call you are about to make.

**`common/types` costs 15 MB in a `js/wasm` build, and `vm/abi` costs 0.4.**
`common/errors.go` imports `rpc/server`, so anything touching a `types.Address`
links a JSON-RPC server. The README has the measurements. Moving `NewErrorWCode`
out of `common` would fix it upstream for every consumer.

---

## Environment

Both chains have to be up. Neither is started by this repo's tests.

```sh
docker compose up -d                    # Solana validator, in this repo
cd ../go-zenon && make devnet-up        # Zenon devnet, chain 69
cd ui && npm run dev                    # the page, on :5188
```

- Solana program: `5mcZ6qPc5YY7PnwvXJTtVDK8hgijqyXBCFH8eR3wzXFF`, deployed on the
  local validator. If the ledger volume is wiped, redeploy it with the command in
  the README — the address is fixed by the committed keypair, so nothing else
  changes.
- The Zenon devnet's account index 1 funds everything and holds the QSR the
  fixture fuses. Index 2 is the ZNN payout address in tests.
- `wasm/cmd/devnet-wallet` stands in for Syrius. It is the **only** code here
  that derives a key from a mnemonic, and it is a test fixture — the application
  has no such path, which is the entire point of the per-swap keys.

## Where things are

`README.md` has the full layout. The four files to read first:

- `program/src/lib.rs` — the Solana contract, ~300 lines, no framework.
- `wasm/actions.go` — what this side should do next, and the safety argument for
  the order it is in.
- `wasm/status.go` — every check that decides whether a leg is safe to act on.
- `wasm/znn/` — Zenon signing in the browser. This is the part worth taking into
  ferry-web whatever happens to the rest; see the README's last section for what
  a port would and would not carry over.

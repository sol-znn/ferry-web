# Issues, gaps and concerns — solzen

A live register. `README.md` §"Status and known gaps" is the considered list that
ships with the project; this file is the working one — what a session found, what
it did about it, and what is still open. Anything here that survives gets promoted
into the README; anything fixed gets struck through and dated rather than deleted,
so the next session can see what was already tried.

Severity: **blocking** (a swap can lose money or cannot complete) · **serious**
(wrong behaviour in a real deployment) · **minor** (rough edge) · **note**
(no action expected, written down so it is not rediscovered).

---

## Found in this session — 2026-09-05

### 1. A wallet extension would have submitted to the wrong chain — *serious, fixed*

`ui/src/core/solana.js` sent every step through the extension's
`signAndSendTransaction`. That call submits through **the extension's own RPC**,
which is whichever cluster the user has selected in Phantom — not the node named
in the page's Nodes panel. Against the local validator the two are never the same
chain: the transaction carries a blockhash this page fetched from `127.0.0.1:8899`,
and Phantom would have offered it to mainnet, where the blockhash is unknown and
the escrow does not exist.

Nothing caught it because the only path with automated coverage is "a key in this
browser", which signs and submits locally.

**Fixed:** the page now prefers `signTransaction` and submits through its own
`Connection`, so the transaction lands on the node the swap is actually against.
`signAndSendTransaction` remains as the fallback for a wallet that offers nothing
else, and the header says so in red when that fallback is what is in use
(`wallet.sendsThroughItsOwnRpc`).

### 2. The wallet-extension path had no end-to-end coverage — *serious, closed*

`ui:test` connects with "Use a key in this browser" because an extension cannot be
installed into a throwaway Chrome profile from a script. That left the entire
injected-wallet branch — connect, account changes, sign, submit — untested, which
is how §1 survived.

**Closed:** `ui:test --phantom` injects a Phantom-shaped provider into the page
before it loads and signs in Node, so the page's real connector code runs against
a real validator. A whole swap settles that way — both Solana legs signed by
something outside the page and submitted by the page — in 14 checks.

What that does *not* cover is Phantom itself: its popup, its cluster setting, its
simulation warnings and its refusals are still only reachable by hand.

### 3. ~~The two chains guard an early refund in different places~~ — *closed 2026-09-06*

`sol.refund` is refused by the engine before it builds anything — "the escrow is
refundable in …, not yet" (`wasm/act.go`). `znn.reclaim` has no such check —
it builds the call, publishes a block, and lets the contract decline it.

The page never offers either action before the leg expires, so a user does not
meet this. Anyone driving the API directly does: they pay plasma, or minutes of
proof of work, for a block that does nothing. The fix is four lines — compare
`s.Terms.ZnnExpiry` against the frontier momentum's timestamp in the `znn.reclaim`
case, the way the Solana branch compares against the cluster clock.

`timeout-test.mjs` covers what actually protects the money here, and this is what
it observed on the devnet:

```
ok   initiator's escrow refuses to build an early refund
       -- sol.instruction: the escrow is refundable in 18m, not yet
ok   participant's HTLC survives an early reclaim
       -- the block published and the contract ignored it
```

Two guards, both holding, in two different places. The money is safe either way;
the Zenon one is just paid for.

**Closed.** `znn.reclaim` now reads the entry and compares its `expirationTime`
against the frontier momentum before it builds anything, which is what the
Solana branch has always done against the cluster clock. An HTLC that is gone
altogether is refused with that as the reason, rather than by publishing a call
the contract will decline. `timeout:test` asserts both guards, in the same
words:

```
ok   initiator's escrow refuses to build an early refund
       -- sol.instruction: the escrow is refundable in 9m, not yet
ok   participant's HTLC refuses to build an early reclaim
       -- znn.act: this HTLC is reclaimable in 5m, not yet
ok   participant's HTLC survives an early reclaim  -- still holding 5
```

The third check is the one that was there before, and it still is: the guard
saves the fee, and what has to hold regardless is that the money does not move.

### 4. Phantom cannot simulate a transaction against a local validator — *note*

Phantom simulates what it is asked to sign, using its own RPC. It cannot reach
`127.0.0.1:8899`, so against the local validator it will show a warning to the
effect that the transaction could not be simulated, and the amounts it would
normally preview will be missing. Signing still works, and the page submits the
result itself, so the swap proceeds.

Nothing to fix in this repo. Worth knowing before someone reads that warning as
this page doing something wrong.

### 5. Wallets are found by window injection, not by the Wallet Standard — *minor, open*

The connector looks for `window.phantom.solana`, `window.solflare`,
`window.backpack` and `window.solana`. Phantom, Solflare and Backpack all still
inject, so this finds them; a wallet that only registers itself through the
Wallet Standard (`@wallet-standard/app`'s `getWallets()`, the Solana analogue of
EIP-6963) would be invisible to this page even though it is installed.

The cost of fixing it is a dependency and a second code path, for wallets nobody
has asked for here. Worth doing if this connector is ever lifted into ferry-web,
where the wallet list is not "whatever the developer has installed".

Re-read 2026-09-06 and left as it is. Nothing has asked for it since, and the
cost has not changed: the discovery half is small, but *driving* a Wallet
Standard wallet is a second signing path — connect, account,
`solana:signTransaction`, chain identifiers — and there is no such wallet here
to run it against. An
untested second branch in the code that signs a swap is worse than not finding a
wallet nobody has. The condition for doing it is still the one written above.

### 6. ~~The shipped engine cannot be made to expire a swap quickly~~ — *closed 2026-09-06*

`MinLegGapSeconds` is a `const`, so nothing short of a recompile can lower it,
and it forbids any pair of deadlines closer than four hours. That is the right
rule for a swap and the wrong one for a test: it means the timeout path can only
be exercised against a build that is not the shipped one, which is why
`timeout-test.mjs` compiles its own.

The patch is two constants that gate creation only, and the test proves it applied
before it runs — so the delta is small and visible. It is still a delta. Two ways
out, neither taken:

- a build tag (`//go:build fastclock`) holding the test values, so the file under
  test is the file that ships and the difference is a compiler flag;
- making the minimums part of `Config`, which is worse: it is a user-facing knob
  whose only purpose is to weaken the participant's protection.

The first is the better one if this ever needs doing again.

**Closed.** The build tag, which is the option this entry called the better one.
`wasm/limits.go` (`//go:build !fastclock`) holds the shipped values and
`wasm/limits_fastclock.go` the test ones; `timeout:test` builds with
`-tags fastclock` and asserts through `env` that it got them:

```
-tags fastclock  MinLegGapSeconds=120s  MinRemainingSeconds=60s
ok   the engine under test reports the fastclock minimums -- gap=120s remaining=60s
ok   and the shipped defaults are otherwise untouched
```

Every other file in the build is the file that ships, so there is no copied tree
to drift and no interrupted run that can leave a weakened constant behind. The
`Config` option is still refused, for the reason given above.

### 7. ~~A reclaim leaves the money at an address only this browser can reach~~ — *closed 2026-09-06*

Reclaiming a Zenon HTLC does not pay the user. It pays the address that created
the HTLC — this swap's address, whose key is in `localStorage` and nowhere else —
and the funds sit there **unreceived** until the page publishes a receive block,
and then a sweep. Three steps, two of which are easy to walk away from: the first
one visibly "worked", and the money is not home.

Nothing is lost while the record survives, and the page leads with the next step
each time — `timeout:test` watched it do exactly that:

```
ok   the HTLC reads as reclaimed, and the money is not home yet
       -- reclaimed by its creator after expiry
ok   the page asks to receive what the contract sent back  -- Receive 1 incoming block(s)
ok   and then to send it on to the wallet it came from     -- Send 5 back to your wallet
```

But the window is real, and it is the sharpest case of README gap #6 (no recovery
file): between reclaim and sweep, a cleared browser profile strands funds the user
has already been told are theirs again.

Worth either doing all three in one action, or saying plainly on the reclaim
button that it is the first of three. The first is better; there is no reason a
user should meet Zenon's receive semantics here.

**Closed**, by doing all three in one action — the option this entry preferred.
`ZenonAct("znn.reclaim")` now runs `zenonReclaimHome`: it reclaims, waits for the
contract's payment back to appear as an incoming block, receives it, waits for
the balance to land in a momentum, and sends it to the address the record
nominated. `ZenonCostOf` prices all three blocks so the quote and the progress
bar cover the whole job, and the mine's hash count is reported as one running
total instead of resetting twice.

A step that fails does not throw. Everything already published is returned in
`steps`, with `incomplete` naming what is left — because an exception there
would hide blocks that have happened and money that has moved. What is left is
exactly what `planActions` still offers one at a time, so the old behaviour is
the fallback rather than the design.

`timeout:test` asserts the whole of it now, where it used to walk the three
actions in turn and note that the money was not home yet:

```
ok   the participant is offered znn.reclaim -- Reclaim your ZNN and send it back to your wallet
reclaimed 90dd38a6... confirmed=true in 0s
  znn.reclaim  90dd38a6...  the contract returned the funds to this swap's address
  znn.receive  a66a4ca9...  received it into this swap's address
  znn.sweep    15594358...  sent it on to z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d
ok   one reclaim published all three blocks -- znn.reclaim, znn.receive, znn.sweep
ok   and none of them was left owing -- nothing left to do
ok   the HTLC reads as reclaimed and this swap holds nothing
       -- reclaimed by its creator after expiry -- balance 0, 0 unreceived
ok   so the page has no further step to offer for it -- await.counterparty
```

Two waits inside it are load-bearing and neither is optional. The contract's
payment back is a block of its own and is not in a momentum the instant the
reclaim confirms, so the receive waits for it to appear as incoming. A balance
then only moves when *that* block lands, so the sweep waits for the balance
rather than asking the node to send money it cannot see yet — the first run of
this happened to be fast enough not to notice, which is exactly the kind of luck
worth not depending on.

### 8. ~~The refusal to fund against a mismatched leg lives in the plan, not the executor~~ — *closed 2026-09-06*

`planActions` marks `znn.create` not-ready with "the counterparty's leg does not
match the agreement", and the page obeys that. `ZenonAct("znn.create")` does not
re-check it: called directly, it builds the HTLC and publishes it.

Same shape as §3. The page is the only caller, so nobody meets it by accident,
and the guard that matters — the one that stops a person clicking — is in place
and now tested. But the engine's own API will do the irreversible thing if asked,
and the check it is missing is the one already computed a few lines away.

`timeout:test mismatch` deliberately does *not* prove this by calling it: doing so
would lock the participant's money, which is a strange thing for a test about not
losing it to do.

**Closed.** `znn.create` now calls `checkCounterpartyLeg`, which re-reads the
Solana escrow through the same `readSolanaLeg` the plan uses and refuses on the
same two conditions — a funded leg that does not verify, and, for the
participant, no verified leg at all. Because it refuses before building
anything, `timeout:test mismatch` can now prove it by calling it, which is what
this entry said it would not risk:

```
ok   the HTLC step is offered but not ready -- the counterparty's leg does not match the agreement
ok   and the engine refuses it too, not just the page
       -- znn.act: the counterparty's leg does not match the agreement: it holds 0.175 SOL,
          but the swap says 0.35 SOL -- creating the HTLC now would lock your ZNN against
          an escrow that will not pay you
ok   nothing was published by asking -- swap address still holds 5
ok   the 5 ZNN came back -- 5.00000000 ZNN
```

### 9. ~~`--pow` passed once without doing any proof of work~~ — *closed 2026-09-06*

**The premise was wrong.** The claiming address in the first run *did* have plasma:
the test fused 60 QSR to it, sixty seconds before it published. Neither the node nor
the page ever skipped work that was owed. The investigation below is left as written,
because the reasoning was sound and only its one unchecked assumption was not; the
evidence that settles it is at the end.

`ui:test --pow` exists to exercise mining a Zenon block in the tab, and it is the
only coverage that path has. It has now been seen behaving two different ways,
from the same command, on the same chains, about nine hours apart:

| Run | Whole run | The unlock | Mined? |
|---|---|---|---|
| 2026-09-05 15:03 | 2m51s | 28s | no |
| 2026-09-06 00:39 | ~9m | **217s** | yes, mining panel and all |

217 seconds for 110,250,000 hashes is about 508,000 a second, which is the
README's browser estimate almost exactly. The model is right; the first run is
the thing that needs explaining.

The claiming address had no plasma in **both** — the first run's own screenshot,
`docs/ui-maker.png` as it stood, reads "Plasma: none — the next block costs about
4 minutes of proof of work in this tab", and the second run asserts the same thing
before it clicks. The node agrees work is required for such an address:

```
znn.cost znn.unlock -> {"difficulty":110250000,"expectedHashes":110250000,"needsWork":true}
```

110M hashes cannot be done in 28 seconds in a browser; the README's own table puts
it at minutes. So the first run published that block without the work the node
said it needed — and the chain took it.

Where to look: `znn.cost` and the publish path ask the node for the difficulty
*separately* (`ZenonCostOf`, and `PrepareUnlockHtlc` via `op.NeedsWork()`). A zero
from either one skips the mine. Whether the node can answer zero for an account
with no plasma, and under what conditions, is the question — and it is a question
about the chain's own accounting, not about this page.

**Closed.** The chain still held both runs, and a block records what it was charged.
Scanning every htlc call on the devnet (`wasm/cmd/unlockscan`) shows every unlock is
a height-1 block from a fresh address, in two populations — and the two runs are one
of each:

```
when(UTC)            mom    method       from                                     h  difficulty   fused   base
2026-09-05 19:02:50  27068  htlc.Unlock  z1qpffqavsz3dvm5wygzrpnu8e2v9zcnxy3zz2wp 1           0   73500  73500   <- run 1
2026-09-06 04:44:50  29411  htlc.Unlock  z1qr6cxf3asd5w7gvyumgcdt3jsuupvfgax8adkf 1   110250000       0  73500   <- run 2
```

(The run log is local time; momentums are UTC, four hours ahead.) Run 1 paid its
73,500 base plasma with **fused** plasma, so the node was right to price it at zero.
`embedded.plasma.getEntriesByAddress` confirms where that came from — a 60 QSR
fusion entry naming the run-1 unlocker as beneficiary, and none for run 2's. 60 QSR
is 126,000 plasma, which covers an unlock's 73,500 with room to spare.

Reading the fuses back by expiration height (`FuseExpiration` is 3600 momentums)
dates them, and the two runs differ in *how many* the script published:

```
run 1   27058 fuse -> the ZNN sender's swap address
        27060 fuse -> the receiving wallet
        27062 fuse -> the CLAIMING swap address   <- the one --pow means to leave bare
        27066 htlc.Create        27068 htlc.Unlock, unmined
run 2   29390 fuse -> the ZNN sender's swap address
        29392 fuse -> the receiving wallet
        29411 htlc.Unlock, mined
```

Three fuses against two, in exactly the order `ui-test.mjs` writes them. Run 1 ran
the `if (!MINE_IN_BROWSER)` branch that fuses the receiver; run 2 skipped it. So the
first run was not exercising the proof-of-work path at all — it was an ordinary
fused run wearing a `--pow` label, and the 28 seconds was a momentum's wait rather
than a mine that never happened.

The label is the whole of it, and not a missing guard: **two minutes later a real
`--pow` run went through and mined correctly** — two fuses, no fusion entry for its
claiming address, and an unlock at 27079 carrying the full 110,250,000. So the guard
was in place at 15:03 and did its job at 15:05. The table above simply has two
different commands in it.

That run also disposes of the arithmetic the original report leaned on. Its unlock
acknowledges momentum 27078 and landed at 27079 — one momentum, so the mine took
about ten seconds, not the ~217 the mean predicts. Nothing is wrong with it: a
proof-of-work search is geometric, not fixed, and finishing 110,250,000 expected
hashes inside the ~5M a browser tab manages in ten seconds is roughly a one-in-twenty
outcome, which will turn up every so often across a session's runs. "Too fast
to have mined" is therefore not evidence on its own, and that is what made a
mislabelled run look like a chain bug. The evidence that does hold is
the block's own `difficulty` field, and every unlock the devnet has ever recorded —
34 of them — is either `(difficulty 110250000, fusedPlasma 0)` or
`(difficulty 0, fusedPlasma 73500)`, with none paying less than its base plasma.

Two things were checked before accepting that, because "the test lied to itself" is
the comfortable answer and not automatically the true one. Both are now asserted by
`plasma:test` (`wasm/cmd/powpaths`, about a minute against a live node):

- **The two callers cannot disagree.** `ZenonCostOf` and `Prepare` were supposed to
  be able to answer differently, one of them zero. They ask
  `embedded.plasma.getRequiredPoWForAccountBlock` with the same block type, the same
  recipient and the same-shaped payload, differing only in whether the arguments are
  real; the node returns the same difficulty for all five actions. Base plasma for an
  embedded call comes from the decoded *method*, not from its arguments, so it could
  not have been otherwise.
- **The chain does not accept a block claiming plasma it does not have.**
  `enoughPlasma` in go-zenon's `vm/vm.go` checks `available < block.FusedPlasma` and
  refuses before it ever compares total against base. Offering one is answered with
  `not enough plasma on account`. A page cannot publish a block the chain should have
  charged for, whatever it puts in the field.

The assertions added when this was half-fixed — the claiming page must show no
plasma, and the mining panel must appear — are what stops the mislabelled run from
recurring, and they hold. `plasma:test` is the cheap version: it prices an unlock on
a bare address at 110,250,000, fuses 60 QSR, prices the same call at zero, and
publishes unmined, so the whole rule can be checked without nine minutes of hashing.

### 10. ~~A failed plasma probe is displayed as a confident wrong number~~ — *closed 2026-09-06*

Found while closing §9. `status.go` asks the node what the next call will cost and
keeps the answer only if the call succeeded:

```go
if req, err := client.RequiredPoWFor(...); err == nil {
    sa.PlasmaFused = req.RequiredDifficulty == 0
    sa.PendingWork = req.RequiredDifficulty
}
```

On any error both fields keep their zero values — `PlasmaFused` false, `PendingWork`
0 — and those two together are a state the node can never actually report. The page
renders it as

> Plasma: none — the next block costs about **1 seconds** of proof of work in this tab.

because `workMinutes` floors at `Math.max(1, ...)`. A node that is unreachable, or
briefly refusing, therefore reads as a confident and specific claim about an account
it failed to ask about. The comment above the call already anticipates half of this
("an error here would read as 'no plasma'"); what it does not say is that the same
default also claims the work is free.

Nothing is lost: the publish path asks the node again and gets the real difficulty,
so the block is still priced and mined correctly. The cost is a user told a four
minute grind is a one second one, and a `--pow` run whose "the claiming side has no
plasma" assertion would pass on a probe that never answered.

The fix is a third state rather than a boolean — leave plasma unknown when the probe
fails and say so — which is a UI decision as much as an engine one, so it is written
down here rather than guessed at.

**Closed**, with the third state this entry asked for rather than a guess at it.
`SwapAddressStatus` gained `plasmaKnown` and `plasmaProblem`; the probe sets them
only when the node answers, and the page reads the unknown case first and dims
it, saying that it is not a claim there is no plasma. The two states that are
decisions — fused, or this much work — still read exactly as before.

Covered by a unit test rather than by a chain, since the interesting input is a
node that will not answer: `TestPlasmaProbeFailureLeavesPlasmaUnknown` runs the
probe against a stub node that refuses, one that is not there, and two that
answer, and checks the four outcomes. It needs no devnet and runs with
`go test ./...`.

---

## Found in this session — 2026-09-06

### 11. Every plain Zenon send waited a minute and a half for nothing — *minor, fixed*

Found while making the reclaim one action. `ZenonAct` ended every operation with
`WaitForContractResult(..., 90s)`, which waits for a block's *paired* block.

That is the right wait for a call to an embedded contract: the contract does its
work in the block it produces in reply, and returning before that leaves the
caller looking up an entry that is not there yet. It is meaningless for the other
two. A receive has no second half at all. A plain send is paired by the
**recipient's** receive — so a sweep to somebody's Syrius address waited for
them to open Syrius, and then timed out reporting `confirmed=false` about a block
the node had accepted ninety seconds earlier.

Nothing was ever wrong with the block. The cost was ninety seconds on the end of
every sweep, which was tolerable while a sweep was its own button and is not
while it is the last third of a reclaim.

**Fixed:** the wait is now taken only for `znn.create`, `znn.unlock` and
`znn.reclaim` (`isContractCall`). For the other two `confirmed` means what it
says for them — there is nothing left to wait for — and the field's doc comment
says so. `timeout:test mismatch` now sweeps and confirms in a few seconds; the
ninety it used to spend is what `WaitForContractResult` does when the paired
block it is waiting for is one only the recipient can publish.

**And it was load-bearing somewhere it should not have been.** `timeout:test
abandon` swept, then asked the test wallet to receive **once, immediately**, and
checked the balance. That is too early: a sweep is an ordinary send and is not
receivable until it is in a momentum. It had always passed, because the ninety
seconds `znn.act` spent waiting for a block that would never be paired was
exactly the pause the receive needed. Removing the wait turned it into
`the 5 ZNN came back to the wallet that sent it -- 0.00000000 ZNN`.

The assertion was right and the test was wrong, so the test now retries the
receive the way every other one here does. Worth writing down as a shape rather
than an incident: a wait that exists for one reason will be depended on for
another, and the dependency is invisible until the wait goes.

### 12. The store could be exported but not imported — *serious, fixed*

`store.import` has been in the call table since the beginning, and `smoke`
exercises it. The page never called it: App.vue had an Export button and nothing
to read the file back with.

That is the sharp edge of README gap #6. The export is the only thing standing
between a cleared profile and a swap whose per-swap Zenon key is gone — and it
was a file the page that wrote it could not read.

**Fixed:** an Import button beside Export, reading a file and calling
`store.import`, which merges by id and keeps whichever copy of a record is newer,
so restoring an old file over live swaps cannot roll one back. It says how many
records it took and how many it left alone.

That does not close gap #6, and the README still lists it: pre-signing a refund
the way ferry does is a different thing from a file somebody has to remember to
save. What it fixes is that the file was useless.

---

---

## Found in this session — 2026-09-07

A comparison against ferry-web's ironed-out primitives (`review.md`), and the
fixes for what it found. ferry's own register is `docs/REVIEW-2026-09.md`; the
labels below are that document's where a finding has a counterpart there.

### 13. Both outcome scanners could be answered by a decoy — *blocking, fixed* (ferry M1)

The sharpest thing in the comparison, and the only one with a complete path to a
counterparty taking a leg.

Once a leg settles the contract is gone, so *which* exit happened is recovered
from history — and both scanners took the first candidate that had the right
shape. `sol.escrowInstructionIn` matched on the program id alone, never reading
`ix.Accounts`, which it parsed and discarded. `znn.FindHtlcOutcome` matched on
the entry id and stopped at the first `Unlock`, whatever its preimage was.
`adoptPreimage` then hash-checked the preimage, refused it — and the caller had
already set `Settled`.

`getSignaturesForAddress` indexes a transaction under *every* account key it
names, including read-only ones no instruction touches. So on Solana, "a
transaction that mentions this escrow" is a set anybody can add to for one fee.
The attack is one extra transaction:

1. The initiator redeems the participant's escrow, publishing the preimage —
   the only place the participant can learn it.
2. They immediately submit a `refund` of an escrow of their own, with the
   victim's now-closed escrow along as an extra account key.
3. The victim's scan finds the decoy first (newest first), reports *"refunded to
   the sender after the timelock"*, and never adopts the preimage.
4. The victim's Zenon HTLC expires. The initiator reclaims it. The victim paid
   SOL and received nothing.

The decoy wins **permanently**: the escrow is closed, so nothing will ever touch
it again to push the decoy down the 50-signature window. `settleOutcome` then
writes "refunded — both legs returned to their senders" onto the record and
archives it. The same shape exists on Zenon, where publishing a send to the htlc
contract costs a fee and the contract merely declines it — the block is on the
chain either way, and the scan walks blocks.

**Fixed** in three places, because two of them would have been enough only until
the next caller:

- `escrowInstructionIn` takes the escrow and requires the instruction's account
  0 — which is the escrow in both spending branches — to be it.
- Both scanners identify a settlement by **hashing**: a candidate whose preimage
  does not open the swap's hashlock is not a match, and the scan carries on past
  it. This is ferry's `FindPreimage` rule (*"Identification is by hashing"*),
  and `znn.matchesHashLock` is now the named predicate.
- `adoptPreimage` keeps its own check and says why it is now the second one
  rather than the only one.

Found while fixing it: `getTransaction` was requested without
`innerInstructions`, so a redeem reached by CPI was invisible. Now read.

*Tests:* `sol.TestOutcomeIgnoresAnInstructionForAnotherEscrow` (the decoy above),
`TestOutcomeIgnoresARedeemThatDoesNotOpenThisHashlock` (the strongest form — a
preimage that is perfectly valid for a *different* swap, so only hashing can
catch it), `TestOutcomeFindsARedeemInvokedByAnotherProgram`,
`TestOutcomeSkipsFailedTransactions`, `TestOutcomeStillFindsOurOwnRefund`,
`znn.TestFindHtlcOutcomeIdentifiesTheUnlockByHashing`.

### 14. The token's decimals came from the counterparty — *blocking, fixed* (ferry Z1/Z2)

An amount is a number and a unit, and only the number crossed as a number.
`ZnnAmount` is base units and `ZnnDecimals` turns them back into a figure a
person reads; both arrived in the offer and neither was ever checked against a
chain. `Accept` never called `resolveToken`, and `validateTerms` bounded
decimals at 0..18 and nothing more.

So an offer for real ZNN saying `ZnnAmount: "10", ZnnDecimals: 0` renders as
**"10"** on every screen on both sides — `FormatAmount(10, 0)` and
`FormatAmount(10^9, 8)` are the same string — and then verifies perfectly
against an HTLC holding ten base units, a ten-millionth of what was displayed.
Right token, right hashlock, right parties, right expiry; the amount comparison
is base units against base units, so it matches too.

solzen already had ferry's Z1 (the token standard *is* compared against the
agreed one) and the first half of Z2 (a null RPC result is refused). This was
the layer under both.

**Fixed.** `checkAgreedToken` resolves the agreed ZTS against this browser's own
node at `Accept` and `ApplyAccept` and refuses terms whose decimals disagree,
naming what the figure would display as on each side. `znn.Client.Token` gained
ferry's two `GetToken` guards: the node must echo back the ZTS it was asked
about, and an implausible `decimals` is refused — without the echo, a node
answering `getByZts` about some *other* token silently rewrote the maker's own
amount at `Create` time.

A node that cannot be asked is a **refusal**, not a pass, which is the opposite
of what `checkPayoutReachable` does with an unreachable node and deliberately
so: proxy-unlock defaults to allowed, so failing open there restores the common
case; failing open here accepts an amount whose unit was never established.

*Test:* `TestAgreedTokenIsCheckedAgainstTheChain`, which asserts the display
collision itself first, so a change to `FormatAmount` cannot quietly remove the
reason the check exists.

### 15. `sol.create` never got the guard `znn.create` got — *serious, fixed*

§8 above closed exactly this defect on the Zenon side and the fix went to one of
the two funding calls. `SolanaInstruction("sol.create")` checked terms
completeness and the timelock, and never asked whether the counterparty's HTLC
existed or matched — so the engine would fund the participant's leg first, which
`planActions` computes a refusal for and the page obeys.

**Fixed.** `checkCounterpartyLeg` now covers both directions, reading whichever
leg is the counterparty's through the same readers the plan uses, and
`sol.create` calls it. It also enforces the two rules that used to be warnings
(§16).

### 16. The ordering re-check was a warning above a live button — *serious, fixed* (ferry M7)

`CheckOrdering`'s own comment says the moment a swap stops being safe is *"the
moment the UI has to stop telling someone to fund it"*. `Refresh` appended its
failure to `st.Warnings` and `planActions` computed `blocked` from two other
conditions, so the funding action stayed `Ready` and `Primary` underneath it.
The same was true of a clock skew past `MaxClockSkewSeconds`: the arithmetic the
ordering rule rests on could not be done, and the button stayed lit.

**Fixed.** `Status.Unfundable` carries the reason and `planActions` uses it as
the blocking reason, last so it wins. Funding only — claiming and refunding take
money back, and a rule about deadlines is not a reason to stop somebody
recovering their own. `checkCounterpartyLeg` asks the same two questions in the
engine.

### 17. Nothing recorded which chains a swap was on — *serious, fixed* (ferry `chainid.go`, M7)

`Config` is one global `SolanaURL` and one `ZenonURL` and every swap is read
through it. Nothing on a swap said which cluster or which Zenon chain it was
made against — an RPC URL is a name somebody typed, and a program deployed from
one keypair sits at the same address on every cluster it was deployed to, so
`terms.SolProgram == cfg.SolProgram` is satisfied by two people on different
clusters.

Change the node URL and every existing swap is silently re-read against the new
chain: `readSolanaLeg` finds no escrow, the leg reads *not funded*, and the next
thing offered is **"Lock 0.35 SOL in the escrow"** — on the wrong chain, with
real lamports. The only clock check is `SolTimelock <= now`, which a real
cluster's clock passes.

**Fixed.** `Terms.SolGenesis` and `Terms.ZnnChainID` are read at `Create` and
carried in the terms, so an acceptance echoing the struct back cannot come from
a browser on another chain. `Terms.CheckChains` compares them against the
configured nodes at `Accept`, before every funding call, and at the top of
`Refresh` — where a mismatch stops the legs being read at all, because "not
funded" would be a false answer rather than a missing one. Empty fields are not
a mismatch: a record written by an older build has less protection, which is not
a reason to strand it.

*Test:* `TestChainPinningRefusesTheWrongCluster`.

### 18. A panic took the module, and with it the only way to reach the keys — *serious, fixed* (ferry L6)

`callFn` ran `api.Handle` on a bare goroutine. A panic anywhere in a call —
go-zenon's `common/types`, ABI packing, base58, a malformed record — aborts the
Go runtime and `solzen.call` stops answering.

That is worse here than in ferry for the reason gap #6 already gives: there is
no recovery file, so the per-swap Zenon key and the initiator's secret are
reachable only through `store.export`, which is a call through the bridge that
just died. The remaining option is devtools.

**Fixed.** A `recover()` turns a panic into a failed call. `panicMessage`
renders the value with `fmt` and never `js.ValueOf` — ferry's L6 — because
`js.ValueOf` panics on a Go value it cannot convert, and it would do so inside
the handler that exists to catch panics, the one place a second panic cannot be
caught.

### 19. No Content-Security-Policy — *serious, fixed*

`ui/index.html` had `charset`, `viewport` and `title`. Behind that missing
header is one `localStorage` record per swap, each holding the per-swap Zenon
key and, for the initiator, the secret — and `store.go`'s own comment says any
script on the origin can read them.

**Fixed**, with ferry's policy and ferry's two arguments carried over intact:
`'wasm-unsafe-eval'` is required and grants WebAssembly compilation and nothing
else, and `connect-src *` is a deliberate trade so that choosing a node stays
the user's. `frame-ancestors` is ignored in meta form, so a host that can set
headers should still add `X-Frame-Options: DENY`.

### 20. The executable check existed only in a comment — *serious, fixed* (ferry Z3)

`checkNodes` carried the comment *"An account that is not executable is not a
program. Saying so here is cheaper than a transaction that fails…"* above code
that reported `ok: true` from the account merely existing. `sol.accountValue`
had no `Executable` field. The README repeated the claim.

**Fixed**, and extended to the question that actually matters and that this
project cannot answer from its own source: `sol.Client.Program` reads the
account, checks `executable`, and — under the upgradeable loader — follows the
programdata address to report whether anyone can still replace the code and who.
An upgradeable program's code, and so the terms of every escrow already funded
under it, belongs to its authority. README gap #8 now says this, and the deploy
snippet says why it is not `--final` locally.

Against the running devnet it reports what it should:

```
ok   and checks that it is executable, rather than only present
ok   and says whether anyone can still replace its code
       -- upgradeable, authority 8S8cXUuRjrqr9obfqjdCJNRQqDgiM7QZKA4T1XkK2Fek
```

### 21. A stranger could kill a swap by paying its escrow address — *serious, fixed*

`create` refused an escrow PDA that already held lamports, and
`system_instruction::create_account` fails on one anyway. The escrow address is
a pure function of a swap id that travels in the offer string, so anyone who has
seen an offer can compute the address and pay it, and the swap can then never be
funded. No money is at risk; a trade dies and the error blames the id.

**Fixed** in `program/src/lib.rs`: when the PDA already holds a balance the
account is opened the long way — `transfer` the shortfall, then `allocate` and
`assign`, the PDA signing for itself — and the stranger's contribution simply
becomes part of the rent the initiator would have paid. `create` now keys "this
id is taken" off the account having *data* or a non-system owner rather than off
its balance, and asserts the funded total afterwards.

**The price of that grief is not one lamport**, which `review.md` originally
said and which the test disproved: the runtime refuses to leave any account
below rent exemption, so the least a stranger can park there is the rent-exempt
minimum for a zero-length account — 890,880 lamports on this chain. Cheap enough
that the fix is still worth having, and worth writing down because the cheaper
figure was an assumption nobody had run.

### 22. The program accepted a redeem its own client could not read — *minor, fixed*

`redeem` bounded the preimage length above and not below, so a zero-length
preimage was hashed. `sol.BuildRedeem` refuses one and `sol.DecodeRedeemPreimage`
does not recognise it — so the program accepted an instruction the scanner would
look straight past, which is §13's failure mode arriving by accident.
Unreachable in practice, and the file's claim is that the two implementations of
this layout are kept in step.

**Fixed:** `len == 0` is refused, and `znn.matchesHashLock` refuses an empty
preimage on the other chain for the same reason.

### 23. Smaller things found in the same pass — *minor, fixed*

- **The recorded Solana signatures did nothing.** `SolCreateSig` and its
  siblings are documented as stopping a refresh re-proposing a step in flight,
  and nothing read them: between broadcast and confirmation the leg reads
  unfunded and the page re-offered *"Lock 0.35 SOL in the escrow"*. Now
  `notePendingCreate` asks the cluster what became of the signature and
  distinguishes the three answers — landed (wait), never seen (dropped, fund
  again), failed (say so) — because a bare "waiting" that could never clear
  would be its own trap.
- **`Htlc` did not check the entry it got back** was the one it asked for
  (ferry L2).
- **A hex hashlock was refused.** `HashLock []byte` decoded as base64, and a
  64-character hex string is *also* valid base64 — it decodes to 48 meaningless
  bytes, which then fail the comparison. `znn.DecodeHashLock` reads both and
  lets the digest-sized result win (ferry L1). It fails closed either way, so
  this was a legitimate HTLC refused rather than a bad one accepted.
- **`sa.Sufficient` failed open.** `bigFromString` returns zero for anything
  unparseable, and every balance is at least zero, so an unusable agreed amount
  read as "you have enough to create the HTLC". `amountOf` is the failing-closed
  version, used where the figure is a threshold.
- **`MinimumRent` was dead code** with a comment saying it existed so that "the
  swap costs 1 SOL" is not quietly untrue. The funding step now names the rent
  and says it comes back.
- **Addresses in a pasted envelope were never parsed.** They failed closed at
  funding time with a message about a mismatch, when the real problem was a
  damaged string pasted minutes earlier (ferry L4).
- **The wallet's answer was not compared to the question.** `send` broadcast
  whatever `signTransaction` returned; it now compares the serialized message
  and refuses a different one. It also refuses to build a transaction whose
  required signer is not the connected account, which used to fail on chain
  with a message about a missing signature rather than about the account.
- **`Import` validated nothing** but the id being non-empty (ferry M4).
  `Swap.validate` now checks the id against the terms, the secret against its
  own hashlock, the per-swap key, the sweep address and the HTLC id; bad records
  are refused individually and named, rather than aborting the run, and the page
  says which did not come back.

### 24. The contract's account-substitution cases had no coverage — *minor, fixed*

`program-test.mjs` covered the happy paths, a wrong preimage, a wrong receiver, a
past timelock, an early refund, a late redeem and a double redeem — all of which
are *bad instructions*. What had no coverage anywhere was the family that
actually loses money in escrow programs: a good instruction pointed at the wrong
account, which is what `load`'s PDA re-derivation exists to refuse.

**Fixed** by extending the existing harness rather than adding a Rust one. A
`litesvm`/`solana-program-test` suite would run without a validator, which is
what `review.md` suggested; against that, these run against the real deployed
program on a real chain, which is stronger evidence, and the harness is already
a deliberately independent second reading of the instruction layout. Eight new
checks:

```
ok   a refund cannot be redirected to another account
ok   a preimage for one escrow cannot spend another
ok   an account this program does not own is not an escrow
ok   the same swap id cannot be created twice
ok   a zero amount is refused at creation
ok   an empty preimage is refused
ok   nothing above moved any money -- 251907040 and 51907040 lamports still held
ok   both escrows still redeem with their own preimage
```

The last two are the ones that make the other six mean anything: nothing moved,
and the legitimate spends still work, so the refusals are refusing the right
thing rather than everything. The redirect case is asked *after* the timelock
has passed, because asked before it the clock would refuse it and the initiator
check would go untested.

## Carried from README §"Status and known gaps"

Unchanged by this session unless noted. Numbers match the README.

| # | Gap | Severity |
|---|---|---|
| 1 | Swap records are not encrypted at rest — `localStorage` holds the per-swap Zenon key and the initiator's secret | serious |
| 2 | Proof of work is single-threaded | minor |
| 3 | The module is 16.5 MB, nearly all of it `common/types` pulling in a JSON-RPC server | minor |
| 4 | The HTLC id is found by scanning, bounded at 2,000 blocks | minor |
| 5 | SOL only; no SPL tokens | note |
| 6 | No recovery file — refunds depend on the page still existing, and on somebody having pressed Export. Import now exists (§12), so the file is at least readable | serious |
| 7 | ~~Timeout paths are tested at the contract level, not end to end~~ — closed 2026-09-05 by `timeout:test`, both directions; what is left is through the page, and with real deadlines | minor |
| 8 | Nothing verifies the program at that address is this source | serious |

## Concerns about the tests themselves

- **Shared devnet accounts.** `swap:test`, `ui:test`, `timeout:test` and
  `plasma:test` all publish from Zenon account index 1. Two at once will build on
  the same account chain and one will be rejected. They have to be run in series, which is most of
  why a full sweep takes the best part of an hour. Balance assertions are the
  first thing to go wrong when they are not: drain what earlier runs left
  unreceived before measuring, the way every script here now does.
- **An interrupted run leaves `devnet-wallet.exe` behind**, still publishing from
  index 1. Kill it before starting anything else.
- **`timeout:test` no longer copies the module.** It builds `wasm/` in place with
  `-tags fastclock` (§6), so an interrupted run leaves a binary in `bin/` and
  nothing else. `SOLZEN_SHORT_SECONDS` and `SOLZEN_LONG_SECONDS` shorten the
  deadlines; 420 and 660 is enough to fund both legs and still watch them
  expire, and takes about twelve minutes instead of twenty-five.
- **Editing `ui/` during a browser run kills the run.** Vite hot-reloads, the
  execution context is destroyed, and the failure looks like a hang.
- **Never edit a shell script that is currently running one of these.** `bash`
  reads a script incrementally, from a byte offset — rewrite the file underneath
  it and it resumes in the middle of the new text, running fragments of it. Doing
  that here started a second run against the same Zenon account while the first
  was live, and the only symptom was one balance assertion off by 2 ZNN. Give the
  new version a new filename.

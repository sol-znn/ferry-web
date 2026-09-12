# solzen vs ferry-web — a scope and safety comparison

> **Status: every finding below is fixed as of 2026-09-07.** This document is
> kept as the reasoning, not as a to-do list; `ISSUES.md` §13–§24 records what
> each fix was and how it is now pinned. Two things changed in the fixing and
> are corrected in place below: **M5**'s cost to an attacker is ~890,880
> lamports, not one lamport — the runtime will not leave an account below rent
> exemption, which the test found — and **L7** was closed by extending
> `program-test.mjs` against a real validator rather than by adding a Rust
> harness, for the reason given there.

What ferry-web has ironed out that solzen has not, read as a list of *primitives*
rather than of diffs. The two codebases are deliberately the same shape — Go to
WebAssembly, a JSON call table, one `localStorage` record per swap, a Vue 3 UI —
so most of ferry's `docs/REVIEW-2026-09.md` translates across almost line for
line, and where it does not, the reason is usually structural and worth saying
out loud.

Scope of this pass: `program/src/lib.rs`, `wasm/`, `ui/src/core/solana.js`, the
test scripts, and the two projects' security documents. **The web interface is
not reviewed and nothing in it is proposed for change** — the two UI-adjacent
findings below (CSP, the wallet handoff) are about `ui/index.html`'s `<head>` and
`ui/src/core/solana.js`'s signing path, not about layout or components.

Everything here is from reading the code and confirming the surrounding paths.
Nothing was exploited on a live chain; where a finding is an argument rather than
an observation, it says so. The tree is green as reviewed: `go vet ./...` and
`go test ./...` pass.

Severities follow ferry's, which are about money and not tidiness:

- **Critical** — a counterparty can take funds, or the user is told something
  false about safety.
- **High** — funds can become unrecoverable, or a check silently does not run.
- **Medium** — wrong state, wrong advice, or a path that fails when it should not.
- **Low** — robustness and correctness with no path to loss.

---

## Critical

### C1 — The outcome scanners identify a settlement by its *shape*, not by its *identity*

`wasm/sol/client.go` (`escrowInstructionIn`, `FindEscrowOutcome`),
`wasm/znn/scan.go` (`FindHtlcOutcome`), `wasm/status.go` (`adoptPreimage`)

This is ferry's **M1** (`absorbSpend` trusted the backend's txid without checking
it spent our output) together with ferry's `FindPreimage` design, and solzen has
neither half. It is the finding that made this document worth writing.

Both exits close the contract, so once a leg is gone the only record of *which*
exit happened is the chain's history — and both scanners stop at the first
structurally plausible candidate and hand it to `adoptPreimage`, which checks the
preimage against the hashlock and, on a mismatch, **discards the secret and keeps
the outcome**:

```go
// status.go
case out.Redeemed:
    st.Sol.Settled, st.Sol.Tx = true, out.RedeemSig
    st.Sol.Note = "redeemed with the preimage"
    m.adoptPreimage(s, st, out.Preimage, "the Solana redeem transaction")
```

`adoptPreimage` appends a warning and returns. `Settled` is already true. The
search does not continue.

**On Solana it is worse than that, because the instruction is never tied to this
escrow at all.** `escrowInstructionIn` parses `ix.Accounts` into its struct and
then never reads it:

```go
for _, ix := range tx.Transaction.Message.Instructions {
    if ix.ProgramIDIndex ... != want { continue }   // right program
    raw, _ := DecodeBase58(ix.Data)
    if preimage, ok := DecodeRedeemPreimage(raw); ok { return tagRedeem, preimage, nil }
    if raw[0] == tagRefund { return tagRefund, nil, nil }
}
```

The signature list came from `getSignaturesForAddress(escrow)`, which indexes a
transaction under *every* account key it names — including read-only ones that no
instruction touches. So any transaction that (a) succeeds, (b) mentions the
victim's escrow anywhere in its account keys, and (c) contains one instruction to
this program for *some other* escrow, is accepted as this escrow's outcome.

**The attack, in the direction where Solana is the participant's leg**
(`Initiator == LegZenon`, which is half of all swaps):

1. The participant funds the Solana escrow. The initiator funds the Zenon HTLC.
2. The initiator redeems the escrow. That publishes the preimage on Solana — it
   is the *only* place the participant can learn it, exactly as
   `program/src/lib.rs` says in its header comment.
3. The initiator immediately submits a second transaction: a `refund` (tag 2, no
   data) of an escrow of their own that is genuinely refundable, with the
   victim's now-closed escrow added as an extra account key.
4. The participant refreshes. `Signatures(escrow, 50)` returns the decoy first
   (newest first). `escrowInstructionIn` sees a valid `tagRefund` and returns it.
   The page reports **"refunded to the sender after the timelock"** and never
   looks at the real redeem.
5. The preimage is never adopted. The Zenon HTLC expires. The initiator reclaims
   it. The participant has paid SOL and received nothing.

The decoy wins permanently, not transiently: the escrow is closed, so no further
transactions will ever touch it, and the decoy stays at position 1 of a 50-entry
window forever. `settleOutcome` then writes `"refunded -- both legs returned to
their senders"` onto the record and archives it. The user is told the swap unwound
cleanly. It did not.

The Zenon side has the same disease from a different cause. `FindHtlcOutcome`
*does* match the entry id (`gotID == id`) — so the Solana account check has an
internal precedent a few files away — but it stops at the first `Unlock` for that
id. A decoy `Unlock(id, junk)` published after the real one is a send block on the
contract's chain like any other; it is newer, it wins, and the preimage is lost
the same way.

Ferry's answer is stated in `wasm/znn/ledger.go`: *"Identification is by
hashing"*. `FindPreimage` walks candidates and returns the first one whose
preimage actually hashes to the swap's secret hash, so a decoy costs the attacker
a fee and buys nothing.

**What is missing, precisely:**

1. `escrowInstructionIn` must check that the instruction operates on *this*
   escrow — resolve `ix.Accounts[0]` through `AccountKeys` and require it to equal
   the escrow pubkey. This is the outpoint check from ferry's M1.
2. Both scanners must treat a preimage that does not hash to the swap's hashlock
   as *not a match* and keep looking, rather than as a match with a bad payload.
   The hashlock is already in hand at both call sites.
3. `FindEscrowOutcome` should not mark `Settled` off a candidate the preimage
   check then rejects.

Worth a test in ferry's style — a decoy carrying a *valid* preimage for a
different swap, so only the account check can catch it.

**Also, related and cheaper to fix:** `escrowInstructionIn` requests
`getTransaction` with `encoding: json` and reads only
`transaction.message.instructions`. Inner instructions (`meta.innerInstructions`)
are not requested, so a redeem performed by CPI from another program is invisible
to the scan. Not an attack on its own — nothing in this project redeems by CPI —
but it is the same "the settlement I am looking for might not be where I look"
gap, and it is one field on the request.

### C2 — The token's decimals are taken from the counterparty's string and never re-derived from the chain

`wasm/swap.go` (`Terms`, `validateTerms`), `wasm/manager.go` (`Accept`,
`resolveToken`), `wasm/status.go` (`readZenonLeg`), `wasm/act.go`
(`formatZnnAmount`)

This is ferry's **Z2** with the mechanism moved. Ferry's version was a null RPC
result decoding into `decimals: 0`; solzen has that guard (`znn/client.go` refuses
a null result with `ErrNotFound`, which is the right fix and is in place). What
solzen does not have is the *structural* half of ferry's answer.

Ferry keeps the agreed Zenon amount as **the decimal string the user typed**
(`Zenon.AmountDisplay`) plus a token standard, and converts to base units at
verification time using decimals looked up from the chain for the **agreed**
token — `zenonExpectations` in `wasm/manager.go`. solzen freezes both the base
units and the decimals into `Terms` at creation and never asks the chain again:

```go
// swap.go
ZnnAmount   string `json:"znnAmount"`     // base units, from the maker
ZnnDecimals int    `json:"znnDecimals"`   // from the maker's node, trusted forever
```

`Accept` does not call `resolveToken`. `validateTerms` checks only
`0 <= ZnnDecimals <= 18`. Every display goes through
`formatZnnAmount(Terms.ZnnAmount, Terms.ZnnDecimals)`, and `readZenonLeg` compares
base units to base units — so the pair is internally consistent no matter what it
says.

**The attack.** A maker who sends ZNN hand-crafts an offer envelope (base64url
JSON behind a documented prefix — `validateTerms` exists precisely because this is
a hostile input) with:

```
ZnnToken:    zts1znnxxxxxxxxxxxxx9z4ulx   (real ZNN)
ZnnAmount:   "10"                          (10 base units = 0.0000001 ZNN)
ZnnDecimals: 0
```

Confirmed by running the formatter: `FormatAmount(10, 0)` is `"10"`, where the
honest pair `FormatAmount(10⁹, 8)` is *also* `"10"`. So the taker's page says
**"Claim 10 ZNN"** on every screen. They lock their SOL, the maker creates an HTLC
holding 10 base units, and `readZenonLeg` verifies it: right token, right
hashlock, right parties, right expiry, and `info.Amount.Cmp(bigFromString("10"))
== 0`. The leg reads **verified, holds 10**.

Every check ferry's Z1 added is present here — the token standard *is* compared
against the agreed one, and the agreed terms are never overwritten by the observed
ones, both of which solzen gets right. The hole is one layer down: the *unit* the
amount is denominated in is a term of the trade, and it arrives from the
counterparty.

**What is missing:**

1. `Accept` (and `ApplyAccept`) must resolve `Terms.ZnnToken` against the user's
   own node and refuse a swap whose `ZnnDecimals` disagrees with what the chain
   says. One call, at the one moment where refusing costs nothing.
2. `Client.Token` needs ferry's `GetToken` guards, which solzen's does not have:
   require the node to echo back the ZTS that was asked for, and refuse an
   implausible `decimals`. Without the echo, a node that answers `getByZts` with
   some *other* token's metadata quietly rewrites the maker's own amount at
   `Create` time — the same defect, self-inflicted rather than delivered.
3. In ferry's spirit, a failure to look either up should be a **refusal** naming
   what could not be checked, not a pass. That is ferry's **Z3**, and the
   reasoning is already written into solzen's own `readSwapAddress` plasma probe
   (ISSUES §10): *a check that did not run is indistinguishable from one that
   passed*.

Carrying the decimal string alongside the base units — and comparing both — would
close it without changing the offer format's shape.

---

## High

### H1 — `sol.create` does not re-check the counterparty's leg; `znn.create` does

`wasm/act.go`

solzen's own ISSUES §8 identified this exact defect on the Zenon side and closed
it: `znn.create` now calls `checkCounterpartyLeg`, which re-reads the Solana
escrow through the same `readSolanaLeg` the planner uses and refuses on the same
two conditions. The write-up is explicit about why — *"the engine's own API will
do the irreversible thing if asked, and the check it is missing is the one already
computed a few lines away"*.

The fix was applied to one of the two funding calls.
`SolanaInstruction("sol.create")` checks only that the terms are complete and that
the Solana timelock has not passed. It does not ask whether the counterparty's
Zenon HTLC exists or matches, and for the participant it does not enforce *"you go
second, on purpose"* — which `planActions` computes and the page obeys, and which
the engine does not.

Same severity argument as §8: the page is the only caller today, so nobody meets
it by accident. But `sol.create` is the irreversible step for the side that sends
SOL, the API is reachable from devtools, a script, a retry or a later UI, and the
guard it needs is the mirror image of one already written.

The symmetric `checkCounterpartyLeg` would read the Zenon leg through
`readZenonLeg` and refuse a funded-but-unverified counterparty HTLC, and refuse
the participant funding first.

### H2 — A panic in the module takes the only copy of the swap keys with it

`wasm/main_js.go`

ferry's bridge wraps every call in a `recover()`, with the reason written down:

> A panic in Go/WASM kills the whole module, and the module is where the only copy
> of an unsaved key lives. Turning it into a rejected call keeps the page alive
> long enough for the user to export their swaps.

solzen's `callFn` spawns the goroutine and calls `api.Handle` with no recovery.
`Handle` has none either. A panic anywhere in the call — in `go-zenon`'s
`common/types`, in ABI packing, in the base58 or edwards25519 paths, in a nil
deref on a record that came back malformed — aborts the Go runtime and the page's
`solzen` object stops answering.

That is worse here than in ferry, for the reason README gap #6 already gives:
solzen has **no recovery file**. The per-swap Zenon key exists in `localStorage`
and in nothing else, and the only way to get it out is `store.export` — a call
through the module that just died. The user's remaining option is devtools.

ferry's `panicError` is worth copying whole, including its note about why the
message is rendered with `fmt` and not `js.ValueOf` (their **L6**): `js.ValueOf`
panics on a Go value it cannot convert, inside the handler that exists to catch
panics, which is the one place a second panic cannot be caught.

### H3 — No Content-Security-Policy

`ui/index.html`

ferry's document carries a `<meta http-equiv="Content-Security-Policy">` and
`docs/SECURITY.md` names `script-src 'self' 'wasm-unsafe-eval'` as *"the directive
doing the real work"*. solzen's `index.html` has `charset`, `viewport` and
`title`.

What is behind that missing header is the same class of material: one
`localStorage` record per swap holding the per-swap Zenon key and, for the
initiator, the secret. `wasm/store.go`'s own comment states the exposure — *"any
script on the origin can read them — which on a shared static host means any page
the same account publishes"* — and then nothing narrows what may be a script on
this origin.

Adding the policy is four lines in `<head>` and changes no component. ferry's
policy transfers directly; only `connect-src` needs the same argument made (ferry
keeps it open on purpose, so the user's choice of node stays theirs, and solzen's
Nodes panel is the same trade).

Worth taking ferry's `frame-ancestors` caveat with it: it is ignored in meta form,
so a host that can set headers should add `X-Frame-Options: DENY`.

### H4 — The program is deployed upgradeable, and nothing checks it

`program/src/lib.rs`, `README.md` §"Status and known gaps" #8, `wasm/api.go`
(`checkNodes`)

README gap #8 is the right gap, written one notch too weakly. It says the page
*"checks the program id is deployed and executable; it cannot check what it
contains"*. Two things:

**The check it claims does not exist.** `checkNodes` reads the account and reports
success from its mere presence:

```go
} else if acc, err := a.m.sol().Account(ctx, programID); err != nil {
    res["program"] = ...error...
} else {
    // An account that is not executable is not a program. Saying so
    // here is cheaper than a transaction that fails with a runtime
    // error naming an address nobody recognises.
    res["program"] = map[string]any{"ok": true, ...}
}
```

The comment describes an executability check. `sol.accountValue` has three fields
— `Data`, `Lamports`, `Owner` — and no `Executable`, and neither `executable` nor
the owning loader is examined. This is ferry's **Z3** shape exactly: a check
reported as having passed when it did not run, here reported in both the code
comment and the README.

**The half that *is* checkable is the one that matters most and is not
mentioned.** The README deploy line is a plain `solana program deploy`, with no
`--final` and no `set-upgrade-authority --final`. That leaves the program
upgradeable with the deployer's key as its authority — which means the terms of
every live escrow can be rewritten *after* it is funded, and every escrow drained,
by whoever holds that key. On Bitcoin this cannot happen: a P2SH script is
immutable once it is an address, which is why ferry has no counterpart primitive
and why `ParseContract` is enough there. On Solana it is the first question anyone
should ask of an escrow program.

It is a two-call check and belongs beside the existing program probe:
`getAccountInfo(programId)` gives `owner == BPFLoaderUpgradeab1e…` and a
programdata address in its data; `getAccountInfo(programdata)` carries an option
byte that is `0` when the program is immutable, and the authority pubkey when it
is not. Reporting *"upgradeable, authority `<pubkey>`"* is honest and cheap;
refusing to fund against an upgradeable program on a real network is a policy
decision that at least becomes possible.

The reproducible-build half of gap #8 stands as written and is not addressed here.

---

## Medium

### M1 — `Store.Import` validates nothing

`wasm/store.go`

ferry's **M4** added `Swap.validate` — the id, the network, the three halves of
the key *against each other*, the secret against its own hash, and the contract
against the template — and made a bad record something that is counted and
reported rather than something that aborts the run.

solzen's `Import` checks `sw != nil && sw.ID != ""` and stores whatever else was
in the file. Nothing checks that `Secret` hashes to `Terms.Hashlock`, that
`ZnnSwapSeed` is a usable seed, that `SwapID` is 32 bytes of hex, that the
addresses parse, or that `Terms` would survive `validateTerms`. A record with an
unusable `ZnnSwapSeed` makes `Refresh` fail outright (`readSwapAddress` returns
the error), which is a confusing way to learn that a file was bad. A record with a
`Terms.SolProgram` of the importer's choosing points `readSolanaLeg` at someone
else's program. And `ZnnHomeAddress` is the address a reclaim sweeps to.

Import is a deliberate act on a file the user chose, so this is not a remote
attack — it is the same "a file the user chose deserves a better answer than a bad
record silently becoming a swap" argument ferry made. `validateTerms` already
exists and does most of the work; the remaining checks are the
secret-against-its-hash one and the key one.

Worth noting that solzen's Import is *better* than ferry's in one respect and
should keep it: merging by id and never overwriting a newer record is the right
rule, and ISSUES §12 is right that it is what makes the export file useful at all.

### M2 — Nothing pins which chains a swap belongs to

`wasm/manager.go` (`Config`), `wasm/swap.go` (`Terms`), `wasm/status.go`

ferry has `wasm/chainid.go`, a whole primitive: both sides exchange a fingerprint
— network name, tip height, and the hash of a block at an agreed depth — and
compare, because *"a swap where the two sides are on different chains cannot
complete, and it fails in the most expensive possible way"*. Ferry also pins
`network` on each swap record and, after their **M7**, **disables every
chain-touching action** when the swap's network and the app's disagree, on the
grounds that *"a warning beside a live button is one people click past"*.

solzen has neither. `Config` is one global `SolanaURL` and one `ZenonURL`; `Terms`
records no cluster, no genesis hash and no Zenon `chainIdentifier`. The frontier
momentum's `chainIdentifier` is read and displayed in the Nodes panel and is never
compared to anything.

Two consequences, of different weights:

**Between two parties** it is mostly covered by something else, and this is worth
saying because it makes the finding smaller than ferry's. The rule that the
participant funds only against a *verified* counterparty leg means two people on
different chains cannot both fund: the participant simply never sees a leg. The
initiator funds into the void and gets it back at expiry. The cost is fees, a
timelock's wait, and a failure that reads as a silent counterparty rather than as
a misconfiguration.

**Within one browser** there is nothing at all. Change `SolanaURL` from a devnet
to a mainnet endpoint — the same field, the same panel, no per-swap memory — and
every existing swap is silently re-read against the new cluster. `readSolanaLeg`
finds no escrow, `FindEscrowOutcome` finds no outcome, the leg reads *not funded*,
and `planActions` offers **"Lock 0.35 SOL in the escrow"** as the primary action.
`SolanaInstruction` will build it: the only clock check is `SolTimelock <= now`,
and a timelock computed against one cluster's real clock is still in the future on
the other's. What saves it today is that the program id usually does not exist on
the other cluster — which stops being true the moment a real deployment uses one
keypair for both.

Recording the Solana genesis hash (`getGenesisHash`, one call) and the Zenon
`chainIdentifier` on the swap at creation, and refusing chain-touching actions
when they disagree with the configured nodes, is ferry's M7 applied here.

### M3 — The leg-ordering re-check is a warning, and the funding button stays primary

`wasm/status.go` (`Refresh`), `wasm/actions.go` (`planActions`)

`Terms.CheckOrdering` is written for exactly this — its comment says it is checked
*"on every refresh, not once at acceptance: the deadlines are absolute, so a swap
that was safe to accept becomes unsafe simply by sitting there, and the moment it
does is the moment the UI has to stop telling someone to fund it"*.

It then does this:

```go
if err := s.Terms.CheckOrdering(minInt64(solNow, znnNow)); err != nil {
    st.Warnings = append(st.Warnings, err.Error())
}
```

`planActions` computes `blocked` from two conditions, neither of which is the
ordering result, so `sol.create` and `znn.create` stay `Ready: true, Primary:
true` under a failing ordering check. The comment describes a stop; the code
produces a line of text above a live button.

The exposure is genuinely smaller than ferry's, because the *gap* between the two
legs is fixed at creation and re-verified against both on-chain legs, so only
`MinRemainingSeconds` can newly fail — and the participant funding a nearly
expired leg mostly loses fees rather than principal. But it is the one place the
stated rule and the implemented rule differ, and `blocked` is one condition away
from agreeing with the comment.

### M4 — Nothing checks that the wallet returned the transaction it was given

`ui/src/core/solana.js` (`send`)

ferry treats the wallet as a separate program with its own settings and its own
mind: `wasm/walletsync.go` refuses to build a block unless a momentum hash read
from *both* nodes at an agreed height matches, and after publishing, *"the block
the wallet actually signed is compared field by field against the one this page
proposed"* (`docs/SECURITY.md`, `docs/EXTENSION-WALLET.md`,
`wasm/walletsent_test.go`).

solzen builds the transaction, hands it to `p.signTransaction(tx)`, and submits
`signed.serialize()` without comparing the two. A wallet that returns a different
transaction — different instruction, different amount, different recipient — is
submitted and its signature recorded as this swap's step. The file's own header
makes the promise that this closes: *"a bug in this file can lose a fee or fail to
send, and cannot change what the instruction says."* That holds for what the page
*builds*; it does not hold for what comes back.

One comparison of `signed.serializeMessage()` against `tx.serializeMessage()`
before `sendRawTransaction` makes the header comment true. The
`signAndSendTransaction` fallback cannot be checked at all, which is a second
argument for the red banner solzen already shows when that path is in use
(ISSUES §1).

Related and in the same file: nothing checks that `wallet.address` equals
`Terms.SolSender` before building `sol.create`. Switching accounts in Phantom
mid-swap produces a transaction with a signer the wallet cannot provide — it
fails, so no money moves, but the failure is opaque where a named refusal is free.
ferry's `EXTENSION-WALLET.md` covers exactly this case for Syrius.

### M5 — Prefunding the escrow PDA permanently blocks a swap id

`program/src/lib.rs` (`create`)

```rust
if !escrow.data_is_empty() || escrow.lamports() > 0 {
    return Err(ProgramError::AccountAlreadyInitialized);
}
```

The refusal is correct — `system_instruction::create_account` fails on an account
with lamports anyway, and the explicit check produces the better error. The
consequence is that **anyone who knows the swap id can permanently prevent that
escrow from being funded** by paying it. The swap id travels in the offer string,
so the counterparty always knows it, and the escrow address is a pure function of
it.

*Corrected while fixing this:* the price is **not one lamport**, as first
written. The runtime refuses to leave any account below rent exemption, so the
least a stranger can park at the address is the rent-exempt minimum for a
zero-length account — 890,880 lamports, about 0.00089 SOL. The test found that;
the original figure was an assumption nobody had run. Cheap enough that the
finding stands, and worth the correction because "one lamport" made it sound
free.

No money is at risk and the message the funder sees (*"swap id is already in
use"*) is accurate. The cost is that a griefer can kill any swap for that dust
plus a fee, and the recovery — make a new swap and re-exchange the strings — is
not obvious from the error.

The standard remedy is to not use `create_account` on a possibly-prefunded PDA:
`transfer` the shortfall, then `allocate`, then `assign`, all signed by the PDA.
It is a real design decision rather than a bug fix, which is why it is Medium and
not Low. ferry has no counterpart — a P2SH address cannot be blocked, only paid.

### M6 — The recorded signatures claim to prevent a double-fund and do not

`wasm/swap.go`, `wasm/act.go` (`RecordSolanaTx`)

```go
// Notes of what has already been done, so a refresh does not re-propose a
// step that is in flight.
SolCreateSig string `json:"solCreateSig,omitempty"`
```

`RecordSolanaTx` writes them. Nothing reads them — confirmed by grep across
`wasm/` and `ui/src/`. Between broadcasting `sol.create` and the escrow appearing
at `confirmed`, `readSolanaLeg` gets `ErrNoAccount`, `FindEscrowOutcome` finds
nothing, and `planActions` re-offers *"Lock 0.35 SOL in the escrow"* as the
primary action. A second click builds a second create, which the program refuses
with `AccountAlreadyInitialized` — so the money is safe and the cost is a fee and
an alarming error, but the field's stated purpose is unserved.

The same fields would also let the page say *"waiting for your funding transaction
to confirm"*, which is what the state actually is.

---

## Low

| | | |
|---|---|---|
| **L1** | `wasm/znn/client.go` | `Htlc` does not check that the entry the node returned is the one that was asked for. This is ferry's **L2** (`VerifyParams.ExpectID`), which they judged worth a field and a test. `HtlcInfo.Id` is decoded and never compared. |
| **L2** | `wasm/znn/abi.go`, `wasm/status.go` | `HtlcInfo.HashLock` is `[]byte`, so `encoding/json` decodes it as base64 — right for go-zenon. A tool that returns the hashlock as hex hands over a 64-character string that is *also* valid base64 and decodes to 48 meaningless bytes, which then fails the hashlock comparison. It fails closed, so this is a legitimate HTLC refused rather than a bad one accepted — ferry's **L1**, whose fix is to decode both and let the digest-sized result win. |
| **L3** | `program/src/lib.rs` (`redeem`) | A zero-length preimage is accepted: `len` is only checked against `MAX_PREIMAGE`. `sol.BuildRedeem` refuses it and `DecodeRedeemPreimage` refuses `n == 0`, so the program and its only client disagree about what a redeem *is* — and a redeem the scanner cannot recognise is C1's failure mode arriving by accident. Unreachable in practice (no 32-byte secret hashes to SHA-256 of the empty string), but the file's own claim is that the two implementations are kept in step and tested for drift. |
| **L4** | `wasm/manager.go` (`bigFromString`), `wasm/status.go` | `bigFromString` returns zero for anything that will not parse. In `readZenonLeg` that fails closed (any real amount then mismatches). In `readSwapAddress` it fails **open**: `sa.Sufficient = held.Cmp(bigFromString(ZnnAmount)) >= 0` is true for every balance, and `planZenonFunding` gates the *"Create the Zenon HTLC"* offer on `Sufficient`. Only reachable through an unvalidated record (M1), but returning an error rather than zero is the cheaper fix. |
| **L5** | `wasm/sol/client.go` | `MinimumRent` is defined, documented as existing *"so that 'the swap costs 1 SOL' is not quietly untrue"*, and never called anywhere in `wasm/` or `ui/`. The rent is shown after funding, in `readSolanaLeg`'s note, and never before. ferry's `wasm/estimate.go` quotes the cost up front. |
| **L6** | `wasm/swap.go` (`validateTerms`) | The counterparty's addresses in a pasted envelope — `SolSender`, `SolReceiver`, `ZnnSender` — are checked for emptiness by `Complete()` and never parsed. Verification compares them as strings, so an unparseable address fails closed; `checkPayoutReachable` parses `ZnnReceiver` and is the only one that does. Parsing all four at decode time is ferry's **L4** ("every field is checked") and produces the useful error at the moment of paste rather than at the moment of funding. |
| **L7** | `program/`, tests | There are **no Rust tests** — no `#[test]`, no `solana-program-test`, no litesvm harness. `ui/scripts/program-test.mjs` is good and covers the happy paths, a wrong preimage, a wrong receiver, a past timelock, an early refund, a late redeem and a double redeem. What has no coverage anywhere is the account-substitution family, which is where escrow programs actually lose money: `refund` with an initiator that is not the escrow's, an escrow account owned by another program (`load`'s `IllegalOwner`), a forged account at a non-PDA address (`InvalidSeeds`), one real escrow substituted for another (the bump re-derivation in `load` — the check the program's own comment calls *"the mistake that costs money rather than the one that merely errors"*), a `create` on a live escrow, `amount == 0`, and the `receiver == initiator` aliasing path in `close_to`. All are unit-testable in seconds without a validator. |

---

## Present in solzen, and correct — checked rather than assumed

Recording these because a comparison that only lists gaps overstates them, and
because several are places where solzen is *ahead* of ferry.

- **The program's own invariants.** Both branches pay a party fixed at creation
  and no instruction takes a destination; neither branch needs a signature from
  the party being paid; `load` re-derives the PDA from the id inside the account's
  own data rather than trusting the caller, which is the check that rules out
  substituting one real escrow for another; `close_to` applies the two credits one
  at a time rather than against a stale read, which is what makes the refund path
  (where `paid` and `rest` alias) correct; the data is zeroed before the lamports
  are drained; `overflow-checks = true` in the release profile; the redeem and
  refund clock comparisons are complements, so exactly one branch is open at any
  instant. `MAX_PREIMAGE` is 32 to match Zenon's `keyMaxSize`, and SHA-256 is the
  one hash both chains can commit to. This is a careful contract.
- **Proxy-unlock.** `checkPayoutReachable` refuses a swap whose ZNN payout address
  has called `DenyProxyUnlock`, before the offer is accepted. ferry does **not**
  make this check and should; solzen's README already says so.
- **Null RPC results.** `znn/client.go` refuses a `null` result rather than
  decoding it into a zero struct — ferry's Z2 first half, in place.
- **The agreed terms are never overwritten by observed ones.** Nothing in
  `readZenonLeg` or `readSolanaLeg` writes back to `Terms`; the only chain-derived
  values that reach the record are `HtlcID` (a discovery) and `Secret` (through
  `adoptPreimage`, which hash-checks it). That is ferry's **Z1**/**M2** by
  construction.
- **The token standard is verified.** `info.TokenStandard != t.ZnnToken` is a
  refusal — ferry's Z1. C2 is a level below this, not a repeat of it.
- **`ApplyAccept` compares the whole terms struct**, allowing only the two fields
  the taker may fill. Stricter than ferry's `CheckCreate`, which compares a named
  list.
- **`DecodeEnvelope` uses `DisallowUnknownFields`**, and so does the whole API
  bridge. ferry does not.
- **`httpx/httpx_js.go` is byte-identical to ferry's**, so ferry's **M5** (the
  `await` that could hang forever without `AbortController`) is fixed here too.
- **`storage_js.go` guards every access with `recover()` and tests `js.TypeString`
  rather than `Truthy()`** — ferry's **L5**, plus the private-window case.
- **Clock discipline.** Every deadline is computed and compared against the chain's
  own clock — Solana's `getBlockTime`, Zenon's frontier momentum timestamp — never
  `time.Now`. `MaxClockSkewSeconds` refuses to do the ordering arithmetic when the
  two disagree by more than 15 minutes. This is better than ferry's, which falls
  back to local time with a log line.
- **The plasma probe's third state** (ISSUES §10) is ferry's Z3 reasoning applied
  independently and correctly: a probe that did not answer leaves plasma
  *unknown*, and says so, rather than defaulting to a pair of zeros that reads as
  a confident promise.
- **`znn.reclaim` is one action for three blocks** (ISSUES §7). ferry has no
  counterpart because Bitcoin has no receive semantics; this is a genuinely better
  answer than offering three buttons.
- **`Import` merges by id and keeps the newer record.** Correct, and worth keeping
  when M1's validation is added.

## Not applicable by construction

- **ferry's P1** (`OP_CHECKLOCKTIMEVERIFY` operand encoding) has no analogue: the
  Solana timelock is an `i64` in instruction data with no script-engine encoding
  rules to get wrong.
- **ferry's M6** (txids interpolated into request paths) has no analogue: both
  clients are JSON-RPC over POST bodies, with no value from a response ever
  reaching a URL.
- **ferry's L3** (fee-rate overflow) has no analogue: Solana fees are not the
  page's to compute.
- **ferry's `walletsync.go` two-node momentum agreement** has no analogue on the
  Zenon side, because solzen signs its own Zenon blocks with the per-swap key —
  there is no second node whose view could differ. The *Solana* wallet is a second
  program with its own RPC, and that gap is M4 above, with ISSUES §1 already
  covering the cluster half of it.

## The two acknowledged gaps this pass does not move

Both are in README §"Status and known gaps" and both are design changes rather
than defects, so they are noted and left:

- **Gap #1, records unencrypted at rest.** Identical in ferry, listed first in
  both. H3 (CSP) is the cheap mitigation that ferry has and solzen does not, and
  is the reason it is High rather than Medium.
- **Gap #6, no recovery file.** ferry pre-signs a refund the moment funding is
  seen and puts it in a downloadable file that the offline `Rebuild` page can
  replay with no store, no node and no network. solzen's export is a real
  substitute for *key loss* and not for *page loss*, and the README says so. It is
  also what makes H2 (no panic recovery) matter more here than it does in ferry.

## Suggested order

By ratio of money protected to work required:

1. **C1** — three small changes in two scanners, and the one finding here with a
   complete path to losing a leg to a counterparty who is already in the swap.
2. **C2** — one node call in `Accept`, plus ferry's two `GetToken` guards.
3. **H2** and **H3** — a `recover()` and four lines of `<head>`, both copyable
   from ferry.
4. **H1** — the mirror of a fix already written for the other chain.
5. **H4** — two RPC calls, and a `--final` in the deploy instructions.
6. **L7** — the account-substitution tests, which is where a reader will look
   first to decide whether to trust the program.
7. The rest.

## How to verify

```sh
cd wasm && go vet ./... && GOOS=js GOARCH=wasm go vet ./... && go test ./...
cd ui   && npm run build && npm run smoke
```

Both were green as this was written. The contract-level findings (C1's Solana
half, M5, L3, L7) want cases added to `ui/scripts/program-test.mjs` or a Rust
harness; C1's decoy case is best written the way ferry wrote theirs, with a decoy
carrying a *valid* preimage for a different swap, so that only the new check can
catch it.

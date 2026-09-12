# solzen — Solana ⇄ Zenon atomic swaps

Trustless cross-chain swaps between Solana and Zenon (NoM), driven from a static
page. Two hashed timelock contracts commit to one secret; either both settle or
both refund.

```
SOL ──── escrow on Solana ────┐
                              ├──── one secret, both legs ────► atomic
ZNN ──── HTLC on Zenon ───────┘
```

A proof of concept, and no longer an untried one: complete swaps have settled in
both directions against a local Solana validator and the `go-zenon` devnet,
including one driven by clicking the actual page in two browser profiles
(`docs/ui-maker.png`). `HANDOFF.md` says what is proven, by what, and what is
left.

---

## Why this exists

[`ferry-web`](../ferry-web) does the same thing between Bitcoin and Zenon, and
its release is blocked on two problems that are not in its code:

1. **The Zenon leg needs a Dart CLI cloned and run from source.** Syrius cannot
   create or unlock a cross-chain HTLC — it hardcodes SHA3-256, caps expiry at
   24h, and has no screen for unlocking an arbitrary HTLC by id.
2. **There may be no public Zenon node a browser can verify against.**

This project is a swap against a different chain, and on the way it answers the
first one. **The Zenon leg here needs nothing but an ordinary send from
Syrius.** The mechanism is in [How the Zenon leg works](#how-the-zenon-leg-works)
and it is not specific to Solana — it applies to ferry's Bitcoin↔Zenon swaps
unchanged.

---

## Why Solana needs a program and Bitcoin does not

ferry's central observation is that **funding an HTLC is just a payment to an
ordinary address**: a Bitcoin contract is a P2SH address, and paying it is
indistinguishable, to the paying wallet, from paying anyone else.

Solana has no script. There is no address whose spending conditions are a
hashlock and a timelock, so the contract has to be a deployed program — which is
what every Solana atomic-swap system does. [`program/src/lib.rs`](program/src/lib.rs)
is that program: about 300 lines, no framework, two spending branches.

What survives the difference is the property that matters:

- **Both branches pay a pre-committed address.** `redeem` pays the receiver named
  at creation, `refund` pays the initiator. No instruction takes a destination,
  so no caller — this page included — can redirect the money.
- **Neither branch needs a signature from the party being paid.** The preimage is
  the authorization for one and an expired clock is the authorization for the
  other. A swap can therefore be finished by whichever side is watching, and a
  counterparty who has closed their laptop cannot strand it.

Funding still needs a wallet that signs an arbitrary instruction, which on Solana
is every wallet.

## How the Zenon leg works

Zenon's `htlc` embedded contract already does exactly what a cross-chain swap
needs: `hashType 1` is SHA-256, and expiry is an absolute timestamp with no cap.
The problem was never the contract. It was that **no Zenon wallet exposes
`htlc.Create` or `htlc.Unlock`**, so ferry's answer was "run this CLI".

Two facts about the contract make a better answer possible:

- `Unlock` may be called **by anyone**, and pays the address recorded in the
  entry (`htlcProxyUnlockInfo` defaults to allowing it — verified live on the
  devnet).
- `Reclaim` may be called **only by the address that created the entry**.

So:

| | Who does it | Wallet support needed |
|---|---|---|
| Pay ZNN into the swap | Your own wallet, to a per-swap address | **None** — an ordinary send |
| Create the HTLC | This page, with a key generated for that one swap | none |
| Unlock it (receiving side) | This page, with a swap key holding no funds at all | none |
| Receive the payout | Your own wallet | **None** — an ordinary receive |

The **receiving** side never touches a swap address at all: they give their real
Syrius address, the contract pays it directly, and the page pushes the unlock
from a throwaway account that has never held anything.

The **sending** side pays a per-swap address with an ordinary transfer, and the
page holds a key for it — the same trade ferry already makes on Bitcoin, where an
ephemeral key spends out of a contract the user funded from their own wallet.

Taking that leg back is three blocks, not one, and the page does all three under
one button. `Reclaim` pays the entry's *creator*, which is the swap address, not
the user; on Zenon that money is not spendable until the swap address publishes
a receive block for it; and only then can it be sent on to the address they
nominated. Offering those separately would mean two chances to stop after the
step that makes the leg read "refunded" and before either of the steps that pay
anybody — with the key that could finish the job living in one browser.

That key is the residual risk, and it is worth stating plainly: between the
funding send and the swap settling, it can spend that one balance. It is not
their wallet key, and losing it costs at most what they put into one swap.

---

## Non-custodial, precisely

- The page never sees, asks for, or stores a wallet key. The Solana side is
  signed by the user's own wallet; the Zenon side by a key generated per swap.
- Both Solana branches and both Zenon branches pay addresses fixed when the
  contract was made. There is no code path that pays anywhere else, and
  [`wasm/sol/htlc_test.go`](wasm/sol/htlc_test.go) asserts it.
- Money never passes through an address the counterparty or an operator controls.
- Verification is not a formality. A leg is refused if it commits to the wrong
  hash, pays the wrong party, holds the wrong token, holds the wrong amount, uses
  SHA3 instead of SHA-256, has too small a `keyMaxSize`, or sits on the wrong side
  of the other leg's deadline. A funded leg that fails any of these is reported
  as **funded, but wrong**, and the page refuses to let you fund yours against it.
- There is no server. The two sides exchange two strings by hand, and both carry
  public terms only — never the secret, never a key.

---

## Quick start

Needs Docker, Go 1.24+, Node 20+.

```sh
# 1. The Solana program: build it, start a local validator, deploy it
docker build -t solzen-build program
docker run --rm -v "$PWD/program:/work" -v solzen-cache:/root -w /work solzen-build \
  --sbf-out-dir /work/target/deploy
docker compose up -d
docker exec solzen-validator sh -c '
  solana config set --url http://172.31.0.10:8899 &&
  solana-keygen new --no-bip39-passphrase --silent -o /root/.config/solana/id.json &&
  solana airdrop 100 &&
  solana program deploy /programs/solzen_htlc.so --program-id /programs/solzen_htlc-keypair.json'
# Deliberately not --final here: a local chain is redeployed constantly. On any
# chain with money on it, deploy with --final -- see gap #8. The Nodes panel
# reports which of the two you are pointed at.

# 2. The Zenon devnet, from the go-zenon checkout
cd ../go-zenon && make devnet-up && cd -

# 3. The page
cd ui && npm install && npm run dev      # http://127.0.0.1:5188
```

`npm run dev` compiles the Go engine to WebAssembly with the deployed program's
address baked in as the default, then serves the site.

### The Solana wallet

The header offers **Connect Phantom** when Phantom is installed, the same button
named for whatever else is there (Solflare, Backpack — anything that injects a
provider), and an install link when nothing is. Connecting once is remembered:
a reload reconnects silently, and switching accounts or locking the extension is
followed rather than ignored, so the page never signs for an address the wallet
has moved on from.

The page asks the wallet to **sign**, and submits the signed transaction itself,
through the node named in **Nodes**. That distinction matters more than it looks:
a wallet's own sign-and-send submits through whichever cluster *it* is pointed at,
which against a local validator is a different chain from the one holding the
escrow. A wallet that can only sign-and-send is still usable and the header says
so in red while it is connected.

Phantom cannot reach a local validator to simulate what it is signing, so against
`127.0.0.1:8899` it warns that it could not simulate the transaction and shows no
preview of the amounts. That is Phantom describing its own reach, not a problem
with the transaction; signing proceeds and the page submits the result.

**Use a key in this browser** is the other option, and the one the tests drive: a
keypair in `localStorage`, for a chain that gets wiped every morning. It is not
how a swap is meant to be done and the page says so.

### Two participants means two browser profiles

Storage is per origin, so two tabs are one participant holding the swap twice. A
second Chrome profile, a private window, or a second `--user-data-dir` is what
makes the counterparty a counterparty. Everything that passes between them — the
offer, the acceptance — is copied from one window and pasted into the other,
which is exactly what two people would be doing.

---

## Doing a swap

**The side that makes the offer is the initiator**, and invents the secret. Its
leg gets the longer timelock. That ordering is the whole scheme: the initiator
can always claim the other side's leg, so their own must stay locked long enough
afterwards for the counterparty to use the secret they just revealed. The page
refuses any pair of deadlines less than four hours apart, and re-checks on every
refresh — because a swap that was safe to accept stops being safe simply by
sitting there.

Either direction can be made:

| | Solana initiates | Zenon initiates |
|---|---|---|
| Long leg (48h) | the Solana escrow | the Zenon HTLC |
| Secret surfaces on | Zenon, when the initiator unlocks | Solana, when the initiator redeems |
| Both sides pick it up | automatically, from the chain that published it | automatically |

### Sending SOL, receiving ZNN

1. Connect a Solana wallet, give your Zenon address, make the offer, send the
   `solzenoffer1:` string.
2. Paste their `solzenaccept1:` reply.
3. Fund the escrow — your wallet signs one instruction.
4. Refresh until their Zenon HTLC appears and is **verified**.
5. Claim the ZNN. The contract pays your Syrius address; the secret becomes
   public on Zenon, which is how they claim the SOL.

### Sending ZNN, receiving SOL

1. Decode their offer, give your Zenon address (where a refund comes back to),
   accept, send the reply.
2. Refresh until their escrow appears and is **verified**.
3. **Pay this swap's Zenon address from Syrius** — an ordinary send. Fusing a
   little QSR to it at the same time is worth it; see below.
4. Receive it, then create the HTLC. Both are one button each.
5. Refresh: when they claim your HTLC the secret appears on Zenon, and you claim
   the SOL.

If nobody claims, the expiry passes and one button takes the ZNN back: it
reclaims the HTLC, receives what the contract returns, and forwards it to the
address you nominated — three Zenon blocks, reported one by one as they land.

### Plasma, and why the page asks about it

Every Zenon block needs plasma, and an account with none pays in proof of work
instead. Measured on the devnet, at roughly 1.0M hashes/sec in Go on a desktop —
call it half that in a browser:

| Block | Difficulty | Work |
|---|---|---|
| receive, or a plain send | 31,500,000 | ~30 s |
| `htlc.Create` | 78,750,000 | ~80 s |
| `htlc.Unlock`, `htlc.Reclaim` | 110,250,000 | ~110 s |

Taking a Zenon leg back is a reclaim, a receive and a send, so an unfused one is
173,250,000 — about six minutes in a tab. The page quotes the whole of it before
it starts, and the progress bar counts all three.

**Both sides pay this, not just the one sending ZNN.** The receiving side's swap
key never holds a coin, but publishing the unlock is still a Zenon block. The
page shows each side its own address and its own estimate, so either can fuse
~60 QSR to it from Syrius and skip the wait entirely; the QSR comes back by
cancelling the fusion.

The mine hands the browser's main thread back every 65,000 hashes, **through a
MessageChannel rather than a timer**. Both halves of that were found by running
it, not by reasoning about it:

- Go compiled to WebAssembly runs on the page's one main thread, so a loop that
  never blocks freezes the tab — no repaint, no clicks, no progress bar. The
  first browser run hit exactly that, with Chrome's own debugging protocol
  timing out behind it.
- Yielding with `time.Sleep` fixed the freeze and made the mine roughly three
  times slower, because a sleep schedules a timer and Chrome clamps timers in a
  page it considers hidden to about one a second. A miner that yields a thousand
  times then spends a thousand seconds doing nothing.

A message posted to a port is a macrotask: the browser gets its chance to paint,
and it is not throttled. `wasm/znn/yield_js.go` has the mechanism; off the
browser the same call compiles to nothing.

---

## Layout

```
program/          the Solana HTLC program
  src/lib.rs      two branches, both paying pre-committed addresses
  Dockerfile      Rust + Agave's cargo-build-sbf, pinned
wasm/             the swap engine: Go, compiled to WebAssembly and to a CLI
  swap.go         terms, roles, the offer format, the leg-ordering rules
  limits.go       the two creation-time minimums; _fastclock.go is the test build
  status.go       reading both chains and checking them against the terms
  actions.go      what this side should do next, derived from that
  act.go          building Solana instructions; performing Zenon operations
  manager.go      create, accept, apply an acceptance
  api.go          the call table the page talks to
  store.go        one record per swap, plus export/import
  main_js.go      the JavaScript bridge
  main_native.go  the same call table, from a terminal
  znn/            Zenon: client, account blocks, signing, proof of work, scans
  sol/            Solana: client, PDAs, instruction building, escrow decoding
  cmd/devnet-wallet/  a Syrius stand-in for tests; never part of the app
  cmd/powpaths/   what the node charges a Zenon block, fused and unfused
  cmd/unlockscan/ every htlc call the chain has recorded, and what it paid
ui/               Vue 3 + Vite + Tailwind 4
  scripts/        the build, and the test runners
docker-compose.yml  the local Solana validator
```

The Zenon half deliberately reuses go-zenon's own code — `vm/abi` for encoding,
`common/types` for addresses and hashes — imported as a module rather than
reimplemented. Only the two pieces that cannot compile for `js/wasm` are written
out here (the HTLC ABI string, whose package reaches goleveldb, and the
proof-of-work loop, whose package reaches the same), and each says so where it is.

---

## Testing

```sh
cd wasm && go test ./...            # the rules, the encoders, the PoW arithmetic
cd ui
npm run program:test                # the Solana program, against the validator
npm run smoke                       # the shipped .wasm, driven under Node
npm run swap:test                   # a whole swap, both chains, via the engine
npm run swap:test znn2sol           # the other direction
npm run timeout:test                # a swap nobody claims, taken back on both chains
npm run timeout:test znn2sol        # the other direction
npm run timeout:test abandon        # a swap address funded, then the swap dropped
npm run timeout:test mismatch       # a leg funded that is not the one agreed
npm run plasma:test                 # what a Zenon block costs, fused and unfused
npm run ui:test                     # a whole swap, by clicking, in two browsers
npm run ui:test -- --phantom        # the same, through the page's wallet connector
```

`go test` needs no chain and `program:test` needs only the validator; everything
below them needs both up. What each one is for:

- **`program:test`** reads `program/src/lib.rs` a second time, in JavaScript, and
  checks the contract's promises rather than its happy path: a wrong preimage
  cannot spend, a redeem naming the wrong receiver cannot spend, a refund before
  the timelock cannot spend, a redeem after it cannot either, the receiver is paid
  exactly the agreed amount, the rent goes back to the initiator, and the escrow
  is gone afterwards.
- **`smoke`** loads the actual `solzen.wasm` the browser will fetch and exercises
  the call table over real `fetch()`, including the refusals — an unknown method,
  a stray field, a damaged offer, an import in the wrong format.
- **`swap:test`** settles a real swap end to end through two stores, and asserts
  the thing the whole scheme rests on: that the participant learns the secret
  *from the chain the initiator claimed on*, not from the counterparty.
- **`timeout:test`** is the same swap with nobody claiming: both legs funded, both
  timelocks left to pass, and each side taking its own leg back. It asserts what
  the money does — that a refund before the timelock is refused by the engine on
  both chains rather than by the contract afterwards, that the expired leg can no
  longer be claimed, that each side is offered only its own refund, that taking a
  Zenon leg back publishes all three of its blocks and leaves the swap address
  empty, and that the secret was never published because nobody ever revealed it.
  It builds the engine with `-tags fastclock`, which swaps `limits.go` for
  `limits_fastclock.go` and lowers the two creation-time minimums, because a real
  pair of deadlines is four hours apart at the closest; every other file is the one
  that ships, and the run asserts through `env` that it got the build it asked for.
  `SOLZEN_SHORT_SECONDS` and `SOLZEN_LONG_SECONDS` shorten the deadlines further.
  Its `abandon` mode needs no clock and so runs against the engine as it ships: the ZNN
  is sent to a swap address and the swap is then dropped before the HTLC, which
  is the one state where money is sitting somewhere with nothing locked, and it
  has to be reachable anyway. Its `mismatch` mode is the one that matters most:
  the initiator edits their own copy of the terms and funds *that*, and the test
  checks the counterparty is stopped — the leg reads funded but not verified, the
  warning names both amounts, the step that cannot be undone is offered but not
  ready, `znn.act znn.create` is refused by the engine as well as by the page, and
  the money already moved to the swap address sweeps back. Every other test in
  this list avoids that branch by being honest.
- **`plasma:test`** walks the boundary between the two ways a Zenon block is paid
  for, on one throwaway address, in about a minute: the node prices an `htlc.Unlock`
  from a bare address at 110,250,000 difficulty, refuses a block that claims fused
  plasma the account does not have, and then — after 60 QSR is fused — prices the
  same call at zero and publishes it unmined. It is the cheap standing check behind
  `ui:test --pow`, which proves the same thing end to end but spends most of nine
  minutes hashing to do it.
- **`ui:test`** does the same by clicking the page in two Chrome profiles.
  `--phantom` runs it through the page's wallet connector against a provider
  shaped like Phantom's, whose key lives in the test rather than in the page — the
  connect, sign and submit path that the default "key in this browser" mode does
  not touch. `--pow` leaves the receiving side to mine its unlock in the browser.

---

## Status and known gaps

Nothing here has been used with real money, on any mainnet.

1. **Swap records are not encrypted at rest.** They live in `localStorage` and
   contain the per-swap Zenon key and, for the initiator, the secret. Any script
   on the origin can read them — which on a shared static host means any page the
   same account publishes. Passphrase encryption is the obvious next step.
2. **Proof of work is single-threaded.** Several minutes per Zenon block without
   fused plasma, on the page's one thread — it yields often enough to stay
   responsive, but it does not get faster. Web Workers would divide that by the
   core count, and the engine already verifies a nonce independently
   (`CheckPoWNonce`), so workers could mine without being trusted.
3. **The module is 16.5 MB** (about 4 MB gzipped, fetched once and then cached).
   Almost none of that is this project's code, and the cause is worth writing
   down because it is upstream and it is fixable there. Measured for `js/wasm`
   with `-s -w`, against a 2.5 MB empty-program baseline:

   | import | module size |
   |---|---|
   | nothing | 2.5 MB |
   | `common/crypto` | 2.8 MB |
   | `common` | 7.5 MB |
   | `common/types` | 15.4 MB |
   | `common/types` + `vm/abi` | 15.9 MB |

   `vm/abi` — the part actually doing the work — costs 0.4 MB. The jump at
   `common` is one line: `common/errors.go` imports `rpc/server`, so anything
   that touches a `types.Address` links a whole JSON-RPC server. The rest is
   `common/types`' generated protobuf and its reflection registry. Vendoring
   `common/types` and the handful of `common` helpers, the way ferry-web
   vendors the daemon's files, would take this to roughly 4 MB; moving
   `NewErrorWCode` out of `common` would fix it upstream for every consumer.
4. **The HTLC id is found by scanning** the contract's account chain, bounded at
   2,000 blocks. That is ample on a devnet and would need an index on a busy
   chain; the page keeps a manual paste as the fallback.
5. **SOL only.** SPL tokens would need a token-account variant of both branches.
6. **No recovery file.** ferry pre-signs a refund the moment funding is seen. The
   equivalent here is the whole-store export — Export writes it, Import reads it
   back, and a record only ever overwrites an older copy of itself — but it is
   weaker in kind: it depends on the page still existing to replay a refund from,
   and on somebody having pressed Export. Nothing prompts them to.
7. **Timeout paths are covered through the engine, not through the page.**
   `timeout:test` leaves a real swap to expire on both live chains and takes both
   legs back, in both directions — but it drives the engine, and the deadlines it
   uses come from a `-tags fastclock` build with the two creation-time minimums
   lowered, because a real pair is four hours apart at the closest. Nobody has watched a swap expire
   under a mouse, and no swap with real deadlines has ever been left to run out.
8. **Nothing verifies that the program at that address is this source.** The page
   checks the program id is deployed and executable, and it reports whether the
   program is still upgradeable and by whom — but it cannot check what the
   program *contains*. On a real network the address would have to come with a
   reproducible build somebody had checked; the Docker image here pins both
   halves of the toolchain so that such a build is possible, but nobody has done
   it. `program/target/deploy/solzen_htlc-keypair.json` is committed so the
   default works out of the box; it fixes the address, and it is a devnet
   program with no authority over anything.

   **The deployment here is upgradeable**, which the Nodes panel now says out
   loud. That is the right default for a chain that gets reset every morning and
   the wrong one for any chain with money on it: an upgradeable program's code —
   and so the terms of every escrow already funded under it — can be replaced by
   whoever holds its upgrade authority. A real deployment ends with
   `--final`, or a later `solana program set-upgrade-authority --final`, and
   until it does, reading this source tells you nothing about what will run.
   There is no counterpart to this on ferry's Bitcoin leg: a P2SH address *is*
   its contract and cannot be edited afterwards.

### What porting this into ferry-web would take

The engine is deliberately shaped like ferry-web's: Go compiled to WebAssembly, a
JSON call table, one `localStorage` record per swap, a Vue 3 + Vite + Tailwind UI.
Three pieces are worth taking whole and are independent of Solana:

- **`wasm/znn`** — Zenon signing in the browser. This is the piece that closes
  ferry-web's `zenonleg` gap. It builds, signs and publishes account blocks,
  mines plasma when the account has none, and scans the htlc contract for the id
  and the preimage. Nothing in it is Solana-aware.
- **The per-swap Zenon address pattern** in `manager.go` and `actions.go` — the
  reason Syrius is enough.
- **The verification in `status.go`** — the same checks ferry already makes, plus
  the proxy-unlock check, which ferry does not make and should: an address that
  has denied proxy unlock cannot be paid by a swap driven this way, and finding
  that out at settlement is finding it out too late.

What would not port is `wasm/sol`, the program, and the Solana half of the UI.
The leg-ordering rules and the offer format are the same shape as ferry's but not
the same code; they would be merged rather than copied.

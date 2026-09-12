# Architecture

```
browser tab
├─ Vue 3            what is on screen, and the wallet plumbing
└─ ferry.wasm (Go)  every rule that decides whether a swap is safe
       ├─ chain/    Esplora over fetch()
       ├─ znn/      Zenon JSON-RPC over fetch() or a WebSocket
       └─ sol/      Solana JSON-RPC over fetch()
```

No server. A call is a method name and a JSON body across the JS/Go boundary
(`wasm/api.go`) — no token, no CORS policy, no endpoint to protect.

**Everything that could be wrong about a swap is decided in Go.** The page picks
which button to offer; Go re-checks every precondition and refuses by name. The
page is reachable from devtools; the module is not talked out of anything.

## Two legs, no chains

A swap is `Out` (you fund) and `In` (they fund). Each carries a chain and one
chain-specific state (`Leg` in `wasm/swap.go`). v1 named the legs after chains,
which is unrepresentable for ZTS⇄ZTS. Naming them by role collapses the rules:

| question | v1 | v2 |
| --- | --- | --- |
| which leg takes the long deadline | role × direction, per chain | `Role == RoleInitiator` |
| where does the preimage surface | a Bitcoin branch and a Zenon branch | the leg you funded |
| may I fund yet | a Bitcoin-shaped check | `readyToFund()` |

One file per chain holds both sides of the trade: `btcleg.go`, `znnleg.go`,
`solleg.go`, mirrored by `BtcLeg.vue`, `ZnnLeg.vue`, `SolLeg.vue` with
`SwapCard.vue` as a shell that picks two.

## What each chain needs

| | funding it | spending out of it |
| --- | --- | --- |
| **Bitcoin** | ordinary payment to a P2SH address | custom witness, built with a per-swap key Ferry holds |
| **Zenon** | `htlc.Create`, an account block Syrius signs | `htlc.Unlock`, callable by anyone, pays the recorded address |
| **Solana** | a `create` instruction Phantom signs | `redeem`/`refund`, no signer, pay recorded addresses |

Bitcoin needs a key here because no wallet builds that witness. The other two
pay a pre-committed address whoever calls, so the wallet is a fee payer rather
than an authority — which is why either side can finish a stuck swap.

Solana needs a deployed program because it has no script.
`program/src/lib.rs` is ~300 lines, no framework, two branches. **Its address is
a swap term**: it travels in every offer and a mismatch is refused.

## Why Go

Bitcoin signing, the script engine and a strict template parser exist and are
correct in Go. The cost is ~9.6 MB raw / ~3 MB gzipped, fetched once — already
halved by not linking `net/http` (it drags in TLS, x509 and HTTP/2 that the
linker cannot prune); `httpx/` calls `fetch()` directly.

## Storage

One `localStorage` record per swap, namespaced per instance (`ferry.swap.*` /
`ferry.dev.swap.*`) so both builds can share an origin. One record per swap
means a corrupted record loses one swap, and a hand-edit in devtools is a
supported emergency. Every record is re-validated on import.

## Verification

`znn.Verify` and `checkSolEscrow` collect problems rather than returning on the
first, and distinguish two failures: a **mismatch** is an answer; a **check that
could not run** has not answered. Both refuse, but a mismatch is final while an
unreachable node leaves the leg *pending* and re-read on the timer. Conflating
them meant a healthy HTLC refused once during a bad minute and never re-checked.

## Sessions and the board

Both ride public Nostr relays — the only free, redundant, no-signup network a
page with no backend can use. A **session** is a sealed room: the code derives
the key inside the browser and never leaves it, every arriving value goes
through the check it would get if pasted by hand, and there is no field for a
preimage. The **board** is the opposite by design — see [BOARD.md](BOARD.md).

## What the page decides

Only what to show. Pairs, chains per pair, the storage namespace, the board's
kinds and tag, presence timing and post TTLs all come **from the module**, not
from constants beside the code that uses them: a filter built from a stale
constant is a board that subscribes and shows nothing.

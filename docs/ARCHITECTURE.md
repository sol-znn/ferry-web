# Architecture

How this is put together, and which decisions were forced rather than chosen.

The design the swap itself rests on is three facts, and none of them are about
the browser: funding an HTLC is an ordinary payment, spending out of one uses a
throwaway per-swap key, and the initiator's leg must expire last. The README
covers those. This document covers everything that follows from running the
whole thing inside a page.

---

## The shape of it

```
┌────────────────────────┐
│  browser tab           │
│    Vue UI              │
│      ↓ ferryWasm.call  │
│    Go, in WebAssembly  │
│      manager, signing  │
│      localStorage      │
│      ↓ fetch()         │
└────────────────────────┘
             ↓
      Esplora, Zenon
```

Everything that can move a coin is Go. The UI decides what to show and what to
ask; it never builds a transaction, never parses a contract and never holds a
key. That split is why the security-relevant code is one language and one
directory, and why the test suite that covers it is `go test`.

### One entry point, not many

The Go surface is a table of `{name, JSON in, JSON out}` — the shape of an HTTP
route table, without the HTTP. The bridge is one exported function:

```js
window.ferryWasm.call(method, bodyJSON) // → Promise<responseJSON>
```

and `wasm/api.go` holds the table. The TypeScript client in `ui/src/core/api.ts`
has one signature per method above it.

Errors come back as `{"error": "..."}` rather than as a rejected promise, so
there is a single error path rather than two.

### Calls must not block the thread they were called on

Go's WebAssembly runtime shares one thread with the page. An exported function
that blocked while an HTTP request ran would deadlock: the `fetch` cannot resolve
until the event loop is free, and the event loop is not free until the function
returns. So `call` returns a promise immediately and does the work on a
goroutine. Everything below it — `manager.go`, the chain clients — is ordinary
blocking Go, which is why none of it needed writing twice.

### Panics are contained

A panic in Go/WASM tears down the module, and the module is where the only copy
of an unsaved key lives. The bridge recovers from one and turns it into a failed
call, so the page survives long enough for the user to export their swaps.

The same reasoning made `signalReady` defensive. It dispatches a DOM event so
the page need not poll, but `CustomEvent` is a *browser* global and this module
also runs under Node in the smoke test; an unguarded call on a missing property
panics, at the worst possible moment. It is guarded, and the page polls as a
fallback.

---

## Storage

`Store` exposes `Save`, `Load` and `List` over a `Storage` interface with two
implementations: `localStorage` in the browser, a map in tests.

**`localStorage` rather than IndexedDB**, deliberately. Records are a couple of
kilobytes and are written a handful of times per swap, so IndexedDB's
asynchronous API would buy nothing and would cost the property that keeps this
small: `Store`'s methods stay synchronous, so `manager.go` is ordinary
straight-line Go rather than a structure built around promises.

`localStorage` writes are atomic per key, so there is no torn-write problem to
defend against. What the browser adds instead is a quota and an outright refusal
in some configurations — so `Set` converts the thrown exception into an error
rather than swallowing it, and the module probes with a real write at startup and
refuses to run if it fails. A swap whose refund key cannot be saved is worse than
no swap.

Keys are prefixed `ferry.swap.` because the origin is shared with every other
page on the same host. See [SECURITY.md](SECURITY.md).

The board keeps its own records under `ferry.board.` — its signing key, and one
entry per post this browser has published. A separate prefix rather than a
separate store, because `Store.List` selects on the swap prefix and would
otherwise try to read a board record as a swap. See [BOARD.md](BOARD.md#storage).

**Export** and **import** exist because browser storage cannot be copied by hand.
Import adds what is missing and leaves existing records alone — the copy in this
browser may have advanced past the backup, and replacing it would discard a newer
signature.

---

## Settings, and why there is no operator

The network, the Esplora URL and the Zenon URL ride on every call that reaches a
chain, and a `Manager` is assembled per call from the settings that call carried.
Both clients are a URL and a timeout, so building one per call is free — and it
means changing a node URL takes effect on the very next call, with nothing cached
from before.

The Zenon URL has **no default**. Baking one in would make every user trust an
endpoint they never chose for the one check where a dishonest answer costs money.

---

## Network access

`chain/esplora.go` and `znn/client.go` talk through `httpx`, a package that is
one function with two build-tagged implementations: `fetch()` under `js/wasm`,
`net/http` everywhere else.

That is not an abstraction for its own sake. It is the single largest
optimisation in the build:

| build | raw | gzipped |
|---|---|---|
| Go runtime baseline | 2.0 MB | 0.58 MB |
| with `net/http` | 10.2 MB | 2.75 MB |
| the whole app, with `net/http` | 14.2 MB | **4.05 MB** |
| the whole app, over `fetch()` | 9.5 MB | **2.9 MB** |

`net/http` unconditionally links `crypto/tls`, `crypto/x509` and the HTTP/2
stack, none of which execute under `GOOS=js` — the round-tripper hands everything
to `fetch` anyway. The linker cannot prune them because `http.Client` reaches
`Transport` reaches `tls`. Calling `fetch` directly removes 8 MB raw and 2.2 MB
gzipped, which is more than the entire rest of the application.

This is also why `net/http` is absent from those files' imports *including for
its constants*: importing it links its package initialisers, and the saving is
gone.

The `!js` implementation is not dead weight. It keeps `go vet` and `go test`
compiling on a normal GOOS, which is what lets the whole suite run on a host.

### What is left, and why

The remaining 2.9 MB is roughly: 0.58 MB Go runtime, 1.35 MB `btcutil` (which
pulls in `crypto/x509` and `net` for helpers this program never calls),
~0.6 MB `encoding/json`, and ~0.3 MB of `txscript`, `btcec`, `chaincfg` and this
program's own code.

`btcutil` is the biggest remaining item and it stays. What it provides is
address decoding across P2PKH, P2SH, bech32 and bech32m with network validation,
`Hash160`, and WIF encoding — precisely the code where a subtle bug sends money
to the wrong place. Trading 1.35 MB of one-time, cached download for a
hand-rolled reimplementation of Bitcoin address handling is a bad trade for this
program.

The module is fetched with a progress indicator rather than behind a spinner,
because a silent multi-megabyte wait reads as a broken page.

---

## The Zenon leg

Zenon needs two signed operations this page cannot perform: creating an HTLC and
unlocking one. Both are ordinary account blocks sent to the embedded HTLC
contract with ABI-encoded arguments, so the page builds the block and something
holding a key signs it.

`wasm/znn/htlcabi.go` encodes the three calls. It does **not** depend on
go-zenon, which pulls in go-ethereum and would end the module-size budget above;
the ABI is three methods, so it is hand-rolled and pinned byte for byte to golden
vectors generated from go-zenon's own encoder.

`wasm/walletblock.go` turns a swap plus an action into that block.
`wasm/walletsync.go` is the gate in front of it: the wallet has its own node, its
own chain identifier and its own selected account, and all three can disagree
with this page in ways that cost money. It refuses to *build* a block unless a
momentum hash read from both nodes at an agreed height matches — and a check that
could not run is a refusal, not a pass. [EXTENSION-WALLET.md](EXTENSION-WALLET.md)
has the whole boundary.

Reads go the other way. `wasm/znn/ledger.go` finds what became of an HTLC and,
when one was unlocked, recovers the preimage out of the transaction that removed
it — by walking the contract's own chain, because `htlc.Unlock` may be called by
anyone, so the caller cannot be assumed.

---

## Routing and paths

**Hash routes.** A static host cannot answer `/history` with the app: GitHub
Pages looks for a file there, does not find one, and serves its 404. The usual
workaround — copying `index.html` to `404.html` — makes the link work but answers
it with an HTTP 404, which is a lie that breaks caches and crawlers. `#/history`
is honest about being a client-side route, needs no host cooperation, and works
identically from a project page, a user page, a local preview, or a copied
folder.

**A fragment inside the fragment.** `#/docs#keys` is one hash route plus one
in-page anchor. The browser spends the outer fragment choosing the route and
scrolls to nothing, so `scrollBehavior` reads `to.hash` and scrolls itself —
guarded with a `try`, because a fragment is whatever somebody typed in the
address bar and `querySelector` throws on one that is not a valid selector.

**The board is inside the gate, like everything else.** A post is signed by a key
the module holds, and a stranger's post is verified by it before anything is
shown; a board without the engine would be a list of unverified claims, which is
precisely what it is not.

**One route outside the engine gate.** Every page goes through the WebAssembly
module, so `App.vue` wraps the router view in `EngineGate` — every page but
`#/docs`, which is prose the build already contains. The exemption is
`meta: {engine: false}` on the route rather than a name checked in `App.vue`, so
it travels with the route it describes. It is what lets the failed-engine screen
link to *how to get your money out*: that answer has to survive the failure it
is about.

**Relative base.** `base: './'` in the Vite config, so one build works under
`/<repo>/`, under `/`, and from any local directory, with no deploy-time flag to
get wrong. It is also what lets the Recover page work from a saved copy of the
site on an offline machine.

**Cache busting for two files.** `ferry.wasm` and `wasm_exec.js` live in
`public/` and are copied verbatim, so they get no content-hashed filename. A
stale shim paired with a fresh module fails at instantiation with an import
mismatch that reads like a corrupt download — so the build appends the module's
content hash to both URLs.

Those two files must always be produced together, which is why `scripts/build.mjs`
copies `wasm_exec.js` out of the Go installation that just compiled the module,
rather than committing it. The shim is version-locked to the compiler.

---

## Things worth knowing about the pieces

- **An in-app manual** at `#/docs`. `dist/` is the site and nothing else, so
  every document in `docs/` is invisible to whoever actually uses the thing. It
  is written rather than rendered from those files — they are addressed to a
  reader of the repository, and it is addressed to somebody mid-swap — but it
  imports the stage names, the `znn-cli` expiry cap and the per-network Esplora
  defaults from the modules that define them, so it cannot drift into naming a
  default the app does not use.
- **The Recover page** reads no store, contacts no node, and needs no settings,
  which is what makes a saved copy of `dist/` a complete offline rescue tool.
- **A network picker**, and a per-card mismatch warning: a swap keeps the network
  it was created on, and everything that reaches a chain is disabled while that
  disagrees with the current setting.
- **`scripts/smoke.mjs`** runs the shipped `.wasm` under Node and covers the
  seam between JavaScript and Go — the boundary, the store, the recovery path.
- **`scripts/regtest-esplora.mjs`**, because choosing Esplora as the only
  backend took the local chain with it. A page cannot reach a regtest node's
  Core RPC, so a local build had nothing to talk to. The shim serves the six
  endpoints `chain/esplora.go` calls, over that node, with CORS. It is the
  smallest thing that makes the *browser* testable against a chain nobody has to
  fund.
- **`scripts/wallet-devnet.mjs`** builds a two-sided swap against a live devnet
  and takes the `htlc.Create` block apart field by field — the three things a
  chain has to answer (chain identifier, token decimals, momentum clock) are
  exactly the ones an offline smoke test cannot.

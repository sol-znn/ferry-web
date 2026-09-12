# Security model

What this software can and cannot do to you, and where the sharp edges are.
Worth reading before putting real money through it.

---

## What Ferry cannot do

**It cannot steal your coins.** That is a property of the *contract*, not of the
code:

- The per-swap key can satisfy exactly two branches of exactly one contract, and
  both branches pay an address you supplied. There is no code path that pays
  anywhere else, and you can read the built transaction before it is broadcast.
- Funding goes from your wallet straight to a P2SH address. It never passes
  through anything Ferry controls.
- Ferry never sees a key from your wallet, and never asks for one.
- The Zenon leg is signed elsewhere. Ferry builds an account block; your wallet
  signs and publishes it, and Ferry compares what came back against what it
  proposed.

**The counterparty is a different question from the software**, and the checks
that stand between you and them are the subject of
[REVIEW-2026-09.md](REVIEW-2026-09.md). Two of them had holes — the Zenon HTLC's
token was not verified at all, and the amount check collapsed to nothing for a
token the node did not know — which together let a counterparty pay a swap in a
worthless token of their own issue and be told it verified. Both are fixed and
both are now pinned by tests. Read that document before trusting the verification
screen with an amount you care about.

**A malicious build could steal your coins**, which is true of every program you
have ever run. What differs for a web page is how you check: for a binary you
compare a hash, for a page you are trusting the host to serve what the repository
builds. See [Trusting the deployment](#trusting-the-deployment).

---

## What running in a browser costs

### 1. Keys sit in `localStorage`, unencrypted

One record per swap, holding the key that can spend that swap's contract. Four
things about that are worse than a file on disk:

**Origin, not directory.** `localStorage` is scoped to an origin, and every page
on that origin can read all of it. On `https://<user>.github.io/<repo>/` the
origin is `https://<user>.github.io` — the path is not part of it. **Every other
GitHub Pages site that user publishes shares this storage.** A single vulnerable
or careless page of theirs can read every swap key. Deploying to a dedicated
domain, or a user site that hosts nothing else, removes this entirely and is the
single most valuable thing an operator can do.

**Any script on the page can read it** — an injected script, a compromised
dependency, a malicious browser extension with host access. The
[Content-Security-Policy](#4-content-security-policy) is the main defence and it
is not a complete one.

**It is erased casually.** "Clear site data" — the thing people click to fix an
unrelated site — deletes it. So does a private window closing, a browser
reinstall, and some "storage pressure" eviction. There is no warning and no
undo. A file in a directory gets swept up by whatever backs up your home folder;
this does not.

**It cannot be copied by hand.** Which is why there is an explicit export, and
why the recovery prompt on a funded swap is a bordered section on the card
rather than a line of small print.

**Mitigations that exist:**

- A refund transaction is signed the moment funding is seen, and every recovery
  file carries it. Broadcasting it after the locktime needs nothing but a text
  field on any node.
- The **Recover** page rebuilds a spend from that file with no store, no node
  and no network.
- **Export all swaps** produces one document you can keep elsewhere and import
  into any browser.

**The fix that does not exist yet:** passphrase encryption of the stored
records. It is the top item in the README's gap list.

### 2. Every chain call is a `fetch()` from a page

A page can only talk to hosts that opt in with CORS headers, over a scheme at
least as secure as its own. Two consequences:

- **Bitcoin Core RPC is unreachable**, so the private-node option is "your own
  Esplora instance" rather than "your own Core node". See the README.
- **A page on `https://` cannot reach a node on `http://`.** Mixed content is
  blocked before the request leaves. A node on `127.0.0.1` therefore needs the
  page served over `http://` too, which is what the local development flow does.

The browser deliberately does not tell a page *why* a cross-origin request
failed — down, bad certificate, and missing CORS header all arrive as the same
opaque `TypeError`. Ferry's error message names all three rather than guessing,
because guessing sends people to check a node that was never the problem.

### 3. There is no server, in both directions

There is no endpoint that can move coins, so there is nothing to authenticate,
nothing to rate-limit and nothing reachable by anything but the page that loaded
it. DNS rebinding, another local user and another origin reading a response are
all gone rather than defended.

What replaces a server's guards is the browser's own boundary: the same-origin
policy, and the CSP below.

### 4. Content-Security-Policy

A static host does not let us set headers, so the policy is a `<meta http-equiv>`
in the document. Same enforcement for every directive except one:
**`frame-ancestors` is ignored in meta form**, so this page cannot refuse to be
framed. A host that can set headers should add `X-Frame-Options: DENY` or
`Content-Security-Policy: frame-ancestors 'none'`.

Two directives are not what a template would produce:

**`script-src 'self' 'wasm-unsafe-eval'`.** WebAssembly compilation is blocked
outright without `'wasm-unsafe-eval'`, and the compiled module is the entire
application. Despite the name it permits WebAssembly compilation and nothing
else — not `eval`, not `new Function`. `'unsafe-inline'` is absent, and the
build emits no inline script, so an injected `<script>` of either kind does not
run. This is the directive doing the real work.

**`connect-src *`.** This one is a genuine trade. A tight `connect-src` is the
standard anti-exfiltration control: it stops injected code from posting your
keys somewhere. But the entire point of Node settings is that *you* choose which
Esplora and which Zenon node to trust, and an allowlist here would mean this page
choosing for you — including, inevitably, a default that everyone ends up using.
Between "you cannot use your own node" and "a script that already runs on this
page could also exfiltrate", the first cost was judged higher, because the second
is already game over: code that can execute on this origin can read
`localStorage` directly. `script-src` is what stops that code existing.

`style-src` allows `'unsafe-inline'` because Vue writes style attributes and the
design system's popovers position themselves that way. Injected CSS cannot read
a private key.

### 5. The wallet is a separate program with its own settings

The Syrius extension has its own node URL, its own chain identifier and its own
selected account. This page can read all three but set none of them, and each
disagreement costs money in its own way: a block signed by the wrong account is
reclaimable only by that account, a block signed for the wrong chain lands where
the counterparty is not looking, and a stale node produces an expiry the chain
disagrees with.

`wasm/walletsync.go` refuses to build a block unless a momentum hash read from
both nodes at an agreed height matches, and treats a check that could not run as
a refusal rather than a pass. After publishing, the block the wallet actually
signed is compared field by field against the one this page proposed. See
[EXTENSION-WALLET.md](EXTENSION-WALLET.md).

### 6. ZNN is locked against one confirmation

When Bitcoin initiates, the participant's Zenon HTLC answers the initiator's
Bitcoin payment, and the initiator already holds the secret that opens it. A
payment still in the mempool can be replaced by its sender for a slightly
higher fee -- so locking ZNN against one lets them take the ZNN and keep the
BTC. Ferry therefore refuses to build the create, withholds the button and the
printed `znn-cli` command, and keeps Auto Mode waiting, until the funding is
present at the contract, covers the agreed amount, is mined at least
`commitConfirmations` deep (`wasm/swap.go`, currently **one** block), and is
still unspent -- all re-read from the chain both when the block is built and
again immediately before it is handed to the wallet -- and the audited
contract's locktime, which is fixed by the script and so is read from the
swap record, not yet passed and far enough away to fit a Zenon leg before it.
A chain that cannot be read is a refusal. No `znn-cli` create command is printed for that
leg at all: a terminal runs no check, and a gate on the text that relies on a
remembered answer always has a moment where the answer is stale. The block
prints the leg's terms as comments for anyone who must compose the command by
hand, with the instruction to check the funding on their own node immediately
before running it. The Zenon-initiated ordering is exempt: that leg goes first
by design.

One block is the threshold at which replacement stops being free: undoing a
mined payment means mining a competing block. It is not finality. A reorg one
block deep happens on Bitcoin from time to time, and for a swap whose value
would make somebody mine for it, one confirmation is the wrong number. The
constant is a single place to raise it for every swap; for an individual
high-value trade, wait for the depth you would want from any payment of that
size before pressing Create -- the card counts to six -- rather than letting
Auto Mode act at one.

---

## Trusting the deployment

You are trusting whoever serves the files. That is not unique to a web page, but
the check is different:

- **The source is the artefact.** The GitHub Actions workflow builds `dist/`
  from this repository and publishes it, with no manual step in between. What is
  served corresponds to a commit you can read.
- **The build is not yet reproducible.** Go's `-trimpath` removes build paths,
  but the toolchain, the module cache and the npm dependency graph are not
  pinned tightly enough to guarantee byte-identical output from a clean machine.
  Until they are, you cannot verify the deployed `ferry.wasm` against your own
  build by hash. This is worth fixing and is not fixed.
- **Anyone can serve it themselves.** `npm run build` and copy `dist/`. It works
  from any path, because nothing is absolute.
- **The Recover page is the escape hatch.** It needs no network and no stored
  state, so a copy of `dist/` saved to a USB stick recovers funds from a
  recovery file on an air-gapped machine, without trusting any host at all.

---

## Practical advice

If you are moving real money:

1. **Download the recovery file the moment a swap is funded.** Not later. The
   card will not stop asking, and this is why.
2. **Export your swaps** and keep the file where you keep other key material.
   Both it and the recovery file can spend the contracts they name — they are
   private keys with extra JSON around them.
3. **Set your own Zenon node.** There is no default because verifying a
   counterparty's HTLC is the one place a lying node costs you money.
4. **Prefer a dedicated origin.** A user site or custom domain that hosts
   nothing else, so browser storage is not shared with unrelated pages.
5. **Use a browser profile with few extensions** for this, in the same spirit as
   not running a wallet in your main browsing profile.
6. **Check the contract address in two places** before funding — the card and a
   block explorer — as you would with any address you are about to pay.
7. **Read the Zenon HTLC's token, not just its amount.** The card shows the
   agreed token standard and, when the entry holds a different one, names both.
   Verification refuses that mismatch now, but a token standard is a long
   base-32 string and two of them can differ in one character: check it the way
   you would check an address.

---

## Reporting

Report anything that lets a counterparty take funds, or that reports a check as
having passed when it did not run. The places worth looking first are the
contract template parser, the leg-ordering rules, the Zenon verification path,
the JavaScript/Go bridge, the storage layer, the CSP, and the deployment.

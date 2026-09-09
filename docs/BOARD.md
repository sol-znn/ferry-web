# The board

A public place to say what you want to trade, with nobody hosting it.

Ferry's [live sessions](../README.md#live-sessions) solve the problem of two
people who have already agreed to swap. The board solves the one before it —
finding somebody at all — and it is a different problem with a different threat
model, because the content is public by construction. A session is a sealed room.
A board is a notice pinned in a square.

This document is what the board guarantees, what it deliberately does not, and
how each of those is enforced.

---

## What it guarantees, and what it does not

**Guaranteed.** A post is signed by its author's key. Nobody else can edit one,
withdraw one, or publish one under somebody else's name without forging a schnorr
signature. Every post a reader sees has had its id recomputed, its signature
checked, and its wallet proofs re-verified locally — a relay hands over claims,
not facts.

**Not guaranteed, and not implied anywhere in the UI.** That a poster will
actually trade. That a rate is fair. That a "verified" badge means anything about
conduct — it means the author holds an address they named, and nothing else. That
the self-reported "swaps completed" number is real; it is typed by the person
claiming it and is labelled as such wherever it appears.

What protects a trade is the swap, not the board: both legs settle or both refund,
the counterparty's contract is audited against terms agreed before either side
funds, and every term is re-checked at creation time by
[`offerterms.go`](../wasm/offerterms.go). A stranger from a public board is
precisely the threat model that was designed for.

---

## Why an addressable event

The two properties the board needed — a post nobody can tamper with, and one its
author can edit or revoke at any moment — turn out to be one property, and Nostr
gives it in a single move.

Kinds 30000–39999 are **addressable**: a relay keeps exactly one event per
`(pubkey, kind, d-tag)` and replaces it only on a newer, validly signed one. So

- **editing** a post is republishing it under the same `d`;
- **revoking** it is republishing it as `void`;
- **impersonating** its author is forging a signature;

and none of it needs an index, a server, or an account. The board is kind
**30777** — the addressable echo of the session's 9797.

| tag | what it is for |
|---|---|
| `d` | the addressable slot: the identity of the **offer**, not of one version of it |
| `t` | `ferry-board-v1` — the only thing a reader needs to know to find the board |
| `n` | the network, so a relay drops the other chains before sending them |
| `s` | the status, so a reader can ask for open posts only |
| `expiration` | NIP-40, so relays that implement it retire the post themselves |

All four are single-letter because those are the tags relays index. A filter on
anything else is one the relay answers by scanning, or refuses outright.

### Dev and prod are different boards, not different filters

`t` is not the fixed string above — it is `boardTag()` in
[`board.go`](../wasm/board.go), and it differs by build: `ferry-board-v1` for
production, `ferry-board-v1-dev` for the development instance. Takes carry it
too, on the same principle.

The network tag looks like enough separation on its own, and it almost is — but
`network` is a setting a user can change, on either build, from the same
`NETWORKS` list. Point a development instance at `mainnet` for a moment — which
costs nothing and is one dropdown away — and without a second axis its test
posts would land in the same public index as real offers, on the very relays
that carry both. A tag a build cannot be talked out of by its own settings is
what actually keeps the two boards apart, the same way `StorageKeyPrefix` keeps
the two instances' swaps apart regardless of what a user does in Settings.
`TestBoardTagIsScopedToTheInstance` in `board_test.go` publishes a post
deliberately mislabelled `mainnet` from a dev-flagged build and checks the tag
on the wire anyway.

### Two readers of the same relays must see the same board

A relay hands over events, not state, and a list is what a client makes of them.
Three things had to be pinned down before two people looking at the same relays
saw the same offers.

**A republish is always strictly newer than what it replaces.** Two versions of
one post can share a `created_at` — edit then immediately withdraw, or renew
twice — and NIP-01 resolves that tie by keeping the event with the **lowest id**,
which over a hash is a coin flip. Roughly half the time a relay answers a
withdrawal by keeping the offer and discarding the retraction; the author's own
board shows it gone, because their store was written locally, while everybody
else is still being offered a trade that was taken back. `SealPostAfter` takes
the previous version's timestamp as a floor, so the tie never happens. `MyPost.
PublishedAt` records the *event's* timestamp rather than the wall clock, because
that value is the floor for the next republish.

**The read side resolves ties the way relays do.** Timestamps alone are a partial
order, and the old rule let whichever version arrived second win — so two
browsers merging the same pair from relays that answered in different orders
reached opposite conclusions, with nothing in either session to suggest anything
was wrong. `accept` in [`useBoard.ts`](../ui/src/core/composables/useBoard.ts)
now mirrors NIP-01: newer wins, and on a tie the lower event id. Not an arbitrary
pick between two equally good rules — it is the one every relay is already
applying, so the list agrees with what the relays will converge on rather than
merely with other copies of this app.

**The network is re-checked on arrival.** The subscription asks for `#n`, but a
relay filter is a *request*: relays may send more than was asked for, and they
differ in which tags they actually index. A post for another chain was reaching
one reader and not another purely on which relay answered. `ReadPost` records the
mismatch rather than refusing it — the same function reads this browser's own
posts back — so the decision not to carry one is made in `receive`, at the door,
where every list built from `byKey` inherits it. Filtering the open market alone
would have left "include expired and withdrawn" free to disagree.

One difference between two sessions is **not** a bug: your own posts never appear
in the market list, only in *Your posts*. A board where your own offer sits among
the others is one where you eventually try to take it.

### Expiry is decided twice

The `expiration` tag asks relays to drop the event. Most relays have never
implemented NIP-40, so the deadline is **also** in the post body and the reader's
own clock is what actually retires it — see `reap()` in
[`useBoard.ts`](../ui/src/core/composables/useBoard.ts). The tag is an
optimisation; the body is load-bearing.

Default life is **24 hours on production, 15 minutes on the development
instance** — `DefaultPostTTL()` in `board.go`, reported to the page as
`limits.defaultTtl` so the form holds no second opinion about it.

A day is roughly the life of the thing being advertised: the participant's leg of
a swap runs 24h, prices move, and it is short enough that abandonment is
self-correcting. It is also what covers somebody closing the tab — only the
browser holding the key can withdraw a post, so the deadline is what retires an
offer its author walked away from.

Fifteen minutes on dev because what is being tested there is not a market. A test
post is made to watch one thing happen and is abandoned within the minute, so a
day-long default would leave every experiment standing on public relays until
tomorrow and open the next run onto a board of yesterday's rubbish. Long enough
to drive a swap through, short enough that a forgotten one is gone before anybody
looks again.

---

## Identity: one key, signed for once

Every post is signed by a **board key** the module mints on first use. It is not
an account and there is nothing to sign up for. It cannot spend, cannot sign a
swap, and cannot claim an address that has not separately been bound to it;
losing it costs the ability to edit posts already published, which expire within
the day.

A **binding** is the separate half: a wallet signature saying "the key that signs
those posts is held by whoever holds this address." It is not optional. Nobody
posts an offer or takes one without a binding on every chain that offer settles
on — see [A proof per chain](#a-proof-per-chain-or-no-trading) below.

**One signature per identity, not one per post.** A post, an edit, a renewal and
a withdrawal are four wallet popups for one offer, and a board where every
keystroke opens the wallet is a board nobody edits. So the wallet signs once, over
the board key, and the board key signs everything after. That is a delegation, and
it is stated in the sentence being signed rather than left implicit:

```
ferry-board-bind-v1
key: <board pubkey, x-only hex>
address: <the address being claimed>

Signing this lets the key above post trade offers as you on the Ferry board.
It authorises no payment and moves nothing.
```

Both halves are in the statement and both have to be. The key alone would let a
signature made for one address be replayed as a claim about another the same
wallet holds; the address alone would let a signature be lifted off one person's
post and pasted onto somebody else's key.

The statement is produced by `boardStatement` rather than assembled in the page —
it is what the signature commits to, and a page that built one while Go verified
another would fail every proof invisibly.

### Bitcoin: `btc-ecdsa`

`signMessage(statement, "ecdsa")` — the 65-byte recoverable signature
`bitcoin-cli signmessage` produces and every wallet has spoken for a decade.
Verification recovers the public key from the signature and derives **every**
address form that key writes on this network — legacy, native segwit, wrapped
segwit and BIP-86 taproot — then requires the claimed address to be one of them.

Four rather than one because a wallet signs with the key behind whichever address
is selected and never says which form that address takes: UniSat defaults to
taproot, older setups are native segwit, hardware imports are often wrapped
segwit. Checking one would refuse most wallets for no reason.

`bip322-simple` is deliberately not used. It is a serialized transaction rather
than a compact signature, and verifying one properly means running a script engine
over a virtual input, for a gain of nothing here.

**The network is deliberately not part of the check.** A signed message is a
signature over a hash by a key — no chain appears in it anywhere — and only the
*encoding* of the address differs between networks (`bc1`/`tb1`/`bcrt1`, and
three base58 version bytes). So every network's spelling is tried and a match on
any is accepted.

Scoping it to the reader's network refused proofs that were simply true, and it
made the feature unusable exactly where it is most used: UniSat has no regtest
chain at all, so a developer on the regtest instance can only ever sign with a
mainnet address. Nothing is given away by accepting it — the statement still
names the board key *and* the address, so a proof cannot be lifted onto either.

### Zenon: `znn-ed25519`

Produced by the Syrius extension's `signMessage`, which answers the `znn_sign`
request desktop Syrius already serves over WalletConnect — the same bytes and the
same encoding, so a signature either one makes verifies against the same code.
See [EXTENSION-WALLET.md](EXTENSION-WALLET.md#the-provider).

**An extension build without it cannot trade a Zenon leg here.** That is the
price of making the proof a requirement rather than a badge, and it is worth
saying plainly: on such a build the Prove button comes back with the sentence in
`zenon-wallet.ts` naming what is missing, and nothing falls back — an address
accepted unproven would be the whole rule with a hole in it. Bitcoin-only
trading is not a fallback either, because there is no pair without a Zenon leg
yet.

The contract, and all of it that has to hold:

> The extension signs the statement's **raw UTF-8 bytes** with the account's
> ed25519 key, and returns that signature alongside the **32-byte public key** —
> because an ed25519 signature carries no recoverable key the way a Bitcoin one
> does.

Go then derives the `z1` address from that key
([`wasm/znn/address.go`](../wasm/znn/address.go): SHA3-256 of the key, first 19
bytes behind the user kind byte, bech32 under `z`, pinned to a vector computed
by an independent implementation) and requires it to equal the address being
claimed. So a wallet cannot claim an account it does not hold, whatever it
reports.

If the extension hashes or prefixes the message first, `verifyZNNProof` in
[`board.go`](../wasm/board.go) is the one function that changes, and
`TestZNNProofIsReadyForTheExtension` is the test that says so.

Two alternatives to asking the wallet were considered and both are worse:

- **Publish a self-send carrying the board key and read the claim off the chain.**
  It works, and it costs plasma plus a permanent on-chain footprint for every
  identity on the board.
- **Accept the address unproven and badge it anyway.** A badge that appears
  because a field was filled in is a badge doing the opposite of its job.

### A proof only badges an address the post names

`ReadPost` refuses to badge a proof whose address is not one the post advertises.
Otherwise it would verify something true about an address nobody is trading with,
while the badge sat next to the one on screen. The mismatch is reported in
`problems` instead — a post carrying a valid proof of an unrelated address is one
of the more interesting things that can appear on a board, and it belongs on
screen rather than filtered away.

---

## A proof per chain, or no trading

Reading the board needs nothing. **Posting an offer or taking one requires a
proven address on every chain that offer settles on** — and both halves of that
sentence are load-bearing.

**Proven**, not merely connected. A connected wallet hands over an address and
nothing else: the page reports whatever it is given, and an address in a signed
post is a claim a stranger will send money against. A proof is a wallet signature
over a statement naming this board key and that address, verified in Go against a
key the address is derived from. It costs one popup per chain, once.

**Per chain**, not "both wallets", because the requirement belongs to the trade
rather than to the app. Today there is one pair and the rule reads as "both",
which is exactly why it is written as a list instead:

| the offer | what must be proven first |
|---|---|
| BTC ⇄ ZNN | Bitcoin **and** Zenon |
| ZNN ⇄ ZNN (a different ZTS) | Zenon only — there is no Bitcoin leg to prove |
| a chain added later | that chain, wherever it appears in the offer |

The requirement is *derived from the offer* rather than asserted beside it:
`chainsForPost` in [`ui/src/core/chains.ts`](../ui/src/core/chains.ts) reads which
chains a post settles on, and `chainsNamed` in
[`board_api.go`](../wasm/board_api.go) does the same from the addresses a publish
or a take carries. Every gate in the UI and both refusals in Go are written over
that answer, so a second pair is a row in `PAIRS` and a chain is a row in
`CHAINS` — neither is a rule change, and neither can leave one of the two sides
enforcing last year's rule.

The buttons that make a proof say **"Prove Bitcoin address"** and **"Prove Zenon
address"** — the chain, never the wallet. Which extension is asked to sign
changes with the build and the chain; a control named after this year's two
extensions is a control that has to be renamed to add a third.

A swap settles to four addresses: each side's payout on each chain. Exactly two
of them are ones nobody else can supply for you. Unproven, there was nowhere
trustworthy to get them, so the introduction ended with two people who had agreed
to trade and still had to find somewhere else to exchange the addresses that
would let them. That "somewhere else" is what this removes: every out-of-band
step is a step outside anything this app can check, and pasting an address into a
chat window is the easiest place in a whole swap to be handed somebody else's.

So the addresses travel with the introduction itself:

| moment | what carries them | who learns what |
|---|---|---|
| **creating** an offer | the post body, in the clear | every reader learns where to pay the author |
| **taking** one | the take, sealed to the author | only the author learns where to pay the taker |
| **accepting** a take | an `addresses` session message | the taker learns the author's *current* addresses |

The asymmetry in the first two is deliberate. A post is a notice in a square and
carries its author's addresses in the open, because that is what makes it
answerable at all. A take is answering one stranger, and sealing means only that
stranger learns where the taker's money goes — publishing a take in the open
would put both sides' addresses on a public relay.

The third looks redundant, since the taker already read the author's addresses
off the post. It is not: a post carries the addresses it was **published** with,
and an author who switched wallets since — or who renewed a post from last week —
would otherwise have the taker building against an address they no longer hold.
The accept sends what is connected *now*.

Of the pair, the **Zenon address is the load-bearing one**: an HTLC is created
*to* an address, so neither side can build their leg without the other's. The
Bitcoin address is each party's own payout and the counterparty never spends to
it — on that chain what matters is a pubkey hash, which is what a contract
commits to and which travels over the session per swap. It is carried anyway so
each side can see the whole shape of what they are agreeing to.

### Where each half is enforced

`handleBoardPublish` refuses a post missing either address, and `handleBoardTake`
refuses a take missing either. Both then call `requireProven`, which refuses any
address the identity holds no binding for — and refuses a binding that is for a
*different* address on that chain, which is where somebody who switched wallet
accounts lands. `TestBoardRefusesAnAddressThatIsNotProven` covers all four cases
plus the take.

Both refusals are in the **handlers**, not in `validate()` — and that asymmetry
is deliberate, because `validate()` is shared by publishing and reading precisely
so this app never publishes what it would refuse to read. A post without
addresses, or with unproven ones, is not incoherent; it is merely older. Making
it a read-time failure would blank every board on the day this shipped and would
keep refusing posts from anyone still on the previous build. This build will not
*make* one. It still reads what is already out there, and a row for an offer that
names neither address shows a disabled Take with a note saying why.

The UI gate — the disabled buttons and the line under the board key — is a
**product guarantee, not a security boundary**. This is a static page, so
anything in it is reachable from devtools. What is not reachable is the module:
Go refuses the publish and refuses the take, and every swap term is still
re-checked against a signed offer on create.

### What it does not require

**A wallet on the matching network.** That check exists and is loud where it
belongs — `WalletConnect` replaces its own button with a switch prompt on a
mismatch — but it cannot be the gate here, because UniSat has no regtest chain at
all. Making it one would lock every developer out of the development board, which
is the board where this is exercised most. It is also why `VerifyProof` does not
scope a Bitcoin proof to the reader's network: a signature is over a message by a
key, no chain appears in it, and the module compares the recovered key against
every network's spelling of the address.

**A proof on a chain the offer does not settle on.** A binding for an address
nobody is trading with is neither required nor in the way: `proofsFor` attaches
only the bindings a post actually names, and `ReadPost` badges only those.

**A separate Connect step.** The Prove buttons connect the wallet first and then
ask for the signature, so an unconnected wallet is connected by pressing one. A
second pair of buttons doing the first half of that would be two controls for one
intention.

---

## Taking an offer

The session code is the problem here. A code **is** the room: `session.go` derives
both the publishing identity and the encryption key from it, so anyone holding one
can read that conversation and post into it. Publishing a code on a public board
would hand every reader the room, and put every interested taker in the same one.

So the board advertises a **key**, and a taker sends a code **to** it:

```
POST  kind 30777, public          TAKE  kind 9778, sealed
  terms, expiry, board pubkey  ←    { postId, sessionCode, amount, note }
                                    ECDH(taker priv, maker pub) → AES-256-GCM
                                    signed by the taker's board key
                                    tagged  p = maker pubkey
```

Each taker gets a room of their own; the maker sees an inbox and accepts one.

- **Regular kind (9778), not addressable or ephemeral.** A relay must *store* it,
  or a take sent while the maker's tab was closed would never be seen — and it
  must not replace the previous one, because two people may take the same post.
- **Signed as well as encrypted.** The signature tells the maker which key sent
  it, which is the same key whose other posts they can look up. An anonymous take
  is a session code from nobody.
- **x-only ECDH is safe here.** Nostr keys carry no y parity, and none is needed:
  negating a point leaves its x coordinate alone, so both sides derive the same
  shared secret whichever parity the stored key had. NIP-04 rests on the same
  fact.

Accepting a take joins that session and moves the post to `taken` rather than
withdrawing it — so a taker who was not the one accepted sees *why*, instead of
watching an offer vanish and guessing. Declining sends nothing back: there is no
message worth publishing, and a refusal under your key would be a permanent public
record of who you would not trade with.

Nothing on the board becomes a swap term by this route. Once a session is open the
maker's real `swapoffer1` travels over it and lands in the create form through the
same decoder a pasted one uses, and is checked field by field by `CheckCreate`.

---

## Is anybody there: the green dot

A post says what somebody will trade. It does not say whether they are at a
keyboard to answer, and on a board where the next step is *send a stranger a
sealed session code and wait*, that is the difference between a trade tonight and
one that starts tomorrow. So every row carries a dot beside its author: **green,
"User is online"; grey, "User is offline"**.

A browser with a live post **and the board in front of it** republishes a signed
**beat** every `PresenceBeat` (20s). A beat still means online for
`PresenceStale` — three beats, one minute. So a closed tab goes grey within a
minute, and one dropped publish to a blinking relay still leaves two more chances
inside the window.

### Why beating stops when the tab is hidden

The window was three minutes, and it was that long because of **timer
throttling**: a hidden tab's `setInterval` is throttled — Chrome to roughly one
firing a minute — so a window that had to keep hidden tabs green had to tolerate
a cadence of a minute *whatever the beat was set to*. The floor came from the
throttled cadence, not the beat interval, so shortening the beat could not move
it. Setting the window near that cadence is worse still: the dot **flaps**, green
or grey depending on whether a throttled firing happened to land inside it.

Three minutes of showing somebody as present after they closed the tab is a lie
told at the moment it costs something — the dot exists to answer *will they see
my take now*, and the reader acts on the answer.

So the page goes quiet while hidden instead. Nothing then has to survive a
throttled timer, the lapse is deterministic, and the window can be three beats of
a tab somebody is genuinely looking at. It also narrows what the dot claims to
something a beat can honestly support: not "this browser is running somewhere"
but **the board is in front of them**. Switching back beats immediately, so
alt-tabbing costs a dot that goes grey and comes straight back rather than one
that lingers green for minutes after the tab is gone.

### Addressable, not ephemeral

Nostr's obvious home for a heartbeat is the **ephemeral** range, 20000–29999 —
relays forward those and never store them, which is the textbook shape and the
wrong one here. A reader opening the board asks relays for a week of posts and
gets them at once; if presence were ephemeral they would arrive with *nothing*,
and every row would sit unknown until each author's next beat happened to land.
The board would take a minute to tell you anything, every single load.

So a beat is **addressable** — kind **30778**, one slot per key (`d: presence`) —
and everything follows: relays hold exactly the latest one, hand it over in the
same backfill as the posts, and replace it on every beat after. One stored event
per author, forever, however long the tab stays open. It carries `t` and `n` like
a post, and a NIP-40 `expiration` of ten minutes so an abandoned key stops costing
relays anything.

### The reader decides who is online, not the author

A beat's payload is `{"v":1,"network":…}` — and the emptiness is the design. What
a beat asserts is *one key signed something at this time*; every judgement made
from it is the reader's:

- **The staleness window is the reader's constant.** A beat carrying its own
  interval would let an author declare themselves online for a week.
- **Online is recomputed on the reader's clock**, on the same ten-second ticker
  that moves the countdowns — `presenceOf()` in `useBoard.ts`, called from
  `BoardRow` with `nowMs`. This is the same split as `Listing.expired`: Go settles
  what cannot be recomputed (the beat is genuine, is for this network, is not
  stamped ahead), and the page re-judges the rest. Presence is the one thing on a
  row that changes *without anything arriving* — a browser that closed sends
  nothing to say so — so a value written when a beat landed would be a dot that
  could never go out.
- **A beat stamped far in the future is refused; one stamped slightly ahead is
  clamped.** `created_at` is written by its author, so a browser whose clock is a
  day fast would publish a beat that stays "recent" for twenty-four hours — past
  `PresenceSkew` (90s) it is refused outright, which shows that key as offline:
  wrong, but wrong in the direction that costs nobody money, and self-correcting
  the moment the clock is. *Within* the tolerance the beat is accepted but taken
  as `now` rather than at face value, because believing it would hand that one
  author a staleness window longer than everybody else's by the amount of their
  drift — silently undoing the tightening above. The window has to mean the same
  thing for everybody or it means nothing.

Note the asymmetry with a post, which does *not* get this check: a post's
`created_at` only decides which of two versions wins, and both were signed by the
same key, so a forward-dated one can suppress nothing but its author's own
earlier work. A beat's timestamp is the entire claim.

### Only while there is something to be present for

The heartbeat is gated on `hasLivePost()` — at least one post `open` or `taken`
and not expired. A beat is a public, signed statement that a key is at a keyboard,
republished every minute for as long as the tab is open; that is worth publishing
while somebody may be deciding whether to take your offer, and nobody's business
the rest of the time. A page that beat unconditionally would be broadcasting a
browsing session.

`taken` counts alongside `open`: a post mid-trade has a counterparty on the other
end of a session who very much cares whether you are still there.

The gate is in the page rather than the module, deliberately. `SealPresence`
signs; it does not decide *whether* to beat. A module that started broadcasting
its user's liveness on its own would be publishing a fact about them they never
asked to publish.

### Closing the tab

Nothing is sent on the way out, and nothing needs to be. A goodbye event on
`beforeunload` would be unreliable — a socket rarely flushes during unload — and
it would be a *lie* whenever another tab is still open. Lapse handles it: the
last beat ages past the window and the dot goes grey within a minute.

A closed tab and a hidden one are the same thing to a reader, deliberately.
Neither is beating, both lapse on the same schedule, and neither is a person who
is going to see a take land.

### The identity is the same one across restarts

This is not new work for presence — it is why presence can be attributed at all.
The board key is minted once by `Store.Identity()` and kept at
`ferry.board.identity` in `localStorage`, so a browser that closes and comes back
signs beats under the same key that signed its posts, joins them up on everybody
else's board, and can still edit, renew and withdraw them. Sealed takes for those
posts keep arriving, because a take is addressed to that key. Clearing site data
is the one thing that breaks it, and it costs the same thing it always did: the
posts already on relays become a stranger's, and expire on their own.

---

## Withdrawal, and retiring itself

`boardWithdraw` emits **two** events and the page publishes both:

1. a **void replacement** — a valid newer version of the same addressable post
   carrying `status: void` and an expiry pulled in to the minimum. Every relay
   honours it, because it is nothing more exotic than an edit;
2. a **NIP-09 deletion request** (kind 5, `a` = `30777:<pubkey>:<d>`), for the
   relays that implement one.

Publishing only the second leaves the post standing on most relays; only the first
leaves a tombstone. Both is the honest answer to "remove this".

**The board updates itself when a swap completes**, and it does so without polling
anything for a post — a post is not on a chain. The join is made when a take is
accepted: `boardLink` writes the swap id onto the stored post, and a watcher in
`useBoard.ts` sits on the swap list the app already keeps. When that swap reports
`settled` (Go's verdict, from the real rule — see `swapView.Settled`) or is
archived, the post is withdrawn as `done`.

It runs only in the browser that made the post, because it is the only browser
that *can*: withdrawing needs the key that signed. A maker who closes the tab
mid-swap leaves the offer standing, and the day-long expiry is what covers that.

---

## What the page does and does not decide

| | where |
|---|---|
| verify a signature, refuse a forged or altered post | Go — `ReadPost` |
| verify a wallet proof against this reader's network | Go — `VerifyProof` |
| refuse a post whose terms do not cohere | Go — `BoardPost.validate`, run on the way **out** as well as in |
| seal and open a take | Go — `SealTake` / `OpenTake` |
| move events | TypeScript — `RelayPool` |
| decide what a verified post *replaces* | TypeScript — `byKey` in `useBoard.ts` |
| rate, filtering, sorting | TypeScript — `board.ts` |

The rate is **derived, never carried**. A rate in the post would be a third number
that could disagree with the two it is computed from, and the two are what the
trade is actually in.

`validate` running on the way out as well as in is deliberate: a post this app
would refuse to *read* is one it has no business publishing, and having one
function decide both is what stops the two drifting apart.

Keys are keyed by `author:postId` rather than `postId` alone. The id is chosen by
whoever publishes, so two people can pick the same one — deliberately or otherwise
— and keying by author as well means one person's post can never replace another's.

---

## Everything else on the page

- **Direction is shown from the reader's side.** Every field on a post is stored
  from the author's — `side: send` means *they* pay Bitcoin — and a board that
  showed that verbatim would be one every reader inverts in their head, every row.
  The flip happens once, in `directionFor`.
- **"Best rate" knows which way round it is.** Buying ZNN wants the most ZNN per
  BTC; selling wants the fewest. Sorting both the same way would put the worst
  offers on top for half the users, silently. With no direction chosen there is no
  "best" to compute, so a mixed board falls back to newest rather than sorting by
  a number that means opposite things in adjacent rows.
- **Size bands.** A post can advertise a range; a taker names a size within it,
  and a take outside it is refused in the dialog rather than a round trip later.
  Size filters match on **overlap**, not containment — a post offering 0.001–0.1
  should appear for somebody looking to do 0.01.
- **Your own posts stay listed after they expire.** An offer that timed out
  overnight is exactly the one you want back, and *Renew* is a republish with a
  fresh deadline. A list that hid it would leave you retyping it.
- **The session panel is on this page too.** Taking an offer opens a session;
  sending the reader elsewhere to see whether anybody answered would be hiding the
  result of the button they pressed.
- **Posting goes through the same terms gate a swap does** — not because a post is
  a swap, but because it is the first step of one, and the gate is worth reading
  before there is anything to lose by reading it.

---

## Storage

Under `ferry.board.` (`ferry.dev.board.` for the development instance), separate
from the `ferry.swap.` records because `Store.List` selects on that prefix and
would otherwise try to read a board record as a swap. Same namespacing rule as
everything else on this origin: the two instances cannot see each other's boards.

- `ferry.board.identity` — the board key and its bindings.
- `ferry.board.post.<id>` — one record per post this browser has published.

*Forget* removes the local record, which is what lets this browser edit or
withdraw the post. It reaches no relay — **so a post that is still standing
cannot be forgotten at all.**

The private key is the only thing that can withdraw a post, and this record is
the only thing that says which slot to withdraw. Deleting it while the offer is
live does not remove the offer; it *strands* it — publicly takeable until it
expires, with the one browser that could retract it having just thrown away the
ability to. Somebody then takes an offer nobody is answering.

`Store.DeleteMyPost` refuses while `BoardPost.Standing` — `open` or `taken`, and
not expired — and the error names withdrawing as the way through. It is refused
in the store rather than the handler because that is the only door: a future
caller reaching for a "local only" delete is exactly who needs telling that local
is not where the consequence lands. The button is disabled to match, so nobody
meets the error by pressing it.

`Standing` is wider than `Live` by one status. A `taken` post is off the open
board, but its record is still published, its session is still running, and takes
still arrive against it.

---

## Tests

- [`wasm/board_test.go`](../wasm/board_test.go) — tampering (edited terms; edited
  terms with a recomputed id; re-signed under another key; filed under a slot the
  post does not name), expiry judged by the reader, incoherent terms, all four
  Bitcoin address forms, a proof claiming somebody else's address, a proof lifted
  onto another board key, a proof verifying on every network regardless of which
  one it was made on, the Zenon scheme against the contract above, and takes that
  only their recipient can open. Also the two things that differ by instance: the
  board tag, and the default post life.
- [`wasm/boardstore_test.go`](../wasm/boardstore_test.go) — forgetting refused
  while a post is open or taken and allowed once withdrawn, finished or expired;
  that the record survives the refusal; and that `Standing` counts a taken post
  where `Live` does not.
- [`wasm/presence_test.go`](../wasm/presence_test.go) — a beat restamped forward,
  the same with the id recomputed so only the signature stands, an author's clock
  running fast (refused) versus ordinary skew (accepted), a beat borrowed from
  another network, a post read as a beat and a beat read as a post, and a beat
  re-signed under another key. Plus the two invariants the timing rests on: the
  staleness window outlasts two beats, and the expiry outlasts the window.
- [`wasm/znn/address_test.go`](../wasm/znn/address_test.go) — the derivation,
  pinned to a vector computed with an independent SHA3 and bech32.
- [`scripts/smoke.mjs`](../scripts/smoke.mjs) — the JS/Go seam: the handlers are
  reachable, the identity is minted once and kept in the right namespace, the tags
  a reader filters on are present, an edit keeps its slot and its creation time, a
  withdrawal emits both events, and a session code is not on the wire.

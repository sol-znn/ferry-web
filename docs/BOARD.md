# The board

A public place to say what you want to trade, with nobody hosting it. A session
is a sealed room for two people who have already agreed; the board is a notice
in a square, public by construction.

## What it guarantees

Every post is signed by its author's key, and every post a reader sees has had
its id recomputed, its signature checked and its proofs re-verified locally — a
relay hands over claims, not facts. Nobody else can edit, withdraw or
impersonate without forging a signature.

**Not** guaranteed, and not implied anywhere in the UI: that a poster will
trade, that a rate is fair, that a badge means anything about conduct, or that
"swaps completed" is real. What protects a trade is the swap.

## Addressable events

Kinds 30000–39999 are addressable: a relay keeps one event per
`(pubkey, kind, d-tag)`, replaced only by a newer validly signed one. Editing is
republishing under the same `d`; revoking is republishing as `void`. Posts are
kind **30777**.

| tag | for |
| --- | --- |
| `d` | the slot: the identity of the **offer**, not of one version |
| `t` | `ferry-board-v2` — how a reader finds the board |
| `n` | the network, so a relay drops other chains |
| `s` | the status, so a reader can ask for open posts only |
| `expiration` | NIP-40, so relays retire the post themselves |

All single-letter — the tags relays index. `t` differs by build (`-dev`),
because `network` is a setting a user can change and a tag a build cannot be
talked out of is what keeps test posts out of the real index.

## What a post says

```jsonc
{
  "v": 2,
  "give": {"chain": "btc", "amount": "1000000", "min": "…", "max": "…"},
  "want": {"chain": "znn", "token": "zts1…", "amount": "1200"},
  "role": "initiator",
  "addrs": {"btc": "bc1…", "znn": "z1…"},
  "proofs": [ … ],
  "status": "open", "createdAt": …, "expiresAt": …
}
```

`give` is what the **author funds**; a reader takes the mirror, flipped once in
`core/board.ts`. A size band goes on `give` only — on both halves it is a band
on the *rate*, which nothing downstream can settle. No rate is carried: it is
`want ÷ give`, computed where shown, comparable only within a pair and
direction.

## The proof rule

**You may act on an offer once you have proven an address on every chain that
offer settles on** — posting and taking alike. Derived from the offer by
`chainsForPost`, not asserted beside it: BTC⇄ZTS needs two proofs, ZTS⇄ZTS needs
one, SOL⇄BTC needs no Zenon wallet.

A proof is one wallet signature over a statement naming your board key **and**
the address. Both halves are needed: the key alone lets a signature be replayed
against another address, the address alone lets one be lifted onto another key.
Enforced in Go, not by a disabled button — the page is reachable from devtools.

## Takes

A take carries a **session code**, and a code *is* the room — so unlike the post
beside it, a take is sealed to the poster's key, along with the taker's
addresses. Several people can take one offer and the poster picks; the post
moves to `taken` rather than vanishing, so an unchosen taker learns why.

## Presence

A browser with a live post and a visible tab republishes a signed beat every 20
seconds (kind **30778**, addressable); a reader counts three of those as online.
Addressable rather than ephemeral because relays do not store ephemeral events,
which would leave every row unknown until the author's next beat.

A beat asserts one fact — this key signed something at this time — and every
judgement is the reader's. It carries no interval of its own, or an author could
declare themselves permanently online. A timestamp more than 90s ahead is
refused; one inside that is clamped to the reader's clock.

Beating stops while the tab is hidden and never starts without a live post.

## Two consistency traps

- **NIP-01 breaks a replaceable-event tie by keeping the lowest id** — a coin
  flip over a hash. Edit then immediately withdraw, and half the time the relay
  keeps the offer. `SealPostAfter` floors each version's timestamp at one second
  past the one it replaces, and the reader's merge mirrors NIP-01.
- **Forgetting a live post strands it.** The local record holds the key that
  signs a withdrawal, so `boardForget` refuses while a post still stands.

## Lifecycle

A post runs a day by default, fifteen minutes on the development build — a test
post is made to watch one thing and then abandoned. Withdrawing publishes
**two** events: a void replacement, honoured by every relay, and a NIP-09
deletion request for those that implement it. When the swap an offer became
settles, the offer withdraws itself.

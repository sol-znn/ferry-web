# Security

Read this before using Ferry with real money.

## What is guaranteed

Both legs settle against one secret, or both refund. Neither party ever holds
both amounts, and nobody — counterparty, operator, or this software — can take
your coins.

That rests on one invariant: **the initiator's leg expires last.** Claiming the
participant's leg publishes the preimage, and the participant then needs time to
use it. Ferry refuses deadlines closer than `MinLegGap` (2h) and re-checks on
every refresh, because a swap that was safe to accept stops being safe by
sitting there.

## Who holds which key

| | where | what it can do |
| --- | --- | --- |
| Bitcoin contract | this browser, per swap | spend that one contract, to an address fixed at creation |
| Zenon / Solana | your wallet | whatever your wallet can do; Ferry never sees it |
| Board identity | this browser | sign your posts; cannot spend anything |

Bitcoin cannot enforce where the redeem branch pays — the script authorises a
key, not an output — so the rule lives in `payoutAddress`, the one function both
spends pass through. Paying elsewhere means exporting the recovery file and
building the transaction yourself.

## The sharp edge: availability, not theft

**Swap records are unencrypted in `localStorage`.** Any script on the origin can
read them, and "clear site data" destroys them with no warning. Losing one does
not hand anybody your coins — it loses that contract's Bitcoin key and the
deadlines you were watching. So a Bitcoin refund is **pre-signed the moment
funding is seen**, the **Recover page** rebuilds a spend from that file offline
with no stored swap, and **Export** takes every swap as one document.

Passphrase encryption at rest is the obvious next step and is not done.

## What a node can do to you

Every answer comes from a node **you** chose, and the defaults reflect exposure:

- **Zenon has no default** — verifying a counterparty's HTLC is the one check
  where a dishonest answer costs money and there is no second opinion.
- **Solana falls back to a public endpoint on the test networks only**; an
  escrow is checked against an address this page derives itself, so a lying node
  is caught by the derivation. **Mainnet has no fallback**, and that is not the
  trust argument: `api.mainnet-beta.solana.com` answers a request carrying a
  browser Origin with HTTP 403, so it cannot serve this app at all.
- **Bitcoin falls back to a public Esplora**, which can hide or invent a
  funding; the contract is parsed locally and every spend goes through the
  script engine before broadcast.

**A check that could not be run is refused, never reported as passed.** A
skipped check reads exactly like a passed one.

## What a wallet can do to you

It may sign with a **different account** than it reported (`diffSignedBlock`
compares published against proposed), return a **different transaction** than
the one it was asked to sign (compared byte for byte, mismatch aborts), or
**sign and send** through its own cluster rather than your node (usable, said in
red).

## What the board proves

A post is signed by a key your browser holds: attributable and tamper-evident,
so only you can edit or withdraw it. That does not make its claim true. A wallet
proof shows somebody **holds an address** and nothing about conduct, and "swaps
completed" is typed by the person claiming it.

## Sessions

Ciphertext under a pseudonymous key, both derived from a code that never leaves
the two browsers. Treat the code like a password; use a fresh one per swap.
Every arriving value goes through the check it would get if pasted by hand.

The preimage is the exception: there is no field for it and no path that would
send or accept one. Early, it hands the other side both legs; later, it is
already on a chain.

**No adversarial review.** The feature is optional.

## Known gaps

1. Records are not encrypted at rest.
2. No RBF or fee bumping; the Recover page is the manual answer.
3. Timeout paths are only partly tested — no run has waited out a real expiry.
4. Sessions have had no adversarial review.
5. Nothing has been used with real money on any mainnet. A full BTC⇄ZTS swap has
   settled on regtest + devnet.

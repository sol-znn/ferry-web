# Wallets

**Ferry produces bytes, a wallet produces signatures.** No key for any chain
reaches this page.

| chain | wallet | asked for | never asked for |
| --- | --- | --- | --- |
| Bitcoin | UniSat | an address; an ordinary send; a signed message | anything about the contract |
| Zenon | [Syrius extension][syrius] | an address; a signature over an account block; a signed message | a key |
| Solana | Phantom / Solflare / Backpack | an address; a signature over a transaction; a signed message | a key |

## Bitcoin

Funding a P2SH contract is an ordinary payment, so any wallet works; UniSat just
saves pasting an address and an amount. Spending *out* needs a witness almost no
wallet builds, so Ferry generates an **ephemeral keypair per swap** — it can
only move coins inside that one contract, and both branches pay an address you
supplied.

Board proofs use `signMessage(msg, "ecdsa")`, not BIP-322: Go recovers the key
and checks it writes the claimed address in every form. A signed message
contains no chain, so the comparison is not scoped to one network.

## Zenon

Syrius's own HTLC screens hardcode SHA3-256 and cap expiry at 24h, so they
cannot do a cross-chain leg and are not used. The extension will publish **any
account block a page hands it**, and an HTLC call is a send to an embedded
contract carrying ABI-encoded arguments:

```
this page       decides toAddress, amount, tokenStandard and data
the extension   supplies the account, key, chain position, plasma and signature
```

Go builds the block and says in a sentence what it does, with nothing signed;
then the extension shows the raw block and signs. Three checks surround it:

1. **Before** — the wallet's chain identifier *and* a momentum hash from its own
   node are compared with this browser's. Equal chain identifiers prove nothing:
   every go-zenon devnet is chain 69. A check that could not run blocks, because
   an unreachable wallet node looks exactly like a wallet on another chain.
2. **While building** — the account is checked against the swap. A `create`
   makes the signer `timeLocked`, the only address that can ever reclaim.
3. **After** — `diffSignedBlock` compares what was published against what was
   proposed.

`htlc.Unlock` may be called by anyone and pays the address in the entry, so the
counterparty can settle your leg — unless that address denied proxy unlock,
checked at creation rather than at settlement. `znn-cli` commands are printed
beside every button; note its cap of whole hours, 1..24.

## Solana

Go returns program id, accounts and data; the page assembles the transaction,
the wallet signs, the page submits through the endpoint in **Nodes**.
`signAndSendTransaction` would submit through whichever cluster the *extension*
is on, which against a local validator is never the same chain — so
`signTransaction` is preferred, and what comes back is compared byte for byte
before broadcast. A sign-and-send-only wallet is usable and the page says so in
red.

**Neither exit needs a signature from the party being paid** — `redeem` is
authorised by the preimage, `refund` by an expired clock. `create` is the
exception: the initiator is the only signer and the refund destination, so a
mismatched account is refused rather than adopted.

## Board proofs

One signature per chain, once, over a statement naming your board key and the
address. It authorises nothing.

| scheme | signature | what Go checks |
| --- | --- | --- |
| `btc-ecdsa` | base64, 65 bytes | recover the key, derive every address form, compare |
| `znn-ed25519` | hex, with the 32-byte key | verify, then check the address that key spends from |
| `sol-ed25519` | hex | verify against the address — a Solana address **is** the key |

## Connectors in the page

Each wallet connects where it is used — beside the payment, beside the block
being signed, and from the board's Prove buttons. None sits permanently in the
header: a wallet asked for before there is anything to do with it reads as a
requirement the app does not have.

Every connect button is offered whether or not an extension was detected.
Detection races a content script, and hiding the control on losing that race
tells people they have nothing installed while it sits in their toolbar.

To drive all three without an extension, see the CLI wallet in
[TESTING.md](TESTING.md).

[syrius]: https://github.com/sol-znn/syrius-extension/releases

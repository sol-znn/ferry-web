package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"

	"github.com/zenon/ferry-web/wasm/znn"
)

// The board: offers, in public, with nobody hosting them.
//
// A session (session.go) solves the problem of two people who have already
// agreed to trade. This solves the one before it -- finding somebody at all --
// and it is a different problem with a different threat model, because the
// content is public by construction. A session is a sealed room; a board is a
// notice pinned in a square.
//
// Everything follows from that:
//
//   - Nothing is encrypted. A price nobody can read is not an offer. The one
//     exception is a TAKE, which carries a session code and is sealed to the
//     poster's key -- see SealTake.
//   - Nothing is trusted. A post is a stranger's claim about what they will do,
//     and this file's entire contribution is making the claim ATTRIBUTABLE:
//     signed by a key, optionally bound to a wallet address, and impossible for
//     anyone else to edit or withdraw. It does not make the claim true.
//   - Nothing is hosted. The list is whatever the relays hand back. There is no
//     index to keep, no operator to remove a post, and no way for this app to
//     rank one above another.
//
// The two properties this needed -- tamper-proof, and revocable by the author at
// any time -- are the same property, and Nostr gives it in one move: an
// ADDRESSABLE event (kind 30000..39999 carrying a `d` tag). A relay keeps exactly
// one event per (pubkey, kind, d) and replaces it only on a newer, validly signed
// one. So editing a post is republishing it, revoking it is republishing it as
// void, and neither is something a third party can do -- they would have to forge
// a schnorr signature over the author's key.

// boardKind is the addressable kind board posts live under. In the addressable
// range on purpose: it is what makes a relay treat a re-post as an EDIT of the
// one it holds rather than as a second offer, which is the whole of edit and
// revoke. 30777 echoes the session's 9797 and collides with no NIP.
const boardKind = 30777

// takeKind is a taker saying "I want this one, here is a room". Regular
// (1000..9999) rather than addressable or ephemeral: a relay must STORE it, or a
// take sent while the poster's tab was closed would never be seen -- and it must
// not replace the previous one, because two people may take the same post.
const takeKind = 9778

// deleteKind is NIP-09. Published alongside a withdrawal for the relays that
// honour it; the void replacement is what covers the ones that do not.
const deleteKind = 5

// presenceKind is "the key that wrote those posts is at a keyboard right now".
//
// ADDRESSABLE, like a post, and that is the whole design decision. Nostr's other
// obvious home for presence is the ephemeral range (20000..29999), which relays
// forward and never store -- the textbook shape for a heartbeat, and wrong here.
// A reader opening the board asks relays for a week of posts and gets them
// immediately; if presence were ephemeral they would get NOTHING with it, and
// every row would sit at "unknown" until each author's next beat happened to
// land. The board would take a minute to tell you anything, every single load.
//
// One addressable slot per key gives the opposite: relays hold exactly the
// latest beat, hand it over in the same backfill as the posts, and replace it on
// every beat after. The freshness question then answers itself from a timestamp
// the reader already has -- see PresenceStale.
//
// 30778 sits beside boardKind's 30777 and collides with no NIP.
const presenceKind = 30778

// presenceSlot is the `d` tag every beat carries. One slot per key: "is this
// person here" has one answer, so a new beat REPLACES the last rather than
// accumulating, which is what keeps this to one stored event per author forever.
const presenceSlot = "presence"

// How presence is timed. All three are the reader's, not the author's, and that
// is deliberate: a beat carries no interval of its own, because an author who
// could declare their own staleness window could declare themselves permanently
// online. What a beat asserts is one fact -- this key signed something at this
// time -- and every judgement made from it is made here.
const (
	// PresenceBeat is how often a browser showing the board republishes.
	//
	// Twenty seconds is chattier than a courtesy signal usually deserves, and it
	// is bounded by the two conditions on beating at all: a live post, and a tab
	// somebody is actually looking at. Outside those the traffic is zero, and
	// inside them it is one small event that replaces the last rather than
	// accumulating — relays store exactly one per key however long the tab stays
	// open.
	PresenceBeat = 20 * time.Second

	// PresenceStale is how long a beat still means "online", and therefore the
	// longest a closed browser can keep a green dot. Three beats: a minute.
	//
	// This was three minutes, and three minutes of showing somebody as present
	// after they closed the tab is a lie told at the exact moment it costs
	// something: the dot exists to answer "will they see my take now", and the
	// person reading it acts on the answer.
	//
	// It was that long because of timer throttling. A backgrounded tab's
	// setInterval is throttled — Chrome to roughly one firing a minute — so a
	// window that had to keep hidden tabs green had to tolerate a cadence of a
	// minute whatever the beat was set to, which put the floor at two of those
	// plus slack. Shortening the beat alone could not move it.
	//
	// So the page stopped beating while hidden instead (see the heartbeat in
	// useBoard.ts). Nothing then has to survive a throttled timer, the window is
	// three beats of a tab that is genuinely being looked at, and the lapse is
	// deterministic rather than flapping between green and grey on whether a
	// throttled firing landed inside the window.
	//
	// Three beats rather than two, still: one dropped publish to a blinking
	// relay leaves two more chances inside the window, so an ordinary hiccup
	// does not blink the dot on somebody who is sitting right there.
	//
	// What it now claims is narrower and truer: the board is in front of them.
	PresenceStale = 3 * PresenceBeat

	// PresenceSkew is how far ahead of the reader's clock a beat may be stamped
	// before it is refused outright.
	//
	// Beyond it, refused rather than clamped: `created_at` is written by its
	// author, so a browser whose clock is a day fast would otherwise publish a
	// beat that stays "recent" for the next twenty-four hours. Refusing shows
	// such a key as offline — wrong, but wrong in the direction that costs
	// nobody money, and self-correcting the moment the clock is.
	//
	// WITHIN it, clamped to the reader's own clock rather than believed. A beat
	// stamped a minute ahead by an ordinary un-synced machine is a beat that
	// would otherwise stay inside the window a minute longer than it should,
	// silently adding the skew to PresenceStale and undoing the tightening
	// above. Clamping keeps the window meaning exactly what it says while still
	// tolerating the drift.
	PresenceSkew = 90 * time.Second

	// PresenceTTL is the NIP-40 expiration a beat asks for. Longer than
	// PresenceStale so a relay that honours expiry does not drop a beat this app
	// would still have shown, and short enough that an abandoned key stops
	// costing relays anything within the hour.
	PresenceTTL = 10 * time.Minute
)

// boardTag is the value of the `t` tag every post and take carries, and the
// only thing a reader needs to know to find the board. Single-letter tags are
// the ones relays index -- a filter on anything else is a filter the relay
// answers by scanning, or refuses outright.
//
// Scoped to the instance, the same way StorageKeyPrefix scopes browser storage
// and for the same reason: the `n` tag already carries the network a post
// names, but the network is a setting a user can change, including on a
// development build -- point dev at "mainnet" for a moment and its test posts
// would otherwise land in the same public index as real offers, on the very
// relays that carry both. A tag a build cannot be talked into changing is what
// keeps the two boards apart regardless of what network either side is pointed
// at.
func boardTag() string {
	if IsDev() {
		return "ferry-board-v1-dev"
	}
	return "ferry-board-v1"
}

// The bounds a post's life is chosen within. These are the same on both
// instances: the floor is what keeps a withdrawal readable long enough to be
// seen, and the ceiling is how far back a reader asks relays to look.
const (
	MinPostTTL = 10 * time.Minute
	MaxPostTTL = 7 * 24 * time.Hour
)

// DefaultPostTTL is how long a post lives when its author does not say.
//
// On production, a day: that is roughly the life of the thing being advertised
// -- the participant's leg of a swap runs 24 hours, prices move, and a board of
// week-old offers is a board where every third entry is dead. Short enough that
// abandonment is self-correcting, long enough to sleep through.
//
// On the development instance, fifteen minutes, because the thing being tested
// there is not a market. A test post is made to watch one thing happen and is
// then abandoned within the minute, and only the browser that made it can
// withdraw it -- so on a day-long default every abandoned experiment sits on
// public relays until tomorrow, and the next run opens onto a board of
// yesterday's rubbish. Fifteen minutes is long enough to take an offer and
// drive a swap through it, and short enough that a forgotten one is gone before
// anybody looks again.
func DefaultPostTTL() time.Duration {
	if IsDev() {
		return 15 * time.Minute
	}
	return 24 * time.Hour
}

// Key separation, for the reason session.go gives. The board key signs in public
// and derives shared secrets with strangers; deriving both from one hash would
// tie the two together for no gain.
const (
	boardECDHLabel = "ferry-board-ecdh-v1"
	bindLabel      = "ferry-board-bind-v1"
)

// Post statuses, in the order a post moves through them.
const (
	// StatusOpen is the only status the board shows by default.
	StatusOpen = "open"
	// StatusTaken means the author has accepted somebody and is mid-swap. Kept
	// visible rather than withdrawn, so a taker whose take was not the one
	// accepted learns why instead of watching a post vanish.
	StatusTaken = "taken"
	// StatusDone is a completed swap. Published once, and expires shortly after.
	StatusDone = "done"
	// StatusVoid is a withdrawal.
	StatusVoid = "void"
)

func knownStatus(s string) bool {
	switch s {
	case StatusOpen, StatusTaken, StatusDone, StatusVoid:
		return true
	}
	return false
}

// ---------- identity ----------

// BoardIdentity is the key a person posts under, and what it has been bound to.
//
// A key of this app's own rather than the wallet's, and that is not a compromise
// -- it is the only shape that works. A wallet signature costs a popup and a
// decision; a post, an edit, a renewal and a withdrawal are four of them for one
// offer, and a board where every keystroke is a wallet prompt is a board nobody
// edits. So the wallet signs ONCE, over this key, and this key signs everything
// after. That is a delegation, it is stated in the signed statement, and anyone
// holding the post can check it.
//
// The private key never leaves the browser and is worth little if it does: it
// cannot spend, cannot sign a swap, and cannot claim an address that has not
// separately been bound to it. Losing it costs the ability to edit posts already
// out there, which expire within the day in any case.
type BoardIdentity struct {
	// Priv is a 32-byte secp256k1 scalar.
	Priv []byte `json:"priv"`
	// PubKey is the x-only public key, hex -- the form Nostr uses everywhere.
	PubKey string `json:"pubKey"`
	// Bindings are the wallet addresses proven to belong to whoever holds this
	// key. At most one per scheme: "which Bitcoin address is this person" has
	// one answer, so a second binding replaces the first.
	Bindings []WalletProof `json:"bindings,omitempty"`
	// CreatedAt is when this browser minted the key.
	CreatedAt time.Time `json:"createdAt"`
}

// NewBoardIdentity mints a key.
func NewBoardIdentity() (*BoardIdentity, error) {
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("generate board key: %w", err)
	}
	return &BoardIdentity{
		Priv:      priv.Serialize(),
		PubKey:    hex.EncodeToString(schnorr.SerializePubKey(priv.PubKey())),
		CreatedAt: time.Now().UTC(),
	}, nil
}

// PrivKey rehydrates the signing key.
func (b *BoardIdentity) PrivKey() (*btcec.PrivateKey, error) {
	if len(b.Priv) != 32 {
		return nil, fmt.Errorf("board key is %d bytes, want 32", len(b.Priv))
	}
	priv, _ := btcec.PrivKeyFromBytes(b.Priv)
	if priv == nil {
		return nil, errors.New("board key is not a usable secp256k1 scalar")
	}
	return priv, nil
}

// Binding returns the proof for one scheme, if there is one.
func (b *BoardIdentity) Binding(scheme string) *WalletProof {
	for i := range b.Bindings {
		if b.Bindings[i].Scheme == scheme {
			return &b.Bindings[i]
		}
	}
	return nil
}

// Bind records a verified proof, replacing any earlier one for the same scheme.
// Whether a proof is good depends on the network, and an identity does not know
// one, so the verification is the caller's -- see VerifyProof.
func (b *BoardIdentity) Bind(p WalletProof) {
	for i := range b.Bindings {
		if b.Bindings[i].Scheme == p.Scheme {
			b.Bindings[i] = p
			return
		}
	}
	b.Bindings = append(b.Bindings, p)
}

// Unbind drops a scheme's proof. Posts already published keep the copy they
// carry -- they are signed, and rewriting history is precisely what this design
// prevents -- so this affects the next post and nothing before it.
func (b *BoardIdentity) Unbind(scheme string) {
	kept := make([]WalletProof, 0, len(b.Bindings))
	for _, p := range b.Bindings {
		if p.Scheme != scheme {
			kept = append(kept, p)
		}
	}
	b.Bindings = kept
}

// ---------- wallet proofs ----------

// The two schemes a wallet can prove an address with.
const (
	// ProofBTC is a Bitcoin signed message: the format `bitcoin-cli signmessage`
	// produces and every wallet has spoken for a decade. UniSat calls it
	// `signMessage(msg, "ecdsa")`.
	ProofBTC = "btc-ecdsa"
	// ProofZNN is a raw ed25519 signature over the same statement.
	//
	// Produced by the Syrius extension's `signMessage`, which answers the
	// `znn_sign` request desktop Syrius already serves over WalletConnect. An
	// extension build without it cannot make one, and since the board now
	// requires a proof before anybody may post or take, such a build cannot
	// trade a Zenon leg at all -- the button says so rather than failing later.
	//
	// What is assumed of the extension, and all that has to hold: it signs the
	// statement's raw UTF-8 bytes with the account's ed25519 key, and returns
	// that signature alongside the 32-byte public key. If it hashes or prefixes
	// first, verifyZNNProof is the one function that changes.
	ProofZNN = "znn-ed25519"
)

// WalletProof is one address, proven to belong to whoever holds a board key.
type WalletProof struct {
	Scheme string `json:"scheme"`
	// Address is the address being claimed, in the form its chain writes it.
	Address string `json:"address"`
	// PubKey is the 32-byte ed25519 key, hex. Required for ProofZNN and absent
	// for ProofBTC: a Bitcoin compact signature carries its own key recoverably,
	// and an ed25519 signature does not.
	PubKey string `json:"pubKey,omitempty"`
	// Sig is base64 for ProofBTC -- the encoding every wallet returns -- and hex
	// for ProofZNN, which has no convention of its own and so matches the rest
	// of this app.
	Sig string `json:"sig"`
	// VerifiedAt is when THIS browser checked it, and it is local bookkeeping
	// rather than part of the claim. A proof arriving off a relay is re-verified
	// rather than believed, so the field is cleared on the way out -- see
	// (*BoardPost).forWire.
	VerifiedAt time.Time `json:"verifiedAt,omitempty"`
}

// BindStatement is what a wallet signs to bind an address to a board key.
//
// Both halves are in it, and both have to be. The key alone would let a signature
// made for one address be replayed as a claim about another the same wallet
// holds; the address alone would let a signature be lifted off one person's post
// and pasted onto somebody else's key. Naming both makes it a statement about
// this pair and no other.
//
// Fixed text, one field per line, no JSON. A human reads it in a wallet popup,
// and a wallet popup is not a place to render a document.
func BindStatement(boardPubKey, address string) string {
	return strings.Join([]string{
		bindLabel,
		"key: " + boardPubKey,
		"address: " + address,
		"",
		"Signing this lets the key above post trade offers as you on the Ferry board.",
		"It authorises no payment and moves nothing.",
	}, "\n")
}

// VerifyProof checks that a proof really binds its address to a board key.
//
// `network` is which chain the reader is on. For Bitcoin it decides only which
// address encodings are tried FIRST, never which are allowed -- see
// verifyBTCProof.
func VerifyProof(p WalletProof, boardPubKey, network string) error {
	if strings.TrimSpace(p.Address) == "" {
		return errors.New("a wallet proof has to name an address")
	}
	statement := BindStatement(boardPubKey, strings.TrimSpace(p.Address))
	switch p.Scheme {
	case ProofBTC:
		return verifyBTCProof(p, statement, network)
	case ProofZNN:
		return verifyZNNProof(p, statement)
	default:
		return fmt.Errorf("unknown wallet proof scheme %q", p.Scheme)
	}
}

// bitcoinMessageMagic is the prefix that makes a signed message unmistakably a
// message and not a transaction. Without it a wallet could be talked into signing
// something that is also a valid sighash.
const bitcoinMessageMagic = "Bitcoin Signed Message:\n"

// magicHash is what a Bitcoin signed message is actually signed over: both
// strings length-prefixed as varstrings, then double-SHA256.
func magicHash(message string) ([]byte, error) {
	var buf strings.Builder
	if err := wire.WriteVarString(&buf, 0, bitcoinMessageMagic); err != nil {
		return nil, err
	}
	if err := wire.WriteVarString(&buf, 0, message); err != nil {
		return nil, err
	}
	return chainhash.DoubleHashB([]byte(buf.String())), nil
}

// verifyBTCProof recovers the signing key and checks the claimed address is one
// that key writes.
//
// Recovery, then derivation, then comparison -- rather than asking the wallet
// which key it used. A compact signature carries its own public key by
// construction, so there is nothing here to take on trust: either the recovered
// key produces the claimed address, or the proof is somebody else's.
//
// Every address form the key could write is tried, because a wallet signs with
// the key behind whichever address is selected and never says which FORM that
// address takes. UniSat defaults to taproot; older setups are native segwit;
// hardware imports are often wrapped segwit or legacy. Checking one of the four
// would refuse most wallets for no reason.
//
// And every NETWORK's encoding of those forms is tried, not just the one the
// reader is on. A signed message is a signature over a hash by a key: no chain
// appears in it anywhere, and the same key holds the same coins on every network
// its address is written for. Only the ENCODING differs -- bc1/tb1/bcrt1, and
// three base58 version bytes -- so scoping the comparison to one network refused
// proofs that were simply true.
//
// It also made the feature unusable where it is most used. UniSat has no regtest
// chain at all (see UNISAT_CHAIN in ui/src/core/unisat.ts), so a developer on the
// regtest instance cannot put their wallet on the network this page is set to,
// and the only address they can possibly sign with is a mainnet one.
//
// Nothing is lost by accepting it. The statement names the board key AND the
// address, so a proof still cannot be lifted onto another key or another
// address; all this widens is which spelling of the same address counts as that
// address. What the badge claims -- that the author holds it -- is exactly as
// true on one chain as another.
func verifyBTCProof(p WalletProof, statement, network string) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(p.Sig))
	if err != nil {
		return fmt.Errorf("that signature is not base64: %w", err)
	}
	// 65 bytes: one recovery byte and two 32-byte scalars. Checked here so a
	// wrong-shaped signature is reported as one, rather than coming back out of
	// the library as something less legible.
	if len(raw) != 65 {
		return fmt.Errorf("a Bitcoin signed message is 65 bytes and this one is %d — if the "+
			"wallet was asked for a BIP-322 signature, ask it for an \"ecdsa\" one instead",
			len(raw))
	}
	hash, err := magicHash(statement)
	if err != nil {
		return err
	}
	pub, compressed, err := ecdsa.RecoverCompact(raw, hash)
	if err != nil {
		return fmt.Errorf("that signature does not verify against the statement it should "+
			"have signed: %w", err)
	}

	serialized := pub.SerializeCompressed()
	if !compressed {
		serialized = pub.SerializeUncompressed()
	}

	want := strings.TrimSpace(p.Address)
	// The reader's own network first, so the error names what the key holds
	// HERE when nothing matches anywhere — that is the spelling somebody
	// comparing by eye is looking at.
	var here []string
	for _, net := range networksForProof(network) {
		params, err := NetworkParams(net)
		if err != nil {
			continue
		}
		candidates, err := addressForms(pub, serialized, params)
		if err != nil {
			return err
		}
		if net == network {
			here = candidates
		}
		for _, got := range candidates {
			if got == want {
				return nil
			}
		}
	}
	if here == nil {
		here = []string{"nothing this build can encode"}
	}
	return fmt.Errorf("that signature is valid, but the key that made it does not hold %s on "+
		"any network — on %s it holds %s. A wallet signs with whichever address it has "+
		"selected, so this is usually the wrong account rather than a bad signature",
		want, network, strings.Join(here, ", "))
}

// networksForProof is every network an address could be written for, with the
// reader's own first so a failure can name what the key holds where they are
// looking. Mirrors the cases NetworkParams accepts.
func networksForProof(network string) []string {
	all := []string{"mainnet", "testnet", "signet", "regtest"}
	out := make([]string, 0, len(all)+1)
	if network != "" {
		out = append(out, network)
	}
	for _, n := range all {
		if n != network {
			out = append(out, n)
		}
	}
	return out
}

// addressForms returns every address the recovered key writes on this network.
func addressForms(pub *btcec.PublicKey, serialized []byte, params *chaincfg.Params) ([]string, error) {
	var out []string

	pkh := btcutil.Hash160(serialized)
	legacy, err := btcutil.NewAddressPubKeyHash(pkh, params)
	if err != nil {
		return nil, err
	}
	out = append(out, legacy.EncodeAddress())

	// The witness forms exist only for a compressed key: a segwit output
	// committing to an uncompressed one is unspendable, so no wallet writes one.
	if len(serialized) == 33 {
		segwit, err := btcutil.NewAddressWitnessPubKeyHash(pkh, params)
		if err != nil {
			return nil, err
		}
		out = append(out, segwit.EncodeAddress())

		// P2SH-wrapped segwit: the redeem script is the witness program itself.
		redeem, err := txscript.PayToAddrScript(segwit)
		if err != nil {
			return nil, err
		}
		wrapped, err := btcutil.NewAddressScriptHash(redeem, params)
		if err != nil {
			return nil, err
		}
		out = append(out, wrapped.EncodeAddress())

		// Taproot, BIP-86: the key tweaked by the hash of itself, no script
		// path. The untweaked key writes a different address, and no wallet
		// derives that one.
		tweaked := txscript.ComputeTaprootKeyNoScript(pub)
		taproot, err := btcutil.NewAddressTaproot(schnorr.SerializePubKey(tweaked), params)
		if err != nil {
			return nil, err
		}
		out = append(out, taproot.EncodeAddress())
	}
	return out, nil
}

// verifyZNNProof checks an ed25519 signature, and that its key holds the address.
//
// Unreachable until the Syrius extension can sign a message -- see ProofZNN. It
// is written now because the format has to be settled before anything publishes
// under it.
func verifyZNNProof(p WalletProof, statement string) error {
	pub, err := hex.DecodeString(strings.TrimSpace(p.PubKey))
	if err != nil {
		return fmt.Errorf("that proof's public key is not hex: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("a Zenon public key is %d bytes and this one is %d",
			ed25519.PublicKeySize, len(pub))
	}
	sig, err := hex.DecodeString(strings.TrimSpace(p.Sig))
	if err != nil {
		return fmt.Errorf("that signature is not hex: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("an ed25519 signature is %d bytes and this one is %d",
			ed25519.SignatureSize, len(sig))
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), []byte(statement), sig) {
		return errors.New("that signature does not verify against the statement it should " +
			"have signed")
	}
	// The signature proves a KEY signed. This is what makes it prove a PERSON
	// did: the address in the post has to be the one that key spends from.
	return znn.AddressOwnedBy(strings.TrimSpace(p.Address), ed25519.PublicKey(pub))
}

package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// Two browsers agreeing on a swap, without a server in the middle.
//
// Setting a swap up by hand is four hand-offs of hex -- a pubkey hash, a
// contract, a hashlock, an HTLC id -- each pasted into a chat window and back
// out. Every one is an opportunity to paste the wrong thing, and one of them
// (the contract) is a value an attacker would like you to accept without
// looking at.
//
// So: a session. One party makes a code, the other types it in, and the values
// travel automatically. What makes that safe is that nothing arriving over a
// session is trusted -- every message is applied through the same handler the
// paste box used, all of which verify against what was already agreed. The
// session removes the typing, not the checking.
//
// The transport is Nostr, because it is the only free, redundant, no-signup
// relay network a static page can use. Relays are named in settings exactly like
// the Esplora and Zenon nodes, because it is the same decision.
//
// A relay operator sees a stream of ciphertext under a pseudonymous key. The
// code never leaves the two browsers: it seeds both the identity the messages
// are published under and the key they are encrypted with, so a relay can serve
// a conversation it cannot read and cannot join.

// sessionKind is the Nostr event kind these messages use. It sits in the
// regular range (1000..9999), so relays store and replay it: a party who joins
// or reloads five minutes late must still receive what was sent before they
// arrived, which an ephemeral kind would not give them.
const sessionKind = 9797

// SessionCodeBytes is the entropy in a session code. 16 bytes is well past what
// an online guessing attack against a room that exists for an hour could reach,
// and it fits in a code short enough to read down a phone line.
const SessionCodeBytes = 16

// sessionCodeAlphabet is Crockford base32 without I, L, O and U: no character
// pair in it can be confused when read aloud or written down, which is how
// these codes actually travel.
const sessionCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// Key separation. The code is one secret used for two unrelated purposes, and
// deriving both from it with the same hash would make the publishing identity
// and the encryption key the same value — so anyone who learned the room's
// public key could decrypt it. These labels are what keep them independent.
const (
	sessionIdentityLabel = "ferry-session-identity-v1"
	sessionEncryptLabel  = "ferry-session-encrypt-v1"
	sessionRoomLabel     = "ferry-session-room-v1"
)

// SessionKeys is everything one code derives.
type SessionKeys struct {
	// Code is the shared secret, in the form the user reads out.
	Code string `json:"code"`
	// PubKey is the x-only Nostr public key both sides publish under. Both
	// parties hold the same private key: the room is the identity. That is not
	// a weakness here, because the only thing the key authenticates is
	// membership of a conversation whose entire content is already encrypted to
	// the same secret.
	PubKey string `json:"pubKey"`
	// RoomID is the value carried in the event's `d` tag and used as the
	// subscription filter. It is derived separately from the key so that
	// knowing what to subscribe to does not reveal the identity to sign with.
	RoomID string `json:"roomId"`
}

// sessionSecrets is the private half, never returned across the API boundary.
type sessionSecrets struct {
	priv *btcec.PrivateKey
	enc  [32]byte
}

// NewSessionCode returns a fresh code in the grouped form users read aloud.
func NewSessionCode() (string, error) {
	raw := make([]byte, SessionCodeBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	var b strings.Builder
	for i, v := range raw {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(sessionCodeAlphabet[int(v)%len(sessionCodeAlphabet)])
		b.WriteByte(sessionCodeAlphabet[int(v>>3)%len(sessionCodeAlphabet)])
	}
	return b.String(), nil
}

// NormalizeSessionCode is what makes a code typed by a human the same code the
// other side generated: case, spaces and dashes carry no information, and a
// code that fails because it was typed in lower case is a code that fails for
// no reason the user can see.
func NormalizeSessionCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(code)) {
		if strings.ContainsRune(sessionCodeAlphabet, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// DeriveSession turns a code into the identity and room both sides share.
func DeriveSession(code string) (*SessionKeys, *sessionSecrets, error) {
	norm := NormalizeSessionCode(code)
	// Short enough to guess is short enough to refuse. A code with less entropy
	// than this is one somebody typed half of, and accepting it would open a
	// room a third party could stumble into.
	if len(norm) < 16 {
		return nil, nil, errors.New("that session code is too short to be one of ours — " +
			"they are 32 characters, usually written in groups of eight")
	}

	identity := sha256.Sum256([]byte(sessionIdentityLabel + ":" + norm))
	priv, pub := btcec.PrivKeyFromBytes(identity[:])
	if priv == nil {
		return nil, nil, errors.New("that code does not derive a usable key")
	}
	room := sha256.Sum256([]byte(sessionRoomLabel + ":" + norm))
	enc := sha256.Sum256([]byte(sessionEncryptLabel + ":" + norm))

	return &SessionKeys{
		Code:   norm,
		PubKey: hex.EncodeToString(schnorr.SerializePubKey(pub)),
		RoomID: hex.EncodeToString(room[:16]),
	}, &sessionSecrets{
		priv: priv,
		enc:  enc,
	}, nil
}

// NostrEvent is the wire form of one message. The field names are the
// protocol's, not this app's.
type NostrEvent struct {
	ID        string     `json:"id"`
	PubKey    string     `json:"pubkey"`
	CreatedAt int64      `json:"created_at"`
	Kind      int        `json:"kind"`
	Tags      [][]string `json:"tags"`
	Content   string     `json:"content"`
	Sig       string     `json:"sig"`
}

// eventID is the protocol's canonical hash: the SHA-256 of a compact JSON array
// of exactly these six fields in exactly this order. Written out by hand rather
// than assembled from the struct because the ordering is consensus.
//
// SetEscapeHTML(false) is not a style choice: encoding/json escapes <, > and &
// by default, which no other implementation of this protocol does, so a message
// containing any of them would hash to an id every relay computes differently
// and therefore rejects. Encode also appends a newline, which is not part of the
// document.
func (e *NostrEvent) eventID() ([32]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode([]any{0, e.PubKey, e.CreatedAt, e.Kind, e.Tags, e.Content}); err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// SessionMessage is what one side actually has to say. Every field is public
// swap data: a pubkey hash, a contract, an HTLC id all end up on a chain anyway.
//
// "hello" carries nothing but a PeerID, so each side can see somebody else is in
// the room. "ping" and "bye" carry the same: a relay has no concept of a closed
// connection, so the heartbeat makes silence measurable and "bye" lets a
// deliberate departure be announced rather than inferred a keepalive later.
//
// The preimage is deliberately absent, and its absence is the security property
// that makes this whole feature safe. Revealing it early hands the counterparty
// both legs, so there is no field for it, no code path that would send one, and
// nothing on the other side that would accept one.
type SessionMessage struct {
	// Type is "hello", "ping", "bye", "offer", "addresses", "pkh", "contract",
	// "zenon", "solana" or "funded".
	Type string `json:"type"`
	// Leg names the chain this hand-off is about, so a receiver applies it to
	// the right half of the trade rather than to whichever half it guessed.
	// It matters most where it looks redundant: a ZTS-against-ZTS swap has two
	// Zenon legs, and an HTLC id applied to the wrong one is an entry checked
	// against the wrong terms.
	Leg string `json:"leg,omitempty"`
	// From is "initiator" or "participant", so each side can ignore its own
	// messages coming back off the relay.
	From string `json:"from"`
	// PeerID identifies the browser that sent this, which is what makes presence
	// possible at all: a relay has no notion of who is in a room. Random per
	// session and meaningless outside it -- it is not an identity, it is a way of
	// telling their message from my own coming back off the relay.
	PeerID string `json:"peerId,omitempty"`

	// Chain is the sender's full claim about which chains they are on: the network
	// name, both tips, and the hash of a block on each the other side should
	// already have. The hashes are what make it evidence rather than assertion --
	// see chainid.go.
	//
	// The network name alone settles nothing: everybody's regtest is their own,
	// and two people on mainnet can still disagree if one is pointed at a node
	// that stopped syncing last week.
	Chain *ChainFingerprint `json:"chain,omitempty"`
	// Offer is a swapoffer2 string, carrying the agreed terms.
	Offer string `json:"offer,omitempty"`
	// PkhHex is the sender's Bitcoin pubkey hash.
	PkhHex string `json:"pkhHex,omitempty"`
	// ContractHex is the funded contract, for the other side to audit.
	ContractHex string `json:"contractHex,omitempty"`
	// HtlcID and ZenonAddr describe the Zenon leg the sender created.
	//
	// ZenonAddr does double duty: on a "zenon" message it is where the sender's
	// HTLC pays, and on an "addresses" message it is simply where they want
	// their Zenon paid, sent before any HTLC exists. The board uses the second
	// when a take is accepted, so the taker learns the author's current address
	// rather than the one their post was published with — the two differ
	// whenever a wallet was switched in between.
	HtlcID    string `json:"htlcId,omitempty"`
	ZenonAddr string `json:"zenonAddr,omitempty"`
	// BtcAddr is where the sender wants their Bitcoin. Carried only on an
	// "addresses" message: everywhere else the Bitcoin side is identified by a
	// pubkey hash, which is what a contract commits to, and an address would be
	// a second way of saying a thing that already has one.
	BtcAddr string `json:"btcAddr,omitempty"`
	// SolAddr is where the sender wants their SOL, and on a "solana" message it
	// is also the account their escrow was funded from. SolProgram and SolSwapID
	// are the two values that decide WHICH account holds the money: both sides
	// derive the escrow from them, so a mismatch is two people watching two
	// different addresses and each reporting the other's leg as unfunded.
	SolAddr    string `json:"solAddr,omitempty"`
	SolProgram string `json:"solProgram,omitempty"`
	SolSwapID  string `json:"solSwapId,omitempty"`
	// FundingTxID is informational: it lets the other side stop refreshing.
	FundingTxID string `json:"fundingTxid,omitempty"`
	// Note is free text the sender typed. Shown as text and never parsed.
	Note string `json:"note,omitempty"`
	// SentAt is the sender's clock, for ordering the transcript.
	SentAt int64 `json:"sentAt,omitempty"`
}

// SealSession encrypts a message and returns the signed event to publish.
func SealSession(code string, msg SessionMessage) (*NostrEvent, error) {
	keys, secrets, err := DeriveSession(code)
	if err != nil {
		return nil, err
	}
	if msg.SentAt == 0 {
		msg.SentAt = time.Now().Unix()
	}
	plain, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	content, err := sealBox(secrets.enc, plain)
	if err != nil {
		return nil, err
	}

	ev := &NostrEvent{
		PubKey:    keys.PubKey,
		CreatedAt: time.Now().Unix(),
		Kind:      sessionKind,
		// A single-letter tag, because those are the ones relays index. `d` is
		// the room, and it is what the other side filters on.
		Tags:    [][]string{{"d", keys.RoomID}},
		Content: content,
	}
	id, err := ev.eventID()
	if err != nil {
		return nil, err
	}
	sig, err := schnorr.Sign(secrets.priv, id[:])
	if err != nil {
		return nil, fmt.Errorf("sign session event: %w", err)
	}
	ev.ID = hex.EncodeToString(id[:])
	ev.Sig = hex.EncodeToString(sig.Serialize())
	return ev, nil
}

// OpenSession verifies an event came from this room and decrypts it.
//
// The signature check stops a relay injecting messages into a conversation it is
// only supposed to carry; the decryption stops it reading one. Neither makes the
// CONTENT trustworthy -- the counterparty is exactly who a malicious contract
// would come from -- so everything returned here still goes through the
// verifying handlers before it touches a swap.
func OpenSession(code string, ev *NostrEvent) (*SessionMessage, error) {
	keys, secrets, err := DeriveSession(code)
	if err != nil {
		return nil, err
	}
	if ev == nil {
		return nil, errors.New("no event")
	}
	if ev.Kind != sessionKind {
		return nil, fmt.Errorf("event kind %d is not a ferry session message", ev.Kind)
	}
	if !strings.EqualFold(ev.PubKey, keys.PubKey) {
		return nil, errors.New("event was published under a different key than this code derives")
	}

	// The id is recomputed rather than believed: it is what the signature is
	// over, so accepting the relay's copy of it would make the signature check
	// a check on nothing.
	want, err := ev.eventID()
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(ev.ID, hex.EncodeToString(want[:])) {
		return nil, errors.New("event id does not match its contents")
	}
	sigRaw, err := hex.DecodeString(ev.Sig)
	if err != nil {
		return nil, fmt.Errorf("bad signature encoding: %w", err)
	}
	sig, err := schnorr.ParseSignature(sigRaw)
	if err != nil {
		return nil, fmt.Errorf("bad signature: %w", err)
	}
	pubRaw, err := hex.DecodeString(keys.PubKey)
	if err != nil {
		return nil, err
	}
	pub, err := schnorr.ParsePubKey(pubRaw)
	if err != nil {
		return nil, err
	}
	if !sig.Verify(want[:], pub) {
		return nil, errors.New("event signature does not verify")
	}

	plain, err := openBox(secrets.enc, ev.Content)
	if err != nil {
		return nil, err
	}
	var msg SessionMessage
	if err := json.Unmarshal(plain, &msg); err != nil {
		return nil, fmt.Errorf("session message is not readable: %w", err)
	}
	return &msg, nil
}

// sealBox encrypts with AES-256-GCM under a random nonce, which is prefixed to
// the ciphertext. GCM rather than a stream cipher because a relay is an
// untrusted intermediary that can modify what it stores, and an authenticated
// mode is what makes a modified message fail to open rather than open into
// something else.
func sealBox(key [32]byte, plain []byte) (string, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, plain, nil)), nil
}

func openBox(key [32]byte, encoded string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("session content is not base64: %w", err)
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, errors.New("session content is too short to be a message")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		// The one failure worth naming precisely: it is what a wrong code looks
		// like, and "authentication failed" would send somebody hunting for a
		// network problem they do not have.
		return nil, errors.New("this message could not be decrypted with that session code — " +
			"either the code is wrong, or somebody else is publishing to this room")
	}
	return plain, nil
}

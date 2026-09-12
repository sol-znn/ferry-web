package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	zt "github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon/solzen/wasm/sol"
	"github.com/zenon/solzen/wasm/znn"
)

// A swap is two hashed timelock contracts on two chains that commit to one
// secret. Either both settle or both refund; there is no state in which one
// party has both legs.
//
// The whole design is one asymmetry repeated. The *initiator* invents the
// secret, so they can always claim the counterparty's leg; to stop that being a
// free option, their own leg is locked for longer. The counterparty's leg
// expires first, which means the initiator has to reveal the secret -- by
// claiming it -- while the initiator's own leg is still live enough for the
// counterparty to use that secret. Every rule in this file follows from that.

// Which leg the initiator funds. It decides the timelock ordering and, with it,
// which chain the secret becomes public on -- because the secret surfaces on
// the leg the initiator *claims*, which is always the other one.
const (
	LegSolana = "sol"
	LegZenon  = "znn"
)

// Default timelocks, matching ferry's Bitcoin<->Zenon swaps. The initiator's
// leg gets the long one.
const (
	DefaultLongSeconds  int64 = 48 * 3600
	DefaultShortSeconds int64 = 24 * 3600
)

// MinLegGapSeconds and MinRemainingSeconds are in limits.go, because they are
// the two values the timeout test has to lower to run at all -- see the build
// tag there.

// MaxClockSkewSeconds is how far the two chains' clocks may disagree before the
// leg-ordering arithmetic is treated as unreliable.
//
// The two deadlines live on different clocks: Solana's is stake-weighted
// validator time, Zenon's is momentum time. Both track unix time closely, and
// neither promises to. Comparing them at all is only sound while they agree to
// within much less than the gap being checked.
const MaxClockSkewSeconds int64 = 15 * 60

// Terms are what the two sides agree on. Both sides hold an identical copy;
// everything else in a swap record is local.
type Terms struct {
	// SwapID is 32 bytes of hex, and does double duty: it is the seed of the
	// Solana escrow's address, and the name the two sides use for the swap. It
	// is not the hashlock -- reusing the hashlock as an address seed would make
	// two swaps that share a secret collide on one escrow.
	SwapID   string `json:"swapId"`
	Hashlock string `json:"hashlock"`

	// Initiator names the leg whose funder invented the secret.
	Initiator string `json:"initiator"`

	// SolGenesis and ZnnChainID say which two chains this swap is on.
	//
	// Neither an RPC URL nor a program id can stand in for them. A URL is a
	// name somebody typed, and a program deployed from one keypair sits at the
	// same address on every cluster it was deployed to -- so "we are both
	// configured for program X" is satisfied by two people on different
	// clusters. The genesis hash and the chain identifier are what actually
	// differ.
	//
	// They are in Terms, not on the local record, because both sides have to
	// agree about them: an acceptance echoes the whole struct back, so a taker
	// on another chain cannot return terms that compare equal. And they are
	// checked again on every refresh, because the node URLs are one global
	// setting that every swap in this browser is read through -- changing it
	// silently re-points swaps that were made against something else.
	SolGenesis string `json:"solGenesis,omitempty"`
	ZnnChainID uint64 `json:"znnChainId,omitempty"`

	SolProgram  string `json:"solProgram"`
	SolSender   string `json:"solSender"`
	SolReceiver string `json:"solReceiver"`
	SolLamports uint64 `json:"solLamports"`
	SolTimelock int64  `json:"solTimelock"`

	// ZnnSender is the address that creates the Zenon HTLC, and the only
	// address that can reclaim it after expiry. It is a per-swap address the
	// sending side's page generated, not their wallet -- see keys.go.
	ZnnSender   string `json:"znnSender"`
	ZnnReceiver string `json:"znnReceiver"`
	ZnnToken    string `json:"znnToken"`
	ZnnAmount   string `json:"znnAmount"`
	ZnnDecimals int    `json:"znnDecimals"`
	ZnnExpiry   int64  `json:"znnExpiry"`
}

// Complete reports whether both sides' addresses are known. An offer is not
// complete: it is missing whatever the taker has to supply.
func (t *Terms) Complete() bool {
	return t.SwapID != "" && t.Hashlock != "" &&
		t.SolSender != "" && t.SolReceiver != "" && t.SolLamports > 0 && t.SolTimelock > 0 &&
		t.ZnnSender != "" && t.ZnnReceiver != "" && t.ZnnAmount != "" && t.ZnnExpiry > 0
}

// InitiatorExpiry and ParticipantExpiry return the two deadlines by role rather
// than by chain, which is the way every ordering rule is actually written.
func (t *Terms) InitiatorExpiry() int64 {
	if t.Initiator == LegSolana {
		return t.SolTimelock
	}
	return t.ZnnExpiry
}

func (t *Terms) ParticipantExpiry() int64 {
	if t.Initiator == LegSolana {
		return t.ZnnExpiry
	}
	return t.SolTimelock
}

// PreimageChain names the chain the secret becomes public on: the participant's
// leg, because that is the one the initiator claims.
func (t *Terms) PreimageChain() string {
	if t.Initiator == LegSolana {
		return LegZenon
	}
	return LegSolana
}

// CheckOrdering enforces the rule the whole scheme rests on.
//
// It is checked by both sides and on every refresh, not once at acceptance: the
// deadlines are absolute, so a swap that was safe to accept becomes unsafe
// simply by sitting there, and the moment it does is the moment the UI has to
// stop telling someone to fund it.
func (t *Terms) CheckOrdering(now int64) error {
	gap := t.InitiatorExpiry() - t.ParticipantExpiry()
	if gap < MinLegGapSeconds {
		return fmt.Errorf(
			"the initiator's leg expires %s after the participant's, and a swap needs at least %s: "+
				"below that, the participant can be shown the secret too late to use it",
			humanDuration(gap), humanDuration(MinLegGapSeconds))
	}
	if left := t.ParticipantExpiry() - now; left < MinRemainingSeconds {
		return fmt.Errorf(
			"the participant's leg has %s left, which is under the %s minimum",
			humanDuration(left), humanDuration(MinRemainingSeconds))
	}
	return nil
}

// Role is what this side of the swap is doing. It is local: the counterparty's
// record says the opposite of every field here.
type Role struct {
	// SendsSol is true for the side that locks SOL and is paid ZNN.
	SendsSol bool `json:"sendsSol"`
	// Initiator is true for the side that invented the secret.
	Initiator bool `json:"initiator"`
}

// MyLeg and TheirLeg name the chains this side funds and claims.
func (r Role) MyLeg() string {
	if r.SendsSol {
		return LegSolana
	}
	return LegZenon
}

func (r Role) TheirLeg() string {
	if r.SendsSol {
		return LegZenon
	}
	return LegSolana
}

// Swap is one swap as this browser knows it.
type Swap struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
	Updated int64  `json:"updated"`
	Terms   Terms  `json:"terms"`
	Role    Role   `json:"role"`

	// Secret is 32 bytes of hex, and only the initiator has it before
	// settlement. After the initiator claims, the participant's refresh finds
	// it on chain and fills this in -- which is the moment their own claim
	// becomes possible.
	Secret string `json:"secret,omitempty"`
	// SecretSource says where it came from -- "you generated it", or the
	// transaction a scan found it in. Kept on the record rather than recomputed,
	// because after the first refresh that found it there is nothing left to
	// recompute from: the secret is simply present.
	SecretSource string `json:"secretSource,omitempty"`

	// ZnnSwapSeed is the per-swap Zenon key, hex. Both sides have one and they
	// use it for different things: the ZNN sender signs the Create and the
	// Reclaim with it, the ZNN receiver signs only the Unlock -- an operation
	// that moves no money out of any account this key controls, because the
	// contract pays the address recorded in the entry.
	ZnnSwapSeed string `json:"znnSwapSeed,omitempty"`

	// ZnnHomeAddress is where the ZNN sender's page sweeps a reclaimed swap
	// address back to. It is their own wallet, supplied by them.
	ZnnHomeAddress string `json:"znnHomeAddress,omitempty"`

	// HtlcID is the Zenon HTLC's id, which is the hash of the block that
	// created it. Found on chain rather than pasted, where the scan reaches it.
	HtlcID string `json:"htlcId,omitempty"`

	// Notes of what has already been done, so a refresh does not re-propose a
	// step that is in flight.
	SolCreateSig string `json:"solCreateSig,omitempty"`
	SolRedeemSig string `json:"solRedeemSig,omitempty"`
	SolRefundSig string `json:"solRefundSig,omitempty"`
	ZnnCreateTx  string `json:"znnCreateTx,omitempty"`
	ZnnUnlockTx  string `json:"znnUnlockTx,omitempty"`
	ZnnReclaimTx string `json:"znnReclaimTx,omitempty"`
	ZnnSweepTx   string `json:"znnSweepTx,omitempty"`

	// Archived swaps are finished, one way or the other.
	Archived bool   `json:"archived"`
	Outcome  string `json:"outcome,omitempty"`
}

// validate reports whether a record could have been written by this program.
//
// Only Import calls it, and only because Import is the one way a record arrives
// without having been built here. Everything it checks is an internal
// consistency the engine relies on afterwards without asking again: that the id
// is the id, that the secret opens the hashlock it is stored beside, that the
// swap key is a key. A record failing any of them is not a swap with a problem,
// it is not a swap.
//
// The secret check is the one worth stating. A record whose Secret does not
// hash to its Hashlock would sit in the store looking settled-and-ready, and
// produce a claim that fails on chain for reasons the page cannot explain --
// exactly the state adoptPreimage exists to keep out when the secret arrives
// from a scan, applied to the other way in.
func (s *Swap) validate() error {
	if err := checkHex(s.ID, 32, "the swap id"); err != nil {
		return err
	}
	if s.ID != s.Terms.SwapID {
		return fmt.Errorf("the record's id and its terms' swap id are different")
	}
	if err := validateTerms(&s.Terms, "record"); err != nil {
		return err
	}
	if s.Secret != "" {
		got, err := HashlockOf(s.Secret)
		if err != nil {
			return fmt.Errorf("the stored secret is not usable: %w", err)
		}
		if got != s.Terms.Hashlock {
			return fmt.Errorf("the stored secret does not open this swap's hashlock")
		}
	}
	if s.ZnnSwapSeed != "" {
		if _, err := znn.KeyFromSeedHex(s.ZnnSwapSeed); err != nil {
			return fmt.Errorf("the per-swap Zenon key is not usable: %w", err)
		}
	}
	if s.ZnnHomeAddress != "" {
		if _, err := zt.ParseAddress(s.ZnnHomeAddress); err != nil {
			return fmt.Errorf("the address a reclaim would be sent to is not a Zenon address: %w", err)
		}
	}
	if s.HtlcID != "" {
		if _, err := znn.ParseHashHex(s.HtlcID); err != nil {
			return fmt.Errorf("the recorded Zenon HTLC id is not a hash: %w", err)
		}
	}
	return nil
}

// NewSwapID returns 32 random bytes of hex, used as both the Solana escrow seed
// and the swap's shared name.
func NewSwapID() (string, error) { return randomHex(32) }

// NewSecret returns a 32-byte secret and its SHA-256 hashlock.
//
// SHA-256 because it is the one hash both chains can commit to: Zenon's htlc
// contract offers SHA3-256 and SHA-256, and the Solana program computes
// SHA-256. An HTLC created with the other one is unlockable by nobody.
func NewSecret() (secret, hashlock string, err error) {
	s, err := randomHex(32)
	if err != nil {
		return "", "", err
	}
	raw, _ := hex.DecodeString(s)
	sum := sha256.Sum256(raw)
	return s, hex.EncodeToString(sum[:]), nil
}

// HashlockOf recomputes the commitment, so a secret is never trusted to match
// the hashlock it was stored beside.
func HashlockOf(secretHex string) (string, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(secretHex))
	if err != nil {
		return "", fmt.Errorf("the secret is not hex: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("the platform random source failed: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// The two strings that pass between the parties.
//
// Both carry public data only -- addresses, amounts, deadlines and a hash. Never
// a secret, never a key. They are copied by hand from one browser to the other,
// which is what keeps this a static page with no server in the middle.
const (
	offerPrefix  = "solzenoffer1:"
	acceptPrefix = "solzenaccept1:"
)

// EncodeOffer produces the maker's half of the agreement.
func EncodeOffer(t Terms) (string, error) { return encodeEnvelope(offerPrefix, t) }

// EncodeAccept produces the taker's reply: the same terms with their addresses
// filled in. Sending the whole thing back rather than only the new fields means
// the maker can see exactly what the taker believes was agreed, and refuse if
// it is not what they offered.
func EncodeAccept(t Terms) (string, error) { return encodeEnvelope(acceptPrefix, t) }

func encodeEnvelope(prefix string, t Terms) (string, error) {
	raw, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeEnvelope parses an offer or an accept, refusing anything malformed
// field by field rather than filling in defaults.
//
// A missing field in a pasted string is not a small problem: an offer with no
// timelock, silently defaulted, is an offer whose ordering rule was never
// checked against what the other side actually intends to create.
func DecodeEnvelope(s string) (kind string, t Terms, err error) {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, offerPrefix):
		kind, s = "offer", strings.TrimPrefix(s, offerPrefix)
	case strings.HasPrefix(s, acceptPrefix):
		kind, s = "accept", strings.TrimPrefix(s, acceptPrefix)
	default:
		return "", t, fmt.Errorf("this does not look like a swap string: it should start with %q or %q", offerPrefix, acceptPrefix)
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return "", t, fmt.Errorf("the string is damaged -- it is not valid base64url: %w", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return "", t, fmt.Errorf("the string does not decode to swap terms: %w", err)
	}
	if err := validateTerms(&t, kind); err != nil {
		return "", t, err
	}
	return kind, t, nil
}

func validateTerms(t *Terms, kind string) error {
	if err := checkHex(t.SwapID, 32, "the swap id"); err != nil {
		return err
	}
	if err := checkHex(t.Hashlock, 32, "the hashlock"); err != nil {
		return err
	}
	if t.Initiator != LegSolana && t.Initiator != LegZenon {
		return fmt.Errorf("the initiating leg must be %q or %q, not %q", LegSolana, LegZenon, t.Initiator)
	}
	if t.SolLamports == 0 {
		return fmt.Errorf("the SOL amount is missing or zero")
	}
	if t.SolTimelock <= 0 {
		return fmt.Errorf("the Solana timelock is missing")
	}
	if t.ZnnExpiry <= 0 {
		return fmt.Errorf("the Zenon expiry is missing")
	}
	if t.ZnnToken == "" {
		return fmt.Errorf("the Zenon token is missing -- an amount with no token cannot be checked")
	}
	if t.ZnnDecimals < 0 || t.ZnnDecimals > 18 {
		return fmt.Errorf("the token's decimals (%d) are not a plausible value", t.ZnnDecimals)
	}
	amount, ok := new(big.Int).SetString(t.ZnnAmount, 10)
	if !ok || amount.Sign() <= 0 {
		return fmt.Errorf("the Zenon amount %q is not a positive whole number of base units", t.ZnnAmount)
	}
	if t.SolProgram == "" {
		return fmt.Errorf("the Solana program id is missing")
	}
	// Every address in the string, parsed rather than carried as text.
	//
	// Verification compares these as strings, so an unparseable one fails
	// closed -- it simply never matches what is on chain. But it fails closed
	// at funding time, with a message about a mismatch, when the real problem
	// was a damaged string somebody pasted minutes earlier. This is the moment
	// to say so. Blank is not checked here: Complete() decides what may still
	// be missing at this stage.
	for _, a := range []struct{ what, value string }{
		{"the Solana program id", t.SolProgram},
		{"the SOL sender", t.SolSender},
		{"the SOL receiver", t.SolReceiver},
	} {
		if a.value == "" {
			continue
		}
		if _, err := sol.ParsePubkey(a.value); err != nil {
			return fmt.Errorf("%s is not a Solana address: %w", a.what, err)
		}
	}
	for _, a := range []struct{ what, value string }{
		{"the Zenon sender", t.ZnnSender},
		{"the Zenon receiver", t.ZnnReceiver},
	} {
		if a.value == "" {
			continue
		}
		if _, err := zt.ParseAddress(a.value); err != nil {
			return fmt.Errorf("%s is not a Zenon address: %w", a.what, err)
		}
	}
	// Only for strings arriving from another browser. A record written by an
	// older build has neither field, and refusing to load it would strand
	// swaps to fix a gap that only ever mattered between two parties -- the
	// per-refresh check below covers a record that carries them, and one that
	// does not is no worse off than it was.
	if kind != "record" {
		if t.SolGenesis == "" {
			return fmt.Errorf(
				"this string does not say which Solana cluster it is for, so there is no way to tell " +
					"whether we are on the same one; it was made by an older build")
		}
		if t.ZnnChainID == 0 {
			return fmt.Errorf(
				"this string does not say which Zenon chain it is for, so there is no way to tell " +
					"whether we are on the same one; it was made by an older build")
		}
	}
	if kind == "accept" && !t.Complete() {
		return fmt.Errorf("this acceptance is missing addresses that both sides need")
	}
	return nil
}

// CheckChains refuses terms that belong to chains other than the ones this
// browser is pointed at.
//
// A swap is read entirely through two node URLs that live in one global
// setting, so pointing that setting somewhere else re-reads every swap against
// somewhere else. What follows is not a lookup failure: an escrow that exists
// on the cluster it was funded on simply is not on the new one, so the leg
// reads *not funded*, and the next thing the page offers is to fund it. Doing
// that would put real lamports into a second escrow on a chain the swap was
// never about.
//
// Empty fields are not a mismatch. They mean a record written before this
// existed, which is a swap with less protection, not a swap to refuse.
func (t *Terms) CheckChains(solGenesis string, znnChainID uint64) error {
	if t.SolGenesis != "" && solGenesis != "" && t.SolGenesis != solGenesis {
		return fmt.Errorf(
			"this swap was made against Solana cluster %s and the node in Nodes serves %s -- "+
				"its escrow does not exist here, and funding it again would be a second escrow "+
				"on the wrong chain",
			shortHash(t.SolGenesis), shortHash(solGenesis))
	}
	if t.ZnnChainID != 0 && znnChainID != 0 && t.ZnnChainID != znnChainID {
		return fmt.Errorf(
			"this swap was made against Zenon chain %d and the node in Nodes serves chain %d -- "+
				"its HTLC does not exist here",
			t.ZnnChainID, znnChainID)
	}
	return nil
}

func shortHash(s string) string {
	if len(s) > 12 {
		return s[:12] + "..."
	}
	return s
}

func checkHex(s string, n int, what string) error {
	raw, err := hex.DecodeString(s)
	if err != nil {
		return fmt.Errorf("%s is not hex: %w", what, err)
	}
	if len(raw) != n {
		return fmt.Errorf("%s should be %d bytes, this is %d", what, n, len(raw))
	}
	return nil
}

func humanDuration(seconds int64) string {
	if seconds < 0 {
		return "-" + humanDuration(-seconds)
	}
	d := time.Duration(seconds) * time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", seconds)
	case d < time.Hour:
		return fmt.Sprintf("%dm", seconds/60)
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", seconds/3600, (seconds%3600)/60)
	default:
		return fmt.Sprintf("%dd%02dh", seconds/86400, (seconds%86400)/3600)
	}
}

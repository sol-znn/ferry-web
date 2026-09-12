package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/zenon/ferry-web-v2/wasm/sol"
	"github.com/zenon/ferry-web-v2/wasm/znn"
)

// A swap is two legs and one secret.
//
// v1 of this app called them "the Bitcoin leg" and "the Zenon leg", which was
// true of the only pair it had. Four pairs make the names wrong — a ZTS↔ZTS
// swap has two Zenon legs — so the legs are named by what they do for THIS
// user instead:
//
//	Out   the leg this user funds. They hold its refund path.
//	In    the leg the counterparty funds. This user holds its claim.
//
// Everything that used to be a rule about Bitcoin-versus-Zenon turns out to be
// a rule about Out-versus-In, and is shorter for it. Which leg takes the long
// timelock is the whole example: it was `ZenonLegIsInitiators()`, a function of
// role AND direction; it is now "the initiator's own funded leg", which is
// `Role == RoleInitiator` and nothing else.

// Role says whether this side generated the secret. The initiator must take the
// longer timelock on their OWN leg: they learn nothing until the participant
// acts, whereas the participant is exposed to the initiator sitting on the
// secret until the last moment.
type Role string

const (
	RoleInitiator   Role = "initiator"
	RoleParticipant Role = "participant"
)

// Opposite returns the role the other side of a trade takes.
func (r Role) Opposite() Role {
	if r == RoleInitiator {
		return RoleParticipant
	}
	return RoleInitiator
}

// Dir says which way one leg moves for this user.
type Dir string

const (
	// DirOut is the leg this user funds: the counterparty claims it with the
	// secret, and this user reclaims it after its deadline.
	DirOut Dir = "out"
	// DirIn is the leg the counterparty funds: this user claims it with the
	// secret, and the counterparty reclaims it after its deadline.
	DirIn Dir = "in"
)

// State is the swap's position in its lifecycle.
//
// One state for the whole swap rather than one per leg, because what a user
// needs to know is whether the trade is live, done, or gone wrong — and the
// per-leg detail is on the legs, where it can be read exactly.
type State string

const (
	StateDraft    State = "draft"            // waiting on counterparty details
	StateAwaiting State = "awaiting_funding" // both legs describable, nothing funded
	StateFunded   State = "funded"           // at least one leg holds money
	StateSettled  State = "settled"          // this user's incoming leg was claimed
	StateRefunded State = "refunded"         // this user took their outgoing leg back
	StateExpired  State = "expired"          // a deadline passed with money still locked
)

// Swap is the full record of one side of one trade. It is the unit of
// persistence, and everything needed to recover funds is inside it.
type Swap struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Network   string    `json:"network"`
	Role      Role      `json:"role"`
	State     State     `json:"state"`

	// Secret is set when this side is the initiator, or once it has been
	// extracted from the counterparty's claim on the outgoing leg. It is the one
	// field that must never leave this machine before the swap is claimed.
	Secret     []byte `json:"secret,omitempty"`
	SecretHash []byte `json:"secretHash"`

	// Out is the leg this user funds; In is the leg they receive. Both are
	// always present on a swap this build created.
	Out *Leg `json:"out"`
	In  *Leg `json:"in"`

	// Archived is set by the user to move a finished swap into the history list.
	Archived bool `json:"archived,omitempty"`

	Events []Event `json:"events"`
}

// Leg is one half of a swap: a chain, a direction, an amount, a deadline, and
// whatever that chain needs to hold a contract.
type Leg struct {
	Chain ChainID `json:"chain"`
	Dir   Dir     `json:"dir"`

	// Token is the ZTS a Zenon leg moves. Empty on Bitcoin and Solana, which
	// move the chain's own coin and have nothing to name.
	Token string `json:"token,omitempty"`

	// LockHours is the duration the user asked this leg to run for, when they
	// overrode the schedule the role implies. Kept on the record rather than
	// used once and dropped: the deadline is replanned every time a leg is
	// re-read, and a plan that forgets what was asked for silently reinstates
	// the default.
	LockHours int `json:"lockHours,omitempty"`

	// Amount is the agreed size as a person types it, in the chain's own unit —
	// satoshi on Bitcoin, SOL on Solana, whole tokens on Zenon. Text rather than
	// a number because a figure that has been through a float is a figure that
	// has changed, and because a ZTS may carry more precision than a float has.
	Amount string `json:"amount"`

	// Base is the same figure in base units, once it is known. On Bitcoin and
	// Solana that is at creation; on Zenon it needs the token's own decimals,
	// which come from a node, so it may be empty on a swap that has not reached
	// one yet. Verification uses this and refuses rather than guessing.
	Base string `json:"base,omitempty"`

	// Expiry is when this leg's lock lifts, in absolute unix seconds. For
	// Bitcoin it is the contract's nLockTime; for Zenon the entry's
	// expirationTime; for Solana the escrow's timelock.
	Expiry int64 `json:"expiry,omitempty"`

	// SelfAddr is this user's address on this chain — where their half of this
	// leg lands. PeerAddr is the counterparty's. Both are the chain's own
	// spelling: a Bitcoin address, a z1 address, a base58 Solana pubkey.
	SelfAddr string `json:"selfAddr,omitempty"`
	PeerAddr string `json:"peerAddr,omitempty"`

	// Exactly one of these is set, matching Chain.
	Btc *BtcLeg `json:"btc,omitempty"`
	Znn *ZnnLeg `json:"znn,omitempty"`
	Sol *SolLeg `json:"sol,omitempty"`
}

// BtcLeg is a Bitcoin P2SH hashed-timelock contract.
//
// Funding it is an ordinary payment, which is why no wallet support is needed
// for that half; spending out of it needs a custom script witness, which is what
// the per-swap key is for.
type BtcLeg struct {
	// Key is this side's ephemeral keypair: the refund key on an out leg, the
	// redeem key on an in leg.
	Key *SwapKey `json:"key"`
	// CounterpartyPKH is the other side's pubkey hash in the contract.
	CounterpartyPKH []byte `json:"counterpartyPkh,omitempty"`

	Contract     []byte `json:"contract,omitempty"`
	ContractAddr string `json:"contractAddr,omitempty"`

	Funding *FundingOutput `json:"funding,omitempty"`
	// FundingBroadcast is a payment to the contract this browser has sent but
	// not yet seen come back off the chain. It is what stops the card offering
	// to send a second one; it is never evidence the contract holds money.
	FundingBroadcast *FundingBroadcast `json:"fundingBroadcast,omitempty"`

	RefundTx *SpendResult `json:"refundTx,omitempty"`
	RedeemTx *SpendResult `json:"redeemTx,omitempty"`

	// Claimed and Reclaimed record what became of the contract output, as seen
	// on chain. Kept per leg rather than in the swap's state because a swap has
	// two legs and they end separately.
	Claimed   bool `json:"claimed,omitempty"`
	Reclaimed bool `json:"reclaimed,omitempty"`
}

// ZnnLeg is one entry in go-zenon's `htlc` embedded contract.
//
// ferry builds the calls that write it — htlc.Create, Unlock and Reclaim are
// account blocks assembled in walletblock.go — but never signs one. A block
// leaves this module as bytes and is handed to the user's wallet, which signs
// with its own key, mines its own plasma and publishes through its own node.
type ZnnLeg struct {
	HtlcID string `json:"htlcId,omitempty"`

	// ObservedToken and ObservedHashLocked are facts about the entry, read off
	// the chain, never expectations. Leg.Token and the agreed recipient are what
	// verification compares against; overwriting those with what the entry
	// happens to hold is exactly the substitution attack.
	ObservedToken      string `json:"observedToken,omitempty"`
	ObservedHashLocked string `json:"observedHashLocked,omitempty"`

	// ExpirationHours and ExpirationSeconds are the duration this leg should be
	// created with, recomputed whenever the other leg's deadline becomes known.
	// Hours is for znn-cli, seconds for the block a wallet signs.
	ExpirationHours   int `json:"expirationHours,omitempty"`
	ExpirationSeconds int `json:"expirationSeconds,omitempty"`

	HashType   int `json:"hashType"`
	KeyMaxSize int `json:"keyMaxSize"`

	Verified    bool   `json:"verified"`
	VerifyError string `json:"verifyError,omitempty"`
	// VerifyPending means the entry has not been checked against the chain YET,
	// not that a check failed. A block published a moment ago is simply not
	// readable, which is the ordinary case after a create.
	VerifyPending bool `json:"verifyPending,omitempty"`

	// UnlockHash is the transaction THIS browser published to unlock this leg.
	// An unlock deletes the entry, so afterwards no node can be asked whether it
	// was collected — a settled leg and an id that never existed answer
	// identically. This is the only record that it happened.
	UnlockHash string `json:"unlockHash,omitempty"`
	// UnlockSeen is the mirror for the side that did not publish it: the
	// counterparty's unlock was FOUND on the chain. Never inferred from merely
	// holding a preimage, which one pasted in by hand satisfies just as well.
	UnlockSeen bool `json:"unlockSeen,omitempty"`
	// ReclaimHash is this browser's reclaim after expiry.
	ReclaimHash string `json:"reclaimHash,omitempty"`
}

// SolLeg is one escrow account of the ferry-htlc Solana program.
type SolLeg struct {
	// ProgramID is the deployment this leg lives on, and it is a TERM of the
	// trade rather than a setting: two deployments of the same source are two
	// different contracts, and an escrow on one is not an escrow on the other.
	// Both sides carry it in the offer and it is compared before anything funds.
	ProgramID string `json:"programId,omitempty"`
	// SwapID is the 32-byte PDA seed, hex. Both sides know it, so both derive
	// the same escrow address without anybody handing one over.
	SwapID string `json:"swapId,omitempty"`
	// Escrow is the derived account. Recomputed on read rather than trusted.
	Escrow string `json:"escrow,omitempty"`

	// Lamports is the agreed amount in base units, cached from Leg.Amount at
	// creation because Solana's decimals are fixed and so it cannot go stale.
	Lamports uint64 `json:"lamports,omitempty"`

	// What the chain says the escrow actually holds. Facts, like the Zenon
	// observed fields, and compared against the agreed terms rather than
	// replacing them.
	Funded            bool   `json:"funded,omitempty"`
	ObservedAmount    uint64 `json:"observedAmount,omitempty"`
	ObservedLamports  uint64 `json:"observedLamports,omitempty"`
	ObservedInitiator string `json:"observedInitiator,omitempty"`
	ObservedReceiver  string `json:"observedReceiver,omitempty"`
	ObservedTimelock  int64  `json:"observedTimelock,omitempty"`

	Verified      bool   `json:"verified"`
	VerifyError   string `json:"verifyError,omitempty"`
	VerifyPending bool   `json:"verifyPending,omitempty"`

	// The three signatures this swap produced or observed. Redeemed and
	// Refunded are the outcome as read off the chain: the escrow account is
	// closed by either exit, so an absent account alone says nothing about which
	// one happened.
	CreateSig string `json:"createSig,omitempty"`
	RedeemSig string `json:"redeemSig,omitempty"`
	RefundSig string `json:"refundSig,omitempty"`
	Redeemed  bool   `json:"redeemed,omitempty"`
	Refunded  bool   `json:"refunded,omitempty"`
}

// Event is a timestamped line in the swap's history, shown in the UI so the
// user can see exactly what was observed and when.
type Event struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`
}

func (s *Swap) log(format string, args ...any) {
	s.Events = append(s.Events, Event{At: time.Now().UTC(), Message: fmt.Sprintf(format, args...)})
	s.UpdatedAt = time.Now().UTC()
}

// loggedOnce reports whether msg is already in the event log, so a note emitted
// from Refresh — which runs on every poll — appears once rather than burying the
// log in copies of itself.
func (s *Swap) loggedOnce(msg string) bool {
	for i := range s.Events {
		if s.Events[i].Message == msg {
			return true
		}
	}
	return false
}

// ---------- the rules ----------

// MinLegGap is the smallest acceptable gap between the participant's leg
// expiring and the initiator's.
//
// The invariant every atomic swap rests on is that the INITIATOR's leg expires
// LAST. Claiming the participant's leg is what publishes the preimage, and the
// participant then needs time to use it — otherwise the initiator could let
// their own leg expire, reclaim it, and still claim the participant's with the
// secret, taking both sides.
//
// Two hours is a floor, not a recommendation; the defaults in manager.go leave
// 24. It exists so a counterparty proposing a dangerously tight pair is refused
// rather than merely frowned at.
const MinLegGap = 2 * time.Hour

// MinLegRemaining is how much life a leg must have left to be worth acting on
// at all. One expiring within minutes is one whose funder reclaims it the
// moment the other side commits.
const MinLegRemaining = 2 * time.Hour

// IsInitiators reports whether this leg is the one that must expire LAST.
//
// The initiator's own funded leg is the long one. So the outgoing leg is the
// initiator's exactly when this user is the initiator, and the incoming leg is
// exactly when they are not. That is the entire ordering rule, and it does not
// mention a chain.
func (s *Swap) legIsInitiators(dir Dir) bool {
	if dir == DirOut {
		return s.Role == RoleInitiator
	}
	return s.Role == RoleParticipant
}

// OutIsInitiators is the long-leg question for the leg this user funds.
func (s *Swap) OutIsInitiators() bool { return s.legIsInitiators(DirOut) }

// InIsInitiators is the same for the leg they receive.
func (s *Swap) InIsInitiators() bool { return s.legIsInitiators(DirIn) }

// SecretArrivesOn names the chain the preimage becomes visible to this user on,
// or "" when they already hold it.
//
// The initiator invents the secret. The participant learns it exactly when the
// initiator claims the participant's funded leg — which is this user's OUT leg.
// It does not matter which chain that is: a Bitcoin redeem carries the preimage
// in its sigScript, a Zenon unlock in its call data, a Solana redeem in its
// instruction data. All three are read off the chain rather than taken from a
// message, because a preimage that came out of a contract was accepted by the
// contract holding the money.
func (s *Swap) SecretArrivesOn() ChainID {
	if s.Role == RoleInitiator || s.Out == nil {
		return ""
	}
	return s.Out.Chain
}

// Pair names this swap's trade, in the stable order Pairs declares.
func (s *Swap) Pair() string {
	if s.Out == nil || s.In == nil {
		return ""
	}
	return PairID(s.Out.Chain, s.In.Chain)
}

// Chains is every chain this swap settles on, deduplicated and in ChainOrder.
// It is what the board's proof rule is written against: you may act on a trade
// once you have proven an address on every chain it settles on.
func (s *Swap) Chains() []ChainID {
	return chainsOf(legChain(s.Out), legChain(s.In))
}

func legChain(l *Leg) ChainID {
	if l == nil {
		return ""
	}
	return l.Chain
}

// chainsOf deduplicates and orders a set of chains.
func chainsOf(ids ...ChainID) []ChainID {
	out := make([]ChainID, 0, len(ids))
	for _, want := range ChainOrder {
		for _, got := range ids {
			if got == want {
				out = append(out, want)
				break
			}
		}
	}
	return out
}

// Active reports whether this swap still belongs in front of the user.
//
// Only the user files a swap away. The "is there anything left to do" question
// is Settled, which is what lets the page shrink a finished swap to a summary
// row without also hiding it.
func (s *Swap) Active() bool { return !s.Archived }

// Settled reports that this swap is over: nothing on either chain is waiting on
// anybody, and all that is left is to file it.
//
// Both legs have to be done, and that is the whole subtlety. On a one-leg view
// a claimed incoming leg looks like the end of the story; it is not, because the
// outgoing leg is still locked until the counterparty claims it, and until they
// do this browser is the only thing watching for the deadline.
func (s *Swap) Settled() bool {
	return legDone(s.Out) && legDone(s.In)
}

// legDone reports that this leg's money has stopped moving: claimed by whoever
// was owed it, or taken back by whoever funded it.
func legDone(l *Leg) bool {
	if l == nil {
		return false
	}
	switch l.Chain {
	case ChainBTC:
		return l.Btc != nil && (l.Btc.Claimed || l.Btc.Reclaimed)
	case ChainZNN:
		if l.Znn == nil {
			return false
		}
		return l.Znn.UnlockHash != "" || l.Znn.UnlockSeen || l.Znn.ReclaimHash != ""
	case ChainSOL:
		return l.Sol != nil && (l.Sol.Redeemed || l.Sol.Refunded)
	}
	return false
}

// Funded reports whether this leg is holding money now, as far as this browser
// has seen.
func (l *Leg) Funded() bool {
	if l == nil {
		return false
	}
	switch l.Chain {
	case ChainBTC:
		return l.Btc != nil && l.Btc.Funding != nil
	case ChainZNN:
		return l.Znn != nil && l.Znn.HtlcID != ""
	case ChainSOL:
		return l.Sol != nil && l.Sol.Funded
	}
	return false
}

// Verified reports whether this leg has been checked against the chain and
// found to match the agreed terms. Always false for a leg this user funded:
// there is nothing to verify about your own contract that building it correctly
// did not already settle.
func (l *Leg) Verified() bool {
	if l == nil {
		return false
	}
	switch l.Chain {
	case ChainBTC:
		// A Bitcoin contract is verified by AUDITING it, which happens offline
		// and once; holding the contract at all means it passed.
		return l.Btc != nil && len(l.Btc.Contract) > 0
	case ChainZNN:
		return l.Znn != nil && l.Znn.Verified
	case ChainSOL:
		return l.Sol != nil && l.Sol.Verified
	}
	return false
}

// Label names this leg for a sentence: "10 ZNN", "400000 sat", "1.5 SOL".
func (l *Leg) Label() string {
	if l == nil {
		return ""
	}
	amount := strings.TrimSpace(l.Amount)
	switch l.Chain {
	case ChainBTC:
		return amount + " sat"
	case ChainSOL:
		return amount + " SOL"
	case ChainZNN:
		return amount + " " + TokenShortName(l.Token)
	}
	return amount
}

// TokenShortName renders a ZTS for a sentence. The well-known two get their
// tickers; anything else gets a shortened standard, because a full one is 26
// characters and unreadable in a line of prose.
func TokenShortName(zts string) string {
	switch resolveToken(zts) {
	case znn.ZnnTokenStandard:
		return "ZNN"
	case QsrTokenStandard:
		return "QSR"
	}
	t := resolveToken(zts)
	if len(t) > 12 {
		return t[:8] + "…" + t[len(t)-4:]
	}
	return t
}

// QsrTokenStandard is the other token every Zenon account holds. Named because
// a ZTS↔ZTS swap's commonest shape is ZNN against QSR, and a board full of
// unreadable standards is a board nobody reads.
const QsrTokenStandard = "zts1qsrxxxxxxxxxxxxxmrhjll"

// AgreedToken resolves a Zenon leg's token, applying the rule the form has
// always had: blank means ZNN.
func (l *Leg) AgreedToken() string {
	if l == nil {
		return ""
	}
	if l.Chain != ChainZNN {
		return ""
	}
	return resolveToken(l.Token)
}

// BaseInt is the agreed amount in base units, or an error saying why it is not
// known yet. Never a guess: an amount that could not be converted is a check
// that cannot run, and a check that cannot run must refuse rather than pass.
func (l *Leg) BaseInt() (*big.Int, error) {
	if l == nil {
		return nil, errors.New("no leg")
	}
	if strings.TrimSpace(l.Base) == "" {
		return nil, fmt.Errorf("the agreed %s amount has not been converted into base units yet",
			ChainLabel(l.Chain))
	}
	v, ok := new(big.Int).SetString(strings.TrimSpace(l.Base), 10)
	if !ok {
		return nil, fmt.Errorf("this swap records an unreadable base amount %q", l.Base)
	}
	return v, nil
}

// Sats is a Bitcoin leg's amount. Zero on any other chain, which callers guard
// against by checking Chain first.
func (l *Leg) Sats() int64 {
	if l == nil || l.Chain != ChainBTC {
		return 0
	}
	v, err := l.BaseInt()
	if err != nil || !v.IsInt64() {
		return 0
	}
	return v.Int64()
}

// Lamports is a Solana leg's amount.
func (l *Leg) Lamports() uint64 {
	if l == nil || l.Chain != ChainSOL || l.Sol == nil {
		return 0
	}
	return l.Sol.Lamports
}

// SwapIDBytes is a Solana leg's PDA seed as bytes.
func (l *Leg) SwapIDBytes() ([32]byte, error) {
	var out [32]byte
	if l == nil || l.Sol == nil {
		return out, errors.New("this leg is not a Solana leg")
	}
	raw, err := hex.DecodeString(strings.TrimSpace(l.Sol.SwapID))
	if err != nil || len(raw) != 32 {
		return out, fmt.Errorf("this swap's Solana id is not 32 bytes of hex")
	}
	copy(out[:], raw)
	return out, nil
}

// ProgramKey parses a Solana leg's program address.
func (l *Leg) ProgramKey() (sol.Pubkey, error) {
	if l == nil || l.Sol == nil || strings.TrimSpace(l.Sol.ProgramID) == "" {
		return sol.Pubkey{}, errors.New("this swap does not name a Solana program. Set one in " +
			"Node settings before creating a Solana leg — an escrow on one deployment is not " +
			"an escrow on another")
	}
	return sol.ParsePubkey(strings.TrimSpace(l.Sol.ProgramID))
}

// unlockPayee is the address a Zenon leg pays, as well as it can be known, for
// narrowing the search for the unlock that revealed the preimage.
//
// A hint, never a check: nothing here decides whether an unlock is this swap's,
// only which blocks are worth fetching first. An empty answer walks the HTLC
// contract's own chain unfiltered, which is slower and finds the same thing —
// a preimage identifies itself by hashing.
func (l *Leg) unlockPayee() string {
	if l == nil || l.Znn == nil {
		return ""
	}
	// The agreed recipient of an out leg is the counterparty; of an in leg,
	// this user. Either way it is what the entry was checked against.
	want := l.PeerAddr
	if l.Dir == DirIn {
		want = l.SelfAddr
	}
	if addr := strings.TrimSpace(want); addr != "" {
		return addr
	}
	return strings.TrimSpace(l.Znn.ObservedHashLocked)
}

// ZnnCliMaxHours is the ceiling znn-cli enforces on htlc.create's
// expirationTime argument: whole hours, 1..24, rejecting anything outside.
//
// A CLI restriction, not a protocol one. go-zenon's HTLC contract stores an
// absolute expirationTime and bounds it only by not already being expired, and
// the Syrius extension signs any expiry — so a Zenon leg longer than 24 hours is
// valid on chain and creatable, and ferry treats it as a note about tooling
// rather than an unsupported swap shape.
const ZnnCliMaxHours = 24

// DeletionRisk says what deleting this record might cost, or "" when it costs
// nothing.
//
// Deleting destroys the per-swap Bitcoin key, which is the only key that can
// spend a Bitcoin contract this swap built, and the record of every leg's
// deadline. Two shapes make that free: a settled swap, whose legs have nothing
// left in them, and one where nothing was ever funded.
func (s *Swap) DeletionRisk() string {
	if s.Settled() {
		return ""
	}
	var risky []string
	for _, l := range []*Leg{s.Out, s.In} {
		if l == nil || legDone(l) {
			continue
		}
		switch {
		case l.Chain == ChainBTC && l.Btc != nil && l.Btc.Funding != nil:
			risky = append(risky, fmt.Sprintf("a Bitcoin contract at %s holding %d sat that this "+
				"browser has not seen spent", l.Btc.ContractAddr, l.Btc.Funding.Value))
		case l.Chain == ChainBTC && l.Btc != nil && l.Btc.ContractAddr != "":
			risky = append(risky, fmt.Sprintf("a Bitcoin contract at %s that was never seen "+
				"funded — but a payment arriving after the last refresh would not be in this "+
				"record", l.Btc.ContractAddr))
		case l.Chain == ChainZNN && l.Znn != nil && l.Znn.HtlcID != "":
			risky = append(risky, fmt.Sprintf("a Zenon HTLC %s holding %s", l.Znn.HtlcID, l.Label()))
		case l.Chain == ChainSOL && l.Sol != nil && l.Sol.Funded:
			risky = append(risky, fmt.Sprintf("a Solana escrow at %s holding %s",
				l.Sol.Escrow, l.Label()))
		}
	}
	if len(risky) == 0 {
		return ""
	}
	return "This swap still has " + strings.Join(risky, ", and ") + "."
}

// Params returns the Bitcoin chain parameters for this swap's network. Every
// swap has them even when neither leg is Bitcoin: the network also picks which
// board and which storage namespace this record belongs to.
func (s *Swap) Params() (*chaincfg.Params, error) { return NetworkParams(s.Network) }

// NetworkParams maps a network name to its chain parameters.
func NetworkParams(name string) (*chaincfg.Params, error) {
	switch name {
	case "mainnet", "":
		return &chaincfg.MainNetParams, nil
	case "testnet", "testnet3":
		return &chaincfg.TestNet3Params, nil
	case "signet":
		return &chaincfg.SigNetParams, nil
	case "regtest":
		return &chaincfg.RegressionNetParams, nil
	default:
		return nil, fmt.Errorf("unknown network %q", name)
	}
}

// DefaultEsploraURL returns a public Esplora endpoint for a network. Users who
// do not want a third party seeing their address queries can point ferry at a
// self-hosted instance instead.
func DefaultEsploraURL(network string) string {
	switch network {
	case "mainnet", "":
		return "https://blockstream.info/api"
	case "testnet", "testnet3":
		return "https://blockstream.info/testnet/api"
	case "signet":
		return "https://mempool.space/signet/api"
	case "regtest":
		return "http://127.0.0.1:3002"
	default:
		return ""
	}
}

// DefaultSolanaURL returns a Solana RPC endpoint for a network.
//
// Where there is a default it is a public endpoint, unlike the Zenon side's
// deliberate blank. The asymmetry is real: verifying a Zenon HTLC is the one
// check where a dishonest answer costs money and there is no second opinion,
// whereas an escrow's contents are checked against a PDA this module derives
// itself — a node that lies about the account gets caught by the derivation
// rather than believed.
//
// **Mainnet has no default**, and that is not the trust argument, it is a fact
// about the endpoint: `api.mainnet-beta.solana.com` answers a request carrying
// a browser Origin with HTTP 403. It passes preflight and then refuses the POST,
// so a page falling back to it reports the chain unreachable and nothing on
// screen says the built-in default cannot work from a browser at all. Better a
// stated requirement than a default that never functions. The devnet endpoint
// does answer browsers, and keeps its default.
func DefaultSolanaURL(network string) string {
	switch network {
	case "testnet", "testnet3", "signet":
		return "https://api.devnet.solana.com"
	case "regtest":
		return "http://127.0.0.1:8899"
	default:
		return ""
	}
}

// ---------- the offer ----------

// Offer is the public metadata one side sends to the other to set a swap up.
//
// A separate type from Swap on purpose: the secret and the private key live on
// Swap and have no field here, so there is no code path that can serialise them
// into something the user is told to paste into a chat window.
//
// Version 2 is where the pair stopped being implied. A v1 offer said "the
// Bitcoin side is this much and the Zenon side is that much"; a v2 offer names
// both legs, so it can describe a trade with no Bitcoin in it. v1 strings are
// no longer read — the field that would have to be invented for them is which
// chain each half is on, and guessing that is how somebody ends up funding the
// wrong contract.
type Offer struct {
	Version    int    `json:"v"`
	Network    string `json:"network"`
	FromRole   Role   `json:"fromRole"`
	SecretHash string `json:"secretHash"`
	// Give is the leg the SENDER funds; Take is the one they receive. The
	// receiver mirrors them: their out leg is the sender's take.
	Give OfferLeg `json:"give"`
	Take OfferLeg `json:"take"`
	Note string   `json:"note,omitempty"`
}

// OfferLeg is one half of a proposal.
type OfferLeg struct {
	Chain ChainID `json:"chain"`
	Token string  `json:"token,omitempty"`
	// Amount as typed, in the chain's unit.
	Amount string `json:"amount"`
	// Addr is the SENDER's address on this chain: where their half of this leg
	// lands. On a give leg it is where a reclaim comes back to; on a take leg it
	// is where the money they are owed is paid.
	Addr string `json:"addr,omitempty"`
	// PKH is the sender's Bitcoin pubkey hash for whichever contract branch they
	// will need. Bitcoin only — the other two chains commit to addresses.
	PKH string `json:"pkh,omitempty"`
	// Expiry is the deadline the sender proposes for this leg, when they have
	// already fixed one.
	Expiry int64 `json:"expiry,omitempty"`
	// Program and SwapID are the Solana deployment and the PDA seed. Both sides
	// need the same two or they are looking at different accounts.
	Program string `json:"program,omitempty"`
	SwapID  string `json:"swapId,omitempty"`
}

// Encode renders the offer as a single copy-pasteable string.
func (o *Offer) Encode() (string, error) {
	raw, err := json.Marshal(o)
	if err != nil {
		return "", err
	}
	return "swapoffer2:" + base64.RawURLEncoding.EncodeToString(raw), nil
}

const offerPrefix = "swapoffer2:"

// DecodeOffer parses a string produced by Encode, checking every field.
//
// An offer is a stranger's input and the values derived from it decide which
// side of a trade the receiver takes, so nothing is accepted merely because it
// parses.
func DecodeOffer(s string) (*Offer, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "swapoffer1:") {
		return nil, errors.New("that is a version 1 offer, from ferry's Bitcoin-only build. It " +
			"does not say which chain each half is on, so it cannot be read here — ask for one " +
			"from this version")
	}
	if len(s) <= len(offerPrefix) || s[:len(offerPrefix)] != offerPrefix {
		return nil, errors.New("not a swap offer string (expected a swapoffer2: prefix)")
	}
	raw, err := base64.RawURLEncoding.DecodeString(s[len(offerPrefix):])
	if err != nil {
		return nil, fmt.Errorf("offer is not valid base64: %w", err)
	}
	var o Offer
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("offer is not valid JSON: %w", err)
	}
	if o.Version != 2 {
		return nil, fmt.Errorf("unsupported offer version %d", o.Version)
	}
	h, err := hex.DecodeString(o.SecretHash)
	if err != nil {
		return nil, fmt.Errorf("offer has a malformed secret hash: %w", err)
	}
	if len(h) != sha256.Size {
		return nil, fmt.Errorf("offer's secret hash is %d bytes, want %d", len(h), sha256.Size)
	}
	if _, err := NetworkParams(o.Network); err != nil {
		return nil, fmt.Errorf("offer names an %w", err)
	}
	if o.FromRole != RoleInitiator && o.FromRole != RoleParticipant {
		return nil, fmt.Errorf("offer's fromRole is %q, must be %q or %q",
			o.FromRole, RoleInitiator, RoleParticipant)
	}
	if err := o.Give.validate("the leg they fund"); err != nil {
		return nil, err
	}
	if err := o.Take.validate("the leg they receive"); err != nil {
		return nil, err
	}
	if err := CheckPair(o.Give.Chain, o.Take.Chain, o.Give.Token, o.Take.Token); err != nil {
		return nil, fmt.Errorf("this offer is not a trade: %w", err)
	}
	return &o, nil
}

// validate refuses an offer leg that could not describe half a trade.
func (l OfferLeg) validate(what string) error {
	if _, err := ChainOf(l.Chain); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if strings.TrimSpace(l.Amount) == "" {
		return fmt.Errorf("%s names no amount", what)
	}
	if err := checkTypedAmount(l.Chain, l.Token, l.Amount); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	switch l.Chain {
	case ChainBTC:
		if l.PKH != "" {
			pkh, err := hex.DecodeString(l.PKH)
			if err != nil || len(pkh) != 20 {
				return fmt.Errorf("%s carries a pubkey hash that is not 20 bytes of hex", what)
			}
		}
	case ChainZNN:
		if l.Token != "" {
			if _, err := znn.ParseTokenStandard(l.Token); err != nil {
				return fmt.Errorf("%s: %w", what, err)
			}
		}
		if l.Addr != "" {
			if _, err := znn.ParseAddress(l.Addr); err != nil {
				return fmt.Errorf("%s: %w", what, err)
			}
		}
	case ChainSOL:
		if l.Addr != "" {
			if _, err := sol.ParsePubkey(l.Addr); err != nil {
				return fmt.Errorf("%s: %w", what, err)
			}
		}
		if l.Program != "" {
			if _, err := sol.ParsePubkey(l.Program); err != nil {
				return fmt.Errorf("%s names a program that is not a Solana address: %w", what, err)
			}
		}
		if l.SwapID != "" {
			raw, err := hex.DecodeString(l.SwapID)
			if err != nil || len(raw) != 32 {
				return fmt.Errorf("%s carries a Solana swap id that is not 32 bytes of hex", what)
			}
		}
	}
	return nil
}

// checkTypedAmount refuses an amount that is not one, in the unit its chain is
// typed in. Bitcoin is whole satoshi; Solana is SOL to nine places; a Zenon
// amount cannot be fully checked without the token's decimals, so only its shape
// is checked here and the conversion refuses later if it does not fit.
func checkTypedAmount(c ChainID, _ string, amount string) error {
	amount = strings.TrimSpace(amount)
	if amount == "" || strings.ContainsAny(amount, "-eE ") {
		return fmt.Errorf("%q is not an amount", amount)
	}
	switch c {
	case ChainBTC:
		v, ok := new(big.Int).SetString(amount, 10)
		if !ok || v.Sign() <= 0 {
			return fmt.Errorf("%q is not a whole number of satoshi", amount)
		}
	case ChainSOL:
		if _, err := solLamports(amount); err != nil {
			return err
		}
	case ChainZNN:
		if normalDecimal(amount) == "" {
			return fmt.Errorf("%q is not an amount this can read", amount)
		}
	}
	return nil
}

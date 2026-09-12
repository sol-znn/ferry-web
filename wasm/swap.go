package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/zenon/ferry-web/wasm/znn"
)

// Role says whether this side generated the secret. The initiator must take the
// longer timelock: they learn nothing until the participant acts, whereas the
// participant is exposed to the initiator sitting on the secret until the last
// moment.
type Role string

const (
	RoleInitiator   Role = "initiator"
	RoleParticipant Role = "participant"
)

// Leg says which way the Bitcoin moves for this user.
type Leg string

const (
	// LegSend means the user funds the Bitcoin contract and the counterparty
	// redeems it. The user holds a refund key.
	LegSend Leg = "send"
	// LegReceive means the counterparty funds the Bitcoin contract and the
	// user redeems it. The user holds a redeem key.
	LegReceive Leg = "receive"
)

// State is the swap's position in its lifecycle.
type State string

const (
	StateDraft           State = "draft"            // waiting on counterparty details
	StateAwaitingFunding State = "awaiting_funding" // contract known, no coins yet
	StateFunded          State = "funded"           // contract output seen on chain
	StateRedeemed        State = "redeemed"         // claimed with the secret
	StateRefunded        State = "refunded"         // reclaimed after the timelock
	StateExpired         State = "expired"          // timelock passed, refundable
)

// Swap is the full record of one side of one swap. It is the unit of
// persistence, and everything needed to recover funds is inside it.
type Swap struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	// Version counts saves. Every call into this module runs on a goroutine of
	// its own and most of them load a record, decide something, and save it;
	// two of those interleaved would let the second save erase the first --
	// a refresh recording funding, then an audit that loaded before it saving
	// a contract over it, and the freeze on a funded contract with it. So Save
	// refuses a record whose version is not the one in the store, and the
	// loser is told to look again rather than allowed to overwrite.
	Version int64  `json:"version,omitempty"`
	Network string `json:"network"`
	Role    Role   `json:"role"`
	Leg     Leg    `json:"leg"`
	State   State  `json:"state"`

	// Secret is set when this side is the initiator, or once it has been
	// extracted from the counterparty's on-chain redeem. It is the one field
	// that must never leave this machine before the swap is claimed.
	Secret     []byte `json:"secret,omitempty"`
	SecretHash []byte `json:"secretHash"`

	// Key is this side's ephemeral keypair: the refund key when funding the
	// contract, the redeem key when claiming it.
	Key *SwapKey `json:"key"`

	// CounterpartyPKH is the other side's pubkey hash on the Bitcoin leg.
	CounterpartyPKH []byte `json:"counterpartyPkh,omitempty"`

	LockTime     int64  `json:"lockTime"`
	Contract     []byte `json:"contract,omitempty"`
	ContractAddr string `json:"contractAddr,omitempty"`
	AmountSats   int64  `json:"amountSats"`

	// DestAddr is where the user's Bitcoin should land. It belongs to the
	// user's own wallet; this service only ever writes an output to it.
	DestAddr string `json:"destAddr,omitempty"`

	Funding  *FundingOutput `json:"funding,omitempty"`
	RefundTx *SpendResult   `json:"refundTx,omitempty"`
	RedeemTx *SpendResult   `json:"redeemTx,omitempty"`

	// FundingBroadcast is a payment to the contract this browser has already
	// sent but has not yet seen come back off the chain. It is what closes the
	// window in which the card would otherwise still be offering to send one.
	FundingBroadcast *FundingBroadcast `json:"fundingBroadcast,omitempty"`

	Zenon ZenonLeg `json:"zenon"`

	// Archived is set by the user to move a finished swap out of the active
	// list. It exists because "terminal on Bitcoin" and "done" are not always
	// the same thing -- see Active.
	Archived bool `json:"archived,omitempty"`

	Events []Event `json:"events"`
}

// Active reports whether this swap still belongs in front of the user, as
// opposed to in the history list.
//
// Only the user files a swap away. It used to file itself the moment its
// Bitcoin leg went terminal, which meant the last thing a completed swap did was
// disappear: no summary of what had just happened, no confirmation, and the one
// screen holding its txids moved to a list the user had to already know about.
// What is left of a finished swap is a receipt, and a receipt is dismissed by
// the person reading it.
//
// The "is there anything left to do" half of that question did not go away -- it
// moved to Settled, which is what lets the page shrink a finished swap to a
// summary row without also hiding it.
func (s *Swap) Active() bool {
	return !s.Archived
}

// Settled reports that this swap is over: nothing on either chain is waiting on
// anybody, and all that is left is to file it. A settled swap still appears on
// the active page -- as a summary row with an Archive button -- until the user
// presses it.
//
// The subtle case is a contract this user funded that the counterparty has
// redeemed. On Bitcoin that is the end of the story, but not always of the swap:
// when this user is the PARTICIPANT, that redeem is the moment the preimage
// becomes public, which is when their own claim on the Zenon leg begins. Calling
// that settled would put a receipt in front of somebody who still has ZNN to
// collect, and stop the heartbeat that is watching for it.
func (s *Swap) Settled() bool {
	switch s.State {
	case StateRefunded:
		return true
	case StateRedeemed:
		if s.Leg == LegSend && s.Role == RoleParticipant {
			return s.Zenon.UnlockHash != ""
		}
		return true
	default:
		return false
	}
}

// ZenonLeg records the other half of the trade.
//
// ferry builds the calls that write it -- htlc.Create, Unlock and Reclaim are
// account blocks assembled in walletblock.go -- but it never signs one. A block
// leaves this module as bytes and is handed to the user's wallet, which signs
// with its own key, mines its own plasma and publishes through its own node.
// The same leg can still be driven from znn-cli; what the wallet path removes
// is the copying, not the choice.
type ZenonLeg struct {
	HtlcID      string `json:"htlcId,omitempty"`
	SelfAddress string `json:"selfAddress,omitempty"`
	PeerAddress string `json:"peerAddress,omitempty"`

	// TokenStandard is the token that was AGREED. It is a term of the trade, so
	// verification never overwrites it with the one the HTLC happens to hold --
	// doing that would let a counterparty substitute a token they issued
	// themselves and have the swap re-verify clean afterwards.
	TokenStandard string `json:"tokenStandard,omitempty"`
	// ObservedToken is the token the entry actually holds, recorded for display
	// so a rejection can name both halves of the mismatch.
	ObservedToken string `json:"observedToken,omitempty"`
	// ObservedHashLocked is the address the entry actually pays, read off the
	// chain when the HTLC was verified.
	//
	// Like ObservedToken it is a fact about the entry, never an expectation --
	// PeerAddress is the AGREED recipient and is what verification compares
	// against. What this is for is afterwards: an unlock deletes the entry, so
	// once the preimage exists there is nothing left to read the payout address
	// off, and knowing it turns the search for that unlock from a walk of every
	// call the contract has received into a filter. Recorded while the entry can
	// still be read, because that is the only time it can be.
	ObservedHashLocked string `json:"observedHashLocked,omitempty"`
	// Amount is in the token's base units, matching what the node's RPC
	// returns, and is what verification compares against.
	Amount string `json:"amount,omitempty"`
	// AmountDisplay is the decimal amount as a human types it, which is what
	// znn-cli expects. Keeping both avoids silently comparing a display amount
	// against a base-unit amount.
	AmountDisplay string `json:"amountDisplay,omitempty"`

	// ExpirationHours and ExpirationSeconds are the duration the HTLC on this
	// leg should be created with: hours for znn-cli, seconds for the block the
	// wallet signs. A plan at creation time, recalculated from the counterparty's
	// Bitcoin locktime once that is known, because the ordering requirement is
	// between the two legs rather than against a fixed clock.
	ExpirationHours   int `json:"expirationHours,omitempty"`
	ExpirationSeconds int `json:"expirationSeconds,omitempty"`

	ExpirationTime int64  `json:"expirationTime,omitempty"`
	HashType       int    `json:"hashType"`
	KeyMaxSize     int    `json:"keyMaxSize"`
	Verified       bool   `json:"verified"`
	VerifyError    string `json:"verifyError,omitempty"`
	// VerifyPending means the id has not been checked against the chain YET --
	// not that the check failed.
	//
	// Conflating the two made every HTLC published through the wallet wear a red
	// "not verified" badge for its first couple of momentums, blaming a mistyped
	// id or a node on the wrong network. A new account block is simply not
	// readable yet, which is the ordinary case after a create, not a fault.
	//
	// It covers both ways a check can fail to produce a verdict: the entry could
	// not be read at all, and the entry was read but a check needed to judge it
	// could not be RUN -- unreadable token metadata, say. VerifyError may be set
	// alongside it, saying which; what pending asserts is only that asking again
	// could still get a different answer, which is what keeps the timer in
	// useAutoRefresh asking.
	VerifyPending bool `json:"verifyPending,omitempty"`

	// UnlockHash is the transaction this browser published to unlock the
	// counterparty's HTLC and take its ZNN.
	//
	// It exists because an unlock leaves nothing to look at: the contract DELETES
	// the entry, so a settled leg and an id that never existed both answer "data
	// non existent". Without a record of having done it, the card could only
	// decide whether to keep offering the unlock from the Bitcoin side's state --
	// a different question, and wrong in the shape that matters: the user who
	// sends BTC and is owed ZNN, whose contract being redeemed ends the Bitcoin
	// story while their own ZNN sits in an HTLC nobody else can open.
	//
	// Records only what THIS browser did. A leg unlocked from znn-cli leaves it
	// empty, and the card goes on offering an unlock the contract will refuse --
	// better than hiding the button from somebody who has not unlocked at all.
	UnlockHash string `json:"unlockHash,omitempty"`

	// UnlockSeen is the mirror of UnlockHash for the side that did NOT publish
	// the unlock: the counterparty's unlock of this leg was found on the chain.
	//
	// Both sides need to know that this leg has been collected, and neither can
	// ask the contract, for the reason above -- the entry is deleted, so a
	// settled leg and an id that never existed answer identically. UnlockHash is
	// what this browser did; this is what it saw.
	//
	// Set only where the unlocking transaction itself was found: a block into the
	// HTLC contract carrying a preimage that hashes to this swap's hash. Never
	// inferred from merely holding the preimage, because one pasted in by hand
	// hashes just as correctly with nothing having been observed anywhere.
	UnlockSeen bool `json:"unlockSeen,omitempty"`
}

// unlockPayee is the address this swap's Zenon HTLC pays, as well as it can be
// known, for narrowing the search for the unlock that revealed the preimage.
//
// The agreed recipient is best: it is what the entry was checked against. The
// observed one is next, read off the entry when it was verified, which covers a
// swap created without the counterparty's Zenon address. Neither is required --
// an empty answer walks the contract's own chain unfiltered, which is slower and
// finds the same thing, because a preimage identifies itself by hashing.
//
// A hint, never a check: nothing here decides whether an unlock is this swap's,
// only which blocks are worth fetching first.
func (s *Swap) unlockPayee() string {
	if addr := strings.TrimSpace(s.Zenon.PeerAddress); addr != "" {
		return addr
	}
	return strings.TrimSpace(s.Zenon.ObservedHashLocked)
}

// AgreedToken is the token standard this swap's Zenon leg is denominated in.
// Blank means ZNN, as the creation form has always said. Resolved in one place
// because three decisions read it -- which token the HTLC must hold, whose
// decimals convert the agreed amount, and which token the printed command names
// -- and a record written before the field was defaulted still has to verify.
func (z ZenonLeg) AgreedToken() string {
	if z.TokenStandard == "" {
		return znn.ZnnTokenStandard
	}
	return z.TokenStandard
}

// ZnnCliMaxHours is the ceiling znn-cli enforces on htlc.create's
// expirationTime argument: whole hours, 1..24, rejecting anything outside.
//
// A CLI restriction, not a protocol one. go-zenon's HTLC contract stores an
// absolute expirationTime and bounds it only by not already being expired, and
// the Syrius extension signs any expiry -- so a Zenon leg longer than 24 hours
// is valid on chain and creatable, and ferry treats it as a note about tooling
// rather than an unsupported swap shape.
const ZnnCliMaxHours = 24

// MinLegGap is the smallest acceptable gap between the participant's leg
// expiring and the initiator's.
//
// The invariant every atomic swap rests on is that the INITIATOR's leg expires
// LAST. Claiming the participant's leg is what publishes the preimage, and the
// participant then needs time to use it -- otherwise the initiator could let
// their own leg expire, reclaim it, and still claim the participant's with the
// secret, taking both sides.
//
// Two hours is a floor, not a recommendation; the defaults in manager.go leave
// 24. It exists so a counterparty proposing a dangerously tight pair is refused
// rather than merely frowned at.
const MinLegGap = 2 * time.Hour

// MinLegRemaining is how much life an HTLC must have left to be worth acting
// on at all. An HTLC expiring within minutes is one whose creator reclaims it
// the moment the other side commits.
const MinLegRemaining = 2 * time.Hour

// ZenonLegIsInitiators reports whether the Zenon HTLC belongs to the initiator,
// i.e. whether it is the leg that must expire LAST. Whoever sends ZNN creates
// it: the counterparty when this user sends BTC, this user when they receive.
func (s *Swap) ZenonLegIsInitiators() bool {
	if s.Leg == LegSend {
		// The counterparty sends ZNN, so their role is the opposite of ours.
		return s.Role != RoleInitiator
	}
	return s.Role == RoleInitiator
}

// ZenonHtlcIsOurs reports whether this user is the one who must create the
// Zenon HTLC. Whoever sends ZNN creates it.
func (s *Swap) ZenonHtlcIsOurs() bool { return s.Leg == LegReceive }

// DeletionRisk says what deleting this record might cost, or "" when it costs
// nothing.
//
// Deleting destroys the ephemeral private key, which is the only key that can
// spend this swap's contract. Two shapes make that free: a redeemed or refunded
// swap, whose key has nothing left to unlock, and one with no contract address,
// where nothing was ever built.
//
// Everything else is a maybe, and a maybe is only useful to somebody told which
// one it is -- hence a sentence rather than a boolean. The awkward case is a
// swap archived by hand halfway through, which reaches the history page looking
// as settled as the rest and whose contract may have been funded since the last
// refresh.
//
// Deliberately not consulted: the Zenon leg. A ZNN HTLC is spent with a key
// this app never holds, so an unclaimed one is a reason to keep the secret --
// which the recovery file carries -- not a reason this delete is unsafe.
func (s *Swap) DeletionRisk() string {
	if s.State == StateRedeemed || s.State == StateRefunded {
		return ""
	}
	if s.ContractAddr == "" {
		return ""
	}
	if s.Funding != nil {
		return fmt.Sprintf("This swap's contract at %s holds %d sat that this browser has not "+
			"seen spent.", s.ContractAddr, s.Funding.Value)
	}
	return fmt.Sprintf("This swap has a contract at %s that was never seen funded -- but a "+
		"payment arriving after the last refresh would not be in this record.", s.ContractAddr)
}

// SecretArrivesOnZenon reports whether the preimage reaches this user on the
// Zenon leg rather than on Bitcoin.
//
// True for exactly one shape: the participant who created the Zenon HTLC. They
// do not invent the secret and their Bitcoin contract is the one they claim, so
// the only place the preimage becomes visible to them is the counterparty's
// unlock -- which deletes the entry as it publishes it.
func (s *Swap) SecretArrivesOnZenon() bool {
	return s.ZenonHtlcIsOurs() && s.Role == RoleParticipant
}

// RedeemRevealsSecret reports whether this side's Bitcoin redeem would be the
// FIRST publication of the secret. That is the initiator on the receiving leg:
// the secret was generated here and has been nowhere else, and the redeem puts
// it in the spending transaction's signature script, on a public chain -- which
// is exactly what lets the counterparty unlock the Zenon HTLC this user locked
// for them.
//
// The participant on the same leg is the opposite case. Their secret came off
// the counterparty's Zenon unlock, so it is already public and their redeem
// gives nothing further away.
func (s *Swap) RedeemRevealsSecret() bool {
	return s.Leg == LegReceive && s.Role == RoleInitiator
}

// FundingShort reports that the contract holds less than was agreed.
func (s *Swap) FundingShort() bool {
	return s.Funding != nil && s.Funding.Value < s.AmountSats
}

// RedeemHeldForShortFunding is the one rule behind both the missing button and
// the refusal in Manager.Redeem: a short contract must not be redeemed while
// the redeem is what would publish the secret. Redeeming it trades the full
// Zenon amount, which the counterparty can then unlock, for a partial Bitcoin
// payment whose size the counterparty chose. Auto Mode reaches the same line
// and stops there.
func (s *Swap) RedeemHeldForShortFunding() bool {
	return s.FundingShort() && s.RedeemRevealsSecret()
}

// commitConfirmations is how deep the counterparty's Bitcoin funding has to be
// before this side locks ZNN against it.
//
// One block is the threshold that matters: a payment in the mempool can be
// replaced by its sender for the cost of a slightly higher fee, so locking ZNN
// against one lets an initiator who already holds the secret take the ZNN and
// keep the BTC. Once mined, replacing it means mining a competing block. A
// deeper requirement buys protection against a reorg at the cost of ten minutes
// a block, on a swap that runs for a day or two; the constant is here so that
// trade can be changed in one place.
const commitConfirmations = 1

// ZenonCreateWaitsOnBtc reports whether this side's Zenon HTLC is created
// AGAINST the counterparty's Bitcoin funding -- the participant's leg, in a
// swap the Bitcoin side initiated. That is the one shape where locking ZNN is a
// response to money already on the other chain, and so the one shape where it
// must wait for that money to be real. When the Zenon leg is the initiator's it
// goes first by design and there is no Bitcoin funding to wait for.
func (s *Swap) ZenonCreateWaitsOnBtc() bool {
	return s.ZenonHtlcIsOurs() && s.BitcoinLegIsInitiators()
}

// FundingCommitBlocker says why the counterparty's Bitcoin funding is not yet
// something to lock ZNN against, or "" when it is, judged from the record as of
// the last refresh. Empty for every shape where nothing is waited on. The card
// shows it in place of the create; planCreate refuses on it and then re-reads
// the chain, because a record is only as current as the last poll.
func (s *Swap) FundingCommitBlocker() string {
	if !s.ZenonCreateWaitsOnBtc() {
		return ""
	}
	f := s.Funding
	switch {
	case f == nil:
		return "their Bitcoin funding has not been seen at the contract yet"
	case s.State == StateRedeemed || s.State == StateRefunded:
		return "the contract output has already been spent"
	case s.State == StateExpired:
		return "the Bitcoin contract's timelock has passed, so their funding is theirs to take back"
	case s.LockTime > 0 && s.LockTime-time.Now().Unix() < int64((MinLegGap+MinLegRemaining)/time.Second):
		// A Zenon leg created now would have to expire before the Bitcoin
		// contract by MinLegGap and still live MinLegRemaining, and there is no
		// longer room for both. planCreate refuses this against the chain's
		// clock; the record refuses it against the browser's, which is enough
		// to withhold a button and a command.
		return fmt.Sprintf("the Bitcoin locktime is %s away, too close to fit a Zenon leg that "+
			"expires before it", time.Duration(s.LockTime-time.Now().Unix())*time.Second)
	case f.Value < s.AmountSats:
		return fmt.Sprintf("the contract holds %d sat but %d sat was agreed", f.Value, s.AmountSats)
	case !f.Confirmed:
		return "their funding is still in the mempool, where the sender can replace it"
	case f.Confirmations < commitConfirmations:
		return fmt.Sprintf("their funding has %d of the %d confirmation%s this needs",
			f.Confirmations, commitConfirmations, plural(commitConfirmations))
	}
	return ""
}

// FundingCommitted is FundingCommitBlocker's verdict as a bool: true whenever
// nothing stands between this side and creating its Zenon HTLC on account of
// the Bitcoin funding -- including every shape where that funding is not
// waited on at all.
func (s *Swap) FundingCommitted() bool { return s.FundingCommitBlocker() == "" }

// MissingZenonTerms lists the terms of the Zenon leg this swap never recorded
// and so cannot check an HTLC against: the address the HTLC must pay (this
// user's own for an incoming HTLC, the counterparty's for one this user
// creates) and the agreed amount. The addresses and the amount are optional at
// creation because they are routinely filled in later; they are not optional
// for a verdict. An expectation left blank is not a check switched off but a
// check with no answer -- an HTLC paying anybody at all would pass it -- so
// every route to `verified` refuses while this is non-empty, a verdict stored
// by an earlier release against incomplete terms is withdrawn on load, and
// planUnlock will not pack the preimage over one.
func (s *Swap) MissingZenonTerms() []MissingTerm {
	var missing []MissingTerm
	if s.ZenonHtlcIsOurs() {
		if strings.TrimSpace(s.Zenon.PeerAddress) == "" {
			missing = append(missing, MissingTerm{"peerAddress",
				"this swap records no Zenon address for the counterparty, the one your HTLC must pay"})
		}
	} else if strings.TrimSpace(s.Zenon.SelfAddress) == "" {
		missing = append(missing, MissingTerm{"selfAddress",
			"this swap records no Zenon address of your own, the one their HTLC must pay"})
	}
	amount := strings.TrimSpace(s.Zenon.AmountDisplay)
	switch {
	case amount == "":
		missing = append(missing, MissingTerm{"amount", "this swap records no agreed Zenon amount"})
	case zeroAmount(amount):
		// A lower bound of zero is no lower bound: one base unit would pass it.
		missing = append(missing, MissingTerm{"amount", "this swap's agreed Zenon amount is zero"})
	}
	return missing
}

// MissingTerm is one term of the Zenon leg a swap never recorded: which field,
// so the card can offer the right input, and why, so a refusal can say.
type MissingTerm struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

// missingReasons is the reasons alone, for a message.
func missingReasons(ms []MissingTerm) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Reason)
	}
	return out
}

// zeroAmount reports whether a plain-decimal amount is nothing at all.
func zeroAmount(s string) bool { return strings.Trim(s, "0.") == "" }

// withdrawStaleVerdict takes back a `verified` that was reached against
// incomplete terms. The release that had this finding could persist such a
// verdict, and a record survives an upgrade in localStorage and in a backup;
// the flag is what planUnlock trusts, so it cannot be allowed to outlive the
// rule. Applied on every load, idempotently, and said once in the log.
func (s *Swap) withdrawStaleVerdict() {
	if !s.Zenon.Verified {
		return
	}
	missing := s.MissingZenonTerms()
	if len(missing) == 0 {
		return
	}
	s.Zenon.Verified = false
	s.Zenon.VerifyPending = false
	s.Zenon.VerifyError = "verified by an earlier release against incomplete terms: " +
		strings.Join(missingReasons(missing), "; ") + ". Add them to the swap and verify again"
	msg := "withdrew a verification reached against incomplete terms; the HTLC must be verified again"
	if !loggedOnce(s, msg) {
		s.log("%s", msg)
	}
// ContractCommitted reports whether anything has been staked on this swap's
// Bitcoin contract as it stands -- money seen or sent to its address, a Zenon
// HTLC created against its locktime, a refund pre-signed to spend its output
// -- and says what. Past that point the contract's bytes are the swap's
// identity: a "corrected" contract arriving afterwards, over a session or by
// hand, would be stored beside a funding outpoint that still pays the OLD
// script, and every spend built from then on would be for the wrong one. So
// AuditContract and SetCounterpartyPKH treat a byte-identical resend as
// nothing new and refuse anything else once this is true.
func (s *Swap) ContractCommitted() (bool, string) {
	switch {
	case len(s.Contract) == 0:
		return false, ""
	case s.Funding != nil:
		return true, "funding has been seen at its address"
	case s.FundingBroadcast != nil:
		return true, "a payment to its address has been sent from this browser"
	case s.RefundTx != nil:
		return true, "a refund of it has been pre-signed"
	case strings.TrimSpace(s.Zenon.HtlcID) != "":
		return true, "a Zenon HTLC exists against its locktime"
	case s.State != StateDraft && s.State != StateAwaitingFunding:
		return true, "the swap is past waiting for funding"
	}
	return false, ""
}

// FundingBound reports that the funding output has been read off its own
// transaction and pays this swap's contract. It is what releases anything
// built or offered against the funding: a redeem, a pre-signed refund, a
// Zenon leg locked in answer to it. A script recorded is not enough on its
// own; it has to be THIS contract's, judged now, against the contract the
// record holds now.
func (s *Swap) FundingBound() bool {
	if s.Funding == nil || s.Funding.PkScriptHex == "" || len(s.Contract) == 0 {
		return false
	}
	params, err := s.Params()
	if err != nil {
		return false
	}
	bound, err := s.Funding.bindsTo(s.Contract, params)
	return err == nil && bound
}

// BitcoinLegIsInitiators reports whether this swap's Bitcoin contract is the
// initiator's leg, i.e. the one that must expire LAST. It is the mirror of
// ZenonLegIsInitiators: exactly one leg of a swap is the initiator's.
func (s *Swap) BitcoinLegIsInitiators() bool { return !s.ZenonLegIsInitiators() }

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

// Params returns the chain parameters for this swap's network.
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

// BuildSwapContract fills in the contract once both the secret hash and the
// counterparty's pubkey hash are known. Which pubkey hash goes in which branch
// is decided by the leg, and getting it backwards would hand the funds to the
// wrong party, so it is derived here rather than left to callers.
func (s *Swap) BuildSwapContract() error {
	if len(s.SecretHash) == 0 {
		return errors.New("secret hash is not set yet")
	}
	if len(s.CounterpartyPKH) != 20 {
		return errors.New("counterparty pubkey hash is not set yet")
	}
	if s.Key == nil {
		return errors.New("swap key is missing")
	}
	if s.LockTime == 0 {
		return errors.New("locktime is not set")
	}
	params, err := s.Params()
	if err != nil {
		return err
	}

	var pkhRefund, pkhRedeem []byte
	switch s.Leg {
	case LegSend:
		// We fund it, so we hold the refund key and they redeem with the secret.
		pkhRefund, pkhRedeem = s.Key.PKH, s.CounterpartyPKH
	case LegReceive:
		// They fund it, so they hold the refund key and we redeem with the secret.
		pkhRefund, pkhRedeem = s.CounterpartyPKH, s.Key.PKH
	default:
		return fmt.Errorf("unknown leg %q", s.Leg)
	}

	contract, err := BuildContract(pkhRefund, pkhRedeem, s.LockTime, s.SecretHash)
	if err != nil {
		return err
	}
	addr, err := ContractAddress(contract, params)
	if err != nil {
		return err
	}
	s.Contract = contract
	s.ContractAddr = addr.String()
	if s.State == StateDraft {
		s.State = StateAwaitingFunding
	}
	s.log("contract built, address %s, locktime %s", s.ContractAddr,
		time.Unix(s.LockTime, 0).UTC().Format(time.RFC3339))
	return nil
}

// Offer is the public metadata one side sends to the other to set a swap up.
//
// It is a separate type from Swap on purpose: the secret and the private key
// live on Swap and have no field here, so there is no code path that can
// serialise them into something the user is told to paste into a chat window.
type Offer struct {
	Version    int    `json:"v"`
	Network    string `json:"network"`
	FromRole   Role   `json:"fromRole"`
	SecretHash string `json:"secretHash"`
	// PKH is the sender's Bitcoin pubkey hash for whichever contract branch
	// they will need.
	PKH string `json:"pkh"`
	// BTCLeg describes the sender's direction, so the receiver takes the other.
	BTCLeg     Leg    `json:"btcLeg"`
	AmountSats int64  `json:"amountSats"`
	LockTime   int64  `json:"lockTime,omitempty"`
	ZenonAddr  string `json:"zenonAddr,omitempty"`
	ZenonToken string `json:"zenonToken,omitempty"`
	ZenonAmt   string `json:"zenonAmt,omitempty"`
	Note       string `json:"note,omitempty"`
}

// Encode renders the offer as a single copy-pasteable string.
func (o *Offer) Encode() (string, error) {
	raw, err := json.Marshal(o)
	if err != nil {
		return "", err
	}
	return "swapoffer1:" + base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeOffer parses a string produced by Encode.
func DecodeOffer(s string) (*Offer, error) {
	const prefix = "swapoffer1:"
	if len(s) <= len(prefix) || s[:len(prefix)] != prefix {
		return nil, errors.New("not a swap offer string (expected a swapoffer1: prefix)")
	}
	raw, err := base64.RawURLEncoding.DecodeString(s[len(prefix):])
	if err != nil {
		return nil, fmt.Errorf("offer is not valid base64: %w", err)
	}
	var o Offer
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("offer is not valid JSON: %w", err)
	}
	if o.Version != 1 {
		return nil, fmt.Errorf("unsupported offer version %d", o.Version)
	}
	// Every field is checked, not just that the hex parses. An offer is a
	// stranger's input, and the two values derived from it -- which leg and which
	// role the receiver takes -- are what the whole timelock ordering hangs off.
	// Leg.Opposite maps anything unrecognised to send, so an unvalidated btcLeg
	// would quietly tell the receiver to take the wrong side of the trade.
	h, err := hex.DecodeString(o.SecretHash)
	if err != nil {
		return nil, fmt.Errorf("offer has a malformed secret hash: %w", err)
	}
	if len(h) != sha256.Size {
		return nil, fmt.Errorf("offer's secret hash is %d bytes, want %d", len(h), sha256.Size)
	}
	pkh, err := hex.DecodeString(o.PKH)
	if err != nil {
		return nil, fmt.Errorf("offer has a malformed pubkey hash: %w", err)
	}
	if len(pkh) != 20 {
		return nil, fmt.Errorf("offer's pubkey hash is %d bytes, want 20", len(pkh))
	}
	if _, err := NetworkParams(o.Network); err != nil {
		return nil, fmt.Errorf("offer names an %w", err)
	}
	if o.BTCLeg != LegSend && o.BTCLeg != LegReceive {
		return nil, fmt.Errorf("offer's btcLeg is %q, must be %q or %q", o.BTCLeg, LegSend, LegReceive)
	}
	if o.FromRole != RoleInitiator && o.FromRole != RoleParticipant {
		return nil, fmt.Errorf("offer's fromRole is %q, must be %q or %q",
			o.FromRole, RoleInitiator, RoleParticipant)
	}
	if o.AmountSats <= 0 {
		return nil, fmt.Errorf("offer's amountSats is %d, which is not an amount", o.AmountSats)
	}
	if err := canonicalZenonAmount(o.ZenonAmt); err != nil {
		return nil, fmt.Errorf("offer's %w", err)
	}
	if o.ZenonAmt != "" && zeroAmount(o.ZenonAmt) {
		return nil, errors.New("offer's Zenon amount is zero, which is not an amount")
	}
	return &o, nil
}

// zenonAmountShape is what a Zenon amount may look like: digits, optionally a
// point and more digits. Nothing else -- not a sign, not an exponent, not a
// thousands separator, and above all nothing that is not a character of a
// number. The amount is a term of the trade that reaches a printed command,
// and a control character inside it is how a stranger's offer becomes a line
// in somebody's terminal.
var zenonAmountShape = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)

// canonicalZenonAmount accepts an absent amount or a plain decimal, and
// nothing else. Checked where amounts come in -- a form, an offer -- rather
// than where they go out, so every later use can trust the shape.
func canonicalZenonAmount(s string) error {
	if s == "" || zenonAmountShape.MatchString(s) {
		return nil
	}
	return fmt.Errorf("the Zenon amount %q is not a plain decimal like 10 or 1.25", s)
}

// Opposite returns the leg the receiver of an offer should take.
func (l Leg) Opposite() Leg {
	if l == LegSend {
		return LegReceive
	}
	return LegSend
}

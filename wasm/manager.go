package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/zenon/ferry-web/wasm/chain"
	"github.com/zenon/ferry-web/wasm/znn"
)

// Manager holds the pieces a swap needs and implements the operations the API
// exposes. It owns no keys beyond what is inside each Swap record.
//
// One is assembled per call from the settings that call carried. The browser is
// the only party, so there is no operator whose node choice anybody would need
// to override.
type Manager struct {
	Store   *Store
	Chain   chain.Backend
	Znn     *znn.Client
	Network string
}

// CreateParams are the inputs for starting a new swap.
type CreateParams struct {
	Role       Role   `json:"role"`
	Leg        Leg    `json:"leg"`
	AmountSats int64  `json:"amountSats"`
	DestAddr   string `json:"destAddr"`
	LockHours  int    `json:"lockHours"`

	// SecretHashHex is required when this side is the participant: the
	// initiator chose the secret, so the participant only ever sees its hash.
	SecretHashHex string `json:"secretHashHex"`

	// CounterpartyPKHHex is the other side's Bitcoin pubkey hash. It is
	// required up front only when this side funds the contract.
	CounterpartyPKHHex string `json:"counterpartyPkhHex"`

	ZenonSelfAddress string `json:"zenonSelfAddress"`
	ZenonPeerAddress string `json:"zenonPeerAddress"`
	ZenonToken       string `json:"zenonToken"`
	// ZenonAmount is the decimal amount as typed (10, 1.25), converted to base
	// units with the token's own decimals when an HTLC is verified against it.
	ZenonAmount string `json:"zenonAmount"`

	// Offer is the swapoffer1 string this form was filled in from, when it was
	// filled in from one -- pasted by hand or delivered over a session, which are
	// the same string. Present, it makes the create a JOIN rather than a proposal,
	// and every term it pins is checked against the rest of these params. See
	// Offer.CheckCreate.
	Offer string `json:"offer"`
}

// The lock durations follow the convention the atomic swap literature uses: the
// initiator waits twice as long as the participant, who must be able to refund
// strictly first -- otherwise the initiator could reveal the secret at the last
// moment and claim both legs.
//
// These are per LEG, not per user. Which one a given user's Bitcoin contract
// takes depends on Swap.BitcoinLegIsInitiators; the Zenon leg takes the other.
const (
	initiatorLockHours   = 48
	participantLockHours = 24
)

// defaultLegGap is the gap ferry puts between the two legs when it computes an
// expiry itself. MinLegGap is the floor it will accept from a counterparty;
// this is what it asks for, and it is the difference between the two defaults
// above.
const defaultLegGap = time.Duration(initiatorLockHours-participantLockHours) * time.Hour

// btcLegHours is how long this swap's Bitcoin contract should run for.
func btcLegHours(sw *Swap) int {
	if sw.BitcoinLegIsInitiators() {
		return initiatorLockHours
	}
	return participantLockHours
}

// planZenonLeg records the duration the Zenon HTLC on this swap should be
// created with, derived from the Bitcoin locktime.
//
// Derived rather than fixed because the requirement is a relationship between
// the legs: the initiator's must outlive the participant's. So a counterparty
// who picked an unusual Bitcoin timelock still gets a correctly ordered Zenon
// leg rather than a hardcoded 24 hours.
func planZenonLeg(sw *Swap, now time.Time) {
	if sw.LockTime == 0 {
		return
	}
	if sw.Zenon.HtlcID != "" && sw.Zenon.Verified {
		// A verified HTLC exists; what it should have been is history, and rewriting
		// the advice under the user would only confuse. An HTLC that FAILED
		// verification is not that -- the advice is exactly what is needed to build a
		// replacement, so it keeps being produced.
		return
	}
	btc := time.Unix(sw.LockTime, 0)
	target := btc.Add(-defaultLegGap)
	if sw.ZenonLegIsInitiators() {
		target = btc.Add(defaultLegGap)
	}
	secs := int(target.Sub(now) / time.Second)
	if secs < int(MinLegRemaining/time.Second) {
		// Advising an expiry that verification would reject anyway is worse
		// than admitting there is no room. The Bitcoin leg is too close to
		// expiry to hang a usable Zenon leg off.
		return
	}
	sw.Zenon.ExpirationSeconds = secs
	sw.Zenon.ExpirationHours = wholeHours(secs, int(btc.Sub(now)/time.Second), sw.ZenonLegIsInitiators())
}

// wholeHours renders the planned expiry as the whole-hour figure znn-cli takes.
//
// Round to nearest, because a target 1 second under 24h should read as 24h --
// then clamp into the range that keeps MinLegGap between the legs, because
// rounding must never push the leg past the ordering constraint. Zero means no
// whole hour satisfies it and the caller should use the seconds form.
func wholeHours(secs, btcSecs int, isInitiatorLeg bool) int {
	hours := (secs + 1800) / 3600
	gap := int(MinLegGap / time.Second)
	if isInitiatorLeg {
		// Must expire after the Bitcoin leg: never round below the floor.
		if earliest := (btcSecs + gap + 3599) / 3600; hours < earliest {
			hours = earliest
		}
		return hours
	}
	// Must expire before the Bitcoin leg: never round above the ceiling.
	if latest := (btcSecs - gap) / 3600; hours > latest {
		hours = latest
	}
	if hours < 1 {
		return 0
	}
	return hours
}

// maxLockHours bounds a caller-supplied timelock. A year is far beyond any
// real swap and keeps the locktime inside the range nLockTime can carry.
const maxLockHours = 24 * 365

func legOwner(isInitiators bool) string {
	if isInitiators {
		return "initiator's"
	}
	return "participant's"
}

// noDestNote is emitted once for a funded swap that has no destination address.
// Swaps created now cannot reach that state -- Create requires one -- but a
// record imported from an older backup can, and it is the shape where the
// pre-signed refund is skipped without saying so.
const noDestNote = "WARNING: this swap records no Bitcoin destination address, so the refund " +
	"cannot be pre-signed and neither Redeem nor Refund can build a transaction. Supply one on " +
	"the card, or use the Recover page with the recovery file and an address of your own."

// loggedOnce reports whether msg is already in the event log, so a note emitted
// from Refresh -- which runs on every poll -- appears once rather than burying
// the log in copies of itself.
func loggedOnce(sw *Swap, msg string) bool {
	for i := range sw.Events {
		if sw.Events[i].Message == msg {
			return true
		}
	}
	return false
}

// Create starts a new swap and persists it.
func (m *Manager) Create(p CreateParams) (*Swap, error) {
	if p.Role != RoleInitiator && p.Role != RoleParticipant {
		return nil, fmt.Errorf("role must be %q or %q", RoleInitiator, RoleParticipant)
	}
	if p.Leg != LegSend && p.Leg != LegReceive {
		return nil, fmt.Errorf("leg must be %q or %q", LegSend, LegReceive)
	}
	if p.AmountSats <= 0 {
		return nil, errors.New("amountSats must be positive")
	}
	params, err := NetworkParams(m.Network)
	if err != nil {
		return nil, err
	}
	// Before anything is generated, and before the destination address is even
	// looked at: a swap that disagrees with the offer it answers is not a swap
	// with a fixable field, it is the wrong swap, and the cheapest moment to
	// say so is the one where nothing exists yet.
	if strings.TrimSpace(p.Offer) != "" {
		offer, err := DecodeOffer(strings.TrimSpace(p.Offer))
		if err != nil {
			return nil, fmt.Errorf("the offer this was filled in from no longer decodes: %w", err)
		}
		if err := offer.CheckCreate(m.Network, p); err != nil {
			return nil, err
		}
	}
	// Required, not optional. Both branches of the contract pay this address and
	// there is no other. Without one the refund cannot be pre-signed when
	// funding appears, and neither Redeem nor Refund can build anything -- which
	// is a swap that can be funded and then not spent, discovered after the
	// money is already in the contract.
	p.DestAddr = strings.TrimSpace(p.DestAddr)
	if p.DestAddr == "" {
		return nil, errors.New("a Bitcoin destination address is required: it is where both " +
			"branches of the contract pay, so a swap without one can be funded but not spent")
	}
	if _, err := addressScript(p.DestAddr, params); err != nil {
		return nil, err
	}

	// The three Zenon fields are all optional here -- the addresses are routinely
	// filled in later and a blank token means ZNN -- but a value that IS given has
	// to be the kind of thing the field asks for. A Bitcoin address, a Zenon
	// address and a Zenon token standard are all bech32, and the last two start
	// with the same letter, which is exactly the shape of accident a paste between
	// similar-looking fields produces.
	zenonSelf := strings.TrimSpace(p.ZenonSelfAddress)
	if zenonSelf != "" {
		if _, err := znn.ParseAddress(zenonSelf); err != nil {
			return nil, fmt.Errorf("your Zenon address: %w", err)
		}
	}
	zenonPeer := strings.TrimSpace(p.ZenonPeerAddress)
	if zenonPeer != "" {
		if _, err := znn.ParseAddress(zenonPeer); err != nil {
			return nil, fmt.Errorf("their Zenon address: %w", err)
		}
	}
	// The creation form's blank token field means ZNN. Resolving it now rather
	// than at verification time means the swap record, the printed command and
	// the check all name the same token, and the user can see which one.
	zenonTokenIn := strings.TrimSpace(p.ZenonToken)
	if zenonTokenIn != "" {
		if _, err := znn.ParseTokenStandard(zenonTokenIn); err != nil {
			return nil, fmt.Errorf("Zenon token: %w", err)
		}
	}
	zenonToken := ZenonLeg{TokenStandard: zenonTokenIn}.AgreedToken()

	id, err := NewID()
	if err != nil {
		return nil, err
	}
	key, err := NewSwapKey()
	if err != nil {
		return nil, err
	}

	zenonAmount := strings.TrimSpace(p.ZenonAmount)
	if err := canonicalZenonAmount(zenonAmount); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	sw := &Swap{
		ID:         id,
		CreatedAt:  now,
		UpdatedAt:  now,
		Network:    m.Network,
		Role:       p.Role,
		Leg:        p.Leg,
		State:      StateDraft,
		Key:        key,
		AmountSats: p.AmountSats,
		DestAddr:   p.DestAddr,
		Zenon: ZenonLeg{
			SelfAddress:   zenonSelf,
			PeerAddress:   zenonPeer,
			TokenStandard: zenonToken,
			AmountDisplay: zenonAmount,
			HashType:      znn.HashTypeSHA256,
			KeyMaxSize:    SecretSize,
		},
	}

	switch p.Role {
	case RoleInitiator:
		secret, hash, err := NewSecret()
		if err != nil {
			return nil, err
		}
		sw.Secret, sw.SecretHash = secret, hash
		sw.log("generated secret, hash %s", hex.EncodeToString(hash))
	case RoleParticipant:
		if p.SecretHashHex == "" {
			return nil, errors.New("participant needs the initiator's secret hash")
		}
		hash, err := hex.DecodeString(p.SecretHashHex)
		if err != nil || len(hash) != 32 {
			return nil, errors.New("secretHashHex must be 32 bytes of hex")
		}
		sw.SecretHash = hash
		sw.log("using initiator's secret hash %s", p.SecretHashHex)
	}

	// The Bitcoin locktime belongs to whichever leg Bitcoin is, not to this
	// user's role: when the user receives BTC the contract is the
	// counterparty's, so it takes the counterparty's duration. For that case
	// this is only a placeholder -- AuditContract replaces it with the real
	// locktime out of the contract they publish.
	hours := p.LockHours
	if hours <= 0 {
		hours = btcLegHours(sw)
	}
	if hours < 1 || hours > maxLockHours {
		return nil, fmt.Errorf("lockHours must be between 1 and %d", maxLockHours)
	}
	sw.LockTime = now.Add(time.Duration(hours) * time.Hour).Unix()
	if err := validLockTime(sw.LockTime); err != nil {
		return nil, err
	}
	planZenonLeg(sw, now)

	// A Zenon leg longer than 24 hours cannot be created with znn-cli, which caps
	// its expirationTime argument. Nothing on chain forbids it, so this is a note
	// about tooling rather than an unsupported swap -- said at creation time
	// instead of after Bitcoin has been funded.
	if sw.Zenon.ExpirationHours > ZnnCliMaxHours {
		sw.log("NOTE: the Zenon leg of this swap runs for %dh, which is over znn-cli's "+
			"1..%dh cap on htlc.create. Use the Syrius extension for that leg; the cap "+
			"is a CLI restriction, not a protocol one.",
			sw.Zenon.ExpirationHours, ZnnCliMaxHours)
	}

	if p.CounterpartyPKHHex != "" {
		pkh, err := hex.DecodeString(p.CounterpartyPKHHex)
		if err != nil || len(pkh) != 20 {
			return nil, errors.New("counterpartyPkhHex must be 20 bytes of hex")
		}
		sw.CounterpartyPKH = pkh
	}

	// When this side funds the contract and already knows the counterparty's
	// pubkey hash, the contract can be built immediately.
	if sw.Leg == LegSend && len(sw.CounterpartyPKH) == 20 {
		if err := sw.BuildSwapContract(); err != nil {
			return nil, err
		}
	}

	sw.log("swap created: role=%s leg=%s amount=%d sat locktime=%s (bitcoin is the %s leg, "+
		"zenon the %s leg, planned zenon expiry %ds)",
		sw.Role, sw.Leg, sw.AmountSats, time.Unix(sw.LockTime, 0).UTC().Format(time.RFC3339),
		legOwner(sw.BitcoinLegIsInitiators()), legOwner(sw.ZenonLegIsInitiators()),
		sw.Zenon.ExpirationSeconds)
	if err := m.Store.Save(sw); err != nil {
		return nil, err
	}
	return sw, nil
}

// SetCounterpartyPKH supplies the other side's pubkey hash and builds the
// contract. Used by the side that funds the Bitcoin contract.
func (m *Manager) SetCounterpartyPKH(id, pkhHex string) (*Swap, error) {
	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, err
	}
	if sw.Leg != LegSend {
		return nil, errors.New("only the side funding the Bitcoin contract sets the counterparty pubkey hash; " +
			"the receiving side should submit the counterparty's contract instead")
	}
	pkh, err := hex.DecodeString(strings.TrimSpace(pkhHex))
	if err != nil || len(pkh) != 20 {
		return nil, errors.New("pubkey hash must be 20 bytes of hex")
	}
	sw.CounterpartyPKH = pkh
	if err := sw.BuildSwapContract(); err != nil {
		return nil, err
	}
	if err := m.Store.Save(sw); err != nil {
		return nil, err
	}
	return sw, nil
}

// AuditContract accepts the contract published by the counterparty and checks
// that it actually pays this user under the agreed terms.
//
// The single most important check in the flow for the receiving side: a
// contract committing to the wrong hash, the wrong pubkey hash or too short a
// locktime is one whose funds are not really claimable, and the only moment to
// catch that is before anything is sent on the other leg.
func (m *Manager) AuditContract(id, contractHex string) (*Swap, error) {
	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, err
	}
	if sw.Leg != LegReceive {
		return nil, errors.New("only the receiving side audits a counterparty contract")
	}
	contract, err := hex.DecodeString(strings.TrimSpace(contractHex))
	if err != nil {
		return nil, fmt.Errorf("contract must be hex: %w", err)
	}
	if sw.Key == nil {
		return nil, errors.New("this swap record has no key, so nothing could redeem the contract")
	}
	details, err := ParseContract(contract)
	if err != nil {
		return nil, err
	}
	params, err := sw.Params()
	if err != nil {
		return nil, err
	}

	if !bytes.Equal(details.PkhRedeem, sw.Key.PKH) {
		return nil, fmt.Errorf("contract lets %s redeem, not this swap's key %s: "+
			"you would not be able to claim these funds",
			hex.EncodeToString(details.PkhRedeem), sw.Key.PKHHex())
	}
	if len(sw.SecretHash) > 0 && !bytes.Equal(details.SecretHash, sw.SecretHash) {
		return nil, fmt.Errorf("contract commits to secret hash %s, this swap uses %s",
			hex.EncodeToString(details.SecretHash), hex.EncodeToString(sw.SecretHash))
	}
	if len(sw.SecretHash) == 0 {
		sw.SecretHash = details.SecretHash
	}

	// The two legs have to be ordered so the INITIATOR's expires last, and this
	// is the moment the counterparty's Bitcoin locktime becomes a fact rather
	// than an assumption. Checking it here is the last point before the user
	// commits anything on Zenon.
	remaining := time.Until(time.Unix(details.LockTime, 0))
	if err := auditLegOrdering(sw, details.LockTime, remaining); err != nil {
		return nil, err
	}

	addr, err := ContractAddress(contract, params)
	if err != nil {
		return nil, err
	}
	sw.Contract = contract
	sw.ContractAddr = addr.String()
	sw.LockTime = details.LockTime
	sw.CounterpartyPKH = details.PkhRefund
	if sw.State == StateDraft {
		sw.State = StateAwaitingFunding
	}
	planZenonLeg(sw, time.Now().UTC())
	sw.log("audited counterparty contract %s: redeemable by our key, locktime %s (%s away), "+
		"which is the %s leg",
		sw.ContractAddr, time.Unix(details.LockTime, 0).UTC().Format(time.RFC3339),
		remaining.Truncate(time.Minute), legOwner(sw.BitcoinLegIsInitiators()))
	if sw.ZenonHtlcIsOurs() && sw.Zenon.ExpirationSeconds > 0 && sw.Zenon.HtlcID == "" {
		sw.log("create your Zenon HTLC with an expiry of %ds (%dh) so it lands on the correct "+
			"side of this contract's locktime",
			sw.Zenon.ExpirationSeconds, sw.Zenon.ExpirationHours)
	}

	if err := m.Store.Save(sw); err != nil {
		return nil, err
	}
	return sw, nil
}

// auditLegOrdering enforces the invariant the whole swap rests on: the
// initiator's leg expires LAST. Otherwise the initiator could let their own leg
// expire, reclaim it, and still claim the participant's leg with the secret --
// taking both sides. The direction of the check depends on which leg the
// audited contract is, which is a property of this user's role.
func auditLegOrdering(sw *Swap, btcLockTime int64, remaining time.Duration) error {
	if remaining < MinLegRemaining {
		return fmt.Errorf("contract locktime is only %s away, under the %s minimum needed to "+
			"act on it; refuse it and ask the counterparty to rebuild with a longer timelock",
			remaining.Truncate(time.Minute), MinLegRemaining)
	}
	if sw.BitcoinLegIsInitiators() {
		// Their Bitcoin contract is the long leg and our Zenon HTLC goes under
		// it, so it has to be long enough to fit one that is still worth acting
		// on after the gap is taken out.
		if need := MinLegGap + MinLegRemaining; remaining < need {
			return fmt.Errorf("contract locktime is %s away, but this is the initiator's leg: "+
				"a Zenon leg must fit under it, expiring at least %s earlier and still leaving %s "+
				"to act, so at least %s is needed. Ask for a longer timelock",
				remaining.Truncate(time.Minute), MinLegGap, MinLegRemaining, need)
		}
		return nil
	}
	// Their Bitcoin contract is the short leg, so our Zenon HTLC -- created first,
	// being the initiator's -- has to outlive it. An expiry recorded from an HTLC
	// that failed verification is not a fact about this swap: treat it as absent.
	if sw.Zenon.ExpirationTime == 0 || !sw.Zenon.Verified {
		// Nothing to compare against yet. Say so rather than passing silently:
		// a skipped check reads exactly like a passed one.
		sw.log("NOTE: this contract is the participant's leg and must expire before your Zenon "+
			"HTLC, but ferry has not seen that HTLC yet so it could not check. Verify it "+
			"(Verify their HTLC) and re-audit before funding anything. Its expiry must be "+
			"after %s.",
			time.Unix(btcLockTime+int64(MinLegGap/time.Second), 0).UTC().Format(time.RFC3339))
		return nil
	}
	if earliest := btcLockTime + int64(MinLegGap/time.Second); sw.Zenon.ExpirationTime < earliest {
		return fmt.Errorf("this contract expires at %s but your Zenon HTLC expires at %s: "+
			"as the initiator yours must expire LAST, by at least %s. Accepting this would let "+
			"the counterparty reclaim on Zenon and still redeem your Bitcoin. Ask them to "+
			"rebuild with a locktime no later than %s",
			time.Unix(btcLockTime, 0).UTC().Format(time.RFC3339),
			time.Unix(sw.Zenon.ExpirationTime, 0).UTC().Format(time.RFC3339),
			MinLegGap,
			time.Unix(sw.Zenon.ExpirationTime-int64(MinLegGap/time.Second), 0).UTC().Format(time.RFC3339))
	}
	return nil
}

// Refresh polls the chain and advances the swap's state.
//
// It is safe to call repeatedly and is the only thing that moves a swap
// forward automatically: funding detected, refund transaction pre-signed,
// counterparty redeem spotted and the secret extracted from it.
func (m *Manager) Refresh(ctx context.Context, id string) (*Swap, error) {
	backend := m.Chain
	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, err
	}
	if len(sw.Contract) == 0 {
		return sw, nil // nothing on chain to look at yet
	}
	params, err := sw.Params()
	if err != nil {
		return nil, err
	}

	// Look for the funding output. Keep looking while what we have does not cover
	// the agreed amount and nothing has been spent yet, so a dust payment that
	// arrived first cannot permanently stand in for the real funding.
	if fundingIsOpen(sw) {
		utxos, err := backend.AddressUTXOs(ctx, sw.ContractAddr)
		if err != nil {
			return nil, fmt.Errorf("look up contract address: %w", err)
		}
		best := pickFunding(utxos, sw.AmountSats)
		changed := best != nil && (sw.Funding == nil || best.Value > sw.Funding.Value)
		if changed {
			if sw.Funding == nil {
				sw.State = StateFunded
				sw.log("funding seen: %s:%d for %d sat (confirmed=%v)",
					best.TxID, best.Vout, best.Value, best.Status.Confirmed)
			} else {
				sw.log("a larger output appeared at the contract address: switching funding from "+
					"%s:%d (%d sat) to %s:%d (%d sat) and re-signing the refund",
					sw.Funding.TxID, sw.Funding.Vout, sw.Funding.Value,
					best.TxID, best.Vout, best.Value)
				sw.RefundTx = nil // it spent the output we just stopped using
			}
			sw.Funding = &FundingOutput{
				TxID:  best.TxID,
				Vout:  best.Vout,
				Value: best.Value,
				// The listing already carries the block, so the first depth is
				// free. Height alone is not depth -- that needs the tip, which
				// trackFundingDepth below fetches.
				Confirmed:   best.Status.Confirmed,
				BlockHeight: best.Status.BlockHeight,
			}

			// Only on a change: this block runs on every refresh while the
			// contract is short, and repeating these lines would bury the
			// event log in copies of themselves.
			if len(utxos) > 1 {
				sw.log("NOTE: the contract address holds %d unspent outputs. Only the one above is "+
					"spent by a redeem or refund; anything else must be recovered separately with "+
					"`ferry recover` after editing the funding outpoint.", len(utxos))
			}
			if sw.Funding.Value < sw.AmountSats {
				sw.log("WARNING: contract holds %d sat but %d sat was agreed; do not proceed until "+
					"this is resolved", sw.Funding.Value, sw.AmountSats)
			}
		}
	}

	// How deep the funding is. Answering "has it arrived" without answering "is
	// it settled" is what leaves somebody acting on a payment that is still one
	// double-spend away from never having happened.
	trackFundingDepth(ctx, sw, backend)

	// A funded contract with nowhere to pay is the one shape where the pre-signed
	// refund silently does not happen. Say it on the card rather than leaving the
	// user with a recovery file that quietly has no transaction in it.
	if sw.Funding != nil && sw.Leg == LegSend && sw.DestAddr == "" && !loggedOnce(sw, noDestNote) {
		sw.log("%s", noDestNote)
	}

	// As soon as funding exists and this side holds the refund key, pre-sign
	// the refund so the user can save it. Recovery must not depend on ferry
	// still being around later.
	if sw.Funding != nil && sw.RefundTx == nil && sw.Leg == LegSend && sw.DestAddr != "" {
		feeRate, err := backend.FeeRate(ctx, 6)
		if err != nil {
			feeRate = 2.0
		}
		refund, err := BuildRefund(sw.Contract, *sw.Funding, sw.Key, sw.DestAddr, feeRate, params)
		if err != nil {
			sw.log("could not pre-sign refund: %v", err)
		} else {
			sw.RefundTx = refund
			sw.log("refund transaction pre-signed (%s, %d sat fee); it becomes broadcastable at %s",
				refund.TxID, refund.Fee, time.Unix(sw.LockTime, 0).UTC().Format(time.RFC3339))
		}
	}

	// Has the contract output been spent?
	if sw.Funding != nil && sw.State != StateRedeemed && sw.State != StateRefunded {
		spend, err := backend.OutspendOf(ctx, sw.Funding.TxID, sw.Funding.Vout)
		if err == nil && spend.Spent && spend.TxID != "" {
			if err := m.absorbSpend(ctx, sw, spend.TxID, backend); err != nil {
				sw.log("could not inspect spending transaction %s: %v", spend.TxID, err)
			}
		}
	}

	// Pick the preimage up off Zenon, for the one shape that cannot get it from
	// Bitcoin: when the secret arrives on the Zenon leg, this user created the
	// HTLC and the counterparty unlocks it. Unlocking DELETES the entry, but the
	// transaction that did it is in the ledger forever, which is what turns
	// pasting the 64 characters they revealed from a step into a fallback.
	if m.Znn != nil && sw.SecretArrivesOnZenon() && len(sw.Secret) == 0 {
		if secret, ferr := m.Znn.FindPreimage(ctx, sw.unlockPayee(), sw.SecretHash,
			preimageSearchPages, preimageSearchPageSize); ferr == nil {
			sw.Secret = secret
			// Finding it IS the observation that the leg was unlocked -- nothing
			// else puts a matching preimage in a block sent to the contract -- and
			// it is the only one this side will ever get, so it is written down
			// rather than re-derived from holding a secret that may have been
			// pasted in instead.
			sw.Zenon.UnlockSeen = true
			sw.log("preimage recovered from the counterparty's Zenon unlock and checked against "+
				"this swap's hash: %s", hex.EncodeToString(secret))
		} else if !loggedOnce(sw, noPreimageYetNote) {
			// Once, not on every refresh: before they unlock, this failing is
			// the normal state of the swap, not news.
			sw.log("%s", noPreimageYetNote)
		}
	}

	// Flag an expired timelock. What that means depends on the leg: the funder
	// gains the refund branch, while the redeemer loses their safe window and
	// is now racing the funder.
	if sw.State == StateFunded && time.Now().Unix() >= sw.LockTime {
		sw.State = StateExpired
		if sw.Leg == LegSend {
			sw.log("timelock reached; the refund transaction can now be broadcast (Bitcoin " +
				"compares it against the block median time past, which trails real time by " +
				"about an hour, so the network may refuse it for a little longer)")
		} else {
			sw.log("timelock reached; this contract can now be refunded by the counterparty. " +
				"Redeeming is still possible but is now a race -- do it immediately or treat " +
				"the Bitcoin leg as lost and reclaim your Zenon leg instead")
		}
	}

	if err := m.Store.Save(sw); err != nil {
		return nil, err
	}
	return sw, nil
}

// How far back to look for a counterparty's unlock. An unlock is one of the
// last things the unlocking address does in a swap, so it is near the top of
// their chain; ten pages of fifty survives a busy account without turning a
// refresh into a ledger crawl.
const (
	preimageSearchPages    = 10
	preimageSearchPageSize = 50
)

// noPreimageYetNote is logged once, because before the counterparty unlocks,
// not finding a preimage is the swap working normally rather than a problem.
const noPreimageYetNote = "watching the counterparty's Zenon address for the unlock that reveals " +
	"the preimage; it will be picked up here automatically, and can still be pasted in by hand"

// confirmationsTracked is how deep this app keeps counting.
//
// Six is the figure the rest of Bitcoin settled on for not coming back, and it
// is where the number stops being interesting: nothing here behaves differently
// at seven, so spending a request per refresh on it would buy nothing.
const confirmationsTracked = 6

// trackFundingDepth updates how many blocks are on top of the funding.
//
// Separate from the scan above because the two stop at opposite moments: that
// scan gives up as soon as the contract holds the agreed amount, which is
// exactly when this starts. Quiet about failure on purpose -- a depth that
// could not be read is a stale number on a card, not a swap in trouble, and an
// event line per poll would bury the funding notice under it.
func trackFundingDepth(ctx context.Context, sw *Swap, backend chain.Backend) {
	if sw.Funding == nil || sw.Funding.Confirmations >= confirmationsTracked {
		return
	}
	status, err := backend.TxStatus(ctx, sw.Funding.TxID)
	if err != nil {
		return
	}
	sw.Funding.Confirmed = status.Confirmed
	sw.Funding.BlockHeight = status.BlockHeight
	if !status.Confirmed || status.BlockHeight <= 0 {
		// Still in the mempool. Zeroed rather than left alone, because a
		// funding that was mined and then reorganised out has to stop claiming
		// the depth it used to have.
		sw.Funding.Confirmations = 0
		return
	}
	tip, err := backend.TipHeight(ctx)
	if err != nil || tip < status.BlockHeight {
		return
	}
	// Counting the block itself, which is what everyone else means by one
	// confirmation.
	sw.Funding.Confirmations = min(tip-status.BlockHeight+1, confirmationsTracked)
}

// RecordFundingBroadcast notes that a payment to the contract has been sent.
//
// It records nothing beyond the id and the time: this is not evidence that the
// contract is funded, and treating it as such would let anyone who can call it
// mark a swap funded without paying. Refresh remains the only thing that can
// say a contract holds money.
func (m *Manager) RecordFundingBroadcast(id, txid string) (*Swap, error) {
	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, err
	}
	txid = strings.TrimSpace(txid)
	if !chain.ValidTxID(txid) {
		return nil, fmt.Errorf("%q is not a transaction id", txid)
	}
	if sw.Leg != LegSend {
		return nil, errors.New("only the side funding the Bitcoin contract broadcasts a funding")
	}
	if sw.FundingBroadcast != nil && sw.FundingBroadcast.TxID == txid {
		return sw, nil // the same send, reported twice
	}
	sw.FundingBroadcast = &FundingBroadcast{TxID: txid, At: time.Now().UTC()}
	sw.log("funding broadcast as %s; waiting for a node to list it against the contract address", txid)
	if err := m.Store.Save(sw); err != nil {
		return nil, err
	}
	return sw, nil
}

// fundingIsOpen reports whether Refresh should still be looking for a better
// funding output. The contract address is public, so anyone can add outputs to
// it; settling for a stray dust payment would leave the real funding stranded.
func fundingIsOpen(sw *Swap) bool {
	if sw.State == StateRedeemed || sw.State == StateRefunded {
		return false
	}
	if sw.RedeemTx != nil {
		return false
	}
	return sw.Funding == nil || sw.Funding.Value < sw.AmountSats
}

// pickFunding chooses which output at the contract address is the funding.
//
// Taking whatever the backend listed first would let a single dust payment
// become the funding: the swap would look funded, the pre-signed refund would
// spend a few hundred satoshis, and the real payment would sit untouched.
func pickFunding(utxos []chain.UTXO, want int64) *chain.UTXO {
	var best *chain.UTXO
	for i := range utxos {
		u := &utxos[i]
		if u.Value <= 0 {
			continue
		}
		if best == nil {
			best = u
			continue
		}
		if covers, bestCovers := u.Value >= want, best.Value >= want; covers != bestCovers {
			if covers {
				best = u
			}
			continue
		}
		if u.Value > best.Value {
			best = u
		}
	}
	return best
}

// absorbSpend inspects the transaction that spent the contract output and
// works out whether it was a redeem (in which case it carries the secret) or a
// refund.
func (m *Manager) absorbSpend(ctx context.Context, sw *Swap, txid string, backend chain.Backend) error {
	rawHex, err := backend.RawTx(ctx, txid)
	if err != nil {
		return err
	}
	tx, err := DecodeRawTx(rawHex)
	if err != nil {
		return err
	}
	// The backend named this transaction as the one that spent our output. Check
	// that it actually does before letting it decide the swap's state: a wrong
	// answer here files a live swap away as settled, which takes it off the
	// active list and stops Refresh looking for the real spend.
	spendsOurs := false
	sigScripts := make([][]byte, 0, len(tx.TxIn))
	for _, in := range tx.TxIn {
		sigScripts = append(sigScripts, in.SignatureScript)
		if in.PreviousOutPoint.Index == sw.Funding.Vout &&
			in.PreviousOutPoint.Hash.String() == sw.Funding.TxID {
			spendsOurs = true
		}
	}
	if !spendsOurs {
		return fmt.Errorf("the node named %s as the spender of %s:%d, but that transaction does "+
			"not spend it; ignoring rather than acting on it",
			txid, sw.Funding.TxID, sw.Funding.Vout)
	}

	secret, err := ExtractSecret(sigScripts, sw.SecretHash)
	if err != nil {
		sw.State = StateRefunded
		sw.log("contract output was spent by %s and no input reveals a preimage matching this "+
			"swap's hash, so it was a refund", txid)
		return nil
	}

	if len(sw.Secret) == 0 {
		sw.Secret = secret
		sw.log("SECRET LEARNED from %s: %s -- use it to claim the Zenon leg before that HTLC expires",
			txid, hex.EncodeToString(secret))
	}
	sw.State = StateRedeemed
	sw.log("contract output was redeemed by %s", txid)
	return nil
}

// SetSecret records a preimage the user observed elsewhere.
//
// When this side sends BTC the counterparty's redeem reveals the preimage on
// Bitcoin and Refresh picks it up. When this side receives BTC they unlock the
// Zenon HTLC instead -- and unlocking deletes the entry, so it cannot be read
// back from getById. Ferry finds it in the unlocking transaction; this is the
// fallback for when that search comes up empty.
//
// Only accepted if it hashes to this swap's secret hash, so a wrong or
// malicious paste cannot get a broken transaction built.
func (m *Manager) SetSecret(id, secretHex string) (*Swap, error) {
	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, err
	}
	secret, err := hex.DecodeString(strings.TrimSpace(secretHex))
	if err != nil {
		return nil, fmt.Errorf("secret must be hex: %w", err)
	}
	if len(secret) != SecretSize {
		return nil, fmt.Errorf("secret must be %d bytes, got %d", SecretSize, len(secret))
	}
	if !bytes.Equal(SHA256(secret), sw.SecretHash) {
		return nil, fmt.Errorf("that preimage hashes to %s, but this swap commits to %s",
			hex.EncodeToString(SHA256(secret)), hex.EncodeToString(sw.SecretHash))
	}
	sw.Secret = secret
	sw.log("secret supplied by hand and verified against the contract's hash")
	if err := m.Store.Save(sw); err != nil {
		return nil, err
	}
	return sw, nil
}

// Archive files a swap away into the history list, or brings it back.
//
// Nothing about it touches the chain or the swap's funds: it only decides which
// of the two lists the swap shows up in.
func (m *Manager) Archive(id string, archived bool) (*Swap, error) {
	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, err
	}
	if sw.Archived == archived {
		return sw, nil
	}
	sw.Archived = archived
	if archived {
		sw.log("archived: moved to the history list")
	} else {
		sw.log("un-archived: moved back to the active list")
	}
	if err := m.Store.Save(sw); err != nil {
		return nil, err
	}
	return sw, nil
}

// payoutAddress settles where a spend pays, and refuses to be talked out of it.
//
// Both ways out of the contract pay one address of this user's, chosen when the
// swap was created, and the card says so in as many words: this address and no
// other. Until this, both spend calls took a destination argument that quietly
// won, so that sentence was a description of the usual case rather than a rule.
//
// Bitcoin cannot enforce it. The redeem branch authorises a KEY -- the script
// ends in CHECKSIG against a pubkey hash, with no output constraint anywhere in
// it (see BuildContract) -- so a valid signature is the whole of what a spend
// needs, and it may pay wherever it likes. That is exactly why the refusal has
// to live here: it is the only place both spends pass through, and the call is
// the same call whether it came from a button on the card, from an automatic
// action, or from a console.
//
// The one address a record adopts is its first. A swap restored from an old
// backup carries none, and without one nothing can build a spend at all.
//
// Somebody who genuinely must pay elsewhere is not trapped, and is not meant to
// be: the recovery file exports the key that spends this contract, and building
// that transaction outside this app is the documented way out. What is refused
// is doing it silently, to a swap already committed to an address.
func payoutAddress(sw *Swap, asked string) (string, error) {
	asked = strings.TrimSpace(asked)
	if sw.DestAddr == "" {
		if asked == "" {
			return "", errors.New("a destination address is required")
		}
		return asked, nil
	}
	if asked != "" && asked != sw.DestAddr {
		return "", fmt.Errorf("this swap pays %s, fixed when it was created, and cannot be "+
			"redirected to %s -- both ways out of the contract pay that address and no other. "+
			"To spend these coins somewhere else, export the recovery file and build the "+
			"transaction yourself", sw.DestAddr, asked)
	}
	return sw.DestAddr, nil
}

// Redeem claims a contract by revealing the secret, then broadcasts it.
func (m *Manager) Redeem(ctx context.Context, id, destAddr string) (*Swap, error) {
	backend := m.Chain
	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, err
	}
	if sw.Leg != LegReceive {
		return nil, errors.New("only the receiving side redeems the Bitcoin contract")
	}
	if sw.Funding == nil {
		return nil, errors.New("no funding output has been seen yet; refresh first")
	}
	if len(sw.Secret) == 0 {
		return nil, errors.New("the secret is not known yet, so this contract cannot be redeemed")
	}
	destAddr, err = payoutAddress(sw, destAddr)
	if err != nil {
		return nil, err
	}
	if now := time.Now().Unix(); now >= sw.LockTime {
		sw.log("WARNING: redeeming after the timelock at %s. The counterparty can broadcast "+
			"their refund now, so this is a race -- if it loses, treat the Bitcoin leg as gone.",
			time.Unix(sw.LockTime, 0).UTC().Format(time.RFC3339))
	}
	params, err := sw.Params()
	if err != nil {
		return nil, err
	}
	feeRate, err := backend.FeeRate(ctx, 3)
	if err != nil {
		feeRate = 2.0
	}
	spend, err := BuildRedeem(sw.Contract, *sw.Funding, sw.Key, sw.Secret, destAddr, feeRate, params)
	if err != nil {
		return nil, err
	}
	txid, err := backend.Broadcast(ctx, spend.RawHex)
	if err != nil {
		return nil, fmt.Errorf("broadcast redeem: %w", err)
	}
	sw.RedeemTx = spend
	sw.State = StateRedeemed
	// Record the address a keyless old backup was just given, so the swap names
	// what it actually paid. payoutAddress has already refused anything that
	// disagrees with an address the record held, so this only ever fills a gap.
	if sw.DestAddr == "" {
		sw.DestAddr = destAddr
	}
	sw.log("redeemed to %s in %s (%d sat after a %d sat fee)", destAddr, txid, spend.Value, spend.Fee)
	if err := m.Store.Save(sw); err != nil {
		return nil, err
	}
	return sw, nil
}

// Refund broadcasts the timelocked reclaim. It rebuilds the transaction at
// current fee rates if one was not pre-signed.
func (m *Manager) Refund(ctx context.Context, id, destAddr string) (*Swap, error) {
	backend := m.Chain
	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, err
	}
	if sw.Leg != LegSend {
		return nil, errors.New("only the side that funded the contract can refund it")
	}
	if sw.Funding == nil {
		return nil, errors.New("no funding output has been seen yet; refresh first")
	}
	if now := time.Now().Unix(); now < sw.LockTime {
		return nil, fmt.Errorf("the timelock does not expire until %s (%s from now)",
			time.Unix(sw.LockTime, 0).UTC().Format(time.RFC3339),
			time.Until(time.Unix(sw.LockTime, 0)).Truncate(time.Second))
	}

	destAddr, err = payoutAddress(sw, destAddr)
	if err != nil {
		return nil, err
	}
	// The pre-signed refund, when there is one, was built to this same address
	// the moment funding appeared — the only address it could have been built
	// to, now that one cannot be swapped out from under it.
	spend := sw.RefundTx
	if spend == nil {
		params, err := sw.Params()
		if err != nil {
			return nil, err
		}
		feeRate, err := backend.FeeRate(ctx, 6)
		if err != nil {
			feeRate = 2.0
		}
		spend, err = BuildRefund(sw.Contract, *sw.Funding, sw.Key, destAddr, feeRate, params)
		if err != nil {
			return nil, err
		}
	}

	txid, err := backend.Broadcast(ctx, spend.RawHex)
	if err != nil {
		// CHECKLOCKTIMEVERIFY is evaluated against the block's median time
		// past, which trails real time by roughly an hour, so a refund
		// broadcast the moment the wall clock passes the locktime is normal to
		// see bounced. Saying so turns a confusing rejection into a wait.
		return nil, fmt.Errorf("broadcast refund: %w (if this says non-final, the locktime has "+
			"passed on your clock but not yet in the chain's median time past, which trails "+
			"real time by about an hour -- retry shortly)", err)
	}
	sw.RefundTx = spend
	sw.State = StateRefunded
	if sw.DestAddr == "" && destAddr != "" {
		sw.DestAddr = destAddr
	}
	sw.log("refunded in %s (%d sat after a %d sat fee)", txid, spend.Value, spend.Fee)
	if err := m.Store.Save(sw); err != nil {
		return nil, err
	}
	return sw, nil
}

// zenonVerifyParams builds the expectations this swap has of its Zenon HTLC.
//
// Everything role-dependent lives here, because each of these inverts between
// the two directions and getting one backwards either rejects a sound swap or
// accepts an unsound one:
//
//   - who may unlock, and who may reclaim, swap over with who created the HTLC;
//   - which side of the Bitcoin locktime the Zenon expiry must fall on depends
//     on which leg is the initiator's, not on which way the coins move.
func zenonVerifyParams(sw *Swap) znn.VerifyParams {
	want := znn.VerifyParams{
		SecretHashHex: hex.EncodeToString(sw.SecretHash),
		MinRemaining:  MinLegRemaining,
		// The agreed token, never the one the entry happens to name. Checking
		// the amount without it means checking that some quantity of SOMETHING
		// is locked, which a counterparty satisfies by issuing their own token.
		ExpectTokenStandard: sw.Zenon.AgreedToken(),
	}
	// Checking "recipient is me" against an HTLC this user created themselves
	// would fail every time, and would skip the check that actually matters
	// there: that it pays the counterparty.
	if sw.ZenonHtlcIsOurs() {
		want.ExpectRecipient = sw.Zenon.PeerAddress
		want.ExpectSender = sw.Zenon.SelfAddress
	} else {
		want.ExpectRecipient = sw.Zenon.SelfAddress
		want.ExpectSender = sw.Zenon.PeerAddress
	}
	// Terms never recorded cannot be checked, and a check that cannot run must
	// not read as one that passed. See Swap.MissingZenonTerms.
	want.MissingTerms = sw.MissingZenonTerms()
	// The initiator's leg must expire LAST, or one party can wait out a chain and
	// still act on the other. A blanket rule that Zenon expires first would reject
	// every swap in which Zenon is the initiating side. Only meaningful once the
	// Bitcoin contract exists.
	if len(sw.Contract) > 0 && sw.LockTime > 0 {
		gap := int64(MinLegGap / time.Second)
		if sw.ZenonLegIsInitiators() {
			want.MinExpiration = sw.LockTime + gap
		} else {
			want.MaxExpiration = sw.LockTime - gap
		}
	}
	return want
}

// zenonExpectations is zenonVerifyParams with the two things only a node can
// supply filled in: the chain's own clock, and the agreed amount in base units.
// One function rather than a step inside VerifyZenon because the search below
// verifies candidates through exactly the same expectations -- a shortlist held
// to laxer terms would be a way of accepting an HTLC by not typing it in.
func (m *Manager) zenonExpectations(ctx context.Context, sw *Swap) znn.VerifyParams {
	want := zenonVerifyParams(sw)
	// The contract compares expirationTime against momentum time, so the expiry
	// checks have to use the chain's clock rather than this machine's.
	if mom, merr := m.Znn.FrontierMomentum(ctx); merr == nil {
		want.Now = mom.Timestamp
	} else {
		want.Now = time.Now().Unix()
		sw.log("could not read the Zenon frontier momentum (%v); using local time for the expiry check", merr)
	}
	// The agreed amount is the decimal string the user typed and the node reports
	// base units. Convert with the decimals of the token that was AGREED, not the
	// one the entry names -- that is the counterparty's choice, and can be a token
	// they issued this morning. Every failure sets AmountUncheckable rather than
	// leaving MinAmount nil, so a comparison that did not run cannot come back as
	// a matching amount.
	// A missing or zero amount is already among MissingTerms; only a real one
	// is converted.
	if amount := strings.TrimSpace(sw.Zenon.AmountDisplay); amount != "" && strings.Trim(amount, "0.") != "" {
		agreed := sw.Zenon.AgreedToken()
		if tok, terr := m.Znn.GetToken(ctx, agreed); terr != nil {
			want.AmountUncheckable = fmt.Sprintf("could not read token %s from the node: %v",
				agreed, terr)
		} else if amt, cerr := decimalToBaseUnits(sw.Zenon.AmountDisplay, tok.Decimals); cerr != nil {
			want.AmountUncheckable = fmt.Sprintf("the agreed amount %q could not be interpreted: %v",
				sw.Zenon.AmountDisplay, cerr)
		} else {
			want.MinAmount = amt
		}
	}
	return want
}

// SetZenonTerms completes the Zenon terms a swap was created without: this
// user's own address, the counterparty's, the agreed amount. Each can be SET
// where it is blank and never changed where it is not -- they are terms of the
// trade, and a term that can be edited after the fact is one that can be
// edited to match whatever the counterparty locked. Filling one in resets the
// leg's verification, because a verdict reached against fewer terms is not a
// verdict against these.
//
// Values that equal what is already recorded are accepted silently, so a page
// that submits the whole form need not work out which fields it changed.
func (m *Manager) SetZenonTerms(ctx context.Context, id, self, peer, amount string) (*Swap, error) {
	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, err
	}
	self, peer, amount = strings.TrimSpace(self), strings.TrimSpace(peer), strings.TrimSpace(amount)
	changed := false
	set := func(name string, current *string, value string, check func(string) error) error {
		if value == "" || strings.EqualFold(*current, value) {
			return nil
		}
		if *current != "" {
			return fmt.Errorf("%s is already %s on this swap and cannot be changed to %s: it is a "+
				"term of the trade. To trade on different terms, create a new swap", name, *current, value)
		}
		if err := check(value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*current = value
		changed = true
		sw.log("%s set to %s", name, value)
		return nil
	}
	// An amount becomes immutable the moment it is recorded, so everything
	// that would make it unusable is checked first: the shape, that it is more
	// than nothing, and that the agreed token can represent it -- which needs
	// the token's decimals from a node, and without a node is a refusal.
	isAmount := func(v string) error {
		if err := canonicalZenonAmount(v); err != nil {
			return err
		}
		if strings.Trim(v, "0.") == "" {
			return fmt.Errorf("%q is zero, and a lower bound of zero is no lower bound", v)
		}
		if m.Znn == nil {
			return errors.New("the amount's precision has to be checked against the agreed token, " +
				"which needs a Zenon node: set one under Nodes and try again")
		}
		tok, err := m.Znn.GetToken(ctx, sw.Zenon.AgreedToken())
		if err != nil {
			return fmt.Errorf("could not read token %s from the node to check the amount's "+
				"precision: %w", sw.Zenon.AgreedToken(), err)
		}
		if _, err := decimalToBaseUnits(v, tok.Decimals); err != nil {
			return fmt.Errorf("%s cannot be locked in %s: %w", v, tok.Symbol, err)
		}
		return nil
	}
	isAddress := func(v string) error { _, err := znn.ParseAddress(v); return err }
	if err := set("your Zenon address", &sw.Zenon.SelfAddress, self, isAddress); err != nil {
		return nil, err
	}
	if err := set("the counterparty's Zenon address", &sw.Zenon.PeerAddress, peer, isAddress); err != nil {
		return nil, err
	}
	if err := set("the agreed Zenon amount", &sw.Zenon.AmountDisplay, amount, isAmount); err != nil {
		return nil, err
	}
	if changed && sw.Zenon.HtlcID != "" {
		sw.Zenon.Verified = false
		sw.Zenon.VerifyError = ""
		sw.Zenon.VerifyPending = false
		sw.log("the Zenon terms changed, so HTLC %s must be verified again", sw.Zenon.HtlcID)
	}
	if !changed {
		return sw, nil
	}
	if err := m.Store.Save(sw); err != nil {
		return nil, err
	}
	return sw, nil
}

// VerifyZenon fetches this swap's Zenon HTLC -- the counterparty's when they
// send ZNN, this user's own when they do -- and checks it against the terms the
// swap already committed to on the Bitcoin side. This is the check most worth
// running against a node you picked yourself: it is the one place a lying node
// could tell you an unsafe HTLC is fine.
func (m *Manager) VerifyZenon(ctx context.Context, id, htlcID string) (*Swap, *znn.HtlcInfo, error) {
	client := m.Znn
	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, nil, err
	}
	if client == nil {
		return nil, nil, errors.New("no Zenon node is set. Open Node settings and give this " +
			"browser a Zenon JSON-RPC URL it can reach")
	}
	htlcID = strings.TrimSpace(htlcID)
	if htlcID == "" {
		return nil, nil, errors.New("a Zenon HTLC id is required")
	}
	info, err := client.GetHtlcByID(ctx, htlcID)
	if err != nil {
		// "data non existent" is two opposite pieces of news wearing one error.
		// The contract deletes an entry when it is unlocked or reclaimed, so a
		// missing id means either that it was never there, or that the swap
		// worked. The ledger can tell them apart, because the id is the hash of
		// the transaction that created it.
		fate := client.WhatHappenedTo(ctx, htlcID)
		if fate.Existed {
			// It ran to completion. If it was unlocked, the preimage is public
			// in the unlocking transaction, and finding it is worth far more to
			// this user than the verification they asked for.
			if len(sw.Secret) == 0 && sw.SecretArrivesOnZenon() && sw.Zenon.PeerAddress != "" {
				if secret, ferr := client.FindPreimage(ctx, sw.Zenon.PeerAddress, sw.SecretHash,
					preimageSearchPages, preimageSearchPageSize); ferr == nil {
					sw.Secret = secret
					sw.Zenon.HtlcID = htlcID
					sw.Zenon.UnlockSeen = true
					sw.log("Zenon HTLC %s is gone from the contract because it was unlocked; the "+
						"preimage was read out of that transaction and checked against this "+
						"swap's hash: %s", htlcID, hex.EncodeToString(secret))
					if serr := m.Store.Save(sw); serr != nil {
						return nil, nil, serr
					}
					return sw, nil, fmt.Errorf("%s The preimage has been recovered and saved to "+
						"this swap -- you can redeem the Bitcoin contract now.", fate.Explanation)
				}
			}
		}
		return nil, nil, fmt.Errorf("%w. %s", err, fate.Explanation)
	}

	want := m.zenonExpectations(ctx, sw)
	want.ExpectID = htlcID

	verr := client.Verify(info, want)

	// Whether this answer is news, decided BEFORE the record is updated with it.
	// A leg awaiting a verdict is re-read on a timer and one arriving over a
	// session is verified on every delivery, so without this a swap accumulates a
	// page of the event that happened once.
	//
	// The verdict alone decides that, and not whether the leg was pending: a
	// refusal that keeps its pending flag (below) is re-read every minute, and
	// reading "FAILED verification" as news each time would write the same line
	// into the log until the node came back.
	repeat := sw.Zenon.HtlcID == htlcID &&
		sw.Zenon.Verified == (verr == nil) &&
		(verr == nil || sw.Zenon.VerifyError == verr.Error())

	// What the entry says is recorded for display either way -- the card needs it
	// to explain a rejection -- but the agreed terms are never overwritten by the
	// observed ones. TokenStandard in particular is a term of the trade, and
	// adopting whatever the counterparty locked is exactly what would make a
	// re-verification of a rejected HTLC pass.
	sw.Zenon.HtlcID = htlcID
	sw.Zenon.ObservedToken = info.TokenStandard
	sw.Zenon.ObservedHashLocked = info.HashLocked
	sw.Zenon.ExpirationTime = info.ExpirationTime
	sw.Zenon.HashType = info.HashType
	sw.Zenon.KeyMaxSize = info.KeyMaxSize
	sw.Zenon.Amount = info.Amount

	// The entry was read off the chain, so a verdict about it replaces a pending
	// one -- unless the refusal was made entirely of checks that could not be
	// RUN, which is not a verdict at all. That distinction is load-bearing well
	// beyond the badge on the card: `verified` is what releases the HTLC id to
	// the counterparty over a session, and what lets autopilot fund the
	// participant's leg. Recording a node's bad minute as an answer stops both
	// permanently, on a swap where nothing is actually wrong, and the only way
	// out is somebody noticing and pressing Send by hand.
	sw.Zenon.VerifyPending = verr != nil && znn.CheckIncomplete(verr)
	if verr != nil {
		sw.Zenon.Verified = false
		sw.Zenon.VerifyError = verr.Error()
		if !repeat {
			sw.log("Zenon HTLC %s FAILED verification: %v", htlcID, verr)
		}
	} else {
		sw.Zenon.Verified = true
		sw.Zenon.VerifyError = ""
		if !repeat {
			sw.log("Zenon HTLC %s verified: hashlock, parties, amount and expiry all match (it is the %s leg)",
				htlcID, legOwner(sw.ZenonLegIsInitiators()))
		}
	}
	if err := m.Store.Save(sw); err != nil {
		return nil, nil, err
	}
	return sw, info, verr
}

// How far back to look for the create this swap is waiting for. A counterparty
// creates their HTLC as part of the swap in progress, so it is among the most
// recent things their address has done.
const (
	htlcSearchPages    = 10
	htlcSearchPageSize = 50
)

// FindZenonHtlc locates this swap's Zenon HTLC without anybody typing its id.
//
// The id is the hash of the transaction that created the entry, so it cannot be
// derived -- but it can be found: the creating address is known, every create is
// a send to the HTLC contract, and its arguments carry the 32-byte hashlock
// verbatim. Every candidate then goes through the same Verify as an id typed in
// by hand, and a failure keeps its reason -- a create with the right hashlock
// and the wrong amount is a swap being short-changed, not a swap that has not
// started. The newest passing candidate wins.
func (m *Manager) FindZenonHtlc(ctx context.Context, id string) (*Swap, *znn.HtlcInfo, error) {
	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, nil, err
	}
	client := m.Znn
	if client == nil {
		return nil, nil, errors.New("no Zenon node is set. Open Node settings and give this " +
			"browser a Zenon JSON-RPC URL it can reach")
	}
	// Whoever sends the ZNN creates the HTLC, which is the same question
	// ZenonHtlcIsOurs answers for every other decision on this leg.
	creator := sw.Zenon.PeerAddress
	if sw.ZenonHtlcIsOurs() {
		creator = sw.Zenon.SelfAddress
	}
	if strings.TrimSpace(creator) == "" {
		return nil, nil, fmt.Errorf("this swap does not record the Zenon address that creates the "+
			"HTLC (%s side), so there is no chain to search. Paste the id in by hand instead",
			legOwner(sw.ZenonHtlcIsOurs()))
	}

	candidates, err := client.FindHtlcCreates(ctx, creator, sw.SecretHash,
		htlcSearchPages, htlcSearchPageSize)
	if err != nil {
		return nil, nil, fmt.Errorf("could not read %s's chain from the node: %w", creator, err)
	}
	if len(candidates) == 0 {
		return nil, nil, fmt.Errorf("nothing in the last %d transactions from %s creates an HTLC "+
			"locked to this swap's hash. Either it has not been created yet, it is not confirmed "+
			"yet -- a new one takes a couple of momentums -- or it was made from a different "+
			"address than the one this swap records",
			htlcSearchPages*htlcSearchPageSize, creator)
	}

	want := m.zenonExpectations(ctx, sw)
	var rejected []string
	for _, c := range candidates {
		info, gerr := client.GetHtlcByID(ctx, c.ID)
		if gerr != nil {
			// Gone from the contract: unlocked, reclaimed, or never confirmed.
			// It is not the entry to adopt, and saying so by id is what lets a
			// user tell "already settled" from "wrong hash".
			rejected = append(rejected, fmt.Sprintf("%s is no longer in the contract", c.ID))
			continue
		}
		w := want
		w.ExpectID = c.ID
		if verr := client.Verify(info, w); verr != nil {
			rejected = append(rejected, fmt.Sprintf("%s: %v", c.ID, verr))
			continue
		}
		// Found and checked. Going through VerifyZenon rather than recording it
		// here is deliberate: that is the one place that writes what an entry
		// says onto the swap, and having two would be two chances to disagree.
		return m.VerifyZenon(ctx, id, c.ID)
	}

	return nil, nil, fmt.Errorf("found %d HTLC%s locked to this swap's hash from %s, and none of "+
		"them matches the agreed terms -- %s",
		len(candidates), plural(len(candidates)), creator, strings.Join(rejected, "; "))
}

// plural is the "s" on a count, so a message can name one candidate or five
// without reading like a template.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// decimalToBaseUnits converts a human decimal amount ("1.25") into the token's
// integer base units, without going through a float.
func decimalToBaseUnits(s string, decimals int) (*big.Int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("amount is empty")
	}
	if strings.HasPrefix(s, "-") {
		return nil, errors.New("amount must not be negative")
	}
	if decimals < 0 || decimals > 30 {
		return nil, fmt.Errorf("token reports an implausible decimals value of %d", decimals)
	}

	intPart, frac, _ := strings.Cut(s, ".")
	if len(frac) > decimals {
		return nil, fmt.Errorf("amount has %d decimal places but the token has only %d",
			len(frac), decimals)
	}
	frac += strings.Repeat("0", decimals-len(frac))

	combined := intPart + frac
	if combined == "" {
		return nil, fmt.Errorf("%q is not a valid amount", s)
	}
	v, ok := new(big.Int).SetString(combined, 10)
	if !ok {
		return nil, fmt.Errorf("%q is not a valid decimal amount", s)
	}
	return v, nil
}

package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
)

// What a swap costs to get *out* of, worked out before the money goes in.
//
// A contract output is spent by exactly one transaction with one input and one
// output, and that transaction's fee comes out of the contract, not out of the
// spender's wallet. So the amount agreed is not the amount the recipient
// receives, and if the difference is large enough they receive nothing at all:
// below the dust limit no such transaction relays, and the refund branch meets
// the same arithmetic.
//
// That failure is only visible after funding, at the moment somebody tries to
// unlock. Everything here moves the discovery to before the money moves, using
// the signer's own sizing rather than a second opinion that could drift from it.

// feeSpikeMultiple and minHeadroomFeeRate size the recommended floor. A contract
// sits for the length of its timelock and the fee market at the end of that is
// not the one at the start, so the recommended minimum is "still clears dust
// after fees triple" rather than "clears dust today" -- and it never assumes a
// market calmer than minHeadroomFeeRate.
const (
	feeSpikeMultiple   = 3.0
	minHeadroomFeeRate = 10.0
)

// fallbackFeeRate is used when the node has no estimate to offer. It matches
// the fallback Manager.Redeem and Manager.Refund already apply, so a quote and
// the spend it describes cannot disagree about the rate.
const fallbackFeeRate = 2.0

// quoteConfTarget is the confirmation target quoted here: the same one
// Manager.Redeem asks for, because the redeem is the spend being quoted.
const quoteConfTarget = 3

// branchCost is one way out of a contract, priced.
type branchCost struct {
	VSize int   `json:"vsize"`
	Fee   int64 `json:"fee"`
	// Net is what lands at the destination. It can be negative: an amount
	// smaller than its own unlock fee is a real thing to be told about, and
	// clamping it at zero would hide how far short it falls.
	Net    int64 `json:"net"`
	Viable bool  `json:"viable"`
}

// SpendCost is the whole answer for one amount at one fee rate.
type SpendCost struct {
	AmountSats int64 `json:"amountSats"`
	DustLimit  int64 `json:"dustLimit"`

	FeeRate float64 `json:"feeRate"`
	// FeeRateFrom is "you", "node" or "fallback", so the UI can say whether a
	// quote reflects the live fee market or a guess made because no node
	// answered. A number whose provenance is invisible gets trusted too much.
	FeeRateFrom string `json:"feeRateFrom"`

	// DestAssumed marks a quote sized against a placeholder output rather than
	// a real address. The output script is 12 vB wider for a taproot or P2WSH
	// destination than for the P2WPKH most wallets hand out, which is worth a
	// few hundred satoshis in a fee spike and worth saying out loud.
	DestAssumed bool `json:"destAssumed"`
	// ContractAssumed marks a quote sized against the contract template rather
	// than a built contract. The template is fixed-length, so this differs only
	// by the one byte a post-2038 locktime adds.
	ContractAssumed bool `json:"contractAssumed"`

	Redeem branchCost `json:"redeem"`
	Refund branchCost `json:"refund"`

	// MinRelayable is the smallest amount that can be unlocked at all: below
	// it, no fee rate the network relays leaves a non-dust output.
	MinRelayable int64 `json:"minRelayable"`
	// MinAtFeeRate is the smallest amount that clears dust at FeeRate.
	MinAtFeeRate int64 `json:"minAtFeeRate"`
	// Recommended is MinAtFeeRate with room for the fee market to move while
	// the contract is locked. HeadroomRate is the rate it survives.
	Recommended  int64   `json:"recommended"`
	HeadroomRate float64 `json:"headroomRate"`

	// MaxFeeRate is the highest sat/vB the redeem can pay and still produce a
	// spendable output, which is the ceiling for the fee field on the Recover
	// page. Zero means there is no such rate.
	MaxFeeRate float64 `json:"maxFeeRate"`

	// Verdict is "ok", "tight" or "unspendable"; Summary says the same thing in
	// a sentence, written here so that every surface showing this says it the
	// same way.
	Verdict string `json:"verdict"`
	Summary string `json:"summary"`
}

// templateLockTime is the locktime the contract template is sized with when no
// real contract exists yet: far enough out to be a plausible swap, and read as
// a timestamp rather than a block height.
func templateLockTime() int64 { return time.Now().UTC().Add(48 * time.Hour).Unix() }

// sizingContract returns a contract to measure. A real one is used when there
// is one — on a funded swap there always is — and the template otherwise, since
// the script is fixed-length whatever the hashes inside it are.
func sizingContract(contractHex string) (script []byte, assumed bool, err error) {
	if h := strings.TrimSpace(contractHex); h != "" {
		script, err = hex.DecodeString(h)
		if err != nil {
			return nil, false, fmt.Errorf("bad contract hex: %w", err)
		}
		if _, err := ParseContract(script); err != nil {
			return nil, false, err
		}
		return script, false, nil
	}
	script, err = BuildContract(make([]byte, 20), make([]byte, 20), templateLockTime(), make([]byte, 32))
	if err != nil {
		return nil, false, err
	}
	return script, true, nil
}

// sizingDestScript returns the output script to measure against.
//
// With no address to go on it assumes a taproot output, the widest of the
// common forms. Assuming the narrowest would quote a fee that is too low, and a
// fee quote that is too low at the dust boundary is the exact mistake this file
// exists to prevent.
func sizingDestScript(addr string, params *chaincfg.Params) (script []byte, assumed bool, err error) {
	if a := strings.TrimSpace(addr); a != "" {
		script, err = addressScript(a, params)
		if err != nil {
			return nil, false, err
		}
		return script, false, nil
	}
	script, err = txscript.NewScriptBuilder().AddOp(txscript.OP_1).AddData(make([]byte, 32)).Script()
	if err != nil {
		return nil, false, err
	}
	return script, true, nil
}

// EstimateSpendCost prices both ways out of a contract holding amountSats.
func EstimateSpendCost(amountSats int64, destAddr, contractHex string, feeRate float64,
	feeRateFrom string, params *chaincfg.Params) (*SpendCost, error) {

	if amountSats <= 0 {
		return nil, errors.New("amountSats must be positive")
	}
	if !(feeRate > 0) || math.IsInf(feeRate, 0) || feeRate > maxFeeRate {
		return nil, fmt.Errorf("fee rate must be a positive number of sat/vB no greater than %d",
			maxFeeRate)
	}

	contract, contractAssumed, err := sizingContract(contractHex)
	if err != nil {
		return nil, err
	}
	destScript, destAssumed, err := sizingDestScript(destAddr, params)
	if err != nil {
		return nil, err
	}
	details, err := ParseContract(contract)
	if err != nil {
		return nil, err
	}

	// The secret's content never changes the size, only its presence: the
	// redeem branch pushes SecretSize bytes the refund branch does not.
	redeemVSize, err := spendVSize(contract, destScript, make([]byte, SecretSize), 0)
	if err != nil {
		return nil, err
	}
	refundVSize, err := spendVSize(contract, destScript, nil, details.LockTime)
	if err != nil {
		return nil, err
	}

	cost := func(vsize int, rate float64) branchCost {
		fee := feeFor(rate, vsize)
		net := amountSats - fee
		return branchCost{VSize: vsize, Fee: fee, Net: net, Viable: net >= dustLimit}
	}

	// The redeem is the larger of the two branches — it carries the preimage —
	// so it is the branch that decides whether an amount works at all. Every
	// floor below is sized on it, and an amount clearing the redeem clears the
	// refund by construction.
	headroom := math.Max(feeRate*feeSpikeMultiple, minHeadroomFeeRate)

	sc := &SpendCost{
		AmountSats:      amountSats,
		DustLimit:       dustLimit,
		FeeRate:         feeRate,
		FeeRateFrom:     feeRateFrom,
		DestAssumed:     destAssumed,
		ContractAssumed: contractAssumed,
		Redeem:          cost(redeemVSize, feeRate),
		Refund:          cost(refundVSize, feeRate),
		MinRelayable:    dustLimit + feeFor(minRelayFeeRate, redeemVSize),
		MinAtFeeRate:    dustLimit + feeFor(feeRate, redeemVSize),
		Recommended:     dustLimit + feeFor(headroom, redeemVSize),
		HeadroomRate:    headroom,
		MaxFeeRate:      maxViableFeeRate(amountSats, redeemVSize),
	}
	sc.Verdict, sc.Summary = verdictFor(sc)
	return sc, nil
}

// verdictFor turns the arithmetic into the one line a person actually reads.
func verdictFor(sc *SpendCost) (verdict, summary string) {
	switch {
	case sc.AmountSats < sc.MinRelayable:
		return "unspendable", fmt.Sprintf(
			"%d sat cannot be unlocked. Unlocking is a %d vB transaction, so even at the %g sat/vB "+
				"network minimum it leaves %d sat — under the %d sat dust limit, which no node "+
				"relays. Lock at least %d sat.",
			sc.AmountSats, sc.Redeem.VSize, minRelayFeeRate,
			sc.AmountSats-feeFor(minRelayFeeRate, sc.Redeem.VSize), sc.DustLimit, sc.MinRelayable)

	case !sc.Redeem.Viable:
		return "tight", fmt.Sprintf(
			"%d sat is too little at %g sat/vB: unlocking costs %d sat and leaves %d sat, under the "+
				"%d sat dust limit. It can still be unlocked at %g sat/vB or below, which may be "+
				"slow to confirm. Lock %d sat to unlock at today's rate, or %d sat to stay clear if "+
				"fees reach %g sat/vB.",
			sc.AmountSats, sc.FeeRate, sc.Redeem.Fee, sc.Redeem.Net, sc.DustLimit,
			sc.MaxFeeRate, sc.MinAtFeeRate, sc.Recommended, sc.HeadroomRate)

	case sc.AmountSats < sc.Recommended:
		return "tight", fmt.Sprintf(
			"%d sat works at today's %g sat/vB — unlocking costs %d sat and leaves %d sat — but it "+
				"stops working above %g sat/vB, and the contract is locked for hours. Lock %d sat to "+
				"stay clear up to %g sat/vB.",
			sc.AmountSats, sc.FeeRate, sc.Redeem.Fee, sc.Redeem.Net,
			sc.MaxFeeRate, sc.Recommended, sc.HeadroomRate)

	default:
		return "ok", fmt.Sprintf(
			"Unlocking costs about %d sat at %g sat/vB, so the recipient nets %d of the %d sat "+
				"locked. It keeps working up to %g sat/vB.",
			sc.Redeem.Fee, sc.FeeRate, sc.Redeem.Net, sc.AmountSats, sc.MaxFeeRate)
	}
}

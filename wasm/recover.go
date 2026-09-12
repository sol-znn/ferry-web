package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcutil"
)

// Offline rescue from a recovery file.
//
// A separate entry point because it must work when everything else does not.
// It reads no store,
// touches no node, and needs nothing but a recovery file the user saved earlier
// — so a copy of this site opened from a USB stick on an offline machine can
// still get money out of a contract. Keeping it free of chain access is what
// makes that true, and is why it prints hex rather than broadcasting.
//
// Where the CLI printed to stderr and stdout, this returns a struct: same
// decisions, same refusals, same explanations.

// recoveryFile is the subset of a downloaded recovery file this needs. It is
// deliberately tolerant: extra fields are ignored so a file written by a later
// version still works.
type recoveryFile struct {
	SwapID       string `json:"swapId"`
	Network      string `json:"network"`
	ContractHex  string `json:"contractHex"`
	ContractAddr string `json:"contractAddr"`
	LockTime     int64  `json:"lockTime"`
	PrivateKey   string `json:"privateKeyWIF"`
	SecretHex    string `json:"secretHex"`
	DestAddr     string `json:"destAddr"`

	Funding *FundingOutput `json:"funding"`

	PresignedRefundHex string `json:"presignedRefundHex"`
}

// RebuildRequest is one rescue attempt.
type RebuildRequest struct {
	// File is the recovery JSON, as text, exactly as it was downloaded.
	File string `json:"file"`
	// DestAddr overrides where the money lands. Empty uses the file's.
	DestAddr string `json:"destAddr"`
	// FeeRate is sat/vB. Zero uses the same 2.0 default the CLI had.
	FeeRate float64 `json:"feeRate"`
	// SecretHex builds a redeem instead of a refund. Empty falls back to a
	// secret already inside the file, if there is one.
	SecretHex string `json:"secretHex"`
	// AllowUnboundFunding builds even when the file does not record what the
	// funding output pays -- a file from before that was recorded. This page
	// reaches no node, so the binding cannot be read here; without it a spend
	// is built for the contract in the file and, if that is not the contract
	// the output pays, the network refuses it. Nothing is lost by trying, but
	// the user is told rather than left to find out.
	AllowUnboundFunding bool `json:"allowUnboundFunding"`
}

// RebuildResult is the rebuilt spend plus everything the CLI printed beside it.
type RebuildResult struct {
	// Action is "redeem" or "refund" — decided by the contract and the key,
	// not by what was asked for.
	Action       string  `json:"action"`
	SwapID       string  `json:"swapId"`
	Network      string  `json:"network"`
	ContractAddr string  `json:"contractAddr"`
	Spending     string  `json:"spending"`
	DestAddr     string  `json:"destAddr"`
	FeeRate      float64 `json:"feeRate"`

	RawHex string `json:"rawHex"`
	TxID   string `json:"txid"`
	Fee    int64  `json:"fee"`
	VSize  int    `json:"vsize"`
	Value  int64  `json:"value"`

	// ValidFrom is when a refund becomes relayable; empty for a redeem.
	ValidFrom string `json:"validFrom,omitempty"`
	// NotYet is set while a refund's locktime is still in the future, with the
	// wait left. The network rejects the transaction until then, and saying so
	// here is the difference between "wait" and "this is broken".
	NotYet string `json:"notYet,omitempty"`

	// PresignedRefundHex is whatever the file already carried, so the page can
	// offer the no-rebuild path first: broadcasting it needs no decisions.
	PresignedRefundHex string `json:"presignedRefundHex,omitempty"`
	// Warning is set when the spend was built on an assumption the page could
	// not check -- see RebuildRequest.AllowUnboundFunding.
	Warning string `json:"warning,omitempty"`
}

// Rebuild reconstructs a spending transaction from a recovery file.
//
// The pre-signed refund in the file covers the common case on its own —
// broadcast it anywhere — but it is signed at one fee rate and pays one
// address. This exists for when that is not good enough: fees have risen, the
// funds should go somewhere else, or the secret turned up and a redeem is
// wanted instead.
func Rebuild(req RebuildRequest) (*RebuildResult, error) {
	var rf recoveryFile
	if err := json.Unmarshal([]byte(req.File), &rf); err != nil {
		return nil, fmt.Errorf("that is not a recovery file: %w", err)
	}

	params, err := NetworkParams(rf.Network)
	if err != nil {
		return nil, err
	}
	contract, err := hex.DecodeString(rf.ContractHex)
	if err != nil {
		return nil, fmt.Errorf("contractHex is not valid hex: %w", err)
	}
	if rf.Funding == nil {
		return nil, errors.New("this recovery file records no funding output, so there is " +
			"nothing to spend yet")
	}
	if rf.PrivateKey == "" {
		return nil, errors.New("this recovery file has no private key")
	}
	wif, err := btcutil.DecodeWIF(strings.TrimSpace(rf.PrivateKey))
	if err != nil {
		return nil, fmt.Errorf("privateKeyWIF is not a valid WIF: %w", err)
	}
	// A loose check -- testnet and regtest share a WIF prefix -- but it catches
	// the file whose network field was changed without its key, which is the
	// shape that otherwise produces a valid signature on the wrong chain's
	// address encoding.
	if !wif.IsForNet(params) {
		return nil, fmt.Errorf("this file is inconsistent: it says network %q but its private key "+
			"is not a %s key. Do not act on it", rf.Network, params.Name)
	}
	pub := wif.PrivKey.PubKey().SerializeCompressed()
	key := &SwapKey{
		Priv: wif.PrivKey.Serialize(),
		Pub:  pub,
		PKH:  btcutil.Hash160(pub),
	}

	to := strings.TrimSpace(req.DestAddr)
	if to == "" {
		to = rf.DestAddr
	}
	if to == "" {
		return nil, errors.New("no destination address: this file records none, so give one")
	}
	rate := req.FeeRate
	if rate <= 0 {
		rate = 2.0
	}

	// Which branch this key can take is a property of the contract, not of what
	// the caller asks for. Deciding it here means a wrong request gets an
	// explanation rather than a signature failure further down.
	details, err := ParseContract(contract)
	if err != nil {
		return nil, fmt.Errorf("recovery file does not contain a valid swap contract: %w", err)
	}
	// The address in the file is what every message here quotes, and it is what
	// the user checks against a block explorer. If it does not actually hash to
	// the contract in the same file, one of the two has been edited and neither
	// can be trusted.
	if rf.ContractAddr != "" {
		addr, aerr := ContractAddress(contract, params)
		if aerr != nil {
			return nil, aerr
		}
		if addr.String() != rf.ContractAddr {
			return nil, fmt.Errorf("this file is inconsistent: contractHex hashes to %s but the "+
				"file says the contract address is %s. Do not act on it", addr, rf.ContractAddr)
		}
	}
	// Same reasoning for the locktime. The refund branch opens when the CONTRACT
	// says it does, and every "valid from" this page prints is read from the
	// contract below -- so a lockTime field that disagrees is either a stale
	// file or an edited one, and quietly preferring either would tell the user
	// to wait the wrong length of time on the screen they reach when everything
	// else has already failed.
	if rf.LockTime != 0 && rf.LockTime != details.LockTime {
		return nil, fmt.Errorf("this file is inconsistent: the contract's locktime is %s but the "+
			"file says %s. Do not act on it",
			time.Unix(details.LockTime, 0).UTC().Format(time.RFC3339),
			time.Unix(rf.LockTime, 0).UTC().Format(time.RFC3339))
	}

	// Whether the funding output pays this contract is recorded in newer files
	// and checked by the signer. An older file does not say, and this page
	// cannot ask a node, so it is built only on the user's say-so, with a
	// warning on the result -- never silently, because a contract swapped out
	// under a funding (see AuditContract) is exactly what such a file could be
	// carrying.
	var warning string
	if rf.Funding.PkScriptHex == "" {
		if !req.AllowUnboundFunding {
			return nil, errors.New("this file does not record what the funding output pays, so " +
				"it cannot be checked offline that the output is this contract's. A newer " +
				"recovery file from the swap would carry that. To build anyway -- if the contract " +
				"is not the one that was funded, the network will refuse the transaction and " +
				"nothing is lost -- tick the box and rebuild")
		}
		assumed, err := contractPkScript(contract, params)
		if err != nil {
			return nil, err
		}
		rf.Funding.PkScriptHex = hex.EncodeToString(assumed)
		warning = "Built on the assumption that the funding output pays this contract, which " +
			"this file does not record and this page could not check. If the network refuses " +
			"the transaction, the contract in this file is not the one that was funded."
	}

	canRedeem := bytes.Equal(details.PkhRedeem, key.PKH)
	canRefund := bytes.Equal(details.PkhRefund, key.PKH)
	if !canRedeem && !canRefund {
		return nil, fmt.Errorf("the key in this file matches neither branch of the contract; "+
			"it cannot spend %s", rf.ContractAddr)
	}

	var (
		spend  *SpendResult
		action string
	)
	secret := strings.TrimSpace(req.SecretHex)
	switch {
	case secret != "" && !canRedeem:
		return nil, errors.New("this key holds the REFUND branch of the contract, so it cannot " +
			"redeem with a secret. Rebuild without a preimage to get the refund, which " +
			"becomes valid once the locktime passes")

	case secret != "" || (canRedeem && rf.SecretHex != ""):
		preimage := secret
		if preimage == "" {
			preimage = rf.SecretHex
		}
		p, err := hex.DecodeString(preimage)
		if err != nil {
			return nil, fmt.Errorf("secret is not valid hex: %w", err)
		}
		spend, err = BuildRedeem(contract, *rf.Funding, key, p, to, rate, params)
		if err != nil {
			return nil, err
		}
		action = "redeem"

	case canRedeem:
		return nil, errors.New("this key holds the REDEEM branch, which requires the preimage. " +
			"Supply it once you have observed it. Only the counterparty who funded this " +
			"contract can refund it")

	default:
		spend, err = BuildRefund(contract, *rf.Funding, key, to, rate, params)
		if err != nil {
			return nil, err
		}
		action = "refund"
	}

	res := &RebuildResult{
		Warning:            warning,
		Action:             action,
		SwapID:             rf.SwapID,
		Network:            rf.Network,
		ContractAddr:       rf.ContractAddr,
		Spending:           fmt.Sprintf("%s:%d (%d sat)", rf.Funding.TxID, rf.Funding.Vout, rf.Funding.Value),
		DestAddr:           to,
		FeeRate:            rate,
		RawHex:             spend.RawHex,
		TxID:               spend.TxID,
		Fee:                spend.Fee,
		VSize:              spend.VSize,
		Value:              spend.Value,
		PresignedRefundHex: rf.PresignedRefundHex,
	}
	if action == "refund" {
		// From the contract, not from the file's own lockTime field: the two are
		// checked against each other above, and the contract is the one the
		// network will read.
		valid := time.Unix(details.LockTime, 0).UTC()
		res.ValidFrom = valid.Format(time.RFC3339)
		if d := time.Until(valid); d > 0 {
			res.NotYet = d.Truncate(time.Second).String()
		}
	}
	return res, nil
}

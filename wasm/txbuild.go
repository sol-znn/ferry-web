package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

const txVersion = 2

// dustLimit is the value below which an output will not relay. 546 sat is the
// standard limit for a P2PKH-sized output; using it for every destination type
// is slightly conservative for segwit outputs, which is the safe direction.
const dustLimit = 546

// maxFeeRate is the ceiling on a caller-supplied sat/vB rate. Bitcoin's worst
// congestion has never approached it, so anything above is a typo or a probe,
// and refusing it keeps the fee arithmetic below inside int64.
const maxFeeRate = 10_000

// FundingOutput identifies the contract output being spent. The last three
// fields are a snapshot from the last Refresh rather than a live figure, and
// take no part in signing: a spend is built from the outpoint and the value.
type FundingOutput struct {
	TxID  string `json:"txid"`
	Vout  uint32 `json:"vout"`
	Value int64  `json:"value"`

	// Confirmed is false while the funding is still only in the mempool, which
	// is where it spends its first few minutes and where it is still
	// replaceable by whoever sent it.
	Confirmed bool `json:"confirmed,omitempty"`
	// BlockHeight is the block that mined it, once one has.
	BlockHeight int64 `json:"blockHeight,omitempty"`
	// Confirmations is that block's depth as of the last Refresh, counting the
	// block itself — so a freshly mined funding reads 1, not 0.
	Confirmations int64 `json:"confirmations,omitempty"`

	// PkScriptHex is the script the output actually pays, read off the funding
	// transaction itself, so that a spend is built against what the chain
	// holds rather than against whatever contract the record carries at the
	// time. Recorded when the funding is adopted; filled in before signing if
	// an older record lacks it. A spend whose contract does not hash to this
	// is refused before it is signed.
	PkScriptHex string `json:"pkScriptHex,omitempty"`
}

// bindsTo reports whether this funding output, as the chain describes it, is
// an output of the given contract. Unknown (no script recorded) is not a match
// and not a mismatch; callers that can read the chain fill it in first.
func (f FundingOutput) bindsTo(contract []byte, params *chaincfg.Params) (bool, error) {
	if f.PkScriptHex == "" {
		return true, nil
	}
	want, err := contractPkScript(contract, params)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(f.PkScriptHex, hex.EncodeToString(want)), nil
}

// FundingBroadcast records that this browser sent a payment to the contract, to
// stop the same contract being funded twice.
//
// A broadcast is not funding: the transaction has to reach a node, get into a
// mempool and be listed against the contract address before Refresh can adopt
// it, and in that gap the card used to show a live send button beside an
// untouched-looking contract. One more click there is a second payment to an
// address whose contract only ever spends one output.
//
// Stored on the swap rather than in the page: a reload is the most natural thing
// to do while waiting for a payment to appear, and a guard a reload removes is
// not a guard.
type FundingBroadcast struct {
	TxID string    `json:"txid"`
	At   time.Time `json:"at"`
}

// SpendResult is a fully signed transaction ready to broadcast.
type SpendResult struct {
	RawHex string `json:"rawHex"`
	TxID   string `json:"txid"`
	Fee    int64  `json:"fee"`
	VSize  int    `json:"vsize"`
	Value  int64  `json:"value"`
}

// BuildRedeem creates and signs the transaction that claims a contract by
// revealing the secret. Publishing it is what makes the secret public, which
// is the mechanism the counterparty relies on to complete their own leg.
func BuildRedeem(contract []byte, funding FundingOutput, key *SwapKey, secret []byte,
	destAddr string, feeRate float64, params *chaincfg.Params) (*SpendResult, error) {

	details, err := ParseContract(contract)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(details.PkhRedeem, key.PKH) {
		return nil, errors.New("this swap's redeem key does not match the contract's redeem pubkey hash")
	}
	// Length before hash: the contract's OP_SIZE clause rejects any other
	// length whatever it hashes to, so a wrong length deserves the error that
	// names the real problem.
	if len(secret) != SecretSize {
		return nil, fmt.Errorf("secret must be %d bytes, got %d", SecretSize, len(secret))
	}
	if !bytes.Equal(SHA256(secret), details.SecretHash) {
		return nil, errors.New("secret does not hash to the contract's secret hash")
	}

	return buildSpend(contract, funding, key, secret, destAddr, feeRate, params, 0)
}

// BuildRefund creates and signs the transaction that returns a contract's funds
// to their funder after the timelock expires. Built as soon as the funding is
// seen rather than at refund time, so the user can save the signed hex
// immediately: it stays valid whether or not this app is ever run again.
func BuildRefund(contract []byte, funding FundingOutput, key *SwapKey,
	destAddr string, feeRate float64, params *chaincfg.Params) (*SpendResult, error) {

	details, err := ParseContract(contract)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(details.PkhRefund, key.PKH) {
		return nil, errors.New("this swap's refund key does not match the contract's refund pubkey hash")
	}
	return buildSpend(contract, funding, key, nil, destAddr, feeRate, params, details.LockTime)
}

// buildSpend constructs, sizes, signs and verifies a one-in one-out spend of
// the contract output. A nil secret selects the refund branch.
func buildSpend(contract []byte, funding FundingOutput, key *SwapKey, secret []byte,
	destAddr string, feeRate float64, params *chaincfg.Params, locktime int64) (*SpendResult, error) {

	if funding.Value <= 0 {
		return nil, errors.New("funding output has no value")
	}
	// The band is checked, not just the sign. feeRate arrives from a form, and
	// `int64(feeRate*vsize)` for a value like 1e30 is not a large fee -- Go leaves
	// an overflowing float-to-int conversion implementation-defined, and on every
	// target here it lands on the most negative int64, which wraps the subtraction
	// below. The dust check happens to catch that; refusing the input keeps it
	// caught.
	if !(feeRate > 0) || math.IsInf(feeRate, 0) {
		return nil, errors.New("fee rate must be a positive number of sat/vB")
	}
	if feeRate > maxFeeRate {
		return nil, fmt.Errorf("fee rate %g sat/vB is above the %d sat/vB ceiling this tool will "+
			"sign at; that is far beyond any real fee market", feeRate, maxFeeRate)
	}

	destScript, err := addressScript(destAddr, params)
	if err != nil {
		return nil, err
	}
	fundingHash, err := chainhash.NewHashFromStr(funding.TxID)
	if err != nil {
		return nil, fmt.Errorf("bad funding txid: %w", err)
	}
	contractPkScript, err := contractPkScript(contract, params)
	if err != nil {
		return nil, err
	}
	// The outpoint is the swap's; the script it pays is the chain's. If the
	// record knows that script, the contract being spent has to hash to it --
	// otherwise this would be a signature over a spend of somebody else's
	// output, which the network refuses and the local engine, fed the contract
	// rather than the chain, would not.
	if bound, err := funding.bindsTo(contract, params); err != nil {
		return nil, err
	} else if !bound {
		return nil, fmt.Errorf("the funding output %s:%d pays script %s, which is not this "+
			"contract's. The contract on this swap is not the one that was funded; restore the "+
			"one that was, or spend the output from a recovery file made when it was funded",
			funding.TxID, funding.Vout, funding.PkScriptHex)
	}

	newTx := func() *wire.MsgTx {
		tx := wire.NewMsgTx(txVersion)
		tx.LockTime = uint32(locktime)
		txIn := wire.NewTxIn(&wire.OutPoint{Hash: *fundingHash, Index: funding.Vout}, nil, nil)
		// CHECKLOCKTIMEVERIFY is only enforced when the input's sequence is not
		// final, so the refund path must not use the default 0xffffffff. Using
		// 0 for both paths keeps the two transactions the same size.
		txIn.Sequence = 0
		tx.AddTxIn(txIn)
		tx.AddTxOut(wire.NewTxOut(0, destScript))
		return tx
	}

	// Sized through the same helper the pre-funding estimate uses, so a fee
	// quoted before the money moved is the fee charged after it did.
	vsize, err := spendVSize(contract, destScript, secret, locktime)
	if err != nil {
		return nil, err
	}

	fee := feeFor(feeRate, vsize)
	value := funding.Value - fee
	if value < dustLimit {
		return nil, errors.New(dustError(funding.Value, fee, vsize))
	}

	tx := newTx()
	tx.TxOut[0].Value = value

	sig, err := txscript.RawTxInSignature(tx, 0, contract, txscript.SigHashAll, key.PrivKey())
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	sigScript, err := branchSigScript(contract, sig, key.Pub, secret)
	if err != nil {
		return nil, err
	}
	tx.TxIn[0].SignatureScript = sigScript

	// Execute the script locally before handing the transaction to anyone.
	// A swap failing at broadcast time is recoverable; one that fails silently
	// is not.
	engine, err := txscript.NewEngine(contractPkScript, tx, 0, txscript.StandardVerifyFlags,
		txscript.NewSigCache(10), txscript.NewTxSigHashes(tx, prevoutFetcher(contractPkScript, funding.Value)),
		funding.Value, prevoutFetcher(contractPkScript, funding.Value))
	if err != nil {
		return nil, fmt.Errorf("script engine: %w", err)
	}
	if err := engine.Execute(); err != nil {
		return nil, fmt.Errorf("built transaction does not satisfy the contract: %w", err)
	}

	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		return nil, err
	}
	return &SpendResult{
		RawHex: hex.EncodeToString(buf.Bytes()),
		TxID:   tx.TxHash().String(),
		Fee:    fee,
		VSize:  vsize,
		Value:  value,
	}, nil
}

// branchSigScript picks the redeem or refund signature script based on whether
// a secret was supplied.
func branchSigScript(contract, sig, pub, secret []byte) ([]byte, error) {
	if secret != nil {
		return RedeemSigScript(contract, sig, pub, secret)
	}
	return RefundSigScript(contract, sig, pub)
}

// contractPkScript returns the P2SH output script that pays the contract.
func contractPkScript(contract []byte, params *chaincfg.Params) ([]byte, error) {
	addr, err := ContractAddress(contract, params)
	if err != nil {
		return nil, err
	}
	return txscript.PayToAddrScript(addr)
}

// addressScript decodes a user-supplied destination address and returns its
// output script, rejecting addresses from a different network.
func addressScript(addr string, params *chaincfg.Params) ([]byte, error) {
	addr = strings.TrimSpace(addr)
	decoded, err := btcutil.DecodeAddress(addr, params)
	if err != nil {
		return nil, fmt.Errorf("invalid destination address: %w", err)
	}
	if !decoded.IsForNet(params) {
		return nil, fmt.Errorf("address %s is not valid on %s", addr, params.Name)
	}
	return txscript.PayToAddrScript(decoded)
}

// prevoutFetcher supplies the single prevout being spent.
func prevoutFetcher(pkScript []byte, value int64) txscript.PrevOutputFetcher {
	return txscript.NewCannedPrevOutputFetcher(pkScript, value)
}

// spendVSize is the virtual size of the one-in one-out transaction that spends a
// contract output to destScript. A nil secret sizes the refund branch, a non-nil
// one the redeem branch, which is 33 vB larger for the preimage push.
//
// Sized against a maximum-length signature and a compressed public key, so it is
// the largest form the signed transaction can take. buildSpend and the
// pre-funding estimate both go through here, which is what makes a fee quoted
// before funding the same fee the signer charges afterwards.
func spendVSize(contract, destScript, secret []byte, locktime int64) (int, error) {
	tx := wire.NewMsgTx(txVersion)
	tx.LockTime = uint32(locktime)
	txIn := wire.NewTxIn(&wire.OutPoint{}, nil, nil)
	txIn.Sequence = 0
	tx.AddTxIn(txIn)
	tx.AddTxOut(wire.NewTxOut(0, destScript))

	dummySig := make([]byte, 73) // 72-byte DER sig + 1 sighash byte
	dummyPub := make([]byte, 33) // compressed secp256k1 point
	sigScript, err := branchSigScript(contract, dummySig, dummyPub, secret)
	if err != nil {
		return 0, err
	}
	tx.TxIn[0].SignatureScript = sigScript
	return mempoolVSize(tx), nil
}

// feeFor is the fee arithmetic, in one place so that an estimate and a
// signature cannot round differently and disagree by a satoshi at the dust
// boundary -- which is the one place a satoshi decides whether money moves.
func feeFor(feeRate float64, vsize int) int64 {
	return int64(feeRate*float64(vsize) + 0.5)
}

// minRelayFeeRate is the floor a default Bitcoin Core mempool accepts. It is
// not a fee policy but the point below which a transaction is not relayed at
// all, so it is the rate that decides whether an output can ever be spent.
const minRelayFeeRate = 1.0

// maxViableFeeRate is the highest sat/vB at which spending value over vsize
// still leaves an output above the dust limit. Zero means no rate works.
//
// Reported in hundredths of a sat/vB, the precision the fee field takes, and
// floored rather than rounded: a rate that rounded up would be one the caller is
// told to use and is then refused at. The inversion only picks the candidate;
// whether it fits is decided by calling feeFor, so the rate quoted here is one
// the signer has agreed to.
func maxViableFeeRate(value int64, vsize int) float64 {
	budget := value - dustLimit
	if budget <= 0 || vsize <= 0 {
		return 0
	}
	rate := math.Floor((float64(budget)+0.5)/float64(vsize)*100) / 100
	if rate > maxFeeRate {
		rate = maxFeeRate
	}
	for rate >= minRelayFeeRate && feeFor(rate, vsize) > budget {
		rate = math.Round((rate-0.01)*100) / 100
	}
	if rate < minRelayFeeRate {
		return 0
	}
	return rate
}

// dustError explains a spend that cannot be built and -- the part that matters
// -- says what would work instead. The bare version ("after a 646 sat fee the
// output would be 354 sat") tells somebody holding a stuck contract only that
// they have failed; the number they need is the fee rate ceiling, because on the
// Recover page that is a field they can type into. Where no rate clears the
// limit, saying so stops them retrying at ever-lower rates for an hour.
func dustError(value, fee int64, vsize int) string {
	base := fmt.Sprintf("after a %d sat fee the output would be %d sat, at or below the %d sat dust limit",
		fee, value-fee, dustLimit)
	budget := value - dustLimit
	if budget <= 0 {
		return fmt.Sprintf("%s. A %d sat output is not larger than the %d sat dust limit itself, so no "+
			"transaction can spend it to a single destination: the contract was funded with too "+
			"little to move.", base, value, dustLimit)
	}
	best := maxViableFeeRate(value, vsize)
	if best == 0 {
		return fmt.Sprintf("%s. Spending it costs %d sat even at the %g sat/vB network minimum, and %d sat "+
			"is all a %d sat output can pay in fees, so it cannot be spent at any relayable rate. "+
			"A %d vB contract spend needs more locked than this.",
			base, feeFor(minRelayFeeRate, vsize), minRelayFeeRate, budget, value, vsize)
	}
	return fmt.Sprintf("%s. A %d sat output can pay at most %d sat in fees over %d vB, so %g sat/vB is the "+
		"highest rate that works here -- at that rate the output is %d sat.",
		base, value, budget, vsize, best, value-feeFor(best, vsize))
}

// mempoolVSize returns the virtual size of tx. The contract spend is a legacy
// P2SH input with no witness, so this currently equals the serialized size,
// but it goes through the full weight calculation so it stays correct if a
// segwit destination or wrapping is introduced later.
func mempoolVSize(tx *wire.MsgTx) int {
	weight := tx.SerializeSizeStripped()*3 + tx.SerializeSize()
	return (weight + 3) / 4
}

// DecodeRawTx parses a raw transaction hex string.
func DecodeRawTx(rawHex string) (*wire.MsgTx, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(rawHex))
	if err != nil {
		return nil, fmt.Errorf("bad transaction hex: %w", err)
	}
	tx := wire.NewMsgTx(txVersion)
	r := bytes.NewReader(raw)
	if err := tx.Deserialize(r); err != nil {
		return nil, fmt.Errorf("decode transaction: %w", err)
	}
	// Deserialize stops at the end of the transaction and says nothing about
	// what follows it. Bytes left over mean this is not one transaction, and
	// silently keeping the prefix would report a txid for something other than
	// what was handed in.
	if r.Len() != 0 {
		return nil, fmt.Errorf("decode transaction: %d bytes of trailing data after the transaction",
			r.Len())
	}
	return tx, nil
}

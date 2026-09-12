package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
)

// SecretSize is the fixed preimage length. The contract enforces it with
// OP_SIZE so that a counterparty on a chain with a different maximum data size
// cannot exploit the mismatch.
//
// 32 also sits inside Zenon's accepted preimage range (1..255, default 32), so
// the same secret works unmodified on both legs of a BTC<->ZNN swap.
const SecretSize = 32

// NewSecret returns a cryptographically random preimage and its SHA-256 hash.
func NewSecret() (secret, hash []byte, err error) {
	secret = make([]byte, SecretSize)
	if _, err = rand.Read(secret); err != nil {
		return nil, nil, err
	}
	h := sha256.Sum256(secret)
	return secret, h[:], nil
}

// SHA256 is the hash used by both legs: Bitcoin script has OP_SHA256, and the
// Zenon HTLC is created with hashType=1 (HashTypeSHA256) to match.
func SHA256(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

// BuildContract returns the atomic swap contract script.
//
// The layout is the well-established one decred/atomicswap popularised, kept
// deliberately so any existing tool that understands that template can audit,
// redeem or refund a contract this program produces.
//
//	OP_IF
//	  OP_SIZE 32 OP_EQUALVERIFY
//	  OP_SHA256 <secretHash> OP_EQUALVERIFY
//	  OP_DUP OP_HASH160 <pkhRedeem>
//	OP_ELSE
//	  <locktime> OP_CHECKLOCKTIMEVERIFY OP_DROP
//	  OP_DUP OP_HASH160 <pkhRefund>
//	OP_ENDIF
//	OP_EQUALVERIFY OP_CHECKSIG
//
// pkhRedeem can spend by revealing the secret; pkhRefund can spend only after
// locktime has passed.
func BuildContract(pkhRefund, pkhRedeem []byte, locktime int64, secretHash []byte) ([]byte, error) {
	if len(pkhRefund) != 20 {
		return nil, fmt.Errorf("refund pubkey hash must be 20 bytes, got %d", len(pkhRefund))
	}
	if len(pkhRedeem) != 20 {
		return nil, fmt.Errorf("redeem pubkey hash must be 20 bytes, got %d", len(pkhRedeem))
	}
	if len(secretHash) != sha256.Size {
		return nil, fmt.Errorf("secret hash must be %d bytes, got %d", sha256.Size, len(secretHash))
	}
	if err := validLockTime(locktime); err != nil {
		return nil, err
	}

	b := txscript.NewScriptBuilder()
	b.AddOp(txscript.OP_IF) // redeem path: requires the secret
	{
		b.AddOp(txscript.OP_SIZE)
		b.AddInt64(SecretSize)
		b.AddOp(txscript.OP_EQUALVERIFY)

		b.AddOp(txscript.OP_SHA256)
		b.AddData(secretHash)
		b.AddOp(txscript.OP_EQUALVERIFY)

		b.AddOp(txscript.OP_DUP)
		b.AddOp(txscript.OP_HASH160)
		b.AddData(pkhRedeem)
	}
	b.AddOp(txscript.OP_ELSE) // refund path: requires locktime to have passed
	{
		b.AddInt64(locktime)
		b.AddOp(txscript.OP_CHECKLOCKTIMEVERIFY)
		b.AddOp(txscript.OP_DROP)

		b.AddOp(txscript.OP_DUP)
		b.AddOp(txscript.OP_HASH160)
		b.AddData(pkhRefund)
	}
	b.AddOp(txscript.OP_ENDIF)

	b.AddOp(txscript.OP_EQUALVERIFY)
	b.AddOp(txscript.OP_CHECKSIG)

	return b.Script()
}

// ContractDetails is the result of parsing a contract script.
type ContractDetails struct {
	SecretHash []byte
	PkhRedeem  []byte
	PkhRefund  []byte
	LockTime   int64
	SecretSize int64
}

type scriptToken struct {
	op   byte
	data []byte
}

// contractTemplate is the opcode at each position that is fixed, i.e. not a
// parameter. Positions absent from the map hold pushed parameters.
var contractTemplate = map[int]byte{
	0:  txscript.OP_IF,
	1:  txscript.OP_SIZE,
	3:  txscript.OP_EQUALVERIFY,
	4:  txscript.OP_SHA256,
	6:  txscript.OP_EQUALVERIFY,
	7:  txscript.OP_DUP,
	8:  txscript.OP_HASH160,
	10: txscript.OP_ELSE,
	12: txscript.OP_CHECKLOCKTIMEVERIFY,
	13: txscript.OP_DROP,
	14: txscript.OP_DUP,
	15: txscript.OP_HASH160,
	17: txscript.OP_ENDIF,
	18: txscript.OP_EQUALVERIFY,
	19: txscript.OP_CHECKSIG,
}

const contractTemplateLen = 20

// ParseContract validates that script matches the atomic swap template exactly
// and extracts its parameters. Anything not matching the template is rejected
// rather than best-effort parsed: a contract that is "close enough" is exactly
// what an attacker constructs.
func ParseContract(script []byte) (*ContractDetails, error) {
	tokens := make([]scriptToken, 0, contractTemplateLen)
	tk := txscript.MakeScriptTokenizer(0, script)
	for tk.Next() {
		if len(tokens) == contractTemplateLen {
			return nil, errors.New("script is longer than the atomic swap template")
		}
		tokens = append(tokens, scriptToken{op: tk.Opcode(), data: tk.Data()})
	}
	if err := tk.Err(); err != nil {
		return nil, fmt.Errorf("malformed script: %w", err)
	}
	if len(tokens) != contractTemplateLen {
		return nil, fmt.Errorf("script has %d elements, the atomic swap template has %d",
			len(tokens), contractTemplateLen)
	}
	for idx, op := range contractTemplate {
		if tokens[idx].op != op {
			return nil, fmt.Errorf("script does not match the atomic swap template at element %d", idx)
		}
	}

	secretSize, err := scriptNum(tokens[2], maxScriptNumLen)
	if err != nil {
		return nil, fmt.Errorf("bad secret size: %w", err)
	}
	if secretSize != SecretSize {
		return nil, fmt.Errorf("contract specifies secret size %d, this tool only handles %d",
			secretSize, SecretSize)
	}

	secretHash := tokens[5].data
	if len(secretHash) != sha256.Size {
		return nil, fmt.Errorf("secret hash is %d bytes, want %d", len(secretHash), sha256.Size)
	}
	pkhRedeem := tokens[9].data
	if len(pkhRedeem) != 20 {
		return nil, errors.New("redeem pubkey hash is not 20 bytes")
	}
	locktime, err := scriptNum(tokens[11], maxCLTVScriptNumLen)
	if err != nil {
		return nil, fmt.Errorf("bad locktime: %w", err)
	}
	// A contract whose locktime is not a timestamp is not one this tool can
	// reason about: every deadline it reports, and the refund it pre-signs,
	// assume the refund branch opens at a wall-clock moment.
	if err := validLockTime(locktime); err != nil {
		return nil, fmt.Errorf("contract %w", err)
	}
	pkhRefund := tokens[16].data
	if len(pkhRefund) != 20 {
		return nil, errors.New("refund pubkey hash is not 20 bytes")
	}

	// Everything above checks WHAT the script says. This checks HOW it says
	// it. The tokenizer hands back a push's payload whatever opcode carried it,
	// so a hash wrapped in OP_PUSHDATA1 reads as the right 32 bytes -- and
	// hashes to a different P2SH address, and fails the minimal-push rule the
	// moment a spend is run under standard policy, which is exactly how the
	// redeem is built and how every node relays. A counterparty who funds such
	// a contract has a refund branch that works and has handed us a redeem
	// branch that does not. So the script is rebuilt from its parsed terms the
	// one way this program builds it, and anything but that byte string is
	// refused: canonical is the only encoding whose spends this program has
	// proven, and the only one it will audit.
	canonical, err := BuildContract(pkhRefund, pkhRedeem, locktime, secretHash)
	if err != nil {
		return nil, fmt.Errorf("rebuilding the contract from its terms: %w", err)
	}
	if !bytes.Equal(script, canonical) {
		return nil, errors.New("script carries the atomic swap template's terms but is not its " +
			"canonical encoding -- a push written with a longer opcode than it needs, most " +
			"likely. Standard policy refuses to spend through such a push, so whichever branch " +
			"it sits in cannot be spent the way this tool spends it, and a contract only one " +
			"side can spend is not a swap. Ask them to rebuild the contract with Ferry, or " +
			"another tool that emits minimal pushes")
	}

	return &ContractDetails{
		SecretHash: secretHash,
		PkhRedeem:  pkhRedeem,
		PkhRefund:  pkhRefund,
		LockTime:   locktime,
		SecretSize: secretSize,
	}, nil
}

// ContractAddress returns the P2SH address funds must be sent to. This is an
// ordinary address: any Bitcoin wallet can pay it without knowing anything
// about atomic swaps, which is what makes the funding leg universally
// compatible.
func ContractAddress(contract []byte, params *chaincfg.Params) (btcutil.Address, error) {
	return btcutil.NewAddressScriptHash(contract, params)
}

// RedeemSigScript builds the signature script for the secret-revealing path:
//
//	<sig> <pubkey> <secret> 1 <contract>
func RedeemSigScript(contract, sig, pubkey, secret []byte) ([]byte, error) {
	b := txscript.NewScriptBuilder()
	b.AddData(sig)
	b.AddData(pubkey)
	b.AddData(secret)
	b.AddInt64(1)
	b.AddData(contract)
	return b.Script()
}

// RefundSigScript builds the signature script for the timelocked path:
//
//	<sig> <pubkey> 0 <contract>
func RefundSigScript(contract, sig, pubkey []byte) ([]byte, error) {
	b := txscript.NewScriptBuilder()
	b.AddData(sig)
	b.AddData(pubkey)
	b.AddInt64(0)
	b.AddData(contract)
	return b.Script()
}

// ExtractSecret scans every input's signature script for a data push that
// hashes to secretHash. This recovers the secret from the counterparty's
// on-chain redeem regardless of how they built their spending transaction.
func ExtractSecret(sigScripts [][]byte, secretHash []byte) ([]byte, error) {
	for _, ss := range sigScripts {
		pushes, err := txscript.PushedData(ss)
		if err != nil {
			continue
		}
		for _, push := range pushes {
			if bytes.Equal(SHA256(push), secretHash) {
				return push, nil
			}
		}
	}
	return nil, errors.New("no input reveals a preimage matching the secret hash")
}

// LockTimeThreshold is the value at which nLockTime and CHECKLOCKTIMEVERIFY
// switch from meaning "block height" to meaning "unix timestamp" (BIP-65).
// Every locktime in this system is a timestamp, so contracts are required to
// sit above it: a value below the threshold would be silently interpreted as a
// block height, and the refund branch would open at a completely different
// time from the one every message in the UI promises.
const LockTimeThreshold = 500_000_000

// MaxLockTime is the largest value nLockTime can carry, since it is a uint32
// on the wire. Rejecting anything larger stops a swap being built whose
// locktime silently truncates when the refund transaction is serialised.
const MaxLockTime = 0xFFFFFFFF

// validLockTime rejects locktimes Bitcoin would not read as the timestamp the
// rest of this system assumes.
func validLockTime(locktime int64) error {
	if locktime < LockTimeThreshold {
		return fmt.Errorf("locktime %d is below %d, which Bitcoin reads as a block height rather than a timestamp",
			locktime, LockTimeThreshold)
	}
	if locktime > MaxLockTime {
		return fmt.Errorf("locktime %d does not fit in the 32-bit nLockTime field (max %d)",
			locktime, int64(MaxLockTime))
	}
	return nil
}

// maxScriptNumLen is the width Bitcoin's arithmetic opcodes accept for an
// operand: four bytes, per CScriptNum's default.
const maxScriptNumLen = 4

// maxCLTVScriptNumLen is the widened limit CHECKLOCKTIMEVERIFY uses, so that a
// timestamp locktime -- which needs five bytes once the sign bit is accounted
// for -- fits. BIP-65 specifies exactly this exception.
const maxCLTVScriptNumLen = 5

// scriptNum reads a small-integer opcode or a number push as an int64, applying
// the same width and minimal-encoding rules the script engine will.
//
// Being lax here is not harmlessly permissive. A locktime padded past five bytes,
// or encoded non-minimally, parses to a sensible-looking number in Go while
// OP_CHECKLOCKTIMEVERIFY refuses to read it at all -- so the contract would pass
// every check this program makes and have a refund branch no node will ever
// execute. The parser is the only place that can catch that before funding.
func scriptNum(t scriptToken, maxLen int) (int64, error) {
	if t.op == txscript.OP_0 {
		return 0, nil
	}
	if t.op >= txscript.OP_1 && t.op <= txscript.OP_16 {
		return int64(t.op - (txscript.OP_1 - 1)), nil
	}
	if t.data == nil {
		return 0, fmt.Errorf("opcode 0x%02x is not a number", t.op)
	}
	if len(t.data) > maxLen {
		return 0, fmt.Errorf("number is %d bytes, over the %d the script engine accepts here",
			len(t.data), maxLen)
	}
	if err := minimallyEncoded(t.data); err != nil {
		return 0, err
	}
	// Script numbers are little-endian with a sign bit in the top bit of the
	// most significant byte.
	var v int64
	for i := len(t.data) - 1; i >= 0; i-- {
		v = v<<8 | int64(t.data[i])
	}
	if t.data[len(t.data)-1]&0x80 != 0 {
		v &^= int64(0x80) << uint(8*(len(t.data)-1))
		v = -v
	}
	return v, nil
}

// minimallyEncoded applies the rule SCRIPT_VERIFY_MINIMALDATA enforces on a
// number: the most significant byte may not be padding. It is either non-zero
// once the sign bit is masked off, or it is a sign byte whose presence is
// justified by the byte below it already using its own high bit.
func minimallyEncoded(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if data[len(data)-1]&0x7f != 0 {
		return nil
	}
	if len(data) > 1 && data[len(data)-2]&0x80 != 0 {
		return nil
	}
	return fmt.Errorf("number 0x%x is not minimally encoded, which the script engine rejects",
		data)
}

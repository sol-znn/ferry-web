package main

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
)

// regtestDest is an arbitrary regtest address used as the payout target in
// tests. Where the coins land is irrelevant to what these tests check; that the
// contract can be spent at all is the point.
const regtestDest = "bcrt1q0rymrte6drs2nvjn73mqsl2meud7nv0dy6tgn4"

func mustKey(t *testing.T) *SwapKey {
	t.Helper()
	k, err := NewSwapKey()
	if err != nil {
		t.Fatalf("NewSwapKey: %v", err)
	}
	return k
}

func TestContractRoundTrip(t *testing.T) {
	refund, redeem := mustKey(t), mustKey(t)
	secret, hash, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	locktime := time.Now().Add(48 * time.Hour).Unix()

	contract, err := BuildContract(refund.PKH, redeem.PKH, locktime, hash)
	if err != nil {
		t.Fatalf("BuildContract: %v", err)
	}

	got, err := ParseContract(contract)
	if err != nil {
		t.Fatalf("ParseContract: %v", err)
	}
	if !bytes.Equal(got.SecretHash, hash) {
		t.Errorf("secret hash: got %x want %x", got.SecretHash, hash)
	}
	if !bytes.Equal(got.PkhRedeem, redeem.PKH) {
		t.Errorf("redeem pkh: got %x want %x", got.PkhRedeem, redeem.PKH)
	}
	if !bytes.Equal(got.PkhRefund, refund.PKH) {
		t.Errorf("refund pkh: got %x want %x", got.PkhRefund, refund.PKH)
	}
	if got.LockTime != locktime {
		t.Errorf("locktime: got %d want %d", got.LockTime, locktime)
	}
	if got.SecretSize != SecretSize {
		t.Errorf("secret size: got %d want %d", got.SecretSize, SecretSize)
	}
	if len(secret) != SecretSize {
		t.Errorf("secret length: got %d want %d", len(secret), SecretSize)
	}
}

// TestParseRejectsNonTemplate makes sure a script that merely resembles the
// contract is refused. Accepting a near-miss is how a counterparty sneaks in a
// contract that does not actually pay you.
func TestParseRejectsNonTemplate(t *testing.T) {
	refund, redeem := mustKey(t), mustKey(t)
	_, hash, _ := NewSecret()
	locktime := time.Now().Add(24 * time.Hour).Unix()

	good, err := BuildContract(refund.PKH, redeem.PKH, locktime, hash)
	if err != nil {
		t.Fatalf("BuildContract: %v", err)
	}

	cases := map[string][]byte{
		"empty":            {},
		"truncated":        good[:len(good)-1],
		"trailing garbage": append(append([]byte{}, good...), txscript.OP_NOP),
		"plain p2pkh": func() []byte {
			s, _ := txscript.NewScriptBuilder().
				AddOp(txscript.OP_DUP).AddOp(txscript.OP_HASH160).
				AddData(redeem.PKH).AddOp(txscript.OP_EQUALVERIFY).
				AddOp(txscript.OP_CHECKSIG).Script()
			return s
		}(),
	}
	for name, script := range cases {
		if _, err := ParseContract(script); err == nil {
			t.Errorf("%s: expected ParseContract to reject the script, got nil error", name)
		}
	}
}

// TestParseRejectsWrongSecretSize covers the exact attack the OP_SIZE clause
// exists to prevent: a contract whose preimage length differs from what the
// other chain accepts.
func TestParseRejectsWrongSecretSize(t *testing.T) {
	refund, redeem := mustKey(t), mustKey(t)
	_, hash, _ := NewSecret()

	script := buildTemplate(t, 16, hash, redeem.PKH, time.Now().Unix(), refund.PKH)
	if _, err := ParseContract(script); err == nil {
		t.Fatal("expected a contract with a 16-byte secret size to be rejected")
	}
}

// buildTemplate emits the swap contract shape with every parameter under the
// caller's control, so a test can produce the near-misses BuildContract itself
// refuses to make. A counterparty is not obliged to build theirs with this
// tool, so the parser has to be tested against scripts it would never emit.
func buildTemplate(t *testing.T, secretSize int64, hash, pkhRedeem []byte,
	locktime int64, pkhRefund []byte) []byte {
	t.Helper()
	b := txscript.NewScriptBuilder()
	b.AddOp(txscript.OP_IF)
	b.AddOp(txscript.OP_SIZE)
	b.AddInt64(secretSize)
	b.AddOp(txscript.OP_EQUALVERIFY)
	b.AddOp(txscript.OP_SHA256)
	b.AddData(hash)
	b.AddOp(txscript.OP_EQUALVERIFY)
	b.AddOp(txscript.OP_DUP)
	b.AddOp(txscript.OP_HASH160)
	b.AddData(pkhRedeem)
	b.AddOp(txscript.OP_ELSE)
	b.AddInt64(locktime)
	b.AddOp(txscript.OP_CHECKLOCKTIMEVERIFY)
	b.AddOp(txscript.OP_DROP)
	b.AddOp(txscript.OP_DUP)
	b.AddOp(txscript.OP_HASH160)
	b.AddData(pkhRefund)
	b.AddOp(txscript.OP_ENDIF)
	b.AddOp(txscript.OP_EQUALVERIFY)
	b.AddOp(txscript.OP_CHECKSIG)
	script, err := b.Script()
	if err != nil {
		t.Fatalf("build template: %v", err)
	}
	return script
}

func TestExtractSecret(t *testing.T) {
	secret, hash, _ := NewSecret()
	contract := []byte{0x01, 0x02}
	sig, pub := bytes.Repeat([]byte{0xAB}, 71), bytes.Repeat([]byte{0xCD}, 33)

	sigScript, err := RedeemSigScript(contract, sig, pub, secret)
	if err != nil {
		t.Fatalf("RedeemSigScript: %v", err)
	}
	got, err := ExtractSecret([][]byte{sigScript}, hash)
	if err != nil {
		t.Fatalf("ExtractSecret: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Errorf("got %x want %x", got, secret)
	}

	// A refund reveals nothing, so extraction must fail rather than return
	// something bogus.
	refundScript, err := RefundSigScript(contract, sig, pub)
	if err != nil {
		t.Fatalf("RefundSigScript: %v", err)
	}
	if _, err := ExtractSecret([][]byte{refundScript}, hash); err == nil {
		t.Error("expected no secret to be found in a refund signature script")
	}
}

// TestSpendPathsVerify builds both spending transactions and runs them through
// the script engine, which is the same check bitcoind performs.
func TestSpendPathsVerify(t *testing.T) {
	params := &chaincfg.RegressionNetParams
	refundKey, redeemKey := mustKey(t), mustKey(t)
	secret, hash, _ := NewSecret()
	locktime := time.Now().Add(-time.Hour).Unix() // already expired, so refund is valid

	contract, err := BuildContract(refundKey.PKH, redeemKey.PKH, locktime, hash)
	if err != nil {
		t.Fatalf("BuildContract: %v", err)
	}
	addr, err := ContractAddress(contract, params)
	if err != nil {
		t.Fatalf("ContractAddress: %v", err)
	}
	t.Logf("contract address: %s", addr)

	funding := FundingOutput{
		TxID:  "5ae77294d1bd1dea7fce8b235ae89b80585424aeaa907f181282db9ed9b9fd0a",
		Vout:  0,
		Value: 100_000,
	}

	// buildSpend runs the script engine internally, so a nil error here means
	// the transaction actually satisfies the contract.
	redeemTx, err := BuildRedeem(contract, funding, redeemKey, secret, regtestDest, 2.0, params)
	if err != nil {
		t.Fatalf("BuildRedeem: %v", err)
	}
	if redeemTx.Value+redeemTx.Fee != funding.Value {
		t.Errorf("redeem value %d + fee %d != funding %d", redeemTx.Value, redeemTx.Fee, funding.Value)
	}

	refundTx, err := BuildRefund(contract, funding, refundKey, regtestDest, 2.0, params)
	if err != nil {
		t.Fatalf("BuildRefund: %v", err)
	}
	if refundTx.Value+refundTx.Fee != funding.Value {
		t.Errorf("refund value %d + fee %d != funding %d", refundTx.Value, refundTx.Fee, funding.Value)
	}

	// The redeem transaction must carry the secret where the counterparty can
	// find it: this is the whole cross-chain mechanism.
	tx, err := DecodeRawTx(redeemTx.RawHex)
	if err != nil {
		t.Fatalf("DecodeRawTx: %v", err)
	}
	found, err := ExtractSecret([][]byte{tx.TxIn[0].SignatureScript}, hash)
	if err != nil {
		t.Fatalf("secret not recoverable from the redeem transaction: %v", err)
	}
	if !bytes.Equal(found, secret) {
		t.Errorf("recovered %x want %x", found, secret)
	}
}

// TestBuildRedeemRejectsWrongSecret guards the check that stops a bad preimage
// from producing an unbroadcastable transaction.
func TestBuildRedeemRejectsWrongSecret(t *testing.T) {
	params := &chaincfg.RegressionNetParams
	refundKey, redeemKey := mustKey(t), mustKey(t)
	_, hash, _ := NewSecret()
	other, _, _ := NewSecret()

	contract, _ := BuildContract(refundKey.PKH, redeemKey.PKH, time.Now().Add(time.Hour).Unix(), hash)
	funding := FundingOutput{
		TxID:  "5ae77294d1bd1dea7fce8b235ae89b80585424aeaa907f181282db9ed9b9fd0a",
		Value: 100_000,
	}

	if _, err := BuildRedeem(contract, funding, redeemKey, other, regtestDest, 2.0, params); err == nil {
		t.Fatal("expected BuildRedeem to reject a secret that does not match the hash")
	}
}

func TestDecimalToBaseUnits(t *testing.T) {
	cases := []struct {
		in       string
		decimals int
		want     string
		wantErr  bool
	}{
		{"1", 8, "100000000", false},
		{"1.5", 8, "150000000", false},
		{"0.00000001", 8, "1", false},
		{"10", 0, "10", false},
		{"1.234", 2, "", true}, // more places than the token has
		{"-1", 8, "", true},
		{"abc", 8, "", true},
		{"", 8, "", true},
	}
	for _, c := range cases {
		got, err := decimalToBaseUnits(c.in, c.decimals)
		if c.wantErr {
			if err == nil {
				t.Errorf("decimalToBaseUnits(%q, %d): expected an error", c.in, c.decimals)
			}
			continue
		}
		if err != nil {
			t.Errorf("decimalToBaseUnits(%q, %d): %v", c.in, c.decimals, err)
			continue
		}
		if got.String() != c.want {
			t.Errorf("decimalToBaseUnits(%q, %d) = %s, want %s", c.in, c.decimals, got, c.want)
		}
	}
}

func TestOfferRoundTripCarriesNoSecrets(t *testing.T) {
	o := &Offer{
		Version: 1, Network: "regtest", FromRole: RoleInitiator,
		SecretHash: hex.EncodeToString(make([]byte, 32)),
		PKH:        hex.EncodeToString(make([]byte, 20)),
		BTCLeg:     LegSend, AmountSats: 100000,
	}
	enc, err := o.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	back, err := DecodeOffer(enc)
	if err != nil {
		t.Fatalf("DecodeOffer: %v", err)
	}
	if back.SecretHash != o.SecretHash || back.PKH != o.PKH || back.BTCLeg != o.BTCLeg {
		t.Errorf("round trip mismatch: %+v vs %+v", back, o)
	}
	if _, err := DecodeOffer("not-an-offer"); err == nil {
		t.Error("expected DecodeOffer to reject a non-offer string")
	}
}

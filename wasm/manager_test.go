package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/zenon/ferry-web/wasm/chain"
	"github.com/zenon/ferry-web/wasm/znn"
)

// stubBackend answers from fixtures, so the manager's chain-facing decisions can
// be exercised without a node. The point is not to re-test the Esplora client;
// it is to pin what happens when the answers are WRONG, which is the case a live
// backend cannot be asked to produce.
type stubBackend struct {
	utxos    []chain.UTXO
	outspend chain.Outspend
	rawTx    map[string]string
	feeRate  float64
	// txStatus answers TxStatus per txid. Absent means the transaction is known
	// but unconfirmed, which is the state a freshly broadcast funding is in and
	// the one the depth tracking has to handle without inventing a height.
	txStatus map[string]chain.Status
	// broadcasts counts what reached the network. A refusal that is only a
	// refusal in the error message, after the transaction has gone out, is the
	// failure this exists to catch.
	broadcasts int
}

func (s *stubBackend) TipHeight(context.Context) (int64, error) { return 100, nil }

func (s *stubBackend) AddressUTXOs(context.Context, string) ([]chain.UTXO, error) {
	return s.utxos, nil
}

func (s *stubBackend) TxStatus(_ context.Context, txid string) (chain.Status, error) {
	return s.txStatus[txid], nil
}

func (s *stubBackend) RawTx(_ context.Context, txid string) (string, error) {
	if raw, ok := s.rawTx[txid]; ok {
		return raw, nil
	}
	return "", errors.New("no such transaction")
}

func (s *stubBackend) OutspendOf(context.Context, string, uint32) (*chain.Outspend, error) {
	os := s.outspend
	return &os, nil
}

func (s *stubBackend) FeeRate(context.Context, int) (float64, error) { return s.feeRate, nil }

// BlockHashAt answers deterministically from the height, which is all the
// chain-agreement checks need from a stub: two stubs at the same height agree,
// and two at different heights do not.
func (s *stubBackend) BlockHashAt(_ context.Context, height int64) (string, error) {
	return fmt.Sprintf("%064x", height), nil
}

func (s *stubBackend) Broadcast(_ context.Context, raw string) (string, error) {
	s.broadcasts++
	tx, err := DecodeRawTx(raw)
	if err != nil {
		return "", err
	}
	return tx.TxHash().String(), nil
}

func (s *stubBackend) Name() string { return "stub" }

func newManager(t *testing.T, backend chain.Backend) *Manager {
	t.Helper()
	return &Manager{Store: NewStore(NewMemStorage()), Chain: backend, Network: "regtest"}
}

// Both branches of the contract pay the destination address and there is no
// other. Without one the refund cannot be pre-signed when funding appears, and
// neither Redeem nor Refund can build anything -- a swap that can be funded and
// then not spent, discovered only after the money is already in the contract.
func TestCreateRequiresADestination(t *testing.T) {
	m := newManager(t, &stubBackend{feeRate: 2})
	base := CreateParams{Role: RoleInitiator, Leg: LegSend, AmountSats: 400_000}

	if _, err := m.Create(base); err == nil {
		t.Fatal("created a swap with nowhere to pay")
	}
	blank := base
	blank.DestAddr = "   "
	if _, err := m.Create(blank); err == nil {
		t.Fatal("accepted whitespace as a destination")
	}

	ok := base
	ok.DestAddr = regtestDest
	sw, err := m.Create(ok)
	if err != nil {
		t.Fatalf("rejected a well-formed swap: %v", err)
	}
	// An agreed amount needs a token to be an amount OF: with no token standard
	// there are no decimals to convert it with and nothing to compare the HTLC's
	// own token against, so verification would have to refuse it.
	if sw.Zenon.TokenStandard != znn.ZnnTokenStandard {
		t.Errorf("token standard is %q, want the ZNN default %q",
			sw.Zenon.TokenStandard, znn.ZnnTokenStandard)
	}
}

// The bug this test is here for: a Bitcoin address pasted into a Zenon address
// field, or a Zenon address pasted into the token field, used to be accepted
// verbatim — trimmed and stored, nothing more — because nothing between the
// form and the record ever asked whether the string was the kind of thing the
// field wanted. A swap built on one sits there looking normal until the Zenon
// leg is verified against a payee that was never a real address at all.
func TestCreateValidatesZenonFields(t *testing.T) {
	const validAddr = "z1qqvwzz2xq7q5gwk6uhcddgrpxlfcyzc8rsu82s"
	const btcAddr = "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"

	m := newManager(t, &stubBackend{feeRate: 2})
	base := CreateParams{
		Role: RoleInitiator, Leg: LegSend, AmountSats: 400_000, DestAddr: regtestDest,
	}

	selfBad := base
	selfBad.ZenonSelfAddress = btcAddr
	if _, err := m.Create(selfBad); err == nil {
		t.Error("accepted a Bitcoin address in the Zenon self-address field")
	}

	peerBad := base
	peerBad.ZenonPeerAddress = btcAddr
	if _, err := m.Create(peerBad); err == nil {
		t.Error("accepted a Bitcoin address in the Zenon peer-address field")
	}

	// A Zenon address is a plausible paste into the token field too — both are
	// bech32 and both start with "z" — and a length check alone would not catch
	// it if the two byte lengths happened to coincide, which is why the prefix
	// is checked and not just the size.
	tokenBad := base
	tokenBad.ZenonToken = validAddr
	if _, err := m.Create(tokenBad); err == nil {
		t.Error("accepted a Zenon ADDRESS in the Zenon TOKEN field")
	}

	tokenGarbage := base
	tokenGarbage.ZenonToken = btcAddr
	if _, err := m.Create(tokenGarbage); err == nil {
		t.Error("accepted a Bitcoin address in the Zenon token field")
	}

	// Valid values, and blank, must still both work: the fields are optional,
	// not merely tolerant of garbage.
	ok := base
	ok.ZenonSelfAddress = validAddr
	ok.ZenonPeerAddress = validAddr
	ok.ZenonToken = znn.ZnnTokenStandard
	if _, err := m.Create(ok); err != nil {
		t.Errorf("rejected well-formed Zenon fields: %v", err)
	}
	if _, err := m.Create(base); err != nil {
		t.Errorf("rejected blank Zenon fields, which are optional: %v", err)
	}
}

// The backend names a transaction as the spender of the contract output. A wrong
// answer must not decide the swap's state: doing so files a live swap away as
// settled, takes it off the active list, and stops Refresh looking for the real
// spend.
func TestRefreshIgnoresASpendThatIsNotOurs(t *testing.T) {
	params := &chaincfg.RegressionNetParams
	refundKey, redeemKey := mustKey(t), mustKey(t)
	secret, hash, _ := NewSecret()
	contract, err := BuildContract(refundKey.PKH, redeemKey.PKH,
		time.Now().Add(48*time.Hour).Unix(), hash)
	if err != nil {
		t.Fatalf("BuildContract: %v", err)
	}
	addr, err := ContractAddress(contract, params)
	if err != nil {
		t.Fatalf("ContractAddress: %v", err)
	}

	funding := FundingOutput{
		TxID:  "5ae77294d1bd1dea7fce8b235ae89b80585424aeaa907f181282db9ed9b9fd0a",
		Vout:  0,
		Value: 400_000,
	}
	// A redeem of a DIFFERENT outpoint, carrying a valid preimage for this
	// swap's hash. It is the strongest form of the wrong answer: the preimage
	// extraction succeeds, so only the outpoint check can catch it.
	elsewhere := FundingOutput{
		TxID:  "1111111111111111111111111111111111111111111111111111111111111111",
		Vout:  0,
		Value: 400_000,
	}
	other, err := BuildRedeem(contract, elsewhere, redeemKey, secret, regtestDest, 2.0, params)
	if err != nil {
		t.Fatalf("BuildRedeem: %v", err)
	}

	m := newManager(t, &stubBackend{
		feeRate:  2,
		outspend: chain.Outspend{Spent: true, TxID: other.TxID},
		rawTx:    map[string]string{other.TxID: other.RawHex},
	})
	sw := &Swap{
		ID: "0011223344556677", Network: "regtest", Role: RoleParticipant, Leg: LegReceive,
		State: StateFunded, Key: redeemKey, SecretHash: hash, AmountSats: 400_000,
		Contract: contract, ContractAddr: addr.String(), DestAddr: regtestDest,
		Funding: &funding, LockTime: time.Now().Add(48 * time.Hour).Unix(),
	}
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.State == StateRedeemed || got.State == StateRefunded {
		t.Errorf("state is %q: a transaction that does not spend our output decided the swap",
			got.State)
	}
	if len(got.Secret) != 0 {
		t.Errorf("learned a secret from a transaction that does not spend our output: %s",
			hex.EncodeToString(got.Secret))
	}
	var complained bool
	for _, ev := range got.Events {
		if strings.Contains(ev.Message, "does not spend it") {
			complained = true
		}
	}
	if !complained {
		t.Error("ignored the bad answer silently; the event log should say what was refused")
	}
}

// The same path with the RIGHT transaction still has to work, or the check above
// is just breakage.
func TestRefreshLearnsTheSecretFromOurOwnOutpoint(t *testing.T) {
	params := &chaincfg.RegressionNetParams
	refundKey, redeemKey := mustKey(t), mustKey(t)
	secret, hash, _ := NewSecret()
	contract, err := BuildContract(refundKey.PKH, redeemKey.PKH,
		time.Now().Add(48*time.Hour).Unix(), hash)
	if err != nil {
		t.Fatalf("BuildContract: %v", err)
	}
	addr, err := ContractAddress(contract, params)
	if err != nil {
		t.Fatalf("ContractAddress: %v", err)
	}

	funding := FundingOutput{
		TxID:  "5ae77294d1bd1dea7fce8b235ae89b80585424aeaa907f181282db9ed9b9fd0a",
		Vout:  0,
		Value: 400_000,
	}
	spend, err := BuildRedeem(contract, funding, redeemKey, secret, regtestDest, 2.0, params)
	if err != nil {
		t.Fatalf("BuildRedeem: %v", err)
	}

	m := newManager(t, &stubBackend{
		feeRate:  2,
		outspend: chain.Outspend{Spent: true, TxID: spend.TxID},
		rawTx:    map[string]string{spend.TxID: spend.RawHex},
	})
	sw := &Swap{
		ID: "0011223344556678", Network: "regtest", Role: RoleParticipant, Leg: LegSend,
		State: StateFunded, Key: refundKey, SecretHash: hash, AmountSats: 400_000,
		Contract: contract, ContractAddr: addr.String(), DestAddr: regtestDest,
		Funding: &funding, LockTime: time.Now().Add(48 * time.Hour).Unix(),
	}
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.State != StateRedeemed {
		t.Errorf("state is %q, want %q", got.State, StateRedeemed)
	}
	if hex.EncodeToString(got.Secret) != hex.EncodeToString(secret) {
		t.Errorf("secret is %x, want %x", got.Secret, secret)
	}
}

// unlockingNode serves one settled htlc.Unlock on the payee's own chain: a send
// to the HTLC contract whose data carries the preimage among the ABI padding,
// which is the shape FindPreimage recognises.
func unlockingNode(t *testing.T, payee string, preimage []byte) *znn.Client {
	t.Helper()
	data := []byte{0xd3, 0x37, 0x91, 0xd3} // the Unlock method id
	data = append(data, make([]byte, 32)...)
	data = append(data, make([]byte, 32)...)
	data = append(data, preimage...)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		result := any(nil)
		if req.Method == "ledger.getAccountBlocksByPage" {
			result = map[string]any{"count": 1, "more": false, "list": []map[string]any{{
				"hash":      strings.Repeat("ab", 32),
				"address":   payee,
				"toAddress": znn.HtlcContractAddress,
				"blockType": 2,
				"data":      base64.StdEncoding.EncodeToString(data),
			}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(srv.Close)
	return znn.New(srv.URL)
}

// The unlock is the one move in a swap that leaves nothing to ask about
// afterwards: it DELETES the entry, so a collected leg and an id that was never
// there answer identically. The side that published it has UnlockHash to go on.
// The side that CREATED the HTLC has only what it saw, and the only thing it
// ever sees is the transaction the preimage was pulled out of -- so if that is
// not written down as it goes past, that side's card can never say the leg was
// collected at all.
func TestRefreshRecordsTheCounterpartysUnlock(t *testing.T) {
	const payee = "z1qqvwzz2xq7q5gwk6uhcddgrpxlfcyzc8rsu82s"
	params := &chaincfg.RegressionNetParams
	refundKey, redeemKey := mustKey(t), mustKey(t)
	secret, hash, _ := NewSecret()
	contract, err := BuildContract(refundKey.PKH, redeemKey.PKH,
		time.Now().Add(48*time.Hour).Unix(), hash)
	if err != nil {
		t.Fatalf("BuildContract: %v", err)
	}
	addr, err := ContractAddress(contract, params)
	if err != nil {
		t.Fatalf("ContractAddress: %v", err)
	}

	m := newManager(t, &stubBackend{feeRate: 2})
	m.Znn = unlockingNode(t, payee, secret)

	// The one shape whose preimage arrives on Zenon: the participant created the
	// Zenon HTLC, so the counterparty's unlock of it is where the secret appears.
	sw := &Swap{
		ID: "0011223344556679", Network: "regtest", Role: RoleParticipant, Leg: LegReceive,
		State: StateFunded, Key: redeemKey, SecretHash: hash, AmountSats: 400_000,
		Contract: contract, ContractAddr: addr.String(), DestAddr: regtestDest,
		LockTime: time.Now().Add(48 * time.Hour).Unix(),
		Zenon:    ZenonLeg{HtlcID: strings.Repeat("9f", 32), PeerAddress: payee, Verified: true},
	}
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if hex.EncodeToString(got.Secret) != hex.EncodeToString(secret) {
		t.Fatalf("secret is %x, want %x", got.Secret, secret)
	}
	if !got.Zenon.UnlockSeen {
		t.Error("the unlock that published the preimage was not recorded, so this side has no " +
			"way to say the Zenon leg was collected")
	}
}

// ...and it is never inferred from merely holding the preimage. SetSecret takes
// one pasted in by hand, which hashes just as correctly as one read off a block
// and proves nothing whatever about any chain. A badge saying the leg was
// unlocked would then be the card's only claim that nothing checked.
func TestSetSecretDoesNotClaimAnUnlockWasSeen(t *testing.T) {
	m := newManager(t, &stubBackend{feeRate: 2})
	secret, hash, _ := NewSecret()
	sw := &Swap{
		ID: "001122334455667a", Network: "regtest", Role: RoleParticipant, Leg: LegReceive,
		State: StateFunded, SecretHash: hash, AmountSats: 400_000,
		LockTime: time.Now().Add(48 * time.Hour).Unix(),
	}
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := m.SetSecret(sw.ID, hex.EncodeToString(secret))
	if err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	if got.Zenon.UnlockSeen {
		t.Error("a preimage pasted in by hand was recorded as an unlock observed on the chain")
	}
}

// A funded swap with no destination cannot pre-sign its refund. That is exactly
// the case where saying nothing leaves the user with a recovery file that
// quietly has no transaction in it, so the note is part of the behaviour.
func TestRefreshSaysWhyTheRefundWasNotPreSigned(t *testing.T) {
	params := &chaincfg.RegressionNetParams
	refundKey, redeemKey := mustKey(t), mustKey(t)
	_, hash, _ := NewSecret()
	contract, err := BuildContract(refundKey.PKH, redeemKey.PKH,
		time.Now().Add(48*time.Hour).Unix(), hash)
	if err != nil {
		t.Fatalf("BuildContract: %v", err)
	}
	addr, err := ContractAddress(contract, params)
	if err != nil {
		t.Fatalf("ContractAddress: %v", err)
	}

	m := newManager(t, &stubBackend{
		feeRate: 2,
		utxos: []chain.UTXO{{
			TxID:  "5ae77294d1bd1dea7fce8b235ae89b80585424aeaa907f181282db9ed9b9fd0a",
			Vout:  0,
			Value: 400_000,
		}},
	})
	// DestAddr empty: Create refuses this now, but a record restored from an
	// older backup can still look like it.
	sw := &Swap{
		ID: "0011223344556679", Network: "regtest", Role: RoleInitiator, Leg: LegSend,
		State: StateAwaitingFunding, Key: refundKey, SecretHash: hash, AmountSats: 400_000,
		Contract: contract, ContractAddr: addr.String(),
		LockTime: time.Now().Add(48 * time.Hour).Unix(),
	}
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.RefundTx != nil {
		t.Fatal("pre-signed a refund with no destination to pay")
	}
	var warned int
	for _, ev := range got.Events {
		if ev.Message == noDestNote {
			warned++
		}
	}
	if warned != 1 {
		t.Errorf("the missing-destination warning appears %d times, want exactly 1", warned)
	}
	// Refresh runs on every poll, so the note must not accumulate.
	got, err = m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	warned = 0
	for _, ev := range got.Events {
		if ev.Message == noDestNote {
			warned++
		}
	}
	if warned != 1 {
		t.Errorf("after a second refresh the warning appears %d times, want 1", warned)
	}
}

// A fee rate that overflows the fee arithmetic is refused as input rather than
// relied on to fail somewhere downstream.
func TestBuildRefusesAbsurdFeeRates(t *testing.T) {
	params := &chaincfg.RegressionNetParams
	refundKey, redeemKey := mustKey(t), mustKey(t)
	_, hash, _ := NewSecret()
	contract, err := BuildContract(refundKey.PKH, redeemKey.PKH,
		time.Now().Add(48*time.Hour).Unix(), hash)
	if err != nil {
		t.Fatalf("BuildContract: %v", err)
	}
	funding := FundingOutput{
		TxID:  "5ae77294d1bd1dea7fce8b235ae89b80585424aeaa907f181282db9ed9b9fd0a",
		Value: 400_000,
	}
	for _, rate := range []float64{0, -1, 1e30, maxFeeRate + 1} {
		if _, err := BuildRefund(contract, funding, refundKey, regtestDest, rate, params); err == nil {
			t.Errorf("signed at a fee rate of %g sat/vB", rate)
		}
	}
}

// fundedSwap is a send-leg swap whose contract is real, for the funding tests
// below. They care about the bookkeeping around a payment, not about the
// script, so the contract only has to be one the address can be derived from.
func fundedSwap(t *testing.T) *Swap {
	t.Helper()
	refundKey, redeemKey := mustKey(t), mustKey(t)
	_, hash, _ := NewSecret()
	lock := time.Now().Add(48 * time.Hour).Unix()
	contract, err := BuildContract(refundKey.PKH, redeemKey.PKH, lock, hash)
	if err != nil {
		t.Fatalf("BuildContract: %v", err)
	}
	addr, err := ContractAddress(contract, &chaincfg.RegressionNetParams)
	if err != nil {
		t.Fatalf("ContractAddress: %v", err)
	}
	return &Swap{
		ID: "aabbccddeeff0011", Network: "regtest", Role: RoleInitiator, Leg: LegSend,
		State: StateAwaitingFunding, Key: refundKey, SecretHash: hash, AmountSats: 400_000,
		Contract: contract, ContractAddr: addr.String(), DestAddr: regtestDest, LockTime: lock,
	}
}

// A payment that is on chain but not yet in a block is zero confirmations, and
// one in the tip block is one. Getting that off by one is how a card tells
// somebody their funding is settled the instant it is mined.
func TestRefreshCountsFundingConfirmations(t *testing.T) {
	sw := fundedSwap(t)
	txid := "5ae77294d1bd1dea7fce8b235ae89b80585424aeaa907f181282db9ed9b9fd0a"
	backend := &stubBackend{
		feeRate: 2,
		utxos:   []chain.UTXO{{TxID: txid, Vout: 0, Value: 400_000}},
	}
	m := newManager(t, backend)
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// In the mempool: seen, but nothing on top of it.
	got, err := m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Funding == nil {
		t.Fatal("the funding was not adopted")
	}
	if got.Funding.Confirmed || got.Funding.Confirmations != 0 {
		t.Errorf("an unmined funding reads confirmed=%v depth=%d, want false and 0",
			got.Funding.Confirmed, got.Funding.Confirmations)
	}

	// Mined into the tip. The stub's tip is 100, so that is one confirmation.
	backend.txStatus = map[string]chain.Status{
		txid: {Confirmed: true, BlockHeight: 100},
	}
	got, err = m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !got.Funding.Confirmed || got.Funding.Confirmations != 1 {
		t.Errorf("a funding in the tip block reads confirmed=%v depth=%d, want true and 1",
			got.Funding.Confirmed, got.Funding.Confirmations)
	}

	// Deeper than this app counts. The number is pinned rather than left to run.
	backend.txStatus[txid] = chain.Status{Confirmed: true, BlockHeight: 10}
	got, err = m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Funding.Confirmations != confirmationsTracked {
		t.Errorf("a funding 91 blocks deep reads %d, want it pinned at %d",
			got.Funding.Confirmations, confirmationsTracked)
	}
}

// A reorg that takes the funding's block away has to take its depth with it.
// Leaving a stale count behind is the one failure that matters here: it says
// "settled" about a payment that has stopped existing.
func TestRefreshForgetsDepthWhenAFundingIsUnmined(t *testing.T) {
	sw := fundedSwap(t)
	txid := "5ae77294d1bd1dea7fce8b235ae89b80585424aeaa907f181282db9ed9b9fd0a"
	sw.State = StateFunded
	sw.Funding = &FundingOutput{
		TxID: txid, Vout: 0, Value: 400_000,
		Confirmed: true, BlockHeight: 98, Confirmations: 3,
	}
	m := newManager(t, &stubBackend{feeRate: 2})
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Funding.Confirmed || got.Funding.Confirmations != 0 {
		t.Errorf("after the block went away the funding still reads confirmed=%v depth=%d",
			got.Funding.Confirmed, got.Funding.Confirmations)
	}
}

// The broadcast note is what stops a contract being paid twice while the first
// payment is still on its way to a mempool. It must record only what it was
// told, and must not be mistakable for funding.
func TestRecordFundingBroadcast(t *testing.T) {
	sw := fundedSwap(t)
	m := newManager(t, &stubBackend{feeRate: 2})
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}
	txid := "5ae77294d1bd1dea7fce8b235ae89b80585424aeaa907f181282db9ed9b9fd0a"

	if _, err := m.RecordFundingBroadcast(sw.ID, "not-a-txid"); err == nil {
		t.Error("accepted something that is not a transaction id")
	}

	got, err := m.RecordFundingBroadcast(sw.ID, txid)
	if err != nil {
		t.Fatalf("RecordFundingBroadcast: %v", err)
	}
	if got.FundingBroadcast == nil || got.FundingBroadcast.TxID != txid {
		t.Fatalf("the broadcast was not recorded: %+v", got.FundingBroadcast)
	}
	if got.Funding != nil || got.State == StateFunded {
		t.Error("a broadcast marked the contract funded; only Refresh may decide that")
	}

	// Reported twice for the same send, which is what a retried call looks like.
	before := len(got.Events)
	got, err = m.RecordFundingBroadcast(sw.ID, txid)
	if err != nil {
		t.Fatalf("RecordFundingBroadcast twice: %v", err)
	}
	if len(got.Events) != before {
		t.Error("the same broadcast reported twice wrote a second event")
	}

	// The receiving side does not fund anything, so it cannot have broadcast one.
	recv := fundedSwap(t)
	recv.ID = "1122334455667788"
	recv.Leg = LegReceive
	if err := m.Store.Save(recv); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := m.RecordFundingBroadcast(recv.ID, txid); err == nil {
		t.Error("the receiving side was allowed to record a funding broadcast")
	}
}

// A verdict that repeats the last one is not a new event.
//
// Verification used to happen only when somebody pressed a button, so writing a
// line every time it ran was the same as writing one per answer. It is not any
// more: a leg awaiting confirmation is re-read on a timer, and an id arriving
// over a session is verified on arrival however many times the counterparty
// sends it. Each of those appending another identical line would leave a swap
// that waited overnight with an event log made almost entirely of one sentence,
// stored in a browser, and no easier to read for any of it.
func TestVerifyZenonLogsAVerdictOnceUntilItChanges(t *testing.T) {
	// A node holding an entry that is not this swap's: the hashlock is somebody
	// else's, so verification refuses it. The refusal is the point — it is the
	// branch a user would re-run most, hoping for a different answer.
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if req.Method != "embedded.htlc.getById" {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": nil})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"result": map[string]any{
				"id":             strings.Repeat("9f", 32),
				"hashLocked":     "z1qqvwzz2xq7q5gwk6uhcddgrpxlfcyzc8rsu82s",
				"tokenStandard":  "zts1znnxxxxxxxxxxxxx9z4ulx",
				"amount":         "1000000000",
				"expirationTime": time.Now().Add(24 * time.Hour).Unix(),
				"hashType":       1,
				"keyMaxSize":     32,
				"hashLock":       base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xAB}, 32)),
			},
		})
	}))
	t.Cleanup(node.Close)

	m := newManager(t, &stubBackend{feeRate: 2})
	m.Znn = znn.New(node.URL)

	_, hash, _ := NewSecret()
	sw := &Swap{
		ID: "00112233445566aa", Network: "regtest", Role: RoleInitiator, Leg: LegSend,
		State: StateAwaitingFunding, SecretHash: hash, AmountSats: 400_000,
		LockTime: time.Now().Add(48 * time.Hour).Unix(),
	}
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}

	htlcID := strings.Repeat("9f", 32)
	count := func() int {
		stored, err := m.Store.Load(sw.ID)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		n := 0
		for _, ev := range stored.Events {
			if strings.Contains(ev.Message, "FAILED verification") {
				n++
			}
		}
		return n
	}

	for range 3 {
		if _, _, verr := m.VerifyZenon(context.Background(), sw.ID, htlcID); verr == nil {
			t.Fatal("an HTLC locked to somebody else's hash was accepted")
		}
	}
	if got := count(); got != 1 {
		t.Errorf("three identical refusals wrote %d log lines, want 1", got)
	}
}

// The payout address is read off the entry while the entry still exists.
//
// It is worth nothing at verification time -- the check compares hashLocked
// against the AGREED recipient, and this is the observed one, which is exactly
// the value that must never stand in for it. It is worth a great deal a moment
// later. An unlock deletes the entry, so once the preimage is public there is
// nothing left to read the payout address off; recording it while the entry can
// still be read is what lets the search for that unlock filter candidates
// instead of walking every call the HTLC contract has received.
//
// The swap here has no PeerAddress at all, which is the shape that used to
// refuse to search: nothing requires it to be filled in, and the party who
// needs the preimage is by then not on speaking terms with the party who has it.
func TestVerifyZenonRecordsTheObservedPayee(t *testing.T) {
	const paid = "z1qqvwzz2xq7q5gwk6uhcddgrpxlfcyzc8rsu82s"
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if req.Method != "embedded.htlc.getById" {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": nil})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"result": map[string]any{
				"id":             strings.Repeat("9f", 32),
				"hashLocked":     paid,
				"tokenStandard":  "zts1znnxxxxxxxxxxxxx9z4ulx",
				"amount":         "1000000000",
				"expirationTime": time.Now().Add(24 * time.Hour).Unix(),
				"hashType":       1,
				"keyMaxSize":     32,
				"hashLock":       base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xAB}, 32)),
			},
		})
	}))
	t.Cleanup(node.Close)

	m := newManager(t, &stubBackend{feeRate: 2})
	m.Znn = znn.New(node.URL)

	_, hash, _ := NewSecret()
	sw := &Swap{
		ID: "00112233445566dd", Network: "regtest", Role: RoleParticipant, Leg: LegReceive,
		State: StateAwaitingFunding, SecretHash: hash, AmountSats: 400_000,
		LockTime: time.Now().Add(24 * time.Hour).Unix(),
	}
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// It fails verification — the hashlock is not this swap's — and that is
	// beside the point. The entry was READ, and what it says is recorded either
	// way, because a rejection has to be able to name both halves of a mismatch.
	got, _, _ := m.VerifyZenon(context.Background(), sw.ID, strings.Repeat("9f", 32))
	if got == nil {
		t.Fatal("VerifyZenon returned no swap")
	}
	if got.Zenon.ObservedHashLocked != paid {
		t.Errorf("the observed payout address was not recorded: got %q, want %q",
			got.Zenon.ObservedHashLocked, paid)
	}
	// The agreed recipient is still blank. Adopting the observed one as the
	// expectation is what would make a rejected HTLC re-verify clean.
	if got.Zenon.PeerAddress != "" {
		t.Errorf("the observed address was adopted as the agreed one: %q", got.Zenon.PeerAddress)
	}
	// And it is what the preimage search will use, having nothing better.
	if got.unlockPayee() != paid {
		t.Errorf("unlockPayee() = %q, want %q", got.unlockPayee(), paid)
	}
}

// Where the Bitcoin lands is not negotiable once a swap names it.
//
// Bitcoin itself cannot enforce this: BuildContract ends in CHECKSIG against a
// pubkey hash, so whoever holds the redeem key may pay their spend anywhere.
// That makes payoutAddress the only thing standing between a committed swap and
// a spend retargeted by whatever called in -- a button, a background action, or
// a console -- which is why the rule is here rather than in the form.
func TestPayoutAddressCannotBeRedirected(t *testing.T) {
	const elsewhere = "bcrt1qw508d6qejxtdg4y5r3zarvary0c5xw7kygt080"

	committed := &Swap{DestAddr: regtestDest}

	got, err := payoutAddress(committed, "")
	if err != nil || got != regtestDest {
		t.Errorf("asking for nothing should pay the swap's own address: got %q, %v", got, err)
	}

	got, err = payoutAddress(committed, "  "+regtestDest+"  ")
	if err != nil || got != regtestDest {
		t.Errorf("asking for the same address should be accepted: got %q, %v", got, err)
	}

	_, err = payoutAddress(committed, elsewhere)
	if err == nil {
		t.Fatal("a different destination was accepted on a swap that already names one")
	}
	// Both addresses in the message: which one it refused is the whole question
	// somebody reading this error is asking.
	if !strings.Contains(err.Error(), regtestDest) || !strings.Contains(err.Error(), elsewhere) {
		t.Errorf("the refusal should name both addresses, got: %v", err)
	}

	// A record restored from an older backup carries no address, and nothing can
	// build a spend without one. That first address is adopted, not refused.
	empty := &Swap{}
	got, err = payoutAddress(empty, regtestDest)
	if err != nil || got != regtestDest {
		t.Errorf("a record with no address should adopt the one supplied: got %q, %v", got, err)
	}
	if _, err = payoutAddress(empty, ""); err == nil {
		t.Error("a record with no address and none supplied should be refused")
	}
}

// A redeem publishes the secret, and the initiator's secret has been nowhere
// else. Against a contract holding less than was agreed, that hands the
// counterparty the full Zenon leg for a Bitcoin payment they chose to leave
// short -- so it is refused, and refused in the engine, where Auto Mode and the
// button both end up. The participant's secret is already public, so their
// redeem of the same short contract is simply collecting what is there.
func TestRedeemWillNotRevealTheSecretForAShortFunding(t *testing.T) {
	params := &chaincfg.RegressionNetParams
	refundKey, redeemKey := mustKey(t), mustKey(t)
	secret, hash, _ := NewSecret()
	lock := time.Now().Add(48 * time.Hour).Unix()
	contract, err := BuildContract(refundKey.PKH, redeemKey.PKH, lock, hash)
	if err != nil {
		t.Fatalf("BuildContract: %v", err)
	}
	addr, err := ContractAddress(contract, params)
	if err != nil {
		t.Fatalf("ContractAddress: %v", err)
	}
	const txid = "5ae77294d1bd1dea7fce8b235ae89b80585424aeaa907f181282db9ed9b9fd0a"
	pkScript, _ := contractPkScript(contract, params)
	receiving := func(id string, role Role, value int64) *Swap {
		return &Swap{
			ID: id, Network: "regtest", Role: role, Leg: LegReceive, State: StateFunded,
			Key: redeemKey, Secret: secret, SecretHash: hash, AmountSats: 400_000,
			Contract: contract, ContractAddr: addr.String(), DestAddr: regtestDest,
			LockTime: lock,
			// Bound: the binding is F05's concern and is tested there.
			Funding: &FundingOutput{TxID: txid, Vout: 0, Value: value, Confirmed: true,
				PkScriptHex: hex.EncodeToString(pkScript)},
		}
	}
	backend := &stubBackend{feeRate: 2}
	m := newManager(t, backend)
	save := func(sw *Swap) {
		t.Helper()
		if err := m.Store.Save(sw); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	// The initiator, short: refused, and the refusal leaves the swap as it was.
	short := receiving("aabbccddeeff0011", RoleInitiator, 100_000)
	save(short)
	if !view(short).RedeemHeldForShortFunding {
		t.Error("the card is not told the redeem is withheld")
	}
	if _, err := m.Redeem(context.Background(), short.ID, ""); err == nil {
		t.Fatal("a short contract was redeemed with the initiator's secret")
	} else if !strings.Contains(err.Error(), "publish your secret") {
		t.Errorf("the refusal does not say what it is protecting: %v", err)
	}
	if backend.broadcasts != 0 {
		t.Errorf("the refusal broadcast %d transaction(s); the secret is out", backend.broadcasts)
	}
	got, err := m.Store.Load(short.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.State != StateFunded || got.RedeemTx != nil {
		t.Errorf("the refusal changed the swap: state %q, redeemTx %v", got.State, got.RedeemTx)
	}

	// The initiator, paid in full: nothing to hold.
	full := receiving("aabbccddeeff0022", RoleInitiator, 400_000)
	save(full)
	if view(full).RedeemHeldForShortFunding {
		t.Error("a fully funded contract reads as withheld")
	}
	if got, err := m.Redeem(context.Background(), full.ID, ""); err != nil {
		t.Errorf("a fully funded contract could not be redeemed: %v", err)
	} else if got.State != StateRedeemed {
		t.Errorf("state after redeem is %q, want %q", got.State, StateRedeemed)
	}

	// The participant, short: their secret came off the counterparty's Zenon
	// unlock and is public. Whatever the contract holds is theirs to take.
	part := receiving("aabbccddeeff0033", RoleParticipant, 100_000)
	save(part)
	if view(part).RedeemHeldForShortFunding {
		t.Error("the participant's redeem of a short contract reads as withheld")
	}
	if got, err := m.Redeem(context.Background(), part.ID, ""); err != nil {
		t.Errorf("the participant could not collect a short contract: %v", err)
	} else if got.State != StateRedeemed {
		t.Errorf("state after redeem is %q, want %q", got.State, StateRedeemed)
	}
}

// The same hold, reached the way a live swap reaches it: Refresh adopts a short
// output off the chain, Redeem is refused, then a single output covering the
// amount appears, Refresh switches to it, and the redeem goes through. Two
// short outputs that only cover the amount together must NOT clear it -- the
// redeem spends one output, so their sum is not what the contract would pay.
func TestShortFundingHoldClearsOnlyForACoveringOutput(t *testing.T) {
	params := &chaincfg.RegressionNetParams
	refundKey, redeemKey := mustKey(t), mustKey(t)
	secret, hash, _ := NewSecret()
	lock := time.Now().Add(48 * time.Hour).Unix()
	contract, err := BuildContract(refundKey.PKH, redeemKey.PKH, lock, hash)
	if err != nil {
		t.Fatalf("BuildContract: %v", err)
	}
	addr, err := ContractAddress(contract, params)
	if err != nil {
		t.Fatalf("ContractAddress: %v", err)
	}
	// Real transactions the stub can serve, so each adopted output binds to
	// the contract (F05) and the redeem is judged on the amount alone.
	pkScript, _ := contractPkScript(contract, params)
	shortHex, shortTx := fundingTxPaying(t, pkScript, 150_000)
	secondHex, secondShortTx := fundingTxPaying(t, pkScript, 300_000)
	fullHex, fullTx := fundingTxPaying(t, pkScript, 400_000)
	mined := chain.Status{Confirmed: true, BlockHeight: 100}
	backend := &stubBackend{
		feeRate:  2,
		utxos:    []chain.UTXO{{TxID: shortTx, Vout: 0, Value: 150_000, Status: mined}},
		txStatus: map[string]chain.Status{shortTx: mined, secondShortTx: mined, fullTx: mined},
		rawTx:    map[string]string{shortTx: shortHex, secondShortTx: secondHex, fullTx: fullHex},
	}
	m := newManager(t, backend)
	sw := &Swap{
		ID: "aabbccddeeff0044", Network: "regtest", Role: RoleInitiator, Leg: LegReceive,
		State: StateAwaitingFunding, Key: redeemKey, Secret: secret, SecretHash: hash,
		AmountSats: 400_000, Contract: contract, ContractAddr: addr.String(),
		DestAddr: regtestDest, LockTime: lock,
	}
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Funding == nil || got.Funding.Value != 150_000 {
		t.Fatalf("the short output was not adopted: %+v", got.Funding)
	}
	if !got.RedeemHeldForShortFunding() {
		t.Fatal("a confirmed short funding does not hold the redeem")
	}
	if _, err := m.Redeem(context.Background(), sw.ID, ""); err == nil {
		t.Fatal("redeemed against a short funding adopted from the chain")
	}

	// A second short output. Together they cover the amount; neither does alone,
	// and only one is ever spent.
	backend.utxos = append(backend.utxos,
		chain.UTXO{TxID: secondShortTx, Vout: 0, Value: 300_000, Status: mined})
	got, err = m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Funding.Value != 300_000 {
		t.Errorf("the larger short output was not preferred: %+v", got.Funding)
	}
	if !got.RedeemHeldForShortFunding() {
		t.Fatal("two short outputs were treated as covering the amount together")
	}
	if _, err := m.Redeem(context.Background(), sw.ID, ""); err == nil {
		t.Fatal("redeemed against two short outputs")
	}
	if backend.broadcasts != 0 {
		t.Fatalf("%d transaction(s) were broadcast while the redeem was held", backend.broadcasts)
	}

	// A single covering output. This is what the hold waits for.
	backend.utxos = append(backend.utxos,
		chain.UTXO{TxID: fullTx, Vout: 0, Value: 400_000, Status: mined})
	got, err = m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Funding.TxID != fullTx {
		t.Fatalf("Refresh did not switch to the covering output: %+v", got.Funding)
	}
	if got.RedeemHeldForShortFunding() {
		t.Fatal("a covering output still holds the redeem")
	}
	got, err = m.Redeem(context.Background(), sw.ID, "")
	if err != nil {
		t.Fatalf("Redeem after a covering output: %v", err)
	}
	if got.State != StateRedeemed || backend.broadcasts != 1 {
		t.Errorf("state %q with %d broadcast(s), want redeemed with exactly one",
			got.State, backend.broadcasts)
	}
}

// fundingTxPaying is a one-output transaction paying `pkScript` `value` sat,
// as a node would serve it: its hex and its id.
func fundingTxPaying(t *testing.T, pkScript []byte, value int64) (rawHex, txid string) {
	t.Helper()
	tx := wire.NewMsgTx(txVersion)
	prev, _ := chainhash.NewHashFromStr("1111111111111111111111111111111111111111111111111111111111111111")
	tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{Hash: *prev, Index: 0}, nil, nil))
	tx.AddTxOut(wire.NewTxOut(value, pkScript))
	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return hex.EncodeToString(buf.Bytes()), tx.TxHash().String()
}

// Once anything is staked on the contract, its bytes are the swap's identity.
// A byte-identical resend -- a session retransmission, a re-sync, a second
// paste -- is answered with the swap as it is; different bytes are refused
// however well they audit, and leave contract, funding, refund and Zenon leg
// exactly as they were. Before a commitment, a re-audit still replaces.
func TestAuditFreezesTheContractOnceCommitted(t *testing.T) {
	m := newManager(t, &stubBackend{feeRate: 2})
	_, hash, _ := NewSecret()
	sw, err := m.Create(CreateParams{Role: RoleParticipant, Leg: LegReceive, AmountSats: 400_000,
		DestAddr: regtestDest, SecretHashHex: hex.EncodeToString(hash)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	lock := time.Now().Add(48 * time.Hour).Unix()
	first, _ := BuildContract(mustKey(t).PKH, sw.Key.PKH, lock, hash)
	second, _ := BuildContract(mustKey(t).PKH, sw.Key.PKH, lock+3600, hash) // as good, and different

	// Before anything is staked, the second replaces the first.
	if _, err := m.AuditContract(sw.ID, hex.EncodeToString(first)); err != nil {
		t.Fatalf("first audit: %v", err)
	}
	got, err := m.AuditContract(sw.ID, hex.EncodeToString(second))
	if err != nil {
		t.Fatalf("a re-audit before commitment was refused: %v", err)
	}
	if !bytes.Equal(got.Contract, second) {
		t.Fatal("a re-audit before commitment did not replace the contract")
	}
	// Back to the first, then stake something on it.
	if _, err := m.AuditContract(sw.ID, hex.EncodeToString(first)); err != nil {
		t.Fatalf("audit: %v", err)
	}
	got, _ = m.Store.Load(sw.ID)
	got.Funding = &FundingOutput{TxID: "5ae77294d1bd1dea7fce8b235ae89b80585424aeaa907f181282db9ed9b9fd0a", Value: 400_000, Confirmed: true}
	got.Zenon.HtlcID = "9f"
	// Complete terms, or the verdict is withdrawn on load (F03) and the
	// "nothing changed" comparison below has something to compare against.
	got.Zenon.PeerAddress = "z1qqvwzz2xq7q5gwk6uhcddgrpxlfcyzc8rsu82s"
	got.Zenon.AmountDisplay = "10"
	got.Zenon.Verified = true
	if err := m.Store.Save(got); err != nil {
		t.Fatalf("Save: %v", err)
	}
	before, _ := m.Store.Load(sw.ID)
	events := len(before.Events)

	// The same bytes: nothing new, nothing changed, nothing logged.
	same, err := m.AuditContract(sw.ID, hex.EncodeToString(first))
	if err != nil {
		t.Fatalf("an identical resend was refused: %v", err)
	}
	if !bytes.Equal(same.Contract, first) || len(same.Events) != events {
		t.Errorf("an identical resend changed the record: events %d -> %d", events, len(same.Events))
	}

	// Different bytes: refused, and the record is as it was.
	_, err = m.AuditContract(sw.ID, hex.EncodeToString(second))
	if err == nil {
		t.Fatal("a different contract replaced a funded one")
	}
	if !strings.Contains(err.Error(), "cannot change now") || !strings.Contains(err.Error(), "funding has been seen") {
		t.Errorf("the refusal does not say why: %v", err)
	}
	after, _ := m.Store.Load(sw.ID)
	if !bytes.Equal(after.Contract, first) || after.Funding == nil || after.Zenon.HtlcID != "9f" ||
		!after.Zenon.Verified || after.ContractAddr != before.ContractAddr || after.LockTime != before.LockTime {
		t.Errorf("a refused contract changed the record: %+v", after)
	}

	// Every kind of commitment freezes it, not only funding.
	for name, stake := range map[string]func(s *Swap){
		"a payment sent":       func(s *Swap) { s.FundingBroadcast = &FundingBroadcast{TxID: "ab"} },
		"a Zenon HTLC":         func(s *Swap) { s.Zenon.HtlcID = "9f" },
		"a pre-signed refund":  func(s *Swap) { s.RefundTx = &SpendResult{TxID: "cd"} },
		"a state past funding": func(s *Swap) { s.State = StateFunded },
	} {
		fresh, err := m.Create(CreateParams{Role: RoleParticipant, Leg: LegReceive, AmountSats: 400_000,
			DestAddr: regtestDest, SecretHashHex: hex.EncodeToString(hash)})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		c1, _ := BuildContract(mustKey(t).PKH, fresh.Key.PKH, lock, hash)
		c2, _ := BuildContract(mustKey(t).PKH, fresh.Key.PKH, lock, hash)
		if _, err := m.AuditContract(fresh.ID, hex.EncodeToString(c1)); err != nil {
			t.Fatalf("%s: audit: %v", name, err)
		}
		staked, _ := m.Store.Load(fresh.ID)
		stake(staked)
		if err := m.Store.Save(staked); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if _, err := m.AuditContract(fresh.ID, hex.EncodeToString(c2)); err == nil {
			t.Errorf("%s: a different contract was accepted", name)
		}
	}
}

// The funding side's mirror: the counterparty's pubkey hash builds the
// contract, so a different hash after funding would rebuild it under a
// funding that pays the old one.
func TestCounterpartyPKHFreezesOnceFunded(t *testing.T) {
	m := newManager(t, &stubBackend{feeRate: 2})
	sw, err := m.Create(CreateParams{Role: RoleInitiator, Leg: LegSend, AmountSats: 400_000, DestAddr: regtestDest})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	a, b := mustKey(t), mustKey(t)
	built, err := m.SetCounterpartyPKH(sw.ID, a.PKHHex())
	if err != nil {
		t.Fatalf("SetCounterpartyPKH: %v", err)
	}
	// Before funding, a different hash rebuilds.
	if _, err := m.SetCounterpartyPKH(sw.ID, b.PKHHex()); err != nil {
		t.Fatalf("a rebuild before funding was refused: %v", err)
	}
	if _, err := m.SetCounterpartyPKH(sw.ID, a.PKHHex()); err != nil {
		t.Fatalf("SetCounterpartyPKH: %v", err)
	}
	got, _ := m.Store.Load(sw.ID)
	got.FundingBroadcast = &FundingBroadcast{TxID: "ab"}
	if err := m.Store.Save(got); err != nil {
		t.Fatalf("Save: %v", err)
	}
	same, err := m.SetCounterpartyPKH(sw.ID, a.PKHHex())
	if err != nil || !bytes.Equal(same.Contract, built.Contract) {
		t.Errorf("an identical resend was refused or changed the contract: %v", err)
	}
	if _, err := m.SetCounterpartyPKH(sw.ID, b.PKHHex()); err == nil ||
		!strings.Contains(err.Error(), "cannot change now") {
		t.Errorf("a different hash rebuilt a contract that has been paid: %v", err)
	}
	after, _ := m.Store.Load(sw.ID)
	if !bytes.Equal(after.Contract, built.Contract) {
		t.Error("the refused hash changed the contract")
	}
}

// A funding is bound to the script the chain says it pays. A contract swapped
// out from under it -- however it got there -- is caught before a signature is
// made; a record from before the binding gets it filled in from the chain, and
// a chain that cannot be read is a refusal, not a pass.
func TestFundingIsBoundToTheScriptItPays(t *testing.T) {
	params := &chaincfg.RegressionNetParams
	refund, redeem := mustKey(t), mustKey(t)
	secret, hash, _ := NewSecret()
	lock := time.Now().Add(48 * time.Hour).Unix()
	contract, _ := BuildContract(refund.PKH, redeem.PKH, lock, hash)
	addr, _ := ContractAddress(contract, params)
	pkScript, _ := contractPkScript(contract, params)
	rawHex, txid := fundingTxPaying(t, pkScript, 400_000)

	backend := &stubBackend{
		feeRate: 2,
		utxos:   []chain.UTXO{{TxID: txid, Vout: 0, Value: 400_000, Status: chain.Status{Confirmed: true, BlockHeight: 100}}},
		rawTx:   map[string]string{txid: rawHex},
	}
	m := newManager(t, backend)
	sw := &Swap{
		ID: "aabbccddeeff0088", Network: "regtest", Role: RoleParticipant, Leg: LegReceive,
		State: StateAwaitingFunding, Key: redeem, Secret: secret, SecretHash: hash, AmountSats: 400_000,
		Contract: contract, ContractAddr: addr.String(), DestAddr: regtestDest, LockTime: lock,
	}
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Funding == nil || !strings.EqualFold(got.Funding.PkScriptHex, hex.EncodeToString(pkScript)) {
		t.Fatalf("the funding was not bound to its script on adoption: %+v", got.Funding)
	}

	// The attack, by the only route left: the record's contract is replaced
	// (as a session could once do) while the funding stays. The redeem is
	// refused before signing, by the binding.
	other, _ := BuildContract(mustKey(t).PKH, redeem.PKH, lock, hash)
	otherAddr, _ := ContractAddress(other, params)
	got.Contract, got.ContractAddr = other, otherAddr.String()
	if err := m.Store.Save(got); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := m.Redeem(context.Background(), sw.ID, ""); err == nil ||
		!strings.Contains(err.Error(), "not this") {
		t.Errorf("a redeem of a funding that pays another script was built: %v", err)
	}
	if backend.broadcasts != 0 {
		t.Error("something was broadcast")
	}
	// The same through the signer alone, as the Recover page reaches it.
	if _, err := BuildRedeem(other, *got.Funding, redeem, secret, regtestDest, 2.0, params); err == nil {
		t.Error("the signer built a spend of a funding bound to another script")
	}

	// A record from before the binding: the script is read from the chain
	// before signing, and with the right contract the redeem goes through.
	got.Contract, got.ContractAddr = contract, addr.String()
	got.Funding.PkScriptHex = ""
	if err := m.Store.Save(got); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := m.Redeem(context.Background(), sw.ID, ""); err != nil {
		t.Fatalf("a bound, correct redeem was refused: %v", err)
	}
	bound, _ := m.Store.Load(sw.ID)
	if !strings.EqualFold(bound.Funding.PkScriptHex, hex.EncodeToString(pkScript)) {
		t.Error("the binding was not filled in before signing")
	}

	// And with the chain unreadable, an unbound record is refused, not signed.
	unbound := &Swap{
		ID: "aabbccddeeff0099", Network: "regtest", Role: RoleParticipant, Leg: LegReceive,
		State: StateFunded, Key: redeem, Secret: secret, SecretHash: hash, AmountSats: 400_000,
		Contract: contract, ContractAddr: addr.String(), DestAddr: regtestDest, LockTime: lock,
		Funding: &FundingOutput{TxID: "2222222222222222222222222222222222222222222222222222222222222222", Value: 400_000, Confirmed: true},
	}
	if err := m.Store.Save(unbound); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := m.Redeem(context.Background(), unbound.ID, ""); err == nil ||
		!strings.Contains(err.Error(), "fetch funding transaction") {
		t.Errorf("an unbound funding was spent without reading the chain: %v", err)
	}
}

// countingStorage counts writes, so a test can prove that a resend of what the
// record already holds writes nothing.
type countingStorage struct {
	*MemStorage
	sets int
}

func (c *countingStorage) Set(key, value string) error {
	c.sets++
	return c.MemStorage.Set(key, value)
}

// racingStorage runs a hook the first time a key is read, which is how a test
// makes "something else saved between my load and my save" happen on demand.
type racingStorage struct {
	*MemStorage
	onGet func()
	fired bool
}

func (r *racingStorage) Get(key string) (string, bool) {
	v, ok := r.MemStorage.Get(key)
	if !r.fired && r.onGet != nil {
		r.fired = true
		r.onGet()
	}
	return v, ok
}

// A save is a compare-and-set on the record's version. Two copies loaded, one
// saved: the other is stale, and saving it is refused rather than allowed to
// overwrite what the first decided.
func TestSaveRefusesAStaleCopy(t *testing.T) {
	st := NewStore(NewMemStorage())
	sw := &Swap{ID: "aabbccddeeff00aa", Network: "regtest", Role: RoleInitiator, Leg: LegSend, Key: mustKey(t)}
	if err := st.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}
	a, _ := st.Load(sw.ID)
	b, _ := st.Load(sw.ID)
	a.State = StateFunded
	if err := st.Save(a); err != nil {
		t.Fatalf("saving the first copy: %v", err)
	}
	b.State = StateDraft
	err := st.Save(b)
	if !errors.Is(err, ErrStaleWrite) {
		t.Fatalf("saving a stale copy: got %v, want ErrStaleWrite", err)
	}
	got, _ := st.Load(sw.ID)
	if got.State != StateFunded {
		t.Errorf("the stale copy overwrote the fresh one: state %q", got.State)
	}
	// Reloading and deciding again is the way through.
	c, _ := st.Load(sw.ID)
	if err := st.Save(c); err != nil {
		t.Errorf("a reloaded copy was refused: %v", err)
	}
}

// The race the review described, made deterministic: an audit loads the swap
// before a refresh records its funding, and saves after. The freeze it checked
// against was the unfunded record; the save must lose, and the funding and the
// original contract must stand.
func TestAuditCannotRaceAStake(t *testing.T) {
	params := &chaincfg.RegressionNetParams
	_, hash, _ := NewSecret()
	backing := NewMemStorage()
	m := &Manager{Store: NewStore(backing), Chain: &stubBackend{feeRate: 2}, Network: "regtest"}
	sw, err := m.Create(CreateParams{Role: RoleParticipant, Leg: LegReceive, AmountSats: 400_000,
		DestAddr: regtestDest, SecretHashHex: hex.EncodeToString(hash)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	lock := time.Now().Add(48 * time.Hour).Unix()
	first, _ := BuildContract(mustKey(t).PKH, sw.Key.PKH, lock, hash)
	second, _ := BuildContract(mustKey(t).PKH, sw.Key.PKH, lock, hash)
	if _, err := m.AuditContract(sw.ID, hex.EncodeToString(first)); err != nil {
		t.Fatalf("audit: %v", err)
	}
	firstAddr, _ := ContractAddress(first, params)
	pkScript, _ := contractPkScript(first, params)
	rawHex, txid := fundingTxPaying(t, pkScript, 400_000)

	// The audit's own store reads through a storage that, on the first read,
	// lets a refresh through another store on the same backing record the
	// funding -- exactly a goroutine interleaving, without the goroutines.
	racing := &racingStorage{MemStorage: backing}
	racing.onGet = func() {
		other := &Manager{Store: NewStore(backing), Network: "regtest", Chain: &stubBackend{
			feeRate: 2,
			utxos:   []chain.UTXO{{TxID: txid, Vout: 0, Value: 400_000, Status: chain.Status{Confirmed: true, BlockHeight: 100}}},
			rawTx:   map[string]string{txid: rawHex},
		}}
		if _, err := other.Refresh(context.Background(), sw.ID); err != nil {
			t.Fatalf("the interleaved refresh failed: %v", err)
		}
	}
	auditor := &Manager{Store: NewStore(racing), Network: "regtest"}
	_, err = auditor.AuditContract(sw.ID, hex.EncodeToString(second))
	if err == nil {
		t.Fatal("an audit that loaded before the funding saved over it")
	}
	if !errors.Is(err, ErrStaleWrite) {
		t.Errorf("refused, but not as a stale write: %v", err)
	}
	got, _ := m.Store.Load(sw.ID)
	if !bytes.Equal(got.Contract, first) || got.ContractAddr != firstAddr.String() {
		t.Errorf("the losing audit replaced the contract: %x", got.Contract)
	}
	if got.Funding == nil || got.Funding.TxID != txid {
		t.Errorf("the funding the refresh recorded was lost: %+v", got.Funding)
	}
	// And now that the record is funded, the same audit is refused by the
	// freeze itself, on a fresh load.
	if _, err := m.AuditContract(sw.ID, hex.EncodeToString(second)); err == nil ||
		!strings.Contains(err.Error(), "cannot change now") {
		t.Errorf("after the race, the freeze did not hold: %v", err)
	}
}

// A resend of what the record already holds is answered, not written.
func TestIdenticalResendsWriteNothing(t *testing.T) {
	counting := &countingStorage{MemStorage: NewMemStorage()}
	m := &Manager{Store: NewStore(counting), Network: "regtest"}
	_, hash, _ := NewSecret()
	recv, err := m.Create(CreateParams{Role: RoleParticipant, Leg: LegReceive, AmountSats: 400_000,
		DestAddr: regtestDest, SecretHashHex: hex.EncodeToString(hash)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	contract, _ := BuildContract(mustKey(t).PKH, recv.Key.PKH, time.Now().Add(48*time.Hour).Unix(), hash)
	if _, err := m.AuditContract(recv.ID, hex.EncodeToString(contract)); err != nil {
		t.Fatalf("audit: %v", err)
	}
	send, err := m.Create(CreateParams{Role: RoleInitiator, Leg: LegSend, AmountSats: 400_000, DestAddr: regtestDest})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pkh := mustKey(t).PKHHex()
	if _, err := m.SetCounterpartyPKH(send.ID, pkh); err != nil {
		t.Fatalf("SetCounterpartyPKH: %v", err)
	}
	before := counting.sets
	if _, err := m.AuditContract(recv.ID, hex.EncodeToString(contract)); err != nil {
		t.Errorf("identical contract refused: %v", err)
	}
	if _, err := m.SetCounterpartyPKH(send.ID, pkh); err != nil {
		t.Errorf("identical pkh refused: %v", err)
	}
	if counting.sets != before {
		t.Errorf("identical resends wrote %d time(s)", counting.sets-before)
	}
}

// The binding is required before the refund is pre-signed, before a
// pre-signed refund is broadcast, and when a larger output replaces the
// funding. A chain that cannot say what the output pays holds all three.
func TestRefundsAreBuiltAndBroadcastOnlyOverBoundFunding(t *testing.T) {
	params := &chaincfg.RegressionNetParams
	refund, redeem := mustKey(t), mustKey(t)
	_, hash, _ := NewSecret()
	lock := time.Now().Add(-time.Hour).Unix() + 48*3600 // 47h out: not yet refundable
	contract, _ := BuildContract(refund.PKH, redeem.PKH, lock, hash)
	addr, _ := ContractAddress(contract, params)
	pkScript, _ := contractPkScript(contract, params)
	rawHex, txid := fundingTxPaying(t, pkScript, 400_000)
	mined := chain.Status{Confirmed: true, BlockHeight: 100}

	// 1. Refresh adopts a SHORT funding whose transaction cannot be read: no
	//    pre-signed refund, one log line over two polls, the binding left for
	//    later. Short, so the scan keeps looking and a covering output can
	//    replace it below -- once the agreed amount is covered, it stops.
	shortHex, shortTxid := fundingTxPaying(t, pkScript, 100_000)
	backend := &stubBackend{feeRate: 2, utxos: []chain.UTXO{{TxID: shortTxid, Vout: 0, Value: 100_000, Status: mined}}}
	m := newManager(t, backend)
	sw := &Swap{
		ID: "aabbccddeeff00bb", Network: "regtest", Role: RoleInitiator, Leg: LegSend,
		State: StateAwaitingFunding, Key: refund, SecretHash: hash, AmountSats: 400_000,
		Contract: contract, ContractAddr: addr.String(), DestAddr: regtestDest, LockTime: lock,
	}
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Funding == nil {
		t.Fatal("funding not adopted")
	}
	if got.RefundTx != nil {
		t.Error("a refund was pre-signed over a funding that could not be bound")
	}
	if got.Funding.PkScriptHex != "" {
		t.Error("a binding was recorded from nowhere")
	}
	got, _ = m.Refresh(context.Background(), sw.ID)
	notes := 0
	for _, ev := range got.Events {
		if strings.Contains(ev.Message, "nothing is built or offered") {
			notes++
		}
	}
	if notes != 1 {
		t.Errorf("the holding note was logged %d times over two refreshes, want once", notes)
	}

	// 2. The transaction becomes readable: bound, and pre-signed.
	backend.rawTx = map[string]string{shortTxid: shortHex}
	got, err = m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.RefundTx == nil || !strings.EqualFold(got.Funding.PkScriptHex, hex.EncodeToString(pkScript)) {
		t.Fatalf("once readable, the funding was not bound and pre-signed: %+v", got.Funding)
	}

	// 3. A covering output appears. Unreadable: switched to, the old refund
	//    dropped, and nothing re-signed until it is bound. Readable: bound and
	//    pre-signed again.
	backend.utxos = append(backend.utxos, chain.UTXO{TxID: txid, Vout: 0, Value: 400_000, Status: mined})
	got, err = m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Funding.TxID != txid || got.RefundTx != nil || got.Funding.PkScriptHex != "" {
		t.Errorf("an unreadable covering output was not switched to unbound and un-signed: %+v, refund %v",
			got.Funding, got.RefundTx != nil)
	}
	backend.rawTx[txid] = rawHex
	got, _ = m.Refresh(context.Background(), sw.ID)
	if got.RefundTx == nil || !strings.EqualFold(got.Funding.PkScriptHex, hex.EncodeToString(pkScript)) {
		t.Errorf("a readable covering output was not bound and pre-signed: %+v", got.Funding)
	}

	// 4. A pre-signed refund from before the binding, over a record whose
	//    contract no longer matches what the output pays: not broadcast.
	past := time.Now().Add(-time.Hour).Unix()
	if past < LockTimeThreshold {
		t.Skip("clock before the locktime threshold")
	}
	// The chain says the output pays a different script: a real transaction,
	// under its own id, paying another contract.
	otherScript, _ := contractPkScript(func() []byte { c, _ := BuildContract(mustKey(t).PKH, redeem.PKH, lock, hash); return c }(), params)
	otherHex, otherTxid := fundingTxPaying(t, otherScript, 400_000)
	backend.rawTx[otherTxid] = otherHex
	stale := &Swap{
		ID: "aabbccddeeff00cc", Network: "regtest", Role: RoleInitiator, Leg: LegSend,
		State: StateFunded, Key: refund, SecretHash: hash, AmountSats: 400_000,
		Contract: contract, ContractAddr: addr.String(), DestAddr: regtestDest, LockTime: past,
		Funding:  &FundingOutput{TxID: otherTxid, Vout: 0, Value: 400_000, Confirmed: true},
		RefundTx: &SpendResult{TxID: "dd", RawHex: "00"},
	}
	if err := m.Store.Save(stale); err != nil {
		t.Fatalf("Save: %v", err)
	}
	broadcasts := backend.broadcasts
	if _, err := m.Refund(context.Background(), stale.ID, ""); err == nil ||
		!strings.Contains(err.Error(), "not this swap") {
		t.Errorf("a pre-signed refund over a mismatched funding was broadcast or refused for another reason: %v", err)
	}
	if backend.broadcasts != broadcasts {
		t.Error("something was broadcast")
	}
	// And unreadable: refused, not broadcast.
	delete(backend.rawTx, otherTxid)
	stale2, _ := m.Store.Load(stale.ID)
	stale2.Funding.PkScriptHex = ""
	if err := m.Store.Save(stale2); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := m.Refund(context.Background(), stale.ID, ""); err == nil ||
		!strings.Contains(err.Error(), "fetch funding transaction") {
		t.Errorf("a pre-signed refund over an unreadable funding was broadcast: %v", err)
	}
	if backend.broadcasts != broadcasts {
		t.Error("something was broadcast")
	}
}

// A recovery file from before the binding cannot be checked offline. It is
// built only on the user's say-so, with a warning; a file that carries the
// binding is checked, and a mismatch is refused regardless.
func TestRebuildNeedsConsentForAnUnboundFile(t *testing.T) {
	params := &chaincfg.RegressionNetParams
	refund, redeem := mustKey(t), mustKey(t)
	_, hash, _ := NewSecret()
	lock := time.Now().Add(48 * time.Hour).Unix()
	contract, _ := BuildContract(refund.PKH, redeem.PKH, lock, hash)
	addr, _ := ContractAddress(contract, params)
	pkScript, _ := contractPkScript(contract, params)
	wif, err := btcutil.NewWIF(refund.PrivKey(), params, true)
	if err != nil {
		t.Fatalf("WIF: %v", err)
	}
	file := func(pkScriptHex string) string {
		f := map[string]any{
			"swapId": "aabbccddeeff00dd", "network": "regtest",
			"contractHex": hex.EncodeToString(contract), "contractAddr": addr.String(),
			"lockTime": lock, "privateKeyWIF": wif.String(), "destAddr": regtestDest,
			"funding": map[string]any{"txid": "5ae77294d1bd1dea7fce8b235ae89b80585424aeaa907f181282db9ed9b9fd0a",
				"vout": 0, "value": 400000, "pkScriptHex": pkScriptHex},
		}
		raw, _ := json.Marshal(f)
		return string(raw)
	}
	if _, err := Rebuild(RebuildRequest{File: file("")}); err == nil ||
		!strings.Contains(err.Error(), "does not record what the funding output pays") {
		t.Errorf("an unbound file was built without consent: %v", err)
	}
	res, err := Rebuild(RebuildRequest{File: file(""), AllowUnboundFunding: true})
	if err != nil {
		t.Fatalf("an unbound refund was refused with consent: %v", err)
	}
	if res.Action != "refund" || res.Warning == "" || res.RawHex == "" {
		t.Errorf("built with consent but not as a warned refund: %+v", res)
	}
	// The redeem key, with the preimage: a redeem carries the preimage, and
	// an invalid one shows it to whatever it is submitted to. No consent path.
	secret, _, _ := NewSecret()
	redeemHash := SHA256(secret)
	redeemContract, _ := BuildContract(refund.PKH, redeem.PKH, lock, redeemHash)
	redeemAddr, _ := ContractAddress(redeemContract, params)
	redeemWIF, _ := btcutil.NewWIF(redeem.PrivKey(), params, true)
	redeemFile := func(pkScriptHex string) string {
		f := map[string]any{
			"swapId": "aabbccddeeff00de", "network": "regtest",
			"contractHex": hex.EncodeToString(redeemContract), "contractAddr": redeemAddr.String(),
			"lockTime": lock, "privateKeyWIF": redeemWIF.String(), "destAddr": regtestDest,
			"secretHex": hex.EncodeToString(secret),
			"funding": map[string]any{"txid": "5ae77294d1bd1dea7fce8b235ae89b80585424aeaa907f181282db9ed9b9fd0a",
				"vout": 0, "value": 400000, "pkScriptHex": pkScriptHex},
		}
		raw, _ := json.Marshal(f)
		return string(raw)
	}
	for _, consent := range []bool{false, true} {
		if _, err := Rebuild(RebuildRequest{File: redeemFile(""), AllowUnboundFunding: consent}); err == nil ||
			!strings.Contains(err.Error(), "carries the preimage") {
			t.Errorf("consent=%v: an unbound redeem was built: %v", consent, err)
		}
	}
	redeemScript, _ := contractPkScript(redeemContract, params)
	if res, err := Rebuild(RebuildRequest{File: redeemFile(hex.EncodeToString(redeemScript))}); err != nil || res.Action != "redeem" {
		t.Errorf("a bound redeem was refused: %v", err)
	}
	res, err = Rebuild(RebuildRequest{File: file(hex.EncodeToString(pkScript))})
	if err != nil || res.Warning != "" {
		t.Errorf("a bound, matching file: err %v warning %q", err, res.Warning)
	}
	otherScript, _ := contractPkScript(func() []byte { c, _ := BuildContract(mustKey(t).PKH, redeem.PKH, lock, hash); return c }(), params)
	if _, err := Rebuild(RebuildRequest{File: file(hex.EncodeToString(otherScript)), AllowUnboundFunding: true}); err == nil ||
		!strings.Contains(err.Error(), "not this") {
		t.Errorf("a file whose funding pays another script was built: %v", err)
	}
}

// The store's other decisions are made under its lock too: an import adds a
// record only if none exists at the moment of writing, and a delete removes
// only what is still safe to remove at the moment of removal. A write that
// fails leaves the caller's version where it was.
func TestStoreDecidesUnderItsLock(t *testing.T) {
	backing := NewMemStorage()
	st := NewStore(backing)
	sw := &Swap{ID: "aabbccddeeff00ee", Network: "regtest", Role: RoleInitiator, Leg: LegSend, Key: mustKey(t)}
	added, err := st.SaveIfAbsent(sw)
	if err != nil || !added {
		t.Fatalf("first SaveIfAbsent: added=%v err=%v", added, err)
	}
	// A record created in between: the second import of the same id must
	// not overwrite it, whatever version the imported copy carries.
	stored, _ := st.Load(sw.ID)
	stored.State = StateFunded
	if err := st.Save(stored); err != nil {
		t.Fatalf("Save: %v", err)
	}
	dup := &Swap{ID: sw.ID, Network: "regtest", Role: RoleInitiator, Leg: LegSend, Key: sw.Key, Version: stored.Version}
	added, err = st.SaveIfAbsent(dup)
	if err != nil || added {
		t.Errorf("an existing record was overwritten by an import: added=%v err=%v", added, err)
	}
	if got, _ := st.Load(sw.ID); got.State != StateFunded {
		t.Errorf("the existing record was replaced: state %q", got.State)
	}

	// Delete: the decision runs on the record as it is when it is removed.
	refused := st.DeleteIf(sw.ID, func(s *Swap) error {
		if s.State == StateFunded {
			return errors.New("funded: keep it")
		}
		return nil
	})
	if refused == nil {
		t.Error("a record the check refused was deleted")
	}
	if _, err := st.Load(sw.ID); err != nil {
		t.Error("the record is gone after a refused delete")
	}
	if err := st.DeleteIf(sw.ID, func(*Swap) error { return nil }); err != nil {
		t.Errorf("an allowed delete failed: %v", err)
	}
	if _, err := st.Load(sw.ID); err == nil {
		t.Error("the record survived an allowed delete")
	}

	// A failed write does not advance the caller's version.
	failing := NewStore(&failingSetStorage{MemStorage: backing})
	v := &Swap{ID: "aabbccddeeff00ff", Network: "regtest", Role: RoleInitiator, Leg: LegSend, Key: mustKey(t), Version: 4}
	if err := failing.Save(v); err == nil {
		t.Fatal("a failing storage saved")
	}
	if v.Version != 4 {
		t.Errorf("a failed write advanced the version to %d", v.Version)
	}
}

type failingSetStorage struct{ *MemStorage }

func (f *failingSetStorage) Set(string, string) error { return errors.New("quota") }

// What happened on the chain is recorded even when the record changed while
// the broadcast was in flight: the outcome is applied to the record as it now
// is, the concurrent change is kept, and nothing is broadcast twice.
func TestAnOutcomeSurvivesAStaleSave(t *testing.T) {
	params := &chaincfg.RegressionNetParams
	refund, redeem := mustKey(t), mustKey(t)
	secret, hash, _ := NewSecret()
	lock := time.Now().Add(48 * time.Hour).Unix()
	contract, _ := BuildContract(refund.PKH, redeem.PKH, lock, hash)
	addr, _ := ContractAddress(contract, params)
	pkScript, _ := contractPkScript(contract, params)
	rawHex, txid := fundingTxPaying(t, pkScript, 400_000)
	backing := NewMemStorage()
	backend := &stubBackend{feeRate: 2, rawTx: map[string]string{txid: rawHex}}
	sw := &Swap{
		ID: "aabbccddeeff0110", Network: "regtest", Role: RoleParticipant, Leg: LegReceive,
		State: StateFunded, Key: redeem, Secret: secret, SecretHash: hash, AmountSats: 400_000,
		Contract: contract, ContractAddr: addr.String(), DestAddr: regtestDest, LockTime: lock,
		Funding: &FundingOutput{TxID: txid, Vout: 0, Value: 400_000, Confirmed: true, PkScriptHex: hex.EncodeToString(pkScript)},
	}
	if err := NewStore(backing).Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Between Redeem's load and its save, another call archives the swap.
	racing := &racingStorage{MemStorage: backing}
	racing.onGet = func() {
		other := NewStore(backing)
		s, _ := other.Load(sw.ID)
		s.Archived = true
		if err := other.Save(s); err != nil {
			t.Fatalf("interleaved save: %v", err)
		}
	}
	m := &Manager{Store: NewStore(racing), Chain: backend, Network: "regtest"}
	got, err := m.Redeem(context.Background(), sw.ID, "")
	if err != nil {
		t.Fatalf("Redeem after a concurrent change: %v", err)
	}
	if got.State != StateRedeemed || got.RedeemTx == nil {
		t.Errorf("the outcome was not recorded: state %q", got.State)
	}
	if !got.Archived {
		t.Error("the concurrent change was lost")
	}
	if backend.broadcasts != 1 {
		t.Errorf("%d broadcasts, want exactly one", backend.broadcasts)
	}
	stored, _ := m.Store.Load(sw.ID)
	if stored.State != StateRedeemed || !stored.Archived {
		t.Errorf("the stored record disagrees: %+v", stored)
	}
}

// A backend answering with a transaction other than the one asked for cannot
// bind a funding to a script the real output does not have.
func TestBindingRequiresTheTransactionAskedFor(t *testing.T) {
	params := &chaincfg.RegressionNetParams
	refund, redeem := mustKey(t), mustKey(t)
	_, hash, _ := NewSecret()
	contract, _ := BuildContract(refund.PKH, redeem.PKH, time.Now().Add(48*time.Hour).Unix(), hash)
	addr, _ := ContractAddress(contract, params)
	pkScript, _ := contractPkScript(contract, params)
	rawHex, _ := fundingTxPaying(t, pkScript, 400_000)
	const claimed = "5ae77294d1bd1dea7fce8b235ae89b80585424aeaa907f181282db9ed9b9fd0a"
	backend := &stubBackend{feeRate: 2, rawTx: map[string]string{claimed: rawHex}}
	sw := &Swap{Network: "regtest", Contract: contract, ContractAddr: addr.String(),
		Funding: &FundingOutput{TxID: claimed, Vout: 0, Value: 400_000}}
	err := bindFunding(context.Background(), backend, sw, params)
	if err == nil || !strings.Contains(err.Error(), "when asked for") {
		t.Errorf("a substituted transaction bound the funding: %v", err)
	}
	if sw.Funding.PkScriptHex != "" {
		t.Error("a binding was recorded from a substituted transaction")
	}
}

// The receiving leg too: a funding that cannot be bound is seen but not acted
// on, and the binding is tried again on every poll until it is.
func TestReceiveLegFundingIsNotActionableUntilBound(t *testing.T) {
	params := &chaincfg.RegressionNetParams
	refund, redeem := mustKey(t), mustKey(t)
	secret, hash, _ := NewSecret()
	lock := time.Now().Add(48 * time.Hour).Unix()
	contract, _ := BuildContract(refund.PKH, redeem.PKH, lock, hash)
	addr, _ := ContractAddress(contract, params)
	pkScript, _ := contractPkScript(contract, params)
	rawHex, txid := fundingTxPaying(t, pkScript, 400_000)
	backend := &stubBackend{feeRate: 2, utxos: []chain.UTXO{{TxID: txid, Vout: 0, Value: 400_000, Status: chain.Status{Confirmed: true, BlockHeight: 100}}}}
	m := newManager(t, backend)
	sw := &Swap{
		ID: "aabbccddeeff0120", Network: "regtest", Role: RoleParticipant, Leg: LegReceive,
		State: StateAwaitingFunding, Key: redeem, Secret: secret, SecretHash: hash, AmountSats: 400_000,
		Contract: contract, ContractAddr: addr.String(), DestAddr: regtestDest, LockTime: lock,
	}
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Funding == nil {
		t.Fatal("funding not seen")
	}
	if view(got).FundingBound {
		t.Error("an unbound funding reads as bound")
	}
	if _, err := m.Redeem(context.Background(), sw.ID, ""); err == nil {
		t.Error("an unbound funding was redeemed")
	}
	got, _ = m.Refresh(context.Background(), sw.ID)
	notes := 0
	for _, ev := range got.Events {
		if strings.Contains(ev.Message, "nothing is built or offered") {
			notes++
		}
	}
	if notes != 1 {
		t.Errorf("the holding note was logged %d times, want once", notes)
	}
	backend.rawTx = map[string]string{txid: rawHex}
	got, err = m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !view(got).FundingBound {
		t.Error("the receiving leg's funding was not bound on a later poll")
	}
}

// An outcome is recorded only on the swap it happened to: a reloaded record
// naming another funding or contract is refused, and a deleted one is not
// brought back. The transaction id travels in the error either way.
func TestAnOutcomeIsNotAttachedToAnotherIdentity(t *testing.T) {
	params := &chaincfg.RegressionNetParams
	refund, redeem := mustKey(t), mustKey(t)
	secret, hash, _ := NewSecret()
	lock := time.Now().Add(48 * time.Hour).Unix()
	contract, _ := BuildContract(refund.PKH, redeem.PKH, lock, hash)
	addr, _ := ContractAddress(contract, params)
	pkScript, _ := contractPkScript(contract, params)
	rawHex, txid := fundingTxPaying(t, pkScript, 400_000)
	otherHex, otherTxid := fundingTxPaying(t, pkScript, 500_000)
	mk := func(id string) (*MemStorage, *stubBackend) {
		backing := NewMemStorage()
		backend := &stubBackend{feeRate: 2, rawTx: map[string]string{txid: rawHex, otherTxid: otherHex}}
		sw := &Swap{
			ID: id, Network: "regtest", Role: RoleParticipant, Leg: LegReceive,
			State: StateFunded, Key: redeem, Secret: secret, SecretHash: hash, AmountSats: 400_000,
			Contract: contract, ContractAddr: addr.String(), DestAddr: regtestDest, LockTime: lock,
			Funding: &FundingOutput{TxID: txid, Vout: 0, Value: 400_000, Confirmed: true, PkScriptHex: hex.EncodeToString(pkScript)},
		}
		if err := NewStore(backing).Save(sw); err != nil {
			t.Fatalf("Save: %v", err)
		}
		return backing, backend
	}

	// The funding switched under the broadcast.
	backing, backend := mk("aabbccddeeff0130")
	racing := &racingStorage{MemStorage: backing}
	racing.onGet = func() {
		st := NewStore(backing)
		s, _ := st.Load("aabbccddeeff0130")
		s.Funding = &FundingOutput{TxID: otherTxid, Vout: 0, Value: 500_000, Confirmed: true, PkScriptHex: hex.EncodeToString(pkScript)}
		if err := st.Save(s); err != nil {
			t.Fatalf("interleaved save: %v", err)
		}
	}
	m := &Manager{Store: NewStore(racing), Chain: backend, Network: "regtest"}
	_, err := m.Redeem(context.Background(), "aabbccddeeff0130", "")
	if err == nil || !strings.Contains(err.Error(), "different funding or contract") {
		t.Errorf("an outcome was attached to a swap naming another funding: %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "was broadcast") {
		t.Errorf("the error does not carry what happened on the chain: %v", err)
	}
	if backend.broadcasts != 1 {
		t.Errorf("%d broadcasts, want one", backend.broadcasts)
	}
	stored, _ := NewStore(backing).Load("aabbccddeeff0130")
	if stored.State == StateRedeemed || stored.Funding.TxID != otherTxid {
		t.Errorf("the reloaded record was changed: %+v", stored)
	}

	// Deleted under the broadcast.
	backing, backend = mk("aabbccddeeff0131")
	racing = &racingStorage{MemStorage: backing}
	racing.onGet = func() {
		if err := NewStore(backing).Delete("aabbccddeeff0131"); err != nil {
			t.Fatalf("interleaved delete: %v", err)
		}
	}
	m = &Manager{Store: NewStore(racing), Chain: backend, Network: "regtest"}
	_, err = m.Redeem(context.Background(), "aabbccddeeff0131", "")
	if err == nil || !strings.Contains(err.Error(), "gone from the store") {
		t.Errorf("a deleted swap was brought back by an outcome: %v", err)
	}
	if _, err := NewStore(backing).Load("aabbccddeeff0131"); err == nil {
		t.Error("the deleted record was resurrected")
	}
}

// A backup restores a record whose stored value no longer parses; "already
// here" is for records that are really here.
func TestImportRestoresACorruptRecord(t *testing.T) {
	backing := NewMemStorage()
	st := NewStore(backing)
	sw := &Swap{ID: "aabbccddeeff0140", Network: "regtest", Role: RoleInitiator, Leg: LegSend, Key: mustKey(t)}
	if err := st.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}
	backup, err := st.Export()
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	k, _ := st.key(sw.ID)
	if err := backing.Set(k, "{not json"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := st.Load(sw.ID); err == nil {
		t.Fatal("the corrupt record loaded")
	}
	added, skipped, rejected, err := st.Import(backup)
	if err != nil || added != 1 || skipped != 0 || rejected != 0 {
		t.Errorf("import over a corrupt record: added=%d skipped=%d rejected=%d err=%v", added, skipped, rejected, err)
	}
	if got, err := st.Load(sw.ID); err != nil || got.ID != sw.ID {
		t.Errorf("the record was not restored: %v", err)
	}
	// And a record that IS here is left alone.
	added, skipped, _, _ = st.Import(backup)
	if added != 0 || skipped != 1 {
		t.Errorf("a healthy record was overwritten: added=%d skipped=%d", added, skipped)
	}
}

// The error a stale write comes back as is named, so a caller acts on the
// kind rather than the words.
func TestStaleWriteIsNamedOnTheWire(t *testing.T) {
	raw := errorJSON(fmt.Errorf("wrapped: %w", ErrStaleWrite))
	var doc map[string]string
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if doc["code"] != "stale" {
		t.Errorf("a stale write is not named: %s", raw)
	}
	raw = errorJSON(errors.New("something else"))
	plain := map[string]string{}
	_ = json.Unmarshal(raw, &plain)
	if _, named := plain["code"]; named {
		t.Errorf("an ordinary error carries a code: %s", raw)
	}
}

// A funding whose transaction pays a different contract is not bound, is
// never recorded as bound, is said so once, and cannot have ZNN locked
// against it -- the shape a replaced contract leaves behind.
func TestAMismatchedFundingIsNeverBound(t *testing.T) {
	params := &chaincfg.RegressionNetParams
	refund, redeem := mustKey(t), mustKey(t)
	_, hash, _ := NewSecret()
	lock := time.Now().Add(48 * time.Hour).Unix()
	contract, _ := BuildContract(refund.PKH, redeem.PKH, lock, hash)
	addr, _ := ContractAddress(contract, params)
	// The chain: a real transaction, under its own id, paying ANOTHER contract.
	other, _ := BuildContract(mustKey(t).PKH, redeem.PKH, lock, hash)
	otherScript, _ := contractPkScript(other, params)
	otherHex, otherTxid := fundingTxPaying(t, otherScript, 400_000)
	backend := &stubBackend{
		feeRate: 2,
		utxos:   []chain.UTXO{{TxID: otherTxid, Vout: 0, Value: 400_000, Status: chain.Status{Confirmed: true, BlockHeight: 100}}},
		rawTx:   map[string]string{otherTxid: otherHex},
	}
	m := newManager(t, backend)
	sw := &Swap{
		ID: "aabbccddeeff0150", Network: "regtest", Role: RoleParticipant, Leg: LegReceive,
		State: StateAwaitingFunding, Key: redeem, SecretHash: hash, AmountSats: 400_000,
		Contract: contract, ContractAddr: addr.String(), DestAddr: regtestDest, LockTime: lock,
		Zenon: ZenonLeg{PeerAddress: "z1qqvwzz2xq7q5gwk6uhcddgrpxlfcyzc8rsu82s", AmountDisplay: "10"},
	}
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Funding == nil {
		t.Fatal("funding not seen")
	}
	if got.Funding.PkScriptHex != "" {
		t.Errorf("a mismatching script was recorded on the funding: %s", got.Funding.PkScriptHex)
	}
	if got.FundingBound() || view(got).FundingBound {
		t.Error("a mismatching funding reads as bound")
	}
	got, _ = m.Refresh(context.Background(), sw.ID)
	notes, misread := 0, 0
	for _, ev := range got.Events {
		if strings.Contains(ev.Message, "NOT this swap's contract") {
			notes++
		}
		if strings.Contains(ev.Message, "could not read the funding transaction") {
			misread++
		}
	}
	if notes != 1 {
		t.Errorf("the mismatch was logged %d times over two polls, want once", notes)
	}
	if misread != 0 {
		t.Errorf("a mismatch was also logged as a transaction that could not be read, %d time(s)", misread)
	}
	// planCreate: nothing locked against it, before any node is asked.
	plan := &walletBlockPlan{Block: newWalletBlock(69, "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d")}
	err = planCreate(context.Background(), m, got, "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d", nil, plan)
	if err == nil || !strings.Contains(err.Error(), "not been confirmed to pay this contract") {
		t.Errorf("a create was planned against a mismatched funding: %v", err)
	}
	// And the same refusal while the funding is merely unread.
	unread := &Swap{
		ID: "aabbccddeeff0151", Network: "regtest", Role: RoleParticipant, Leg: LegReceive,
		State: StateFunded, Key: redeem, SecretHash: hash, AmountSats: 400_000,
		Contract: contract, ContractAddr: addr.String(), DestAddr: regtestDest, LockTime: lock,
		Funding: &FundingOutput{TxID: "5ae77294d1bd1dea7fce8b235ae89b80585424aeaa907f181282db9ed9b9fd0a", Value: 400_000, Confirmed: true},
		Zenon:   ZenonLeg{PeerAddress: "z1qqvwzz2xq7q5gwk6uhcddgrpxlfcyzc8rsu82s", AmountDisplay: "10"},
	}
	plan = &walletBlockPlan{Block: newWalletBlock(69, "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d")}
	if err := planCreate(context.Background(), m, unread, "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d", nil, plan); err == nil ||
		!strings.Contains(err.Error(), "not been confirmed to pay this contract") {
		t.Errorf("a create was planned against an unread funding: %v", err)
	}
	// A bound record is judged against the contract it holds NOW: a script
	// recorded for one contract does not bind another.
	bound := &Swap{Network: "regtest", Contract: contract,
		Funding: &FundingOutput{TxID: otherTxid, Value: 400_000, PkScriptHex: hex.EncodeToString(otherScript)}}
	if bound.FundingBound() {
		t.Error("a script recorded for another contract reads as bound to this one")
	}
}

// The outcome identity check, one field at a time: the output index alone,
// and the contract alone.
func TestAnOutcomeChecksEachPartOfTheIdentity(t *testing.T) {
	params := &chaincfg.RegressionNetParams
	refund, redeem := mustKey(t), mustKey(t)
	secret, hash, _ := NewSecret()
	lock := time.Now().Add(48 * time.Hour).Unix()
	contract, _ := BuildContract(refund.PKH, redeem.PKH, lock, hash)
	addr, _ := ContractAddress(contract, params)
	pkScript, _ := contractPkScript(contract, params)
	rawHex, txid := fundingTxPaying(t, pkScript, 400_000)
	for name, change := range map[string]func(s *Swap){
		"the output index": func(s *Swap) { s.Funding.Vout = 1 },
		"the contract": func(s *Swap) {
			s.Contract, _ = BuildContract(mustKey(t).PKH, redeem.PKH, lock, hash)
		},
	} {
		backing := NewMemStorage()
		backend := &stubBackend{feeRate: 2, rawTx: map[string]string{txid: rawHex}}
		sw := &Swap{
			ID: "aabbccddeeff0160", Network: "regtest", Role: RoleParticipant, Leg: LegReceive,
			State: StateFunded, Key: redeem, Secret: secret, SecretHash: hash, AmountSats: 400_000,
			Contract: contract, ContractAddr: addr.String(), DestAddr: regtestDest, LockTime: lock,
			Funding: &FundingOutput{TxID: txid, Vout: 0, Value: 400_000, Confirmed: true, PkScriptHex: hex.EncodeToString(pkScript)},
		}
		if err := NewStore(backing).Save(sw); err != nil {
			t.Fatalf("Save: %v", err)
		}
		racing := &racingStorage{MemStorage: backing}
		racing.onGet = func() {
			st := NewStore(backing)
			s, _ := st.Load(sw.ID)
			change(s)
			if err := st.Save(s); err != nil {
				t.Fatalf("interleaved save: %v", err)
			}
		}
		m := &Manager{Store: NewStore(racing), Chain: backend, Network: "regtest"}
		_, err := m.Redeem(context.Background(), sw.ID, "")
		if err == nil || !strings.Contains(err.Error(), "different funding or contract") {
			t.Errorf("%s changed: the outcome was attached anyway: %v", name, err)
		}
		stored, _ := NewStore(backing).Load(sw.ID)
		if stored.State == StateRedeemed {
			t.Errorf("%s changed: the record was marked redeemed", name)
		}
	}
}

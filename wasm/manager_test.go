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

	"github.com/btcsuite/btcd/chaincfg"
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
	receiving := func(id string, role Role, value int64) *Swap {
		return &Swap{
			ID: id, Network: "regtest", Role: role, Leg: LegReceive, State: StateFunded,
			Key: redeemKey, Secret: secret, SecretHash: hash, AmountSats: 400_000,
			Contract: contract, ContractAddr: addr.String(), DestAddr: regtestDest,
			LockTime: lock,
			Funding:  &FundingOutput{TxID: txid, Vout: 0, Value: value, Confirmed: true},
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
	const shortTx = "5ae77294d1bd1dea7fce8b235ae89b80585424aeaa907f181282db9ed9b9fd0a"
	const secondShortTx = "1111111111111111111111111111111111111111111111111111111111111111"
	const fullTx = "2222222222222222222222222222222222222222222222222222222222222222"
	mined := chain.Status{Confirmed: true, BlockHeight: 100}
	backend := &stubBackend{
		feeRate:  2,
		utxos:    []chain.UTXO{{TxID: shortTx, Vout: 0, Value: 150_000, Status: mined}},
		txStatus: map[string]chain.Status{shortTx: mined, secondShortTx: mined, fullTx: mined},
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

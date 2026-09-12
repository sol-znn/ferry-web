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
	"github.com/zenon/ferry-web-v2/wasm/chain"
	"github.com/zenon/ferry-web-v2/wasm/znn"
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

	base := btcZnn(RoleInitiator)
	base.Out.SelfAddr = ""
	if _, err := m.Create(base); err == nil {
		t.Fatal("created a swap with nowhere to pay")
	}
	blank := btcZnn(RoleInitiator)
	blank.Out.SelfAddr = "   "
	if _, err := m.Create(blank); err == nil {
		t.Fatal("accepted whitespace as a destination")
	}

	sw := mustCreate(t, m, btcZnn(RoleInitiator))
	// An agreed amount needs a token to be an amount OF: with no token standard
	// there are no decimals to convert it with and nothing to compare the HTLC's
	// own token against, so verification would have to refuse it.
	if sw.In.Token != znn.ZnnTokenStandard {
		t.Errorf("token standard is %q, want the ZNN default %q",
			sw.In.Token, znn.ZnnTokenStandard)
	}
	// A Solana leg has the same problem in a different shape: an escrow lives at
	// a PDA of one deployment, and without naming which there is no account to
	// look at.
	noProgram := solZnn(RoleInitiator)
	noProgram.Out.Program = ""
	if _, err := m.Create(noProgram); err == nil {
		t.Fatal("created a Solana leg with no program named")
	}
}

// The four pairs this build supports, and the two shapes that look like pairs
// and are not: a chain against itself with nothing being traded.
func TestCreateAcceptsEveryPairAndRefusesNonTrades(t *testing.T) {
	m := newManager(t, &stubBackend{feeRate: 2})
	for name, p := range map[string]CreateParams{
		"btc-znn": btcZnn(RoleInitiator),
		"znn-btc": znnBtc(RoleParticipant),
		"znn-znn": znnZnn(RoleInitiator),
		"sol-znn": solZnn(RoleInitiator),
		"sol-btc": solBtc(RoleParticipant),
	} {
		p := p
		if p.Role == RoleParticipant {
			p.SecretHashHex = strings.Repeat("ab", 32)
		}
		t.Run(name, func(t *testing.T) {
			sw, err := m.Create(p)
			if err != nil {
				t.Fatalf("refused a supported pair: %v", err)
			}
			if len(sw.Chains()) == 0 {
				t.Fatal("a swap that settles on no chain")
			}
			// The initiator's own leg is the long one, whatever chain it lands
			// on. That is the whole ordering rule and it does not mention a
			// chain anywhere.
			if sw.OutIsInitiators() != (sw.Role == RoleInitiator) {
				t.Errorf("the long leg is on the wrong side for role %s", sw.Role)
			}
		})
	}

	same := znnZnn(RoleInitiator)
	same.In.Token = same.Out.Token
	if _, err := m.Create(same); err == nil {
		t.Fatal("a ZTS against the same ZTS was accepted; nothing is being traded")
	}
	btcBtc := btcZnn(RoleInitiator)
	btcBtc.In = CreateLeg{Chain: ChainBTC, Amount: "1", SelfAddr: regtestDest}
	if _, err := m.Create(btcBtc); err == nil {
		t.Fatal("Bitcoin against Bitcoin was accepted")
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

	selfBad := btcZnn(RoleInitiator)
	selfBad.In.SelfAddr = btcAddr
	if _, err := m.Create(selfBad); err == nil {
		t.Error("accepted a Bitcoin address in the Zenon self-address field")
	}

	peerBad := btcZnn(RoleInitiator)
	peerBad.In.PeerAddr = btcAddr
	if _, err := m.Create(peerBad); err == nil {
		t.Error("accepted a Bitcoin address in the Zenon peer-address field")
	}

	// And the same accident on a Solana leg: a base58 field will take almost
	// anything, so the check is that the bytes decode to a 32-byte key.
	solBad := solZnn(RoleInitiator)
	solBad.Out.SelfAddr = znnSelfAddr
	if _, err := m.Create(solBad); err == nil {
		t.Error("accepted a Zenon address in the Solana address field")
	}

	// A Zenon address is a plausible paste into the token field too — both are
	// bech32 and both start with "z" — and a length check alone would not catch
	// it if the two byte lengths happened to coincide, which is why the prefix
	// is checked and not just the size.
	tokenBad := btcZnn(RoleInitiator)
	tokenBad.In.Token = validAddr
	if _, err := m.Create(tokenBad); err == nil {
		t.Error("accepted a Zenon ADDRESS in the Zenon TOKEN field")
	}

	tokenGarbage := btcZnn(RoleInitiator)
	tokenGarbage.In.Token = btcAddr
	if _, err := m.Create(tokenGarbage); err == nil {
		t.Error("accepted a Bitcoin address in the Zenon token field")
	}

	// Valid values, and blank, must still both work: the fields are optional,
	// not merely tolerant of garbage.
	ok := btcZnn(RoleInitiator)
	ok.In.SelfAddr = validAddr
	ok.In.PeerAddr = validAddr
	ok.In.Token = znn.ZnnTokenStandard
	if _, err := m.Create(ok); err != nil {
		t.Errorf("rejected well-formed Zenon fields: %v", err)
	}
	blankAddrs := btcZnn(RoleInitiator)
	blankAddrs.In.SelfAddr, blankAddrs.In.PeerAddr = "", ""
	if _, err := m.Create(blankAddrs); err != nil {
		t.Errorf("rejected blank Zenon addresses, which are optional: %v", err)
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
		ID: "0011223344556677", Network: "regtest", Role: RoleParticipant,
		State: StateFunded, SecretHash: hash,
		In: &Leg{Chain: ChainBTC, Dir: DirIn, Amount: "400000", Base: "400000",
			SelfAddr: regtestDest, Expiry: time.Now().Add(48 * time.Hour).Unix(),
			Btc: &BtcLeg{Key: redeemKey, Contract: contract, ContractAddr: addr.String(),
				Funding: &funding}},
		Out: &Leg{Chain: ChainZNN, Dir: DirOut, Amount: "10", Znn: &ZnnLeg{}},
	}
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.In.Btc.Claimed || got.In.Btc.Reclaimed {
		t.Error("a transaction that does not spend our output decided the leg")
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
		ID: "0011223344556678", Network: "regtest", Role: RoleParticipant,
		State: StateFunded, SecretHash: hash,
		Out: &Leg{Chain: ChainBTC, Dir: DirOut, Amount: "400000", Base: "400000",
			SelfAddr: regtestDest, Expiry: time.Now().Add(48 * time.Hour).Unix(),
			Btc: &BtcLeg{Key: refundKey, Contract: contract, ContractAddr: addr.String(),
				Funding: &funding}},
		In: &Leg{Chain: ChainZNN, Dir: DirIn, Amount: "10", Znn: &ZnnLeg{}},
	}
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !got.Out.Btc.Claimed {
		t.Error("the redeem of our own outpoint was not recorded")
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
		ID: "0011223344556679", Network: "regtest", Role: RoleParticipant,
		State: StateFunded, SecretHash: hash,
		In: &Leg{Chain: ChainBTC, Dir: DirIn, Amount: "400000", Base: "400000",
			SelfAddr: regtestDest, Expiry: time.Now().Add(48 * time.Hour).Unix(),
			Btc: &BtcLeg{Key: redeemKey, Contract: contract, ContractAddr: addr.String()}},
		Out: &Leg{Chain: ChainZNN, Dir: DirOut, Amount: "10", SelfAddr: znnSelfAddr,
			PeerAddr: payee,
			Znn:      &ZnnLeg{HtlcID: strings.Repeat("9f", 32), Verified: true}},
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
	if !got.Out.Znn.UnlockSeen {
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
		ID: "001122334455667a", Network: "regtest", Role: RoleParticipant,
		State: StateFunded, SecretHash: hash,
		Out: &Leg{Chain: ChainZNN, Dir: DirOut, Amount: "10", Znn: &ZnnLeg{}},
		In: &Leg{Chain: ChainBTC, Dir: DirIn, Amount: "400000", Base: "400000",
			SelfAddr: regtestDest, Expiry: time.Now().Add(48 * time.Hour).Unix(),
			Btc: &BtcLeg{}},
	}
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := m.SetSecret(sw.ID, hex.EncodeToString(secret))
	if err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	if got.Out.Znn.UnlockSeen {
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
		ID: "0011223344556679", Network: "regtest", Role: RoleInitiator,
		State: StateAwaiting, SecretHash: hash,
		Out: &Leg{Chain: ChainBTC, Dir: DirOut, Amount: "400000", Base: "400000",
			Expiry: time.Now().Add(48 * time.Hour).Unix(),
			Btc:    &BtcLeg{Key: refundKey, Contract: contract, ContractAddr: addr.String()}},
		In: &Leg{Chain: ChainZNN, Dir: DirIn, Amount: "10", Znn: &ZnnLeg{}},
	}
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Out.Btc.RefundTx != nil {
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
		ID: "aabbccddeeff0011", Network: "regtest", Role: RoleInitiator,
		State: StateAwaiting, SecretHash: hash,
		Out: &Leg{Chain: ChainBTC, Dir: DirOut, Amount: "400000", Base: "400000",
			SelfAddr: regtestDest, Expiry: lock,
			Btc: &BtcLeg{Key: refundKey, Contract: contract, ContractAddr: addr.String()}},
		In: &Leg{Chain: ChainZNN, Dir: DirIn, Amount: "10", Znn: &ZnnLeg{}},
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
	if got.Out.Btc.Funding == nil {
		t.Fatal("the funding was not adopted")
	}
	if got.Out.Btc.Funding.Confirmed || got.Out.Btc.Funding.Confirmations != 0 {
		t.Errorf("an unmined funding reads confirmed=%v depth=%d, want false and 0",
			got.Out.Btc.Funding.Confirmed, got.Out.Btc.Funding.Confirmations)
	}

	// Mined into the tip. The stub's tip is 100, so that is one confirmation.
	backend.txStatus = map[string]chain.Status{
		txid: {Confirmed: true, BlockHeight: 100},
	}
	got, err = m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !got.Out.Btc.Funding.Confirmed || got.Out.Btc.Funding.Confirmations != 1 {
		t.Errorf("a funding in the tip block reads confirmed=%v depth=%d, want true and 1",
			got.Out.Btc.Funding.Confirmed, got.Out.Btc.Funding.Confirmations)
	}

	// Deeper than this app counts. The number is pinned rather than left to run.
	backend.txStatus[txid] = chain.Status{Confirmed: true, BlockHeight: 10}
	got, err = m.Refresh(context.Background(), sw.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.Out.Btc.Funding.Confirmations != confirmationsTracked {
		t.Errorf("a funding 91 blocks deep reads %d, want it pinned at %d",
			got.Out.Btc.Funding.Confirmations, confirmationsTracked)
	}
}

// A reorg that takes the funding's block away has to take its depth with it.
// Leaving a stale count behind is the one failure that matters here: it says
// "settled" about a payment that has stopped existing.
func TestRefreshForgetsDepthWhenAFundingIsUnmined(t *testing.T) {
	sw := fundedSwap(t)
	txid := "5ae77294d1bd1dea7fce8b235ae89b80585424aeaa907f181282db9ed9b9fd0a"
	sw.State = StateFunded
	sw.Out.Btc.Funding = &FundingOutput{
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
	if got.Out.Btc.Funding.Confirmed || got.Out.Btc.Funding.Confirmations != 0 {
		t.Errorf("after the block went away the funding still reads confirmed=%v depth=%d",
			got.Out.Btc.Funding.Confirmed, got.Out.Btc.Funding.Confirmations)
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
	if got.Out.Btc.FundingBroadcast == nil || got.Out.Btc.FundingBroadcast.TxID != txid {
		t.Fatalf("the broadcast was not recorded: %+v", got.Out.Btc.FundingBroadcast)
	}
	if got.Out.Btc.Funding != nil || got.State == StateFunded {
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
	recv.Out, recv.In = recv.In, recv.Out
	recv.Out.Dir, recv.In.Dir = DirOut, DirIn
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
		ID: "00112233445566aa", Network: "regtest", Role: RoleInitiator,
		State: StateAwaiting, SecretHash: hash,
		Out: &Leg{Chain: ChainBTC, Dir: DirOut, Amount: "400000", Base: "400000",
			SelfAddr: regtestDest, Expiry: time.Now().Add(48 * time.Hour).Unix(),
			Btc: &BtcLeg{}},
		In: &Leg{Chain: ChainZNN, Dir: DirIn, Amount: "10", Znn: &ZnnLeg{}},
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
		if _, _, verr := m.VerifyZenon(context.Background(), sw.ID, DirIn, htlcID); verr == nil {
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
		ID: "00112233445566dd", Network: "regtest", Role: RoleParticipant,
		State: StateAwaiting, SecretHash: hash,
		Out: &Leg{Chain: ChainZNN, Dir: DirOut, Amount: "10", SelfAddr: znnSelfAddr,
			Znn: &ZnnLeg{}},
		In: &Leg{Chain: ChainBTC, Dir: DirIn, Amount: "400000", Base: "400000",
			SelfAddr: regtestDest, Expiry: time.Now().Add(24 * time.Hour).Unix(),
			Btc: &BtcLeg{}},
	}
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// It fails verification — the hashlock is not this swap's — and that is
	// beside the point. The entry was READ, and what it says is recorded either
	// way, because a rejection has to be able to name both halves of a mismatch.
	got, _, _ := m.VerifyZenon(context.Background(), sw.ID, DirOut, strings.Repeat("9f", 32))
	if got == nil {
		t.Fatal("VerifyZenon returned no swap")
	}
	if got.Out.Znn.ObservedHashLocked != paid {
		t.Errorf("the observed payout address was not recorded: got %q, want %q",
			got.Out.Znn.ObservedHashLocked, paid)
	}
	// The agreed recipient is still blank. Adopting the observed one as the
	// expectation is what would make a rejected HTLC re-verify clean.
	if got.Out.PeerAddr != "" {
		t.Errorf("the observed address was adopted as the agreed one: %q", got.Out.PeerAddr)
	}
	// And it is what the preimage search will use, having nothing better.
	if got.Out.unlockPayee() != paid {
		t.Errorf("unlockPayee() = %q, want %q", got.Out.unlockPayee(), paid)
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

	committed := &Leg{Chain: ChainBTC, Dir: DirOut, SelfAddr: regtestDest}

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
	empty := &Leg{Chain: ChainBTC, Dir: DirOut}
	got, err = payoutAddress(empty, regtestDest)
	if err != nil || got != regtestDest {
		t.Errorf("a record with no address should adopt the one supplied: got %q, %v", got, err)
	}
	if _, err = payoutAddress(empty, ""); err == nil {
		t.Error("a record with no address and none supplied should be refused")
	}
}

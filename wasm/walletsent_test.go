package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// What happens to the HTLC id in the seconds after the wallet publishes.
//
// A create is verified through the ordinary path the moment the wallet reports
// it, and that verification almost always fails the first time: an account
// block takes a momentum or two to become readable, and the wallet answers as
// soon as it has published. So the interesting case is not the happy one, it is
// this one -- and the swap has to come out of it holding the id.

// notYetOnChain is a Zenon node that has never heard of anything: the HTLC
// contract has no such entry and the ledger has no such transaction. It is what
// this browser's node looks like for the momentum or two after a create.
func notYetOnChain(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "embedded.htlc.getById":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32000, "message": "data non existent"},
			})
		case "ledger.getAccountBlockByHash":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID, "result": nil,
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID, "result": nil,
			})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

const publishedHash = "73cf0a610d26890d18275c26b949b75e8a587b1f001cd06bde5a6aecb7ab420f"

// sentCreate reports one create through handleWalletSent and hands back the
// swap as it was left in the store -- not as it was returned, because what the
// next page load reads is the stored one.
func sentCreate(t *testing.T, hash string) (*API, map[string]any, *Swap) {
	t.Helper()
	node := notYetOnChain(t)

	api := &API{Store: NewStore(NewMemStorage())}
	sw := &Swap{ID: "a1b2c3d4", Role: RoleParticipant, State: StateAwaiting,
		Out: &Leg{Chain: ChainZNN, Dir: DirOut, Amount: "10",
			PeerAddr: "z1qqvwzz2xq7q5gwk6uhcddgrpxlfcyzc8rsu82s", Znn: &ZnnLeg{}},
		In: &Leg{Chain: ChainBTC, Dir: DirIn, Amount: "400000", Btc: &BtcLeg{}},
	}
	if err := api.Store.Save(sw); err != nil {
		t.Fatal(err)
	}

	body, err := json.Marshal(map[string]any{
		"id":     "a1b2c3d4",
		"action": "create",
		"hash":   hash,
		"from":   testAddr,
		"settings": map[string]any{
			"network": "regtest", "btcEsplora": "http://esplora.invalid", "znnUrl": node.URL,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := handleWalletSent(t.Context(), api, body)
	if err != nil {
		t.Fatalf("a published create was reported as an error: %v", err)
	}
	resp, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("unexpected response shape %T", got)
	}
	stored, err := api.Store.Load("a1b2c3d4")
	if err != nil {
		t.Fatal(err)
	}
	return api, resp, stored
}

// The bug this test is here for: the id used to exist, after a pending verify,
// nowhere but in a sentence on the card. A reload lost it -- and the card,
// seeing a swap with no Zenon HTLC, went back to offering to create one.
// Pressing that locks a second lot of ZNN in a second contract.
func TestWalletSentRecordsTheIdEvenWhenItIsNotOnChainYet(t *testing.T) {
	_, resp, stored := sentCreate(t, publishedHash)

	if resp["pending"] != true {
		t.Errorf("a create the node cannot see yet was not reported as pending: %v", resp)
	}
	if stored.Out.Znn.HtlcID != publishedHash {
		t.Errorf("the HTLC id was not written to the swap: got %q, want %q",
			stored.Out.Znn.HtlcID, publishedHash)
	}
	if stored.Out.SelfAddr != testAddr {
		t.Errorf("the account that signed was not recorded: got %q", stored.Out.SelfAddr)
	}
}

// Recording the id must not be mistaken for having checked it. The chain has
// said nothing about this HTLC yet, and the card reads Verified to decide
// whether to say so.
func TestWalletSentDoesNotClaimAnUnseenHtlcIsVerified(t *testing.T) {
	_, _, stored := sentCreate(t, publishedHash)

	if stored.Out.Znn.Verified {
		t.Error("an HTLC no node has confirmed was recorded as verified")
	}
	// Something must say why, or the card shows an unverified HTLC with no
	// stated reason. That is now the pending flag rather than an error string.
	if !stored.Out.Znn.VerifyPending {
		t.Error("nothing was recorded to explain why the HTLC is not verified")
	}
}

// Not yet checked is not the same as checked and found wanting, and the
// difference is what the card paints red.
//
// A create this page watched the wallet publish is unreadable for a momentum or
// two, which is ordinary. Storing VerifyZenon's message for that put a
// destructive badge on every healthy HTLC saying the id was probably mistyped
// or the node on the wrong network -- both false, with the true reason last.
func TestWalletSentReportsAnUnseenHtlcAsPendingRatherThanFailed(t *testing.T) {
	_, resp, stored := sentCreate(t, publishedHash)

	if strings.TrimSpace(stored.Out.Znn.VerifyError) != "" {
		t.Errorf("a create that is merely unconfirmed was stored as a verification failure: %q",
			stored.Out.Znn.VerifyError)
	}
	// The reason still reaches the caller, so the card can say "waiting" in
	// words. It is just not recorded as a verdict against the HTLC.
	if resp["pending"] != true || strings.TrimSpace(resp["error"].(string)) == "" {
		t.Errorf("the pending reason was not reported to the caller: %v", resp)
	}
}

// A wallet that publishes and reports no hash leaves nothing to record. That is
// worth an error rather than a swap quietly holding an empty id, because the
// answer -- Find it, or the wallet's own history -- is something the user has
// to be told to go and do.
func TestWalletSentRefusesACreateWithNoHash(t *testing.T) {
	node := notYetOnChain(t)
	api := &API{Store: NewStore(NewMemStorage())}
	if err := api.Store.Save(&Swap{ID: "a1b2c3d4", Role: RoleParticipant,
		Out: &Leg{Chain: ChainZNN, Dir: DirOut, Amount: "10", Znn: &ZnnLeg{}},
		In:  &Leg{Chain: ChainBTC, Dir: DirIn, Amount: "400000", Btc: &BtcLeg{}}}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"id": "a1b2c3d4", "action": "create", "hash": "  ", "from": testAddr,
		"settings": map[string]any{
			"network": "regtest", "btcEsplora": "http://esplora.invalid", "znnUrl": node.URL,
		},
	})
	_, err := handleWalletSent(t.Context(), api, body)
	if err == nil {
		t.Fatal("a create with no transaction hash was accepted")
	}
	if !strings.Contains(err.Error(), "Find it") {
		t.Errorf("the error does not say how to recover the id: %v", err)
	}
}

// An unlock leaves nothing behind to ask about, so it has to be written down.
//
// The contract DELETES the entry as it settles, which means that afterwards a
// node cannot tell "this leg was collected" from "no such id" -- both answer
// "data non existent". The card decides whether to keep offering the Syrius
// unlock button from this record and nothing else, because the alternative it
// used to use, whether the swap was still active, is a fact about the BITCOIN
// side: for the user who sends BTC and is owed ZNN, their contract being
// redeemed ends the Bitcoin story while their own ZNN is still locked up.
func TestWalletSentRecordsAnUnlock(t *testing.T) {
	node := notYetOnChain(t)
	api := &API{Store: NewStore(NewMemStorage())}
	// LegSend: this user sends BTC, so the counterparty created the Zenon HTLC
	// and this user is the one who unlocks it.
	sw := &Swap{ID: "a1b2c3d4", Role: RoleInitiator, State: StateFunded,
		Out: &Leg{Chain: ChainBTC, Dir: DirOut, Amount: "400000", Btc: &BtcLeg{}},
		In: &Leg{Chain: ChainZNN, Dir: DirIn, Amount: "10",
			Znn: &ZnnLeg{HtlcID: publishedHash}},
	}
	if err := api.Store.Save(sw); err != nil {
		t.Fatal(err)
	}

	body, err := json.Marshal(map[string]any{
		"id":     "a1b2c3d4",
		"action": "unlock",
		"hash":   publishedHash,
		"from":   testAddr,
		"settings": map[string]any{
			"network": "regtest", "btcEsplora": "http://esplora.invalid", "znnUrl": node.URL,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handleWalletSent(t.Context(), api, body); err != nil {
		t.Fatalf("a published unlock was reported as an error: %v", err)
	}

	stored, err := api.Store.Load("a1b2c3d4")
	if err != nil {
		t.Fatal(err)
	}
	if stored.In.Znn.UnlockHash != publishedHash {
		t.Errorf("the unlock was not recorded: got %q, want %q",
			stored.In.Znn.UnlockHash, publishedHash)
	}
}

// A create must not look like a settled leg. The two share a code path and one
// field decides whether the card goes on offering to unlock.
func TestWalletSentDoesNotRecordACreateAsAnUnlock(t *testing.T) {
	_, _, stored := sentCreate(t, publishedHash)

	if stored.Out.Znn.UnlockHash != "" {
		t.Errorf("a create was recorded as an unlock: %q", stored.Out.Znn.UnlockHash)
	}
}

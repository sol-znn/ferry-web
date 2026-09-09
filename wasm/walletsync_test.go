package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zenon/ferry-web/wasm/chain"
	"github.com/zenon/ferry-web/wasm/znn"
)

// A stub go-zenon node, answering only the two calls the wallet check makes.
//
// The point of the whole file is the case where two of these disagree while
// claiming the same chain identifier, which is what two unrelated devnets look
// like — and what no amount of comparing chain ids will ever catch.
type fakeNode struct {
	chainID uint64
	height  uint64
	// momentumHash is what this node says the momentum at any height is. Two
	// nodes on one chain agree on it; two nodes on different chains do not.
	momentumHash string
}

func (f fakeNode) serve() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		reply := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID, "result": result,
			})
		}
		switch req.Method {
		case "ledger.getFrontierMomentum":
			reply(map[string]any{
				"height":          f.height,
				"hash":            f.momentumHash,
				"chainIdentifier": f.chainID,
				"timestamp":       1788700000,
			})
		case "ledger.getMomentumsByHeight":
			height := uint64(1)
			if len(req.Params) > 0 {
				if n, ok := req.Params[0].(float64); ok {
					height = uint64(n)
				}
			}
			reply(map[string]any{"list": []map[string]any{{
				"height": height, "hash": f.momentumHash, "chainIdentifier": f.chainID,
			}}})
		default:
			http.Error(w, "unexpected method "+req.Method, http.StatusNotFound)
		}
	}))
}

const (
	chainAHash = "1111111111111111111111111111111111111111111111111111111111111111"
	chainBHash = "2222222222222222222222222222222222222222222222222222222222222222"
	testAddr   = "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d"
)

// syncAgainst runs the gate with this browser pointed at `ours` and the wallet
// claiming `walletURL`.
func syncAgainst(t *testing.T, ours *httptest.Server, walletURL string, chainID uint64) *walletSyncResult {
	t.Helper()
	api := &API{Store: NewStore(NewMemStorage())}
	mgr := &Manager{
		Store:   api.Store,
		Chain:   chain.New("http://esplora.invalid"),
		Znn:     znn.New(ours.URL),
		Network: "regtest",
	}
	return runWalletSync(t.Context(), api, mgr, walletSyncReq{
		Address: testAddr,
		ChainID: chainID,
		NodeURL: walletURL,
	})
}

// The case this file exists for. Both nodes call themselves chain 69 — every
// go-zenon devnet does — and they are not the same chain. Nothing but comparing
// a momentum catches it, and an HTLC created through the wallet would be
// invisible to this page and to the counterparty.
func TestWalletOnADifferentChainWithTheSameID(t *testing.T) {
	ours := fakeNode{chainID: 69, height: 30000, momentumHash: chainAHash}.serve()
	defer ours.Close()
	theirs := fakeNode{chainID: 69, height: 30000, momentumHash: chainBHash}.serve()
	defer theirs.Close()

	got := syncAgainst(t, ours, theirs.URL, 69)
	if got.Ok {
		t.Fatal("two unrelated chains sharing an identifier were accepted")
	}
	if got.SameChain {
		t.Error("SameChain was set for chains whose momentums differ")
	}
	if len(got.Problems) == 0 || !strings.Contains(got.Problems[0], "different chains") {
		t.Errorf("the mismatch was not named as different chains: %v", got.Problems)
	}
}

func TestWalletOnTheSameChain(t *testing.T) {
	ours := fakeNode{chainID: 69, height: 30000, momentumHash: chainAHash}.serve()
	defer ours.Close()
	// A separate node, slightly behind, on the same chain. It must pass: two
	// nodes are never exactly level, and refusing over that would refuse
	// everything.
	theirs := fakeNode{chainID: 69, height: 29990, momentumHash: chainAHash}.serve()
	defer theirs.Close()

	got := syncAgainst(t, ours, theirs.URL, 69)
	if !got.Ok {
		t.Fatalf("two nodes on one chain were refused: %v %v", got.Problems, got.Unchecked)
	}
	if !got.SameChain {
		t.Error("SameChain was not set even though the momentums matched")
	}
}

// A node far enough behind to compute an expiry the chain disagrees with is a
// problem even when it is provably the same chain.
func TestWalletNodeTooFarBehind(t *testing.T) {
	ours := fakeNode{chainID: 69, height: 30000, momentumHash: chainAHash}.serve()
	defer ours.Close()
	theirs := fakeNode{chainID: 69, height: 20000, momentumHash: chainAHash}.serve()
	defer theirs.Close()

	got := syncAgainst(t, ours, theirs.URL, 69)
	if got.Ok {
		t.Fatal("a wallet node ten thousand momentums behind was accepted")
	}
	if !got.SameChain {
		t.Error("the chains do match, and that should still have been established")
	}
}

// "Could not tell" must not read as "fine". This is the case a wallet pointed
// at a chain this page cannot reach looks like from here, so it blocks.
func TestUnreachableWalletNodeBlocks(t *testing.T) {
	ours := fakeNode{chainID: 69, height: 30000, momentumHash: chainAHash}.serve()
	defer ours.Close()
	// Served and then closed: the port is dead, which is what an unreachable
	// node is.
	dead := fakeNode{chainID: 69, height: 30000, momentumHash: chainAHash}.serve()
	deadURL := dead.URL
	dead.Close()

	got := syncAgainst(t, ours, deadURL, 69)
	if got.Ok {
		t.Fatal("a wallet whose node could not be read was accepted")
	}
	if len(got.Unchecked) == 0 {
		t.Error("the failure was not reported as a check that could not run")
	}
	if got.refusal() == nil {
		t.Error("a failed sync produced no refusal")
	}
}

// The extension's configured chain id disagreeing with its own node is its own
// finding: it means a wallet set to one chain while pointed at another.
func TestWalletConfiguredForAnotherChain(t *testing.T) {
	ours := fakeNode{chainID: 69, height: 30000, momentumHash: chainAHash}.serve()
	defer ours.Close()
	theirs := fakeNode{chainID: 69, height: 30000, momentumHash: chainAHash}.serve()
	defer theirs.Close()

	got := syncAgainst(t, ours, theirs.URL, 1)
	if got.Ok {
		t.Fatal("a wallet configured for chain 1 against a chain 69 node was accepted")
	}
	if len(got.Problems) == 0 || !strings.Contains(got.Problems[0], "configured for chain 1") {
		t.Errorf("the mismatch was not named: %v", got.Problems)
	}
}

// A wallet that reports no node at all cannot be compared against anything, and
// that is a refusal rather than a pass.
func TestWalletWithNoNodeURL(t *testing.T) {
	ours := fakeNode{chainID: 69, height: 30000, momentumHash: chainAHash}.serve()
	defer ours.Close()

	got := syncAgainst(t, ours, "", 69)
	if got.Ok {
		t.Fatal("a wallet that named no node was accepted")
	}
	if len(got.Unchecked) == 0 {
		t.Error("the missing node URL was not reported as a check that could not run")
	}
}

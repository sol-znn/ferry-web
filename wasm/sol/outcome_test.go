package sol

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// What became of an escrow is read out of the chain's history, and the history
// is a set anybody can add to: getSignaturesForAddress indexes a transaction
// under every account key it names, so "a transaction that mentions this
// escrow" costs a stranger one fee.
//
// These are the two decoys that follow from that, and the reason they matter is
// the same in both cases. Both branches close the escrow, so the settling
// transaction is the only remaining copy of the preimage -- a decoy that
// answers in its place leaves the counterparty unable to claim the other leg,
// which then expires back to whoever published the decoy.

// stubNode is a Solana JSON-RPC node that answers only the two calls the
// outcome scan makes.
type stubNode struct {
	// signatures, newest first, as getSignaturesForAddress returns them.
	signatures []string
	// txs maps a signature to the transaction the node reports for it.
	txs map[string]any
}

func (s *stubNode) start(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("stub node got an undecodable request: %v", err)
			return
		}
		var result any
		switch req.Method {
		case "getSignaturesForAddress":
			list := []any{}
			for _, sig := range s.signatures {
				list = append(list, map[string]any{"signature": sig, "slot": 1})
			}
			result = list
		case "getTransaction":
			var params []any
			_ = json.Unmarshal(req.Params, &params)
			sig, _ := params[0].(string)
			result = s.txs[sig]
		default:
			t.Errorf("stub node was asked for %q, which the scan should not need", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

// tx builds what getTransaction returns: an account list, and instructions
// indexing into it.
func tx(keys []string, instructions ...map[string]any) map[string]any {
	return map[string]any{
		"transaction": map[string]any{
			"message": map[string]any{
				"accountKeys":  keys,
				"instructions": instructions,
			},
		},
		"meta": map[string]any{"err": nil},
	}
}

func ix(programIndex int, accounts []int, data []byte) map[string]any {
	return map[string]any{
		"programIdIndex": programIndex,
		"accounts":       accounts,
		"data":           EncodeBase58(data),
	}
}

func pubkeyFrom(seed byte) Pubkey {
	var p Pubkey
	for i := range p {
		p[i] = seed
	}
	return p
}

// The decoy that hides a preimage. A real redeem publishes the secret; a
// transaction submitted straight afterwards, naming this escrow as an
// otherwise-unused account key and carrying a refund of an escrow the attacker
// controls, is newer and would win a scan that matched on the program id alone.
// The escrow is closed by then, so nothing will ever push it back down the
// list: the wrong answer is permanent.
func TestOutcomeIgnoresAnInstructionForAnotherEscrow(t *testing.T) {
	programID := pubkeyFrom(1)
	ours := pubkeyFrom(2)
	theirs := pubkeyFrom(3)
	receiver := pubkeyFrom(4)
	initiator := pubkeyFrom(5)

	preimage := []byte(strings.Repeat("s", 32))
	hashlock := sha256.Sum256(preimage)

	node := &stubNode{
		// Newest first: the decoy, then the real redeem.
		signatures: []string{"decoy", "real"},
		txs: map[string]any{
			// A refund of THEIR escrow, with ours along for the ride as an
			// account key the instruction never touches.
			"decoy": tx(
				[]string{programID.String(), theirs.String(), initiator.String(), ours.String()},
				ix(0, []int{1, 2}, []byte{tagRefund}),
			),
			"real": tx(
				[]string{programID.String(), ours.String(), receiver.String(), initiator.String()},
				ix(0, []int{1, 2, 3}, append([]byte{tagRedeem, byte(len(preimage))}, preimage...)),
			),
		},
	}
	client := node.start(t)

	out, err := client.FindEscrowOutcome(context.Background(), programID, ours, hashlock, 50)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if out.Refunded {
		t.Fatal("the decoy's refund was reported as this escrow's outcome; " +
			"the preimage is lost and the other leg cannot be claimed")
	}
	if !out.Redeemed {
		t.Fatal("the real redeem was not found")
	}
	if string(out.Preimage) != string(preimage) {
		t.Fatalf("wrong preimage: %x", out.Preimage)
	}
}

// The same decoy in its strongest form: an instruction that really does spend
// this escrow's address as far as the account list is concerned, carrying a
// preimage that is perfectly valid -- for a different swap. Only hashing it
// against the hashlock this swap committed to can catch it.
func TestOutcomeIgnoresARedeemThatDoesNotOpenThisHashlock(t *testing.T) {
	programID := pubkeyFrom(1)
	ours := pubkeyFrom(2)
	receiver := pubkeyFrom(4)
	initiator := pubkeyFrom(5)

	preimage := []byte(strings.Repeat("s", 32))
	hashlock := sha256.Sum256(preimage)
	other := []byte(strings.Repeat("x", 32))

	node := &stubNode{
		signatures: []string{"decoy", "real"},
		txs: map[string]any{
			"decoy": tx(
				[]string{programID.String(), ours.String(), receiver.String(), initiator.String()},
				ix(0, []int{1, 2, 3}, append([]byte{tagRedeem, byte(len(other))}, other...)),
			),
			"real": tx(
				[]string{programID.String(), ours.String(), receiver.String(), initiator.String()},
				ix(0, []int{1, 2, 3}, append([]byte{tagRedeem, byte(len(preimage))}, preimage...)),
			),
		},
	}
	client := node.start(t)

	out, err := client.FindEscrowOutcome(context.Background(), programID, ours, hashlock, 50)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if !out.Redeemed {
		t.Fatal("the real redeem was not found past the decoy")
	}
	if string(out.Preimage) != string(preimage) {
		t.Fatalf("adopted the decoy's preimage: %x", out.Preimage)
	}
}

// A redeem reached by CPI is still a redeem: the escrow is closed and the
// preimage is on chain. Missing it would be missing a real settlement.
func TestOutcomeFindsARedeemInvokedByAnotherProgram(t *testing.T) {
	programID := pubkeyFrom(1)
	ours := pubkeyFrom(2)
	receiver := pubkeyFrom(4)
	initiator := pubkeyFrom(5)
	caller := pubkeyFrom(9)

	preimage := []byte(strings.Repeat("s", 32))
	hashlock := sha256.Sum256(preimage)

	inner := tx([]string{caller.String(), programID.String(), ours.String(), receiver.String(), initiator.String()},
		ix(0, []int{2, 3, 4}, []byte{0x00}))
	inner["meta"] = map[string]any{
		"err": nil,
		"innerInstructions": []any{
			map[string]any{
				"index":        0,
				"instructions": []any{ix(1, []int{2, 3, 4}, append([]byte{tagRedeem, byte(len(preimage))}, preimage...))},
			},
		},
	}

	node := &stubNode{signatures: []string{"cpi"}, txs: map[string]any{"cpi": inner}}
	client := node.start(t)

	out, err := client.FindEscrowOutcome(context.Background(), programID, ours, hashlock, 50)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if !out.Redeemed || string(out.Preimage) != string(preimage) {
		t.Fatalf("a redeem invoked by CPI was not found: %+v", out)
	}
}

// A failed transaction proves nothing, and a wrong preimage produces one.
func TestOutcomeSkipsFailedTransactions(t *testing.T) {
	programID := pubkeyFrom(1)
	ours := pubkeyFrom(2)
	receiver := pubkeyFrom(4)
	initiator := pubkeyFrom(5)

	preimage := []byte(strings.Repeat("s", 32))
	hashlock := sha256.Sum256(preimage)

	failed := tx([]string{programID.String(), ours.String(), receiver.String(), initiator.String()},
		ix(0, []int{1, 2, 3}, []byte{tagRefund}))
	failed["meta"] = map[string]any{"err": map[string]any{"InstructionError": []any{0, "Custom"}}}

	node := &stubNode{
		signatures: []string{"failed", "real"},
		txs: map[string]any{
			"failed": failed,
			"real": tx(
				[]string{programID.String(), ours.String(), receiver.String(), initiator.String()},
				ix(0, []int{1, 2, 3}, append([]byte{tagRedeem, byte(len(preimage))}, preimage...)),
			),
		},
	}
	client := node.start(t)

	out, err := client.FindEscrowOutcome(context.Background(), programID, ours, hashlock, 50)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if out.Refunded {
		t.Fatal("a failed refund was reported as the outcome")
	}
	if !out.Redeemed {
		t.Fatal("the real redeem was not found past the failed one")
	}
}

// The legitimate refund still reads as one. The checks above refuse the right
// thing only if this still passes.
func TestOutcomeStillFindsOurOwnRefund(t *testing.T) {
	programID := pubkeyFrom(1)
	ours := pubkeyFrom(2)
	initiator := pubkeyFrom(5)
	hashlock := sha256.Sum256([]byte("anything"))

	node := &stubNode{
		signatures: []string{"refund"},
		txs: map[string]any{
			"refund": tx(
				[]string{programID.String(), ours.String(), initiator.String()},
				ix(0, []int{1, 2}, []byte{tagRefund}),
			),
		},
	}
	client := node.start(t)

	out, err := client.FindEscrowOutcome(context.Background(), programID, ours, hashlock, 50)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if !out.Refunded || out.RefundSig != "refund" {
		t.Fatalf("our own refund was not recognised: %+v", out)
	}
}

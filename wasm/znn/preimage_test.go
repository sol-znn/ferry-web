package znn

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Finding the preimage when the account that unlocked is not the account being
// paid.
//
// That is the normal shape here, not an edge case: htlc.Unlock may be called by
// anyone and pays hashLocked regardless, which is what lets this app settle a
// Zenon leg from a wallet that holds nothing. A search that assumes the payee
// unlocked finds nothing, and the swap it fails is one that actually completed.

const (
	payee     = "z1qqvwzz2xq7q5gwk6uhcddgrpxlfcyzc8rsu82s"
	stranger  = "z1qq9n7fpaqd8lpcljandzmx4xtku9w4ftwyg0mq"
	unlockTxn = "fb40ddd0fa18c73c9cd92e3f64a894bb25490138af6c7d35c8d31b5e4ceedfeb"
)

// unlockData is an htlc.Unlock as the contract encodes it: a method id, the
// HTLC id, then the preimage among the padding.
func unlockData(preimage []byte) string {
	data := []byte{0xd3, 0x37, 0x91, 0xd3}
	data = append(data, make([]byte, 32)...) // the id
	data = append(data, make([]byte, 32)...) // the bytes offset
	data = append(data, preimage...)
	return base64.StdEncoding.EncodeToString(data)
}

// htlcChain is a node serving one settled unlock: the contract's receive of it,
// with the payout to the payee hanging off it as a descendant, and the caller's
// own transaction reachable by hash.
type htlcChain struct {
	// caller is the address that signed the unlock.
	caller string
	// paid is the address the contract paid.
	paid string
	// preimage is what the caller's transaction carries.
	preimage []byte
	// payeeSends is what the payee's own chain holds. Empty is the interesting
	// case: they were paid and never called anything.
	payeeSends []*AccountBlock
}

func (h htlcChain) serve(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		reply := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		}
		switch req.Method {
		case "ledger.getAccountBlocksByPage":
			addr, _ := req.Params[0].(string)
			if strings.EqualFold(addr, HtlcContractAddress) {
				reply(map[string]any{"count": 1, "more": false, "list": []*AccountBlock{{
					Hash:          "contract-receive",
					Address:       HtlcContractAddress,
					BlockType:     5,
					FromBlockHash: unlockTxn,
					DescendantBlocks: []*AccountBlock{
						{Hash: "payout", Address: HtlcContractAddress, ToAddress: h.paid, BlockType: 4},
					},
				}}})
				return
			}
			reply(map[string]any{"count": len(h.payeeSends), "more": false, "list": h.payeeSends})
		case "ledger.getAccountBlockByHash":
			hash, _ := req.Params[0].(string)
			if hash == unlockTxn {
				reply(&AccountBlock{
					Hash: unlockTxn, Address: h.caller,
					ToAddress: HtlcContractAddress, BlockType: 2,
					Data: unlockData(h.preimage),
				})
				return
			}
			reply(nil)
		default:
			reply(nil)
		}
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

func secretAndHash() ([]byte, []byte) {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 1)
	}
	sum := sha256.Sum256(secret)
	return secret, sum[:]
}

// The case the address search cannot see: a stranger unlocked, the payee's own
// chain is empty, and the preimage is nonetheless public.
func TestFindPreimageWhenAStrangerUnlocked(t *testing.T) {
	secret, hash := secretAndHash()
	c := htlcChain{caller: stranger, paid: payee, preimage: secret}.serve(t)

	got, err := c.FindPreimage(t.Context(), payee, hash, 2, 10)
	if err != nil {
		t.Fatalf("the preimage of a settled unlock was not found: %v", err)
	}
	if string(got) != string(secret) {
		t.Errorf("found %x, want %x", got, secret)
	}
}

// The fast path still works, and is still preferred: when the payee did call
// the unlock themselves, their own chain answers without walking the contract.
func TestFindPreimageWhenThePayeeUnlockedThemselves(t *testing.T) {
	secret, hash := secretAndHash()
	own := &AccountBlock{
		Hash: "own", Address: payee, ToAddress: HtlcContractAddress,
		BlockType: 2, Data: unlockData(secret),
	}
	c := htlcChain{caller: payee, paid: payee, preimage: secret, payeeSends: []*AccountBlock{own}}.serve(t)

	got, err := c.FindPreimage(t.Context(), payee, hash, 2, 10)
	if err != nil {
		t.Fatalf("the payee's own unlock was not found: %v", err)
	}
	if string(got) != string(secret) {
		t.Errorf("found %x, want %x", got, secret)
	}
}

// An unlock that settles somebody else's HTLC is not this swap's news. The
// hash is what decides, not the shape.
func TestFindPreimageIgnoresAnUnlockOfAnotherSwap(t *testing.T) {
	_, hash := secretAndHash()
	other := make([]byte, 32) // a different secret entirely
	c := htlcChain{caller: stranger, paid: payee, preimage: other}.serve(t)

	if _, err := c.FindPreimage(t.Context(), payee, hash, 2, 10); err == nil {
		t.Fatal("a preimage belonging to another swap was accepted as this one's")
	}
}

// A payout to somebody else is not a candidate at all, so the caller's block is
// never even fetched for it.
func TestFindPreimageIgnoresAnUnlockPayingSomebodyElse(t *testing.T) {
	secret, hash := secretAndHash()
	c := htlcChain{caller: stranger, paid: stranger, preimage: secret}.serve(t)

	if _, err := c.FindPreimage(t.Context(), payee, hash, 2, 10); err == nil {
		t.Fatal("an unlock paying a different address was reported as this swap's")
	}
}

// The uncooperative counterparty, which is the case this has to survive.
//
// A swap can reach the point of needing this preimage without either browser
// ever having recorded the other side's Zenon address: it is not required to
// create a swap, and by the time it is wanted the party who could supply it is
// the party who has stopped answering. Refusing to search without it made a
// counterparty going quiet into a lost swap -- and it was never needed, only
// convenient. The preimage identifies itself by hashing, so the contract's own
// chain can be walked with no address at all; what is lost is a filter that
// skips candidates, not the ability to find the answer.
func TestFindPreimageWithNoAddressToSearch(t *testing.T) {
	secret, hash := secretAndHash()
	c := htlcChain{caller: stranger, paid: payee, preimage: secret}.serve(t)

	got, err := c.FindPreimage(t.Context(), "", hash, 2, 10)
	if err != nil {
		t.Fatalf("a public preimage was not found without a payee address: %v", err)
	}
	if string(got) != string(secret) {
		t.Errorf("found %x, want %x", got, secret)
	}
}

// Not finding one is still not finding one. The unfiltered walk must not start
// accepting whatever it comes across -- the hash is what identifies the answer,
// and a chain that does not contain this swap's unlock has to say so.
func TestFindPreimageWithNoAddressStillRefusesAStranger(t *testing.T) {
	secret, _ := secretAndHash()
	// A chain carrying somebody else's unlock: a real preimage, a real payout,
	// and nothing to do with this swap.
	c := htlcChain{caller: stranger, paid: payee, preimage: secret}.serve(t)

	other := make([]byte, 32)
	for i := range other {
		other[i] = byte(200 - i)
	}
	sum := sha256.Sum256(other)

	if got, err := c.FindPreimage(t.Context(), "", sum[:], 2, 10); err == nil {
		t.Fatalf("another swap's unlock was accepted as this one's preimage: %x", got)
	}
}

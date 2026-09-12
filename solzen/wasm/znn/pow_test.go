package znn

import (
	"context"
	"testing"
	"time"

	"github.com/zenon-network/go-zenon/common/types"
)

// A nonce this miner finds must be one go-zenon's verifier accepts. The two
// implementations are independent -- one searches, one checks -- so agreeing is
// evidence the arithmetic was copied correctly.
func TestMinedNonceVerifies(t *testing.T) {
	dataHash := types.NewHash([]byte("solzen"))
	for _, difficulty := range []uint64{1, 2, 1000, 250_000} {
		nonce, err := MinePoWNonce(context.Background(), difficulty, dataHash, make([]byte, 8), nil)
		if err != nil {
			t.Fatalf("difficulty %d: %v", difficulty, err)
		}
		if !CheckPoWNonce(difficulty, dataHash, nonce) {
			t.Errorf("difficulty %d: mined nonce %x does not verify", difficulty, nonce)
		}
	}
}

// The target is the consensus rule; these are its two ends. Difficulty 1 admits
// every hash, so mining terminates immediately, and a difficulty that admits
// almost nothing must still be rejected by the checker for an arbitrary nonce.
func TestTargetBounds(t *testing.T) {
	dataHash := types.NewHash([]byte("solzen"))
	if !CheckPoWNonce(1, dataHash, [8]byte{}) {
		t.Error("difficulty 1 should accept any nonce")
	}
	if !CheckPoWNonce(0, dataHash, [8]byte{}) {
		t.Error("difficulty 0 means no work was required")
	}
	rejected := 0
	for i := byte(0); i < 32; i++ {
		if !CheckPoWNonce(1<<40, dataHash, [8]byte{i}) {
			rejected++
		}
	}
	if rejected == 0 {
		t.Error("a very high difficulty accepted every nonce tried")
	}
}

func TestMiningIsCancellable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	// High enough that it cannot finish inside the timeout on any machine.
	_, err := MinePoWNonce(ctx, 1<<58, types.NewHash([]byte("solzen")), make([]byte, 8), nil)
	if err == nil {
		t.Fatal("expected the search to be cancelled")
	}
}

// Reports the rate the browser will see, so the difficulty the node hands back
// can be turned into a number of seconds a user is told up front.
func BenchmarkHashRate(b *testing.B) {
	dataHash := types.NewHash([]byte("solzen"))
	var nonce [8]byte
	for i := 0; i < b.N; i++ {
		nonce[0] = byte(i)
		nonce[1] = byte(i >> 8)
		nonce[2] = byte(i >> 16)
		CheckPoWNonce(1<<60, dataHash, nonce)
	}
}

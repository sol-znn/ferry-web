package znn

import (
	"context"
	"encoding/binary"
	"math/big"

	"github.com/zenon-network/go-zenon/common/crypto"
	"github.com/zenon-network/go-zenon/common/types"
)

// This is go-zenon's pow package, reimplemented rather than imported.
//
// The upstream one is thirty lines of arithmetic wrapped around two imports
// this build cannot have: chain/nom, which reaches goleveldb through common/db,
// and wallet, for eight bytes of entropy. Both are avoidable; the arithmetic is
// not, and it has to match go-zenon exactly or every block this app signs is
// rejected. pow_test.go pins it against CheckPoWNonce's own rules.
//
// The work itself is what buys plasma for an account that has none fused. An
// account the user has fused QSR to skips all of this -- see RequiredPoW, which
// asks the node which case applies rather than guessing.

// PoWDataHash is the value the nonce is mined against: sha3-256 of the address
// followed by the previous block hash. Note what is absent -- the block's
// contents. The work commits to a position on one account chain, not to a
// payload, so it cannot be precomputed for an account you do not control and it
// has to be redone for every block.
func PoWDataHash(address types.Address, previousHash types.Hash) types.Hash {
	return types.NewHash(append(address.Bytes(), previousHash.Bytes()...))
}

// Progress reports mining progress. Called often enough for a UI to look alive
// and rarely enough not to dominate the cost of mining.
type Progress func(hashes uint64)

// progressInterval is how many hashes pass between turns. At the ~1M hashes a
// second this manages, that is a breath roughly every 60 ms: often enough for a
// progress bar to look continuous and for a cancel to be felt, rare enough that
// handing the thread over costs a fraction of a percent. See yield_js.go for
// what "handing over" has to mean in a browser.
const progressInterval = 1 << 16

// MinePoWNonce searches for a nonce satisfying difficulty, mirroring
// go-zenon's pow.GetPoWNonce.
//
// The search space is walked from a random start rather than from zero. Two
// miners starting at zero on the same account and difficulty would do the same
// work and find the same nonce, which wastes the second one; more to the point,
// a predictable start is a free hint to anyone watching how far along a miner
// is.
//
// ctx cancels the search. Mining a Create call takes long enough that a user
// changing their mind has to be able to stop it, and in the browser this loop
// owns its goroutine until it returns.
func MinePoWNonce(ctx context.Context, difficulty uint64, dataHash types.Hash, seed []byte, progress Progress) ([8]byte, error) {
	var out [8]byte
	if difficulty == 0 {
		return out, nil
	}
	target := targetForDifficulty(difficulty)

	// calc is nonce(8) || dataHash(32), hashed whole on every attempt. Building
	// it once and incrementing in place is what makes the loop cheap.
	calc := make([]byte, 8+types.HashSize)
	copy(calc, seed)
	copy(calc[8:], dataHash[:])

	var hashes uint64
	for {
		if greaterDifficulty(crypto.Hash(calc), target[:]) {
			copy(out[:], calc[:8])
			return out, nil
		}
		quickInc(calc)
		hashes++
		if hashes%progressInterval == 0 {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			if progress != nil {
				progress(hashes)
			}
			// Hand the thread back. Without this the browser cannot repaint,
			// cannot deliver a cancel, and cannot show the callback above to
			// any visible effect. Off the browser it costs nothing.
			yieldToHost()
		}
	}
}

// CheckPoWNonce is go-zenon's verifier, so a nonce can be checked without
// asking a node. Used by the tests, and worth having: mining a nonce the node
// then rejects is the kind of failure that is very hard to read from the
// outside.
func CheckPoWNonce(difficulty uint64, dataHash types.Hash, nonce [8]byte) bool {
	if difficulty == 0 {
		return true
	}
	target := targetForDifficulty(difficulty)
	calc := make([]byte, 8+types.HashSize)
	copy(calc, nonce[:])
	copy(calc[8:], dataHash[:])
	return greaterDifficulty(crypto.Hash(calc), target[:])
}

// ExpectedHashes is how many attempts a difficulty costs on average. A nonce
// succeeds with probability 1/difficulty per hash, so the mean is difficulty
// itself -- which makes the number the UI needs to show a meaningful estimate
// the difficulty the node already handed us.
func ExpectedHashes(difficulty uint64) uint64 { return difficulty }

// targetForDifficulty is 2^64 - 2^64/difficulty, little-endian, matching
// go-zenon's getTargetByDifficulty.
func targetForDifficulty(difficulty uint64) [8]byte {
	var target [8]byte
	if difficulty == 0 {
		return target
	}
	x := new(big.Int).Lsh(big.NewInt(1), 64)
	y := new(big.Int).Quo(x, new(big.Int).SetUint64(difficulty))
	x.Sub(x, y)
	binary.LittleEndian.PutUint64(target[:], x.Uint64())
	return target
}

// greaterDifficulty compares the first eight bytes of a hash against the target,
// little-endian. Copied from go-zenon including its equal-means-true ending: a
// hash that ties the target counts as a hit there, and a stricter comparison
// here would reject nonces the node accepts.
func greaterDifficulty(x, y []byte) bool {
	for i := 7; i >= 0; i-- {
		if x[i] > y[i] {
			return true
		}
		if x[i] < y[i] {
			return false
		}
	}
	return true
}

// quickInc adds one to the little-endian counter at the head of the buffer.
func quickInc(x []byte) {
	for i := 0; i < len(x); i++ {
		x[i]++
		if x[i] != 0 {
			return
		}
	}
}

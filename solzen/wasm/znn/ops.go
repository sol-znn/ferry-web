package znn

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/zenon-network/go-zenon/common/types"
)

// Op is a block this app is about to publish, after the node has been asked
// what it will cost.
//
// It exists because the cost is worth showing before it is paid. An account
// with fused plasma publishes instantly; one without has to mine, and at an
// embedded-contract call's difficulty that is minutes in a browser tab. A user
// who is told which case they are in can fuse plasma from Syrius and come back,
// instead of watching a spinner and assuming the page has hung.
type Op struct {
	Block      *Block
	Difficulty uint64
	// Expected work, as a hash count. Zero when the account has plasma.
	ExpectedHashes uint64
}

// NeedsWork reports whether publishing this block requires mining.
func (o *Op) NeedsWork() bool { return o.Difficulty > 0 }

// Prepare builds an unsigned block, fills in its chain position, and asks the
// node what plasma or work it needs.
//
// Everything time-related is anchored to the node's frontier momentum rather
// than to the browser's clock: the htlc contract compares expiry against
// momentum time, and a machine whose clock is minutes off would otherwise
// compute a deadline the chain disagrees with.
func (c *Client) Prepare(ctx context.Context, key *Key, blockType uint64, to types.Address, amount *big.Int, zts string, data []byte, fromBlockHash types.Hash) (*Op, error) {
	frontierMomentum, err := c.FrontierMomentum(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the chain tip: %w", err)
	}

	height := uint64(1)
	previous := types.Hash{}
	frontierBlock, err := c.FrontierAccountBlock(ctx, key.Address)
	switch {
	case err == nil:
		height = frontierBlock.Height + 1
		previous = frontierBlock.Hash
	case err == ErrNotFound:
		// A brand-new account. Height 1, previous hash zero.
	default:
		return nil, fmt.Errorf("reading the account's frontier block: %w", err)
	}

	if amount == nil {
		amount = new(big.Int)
	}
	tokenStandard, err := types.ParseZTS(zts)
	if err != nil && zts != "" {
		return nil, fmt.Errorf("token standard %q: %w", zts, err)
	}

	b := &Block{
		Version:         blockVersion,
		ChainIdentifier: frontierMomentum.ChainIdentifier,
		BlockType:       blockType,
		PreviousHash:    previous,
		Height:          height,
		MomentumAcknowledged: types.HashHeight{
			Hash:   frontierMomentum.Hash,
			Height: frontierMomentum.Height,
		},
		Address:       key.Address,
		ToAddress:     to,
		Amount:        amount.String(),
		TokenStandard: tokenStandard,
		FromBlockHash: fromBlockHash,
		Data:          data,
		Nonce:         "0000000000000000",
	}

	var toPtr *types.Address
	if blockType == BlockTypeUserSend {
		toPtr = &to
	}
	req, err := c.RequiredPoWFor(ctx, key.Address, blockType, toPtr, data)
	if err != nil {
		return nil, fmt.Errorf("asking the node what this block costs: %w", err)
	}

	op := &Op{Block: b, Difficulty: req.RequiredDifficulty}
	if req.RequiredDifficulty != 0 {
		// Mirrors the reference SDKs: when work is needed, the block claims
		// only the plasma the account actually has and the work covers the
		// rest. Claiming basePlasma here instead would be rejected.
		b.FusedPlasma = req.AvailablePlasma
		b.Difficulty = req.RequiredDifficulty
		op.ExpectedHashes = ExpectedHashes(req.RequiredDifficulty)
	} else {
		b.FusedPlasma = req.BasePlasma
	}
	return op, nil
}

// Publish mines the block's proof of work if it needs one, signs it, and sends
// it to the node. It returns the block hash, which for a send to an embedded
// contract is also the id the contract will file the result under.
func (c *Client) PublishOp(ctx context.Context, key *Key, op *Op, progress Progress) (types.Hash, error) {
	b := op.Block
	if op.NeedsWork() {
		seed := make([]byte, 8)
		if _, err := rand.Read(seed); err != nil {
			return types.Hash{}, fmt.Errorf("generating a nonce seed: %w", err)
		}
		nonce, err := MinePoWNonce(ctx, b.Difficulty, PoWDataHash(b.Address, b.PreviousHash), seed, progress)
		if err != nil {
			return types.Hash{}, err
		}
		b.setNonce(nonce)
	}
	b.Sign(key)
	if err := c.Publish(ctx, b); err != nil {
		return types.Hash{}, err
	}
	return b.Hash, nil
}

// CreateHtlcParams is one HTLC to create.
type CreateHtlcParams struct {
	// HashLocked receives the funds on a successful unlock. This is the
	// counterparty's own address, and nothing this app holds a key for.
	HashLocked types.Address
	// ExpirationTime is absolute, in seconds, on the chain's clock. Past this
	// point the entry can only be reclaimed by its creator.
	ExpirationTime int64
	HashLock       []byte
	Amount         *big.Int
	TokenStandard  string
}

// PrepareCreateHtlc builds the Create call.
//
// hashType is fixed at SHA256 and keyMaxSize at 32: those are the two values a
// swap against the Solana program requires, and making them parameters would
// only create the opportunity to get them wrong. An HTLC with hashType 0 is
// unlockable by nobody in this protocol, and it is not recoverable until it
// expires.
func (c *Client) PrepareCreateHtlc(ctx context.Context, key *Key, p CreateHtlcParams) (*Op, error) {
	if len(p.HashLock) != 32 {
		return nil, fmt.Errorf("the hashlock must be 32 bytes, got %d", len(p.HashLock))
	}
	if p.Amount == nil || p.Amount.Sign() <= 0 {
		return nil, fmt.Errorf("the amount must be positive")
	}
	data, err := PackCreate(p.HashLocked, p.ExpirationTime, HashTypeSHA256, PreimageSize, p.HashLock)
	if err != nil {
		return nil, fmt.Errorf("encoding the Create call: %w", err)
	}
	return c.Prepare(ctx, key, BlockTypeUserSend, HtlcContract, p.Amount, p.TokenStandard, data, types.Hash{})
}

// PrepareUnlockHtlc builds the Unlock call: reveal the preimage, and the
// contract pays the entry's hashLocked address.
//
// The block carries no funds -- it is a zero-amount send whose payload is the
// secret -- which is why this is the operation the swap key can perform on
// behalf of a counterparty who never touches this page.
func (c *Client) PrepareUnlockHtlc(ctx context.Context, key *Key, id types.Hash, preimage []byte) (*Op, error) {
	data, err := PackUnlock(id, preimage)
	if err != nil {
		return nil, fmt.Errorf("encoding the Unlock call: %w", err)
	}
	return c.Prepare(ctx, key, BlockTypeUserSend, HtlcContract, nil, ZnnTokenStandard, data, types.Hash{})
}

// PrepareReclaimHtlc builds the Reclaim call, which only the entry's creator
// may make and only after it has expired.
func (c *Client) PrepareReclaimHtlc(ctx context.Context, key *Key, id types.Hash) (*Op, error) {
	data, err := PackReclaim(id)
	if err != nil {
		return nil, fmt.Errorf("encoding the Reclaim call: %w", err)
	}
	return c.Prepare(ctx, key, BlockTypeUserSend, HtlcContract, nil, ZnnTokenStandard, data, types.Hash{})
}

// PrepareReceive builds the receive block for one incoming transfer.
func (c *Client) PrepareReceive(ctx context.Context, key *Key, fromBlockHash types.Hash) (*Op, error) {
	return c.Prepare(ctx, key, BlockTypeUserReceive, types.Address{}, nil, "", nil, fromBlockHash)
}

// PrepareSend builds a plain transfer, which is how the swap address is swept
// back to the user's own wallet after a reclaim.
func (c *Client) PrepareSend(ctx context.Context, key *Key, to types.Address, amount *big.Int, zts string) (*Op, error) {
	return c.Prepare(ctx, key, BlockTypeUserSend, to, amount, zts, nil, types.Hash{})
}

// WaitForContractResult waits until the htlc contract has actually processed a
// call.
//
// Publishing a send to an embedded contract does not do anything by itself: the
// contract produces its paired receive block a momentum or two later, and only
// then does the entry exist. Returning before that leaves the caller looking up
// an id that is not there yet, which reads as "the swap failed" when it has
// simply not landed.
func (c *Client) WaitForContractResult(ctx context.Context, blockHash types.Hash, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		b, err := c.AccountBlockByHash(ctx, blockHash)
		if err == nil && b.PairedAccountBlock != nil && b.PairedAccountBlock.ConfirmationDetail != nil {
			return true, nil
		}
		if err != nil && err != ErrNotFound {
			return false, err
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// ParseAmount converts a decimal figure to base units without going through a
// float, which would silently lose the low digits of an 8-decimal token.
func ParseAmount(decimal string, decimals int) (*big.Int, error) {
	s := strings.TrimSpace(decimal)
	if s == "" || strings.HasPrefix(s, "-") {
		return nil, fmt.Errorf("the amount must be a positive number")
	}
	parts := strings.Split(s, ".")
	if len(parts) > 2 {
		return nil, fmt.Errorf("%q is not a number", decimal)
	}
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
	}
	if len(frac) > decimals {
		return nil, fmt.Errorf("%q has %d decimal places but the token has %d", decimal, len(frac), decimals)
	}
	frac += strings.Repeat("0", decimals-len(frac))
	v, ok := new(big.Int).SetString(parts[0]+frac, 10)
	if !ok {
		return nil, fmt.Errorf("%q is not a number", decimal)
	}
	return v, nil
}

// FormatAmount is ParseAmount's inverse, for display.
func FormatAmount(v *big.Int, decimals int) string {
	if v == nil {
		return "0"
	}
	s := v.String()
	if len(s) <= decimals {
		s = strings.Repeat("0", decimals-len(s)+1) + s
	}
	whole := s[:len(s)-decimals]
	if decimals == 0 {
		return whole
	}
	frac := strings.TrimRight(s[len(s)-decimals:], "0")
	if frac == "" {
		return whole
	}
	return whole + "." + frac
}

// HexBytes renders bytes for display and for pasting between the two sides.
func HexBytes(b []byte) string { return hex.EncodeToString(b) }

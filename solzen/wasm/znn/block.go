package znn

import (
	"crypto/ed25519"
	"encoding/hex"
	"math/big"

	"github.com/zenon-network/go-zenon/common"
	"github.com/zenon-network/go-zenon/common/types"
)

// Block types, from go-zenon's chain/nom. Only the two a user account can
// produce are here; contract blocks are made by the VM, never by this app.
const (
	BlockTypeUserSend    uint64 = 2
	BlockTypeUserReceive uint64 = 3
)

// blockVersion is the only version go-zenon's verifier accepts.
const blockVersion uint64 = 1

// Block is an account block, in the shape the node's ledger.publishRawTransaction
// expects and with the hash computation go-zenon uses.
//
// It duplicates chain/nom.AccountBlock, minus the fields a client never sets --
// descendant blocks, the changes hash, the cached producer -- because that type
// cannot be compiled for js/wasm: it carries a db.Patch, and common/db reaches
// goleveldb. Everything that feeds the hash is here in the same order, and
// block_test.go checks the result against blocks the running chain already
// accepted, which is the only check that actually matters.
type Block struct {
	Version              uint64                   `json:"version"`
	ChainIdentifier      uint64                   `json:"chainIdentifier"`
	BlockType            uint64                   `json:"blockType"`
	Hash                 types.Hash               `json:"hash"`
	PreviousHash         types.Hash               `json:"previousHash"`
	Height               uint64                   `json:"height"`
	MomentumAcknowledged types.HashHeight         `json:"momentumAcknowledged"`
	Address              types.Address            `json:"address"`
	ToAddress            types.Address            `json:"toAddress"`
	Amount               string                   `json:"amount"`
	TokenStandard        types.ZenonTokenStandard `json:"tokenStandard"`
	FromBlockHash        types.Hash               `json:"fromBlockHash"`
	DescendantBlocks     []*Block                 `json:"descendantBlocks"`
	Data                 []byte                   `json:"data"`
	FusedPlasma          uint64                   `json:"fusedPlasma"`
	Difficulty           uint64                   `json:"difficulty"`
	Nonce                string                   `json:"nonce"`
	BasePlasma           uint64                   `json:"basePlasma"`
	TotalPlasma          uint64                   `json:"usedPlasma"`
	ChangesHash          types.Hash               `json:"changesHash"`
	PublicKey            ed25519.PublicKey        `json:"publicKey"`
	Signature            []byte                   `json:"signature"`
}

// emptyDescendantsHash is sha3-256 of nothing, which is what
// DescendantBlocksHash returns for a block with no descendants. Every block
// this app builds is one of those -- batching is for contract calls that spawn
// other calls -- so the value is constant and computed once.
var emptyDescendantsHash = types.NewHash(nil)

// ComputeHash is chain/nom.AccountBlock.ComputeHash, field for field.
//
// The order is the consensus rule. A field in the wrong place, or an amount
// serialized to a different width, produces a hash the node disagrees with, and
// the block is rejected as a bad signature -- an error that points at the key
// rather than at the serialization, which is why this mirrors the original line
// by line instead of being rewritten more tidily.
func (b *Block) ComputeHash() types.Hash {
	amount, ok := new(big.Int).SetString(b.amountOrZero(), 10)
	if !ok {
		amount = big.NewInt(0)
	}
	nonce := b.nonceBytes()
	return types.NewHash(common.JoinBytes(
		common.Uint64ToBytes(b.Version),
		common.Uint64ToBytes(b.ChainIdentifier),
		common.Uint64ToBytes(b.BlockType),
		b.PreviousHash.Bytes(),
		common.Uint64ToBytes(b.Height),
		b.MomentumAcknowledged.Bytes(),
		b.Address.Bytes(),
		b.ToAddress.Bytes(),
		common.BigIntToBytes(amount),
		b.TokenStandard.Bytes(),
		b.FromBlockHash.Bytes(),
		emptyDescendantsHash.Bytes(),
		types.NewHash(b.Data).Bytes(),
		common.Uint64ToBytes(b.FusedPlasma),
		common.Uint64ToBytes(b.Difficulty),
		nonce[:],
	))
}

// Sign fills in the hash, the public key and the signature. The signature is
// over the hash, not over the serialization -- go-zenon's verifier checks
// ed25519.Verify(publicKey, block.Hash, signature) after recomputing the hash
// itself, so both have to be right independently.
func (b *Block) Sign(key *Key) {
	b.Version = blockVersion
	b.Hash = b.ComputeHash()
	b.PublicKey = key.Public
	b.Signature = key.Sign(b.Hash.Bytes())
}

func (b *Block) amountOrZero() string {
	if b.Amount == "" {
		return "0"
	}
	return b.Amount
}

func (b *Block) nonceBytes() [8]byte {
	var n [8]byte
	raw, err := hex.DecodeString(b.Nonce)
	if err != nil || len(raw) > 8 {
		return n
	}
	// Left-pad, matching how go-zenon deserializes a nonce: the value is
	// eight bytes and a shorter hex string is the leading zeros elided.
	copy(n[8-len(raw):], raw)
	return n
}

func (b *Block) setNonce(n [8]byte) { b.Nonce = hex.EncodeToString(n[:]) }

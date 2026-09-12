package znn

import (
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/zenon-network/go-zenon/common/types"
)

// block.go re-implements chain/nom.AccountBlock's hash because that type cannot
// be compiled for js/wasm. A re-implementation of a consensus rule is only worth
// as much as its evidence, so the evidence is a block the chain accepted.
//
// This one is an `htlc.Unlock` published by this package during an end-to-end
// swap on the go-zenon devnet, read back with ledger.getAccountBlocksByPage. It
// covers the whole surface at once: a first block on a fresh account, a
// zero-amount send to an embedded contract, a payload, and a nonce mined
// because the account had no plasma.
var liveBlock = struct {
	Hash, PreviousHash, Address, ToAddress, TokenStandard string
	MomentumHash                                          string
	MomentumHeight, Height                                uint64
	Version, ChainIdentifier, BlockType                   uint64
	Amount, Nonce, DataBase64                             string
	FusedPlasma, Difficulty                               uint64
	PublicKeyBase64, SignatureBase64                      string
}{
	Hash:            "7c559ffb7fe99cf769b5b07ddc789164a6916f7552d10c9f6e887d06b611e07a",
	PreviousHash:    "0000000000000000000000000000000000000000000000000000000000000000",
	Address:         "z1qp6wde9234yxx6nhhwaaeeyk83595rm8d4t245",
	ToAddress:       "z1qxemdeddedxhtlcxxxxxxxxxxxxxxxxxygecvw",
	TokenStandard:   "zts1znnxxxxxxxxxxxxx9z4ulx",
	MomentumHash:    "4ee89b4e2d6c8f2d6e101bcb9dcc9c6ba3c09c90d4b056a63b4db4a4ab2b7d48",
	MomentumHeight:  26385,
	Height:          1,
	Version:         1,
	ChainIdentifier: 69,
	BlockType:       BlockTypeUserSend,
	Amount:          "0",
	Nonce:           "219c03692d869ed8",
	DataBase64: "0zeR09AVbMtOB4wEZt5hYEZdswX+Dw/+26ivYUdG+1KiD/qwAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAEAAAA" +
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAIDKrvaPIYtgoHX5bwjEkFNN9MBy/O7mN/cXTa7jpaBSq",
	FusedPlasma:     0,
	Difficulty:      110250000,
	PublicKeyBase64: "KUV5bXU8r29o14Crx2mfDHrQsQuUXCeMCPIBy0hBfCM=",
	SignatureBase64: "+dajC2UFzL4JtTqALiXG/H9rTUejy0tDcIr85qh4g690GmRPAxhZj4OmAAzevk7wcyb1QWHAi1NI6FPRx1MVCA==",
}

func liveBlockFixture(t *testing.T) *Block {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(liveBlock.DataBase64)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := base64.StdEncoding.DecodeString(liveBlock.PublicKeyBase64)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := types.ParseAddress(liveBlock.Address)
	if err != nil {
		t.Fatal(err)
	}
	to, err := types.ParseAddress(liveBlock.ToAddress)
	if err != nil {
		t.Fatal(err)
	}
	zts, err := types.ParseZTS(liveBlock.TokenStandard)
	if err != nil {
		t.Fatal(err)
	}
	prev, err := ParseHashHex(liveBlock.PreviousHash)
	if err != nil {
		t.Fatal(err)
	}
	mom, err := ParseHashHex(liveBlock.MomentumHash)
	if err != nil {
		t.Fatal(err)
	}
	return &Block{
		Version:              liveBlock.Version,
		ChainIdentifier:      liveBlock.ChainIdentifier,
		BlockType:            liveBlock.BlockType,
		PreviousHash:         prev,
		Height:               liveBlock.Height,
		MomentumAcknowledged: types.HashHeight{Hash: mom, Height: liveBlock.MomentumHeight},
		Address:              addr,
		ToAddress:            to,
		Amount:               liveBlock.Amount,
		TokenStandard:        zts,
		Data:                 data,
		FusedPlasma:          liveBlock.FusedPlasma,
		Difficulty:           liveBlock.Difficulty,
		Nonce:                liveBlock.Nonce,
		PublicKey:            pub,
	}
}

func TestComputeHashMatchesABlockTheChainAccepted(t *testing.T) {
	b := liveBlockFixture(t)
	if got := b.ComputeHash().String(); got != liveBlock.Hash {
		t.Fatalf("ComputeHash = %s\n           want %s", got, liveBlock.Hash)
	}
}

// Every field is in the hash, so changing any of them must change it. Without
// this, a field silently dropped from the serialization would still pass the
// fixture above -- right up until it let someone alter an amount.
func TestEveryHashedFieldChangesTheHash(t *testing.T) {
	base := liveBlockFixture(t).ComputeHash()
	other, err := types.ParseAddress("z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d")
	if err != nil {
		t.Fatal(err)
	}
	otherHash := types.NewHash([]byte("other"))

	for name, mutate := range map[string]func(*Block){
		"version":          func(b *Block) { b.Version = 2 },
		"chain identifier": func(b *Block) { b.ChainIdentifier = 1 },
		"block type":       func(b *Block) { b.BlockType = BlockTypeUserReceive },
		"previous hash":    func(b *Block) { b.PreviousHash = otherHash },
		"height":           func(b *Block) { b.Height = 2 },
		"momentum hash":    func(b *Block) { b.MomentumAcknowledged.Hash = otherHash },
		"momentum height":  func(b *Block) { b.MomentumAcknowledged.Height++ },
		"address":          func(b *Block) { b.Address = other },
		"to address":       func(b *Block) { b.ToAddress = other },
		"amount":           func(b *Block) { b.Amount = "1" },
		"token standard":   func(b *Block) { b.TokenStandard, _ = types.ParseZTS(QsrTokenStandard) },
		"from block hash":  func(b *Block) { b.FromBlockHash = otherHash },
		"data":             func(b *Block) { b.Data = append(append([]byte(nil), b.Data...), 0) },
		"fused plasma":     func(b *Block) { b.FusedPlasma = 21000 },
		"difficulty":       func(b *Block) { b.Difficulty++ },
		"nonce":            func(b *Block) { b.Nonce = "0000000000000001" },
	} {
		t.Run(name, func(t *testing.T) {
			b := liveBlockFixture(t)
			mutate(b)
			if b.ComputeHash() == base {
				t.Errorf("changing the %s did not change the hash", name)
			}
		})
	}
}

// The signature the chain accepted has to verify against the hash this package
// computes -- which is the same statement as "this package can sign", read from
// the other end.
func TestTheChainsSignatureVerifiesAgainstOurHash(t *testing.T) {
	b := liveBlockFixture(t)
	sig, err := base64.StdEncoding.DecodeString(liveBlock.SignatureBase64)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyEd25519(b.PublicKey, b.ComputeHash().Bytes(), sig) {
		t.Fatal("the block's own signature does not verify against the hash computed here")
	}
}

// The nonce on that block was mined by this package. Checking it with the
// verifier's own arithmetic closes the loop: the work is the work go-zenon
// asked for, at the difficulty go-zenon set.
func TestTheMinedNonceSatisfiesTheDifficultyTheChainRecorded(t *testing.T) {
	b := liveBlockFixture(t)
	raw, err := hex.DecodeString(b.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	var nonce [8]byte
	copy(nonce[:], raw)
	if !CheckPoWNonce(b.Difficulty, PoWDataHash(b.Address, b.PreviousHash), nonce) {
		t.Error("the nonce the chain accepted does not satisfy the difficulty it recorded")
	}
	if CheckPoWNonce(b.Difficulty, PoWDataHash(b.Address, types.NewHash([]byte("elsewhere"))), nonce) {
		t.Error("the nonce verified against a different account position, so it is not bound to one")
	}
}

// A key round-trips through its seed, and the address is derived rather than
// remembered -- which is what makes a tampered recovery record fail rather than
// sign for an account nobody holds.
func TestKeyDerivationIsStable(t *testing.T) {
	k, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	again, err := KeyFromSeedHex(k.SeedHex())
	if err != nil {
		t.Fatal(err)
	}
	if again.Address != k.Address {
		t.Errorf("the same seed gave two addresses: %s and %s", k.Address, again.Address)
	}
	sig := k.Sign([]byte("message"))
	if !verifyEd25519(again.Public, []byte("message"), sig) {
		t.Error("a key rebuilt from its seed cannot verify the original's signature")
	}
	if _, err := KeyFromSeed(make([]byte, 31)); err == nil {
		t.Error("a short seed was accepted")
	}
}

func TestParseAmountAndFormatAmountAreInverses(t *testing.T) {
	cases := []struct{ decimal, base string }{
		{"10", "1000000000"},
		{"0.00000001", "1"},
		{"1.25", "125000000"},
		{"123456.789", "12345678900000"},
	}
	for _, c := range cases {
		v, err := ParseAmount(c.decimal, 8)
		if err != nil {
			t.Fatalf("%s: %v", c.decimal, err)
		}
		if v.String() != c.base {
			t.Errorf("ParseAmount(%s) = %s, want %s", c.decimal, v, c.base)
		}
		if got := FormatAmount(v, 8); got != c.decimal {
			t.Errorf("FormatAmount(%s) = %s, want %s", c.base, got, c.decimal)
		}
	}
	// More decimal places than the token has would silently truncate. It is
	// refused instead, because "0.000000001 ZNN" quietly becoming zero is a
	// swap for nothing.
	if _, err := ParseAmount("0.000000001", 8); err == nil {
		t.Error("an amount finer than the token's precision was accepted")
	}
	for _, bad := range []string{"", "-1", "1.2.3", "abc"} {
		if _, err := ParseAmount(bad, 8); err == nil {
			t.Errorf("%q was accepted as an amount", bad)
		}
	}
}

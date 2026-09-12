package znn

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/zenon-network/go-zenon/common/types"
)

// SeedLen is the length of the seed a Key is generated from. It is ed25519's
// own seed length, so a seed round-trips through KeyFromSeed exactly.
const SeedLen = ed25519.SeedSize

// Key is one Zenon account this program can sign for.
//
// Every Key in a swap is ephemeral: generated for that swap, used for at most a
// handful of blocks, and never reused. That is the whole reason this package
// can sign at all without the app becoming a wallet. ferry's Bitcoin leg makes
// the same trade -- a per-swap key that can only move coins already inside one
// contract -- and the argument carries over: the user funds the swap from
// Syrius with an ordinary send, and the throwaway key is what performs the two
// operations Syrius has no screen for.
//
// What the user must accept is narrow and worth stating plainly: between the
// funding send and the swap settling, this key can spend that one balance. It
// is not their wallet key, it never was, and losing it costs at most the amount
// they put into this one swap -- which is why the recovery file exists and why
// the UI pushes it at them.
type Key struct {
	seed    []byte
	Private ed25519.PrivateKey
	Public  ed25519.PublicKey
	Address types.Address
}

// NewKey generates a fresh key from the platform CSPRNG. In the browser that is
// crypto.getRandomValues by way of Go's runtime.
func NewKey() (*Key, error) {
	seed := make([]byte, SeedLen)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("generating a key: %w", err)
	}
	return KeyFromSeed(seed)
}

// KeyFromSeed rebuilds a key from its seed, which is what a recovery file
// stores. The address is derived here rather than trusted from the file, so a
// tampered record cannot make the app sign for an account it does not hold.
func KeyFromSeed(seed []byte) (*Key, error) {
	if len(seed) != SeedLen {
		return nil, fmt.Errorf("seed must be %d bytes, got %d", SeedLen, len(seed))
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	return &Key{
		seed:    append([]byte(nil), seed...),
		Private: priv,
		Public:  pub,
		Address: types.PubKeyToAddress(pub),
	}, nil
}

// KeyFromSeedHex is KeyFromSeed over a hex string, which is the shape a seed
// takes everywhere outside this package.
func KeyFromSeedHex(s string) (*Key, error) {
	raw, err := hex.DecodeString(trimHex(s))
	if err != nil {
		return nil, fmt.Errorf("seed is not hex: %w", err)
	}
	return KeyFromSeed(raw)
}

// SeedHex is the only way the seed leaves this type. Callers write it to the
// swap record and the recovery file; nothing else needs it.
func (k *Key) SeedHex() string { return hex.EncodeToString(k.seed) }

// Sign signs a message. The only message this program ever signs is an account
// block hash.
func (k *Key) Sign(msg []byte) []byte { return ed25519.Sign(k.Private, msg) }

func trimHex(s string) string {
	if len(s) >= 2 && (s[0:2] == "0x" || s[0:2] == "0X") {
		return s[2:]
	}
	return s
}

// verifyEd25519 exists so the tests can check a signature without importing
// crypto/ed25519 beside every one of them. Nothing in the app verifies its own
// signatures -- the node does that -- but a test that can is what turns "this
// signs" into "this signs something the chain accepts".
func verifyEd25519(pub, msg, sig []byte) bool {
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(pub, msg, sig)
}

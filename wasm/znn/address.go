package znn

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/btcsuite/btcd/btcutil/bech32"
	"golang.org/x/crypto/sha3"
)

// Turning an ed25519 public key into the z1 address it spends from.
//
// The reverse of ParseAddress, and it exists for one reason: a signature is a
// claim about a KEY, and every field in this app that names a counterparty names
// an ADDRESS. Without this, "the wallet holding z1qx… signed this" cannot be
// checked -- only "some ed25519 key signed this", which is worth nothing.
//
// Copied from go-zenon's common/types/address.go and wallet/keypair.go rather
// than imported, for the reason htlcabi.go gives at length: go-zenon pulls
// go-ethereum and the module size is a hard budget. Three lines of derivation
// are cheaper than that dependency, and address_test.go pins them to vectors.

// userAddressPrefix is the kind byte at the front of a user address. go-zenon
// calls it AddressTypeUser; contract addresses carry a different one, and no
// key derives to those.
const userAddressPrefix = byte(0)

// coreSize is how much of the key digest an address actually carries: 19 bytes,
// the 20 of AddressSize less the kind byte. Truncation is go-zenon's, not ours.
const coreSize = AddressSize - 1

// DeriveAddress returns the z1 address an ed25519 public key spends from.
//
// SHA3-256 of the raw 32-byte key, truncated to 19 bytes, behind the user kind
// byte, bech32'd under "z". Standard SHA3, not Keccak -- the same distinction
// htlcabi.go's methodID rests on, and the same one that makes a wrong answer
// here look like a valid address belonging to nobody.
func DeriveAddress(pub ed25519.PublicKey) (string, error) {
	if len(pub) != ed25519.PublicKeySize {
		return "", fmt.Errorf("an ed25519 public key is %d bytes, got %d",
			ed25519.PublicKeySize, len(pub))
	}
	digest := sha3.Sum256(pub)
	raw := make([]byte, AddressSize)
	raw[0] = userAddressPrefix
	copy(raw[1:], digest[:coreSize])
	return EncodeAddress(raw)
}

// EncodeAddress renders 20 raw bytes as the z1 string. Separate from
// DeriveAddress so a round trip through ParseAddress can be tested without a
// key in it.
func EncodeAddress(raw []byte) (string, error) {
	if len(raw) != AddressSize {
		return "", fmt.Errorf("a Zenon address is %d bytes, got %d", AddressSize, len(raw))
	}
	data, err := bech32.ConvertBits(raw, 8, 5, true)
	if err != nil {
		return "", fmt.Errorf("encode Zenon address: %w", err)
	}
	addr, err := bech32.Encode(addressPrefix, data)
	if err != nil {
		return "", fmt.Errorf("encode Zenon address: %w", err)
	}
	return addr, nil
}

// AddressOwnedBy reports whether `addr` is the address `pub` derives to.
//
// The whole check, in one call, because every caller wants the question in that
// form and none of them wants to be the place that forgot to compare. A parse
// failure is a mismatch rather than an error: an address that cannot be decoded
// is not one this key controls either.
func AddressOwnedBy(addr string, pub ed25519.PublicKey) error {
	if _, err := ParseAddress(addr); err != nil {
		return err
	}
	derived, err := DeriveAddress(pub)
	if err != nil {
		return err
	}
	if derived != addr {
		return errors.New("that signature is valid, but the key that made it holds " +
			derived + ", not " + addr)
	}
	return nil
}

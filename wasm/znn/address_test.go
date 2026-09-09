package znn

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
)

// A public key, and the address go-zenon's derivation produces from it.
//
// The key is all-zero bytes: not a key anybody holds, which is the point --
// nothing about this vector depends on a secret, so it can sit in a repository.
// sha3-256 of those 32 bytes is
// 9e6291970cb44dd94008c79bcaf9d86f18b4b49ba5b2a04781db7199ed3b9e4e, and the
// address below is its first 19 bytes behind the user kind byte, bech32'd under
// "z" -- computed a second time with an unrelated sha3 and bech32
// implementation, so this pins the ALGORITHM and not merely this file's
// agreement with itself.
//
// Which is what makes it worth having: a change to the hash, the truncation or
// the kind byte fails here rather than in production, where the symptom is a
// valid-looking address belonging to nobody.
const (
	zeroKeyHex  = "0000000000000000000000000000000000000000000000000000000000000000"
	zeroKeyAddr = "z1qz0x9yvhpj6ymk2qprrehjhemph33d958q2nfy"
)

func TestDeriveAddressIsPinned(t *testing.T) {
	pub, err := hex.DecodeString(zeroKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DeriveAddress(pub)
	if err != nil {
		t.Fatalf("DeriveAddress: %v", err)
	}
	if got != zeroKeyAddr {
		t.Fatalf("derivation changed:\n got %s\nwant %s", got, zeroKeyAddr)
	}
}

func TestDeriveAddressRoundTrips(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := DeriveAddress(pub)
	if err != nil {
		t.Fatalf("DeriveAddress: %v", err)
	}
	if !strings.HasPrefix(addr, "z1") {
		t.Fatalf("a Zenon address starts z1, got %q", addr)
	}
	raw, err := ParseAddress(addr)
	if err != nil {
		t.Fatalf("ParseAddress refused what DeriveAddress produced: %v", err)
	}
	if len(raw) != AddressSize {
		t.Fatalf("decoded to %d bytes, want %d", len(raw), AddressSize)
	}
	// The kind byte is what separates a user address from a contract one. A
	// derivation that dropped it would still round trip, and would still be
	// wrong.
	if raw[0] != userAddressPrefix {
		t.Fatalf("kind byte is %d, want %d", raw[0], userAddressPrefix)
	}
	if again, err := EncodeAddress(raw); err != nil || again != addr {
		t.Fatalf("re-encoding gave (%q, %v), want (%q, nil)", again, err, addr)
	}
}

func TestAddressOwnedByRefusesAnotherKey(t *testing.T) {
	minePub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	theirsPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	mine, err := DeriveAddress(minePub)
	if err != nil {
		t.Fatal(err)
	}
	if err := AddressOwnedBy(mine, minePub); err != nil {
		t.Fatalf("a key should own its own address: %v", err)
	}
	// The case that matters: a valid signature from a real key, claiming an
	// address that key does not hold. Without this comparison the signature
	// would prove only that somebody, somewhere, signed something.
	if err := AddressOwnedBy(mine, theirsPub); err == nil {
		t.Fatal("a different key was accepted as the owner of this address")
	}
}

func TestDeriveAddressRefusesWrongKeySize(t *testing.T) {
	if _, err := DeriveAddress(make([]byte, 31)); err == nil {
		t.Fatal("a 31-byte key was accepted as ed25519")
	}
	if _, err := EncodeAddress(make([]byte, 19)); err == nil {
		t.Fatal("19 bytes was accepted as an address")
	}
}

package znn

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// go-zenon marshals hashLock as []byte, so it arrives base64. Some tooling
// hands back hex instead, and the two cannot be told apart by trying one and
// falling back on the error -- a 64-character hex string is ALSO valid base64,
// decoding without complaint into 48 meaningless bytes.
//
// Nothing is stolen by getting this wrong: 48 bytes never match a 32-byte
// hashlock, so the leg is refused. It is a legitimate HTLC refused, which for
// the side waiting to be paid is its own kind of expensive.
func TestHashLockAcceptsBothEncodings(t *testing.T) {
	digest := make([]byte, HashLockSize)
	for i := range digest {
		digest[i] = byte(i)
	}
	asHex := hex.EncodeToString(digest)
	asB64 := base64.StdEncoding.EncodeToString(digest)

	// The trap this exists for, asserted so it cannot quietly stop being true.
	if b, err := base64.StdEncoding.DecodeString(asHex); err != nil || len(b) != 48 {
		t.Fatalf("the premise no longer holds: hex decoded as base64 gave %d bytes (%v)", len(b), err)
	}

	for _, tc := range []struct{ name, in string }{
		{"base64, as a node sends it", asB64},
		{"hex", asHex},
		{"hex with an 0x prefix", "0x" + asHex},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeHashLock(tc.in)
			if err != nil {
				t.Fatalf("refused a valid hashlock: %v", err)
			}
			if hex.EncodeToString(got) != asHex {
				t.Fatalf("decoded to %x, want %s", got, asHex)
			}
		})
	}

	// Undecodable is still undecodable.
	if _, err := DecodeHashLock("!!!not either!!!"); err == nil {
		t.Fatal("expected a refusal for a string that is neither encoding")
	}
}

// The Zenon half of the decoy problem. Publishing a send to the htlc contract
// costs a fee and nothing else -- the contract declines a call it does not
// like, but the block is on the chain either way, and this scan walks blocks.
//
// So an Unlock naming the right id and carrying a preimage that opens nothing
// is one transaction, and being newer than the real unlock it wins a scan that
// stops at the first match. What it wins is the secret: the entry is deleted
// when it is unlocked, so the unlocking block is the only remaining copy.
func TestFindHtlcOutcomeIdentifiesTheUnlockByHashing(t *testing.T) {
	// Exercised through the same predicate FindHtlcOutcome applies, because the
	// scan itself needs a node and the decision is what is under test.
	preimage := []byte(strings.Repeat("s", PreimageSize))
	hashLock := sha256Of(preimage)
	decoy := []byte(strings.Repeat("x", PreimageSize))

	if !matchesHashLock(preimage, hashLock) {
		t.Fatal("the real preimage was not recognised")
	}
	if matchesHashLock(decoy, hashLock) {
		t.Fatal("a preimage that opens nothing was accepted; the secret would be lost")
	}
	if matchesHashLock(nil, hashLock) {
		t.Fatal("an empty preimage was accepted")
	}
}

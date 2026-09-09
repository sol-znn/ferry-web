package znn

import (
	"encoding/hex"
	"testing"
)

// The vectors below were produced by go-zenon's own ABI encoder. They are the
// whole reason a hand-rolled encoder is acceptable here: if this file passes,
// the bytes this package builds are byte-for-byte the bytes the chain's own code
// builds, so keeping go-ethereum out of the wasm module costs nothing in
// correctness.
//
// Regenerate them from a program that imports go-zenon's vm/abi and
// vm/embedded/definition and packs the three methods below with these inputs.

const (
	vectorAddress  = "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d"
	vectorAddrHex  = "0035941e758a1f155fb30c1217a8186ed9e12651"
	vectorHashLock = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	vectorID       = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	vectorPreimage = "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f"
	vectorExpiry   = 1788700000

	wantCreate = "5c7e7110" +
		"0000000000000000000000000035941e758a1f155fb30c1217a8186ed9e12651" +
		"000000000000000000000000000000000000000000000000000000006a9d6560" +
		"0000000000000000000000000000000000000000000000000000000000000001" +
		"0000000000000000000000000000000000000000000000000000000000000020" +
		"00000000000000000000000000000000000000000000000000000000000000a0" +
		"0000000000000000000000000000000000000000000000000000000000000020" +
		"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

	wantUnlock = "d33791d3" +
		"aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899" +
		"0000000000000000000000000000000000000000000000000000000000000040" +
		"0000000000000000000000000000000000000000000000000000000000000020" +
		"202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f"

	wantReclaim = "7e003c8d" +
		"aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad test vector %q: %v", s, err)
	}
	return raw
}

func TestMethodIDs(t *testing.T) {
	for _, tc := range []struct{ sig, want string }{
		{sigCreate, "5c7e7110"},
		{sigUnlock, "d33791d3"},
		{sigReclaim, "7e003c8d"},
	} {
		if got := hex.EncodeToString(methodID(tc.sig)); got != tc.want {
			t.Errorf("methodID(%q) = %s, want %s", tc.sig, got, tc.want)
		}
	}
}

func TestParseAddress(t *testing.T) {
	got, err := ParseAddress(vectorAddress)
	if err != nil {
		t.Fatalf("ParseAddress: %v", err)
	}
	if hex.EncodeToString(got) != vectorAddrHex {
		t.Errorf("ParseAddress = %s, want %s", hex.EncodeToString(got), vectorAddrHex)
	}

	// The contract's own address round-trips too, which is the one this package
	// sends every call TO.
	if _, err := ParseAddress(HtlcContractAddress); err != nil {
		t.Errorf("ParseAddress(HtlcContractAddress): %v", err)
	}
}

// A mistyped address must not survive to become a payee. bech32's checksum is
// what catches it, and the point of decoding here rather than passing the
// string through is that it is caught at all.
func TestParseAddressRejects(t *testing.T) {
	for _, bad := range []string{
		"",
		"z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5e", // one character changed
		"bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
		"z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y",
		ZnnTokenStandard, // right family, wrong field: a ZTS is not an address
	} {
		if _, err := ParseAddress(bad); err == nil {
			t.Errorf("ParseAddress(%q) succeeded, want an error", bad)
		}
	}
}

// go-zenon's own constant, decoded, is the vector: it is public, stable, and
// exactly what a real "blank means ZNN" field resolves to.
func TestParseTokenStandard(t *testing.T) {
	got, err := ParseTokenStandard(ZnnTokenStandard)
	if err != nil {
		t.Fatalf("ParseTokenStandard: %v", err)
	}
	if len(got) != TokenStandardSize {
		t.Errorf("ParseTokenStandard decoded %d bytes, want %d", len(got), TokenStandardSize)
	}
}

// The confusion this pair of fields invites, both directions. A ZTS and an
// address are both bech32 and both start with the letter z, which is exactly
// what makes a copy-paste mistake between the two fields easy and what a
// prefix check has to catch even though the strings look superficially alike.
func TestParseTokenStandardRejects(t *testing.T) {
	for _, bad := range []string{
		"",
		vectorAddress, // a real Zenon ADDRESS, not a token
		"bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4", // a Bitcoin address
		"zts1znnxxxxxxxxxxxxx9z4ule",                 // one character changed
	} {
		if _, err := ParseTokenStandard(bad); err == nil {
			t.Errorf("ParseTokenStandard(%q) succeeded, want an error", bad)
		}
	}
}

func TestPackCreate(t *testing.T) {
	addr, err := ParseAddress(vectorAddress)
	if err != nil {
		t.Fatalf("ParseAddress: %v", err)
	}
	got, err := PackCreate(addr, vectorExpiry, HashTypeSHA256, 32, mustHex(t, vectorHashLock))
	if err != nil {
		t.Fatalf("PackCreate: %v", err)
	}
	if hex.EncodeToString(got) != wantCreate {
		t.Errorf("PackCreate\n got %s\nwant %s", hex.EncodeToString(got), wantCreate)
	}
}

func TestPackUnlock(t *testing.T) {
	got, err := PackUnlock(mustHex(t, vectorID), mustHex(t, vectorPreimage))
	if err != nil {
		t.Fatalf("PackUnlock: %v", err)
	}
	if hex.EncodeToString(got) != wantUnlock {
		t.Errorf("PackUnlock\n got %s\nwant %s", hex.EncodeToString(got), wantUnlock)
	}
}

func TestPackReclaim(t *testing.T) {
	got, err := PackReclaim(mustHex(t, vectorID))
	if err != nil {
		t.Fatalf("PackReclaim: %v", err)
	}
	if hex.EncodeToString(got) != wantReclaim {
		t.Errorf("PackReclaim\n got %s\nwant %s", hex.EncodeToString(got), wantReclaim)
	}
}

// A preimage shorter than a word still has to occupy a whole one, and the
// length prefix still has to be its real length. This is the case the vectors
// above do not cover, because ferry's preimages are always 32 bytes — but a
// recovered swap can carry whatever the initiator chose.
func TestPackUnlockShortPreimage(t *testing.T) {
	got, err := PackUnlock(mustHex(t, vectorID), []byte{0xff})
	if err != nil {
		t.Fatalf("PackUnlock: %v", err)
	}
	want := "d33791d3" +
		vectorID +
		"0000000000000000000000000000000000000000000000000000000000000040" +
		"0000000000000000000000000000000000000000000000000000000000000001" +
		"ff00000000000000000000000000000000000000000000000000000000000000"
	if hex.EncodeToString(got) != want {
		t.Errorf("PackUnlock\n got %s\nwant %s", hex.EncodeToString(got), want)
	}
}

func TestPackRejectsBadSizes(t *testing.T) {
	addr, err := ParseAddress(vectorAddress)
	if err != nil {
		t.Fatalf("ParseAddress: %v", err)
	}
	lock := mustHex(t, vectorHashLock)

	if _, err := PackCreate(addr[:10], vectorExpiry, HashTypeSHA256, 32, lock); err == nil {
		t.Error("PackCreate accepted a short address")
	}
	if _, err := PackCreate(addr, 0, HashTypeSHA256, 32, lock); err == nil {
		t.Error("PackCreate accepted a zero expiry")
	}
	if _, err := PackCreate(addr, vectorExpiry, HashTypeSHA256, 32, nil); err == nil {
		t.Error("PackCreate accepted an empty hashlock")
	}
	if _, err := PackUnlock(mustHex(t, vectorID)[:16], []byte{1}); err == nil {
		t.Error("PackUnlock accepted a short id")
	}
	if _, err := PackReclaim(mustHex(t, vectorID)[:16]); err == nil {
		t.Error("PackReclaim accepted a short id")
	}
}

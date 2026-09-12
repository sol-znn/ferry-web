package main

import (
	"testing"

	"github.com/btcsuite/btcd/chaincfg"
)

// TestDestinationAddressTypes pins down exactly which address types a user can
// name as the place their coins should land. The wallet guide claims all of
// these work, so the claim is tested rather than assumed.
func TestDestinationAddressTypes(t *testing.T) {
	params := &chaincfg.MainNetParams
	cases := []struct {
		kind string
		addr string
	}{
		{"P2PKH (legacy, 1...)", "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2"},
		{"P2SH (3...)", "3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy"},
		{"P2WPKH (native segwit, bc1q...)", "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"},
		{"P2WSH (bc1q..., 32-byte)", "bc1qrp33g0q5c5txsp9arysrx4k6zdkfs4nce4xj0gdcccefvpysxf3qccfmv3"},
		{"P2TR (taproot, bc1p...)", "bc1p0xlxvlhemja6c4dqv22uapctqupfhlxm9h8z3k2e72q4k9hcz7vqzk5jj0"},
	}
	for _, c := range cases {
		if _, err := addressScript(c.addr, params); err != nil {
			t.Errorf("%s: %v", c.kind, err)
		} else {
			t.Logf("%s: accepted", c.kind)
		}
	}
}

// TestDestinationRejectsWrongNetwork makes sure a mainnet address cannot be
// used on a testnet swap, which would otherwise burn the funds.
func TestDestinationRejectsWrongNetwork(t *testing.T) {
	if _, err := addressScript("1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2", &chaincfg.TestNet3Params); err == nil {
		t.Error("expected a mainnet address to be rejected on testnet")
	}
	if _, err := addressScript("not-an-address", &chaincfg.MainNetParams); err == nil {
		t.Error("expected garbage to be rejected")
	}
}

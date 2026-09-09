package main

import (
	"strings"
	"testing"
)

// diffSignedBlock is the check that runs after the wallet has already acted, so
// what it must not do is stay quiet. Every case below is a way the published
// block can differ from the one this page built, and the one that matters most
// is the first: the extension re-reads its selected account at signing time, so
// the account that signs is not necessarily the account it reported.

func proposal() *walletBlock {
	b := newWalletBlock(69, "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d")
	b.Amount = "1000000000"
	b.TokenStandard = "zts1znnxxxxxxxxxxxxx9z4ulx"
	b.Data = "XH5xEAAA"
	return b
}

// signedFrom renders a proposal the way the wallet's toJson would, with the
// fields it fills in itself added.
func signedFrom(b *walletBlock) map[string]any {
	return map[string]any{
		"address":         b.Address,
		"chainIdentifier": float64(b.ChainIdentifier),
		"toAddress":       b.ToAddress,
		"amount":          b.Amount,
		"tokenStandard":   b.TokenStandard,
		"data":            b.Data,
		// The wallet's own, and deliberately not compared.
		"height":       float64(42),
		"previousHash": "aa",
		"nonce":        "0000000000000001",
		"signature":    "not-compared",
	}
}

func TestDiffSignedBlockAccepts(t *testing.T) {
	if got := diffSignedBlock(proposal(), signedFrom(proposal())); len(got) != 0 {
		t.Errorf("an untouched block was reported as changed: %v", got)
	}
}

func TestDiffSignedBlockCatchesADifferentSigner(t *testing.T) {
	signed := signedFrom(proposal())
	signed["address"] = "z1qzsomewhereelsexxxxxxxxxxxxxxxxxxxxxxx"
	got := diffSignedBlock(proposal(), signed)
	if len(got) != 1 || !strings.Contains(got[0], "signed by") {
		t.Fatalf("a different signing account was not reported: %v", got)
	}
}

func TestDiffSignedBlockCatchesTheRest(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"chain":  func(m map[string]any) { m["chainIdentifier"] = float64(1) },
		"payee":  func(m map[string]any) { m["toAddress"] = "z1qzsomewhereelsexxxxxxxxxxxxxxxxxxxxxxx" },
		"data":   func(m map[string]any) { m["data"] = "different" },
		"token":  func(m map[string]any) { m["tokenStandard"] = "zts1qsrxxxxxxxxxxxxxmrhjll" },
		"amount": func(m map[string]any) { m["amount"] = "1" },
	} {
		signed := signedFrom(proposal())
		mutate(signed)
		if got := diffSignedBlock(proposal(), signed); len(got) == 0 {
			t.Errorf("a changed %s was not reported", name)
		}
	}
}

// A wallet that reports nothing back must not read as a wallet that agreed.
// There is nothing to compare, so there is nothing to say -- the create's own
// verification against the chain is what covers this case, and inventing a
// mismatch here would block a swap over a field the extension simply omits.
func TestDiffSignedBlockWithNothingToCompare(t *testing.T) {
	if got := diffSignedBlock(proposal(), nil); got != nil {
		t.Errorf("an absent signed block produced findings: %v", got)
	}
	if got := diffSignedBlock(nil, signedFrom(proposal())); got != nil {
		t.Errorf("an absent proposal produced findings: %v", got)
	}
	// A partial block is compared on what it does carry, and silent on the rest.
	if got := diffSignedBlock(proposal(), map[string]any{"height": float64(3)}); len(got) != 0 {
		t.Errorf("a block carrying none of the compared fields produced findings: %v", got)
	}
}

// The amount arrives as a string from toJson, but a JSON number is what a
// hand-assembled payload carries, and reporting a mismatch between 1000000000
// and 1000000000 would block a swap over a type.
func TestDiffSignedBlockAmountAsNumber(t *testing.T) {
	signed := signedFrom(proposal())
	signed["amount"] = float64(1000000000)
	if got := diffSignedBlock(proposal(), signed); len(got) != 0 {
		t.Errorf("a numeric amount was reported as a mismatch: %v", got)
	}
}

func TestSameEndpoint(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"http://127.0.0.1:35997", "http://127.0.0.1:35997", true},
		{"http://127.0.0.1:35997/", "http://127.0.0.1:35997", true},
		{"HTTP://127.0.0.1:35997", "http://127.0.0.1:35997", true},
		// Almost always the same node, and deliberately not claimed to be:
		// the momentum comparison is what proves it.
		{"ws://127.0.0.1:35998", "http://127.0.0.1:35997", false},
		{"", "http://127.0.0.1:35997", false},
	} {
		if got := sameEndpoint(tc.a, tc.b); got != tc.want {
			t.Errorf("sameEndpoint(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

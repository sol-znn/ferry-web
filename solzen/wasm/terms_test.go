package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An amount is a number and a unit, and only the number crosses in an offer as
// a number. ZnnDecimals is the unit, it arrives from the counterparty, and
// nothing used to check it against the chain.
//
// The consequence is the whole attack: for real ZNN, `ZnnAmount: "10"` with
// `ZnnDecimals: 0` renders as "10" on every screen on both sides -- because
// FormatAmount(10, 0) and FormatAmount(10^9, 8) are the same string -- and then
// verifies perfectly against an HTLC holding ten base units, a ten-millionth of
// what was displayed. Right token, right hashlock, right parties, right expiry.
func TestAgreedTokenIsCheckedAgainstTheChain(t *testing.T) {
	const zts = "zts1znnxxxxxxxxxxxxx9z4ulx"

	// The display collision the attack depends on. Asserted rather than
	// asserted-about, so that a change to FormatAmount cannot quietly remove
	// the reason this check exists.
	if a, b := formatZnnAmount("10", 0), formatZnnAmount("1000000000", 8); a != b {
		t.Fatalf("the premise of this test no longer holds: %q vs %q", a, b)
	}

	cases := []struct {
		name     string
		decimals int
		node     string
		wantErr  string
	}{
		{
			name:     "the offer's decimals match the chain",
			decimals: 8,
			node:     `{"jsonrpc":"2.0","id":1,"result":{"tokenStandard":"` + zts + `","symbol":"ZNN","decimals":8}}`,
		},
		{
			name:     "an offer that understates the unit is refused",
			decimals: 0,
			node:     `{"jsonrpc":"2.0","id":1,"result":{"tokenStandard":"` + zts + `","symbol":"ZNN","decimals":8}}`,
			wantErr:  "your node says 8",
		},
		{
			name:     "a node answering about another token is not read for decimals",
			decimals: 8,
			node:     `{"jsonrpc":"2.0","id":1,"result":{"tokenStandard":"zts1qsrxxxxxxxxxxxxxmrhjll","symbol":"QSR","decimals":0}}`,
			wantErr:  "it answered about",
		},
		{
			name:     "an implausible decimals value is refused",
			decimals: 8,
			node:     `{"jsonrpc":"2.0","id":1,"result":{"tokenStandard":"` + zts + `","symbol":"ZNN","decimals":99}}`,
			wantErr:  "implausible",
		},
		{
			// A skipped check reads exactly like a passed one, and this is the
			// one number that decides what an amount means.
			name:     "a node that will not answer is a refusal, not a pass",
			decimals: 8,
			node:     `{"jsonrpc":"2.0","id":1,"result":null}`,
			wantErr:  "could not be read from your Zenon node",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.node))
			}))
			defer node.Close()

			m := NewManager(NewStore(NewMemoryBackend()))
			m.SetConfig(Config{ZenonURL: node.URL})

			terms := termsFixture()
			terms.ZnnToken = zts
			terms.ZnnAmount = "10"
			terms.ZnnDecimals = tc.decimals

			err := m.checkAgreedToken(context.Background(), &terms)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected these terms to be accepted, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected a refusal, got none -- this is the amount attack")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("refused for the wrong reason: %v", err)
			}
		})
	}
}

// A swap is read through two node URLs that live in one global setting, so
// changing that setting re-reads every swap against somewhere else. The escrow
// is not on the new cluster, so the leg reads *not funded* -- and the next
// thing the page offers is to fund it.
func TestChainPinningRefusesTheWrongCluster(t *testing.T) {
	terms := termsFixture()

	if err := terms.CheckChains(terms.SolGenesis, terms.ZnnChainID); err != nil {
		t.Fatalf("the chains it was made on should be accepted: %v", err)
	}
	if err := terms.CheckChains("SomeOtherClusterGenesisHash", terms.ZnnChainID); err == nil {
		t.Fatal("a different Solana cluster was accepted; funding here would be a second escrow on the wrong chain")
	}
	if err := terms.CheckChains(terms.SolGenesis, terms.ZnnChainID+1); err == nil {
		t.Fatal("a different Zenon chain was accepted")
	}
	// A node that could not be asked is not a mismatch: every other read fails
	// too, and this one has nothing to add.
	if err := terms.CheckChains("", 0); err != nil {
		t.Fatalf("an unreachable node should not read as a mismatch: %v", err)
	}
	// Neither is a record written before this existed. It has less protection,
	// which is not a reason to strand it.
	old := terms
	old.SolGenesis, old.ZnnChainID = "", 0
	if err := old.CheckChains("anything", 42); err != nil {
		t.Fatalf("an older record should still load: %v", err)
	}
}

// Import is the one way a record arrives without having been built here, so it
// is the one place these consistencies have to be established rather than
// assumed. A record whose secret does not open its own hashlock would sit in
// the store looking ready and produce a claim that fails on chain.
func TestImportRefusesRecordsThisProgramCouldNotHaveWritten(t *testing.T) {
	good := &Swap{
		ID:      strings.Repeat("11", 32),
		Created: 1,
		Updated: 1,
		Terms:   termsFixture(),
		Role:    Role{SendsSol: true, Initiator: true},
	}
	// A real secret/hashlock pair, so the record is internally consistent.
	secret, hashlock, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	good.Secret = secret
	good.Terms.Hashlock = hashlock

	broken := func(mutate func(*Swap)) *Swap {
		s := *good
		s.Terms = good.Terms
		mutate(&s)
		return &s
	}

	cases := []struct {
		name string
		swap *Swap
	}{
		{"an id that is not 32 bytes of hex", broken(func(s *Swap) { s.ID = "nonsense" })},
		{"an id that disagrees with its own terms", broken(func(s *Swap) { s.ID = strings.Repeat("22", 32) })},
		{"a secret that does not open its hashlock", broken(func(s *Swap) { s.Secret = strings.Repeat("ab", 32) })},
		{"a per-swap key that is not a key", broken(func(s *Swap) { s.ZnnSwapSeed = "zzzz" })},
		{"a sweep address that is not a Zenon address", broken(func(s *Swap) { s.ZnnHomeAddress = "z1nope" })},
		{"an HTLC id that is not a hash", broken(func(s *Swap) { s.HtlcID = "not-a-hash" })},
		{"a Solana address that will not parse", broken(func(s *Swap) { s.Terms.SolReceiver = "0OIl" })},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewStore(NewMemoryBackend())
			raw, err := json.Marshal(Export{Format: exportFormat, Swaps: []*Swap{tc.swap, good}})
			if err != nil {
				t.Fatal(err)
			}
			imported, _, refused, err := store.Import(string(raw))
			if err != nil {
				t.Fatalf("one bad record must not abort the run: %v", err)
			}
			if len(refused) != 1 {
				t.Fatalf("expected exactly one refusal, got %v", refused)
			}
			// The healthy record still came back. A file with one bad entry is
			// still a file whose other entries hold keys.
			if imported != 1 {
				t.Fatalf("the good record was not imported alongside the bad one (imported %d)", imported)
			}
		})
	}

	t.Run("a consistent record is imported", func(t *testing.T) {
		store := NewStore(NewMemoryBackend())
		raw, _ := json.Marshal(Export{Format: exportFormat, Swaps: []*Swap{good}})
		imported, _, refused, err := store.Import(string(raw))
		if err != nil || imported != 1 || len(refused) != 0 {
			t.Fatalf("a good record was refused: imported=%d refused=%v err=%v", imported, refused, err)
		}
	})
}

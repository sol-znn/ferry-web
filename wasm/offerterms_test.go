package main

import (
	"strings"
	"testing"
)

// An offer from someone funding a Bitcoin leg as the initiator, and the params
// the form produces when the other browser decodes it and changes nothing.
// Every case below starts from this pair, so what a case is testing is the one
// line it changes.
func joiningOffer() (Offer, CreateParams) {
	o := Offer{
		Version: 2, Network: "regtest", FromRole: RoleInitiator,
		SecretHash: strings.Repeat("ab", 32),
		Give: OfferLeg{
			Chain: ChainBTC, Amount: "400000", PKH: strings.Repeat("cd", 20),
		},
		Take: OfferLeg{Chain: ChainZNN, Amount: "10", Addr: znnPeerAddr},
	}
	p := CreateParams{
		Role:          RoleParticipant,
		SecretHashHex: o.SecretHash,
		Out: CreateLeg{
			Chain: ChainZNN, Amount: "10", SelfAddr: znnSelfAddr, PeerAddr: znnPeerAddr,
		},
		In: CreateLeg{
			Chain: ChainBTC, Amount: "400000", SelfAddr: regtestDest,
		},
	}
	return o, p
}

func TestCheckCreateAcceptsAnUneditedJoin(t *testing.T) {
	o, p := joiningOffer()
	if err := o.CheckCreate("regtest", p); err != nil {
		t.Fatalf("refused a form filled in from the offer and left alone: %v", err)
	}
}

// The whole point. Each of these is a field somebody can edit in the form after
// decoding, and every one of them puts the two browsers on different swaps.
func TestCheckCreateRefusesEveryChangedTerm(t *testing.T) {
	for name, edit := range map[string]func(*CreateParams){
		"btc amount lowered":  func(p *CreateParams) { p.In.Amount = "399000" },
		"btc amount raised":   func(p *CreateParams) { p.In.Amount = "500000" },
		"zenon amount":        func(p *CreateParams) { p.Out.Amount = "9" },
		"zenon amount blank":  func(p *CreateParams) { p.Out.Amount = "" },
		"zenon token":         func(p *CreateParams) { p.Out.Token = QsrTokenStandard },
		"secret hash":         func(p *CreateParams) { p.SecretHashHex = strings.Repeat("ef", 32) },
		"their zenon address": func(p *CreateParams) { p.Out.PeerAddr = "z1qqsomeoneelse" },
		"their pkh":           func(p *CreateParams) { p.In.PeerPKH = strings.Repeat("11", 20) },
		"legs swapped":        func(p *CreateParams) { p.Out, p.In = p.In, p.Out },
		"role flipped":        func(p *CreateParams) { p.Role = RoleInitiator },
		// The pair itself. A form that quietly changed which chain a half
		// settles on would leave two people watching two different chains and
		// each reporting the other as not having funded.
		"a chain changed": func(p *CreateParams) {
			p.In = CreateLeg{Chain: ChainSOL, Amount: "1.5", SelfAddr: solSelfAddr,
				Program: solProgramID, SwapID: solSwapIDHex}
		},
	} {
		o, p := joiningOffer()
		edit(&p)
		if err := o.CheckCreate("regtest", p); err == nil {
			t.Errorf("%s: accepted, so both sides would build different contracts", name)
		}
	}
}

// A Solana leg has two extra terms that decide WHICH account holds the money.
// Two sides on different deployments, or with different seeds, each derive an
// escrow the other never looks at -- and both report the other's leg as simply
// not funded yet, which is the least legible way a swap can fail.
func TestCheckCreateRefusesADifferentSolanaEscrow(t *testing.T) {
	base := func() (Offer, CreateParams) {
		o := Offer{
			Version: 2, Network: "regtest", FromRole: RoleInitiator,
			SecretHash: strings.Repeat("ab", 32),
			Give: OfferLeg{Chain: ChainSOL, Amount: "1.5", Addr: solPeerAddr,
				Program: solProgramID, SwapID: solSwapIDHex},
			Take: OfferLeg{Chain: ChainZNN, Amount: "10", Addr: znnPeerAddr},
		}
		p := CreateParams{
			Role: RoleParticipant, SecretHashHex: o.SecretHash,
			Out: CreateLeg{Chain: ChainZNN, Amount: "10",
				SelfAddr: znnSelfAddr, PeerAddr: znnPeerAddr},
			In: CreateLeg{Chain: ChainSOL, Amount: "1.5", SelfAddr: solSelfAddr,
				PeerAddr: solPeerAddr, Program: solProgramID, SwapID: solSwapIDHex},
		}
		return o, p
	}
	o, p := base()
	if err := o.CheckCreate("regtest", p); err != nil {
		t.Fatalf("refused an unedited Solana join: %v", err)
	}
	for name, edit := range map[string]func(*CreateParams){
		"another deployment": func(p *CreateParams) { p.In.Program = solSelfAddr },
		"another seed":       func(p *CreateParams) { p.In.SwapID = strings.Repeat("11", 32) },
	} {
		o, p := base()
		edit(&p)
		if err := o.CheckCreate("regtest", p); err == nil {
			t.Errorf("%s: accepted, so the two sides watch different escrows", name)
		}
	}
}

// A swap on the wrong chain is two swaps that can never meet, whatever else
// they agree about.
func TestCheckCreateRefusesAnotherNetwork(t *testing.T) {
	o, p := joiningOffer()
	if err := o.CheckCreate("mainnet", p); err == nil {
		t.Error("accepted a regtest offer being created on mainnet")
	}
}

// The joiner's own details are the joiner's own. Refusing these would refuse
// the swaps this check exists to protect.
func TestCheckCreateIgnoresWhatIsNotATerm(t *testing.T) {
	for name, edit := range map[string]func(*CreateParams){
		"own destination address": func(p *CreateParams) { p.In.SelfAddr = "bcrt1qelsewhere" },
		"own zenon address":       func(p *CreateParams) { p.Out.SelfAddr = "z1qqotherofmine" },
		"own locktime":            func(p *CreateParams) { p.LockHours = 9 },
		// Both of these are routinely supplied after creation, over a session
		// or by hand. Not yet filled in is not a disagreement.
		"peer zenon address not yet known": func(p *CreateParams) { p.Out.PeerAddr = "" },
		"peer pkh not yet known":           func(p *CreateParams) { p.In.PeerPKH = "" },
	} {
		o, p := joiningOffer()
		edit(&p)
		if err := o.CheckCreate("regtest", p); err != nil {
			t.Errorf("%s: refused, but it is not a term of the trade: %v", name, err)
		}
	}
}

// Two spellings of one number are one term. A check that called these a
// mismatch would be inventing disagreements of its own, which costs more trust
// than it buys.
func TestCheckCreateComparesAmountsNotSpellings(t *testing.T) {
	for _, same := range []struct{ theirs, ours string }{
		{"10", "10.0"},
		{"10", "010"},
		{"1.25", "1.250"},
		{"0.5", ".5"},
		{"10", " 10 "},
		{"10.", "10"},
	} {
		o, p := joiningOffer()
		o.Take.Amount, p.Out.Amount = same.theirs, same.ours
		if err := o.CheckCreate("regtest", p); err != nil {
			t.Errorf("%q vs %q: called one amount two: %v", same.theirs, same.ours, err)
		}
	}
	for _, differ := range []struct{ theirs, ours string }{
		{"10", "100"},
		{"1.25", "1.26"},
		{"0.5", "0.05"},
	} {
		o, p := joiningOffer()
		o.Take.Amount, p.Out.Amount = differ.theirs, differ.ours
		if err := o.CheckCreate("regtest", p); err == nil {
			t.Errorf("%q vs %q: accepted two different amounts", differ.theirs, differ.ours)
		}
	}
}

// Blank means ZNN in the creation form, so a form that leaves the token empty
// and an offer that names ZNN outright have agreed.
func TestCheckCreateTreatsBlankTokenAsZnn(t *testing.T) {
	o, p := joiningOffer()
	o.Take.Token = ""
	p.Out.Token = resolveToken("")
	if err := o.CheckCreate("regtest", p); err != nil {
		t.Errorf("blank and an explicit ZNN are the same term: %v", err)
	}
}

// The error is read by somebody who has to go and fix a field, so it has to say
// which one and what both sides think it is.
func TestCheckCreateNamesTheFieldAndBothValues(t *testing.T) {
	o, p := joiningOffer()
	p.In.Amount = "399000"
	err := o.CheckCreate("regtest", p)
	if err == nil {
		t.Fatal("accepted a changed amount")
	}
	for _, want := range []string{"amount", "400000", "399000"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// An initiator answering a participant's offer generates their own secret, so
// there is no hash for them to have copied and none to hold them to.
func TestCheckCreateSkipsTheSecretHashForAnInitiator(t *testing.T) {
	o, p := joiningOffer()
	o.FromRole = RoleParticipant
	p.Role = RoleInitiator
	p.SecretHashHex = ""
	if err := o.CheckCreate("regtest", p); err != nil {
		t.Errorf("an initiator has no hash to match: %v", err)
	}
}

// One create call, every changed term. Somebody who edited three fields should
// not have to press Create three times to find that out.
func TestCheckCreateReportsEveryMismatchAtOnce(t *testing.T) {
	o, p := joiningOffer()
	p.In.Amount = "1"
	p.Out.Amount = "3"
	p.SecretHashHex = strings.Repeat("ef", 32)
	err := o.CheckCreate("regtest", p)
	if err == nil {
		t.Fatal("accepted three changed terms")
	}
	if n := strings.Count(err.Error(), "\n  · "); n != 3 {
		t.Errorf("reported %d mismatches, want 3: %v", n, err)
	}
}

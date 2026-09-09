package main

import (
	"strings"
	"testing"
)

// An offer from someone sending BTC as the initiator, and the params the form
// produces when the other browser decodes it and changes nothing. Every case
// below starts from this pair, so what a case is testing is the one line it
// changes.
func joiningOffer() (Offer, CreateParams) {
	o := Offer{
		Version: 1, Network: "regtest", FromRole: RoleInitiator,
		SecretHash: strings.Repeat("ab", 32), PKH: strings.Repeat("cd", 20),
		BTCLeg: LegSend, AmountSats: 400_000,
		ZenonAddr: "z1qqsyrius", ZenonToken: "", ZenonAmt: "10",
	}
	p := CreateParams{
		Role: RoleParticipant, Leg: LegReceive, AmountSats: 400_000,
		DestAddr:         "bcrt1qsomewhere",
		SecretHashHex:    o.SecretHash,
		ZenonSelfAddress: "z1qqmine",
		ZenonPeerAddress: o.ZenonAddr,
		ZenonAmount:      o.ZenonAmt,
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
		"btc amount lowered":  func(p *CreateParams) { p.AmountSats = 399_000 },
		"btc amount raised":   func(p *CreateParams) { p.AmountSats = 500_000 },
		"zenon amount":        func(p *CreateParams) { p.ZenonAmount = "9" },
		"zenon amount blank":  func(p *CreateParams) { p.ZenonAmount = "" },
		"zenon token":         func(p *CreateParams) { p.ZenonToken = "zts1someothertoken" },
		"secret hash":         func(p *CreateParams) { p.SecretHashHex = strings.Repeat("ef", 32) },
		"their zenon address": func(p *CreateParams) { p.ZenonPeerAddress = "z1qqsomeoneelse" },
		"their pkh":           func(p *CreateParams) { p.CounterpartyPKHHex = strings.Repeat("11", 20) },
		"leg flipped":         func(p *CreateParams) { p.Leg = LegSend },
		"role flipped":        func(p *CreateParams) { p.Role = RoleInitiator },
	} {
		o, p := joiningOffer()
		edit(&p)
		if err := o.CheckCreate("regtest", p); err == nil {
			t.Errorf("%s: accepted, so both sides would build different contracts", name)
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
		"own destination address": func(p *CreateParams) { p.DestAddr = "bcrt1qelsewhere" },
		"own zenon address":       func(p *CreateParams) { p.ZenonSelfAddress = "z1qqotherofmine" },
		"own locktime":            func(p *CreateParams) { p.LockHours = 9 },
		// Both of these are routinely supplied after creation, over a session
		// or by hand. Not yet filled in is not a disagreement.
		"peer zenon address not yet known": func(p *CreateParams) { p.ZenonPeerAddress = "" },
		"peer pkh not yet known":           func(p *CreateParams) { p.CounterpartyPKHHex = "" },
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
		o.ZenonAmt, p.ZenonAmount = same.theirs, same.ours
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
		o.ZenonAmt, p.ZenonAmount = differ.theirs, differ.ours
		if err := o.CheckCreate("regtest", p); err == nil {
			t.Errorf("%q vs %q: accepted two different amounts", differ.theirs, differ.ours)
		}
	}
}

// Blank means ZNN in the creation form, so a form that leaves the token empty
// and an offer that names ZNN outright have agreed.
func TestCheckCreateTreatsBlankTokenAsZnn(t *testing.T) {
	o, p := joiningOffer()
	o.ZenonToken = ""
	p.ZenonToken = ZenonLeg{}.AgreedToken()
	if err := o.CheckCreate("regtest", p); err != nil {
		t.Errorf("blank and an explicit ZNN are the same term: %v", err)
	}
}

// The error is read by somebody who has to go and fix a field, so it has to say
// which one and what both sides think it is.
func TestCheckCreateNamesTheFieldAndBothValues(t *testing.T) {
	o, p := joiningOffer()
	p.AmountSats = 399_000
	err := o.CheckCreate("regtest", p)
	if err == nil {
		t.Fatal("accepted a changed amount")
	}
	for _, want := range []string{"Bitcoin amount", "400000", "399000"} {
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
	p.AmountSats = 1
	p.ZenonAmount = "3"
	p.SecretHashHex = strings.Repeat("ef", 32)
	err := o.CheckCreate("regtest", p)
	if err == nil {
		t.Fatal("accepted three changed terms")
	}
	if n := strings.Count(err.Error(), "\n  · "); n != 3 {
		t.Errorf("reported %d mismatches, want 3: %v", n, err)
	}
}

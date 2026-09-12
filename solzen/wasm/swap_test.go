package main

import (
	"encoding/hex"
	"strings"
	"testing"
)

// The rules a swap is safe by, checked without a chain in the way. These are
// the ones that cost money when they are wrong, and the ones a chain test would
// only exercise for the values it happened to use.

func termsFixture() Terms {
	return Terms{
		SwapID:      strings.Repeat("11", 32),
		Hashlock:    strings.Repeat("22", 32),
		Initiator:   LegSolana,
		SolGenesis:  "EtWTRABZaYq6iMfeYKouRu166VU2xqa1wcaWoxPkrZBG",
		ZnnChainID:  1,
		SolProgram:  "5mcZ6qPc5YY7PnwvXJTtVDK8hgijqyXBCFH8eR3wzXFF",
		SolSender:   "8S8cXUuRjrqr9obfqjdCJNRQqDgiM7QZKA4T1XkK2Fek",
		SolReceiver: "9tPYQGDCDpZ8Vs4bxdb4TVsNbTMuNvPHrfBK7q9tKAcH",
		SolLamports: 400_000_000,
		SolTimelock: 1_800_000_000 + 48*3600,
		ZnnSender:   "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d",
		ZnnReceiver: "z1qzmzssx28dc0fmvlca05hyxk2kgkgu7n0cj8pl",
		ZnnToken:    "zts1znnxxxxxxxxxxxxx9z4ulx",
		ZnnAmount:   "700000000",
		ZnnDecimals: 8,
		ZnnExpiry:   1_800_000_000 + 24*3600,
	}
}

// The initiator's leg must outlast the participant's by enough that the
// participant can act on a secret revealed at the last moment. This is the one
// rule whose violation is invisible until it costs someone the whole amount.
func TestOrderingRefusesATooNarrowGap(t *testing.T) {
	now := int64(1_800_000_000)
	terms := termsFixture()
	if err := terms.CheckOrdering(now); err != nil {
		t.Fatalf("48h against 24h should be accepted: %v", err)
	}

	terms.ZnnExpiry = terms.SolTimelock - MinLegGapSeconds + 1
	err := terms.CheckOrdering(now)
	if err == nil {
		t.Fatal("a gap one second under the minimum was accepted")
	}
	if !strings.Contains(err.Error(), "at least") {
		t.Errorf("the refusal should say what the minimum is, got %q", err)
	}

	terms.ZnnExpiry = terms.SolTimelock - MinLegGapSeconds
	if err := terms.CheckOrdering(now); err != nil {
		t.Errorf("a gap exactly at the minimum should be accepted: %v", err)
	}
}

// The ordering is about roles, not chains: reversing which side initiates has
// to reverse which deadline must be the later one.
func TestOrderingFollowsTheInitiatorNotTheChain(t *testing.T) {
	now := int64(1_800_000_000)
	terms := termsFixture()
	terms.Initiator = LegZenon
	if err := terms.CheckOrdering(now); err == nil {
		t.Fatal("with Zenon initiating, a Zenon leg that expires first must be refused")
	}
	terms.ZnnExpiry, terms.SolTimelock = terms.SolTimelock, terms.ZnnExpiry
	if err := terms.CheckOrdering(now); err != nil {
		t.Errorf("swapping the deadlines with the roles should be accepted: %v", err)
	}
}

// A swap that was safe to accept becomes unsafe by sitting still, so the check
// takes the current time rather than being made once.
func TestOrderingRefusesALegAboutToExpire(t *testing.T) {
	terms := termsFixture()
	tooLate := terms.ZnnExpiry - MinRemainingSeconds + 60
	err := terms.CheckOrdering(tooLate)
	if err == nil {
		t.Fatal("a participant leg with minutes left was accepted")
	}
	if !strings.Contains(err.Error(), "left") {
		t.Errorf("the refusal should say how much time is left, got %q", err)
	}
}

// The secret surfaces on the leg the initiator claims, which is always the
// participant's. Getting this backwards would point the page at the wrong chain
// to look for it.
func TestPreimageSurfacesOnTheParticipantsChain(t *testing.T) {
	terms := termsFixture()
	terms.Initiator = LegSolana
	if got := terms.PreimageChain(); got != LegZenon {
		t.Errorf("with Solana initiating the secret surfaces on Zenon, got %q", got)
	}
	terms.Initiator = LegZenon
	if got := terms.PreimageChain(); got != LegSolana {
		t.Errorf("with Zenon initiating the secret surfaces on Solana, got %q", got)
	}
}

func TestOfferRoundTrips(t *testing.T) {
	terms := termsFixture()
	offer, err := EncodeOffer(terms)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(offer, offerPrefix) {
		t.Fatalf("an offer should be recognisable by its prefix, got %q", offer[:20])
	}
	kind, back, err := DecodeEnvelope(offer)
	if err != nil {
		t.Fatal(err)
	}
	if kind != "offer" {
		t.Errorf("kind = %q", kind)
	}
	if back != terms {
		t.Errorf("the terms changed in transit:\n got %+v\nwant %+v", back, terms)
	}
}

// An offer carries no secret and no key. This is checked structurally rather
// than by eye, because the day someone adds a field is the day it stops being
// true silently.
func TestOfferCarriesNoSecret(t *testing.T) {
	secret, hashlock, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	terms := termsFixture()
	terms.Hashlock = hashlock
	offer, err := EncodeOffer(terms)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(offer, secret) {
		t.Fatal("the secret appears in the offer")
	}
	_, back, err := DecodeEnvelope(offer)
	if err != nil {
		t.Fatal(err)
	}
	if back.Hashlock != hashlock {
		t.Error("the hashlock did not survive the round trip")
	}
}

// Every field is validated on the way in. A pasted string is the one input that
// comes from someone with an interest in it being wrong.
func TestDecodeRefusesMalformedTermsFieldByField(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Terms)
		expect string
	}{
		{"a short swap id", func(t *Terms) { t.SwapID = "aabb" }, "swap id"},
		{"a hashlock that is not hex", func(t *Terms) { t.Hashlock = strings.Repeat("zz", 32) }, "hashlock"},
		{"an initiating leg that is neither chain", func(t *Terms) { t.Initiator = "btc" }, "initiating leg"},
		{"no SOL amount", func(t *Terms) { t.SolLamports = 0 }, "SOL amount"},
		{"no Solana timelock", func(t *Terms) { t.SolTimelock = 0 }, "Solana timelock"},
		{"no Zenon expiry", func(t *Terms) { t.ZnnExpiry = 0 }, "Zenon expiry"},
		{"no token", func(t *Terms) { t.ZnnToken = "" }, "token is missing"},
		{"a negative Zenon amount", func(t *Terms) { t.ZnnAmount = "-5" }, "positive"},
		{"a Zenon amount that is not a number", func(t *Terms) { t.ZnnAmount = "5.5" }, "whole number"},
		{"implausible decimals", func(t *Terms) { t.ZnnDecimals = 40 }, "decimals"},
		{"no program", func(t *Terms) { t.SolProgram = "" }, "program id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			terms := termsFixture()
			c.mutate(&terms)
			offer, err := EncodeOffer(terms)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = DecodeEnvelope(offer)
			if err == nil {
				t.Fatalf("%s was accepted", c.name)
			}
			if !strings.Contains(err.Error(), c.expect) {
				t.Errorf("the refusal should mention %q, got %q", c.expect, err)
			}
		})
	}
}

func TestDecodeRefusesJunk(t *testing.T) {
	for _, s := range []string{
		"",
		"hello",
		"solzenoffer1:not base64!!",
		"solzenoffer1:" + strings.Repeat("A", 40),
	} {
		if _, _, err := DecodeEnvelope(s); err == nil {
			t.Errorf("%q was accepted", s)
		}
	}
}

// An acceptance must be complete, because the maker acts on it: an incomplete
// one would produce a contract paying an address nobody set.
func TestAcceptMustBeComplete(t *testing.T) {
	terms := termsFixture()
	terms.SolReceiver = ""
	accept, err := EncodeAccept(terms)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeEnvelope(accept); err == nil {
		t.Fatal("an acceptance missing an address was accepted")
	}
}

func TestHashlockOfMatchesTheGeneratedPair(t *testing.T) {
	secret, hashlock, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	got, err := HashlockOf(secret)
	if err != nil {
		t.Fatal(err)
	}
	if got != hashlock {
		t.Errorf("HashlockOf(secret) = %s, want %s", got, hashlock)
	}
	raw, _ := hex.DecodeString(secret)
	if len(raw) != 32 {
		t.Errorf("a secret should be 32 bytes, got %d", len(raw))
	}
	if _, err := HashlockOf("not hex"); err == nil {
		t.Error("a non-hex secret was accepted")
	}
}

func TestRoleNamesTheRightLegs(t *testing.T) {
	solSide := Role{SendsSol: true}
	if solSide.MyLeg() != LegSolana || solSide.TheirLeg() != LegZenon {
		t.Errorf("the SOL sender funds Solana and claims Zenon, got %s/%s", solSide.MyLeg(), solSide.TheirLeg())
	}
	znnSide := Role{SendsSol: false}
	if znnSide.MyLeg() != LegZenon || znnSide.TheirLeg() != LegSolana {
		t.Errorf("the ZNN sender funds Zenon and claims Solana, got %s/%s", znnSide.MyLeg(), znnSide.TheirLeg())
	}
}

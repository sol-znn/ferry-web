package main

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/zenon/ferry-web-v2/wasm/chain"
	"github.com/zenon/ferry-web-v2/wasm/znn"
)

// legs builds a swap with one leg on each side, for the ordering rules.
func legs(role Role, outChain, inChain ChainID) *Swap {
	return &Swap{
		Role: role,
		Out:  &Leg{Chain: outChain, Dir: DirOut, Amount: "1"},
		In:   &Leg{Chain: inChain, Dir: DirIn, Amount: "1"},
	}
}

// Exactly one leg is the initiator's, and it is the one that must expire LAST.
// Getting this backwards is what lets a counterparty take both legs.
//
// In v1 this was a function of a role AND a direction, because the two legs were
// named after their chains. Naming them after what they do for this user makes
// it one comparison: the initiator's own funded leg is the long one, on whatever
// chain it lands.
func TestLegOwnership(t *testing.T) {
	for _, c := range []struct {
		name     string
		role     Role
		outChain ChainID
		inChain  ChainID
	}{
		{"btc out, initiator", RoleInitiator, ChainBTC, ChainZNN},
		{"btc in, participant", RoleParticipant, ChainZNN, ChainBTC},
		{"znn out, participant", RoleParticipant, ChainZNN, ChainBTC},
		{"sol out, initiator", RoleInitiator, ChainSOL, ChainZNN},
		{"znn out against znn in", RoleInitiator, ChainZNN, ChainZNN},
	} {
		sw := legs(c.role, c.outChain, c.inChain)
		if got := sw.OutIsInitiators(); got != (c.role == RoleInitiator) {
			t.Errorf("%s: OutIsInitiators = %v", c.name, got)
		}
		if sw.OutIsInitiators() == sw.InIsInitiators() {
			t.Errorf("%s: both legs claim to belong to the same party", c.name)
		}
	}
}

// The preimage reaches the participant on the leg they funded, whatever chain
// that is: the initiator claims the participant's leg, and claiming is what
// publishes the secret. The initiator invented it and learns nothing.
func TestSecretArrivesOnTheLegWeFunded(t *testing.T) {
	for _, chainID := range []ChainID{ChainBTC, ChainZNN, ChainSOL} {
		participant := legs(RoleParticipant, chainID, ChainZNN)
		if got := participant.SecretArrivesOn(); got != chainID {
			t.Errorf("participant funding %s learns the secret on %q, want %q",
				chainID, got, chainID)
		}
		initiator := legs(RoleInitiator, chainID, ChainZNN)
		if got := initiator.SecretArrivesOn(); got != "" {
			t.Errorf("the initiator was told to wait for a secret on %q", got)
		}
	}
}

// planOutLeg must put this user's own leg on the correct side of the
// counterparty's deadline in every shape, with a gap wide enough to satisfy the
// floor that verification later enforces.
func TestPlanOutLegOrdersTheLegs(t *testing.T) {
	now := time.Now().UTC()
	for _, c := range []struct {
		name    string
		role    Role
		theirs  time.Duration
		wantEnd string // "before" or "after" the counterparty's deadline
	}{
		{"they initiate, we go under them", RoleParticipant, 48 * time.Hour, "before"},
		{"we initiate, we go over them", RoleInitiator, 24 * time.Hour, "after"},
	} {
		sw := legs(c.role, ChainZNN, ChainBTC)
		sw.Out.Znn = &ZnnLeg{}
		sw.In.Btc = &BtcLeg{Contract: []byte{0x01}}
		sw.In.Expiry = now.Add(c.theirs).Unix()
		sw.planOutLeg(now)
		if sw.Out.Znn.ExpirationSeconds <= 0 {
			t.Errorf("%s: no expiry planned", c.name)
			continue
		}
		ours := now.Add(time.Duration(sw.Out.Znn.ExpirationSeconds) * time.Second)
		theirs := time.Unix(sw.In.Expiry, 0)
		gap := ours.Sub(theirs)
		if c.wantEnd == "before" && gap > -MinLegGap {
			t.Errorf("%s: ours expires %s relative to theirs, want at least %s earlier",
				c.name, gap, MinLegGap)
		}
		if c.wantEnd == "after" && gap < MinLegGap {
			t.Errorf("%s: ours expires %s relative to theirs, want at least %s later",
				c.name, gap, MinLegGap)
		}
		// The whole-hour form znn-cli takes must satisfy the same gap the
		// seconds form does, never merely land on the right side of it.
		hoursAt := now.Add(time.Duration(sw.Out.Znn.ExpirationHours) * time.Hour)
		hourGap := hoursAt.Sub(theirs)
		if c.wantEnd == "before" && hourGap > -MinLegGap {
			t.Errorf("%s: %dh leaves a gap of %s, want at least %s earlier",
				c.name, sw.Out.Znn.ExpirationHours, hourGap, MinLegGap)
		}
		if c.wantEnd == "after" && hourGap < MinLegGap {
			t.Errorf("%s: %dh leaves a gap of %s, want at least %s later",
				c.name, sw.Out.Znn.ExpirationHours, hourGap, MinLegGap)
		}
	}
}

// A leg already on a chain has a deadline that is a fact rather than a plan.
// Rewriting the advice under the user after that would be describing an entry
// that does not exist.
func TestPlanOutLegLeavesACommittedLegAlone(t *testing.T) {
	now := time.Now().UTC()
	sw := legs(RoleParticipant, ChainZNN, ChainBTC)
	sw.Out.Znn = &ZnnLeg{HtlcID: "abc"}
	sw.Out.Expiry = now.Add(3 * time.Hour).Unix()
	sw.In.Btc = &BtcLeg{Contract: []byte{0x01}}
	sw.In.Expiry = now.Add(48 * time.Hour).Unix()
	before := sw.Out.Expiry
	sw.planOutLeg(now)
	if sw.Out.Expiry != before {
		t.Error("re-planned a leg that is already on a chain")
	}
}

// The deadline the user typed has to survive planning. It was validated,
// written onto the leg, and then overwritten by the role's default on the very
// next line -- so the field on the create form did nothing at all, quietly.
func TestPlanOutLegKeepsTheHoursTheUserAsked(t *testing.T) {
	now := time.Now().UTC()
	sw := legs(RoleInitiator, ChainZNN, ChainBTC)
	sw.Out.Znn = &ZnnLeg{}
	sw.In.Btc = &BtcLeg{}
	sw.Out.LockHours = 6
	sw.Out.Expiry = now.Add(6 * time.Hour).Unix()
	sw.planOutLeg(now)
	if got := time.Unix(sw.Out.Expiry, 0).Sub(now).Round(time.Minute); got != 6*time.Hour {
		t.Errorf("asked for 6h, planned %s", got)
	}

	// The counterparty's leg, once it is a fact, still wins: the ordering
	// between the two legs is a safety rule and not a preference.
	sw.In.Expiry = now.Add(24 * time.Hour).Unix()
	sw.In.Btc = &BtcLeg{Contract: []byte{0x01}}
	sw.planOutLeg(now)
	if got := time.Unix(sw.Out.Expiry, 0); !got.After(time.Unix(sw.In.Expiry, 0)) {
		t.Errorf("initiator's leg ends %s, not after the participant's %s",
			got, time.Unix(sw.In.Expiry, 0))
	}
}

func TestPlanOutLegSkipsAnExpiringCounterpartyLeg(t *testing.T) {
	now := time.Now().UTC()
	// A counterparty leg only an hour out leaves no room beneath it.
	sw := legs(RoleParticipant, ChainZNN, ChainBTC)
	sw.Out.Znn = &ZnnLeg{}
	sw.In.Btc = &BtcLeg{Contract: []byte{0x01}}
	sw.In.Expiry = now.Add(time.Hour).Unix()
	sw.Out.Expiry = 0
	sw.planOutLeg(now)
	if sw.Out.Znn.ExpirationSeconds != 0 {
		t.Errorf("planned a %ds leg under one expiring in an hour",
			sw.Out.Znn.ExpirationSeconds)
	}
}

// auditLegOrdering is the last check before a user commits anything on the
// other chain, so both failure directions are pinned.
func TestAuditLegOrdering(t *testing.T) {
	now := time.Now()
	hours := func(n float64) time.Duration { return time.Duration(n * float64(time.Hour)) }

	t.Run("initiators leg must leave room for ours under it", func(t *testing.T) {
		sw := legs(RoleParticipant, ChainZNN, ChainBTC)
		if err := sw.auditLegOrdering(sw.In, now.Add(48*time.Hour).Unix()); err != nil {
			t.Errorf("rejected a healthy 48h initiator leg: %v", err)
		}
		if err := sw.auditLegOrdering(sw.In, now.Add(hours(3)).Unix()); err == nil {
			t.Error("accepted a 3h initiator leg, which cannot fit a gapped leg under it " +
				"that still leaves time to act")
		}
	})

	t.Run("participants leg must expire before ours", func(t *testing.T) {
		sw := legs(RoleInitiator, ChainZNN, ChainBTC)
		sw.Out.Znn = &ZnnLeg{Verified: true}
		sw.Out.Expiry = now.Add(48 * time.Hour).Unix()
		if err := sw.auditLegOrdering(sw.In, now.Add(24*time.Hour).Unix()); err != nil {
			t.Errorf("rejected a healthy 24h participant leg under a 48h one: %v", err)
		}
		// Their leg outliving ours is the exact shape that lets them reclaim
		// their side and still claim ours.
		if err := sw.auditLegOrdering(sw.In, now.Add(72*time.Hour).Unix()); err == nil {
			t.Error("accepted their leg outliving the initiator's own")
		}
		// A leg that merely eats into the safety gap must go too.
		if err := sw.auditLegOrdering(sw.In, now.Add(hours(47)).Unix()); err == nil {
			t.Error("accepted a leg inside the minimum gap")
		}
	})

	// An expiry copied off a leg that FAILED verification is not a fact about
	// this swap: verification is what establishes that the entry is the one the
	// swap is about. Treating it as one would let a counterparty park a decoy
	// with a comfortable expiry and have this check pass on its strength.
	t.Run("an unverified expiry is not trusted", func(t *testing.T) {
		sw := legs(RoleInitiator, ChainZNN, ChainBTC)
		sw.Out.Znn = &ZnnLeg{Verified: false}
		sw.Out.Expiry = now.Add(48 * time.Hour).Unix()
		if err := sw.auditLegOrdering(sw.In, now.Add(24*time.Hour).Unix()); err != nil {
			t.Errorf("errored rather than falling back to the could-not-check note: %v", err)
		}
		if len(sw.Events) == 0 {
			t.Error("passed silently on an unverified expiry; a skipped check must say so")
		}
	})

	t.Run("an almost-expired leg is refused whatever the shape", func(t *testing.T) {
		for _, role := range []Role{RoleInitiator, RoleParticipant} {
			sw := legs(role, ChainZNN, ChainBTC)
			if err := sw.auditLegOrdering(sw.In, now.Add(time.Minute).Unix()); err == nil {
				t.Errorf("%s accepted a leg expiring in a minute", role)
			}
		}
	})
}

// The verification bounds are the runtime half of the same rule, and applying
// the wrong one either rejects a valid swap or accepts a dangerous one.
func TestZenonVerifyExpiryBounds(t *testing.T) {
	now := time.Now().Unix()
	secs := func(d time.Duration) int64 { return int64(d / time.Second) }
	base := func() *znn.HtlcInfo {
		return &znn.HtlcInfo{
			HashLocked: "z1me", TimeLocked: "z1them",
			HashType: znn.HashTypeSHA256, KeyMaxSize: 32,
			Amount: "1000000000",
		}
	}
	c := &znn.Client{}

	t.Run("a participants leg may not outlive the initiators", func(t *testing.T) {
		info := base()
		info.ExpirationTime = now + secs(24*time.Hour)
		want := znn.VerifyParams{
			ExpectRecipient: "z1me", ExpectSender: "z1them",
			Now: now, MinRemaining: MinLegRemaining,
			MaxExpiration: now + secs(46*time.Hour),
		}
		if err := c.Verify(info, want); err != nil {
			t.Errorf("rejected a 24h participant leg under a 48h one: %v", err)
		}
		info.ExpirationTime = now + secs(50*time.Hour)
		if err := c.Verify(info, want); err == nil {
			t.Error("accepted a participant leg outliving the initiator's")
		}
	})

	t.Run("an initiators leg must outlive the participants", func(t *testing.T) {
		info := base()
		info.ExpirationTime = now + secs(48*time.Hour)
		want := znn.VerifyParams{
			ExpectRecipient: "z1me", ExpectSender: "z1them",
			Now: now, MinRemaining: MinLegRemaining,
			MinExpiration: now + secs(26*time.Hour),
		}
		if err := c.Verify(info, want); err != nil {
			t.Errorf("rejected a 48h initiator leg over a 24h one: %v", err)
		}
		info.ExpirationTime = now + secs(12*time.Hour)
		if err := c.Verify(info, want); err == nil {
			t.Error("accepted an initiator leg expiring before the participant's")
		}
	})

	t.Run("an htlc about to expire is refused", func(t *testing.T) {
		info := base()
		info.ExpirationTime = now + 60
		if err := c.Verify(info, znn.VerifyParams{Now: now, MinRemaining: MinLegRemaining}); err == nil {
			t.Error("accepted an HTLC expiring in a minute: the creator reclaims it the " +
				"moment the other leg is committed")
		}
	})

	t.Run("the chain clock is used, not the local one", func(t *testing.T) {
		info := base()
		// Comfortably ahead of the local clock but already behind the chain's,
		// which is the clock the contract actually compares against.
		info.ExpirationTime = now + secs(time.Hour)
		if err := c.Verify(info, znn.VerifyParams{
			Now: now + secs(2*time.Hour), MinRemaining: MinLegRemaining}); err == nil {
			t.Error("accepted an HTLC that is already expired by momentum time")
		}
	})
}

// The bound the manager selects is the actual blocker: picking MaxExpiration
// where MinExpiration belongs rejects a correctly built counterparty leg.
func TestZenonVerifyParamsPerShape(t *testing.T) {
	theirs := time.Now().Add(24 * time.Hour).Unix()
	gap := int64(MinLegGap / time.Second)

	for _, c := range []struct {
		name      string
		role      Role
		dir       Dir
		wantMin   int64
		wantMax   int64
		recipient string
		sender    string
	}{
		{"our leg, we initiate", RoleInitiator, DirOut, theirs + gap, 0, "z1them", "z1me"},
		{"our leg, they initiate", RoleParticipant, DirOut, 0, theirs - gap, "z1them", "z1me"},
		{"their leg, we initiate", RoleInitiator, DirIn, 0, theirs - gap, "z1me", "z1them"},
		{"their leg, they initiate", RoleParticipant, DirIn, theirs + gap, 0, "z1me", "z1them"},
	} {
		// The Zenon leg under test on one side, a verified counterparty leg on
		// the other so there is a deadline to be ordered against.
		sw := legs(c.role, ChainZNN, ChainZNN)
		sw.Out.Token, sw.In.Token = znn.ZnnTokenStandard, QsrTokenStandard
		sw.Out.Znn, sw.In.Znn = &ZnnLeg{}, &ZnnLeg{}
		l := sw.leg(c.dir)
		other := sw.other(l)
		l.SelfAddr, l.PeerAddr = "z1me", "z1them"
		other.Expiry = theirs
		other.Znn.Verified = true

		got := zenonVerifyParams(sw, l)
		if got.MinExpiration != c.wantMin || got.MaxExpiration != c.wantMax {
			t.Errorf("%s: bounds min=%d max=%d, want min=%d max=%d",
				c.name, got.MinExpiration, got.MaxExpiration, c.wantMin, c.wantMax)
		}
		if got.ExpectRecipient != c.recipient || got.ExpectSender != c.sender {
			t.Errorf("%s: parties recipient=%s sender=%s, want recipient=%s sender=%s",
				c.name, got.ExpectRecipient, got.ExpectSender, c.recipient, c.sender)
		}
		if got.MinRemaining != MinLegRemaining {
			t.Errorf("%s: MinRemaining not set, so an HTLC about to expire would pass", c.name)
		}
	}
}

// Before the counterparty's leg is verified, its deadline is a plan rather than
// a fact, so no ordering bound can be drawn from it.
func TestZenonVerifyParamsWithoutAVerifiedCounterpartyLeg(t *testing.T) {
	sw := legs(RoleInitiator, ChainZNN, ChainBTC)
	sw.Out.Znn = &ZnnLeg{}
	sw.In.Expiry = time.Now().Add(24 * time.Hour).Unix()
	got := zenonVerifyParams(sw, sw.Out)
	if got.MinExpiration != 0 || got.MaxExpiration != 0 {
		t.Errorf("bounded the expiry against an unverified leg: min=%d max=%d",
			got.MinExpiration, got.MaxExpiration)
	}
}

func TestZenonVerifyParties(t *testing.T) {
	c := &znn.Client{}
	info := &znn.HtlcInfo{
		HashLocked: "z1alice", TimeLocked: "z1bob",
		HashType: znn.HashTypeSHA256, KeyMaxSize: 32, Amount: "1",
	}
	if err := c.Verify(info, znn.VerifyParams{ExpectRecipient: "z1alice", ExpectSender: "z1bob"}); err != nil {
		t.Errorf("rejected an HTLC with the agreed parties: %v", err)
	}
	// Verifying an HTLC this user created themselves means checking it pays the
	// COUNTERPARTY. Expecting our own address there is the bug that made
	// self-verification fail every time.
	if err := c.Verify(info, znn.VerifyParams{ExpectRecipient: "z1bob"}); err == nil {
		t.Error("accepted an HTLC unlockable by the wrong party")
	}
	if err := c.Verify(info, znn.VerifyParams{ExpectSender: "z1alice"}); err == nil {
		t.Error("accepted an HTLC reclaimable by the wrong party")
	}
	if err := c.Verify(info, znn.VerifyParams{MinAmount: big.NewInt(2)}); err == nil {
		t.Error("accepted an underpaid HTLC")
	}
	sha3 := *info
	sha3.HashType = znn.HashTypeSHA3
	if err := c.Verify(&sha3, znn.VerifyParams{}); err == nil {
		t.Error("accepted a SHA3 HTLC, which no other leg can produce a preimage for")
	}
	small := *info
	small.KeyMaxSize = 16
	if err := c.Verify(&small, znn.VerifyParams{}); err == nil {
		t.Error("accepted an HTLC that cannot hold a 32-byte preimage")
	}
}

// The contract address is public, so anyone can pay it. Picking the wrong
// output means the pre-signed refund spends dust while the real funding sits
// stranded.
func TestPickFunding(t *testing.T) {
	const want = 400_000
	utxos := []chain.UTXO{
		{TxID: "dust", Vout: 0, Value: 600},
		{TxID: "real", Vout: 1, Value: want},
		{TxID: "spam", Vout: 0, Value: 1000},
	}
	if got := pickFunding(utxos, want); got == nil || got.TxID != "real" {
		t.Errorf("picked %+v, want the output covering the agreed amount", got)
	}
	// Nothing covers it: take the largest rather than whatever came first.
	short := []chain.UTXO{{TxID: "a", Value: 600}, {TxID: "b", Value: 90_000}}
	if got := pickFunding(short, want); got == nil || got.TxID != "b" {
		t.Errorf("picked %+v, want the largest of the short outputs", got)
	}
	// Two that both cover: take the larger.
	both := []chain.UTXO{{TxID: "a", Value: want}, {TxID: "b", Value: want + 1}}
	if got := pickFunding(both, want); got == nil || got.TxID != "b" {
		t.Errorf("picked %+v, want the larger covering output", got)
	}
	if pickFunding(nil, want) != nil {
		t.Error("found funding in an empty utxo set")
	}
	if pickFunding([]chain.UTXO{{TxID: "z", Value: 0}}, want) != nil {
		t.Error("treated a zero-value output as funding")
	}
}

func TestFundingIsOpen(t *testing.T) {
	leg := func(f *FundingOutput, claimed, reclaimed bool, redeemed *SpendResult) *Leg {
		return &Leg{
			Chain: ChainBTC, Dir: DirOut, Amount: "400000", Base: "400000",
			Btc: &BtcLeg{Funding: f, Claimed: claimed, Reclaimed: reclaimed, RedeemTx: redeemed},
		}
	}
	full := &FundingOutput{Value: 400_000}
	dust := &FundingOutput{Value: 600}
	cases := []struct {
		name string
		l    *Leg
		want bool
	}{
		{"nothing seen yet", leg(nil, false, false, nil), true},
		{"only dust so far", leg(dust, false, false, nil), true},
		{"fully funded", leg(full, false, false, nil), false},
		{"already claimed", leg(dust, true, false, nil), false},
		{"already reclaimed", leg(dust, false, true, nil), false},
		{"redeem broadcast", leg(dust, false, false, &SpendResult{}), false},
	}
	for _, c := range cases {
		if got := btcFundingIsOpen(c.l); got != c.want {
			t.Errorf("%s: btcFundingIsOpen = %v, want %v", c.name, got, c.want)
		}
	}
}

// A locktime below the BIP-65 threshold is read as a block height, not a
// timestamp, which would silently move the refund branch to a completely
// different moment from the one every message in the UI promises.
func TestContractRejectsNonTimestampLocktimes(t *testing.T) {
	refund, redeem := mustKey(t), mustKey(t)
	_, hash, _ := NewSecret()

	for _, bad := range []int64{0, 1, 800_000, LockTimeThreshold - 1, int64(MaxLockTime) + 1} {
		if _, err := BuildContract(refund.PKH, redeem.PKH, bad, hash); err == nil {
			t.Errorf("BuildContract accepted locktime %d", bad)
		}
	}
	good, err := BuildContract(refund.PKH, redeem.PKH, LockTimeThreshold, hash)
	if err != nil {
		t.Fatalf("BuildContract rejected the threshold value itself: %v", err)
	}
	if _, err := ParseContract(good); err != nil {
		t.Fatalf("ParseContract rejected a contract it just built: %v", err)
	}
}

// ParseContract must refuse a contract carrying a block-height locktime just as
// firmly as BuildContract refuses to make one: a counterparty is not obliged to
// use this tool to build theirs.
func TestParseRejectsBlockHeightLocktime(t *testing.T) {
	refund, redeem := mustKey(t), mustKey(t)
	_, hash, _ := NewSecret()
	script := buildTemplate(t, SecretSize, hash, redeem.PKH, 800_000, refund.PKH)
	if _, err := ParseContract(script); err == nil {
		t.Error("accepted a contract whose locktime is a block height")
	}
}

// Which list a swap belongs in is one question with one answer: the user filed
// it, or they did not. A swap that finishes stays on the Swaps page as a summary
// row until somebody archives it, so nothing leaves that page on its own.
func TestActiveVsHistory(t *testing.T) {
	settled := func() *Swap {
		sw := legs(RoleInitiator, ChainBTC, ChainZNN)
		sw.Out.Btc = &BtcLeg{Claimed: true}
		sw.In.Znn = &ZnnLeg{UnlockHash: "01"}
		return sw
	}
	cases := []struct {
		name string
		sw   *Swap
		want bool
	}{
		{"draft", legs(RoleInitiator, ChainBTC, ChainZNN), true},
		{"settled, not yet archived", settled(), true},
		{"archived by hand", func() *Swap {
			sw := legs(RoleInitiator, ChainBTC, ChainZNN)
			sw.Archived = true
			return sw
		}(), false},
		{"archived after settling", func() *Swap {
			sw := settled()
			sw.Archived = true
			return sw
		}(), false},
	}
	for _, c := range cases {
		if got := c.sw.Active(); got != c.want {
			t.Errorf("%s: Active() = %v, want %v", c.name, got, c.want)
		}
	}
}

// Settled is "over", not "filed": it decides whether the page shows a summary
// row or a working card, and whether the heartbeat keeps asking nodes about it.
//
// BOTH legs have to be done, which is the whole subtlety. On a one-leg view a
// claimed incoming leg looks like the end of the story; it is not, because the
// outgoing leg is still locked until the counterparty claims it, and until they
// do this browser is the only thing watching for the deadline.
func TestSettled(t *testing.T) {
	build := func(outDone, inDone bool) *Swap {
		sw := legs(RoleInitiator, ChainBTC, ChainZNN)
		sw.Out.Btc = &BtcLeg{Claimed: outDone}
		sw.In.Znn = &ZnnLeg{}
		if inDone {
			sw.In.Znn.UnlockHash = "01"
		}
		return sw
	}
	cases := []struct {
		name string
		sw   *Swap
		want bool
	}{
		{"nothing has happened", build(false, false), false},
		{"only our leg is done", build(true, false), false},
		{"only their leg is done", build(false, true), false},
		{"both legs done", build(true, true), true},
		{"our leg reclaimed and theirs collected", func() *Swap {
			sw := build(false, true)
			sw.Out.Btc.Reclaimed = true
			return sw
		}(), true},
		// A Solana leg ends the same way, through its own two exits.
		{"a solana leg redeemed", func() *Swap {
			sw := legs(RoleInitiator, ChainSOL, ChainZNN)
			sw.Out.Sol = &SolLeg{Refunded: true}
			sw.In.Znn = &ZnnLeg{UnlockHash: "01"}
			return sw
		}(), true},
	}
	for _, c := range cases {
		if got := c.sw.Settled(); got != c.want {
			t.Errorf("%s: Settled() = %v, want %v", c.name, got, c.want)
		}
	}
}

// A leg taken back by the side that funded it and a leg claimed by the side it
// was owed to both stop the money moving, and `legDone` cannot tell them apart.
// The view has to, because the card reads it: a refunded leg presented as a
// claimed one tells the person who just reclaimed their own money that the
// counterparty published a preimage, and sends them off to claim a leg nobody
// ever funded.
func TestReclaimedIsNotClaimed(t *testing.T) {
	sw := legs(RoleInitiator, ChainBTC, ChainZNN)
	sw.Out.Btc = &BtcLeg{
		Contract: []byte{0x01},
		Funding:  &FundingOutput{TxID: strings.Repeat("ab", 32), Value: 250000},
	}
	sw.In.Znn = &ZnnLeg{}

	sw.Out.Btc.Reclaimed = true
	sw.settle()
	if sw.State != StateRefunded {
		t.Errorf("a leg taken back with nothing funded against it left the swap %q, want %q",
			sw.State, StateRefunded)
	}
	v := legToView(sw, sw.Out)
	if !v.Done || !v.Reclaimed {
		t.Errorf("view says done=%v reclaimed=%v, want both true", v.Done, v.Reclaimed)
	}

	// The other exit is a claim, and must not be reported as a reclaim.
	sw.Out.Btc.Reclaimed, sw.Out.Btc.Claimed = false, true
	if v := legToView(sw, sw.Out); !v.Done || v.Reclaimed {
		t.Errorf("a claimed leg reports reclaimed=%v, want false", v.Reclaimed)
	}
}

// A deadline typed into the form has to survive the minutes between submitting
// it and pressing the funding button. Accepting exactly MinLegRemaining creates
// a swap that looks fine and then refuses its own funding, with a figure like
// "1h58m55s away, under the 2h0m0s minimum" -- so the floor carries headroom.
func TestCreateRefusesADeadlineWithNoRoomToFundIt(t *testing.T) {
	m := newManager(t, &stubBackend{feeRate: 2})
	floor := int(MinLegRemaining / time.Hour)
	for _, hours := range []int{1, floor} {
		p := znnZnn(RoleInitiator)
		p.LockHours = hours
		if _, err := m.Create(p); err == nil {
			t.Errorf("accepted a %dh leg; it would be under the %s floor before it could be funded",
				hours, MinLegRemaining)
		}
	}

	// One hour of room is enough, and is what a caller asking for the shortest
	// usable swap gets.
	p := znnZnn(RoleInitiator)
	p.LockHours = floor + 1
	sw, err := m.Create(p)
	if err != nil {
		t.Fatalf("refused the shortest usable leg: %v", err)
	}
	left := time.Until(time.Unix(sw.Out.Expiry, 0))
	if left <= MinLegRemaining {
		t.Errorf("a %dh leg has %s left, want more than %s", floor+1, left, MinLegRemaining)
	}
}

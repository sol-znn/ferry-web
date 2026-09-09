package main

import (
	"math/big"
	"testing"
	"time"

	"github.com/zenon/ferry-web/wasm/chain"
	"github.com/zenon/ferry-web/wasm/znn"
)

// The four shapes a swap can take. Exactly one leg is the initiator's, and it
// is the one that must expire LAST. Getting this backwards is what lets a
// counterparty take both legs, so every combination is pinned here rather than
// only the shape the end-to-end script happens to run.
func TestLegOwnership(t *testing.T) {
	cases := []struct {
		leg             Leg
		role            Role
		zenonIsInits    bool
		zenonHtlcIsOurs bool
	}{
		// Bitcoin initiates: we send BTC on the long leg, they lock ZNN short.
		{LegSend, RoleInitiator, false, false},
		// The mirror of that, seen from the other side.
		{LegReceive, RoleParticipant, false, true},
		// Zenon initiates: they lock ZNN long, we fund BTC short.
		{LegSend, RoleParticipant, true, false},
		// The mirror of that: we hold the secret and lock ZNN long.
		{LegReceive, RoleInitiator, true, true},
	}
	for _, c := range cases {
		sw := &Swap{Leg: c.leg, Role: c.role}
		if got := sw.ZenonLegIsInitiators(); got != c.zenonIsInits {
			t.Errorf("%s/%s: ZenonLegIsInitiators = %v, want %v", c.leg, c.role, got, c.zenonIsInits)
		}
		if got := sw.BitcoinLegIsInitiators(); got == c.zenonIsInits {
			t.Errorf("%s/%s: both legs claim to belong to the same party", c.leg, c.role)
		}
		if got := sw.ZenonHtlcIsOurs(); got != c.zenonHtlcIsOurs {
			t.Errorf("%s/%s: ZenonHtlcIsOurs = %v, want %v", c.leg, c.role, got, c.zenonHtlcIsOurs)
		}
	}
}

// planZenonLeg must put the Zenon HTLC on the correct side of the Bitcoin
// locktime in every shape, with a gap wide enough to satisfy the floor that
// verification later enforces.
func TestPlanZenonLegOrdersTheLegs(t *testing.T) {
	now := time.Now().UTC()
	for _, c := range []struct {
		name    string
		leg     Leg
		role    Role
		btcIn   time.Duration
		wantEnd string // "before" or "after" the Bitcoin locktime
	}{
		{"btc initiates, we send btc", LegSend, RoleInitiator, 48 * time.Hour, "before"},
		{"btc initiates, we receive btc", LegReceive, RoleParticipant, 48 * time.Hour, "before"},
		{"znn initiates, we send btc", LegSend, RoleParticipant, 24 * time.Hour, "after"},
		{"znn initiates, we receive btc", LegReceive, RoleInitiator, 24 * time.Hour, "after"},
	} {
		sw := &Swap{Leg: c.leg, Role: c.role, LockTime: now.Add(c.btcIn).Unix()}
		planZenonLeg(sw, now)
		if sw.Zenon.ExpirationSeconds <= 0 {
			t.Errorf("%s: no zenon expiry planned", c.name)
			continue
		}
		znnAt := now.Add(time.Duration(sw.Zenon.ExpirationSeconds) * time.Second)
		btcAt := time.Unix(sw.LockTime, 0)
		gap := znnAt.Sub(btcAt)
		if c.wantEnd == "before" && gap > -MinLegGap {
			t.Errorf("%s: zenon expires %s relative to bitcoin, want at least %s earlier",
				c.name, gap, MinLegGap)
		}
		if c.wantEnd == "after" && gap < MinLegGap {
			t.Errorf("%s: zenon expires %s relative to bitcoin, want at least %s later",
				c.name, gap, MinLegGap)
		}
		// The whole-hour form znn-cli takes must satisfy the same gap the
		// seconds form does, never merely land on the right side of it.
		hoursAt := now.Add(time.Duration(sw.Zenon.ExpirationHours) * time.Hour)
		hourGap := hoursAt.Sub(btcAt)
		if c.wantEnd == "before" && hourGap > -MinLegGap {
			t.Errorf("%s: %dh leaves a gap of %s, want at least %s before the bitcoin locktime",
				c.name, sw.Zenon.ExpirationHours, hourGap, MinLegGap)
		}
		if c.wantEnd == "after" && hourGap < MinLegGap {
			t.Errorf("%s: %dh leaves a gap of %s, want at least %s after the bitcoin locktime",
				c.name, sw.Zenon.ExpirationHours, hourGap, MinLegGap)
		}
		// A target one second under a round number should read as that round
		// number, not an hour short of it.
		if want := (sw.Zenon.ExpirationSeconds + 1800) / 3600; sw.Zenon.ExpirationHours != want {
			t.Errorf("%s: %ds rendered as %dh, want %dh",
				c.name, sw.Zenon.ExpirationSeconds, sw.Zenon.ExpirationHours, want)
		}
	}
}

func TestPlanZenonLegSkipsAnExpiringBitcoinLeg(t *testing.T) {
	now := time.Now().UTC()
	// A participant's Bitcoin leg only an hour out leaves no room beneath it.
	sw := &Swap{Leg: LegReceive, Role: RoleParticipant, LockTime: now.Add(time.Hour).Unix()}
	planZenonLeg(sw, now)
	if sw.Zenon.ExpirationSeconds != 0 {
		t.Errorf("planned a %ds zenon leg under a bitcoin leg expiring in an hour",
			sw.Zenon.ExpirationSeconds)
	}
}

// auditLegOrdering is the last check before a user commits anything on the
// other chain, so both failure directions are pinned.
func TestAuditLegOrdering(t *testing.T) {
	now := time.Now()
	hours := func(n float64) time.Duration { return time.Duration(n * float64(time.Hour)) }

	t.Run("initiators contract must leave room for our leg under it", func(t *testing.T) {
		sw := &Swap{Leg: LegReceive, Role: RoleParticipant}
		if err := auditLegOrdering(sw, now.Add(48*time.Hour).Unix(), 48*time.Hour); err != nil {
			t.Errorf("rejected a healthy 48h initiator contract: %v", err)
		}
		if err := auditLegOrdering(sw, now.Add(hours(3)).Unix(), hours(3)); err == nil {
			t.Error("accepted a 3h initiator contract, which cannot fit a gapped zenon leg " +
				"that still leaves time to act")
		}
	})

	t.Run("participants contract must expire before our zenon leg", func(t *testing.T) {
		sw := &Swap{Leg: LegReceive, Role: RoleInitiator}
		sw.Zenon.ExpirationTime = now.Add(48 * time.Hour).Unix()
		sw.Zenon.Verified = true
		if err := auditLegOrdering(sw, now.Add(24*time.Hour).Unix(), 24*time.Hour); err != nil {
			t.Errorf("rejected a healthy 24h participant contract under a 48h zenon leg: %v", err)
		}
		// Their Bitcoin leg outliving our Zenon leg is the exact shape that
		// lets them reclaim on Zenon and still redeem the Bitcoin.
		if err := auditLegOrdering(sw, now.Add(72*time.Hour).Unix(), 72*time.Hour); err == nil {
			t.Error("accepted a bitcoin contract outliving the initiators own zenon leg")
		}
		// A contract that merely eats into the safety gap must go too.
		if err := auditLegOrdering(sw, now.Add(hours(47)).Unix(), hours(47)); err == nil {
			t.Error("accepted a bitcoin contract inside the minimum leg gap")
		}
	})

	// An expiry copied off an HTLC that FAILED verification is not a fact about
	// this swap: verification is what establishes that the entry is the one the
	// swap is about. Treating it as one would let a counterparty park a decoy
	// HTLC with a comfortable expiry and have this check pass on its strength.
	t.Run("an unverified zenon expiry is not trusted", func(t *testing.T) {
		sw := &Swap{Leg: LegReceive, Role: RoleInitiator}
		sw.Zenon.ExpirationTime = now.Add(48 * time.Hour).Unix()
		sw.Zenon.Verified = false
		if err := auditLegOrdering(sw, now.Add(24*time.Hour).Unix(), 24*time.Hour); err != nil {
			t.Errorf("errored rather than falling back to the could-not-check note: %v", err)
		}
		if len(sw.Events) == 0 {
			t.Error("passed silently on an unverified expiry; a skipped check must say so")
		}
	})

	t.Run("an almost-expired contract is refused whatever the shape", func(t *testing.T) {
		for _, sw := range []*Swap{
			{Leg: LegReceive, Role: RoleParticipant},
			{Leg: LegReceive, Role: RoleInitiator},
		} {
			if err := auditLegOrdering(sw, now.Add(time.Minute).Unix(), time.Minute); err == nil {
				t.Errorf("%s accepted a contract expiring in a minute", sw.Role)
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

	t.Run("participants zenon leg may not outlive the bitcoin leg", func(t *testing.T) {
		info := base()
		info.ExpirationTime = now + secs(24*time.Hour)
		want := znn.VerifyParams{
			ExpectRecipient: "z1me", ExpectSender: "z1them",
			Now: now, MinRemaining: MinLegRemaining,
			MaxExpiration: now + secs(46*time.Hour),
		}
		if err := c.Verify(info, want); err != nil {
			t.Errorf("rejected a 24h participant leg under a 48h bitcoin leg: %v", err)
		}
		info.ExpirationTime = now + secs(50*time.Hour)
		if err := c.Verify(info, want); err == nil {
			t.Error("accepted a participant zenon leg outliving the bitcoin leg")
		}
	})

	t.Run("initiators zenon leg must outlive the bitcoin leg", func(t *testing.T) {
		info := base()
		info.ExpirationTime = now + secs(48*time.Hour)
		want := znn.VerifyParams{
			ExpectRecipient: "z1me", ExpectSender: "z1them",
			Now: now, MinRemaining: MinLegRemaining,
			MinExpiration: now + secs(26*time.Hour),
		}
		// This is the case the old "zenon always expires first" rule rejected,
		// which made every Zenon-initiated swap unverifiable.
		if err := c.Verify(info, want); err != nil {
			t.Errorf("rejected a 48h initiator leg over a 24h bitcoin leg: %v", err)
		}
		info.ExpirationTime = now + secs(12*time.Hour)
		if err := c.Verify(info, want); err == nil {
			t.Error("accepted an initiator zenon leg expiring before the bitcoin leg")
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

// The bound the manager selects is the actual blocker for a Zenon-initiated
// swap: picking MaxExpiration there rejects a correctly built counterparty
// HTLC, which is what stopped that direction working at all.
func TestZenonVerifyParamsPerShape(t *testing.T) {
	btc := time.Now().Add(24 * time.Hour).Unix()
	gap := int64(MinLegGap / time.Second)

	for _, c := range []struct {
		name      string
		leg       Leg
		role      Role
		wantMin   int64
		wantMax   int64
		recipient string
		sender    string
	}{
		{"btc initiates, we send btc", LegSend, RoleInitiator, 0, btc - gap, "z1me", "z1them"},
		{"btc initiates, we receive btc", LegReceive, RoleParticipant, 0, btc - gap, "z1them", "z1me"},
		{"znn initiates, we send btc", LegSend, RoleParticipant, btc + gap, 0, "z1me", "z1them"},
		{"znn initiates, we receive btc", LegReceive, RoleInitiator, btc + gap, 0, "z1them", "z1me"},
	} {
		sw := &Swap{
			Leg: c.leg, Role: c.role, LockTime: btc,
			Contract: []byte{0x01}, // any non-empty contract: the locktime is real
			Zenon:    ZenonLeg{SelfAddress: "z1me", PeerAddress: "z1them"},
		}
		got := zenonVerifyParams(sw)
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

// Before a contract exists the swap's locktime is a placeholder, so no ordering
// bound can be drawn from it.
func TestZenonVerifyParamsWithoutAContract(t *testing.T) {
	sw := &Swap{Leg: LegReceive, Role: RoleInitiator, LockTime: time.Now().Add(24 * time.Hour).Unix()}
	got := zenonVerifyParams(sw)
	if got.MinExpiration != 0 || got.MaxExpiration != 0 {
		t.Errorf("bounded the expiry against a placeholder locktime: min=%d max=%d",
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
		t.Error("accepted a SHA3 HTLC, which no Bitcoin-committed preimage can unlock")
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
	full := &FundingOutput{Value: 400_000}
	dust := &FundingOutput{Value: 600}
	cases := []struct {
		name string
		sw   *Swap
		want bool
	}{
		{"nothing seen yet", &Swap{AmountSats: 400_000}, true},
		{"only dust so far", &Swap{AmountSats: 400_000, Funding: dust, State: StateFunded}, true},
		{"fully funded", &Swap{AmountSats: 400_000, Funding: full, State: StateFunded}, false},
		{"already redeemed", &Swap{AmountSats: 400_000, Funding: dust, State: StateRedeemed}, false},
		{"already refunded", &Swap{AmountSats: 400_000, Funding: dust, State: StateRefunded}, false},
		{"redeem broadcast", &Swap{AmountSats: 400_000, Funding: dust, RedeemTx: &SpendResult{}}, false},
	}
	for _, c := range cases {
		if got := fundingIsOpen(c.sw); got != c.want {
			t.Errorf("%s: fundingIsOpen = %v, want %v", c.name, got, c.want)
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

// A gap too small for a whole-hour value on the correct side must produce no
// hour advice at all, rather than one that crosses the Bitcoin locktime.
func TestPlanZenonLegRefusesToRoundAcross(t *testing.T) {
	now := time.Now().UTC()
	// Participant leg with a 2h05m target: the seconds form is fine, but the
	// only whole hour that fits is 2, and rounding up to 3 would cross.
	sw := &Swap{
		Leg: LegReceive, Role: RoleParticipant,
		LockTime: now.Add(defaultLegGap + 2*time.Hour + 5*time.Minute).Unix(),
	}
	planZenonLeg(sw, now)
	if sw.Zenon.ExpirationSeconds <= 0 {
		t.Fatal("no zenon expiry planned for a leg that has room for one")
	}
	btcAt := time.Unix(sw.LockTime, 0)
	if h := sw.Zenon.ExpirationHours; h != 0 {
		if gap := now.Add(time.Duration(h) * time.Hour).Sub(btcAt); gap > -MinLegGap {
			t.Errorf("%dh leaves a gap of %s, want at least %s before the bitcoin locktime",
				h, gap, MinLegGap)
		}
	}
}

// Which list a swap belongs in is now one question with one answer: the user
// filed it, or they did not. A swap that finishes stays on the Swaps page as a
// summary row until somebody archives it, so nothing leaves that page on its
// own.
func TestActiveVsHistory(t *testing.T) {
	cases := []struct {
		name string
		sw   *Swap
		want bool
	}{
		{"draft", &Swap{State: StateDraft}, true},
		{"awaiting funding", &Swap{State: StateAwaitingFunding}, true},
		{"funded", &Swap{State: StateFunded}, true},
		{"expired, still refundable", &Swap{State: StateExpired}, true},

		// Terminal on Bitcoin is not filed away. These used to drop off the
		// active list the moment they settled, which is the behaviour this
		// replaces: a finished swap is a receipt, and the user dismisses it.
		{"refunded, not yet archived", &Swap{State: StateRefunded}, true},
		{"redeemed, not yet archived",
			&Swap{State: StateRedeemed, Leg: LegReceive, Role: RoleInitiator}, true},

		{"archived by hand", &Swap{State: StateFunded, Archived: true}, false},
		{"archived after settling", &Swap{State: StateRedeemed, Archived: true}, false},
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
// The exception is the case that would otherwise hide money: a contract this
// user funded, redeemed by the counterparty, when this user is the participant
// -- that redeem is what publishes the preimage, and claiming the ZNN with it
// has not happened yet.
func TestSettled(t *testing.T) {
	claimed := ZenonLeg{UnlockHash: "0102"}

	cases := []struct {
		name string
		sw   *Swap
		want bool
	}{
		{"draft", &Swap{State: StateDraft}, false},
		{"funded", &Swap{State: StateFunded}, false},
		{"expired, still refundable", &Swap{State: StateExpired}, false},
		{"refunded", &Swap{State: StateRefunded}, true},

		{"we sent BTC as initiator and they redeemed: our ZNN was already claimed",
			&Swap{State: StateRedeemed, Leg: LegSend, Role: RoleInitiator}, true},
		{"we sent BTC as participant and they redeemed: our ZNN claim starts now",
			&Swap{State: StateRedeemed, Leg: LegSend, Role: RoleParticipant}, false},
		{"we sent BTC as participant and have since claimed the ZNN",
			&Swap{State: StateRedeemed, Leg: LegSend, Role: RoleParticipant, Zenon: claimed}, true},
		{"we received BTC and redeemed it ourselves",
			&Swap{State: StateRedeemed, Leg: LegReceive, Role: RoleParticipant}, true},
		{"we received BTC as initiator and redeemed it",
			&Swap{State: StateRedeemed, Leg: LegReceive, Role: RoleInitiator}, true},

		// Archiving is filing, not finishing. A swap archived by hand halfway
		// through is not over, and the history row that shows it says so.
		{"archived mid-flight is still not settled",
			&Swap{State: StateFunded, Archived: true}, false},
	}
	for _, c := range cases {
		if got := c.sw.Settled(); got != c.want {
			t.Errorf("%s: Settled() = %v, want %v", c.name, got, c.want)
		}
	}
}

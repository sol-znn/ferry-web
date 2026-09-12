package main

import (
	"strings"
	"testing"
	"time"

	"github.com/zenon/ferry-web-v2/wasm/sol"
)

// escrowFor builds the escrow a correctly funded leg would produce, so each
// case below breaks exactly one field.
func escrowFor(t *testing.T, sw *Swap, l *Leg, now int64) *sol.Escrow {
	t.Helper()
	initiator, err := sol.ParsePubkey(l.SelfAddr)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := sol.ParsePubkey(l.PeerAddr)
	if err != nil {
		t.Fatal(err)
	}
	if l.Dir == DirIn {
		initiator, receiver = receiver, initiator
	}
	e := &sol.Escrow{
		Initiator: initiator,
		Receiver:  receiver,
		Amount:    l.Sol.Lamports,
		Timelock:  now + int64(48*time.Hour/time.Second),
	}
	copy(e.Hashlock[:], sw.SecretHash)
	return e
}

// An escrow that matches in four of five respects is not a four-fifths-safe
// swap; it is a swap to walk away from. Each case here is one thing a
// counterparty could publish that looks like the agreed leg and is not.
func TestCheckSolEscrowRefusesEveryMismatch(t *testing.T) {
	m := newManager(t, &stubBackend{feeRate: 2})
	sw := mustCreate(t, m, solZnn(RoleInitiator))
	l := sw.Out
	now := time.Now().Unix()

	if got := sw.checkSolEscrow(l, escrowFor(t, sw, l, now), now); len(got) != 0 {
		t.Fatalf("a correctly funded escrow was refused: %v", got)
	}

	other, err := sol.ParsePubkey("11111111111111111111111111111112")
	if err != nil {
		t.Fatal(err)
	}
	for name, breakIt := range map[string]func(*sol.Escrow){
		"pays somebody else":      func(e *sol.Escrow) { e.Receiver = other },
		"funded by somebody else": func(e *sol.Escrow) { e.Initiator = other },
		"holds less than agreed":  func(e *sol.Escrow) { e.Amount-- },
		"another hashlock":        func(e *sol.Escrow) { e.Hashlock[0] ^= 0xff },
		"already expired":         func(e *sol.Escrow) { e.Timelock = now - 1 },
		"about to expire":         func(e *sol.Escrow) { e.Timelock = now + 60 },
	} {
		e := escrowFor(t, sw, l, now)
		breakIt(e)
		if got := sw.checkSolEscrow(l, e, now); len(got) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The ordering rule, on a Solana leg, checked against a fact rather than a
// plan: the initiator's leg must outlive the participant's by MinLegGap.
func TestCheckSolEscrowEnforcesLegOrdering(t *testing.T) {
	m := newManager(t, &stubBackend{feeRate: 2})
	sw := mustCreate(t, m, solZnn(RoleInitiator))
	now := time.Now().Unix()

	// The counterparty's Zenon leg, verified, expiring in 24h.
	sw.In.Expiry = now + int64(24*time.Hour/time.Second)
	sw.In.Znn.Verified = true

	e := escrowFor(t, sw, sw.Out, now) // 48h: comfortably over
	if got := sw.checkSolEscrow(sw.Out, e, now); len(got) != 0 {
		t.Fatalf("refused a correctly ordered initiator leg: %v", got)
	}
	// Ours expiring first is the exact shape that lets the counterparty reclaim
	// their side and still claim ours.
	e.Timelock = now + int64(12*time.Hour/time.Second)
	if got := sw.checkSolEscrow(sw.Out, e, now); len(got) == 0 {
		t.Error("accepted an initiator leg expiring before the participant's")
	}
}

// The escrow address is recomputed from the program and the swap id, never read
// back off the record. Both sides derive the same account only if they agree
// about both, which is why an offer carries them.
func TestEscrowIsDerivedFromTheProgramAndSeed(t *testing.T) {
	m := newManager(t, &stubBackend{feeRate: 2})
	sw := mustCreate(t, m, solZnn(RoleInitiator))

	first, err := sw.Out.deriveEscrow()
	if err != nil {
		t.Fatalf("deriveEscrow: %v", err)
	}
	if first.String() != sw.Out.Sol.Escrow {
		t.Errorf("the stored escrow %q is not the derived one %s", sw.Out.Sol.Escrow, first)
	}
	// A different seed is a different account, and the difference is silent:
	// each side simply reports the other's leg as not funded.
	sw.Out.Sol.SwapID = strings.Repeat("11", 32)
	second, err := sw.Out.deriveEscrow()
	if err != nil {
		t.Fatalf("deriveEscrow: %v", err)
	}
	if second == first {
		t.Error("two different swap ids derive the same escrow")
	}
	// And so is a different deployment.
	sw.Out.Sol.SwapID = solSwapIDHex
	sw.Out.Sol.ProgramID = solSelfAddr
	third, err := sw.Out.deriveEscrow()
	if err != nil {
		t.Fatalf("deriveEscrow: %v", err)
	}
	if third == first {
		t.Error("two different program deployments derive the same escrow")
	}
}

// A create is this side's irreversible step, so the engine refuses it rather
// than trusting the page's own sequencing. The participant funds second.
func TestSolCreateRefusesOutOfTurn(t *testing.T) {
	m := newManager(t, &stubBackend{feeRate: 2})
	p := solZnn(RoleParticipant)
	p.SecretHashHex = strings.Repeat("ab", 32)
	sw := mustCreate(t, m, p)

	if err := sw.readyToFund(); err == nil {
		t.Fatal("the participant was cleared to fund with nothing on the other chain")
	}
	// The counterparty's leg exists but has not been checked. Funding against
	// one nobody has verified is the move this app will not help anybody make.
	sw.In.Znn.HtlcID = "aa"
	if err := sw.readyToFund(); err == nil {
		t.Fatal("cleared to fund against an unverified counterparty leg")
	}
	sw.In.Znn.Verified = true
	if err := sw.readyToFund(); err != nil {
		t.Fatalf("refused a properly ordered participant funding: %v", err)
	}

	// The initiator goes first by construction, and only needs somewhere to pay.
	first := mustCreate(t, m, solZnn(RoleInitiator))
	if err := first.readyToFund(); err != nil {
		t.Fatalf("the initiator was told to wait: %v", err)
	}
	first.Out.PeerAddr = ""
	if err := first.readyToFund(); err == nil {
		t.Fatal("cleared to lock lamports with nobody to lock them to")
	}
}

// A Solana amount is typed in SOL and held in lamports, and that conversion is
// the one place a factor of a billion can go missing.
func TestSolAmountConversion(t *testing.T) {
	for typed, want := range map[string]uint64{
		"1":           1_000_000_000,
		"1.5":         1_500_000_000,
		"0.000000001": 1,
		"0.5":         500_000_000,
	} {
		got, err := solLamports(typed)
		if err != nil {
			t.Errorf("%q: %v", typed, err)
			continue
		}
		if got != want {
			t.Errorf("%q is %d lamports, want %d", typed, got, want)
		}
		if back := solDisplay(got); normalDecimal(back) != normalDecimal(typed) {
			t.Errorf("%q rendered back as %q", typed, back)
		}
	}
	for _, bad := range []string{"", "-1", "0", "0.0000000001", "abc"} {
		if _, err := solLamports(bad); err == nil {
			t.Errorf("%q was accepted as an amount of SOL", bad)
		}
	}
}

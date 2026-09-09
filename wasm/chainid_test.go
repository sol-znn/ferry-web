package main

import (
	"context"
	"strings"
	"testing"
)

// The case this whole mechanism exists for: two people who both say "regtest"
// and are on unrelated chains. Nothing about the network name, the genesis
// block or the tip height distinguishes them — only a block hash does.
func TestDifferentChainsWithTheSameNameAreCaught(t *testing.T) {
	m := newManager(t, &stubBackend{feeRate: 2})

	theirs := &ChainFingerprint{
		Network:         "regtest",
		BtcHeight:       100,
		BtcAnchorHeight: 94,
		// Same height, same network name, different chain.
		BtcAnchorHash: strings.Repeat("ff", 32),
	}

	got := VerifyFingerprint(context.Background(), m, theirs)
	if got.Ok || got.SameChain {
		t.Fatalf("a different chain was accepted: %+v", got)
	}
	if len(got.Problems) == 0 {
		t.Fatal("no problem reported for a mismatched block hash")
	}
	joined := strings.Join(got.Problems, " ")
	for _, want := range []string{"different Bitcoin chains", "block 94", "invisible"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, joined)
		}
	}
}

// The matching case has to pass, or the check above is only breakage. The stub
// derives its hash from the height, so a peer reporting what our own node would
// report is a peer on the same chain.
func TestMatchingChainsAgree(t *testing.T) {
	m := newManager(t, &stubBackend{feeRate: 2})
	ctx := context.Background()

	mine := BuildFingerprint(ctx, m)
	if mine.BtcAnchorHash == "" {
		t.Fatalf("no anchor built: %+v", mine)
	}
	// The anchor sits the conventional six blocks below the stub's tip of 100.
	if mine.BtcAnchorHeight != 94 {
		t.Errorf("anchor height %d, want 94 (tip 100 less %d)", mine.BtcAnchorHeight, btcAnchorDepth)
	}

	got := VerifyFingerprint(ctx, m, mine)
	if !got.SameChain {
		t.Errorf("a peer on our own chain was not recognised: %+v", got)
	}
	if len(got.Problems) != 0 {
		t.Errorf("problems reported against our own chain: %v", got.Problems)
	}
	// No Zenon node is configured on this manager, so the Zenon leg is unknown
	// rather than agreed — and that must keep Ok false.
	if got.Ok {
		t.Error("Ok is true while the Zenon leg went unchecked; unknown must not read as agreement")
	}
	if len(got.Unchecked) == 0 {
		t.Error("the unchecked Zenon leg was not reported")
	}
}

// A network name mismatch is the cheap check, and it should short-circuit: once
// the two are on different networks, comparing block hashes between them is
// meaningless and the message should be about the networks.
func TestNetworkMismatchIsReportedPlainly(t *testing.T) {
	m := newManager(t, &stubBackend{feeRate: 2}) // regtest

	got := VerifyFingerprint(context.Background(), m, &ChainFingerprint{
		Network:         "mainnet",
		BtcAnchorHeight: 94,
		BtcAnchorHash:   strings.Repeat("ab", 32),
	})
	if got.Ok {
		t.Fatal("a mainnet peer was accepted by a regtest browser")
	}
	if len(got.Problems) != 1 {
		t.Fatalf("want exactly one problem about the networks, got %v", got.Problems)
	}
	if !strings.Contains(got.Problems[0], "mainnet") || !strings.Contains(got.Problems[0], "regtest") {
		t.Errorf("the message does not name both networks: %s", got.Problems[0])
	}
}

// A peer who sends nothing must not come back as agreement. "We could not
// check" and "we checked and it was fine" are the two answers this must never
// confuse, because only one of them is safe to fund against.
func TestMissingPeerDataIsUncheckedNotAgreed(t *testing.T) {
	m := newManager(t, &stubBackend{feeRate: 2})

	got := VerifyFingerprint(context.Background(), m, &ChainFingerprint{Network: "regtest"})
	if got.Ok || got.SameChain {
		t.Fatalf("a peer that proved nothing was treated as agreeing: %+v", got)
	}
	if len(got.Unchecked) == 0 {
		t.Fatal("nothing was reported as unchecked")
	}

	if nilPeer := VerifyFingerprint(context.Background(), m, nil); nilPeer.Ok {
		t.Error("a nil fingerprint was treated as agreement")
	}
}

// Two nodes on one chain drift apart by a block or two constantly; that is
// normal and must stay quiet. A large gap means somebody is not keeping up and
// is about to miss a funding, which is worth saying even though the chain
// itself agrees.
func TestLargeHeightDriftIsReportedOnTheSameChain(t *testing.T) {
	m := newManager(t, &stubBackend{feeRate: 2}) // tip 100
	ctx := context.Background()
	mine := BuildFingerprint(ctx, m)

	near := *mine
	near.BtcHeight = 100 - btcHeightDrift // still within tolerance
	if got := VerifyFingerprint(ctx, m, &near); len(got.Problems) != 0 {
		t.Errorf("normal drift was reported as a problem: %v", got.Problems)
	}

	far := *mine
	far.BtcHeight = 40
	got := VerifyFingerprint(ctx, m, &far)
	if !got.SameChain {
		t.Fatal("a lagging peer on the same chain should still be recognised as the same chain")
	}
	if len(got.Problems) == 0 {
		t.Fatal("a 60-block lag was not reported")
	}
	if !strings.Contains(strings.Join(got.Problems, " "), "blocks apart") {
		t.Errorf("the message does not describe the lag: %v", got.Problems)
	}
}

package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zenon/ferry-web-v2/wasm/znn"
)

// The Zenon leg: one entry in go-zenon's `htlc` embedded contract.
//
// A swap may have one Zenon leg, on either side, or two — a ZTS against a
// different ZTS is the one pair where both halves live on the same chain. That
// is why every function here takes the leg it is working on: "the Zenon HTLC"
// is no longer a thing a swap has exactly one of.

// zenonVerifyParams builds the expectations this swap has of one Zenon leg.
//
// Everything direction-dependent lives here, because each of these inverts
// between the two and getting one backwards either rejects a sound swap or
// accepts an unsound one:
//
//   - who may unlock, and who may reclaim, swap over with who created the entry;
//   - which side of the OTHER leg's deadline this expiry must fall on depends on
//     which leg is the initiator's, not on which way the coins move.
func zenonVerifyParams(sw *Swap, l *Leg) znn.VerifyParams {
	want := znn.VerifyParams{
		SecretHashHex: hex.EncodeToString(sw.SecretHash),
		MinRemaining:  MinLegRemaining,
		// The agreed token, never the one the entry happens to name. Checking
		// the amount without it means checking that some quantity of SOMETHING
		// is locked, which a counterparty satisfies by issuing their own token.
		ExpectTokenStandard: l.AgreedToken(),
	}
	// An out leg is one this user created: it must pay the COUNTERPARTY, and be
	// reclaimable by this user. An in leg is the mirror. Checking "recipient is
	// me" against an entry this user created would fail every time, and would
	// skip the check that actually matters there.
	if l.Dir == DirOut {
		want.ExpectRecipient = l.PeerAddr
		want.ExpectSender = l.SelfAddr
	} else {
		want.ExpectRecipient = l.SelfAddr
		want.ExpectSender = l.PeerAddr
	}
	// The initiator's leg must expire LAST, or one party can wait out a chain and
	// still act on the other. Only meaningful once the other leg's deadline is a
	// fact rather than a plan.
	if other := sw.other(l); other != nil && other.Expiry > 0 && other.Verified() {
		gap := int64(MinLegGap / time.Second)
		if sw.legIsInitiators(l.Dir) {
			want.MinExpiration = other.Expiry + gap
		} else {
			want.MaxExpiration = other.Expiry - gap
		}
	}
	return want
}

// zenonExpectations is zenonVerifyParams with the two things only a node can
// supply filled in: the chain's own clock, and the agreed amount in base units.
//
// One function rather than a step inside VerifyZenon because the search below
// verifies candidates through exactly the same expectations — a shortlist held
// to laxer terms would be a way of accepting an HTLC by not typing it in.
func (m *Manager) zenonExpectations(ctx context.Context, sw *Swap, l *Leg) znn.VerifyParams {
	want := zenonVerifyParams(sw, l)
	// The contract compares expirationTime against momentum time, so the expiry
	// checks have to use the chain's clock rather than this machine's.
	if mom, merr := m.Znn.FrontierMomentum(ctx); merr == nil {
		want.Now = mom.Timestamp
	} else {
		want.Now = time.Now().Unix()
		sw.logOnce(fmt.Sprintf("could not read the Zenon frontier momentum (%v); using local "+
			"time for the expiry check", merr))
	}
	// Convert with the decimals of the token that was AGREED, not the one the
	// entry names — that is the counterparty's choice, and can be a token they
	// issued this morning. Every failure sets AmountUncheckable rather than
	// leaving MinAmount nil, so a comparison that did not run cannot come back as
	// a matching amount.
	if strings.TrimSpace(l.Amount) != "" {
		agreed := l.AgreedToken()
		if tok, terr := m.Znn.GetToken(ctx, agreed); terr != nil {
			want.AmountUncheckable = fmt.Sprintf("could not read token %s from the node: %v",
				agreed, terr)
		} else if amt, cerr := baseUnits(l.Amount, tok.Decimals); cerr != nil {
			want.AmountUncheckable = fmt.Sprintf("the agreed amount %q could not be interpreted: %v",
				l.Amount, cerr)
		} else {
			want.MinAmount = amt
			l.Base = amt.String()
		}
	}
	return want
}

// VerifyZenon fetches one of this swap's Zenon entries — the counterparty's or
// this user's own — and checks it against the terms the swap committed to.
//
// This is the check most worth running against a node you picked yourself: it is
// the one place a lying node could tell you an unsafe entry is fine.
func (m *Manager) VerifyZenon(ctx context.Context, id string, dir Dir, htlcID string) (
	*Swap, *znn.HtlcInfo, error) {

	client := m.Znn
	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, nil, err
	}
	l, err := sw.znnLeg(dir)
	if err != nil {
		return nil, nil, err
	}
	if client == nil {
		return nil, nil, errNoZenonNode
	}
	htlcID = strings.TrimSpace(htlcID)
	if htlcID == "" {
		return nil, nil, errors.New("a Zenon HTLC id is required")
	}
	info, err := client.GetHtlcByID(ctx, htlcID)
	if err != nil {
		// "data non existent" is two opposite pieces of news wearing one error.
		// The contract deletes an entry when it is unlocked or reclaimed, so a
		// missing id means either that it was never there, or that the swap
		// worked. The ledger can tell them apart, because the id is the hash of
		// the transaction that created it.
		fate := client.WhatHappenedTo(ctx, htlcID)
		if fate.Existed && len(sw.Secret) == 0 && sw.SecretArrivesOn() == ChainZNN &&
			l.Dir == DirOut {
			if secret, ferr := client.FindPreimage(ctx, l.unlockPayee(), sw.SecretHash,
				preimageSearchPages, preimageSearchPageSize); ferr == nil {
				sw.Secret = secret
				l.Znn.HtlcID = htlcID
				l.Znn.UnlockSeen = true
				sw.log("Zenon HTLC %s is gone from the contract because it was unlocked; the "+
					"preimage was read out of that transaction and checked against this swap's "+
					"hash: %s", htlcID, hex.EncodeToString(secret))
				sw.settle()
				if serr := m.Store.Save(sw); serr != nil {
					return nil, nil, serr
				}
				return sw, nil, fmt.Errorf("%s The preimage has been recovered and saved to this "+
					"swap — you can claim your incoming leg now.", fate.Explanation)
			}
		}
		return nil, nil, fmt.Errorf("%w. %s", err, fate.Explanation)
	}

	want := m.zenonExpectations(ctx, sw, l)
	want.ExpectID = htlcID

	verr := client.Verify(info, want)

	// Whether this answer is news, decided BEFORE the record is updated with it.
	// A leg awaiting a verdict is re-read on a timer and one arriving over a
	// session is verified on every delivery, so without this a swap accumulates a
	// page of the event that happened once.
	repeat := l.Znn.HtlcID == htlcID &&
		l.Znn.Verified == (verr == nil) &&
		(verr == nil || l.Znn.VerifyError == verr.Error())

	// What the entry says is recorded for display either way — the card needs it
	// to explain a rejection — but the agreed terms are never overwritten by the
	// observed ones.
	l.Znn.HtlcID = htlcID
	l.Znn.ObservedToken = info.TokenStandard
	l.Znn.ObservedHashLocked = info.HashLocked
	l.Expiry = info.ExpirationTime
	l.Znn.HashType = info.HashType
	l.Znn.KeyMaxSize = info.KeyMaxSize

	// The entry was read off the chain, so a verdict about it replaces a pending
	// one — unless the refusal was made entirely of checks that could not be RUN,
	// which is not a verdict at all. That distinction is load-bearing well beyond
	// the badge on the card: `verified` is what releases the HTLC id to the
	// counterparty over a session, and what lets autopilot act.
	l.Znn.VerifyPending = verr != nil && znn.CheckIncomplete(verr)
	if verr != nil {
		l.Znn.Verified = false
		l.Znn.VerifyError = verr.Error()
		if !repeat {
			sw.log("Zenon HTLC %s FAILED verification: %v", htlcID, verr)
		}
	} else {
		l.Znn.Verified = true
		l.Znn.VerifyError = ""
		if !repeat {
			sw.log("Zenon HTLC %s verified: hashlock, parties, token, amount and expiry all "+
				"match (it is the %s leg)", htlcID, legOwner(sw.legIsInitiators(l.Dir)))
		}
		if sw.State == StateDraft || sw.State == StateAwaiting {
			sw.State = StateFunded
		}
	}
	sw.planOutLeg(time.Now().UTC())
	if err := m.Store.Save(sw); err != nil {
		return nil, nil, err
	}
	return sw, info, verr
}

// How far back to look for the create this swap is waiting for. A counterparty
// creates their HTLC as part of the swap in progress, so it is among the most
// recent things their address has done.
const (
	htlcSearchPages    = 10
	htlcSearchPageSize = 50
)

// FindZenonHtlc locates one of this swap's Zenon entries without anybody typing
// its id.
//
// The id is the hash of the transaction that created the entry, so it cannot be
// derived — but it can be found: the creating address is known, every create is
// a send to the HTLC contract, and its arguments carry the 32-byte hashlock
// verbatim. Every candidate then goes through the same Verify as an id typed in
// by hand, and a failure keeps its reason.
func (m *Manager) FindZenonHtlc(ctx context.Context, id string, dir Dir) (
	*Swap, *znn.HtlcInfo, error) {

	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, nil, err
	}
	l, err := sw.znnLeg(dir)
	if err != nil {
		return nil, nil, err
	}
	client := m.Znn
	if client == nil {
		return nil, nil, errNoZenonNode
	}
	// Whoever funds the leg creates the entry.
	creator := l.PeerAddr
	if l.Dir == DirOut {
		creator = l.SelfAddr
	}
	if strings.TrimSpace(creator) == "" {
		return nil, nil, fmt.Errorf("this swap does not record the Zenon address that creates the "+
			"%s HTLC, so there is no chain to search. Paste the id in by hand instead", dir)
	}

	candidates, err := client.FindHtlcCreates(ctx, creator, sw.SecretHash,
		htlcSearchPages, htlcSearchPageSize)
	if err != nil {
		return nil, nil, fmt.Errorf("could not read %s's chain from the node: %w", creator, err)
	}
	if len(candidates) == 0 {
		return nil, nil, fmt.Errorf("nothing in the last %d transactions from %s creates an HTLC "+
			"locked to this swap's hash. Either it has not been created yet, it is not confirmed "+
			"yet — a new one takes a couple of momentums — or it was made from a different "+
			"address than the one this swap records",
			htlcSearchPages*htlcSearchPageSize, creator)
	}

	want := m.zenonExpectations(ctx, sw, l)
	var rejected []string
	for _, c := range candidates {
		info, gerr := client.GetHtlcByID(ctx, c.ID)
		if gerr != nil {
			rejected = append(rejected, fmt.Sprintf("%s is no longer in the contract", c.ID))
			continue
		}
		w := want
		w.ExpectID = c.ID
		if verr := client.Verify(info, w); verr != nil {
			rejected = append(rejected, fmt.Sprintf("%s: %v", c.ID, verr))
			continue
		}
		// Found and checked. Going through VerifyZenon rather than recording it
		// here is deliberate: that is the one place that writes what an entry
		// says onto the swap, and having two would be two chances to disagree.
		return m.VerifyZenon(ctx, id, dir, c.ID)
	}

	return nil, nil, fmt.Errorf("found %d HTLC%s locked to this swap's hash from %s, and none of "+
		"them matches the agreed terms — %s",
		len(candidates), plural(len(candidates)), creator, strings.Join(rejected, "; "))
}

// How far back to look for a counterparty's unlock. An unlock is one of the last
// things the unlocking address does in a swap, so it is near the top of their
// chain; ten pages of fifty survives a busy account without turning a refresh
// into a ledger crawl.
const (
	preimageSearchPages    = 10
	preimageSearchPageSize = 50
)

// noPreimageYetNote is logged once, because before the counterparty claims, not
// finding a preimage is the swap working normally rather than a problem.
const noPreimageYetNote = "watching for the claim that reveals the preimage; it will be picked " +
	"up here automatically, and can still be pasted in by hand"

var errNoZenonNode = errors.New("no Zenon node is set. Open Node settings and give this browser " +
	"a Zenon JSON-RPC URL it can reach")

// refreshZnn re-reads one Zenon leg: the verdict on an entry that has one, and
// the preimage out of a counterparty's unlock where that is where it surfaces.
func (m *Manager) refreshZnn(ctx context.Context, sw *Swap, l *Leg) {
	if m.Znn == nil || l.Znn == nil {
		return
	}
	// A leg whose verdict never arrived is re-read: an entry published a moment
	// ago is not readable yet, and a node's bad minute is not an answer.
	if l.Znn.HtlcID != "" && (!l.Znn.Verified || l.Znn.VerifyPending) {
		if _, _, err := m.VerifyZenon(ctx, sw.ID, l.Dir, l.Znn.HtlcID); err == nil {
			if fresh, lerr := m.Store.Load(sw.ID); lerr == nil {
				*sw = *fresh
				l = sw.leg(l.Dir)
			}
		}
	}
	// Pick the preimage up off the chain, for the shape that cannot get it
	// anywhere else: this user funded this leg and the counterparty unlocks it.
	// Unlocking DELETES the entry, but the transaction that did it is in the
	// ledger forever.
	if l.Dir == DirOut && len(sw.Secret) == 0 && sw.SecretArrivesOn() == ChainZNN {
		if secret, ferr := m.Znn.FindPreimage(ctx, l.unlockPayee(), sw.SecretHash,
			preimageSearchPages, preimageSearchPageSize); ferr == nil {
			sw.Secret = secret
			// Finding it IS the observation that the leg was unlocked — nothing
			// else puts a matching preimage in a block sent to the contract.
			l.Znn.UnlockSeen = true
			sw.log("preimage recovered from the counterparty's Zenon unlock and checked against "+
				"this swap's hash: %s", hex.EncodeToString(secret))
		} else {
			sw.logOnce(noPreimageYetNote)
		}
	}
	// An out leg whose entry is gone and which this browser never unlocked is
	// one the counterparty took. Read once the preimage is known, so this cannot
	// be confused with an entry that was never published.
	if l.Dir == DirOut && l.Znn.HtlcID != "" && !l.Znn.UnlockSeen && l.Znn.ReclaimHash == "" &&
		len(sw.Secret) > 0 {
		if _, err := m.Znn.GetHtlcByID(ctx, l.Znn.HtlcID); err != nil {
			fate := m.Znn.WhatHappenedTo(ctx, l.Znn.HtlcID)
			if fate.Existed {
				l.Znn.UnlockSeen = true
				sw.log("the %s Zenon HTLC is no longer in the contract and the preimage is known, "+
					"so the counterparty collected it", l.Dir)
			}
		}
	}
}

// plural is the "s" on a count, so a message can name one candidate or five
// without reading like a template.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

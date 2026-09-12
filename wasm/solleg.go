package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zenon/ferry-web-v2/wasm/sol"
)

// The Solana leg: one escrow account of the ferry-htlc program.
//
// Bitcoin needs no program for this — a P2SH script IS the contract — and Zenon
// has one built into the protocol. Solana has neither, so the contract is a
// deployed program and the escrow is a PDA it owns. What survives the
// difference is the two properties the other two legs have:
//
//   - Both exits pay an address fixed at creation. No instruction takes a
//     destination, so no caller — this page included — can redirect the money.
//   - Neither exit needs a signature from the party being paid. The preimage
//     authorises one and an expired clock the other, so whoever is watching can
//     finish a swap the other side has walked away from.
//
// Everything below decides what an escrow must contain. Assembling and signing
// the transaction happens in the page, where the wallet ecosystem lives — see
// ui/src/core/solana.ts.

// solRedeemFeeLamports is a generous allowance for the transaction fee a claim
// or a reclaim costs. Solana's base fee is 5,000 lamports per signature; this is
// what the card quotes so "you will need a little SOL for fees" is a number.
const solRedeemFeeLamports uint64 = 10_000

// solLeg returns the swap's Solana leg on one side, or an error naming what the
// swap actually has.
func (s *Swap) solLeg(dir Dir) (*Leg, error) {
	l := s.leg(dir)
	if l == nil || l.Chain != ChainSOL || l.Sol == nil {
		return nil, fmt.Errorf("this swap has no %s Solana leg (it is %s)", dir, s.Pair())
	}
	return l, nil
}

// znnLeg is the same for Zenon. Two Zenon legs are possible, which is exactly
// why the direction is a parameter rather than something to search for.
func (s *Swap) znnLeg(dir Dir) (*Leg, error) {
	l := s.leg(dir)
	if l == nil || l.Chain != ChainZNN || l.Znn == nil {
		return nil, fmt.Errorf("this swap has no %s Zenon leg (it is %s)", dir, s.Pair())
	}
	return l, nil
}

// deriveEscrow recomputes this leg's escrow account from the program and the
// swap id.
//
// Recomputed rather than read back off the record, every time. The address is
// what the balance check and the verification are aimed at, so taking it from
// storage — or worse, from the page — would be checking this browser against
// itself. It is two hashes; there is no reason to cache it.
func (l *Leg) deriveEscrow() (sol.Pubkey, error) {
	programID, err := l.ProgramKey()
	if err != nil {
		return sol.Pubkey{}, err
	}
	swapID, err := l.SwapIDBytes()
	if err != nil {
		return sol.Pubkey{}, err
	}
	addr, _, err := sol.EscrowAddress(programID, swapID)
	return addr, err
}

// SolInstruction builds one Solana instruction for this swap: the create that
// funds an outgoing leg, the redeem that claims an incoming one, or the refund
// that takes an outgoing one back.
//
// Nothing here signs, and nothing here sends. What comes back is program id,
// accounts and data — the page assembles a transaction and hands it to the
// user's wallet, which shows it and signs it.
func (m *Manager) SolInstruction(ctx context.Context, id, action, feePayer string) (
	*sol.Instruction, *Swap, error) {

	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, nil, err
	}
	if m.Sol == nil {
		return nil, nil, errNoSolanaNode
	}
	switch action {
	case "create":
		l, err := sw.solLeg(DirOut)
		if err != nil {
			return nil, nil, err
		}
		ix, err := m.buildSolCreate(ctx, sw, l, feePayer)
		return ix, sw, err
	case "redeem":
		l, err := sw.solLeg(DirIn)
		if err != nil {
			return nil, nil, err
		}
		ix, err := m.buildSolRedeem(ctx, sw, l)
		return ix, sw, err
	case "refund":
		l, err := sw.solLeg(DirOut)
		if err != nil {
			return nil, nil, err
		}
		ix, err := m.buildSolRefund(ctx, l)
		return ix, sw, err
	}
	return nil, nil, fmt.Errorf("unknown Solana action %q: it is create, redeem or refund", action)
}

// buildSolCreate is this side's irreversible step, so every check it can make is
// made before the wallet is asked for anything.
func (m *Manager) buildSolCreate(ctx context.Context, sw *Swap, l *Leg, feePayer string) (
	*sol.Instruction, error) {

	if l.Sol.Funded || l.Sol.CreateSig != "" {
		return nil, fmt.Errorf("this swap's Solana escrow has already been funded (%s). A second "+
			"create would lock a second lot of SOL that only expiry can return",
			orElse(l.Sol.CreateSig, l.Sol.Escrow))
	}
	programID, err := l.ProgramKey()
	if err != nil {
		return nil, err
	}
	swapID, err := l.SwapIDBytes()
	if err != nil {
		return nil, err
	}
	// The escrow's initiator is the account that pays, and it is the ONLY
	// address a refund can reach. So the wallet the page has connected has to be
	// the one this swap recorded, and a mismatch is refused rather than adopted:
	// adopting it would silently point the refund at whichever account happened
	// to be selected.
	sender, err := sol.ParsePubkey(strings.TrimSpace(l.SelfAddr))
	if err != nil {
		return nil, fmt.Errorf("this swap's own Solana address: %w", err)
	}
	if fp := strings.TrimSpace(feePayer); fp != "" && fp != sender.String() {
		return nil, fmt.Errorf("this swap funds its Solana escrow from %s, and the connected "+
			"wallet is %s. Only the funding account can ever reclaim it, so switch accounts in "+
			"the wallet rather than funding from this one", sender, fp)
	}
	receiver, err := sol.ParsePubkey(strings.TrimSpace(l.PeerAddr))
	if err != nil {
		return nil, fmt.Errorf("the counterparty's Solana address: %w", err)
	}
	if len(sw.SecretHash) != 32 {
		return nil, errors.New("this swap has no secret hash yet")
	}
	if l.Expiry == 0 {
		return nil, errors.New("this leg has no deadline yet")
	}
	// The counterparty's leg has to be there and checked first. The engine
	// refuses regardless of what the page offered: obeying the page's own
	// sequencing would make the rule a suggestion.
	if err := sw.readyToFund(); err != nil {
		return nil, err
	}
	now, err := m.Sol.Now(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the Solana cluster clock: %w", err)
	}
	// A create whose timelock is already behind the cluster clock is refused on
	// chain. Saying so here costs the user a fee they would otherwise pay to
	// learn it.
	if l.Expiry <= now {
		return nil, fmt.Errorf("this swap's Solana deadline passed at %s; it can no longer be "+
			"funded", utcTime(l.Expiry))
	}
	if l.Expiry-now < int64(MinLegRemaining/time.Second) {
		return nil, fmt.Errorf("this swap's Solana deadline is %s away, under the %s minimum "+
			"worth locking money behind",
			time.Duration(l.Expiry-now)*time.Second, MinLegRemaining)
	}
	var hashlock [32]byte
	copy(hashlock[:], sw.SecretHash)
	return sol.BuildCreate(sol.CreateParams{
		ProgramID: programID,
		SwapID:    swapID,
		Initiator: sender,
		Receiver:  receiver,
		Amount:    l.Sol.Lamports,
		Hashlock:  hashlock,
		Timelock:  l.Expiry,
	})
}

func (m *Manager) buildSolRedeem(ctx context.Context, sw *Swap, l *Leg) (*sol.Instruction, error) {
	if len(sw.Secret) == 0 {
		return nil, errors.New("the secret is not known yet, so there is nothing to redeem with")
	}
	programID, err := l.ProgramKey()
	if err != nil {
		return nil, err
	}
	swapID, err := l.SwapIDBytes()
	if err != nil {
		return nil, err
	}
	escrow, addr, err := m.Sol.Escrow(ctx, programID, swapID)
	if err != nil {
		return nil, err
	}
	return sol.BuildRedeem(programID, escrow, addr, sw.Secret)
}

func (m *Manager) buildSolRefund(ctx context.Context, l *Leg) (*sol.Instruction, error) {
	programID, err := l.ProgramKey()
	if err != nil {
		return nil, err
	}
	swapID, err := l.SwapIDBytes()
	if err != nil {
		return nil, err
	}
	escrow, addr, err := m.Sol.Escrow(ctx, programID, swapID)
	if err != nil {
		return nil, err
	}
	now, err := m.Sol.Now(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the Solana cluster clock: %w", err)
	}
	if now < escrow.Timelock {
		return nil, fmt.Errorf("the escrow becomes refundable at %s, in %s — not yet",
			utcTime(escrow.Timelock), time.Duration(escrow.Timelock-now)*time.Second)
	}
	return sol.BuildRefund(programID, escrow, addr)
}

// RecordSolTx notes a signature the page obtained from a wallet, so a refresh
// that has not yet seen the transaction land does not re-propose the same step.
//
// A signature is not evidence that anything settled: a transaction can be
// dropped, and one that landed can have failed. Refresh asks the cluster.
func (m *Manager) RecordSolTx(id, action, signature string) (*Swap, error) {
	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, err
	}
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return nil, errors.New("a transaction signature is required")
	}
	if _, err := sol.DecodeBase58(signature); err != nil {
		return nil, fmt.Errorf("%q is not a Solana signature: %w", signature, err)
	}
	var l *Leg
	switch action {
	case "create", "refund":
		l, err = sw.solLeg(DirOut)
	case "redeem":
		l, err = sw.solLeg(DirIn)
	default:
		return nil, fmt.Errorf("unknown Solana action %q", action)
	}
	if err != nil {
		return nil, err
	}
	switch action {
	case "create":
		l.Sol.CreateSig = signature
		sw.log("Solana escrow create submitted as %s; waiting for the cluster to confirm it",
			signature)
	case "redeem":
		l.Sol.RedeemSig = signature
		sw.log("Solana redeem submitted as %s", signature)
	case "refund":
		l.Sol.RefundSig = signature
		sw.log("Solana refund submitted as %s", signature)
	}
	if err := m.Store.Save(sw); err != nil {
		return nil, err
	}
	return sw, nil
}

// escrowScanLimit is how many signatures back a settled escrow is looked for.
//
// Wider than the number of transactions a swap needs, because failed attempts
// sit in the same history: a wrong preimage leaves one, and so does a decoy
// somebody published to be found here.
const escrowScanLimit = 50

// refreshSol re-reads one Solana leg: whether the escrow exists, whether it
// matches what was agreed, and — once it is gone — which exit took it.
func (m *Manager) refreshSol(ctx context.Context, sw *Swap, l *Leg) {
	if m.Sol == nil || l.Sol == nil {
		return
	}
	programID, err := l.ProgramKey()
	if err != nil {
		l.Sol.VerifyError = err.Error()
		l.Sol.VerifyPending = true
		return
	}
	swapID, err := l.SwapIDBytes()
	if err != nil {
		l.Sol.VerifyError = err.Error()
		return
	}
	addr, derr := l.deriveEscrow()
	if derr == nil {
		l.Sol.Escrow = addr.String()
	}

	escrow, _, err := m.Sol.Escrow(ctx, programID, swapID)
	if errors.Is(err, sol.ErrNoAccount) {
		m.readSolOutcome(ctx, sw, l, programID, addr)
		return
	}
	if err != nil {
		// Unreadable is not a verdict. A leg that has never been read stays
		// pending so the timer keeps asking; one already verified keeps its
		// verdict rather than losing it to a node's bad minute.
		l.Sol.VerifyError = err.Error()
		l.Sol.VerifyPending = !l.Sol.Verified
		return
	}

	first := !l.Sol.Funded
	l.Sol.Funded = true
	l.Sol.ObservedAmount = escrow.Amount
	l.Sol.ObservedLamports = escrow.Lamports
	l.Sol.ObservedInitiator = escrow.Initiator.String()
	l.Sol.ObservedReceiver = escrow.Receiver.String()
	l.Sol.ObservedTimelock = escrow.Timelock
	if first {
		sw.log("Solana escrow %s seen holding %s plus %s rent", addr,
			solDisplay(escrow.Amount), solDisplay(escrow.Lamports-escrow.Amount))
		if sw.State == StateDraft || sw.State == StateAwaiting {
			sw.State = StateFunded
		}
	}

	now, nerr := m.Sol.Now(ctx)
	if nerr != nil {
		now = time.Now().Unix()
	}
	problems := sw.checkSolEscrow(l, escrow, now)
	l.Sol.VerifyPending = false
	if len(problems) == 0 {
		if !l.Sol.Verified {
			sw.log("Solana escrow %s verified: parties, amount, hashlock and deadline all match "+
				"(it is the %s leg)", addr, legOwner(sw.legIsInitiators(l.Dir)))
		}
		l.Sol.Verified, l.Sol.VerifyError = true, ""
		// The escrow's own timelock is the fact; the planned one was a proposal.
		l.Expiry = escrow.Timelock
	} else {
		msg := strings.Join(problems, "; ")
		if l.Sol.VerifyError != msg {
			sw.log("Solana escrow %s FAILED verification: %s", addr, msg)
		}
		l.Sol.Verified, l.Sol.VerifyError = false, msg
	}
}

// checkSolEscrow compares an escrow against everything the agreement fixed.
//
// An escrow that matches in four of five respects is not a four-fifths-safe
// swap; it is a swap to walk away from.
func (s *Swap) checkSolEscrow(l *Leg, e *sol.Escrow, now int64) []string {
	var problems []string

	// Who is paid and who is refunded invert with the direction, exactly as on
	// the Zenon leg: an out leg is one this user funded and the counterparty
	// claims.
	wantReceiver, wantInitiator := l.SelfAddr, l.PeerAddr
	if l.Dir == DirOut {
		wantReceiver, wantInitiator = l.PeerAddr, l.SelfAddr
	}
	if want := strings.TrimSpace(wantReceiver); want != "" && e.Receiver.String() != want {
		problems = append(problems, fmt.Sprintf("it pays %s, but this swap says %s",
			e.Receiver, want))
	}
	if want := strings.TrimSpace(wantInitiator); want != "" && e.Initiator.String() != want {
		problems = append(problems, fmt.Sprintf("it was funded by %s, but this swap says %s",
			e.Initiator, want))
	}
	if e.Amount < l.Sol.Lamports {
		problems = append(problems, fmt.Sprintf("it holds %s, but this swap agreed %s",
			solDisplay(e.Amount), solDisplay(l.Sol.Lamports)))
	}
	if hex.EncodeToString(e.Hashlock[:]) != hex.EncodeToString(s.SecretHash) {
		problems = append(problems, "it commits to a different hashlock, so this swap's secret "+
			"would not open it")
	}
	if e.Timelock <= now {
		problems = append(problems, fmt.Sprintf("its deadline passed at %s, so its funder can "+
			"take it back at any moment", utcTime(e.Timelock)))
	} else if left := time.Duration(e.Timelock-now) * time.Second; left < MinLegRemaining {
		problems = append(problems, fmt.Sprintf("its deadline is only %s away, under the %s "+
			"minimum needed to act on it", left.Truncate(time.Minute), MinLegRemaining))
	}
	// The ordering rule, in the one place it can be checked against a fact: the
	// initiator's leg must outlive the participant's, by MinLegGap at least.
	if other := s.other(l); other != nil && other.Expiry > 0 && other.Verified() {
		gap := int64(MinLegGap / time.Second)
		if s.legIsInitiators(l.Dir) {
			if e.Timelock < other.Expiry+gap {
				problems = append(problems, fmt.Sprintf("it expires at %s, but as the initiator's "+
					"leg it must outlive the other leg's %s by at least %s",
					utcTime(e.Timelock), utcTime(other.Expiry), MinLegGap))
			}
		} else if e.Timelock > other.Expiry-gap {
			problems = append(problems, fmt.Sprintf("it expires at %s, but as the participant's "+
				"leg it must expire at least %s before the other leg's %s",
				utcTime(e.Timelock), MinLegGap, utcTime(other.Expiry)))
		}
	}
	return problems
}

// readSolOutcome explains an escrow that is not there.
//
// Both exits close the account, so an absent one says nothing on its own: it may
// never have been funded, or the swap may have finished. The address's history
// is where the answer is, and for a redeem it is also where the preimage is —
// published in the clear in the instruction that spent the escrow, which is how
// the secret crosses chains when Solana is the leg the initiator claims.
func (m *Manager) readSolOutcome(ctx context.Context, sw *Swap, l *Leg,
	programID, addr sol.Pubkey) {

	var hashlock [32]byte
	copy(hashlock[:], sw.SecretHash)
	out, err := m.Sol.FindEscrowOutcome(ctx, programID, addr, hashlock, escrowScanLimit)
	if err != nil {
		l.Sol.VerifyPending = !l.Sol.Verified
		l.Sol.VerifyError = err.Error()
		return
	}
	switch {
	case out.Redeemed:
		if !l.Sol.Redeemed {
			sw.log("the %s Solana escrow was redeemed with the preimage in %s", l.Dir, out.RedeemSig)
		}
		l.Sol.Redeemed, l.Sol.Funded = true, false
		if l.Sol.RedeemSig == "" {
			l.Sol.RedeemSig = out.RedeemSig
		}
		if len(sw.Secret) == 0 && len(out.Preimage) > 0 {
			sw.Secret = out.Preimage
			sw.log("SECRET LEARNED from the Solana redeem %s: %s — use it to claim the other leg "+
				"before it expires", out.RedeemSig, hex.EncodeToString(out.Preimage))
		}
	case out.Refunded:
		if !l.Sol.Refunded {
			sw.log("the %s Solana escrow was refunded to its funder in %s", l.Dir, out.RefundSig)
		}
		l.Sol.Refunded, l.Sol.Funded = true, false
		if l.Sol.RefundSig == "" {
			l.Sol.RefundSig = out.RefundSig
		}
	default:
		// Nothing on chain and nothing in the history. If this side has already
		// broadcast a create, that is what this state is — but a submitted
		// signature is not evidence, so the cluster is asked what became of it.
		m.checkPendingSolCreate(ctx, sw, l)
	}
}

// checkPendingSolCreate resolves a create this browser submitted and has not
// seen land. The three answers are different news and only one of them means
// "wait": a failure has to be said out loud, and a signature the cluster has
// never heard of is cleared off the record so the page goes back to offering the
// step.
func (m *Manager) checkPendingSolCreate(ctx context.Context, sw *Swap, l *Leg) {
	if l.Sol.CreateSig == "" {
		return
	}
	state, err := m.Sol.SignatureStatus(ctx, l.Sol.CreateSig)
	if err != nil {
		return
	}
	switch {
	case state == nil || !state.Known:
		sw.log("the Solana cluster has never heard of the create %s, so it was dropped before it "+
			"landed. Funding the escrow is offered again", l.Sol.CreateSig)
		l.Sol.CreateSig = ""
	case state.Failed:
		sw.log("the Solana create %s failed on chain: %s. Nothing was locked", l.Sol.CreateSig,
			orElse(state.Err, "the cluster gave no reason"))
		l.Sol.CreateSig = ""
	}
}

var errNoSolanaNode = errors.New("no Solana RPC endpoint is set. Open Node settings and give " +
	"this browser one it can reach")

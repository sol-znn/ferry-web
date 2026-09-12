package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	zt "github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon/solzen/wasm/sol"
	"github.com/zenon/solzen/wasm/znn"
)

// Doing things, as opposed to observing them.
//
// The two chains are asked for in opposite ways, and the asymmetry is the
// point. On Solana this module builds an instruction and stops: the user's own
// wallet signs and sends it, so no key for their SOL is ever in the page. On
// Zenon it builds, signs and publishes -- with the per-swap key, which holds
// nothing but what the user deliberately sent into this swap, because no Zenon
// wallet can make these two calls.

// SolanaInstruction builds the next Solana instruction for a swap.
//
// It is a build, not a send. What comes back is program id, accounts and data;
// the page turns that into a transaction and hands it to a wallet. Nothing here
// can move SOL on its own.
func (m *Manager) SolanaInstruction(ctx context.Context, swapID, kind string) (*sol.Instruction, error) {
	s, err := m.store.Get(swapID)
	if err != nil {
		return nil, err
	}
	programID, err := sol.ParsePubkey(s.Terms.SolProgram)
	if err != nil {
		return nil, err
	}
	id32, err := hexBytes32(s.Terms.SwapID)
	if err != nil {
		return nil, err
	}

	switch kind {
	case "sol.create":
		if !s.Terms.Complete() {
			return nil, fmt.Errorf("the terms are not complete yet -- the counterparty's addresses are missing")
		}
		// The mirror of the check znn.create makes. Locking lamports is this
		// side's irreversible step, and the reasoning is the same in both
		// directions: the page obeys planActions, and the engine must not
		// depend on having been called by the page.
		if err := m.checkCounterpartyLeg(ctx, s); err != nil {
			return nil, err
		}
		sender, err := sol.ParsePubkey(s.Terms.SolSender)
		if err != nil {
			return nil, fmt.Errorf("the SOL sender address: %w", err)
		}
		receiver, err := sol.ParsePubkey(s.Terms.SolReceiver)
		if err != nil {
			return nil, fmt.Errorf("the SOL receiver address: %w", err)
		}
		hashlock, err := hexBytes32(s.Terms.Hashlock)
		if err != nil {
			return nil, err
		}
		now, err := m.sol().Now(ctx)
		if err != nil {
			return nil, err
		}
		// The deadline was fixed when the swap was made, and a program call
		// with a timelock already behind the cluster clock is refused on chain.
		// Saying so here costs the user a fee they would otherwise pay to
		// learn it.
		if s.Terms.SolTimelock <= now {
			return nil, fmt.Errorf(
				"this swap's Solana timelock passed %s ago; it can no longer be funded",
				humanDuration(now-s.Terms.SolTimelock))
		}
		return sol.BuildCreate(sol.CreateParams{
			ProgramID: programID,
			SwapID:    id32,
			Initiator: sender,
			Receiver:  receiver,
			Amount:    s.Terms.SolLamports,
			Hashlock:  hashlock,
			Timelock:  s.Terms.SolTimelock,
		})

	case "sol.redeem":
		if s.Secret == "" {
			return nil, fmt.Errorf("the secret is not known yet, so there is nothing to redeem with")
		}
		preimage, err := hex.DecodeString(s.Secret)
		if err != nil {
			return nil, fmt.Errorf("the stored secret is not hex: %w", err)
		}
		escrow, addr, err := m.sol().Escrow(ctx, programID, id32)
		if err != nil {
			return nil, err
		}
		return sol.BuildRedeem(programID, escrow, addr, preimage)

	case "sol.refund":
		escrow, addr, err := m.sol().Escrow(ctx, programID, id32)
		if err != nil {
			return nil, err
		}
		now, err := m.sol().Now(ctx)
		if err != nil {
			return nil, err
		}
		if now < escrow.Timelock {
			return nil, fmt.Errorf("the escrow is refundable in %s, not yet", humanDuration(escrow.Timelock-now))
		}
		return sol.BuildRefund(programID, escrow, addr)
	}
	return nil, fmt.Errorf("unknown Solana action %q", kind)
}

// RecordSolanaTx notes a signature the page obtained from a wallet, so a
// refresh that has not yet seen the transaction land does not re-propose the
// same step.
func (m *Manager) RecordSolanaTx(swapID, kind, signature string) error {
	s, err := m.store.Get(swapID)
	if err != nil {
		return err
	}
	signature = strings.TrimSpace(signature)
	switch kind {
	case "sol.create":
		s.SolCreateSig = signature
	case "sol.redeem":
		s.SolRedeemSig = signature
	case "sol.refund":
		s.SolRefundSig = signature
	default:
		return fmt.Errorf("unknown Solana action %q", kind)
	}
	s.Updated = time.Now().Unix()
	return m.store.Put(s)
}

// ZenonResult is what a signed Zenon operation produced.
type ZenonResult struct {
	Kind    string `json:"kind"`
	Tx      string `json:"tx"`
	HtlcID  string `json:"htlcId,omitempty"`
	Mined   bool   `json:"mined"`
	Hashes  uint64 `json:"hashes,omitempty"`
	Seconds int64  `json:"seconds"`
	// Confirmed is whether there is anything left to wait for. For a call to an
	// embedded contract it means the contract has processed it -- false is not
	// a failure there, an embedded call lands a momentum or two later, but it
	// means the entry will not resolve yet. For a receive, or a plain send to a
	// wallet, there is no second half, so it is true once the node has taken
	// the block.
	Confirmed bool `json:"confirmed"`

	// Steps is filled in when one action publishes more than one block. Only
	// znn.reclaim does, and zenonReclaimHome says why it has to. The fields
	// above then describe the action as a whole: Tx is the block that names it,
	// and the counters are totals.
	Steps []ZenonStep `json:"steps,omitempty"`
	// Incomplete is what a multi-block action still owes, when it stopped part
	// way. Everything in Steps was published and cannot be undone, so this is
	// reported as a result rather than raised as an error -- the money has
	// moved, and hiding that behind a failure is how it gets left somewhere.
	Incomplete string `json:"incomplete,omitempty"`
}

// ZenonStep is one published block inside an action that takes several.
type ZenonStep struct {
	Kind    string `json:"kind"`
	Tx      string `json:"tx"`
	Note    string `json:"note"`
	Mined   bool   `json:"mined"`
	Seconds int64  `json:"seconds"`
}

// ZenonAct performs one Zenon action with the swap's own key.
//
// progress is called while proof of work is being mined. An account with fused
// plasma skips that entirely; without it, an embedded-contract call is a couple
// of minutes of hashing in a browser tab, which is a thing a user has to be
// able to watch and cancel rather than a thing that happens to them.
//
// An action is usually one block. znn.reclaim is the exception, because on
// Zenon taking your own money back is three of them -- see zenonReclaimHome.
func (m *Manager) ZenonAct(ctx context.Context, swapID, kind string, progress znn.Progress) (*ZenonResult, error) {
	if kind == "znn.reclaim" {
		return m.zenonReclaimHome(ctx, swapID, progress)
	}
	return m.zenonStep(ctx, swapID, kind, progress)
}

// zenonStep builds, signs and publishes exactly one block.
func (m *Manager) zenonStep(ctx context.Context, swapID, kind string, progress znn.Progress) (*ZenonResult, error) {
	s, err := m.store.Get(swapID)
	if err != nil {
		return nil, err
	}
	if s.ZnnSwapSeed == "" {
		return nil, fmt.Errorf("this swap has no Zenon key")
	}
	key, err := znn.KeyFromSeedHex(s.ZnnSwapSeed)
	if err != nil {
		return nil, err
	}
	client := m.znn()

	var op *znn.Op
	switch kind {
	case "znn.receive":
		pending, err := client.Unreceived(ctx, key.Address, 20)
		if err != nil {
			return nil, err
		}
		if len(pending) == 0 {
			return nil, fmt.Errorf("there is nothing waiting to be received")
		}
		op, err = client.PrepareReceive(ctx, key, pending[0].Hash)
		if err != nil {
			return nil, err
		}

	case "znn.create":
		if !s.Terms.Complete() {
			return nil, fmt.Errorf("the terms are not complete yet")
		}
		if s.Role.SendsSol {
			return nil, fmt.Errorf("this side sends SOL; it does not create the Zenon HTLC")
		}
		// planActions computes this same refusal for the button, and the page
		// obeys it. This is that refusal in the place the money actually moves,
		// because creating the HTLC is the irreversible step and this API is
		// reachable without a plan -- by a script, a retry, a later UI. The two
		// must not drift, so both read it off readSolanaLeg.
		if err := m.checkCounterpartyLeg(ctx, s); err != nil {
			return nil, err
		}
		receiver, err := zt.ParseAddress(s.Terms.ZnnReceiver)
		if err != nil {
			return nil, fmt.Errorf("the Zenon payout address: %w", err)
		}
		hashLock, err := hex.DecodeString(s.Terms.Hashlock)
		if err != nil {
			return nil, err
		}
		mom, err := client.FrontierMomentum(ctx)
		if err != nil {
			return nil, err
		}
		if s.Terms.ZnnExpiry <= mom.Timestamp {
			return nil, fmt.Errorf(
				"this swap's Zenon expiry passed %s ago; creating the HTLC now would make one that is already expired",
				humanDuration(mom.Timestamp-s.Terms.ZnnExpiry))
		}
		op, err = client.PrepareCreateHtlc(ctx, key, znn.CreateHtlcParams{
			HashLocked:     receiver,
			ExpirationTime: s.Terms.ZnnExpiry,
			HashLock:       hashLock,
			Amount:         bigFromString(s.Terms.ZnnAmount),
			TokenStandard:  s.Terms.ZnnToken,
		})
		if err != nil {
			return nil, err
		}

	case "znn.unlock":
		if s.Secret == "" {
			return nil, fmt.Errorf("the secret is not known yet, so there is nothing to unlock with")
		}
		if s.HtlcID == "" {
			return nil, fmt.Errorf("this swap's Zenon HTLC has not been found yet")
		}
		id, err := znn.ParseHashHex(s.HtlcID)
		if err != nil {
			return nil, err
		}
		preimage, err := hex.DecodeString(s.Secret)
		if err != nil {
			return nil, err
		}
		op, err = client.PrepareUnlockHtlc(ctx, key, id, preimage)
		if err != nil {
			return nil, err
		}

	case "znn.reclaim":
		if s.HtlcID == "" {
			return nil, fmt.Errorf("this swap has no Zenon HTLC to reclaim")
		}
		id, err := znn.ParseHashHex(s.HtlcID)
		if err != nil {
			return nil, err
		}
		// The contract refuses a reclaim before the entry expires -- but it
		// refuses it after the block has been published, which on Zenon costs
		// the plasma, or the minutes of proof of work, either way. The Solana
		// branch spends a cluster-clock read to save the user that fee; this
		// spends a momentum read to save them the mine.
		info, err := client.Htlc(ctx, id)
		if err != nil {
			if znn.IsDataNonExistent(err) || err == znn.ErrNotFound {
				return nil, fmt.Errorf(
					"this swap's HTLC is no longer on chain -- it was already unlocked or reclaimed")
			}
			return nil, err
		}
		mom, err := client.FrontierMomentum(ctx)
		if err != nil {
			return nil, err
		}
		if mom.Timestamp < info.ExpirationTime {
			return nil, fmt.Errorf("this HTLC is reclaimable in %s, not yet",
				humanDuration(info.ExpirationTime-mom.Timestamp))
		}
		op, err = client.PrepareReclaimHtlc(ctx, key, id)
		if err != nil {
			return nil, err
		}

	case "znn.sweep":
		if s.ZnnHomeAddress == "" {
			return nil, fmt.Errorf("this swap has no address to sweep to")
		}
		home, err := zt.ParseAddress(s.ZnnHomeAddress)
		if err != nil {
			return nil, err
		}
		balances, err := client.Balances(ctx, key.Address)
		if err != nil {
			return nil, err
		}
		b, ok := balances[s.Terms.ZnnToken]
		if !ok || b.Amount.Sign() <= 0 {
			return nil, fmt.Errorf("this swap's address holds nothing to sweep")
		}
		op, err = client.PrepareSend(ctx, key, home, b.Amount, s.Terms.ZnnToken)
		if err != nil {
			return nil, err
		}

	default:
		return nil, fmt.Errorf("unknown Zenon action %q", kind)
	}

	started := time.Now()
	var hashes uint64
	wrapped := func(n uint64) {
		hashes = n
		if progress != nil {
			progress(n)
		}
	}
	hash, err := client.PublishOp(ctx, key, op, wrapped)
	if err != nil {
		return nil, err
	}

	res := &ZenonResult{
		Kind:    kind,
		Tx:      hash.String(),
		Mined:   op.NeedsWork(),
		Hashes:  hashes,
		Seconds: int64(time.Since(started).Seconds()),
	}

	// An embedded-contract call does nothing until the contract produces its
	// paired receive block. Waiting here means the caller's next read finds the
	// entry, instead of finding nothing and reporting a failure.
	//
	// Only such a call, though. A receive has no second half, and a plain send
	// to a wallet is paired by the *wallet's* receive -- so waiting on one
	// parks here until somebody opens Syrius, and then times out having learnt
	// nothing about a block the node accepted a minute and a half ago.
	if isContractCall(kind) {
		confirmed, werr := client.WaitForContractResult(ctx, hash, 90*time.Second)
		if werr != nil {
			return nil, werr
		}
		res.Confirmed = confirmed
	} else {
		res.Confirmed = true
	}

	switch kind {
	case "znn.create":
		// The id of an HTLC is the hash of the block that created it.
		s.HtlcID = hash.String()
		s.ZnnCreateTx = hash.String()
		res.HtlcID = s.HtlcID
	case "znn.unlock":
		s.ZnnUnlockTx = hash.String()
	case "znn.reclaim":
		s.ZnnReclaimTx = hash.String()
	case "znn.sweep":
		s.ZnnSweepTx = hash.String()
	}
	s.Updated = time.Now().Unix()
	if err := m.store.Put(s); err != nil {
		return nil, err
	}
	return res, nil
}

// reclaimHomeSteps is the reclaim action, in the blocks it actually takes.
var reclaimHomeSteps = []string{"znn.reclaim", "znn.receive", "znn.sweep"}

// How long to wait for the contract to pay the reclaim back. It is a couple of
// momentums in practice; this is generous because the alternative to waiting is
// handing the user a half-finished job.
const reclaimReturnTimeout = 90 * time.Second

// zenonReclaimHome takes a Zenon leg back, and then actually gives it to the user.
//
// Reclaiming pays nobody. The contract pays the address that created the entry
// -- this swap's address -- and on Zenon an incoming transfer is not spendable
// until the receiving account publishes a block for it. So the money coming
// home is three blocks: reclaim, receive, send. Only the first looks like the
// thing the user asked for, and the two after it are the easy ones to walk away
// from, because the first one visibly worked and the leg already reads as
// "refunded".
//
// That gap matters more here than anywhere else in this engine, because the key
// that can finish the job is in one browser's localStorage and nowhere else.
// Between the reclaim and the sweep, a cleared profile strands money the user
// has already been told is theirs again.
//
// So the action is all three. A step that fails leaves the ones before it
// published and says what is left, rather than reporting a failure that hides
// them -- and what is left is exactly what planActions goes back to offering
// one at a time, so the fallback is the behaviour this replaces.
func (m *Manager) zenonReclaimHome(ctx context.Context, swapID string, progress znn.Progress) (*ZenonResult, error) {
	out := &ZenonResult{Kind: "znn.reclaim"}

	// Work is reported as one running total across the three blocks, so a
	// progress bar fed by it climbs once instead of resetting twice. ZenonCostOf
	// quotes the same three, which is what the total is measured against.
	var done uint64
	running := func(n uint64) {
		if progress != nil {
			progress(done + n)
		}
	}
	run := func(kind, note string) (*ZenonResult, error) {
		r, err := m.zenonStep(ctx, swapID, kind, running)
		if err != nil {
			return nil, err
		}
		done += r.Hashes
		out.Steps = append(out.Steps, ZenonStep{
			Kind: kind, Tx: r.Tx, Note: note, Mined: r.Mined, Seconds: r.Seconds,
		})
		out.Hashes += r.Hashes
		out.Seconds += r.Seconds
		out.Mined = out.Mined || r.Mined
		return r, nil
	}
	stop := func(format string, a ...any) (*ZenonResult, error) {
		out.Incomplete = fmt.Sprintf(format, a...)
		return out, nil
	}

	reclaim, err := run("znn.reclaim", "the contract returned the funds to this swap's address")
	if err != nil {
		// Nothing was published, so nothing is half-done: this is an ordinary
		// failure and is reported as one.
		return nil, err
	}
	out.Tx, out.Confirmed = reclaim.Tx, reclaim.Confirmed

	s, err := m.store.Get(swapID)
	if err != nil {
		return nil, err
	}
	key, err := znn.KeyFromSeedHex(s.ZnnSwapSeed)
	if err != nil {
		return nil, err
	}

	pending, err := m.waitForIncoming(ctx, key.Address, reclaimReturnTimeout)
	if err != nil {
		return stop("the HTLC was reclaimed, but waiting for the contract to pay it back failed: %v. "+
			"Refresh -- the page will offer to receive it, and then to send it home.", err)
	}
	if pending == 0 {
		return stop("the HTLC was reclaimed, but the contract's payment back has not appeared within %s. "+
			"Refresh in a moment -- the page will offer to receive it, and then to send it home.",
			humanDuration(int64(reclaimReturnTimeout/time.Second)))
	}
	// One receive, for the one block a reclaim returns. Asking Unreceived again
	// straight afterwards would still list it -- it is not gone until the
	// receive is in a momentum -- so looping here would try to receive the same
	// block twice and be refused for it. Anything else that happens to be
	// waiting is left to planActions, which offers a receive and a sweep the way
	// it always has.
	if _, err := run("znn.receive", "received it into this swap's address"); err != nil {
		return stop("the money is back at this swap's address, but receiving it failed: %v. "+
			"Refresh and the page will offer the rest.", err)
	}

	// And a balance only moves when that block lands. Sweeping before it does
	// asks the node to send money it cannot see yet, and is refused for it.
	held, err := m.waitForBalance(ctx, key.Address, s.Terms.ZnnToken, reclaimReturnTimeout)
	if err != nil {
		return stop("the money was received into this swap's address, but reading its balance failed: %v. "+
			"Refresh and the page will offer to send it on.", err)
	}
	if held.Sign() <= 0 {
		return stop("the money was received into this swap's address, but the node still reports it empty "+
			"after %s. Refresh in a moment -- the page will offer to send it on.",
			humanDuration(int64(reclaimReturnTimeout/time.Second)))
	}

	if s.ZnnHomeAddress == "" {
		return stop("the money is at this swap's address, but this record has no wallet address to send it to. " +
			"Export the swap before clearing this browser.")
	}
	if _, err := run("znn.sweep", "sent it on to "+s.ZnnHomeAddress); err != nil {
		return stop("the money is at this swap's address but not yet in your wallet: %v. "+
			"Refresh and the page will offer to send it on.", err)
	}
	return out, nil
}

// waitForBalance polls until an address holds some of a token, and reports how
// much. Like waitForIncoming, running out of time is not an error: it is the
// answer zero.
func (m *Manager) waitForBalance(ctx context.Context, addr zt.Address, zts string, timeout time.Duration) (*big.Int, error) {
	client := m.znn()
	deadline := time.Now().Add(timeout)
	for {
		balances, err := client.Balances(ctx, addr)
		if err != nil {
			return nil, err
		}
		if b, ok := balances[zts]; ok && b.Amount.Sign() > 0 {
			return b.Amount, nil
		}
		if time.Now().After(deadline) {
			return new(big.Int), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// waitForIncoming polls until an address has something waiting to be received.
//
// It reports how many, and no error when the wait simply ran out: a caller that
// has already published something needs to tell "not yet" apart from "the node
// stopped answering", and act differently on neither.
func (m *Manager) waitForIncoming(ctx context.Context, addr zt.Address, timeout time.Duration) (int, error) {
	client := m.znn()
	deadline := time.Now().Add(timeout)
	for {
		pending, err := client.Unreceived(ctx, addr, 20)
		if err != nil {
			return 0, err
		}
		if len(pending) > 0 {
			return len(pending), nil
		}
		if time.Now().After(deadline) {
			return 0, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// checkCounterpartyLeg refuses to fund one leg against a counterparty leg that
// is not the one that was agreed, or -- for the participant -- against no
// counterparty leg at all.
//
// It is the same test planActions runs to decide whether the button is enabled,
// asked of the same readers, so the two cannot answer differently. It exists
// separately from the plan because funding is the irreversible step and this
// API is reachable without a plan -- by a script, a retry, a later UI.
//
// Both directions are covered here rather than only the Zenon one. The two
// funding calls are mirror images and so is the way each loses money: the ZNN
// sender locks tokens against an escrow that will not pay them, the SOL sender
// locks lamports against an HTLC that will not. A guard on one of the two is a
// guard on the swaps that happen to go that way.
func (m *Manager) checkCounterpartyLeg(ctx context.Context, s *Swap) error {
	if err := m.checkChains(ctx, &s.Terms); err != nil {
		return err
	}
	solNow, znnNow, err := m.clocks(ctx)
	if err != nil {
		return err
	}
	// The same two rules Refresh turns into Status.Unfundable, asked here for
	// the same reason the counterparty check is: the page obeys the plan, and
	// the engine must not depend on having been called by the page.
	if skew := solNow - znnNow; abs64(skew) > MaxClockSkewSeconds {
		return fmt.Errorf(
			"the two chains' clocks are %s apart, so the leg ordering cannot be checked reliably -- "+
				"funding against arithmetic that could not be done is the thing this refuses",
			humanDuration(abs64(skew)))
	}
	if err := s.Terms.CheckOrdering(minInt64(solNow, znnNow)); err != nil {
		return fmt.Errorf("this swap is no longer safe to fund: %w", err)
	}
	st := &Status{SolNow: solNow, ZnnNow: znnNow}

	theirs, whatTheirs, mineText := &st.Sol, "escrow", tokenName(s)
	if s.Role.SendsSol {
		theirs, whatTheirs, mineText = &st.Znn, "HTLC", "SOL"
	}

	// Only the counterparty's leg is read: reading both would double the calls
	// for an answer that does not use the second one.
	if s.Role.SendsSol {
		if err := m.readZenonLeg(ctx, s, st); err != nil {
			return err
		}
	} else {
		if err := m.readSolanaLeg(ctx, s, st); err != nil {
			return err
		}
	}

	if theirs.Funded && !theirs.Verified {
		return fmt.Errorf(
			"the counterparty's leg does not match the agreement: %s -- funding now would lock "+
				"your %s against an %s that will not pay you",
			joinProblems(theirs.Problems), mineText, whatTheirs)
	}
	if !s.Role.Initiator && !(theirs.Funded && theirs.Verified) {
		return fmt.Errorf("the initiator has not funded a matching leg yet -- you go second, on purpose")
	}
	return nil
}

// ZenonCost asks what the next Zenon operation will cost, without doing it.
//
// It is what lets the UI say "this will take about four minutes, or none at all
// if you fuse plasma to this address first" before the user commits to
// watching a progress bar.
type ZenonCost struct {
	// Difficulty and ExpectedHashes are totals over Blocks, which is one for
	// every action but znn.reclaim. Quoting a third of a reclaim's work would
	// be quoting a third of the wait.
	Difficulty     uint64 `json:"difficulty"`
	ExpectedHashes uint64 `json:"expectedHashes"`
	Blocks         int    `json:"blocks"`
	NeedsWork      bool   `json:"needsWork"`
	Address        string `json:"address"`
}

func (m *Manager) ZenonCostOf(ctx context.Context, swapID, kind string) (*ZenonCost, error) {
	s, err := m.store.Get(swapID)
	if err != nil {
		return nil, err
	}
	key, err := znn.KeyFromSeedHex(s.ZnnSwapSeed)
	if err != nil {
		return nil, err
	}
	kinds := []string{kind}
	if kind == "znn.reclaim" {
		kinds = reclaimHomeSteps
	}
	out := &ZenonCost{Blocks: len(kinds), Address: key.Address.String()}
	for _, k := range kinds {
		blockType, to, data, err := zenonProbeShape(k, key.Address)
		if err != nil {
			return nil, err
		}
		var toPtr *zt.Address
		if blockType == znn.BlockTypeUserSend {
			toPtr = &to
		}
		req, err := m.znn().RequiredPoWFor(ctx, key.Address, blockType, toPtr, data)
		if err != nil {
			return nil, err
		}
		out.Difficulty += req.RequiredDifficulty
		out.ExpectedHashes += znn.ExpectedHashes(req.RequiredDifficulty)
	}
	out.NeedsWork = out.Difficulty > 0
	return out, nil
}

// zenonProbeShape is the block one action publishes, in just enough detail for
// the node to price it.
//
// The difficulty depends on the method, not on its arguments, so an empty call
// of the right shape gives the same answer as the real one -- but it does have
// to be the right shape: base plasma for a send to an embedded contract is
// worked out by decoding the call.
func zenonProbeShape(kind string, self zt.Address) (blockType uint64, to zt.Address, data []byte, err error) {
	blockType, to = znn.BlockTypeUserSend, znn.HtlcContract
	switch kind {
	case "znn.receive":
		blockType = znn.BlockTypeUserReceive
	case "znn.create":
		data, _ = znn.PackCreate(zt.Address{}, 0, znn.HashTypeSHA256, znn.PreimageSize, make([]byte, 32))
	case "znn.unlock":
		data, _ = znn.PackUnlock(zt.Hash{}, make([]byte, 32))
	case "znn.reclaim":
		data, _ = znn.PackReclaim(zt.Hash{})
	case "znn.sweep":
		// A sweep is an ordinary transfer to the user's own wallet. Addressing
		// this probe at the htlc contract with no payload would ask the node to
		// decode nothing as a contract call, which it refuses.
		to = self
	default:
		return 0, zt.Address{}, nil, fmt.Errorf("unknown Zenon action %q", kind)
	}
	return blockType, to, data, nil
}

// isContractCall reports whether an action is a send to an embedded contract,
// which is the only kind of block that does its work in a second, later block.
func isContractCall(kind string) bool {
	switch kind {
	case "znn.create", "znn.unlock", "znn.reclaim":
		return true
	}
	return false
}

func formatZnnAmount(base string, decimals int) string {
	v, ok := new(big.Int).SetString(base, 10)
	if !ok {
		return base
	}
	return znn.FormatAmount(v, decimals)
}

func humanTime(unix int64) string {
	return time.Unix(unix, 0).UTC().Format("2006-01-02 15:04 MST")
}

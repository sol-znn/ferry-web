package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	zt "github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon/solzen/wasm/sol"
	"github.com/zenon/solzen/wasm/znn"
)

// Refresh is the only place chain state enters a swap.
//
// It reads both legs, checks each against what was agreed, works out what this
// side should do next, and -- when the counterparty has revealed the secret --
// picks it up. Everything the UI shows comes from here, so there is one place
// where "is this swap safe to act on" is decided, rather than a rule per button.

// LegStatus is one leg as the chain currently reports it.
type LegStatus struct {
	// Funded is true while the contract exists and holds the money.
	Funded bool `json:"funded"`
	// Settled and Refunded are mutually exclusive and both mean the contract is
	// gone. Unknown is the third case: gone, with no explanation found within
	// the scan's reach.
	Settled  bool `json:"settled"`
	Refunded bool `json:"refunded"`

	// Verified is true when a funded leg matches the agreed terms in every
	// checked respect. A funded leg that is not verified is worse than an
	// unfunded one, because it looks like progress.
	Verified bool     `json:"verified"`
	Problems []string `json:"problems,omitempty"`

	Address     string `json:"address,omitempty"`
	Amount      string `json:"amount,omitempty"`
	Expiry      int64  `json:"expiry,omitempty"`
	SecondsLeft int64  `json:"secondsLeft,omitempty"`
	Expired     bool   `json:"expired"`
	Tx          string `json:"tx,omitempty"`
	Note        string `json:"note,omitempty"`

	// Pending is set when this side has broadcast a transaction for this leg
	// that the chain has not shown yet. Between the broadcast and the escrow
	// appearing at `confirmed` the leg reads exactly like an unfunded one, and
	// the next thing offered would otherwise be to fund it again.
	Pending string `json:"pending,omitempty"`
}

// SwapAddressStatus is the per-swap Zenon account.
//
// Both sides have one, for different reasons, and the difference is what
// NeedsFunding says. The side sending ZNN pays this address and the page creates
// the HTLC from it, because only its creator can reclaim after expiry. The side
// *receiving* ZNN never puts anything in it -- the contract pays their own
// wallet -- but the unlock is still a Zenon block, and a block needs plasma. So
// both sides care whether it has any, and only one of them cares about its
// balance.
//
// Balance and Pending are separate because Zenon is account-chain based: money
// sent to an address is not spendable until that address publishes a receive
// block for it, so an address that has just been paid shows a balance of zero
// and one pending block. That reads as "the payment did not arrive" unless it is
// shown for what it is.
type SwapAddressStatus struct {
	Address string `json:"address"`
	// NeedsFunding is true only for the side that sends ZNN. For the other, an
	// empty balance here is correct and permanent.
	NeedsFunding bool   `json:"needsFunding"`
	Balance      string `json:"balance"`
	Pending      int    `json:"pending"`
	PendingFrom  string `json:"pendingFrom,omitempty"`
	Sufficient   bool   `json:"sufficient"`
	// PlasmaFused is true when the address has fused plasma, which is the
	// difference between a block that publishes at once and one that has to be
	// mined for minutes.
	PlasmaFused bool `json:"plasmaFused"`
	// PendingWork is the difficulty the next contract call from this address
	// would cost, zero when plasma covers it.
	PendingWork uint64 `json:"pendingWork"`
	// PlasmaKnown is false when the node could not be asked. The two fields
	// above are then meaningless rather than zero, and must not be shown: "no
	// plasma, and the next block is free" is a pair the node never reports, but
	// it is what their zero values say, and it reads as a confident promise
	// that a four-minute grind will take a second.
	PlasmaKnown bool `json:"plasmaKnown"`
	// PlasmaProblem is why, when PlasmaKnown is false.
	PlasmaProblem string `json:"plasmaProblem,omitempty"`
}

// Action is one thing this side can do next.
type Action struct {
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
	// Ready is false for an action that is the right next step but cannot be
	// taken yet -- waiting on a timelock, or on the counterparty.
	Ready   bool   `json:"ready"`
	Blocked string `json:"blocked,omitempty"`
	// Primary marks the one action the user is actually being asked for.
	Primary bool `json:"primary"`
}

// Status is the whole picture of a swap, recomputed on every refresh.
type Status struct {
	SwapID   string `json:"swapId"`
	SolNow   int64  `json:"solNow"`
	ZnnNow   int64  `json:"znnNow"`
	Complete bool   `json:"complete"`

	Sol LegStatus `json:"sol"`
	Znn LegStatus `json:"znn"`

	// SolRent is what opening the escrow account costs on top of the amount.
	// Zero when the node could not be asked, in which case the funding step
	// says what it always said rather than naming a figure that is not one.
	SolRent uint64 `json:"solRent,omitempty"`

	SwapAddress *SwapAddressStatus `json:"swapAddress,omitempty"`

	// SecretKnown is true once this side can act on the preimage, whether it
	// invented it or found it on chain.
	SecretKnown bool   `json:"secretKnown"`
	SecretFrom  string `json:"secretFrom,omitempty"`
	Outcome     string `json:"outcome,omitempty"`

	// WrongChain, when set, is why this swap cannot be read here at all: the
	// nodes now configured serve chains it was not made against. Its legs are
	// not read, because "not funded" would be a false answer rather than a
	// missing one, and no action is offered.
	WrongChain string `json:"wrongChain,omitempty"`
	// Unfundable, when set, is why no leg may be funded right now, whatever
	// else the swap looks ready for. It does not stop claiming or refunding:
	// those take money back, and a rule about deadlines is not a reason to
	// stop somebody recovering their own.
	Unfundable string `json:"unfundable,omitempty"`

	Actions  []Action `json:"actions"`
	Warnings []string `json:"warnings,omitempty"`
}

// Refresh reads both chains and returns the swap's state.
func (m *Manager) Refresh(ctx context.Context, swapID string) (*Swap, *Status, error) {
	s, err := m.store.Get(swapID)
	if err != nil {
		return nil, nil, err
	}
	solNow, znnNow, err := m.clocks(ctx)
	if err != nil {
		return nil, nil, err
	}
	st := &Status{
		SwapID:   s.ID,
		SolNow:   solNow,
		ZnnNow:   znnNow,
		Complete: s.Terms.Complete(),
	}
	// Before any leg is read. The node URLs are one global setting and every
	// swap in this browser is read through it, so a swap made against another
	// chain comes back as two legs that do not exist -- and the next thing
	// offered would be to fund them. Reading nothing and saying why is the only
	// honest answer.
	if err := m.checkChains(ctx, &s.Terms); err != nil {
		st.WrongChain = err.Error()
		st.Warnings = append(st.Warnings, err.Error())
		st.Actions = append(st.Actions, Action{
			Kind:   "await.chain",
			Label:  "This swap belongs to another chain",
			Detail: "Point Nodes back at the chain it was made on to work with it.",
		})
		return s, st, nil
	}

	if skew := solNow - znnNow; abs64(skew) > MaxClockSkewSeconds {
		st.Warnings = append(st.Warnings, fmt.Sprintf(
			"the two chains' clocks are %s apart; the leg ordering below is only as good as that agreement",
			humanDuration(abs64(skew))))
		// The ordering rule is arithmetic across two clocks. If they do not
		// agree, the arithmetic is not evidence, and funding on the strength of
		// a sum that could not be done is what this refuses.
		st.Unfundable = fmt.Sprintf(
			"the two chains' clocks are %s apart, so the leg ordering cannot be checked reliably",
			humanDuration(abs64(skew)))
	}
	if st.Complete {
		if err := s.Terms.CheckOrdering(minInt64(solNow, znnNow)); err != nil {
			st.Warnings = append(st.Warnings, err.Error())
			// A refusal, not a note. CheckOrdering's own comment says the
			// moment a swap stops being safe is "the moment the UI has to stop
			// telling someone to fund it", and a warning above a live button is
			// not stopping. Funding only: claiming and refunding take money
			// back, and a rule about deadlines is not a reason to stop somebody
			// recovering their own.
			if st.Unfundable == "" {
				st.Unfundable = err.Error()
			}
		}
	}

	if err := m.readSolanaLeg(ctx, s, st); err != nil {
		return nil, nil, err
	}
	if err := m.readZenonLeg(ctx, s, st); err != nil {
		return nil, nil, err
	}
	if err := m.readSwapAddress(ctx, s, st); err != nil {
		return nil, nil, err
	}

	if s.Secret != "" {
		st.SecretKnown = true
		if st.SecretFrom == "" {
			st.SecretFrom = s.SecretSource
		}
		if st.SecretFrom == "" {
			st.SecretFrom = "you generated it"
		}
	}
	m.planActions(s, st)
	m.settleOutcome(s, st)

	s.Updated = solNow
	if err := m.store.Put(s); err != nil {
		return nil, nil, err
	}
	return s, st, nil
}

func (m *Manager) readSolanaLeg(ctx context.Context, s *Swap, st *Status) error {
	t := &s.Terms
	programID, err := sol.ParsePubkey(t.SolProgram)
	if err != nil {
		st.Sol.Problems = append(st.Sol.Problems, err.Error())
		return nil
	}
	swapID, err := hexBytes32(t.SwapID)
	if err != nil {
		st.Sol.Problems = append(st.Sol.Problems, err.Error())
		return nil
	}
	addr, _, err := sol.EscrowAddress(programID, swapID)
	if err != nil {
		return err
	}
	st.Sol.Address = addr.String()

	hashlock, err := hexBytes32(t.Hashlock)
	if err != nil {
		st.Sol.Problems = append(st.Sol.Problems, err.Error())
		return nil
	}

	client := m.sol()
	e, _, err := client.Escrow(ctx, programID, swapID)
	if err == sol.ErrNoAccount {
		// Not there. Either it has not been funded yet, or it settled -- and
		// the history says which.
		// 50 signatures back. Failed attempts sit in the same history -- a wrong
		// preimage leaves one, and so does a decoy somebody published to be
		// found here -- so the window has to be wider than the number of
		// transactions a swap needs, not equal to it.
		out, oerr := client.FindEscrowOutcome(ctx, programID, addr, hashlock, 50)
		if oerr != nil {
			return oerr
		}
		switch {
		case out.Redeemed:
			st.Sol.Settled, st.Sol.Tx = true, out.RedeemSig
			st.Sol.Note = "redeemed with the preimage"
			m.adoptPreimage(s, st, out.Preimage, "the Solana redeem transaction")
		case out.Refunded:
			st.Sol.Refunded, st.Sol.Tx = true, out.RefundSig
			st.Sol.Note = "refunded to the sender after the timelock"
		default:
			// Nothing on chain and nothing in the history. If this side has
			// already broadcast a create, that is what this state is, and
			// saying so is what stops the page offering to fund it a second
			// time -- which is what the recorded signature was always for.
			m.notePendingCreate(ctx, s, st)
			// Still to be funded, so what it will cost is worth knowing before
			// rather than after. A node that will not answer leaves this zero
			// and the step says nothing about rent, which is what it did
			// before this existed.
			if s.Role.SendsSol {
				if rent, rerr := client.MinimumRent(ctx); rerr == nil {
					st.SolRent = rent
				}
			}
		}
		return nil
	}
	if err != nil {
		st.Sol.Problems = append(st.Sol.Problems, err.Error())
		return nil
	}

	st.Sol.Funded = true
	st.Sol.Amount = formatSol(e.Amount)
	st.Sol.Expiry = e.Timelock
	st.Sol.SecondsLeft = e.Timelock - st.SolNow
	st.Sol.Expired = st.SolNow >= e.Timelock
	st.Sol.Note = fmt.Sprintf("holds %s plus %s rent", formatSol(e.Amount), formatSol(e.Lamports-e.Amount))

	// Every field the agreement fixed, checked against what is actually on
	// chain. An escrow that matches in four of five respects is not a
	// four-fifths-safe swap; it is a swap to walk away from.
	var problems []string
	if want := t.SolReceiver; want != "" && e.Receiver.String() != want {
		problems = append(problems, fmt.Sprintf("it pays %s, but the swap says %s", e.Receiver, want))
	}
	if want := t.SolSender; want != "" && e.Initiator.String() != want {
		problems = append(problems, fmt.Sprintf("it was funded by %s, but the swap says %s", e.Initiator, want))
	}
	if e.Amount != t.SolLamports {
		problems = append(problems, fmt.Sprintf("it holds %s, but the swap says %s", formatSol(e.Amount), formatSol(t.SolLamports)))
	}
	if hex.EncodeToString(e.Hashlock[:]) != t.Hashlock {
		problems = append(problems, "it commits to a different hashlock, so this swap's secret would not open it")
	}
	if e.Timelock != t.SolTimelock {
		problems = append(problems, fmt.Sprintf("its timelock is %d, but the swap says %d", e.Timelock, t.SolTimelock))
	}
	st.Sol.Problems = append(st.Sol.Problems, problems...)
	st.Sol.Verified = len(st.Sol.Problems) == 0
	return nil
}

// notePendingCreate explains an escrow that is not there yet but has been paid
// for.
//
// A submitted signature is not evidence on its own -- a transaction can be
// dropped, and one that landed can have reverted -- so the cluster is asked
// what became of it. The three answers are different pieces of news and only
// one of them means "wait": a failure has to be said out loud, and a signature
// the cluster has never heard of is cleared off the record so the page goes
// back to offering the step.
func (m *Manager) notePendingCreate(ctx context.Context, s *Swap, st *Status) {
	if s.SolCreateSig == "" {
		return
	}
	state, err := m.sol().SignatureStatus(ctx, s.SolCreateSig)
	if err != nil {
		// Asking failed, which says nothing about the transaction. Keep the
		// record and keep the note: re-offering the funding step because one
		// call did not answer is the mistake this is here to avoid.
		st.Sol.Pending = "you have broadcast a funding transaction and this browser could not check on it"
		return
	}
	switch {
	case state.Failed:
		st.Sol.Problems = append(st.Sol.Problems, fmt.Sprintf(
			"your funding transaction %s failed on chain (%s), so nothing was locked",
			s.SolCreateSig, state.Err))
		s.SolCreateSig = ""
	case state.Known:
		st.Sol.Pending = fmt.Sprintf(
			"your funding transaction %s has landed; the escrow will appear here in a moment",
			s.SolCreateSig)
	default:
		// Never seen by the cluster. Dropped before it was included, so there
		// is nothing to wait for and the step is owed again.
		st.Sol.Problems = append(st.Sol.Problems, fmt.Sprintf(
			"your funding transaction %s never reached the chain; fund again", s.SolCreateSig))
		s.SolCreateSig = ""
	}
}

func (m *Manager) readZenonLeg(ctx context.Context, s *Swap, st *Status) error {
	t := &s.Terms
	client := m.znn()

	if s.HtlcID == "" && t.ZnnReceiver != "" {
		// The id of an HTLC is the hash of the block that created it, so it
		// cannot be derived -- but it can be found, which saves the two sides
		// one more string to copy between them.
		hashLock, err := hex.DecodeString(t.Hashlock)
		if err != nil {
			return nil
		}
		receiver, err := zt.ParseAddress(t.ZnnReceiver)
		if err != nil {
			return nil
		}
		id, found, err := client.FindHtlcByHashlock(ctx, hashLock, receiver, znn.DefaultScanBlocks)
		if err != nil {
			st.Znn.Problems = append(st.Znn.Problems, err.Error())
			return nil
		}
		if found {
			s.HtlcID = id.String()
		}
	}
	if s.HtlcID == "" {
		return nil
	}
	st.Znn.Address = s.HtlcID

	id, err := znn.ParseHashHex(s.HtlcID)
	if err != nil {
		st.Znn.Problems = append(st.Znn.Problems, err.Error())
		return nil
	}

	info, err := client.Htlc(ctx, id)
	if err != nil {
		if !znn.IsDataNonExistent(err) && err != znn.ErrNotFound {
			st.Znn.Problems = append(st.Znn.Problems, err.Error())
			return nil
		}
		// Gone: unlocked or reclaimed. The entry is deleted either way, so the
		// contract's own account chain is the only record of which -- and the
		// hashlock is what stops a block shaped like this swap's unlock from
		// answering for it.
		hashLock, herr := hex.DecodeString(t.Hashlock)
		if herr != nil {
			st.Znn.Problems = append(st.Znn.Problems, herr.Error())
			return nil
		}
		out, oerr := client.FindHtlcOutcome(ctx, id, hashLock, znn.DefaultScanBlocks)
		if oerr != nil {
			return oerr
		}
		switch {
		case out.Unlocked:
			st.Znn.Settled, st.Znn.Tx = true, out.UnlockTx.String()
			st.Znn.Note = "unlocked with the preimage"
			m.adoptPreimage(s, st, out.Preimage, "the Zenon unlock block")
		case out.Reclaimed:
			st.Znn.Refunded, st.Znn.Tx = true, out.ReclaimTx.String()
			st.Znn.Note = "reclaimed by its creator after expiry"
		}
		return nil
	}

	st.Znn.Funded = true
	st.Znn.Amount = znn.FormatAmount(info.Amount, t.ZnnDecimals)
	st.Znn.Expiry = info.ExpirationTime
	st.Znn.SecondsLeft = info.ExpirationTime - st.ZnnNow
	st.Znn.Expired = st.ZnnNow >= info.ExpirationTime

	var problems []string
	if info.HashType != znn.HashTypeSHA256 {
		problems = append(problems, fmt.Sprintf(
			"it commits with hashType %d (SHA3-256); a swap against Solana needs hashType %d (SHA-256), "+
				"and no preimage can satisfy both", info.HashType, znn.HashTypeSHA256))
	}
	if int(info.KeyMaxSize) < znn.PreimageSize {
		problems = append(problems, fmt.Sprintf(
			"its keyMaxSize is %d, too small for this swap's %d-byte secret", info.KeyMaxSize, znn.PreimageSize))
	}
	if hex.EncodeToString(info.HashLock) != t.Hashlock {
		problems = append(problems, "it commits to a different hashlock, so this swap's secret would not open it")
	}
	if want := t.ZnnReceiver; want != "" && info.HashLocked.String() != want {
		problems = append(problems, fmt.Sprintf("it pays %s, but the swap says %s", info.HashLocked, want))
	}
	if want := t.ZnnSender; want != "" && info.TimeLocked.String() != want {
		problems = append(problems, fmt.Sprintf("it was created by %s, but the swap says %s", info.TimeLocked, want))
	}
	if info.TokenStandard != t.ZnnToken {
		problems = append(problems, fmt.Sprintf("it holds %s, but the swap is for %s", info.TokenStandard, t.ZnnToken))
	}
	if want := bigFromString(t.ZnnAmount); info.Amount == nil || info.Amount.Cmp(want) != 0 {
		problems = append(problems, fmt.Sprintf("it holds %s, but the swap says %s",
			znn.FormatAmount(info.Amount, t.ZnnDecimals), znn.FormatAmount(want, t.ZnnDecimals)))
	}
	if info.ExpirationTime != t.ZnnExpiry {
		problems = append(problems, fmt.Sprintf("it expires at %d, but the swap says %d", info.ExpirationTime, t.ZnnExpiry))
	}
	st.Znn.Problems = append(st.Znn.Problems, problems...)
	st.Znn.Verified = len(st.Znn.Problems) == 0
	return nil
}

// readSwapAddress reports on the per-swap Zenon account. Both sides have one;
// see SwapAddressStatus for why.
func (m *Manager) readSwapAddress(ctx context.Context, s *Swap, st *Status) error {
	if s.ZnnSwapSeed == "" {
		return nil
	}
	key, err := znn.KeyFromSeedHex(s.ZnnSwapSeed)
	if err != nil {
		return err
	}
	client := m.znn()
	sa := &SwapAddressStatus{Address: key.Address.String(), NeedsFunding: !s.Role.SendsSol}

	if sa.NeedsFunding {
		balances, err := client.Balances(ctx, key.Address)
		if err != nil {
			return err
		}
		held := new(big.Int)
		if b, ok := balances[s.Terms.ZnnToken]; ok {
			held = b.Amount
		}
		sa.Balance = znn.FormatAmount(held, s.Terms.ZnnDecimals)
		// Not bigFromString: an agreed amount that will not parse would become
		// zero, and every balance is at least zero, so "you have enough to
		// create the HTLC" would be true for an empty address. The one place
		// this figure is read as a threshold is the one place it has to fail
		// closed.
		want, werr := amountOf(s.Terms.ZnnAmount)
		if werr != nil {
			return fmt.Errorf("this swap's agreed Zenon amount is unusable: %w", werr)
		}
		sa.Sufficient = held.Cmp(want) >= 0

		pending, err := client.Unreceived(ctx, key.Address, 20)
		if err != nil {
			return err
		}
		sa.Pending = len(pending)
		if len(pending) > 0 {
			sa.PendingFrom = pending[0].Address.String()
		}
	} else {
		sa.Balance = "0"
	}

	// One cheap question answers "will the next block be instant or a
	// multi-minute grind": the node reports zero required difficulty exactly
	// when the account has the plasma to cover the block.
	//
	// The probe has to carry a real Create payload. Base plasma for a send to
	// an embedded contract is worked out by decoding the call, so an empty
	// send addressed to the htlc contract is not a cheap approximation of one
	// -- it is a request the node answers with "method not found in the abi".
	//
	// The payload is the call this side will actually make, because the two cost
	// different amounts: Create is 52,500 plasma and Unlock is 73,500, and
	// telling the unlocking side the cheaper number would understate their wait
	// by a third.
	//
	// A probe that does not get an answer leaves plasma unknown, and says so.
	// This is the one number on the page a user makes a decision on -- fuse
	// QSR, or sit through the mine -- and a node that is briefly unreachable is
	// not evidence for either. Falling through to the zero values would claim
	// both at once: no plasma, and no work to do.
	var probe []byte
	var perr error
	if sa.NeedsFunding {
		probe, perr = znn.PackCreate(zt.Address{}, 0, znn.HashTypeSHA256, znn.PreimageSize, make([]byte, 32))
	} else {
		probe, perr = znn.PackUnlock(zt.Hash{}, make([]byte, znn.PreimageSize))
	}
	if perr != nil {
		sa.PlasmaProblem = perr.Error()
	} else if req, err := client.RequiredPoWFor(ctx, key.Address, znn.BlockTypeUserSend, &znn.HtlcContract, probe); err != nil {
		sa.PlasmaProblem = err.Error()
	} else {
		sa.PlasmaKnown = true
		sa.PlasmaFused = req.RequiredDifficulty == 0
		sa.PendingWork = req.RequiredDifficulty
	}
	st.SwapAddress = sa
	return nil
}

// adoptPreimage records a secret found on chain, after checking it against the
// hashlock this swap committed to.
//
// The check is the point, and it is now the second one rather than the only
// one: both scanners identify a settlement by hashing its preimage, so a
// candidate that reaches here has already matched. That is deliberate
// duplication. The scanners' check decides whether the leg is *settled*, and
// getting it wrong loses the secret; this one decides whether the secret is
// *usable*, and getting it wrong produces a claim attempt that fails for
// reasons nobody can read. Neither is a good place to trust the other.
func (m *Manager) adoptPreimage(s *Swap, st *Status, preimage []byte, from string) {
	if len(preimage) == 0 {
		return
	}
	candidate := hex.EncodeToString(preimage)
	got, err := HashlockOf(candidate)
	if err != nil || got != s.Terms.Hashlock {
		st.Warnings = append(st.Warnings, fmt.Sprintf(
			"a preimage was found in %s but it does not match this swap's hashlock; ignoring it", from))
		return
	}
	if s.Secret == "" {
		s.Secret = candidate
		s.SecretSource = from
		st.SecretFrom = from
	}
	st.SecretKnown = true
}

func (m *Manager) settleOutcome(s *Swap, st *Status) {
	switch {
	case st.Sol.Settled && st.Znn.Settled:
		st.Outcome = "settled -- both legs claimed against one secret"
	case st.Sol.Refunded && st.Znn.Refunded:
		st.Outcome = "refunded -- both legs returned to their senders"
	case st.Sol.Settled && st.Znn.Refunded, st.Sol.Refunded && st.Znn.Settled:
		st.Outcome = "split -- one leg settled and the other refunded, which should not happen; keep this record"
	}
	if st.Outcome != "" && !s.Archived {
		s.Archived = true
		s.Outcome = st.Outcome
	}
}

func formatSol(lamports uint64) string {
	return znn.FormatAmount(new(big.Int).SetUint64(lamports), 9) + " SOL"
}

func joinProblems(p []string) string { return strings.Join(p, "; ") }

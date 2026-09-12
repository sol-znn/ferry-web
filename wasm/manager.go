package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/zenon/ferry-web-v2/wasm/chain"
	"github.com/zenon/ferry-web-v2/wasm/sol"
	"github.com/zenon/ferry-web-v2/wasm/znn"
)

// Manager holds the pieces a swap needs and implements the operations the API
// exposes. It owns no keys beyond what is inside each Swap record.
//
// One is assembled per call from the settings that call carried. The browser is
// the only party, so there is no operator whose node choice anybody would need
// to override.
type Manager struct {
	Store   *Store
	Chain   chain.Backend
	Znn     *znn.Client
	Sol     *sol.Client
	Network string
}

// leg returns one side of the trade.
func (s *Swap) leg(dir Dir) *Leg {
	if dir == DirOut {
		return s.Out
	}
	return s.In
}

// other returns the leg this one is not.
func (s *Swap) other(l *Leg) *Leg {
	if l == nil {
		return nil
	}
	if l.Dir == DirOut {
		return s.In
	}
	return s.Out
}

// logOnce writes a line only if the log does not already carry it, so a note
// emitted from Refresh appears once rather than on every poll.
func (s *Swap) logOnce(msg string) {
	if !s.loggedOnce(msg) {
		s.log("%s", msg)
	}
}

// CreateLeg is one half of a proposed trade, as the form sends it.
type CreateLeg struct {
	Chain ChainID `json:"chain"`
	// Token is the ZTS, on a Zenon leg. Blank means ZNN.
	Token string `json:"token,omitempty"`
	// Amount as typed, in the chain's unit: satoshi, SOL, whole tokens.
	Amount string `json:"amount"`
	// SelfAddr is this user's address on this chain — where their half of this
	// leg lands. PeerAddr is the counterparty's.
	SelfAddr string `json:"selfAddr,omitempty"`
	PeerAddr string `json:"peerAddr,omitempty"`
	// PeerPKH is the counterparty's Bitcoin pubkey hash, when it is already
	// known. Required up front only when this side funds a Bitcoin contract.
	PeerPKH string `json:"peerPkh,omitempty"`
	// Program is the Solana deployment this leg settles on, and SwapID the PDA
	// seed. Both travel in the offer so the two sides derive the same escrow.
	Program string `json:"program,omitempty"`
	SwapID  string `json:"swapId,omitempty"`
}

// CreateParams are the inputs for starting a new swap.
type CreateParams struct {
	Role Role `json:"role"`
	// Out is the leg this user funds; In is the one they receive.
	Out CreateLeg `json:"out"`
	In  CreateLeg `json:"in"`

	// LockHours overrides the schedule below for this user's own leg. Rarely
	// used; the defaults are what keeps two independent browsers agreeing.
	LockHours int `json:"lockHours,omitempty"`

	// SecretHashHex is required when this side is the participant: the initiator
	// chose the secret, so the participant only ever sees its hash.
	SecretHashHex string `json:"secretHashHex,omitempty"`

	// Offer is the swapoffer2 string this form was filled in from, when it was
	// filled in from one. Present, it makes the create a JOIN rather than a
	// proposal, and every term it pins is checked against the rest of these
	// params. See Offer.CheckCreate.
	Offer string `json:"offer,omitempty"`
}

// The lock durations follow the convention the atomic swap literature uses: the
// initiator waits twice as long as the participant, who must be able to refund
// strictly first — otherwise the initiator could reveal the secret at the last
// moment and claim both legs.
//
// These are per LEG, not per user: whichever leg is the initiator's takes the
// long one, on whatever chain it lands.
const (
	initiatorLockHours   = 48
	participantLockHours = 24
)

// defaultLegGap is the gap this app puts between the two legs when it computes
// an expiry itself. MinLegGap is the floor it will accept from a counterparty;
// this is what it asks for.
const defaultLegGap = time.Duration(initiatorLockHours-participantLockHours) * time.Hour

// maxLockHours bounds a caller-supplied timelock. A year is far beyond any real
// swap and keeps the value inside the range nLockTime can carry.
const maxLockHours = 24 * 365

// legHours is how long a leg should run for, given whose it is.
func legHours(isInitiators bool) int {
	if isInitiators {
		return initiatorLockHours
	}
	return participantLockHours
}

func legOwner(isInitiators bool) string {
	if isInitiators {
		return "initiator's"
	}
	return "participant's"
}

// Create starts a new swap and persists it.
func (m *Manager) Create(p CreateParams) (*Swap, error) {
	if p.Role != RoleInitiator && p.Role != RoleParticipant {
		return nil, fmt.Errorf("role must be %q or %q", RoleInitiator, RoleParticipant)
	}
	if err := CheckPair(p.Out.Chain, p.In.Chain, p.Out.Token, p.In.Token); err != nil {
		return nil, err
	}
	// Before anything is generated: a swap that disagrees with the offer it
	// answers is not a swap with a fixable field, it is the wrong swap, and the
	// cheapest moment to say so is the one where nothing exists yet.
	if strings.TrimSpace(p.Offer) != "" {
		offer, err := DecodeOffer(strings.TrimSpace(p.Offer))
		if err != nil {
			return nil, fmt.Errorf("the offer this was filled in from no longer decodes: %w", err)
		}
		if err := offer.CheckCreate(m.Network, p); err != nil {
			return nil, err
		}
	}

	id, err := NewID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	sw := &Swap{
		ID:        id,
		CreatedAt: now,
		UpdatedAt: now,
		Network:   m.Network,
		Role:      p.Role,
		State:     StateDraft,
	}

	switch p.Role {
	case RoleInitiator:
		secret, hash, err := NewSecret()
		if err != nil {
			return nil, err
		}
		sw.Secret, sw.SecretHash = secret, hash
		sw.log("generated secret, hash %s", hex.EncodeToString(hash))
	case RoleParticipant:
		if p.SecretHashHex == "" {
			return nil, errors.New("participant needs the initiator's secret hash")
		}
		hash, err := hex.DecodeString(p.SecretHashHex)
		if err != nil || len(hash) != 32 {
			return nil, errors.New("secretHashHex must be 32 bytes of hex")
		}
		sw.SecretHash = hash
		sw.log("using initiator's secret hash %s", p.SecretHashHex)
	}

	// This user's own leg takes the duration its role implies. The counterparty's
	// deadline is theirs to choose and ours to check, so it stays unset until it
	// is a fact rather than a guess.
	hours := p.LockHours
	if hours <= 0 {
		hours = legHours(sw.OutIsInitiators())
	}
	// The floor is MinLegRemaining plus room, not 1 and not MinLegRemaining
	// exactly. Two reasons, and the second is the one that bites:
	//
	// Below MinLegRemaining, planning refuses to advise a duration nothing would
	// act on, so the leg is accepted here and then cannot be built at all — the
	// card asks for a refresh that will never produce one.
	//
	// AT MinLegRemaining it is worse, because it looks fine: the swap is
	// created, and by the time the funding button is pressed a minute later the
	// leg is under the floor and funding is refused with a figure like "1h58m55s
	// away, under the 2h0m0s minimum". A deadline has to still clear the floor
	// when the money actually moves, which is never the instant it was typed.
	minHours := int(MinLegRemaining/time.Hour) + 1
	if hours < minHours || hours > maxLockHours {
		return nil, fmt.Errorf("lockHours must be between %d and %d: a leg needs %s left when it "+
			"is funded, and funding happens some minutes after this form is submitted",
			minHours, maxLockHours, MinLegRemaining)
	}
	outExpiry := now.Add(time.Duration(hours) * time.Hour).Unix()

	if sw.Out, err = m.newLeg(sw, p.Out, DirOut, outExpiry); err != nil {
		return nil, fmt.Errorf("the leg you send: %w", err)
	}
	if p.LockHours > 0 {
		sw.Out.LockHours = p.LockHours
	}
	if sw.In, err = m.newLeg(sw, p.In, DirIn, 0); err != nil {
		return nil, fmt.Errorf("the leg you receive: %w", err)
	}
	sw.planOutLeg(now)

	// When this side funds a Bitcoin contract and already knows the
	// counterparty's pubkey hash, the contract can be built immediately.
	if sw.Out.Chain == ChainBTC && len(sw.Out.Btc.CounterpartyPKH) == 20 {
		if err := sw.buildBtcContract(sw.Out); err != nil {
			return nil, err
		}
	}
	// A Zenon leg longer than 24 hours cannot be created with znn-cli, which
	// caps its expirationTime argument. Nothing on chain forbids it, so this is a
	// note about tooling rather than an unsupported swap.
	if sw.Out.Chain == ChainZNN && sw.Out.Znn.ExpirationHours > ZnnCliMaxHours {
		sw.log("NOTE: your Zenon leg runs for %dh, over znn-cli's 1..%dh cap on htlc.create. "+
			"Use the Syrius extension for it; the cap is a CLI restriction, not a protocol one.",
			sw.Out.Znn.ExpirationHours, ZnnCliMaxHours)
	}

	sw.log("swap created: %s, role=%s, sending %s and receiving %s. Your leg is the %s one and "+
		"runs until %s", sw.Pair(), sw.Role, sw.Out.Label(), sw.In.Label(),
		legOwner(sw.OutIsInitiators()), utcTime(sw.Out.Expiry))
	if err := m.Store.Save(sw); err != nil {
		return nil, err
	}
	return sw, nil
}

// newLeg validates one half of a create and builds the record for it.
func (m *Manager) newLeg(sw *Swap, in CreateLeg, dir Dir, expiry int64) (*Leg, error) {
	c, err := ChainOf(in.Chain)
	if err != nil {
		return nil, err
	}
	if err := checkTypedAmount(in.Chain, in.Token, in.Amount); err != nil {
		return nil, err
	}
	l := &Leg{
		Chain:    in.Chain,
		Dir:      dir,
		Amount:   strings.TrimSpace(in.Amount),
		Expiry:   expiry,
		SelfAddr: strings.TrimSpace(in.SelfAddr),
		PeerAddr: strings.TrimSpace(in.PeerAddr),
	}

	switch in.Chain {
	case ChainBTC:
		params, err := NetworkParams(m.Network)
		if err != nil {
			return nil, err
		}
		// Required, not optional. Both branches of the contract pay this address
		// and there is no other. Without one the refund cannot be pre-signed when
		// funding appears, and neither Redeem nor Refund can build anything —
		// which is a swap that can be funded and then not spent, discovered after
		// the money is already in the contract.
		if l.SelfAddr == "" {
			return nil, errors.New("a Bitcoin destination address is required: it is where both " +
				"branches of the contract pay, so a swap without one can be funded but not spent")
		}
		if _, err := addressScript(l.SelfAddr, params); err != nil {
			return nil, err
		}
		amt, ok := new(big.Int).SetString(l.Amount, 10)
		if !ok || amt.Sign() <= 0 || !amt.IsInt64() {
			return nil, fmt.Errorf("%q is not a number of satoshi", l.Amount)
		}
		l.Base = amt.String()
		key, err := NewSwapKey()
		if err != nil {
			return nil, err
		}
		l.Btc = &BtcLeg{Key: key}
		if in.PeerPKH != "" {
			pkh, err := hex.DecodeString(strings.TrimSpace(in.PeerPKH))
			if err != nil || len(pkh) != 20 {
				return nil, errors.New("the counterparty pubkey hash must be 20 bytes of hex")
			}
			l.Btc.CounterpartyPKH = pkh
		}
		if expiry != 0 {
			if err := validLockTime(expiry); err != nil {
				return nil, err
			}
		}

	case ChainZNN:
		// The addresses are routinely filled in later, but a value that IS given
		// has to be the kind of thing the field asks for. A Zenon address and a
		// ZTS are both bech32 and start with the same letter, which is exactly
		// the shape of accident a paste between similar fields produces.
		if l.SelfAddr != "" {
			if _, err := znn.ParseAddress(l.SelfAddr); err != nil {
				return nil, fmt.Errorf("your Zenon address: %w", err)
			}
		}
		if l.PeerAddr != "" {
			if _, err := znn.ParseAddress(l.PeerAddr); err != nil {
				return nil, fmt.Errorf("their Zenon address: %w", err)
			}
		}
		if t := strings.TrimSpace(in.Token); t != "" {
			if _, err := znn.ParseTokenStandard(t); err != nil {
				return nil, fmt.Errorf("Zenon token: %w", err)
			}
		}
		// The form's blank token field means ZNN. Resolving it now means the
		// record, the printed command and the check all name the same token.
		l.Token = resolveToken(in.Token)
		l.Znn = &ZnnLeg{HashType: znn.HashTypeSHA256, KeyMaxSize: SecretSize}

	case ChainSOL:
		if l.SelfAddr == "" {
			return nil, errors.New("a Solana address is required: it is where this leg pays, " +
				"and the program fixes it at creation so it cannot be supplied later")
		}
		if _, err := sol.ParsePubkey(l.SelfAddr); err != nil {
			return nil, fmt.Errorf("your Solana address: %w", err)
		}
		if l.PeerAddr != "" {
			if _, err := sol.ParsePubkey(l.PeerAddr); err != nil {
				return nil, fmt.Errorf("their Solana address: %w", err)
			}
		}
		program := strings.TrimSpace(in.Program)
		if program == "" {
			return nil, errors.New("this swap does not name a Solana program. Set one in Node " +
				"settings: an escrow on one deployment is not an escrow on another, so both " +
				"sides have to agree which")
		}
		if _, err := sol.ParsePubkey(program); err != nil {
			return nil, fmt.Errorf("the Solana program address: %w", err)
		}
		lamports, err := solLamports(l.Amount)
		if err != nil {
			return nil, err
		}
		l.Base = new(big.Int).SetUint64(lamports).String()
		// The swap id is the PDA seed and so must be identical on both sides. A
		// joining side takes the one from the offer; a proposing side mints one.
		swapID := strings.TrimSpace(in.SwapID)
		if swapID == "" {
			raw, err := NewID()
			if err != nil {
				return nil, err
			}
			swapID = hex.EncodeToString(SHA256([]byte("ferry-sol-swap-v1:" + raw)))
		}
		if raw, err := hex.DecodeString(swapID); err != nil || len(raw) != 32 {
			return nil, errors.New("the Solana swap id must be 32 bytes of hex")
		}
		l.Sol = &SolLeg{ProgramID: program, SwapID: swapID, Lamports: lamports}
		if addr, err := l.deriveEscrow(); err == nil {
			l.Sol.Escrow = addr.String()
		}
	}
	_ = c
	return l, nil
}

// planOutLeg records the deadline this user's own leg should be created with,
// derived from the counterparty's once that is a fact.
//
// Derived rather than fixed because the requirement is a RELATIONSHIP between
// the legs: the initiator's must outlive the participant's. So a counterparty
// who picked an unusual deadline still gets a correctly ordered leg back rather
// than a hardcoded 24 hours.
//
// It only ever moves a leg that has not been committed to yet — no Bitcoin
// contract built, no HTLC created, no escrow funded. Past that point what the
// expiry should have been is history, and rewriting the advice under the user
// would only confuse.
func (s *Swap) planOutLeg(now time.Time) {
	out, in := s.Out, s.In
	if out == nil || in == nil {
		return
	}
	if committed(out) {
		return
	}
	// What the user asked for, when they asked for anything. Without this the
	// deadline field on the create form was written down and then immediately
	// overwritten here with the role's default -- the value was validated,
	// stored on the leg, and never survived the next line.
	hours := legHours(s.OutIsInitiators())
	if out.LockHours > 0 {
		hours = out.LockHours
	}
	target := now.Add(time.Duration(hours) * time.Hour)
	if in.Expiry > 0 && in.Verified() {
		theirs := time.Unix(in.Expiry, 0)
		if s.OutIsInitiators() {
			target = theirs.Add(defaultLegGap)
		} else {
			target = theirs.Add(-defaultLegGap)
		}
	}
	secs := int(target.Sub(now) / time.Second)
	if secs < int(MinLegRemaining/time.Second) {
		// Advising an expiry that verification would reject anyway is worse than
		// admitting there is no room.
		return
	}
	out.Expiry = target.Unix()
	if out.Chain == ChainZNN && out.Znn != nil {
		out.Znn.ExpirationSeconds = secs
		out.Znn.ExpirationHours = wholeHours(secs, int(time.Unix(in.Expiry, 0).Sub(now)/time.Second),
			s.OutIsInitiators(), in.Expiry > 0)
	}
}

// committed reports whether a leg has reached the point where its deadline is
// fixed by something on a chain rather than by a plan.
func committed(l *Leg) bool {
	switch l.Chain {
	case ChainBTC:
		return l.Btc != nil && len(l.Btc.Contract) > 0
	case ChainZNN:
		return l.Znn != nil && l.Znn.HtlcID != ""
	case ChainSOL:
		return l.Sol != nil && (l.Sol.Funded || l.Sol.CreateSig != "")
	}
	return false
}

// wholeHours renders a planned expiry as the whole-hour figure znn-cli takes.
//
// Round to nearest, because a target one second under 24h should read as 24h —
// then clamp into the range that keeps MinLegGap between the legs, because
// rounding must never push a leg past the ordering constraint. Zero means no
// whole hour satisfies it and the caller should use the seconds form.
func wholeHours(secs, otherSecs int, isInitiatorLeg, otherKnown bool) int {
	hours := (secs + 1800) / 3600
	if !otherKnown {
		if hours < 1 {
			return 0
		}
		return hours
	}
	gap := int(MinLegGap / time.Second)
	if isInitiatorLeg {
		// Must expire after the other leg: never round below the floor.
		if earliest := (otherSecs + gap + 3599) / 3600; hours < earliest {
			hours = earliest
		}
		return hours
	}
	// Must expire before the other leg: never round above the ceiling.
	if latest := (otherSecs - gap) / 3600; hours > latest {
		hours = latest
	}
	if hours < 1 {
		return 0
	}
	return hours
}

// auditLegOrdering enforces the invariant the whole swap rests on: the
// initiator's leg expires LAST. Otherwise the initiator could let their own leg
// expire, reclaim it, and still claim the participant's leg with the secret —
// taking both sides.
//
// `l` is the leg whose deadline has just become known and `expiry` is what it
// turned out to be.
func (s *Swap) auditLegOrdering(l *Leg, expiry int64) error {
	remaining := time.Until(time.Unix(expiry, 0))
	if remaining < MinLegRemaining {
		return fmt.Errorf("that deadline is only %s away, under the %s minimum needed to act on "+
			"it; refuse it and ask the counterparty to rebuild with a longer one",
			remaining.Truncate(time.Minute), MinLegRemaining)
	}
	other := s.other(l)
	if s.legIsInitiators(l.Dir) {
		// This is the long leg and the other goes under it, so it has to be long
		// enough to fit one that is still worth acting on after the gap.
		if need := MinLegGap + MinLegRemaining; remaining < need {
			return fmt.Errorf("that deadline is %s away, but this is the initiator's leg: the "+
				"other must fit under it, expiring at least %s earlier and still leaving %s to "+
				"act, so at least %s is needed. Ask for a longer one",
				remaining.Truncate(time.Minute), MinLegGap, MinLegRemaining, need)
		}
		return nil
	}
	// This is the short leg, so the other one — created first, being the
	// initiator's — has to outlive it. An expiry recorded from a leg that failed
	// verification is not a fact about this swap: treat it as absent.
	if other == nil || other.Expiry == 0 || !other.Verified() {
		// Say so rather than passing silently: a skipped check reads exactly
		// like a passed one.
		s.log("NOTE: this is the participant's leg and must expire before your own, but ferry "+
			"has not verified yours yet so it could not check. Verify it and re-audit before "+
			"funding anything. Yours must expire after %s.",
			utcTime(expiry+int64(MinLegGap/time.Second)))
		return nil
	}
	if earliest := expiry + int64(MinLegGap/time.Second); other.Expiry < earliest {
		return fmt.Errorf("their leg expires at %s but yours expires at %s: as the initiator "+
			"yours must expire LAST, by at least %s. Accepting this would let the counterparty "+
			"reclaim their side and still claim yours. Ask them to rebuild with a deadline no "+
			"later than %s",
			utcTime(expiry), utcTime(other.Expiry), MinLegGap,
			utcTime(other.Expiry-int64(MinLegGap/time.Second)))
	}
	return nil
}

// readyToFund is the rule about ORDER, enforced in the engine rather than in the
// page: the participant funds only after the initiator's leg is on a chain and
// has been checked.
//
// The initiator goes first by construction — nothing exists for them to check —
// so for them this only asks that the counterparty's address is known, which is
// what their own contract has to pay.
func (s *Swap) readyToFund() error {
	if s.In == nil || s.Out == nil {
		return errors.New("this swap record is incomplete")
	}
	if s.Role == RoleInitiator {
		if s.Out.Chain != ChainBTC && strings.TrimSpace(s.Out.PeerAddr) == "" {
			return fmt.Errorf("this swap has no counterparty %s address, so there is nobody to "+
				"lock the money to", ChainLabel(s.Out.Chain))
		}
		return nil
	}
	if !s.In.Funded() {
		return fmt.Errorf("the counterparty's %s leg is not on chain yet. As the participant you "+
			"fund second: locking your side against a leg that does not exist is the one move "+
			"this app will not help you make", ChainLabel(s.In.Chain))
	}
	if !s.In.Verified() {
		return fmt.Errorf("the counterparty's %s leg has not passed verification. Check it holds "+
			"the agreed amount, pays you, and expires late enough before locking your own",
			ChainLabel(s.In.Chain))
	}
	return nil
}

// ---------- refresh ----------

// Refresh polls both chains and advances the swap.
//
// It is safe to call repeatedly and is the only thing that moves a swap forward
// automatically: funding detected, refund transaction pre-signed, counterparty's
// claim spotted and the secret extracted from it.
func (m *Manager) Refresh(ctx context.Context, id string) (*Swap, error) {
	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, err
	}
	for _, l := range []*Leg{sw.In, sw.Out} {
		if l == nil {
			continue
		}
		switch l.Chain {
		case ChainBTC:
			m.refreshBtc(ctx, sw, l)
		case ChainZNN:
			m.refreshZnn(ctx, sw, l)
		case ChainSOL:
			m.refreshSol(ctx, sw, l)
		}
	}
	sw.settle()
	if err := m.Store.Save(sw); err != nil {
		return nil, err
	}
	return sw, nil
}

// settle recomputes the swap-level state from what the legs now say.
//
// One state for two legs, so the rule has to be explicit about which leg wins.
// Settled beats everything: both legs stopped moving. Otherwise an expired
// outgoing leg is the loudest thing on the card, because it is the one with a
// button behind it.
func (s *Swap) settle() {
	now := time.Now().Unix()
	switch {
	case s.Settled():
		if s.Out != nil && s.Out.Chain == ChainBTC && s.Out.Btc != nil && s.Out.Btc.Reclaimed {
			s.State = StateRefunded
		} else if legReclaimed(s.Out) {
			s.State = StateRefunded
		} else {
			s.State = StateSettled
		}
	// This user took their own leg back and the counterparty never committed
	// anything. Nothing is owed either way, so the swap is over — without this
	// it stayed "funded" for good, because a reclaimed Bitcoin contract still
	// has a funding output hanging off it.
	case legReclaimed(s.Out) && (s.In == nil || !s.In.Funded()):
		s.State = StateRefunded
	case s.Out != nil && s.Out.Funded() && s.Out.Expiry > 0 && now >= s.Out.Expiry:
		if s.State != StateExpired {
			s.log("your own leg's deadline has passed; you can take it back now%s",
				expiryFootnote(s.Out))
		}
		s.State = StateExpired
	case (s.Out != nil && s.Out.Funded()) || (s.In != nil && s.In.Funded()):
		s.State = StateFunded
	case s.State == StateDraft && s.In != nil && s.Out != nil && describable(s.Out) && describable(s.In):
		s.State = StateAwaiting
	}
}

// expiryFootnote adds the one thing about a Bitcoin refund that surprises
// people: nLockTime is compared against the block median time past, which trails
// real time by about an hour.
func expiryFootnote(l *Leg) string {
	if l != nil && l.Chain == ChainBTC {
		return " (Bitcoin compares the refund against the block median time past, which trails " +
			"real time by about an hour, so the network may refuse it for a little longer)"
	}
	return ""
}

func legReclaimed(l *Leg) bool {
	if l == nil {
		return false
	}
	switch l.Chain {
	case ChainBTC:
		return l.Btc != nil && l.Btc.Reclaimed
	case ChainZNN:
		return l.Znn != nil && l.Znn.ReclaimHash != ""
	case ChainSOL:
		return l.Sol != nil && l.Sol.Refunded
	}
	return false
}

// describable reports whether a leg knows enough for the swap to be more than a
// draft: an amount, and wherever the money is meant to land.
func describable(l *Leg) bool {
	if l == nil || strings.TrimSpace(l.Amount) == "" {
		return false
	}
	switch l.Chain {
	case ChainBTC:
		return l.Btc != nil && (len(l.Btc.Contract) > 0 || len(l.Btc.CounterpartyPKH) == 20)
	case ChainZNN:
		return l.SelfAddr != "" && l.PeerAddr != ""
	case ChainSOL:
		return l.SelfAddr != "" && l.PeerAddr != ""
	}
	return false
}

// ---------- odds and ends ----------

// SetSecret records a preimage the user observed elsewhere.
//
// Only accepted if it hashes to this swap's secret hash, so a wrong or malicious
// paste cannot get a broken transaction built.
func (m *Manager) SetSecret(id, secretHex string) (*Swap, error) {
	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, err
	}
	secret, err := hex.DecodeString(strings.TrimSpace(secretHex))
	if err != nil {
		return nil, fmt.Errorf("secret must be hex: %w", err)
	}
	if len(secret) != SecretSize {
		return nil, fmt.Errorf("secret must be %d bytes, got %d", SecretSize, len(secret))
	}
	if !bytes.Equal(SHA256(secret), sw.SecretHash) {
		return nil, fmt.Errorf("that preimage hashes to %s, but this swap commits to %s",
			hex.EncodeToString(SHA256(secret)), hex.EncodeToString(sw.SecretHash))
	}
	sw.Secret = secret
	sw.log("secret supplied by hand and verified against this swap's hash")
	if err := m.Store.Save(sw); err != nil {
		return nil, err
	}
	return sw, nil
}

// Archive files a swap away into the history list, or brings it back. Nothing
// about it touches a chain: it only decides which list the swap shows up in.
func (m *Manager) Archive(id string, archived bool) (*Swap, error) {
	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, err
	}
	if sw.Archived == archived {
		return sw, nil
	}
	sw.Archived = archived
	if archived {
		sw.log("archived: moved to the history list")
	} else {
		sw.log("un-archived: moved back to the active list")
	}
	if err := m.Store.Save(sw); err != nil {
		return nil, err
	}
	return sw, nil
}

// decimalToBaseUnits converts a human decimal amount ("1.25") into integer base
// units, without going through a float.
func decimalToBaseUnits(s string, decimals int) (*big.Int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("amount is empty")
	}
	if strings.HasPrefix(s, "-") {
		return nil, errors.New("amount must not be negative")
	}
	if decimals < 0 || decimals > 30 {
		return nil, fmt.Errorf("token reports an implausible decimals value of %d", decimals)
	}

	intPart, frac, _ := strings.Cut(s, ".")
	if len(frac) > decimals {
		return nil, fmt.Errorf("amount has %d decimal places but the unit has only %d",
			len(frac), decimals)
	}
	frac += strings.Repeat("0", decimals-len(frac))

	combined := intPart + frac
	if combined == "" {
		return nil, fmt.Errorf("%q is not a valid amount", s)
	}
	v, ok := new(big.Int).SetString(combined, 10)
	if !ok {
		return nil, fmt.Errorf("%q is not a valid decimal amount", s)
	}
	return v, nil
}

// utcTime renders a unix timestamp the one way this app writes them.
func utcTime(unix int64) string {
	if unix == 0 {
		return "not set"
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

func orElse(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

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

// Config is the two node URLs and the deployed program. None of it has a
// default that reaches a stranger's server: verifying a counterparty's leg is
// the one place a dishonest answer costs money, so the node has to be one the
// user named.
type Config struct {
	SolanaURL  string `json:"solanaUrl"`
	ZenonURL   string `json:"zenonUrl"`
	SolProgram string `json:"solProgram"`
}

// Manager owns the store and the two clients.
type Manager struct {
	cfg   Config
	store *Store

	// The configured cluster's genesis hash, and the URL it was read from.
	// See solGenesis.
	genesisURL  string
	genesisHash string
}

func NewManager(store *Store) *Manager { return &Manager{store: store} }

func (m *Manager) SetConfig(c Config) { m.cfg = c }
func (m *Manager) Config() Config     { return m.cfg }

func (m *Manager) sol() *sol.Client { return sol.New(m.cfg.SolanaURL) }
func (m *Manager) znn() *znn.Client { return znn.New(m.cfg.ZenonURL) }

// CreateParams is what the maker fills in.
//
// The maker is always the initiator. That is a simplification and it is the
// right one: the initiator is whoever invents the secret, an offer has to
// commit to that secret's hash, and a maker who did not invent it would have to
// obtain the hash from the taker first -- an extra round trip that buys nothing,
// since either party can be the maker and so either party can initiate.
type CreateParams struct {
	// SendsSol is the direction: true means this side locks SOL and is paid
	// ZNN.
	SendsSol bool `json:"sendsSol"`

	// SolAddress is this side's Solana wallet. It funds the escrow when this
	// side sends SOL, and is paid by it when this side receives.
	SolAddress string `json:"solAddress"`
	// ZnnAddress is this side's Zenon wallet -- a real Syrius address. When
	// this side receives ZNN it is the payout address; when it sends, it is
	// where a reclaimed swap address is swept back to.
	ZnnAddress string `json:"znnAddress"`

	SolAmount string `json:"solAmount"` // decimal SOL
	ZnnAmount string `json:"znnAmount"` // decimal, in the token's own units
	ZnnToken  string `json:"znnToken"`  // ZNN, QSR or a zts

	// LongSeconds and ShortSeconds override the 48h/24h defaults, which is what
	// makes an end-to-end test possible in less than two days.
	LongSeconds  int64 `json:"longSeconds"`
	ShortSeconds int64 `json:"shortSeconds"`
}

// Create makes a swap and its offer.
func (m *Manager) Create(ctx context.Context, p CreateParams) (*Swap, error) {
	programID, err := sol.ParsePubkey(m.cfg.SolProgram)
	if err != nil {
		return nil, fmt.Errorf("the swap program id is not usable: %w", err)
	}
	long, short := p.LongSeconds, p.ShortSeconds
	if long <= 0 {
		long = DefaultLongSeconds
	}
	if short <= 0 {
		short = DefaultShortSeconds
	}
	if long-short < MinLegGapSeconds {
		return nil, fmt.Errorf(
			"the two timelocks are %s apart, and a swap needs at least %s between them",
			humanDuration(long-short), humanDuration(MinLegGapSeconds))
	}

	lamports, err := parseSol(p.SolAmount)
	if err != nil {
		return nil, err
	}
	token, err := m.resolveToken(ctx, p.ZnnToken)
	if err != nil {
		return nil, err
	}
	znnAmount, err := znn.ParseAmount(p.ZnnAmount, token.Decimals)
	if err != nil {
		return nil, fmt.Errorf("the %s amount: %w", token.Symbol, err)
	}
	if znnAmount.Sign() <= 0 {
		return nil, fmt.Errorf("the %s amount must be positive", token.Symbol)
	}

	myZnn, err := zt.ParseAddress(strings.TrimSpace(p.ZnnAddress))
	if err != nil {
		return nil, fmt.Errorf("your Zenon address: %w", err)
	}
	mySol, err := sol.ParsePubkey(strings.TrimSpace(p.SolAddress))
	if err != nil {
		return nil, fmt.Errorf("your Solana address: %w", err)
	}

	// Both clocks, because the two deadlines live on different ones and each
	// has to be computed against its own.
	solNow, err := m.sol().Now(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading Solana's clock: %w", err)
	}
	znnMomentum, err := m.znn().FrontierMomentum(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading Zenon's clock: %w", err)
	}
	// Which two chains this is. Read here, at the only moment the answer is
	// unambiguous, and carried in the terms from then on -- see Terms.SolGenesis.
	solGenesis, err := m.sol().GenesisHash(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading Solana's genesis hash: %w", err)
	}
	if znnMomentum.ChainIdentifier == 0 {
		return nil, fmt.Errorf(
			"the Zenon node did not report a chain identifier, so this swap cannot record which chain it is on")
	}
	if skew := solNow - znnMomentum.Timestamp; skew > MaxClockSkewSeconds || skew < -MaxClockSkewSeconds {
		return nil, fmt.Errorf(
			"the two chains' clocks are %s apart, which is more than the %s this arithmetic assumes -- "+
				"the leg ordering cannot be checked reliably",
			humanDuration(abs64(skew)), humanDuration(MaxClockSkewSeconds))
	}

	swapID, err := NewSwapID()
	if err != nil {
		return nil, err
	}
	secret, hashlock, err := NewSecret()
	if err != nil {
		return nil, err
	}
	swapKey, err := znn.NewKey()
	if err != nil {
		return nil, err
	}

	role := Role{SendsSol: p.SendsSol, Initiator: true}
	terms := Terms{
		SwapID:      swapID,
		Hashlock:    hashlock,
		Initiator:   role.MyLeg(),
		SolGenesis:  solGenesis,
		ZnnChainID:  znnMomentum.ChainIdentifier,
		SolProgram:  programID.String(),
		SolLamports: lamports,
		ZnnToken:    token.TokenStandard,
		ZnnAmount:   znnAmount.String(),
		ZnnDecimals: token.Decimals,
	}
	if p.SendsSol {
		terms.SolSender = mySol.String()
		terms.ZnnReceiver = myZnn.String()
		terms.SolTimelock = solNow + long
		terms.ZnnExpiry = znnMomentum.Timestamp + short
	} else {
		terms.SolReceiver = mySol.String()
		// The address that creates the Zenon HTLC is this per-swap key, not the
		// user's wallet: only the creator can reclaim after expiry, and Syrius
		// cannot make the call. See keys.go for what this key can and cannot do.
		terms.ZnnSender = swapKey.Address.String()
		terms.ZnnExpiry = znnMomentum.Timestamp + long
		terms.SolTimelock = solNow + short
	}

	s := &Swap{
		ID:          swapID,
		Created:     time.Now().Unix(),
		Updated:     time.Now().Unix(),
		Terms:       terms,
		Role:        role,
		Secret:      secret,
		ZnnSwapSeed: swapKey.SeedHex(),
	}
	if !p.SendsSol {
		s.ZnnHomeAddress = myZnn.String()
	}
	if err := m.store.Put(s); err != nil {
		return nil, err
	}
	return s, nil
}

// AcceptParams is what the taker fills in: their own two addresses, and the
// maker's offer.
type AcceptParams struct {
	Offer      string `json:"offer"`
	SolAddress string `json:"solAddress"`
	ZnnAddress string `json:"znnAddress"`
}

// Accept turns an offer into a swap on the taker's side.
//
// The taker's side is derived from the offer rather than chosen: an offer that
// names a SolSender is one whose maker sends SOL, so the taker sends ZNN. Asking
// the taker to state a direction as well would only create the chance to state
// it wrongly.
func (m *Manager) Accept(ctx context.Context, p AcceptParams) (*Swap, error) {
	kind, terms, err := DecodeEnvelope(p.Offer)
	if err != nil {
		return nil, err
	}
	if kind != "offer" {
		return nil, fmt.Errorf("that is an acceptance, not an offer -- paste it into the swap you already have")
	}
	if terms.SolProgram != m.cfg.SolProgram {
		return nil, fmt.Errorf(
			"this offer names swap program %s, and you are configured for %s -- "+
				"one of you is pointed at a different deployment",
			terms.SolProgram, m.cfg.SolProgram)
	}
	if existing, _ := m.store.Get(terms.SwapID); existing != nil {
		return nil, fmt.Errorf("you already have a swap with this id")
	}
	// Before anything else about the offer is considered: two people on
	// different chains cannot trade, and every check below would pass anyway
	// because each browser is checking terms against its own chain.
	if err := m.checkChains(ctx, &terms); err != nil {
		return nil, fmt.Errorf("this offer is for a different chain than yours: %w", err)
	}

	makerSendsSol := terms.SolSender != ""
	role := Role{SendsSol: !makerSendsSol, Initiator: false}
	if terms.Initiator != oppositeLeg(role.MyLeg()) {
		return nil, fmt.Errorf(
			"this offer says the %s leg initiates, but its maker is funding the %s leg -- "+
				"the maker of an offer is always the initiator, so it is inconsistent",
			terms.Initiator, oppositeLeg(role.MyLeg()))
	}

	myZnn, err := zt.ParseAddress(strings.TrimSpace(p.ZnnAddress))
	if err != nil {
		return nil, fmt.Errorf("your Zenon address: %w", err)
	}
	mySol, err := sol.ParsePubkey(strings.TrimSpace(p.SolAddress))
	if err != nil {
		return nil, fmt.Errorf("your Solana address: %w", err)
	}
	swapKey, err := znn.NewKey()
	if err != nil {
		return nil, err
	}

	s := &Swap{
		ID:          terms.SwapID,
		Created:     time.Now().Unix(),
		Updated:     time.Now().Unix(),
		Terms:       terms,
		Role:        role,
		ZnnSwapSeed: swapKey.SeedHex(),
	}
	if role.SendsSol {
		s.Terms.SolSender = mySol.String()
		s.Terms.ZnnReceiver = myZnn.String()
	} else {
		s.Terms.SolReceiver = mySol.String()
		s.Terms.ZnnSender = swapKey.Address.String()
		s.ZnnHomeAddress = myZnn.String()
	}

	// The ordering check happens here, before anything is funded, and it is the
	// taker's whole protection: they are the participant, so their leg expires
	// first and the margin is theirs to lose.
	solNow, znnNow, err := m.clocks(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Terms.CheckOrdering(minInt64(solNow, znnNow)); err != nil {
		return nil, fmt.Errorf("this offer is not safe to accept: %w", err)
	}
	if err := m.checkAgreedToken(ctx, &s.Terms); err != nil {
		return nil, err
	}
	if err := m.checkPayoutReachable(ctx, &s.Terms, role); err != nil {
		return nil, err
	}
	if err := m.store.Put(s); err != nil {
		return nil, err
	}
	return s, nil
}

// ApplyAccept completes the maker's copy of the terms from the taker's reply.
//
// The reply is checked against what the maker offered rather than trusted:
// everything the maker set has to come back unchanged, and only the two fields
// the taker is entitled to fill may differ.
func (m *Manager) ApplyAccept(ctx context.Context, swapID, accept string) (*Swap, error) {
	s, err := m.store.Get(swapID)
	if err != nil {
		return nil, err
	}
	kind, theirs, err := DecodeEnvelope(accept)
	if err != nil {
		return nil, err
	}
	if kind != "accept" {
		return nil, fmt.Errorf("that is an offer, not an acceptance")
	}
	if theirs.SwapID != s.Terms.SwapID {
		return nil, fmt.Errorf("that acceptance is for a different swap")
	}

	mine := s.Terms
	if s.Role.SendsSol {
		mine.SolReceiver = theirs.SolReceiver
		mine.ZnnSender = theirs.ZnnSender
	} else {
		mine.SolSender = theirs.SolSender
		mine.ZnnReceiver = theirs.ZnnReceiver
	}
	if mine != theirs {
		return nil, fmt.Errorf(
			"that acceptance changes terms you did not offer -- compare it field by field before using it")
	}
	if !mine.Complete() {
		return nil, fmt.Errorf("that acceptance is still missing an address")
	}
	// The struct comparison above already means the taker cannot have moved
	// ZnnDecimals -- but the value it is holding still came from one node's
	// answer at Create, and this is the last gate before the maker funds.
	if err := m.checkAgreedToken(ctx, &mine); err != nil {
		return nil, err
	}
	if err := m.checkPayoutReachable(ctx, &mine, s.Role); err != nil {
		return nil, err
	}

	s.Terms = mine
	s.Updated = time.Now().Unix()
	if err := m.store.Put(s); err != nil {
		return nil, err
	}
	return s, nil
}

// checkAgreedToken refuses terms whose token metadata is not what this
// browser's own node says it is.
//
// An amount is a number and a unit, and only the number crosses in the offer as
// a number: ZnnAmount is base units, and ZnnDecimals is what turns those back
// into the figure a person reads. Both arrive from the counterparty. So an
// offer saying `ZnnAmount: "10", ZnnDecimals: 0` for real ZNN renders as "10"
// on every screen on both sides -- FormatAmount(10, 0) and FormatAmount(10^9,
// 8) are the same string -- and then verifies perfectly against an HTLC holding
// ten base units, a ten-millionth of what was displayed. Every other check
// passes for free: right token, right hashlock, right parties, right expiry.
//
// The unit is a term of the trade and the chain is the only authority on it, so
// it is read from the chain rather than believed. Here rather than at
// verification because here it costs nothing: nobody has committed anything
// yet.
//
// A node that will not answer is a refusal, not a pass. That is the opposite of
// what checkPayoutReachable does with an unreachable node, and deliberately:
// proxy-unlock defaults to allowed, so failing open there restores the common
// case, whereas failing open here means accepting an amount whose unit was
// never established. A skipped check reads exactly like a passed one.
func (m *Manager) checkAgreedToken(ctx context.Context, t *Terms) error {
	token, err := m.znn().Token(ctx, t.ZnnToken)
	if err != nil {
		return fmt.Errorf(
			"the token %s could not be read from your Zenon node (%w), so the agreed amount has no "+
				"unit this browser can check -- and an amount whose unit was never established is "+
				"not an amount. Set a node you can reach and try again",
			t.ZnnToken, err)
	}
	if token.Decimals != t.ZnnDecimals {
		return fmt.Errorf(
			"this offer says %s has %d decimals and your node says %d -- so its %q would display as "+
				"%s here and %s to them. Do not act on it: the two sides are not agreeing about the "+
				"same amount",
			t.ZnnToken, t.ZnnDecimals, token.Decimals, t.ZnnAmount,
			formatZnnAmount(t.ZnnAmount, token.Decimals), formatZnnAmount(t.ZnnAmount, t.ZnnDecimals))
	}
	return nil
}

// checkPayoutReachable refuses a swap whose ZNN payout address has switched off
// proxy unlock.
//
// The whole Zenon design here depends on the htlc contract's default: anyone may
// push an Unlock, and the contract pays the address recorded in the entry. An
// address that has called DenyProxyUnlock has opted out, and then only that
// address can unlock -- which Syrius cannot do. Catching it here means the
// refusal costs nothing; catching it at settlement would mean catching it after
// the money is locked.
func (m *Manager) checkPayoutReachable(ctx context.Context, t *Terms, role Role) error {
	if t.ZnnReceiver == "" {
		return nil
	}
	addr, err := zt.ParseAddress(t.ZnnReceiver)
	if err != nil {
		return fmt.Errorf("the Zenon payout address is not valid: %w", err)
	}
	allowed, err := m.znn().ProxyUnlockAllowed(ctx, addr)
	if err != nil {
		// A node that will not answer is not evidence of a problem, and
		// refusing here would make an unreachable node look like a hostile
		// counterparty. The check is repeated on every refresh.
		return nil
	}
	if !allowed {
		return fmt.Errorf(
			"%s has denied proxy unlock on the htlc contract, so only that address itself can unlock -- "+
				"and no Zenon wallet can make that call. Use an address that has not denied it",
			t.ZnnReceiver)
	}
	return nil
}

func (m *Manager) clocks(ctx context.Context) (solNow, znnNow int64, err error) {
	solNow, err = m.sol().Now(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("reading Solana's clock: %w", err)
	}
	mom, err := m.znn().FrontierMomentum(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("reading Zenon's clock: %w", err)
	}
	return solNow, mom.Timestamp, nil
}

// solGenesis returns the configured Solana node's genesis hash, cached per URL.
//
// A cluster's genesis hash does not change, and this is asked on every refresh
// of every swap, so caching it turns a per-swap round trip into a per-node one.
// The cache is keyed by URL so that changing the node -- which is the whole
// reason this value is checked -- invalidates it.
func (m *Manager) solGenesis(ctx context.Context) (string, error) {
	url := m.cfg.SolanaURL
	if m.genesisURL == url && m.genesisHash != "" {
		return m.genesisHash, nil
	}
	hash, err := m.sol().GenesisHash(ctx)
	if err != nil {
		return "", err
	}
	m.genesisURL, m.genesisHash = url, hash
	return hash, nil
}

// checkChains compares a swap's recorded chains against the nodes now
// configured.
//
// A node that cannot be asked is not a mismatch -- an unreachable node makes
// every other read fail too, and this one has nothing to add. A node that
// answers with a different chain is.
func (m *Manager) checkChains(ctx context.Context, t *Terms) error {
	if t.SolGenesis == "" && t.ZnnChainID == 0 {
		return nil
	}
	var genesis string
	if t.SolGenesis != "" {
		if g, err := m.solGenesis(ctx); err == nil {
			genesis = g
		}
	}
	var chainID uint64
	if t.ZnnChainID != 0 {
		if mom, err := m.znn().FrontierMomentum(ctx); err == nil {
			chainID = mom.ChainIdentifier
		}
	}
	return t.CheckChains(genesis, chainID)
}

func (m *Manager) resolveToken(ctx context.Context, spec string) (*znn.Token, error) {
	zts := strings.TrimSpace(spec)
	switch strings.ToUpper(zts) {
	case "", "ZNN":
		zts = znn.ZnnTokenStandard
	case "QSR":
		zts = znn.QsrTokenStandard
	}
	t, err := m.znn().Token(ctx, zts)
	if err != nil {
		return nil, fmt.Errorf("looking up token %q: %w", spec, err)
	}
	return t, nil
}

func parseSol(decimal string) (uint64, error) {
	v, err := znn.ParseAmount(decimal, 9)
	if err != nil {
		return 0, fmt.Errorf("the SOL amount: %w", err)
	}
	if v.Sign() <= 0 {
		return 0, fmt.Errorf("the SOL amount must be positive")
	}
	if !v.IsUint64() {
		return 0, fmt.Errorf("the SOL amount is too large")
	}
	return v.Uint64(), nil
}

func oppositeLeg(leg string) string {
	if leg == LegSolana {
		return LegZenon
	}
	return LegSolana
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func hexBytes32(s string) ([32]byte, error) {
	var out [32]byte
	raw, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return out, err
	}
	if len(raw) != 32 {
		return out, fmt.Errorf("expected 32 bytes, got %d", len(raw))
	}
	copy(out[:], raw)
	return out, nil
}

// bigFromString parses a base-units figure, returning zero for anything that
// will not parse.
//
// Zero is the safe direction wherever the result is compared against a chain
// amount -- a real amount never equals it, so the leg is refused. It is the
// unsafe direction wherever the comparison is "do I have enough", because
// everything is at least zero. amountOf below is for those; see
// readSwapAddress.
func bigFromString(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return new(big.Int)
	}
	return v
}

// amountOf parses a base-units figure and says so when it cannot, for the
// callers where an unparseable amount must not read as a satisfied one.
func amountOf(s string) (*big.Int, error) {
	v, ok := new(big.Int).SetString(strings.TrimSpace(s), 10)
	if !ok || v.Sign() < 0 {
		return nil, fmt.Errorf("%q is not a whole number of base units", s)
	}
	return v, nil
}

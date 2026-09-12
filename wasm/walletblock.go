package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zenon/ferry-web/wasm/chain"
	"github.com/zenon/ferry-web/wasm/znn"
)

// The Zenon leg, as a block a browser wallet can sign.
//
// Creating and unlocking an HTLC are signed operations and ferry holds no Zenon
// key. What makes a button possible anyway is that the Syrius extension will
// publish an account block handed to it by a page, and an HTLC call is an
// ordinary send to an embedded contract carrying ABI-encoded data.
//
// The division of labour is what makes it safe to do at all:
//
//	this page       decides toAddress, amount, tokenStandard and data
//	the extension   supplies the account, the public key, the chain position,
//	                the plasma or the proof of work, and the signature
//
// The key never comes near this module, and the user sees the whole block in the
// extension's own window before approving it. Nothing here can move a coin; it
// can only propose that the user's wallet does.
//
// Everything below is therefore about refusing to propose the wrong thing. Each
// action is checked against the swap record -- the same record `verify` checks a
// counterparty's HTLC against -- and against the account the wallet has actually
// selected, because the extension signs with whatever that is and will not be
// told otherwise.

// blockTypeUserSend is go-zenon's BlockTypeUserSend. Every HTLC call is one.
const blockTypeUserSend = 2

// blockVersion is the only account block version go-zenon accepts.
const blockVersion = 1

// zeroHash and zeroToken are the empty values znn-ts-sdk's
// AccountBlockTemplate.fromJson expects to find in the fields it is not being
// given. It parses every field it reads, so a template with anything omitted
// throws inside the extension rather than arriving as a readable error here.
const (
	zeroHash  = "0000000000000000000000000000000000000000000000000000000000000000"
	zeroToken = "zts1qqqqqqqqqqqqqqqqtq587y"
)

// walletBlock is the account block template, in the field names and forms
// znn-ts-sdk's AccountBlockTemplate.toJson produces -- because fromJson is what
// will read it.
//
// The extension overwrites Address, PublicKey, Height, PreviousHash,
// MomentumAcknowledged, FusedPlasma, Difficulty and Nonce. They are present
// because fromJson parses them, not because their values here mean anything --
// except Address, filled with the account the wallet reported, so what the user
// is shown before approving names the account that will actually sign.
type walletBlock struct {
	Version              int    `json:"version"`
	ChainIdentifier      uint64 `json:"chainIdentifier"`
	BlockType            int    `json:"blockType"`
	Hash                 string `json:"hash"`
	PreviousHash         string `json:"previousHash"`
	Height               int    `json:"height"`
	MomentumAcknowledged struct {
		Hash   string `json:"hash"`
		Height int    `json:"height"`
	} `json:"momentumAcknowledged"`
	Address       string `json:"address"`
	ToAddress     string `json:"toAddress"`
	Amount        string `json:"amount"`
	TokenStandard string `json:"tokenStandard"`
	FromBlockHash string `json:"fromBlockHash"`
	Data          string `json:"data"`
	FusedPlasma   int    `json:"fusedPlasma"`
	Difficulty    int    `json:"difficulty"`
	Nonce         string `json:"nonce"`
	PublicKey     string `json:"publicKey"`
	Signature     string `json:"signature"`
}

// newWalletBlock returns a template with every field the SDK will parse set to
// a value it can parse, ready for the four this app actually decides.
func newWalletBlock(chainID uint64, from string) *walletBlock {
	b := &walletBlock{
		Version:         blockVersion,
		ChainIdentifier: chainID,
		BlockType:       blockTypeUserSend,
		Hash:            zeroHash,
		PreviousHash:    zeroHash,
		Address:         from,
		ToAddress:       znn.HtlcContractAddress,
		Amount:          "0",
		TokenStandard:   zeroToken,
		FromBlockHash:   zeroHash,
	}
	b.MomentumAcknowledged.Hash = zeroHash
	return b
}

// walletBlockPlan is one proposed call: the block, and everything the UI needs
// to say what it is before the user approves it in the wallet.
type walletBlockPlan struct {
	Action string       `json:"action"`
	Block  *walletBlock `json:"block"`

	// Summary is a sentence naming what this call does, for the page. The
	// extension shows the raw block; this is what makes it legible beforehand.
	Summary string `json:"summary"`
	// Warnings are things that are true and worth knowing, but do not stop the
	// call. A refusal is an error instead.
	Warnings []string `json:"warnings,omitempty"`

	// Signer is the account this plan was built for, restated so the page can
	// tell that the wallet has not changed account since. It is also what
	// walletSent compares the published block against.
	Signer string `json:"signer"`
	// Sync is the wallet-against-page verdict this plan was allowed by. It is
	// returned rather than merely acted on, so the card can show what was
	// actually proved instead of a green tick that means nothing.
	Sync *walletSyncResult `json:"sync,omitempty"`

	// The decoded terms, so the card can show what is about to be locked
	// without re-deriving any of it in JavaScript.
	HashLocked     string `json:"hashLocked,omitempty"`
	ExpirationTime int64  `json:"expirationTime,omitempty"`
	ExpiresAt      string `json:"expiresAt,omitempty"`
	AmountDisplay  string `json:"amountDisplay,omitempty"`
	TokenSymbol    string `json:"tokenSymbol,omitempty"`
	HtlcID         string `json:"htlcId,omitempty"`
}

type walletBlockReq struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	// From is the address the wallet has selected. It is required because the
	// extension signs with that account and cannot be told to use another: for
	// a create it becomes timeLocked, the only address that will ever be able
	// to reclaim, so it has to be checked against the swap rather than assumed.
	From string `json:"from"`
	// ChainID and NodeURL are what the wallet says about itself. Neither is
	// trusted: they go into walletsync.go, which reads the wallet's node and
	// compares a momentum against this browser's rather than taking either
	// number at face value.
	ChainID  uint64   `json:"chainId,omitempty"`
	NodeURL  string   `json:"nodeUrl,omitempty"`
	Settings Settings `json:"settings"`
}

func handleWalletBlock(ctx context.Context, a *API, body []byte) (any, error) {
	var req walletBlockReq
	mgr, err := withManager(a, body, &req, func(r *walletBlockReq) Settings { return r.Settings })
	if err != nil {
		return nil, err
	}
	if mgr.Znn == nil {
		return nil, errors.New("no Zenon node is set. Open Node settings and give this browser a " +
			"Zenon JSON-RPC URL it can reach — the block has to be built against a chain, and " +
			"the wallet's own node is not one this page can read")
	}
	sw, err := a.Store.Load(req.ID)
	if err != nil {
		return nil, err
	}
	from := strings.TrimSpace(req.From)
	if from == "" {
		return nil, errors.New("connect a Zenon wallet first: the block has to name the account " +
			"that will sign it")
	}

	// The gate, before anything is built: the same function the page calls to
	// display the verdict, run again so a page that skipped the display -- or a
	// wallet that changed account since -- still cannot get a block out of this
	// call. Refusing to BUILD one is the only refusal worth anything; once the
	// block exists, handing it over is a postMessage this module does not mediate.
	sync := runWalletSync(ctx, a, mgr, walletSyncReq{
		ID:      req.ID,
		Action:  req.Action,
		Address: from,
		ChainID: req.ChainID,
		NodeURL: req.NodeURL,
	})
	if !sync.Ok {
		return nil, sync.refusal()
	}

	mom, err := mgr.Znn.FrontierMomentum(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the Zenon chain tip: %w", err)
	}

	plan := &walletBlockPlan{
		Action: req.Action,
		Block:  newWalletBlock(mom.ChainIdentifier, from),
		Signer: from,
		Sync:   sync,
	}
	plan.Warnings = append(plan.Warnings, sync.Warnings...)

	switch req.Action {
	case "create":
		err = planCreate(ctx, mgr, sw, from, mom, plan)
	case "unlock":
		err = planUnlock(ctx, mgr, sw, from, plan)
	case "reclaim":
		err = planReclaim(ctx, mgr, sw, from, mom, plan)
	default:
		return nil, fmt.Errorf("unknown wallet action %q: it is create, unlock or reclaim", req.Action)
	}
	if err != nil {
		return nil, err
	}
	return plan, nil
}

// planCreate builds htlc.Create — this user locking their own ZNN.
func planCreate(ctx context.Context, mgr *Manager, sw *Swap, from string, mom *znn.Momentum,
	plan *walletBlockPlan) error {

	if !sw.ZenonHtlcIsOurs() {
		return errors.New("the counterparty sends ZNN on this swap, so they create the Zenon " +
			"HTLC. There is nothing for you to create")
	}
	if sw.Zenon.HtlcID != "" {
		return fmt.Errorf("this swap already has a Zenon HTLC (%s). Creating a second one would "+
			"lock a second lot of ZNN that only expiry can return", sw.Zenon.HtlcID)
	}
	// The counterparty's Bitcoin contract is what the Zenon expiry has to be
	// ordered against, and it is also the thing that has to be audited before
	// any ZNN moves. Both are the same precondition.
	if len(sw.Contract) == 0 {
		return errors.New("the Bitcoin contract has not been audited yet. Audit it first: the " +
			"Zenon expiry is computed from its locktime, and locking ZNN against a contract you " +
			"have not checked is the one move this app will not help you make")
	}
	// An audited contract is a script; it says nothing about whether money is
	// in it. Where this leg answers the counterparty's Bitcoin funding, that
	// funding has to be real before ZNN is locked against it -- and real is
	// read off the chain now, not off the record.
	if err := requireCounterLegFunding(ctx, mgr.Chain, sw); err != nil {
		return err
	}

	peer := strings.TrimSpace(sw.Zenon.PeerAddress)
	if peer == "" {
		return errors.New("this swap has no counterparty Zenon address, so there is nobody to " +
			"lock the ZNN to. Add theirs to the swap first")
	}
	hashLocked, err := znn.ParseAddress(peer)
	if err != nil {
		return fmt.Errorf("the counterparty's Zenon address is unusable: %w", err)
	}

	// That the wallet has the right account selected -- the one that becomes
	// timeLocked, and therefore the only address that can ever reclaim -- was
	// settled by the sync gate in handleWalletBlock. Checked there rather than
	// here so the account, the chain and the node are one verdict the user reads
	// once, instead of three refusals a wallet-settings trip apart.

	// The expiry is absolute and is measured against the chain's clock, not the
	// browser's: the contract compares expirationTime to momentum time, and a
	// machine a few minutes out would compute a deadline the chain disagrees
	// with. The ordering rule against the Bitcoin leg is the same one
	// VerifyZenon enforces on a counterparty's HTLC, applied here to our own.
	if sw.Zenon.ExpirationSeconds <= 0 {
		return errors.New("this swap does not yet say how long its Zenon leg should run. That is " +
			"computed from the Bitcoin contract's locktime, so refresh the swap first")
	}
	expiration := mom.Timestamp + int64(sw.Zenon.ExpirationSeconds)
	want := zenonVerifyParams(sw)
	if want.MaxExpiration > 0 && expiration > want.MaxExpiration {
		return fmt.Errorf("an HTLC created now would expire at %s, and this leg is the "+
			"participant's so it must expire before %s. The Bitcoin locktime has moved too "+
			"close: refresh the swap, and if the gap has gone, do not fund this leg",
			utcTime(expiration), utcTime(want.MaxExpiration))
	}
	if want.MinExpiration > 0 && expiration < want.MinExpiration {
		return fmt.Errorf("an HTLC created now would expire at %s, and this leg is the "+
			"initiator's so it must outlive the Bitcoin leg's %s. Refresh the swap",
			utcTime(expiration), utcTime(want.MinExpiration))
	}

	// The amount is agreed as the decimal a human typed and locked in the
	// token's base units. It is converted with the decimals of the token that
	// was AGREED, for the same reason VerifyZenon does: the token is a term of
	// the trade, not something to be read off whatever is convenient.
	token := sw.Zenon.AgreedToken()
	tok, err := mgr.Znn.GetToken(ctx, token)
	if err != nil {
		return fmt.Errorf("could not read token %s from the Zenon node, so the amount cannot be "+
			"converted into base units: %w", token, err)
	}
	if strings.TrimSpace(sw.Zenon.AmountDisplay) == "" {
		return errors.New("this swap does not record how much ZNN its Zenon leg is for")
	}
	amount, err := decimalToBaseUnits(sw.Zenon.AmountDisplay, tok.Decimals)
	if err != nil {
		return fmt.Errorf("the agreed amount %q could not be interpreted: %w",
			sw.Zenon.AmountDisplay, err)
	}
	if amount.Sign() <= 0 {
		return fmt.Errorf("the agreed amount %q is not a positive number", sw.Zenon.AmountDisplay)
	}

	// hashType 1 is SHA-256, which is what Bitcoin's OP_SHA256 requires. The
	// other value the contract takes is SHA3, and an HTLC created with it is
	// one the Bitcoin side can never produce a matching preimage for.
	data, err := znn.PackCreate(hashLocked, expiration, znn.HashTypeSHA256, SecretSize, sw.SecretHash)
	if err != nil {
		return err
	}

	// The counterparty will settle this leg by unlocking it, and the contract
	// pays hashLocked rather than the caller — which is what lets them do it
	// from a wallet with no HTLC support. Unless they have opted out of that,
	// in which case they need to unlock from the payee account itself, and it
	// is worth knowing now rather than at settlement.
	if allowed, perr := mgr.Znn.ProxyUnlockAllowed(ctx, peer); perr == nil && !allowed {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"%s has denied proxy unlock, so only that account itself can unlock this HTLC. "+
				"Make sure the counterparty can sign from it.", peer))
	}

	b := plan.Block
	b.Amount = amount.String()
	b.TokenStandard = token
	b.Data = base64.StdEncoding.EncodeToString(data)

	plan.HashLocked = peer
	plan.ExpirationTime = expiration
	plan.ExpiresAt = utcTime(expiration)
	plan.AmountDisplay = sw.Zenon.AmountDisplay
	plan.TokenSymbol = tok.Symbol
	plan.Summary = fmt.Sprintf("Lock %s %s to %s until %s, redeemable with the preimage of %s.",
		sw.Zenon.AmountDisplay, tok.Symbol, peer, plan.ExpiresAt,
		hex.EncodeToString(sw.SecretHash))
	return nil
}

// planUnlock builds htlc.Unlock — taking the ZNN out of the counterparty's HTLC
// by revealing the preimage.
func planUnlock(ctx context.Context, mgr *Manager, sw *Swap, from string,
	plan *walletBlockPlan) error {

	if sw.ZenonHtlcIsOurs() {
		return errors.New("you created this swap's Zenon HTLC, so it is the counterparty who " +
			"unlocks it. There is nothing for you to unlock")
	}
	if len(sw.Secret) == 0 {
		return errors.New("you do not hold the preimage yet, and an unlock is nothing but " +
			"revealing it. It appears on Bitcoin when the counterparty redeems your contract; " +
			"ferry picks it up on Refresh")
	}
	htlcID := strings.TrimSpace(sw.Zenon.HtlcID)
	if htlcID == "" {
		return errors.New("this swap has no Zenon HTLC id. Verify or find the counterparty's " +
			"HTLC first")
	}
	// Verification is not a formality here. Unlocking publishes the preimage,
	// which is the one secret that keeps both legs bound together — doing it
	// against an HTLC nobody has checked hands the counterparty the Bitcoin leg
	// for whatever the Zenon entry happens to contain.
	if missing := sw.MissingZenonTerms(); len(missing) > 0 {
		return fmt.Errorf("not unlocking: %s. An unlock publishes the preimage, and against an "+
			"HTLC whose payee or amount this swap never recorded that is handing the counterparty "+
			"your Bitcoin for whatever the entry happens to hold. Add the missing terms to the "+
			"swap and verify the HTLC first", strings.Join(missing, "; "))
	}
	if !sw.Zenon.Verified {
		return errors.New("this swap's Zenon HTLC has not passed verification. Unlocking " +
			"publishes the preimage, which is what lets the counterparty take your Bitcoin — so " +
			"check the HTLC really holds the agreed token, amount and expiry first")
	}
	// The stored verdict is as old as the last check, and a record can carry
	// one from a release with a laxer rule. So the entry is read again now and
	// held to today's expectations, immediately before the preimage is packed
	// into something a wallet will sign. A node that cannot answer is a
	// refusal.
	info, err := mgr.Znn.GetHtlcByID(ctx, htlcID)
	if err != nil {
		return fmt.Errorf("could not re-read HTLC %s before unlocking it, so it cannot be "+
			"confirmed to still match the agreed terms: %w", htlcID, err)
	}
	want := mgr.zenonExpectations(ctx, sw)
	want.ExpectID = htlcID
	if err := mgr.Znn.Verify(info, want); err != nil {
		return fmt.Errorf("HTLC %s does not pass verification now, so nothing is unlocked: %w",
			htlcID, err)
	}
	id, err := hex.DecodeString(htlcID)
	if err != nil {
		return fmt.Errorf("the stored HTLC id %q is not hex: %w", htlcID, err)
	}
	data, err := znn.PackUnlock(id, sw.Secret)
	if err != nil {
		return err
	}

	// The contract pays hashLocked — this user's own address — whoever makes
	// the call, which is what lets a wallet holding nothing settle this leg.
	// Whether that address still permits it, and what it means when the signer
	// is somebody else, is the sync gate's business; see walletsync.go.
	payee := strings.TrimSpace(sw.Zenon.SelfAddress)

	plan.Block.Data = base64.StdEncoding.EncodeToString(data)
	plan.HtlcID = htlcID
	plan.Summary = fmt.Sprintf("Unlock HTLC %s with the preimage, paying its ZNN to %s. "+
		"This publishes the preimage on Zenon.", htlcID, orElse(payee, "the address in the entry"))
	plan.Warnings = append(plan.Warnings, "The ZNN arrives as an unreceived block. Collect it in "+
		"your wallet after a couple of momentums, or it will look as though the unlock failed.")
	return nil
}

// planReclaim builds htlc.Reclaim — taking back ZNN this user locked, after the
// swap has failed to complete.
func planReclaim(_ context.Context, _ *Manager, sw *Swap, from string, mom *znn.Momentum,
	plan *walletBlockPlan) error {

	if !sw.ZenonHtlcIsOurs() {
		return errors.New("the counterparty locked this swap's ZNN, so only they can reclaim it")
	}
	htlcID := strings.TrimSpace(sw.Zenon.HtlcID)
	if htlcID == "" {
		return errors.New("this swap has no Zenon HTLC id, so there is nothing to reclaim")
	}
	// Only timeLocked may reclaim, and timeLocked is the address that created
	// the entry — which the sync gate has already checked the wallet is on.
	self := strings.TrimSpace(sw.Zenon.SelfAddress)
	if sw.Zenon.ExpirationTime > 0 && mom.Timestamp < sw.Zenon.ExpirationTime {
		return fmt.Errorf("this HTLC does not expire until %s and the Zenon chain's clock reads "+
			"%s. A reclaim before then is refused by the contract",
			utcTime(sw.Zenon.ExpirationTime), utcTime(mom.Timestamp))
	}
	id, err := hex.DecodeString(htlcID)
	if err != nil {
		return fmt.Errorf("the stored HTLC id %q is not hex: %w", htlcID, err)
	}
	data, err := znn.PackReclaim(id)
	if err != nil {
		return err
	}
	plan.Block.Data = base64.StdEncoding.EncodeToString(data)
	plan.HtlcID = htlcID
	plan.Summary = fmt.Sprintf("Reclaim the ZNN locked in HTLC %s, back to %s.",
		htlcID, orElse(self, from))
	if len(sw.Secret) > 0 && sw.Leg == LegReceive && sw.State != StateRedeemed {
		plan.Warnings = append(plan.Warnings, "You hold the preimage and have not redeemed the "+
			"Bitcoin contract. Reclaiming here does not stop the counterparty unlocking this "+
			"HTLC first if it has not actually expired on their node's clock.")
	}
	return nil
}

// handleWalletSent records what the wallet published.
//
// A separate call from building the block because the two are separated by a
// person, and the extension reports the outcome only as a transaction hash.
// That hash is the HTLC id for a create -- the value the rest of this app is
// organised around -- so it is fed back through the same verifying path a
// hand-typed id goes through, rather than believed.
type walletSentReq struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	Hash   string `json:"hash"`
	// From is the account the wallet signed with, recorded on a create because
	// it is the only address that will be able to reclaim.
	From string `json:"from,omitempty"`
	// Proposed and Signed are the block as this page built it and the block as
	// the wallet actually published it. Comparing them is the only way to learn
	// that the wallet signed with a different account than the one it reported
	// -- see diffSignedBlock.
	Proposed *walletBlock   `json:"proposed,omitempty"`
	Signed   map[string]any `json:"signed,omitempty"`
	Settings Settings       `json:"settings"`
}

// diffSignedBlock reports where the published block departs from the proposal.
//
// The last of the three checks around a wallet hand-off, and the only one that
// runs after the fact. The window between the wallet reporting its selected
// account and the wallet signing is one this page cannot close: the extension
// re-reads that account at signing time, offers no way to pin one, and announces
// a change only sometimes.
//
// So the sync gate prevents what can be prevented and this names what could not
// be. It cannot undo a create made from the wrong account -- but it is the
// difference between being told in the next sentence that the reclaim key is
// somewhere else, and finding out in two days when the swap stalls.
//
// Only the fields this page decided are compared. Height, previousHash, plasma,
// nonce, signature and hash are the wallet's to fill in and differ by design.
func diffSignedBlock(proposed *walletBlock, signed map[string]any) []string {
	if proposed == nil || len(signed) == 0 {
		return nil
	}
	// A field the wallet did not report is one this cannot speak about; a field
	// reported as empty is a different statement, and for `data` it is the
	// dangerous one -- an HTLC call stripped of its arguments is a plain send of
	// the same ZNN to the contract, which keeps the money and creates nothing.
	str := func(key string) (string, bool) {
		raw, present := signed[key]
		if !present {
			return "", false
		}
		v, ok := raw.(string)
		return v, ok
	}
	num := func(key string) (float64, bool) {
		raw, present := signed[key]
		if !present {
			return 0, false
		}
		v, ok := raw.(float64)
		return v, ok
	}

	var out []string
	if got, ok := str("address"); ok && !strings.EqualFold(got, proposed.Address) {
		out = append(out, fmt.Sprintf("it was signed by %s, not by %s as the wallet reported",
			orElse(got, "an account it did not name"), proposed.Address))
	}
	if got, ok := num("chainIdentifier"); ok && uint64(got) != proposed.ChainIdentifier {
		out = append(out, fmt.Sprintf("it went to chain %d, not chain %d",
			int64(got), proposed.ChainIdentifier))
	}
	if got, ok := str("toAddress"); ok && !strings.EqualFold(got, proposed.ToAddress) {
		out = append(out, fmt.Sprintf("it was sent to %s, not to the HTLC contract at %s",
			orElse(got, "an address it did not name"), proposed.ToAddress))
	}
	if got, ok := str("data"); ok && got != proposed.Data {
		out = append(out, "the call data does not match what this page built, so the arguments "+
			"of the HTLC call are not the ones shown")
	}
	if got, ok := str("tokenStandard"); ok && !strings.EqualFold(got, proposed.TokenStandard) {
		out = append(out, fmt.Sprintf("it moved %s, not %s", got, proposed.TokenStandard))
	}
	// The amount comes back as a string from toJson, but a JSON number is what
	// a hand-assembled payload would carry, so both are read before comparing.
	if got, ok := str("amount"); ok && got != proposed.Amount {
		out = append(out, fmt.Sprintf("it moved %s base units, not %s", got, proposed.Amount))
	} else if !ok {
		if n, isNum := num("amount"); isNum {
			if got := fmt.Sprintf("%.0f", n); got != proposed.Amount {
				out = append(out, fmt.Sprintf("it moved %s base units, not %s", got, proposed.Amount))
			}
		}
	}
	return out
}

func handleWalletSent(ctx context.Context, a *API, body []byte) (any, error) {
	var req walletSentReq
	mgr, err := withManager(a, body, &req, func(r *walletSentReq) Settings { return r.Settings })
	if err != nil {
		return nil, err
	}
	sw, err := a.Store.Load(req.ID)
	if err != nil {
		return nil, err
	}
	hash := strings.TrimSpace(req.Hash)

	// Recorded on the swap before anything else happens, whatever it says. A
	// block that was published and did not match is the case where the log
	// matters most, and returning an error without writing it down would lose
	// the only record of what the wallet actually did.
	mismatch := diffSignedBlock(req.Proposed, req.Signed)
	if len(mismatch) > 0 {
		sw.log("WARNING: the wallet published %s, but it is not the block this page built: %s",
			orElse(hash, "a block"), strings.Join(mismatch, "; "))
		if signer, ok := req.Signed["address"].(string); ok && signer != "" {
			// The account that really signed is the one that owns the leg,
			// whatever was intended -- for a create it is the only address that
			// can reclaim, so recording the intended one would be recording a
			// key that cannot open anything.
			sw.Zenon.SelfAddress = signer
		}
		if serr := a.Store.Save(sw); serr != nil {
			return nil, serr
		}
		return nil, fmt.Errorf("the wallet published a block that is not the one this page "+
			"built: %s. Nothing here can undo that — check the transaction%s on your node and, "+
			"if ZNN was locked from an account you did not intend, note that only that account "+
			"can reclaim it",
			strings.Join(mismatch, "; "), func() string {
				if hash == "" {
					return ""
				}
				return " " + hash
			}())
	}

	switch req.Action {
	case "create":
		if hash == "" {
			return nil, errors.New("the wallet reported no transaction hash, so the HTLC id is " +
				"not known. Use Find it, or paste the id from the wallet's history")
		}
		if from := strings.TrimSpace(req.From); from != "" && sw.Zenon.SelfAddress == "" {
			sw.Zenon.SelfAddress = from
		}
		// The id is written down before anything is checked, because verification
		// almost always fails on the first attempt -- an account block needs a
		// momentum or two -- and until it was, the id survived that failure nowhere
		// but in a sentence on the card. A reload lost it, and the card, seeing a
		// swap with no Zenon HTLC, went back to offering to create one: pressing
		// that locks a second lot of ZNN.
		//
		// Recording it asserts nothing about it. Verified stays false until the
		// chain agrees, and the pending reason goes where the card's badge reads it.
		sw.Zenon.HtlcID = hash
		sw.log("the wallet published an htlc.create as transaction %s", hash)
		if err := a.Store.Save(sw); err != nil {
			return nil, err
		}
		// Verified through the ordinary path, against this browser's node and this
		// swap's agreed terms -- a create the wallet says succeeded is still one
		// nobody has looked at. It usually will not be there yet, so a failure here
		// is reported as an unverified result rather than an error, and the card's
		// Verify button is the retry.
		verified, info, verr := mgr.VerifyZenon(ctx, req.ID, hash)
		if verified == nil {
			// Pending, not failed. The wallet has just published this block and a new
			// one takes a momentum or two to become readable, so the node not knowing
			// it yet is the expected answer -- not evidence that anything is wrong.
			//
			// VerifyZenon's message is written for somebody who typed an id in by
			// hand, so it leads with "mistyped" and "wrong network"; against a create
			// this page watched the wallet publish seconds ago, both are false and
			// only its last clause is true. Storing it as a FAILURE put a red badge
			// saying those things on every healthy HTLC for its first two momentums.
			//
			// VerifyZenon saves nothing on this path, so the swap here is still the
			// one saved above and can carry the reason.
			sw.Zenon.VerifyPending = true
			sw.Zenon.VerifyError = ""
			if serr := a.Store.Save(sw); serr != nil {
				return nil, serr
			}
			return map[string]any{
				"swap":    view(sw),
				"pending": true,
				"error":   verr.Error(),
			}, nil
		}
		resp := map[string]any{"swap": view(verified), "htlc": info}
		if verr != nil {
			// Whether this is pending is the swap's own answer, not a
			// hopeful default. A create whose entry was read and REFUSED is
			// not waiting on anything: saying so put "this checks again every
			// minute" under a leg nothing would look at again, which is the
			// most misleading thing this card can say.
			resp["pending"] = verified.Zenon.VerifyPending
			resp["error"] = verr.Error()
		}
		return resp, nil

	case "unlock", "reclaim":
		sw.log("the wallet published an htlc.%s as transaction %s", req.Action, hash)
		// Written down because the chain will not remember for us: both calls
		// delete the entry, so from here on "has this leg been settled" has no
		// answer available by asking. See ZenonLeg.UnlockHash.
		if req.Action == "unlock" {
			sw.Zenon.UnlockHash = hash
		}
		if err := a.Store.Save(sw); err != nil {
			return nil, err
		}
		// Refresh rather than verify: the entry is deleted by both of these, so
		// there is nothing left to check against. What matters now is the
		// Bitcoin side, which Refresh reads.
		refreshed, rerr := mgr.Refresh(ctx, req.ID)
		if rerr != nil {
			return map[string]any{"swap": view(sw), "error": rerr.Error()}, nil
		}
		return map[string]any{"swap": view(refreshed)}, nil
	}
	return nil, fmt.Errorf("unknown wallet action %q", req.Action)
}

func utcTime(unix int64) string {
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

func orElse(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// requireCounterLegFunding is the gate in front of locking ZNN against the
// counterparty's Bitcoin: nothing to do unless this swap's create waits on that
// funding, and otherwise a refusal unless it is present, covers the agreed
// amount, is mined at least commitConfirmations deep, and is still unspent --
// all of it read from the chain at this moment.
//
// The record is checked first, because it is what the card showed and its
// refusal names what the user is looking at. Then the chain is asked again,
// because a record is only as current as the last poll and the block built
// here is about to be signed: a funding that was replaced or reorganised out a
// minute ago must not get a lock of ZNN. A chain that cannot be read is a
// refusal too, not a pass.
func requireCounterLegFunding(ctx context.Context, backend chain.Backend, sw *Swap) error {
	if !sw.ZenonCreateWaitsOnBtc() {
		return nil
	}
	if blocker := sw.FundingCommitBlocker(); blocker != "" {
		return fmt.Errorf("not locking ZNN yet: %s. This leg answers their Bitcoin, so it waits "+
			"until that payment is confirmed and covers the agreed amount; Refresh keeps "+
			"checking, and Auto Mode creates the HTLC when it is", blocker)
	}
	return checkFundingOnChain(ctx, backend, sw, commitConfirmations)
}

// checkFundingOnChain is the fresh half of requireCounterLegFunding, with the
// depth as a parameter so the rule can be tested at every threshold.
func checkFundingOnChain(ctx context.Context, backend chain.Backend, sw *Swap, minDepth int64) error {
	if backend == nil {
		return errors.New("no Bitcoin backend to re-read the funding from; check Node settings")
	}
	utxos, err := backend.AddressUTXOs(ctx, sw.ContractAddr)
	if err != nil {
		return fmt.Errorf("could not re-read the contract address before locking ZNN, so the "+
			"funding could not be confirmed as still there: %w", err)
	}
	var found *chain.UTXO
	for i := range utxos {
		if utxos[i].TxID == sw.Funding.TxID && utxos[i].Vout == sw.Funding.Vout {
			found = &utxos[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("the funding output %s:%d is no longer unspent at the contract address: "+
			"it has been spent, replaced or reorganised out since it was last seen. Refresh the "+
			"swap before doing anything else", sw.Funding.TxID, sw.Funding.Vout)
	}
	if found.Value < sw.AmountSats {
		return fmt.Errorf("the funding output holds %d sat but %d sat was agreed",
			found.Value, sw.AmountSats)
	}
	if !found.Status.Confirmed || found.Status.BlockHeight <= 0 {
		return errors.New("their funding is back in the mempool, where the sender can replace it; " +
			"wait for it to be mined again")
	}
	tip, err := backend.TipHeight(ctx)
	if err != nil {
		return fmt.Errorf("could not read the Bitcoin chain tip to count confirmations: %w", err)
	}
	if depth := tip - found.Status.BlockHeight + 1; depth < minDepth {
		return fmt.Errorf("their funding has %d of the %d confirmation%s this needs",
			depth, minDepth, plural(int(minDepth)))
	}
	return nil
}

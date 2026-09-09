package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/zenon/ferry-web/wasm/znn"
)

// Proving the wallet and this page are looking at the same chain, and that the
// account about to sign is the right one.
//
// The extension is not an extension of this page: it has its own node URL, chain
// identifier and selected account, none of them derived from ferry's settings.
// Three mistakes live on that boundary:
//
//   - **The wrong account.** The extension signs with whichever account it has
//     selected and cannot be told to use another. For an htlc.Create the signer
//     becomes `timeLocked`, the only address the contract will ever pay a
//     reclaim to -- so a create signed by the wrong account is ZNN that can
//     never come back if the swap stalls.
//   - **The wrong chain.** An HTLC created somewhere the counterparty is not
//     looking -- and if one side is mainnet, with real money.
//   - **A stale node.** The expiry is computed against momentum time, so a
//     lagging node produces a deadline the chain disagrees with.
//
// The chain check is the one worth being careful about, because the obvious
// version is wrong. **Equal chain identifiers are not proof of the same chain**:
// every go-zenon devnet anyone starts is chain 69, and two of them share nothing
// else. What settles it is a momentum both nodes should already have -- read the
// hash at an agreed height from each and compare, which is what chainid.go does
// with a counterparty's claim.
//
// The verdict is a hard gate rather than a warning. A check that could not run
// is reported as unchecked and blocks just as a failure does, because "we could
// not tell" and "it is fine" are the two things this file exists to keep apart.

// walletSyncResult is what the page must see before it may hand over a block.
type walletSyncResult struct {
	// Ok is true only when every check ran and every check passed. The page
	// refuses to send when it is false; there is no override, because every
	// mistake this guards against costs money and none of them is recoverable
	// by noticing afterwards.
	Ok bool `json:"ok"`

	// Address is the account the wallet says it will sign with.
	Address string `json:"address"`
	// NodeURL is the node the wallet says it will publish through.
	NodeURL string `json:"nodeUrl,omitempty"`
	// SameNodeURL records that both sides named the same endpoint. It is
	// reported, never relied on: the momentum comparison below is what proves
	// anything, and it is run either way.
	SameNodeURL bool `json:"sameNodeUrl"`

	// ChainIdentifier and WalletChainIdentifier are what each node says it is.
	ChainIdentifier       uint64 `json:"chainIdentifier"`
	WalletChainIdentifier uint64 `json:"walletChainIdentifier,omitempty"`
	// ReportedChainID is what the extension told the page, kept because it can
	// disagree with what its own node answers — which means the extension is
	// configured for one chain and pointed at another.
	ReportedChainID uint64 `json:"reportedChainId,omitempty"`

	// SameChain is set only when a momentum hash was actually compared and
	// matched. It is the single positive result here; everything else is at
	// best the absence of a contradiction.
	SameChain    bool   `json:"sameChain"`
	AnchorHeight uint64 `json:"anchorHeight,omitempty"`
	AnchorHash   string `json:"anchorHash,omitempty"`

	// Height and WalletHeight are the two tips, for the drift check.
	Height       uint64 `json:"height,omitempty"`
	WalletHeight uint64 `json:"walletHeight,omitempty"`

	// Problems are proven mismatches. Unchecked are checks that could not run.
	// Both block; they are separate because they need different sentences —
	// one says "this is wrong", the other says "this could not be established".
	Problems  []string `json:"problems,omitempty"`
	Unchecked []string `json:"unchecked,omitempty"`
	// Warnings are true, worth saying, and not disqualifying.
	Warnings []string `json:"warnings,omitempty"`
}

type walletSyncReq struct {
	// ID and Action are optional: without them this is a check of the wallet
	// against this browser, and with them it is also a check of the wallet
	// against the swap that is about to be acted on.
	ID     string `json:"id,omitempty"`
	Action string `json:"action,omitempty"`

	Address  string   `json:"address"`
	ChainID  uint64   `json:"chainId,omitempty"`
	NodeURL  string   `json:"nodeUrl,omitempty"`
	Settings Settings `json:"settings"`
}

func handleWalletSync(ctx context.Context, a *API, body []byte) (any, error) {
	var req walletSyncReq
	mgr, err := withManager(a, body, &req, func(r *walletSyncReq) Settings { return r.Settings })
	if err != nil {
		return nil, err
	}
	if mgr.Znn == nil {
		return nil, errors.New("no Zenon node is set for this browser, so there is nothing to " +
			"check the wallet against. Open Node settings and give it a Zenon JSON-RPC URL")
	}
	return runWalletSync(ctx, a, mgr, req), nil
}

// runWalletSync is the check itself, so that building a block runs exactly the
// same gate the page shows rather than a second, laxer copy of it. The page's
// call to walletSync is what puts the verdict on screen; this call is what
// makes it load-bearing.
func runWalletSync(ctx context.Context, a *API, mgr *Manager, req walletSyncReq) *walletSyncResult {
	out := &walletSyncResult{
		Address:         strings.TrimSpace(req.Address),
		NodeURL:         strings.TrimSpace(req.NodeURL),
		ReportedChainID: req.ChainID,
	}
	out.SameNodeURL = sameEndpoint(out.NodeURL, mgr.Znn.URL)

	if out.Address == "" {
		out.Problems = append(out.Problems, "the wallet reported no address, so there is no way "+
			"to know which account would sign")
	} else if _, aerr := znn.ParseAddress(out.Address); aerr != nil {
		out.Problems = append(out.Problems, fmt.Sprintf(
			"the wallet reported an address this page cannot read: %v", aerr))
	}

	checkWalletChain(ctx, mgr, req, out)
	if req.ID != "" {
		checkWalletAgainstSwap(ctx, a, mgr, req, out)
	}

	out.Ok = len(out.Problems) == 0 && len(out.Unchecked) == 0
	return out
}

// refusal turns a failed sync into the error a caller gets back. Problems and
// unchecked items are joined into one sentence rather than reported one at a
// time, because they are usually one situation seen from two angles -- a wallet
// on the wrong network has a different chain id AND an unreadable node -- and
// fixing them one message at a time is three trips through a settings screen.
func (w *walletSyncResult) refusal() error {
	reasons := append(append([]string{}, w.Problems...), w.Unchecked...)
	if len(reasons) == 0 {
		return nil
	}
	return fmt.Errorf("the wallet and this page do not agree, so nothing will be sent to it: %s",
		strings.Join(reasons, "; "))
}

// checkWalletChain compares the wallet's node against this browser's.
func checkWalletChain(ctx context.Context, mgr *Manager, req walletSyncReq, out *walletSyncResult) {
	ours, err := mgr.Znn.FrontierMomentum(ctx)
	if err != nil {
		out.Unchecked = append(out.Unchecked, fmt.Sprintf(
			"this browser's Zenon node could not be read (%v), so the wallet's chain could not be "+
				"compared against anything", err))
		return
	}
	out.ChainIdentifier = ours.ChainIdentifier
	out.Height = ours.Height

	// What the extension told the page, against what its own node says, is a
	// check the page can make for free and the extension apparently does not:
	// the chain identifier is a setting in the wallet, and a wallet set to one
	// chain while pointed at a node serving another signs blocks that node
	// rejects.
	if req.ChainID != 0 && req.ChainID != ours.ChainIdentifier {
		out.Problems = append(out.Problems, fmt.Sprintf(
			"the wallet is configured for chain %d and this browser's Zenon node is on chain %d. "+
				"A block built here would be signed for the wrong chain",
			req.ChainID, ours.ChainIdentifier))
	}

	if out.NodeURL == "" {
		out.Unchecked = append(out.Unchecked, "the wallet did not say which node it publishes "+
			"through, so there is no way to check it is the same chain this page is reading. "+
			"Reconnect the wallet — the connect prompt is what reports its node URL")
		return
	}

	// The wallet's node is read directly, over whichever transport its URL names.
	// This is the only check here that proves anything, and also the one most
	// likely to be unreachable: a page on https cannot open a ws:// or http://
	// endpoint at all, and a node without permissive CORS refuses an http request
	// from an origin it does not know. Both come back as unchecked, which blocks.
	theirs := znn.New(out.NodeURL)
	walletTip, err := theirs.FrontierMomentum(ctx)
	if err != nil {
		out.Unchecked = append(out.Unchecked, fmt.Sprintf(
			"the wallet's own node at %s could not be read from this page (%v), so there is no "+
				"proof it is on the same chain. Point the wallet and this browser at the same "+
				"node, or at one this page can reach", out.NodeURL, err))
		return
	}
	out.WalletChainIdentifier = walletTip.ChainIdentifier
	out.WalletHeight = walletTip.Height

	if walletTip.ChainIdentifier != ours.ChainIdentifier {
		out.Problems = append(out.Problems, fmt.Sprintf(
			"different chains: the wallet's node is on chain %d and this browser's is on chain %d",
			walletTip.ChainIdentifier, ours.ChainIdentifier))
		return
	}

	// The comparison that settles it. The anchor is taken from whichever tip is
	// lower, so a node that is merely behind still has the momentum being asked
	// about — otherwise a lagging node would look like a different chain.
	tip := min(ours.Height, walletTip.Height)
	anchor := uint64(1)
	if tip > znnAnchorDepth {
		anchor = tip - znnAnchorDepth
	}
	oursAt, oerr := mgr.Znn.MomentumAt(ctx, anchor)
	theirsAt, terr := theirs.MomentumAt(ctx, anchor)
	if oerr != nil || terr != nil {
		out.Unchecked = append(out.Unchecked, fmt.Sprintf(
			"momentum %d could not be read from both nodes to compare them (%v / %v), so the "+
				"chains could not be proven to be the same one",
			anchor, firstErr(oerr), firstErr(terr)))
		return
	}
	out.AnchorHeight = anchor
	if !strings.EqualFold(oursAt.Hash, theirsAt.Hash) {
		out.Problems = append(out.Problems, fmt.Sprintf(
			"different chains: at momentum %d this browser's node has %s and the wallet's has %s. "+
				"Both call themselves chain %d — every go-zenon devnet does — but an HTLC created "+
				"through the wallet would be invisible to this page and to the counterparty",
			anchor, shortHash(oursAt.Hash), shortHash(theirsAt.Hash), ours.ChainIdentifier))
		return
	}
	out.SameChain = true
	out.AnchorHash = oursAt.Hash

	// Same chain, but not necessarily the same view of it. The expiry an
	// htlc.Create commits to is compared by the contract against momentum time,
	// and it is computed here from THIS browser's tip while the block is
	// published through the wallet's — so a large gap is a real hazard rather
	// than a cosmetic one.
	if drift := abs64(int64(ours.Height) - int64(walletTip.Height)); drift > znnHeightDrift {
		out.Problems = append(out.Problems, fmt.Sprintf(
			"same chain, but %d momentums apart: this browser's node is at %d and the wallet's at "+
				"%d. One of them is not keeping up, and an expiry computed from one and published "+
				"through the other would not mean what it says",
			drift, ours.Height, walletTip.Height))
	}
}

// checkWalletAgainstSwap asks whether this account is the right one for what is
// about to be done to this particular swap.
func checkWalletAgainstSwap(ctx context.Context, a *API, mgr *Manager, req walletSyncReq,
	out *walletSyncResult) {

	sw, err := a.Store.Load(req.ID)
	if err != nil {
		out.Problems = append(out.Problems, err.Error())
		return
	}
	if sw.Network != mgr.Network {
		out.Problems = append(out.Problems, fmt.Sprintf(
			"this swap is on %s and the app is set to %s", sw.Network, mgr.Network))
	}
	self := strings.TrimSpace(sw.Zenon.SelfAddress)

	switch req.Action {
	case "create", "reclaim":
		// Both of these are only ever valid from the address the swap records:
		// a create because the signer becomes timeLocked and nothing else can
		// reclaim it, a reclaim because the contract pays timeLocked and
		// refuses anyone else.
		if self == "" {
			out.Warnings = append(out.Warnings, fmt.Sprintf(
				"this swap records no Zenon address of its own, so %s will become the account "+
					"that owns this leg — and after a create it is the only one that can reclaim.",
				out.Address))
			return
		}
		if !strings.EqualFold(self, out.Address) {
			out.Problems = append(out.Problems, fmt.Sprintf(
				"wrong account: this swap's Zenon leg belongs to %s and the wallet has %s "+
					"selected. Switch the account in the extension's settings, then reconnect",
				self, out.Address))
		}

	case "unlock":
		// An unlock may come from any account, because the contract pays the
		// address in the entry rather than the caller. Unless that address has
		// turned the behaviour off, in which case it is the only account that
		// can make the call at all.
		if self == "" || strings.EqualFold(self, out.Address) {
			return
		}
		allowed, perr := mgr.Znn.ProxyUnlockAllowed(ctx, self)
		switch {
		case perr != nil:
			out.Unchecked = append(out.Unchecked, fmt.Sprintf(
				"the wallet would sign from %s while the ZNN goes to %s, and whether that address "+
					"still permits being paid by another account could not be read from the node "+
					"(%v)", out.Address, self, perr))
		case !allowed:
			out.Problems = append(out.Problems, fmt.Sprintf(
				"%s has denied proxy unlock, so only that account can unlock an HTLC that pays "+
					"it — and the wallet has %s selected. Switch the account in the extension",
				self, out.Address))
		default:
			out.Warnings = append(out.Warnings, fmt.Sprintf(
				"The wallet will sign from %s, and the ZNN will go to %s — the contract pays the "+
					"address in the entry, not the caller.", out.Address, self))
		}
	}
}

// sameEndpoint reports whether two node URLs name the same endpoint, ignoring
// case and a trailing slash. Nothing is inferred beyond that: it is tempting to
// treat http://host:35997 and ws://host:35998 as one node, because they usually
// are, but "usually" is not a basis for skipping the comparison that proves it.
func sameEndpoint(a, b string) bool {
	norm := func(s string) string {
		return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "/"))
	}
	return a != "" && norm(a) == norm(b)
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

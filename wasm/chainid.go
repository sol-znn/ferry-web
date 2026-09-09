package main

import (
	"context"
	"fmt"
	"strings"
)

// Proving two parties are on the same chains, rather than assuming it.
//
// A swap where the two sides are on different chains cannot complete, and it
// fails in the most expensive possible way: everything looks right, both people
// fund, and neither ever sees the other's contract. The obvious guard is to
// compare the network name, and it is not enough on its own:
//
//   - Everybody's regtest is their own. Two developers both on "regtest" are on
//     unrelated chains that share a genesis block, so nothing about the name or
//     the genesis distinguishes them.
//   - A node can be stalled. Two people both honestly on mainnet disagree about
//     the chain if one is pointed at something that stopped syncing last week.
//   - A signet is a parameter, not a network. Two custom signets are different
//     chains wearing the same name.
//
// What settles it is a block both sides should already have: exchange the hash
// at an agreed height and compare. Matching hashes are proof of a shared chain;
// differing hashes are proof of the opposite. That is the whole idea here, and
// it is applied to both legs -- a Bitcoin block and a Zenon momentum -- because
// a swap needs both chains to agree, not one.

// anchorDepth is how far below the tip to take the compared block.
//
// Deep enough that a reorg cannot make two honest parties disagree, shallow
// enough that a node which stopped syncing recently still gets caught. Six
// blocks is the conventional Bitcoin figure and roughly an hour of chain.
const btcAnchorDepth = 6

// znnAnchorDepth is the same idea in momentums, which arrive roughly every ten
// seconds. Sixty is about ten minutes: past any plausible fork, well inside the
// window where a stalled node still looks stalled.
const znnAnchorDepth = 60

// heightDrift is how far apart two tips may be before it is worth mentioning.
//
// Two nodes on one chain are never exactly level -- they learn of blocks at
// different moments -- so a small gap is normal and silence is correct. A large
// one means somebody is not keeping up, which is worth saying even when the
// anchor hashes agree, because a lagging node is about to miss a funding.
const (
	btcHeightDrift = 3
	znnHeightDrift = 120
)

// ChainFingerprint is one party's answer to "which chains are you on".
//
// Every field is public chain data. It reveals nothing about the swap, the
// user, or their addresses -- it is a claim about which chains a node serves,
// and the point of it is that the other side can check it against their own.
type ChainFingerprint struct {
	Network string `json:"network"`

	BtcHeight       int64  `json:"btcHeight,omitempty"`
	BtcAnchorHeight int64  `json:"btcAnchorHeight,omitempty"`
	BtcAnchorHash   string `json:"btcAnchorHash,omitempty"`
	BtcError        string `json:"btcError,omitempty"`

	ZnnHeight       uint64 `json:"znnHeight,omitempty"`
	ZnnAnchorHeight uint64 `json:"znnAnchorHeight,omitempty"`
	ZnnAnchorHash   string `json:"znnAnchorHash,omitempty"`
	ZnnError        string `json:"znnError,omitempty"`
}

// ChainAgreement is what came of comparing two fingerprints.
type ChainAgreement struct {
	// Ok is true only when nothing at all disagreed. It is deliberately not
	// "no hard errors": a check that could not run is not a check that passed,
	// and this value is what a UI turns into a green badge.
	Ok bool `json:"ok"`
	// SameChain is true when a block hash was actually compared and matched --
	// the only positive proof available here. Absent that, agreement is at best
	// "nothing contradicted it".
	SameChain bool `json:"sameChain"`
	// Problems are mismatches: a swap with any of these cannot complete.
	Problems []string `json:"problems,omitempty"`
	// Unchecked are checks that could not run, usually because one side has no
	// node for that leg. They are reported separately from problems because
	// they mean "unknown", and reporting unknown as agreement is the failure
	// this whole file exists to prevent.
	Unchecked []string `json:"unchecked,omitempty"`
}

// BuildFingerprint asks this browser's own nodes which chains they are on.
//
// A leg that cannot be reached records why rather than failing the whole call:
// a user with no Zenon node still benefits from having their Bitcoin leg
// checked, and the gap is reported to them as a gap.
func BuildFingerprint(ctx context.Context, m *Manager) *ChainFingerprint {
	fp := &ChainFingerprint{Network: m.Network}

	if m.Chain != nil {
		tip, err := m.Chain.TipHeight(ctx)
		switch {
		case err != nil:
			fp.BtcError = err.Error()
		default:
			fp.BtcHeight = tip
			anchor := max(tip-btcAnchorDepth, 0)
			if hash, herr := m.Chain.BlockHashAt(ctx, anchor); herr != nil {
				fp.BtcError = herr.Error()
			} else {
				fp.BtcAnchorHeight, fp.BtcAnchorHash = anchor, hash
			}
		}
	} else {
		fp.BtcError = "no Bitcoin backend configured"
	}

	if m.Znn != nil {
		mom, err := m.Znn.FrontierMomentum(ctx)
		switch {
		case err != nil:
			fp.ZnnError = err.Error()
		default:
			fp.ZnnHeight = mom.Height
			anchor := uint64(1)
			if mom.Height > znnAnchorDepth {
				anchor = mom.Height - znnAnchorDepth
			}
			if at, merr := m.Znn.MomentumAt(ctx, anchor); merr != nil {
				fp.ZnnError = merr.Error()
			} else {
				fp.ZnnAnchorHeight, fp.ZnnAnchorHash = anchor, at.Hash
			}
		}
	} else {
		fp.ZnnError = "no Zenon node configured"
	}
	return fp
}

// VerifyFingerprint checks a counterparty's claim against this browser's nodes.
//
// The counterparty's numbers are never trusted as facts about the world. Their
// anchor HEIGHT is used to ask our own node what hash it has there, and it is
// our node's answer that is compared -- so a peer who reports whatever they
// like proves nothing and is caught by the comparison rather than believed by
// it.
func VerifyFingerprint(ctx context.Context, m *Manager, peer *ChainFingerprint) *ChainAgreement {
	out := &ChainAgreement{}
	if peer == nil {
		out.Problems = append(out.Problems, "they sent no chain information")
		return out
	}

	// The cheap check first, because a name mismatch makes every other
	// comparison meaningless and deserves to be the sentence the user reads.
	if !strings.EqualFold(peer.Network, m.Network) {
		out.Problems = append(out.Problems, fmt.Sprintf(
			"they are on %s and you are on %s — the two legs of this swap would be on different "+
				"networks, so neither side would ever see the other's contract",
			nameOr(peer.Network, "an unnamed network"), m.Network))
		return out
	}

	verifyBitcoinLeg(ctx, m, peer, out)
	verifyZenonLeg(ctx, m, peer, out)

	out.Ok = len(out.Problems) == 0 && len(out.Unchecked) == 0
	return out
}

func verifyBitcoinLeg(ctx context.Context, m *Manager, peer *ChainFingerprint, out *ChainAgreement) {
	switch {
	case peer.BtcAnchorHash == "":
		out.Unchecked = append(out.Unchecked, bitcoinGap(peer.BtcError))
		return
	case m.Chain == nil:
		out.Unchecked = append(out.Unchecked, "your own Bitcoin backend is not configured, so their chain could not be checked")
		return
	}

	ours, err := m.Chain.BlockHashAt(ctx, peer.BtcAnchorHeight)
	if err != nil {
		out.Unchecked = append(out.Unchecked, fmt.Sprintf(
			"could not read block %d from your own Bitcoin node to compare against theirs: %v",
			peer.BtcAnchorHeight, err))
		return
	}
	if !strings.EqualFold(ours, peer.BtcAnchorHash) {
		out.Problems = append(out.Problems, fmt.Sprintf(
			"different Bitcoin chains: at block %d your node has %s and theirs has %s. Both of you "+
				"may be calling it %q, but a contract funded on one is invisible on the other",
			peer.BtcAnchorHeight, shortHash(ours), shortHash(peer.BtcAnchorHash), m.Network))
		return
	}

	out.SameChain = true
	if tip, err := m.Chain.TipHeight(ctx); err == nil && peer.BtcHeight > 0 {
		if drift := abs64(tip - peer.BtcHeight); drift > btcHeightDrift {
			out.Problems = append(out.Problems, fmt.Sprintf(
				"same Bitcoin chain, but %d blocks apart — your node is at %d and theirs at %d. "+
					"Whichever is behind will be slow to see a funding, or miss it",
				drift, tip, peer.BtcHeight))
		}
	}
}

func verifyZenonLeg(ctx context.Context, m *Manager, peer *ChainFingerprint, out *ChainAgreement) {
	switch {
	case peer.ZnnAnchorHash == "":
		out.Unchecked = append(out.Unchecked, zenonGap(peer.ZnnError))
		return
	case m.Znn == nil:
		out.Unchecked = append(out.Unchecked,
			"you have no Zenon node set, so their Zenon chain could not be checked")
		return
	}

	ours, err := m.Znn.MomentumAt(ctx, peer.ZnnAnchorHeight)
	if err != nil {
		out.Unchecked = append(out.Unchecked, fmt.Sprintf(
			"could not read momentum %d from your own Zenon node to compare against theirs: %v",
			peer.ZnnAnchorHeight, err))
		return
	}
	if !strings.EqualFold(ours.Hash, peer.ZnnAnchorHash) {
		out.Problems = append(out.Problems, fmt.Sprintf(
			"different Zenon chains: at momentum %d your node has %s and theirs has %s — an HTLC "+
				"created on one cannot be unlocked from the other",
			peer.ZnnAnchorHeight, shortHash(ours.Hash), shortHash(peer.ZnnAnchorHash)))
		return
	}
	if mom, err := m.Znn.FrontierMomentum(ctx); err == nil && peer.ZnnHeight > 0 {
		if drift := abs64(int64(mom.Height) - int64(peer.ZnnHeight)); drift > znnHeightDrift {
			out.Problems = append(out.Problems, fmt.Sprintf(
				"same Zenon chain, but %d momentums apart — yours is at %d and theirs at %d, so "+
					"one of you is looking at a stale view of the HTLC",
				drift, mom.Height, peer.ZnnHeight))
		}
	}
}

func bitcoinGap(reason string) string {
	if reason == "" {
		return "they sent no Bitcoin chain information, so the Bitcoin leg could not be checked"
	}
	return "their Bitcoin node could not be read, so the Bitcoin leg could not be checked: " + reason
}

func zenonGap(reason string) string {
	if reason == "" {
		return "they sent no Zenon chain information, so the Zenon leg could not be checked"
	}
	return "their Zenon node could not be read, so the Zenon leg could not be checked: " + reason
}

func nameOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func shortHash(h string) string {
	if len(h) <= 16 {
		return h
	}
	return h[:8] + "…" + h[len(h)-6:]
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

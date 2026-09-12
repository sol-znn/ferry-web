package main

import "fmt"

// planActions turns the two legs' observed state into the one thing this side
// should do next.
//
// The order below is the safety argument, written as code:
//
//  1. Nothing happens until both sides' addresses are known.
//  2. The participant does not fund until the initiator's leg is on chain and
//     verified. Funding first would hand the initiator a free option -- they
//     could take the participant's leg or walk away, and only one of those is
//     bad for them.
//  3. Claiming needs the secret and a verified counterparty leg.
//  4. Refunding needs only this side's own timelock to have passed, and is
//     always offered once it has, whatever else is true.
//
// Everything is recomputed from chain state on every refresh rather than
// advanced through stored steps. A swap that was in one state can be in a very
// different one by the next poll -- the counterparty acts without telling us --
// and a state machine that only moves forwards would keep offering a button
// that no longer applies.
func (m *Manager) planActions(s *Swap, st *Status) {
	add := func(a Action) { st.Actions = append(st.Actions, a) }

	if !st.Complete {
		if s.Role.Initiator {
			add(Action{
				Kind:    "exchange.applyAccept",
				Label:   "Paste the counterparty's acceptance",
				Detail:  "Send them your offer string. They reply with an acceptance that carries their two addresses.",
				Ready:   true,
				Primary: true,
			})
		} else {
			add(Action{
				Kind:   "exchange.sendAccept",
				Label:  "Send your acceptance to the counterparty",
				Detail: "Until they apply it, they cannot see where to pay you.",
				Ready:  true, Primary: true,
			})
		}
		return
	}

	mine, theirs := &st.Sol, &st.Znn
	if !s.Role.SendsSol {
		mine, theirs = &st.Znn, &st.Sol
	}

	// A counterparty leg that is on chain but does not match the agreement is
	// the one state worth interrupting for. It is not a step to be waited out:
	// it means either a mistake or an attempt, and funding against it is how
	// the money is lost.
	if theirs.Funded && !theirs.Verified {
		st.Warnings = append(st.Warnings, fmt.Sprintf(
			"the counterparty's leg is funded but does not match what was agreed: %s. Do not fund yours.",
			joinProblems(theirs.Problems)))
	}
	if mine.Funded && !mine.Verified {
		st.Warnings = append(st.Warnings, fmt.Sprintf(
			"your own leg does not match what was agreed: %s", joinProblems(mine.Problems)))
	}

	// ---------------------------------------------------------------- claim
	// Offered before funding, because once the secret is out this is the step
	// that matters and the money is already committed either way.
	if theirs.Funded && theirs.Verified && !theirs.Settled {
		if st.SecretKnown {
			if s.Role.SendsSol {
				add(Action{
					Kind:  "znn.unlock",
					Label: fmt.Sprintf("Claim %s %s", theirs.Amount, tokenName(s)),
					Detail: "Unlocks the Zenon HTLC with the secret. The contract pays your own wallet address, " +
						"not this page. The secret becomes public on Zenon, which is how the counterparty claims the SOL.",
					Ready: !theirs.Expired, Primary: true,
					Blocked: expiredNote(theirs, "the Zenon HTLC"),
				})
			} else {
				add(Action{
					Kind:  "sol.redeem",
					Label: fmt.Sprintf("Claim %s", theirs.Amount),
					Detail: "Spends the Solana escrow with the secret, paying your own wallet. " +
						"The secret becomes public on Solana, which is how the counterparty claims the ZNN.",
					Ready: !theirs.Expired, Primary: true,
					Blocked: expiredNote(theirs, "the Solana escrow"),
				})
			}
		} else if mine.Funded {
			add(Action{
				Kind:  "await.secret",
				Label: "Waiting for the counterparty to reveal the secret",
				Detail: fmt.Sprintf("They claim your leg first; that publishes the secret on %s, and refreshing picks it up.",
					chainName(s.Terms.PreimageChain())),
			})
		}
	}

	// -------------------------------------------------------------- my leg
	if !mine.Funded && !mine.Settled && !mine.Refunded && mine.Pending != "" {
		add(Action{
			Kind:   "await.funding",
			Label:  "Waiting for your funding transaction",
			Detail: mine.Pending + ". Refresh in a moment; do not send it again.",
		})
	} else if !mine.Funded && !mine.Settled && !mine.Refunded {
		blocked := ""
		if !s.Role.Initiator && !(theirs.Funded && theirs.Verified) {
			blocked = "the initiator has not funded a matching leg yet -- you go second, on purpose"
		}
		if theirs.Funded && !theirs.Verified {
			blocked = "the counterparty's leg does not match the agreement"
		}
		// Last, so it wins: a swap whose deadlines no longer work is one
		// nobody should fund whatever the counterparty has done.
		if st.Unfundable != "" {
			blocked = st.Unfundable
		}
		if s.Role.SendsSol {
			// The rent is named here rather than discovered afterwards. The
			// escrow account has to be rent-exempt to exist, so funding costs
			// the amount plus that deposit, and a step labelled with only the
			// amount is quietly untrue about what leaves the wallet. It comes
			// back on either exit, which is the other half of the sentence.
			detail := "Your wallet signs this. The escrow can only pay the counterparty with the secret, or you back after the timelock."
			if st.SolRent > 0 {
				detail = fmt.Sprintf(
					"Your wallet sends %s: the %s being swapped plus %s of rent, which the escrow account needs to exist and which comes back to you whichever way this ends. %s",
					formatSol(s.Terms.SolLamports+st.SolRent), formatSol(s.Terms.SolLamports),
					formatSol(st.SolRent), detail)
			}
			add(Action{
				Kind:   "sol.create",
				Label:  fmt.Sprintf("Lock %s in the escrow", formatSol(s.Terms.SolLamports)),
				Detail: detail,
				Ready:  blocked == "", Primary: blocked == "",
				Blocked: blocked,
			})
		} else {
			m.planZenonFunding(s, st, blocked, add)
		}
	}

	// ------------------------------------------------------------- refund
	if mine.Funded && mine.Expired && !mine.Settled {
		if s.Role.SendsSol {
			add(Action{
				Kind:   "sol.refund",
				Label:  "Refund your escrow",
				Detail: "The timelock has passed, so the escrow can only pay you now.",
				Ready:  true, Primary: true,
			})
		} else {
			// One action, three blocks. The contract can only pay the address
			// that created the entry, and on Zenon that money is not the
			// user's until it has been received there and sent on -- so
			// offering the reclaim alone would be offering the step that
			// changes the leg to "refunded" and none of the steps that pay
			// anyone. zenonReclaimHome does all three; the two below are what
			// is left if it stops part way.
			add(Action{
				Kind:  "znn.reclaim",
				Label: fmt.Sprintf("Reclaim your %s and send it back to your wallet", tokenName(s)),
				Detail: fmt.Sprintf(
					"The expiry has passed, so the contract can only pay the address that created it -- this "+
						"swap's address. This reclaims it there, receives it, and forwards it to %s.",
					homeText(s)),
				Ready: true, Primary: true,
			})
		}
	}
	if mine.Settled && theirs.Funded && !theirs.Settled && !theirs.Expired {
		st.Warnings = append(st.Warnings, fmt.Sprintf(
			"the counterparty has claimed your leg, which published the secret. Claim theirs -- it expires in %s.",
			humanDuration(theirs.SecondsLeft)))
	}

	// ------------------------------------------ the per-swap Zenon account
	if sa := st.SwapAddress; sa != nil {
		if sa.NeedsFunding && sa.Pending > 0 {
			add(Action{
				Kind:    "znn.receive",
				Label:   fmt.Sprintf("Receive %d incoming block(s)", sa.Pending),
				Detail:  "Zenon funds are not spendable until the receiving account publishes a block for them.",
				Ready:   true,
				Primary: !st.Znn.Funded && !st.Znn.Settled,
			})
		}
		// Sweeping is offered whenever the swap address holds something and the
		// Zenon leg is not currently holding it -- after a reclaim, or after an
		// overpayment, or when a swap was abandoned before the HTLC was made.
		if sa.NeedsFunding && sa.Balance != "0" && !st.Znn.Funded && s.ZnnHomeAddress != "" {
			add(Action{
				Kind:    "znn.sweep",
				Label:   fmt.Sprintf("Send %s back to your wallet", sa.Balance),
				Detail:  fmt.Sprintf("Empties this swap's address into %s.", s.ZnnHomeAddress),
				Ready:   true,
				Primary: st.Znn.Refunded,
			})
		}
	}

	if len(st.Actions) == 0 {
		add(Action{
			Kind:   "await.counterparty",
			Label:  "Waiting for the counterparty",
			Detail: "Nothing for you to do. Refresh to pick up their next move.",
		})
	}
}

// planZenonFunding breaks the Zenon side's funding into the steps it actually
// takes, which is more than one because Zenon accounts must receive before they
// can spend.
//
// The first step is not something this page does at all: the user pays the swap
// address from their own wallet, an ordinary send that needs no HTLC support.
// That is the same move ferry's Bitcoin leg makes, and it is why Syrius is
// enough here.
func (m *Manager) planZenonFunding(s *Swap, st *Status, blocked string, add func(Action)) {
	sa := st.SwapAddress
	if sa == nil {
		return
	}
	if !sa.Sufficient {
		if sa.Pending > 0 {
			// The receive action is added elsewhere and is the real next step.
			return
		}
		// Deliberately not gated on `blocked`. This moves the user's money to
		// an address the user controls, which is reversible by sweeping it back
		// -- so making them wait for the counterparty here buys nothing and
		// serializes two steps that need not be. The HTLC below is the
		// irreversible one, and that is where the block belongs.
		add(Action{
			Kind:  "znn.awaitFunding",
			Label: fmt.Sprintf("Send %s %s to this swap's address", amountText(s), tokenName(s)),
			Detail: fmt.Sprintf(
				"From Syrius or any wallet -- it is an ordinary transfer to %s. "+
					"This page holds a key for that address so it can make the HTLC call your wallet cannot, "+
					"and you can send it back to yourself at any point before the HTLC is made.",
				sa.Address),
			Ready:   true,
			Primary: true,
		})
		return
	}
	add(Action{
		Kind:  "znn.create",
		Label: fmt.Sprintf("Create the Zenon HTLC for %s %s", amountText(s), tokenName(s)),
		Detail: fmt.Sprintf(
			"Locks it until %s, payable to %s with the secret, or back to this swap's address after that.",
			humanTime(s.Terms.ZnnExpiry), s.Terms.ZnnReceiver),
		Ready:   blocked == "",
		Blocked: blocked,
		Primary: blocked == "",
	})
}

func expiredNote(l *LegStatus, what string) string {
	if l.Expired {
		return "too late -- " + what + " has expired and can now only be refunded to its sender"
	}
	return ""
}

func chainName(leg string) string {
	if leg == LegSolana {
		return "Solana"
	}
	return "Zenon"
}

// homeText names where a reclaim ends up, for a record that has somewhere to
// put it and for the one that does not.
func homeText(s *Swap) string {
	if s.ZnnHomeAddress == "" {
		return "your own wallet"
	}
	return s.ZnnHomeAddress
}

func tokenName(s *Swap) string {
	if s.Terms.ZnnToken == "zts1qsrxxxxxxxxxxxxxmrhjll" {
		return "QSR"
	}
	if s.Terms.ZnnToken == "zts1znnxxxxxxxxxxxxx9z4ulx" {
		return "ZNN"
	}
	return s.Terms.ZnnToken
}

func amountText(s *Swap) string {
	return formatZnnAmount(s.Terms.ZnnAmount, s.Terms.ZnnDecimals)
}

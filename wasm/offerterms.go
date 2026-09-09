package main

import (
	"fmt"
	"strings"
)

// What an offer PINS, and what it costs when the side answering it changes one.
//
// A swap is two contracts on two chains that never speak to each other. Nothing
// on either chain compares them: each is built by one browser out of that
// browser's own record, and the only thing making them two halves of one trade
// is that both records say the same thing. An offer is how the second record
// gets filled in from the first -- and it fills in an ordinary form, every value
// of which stayed editable afterwards until this check existed.
//
// A changed term is not a validation error at the time. The two sides go on
// agreeing about everything except the field that moved, so the disagreement
// surfaces at whichever chain-touching step first depends on it -- an audit that
// refuses a contract, a verification that refuses an HTLC, a funding that
// arrives short. All of those are after somebody has committed money, and most
// read as the COUNTERPARTY acting in bad faith rather than as a number quietly
// edited on this side.
//
// So terms are checked where the divergence is made rather than where it is
// found, and in Go rather than in the form -- because the form is the thing
// being checked. A create call is a create call whether it came from the page,
// from devtools, or from a session message, and this is where all of them pass.
//
// It refuses rather than warns. A swap built on terms the counterparty never
// agreed to is not a swap.
//
// Deliberately NOT pinned:
//
//   - the destination address and this side's own Zenon address, which are
//     nobody else's business and are what the joiner is supposed to supply;
//   - lockHours. The offer carries the sender's own contract locktime, which
//     says nothing about what this side's should be, and the ordering the swap
//     actually depends on is enforced against the real contract by
//     auditLegOrdering rather than against a number in a form.
func (o *Offer) CheckCreate(network string, p CreateParams) error {
	var bad []string
	add := func(what, theirs, yours string) {
		bad = append(bad, fmt.Sprintf("  · %s — their offer: %s / this form: %s",
			what, show(theirs), show(yours)))
	}

	if !strings.EqualFold(strings.TrimSpace(o.Network), strings.TrimSpace(network)) {
		add("network", o.Network, network)
	}
	// Which side sends Bitcoin, and who invented the secret. These two are not
	// preferences and cannot be negotiated by both parties picking the same
	// answer: the trade has one Bitcoin sender and one initiator, so the
	// answering side takes the other of each.
	if want := o.BTCLeg.Opposite(); p.Leg != want {
		add("your Bitcoin leg", string(want), string(p.Leg))
	}
	if want := oppositeRole(o.FromRole); p.Role != want {
		add("your role", string(want), string(p.Role))
	}
	// Only for the participant. The initiator makes their own secret and never
	// has a hash to match — and an offer sent BY a participant carries the hash
	// they were given, which is not a term the initiator receiving it can be
	// held to.
	if p.Role == RoleParticipant && !sameHex(o.SecretHash, p.SecretHashHex) {
		add("secret hash", o.SecretHash, p.SecretHashHex)
	}
	if o.AmountSats != p.AmountSats {
		add("Bitcoin amount (sats)", fmt.Sprint(o.AmountSats), fmt.Sprint(p.AmountSats))
	}
	// Blank means ZNN on both sides, so a form that leaves the field empty and
	// an offer that names ZNN outright are the same term, not a mismatch.
	theirToken := ZenonLeg{TokenStandard: strings.TrimSpace(o.ZenonToken)}.AgreedToken()
	ourToken := ZenonLeg{TokenStandard: strings.TrimSpace(p.ZenonToken)}.AgreedToken()
	if theirToken != ourToken {
		add("Zenon token", theirToken, ourToken)
	}
	// Compared as amounts, not as strings: "10", "10.0" and "010.00" are one
	// term typed three ways, and refusing those would be this check inventing a
	// disagreement of its own.
	if normalDecimal(o.ZenonAmt) != normalDecimal(p.ZenonAmount) {
		add("Zenon amount", o.ZenonAmt, p.ZenonAmount)
	}
	// The addresses in an offer are the SENDER's, so each one is what this side
	// should be calling the peer. Blank is not a mismatch — both are routinely
	// filled in later, over a session or by hand, and refusing an incomplete
	// form would stop swaps that disagree about nothing. A value that is filled
	// in and different is the whole point of this check.
	if o.ZenonAddr != "" && p.ZenonPeerAddress != "" &&
		strings.TrimSpace(o.ZenonAddr) != strings.TrimSpace(p.ZenonPeerAddress) {
		add("their Zenon address", o.ZenonAddr, p.ZenonPeerAddress)
	}
	if o.PKH != "" && p.CounterpartyPKHHex != "" && !sameHex(o.PKH, p.CounterpartyPKHHex) {
		add("their pubkey hash", o.PKH, p.CounterpartyPKHHex)
	}

	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("this does not match the offer it was filled in from, so the two sides "+
		"would be running different swaps:\n%s\n\nBoth browsers build their own contract out of "+
		"their own record and nothing on either chain compares the two, so a term that differs "+
		"here is not found until money is already committed. Restore their terms, or clear the "+
		"offer and create a swap of your own to propose different ones.",
		strings.Join(bad, "\n"))
}

// show renders a term for the error message, so an emptied field reads as
// something rather than as the sentence losing a word.
func show(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(blank)"
	}
	return s
}

// sameHex compares two hex terms as the values they stand for. Case is not a
// difference: nothing downstream reads these as text, and a pubkey hash that
// came back from a wallet in capitals is the same 20 bytes.
func sameHex(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// normalDecimal puts a typed decimal amount in one form, so two spellings of one
// number compare equal. It does not validate: an amount this cannot make sense
// of is returned trimmed and compared as text, which is the safe direction.
// Whether it is a real amount is decided later by decimalToBaseUnits, the only
// code that knows how many places the token allows.
func normalDecimal(s string) string {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "+"))
	if s == "" {
		return ""
	}
	intPart, frac, hasPoint := strings.Cut(s, ".")
	if strings.ContainsAny(s, "-eE") || strings.Count(s, ".") > 1 {
		return s
	}
	intPart = strings.TrimLeft(intPart, "0")
	if intPart == "" {
		intPart = "0"
	}
	if !hasPoint {
		return intPart
	}
	frac = strings.TrimRight(frac, "0")
	if frac == "" {
		return intPart
	}
	return intPart + "." + frac
}

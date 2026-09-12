package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zenon/ferry-web-v2/wasm/chain"
)

// The Bitcoin leg: a P2SH contract, funded by an ordinary payment and spent
// with a key generated for this one swap.
//
// Everything here was in manager.go when Bitcoin was the only chain with a
// contract. It moved because the questions it answers are now asked of a LEG
// rather than of a swap: which contract, whose funding, which deadline. A swap
// may have one Bitcoin leg, on either side of the trade, or none at all.

// BuildContract fills in the contract once both the secret hash and the
// counterparty's pubkey hash are known.
//
// Which pubkey hash goes in which branch is decided by the direction, and
// getting it backwards would hand the funds to the wrong party, so it is
// derived here rather than left to callers.
func (s *Swap) buildBtcContract(l *Leg) error {
	if l == nil || l.Chain != ChainBTC || l.Btc == nil {
		return errors.New("this swap has no Bitcoin leg on that side")
	}
	if len(s.SecretHash) == 0 {
		return errors.New("secret hash is not set yet")
	}
	if len(l.Btc.CounterpartyPKH) != 20 {
		return errors.New("counterparty pubkey hash is not set yet")
	}
	if l.Btc.Key == nil {
		return errors.New("swap key is missing")
	}
	if l.Expiry == 0 {
		return errors.New("locktime is not set")
	}
	params, err := s.Params()
	if err != nil {
		return err
	}

	var pkhRefund, pkhRedeem []byte
	if l.Dir == DirOut {
		// We fund it, so we hold the refund key and they redeem with the secret.
		pkhRefund, pkhRedeem = l.Btc.Key.PKH, l.Btc.CounterpartyPKH
	} else {
		// They fund it, so they hold the refund key and we redeem with the secret.
		pkhRefund, pkhRedeem = l.Btc.CounterpartyPKH, l.Btc.Key.PKH
	}

	contract, err := BuildContract(pkhRefund, pkhRedeem, l.Expiry, s.SecretHash)
	if err != nil {
		return err
	}
	addr, err := ContractAddress(contract, params)
	if err != nil {
		return err
	}
	l.Btc.Contract = contract
	l.Btc.ContractAddr = addr.String()
	if s.State == StateDraft {
		s.State = StateAwaiting
	}
	s.log("Bitcoin contract built for the %s leg, address %s, locktime %s",
		l.Dir, l.Btc.ContractAddr, utcTime(l.Expiry))
	return nil
}

// SetCounterpartyPKH supplies the other side's pubkey hash and builds the
// contract. Used by the side that funds the Bitcoin contract.
func (m *Manager) SetCounterpartyPKH(id, pkhHex string) (*Swap, error) {
	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, err
	}
	l := sw.Out
	if l == nil || l.Chain != ChainBTC {
		return nil, errors.New("only the side funding a Bitcoin contract sets the counterparty " +
			"pubkey hash; on this swap you receive the Bitcoin, so submit their contract instead")
	}
	pkh, err := hex.DecodeString(strings.TrimSpace(pkhHex))
	if err != nil || len(pkh) != 20 {
		return nil, errors.New("pubkey hash must be 20 bytes of hex")
	}
	l.Btc.CounterpartyPKH = pkh
	if err := sw.buildBtcContract(l); err != nil {
		return nil, err
	}
	if err := m.Store.Save(sw); err != nil {
		return nil, err
	}
	return sw, nil
}

// AuditContract accepts the contract published by the counterparty and checks
// that it actually pays this user under the agreed terms.
//
// The single most important check in the flow for the receiving side: a contract
// committing to the wrong hash, the wrong pubkey hash or too short a locktime is
// one whose funds are not really claimable, and the only moment to catch that is
// before anything is sent on the other leg. It reaches no node — it is arithmetic
// over the script and this swap's own record.
func (m *Manager) AuditContract(id, contractHex string) (*Swap, error) {
	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, err
	}
	l := sw.In
	if l == nil || l.Chain != ChainBTC || l.Btc == nil {
		return nil, errors.New("only the side receiving Bitcoin audits a counterparty contract")
	}
	contract, err := hex.DecodeString(strings.TrimSpace(contractHex))
	if err != nil {
		return nil, fmt.Errorf("contract must be hex: %w", err)
	}
	if l.Btc.Key == nil {
		return nil, errors.New("this swap record has no key, so nothing could redeem the contract")
	}
	details, err := ParseContract(contract)
	if err != nil {
		return nil, err
	}
	params, err := sw.Params()
	if err != nil {
		return nil, err
	}

	if !bytes.Equal(details.PkhRedeem, l.Btc.Key.PKH) {
		return nil, fmt.Errorf("contract lets %s redeem, not this swap's key %s: "+
			"you would not be able to claim these funds",
			hex.EncodeToString(details.PkhRedeem), l.Btc.Key.PKHHex())
	}
	if len(sw.SecretHash) > 0 && !bytes.Equal(details.SecretHash, sw.SecretHash) {
		return nil, fmt.Errorf("contract commits to secret hash %s, this swap uses %s",
			hex.EncodeToString(details.SecretHash), hex.EncodeToString(sw.SecretHash))
	}
	if len(sw.SecretHash) == 0 {
		sw.SecretHash = details.SecretHash
	}

	// The two legs have to be ordered so the INITIATOR's expires last, and this
	// is the moment the counterparty's locktime becomes a fact rather than an
	// assumption. Checking it here is the last point before the user commits
	// anything on the other leg.
	if err := sw.auditLegOrdering(l, details.LockTime); err != nil {
		return nil, err
	}

	addr, err := ContractAddress(contract, params)
	if err != nil {
		return nil, err
	}
	l.Btc.Contract = contract
	l.Btc.ContractAddr = addr.String()
	l.Expiry = details.LockTime
	l.Btc.CounterpartyPKH = details.PkhRefund
	if sw.State == StateDraft {
		sw.State = StateAwaiting
	}
	sw.planOutLeg(time.Now().UTC())
	sw.log("audited counterparty contract %s: redeemable by our key, locktime %s (%s away), "+
		"which is the %s leg",
		l.Btc.ContractAddr, utcTime(details.LockTime),
		time.Until(time.Unix(details.LockTime, 0)).Truncate(time.Minute),
		legOwner(sw.InIsInitiators()))
	if sw.Out != nil && sw.Out.Chain == ChainZNN && sw.Out.Znn != nil &&
		sw.Out.Znn.ExpirationSeconds > 0 && sw.Out.Znn.HtlcID == "" {
		sw.log("create your Zenon HTLC with an expiry of %ds (%dh) so it lands on the correct "+
			"side of this contract's locktime",
			sw.Out.Znn.ExpirationSeconds, sw.Out.Znn.ExpirationHours)
	}

	if err := m.Store.Save(sw); err != nil {
		return nil, err
	}
	return sw, nil
}

// ---------- refresh ----------

// refreshBtc polls Bitcoin and advances one leg: funding detected, its depth
// counted, a refund pre-signed the moment there is something to refund, and the
// spend that emptied the contract read for what it says.
func (m *Manager) refreshBtc(ctx context.Context, sw *Swap, l *Leg) {
	backend := m.Chain
	b := l.Btc
	if b == nil || len(b.Contract) == 0 {
		return // nothing on chain to look at yet
	}
	params, err := sw.Params()
	if err != nil {
		return
	}
	want := l.Sats()

	// Look for the funding output. Keep looking while what we have does not cover
	// the agreed amount and nothing has been spent yet, so a dust payment that
	// arrived first cannot permanently stand in for the real funding.
	if btcFundingIsOpen(l) {
		utxos, err := backend.AddressUTXOs(ctx, b.ContractAddr)
		if err != nil {
			sw.logOnce("could not read the Bitcoin contract address from Esplora; the funding " +
				"check will keep trying")
			return
		}
		best := pickFunding(utxos, want)
		if best != nil && (b.Funding == nil || best.Value > b.Funding.Value) {
			if b.Funding == nil {
				sw.State = StateFunded
				sw.log("funding seen on the %s Bitcoin leg: %s:%d for %d sat (confirmed=%v)",
					l.Dir, best.TxID, best.Vout, best.Value, best.Status.Confirmed)
			} else {
				sw.log("a larger output appeared at %s: switching funding from %s:%d (%d sat) "+
					"to %s:%d (%d sat) and re-signing the refund", b.ContractAddr,
					b.Funding.TxID, b.Funding.Vout, b.Funding.Value,
					best.TxID, best.Vout, best.Value)
				b.RefundTx = nil // it spent the output we just stopped using
			}
			b.Funding = &FundingOutput{
				TxID:        best.TxID,
				Vout:        best.Vout,
				Value:       best.Value,
				Confirmed:   best.Status.Confirmed,
				BlockHeight: best.Status.BlockHeight,
			}
			if len(utxos) > 1 {
				sw.log("NOTE: %s holds %d unspent outputs. Only the one above is spent by a "+
					"redeem or refund; anything else must be recovered separately with the "+
					"Recover page after editing the funding outpoint.", b.ContractAddr, len(utxos))
			}
			if b.Funding.Value < want {
				sw.log("WARNING: the contract holds %d sat but %d sat was agreed; do not proceed "+
					"until this is resolved", b.Funding.Value, want)
			}
		}
	}

	// How deep the funding is. Answering "has it arrived" without answering "is
	// it settled" is what leaves somebody acting on a payment that is still one
	// double-spend away from never having happened.
	trackFundingDepth(ctx, b, backend)

	// A funded contract with nowhere to pay is the one shape where the pre-signed
	// refund silently does not happen.
	if b.Funding != nil && l.Dir == DirOut && l.SelfAddr == "" && !sw.loggedOnce(noDestNote) {
		sw.log("%s", noDestNote)
	}

	// As soon as funding exists and this side holds the refund key, pre-sign the
	// refund so the user can save it. Recovery must not depend on this app still
	// being around later.
	if b.Funding != nil && b.RefundTx == nil && l.Dir == DirOut && l.SelfAddr != "" {
		feeRate, err := backend.FeeRate(ctx, 6)
		if err != nil {
			feeRate = fallbackFeeRate
		}
		refund, err := BuildRefund(b.Contract, *b.Funding, b.Key, l.SelfAddr, feeRate, params)
		if err != nil {
			sw.log("could not pre-sign refund: %v", err)
		} else {
			b.RefundTx = refund
			sw.log("refund transaction pre-signed (%s, %d sat fee); it becomes broadcastable at %s",
				refund.TxID, refund.Fee, utcTime(l.Expiry))
		}
	}

	// Has the contract output been spent?
	if b.Funding != nil && !b.Claimed && !b.Reclaimed {
		spend, err := backend.OutspendOf(ctx, b.Funding.TxID, b.Funding.Vout)
		if err == nil && spend.Spent && spend.TxID != "" {
			if err := m.absorbBtcSpend(ctx, sw, l, spend.TxID, backend); err != nil {
				sw.log("could not inspect spending transaction %s: %v", spend.TxID, err)
			}
		}
	}
}

// btcFundingIsOpen reports whether Refresh should still be looking for a better
// funding output. The contract address is public, so anyone can add outputs to
// it; settling for a stray dust payment would leave the real funding stranded.
func btcFundingIsOpen(l *Leg) bool {
	b := l.Btc
	if b.Claimed || b.Reclaimed || b.RedeemTx != nil {
		return false
	}
	return b.Funding == nil || b.Funding.Value < l.Sats()
}

// pickFunding chooses which output at the contract address is the funding.
//
// Taking whatever the backend listed first would let a single dust payment
// become the funding: the swap would look funded, the pre-signed refund would
// spend a few hundred satoshis, and the real payment would sit untouched.
func pickFunding(utxos []chain.UTXO, want int64) *chain.UTXO {
	var best *chain.UTXO
	for i := range utxos {
		u := &utxos[i]
		if u.Value <= 0 {
			continue
		}
		if best == nil {
			best = u
			continue
		}
		if covers, bestCovers := u.Value >= want, best.Value >= want; covers != bestCovers {
			if covers {
				best = u
			}
			continue
		}
		if u.Value > best.Value {
			best = u
		}
	}
	return best
}

// confirmationsTracked is how deep this app keeps counting. Six is the figure
// the rest of Bitcoin settled on for not coming back, and nothing here behaves
// differently at seven.
const confirmationsTracked = 6

// trackFundingDepth updates how many blocks are on top of the funding.
//
// Quiet about failure on purpose: a depth that could not be read is a stale
// number on a card, not a swap in trouble, and an event line per poll would bury
// the funding notice under it.
func trackFundingDepth(ctx context.Context, b *BtcLeg, backend chain.Backend) {
	if b.Funding == nil || b.Funding.Confirmations >= confirmationsTracked {
		return
	}
	status, err := backend.TxStatus(ctx, b.Funding.TxID)
	if err != nil {
		return
	}
	b.Funding.Confirmed = status.Confirmed
	b.Funding.BlockHeight = status.BlockHeight
	if !status.Confirmed || status.BlockHeight <= 0 {
		// Still in the mempool. Zeroed rather than left alone, because a funding
		// that was mined and then reorganised out has to stop claiming the depth
		// it used to have.
		b.Funding.Confirmations = 0
		return
	}
	tip, err := backend.TipHeight(ctx)
	if err != nil || tip < status.BlockHeight {
		return
	}
	b.Funding.Confirmations = min(tip-status.BlockHeight+1, confirmationsTracked)
}

// absorbBtcSpend inspects the transaction that spent the contract output and
// works out whether it was a redeem — in which case it carries the secret — or a
// refund.
func (m *Manager) absorbBtcSpend(ctx context.Context, sw *Swap, l *Leg, txid string,
	backend chain.Backend) error {

	b := l.Btc
	rawHex, err := backend.RawTx(ctx, txid)
	if err != nil {
		return err
	}
	tx, err := DecodeRawTx(rawHex)
	if err != nil {
		return err
	}
	// The backend named this transaction as the one that spent our output. Check
	// that it actually does before letting it decide anything: a wrong answer
	// here files a live leg away as settled, and stops Refresh looking for the
	// real spend.
	spendsOurs := false
	sigScripts := make([][]byte, 0, len(tx.TxIn))
	for _, in := range tx.TxIn {
		sigScripts = append(sigScripts, in.SignatureScript)
		if in.PreviousOutPoint.Index == b.Funding.Vout &&
			in.PreviousOutPoint.Hash.String() == b.Funding.TxID {
			spendsOurs = true
		}
	}
	if !spendsOurs {
		return fmt.Errorf("the node named %s as the spender of %s:%d, but that transaction does "+
			"not spend it; ignoring rather than acting on it",
			txid, b.Funding.TxID, b.Funding.Vout)
	}

	secret, err := ExtractSecret(sigScripts, sw.SecretHash)
	if err != nil {
		b.Reclaimed = true
		sw.log("the %s Bitcoin contract was spent by %s and no input reveals a preimage matching "+
			"this swap's hash, so it was a refund", l.Dir, txid)
		return nil
	}

	if len(sw.Secret) == 0 {
		sw.Secret = secret
		sw.log("SECRET LEARNED from %s: %s — use it to claim the other leg before it expires",
			txid, hex.EncodeToString(secret))
	}
	b.Claimed = true
	sw.log("the %s Bitcoin contract was redeemed by %s", l.Dir, txid)
	return nil
}

// noDestNote is emitted once for a funded leg that has no destination address.
// Swaps created now cannot reach that state — Create requires one — but a record
// imported from an older backup can, and it is the shape where the pre-signed
// refund is skipped without saying so.
const noDestNote = "WARNING: this swap records no Bitcoin destination address, so the refund " +
	"cannot be pre-signed and neither Redeem nor Refund can build a transaction. Supply one on " +
	"the card, or use the Recover page with the recovery file and an address of your own."

// ---------- spending ----------

// RecordFundingBroadcast notes that a payment to a contract has been sent.
//
// It records nothing beyond the id and the time: this is not evidence that the
// contract is funded, and treating it as such would let anyone who can call it
// mark a swap funded without paying. Refresh remains the only thing that can
// say a contract holds money.
func (m *Manager) RecordFundingBroadcast(id, txid string) (*Swap, error) {
	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, err
	}
	l := sw.Out
	if l == nil || l.Chain != ChainBTC || l.Btc == nil {
		return nil, errors.New("only the side funding a Bitcoin contract broadcasts a funding")
	}
	txid = strings.TrimSpace(txid)
	if !chain.ValidTxID(txid) {
		return nil, fmt.Errorf("%q is not a transaction id", txid)
	}
	if l.Btc.FundingBroadcast != nil && l.Btc.FundingBroadcast.TxID == txid {
		return sw, nil // the same send, reported twice
	}
	l.Btc.FundingBroadcast = &FundingBroadcast{TxID: txid, At: time.Now().UTC()}
	sw.log("funding broadcast as %s; waiting for a node to list it against the contract address",
		txid)
	if err := m.Store.Save(sw); err != nil {
		return nil, err
	}
	return sw, nil
}

// payoutAddress settles where a spend pays, and refuses to be talked out of it.
//
// Both ways out of the contract pay one address of this user's, chosen when the
// swap was created, and the card says so in as many words: this address and no
// other. Bitcoin cannot enforce it — the redeem branch authorises a KEY, with no
// output constraint anywhere in the script — so the refusal has to live here.
//
// Somebody who genuinely must pay elsewhere is not trapped: the recovery file
// exports the key that spends this contract, and building that transaction
// outside this app is the documented way out. What is refused is doing it
// silently, to a swap already committed to an address.
func payoutAddress(l *Leg, asked string) (string, error) {
	asked = strings.TrimSpace(asked)
	if l.SelfAddr == "" {
		if asked == "" {
			return "", errors.New("a destination address is required")
		}
		return asked, nil
	}
	if asked != "" && asked != l.SelfAddr {
		return "", fmt.Errorf("this swap pays %s, fixed when it was created, and cannot be "+
			"redirected to %s — both ways out of the contract pay that address and no other. "+
			"To spend these coins somewhere else, export the recovery file and build the "+
			"transaction yourself", l.SelfAddr, asked)
	}
	return l.SelfAddr, nil
}

// Redeem claims an incoming Bitcoin contract by revealing the secret, then
// broadcasts it.
func (m *Manager) Redeem(ctx context.Context, id, destAddr string) (*Swap, error) {
	backend := m.Chain
	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, err
	}
	l := sw.In
	if l == nil || l.Chain != ChainBTC || l.Btc == nil {
		return nil, errors.New("this swap has no incoming Bitcoin contract to redeem")
	}
	b := l.Btc
	if b.Funding == nil {
		return nil, errors.New("no funding output has been seen yet; refresh first")
	}
	if len(sw.Secret) == 0 {
		return nil, errors.New("the secret is not known yet, so this contract cannot be redeemed")
	}
	destAddr, err = payoutAddress(l, destAddr)
	if err != nil {
		return nil, err
	}
	if time.Now().Unix() >= l.Expiry {
		sw.log("WARNING: redeeming after the timelock at %s. The counterparty can broadcast "+
			"their refund now, so this is a race — if it loses, treat the Bitcoin leg as gone.",
			utcTime(l.Expiry))
	}
	params, err := sw.Params()
	if err != nil {
		return nil, err
	}
	feeRate, err := backend.FeeRate(ctx, 3)
	if err != nil {
		feeRate = fallbackFeeRate
	}
	spend, err := BuildRedeem(b.Contract, *b.Funding, b.Key, sw.Secret, destAddr, feeRate, params)
	if err != nil {
		return nil, err
	}
	txid, err := backend.Broadcast(ctx, spend.RawHex)
	if err != nil {
		return nil, fmt.Errorf("broadcast redeem: %w", err)
	}
	b.RedeemTx = spend
	b.Claimed = true
	if l.SelfAddr == "" {
		l.SelfAddr = destAddr
	}
	sw.log("redeemed to %s in %s (%d sat after a %d sat fee)",
		destAddr, txid, spend.Value, spend.Fee)
	sw.settle()
	if err := m.Store.Save(sw); err != nil {
		return nil, err
	}
	return sw, nil
}

// Refund broadcasts the timelocked reclaim of this user's own Bitcoin contract.
// It rebuilds the transaction at current fee rates if one was not pre-signed.
func (m *Manager) Refund(ctx context.Context, id, destAddr string) (*Swap, error) {
	backend := m.Chain
	sw, err := m.Store.Load(id)
	if err != nil {
		return nil, err
	}
	l := sw.Out
	if l == nil || l.Chain != ChainBTC || l.Btc == nil {
		return nil, errors.New("this swap has no outgoing Bitcoin contract to refund")
	}
	b := l.Btc
	if b.Funding == nil {
		return nil, errors.New("no funding output has been seen yet; refresh first")
	}
	if now := time.Now().Unix(); now < l.Expiry {
		return nil, fmt.Errorf("the timelock does not expire until %s (%s from now)",
			utcTime(l.Expiry), time.Until(time.Unix(l.Expiry, 0)).Truncate(time.Second))
	}
	destAddr, err = payoutAddress(l, destAddr)
	if err != nil {
		return nil, err
	}
	// The pre-signed refund, when there is one, was built to this same address
	// the moment funding appeared — the only address it could have been built to.
	spend := b.RefundTx
	if spend == nil {
		params, err := sw.Params()
		if err != nil {
			return nil, err
		}
		feeRate, err := backend.FeeRate(ctx, 6)
		if err != nil {
			feeRate = fallbackFeeRate
		}
		spend, err = BuildRefund(b.Contract, *b.Funding, b.Key, destAddr, feeRate, params)
		if err != nil {
			return nil, err
		}
	}
	txid, err := backend.Broadcast(ctx, spend.RawHex)
	if err != nil {
		// CHECKLOCKTIMEVERIFY is evaluated against the block's median time past,
		// which trails real time by roughly an hour, so a refund broadcast the
		// moment the wall clock passes the locktime is normal to see bounced.
		return nil, fmt.Errorf("broadcast refund: %w (if this says non-final, the locktime has "+
			"passed on your clock but not yet in the chain's median time past, which trails "+
			"real time by about an hour — retry shortly)", err)
	}
	b.RefundTx = spend
	b.Reclaimed = true
	if l.SelfAddr == "" && destAddr != "" {
		l.SelfAddr = destAddr
	}
	sw.log("refunded in %s (%d sat after a %d sat fee)", txid, spend.Value, spend.Fee)
	sw.settle()
	if err := m.Store.Save(sw); err != nil {
		return nil, err
	}
	return sw, nil
}

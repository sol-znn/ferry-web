package znn

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Reading the Zenon ledger directly, for the two questions the HTLC contract
// itself cannot answer. Both come from one fact: an entry is DELETED when it is
// unlocked or reclaimed.
//
//   - "data non existent" from embedded.htlc.getById is ambiguous. The id was
//     never on this chain, OR the swap worked and the entry has been consumed.
//     Opposite pieces of news, the same error.
//   - the preimage a counterparty reveals by unlocking is public for as long as
//     their transaction is in the ledger, which is forever -- but it is in the
//     transaction, not in the entry.
//
// Both are answered by reading account blocks, which a node serves to anybody:
// an HTLC id is the hash of the block that created it, and an unlock is an
// ordinary send to the contract carrying the preimage in its data.

// HtlcContractAddress is the embedded HTLC contract, from go-zenon's
// common/types/address.go. Every create, unlock and reclaim is a send to it.
const HtlcContractAddress = "z1qxemdeddedxhtlcxxxxxxxxxxxxxxxxxygecvw"

// ErrNoResult is what a JSON-RPC null result becomes. A sentinel rather than a
// message because callers have to tell "the node answered, and the answer is
// that this does not exist" apart from "the call failed" -- go-zenon answers an
// unknown hash with a null result and an unknown HTLC id with an error.
var ErrNoResult = errors.New("the node returned no result")

// AccountBlock is the subset of a block this package reads. The node marshals
// Data as []byte, so it arrives base64-encoded.
type AccountBlock struct {
	Hash      string `json:"hash"`
	Address   string `json:"address"`
	ToAddress string `json:"toAddress"`
	BlockType uint64 `json:"blockType"`
	Height    uint64 `json:"height"`
	Data      string `json:"data"`
	// FromBlockHash is the block this one is the receive of. On the HTLC
	// contract's own chain it is the caller's transaction -- the create, unlock
	// or reclaim -- which is the only place the arguments of the call survive.
	FromBlockHash    string          `json:"fromBlockHash"`
	DescendantBlocks []*AccountBlock `json:"descendantBlocks"`

	ConfirmationDetail *struct {
		MomentumHeight    uint64 `json:"momentumHeight"`
		MomentumTimestamp int64  `json:"momentumTimestamp"`
	} `json:"confirmationDetail"`
}

// DataBytes decodes the block's data payload.
func (b *AccountBlock) DataBytes() []byte {
	if b == nil || b.Data == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(b.Data)
	if err != nil {
		return nil
	}
	return raw
}

// ToHtlcContract reports whether this block is a call into the HTLC contract.
func (b *AccountBlock) ToHtlcContract() bool {
	return b != nil && strings.EqualFold(b.ToAddress, HtlcContractAddress)
}

// AccountBlockList is one page of an address's chain.
type AccountBlockList struct {
	List  []*AccountBlock `json:"list"`
	Count int             `json:"count"`
	More  bool            `json:"more"`
}

// GetAccountBlockByHash fetches one block. A hash the node has never seen comes
// back as ErrNoResult rather than as a failure.
func (c *Client) GetAccountBlockByHash(ctx context.Context, hash string) (*AccountBlock, error) {
	var b AccountBlock
	if err := c.call(ctx, "ledger.getAccountBlockByHash", []any{hash}, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// AccountBlocksByPage reads one page of an address's own chain, newest first.
func (c *Client) AccountBlocksByPage(ctx context.Context, address string, page, size int) (*AccountBlockList, error) {
	var out AccountBlockList
	if err := c.call(ctx, "ledger.getAccountBlocksByPage", []any{address, page, size}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// HtlcFate is what became of an id that embedded.htlc.getById will not return.
type HtlcFate struct {
	// Existed is true when a block with this hash is in the ledger and was a
	// call into the HTLC contract — so the entry was real and has since been
	// consumed, rather than never having been there.
	Existed bool
	// CreatedBy is the address that made it, when the block was a create.
	CreatedBy string
	// Explanation is the sentence to show the user.
	Explanation string
}

// WhatHappenedTo explains an HTLC id the contract no longer knows about.
//
// The distinction decides what the user should do next. An id not in the ledger
// at all is a typo, a wrong network or an unconfirmed transaction -- all fixed
// by the user. An id that IS in the ledger and is nevertheless gone from the
// contract was unlocked or reclaimed, which is usually the swap working, and the
// useful next step is to go and find the preimage.
func (c *Client) WhatHappenedTo(ctx context.Context, htlcID string) HtlcFate {
	block, err := c.GetAccountBlockByHash(ctx, htlcID)
	if err != nil || block == nil || block.Hash == "" {
		if err != nil && !errors.Is(err, ErrNoResult) {
			return HtlcFate{Explanation: fmt.Sprintf(
				"The HTLC contract has no entry with this id, and the id could not be looked up in "+
					"the ledger either (%v). Check the id, and that this browser's Zenon node is on "+
					"the same network as the swap.", err)}
		}
		return HtlcFate{Explanation: "No entry with this id, and no transaction with this hash " +
			"anywhere in the ledger. Either the id is mistyped, this browser's Zenon node is on a " +
			"different network from the one the HTLC was created on, or the create has not been " +
			"confirmed yet — a new one takes a couple of momentums to appear."}
	}
	if !block.ToHtlcContract() {
		return HtlcFate{Existed: false, Explanation: fmt.Sprintf(
			"This hash is a real transaction, but it was sent to %s rather than to the HTLC "+
				"contract, so it never created an HTLC. It is probably the wrong hash — an HTLC id "+
				"is the hash of the transaction that created it.", block.ToAddress)}
	}
	return HtlcFate{
		Existed:   true,
		CreatedBy: block.Address,
		Explanation: "This HTLC was real and is no longer in the contract, which means it has been " +
			"unlocked or reclaimed — the entry is deleted either way. If it was unlocked, the " +
			"preimage is public in the unlocking transaction; ferry looks for it on Refresh.",
	}
}

// FindPreimage looks for the secret an unlock published, given the address the
// HTLC pays.
//
// It matches by hashing rather than by decoding the contract's ABI: every
// candidate 32-byte window in the block's data is hashed and compared to the
// hash both legs committed to. The preimage is self-identifying, so nothing has
// to be known about the encoding around it, and a false positive would require
// finding a second preimage of SHA-256. That is also what keeps both searches
// below honest.
//
// Two searches, because there are two ways to reach the same transaction and
// only the second always works. The first assumes the party being paid is the
// party who called Unlock and searches their own sends -- one page and no
// guesswork when it holds.
//
// It does not always hold, and the contract is explicit about why: `Unlock` may
// be called by ANYONE and pays `hashLocked` regardless. That is the property this
// whole application rests on -- it is what lets a wallet holding nothing settle
// this leg -- so an unlock published by an address this swap has never heard of
// is the normal case, not the exotic one. Found exactly that way: a live unlock
// that really had published the preimage, which the counterparty could not find.
func (c *Client) FindPreimage(ctx context.Context, payee string, secretHash []byte,
	maxPages, pageSize int) ([]byte, error) {

	if len(secretHash) != HashLockSize {
		return nil, fmt.Errorf("secret hash must be %d bytes, got %d", HashLockSize, len(secretHash))
	}
	// A blank payee is not a refusal. The payee was only ever an optimisation --
	// the preimage is self-identifying, so the contract's own chain can be walked
	// without it, and the cost of not knowing is fetching candidates a filter
	// would have skipped rather than being unable to look.
	//
	// It matters in exactly the case that matters most: a counterparty who has
	// gone quiet. A swap can reach the point of needing this preimage without
	// anybody having typed the other side's Zenon address in, so refusing to look
	// because a convenience field is empty made an uncooperative counterparty into
	// a lost swap.
	payee = strings.TrimSpace(payee)
	if payee != "" {
		if secret, err := c.preimageFromSends(ctx, payee, secretHash, maxPages, pageSize); err == nil {
			return secret, nil
		}
	}
	return c.preimageFromContract(ctx, payee, secretHash, maxPages, pageSize)
}

// preimageFromSends searches one address's own sends to the HTLC contract.
func (c *Client) preimageFromSends(ctx context.Context, unlocker string, secretHash []byte,
	maxPages, pageSize int) ([]byte, error) {

	for page := range maxPages {
		list, err := c.AccountBlocksByPage(ctx, unlocker, page, pageSize)
		if err != nil {
			return nil, err
		}
		for _, b := range list.List {
			if secret := searchBlock(b, secretHash); secret != nil {
				return secret, nil
			}
		}
		if !list.More || len(list.List) == 0 {
			break
		}
	}
	return nil, fmt.Errorf("no transaction from %s to the HTLC contract reveals a preimage "+
		"matching this swap's hash", unlocker)
}

// preimageFromContract finds the unlock without assuming who published it.
//
// What is true of every unlock, whoever called it, is its shape in the
// CONTRACT's own chain: the contract receives the call, and that receive carries
// a descendant paying the entry's `hashLocked`. So the contract's chain is
// walked, the payout identifies which blocks are worth looking at, and the
// caller's transaction -- reached through the receive's `fromBlockHash`, because
// the receive does not carry the call's arguments -- is where the preimage is.
//
// Only candidates are fetched, which is what keeps this affordable on a chain
// where the contract is busy.
//
// `payee` is that filter and nothing more, which is why it may be empty.
// Identification is by hashing, so the filter only decides how many caller
// blocks get fetched, never which answer comes back. Without it every call the
// contract received is a candidate: slower, just as correct, and the difference
// between a slow search and no search at all for somebody who never recorded
// the counterparty's address.
func (c *Client) preimageFromContract(ctx context.Context, payee string, secretHash []byte,
	maxPages, pageSize int) ([]byte, error) {

	payee = strings.TrimSpace(payee)
	for page := range maxPages {
		list, err := c.AccountBlocksByPage(ctx, HtlcContractAddress, page, pageSize)
		if err != nil {
			return nil, err
		}
		for _, b := range list.List {
			if b == nil || isZeroHash(b.FromBlockHash) {
				continue
			}
			if payee != "" && !paysTo(b, payee) {
				continue
			}
			caller, err := c.GetAccountBlockByHash(ctx, b.FromBlockHash)
			if err != nil || caller == nil {
				// One candidate that cannot be read is not a reason to stop
				// looking at the others.
				continue
			}
			if secret := searchBlock(caller, secretHash); secret != nil {
				return secret, nil
			}
		}
		if !list.More || len(list.List) == 0 {
			break
		}
	}
	if payee == "" {
		return nil, fmt.Errorf("no call to the HTLC contract in the last %d blocks reveals a "+
			"preimage matching this swap's hash; it may not have been unlocked yet",
			maxPages*pageSize)
	}
	return nil, fmt.Errorf("no unlock paying %s reveals a preimage matching this swap's hash; "+
		"they may not have unlocked it yet", payee)
}

// paysTo reports whether this block settles anything to `addr`.
//
// An embedded contract answers a call by attaching descendant blocks to its own
// receive, so the payout is not the block itself but hangs off it.
func paysTo(b *AccountBlock, addr string) bool {
	for _, d := range b.DescendantBlocks {
		if d != nil && strings.EqualFold(strings.TrimSpace(d.ToAddress), strings.TrimSpace(addr)) {
			return true
		}
	}
	return false
}

// isZeroHash reports whether a hash field is absent — either empty or the
// all-zero hash a node sends for "no such block".
func isZeroHash(h string) bool {
	h = strings.TrimSpace(h)
	if h == "" {
		return true
	}
	return strings.Trim(h, "0") == ""
}

// searchBlock looks in one block and the blocks batched under it.
func searchBlock(b *AccountBlock, secretHash []byte) []byte {
	if b == nil {
		return nil
	}
	if b.ToHtlcContract() {
		if secret := scanForPreimage(b.DataBytes(), secretHash); secret != nil {
			return secret
		}
	}
	// A block can carry descendants, and a batched unlock is still an unlock.
	for _, d := range b.DescendantBlocks {
		if secret := searchBlock(d, secretHash); secret != nil {
			return secret
		}
	}
	return nil
}

// scanForPreimage slides a 32-byte window over data looking for the preimage.
// A byte at a time rather than in ABI-sized steps: the encoding pads to 32-byte
// boundaries today, but a scan that assumes the alignment silently stops working
// if that changes, and hashing a few hundred windows costs nothing.
func scanForPreimage(data, secretHash []byte) []byte {
	const size = HashLockSize
	if len(data) < size {
		return nil
	}
	for i := 0; i+size <= len(data); i++ {
		sum := sha256.Sum256(data[i : i+size])
		if bytes.Equal(sum[:], secretHash) {
			return bytes.Clone(data[i : i+size])
		}
	}
	return nil
}

// HtlcCandidate is one call into the HTLC contract that looks like the create
// this swap is waiting for. The block's own hash IS the HTLC id, which is what
// makes this search possible at all: nothing has to be decoded to name the
// entry, only found.
type HtlcCandidate struct {
	// ID is the block hash, which the contract uses as the entry's id.
	ID string
	// At is the momentum timestamp the block was confirmed in, so the caller
	// can prefer the most recent when more than one candidate verifies.
	At int64
}

// FindHtlcCreates scans creator's own chain for sends to the HTLC contract whose
// data carries hashLock, newest first.
//
// The hashlock is the discriminator rather than the amount, the token or the
// timestamp, and it is the strongest of the four: a create's ABI-encoded
// arguments contain it verbatim, and it is the one term a counterparty cannot
// vary without breaking their own swap. Two creates from the same address for
// the same amount an hour apart are indistinguishable by the other three.
//
// Still only a shortlist. Every candidate goes through GetHtlcByID and Verify,
// so this saves the copy-paste, not a single check.
func (c *Client) FindHtlcCreates(ctx context.Context, creator string, hashLock []byte,
	maxPages, pageSize int) ([]HtlcCandidate, error) {

	if len(hashLock) != HashLockSize {
		return nil, fmt.Errorf("hashlock must be %d bytes, got %d", HashLockSize, len(hashLock))
	}
	if strings.TrimSpace(creator) == "" {
		return nil, errors.New("no Zenon address to search: this swap does not record who creates " +
			"the HTLC. Fill in the Zenon addresses on the swap and try again")
	}
	var found []HtlcCandidate
	for page := range maxPages {
		list, err := c.AccountBlocksByPage(ctx, creator, page, pageSize)
		if err != nil {
			return nil, err
		}
		for _, b := range list.List {
			found = collectCreates(b, hashLock, found)
		}
		if !list.More || len(list.List) == 0 {
			break
		}
	}
	return found, nil
}

// collectCreates appends every block in this subtree that commits to hashLock.
//
// Descendants are searched for the same reason FindPreimage searches them: a
// create sent as part of a batch is still a create, and its own hash is still
// the id the contract will answer to.
func collectCreates(b *AccountBlock, hashLock []byte, into []HtlcCandidate) []HtlcCandidate {
	if b == nil {
		return into
	}
	if b.ToHtlcContract() && b.Hash != "" && bytes.Contains(b.DataBytes(), hashLock) {
		var at int64
		if b.ConfirmationDetail != nil {
			at = b.ConfirmationDetail.MomentumTimestamp
		}
		into = append(into, HtlcCandidate{ID: b.Hash, At: at})
	}
	for _, d := range b.DescendantBlocks {
		into = collectCreates(d, hashLock, into)
	}
	return into
}

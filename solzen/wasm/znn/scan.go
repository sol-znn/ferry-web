package znn

import (
	"context"
	"crypto/sha256"

	"github.com/zenon-network/go-zenon/common/types"
)

// Reading the htlc contract's own account chain.
//
// An embedded contract on Zenon has an account like any other, and every call
// to it leaves a receive block there paired with the caller's send. The send
// carries the ABI-encoded call, so the contract's chain is a complete, ordered
// log of everything anyone asked it to do -- which is the only place two facts
// a swap needs actually exist:
//
//   * the id of an HTLC, which is the hash of the block that created it and is
//     not derivable from its contents; and
//   * the preimage that unlocked one, since a successful unlock deletes the
//     entry and takes the secret out of contract storage with it.
//
// Scanning is bounded. On a chain where the contract is busy this would need an
// index, and the UI keeps a manual paste as the fallback -- but it means the
// common case asks the user for nothing.

// DefaultScanBlocks is how far back a scan reaches by default.
const DefaultScanBlocks = 2000

// Outcome is what became of an HTLC that no longer exists.
type Outcome struct {
	Unlocked  bool
	Preimage  []byte
	UnlockTx  types.Hash
	Reclaimed bool
	ReclaimTx types.Hash
}

// Settled reports whether anything at all was found. An entry that is gone from
// storage but has neither result on the visible chain is one whose settlement
// is further back than the scan reached.
func (o *Outcome) Settled() bool { return o.Unlocked || o.Reclaimed }

// FindHtlcOutcome looks for the Unlock or Reclaim that ended an HTLC.
//
// hashLock is what tells a real unlock from one shaped like it. Publishing a
// send to the htlc contract costs a fee and nothing else -- the contract
// declines a call it does not like, but the *block* is on the chain either way,
// and this scan walks blocks. So an Unlock naming the right id and carrying a
// preimage that opens nothing is one transaction for an attacker, and, being
// newer than the real unlock, it wins a scan that stops at the first match.
//
// What it wins is the secret. The entry is deleted when it is unlocked, so the
// unlocking block is the only remaining copy of the preimage; a decoy in its
// place leaves the counterparty unable to claim the other leg, which then
// expires back to whoever published the decoy. Identification is therefore by
// hashing, not by shape: a candidate whose preimage does not hash to the
// hashlock this swap committed to is not this swap's unlock, and the scan
// carries on past it.
func (c *Client) FindHtlcOutcome(ctx context.Context, id types.Hash, hashLock []byte, maxBlocks int) (*Outcome, error) {
	out := &Outcome{}
	err := c.scanContract(ctx, maxBlocks, func(send *AccountBlock) bool {
		if gotID, preimage, ok := decodeUnlock(send.Data); ok && gotID == id {
			if !matchesHashLock(preimage, hashLock) {
				return false
			}
			out.Unlocked = true
			out.Preimage = preimage
			out.UnlockTx = send.Hash
			return true
		}
		if gotID, ok := decodeReclaim(send.Data); ok && gotID == id {
			out.Reclaimed = true
			out.ReclaimTx = send.Hash
			return true
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// FindHtlcByHashlock looks for the Create that opened an HTLC committing to
// this hashlock and paying this address.
//
// Both are matched, not just the hashlock: the hashlock is public the moment an
// offer is sent, so anyone could create an entry quoting it. Only one that also
// pays the agreed address is this swap's leg -- and even that is a candidate,
// not a conclusion, which is why the caller re-reads the entry by id and checks
// every field against the terms.
func (c *Client) FindHtlcByHashlock(ctx context.Context, hashLock []byte, hashLocked types.Address, maxBlocks int) (types.Hash, bool, error) {
	var found types.Hash
	ok := false
	err := c.scanContract(ctx, maxBlocks, func(send *AccountBlock) bool {
		p, valid := decodeCreate(send.Data)
		if !valid || p.HashLocked != hashLocked || !bytesEqual(p.HashLock, hashLock) {
			return false
		}
		// The id of an HTLC is the hash of the block that created it.
		found, ok = send.Hash, true
		return true
	})
	if err != nil {
		return types.Hash{}, false, err
	}
	return found, ok, nil
}

// scanContract walks the htlc contract's account chain newest first, handing
// each paired send block to visit until it returns true or the budget runs out.
func (c *Client) scanContract(ctx context.Context, maxBlocks int, visit func(send *AccountBlock) bool) error {
	if maxBlocks <= 0 {
		maxBlocks = DefaultScanBlocks
	}
	const pageSize = 50
	scanned := 0
	for page := 0; scanned < maxBlocks; page++ {
		blocks, err := c.AccountBlocksByPage(ctx, HtlcContract, page, pageSize)
		if err != nil {
			return err
		}
		for _, b := range blocks {
			scanned++
			send := b.PairedAccountBlock
			if send == nil || len(send.Data) == 0 {
				continue
			}
			if visit(send) {
				return nil
			}
		}
		if len(blocks) < pageSize {
			return nil
		}
	}
	return nil
}

type createParam struct {
	HashLocked     types.Address
	ExpirationTime int64
	HashType       uint8
	KeyMaxSize     uint8
	HashLock       []byte
}

func decodeCreate(data []byte) (createParam, bool) {
	var p createParam
	if err := ABIHtlc.UnpackMethod(&p, "Create", data); err != nil {
		return p, false
	}
	return p, true
}

func decodeReclaim(data []byte) (types.Hash, bool) {
	var id types.Hash
	if err := ABIHtlc.UnpackMethod(&id, "Reclaim", data); err != nil {
		return types.Hash{}, false
	}
	return id, true
}

// sha256Of is the commitment both chains use. SHA3-256 is the htlc contract's
// other option and is not available on Solana, so a swap of this shape is
// always SHA-256 and a preimage is always checked with it.
func sha256Of(preimage []byte) []byte {
	sum := sha256.Sum256(preimage)
	return sum[:]
}

// matchesHashLock is what makes a candidate this swap's settlement rather than
// one shaped like it. Named and separate because it is the whole of the
// decision the scan makes, and a decision worth a test of its own.
//
// An empty preimage is not a match: no secret this system produces is empty,
// and the Solana program refuses one outright, so accepting it here would be
// the two chains disagreeing about what a settlement is.
func matchesHashLock(preimage, hashLock []byte) bool {
	if len(preimage) == 0 || len(hashLock) != HashLockSize {
		return false
	}
	return bytesEqual(sha256Of(preimage), hashLock)
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func decodeUnlock(data []byte) (types.Hash, []byte, bool) {
	var param struct {
		Id       types.Hash
		Preimage []byte
	}
	if err := ABIHtlc.UnpackMethod(&param, "Unlock", data); err != nil {
		return types.Hash{}, nil, false
	}
	return param.Id, param.Preimage, true
}

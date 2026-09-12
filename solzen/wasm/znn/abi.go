package znn

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/abi"
)

// jsonHtlc is go-zenon's own ABI for the htlc embedded contract, copied from
// vm/embedded/definition/htlc.go rather than imported.
//
// Importing it would be better, and does not work: that package reaches
// common/db for the storage helpers on HtlcInfo, common/db reaches goleveldb,
// and goleveldb has no js/wasm build -- it wants a file lock. The ABI itself is
// a string constant with no dependencies, so copying it costs nothing but a
// note to keep it in step. `vm/abi`, which does the encoding, imports cleanly
// and is used as-is: this is the description, not the encoder.
const jsonHtlc = `
	[
		{"type":"function","name":"Create", "inputs":[
			{"name":"hashLocked","type":"address"},
			{"name":"expirationTime","type":"int64"},
			{"name":"hashType","type":"uint8"},
			{"name":"keyMaxSize","type":"uint8"},
			{"name":"hashLock","type":"bytes"}
		]},
		{"type":"function","name":"Reclaim","inputs":[
			{"name":"id","type":"hash"}
		]},
		{"type":"function","name":"Unlock","inputs":[
			{"name":"id","type":"hash"},
			{"name":"preimage","type":"bytes"}
		]},

		{"type":"variable","name":"htlcInfo","inputs":[
			{"name":"timeLocked","type":"address"},
			{"name":"hashLocked","type":"address"},
			{"name":"tokenStandard","type":"tokenStandard"},
			{"name":"amount","type":"uint256"},
			{"name":"expirationTime", "type":"int64"},
			{"name":"hashType","type":"uint8"},
			{"name":"keyMaxSize","type":"uint8"},
			{"name":"hashLock","type":"bytes"}
		]},

		{"type":"function","name":"DenyProxyUnlock","inputs":[]},
		{"type":"function","name":"AllowProxyUnlock","inputs":[]}
	]`

// ABIHtlc encodes calls to the htlc contract.
var ABIHtlc = abi.JSONToABIContract(strings.NewReader(jsonHtlc))

// Hash types the htlc contract accepts. A cross-chain swap must use SHA256:
// it is the only one of the two that the Solana program can compute, and
// picking the other is the mistake that produces an HTLC nobody can unlock.
const (
	HashTypeSHA3   uint8 = 0
	HashTypeSHA256 uint8 = 1
)

// PreimageSize is the preimage length this app commits to on both chains, and
// the keyMaxSize it asks the htlc contract for. The Solana program caps its own
// preimage at the same 32 bytes, so a preimage one chain accepts is one the
// other accepts.
const PreimageSize = 32

// HtlcContract is the embedded htlc contract's address.
var HtlcContract = types.HtlcContract

// ZnnTokenStandard and QsrTokenStandard are the two tokens with names. Anything
// else is named by its zts directly.
const (
	ZnnTokenStandard = "zts1znnxxxxxxxxxxxxx9z4ulx"
	QsrTokenStandard = "zts1qsrxxxxxxxxxxxxxmrhjll"
)

// PackCreate builds the data for a Create call.
//
// hashLocked is the address the funds go to on a successful unlock. It is the
// counterparty's own address -- not anything this app controls -- which is what
// makes the contract, rather than this app, the thing holding the money.
func PackCreate(hashLocked types.Address, expirationTime int64, hashType, keyMaxSize uint8, hashLock []byte) ([]byte, error) {
	return ABIHtlc.PackMethod("Create", hashLocked, expirationTime, hashType, keyMaxSize, hashLock)
}

// PackUnlock builds the data for an Unlock call: reveal the preimage, and the
// contract pays hashLocked. The caller need not be hashLocked -- see
// ProxyUnlock in client.go.
func PackUnlock(id types.Hash, preimage []byte) ([]byte, error) {
	return ABIHtlc.PackMethod("Unlock", id, preimage)
}

// PackReclaim builds the data for a Reclaim call: after expiry, the contract
// pays timeLocked. Only timeLocked may call it, which is why the funding side
// of a swap has to be an account this app can sign for.
func PackReclaim(id types.Hash) ([]byte, error) {
	return ABIHtlc.PackMethod("Reclaim", id)
}

// HtlcInfo is one live entry in the htlc contract, as the node reports it.
//
// It is a local type rather than definition.HtlcInfo for the same reason the
// ABI is copied: that struct lives in the package that reaches goleveldb. The
// fields and their JSON names match, so the node's response decodes into it
// unchanged.
type HtlcInfo struct {
	Id             types.Hash    `json:"id"`
	TimeLocked     types.Address `json:"timeLocked"`
	HashLocked     types.Address `json:"hashLocked"`
	TokenStandard  string        `json:"tokenStandard"`
	Amount         *big.Int      `json:"-"`
	AmountRaw      string        `json:"amount"`
	ExpirationTime int64         `json:"expirationTime"`
	HashType       uint8         `json:"hashType"`
	KeyMaxSize     uint8         `json:"keyMaxSize"`
	// HashLock is decoded by Client.Htlc rather than by encoding/json, because
	// which encoding it arrives in is not decidable by trying one. See
	// DecodeHashLock.
	HashLock    []byte `json:"-"`
	HashLockRaw string `json:"hashLock"`
}

// HashLockSize is the digest length both hash types produce: SHA3-256 and
// SHA-256 are each 32 bytes.
const HashLockSize = 32

// DecodeHashLock reads the hashlock a node reported.
//
// go-zenon marshals it as []byte, so it arrives base64-encoded, and that is the
// case that matters. Some tooling hands back hex instead -- and the two cannot
// be told apart by trying one and falling back on an error, because a
// 64-character hex string is ALSO valid base64 and decodes, without complaint,
// into 48 meaningless bytes. So both are decoded and the one that yields a
// digest-sized result wins.
//
// Getting this wrong is not a security failure in the dangerous direction: 48
// bytes never match a 32-byte hashlock, so the leg is refused. It is a
// legitimate HTLC refused, which for the side waiting to be paid is its own
// kind of expensive.
func DecodeHashLock(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == HashLockSize {
		return b, nil
	}
	if b, err := hex.DecodeString(strings.TrimPrefix(s, "0x")); err == nil && len(b) == HashLockSize {
		return b, nil
	}
	// Neither encoding produced a digest. Return the base64 reading when there
	// is one so the caller reports a hashlock mismatch, which says more than
	// "undecodable".
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return nil, fmt.Errorf("the hashlock %q is neither base64 nor hex", s)
}

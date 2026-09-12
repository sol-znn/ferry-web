package znn

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/btcsuite/btcd/btcutil/bech32"
	"golang.org/x/crypto/sha3"
)

// Encoding calls to the htlc contract.
//
// The rest of this package reads the Zenon ledger; this file is the one place
// that writes to it, in the only sense a page can -- by producing the bytes a
// wallet will sign. Ferry still holds no Zenon key.
//
// The encoding is go-zenon's, hand-rolled here rather than imported. Importing
// `vm/abi` would be better and is not possible at this size: it reaches
// go-ethereum's `common`, and `vm/embedded/definition` -- where the htlc ABI
// lives -- reaches `common/db` and goleveldb, which has no js/wasm build. See
// ARCHITECTURE.md for why the module size is a budget rather than an
// observation. So the three encodings are written out and pinned by
// htlcabi_test.go to vectors produced by go-zenon's own encoder: a divergence is
// a failing test, not an HTLC nobody can unlock.
//
// The rules, from go-zenon's vm/abi:
//
//   - a method id is the first 4 bytes of SHA3-256 over "Name(type,type,...)",
//     where the type names are the strings in the ABI JSON. SHA3-256 proper, not
//     Keccak -- common/crypto.Hash is sha3.New256.
//   - arguments are 32-byte words, Ethereum-style: static values inline, dynamic
//     ones (`bytes`) as a byte offset in the head and [length, right-padded
//     value] in the tail.
//   - `address` and `hash` are LEFT-padded; `bytes` payloads are RIGHT-padded.

// wordSize is abi.WordSize.
const wordSize = 32

// AddressSize is a Zenon address in bytes: a one-byte kind followed by 19 bytes
// of core. From go-zenon's common/types/address.go.
const AddressSize = 20

// addressPrefix is the bech32 human-readable part every Zenon address carries.
const addressPrefix = "z"

// TokenStandardSize is a ZTS in bytes. From go-zenon's
// common/types/tokenstandard.go, where it is ZenonTokenStandardSize.
const TokenStandardSize = 10

// tokenStandardPrefix is the bech32 human-readable part every ZTS carries —
// three letters where an address carries one, which is what makes "an address
// pasted into a token field" and "a token pasted into an address field" both
// catchable without ever looking past the prefix.
const tokenStandardPrefix = "zts"

// HashSize is the size of a Zenon hash, which is what an HTLC id is.
const HashSize = 32

// The three method signatures, copied from the htlc ABI in go-zenon's
// vm/embedded/definition/htlc.go. They are strings rather than a parsed ABI
// because the id is all that is derived from them and the argument order is
// written out below in any case.
const (
	sigCreate  = "Create(address,int64,uint8,uint8,bytes)"
	sigUnlock  = "Unlock(hash,bytes)"
	sigReclaim = "Reclaim(hash)"
)

// methodID is the 4-byte selector go-zenon's abi.Method.Id() produces.
func methodID(signature string) []byte {
	sum := sha3.Sum256([]byte(signature))
	return sum[:4]
}

// ParseAddress decodes a z1 address into its 20 bytes.
//
// The checksum is the point. An address is the party an HTLC pays, and a
// mistyped one is money sent somewhere nobody holds a key for — so it is decoded
// here, where a bad one is an error, rather than passed through as a string that
// only the chain will judge.
func ParseAddress(addr string) ([]byte, error) {
	return parseZenonBech32(addr, "Zenon address", addressPrefix, AddressSize)
}

// ParseTokenStandard decodes a zts1 token standard into its 10 bytes.
//
// A token standard is as much a term of a trade as the amount, and this catches
// the pasting accident this pair of fields invites -- an address where a token
// belongs, or the reverse. Both are bech32 and both start with "z", so a length
// check alone lets a 20-byte address through a field wanting 10 bytes of token,
// silently truncated rather than refused. Checking the human-readable part first
// turns that into an error naming the prefix that was actually there.
func ParseTokenStandard(zts string) ([]byte, error) {
	return parseZenonBech32(zts, "Zenon token", tokenStandardPrefix, TokenStandardSize)
}

// parseZenonBech32 is ParseAddress and ParseTokenStandard's shared decode: same
// encoding, same failure modes, different prefix and length. `kind` is what the
// error calls the thing being parsed, so a bad value in the peer-address field
// is reported as a bad Zenon address and one in the token field as a bad Zenon
// token, never as a bad "bech32 string" that leaves the reader guessing which
// field it came from.
func parseZenonBech32(s, kind, wantHRP string, wantLen int) ([]byte, error) {
	hrp, data, err := bech32.Decode(s)
	if err != nil {
		return nil, fmt.Errorf("%q is not a valid %s: %w", s, kind, err)
	}
	if hrp != wantHRP {
		return nil, fmt.Errorf("%q is not a %s: its prefix is %q, not %q", s, kind, hrp, wantHRP)
	}
	raw, err := bech32.ConvertBits(data, 5, 8, true)
	if err != nil {
		return nil, fmt.Errorf("%q is not a valid %s: %w", s, kind, err)
	}
	// ConvertBits with padding can leave a trailing zero byte; go-zenon's own
	// parse takes the result as-is and then requires an exact length, which is
	// what rejects both a short value and a padded-long one.
	if len(raw) != wantLen {
		return nil, fmt.Errorf("%q decodes to %d bytes, and a %s is %d", s, len(raw), kind, wantLen)
	}
	return raw, nil
}

// PackCreate builds the data for htlc.Create.
//
// hashLocked is the address the contract pays on a successful unlock — the
// counterparty's own address, never one this app controls. expirationTime is
// absolute Unix seconds, compared by the contract against momentum time rather
// than against any clock in this browser.
func PackCreate(hashLocked []byte, expirationTime int64, hashType, keyMaxSize uint8,
	hashLock []byte) ([]byte, error) {

	if len(hashLocked) != AddressSize {
		return nil, fmt.Errorf("hashLocked must be %d bytes, got %d", AddressSize, len(hashLocked))
	}
	if len(hashLock) == 0 {
		return nil, errors.New("a hashlock is required")
	}
	if expirationTime <= 0 {
		return nil, errors.New("expirationTime must be an absolute Unix time")
	}

	out := methodID(sigCreate)
	// Four static arguments, then one dynamic — so the tail starts five words in.
	const head = 5 * wordSize
	out = append(out, leftPad(hashLocked)...)
	out = append(out, packInt(big.NewInt(expirationTime))...)
	out = append(out, packInt(big.NewInt(int64(hashType)))...)
	out = append(out, packInt(big.NewInt(int64(keyMaxSize)))...)
	out = append(out, packInt(big.NewInt(head))...)
	out = append(out, packBytes(hashLock)...)
	return out, nil
}

// PackUnlock builds the data for htlc.Unlock: reveal the preimage, and the
// contract pays hashLocked — which need not be the caller. See the proxy-unlock
// note on VerifyParams.
func PackUnlock(id, preimage []byte) ([]byte, error) {
	if len(id) != HashSize {
		return nil, fmt.Errorf("an HTLC id must be %d bytes, got %d", HashSize, len(id))
	}
	if len(preimage) == 0 {
		return nil, errors.New("a preimage is required")
	}
	out := methodID(sigUnlock)
	const head = 2 * wordSize
	out = append(out, leftPad(id)...)
	out = append(out, packInt(big.NewInt(head))...)
	out = append(out, packBytes(preimage)...)
	return out, nil
}

// PackReclaim builds the data for htlc.Reclaim: after expiry, the contract pays
// timeLocked. Only timeLocked may call it, which is why the side that funds a
// Zenon leg has to create it from an address it can still sign with later.
func PackReclaim(id []byte) ([]byte, error) {
	if len(id) != HashSize {
		return nil, fmt.Errorf("an HTLC id must be %d bytes, got %d", HashSize, len(id))
	}
	out := methodID(sigReclaim)
	return append(out, leftPad(id)...), nil
}

// leftPad places a value in the low-order end of a word, which is how the
// encoder treats addresses, hashes and token standards.
func leftPad(v []byte) []byte {
	word := make([]byte, wordSize)
	if len(v) >= wordSize {
		copy(word, v[len(v)-wordSize:])
		return word
	}
	copy(word[wordSize-len(v):], v)
	return word
}

// packInt encodes a number as one word. Only non-negative values occur here —
// an expiry, two sizes and an offset — so the two's-complement branch of
// go-zenon's U256 is unreachable and is not reproduced.
func packInt(v *big.Int) []byte {
	return leftPad(v.Bytes())
}

// packBytes is the tail of a dynamic argument: its length, then the value
// right-padded out to a whole number of words.
func packBytes(v []byte) []byte {
	out := packInt(big.NewInt(int64(len(v))))
	padded := (len(v) + wordSize - 1) / wordSize * wordSize
	body := make([]byte, padded)
	copy(body, v)
	return append(out, body...)
}

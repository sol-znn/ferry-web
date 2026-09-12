package sol

import (
	"crypto/sha256"
	"fmt"

	"filippo.io/edwards25519"
)

// PubkeyLen is the length of a Solana address.
const PubkeyLen = 32

// Pubkey is a Solana address: 32 bytes, shown as base58.
type Pubkey [PubkeyLen]byte

// SystemProgram is 32 zero bytes, base58 "11111111111111111111111111111111".
var SystemProgram Pubkey

func ParsePubkey(s string) (Pubkey, error) {
	var p Pubkey
	raw, err := DecodeBase58(s)
	if err != nil {
		return p, fmt.Errorf("%q is not a Solana address: %w", s, err)
	}
	if len(raw) != PubkeyLen {
		return p, fmt.Errorf("a Solana address is %d bytes, %q decodes to %d", PubkeyLen, s, len(raw))
	}
	copy(p[:], raw)
	return p, nil
}

func (p Pubkey) String() string       { return EncodeBase58(p[:]) }
func (p Pubkey) Bytes() []byte        { return p[:] }
func (p Pubkey) IsZero() bool         { return p == Pubkey{} }
func (p Pubkey) Equals(q Pubkey) bool { return p == q }

// MarshalText and UnmarshalText keep a Pubkey as base58 in JSON, which is what
// every other tool shows, rather than as an array of numbers.
func (p Pubkey) MarshalText() ([]byte, error) { return []byte(p.String()), nil }

func (p *Pubkey) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*p = Pubkey{}
		return nil
	}
	q, err := ParsePubkey(string(b))
	if err != nil {
		return err
	}
	*p = q
	return nil
}

// pdaMarker is appended before hashing, so that a program-derived address can
// never collide with a real ed25519 public key that someone holds the key for.
const pdaMarker = "ProgramDerivedAddress"

// FindProgramAddress is Solana's find_program_address.
//
// It is derived here rather than taken from the JavaScript side on purpose.
// Verifying a counterparty's escrow means checking that the account holding the
// money is the one this swap id implies -- if that check ran on a value the
// page's own JS supplied, it would be checking the page against itself.
//
// The loop is the definition: hash the seeds with a candidate bump, and accept
// the first result that is *not* a point on the curve. An off-curve address has
// no private key, which is exactly why only the program can sign for it.
func FindProgramAddress(seeds [][]byte, programID Pubkey) (Pubkey, uint8, error) {
	for bump := 255; bump >= 0; bump-- {
		candidate, err := createProgramAddress(append(seeds, []byte{byte(bump)}), programID)
		if err == nil {
			return candidate, uint8(bump), nil
		}
	}
	return Pubkey{}, 0, fmt.Errorf("no program address could be derived from these seeds")
}

// createProgramAddress hashes one seed set, and fails if the result lands on
// the curve -- the same condition Solana's own create_program_address reports.
func createProgramAddress(seeds [][]byte, programID Pubkey) (Pubkey, error) {
	h := sha256.New()
	for _, s := range seeds {
		if len(s) > 32 {
			return Pubkey{}, fmt.Errorf("a seed may be at most 32 bytes, got %d", len(s))
		}
		h.Write(s)
	}
	h.Write(programID[:])
	h.Write([]byte(pdaMarker))

	var out Pubkey
	copy(out[:], h.Sum(nil))
	if isOnCurve(out) {
		return Pubkey{}, fmt.Errorf("the derived address is a valid public key")
	}
	return out, nil
}

// isOnCurve reports whether the bytes decompress to a point on ed25519.
//
// Decoding is the test: SetBytes rejects a value that is not a canonical
// encoding of a curve point, which is precisely the "no private key exists for
// this" property a PDA needs.
func isOnCurve(p Pubkey) bool {
	_, err := new(edwards25519.Point).SetBytes(p[:])
	return err == nil
}

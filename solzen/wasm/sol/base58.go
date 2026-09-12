package sol

import (
	"fmt"
	"math/big"
)

// Base58, Bitcoin alphabet -- which is also Solana's. Every address, signature
// and transaction id in this package crosses the JS boundary in this encoding,
// because that is the only form the RPC and the wallets speak.
const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var decodeMap = func() [256]int8 {
	var m [256]int8
	for i := range m {
		m[i] = -1
	}
	for i, c := range []byte(alphabet) {
		m[c] = int8(i)
	}
	return m
}()

// EncodeBase58 encodes bytes. Leading zero bytes become leading '1's, which is
// the part a naive big.Int implementation drops -- and dropping it produces a
// short address that looks plausible and is wrong.
func EncodeBase58(b []byte) string {
	zeros := 0
	for zeros < len(b) && b[zeros] == 0 {
		zeros++
	}
	x := new(big.Int).SetBytes(b)
	radix := big.NewInt(58)
	mod := new(big.Int)
	out := make([]byte, 0, len(b)*137/100+1)
	for x.Sign() > 0 {
		x.DivMod(x, radix, mod)
		out = append(out, alphabet[mod.Int64()])
	}
	for i := 0; i < zeros; i++ {
		out = append(out, alphabet[0])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// DecodeBase58 decodes a string, refusing any character outside the alphabet
// rather than skipping it. A typo'd address must fail here; silently dropping
// the bad character would produce a different, valid-looking key.
func DecodeBase58(s string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("empty")
	}
	x := new(big.Int)
	radix := big.NewInt(58)
	for i := 0; i < len(s); i++ {
		v := decodeMap[s[i]]
		if v < 0 {
			return nil, fmt.Errorf("%q is not a base58 character", s[i])
		}
		x.Mul(x, radix)
		x.Add(x, big.NewInt(int64(v)))
	}
	zeros := 0
	for zeros < len(s) && s[zeros] == alphabet[0] {
		zeros++
	}
	raw := x.Bytes()
	out := make([]byte, zeros+len(raw))
	copy(out[zeros:], raw)
	return out, nil
}

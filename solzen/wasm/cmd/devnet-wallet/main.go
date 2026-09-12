// devnet-wallet stands in for Syrius in the automated tests.
//
// It is a test fixture and nothing else. The application never derives a key
// from a mnemonic, never asks for one, and has no code path that could -- that
// is the whole point of the per-swap keys in wasm/znn/keys.go. But a test needs
// somebody to play the part of the user's wallet: to pay the swap address, and
// to fuse plasma to it so the swap's own blocks publish without minutes of
// proof of work. Those are ordinary wallet operations that Syrius performs with
// a click each, and this performs them without a person.
//
//	devnet-wallet address  -index 1
//	devnet-wallet balance  -index 1
//	devnet-wallet send     -index 1 -to <addr> -amount 10 [-token ZNN]
//	devnet-wallet fuse     -index 1 -to <addr> -amount 50
//	devnet-wallet receive  -index 1
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/pbkdf2"

	zt "github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon-network/go-zenon/vm/abi"
	"github.com/zenon/solzen/wasm/znn"
)

// The committed devnet mnemonic from go-zenon/docker/devnet/README.md. These
// keys are public, they exist in a git repository, and they must never be used
// anywhere but a local devnet.
const devnetMnemonic = "abstract affair idle position alien fluid board ordinary exist afraid " +
	"chapter wood wood guide sun walnut crew perfect place firm poverty model " +
	"side million"

// Zenon's coin type, from SLIP-44.
const zenonCoinType = 73404

const jsonPlasma = `[{"type":"function","name":"Fuse","inputs":[{"name":"address","type":"address"}]}]`

var abiPlasma = abi.JSONToABIContract(strings.NewReader(jsonPlasma))

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	url := fs.String("url", envOr("SOLZEN_ZENON_URL", "http://127.0.0.1:35997"), "Zenon node HTTP JSON-RPC URL")
	index := fs.Int("index", 1, "account index in the devnet mnemonic")
	to := fs.String("to", "", "destination address")
	amount := fs.String("amount", "", "decimal amount")
	token := fs.String("token", "ZNN", "ZNN, QSR or a zts")
	mnemonic := fs.String("mnemonic", devnetMnemonic, "BIP-39 mnemonic")
	_ = fs.Parse(os.Args[2:])

	key, err := keyFromMnemonic(*mnemonic, *index)
	check(err)
	client := znn.New(*url)
	ctx := context.Background()

	switch cmd {
	case "address":
		fmt.Println(key.Address)

	case "balance":
		balances, err := client.Balances(ctx, key.Address)
		check(err)
		fmt.Println(key.Address)
		for zts, b := range balances {
			symbol := b.Symbol
			if symbol == "" {
				symbol = zts
			}
			fmt.Printf("  %-6s %s\n", symbol, znn.FormatAmount(b.Amount, b.Decimals))
		}

	case "receive":
		n := 0
		for {
			pending, err := client.Unreceived(ctx, key.Address, 20)
			check(err)
			if len(pending) == 0 {
				break
			}
			op, err := client.PrepareReceive(ctx, key, pending[0].Hash)
			check(err)
			_, err = client.PublishOp(ctx, key, op, mineProgress)
			check(err)
			n++
			time.Sleep(2 * time.Second)
		}
		fmt.Printf("received %d\n", n)

	case "send":
		dest, err := zt.ParseAddress(*to)
		check(err)
		zts, decimals := resolveToken(ctx, client, *token)
		value, err := znn.ParseAmount(*amount, decimals)
		check(err)
		op, err := client.PrepareSend(ctx, key, dest, value, zts)
		check(err)
		hash, err := client.PublishOp(ctx, key, op, mineProgress)
		check(err)
		fmt.Println(hash)

	case "fuse":
		// Fusing QSR to an address is what turns every later block from that
		// address into an instant publish instead of a proof-of-work grind. It
		// is a normal Syrius operation and it is reversible: the QSR is
		// reclaimed by cancelling the fusion.
		dest, err := zt.ParseAddress(*to)
		check(err)
		data, err := abiPlasma.PackMethod("Fuse", dest)
		check(err)
		value, err := znn.ParseAmount(*amount, 8)
		check(err)
		op, err := client.Prepare(ctx, key, znn.BlockTypeUserSend, zt.PlasmaContract, value, znn.QsrTokenStandard, data, zt.Hash{})
		check(err)
		hash, err := client.PublishOp(ctx, key, op, mineProgress)
		check(err)
		ok, err := client.WaitForContractResult(ctx, hash, 90*time.Second)
		check(err)
		fmt.Printf("%s confirmed=%v\n", hash, ok)

	default:
		usage()
		os.Exit(2)
	}
}

func resolveToken(ctx context.Context, c *znn.Client, spec string) (string, int) {
	zts := spec
	switch strings.ToUpper(spec) {
	case "ZNN", "":
		zts = znn.ZnnTokenStandard
	case "QSR":
		zts = znn.QsrTokenStandard
	}
	t, err := c.Token(ctx, zts)
	check(err)
	return t.TokenStandard, t.Decimals
}

// keyFromMnemonic derives m/44'/73404'/index' the way every Zenon wallet does:
// a BIP-39 seed, then SLIP-0010 for ed25519, which has hardened derivation only.
func keyFromMnemonic(mnemonic string, index int) (*znn.Key, error) {
	seed := pbkdf2.Key([]byte(normalize(mnemonic)), []byte("mnemonic"), 2048, 64, sha512.New)

	sum := hmacSHA512([]byte("ed25519 seed"), seed)
	k, chain := sum[:32], sum[32:]
	for _, i := range []uint32{44, zenonCoinType, uint32(index)} {
		data := make([]byte, 0, 37)
		data = append(data, 0x00)
		data = append(data, k...)
		data = binary.BigEndian.AppendUint32(data, i|0x80000000)
		sum = hmacSHA512(chain, data)
		k, chain = sum[:32], sum[32:]
	}
	return znn.KeyFromSeed(k)
}

func hmacSHA512(key, data []byte) []byte {
	h := hmac.New(sha512.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func normalize(s string) string { return strings.Join(strings.Fields(s), " ") }

var lastProgress time.Time

func mineProgress(hashes uint64) {
	if time.Since(lastProgress) < time.Second {
		return
	}
	lastProgress = time.Now()
	fmt.Fprintf(os.Stderr, "\rmining plasma: %.1fM hashes", float64(hashes)/1e6)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "\ndevnet-wallet:", err)
		os.Exit(1)
	}
}

func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func usage() {
	fmt.Fprint(os.Stderr, `devnet-wallet -- stands in for Syrius in tests. Devnet keys only.

  devnet-wallet address [-index N]
  devnet-wallet balance [-index N]
  devnet-wallet receive [-index N]
  devnet-wallet send    -to ADDR -amount N [-token ZNN] [-index N]
  devnet-wallet fuse    -to ADDR -amount N [-index N]
`)
}

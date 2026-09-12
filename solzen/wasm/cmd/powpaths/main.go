// powpaths exercises both ways a Zenon block gets paid for, on one fresh
// address, and the rule that separates them.
//
// Issue #9 suspected the node of pricing a block at zero for an account with
// no plasma, letting the page publish work it should have done. This walks the
// boundary directly:
//
//  1. unfused  -> the node prices an unlock at full difficulty   (the PoW path)
//  2. unfused  -> a block that CLAIMS fused plasma it lacks is refused
//  3. fuse 60 QSR
//  4. fused    -> the node prices the same unlock at zero        (the plasma path)
//  5. fused    -> the same block publishes, unmined, and the chain records it
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	zt "github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon/solzen/wasm/znn"
)

var failures int

func check(name string, ok bool, detail string) {
	mark := "ok  "
	if !ok {
		mark = "FAIL"
		failures++
	}
	fmt.Printf("%s %s\n", mark, name)
	if detail != "" {
		fmt.Printf("       -- %s\n", detail)
	}
}

func main() {
	url := flag.String("url", "http://127.0.0.1:35997", "zenon rpc")
	wallet := flag.String("wallet", `..\bin\devnet-wallet.exe`, "devnet-wallet binary")
	qsr := flag.String("qsr", "60", "QSR to fuse in step 3")
	flag.Parse()

	c := znn.New(*url)
	ctx := context.Background()

	key, err := znn.NewKey()
	if err != nil {
		die(err)
	}
	fmt.Printf("fresh address %s\n\n", key.Address)

	unlockProbe, _ := znn.PackUnlock(zt.Hash{}, make([]byte, znn.PreimageSize))
	cost := func() *znn.RequiredPoW {
		req, err := c.RequiredPoWFor(ctx, key.Address, znn.BlockTypeUserSend, &znn.HtlcContract, unlockProbe)
		if err != nil {
			die(err)
		}
		return req
	}

	// 0. The two callers. ZenonCostOf quotes a price from a synthetic call of
	// the right shape; Prepare asks again with the real arguments just before
	// publishing. Issue #9 supposed one of them could answer zero while the
	// other did not, which would skip the mine.
	fmt.Println("=== 0. the price quote and the publish path ask the same question")
	agree := true
	var disagreed []string
	for _, kind := range []string{"znn.receive", "znn.create", "znn.unlock", "znn.reclaim", "znn.sweep"} {
		q := ask(ctx, c, kind, key.Address, quoteArgs)
		p := ask(ctx, c, kind, key.Address, realArgs)
		if q != p {
			agree = false
			disagreed = append(disagreed, fmt.Sprintf("%s: quote=%d publish=%d", kind, q, p))
		}
	}
	check("both give the same difficulty for all five actions", agree,
		strings.Join(append(disagreed, "receive/sweep 31.5M, create 78.75M, unlock/reclaim 110.25M"), "; "))

	// 1. The PoW path.
	fmt.Println("\n=== 1. unfused: what does the node charge for an htlc.Unlock?")
	before := cost()
	check("the node prices the unlock at full difficulty",
		before.RequiredDifficulty == 110_250_000 && before.AvailablePlasma == 0,
		fmt.Sprintf("available=%d base=%d difficulty=%d", before.AvailablePlasma, before.BasePlasma, before.RequiredDifficulty))

	// 2. The rule between the two paths: claiming plasma you do not have.
	fmt.Println("\n=== 2. unfused: a block that claims fused plasma it does not have")
	op, err := c.PrepareSend(ctx, key, key.Address, big.NewInt(0), znn.ZnnTokenStandard)
	if err != nil {
		die(err)
	}
	sendBase, err := c.RequiredPoWFor(ctx, key.Address, znn.BlockTypeUserSend, &key.Address, nil)
	if err != nil {
		die(err)
	}
	forged := op.Block
	forged.Difficulty = 0
	forged.Nonce = "0000000000000000"
	forged.FusedPlasma = sendBase.BasePlasma // a lie: the account has none
	forged.Sign(key)
	perr := c.Publish(ctx, forged)
	check("the chain refuses it", perr != nil, describe(perr))

	// 3. Fuse.
	fmt.Printf("\n=== 3. fusing %s QSR to it\n", *qsr)
	out, err := exec.Command(*wallet, "fuse", "-index", "1", "-to", key.Address.String(), "-amount", *qsr).CombinedOutput()
	if err != nil {
		die(fmt.Errorf("devnet-wallet fuse: %v: %s", err, out))
	}
	fmt.Printf("       %s\n", strings.TrimSpace(string(out)))

	deadline := time.Now().Add(3 * time.Minute)
	var after *znn.RequiredPoW
	for {
		after = cost()
		if after.RequiredDifficulty == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Second)
	}

	// 4. The plasma path.
	fmt.Println("\n=== 4. fused: the same question again")
	check("the node now prices the same unlock at zero, and says why",
		after.RequiredDifficulty == 0 && after.AvailablePlasma >= after.BasePlasma,
		fmt.Sprintf("available=%d base=%d difficulty=%d", after.AvailablePlasma, after.BasePlasma, after.RequiredDifficulty))

	// 5. Publishing on the plasma path, honestly this time.
	fmt.Println("\n=== 5. fused: publishing without mining")
	op2, err := c.PrepareSend(ctx, key, key.Address, big.NewInt(0), znn.ZnnTokenStandard)
	if err != nil {
		die(err)
	}
	check("the publish path agrees no work is needed", !op2.NeedsWork(),
		fmt.Sprintf("difficulty=%d fusedPlasma claimed=%d", op2.Block.Difficulty, op2.Block.FusedPlasma))
	started := time.Now()
	hash, err := c.PublishOp(ctx, key, op2, nil)
	if err != nil {
		die(err)
	}
	check("it published", true, fmt.Sprintf("%s in %.1fs", hash, time.Since(started).Seconds()))

	// What the chain recorded is the part that settles the question. The read
	// model here drops the plasma fields, so ask the node for the raw block.
	var landed struct {
		Difficulty  uint64 `json:"difficulty"`
		FusedPlasma uint64 `json:"fusedPlasma"`
		BasePlasma  uint64 `json:"basePlasma"`
		Hash        string `json:"hash"`
	}
	found := false
	for i := 0; i < 30; i++ {
		if err := rpc(ctx, *url, &landed, "ledger.getAccountBlockByHash", hash.String()); err == nil && landed.Hash != "" {
			found = true
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !found {
		check("the chain recorded the block", false, "not found")
	} else {
		check("the chain recorded it as paid with plasma, not work",
			landed.Difficulty == 0 && landed.FusedPlasma >= landed.BasePlasma,
			fmt.Sprintf("difficulty=%d fusedPlasma=%d basePlasma=%d", landed.Difficulty, landed.FusedPlasma, landed.BasePlasma))
	}

	fmt.Println()
	if failures > 0 {
		fmt.Printf("%d failed\n", failures)
		os.Exit(1)
	}
	fmt.Println("both paths behave as documented")
}

// quoteArgs mirrors ZenonCostOf: a synthetic call of the right shape, with
// zero arguments. realArgs mirrors Prepare: the same shapes, real arguments.
type argStyle int

const (
	quoteArgs argStyle = iota
	realArgs
)

func ask(ctx context.Context, c *znn.Client, kind string, self zt.Address, style argStyle) uint64 {
	id := zt.Hash{}
	preimage := make([]byte, znn.PreimageSize)
	hashlock := make([]byte, 32)
	expiry := int64(0)
	payee := zt.Address{}
	if style == realArgs {
		id = zt.NewHash([]byte("an id"))
		for i := range preimage {
			preimage[i] = byte(i + 1)
		}
		for i := range hashlock {
			hashlock[i] = byte(255 - i)
		}
		expiry = time.Now().Unix() + 7200
		payee = self
	}

	var blockType uint64 = znn.BlockTypeUserSend
	var data []byte
	to := znn.HtlcContract
	switch kind {
	case "znn.receive":
		blockType = znn.BlockTypeUserReceive
	case "znn.create":
		data, _ = znn.PackCreate(payee, expiry, znn.HashTypeSHA256, znn.PreimageSize, hashlock)
	case "znn.unlock":
		data, _ = znn.PackUnlock(id, preimage)
	case "znn.reclaim":
		data, _ = znn.PackReclaim(id)
	case "znn.sweep":
		to = self
	}
	var toPtr *zt.Address
	if blockType == znn.BlockTypeUserSend {
		toPtr = &to
	}
	req, err := c.RequiredPoWFor(ctx, self, blockType, toPtr, data)
	if err != nil {
		die(err)
	}
	return req.RequiredDifficulty
}

func rpc(ctx context.Context, url string, out any, method string, params ...any) error {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if envelope.Error != nil {
		return fmt.Errorf("%s: %s", method, envelope.Error.Message)
	}
	return json.Unmarshal(envelope.Result, out)
}

func describe(err error) string {
	if err == nil {
		return "it was ACCEPTED -- a block was published for free"
	}
	return err.Error()
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "powpaths:", err)
	os.Exit(1)
}

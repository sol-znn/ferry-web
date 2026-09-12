// unlockscan finds every htlc call on the devnet and prints what the chain
// actually charged for it.
//
// Issue #9 asks how a --pow run published an htlc.Unlock without doing the
// work the node priced at 110,250,000. The chain still holds both runs, so
// rather than reasoning about it, read the blocks: difficulty, fusedPlasma and
// basePlasma are all recorded, and enoughPlasma() will only have accepted a
// block whose powPlasma+fusedPlasma covered its basePlasma.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	zt "github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon/solzen/wasm/znn"
)

type detailed struct {
	Momentum struct {
		Height    uint64 `json:"height"`
		Timestamp int64  `json:"timestamp"`
	} `json:"momentum"`
	Blocks []struct {
		Address     string `json:"address"`
		ToAddress   string `json:"toAddress"`
		BlockType   uint64 `json:"blockType"`
		Height      uint64 `json:"height"`
		Hash        string `json:"hash"`
		Data        string `json:"data"`
		Difficulty  uint64 `json:"difficulty"`
		FusedPlasma uint64 `json:"fusedPlasma"`
		BasePlasma  uint64 `json:"basePlasma"`
		TotalPlasma uint64 `json:"usedPlasma"`
		Nonce       string `json:"nonce"`
	} `json:"blocks"`
}

func main() {
	url := flag.String("url", "http://127.0.0.1:35997", "zenon rpc")
	from := flag.Uint64("from", 1, "first momentum")
	flag.Parse()

	c := znn.New(*url)
	ctx := context.Background()

	tip, err := c.FrontierMomentum(ctx)
	if err != nil {
		die(err)
	}
	fmt.Fprintf(os.Stderr, "scanning momentums %d..%d\n", *from, tip.Height)

	// Selectors for the three htlc methods this app sends.
	create, _ := znn.PackCreate(zt.Address{}, 0, znn.HashTypeSHA256, znn.PreimageSize, make([]byte, 32))
	unlock, _ := znn.PackUnlock(zt.Hash{}, make([]byte, znn.PreimageSize))
	reclaim, _ := znn.PackReclaim(zt.Hash{})
	name := map[string]string{
		hex.EncodeToString(create[:4]):  "htlc.Create",
		hex.EncodeToString(unlock[:4]):  "htlc.Unlock",
		hex.EncodeToString(reclaim[:4]): "htlc.Reclaim",
	}
	htlc := znn.HtlcContract.String()

	fmt.Printf("%-19s %-7s %-12s %-40s %6s %11s %7s %7s %s\n",
		"when", "mom", "method", "from", "height", "difficulty", "fused", "base", "mined")
	for h := *from; h <= tip.Height; h += 1024 {
		n := uint64(1024)
		if h+n > tip.Height+1 {
			n = tip.Height + 1 - h
		}
		var page struct {
			List []detailed `json:"list"`
		}
		if err := call(ctx, *url, &page, "ledger.getDetailedMomentumsByHeight", h, n); err != nil {
			die(err)
		}
		for _, d := range page.List {
			for _, b := range d.Blocks {
				if b.ToAddress != htlc || b.BlockType != znn.BlockTypeUserSend {
					continue
				}
				raw, err := base64.StdEncoding.DecodeString(b.Data)
				if err != nil || len(raw) < 4 {
					continue
				}
				method, ok := name[hex.EncodeToString(raw[:4])]
				if !ok {
					method = "htlc.?" + hex.EncodeToString(raw[:4])
				}
				mined := "no"
				if b.Difficulty > 0 {
					mined = "YES"
				}
				fmt.Printf("%-19s %-7d %-12s %-40s %6d %11d %7d %7d %s\n",
					time.Unix(d.Momentum.Timestamp, 0).UTC().Format("2006-01-02 15:04:05"),
					d.Momentum.Height, method, b.Address, b.Height,
					b.Difficulty, b.FusedPlasma, b.BasePlasma, mined)
			}
		}
	}
}

func call(ctx context.Context, url string, out any, method string, params ...any) error {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	raw, err := post(ctx, url, body)
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

func die(err error) {
	fmt.Fprintln(os.Stderr, "unlockscan:", err)
	os.Exit(1)
}

func post(ctx context.Context, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 120 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	return io.ReadAll(res.Body)
}

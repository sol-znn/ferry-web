// Package chain talks to a Bitcoin chain data source.
//
// The backend speaks the Esplora REST API rather than Bitcoin Core's wallet
// RPC, which is what removes the "run a fully synced full node with RPC
// credentials" requirement. Esplora is served publicly by mempool.space and
// blockstream.info, and can be self-hosted by anyone who would rather not
// share their address queries with a third party.
//
// Under GOOS=js this net/http client is backed by the browser's Fetch API, so
// every request is subject to the same-origin policy: an Esplora instance is
// only usable from a page if it answers preflight with permissive CORS headers.
// The public instances do. A self-hosted one may need `Access-Control-Allow-Origin`
// adding to its reverse proxy.
//
// Nothing here ever sees a private key. A backend is used only to read public
// chain state and to broadcast already-signed transactions.
package chain

// net/http is deliberately absent from this import list, including for its
// constants: importing it links its package initialisers, which is precisely
// the 8 MB of TLS and HTTP/2 that httpx exists to keep out of the build.
// net/url is a different package with none of those dependencies -- it is
// string handling and nothing else -- so escaping a path segment costs nothing.
import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/zenon/ferry-web-v2/wasm/httpx"
)

// Client is an Esplora REST client.
type Client struct {
	BaseURL string
	Timeout time.Duration
}

// New returns a client for the given Esplora base URL, e.g.
// https://mempool.space/api or https://blockstream.info/testnet/api.
func New(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Timeout: 30 * time.Second,
	}
}

// UTXO is an unspent output paying to a watched address.
type UTXO struct {
	TxID   string `json:"txid"`
	Vout   uint32 `json:"vout"`
	Value  int64  `json:"value"`
	Status Status `json:"status"`
}

// Status is the confirmation state of a transaction or output.
type Status struct {
	Confirmed   bool   `json:"confirmed"`
	BlockHeight int64  `json:"block_height"`
	BlockHash   string `json:"block_hash"`
	BlockTime   int64  `json:"block_time"`
}

// Vin is one input of a transaction.
type Vin struct {
	TxID         string `json:"txid"`
	Vout         uint32 `json:"vout"`
	ScriptSigHex string `json:"scriptsig"`
	Sequence     uint32 `json:"sequence"`
}

// Tx is a decoded transaction as returned by Esplora.
type Tx struct {
	TxID   string `json:"txid"`
	Vin    []Vin  `json:"vin"`
	Status Status `json:"status"`
}

// Outspend reports whether a specific output has been spent, and by which
// transaction. This is how the secret is located once the counterparty
// redeems.
type Outspend struct {
	Spent  bool   `json:"spent"`
	TxID   string `json:"txid"`
	Vin    uint32 `json:"vin"`
	Status Status `json:"status"`
}

func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	resp, err := httpx.Do(ctx, httpx.Request{
		Method:  "GET",
		URL:     c.BaseURL + path,
		Timeout: c.Timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	if !resp.OK() {
		return nil, fmt.Errorf("GET %s: %d %s: %s", path, resp.Status, resp.StatusText, resp.Text())
	}
	return resp.Body, nil
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	body, err := c.get(ctx, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// TipHeight returns the current best block height.
func (c *Client) TipHeight(ctx context.Context) (int64, error) {
	body, err := c.get(ctx, "/blocks/tip/height")
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(body)), 10, 64)
}

// ValidTxID reports whether s is a 64-character hex transaction id.
//
// Every txid this client sends back out in a URL came from a previous response,
// so it is a value the node chose rather than one the user typed. That makes it
// worth checking twice over: a value carrying `/` or `..` would walk the path of
// the request built from it, and a value that is not a txid at all has no
// business being written onto a swap record as its funding outpoint.
func ValidTxID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// AddressUTXOs returns the unspent outputs paying to addr.
func (c *Client) AddressUTXOs(ctx context.Context, addr string) ([]UTXO, error) {
	var utxos []UTXO
	if err := c.getJSON(ctx, "/address/"+url.PathEscape(addr)+"/utxo", &utxos); err != nil {
		return nil, err
	}
	for i := range utxos {
		if !ValidTxID(utxos[i].TxID) {
			return nil, fmt.Errorf("the node listed an output with %q as its txid, which is not a "+
				"transaction id", utxos[i].TxID)
		}
	}
	return utxos, nil
}

// TxStatus reports whether a transaction has been mined, and where.
//
// This is how the funding's depth is followed after it has been adopted.
// AddressUTXOs carries the same Status, but the scan that calls it stops the
// moment the contract holds the agreed amount — which is exactly when the user
// starts wanting to know how many blocks are on top of their payment. One
// transaction by id is also a far cheaper question than the whole address.
func (c *Client) TxStatus(ctx context.Context, txid string) (Status, error) {
	if !ValidTxID(txid) {
		return Status{}, fmt.Errorf("%q is not a transaction id", txid)
	}
	var st Status
	if err := c.getJSON(ctx, "/tx/"+txid+"/status", &st); err != nil {
		return Status{}, err
	}
	return st, nil
}

// RawTx returns the raw serialized transaction as hex.
func (c *Client) RawTx(ctx context.Context, txid string) (string, error) {
	if !ValidTxID(txid) {
		return "", fmt.Errorf("%q is not a transaction id", txid)
	}
	body, err := c.get(ctx, "/tx/"+txid+"/hex")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}

// OutspendOf reports whether output vout of txid has been spent.
func (c *Client) OutspendOf(ctx context.Context, txid string, vout uint32) (*Outspend, error) {
	if !ValidTxID(txid) {
		return nil, fmt.Errorf("%q is not a transaction id", txid)
	}
	var os Outspend
	if err := c.getJSON(ctx, fmt.Sprintf("/tx/%s/outspend/%d", txid, vout), &os); err != nil {
		return nil, err
	}
	if os.Spent && !ValidTxID(os.TxID) {
		return nil, fmt.Errorf("the node says the output is spent by %q, which is not a "+
			"transaction id", os.TxID)
	}
	return &os, nil
}

// BlockHashAt returns the hash of the block at the given height.
//
// Esplora answers this as plain text, so it is read rather than decoded. The
// shape is checked because the value is about to be compared against a
// counterparty's answer and reported as proof of anything: a node that replies
// with an error page must not become a "chain mismatch".
func (c *Client) BlockHashAt(ctx context.Context, height int64) (string, error) {
	if height < 0 {
		return "", fmt.Errorf("block height %d is not a height", height)
	}
	body, err := c.get(ctx, "/block-height/"+strconv.FormatInt(height, 10))
	if err != nil {
		return "", err
	}
	hash := strings.TrimSpace(string(body))
	if !ValidTxID(hash) {
		return "", fmt.Errorf("the node answered %q for the hash of block %d, which is not a block hash",
			truncate(hash, 80), height)
	}
	return hash, nil
}

// truncate keeps an unexpected response from pasting a whole error page into a
// message somebody has to read.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// FeeRate returns a sat/vB fee estimate for the given confirmation target,
// falling back to a sane floor when the source has no estimate (common on
// testnet and signet, where the map is frequently empty).
func (c *Client) FeeRate(ctx context.Context, blocks int) (float64, error) {
	var est map[string]float64
	if err := c.getJSON(ctx, "/fee-estimates", &est); err != nil {
		return 0, err
	}
	if v, ok := est[strconv.Itoa(blocks)]; ok && v > 0 {
		return v, nil
	}
	// Pick the closest available target at or above the requested one.
	best, bestTarget := 0.0, 0
	for k, v := range est {
		t, err := strconv.Atoi(k)
		if err != nil || v <= 0 {
			continue
		}
		if t >= blocks && (bestTarget == 0 || t < bestTarget) {
			best, bestTarget = v, t
		}
	}
	if best > 0 {
		return best, nil
	}
	return 1.0, nil // minimum relay floor
}

// Broadcast publishes a signed raw transaction and returns its txid.
//
// This is the only write this service performs against Bitcoin, and the
// transaction it publishes was signed with an ephemeral swap key, never with a
// key belonging to the user's wallet.
func (c *Client) Broadcast(ctx context.Context, rawHex string) (string, error) {
	resp, err := httpx.Do(ctx, httpx.Request{
		Method:  "POST",
		URL:     c.BaseURL + "/tx",
		Headers: map[string]string{"Content-Type": "text/plain"},
		Body:    []byte(rawHex),
		Timeout: c.Timeout,
	})
	if err != nil {
		return "", fmt.Errorf("broadcast: %w", err)
	}
	if !resp.OK() {
		return "", fmt.Errorf("broadcast rejected (%d %s): %s", resp.Status, resp.StatusText, resp.Text())
	}
	return resp.Text(), nil
}

// Name identifies the backend in the UI, so the user can see which data source
// their swap is trusting.
func (c *Client) Name() string { return "esplora:" + c.BaseURL }

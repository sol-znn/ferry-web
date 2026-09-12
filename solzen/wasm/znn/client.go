// Package znn talks to a Zenon node: it reads the chain, and -- unlike ferry's
// Zenon client, which is deliberately read-only -- it also builds, signs and
// publishes account blocks.
//
// That difference is the point of this project. ferry cannot sign on Zenon
// because doing so would mean holding the user's wallet key, so its Zenon leg
// is a set of instructions for a separate CLI. Here the key this package signs
// with is generated per swap and holds nothing but what the user deliberately
// sent into that swap, so signing costs the user no more than ferry's Bitcoin
// leg already costs them -- and it removes the CLI.
//
// Everything cryptographic is go-zenon's own: the ABI encoder, the address and
// hash types, the hash preimage layout. Only the pieces that reach a database
// or a filesystem are reimplemented, and each says so where it is.
package znn

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/zenon-network/go-zenon/common/types"
	"github.com/zenon/solzen/wasm/httpx"
)

// Client is a Zenon node's HTTP JSON-RPC endpoint, port 35997 on a stock node.
//
// Not 35998: that is the WebSocket port every Zenon wallet uses, and it is a
// different endpoint on the same node. Pasting the URL from Syrius here does
// not work, and the error it produces looks like the node is down, so the UI
// says which port it wants before asking for one.
type Client struct {
	URL     string
	Timeout time.Duration
}

func New(url string) *Client {
	return &Client{URL: strings.TrimRight(url, "/"), Timeout: 30 * time.Second}
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// call makes one JSON-RPC request and decodes the result into out.
//
// A node error is returned with its message intact rather than flattened to
// "the call failed": the contract's refusals arrive this way, and "data non
// existent" -- which is how the node reports an HTLC that was unlocked or
// reclaimed -- is a normal, expected state that callers branch on.
func (c *Client) call(ctx context.Context, out any, method string, params ...any) error {
	if c.URL == "" {
		return fmt.Errorf("no Zenon node URL is set")
	}
	if params == nil {
		params = []any{}
	}
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("%s: encoding the request: %w", method, err)
	}

	resp, err := httpx.Do(ctx, httpx.Request{
		Method:  "POST",
		URL:     c.URL,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
		Timeout: c.Timeout,
	})
	if err != nil {
		return err
	}
	if !resp.OK() {
		return fmt.Errorf("%s: node returned %d %s: %s", method, resp.Status, resp.StatusText, truncate(resp.Text(), 200))
	}

	var r rpcResponse
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return fmt.Errorf("%s: the node's reply is not JSON-RPC: %s", method, truncate(resp.Text(), 200))
	}
	if r.Error != nil {
		return &NodeError{Method: method, Code: r.Error.Code, Message: r.Error.Message}
	}
	if out == nil {
		return nil
	}
	if len(r.Result) == 0 || string(r.Result) == "null" {
		return ErrNotFound
	}
	if err := json.Unmarshal(r.Result, out); err != nil {
		return fmt.Errorf("%s: could not decode the node's reply: %w", method, err)
	}
	return nil
}

// NodeError is an error the node itself reported.
type NodeError struct {
	Method  string
	Code    int
	Message string
}

func (e *NodeError) Error() string { return fmt.Sprintf("%s: %s", e.Method, e.Message) }

// IsDataNonExistent reports whether an error is the node saying an entry is not
// there. For an HTLC that is not a failure: it is how a successful unlock or
// reclaim looks afterwards, because both delete the entry.
func IsDataNonExistent(err error) bool {
	var ne *NodeError
	if !asNodeError(err, &ne) {
		return false
	}
	return strings.Contains(strings.ToLower(ne.Message), "data non existent")
}

func asNodeError(err error, out **NodeError) bool {
	for err != nil {
		if ne, ok := err.(*NodeError); ok {
			*out = ne
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// ErrNotFound is a null result: the node answered, and the answer is "no such
// thing". Distinct from a NodeError, which is the node refusing.
var ErrNotFound = fmt.Errorf("not found")

// Momentum is the chain tip, trimmed to what a swap needs.
//
// Timestamp is the one that matters: the htlc contract compares expiry against
// momentum time, not against the caller's clock, so every deadline this app
// computes is anchored here rather than to time.Now.
type Momentum struct {
	Hash            types.Hash `json:"hash"`
	Height          uint64     `json:"height"`
	Timestamp       int64      `json:"timestamp"`
	ChainIdentifier uint64     `json:"chainIdentifier"`
}

func (c *Client) FrontierMomentum(ctx context.Context) (*Momentum, error) {
	m := new(Momentum)
	if err := c.call(ctx, m, "ledger.getFrontierMomentum"); err != nil {
		return nil, err
	}
	return m, nil
}

// Token is a token's metadata. Decimals is why this is fetched at all: an
// agreed amount written as "10" is meaningless until it is known whether that
// is 10^8 base units or 10^6.
type Token struct {
	Name          string `json:"name"`
	Symbol        string `json:"symbol"`
	Domain        string `json:"domain"`
	Decimals      int    `json:"decimals"`
	TokenStandard string `json:"tokenStandard"`
}

// Token looks up a token's metadata.
//
// The two checks after the call are not decoration. Decimals decide what an
// agreed figure *means* -- "10" is 10^8 base units of ZNN and 10^6 of a token
// that says 6 -- so reading them off the wrong token silently rewrites the
// trade by a factor of a hundred. The node is asked for one ZTS and must answer
// about that ZTS; anything else is a node bug or a node lying, and either way
// the decimals in hand are not the ones that were asked for.
func (c *Client) Token(ctx context.Context, zts string) (*Token, error) {
	t := new(Token)
	if err := c.call(ctx, t, "embedded.token.getByZts", zts); err != nil {
		return nil, err
	}
	if !strings.EqualFold(t.TokenStandard, zts) {
		return nil, fmt.Errorf(
			"asked the node for token %s and it answered about %q; refusing to read decimals from it",
			zts, t.TokenStandard)
	}
	if t.Decimals < 0 || t.Decimals > MaxTokenDecimals {
		return nil, fmt.Errorf("token %s reports an implausible decimals value of %d", zts, t.Decimals)
	}
	return t, nil
}

// MaxTokenDecimals is the largest decimals value this app will believe. It is
// the same bound validateTerms applies to a pasted offer, so a token the node
// describes and a token an offer names are held to one rule.
const MaxTokenDecimals = 18

// AccountBlock is a block as the node reports it. Only the fields this app
// reads are declared; the node sends more.
type AccountBlock struct {
	Hash                 types.Hash       `json:"hash"`
	PreviousHash         types.Hash       `json:"previousHash"`
	Height               uint64           `json:"height"`
	BlockType            uint64           `json:"blockType"`
	Address              types.Address    `json:"address"`
	ToAddress            types.Address    `json:"toAddress"`
	Amount               string           `json:"amount"`
	TokenStandard        string           `json:"tokenStandard"`
	FromBlockHash        types.Hash       `json:"fromBlockHash"`
	Data                 []byte           `json:"data"`
	MomentumAcknowledged types.HashHeight `json:"momentumAcknowledged"`
	ConfirmationDetail   *struct {
		NumConfirmations  uint64 `json:"numConfirmations"`
		MomentumHeight    uint64 `json:"momentumHeight"`
		MomentumTimestamp int64  `json:"momentumTimestamp"`
	} `json:"confirmationDetail"`
	PairedAccountBlock *AccountBlock `json:"pairedAccountBlock"`
}

type accountBlockList struct {
	List  []*AccountBlock `json:"list"`
	Count int             `json:"count"`
	More  bool            `json:"more"`
}

// FrontierAccountBlock returns the account's newest block, or ErrNotFound for
// an address that has never produced one -- which is the normal state of a
// freshly generated swap address.
func (c *Client) FrontierAccountBlock(ctx context.Context, addr types.Address) (*AccountBlock, error) {
	b := new(AccountBlock)
	if err := c.call(ctx, b, "ledger.getFrontierAccountBlock", addr.String()); err != nil {
		return nil, err
	}
	return b, nil
}

func (c *Client) AccountBlockByHash(ctx context.Context, hash types.Hash) (*AccountBlock, error) {
	b := new(AccountBlock)
	if err := c.call(ctx, b, "ledger.getAccountBlockByHash", hash.String()); err != nil {
		return nil, err
	}
	return b, nil
}

func (c *Client) AccountBlocksByPage(ctx context.Context, addr types.Address, pageIndex, pageSize int) ([]*AccountBlock, error) {
	l := new(accountBlockList)
	if err := c.call(ctx, l, "ledger.getAccountBlocksByPage", addr.String(), pageIndex, pageSize); err != nil {
		if err == ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	return l.List, nil
}

// Unreceived returns blocks sent to this address that it has not received yet.
//
// Zenon is account-chain based: money sent to you is not spendable until your
// own chain has a receive block for it. A swap address that has just been paid
// therefore has a balance of zero and a pending block, which is why funding
// detection looks here and not only at the balance.
func (c *Client) Unreceived(ctx context.Context, addr types.Address, pageSize int) ([]*AccountBlock, error) {
	l := new(accountBlockList)
	if err := c.call(ctx, l, "ledger.getUnreceivedBlocksByAddress", addr.String(), 0, pageSize); err != nil {
		if err == ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	return l.List, nil
}

// Balance is one token's balance on one account.
type Balance struct {
	TokenStandard string
	Symbol        string
	Decimals      int
	Amount        *big.Int
}

type accountInfo struct {
	Address        types.Address `json:"address"`
	AccountHeight  uint64        `json:"accountHeight"`
	BalanceInfoMap map[string]struct {
		TokenInfo *Token `json:"token"`
		Balance   string `json:"balance"`
	} `json:"balanceInfoMap"`
}

// Balances returns the account's confirmed, received balances.
func (c *Client) Balances(ctx context.Context, addr types.Address) (map[string]Balance, error) {
	info := new(accountInfo)
	if err := c.call(ctx, info, "ledger.getAccountInfoByAddress", addr.String()); err != nil {
		if err == ErrNotFound {
			return map[string]Balance{}, nil
		}
		return nil, err
	}
	out := make(map[string]Balance, len(info.BalanceInfoMap))
	for zts, b := range info.BalanceInfoMap {
		// Amounts arrive as JSON strings, not numbers -- a uint64 would not
		// hold a supply this big and a float would lose base units.
		amount, ok := new(big.Int).SetString(strings.Trim(b.Balance, `"`), 10)
		if !ok {
			amount = new(big.Int)
		}
		bal := Balance{TokenStandard: zts, Amount: amount}
		if b.TokenInfo != nil {
			bal.Symbol = b.TokenInfo.Symbol
			bal.Decimals = b.TokenInfo.Decimals
		}
		out[zts] = bal
	}
	return out, nil
}

// Htlc reads one entry. A "data non existent" error means it was unlocked or
// reclaimed, which callers must treat as a state rather than a failure --
// IsDataNonExistent is how they tell.
func (c *Client) Htlc(ctx context.Context, id types.Hash) (*HtlcInfo, error) {
	raw := new(HtlcInfo)
	if err := c.call(ctx, raw, "embedded.htlc.getById", id.String()); err != nil {
		return nil, err
	}
	// The node chose what to answer with. An entry that is not the one asked
	// for is a node bug or a node lying, and either way every check the caller
	// is about to make would be made against the wrong object.
	if raw.Id != id {
		return nil, fmt.Errorf("asked the node for HTLC %s and it answered about %s", id, raw.Id)
	}
	amount, ok := new(big.Int).SetString(strings.Trim(raw.AmountRaw, `"`), 10)
	if !ok {
		return nil, fmt.Errorf("the node reported an amount that will not parse: %q", raw.AmountRaw)
	}
	raw.Amount = amount
	hashLock, err := DecodeHashLock(raw.HashLockRaw)
	if err != nil {
		return nil, err
	}
	raw.HashLock = hashLock
	return raw, nil
}

// ProxyUnlockAllowed reports whether anyone may unlock on behalf of addr.
//
// The htlc contract allows it by default, and that default is what makes this
// project's Zenon leg work without a wallet that understands HTLCs: the
// counterparty's payout address never has to sign anything, because the swap
// key can push the unlock and the contract still pays the address recorded in
// the entry. An address that has explicitly denied it cannot be the receiving
// side of a swap driven this way, so this is checked before the offer is
// accepted rather than at settlement time.
func (c *Client) ProxyUnlockAllowed(ctx context.Context, addr types.Address) (bool, error) {
	var allowed bool
	if err := c.call(ctx, &allowed, "embedded.htlc.getProxyUnlockStatus", addr.String()); err != nil {
		return false, err
	}
	return allowed, nil
}

// RequiredPoW is the node's answer to "what will this block cost".
type RequiredPoW struct {
	AvailablePlasma    uint64 `json:"availablePlasma"`
	BasePlasma         uint64 `json:"basePlasma"`
	RequiredDifficulty uint64 `json:"requiredDifficulty"`
}

// RequiredPoWFor asks what plasma a block needs and how much work stands in for
// the plasma the account does not have.
//
// RequiredDifficulty of zero means the account has enough fused plasma and the
// block can be published immediately. Anything else is a proof-of-work the
// browser has to compute, which is slow enough that the UI treats "fuse plasma
// to this address first" as the recommended path and this as the fallback.
func (c *Client) RequiredPoWFor(ctx context.Context, from types.Address, blockType uint64, to *types.Address, data []byte) (*RequiredPoW, error) {
	param := map[string]any{
		"address":   from.String(),
		"blockType": blockType,
		"data":      base64.StdEncoding.EncodeToString(data),
	}
	if to != nil {
		param["toAddress"] = to.String()
	}
	r := new(RequiredPoW)
	if err := c.call(ctx, r, "embedded.plasma.getRequiredPoWForAccountBlock", param); err != nil {
		return nil, err
	}
	return r, nil
}

// Publish submits a signed block.
func (c *Client) Publish(ctx context.Context, b *Block) error {
	return c.call(ctx, nil, "ledger.publishRawTransaction", b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ParseHashHex accepts a hash with or without a 0x prefix, which is how ids
// arrive from a counterparty who copied them out of any of three tools.
func ParseHashHex(s string) (types.Hash, error) {
	raw, err := hex.DecodeString(trimHex(strings.TrimSpace(s)))
	if err != nil {
		return types.Hash{}, fmt.Errorf("not hex: %w", err)
	}
	if len(raw) != types.HashSize {
		return types.Hash{}, fmt.Errorf("a hash is %d bytes, got %d", types.HashSize, len(raw))
	}
	var h types.Hash
	if err := h.SetBytes(raw); err != nil {
		return types.Hash{}, err
	}
	return h, nil
}

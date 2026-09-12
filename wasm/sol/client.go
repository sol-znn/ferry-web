// Package sol reads Solana and builds the instructions for the solzen-htlc
// program. It does not sign or send: a transaction is assembled and signed on
// the JavaScript side, by the user's wallet.
//
// The split is deliberate rather than incidental. Everything that decides
// whether a swap is safe -- what an escrow contains, whether it is the account
// this swap id implies, whether it pays the right party at the right time, and
// what preimage a redeem published -- happens here, in the same module as the
// Zenon checks and the swap's own rules. Everything that is wallet plumbing
// happens where the wallet ecosystem lives.
package sol

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/zenon/ferry-web-v2/wasm/httpx"
)

// LamportsPerSol is Solana's base-unit scale.
const LamportsPerSol uint64 = 1_000_000_000

// Client is a Solana JSON-RPC endpoint.
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
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// NodeError is an error the RPC node reported.
type NodeError struct {
	Method  string
	Code    int
	Message string
}

func (e *NodeError) Error() string { return fmt.Sprintf("%s: %s", e.Method, e.Message) }

// ErrNoAccount is a successful query whose answer is "nothing is there". For a
// swap that is a state, not a failure: an escrow reads this way both before it
// is funded and after it settles, and telling those apart is the caller's job.
var ErrNoAccount = fmt.Errorf("no account at that address")

func (c *Client) call(ctx context.Context, out any, method string, params ...any) error {
	if c.URL == "" {
		return fmt.Errorf("no Solana RPC URL is set")
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
	return json.Unmarshal(r.Result, out)
}

// Health returns the cluster version, and is how the UI tells "you named a node
// that answers" from "you named a node".
func (c *Client) Version(ctx context.Context) (string, error) {
	var v struct {
		SolanaCore string `json:"solana-core"`
	}
	if err := c.call(ctx, &v, "getVersion"); err != nil {
		return "", err
	}
	return v.SolanaCore, nil
}

// Now returns the cluster's own clock, which is what the program's timelock is
// compared against.
//
// Not the browser's clock. Solana's on-chain unix timestamp is derived from
// stake-weighted validator time and can sit some way from wall time -- on a
// freshly started local validator it starts at genesis and catches up. A swap
// whose deadlines were computed against the wrong one is a swap whose refund
// arrives at a moment nobody predicted.
func (c *Client) Now(ctx context.Context) (int64, error) {
	var slot uint64
	if err := c.call(ctx, &slot, "getSlot", map[string]any{"commitment": "confirmed"}); err != nil {
		return 0, err
	}
	var t *int64
	if err := c.call(ctx, &t, "getBlockTime", slot); err != nil {
		return 0, err
	}
	if t == nil {
		return 0, fmt.Errorf("the node has no block time for slot %d yet", slot)
	}
	return *t, nil
}

func (c *Client) Balance(ctx context.Context, addr Pubkey) (uint64, error) {
	var r struct {
		Value uint64 `json:"value"`
	}
	if err := c.call(ctx, &r, "getBalance", addr.String(), map[string]any{"commitment": "confirmed"}); err != nil {
		return 0, err
	}
	return r.Value, nil
}

type accountValue struct {
	Data       []string `json:"data"`
	Lamports   uint64   `json:"lamports"`
	Owner      string   `json:"owner"`
	Executable bool     `json:"executable"`
}

// Account reads an account's raw data.
func (c *Client) Account(ctx context.Context, addr Pubkey) (*accountValue, error) {
	var r struct {
		Value *accountValue `json:"value"`
	}
	err := c.call(ctx, &r, "getAccountInfo", addr.String(), map[string]any{
		"encoding":   "base64",
		"commitment": "confirmed",
	})
	if err != nil {
		return nil, err
	}
	if r.Value == nil {
		return nil, ErrNoAccount
	}
	return r.Value, nil
}

// Escrow reads and decodes the escrow for a swap id.
//
// Ownership is checked here rather than left to the caller: an account at the
// right address that some other program owns is not an escrow, however much its
// bytes may happen to parse.
func (c *Client) Escrow(ctx context.Context, programID Pubkey, swapID [32]byte) (*Escrow, Pubkey, error) {
	addr, _, err := EscrowAddress(programID, swapID)
	if err != nil {
		return nil, Pubkey{}, err
	}
	acc, err := c.Account(ctx, addr)
	if err != nil {
		return nil, addr, err
	}
	if acc.Owner != programID.String() {
		return nil, addr, fmt.Errorf("the account at %s is owned by %s, not by the swap program", addr, acc.Owner)
	}
	if len(acc.Data) < 1 {
		return nil, addr, fmt.Errorf("the node returned an account with no data")
	}
	raw, err := base64.StdEncoding.DecodeString(acc.Data[0])
	if err != nil {
		return nil, addr, fmt.Errorf("the node returned data that is not base64: %w", err)
	}
	e, err := DecodeEscrow(raw, acc.Lamports)
	if err != nil {
		return nil, addr, err
	}
	// The stored id must be the id we asked about. Without this, a caller
	// pointed at the wrong swap would read a real escrow and believe it was
	// theirs.
	if e.SwapID != swapID {
		return nil, addr, fmt.Errorf("the escrow at %s belongs to a different swap", addr)
	}
	return e, addr, nil
}

// MinimumRent is what an escrow account costs to open. The initiator pays it
// and gets it back on either exit; it is shown so that "the swap costs 1 SOL"
// is not quietly untrue.
func (c *Client) MinimumRent(ctx context.Context) (uint64, error) {
	var lamports uint64
	if err := c.call(ctx, &lamports, "getMinimumBalanceForRentExemption", EscrowLen); err != nil {
		return 0, err
	}
	return lamports, nil
}

// GenesisHash identifies the cluster. It is the only thing that reliably tells
// two Solana networks apart: an RPC URL is a name somebody typed, and a program
// id deployed from one keypair exists at the same address on every cluster it
// was deployed to.
func (c *Client) GenesisHash(ctx context.Context) (string, error) {
	var hash string
	if err := c.call(ctx, &hash, "getGenesisHash"); err != nil {
		return "", err
	}
	return hash, nil
}

// The loaders a program account can be owned by. A program deployed by
// `solana program deploy` is owned by the upgradeable loader and keeps an
// authority that can replace its code; `--final`, or a later
// `set-upgrade-authority --final`, removes that authority. The other two are
// the older loaders, under which code cannot be changed at all.
const (
	loaderUpgradeable = "BPFLoaderUpgradeab1e11111111111111111111111"
	loaderV2          = "BPFLoader2111111111111111111111111111111111"
	loaderV1          = "BPFLoader1111111111111111111111111111111111"
)

// ProgramInfo is what can be established about a deployed program without
// reading its bytecode.
type ProgramInfo struct {
	Address string `json:"address"`
	Owner   string `json:"owner"`
	// Executable is false for an account that is not a program at all -- a
	// keypair address somebody pasted, or a deploy that never happened.
	Executable bool `json:"executable"`
	// Upgradeable is true when someone can still replace this program's code.
	Upgradeable bool `json:"upgradeable"`
	// Authority is who that someone is, empty when nobody can.
	Authority string `json:"authority,omitempty"`
	// Problem is why the two fields above could not be established, when they
	// could not. They are then meaningless rather than false, in the same way
	// SwapAddressStatus.PlasmaKnown works on the Zenon side: "nobody can change
	// this program" is not a thing to say because a call failed.
	Problem string `json:"problem,omitempty"`
}

// Program reports what is at a program id.
//
// Executability is checked rather than assumed from the account existing, and
// upgradeability is read rather than hoped for. The second is the one that
// matters and has no counterpart on a chain with scripts: a Bitcoin P2SH
// address IS its contract and cannot be edited afterwards, whereas an
// upgradeable Solana program can have its code -- and therefore the terms of
// every escrow already funded under it -- replaced by whoever holds its
// authority. That is a property of the deployment, not of this source, so
// reading this source tells a user nothing about it.
func (c *Client) Program(ctx context.Context, programID Pubkey) (*ProgramInfo, error) {
	acc, err := c.Account(ctx, programID)
	if err != nil {
		return nil, err
	}
	info := &ProgramInfo{
		Address:    programID.String(),
		Owner:      acc.Owner,
		Executable: acc.Executable,
	}
	if !acc.Executable {
		info.Problem = "this account is not executable, so it is not a program"
		return info, nil
	}
	switch acc.Owner {
	case loaderV1, loaderV2:
		// These loaders have no upgrade path at all.
		return info, nil
	case loaderUpgradeable:
	default:
		info.Problem = fmt.Sprintf("owned by %s, which is not a known BPF loader", acc.Owner)
		return info, nil
	}

	// Under the upgradeable loader the program account is a 36-byte stub:
	// a 4-byte little-endian enum (2 = Program) then the programdata address.
	raw, derr := decodeAccountData(acc)
	if derr != nil {
		info.Problem = derr.Error()
		return info, nil
	}
	if len(raw) < 36 || binary.LittleEndian.Uint32(raw[0:4]) != 2 {
		info.Problem = "the program account is not the shape the upgradeable loader writes"
		return info, nil
	}
	var programData Pubkey
	copy(programData[:], raw[4:36])

	pd, perr := c.Account(ctx, programData)
	if perr != nil {
		info.Problem = fmt.Sprintf("could not read the program's data account %s: %v", programData, perr)
		return info, nil
	}
	pdRaw, derr := decodeAccountData(pd)
	if derr != nil {
		info.Problem = derr.Error()
		return info, nil
	}
	// ProgramData: a 4-byte enum (3), an 8-byte deployed slot, then an Option
	// whose first byte is 0 for None -- which is what `--final` leaves behind
	// and the only state in which nobody can change the code.
	if len(pdRaw) < 13 || binary.LittleEndian.Uint32(pdRaw[0:4]) != 3 {
		info.Problem = "the program data account is not the shape the upgradeable loader writes"
		return info, nil
	}
	if pdRaw[12] == 0 {
		return info, nil
	}
	if len(pdRaw) < 45 {
		info.Problem = "the program data account claims an upgrade authority and does not carry one"
		return info, nil
	}
	var authority Pubkey
	copy(authority[:], pdRaw[13:45])
	info.Upgradeable = true
	info.Authority = authority.String()
	return info, nil
}

func decodeAccountData(acc *accountValue) ([]byte, error) {
	if len(acc.Data) < 1 {
		return nil, fmt.Errorf("the node returned an account with no data")
	}
	raw, err := base64.StdEncoding.DecodeString(acc.Data[0])
	if err != nil {
		return nil, fmt.Errorf("the node returned data that is not base64: %w", err)
	}
	return raw, nil
}

type signatureInfo struct {
	Signature string  `json:"signature"`
	Slot      uint64  `json:"slot"`
	BlockTime *int64  `json:"blockTime"`
	Err       any     `json:"err"`
	Memo      *string `json:"memo"`
}

// Signatures lists transactions that touched an address, newest first.
func (c *Client) Signatures(ctx context.Context, addr Pubkey, limit int) ([]signatureInfo, error) {
	var out []signatureInfo
	err := c.call(ctx, &out, "getSignaturesForAddress", addr.String(), map[string]any{
		"limit":      limit,
		"commitment": "confirmed",
	})
	return out, err
}

// SigState is what became of a transaction this page submitted.
type SigState struct {
	// Known is false when the cluster has never heard of the signature. That is
	// two things at once -- not landed yet, or dropped and never will -- and
	// only time tells them apart, so the caller is told which question it is.
	Known bool
	// Failed is true for a transaction that landed and reverted.
	Failed bool
	Err    string
}

// SignatureStatus asks what happened to one signature.
//
// searchTransactionHistory, because the default only looks at recent status
// caches and a swap step can be older than those by the time anybody asks.
func (c *Client) SignatureStatus(ctx context.Context, signature string) (*SigState, error) {
	var r struct {
		Value []*struct {
			Err                any     `json:"err"`
			ConfirmationStatus *string `json:"confirmationStatus"`
		} `json:"value"`
	}
	err := c.call(ctx, &r, "getSignatureStatuses", []string{signature}, map[string]any{
		"searchTransactionHistory": true,
	})
	if err != nil {
		return nil, err
	}
	if len(r.Value) == 0 || r.Value[0] == nil {
		return &SigState{}, nil
	}
	out := &SigState{Known: true}
	if e := r.Value[0].Err; e != nil {
		out.Failed = true
		out.Err = fmt.Sprintf("%v", e)
	}
	return out, nil
}

// Outcome is what became of an escrow that no longer exists.
type Outcome struct {
	Redeemed  bool
	Preimage  []byte
	RedeemSig string
	Refunded  bool
	RefundSig string
}

// Settled reports whether the scan explained the escrow's disappearance.
func (o *Outcome) Settled() bool { return o.Redeemed || o.Refunded }

// FindEscrowOutcome looks for the transaction that spent an escrow, and pulls
// the preimage out of it if it was a redeem.
//
// This is the Solana half of the mechanism that makes the swap atomic. The
// escrow account is gone by the time anyone needs to ask -- both exits close it
// -- so the answer is recovered from the history of the address, which the
// chain keeps.
//
// A zero Outcome and no error means nothing matching was found. A swap that has
// not settled looks exactly like one whose history has not caught up, and the
// caller polls either way.
//
// hashlock is what makes a redeem *this* swap's redeem rather than a redeem
// shaped like one. The scan cannot stop at the first candidate: everything it
// walks is a transaction somebody else chose to publish, and a decoy is one
// transaction. See scanning below for why that is not paranoia.
func (c *Client) FindEscrowOutcome(ctx context.Context, programID Pubkey, escrow Pubkey, hashlock [32]byte, limit int) (*Outcome, error) {
	sigs, err := c.Signatures(ctx, escrow, limit)
	if err != nil {
		return nil, err
	}
	out := &Outcome{}
	for _, s := range sigs {
		if s.Err != nil {
			// A failed transaction proves nothing: a wrong preimage produces
			// one of these, and reading it as the answer would hand the caller
			// a secret that opens nothing.
			continue
		}
		tag, payload, err := c.escrowInstructionIn(ctx, programID, escrow, s.Signature)
		if err != nil {
			return nil, err
		}
		switch tag {
		case tagRedeem:
			// A redeem that does not open this hashlock did not spend this
			// escrow, whatever it says: the program checks the preimage before
			// it pays, so a *successful* redeem of this escrow always carries a
			// preimage that hashes to this hashlock. Keep scanning.
			if sha256.Sum256(payload) != hashlock {
				continue
			}
			out.Redeemed, out.Preimage, out.RedeemSig = true, payload, s.Signature
			return out, nil
		case tagRefund:
			out.Refunded, out.RefundSig = true, s.Signature
			return out, nil
		}
	}
	return out, nil
}

// txInstruction is one instruction as a json-encoded transaction reports it:
// indices into the transaction's account list, and base58 data.
type txInstruction struct {
	ProgramIDIndex int    `json:"programIdIndex"`
	Accounts       []int  `json:"accounts"`
	Data           string `json:"data"`
}

// txMessage is as much of a transaction as this needs: the account list, the
// instructions indexing into it, and the ones invoked by CPI, which the node
// reports separately in meta.
type txMessage struct {
	Transaction struct {
		Message struct {
			AccountKeys  []string        `json:"accountKeys"`
			Instructions []txInstruction `json:"instructions"`
		} `json:"message"`
	} `json:"transaction"`
	Meta *struct {
		Err               any `json:"err"`
		InnerInstructions []struct {
			Index        int             `json:"index"`
			Instructions []txInstruction `json:"instructions"`
		} `json:"innerInstructions"`
	} `json:"meta"`
}

// escrowInstructionIn finds the instruction that spent *this* escrow inside a
// transaction and reports its tag, plus the preimage when it is a redeem. It
// returns 0xff for a transaction that does not spend this escrow.
//
// Both halves of that sentence are load-bearing, and the second one is the
// reason this takes an escrow at all.
//
// getSignaturesForAddress indexes a transaction under every account key it
// names, including read-only ones no instruction touches. So "a transaction
// that mentions this escrow" is a set anybody can add to for the price of one
// fee -- and matching on the program id alone would let a transaction carrying
// a redeem of somebody *else's* escrow answer for this one. Published straight
// after a real redeem it is also the newest, so it wins the scan permanently:
// the escrow is closed and nothing will ever touch it again to push the decoy
// down the list.
//
// What that buys an attacker is the preimage. Both branches close the account,
// so the settling transaction is the only place the counterparty can read the
// secret out; a decoy that answers "refunded" in its place leaves them unable
// to claim the other leg, which then expires back to the attacker. Hence the
// account check here and the hashlock check in the caller: one says this
// instruction spent this escrow, the other says this redeem opened this swap.
func (c *Client) escrowInstructionIn(ctx context.Context, programID, escrow Pubkey, signature string) (byte, []byte, error) {
	var tx *txMessage
	err := c.call(ctx, &tx, "getTransaction", signature, map[string]any{
		"encoding":                       "json",
		"commitment":                     "confirmed",
		"maxSupportedTransactionVersion": 0,
	})
	if err != nil {
		return 0xff, nil, err
	}
	if tx == nil || (tx.Meta != nil && tx.Meta.Err != nil) {
		return 0xff, nil, nil
	}
	keys := tx.Transaction.Message.AccountKeys
	want := programID.String()
	wantEscrow := escrow.String()

	// Top-level instructions, then the ones reached by CPI. A redeem invoked
	// from another program is a redeem: the escrow is closed and the preimage
	// is on chain either way, so a scan that only reads the outer list would
	// miss a real settlement rather than merely a contrived one.
	type instr struct {
		programIndex int
		accounts     []int
		data         string
	}
	var all []instr
	for _, ix := range tx.Transaction.Message.Instructions {
		all = append(all, instr{ix.ProgramIDIndex, ix.Accounts, ix.Data})
	}
	if tx.Meta != nil {
		for _, inner := range tx.Meta.InnerInstructions {
			for _, ix := range inner.Instructions {
				all = append(all, instr{ix.ProgramIDIndex, ix.Accounts, ix.Data})
			}
		}
	}

	for _, ix := range all {
		if ix.programIndex < 0 || ix.programIndex >= len(keys) || keys[ix.programIndex] != want {
			continue
		}
		// The escrow is account 0 of every spending branch -- redeem and refund
		// both take it first, and create does not spend anything. An
		// instruction whose first account is some other escrow is some other
		// swap's business.
		if len(ix.accounts) == 0 {
			continue
		}
		if a := ix.accounts[0]; a < 0 || a >= len(keys) || keys[a] != wantEscrow {
			continue
		}
		// Instruction data in a json-encoded transaction is base58, not
		// base64 -- the one place in this API where the two differ.
		raw, err := DecodeBase58(ix.data)
		if err != nil || len(raw) == 0 {
			continue
		}
		if preimage, ok := DecodeRedeemPreimage(raw); ok {
			return tagRedeem, preimage, nil
		}
		if raw[0] == tagRefund {
			return tagRefund, nil, nil
		}
	}
	return 0xff, nil, nil
}

func encodeBase64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

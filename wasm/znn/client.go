// Package znn is a read-only JSON-RPC client for a Zenon node.
//
// Deliberately read-only: creating and unlocking a Zenon HTLC are signed
// operations needing the user's Zenon keys, and those stay in the wallet. ferry's
// job on this leg is to verify what was actually published.
package znn

// net/http is deliberately absent: see the note in chain/esplora.go and the
// package comment on httpx.
import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/zenon/ferry-web/wasm/httpx"
)

// HashTypeSHA3 and HashTypeSHA256 mirror the constants in go-zenon's
// vm/embedded/definition/htlc.go. A cross-chain swap with Bitcoin must use
// SHA256, because Bitcoin script can only hash the preimage with OP_SHA256.
const (
	HashTypeSHA3   = 0
	HashTypeSHA256 = 1
)

// ZnnTokenStandard is ZNN's own ZTS. It is the default a swap uses when the user
// names no token, so that the agreed amount always has a token to be an amount
// OF -- with no token standard there are no decimals to convert the figure with
// and nothing to compare the entry's own token against.
const ZnnTokenStandard = "zts1znnxxxxxxxxxxxxx9z4ulx"

// Client is a Zenon node JSON-RPC client.
type Client struct {
	URL     string
	Timeout time.Duration
}

// New returns a client for a Zenon node's JSON-RPC endpoint. Both transports a
// go-zenon node serves are accepted, and the URL scheme picks between them:
//
//	https://node.example:35997   HTTP JSON-RPC
//	wss://node.example:35998     the same JSON-RPC over a WebSocket
//
// Which works against a given node is not a matter of taste. The HTTP endpoint
// is a cross-origin request, unreachable from a page unless that node answers
// with permissive CORS headers, and most public nodes do not. A WebSocket has no
// preflight to fail, so wss:// is the transport that works against
// infrastructure this app's users do not run.
//
// Either way the endpoint must match the page's own scheme: mixed content is
// blocked before the connection is made, which is why the local development
// flow serves the app over plain http.
func New(url string) *Client {
	return &Client{
		URL:     strings.TrimRight(url, "/"),
		Timeout: 20 * time.Second,
	}
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// IsWebSocket reports whether this client will use the WebSocket transport.
func (c *Client) IsWebSocket() bool {
	lower := strings.ToLower(c.URL)
	return strings.HasPrefix(lower, "ws://") || strings.HasPrefix(lower, "wss://")
}

// Transport names the transport in use, for the settings dialog.
func (c *Client) Transport() string {
	if c.IsWebSocket() {
		return "websocket"
	}
	return "http"
}

// frameID reads the id back off a JSON-RPC response. A WebSocket is one stream
// carrying every call, so the id is the only thing that says which reply belongs
// to which request -- decoded on its own, before the body is parsed, because a
// frame whose id cannot be read cannot be routed.
func frameID(frame []byte) (int, bool) {
	var envelope struct {
		ID *int `json:"id"`
	}
	if err := json.Unmarshal(frame, &envelope); err != nil || envelope.ID == nil {
		return 0, false
	}
	return *envelope.ID, true
}

// rpc issues one call over whichever transport the URL names and returns the
// raw response frame.
func (c *Client) rpc(ctx context.Context, method string, params []any) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	if c.IsWebSocket() {
		// The id is chosen by the connection, not here: it is what multiplexes
		// concurrent calls onto one socket, so it has to be unique per socket
		// rather than per call site.
		return wsCall(ctx, c.URL, func(id int) ([]byte, error) {
			return json.Marshal(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
		})
	}

	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	resp, err := httpx.Do(ctx, httpx.Request{
		Method:  "POST",
		URL:     c.URL,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
		Timeout: c.Timeout,
	})
	if err != nil {
		return nil, err
	}
	if !resp.OK() {
		return nil, fmt.Errorf("%d %s: %s", resp.Status, resp.StatusText, resp.Text())
	}
	return resp.Body, nil
}

func (c *Client) call(ctx context.Context, method string, params []any, out any) error {
	frame, err := c.rpc(ctx, method, params)
	if err != nil {
		return fmt.Errorf("zenon rpc %s: %w", method, err)
	}
	if len(frame) == 0 {
		return fmt.Errorf("zenon rpc %s: the connection closed without answering", method)
	}
	var rr rpcResponse
	if err := json.Unmarshal(frame, &rr); err != nil {
		return fmt.Errorf("zenon rpc %s: bad response: %w", method, err)
	}
	if rr.Error != nil {
		return fmt.Errorf("zenon rpc %s: %s (code %d)", method, rr.Error.Message, rr.Error.Code)
	}
	if out == nil {
		return nil
	}
	// A null result is not an empty struct, and unmarshalling it as one is how a
	// missing object turns into zero values that later read as data.
	// embedded.token.getByZts is the case that matters: go-zenon answers an
	// unknown ZTS with `"result": null`, and decoding that into a Token leaves
	// decimals at 0 -- silently reducing an agreed "10 ZNN" to ten base units.
	if len(rr.Result) == 0 || string(rr.Result) == "null" {
		// Wrapped rather than formatted, so a caller that has something useful
		// to say about a missing object -- ledger.go turns a missing HTLC into
		// an explanation of what became of it -- can tell "not there" apart
		// from "the call failed".
		return fmt.Errorf("zenon rpc %s: %w (the id or token standard does not exist on this node)",
			method, ErrNoResult)
	}
	return json.Unmarshal(rr.Result, out)
}

// HtlcInfo mirrors the JSON shape of go-zenon's HtlcInfoMarshal.
type HtlcInfo struct {
	ID             string `json:"id"`
	TimeLocked     string `json:"timeLocked"`
	HashLocked     string `json:"hashLocked"`
	TokenStandard  string `json:"tokenStandard"`
	Amount         string `json:"amount"`
	ExpirationTime int64  `json:"expirationTime"`
	HashType       int    `json:"hashType"`
	KeyMaxSize     int    `json:"keyMaxSize"`
	// HashLock is a []byte on the node, so it arrives base64-encoded.
	HashLock string `json:"hashLock"`
}

// MarshalJSON adds a hex rendering of the hashlock alongside the node's base64
// form. Comparing the Zenon hashlock against the Bitcoin secret hash by eye is
// something a user will actually want to do, and base64 makes that impossible.
func (h *HtlcInfo) MarshalJSON() ([]byte, error) {
	type alias HtlcInfo
	return json.Marshal(struct {
		*alias
		HashLockHex string `json:"hashLockHex"`
		ExpiresAt   string `json:"expiresAt"`
	}{
		alias:       (*alias)(h),
		HashLockHex: h.HashLockHex(),
		ExpiresAt:   time.Unix(h.ExpirationTime, 0).UTC().Format(time.RFC3339),
	})
}

// HashLockSize is the digest length both hash types produce, mirroring
// go-zenon's HashTypeDigestSizes: SHA3-256 and SHA-256 are both 32 bytes.
const HashLockSize = 32

// HashLockBytes decodes the hashlock the node returns.
//
// go-zenon marshals hashLock as []byte, so it arrives base64-encoded; some
// tooling hands back hex instead, and the two cannot be told apart by trying one
// and falling back on error -- a 64-character hex string is ALSO valid base64,
// decoding without complaint into 48 meaningless bytes. So both are decoded and
// the one that yields a digest-sized result wins.
func (h *HtlcInfo) HashLockBytes() ([]byte, error) {
	b, berr := base64.StdEncoding.DecodeString(h.HashLock)
	if berr == nil && len(b) == HashLockSize {
		return b, nil
	}
	if hb, herr := hex.DecodeString(h.HashLock); herr == nil && len(hb) == HashLockSize {
		return hb, nil
	}
	if berr == nil {
		// Decodable, but not a length any hash type in this system produces.
		// Return it rather than an error so Verify reports the mismatch against
		// the expected hash, which is the more useful message.
		return b, nil
	}
	return nil, fmt.Errorf("hashlock is neither base64 nor hex: %w", berr)
}

// HashLockHex renders the hashlock as hex for comparison with the Bitcoin side.
func (h *HtlcInfo) HashLockHex() string {
	b, err := h.HashLockBytes()
	if err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// GetHtlcByID fetches one HTLC entry by its id.
func (c *Client) GetHtlcByID(ctx context.Context, id string) (*HtlcInfo, error) {
	var info HtlcInfo
	if err := c.call(ctx, "embedded.htlc.getById", []any{id}, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// Momentum is the subset of a Zenon momentum this service needs.
type Momentum struct {
	Height    uint64 `json:"height"`
	Timestamp int64  `json:"timestamp"`
	Hash      string `json:"hash"`
	// ChainIdentifier is what a signed account block commits to, so a block
	// built for one chain cannot be replayed on another. It is read from the
	// tip rather than configured, because the node this browser is pointed at
	// is the only authority on which chain it is serving.
	ChainIdentifier uint64 `json:"chainIdentifier"`
}

// FrontierMomentum returns the chain tip, whose timestamp is the clock the
// HTLC contract compares expirationTime against.
func (c *Client) FrontierMomentum(ctx context.Context) (*Momentum, error) {
	var m Momentum
	if err := c.call(ctx, "ledger.getFrontierMomentum", []any{}, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// MomentumAt returns the momentum at a height, so two parties can compare the
// hash of one they should both already have. Zenon has no chain id in its RPC,
// and matching heights prove nothing -- two devnets started an hour apart look
// identical by that measure. A momentum hash at an agreed height is what
// actually differs, and it costs one call.
func (c *Client) MomentumAt(ctx context.Context, height uint64) (*Momentum, error) {
	var page struct {
		List []Momentum `json:"list"`
	}
	if err := c.call(ctx, "ledger.getMomentumsByHeight", []any{height, 1}, &page); err != nil {
		return nil, err
	}
	if len(page.List) == 0 {
		return nil, fmt.Errorf("the node has no momentum at height %d", height)
	}
	got := page.List[0]
	// The node chooses what to answer with; a page starting somewhere else
	// would silently compare the wrong momentums.
	if got.Height != height {
		return nil, fmt.Errorf("asked for momentum %d and the node answered with %d",
			height, got.Height)
	}
	return &got, nil
}

// Token is the subset of token metadata needed to render an amount.
type Token struct {
	Name          string `json:"name"`
	Symbol        string `json:"symbol"`
	TokenStandard string `json:"tokenStandard"`
	Decimals      int    `json:"decimals"`
}

// GetToken looks up token metadata so amounts can be compared in base units.
// The echo check is not decoration: decimals from the wrong token turn an agreed
// "10 ZNN" into ten base units, and the node answers an unknown ZTS with a null
// result rather than an error.
func (c *Client) GetToken(ctx context.Context, zts string) (*Token, error) {
	var t Token
	if err := c.call(ctx, "embedded.token.getByZts", []any{zts}, &t); err != nil {
		return nil, err
	}
	if !strings.EqualFold(t.TokenStandard, zts) {
		return nil, fmt.Errorf("asked the node for token %s and it answered with %q; "+
			"refusing to read decimals from it", zts, t.TokenStandard)
	}
	if t.Decimals < 0 || t.Decimals > 30 {
		return nil, fmt.Errorf("token %s reports an implausible decimals value of %d", zts, t.Decimals)
	}
	return &t, nil
}

// VerifyParams describes what the caller expects an HTLC to say.
//
// The fields exist in pairs on purpose: which of MinExpiration and MaxExpiration
// applies depends on whether the Zenon leg is the initiator's, and getting that
// backwards is the difference between a safe swap and one where a counterparty
// can take both legs. See Manager.VerifyZenon.
type VerifyParams struct {
	// SecretHashHex is the hash committed on the Bitcoin side. Both legs must
	// commit to the same hash or the swap is not atomic.
	SecretHashHex string
	// ExpectID, when set, is the id the caller asked the node for. An answer
	// describing a different entry is a node bug or a node lying, and either
	// way the object in hand is not the one being verified.
	ExpectID string
	// ExpectTokenStandard is the ZTS that was agreed. Without it "the amount
	// matches" means only that some quantity of SOMETHING is locked: a
	// counterparty can issue a worthless token, lock the agreed number of units
	// of it, and satisfy every other check in this function.
	ExpectTokenStandard string
	// ExpectRecipient is the address that must be able to unlock (hashLocked).
	// For a counterparty's HTLC that is the user; for the user's own HTLC it is
	// the counterparty.
	ExpectRecipient string
	// ExpectSender, when set, is the address that created the HTLC and can
	// reclaim it after expiry (timeLocked).
	ExpectSender string
	// MinAmount, when set, is the smallest acceptable amount in the token's
	// base units.
	MinAmount *big.Int
	// MissingTerms lists terms of the trade this swap never recorded -- the
	// address the HTLC must pay, the amount it must hold -- and so cannot check.
	// Each is a REFUSAL. An expectation left blank used to switch its check
	// off, and a check switched off reads exactly like a check that passed:
	// an HTLC paying the counterparty's own address verified clean on a swap
	// that had no address of its own to compare against. These are not
	// pending either: no node answering later can supply a term the user
	// never agreed.
	MissingTerms []string
	// AmountUncheckable, when set, is why the agreed amount could not be turned
	// into base units -- the token metadata was unreachable, or the agreed
	// figure could not be parsed. It is a REFUSAL, not a note: an amount that
	// was agreed and then not checked must not be reported as one that matched.
	AmountUncheckable string

	// Now is the chain's own clock -- the frontier momentum timestamp. The
	// contract compares expirationTime against momentum time, not against the
	// verifying machine's wall clock, so every time comparison here uses this.
	Now int64
	// MinRemaining is how much time must be left before expiry for the HTLC to
	// be worth acting on. An HTLC that expires in a minute is one the creator
	// reclaims the moment you commit to the other leg.
	MinRemaining time.Duration
	// MaxExpiration, when non-zero, is the latest acceptable expirationTime.
	// It applies when the Zenon leg is the PARTICIPANT's, which must expire
	// before the initiator's Bitcoin leg.
	MaxExpiration int64
	// MinExpiration, when non-zero, is the earliest acceptable expirationTime.
	// It applies when the Zenon leg is the INITIATOR's, which must outlive the
	// participant's Bitcoin leg.
	MinExpiration int64
}

// incompleteCheck marks a refusal in which every problem was a check that could
// not be RUN, rather than one that ran and found a disagreement. Its message is
// the refusal's own, unchanged: the distinction is for callers deciding what to
// do next, not for the reader of the sentence.
type incompleteCheck struct{ error }

func (incompleteCheck) Is(target error) bool { return target == ErrCheckIncomplete }

// ErrCheckIncomplete is what a refusal made entirely of checks that could not be
// run matches, through errors.Is.
//
// Both kinds refuse and neither may be acted on -- a skipped check reads exactly
// like a passed one. They differ in what happens NEXT. A mismatch is an answer,
// and asking a second time gets the same one. A check that could not run has not
// answered at all, so a caller that stops asking has decided a node's bad minute
// is the swap's verdict. That is the shape of stall this exists to prevent: an
// HTLC that is perfectly good, refused once because the token metadata was
// briefly unreadable, and never looked at again.
var ErrCheckIncomplete = errors.New("a check could not be completed")

// CheckIncomplete reports whether this refusal is one worth repeating later.
func CheckIncomplete(err error) bool {
	return errors.Is(err, ErrCheckIncomplete)
}

// Verify checks an HTLC against what was agreed. Every one of these exists
// because skipping it loses money: a mismatched hash means the secret will not
// unlock it, a wrong recipient means someone else claims it, a short amount
// means underpayment, an expiry already upon us means the creator reclaims
// first, and one on the wrong side of the Bitcoin locktime means a party can
// wait out a chain and still act on the other.
func (c *Client) Verify(info *HtlcInfo, want VerifyParams) error {
	var problems []string
	// How many of the above are checks this browser could not run. Counted
	// rather than collected separately so the message keeps the order the
	// checks are written in.
	incomplete := 0

	// Terms the swap never recorded come first: nothing below can stand in for
	// them, and a verdict reached without them is not a verdict.
	for _, term := range want.MissingTerms {
		problems = append(problems, fmt.Sprintf(
			"%s, so that part of the HTLC cannot be checked. A skipped check reads exactly like "+
				"a passed one, so this is a refusal: add it to the swap and verify again", term))
	}
	if want.ExpectID != "" && !strings.EqualFold(strings.TrimPrefix(info.ID, "0x"),
		strings.TrimPrefix(want.ExpectID, "0x")) {
		problems = append(problems, fmt.Sprintf(
			"the node answered with entry %s, not the %s that was asked for",
			info.ID, want.ExpectID))
	}
	// Which token is locked is as much a term of the trade as how much of it.
	// Checked before the amount so a token mismatch is not buried under a
	// base-units comparison that was never meaningful.
	if want.ExpectTokenStandard != "" && !strings.EqualFold(info.TokenStandard, want.ExpectTokenStandard) {
		problems = append(problems, fmt.Sprintf(
			"holds token %s, but this swap agreed %s -- anyone can issue a token and lock "+
				"the agreed number of units of it",
			info.TokenStandard, want.ExpectTokenStandard))
	}
	if info.HashType != HashTypeSHA256 {
		problems = append(problems, fmt.Sprintf(
			"hashType is %d, must be %d (SHA-256) to match Bitcoin's OP_SHA256",
			info.HashType, HashTypeSHA256))
	}
	if info.KeyMaxSize < 32 {
		problems = append(problems, fmt.Sprintf(
			"keyMaxSize is %d, must be at least 32 to accept the 32-byte secret", info.KeyMaxSize))
	}
	if want.SecretHashHex != "" {
		got := info.HashLockHex()
		if !strings.EqualFold(got, want.SecretHashHex) {
			problems = append(problems, fmt.Sprintf(
				"hashlock is %s, expected %s", got, want.SecretHashHex))
		}
	}
	if want.ExpectRecipient != "" && !strings.EqualFold(info.HashLocked, want.ExpectRecipient) {
		problems = append(problems, fmt.Sprintf(
			"hashLocked address is %s, expected %s", info.HashLocked, want.ExpectRecipient))
	}
	if want.ExpectSender != "" && !strings.EqualFold(info.TimeLocked, want.ExpectSender) {
		problems = append(problems, fmt.Sprintf(
			"timeLocked address is %s, expected %s", info.TimeLocked, want.ExpectSender))
	}
	if want.AmountUncheckable != "" {
		problems = append(problems, fmt.Sprintf(
			"the agreed amount could not be checked (%s). A skipped check reads exactly like a "+
				"passed one, so this is a refusal: fix the node or the agreed amount and verify again",
			want.AmountUncheckable))
		incomplete++
	} else if want.MinAmount != nil {
		amt, ok := new(big.Int).SetString(info.Amount, 10)
		if !ok {
			problems = append(problems, fmt.Sprintf("amount %q is not a number", info.Amount))
		} else if amt.Cmp(want.MinAmount) < 0 {
			problems = append(problems, fmt.Sprintf(
				"amount %s is below the agreed %s", amt.String(), want.MinAmount.String()))
		}
	}
	if want.Now > 0 && want.MinRemaining > 0 {
		left := time.Duration(info.ExpirationTime-want.Now) * time.Second
		if left < want.MinRemaining {
			problems = append(problems, fmt.Sprintf(
				"expires in %s (at %s, chain time %s); at least %s must remain to act on it safely",
				left.Truncate(time.Second), utc(info.ExpirationTime), utc(want.Now),
				want.MinRemaining))
		}
	}
	if want.MaxExpiration > 0 && info.ExpirationTime > want.MaxExpiration {
		problems = append(problems, fmt.Sprintf(
			"expirationTime %s is later than %s; this Zenon leg is the participant's "+
				"and must expire before the initiator's Bitcoin leg",
			utc(info.ExpirationTime), utc(want.MaxExpiration)))
	}
	if want.MinExpiration > 0 && info.ExpirationTime < want.MinExpiration {
		problems = append(problems, fmt.Sprintf(
			"expirationTime %s is earlier than %s; this Zenon leg is the initiator's "+
				"and must outlive the participant's Bitcoin leg",
			utc(info.ExpirationTime), utc(want.MinExpiration)))
	}

	if len(problems) > 0 {
		err := fmt.Errorf("HTLC does not match the agreed terms: %s", strings.Join(problems, "; "))
		if len(problems) == incomplete {
			return incompleteCheck{err}
		}
		return err
	}
	return nil
}

func utc(unix int64) string {
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

// ProxyUnlockAllowed reports whether anyone may unlock an HTLC that pays this
// address, or only the address itself.
//
// The contract pays `hashLocked`, not the caller, and by default lets any
// account make the call -- which is what lets a wallet with no HTLC support
// settle this leg at all. An address that has called DenyProxyUnlock opts out,
// and then only the payee can unlock, which no ordinary wallet can be made to
// do. Asked before a block is built, because discovering it at settlement is
// discovering it after the money is locked.
func (c *Client) ProxyUnlockAllowed(ctx context.Context, address string) (bool, error) {
	// The RPC answers with a bare boolean, not an object
	// (rpc/api/embedded/htlc.go: GetProxyUnlockStatus returns (bool, error)).
	var allowed bool
	if err := c.call(ctx, "embedded.htlc.getProxyUnlockStatus", []any{address}, &allowed); err != nil {
		return false, err
	}
	return allowed, nil
}

package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zenon/ferry-web/wasm/chain"
	"github.com/zenon/ferry-web/wasm/znn"
)

// The call table. There is no server here, so nothing about being one survives:
// no token, no loopback guard, no CORS policy, no DNS-rebinding defence -- those
// stopped another origin or another local user reaching an HTTP endpoint that
// could move coins, and there is no endpoint. A route was a string plus a method
// plus a body decoder; what is left is the string and the body, so it is one map.

// Settings are the browser's own choices, sent with every call. There is no
// operator to defer to and nothing that outlives a call, so the same fields
// arrive on every request and a Manager is assembled from them.
type Settings struct {
	Network    string `json:"network,omitempty"`
	BtcEsplora string `json:"btcEsplora,omitempty"`
	ZnnURL     string `json:"znnUrl,omitempty"`
}

// manager assembles the pieces one call needs. Building one per call is
// affordable -- both clients are a URL and an http.Client -- and it is what
// keeps the settings honest: change the Esplora URL and the very next call uses
// it, with nothing cached from before.
func (s Settings) manager(store *Store) (*Manager, error) {
	network := s.Network
	if network == "" {
		network = "mainnet"
	}
	if _, err := NetworkParams(network); err != nil {
		return nil, err
	}

	backend := chain.Build(s.BtcEsplora)
	if backend == nil {
		url := DefaultEsploraURL(network)
		if url == "" {
			return nil, fmt.Errorf("no default Esplora instance for %q: set one in Node settings", network)
		}
		backend = chain.New(url)
	}

	var znnClient *znn.Client
	if s.ZnnURL != "" {
		znnClient = znn.New(s.ZnnURL)
	}
	return &Manager{Store: store, Chain: backend, Znn: znnClient, Network: network}, nil
}

// ---------- response shaping ----------

// swapView is what the UI sees. The ephemeral private key is redacted here and
// served only from the explicit recovery call, so it does not sit in every
// list response.
type swapView struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	Network   string    `json:"network"`
	Role      Role      `json:"role"`
	Leg       Leg       `json:"leg"`
	State     State     `json:"state"`

	AmountSats int64  `json:"amountSats"`
	DestAddr   string `json:"destAddr,omitempty"`

	// Every []byte on Swap is rendered as hex here. Left to encoding/json they
	// would come out base64, which nothing else in this system speaks.
	SecretHashHex      string `json:"secretHashHex"`
	SecretHex          string `json:"secretHex,omitempty"`
	ContractHex        string `json:"contractHex,omitempty"`
	CounterpartyPKHHex string `json:"counterpartyPkhHex,omitempty"`

	ContractAddr string   `json:"contractAddr,omitempty"`
	Key          *keyView `json:"key,omitempty"`

	LockTime   int64  `json:"lockTime"`
	LockTimeAt string `json:"lockTimeAt"`
	Refundable bool   `json:"refundable"`

	// Active is whether this swap is still on the Swaps page; Archived is
	// whether the user filed it away by hand. Those are now the same question
	// asked twice -- only the user files a swap -- and both are kept because the
	// UI reads one to pick a list and the other to point the archive button.
	//
	// Settled is the separate question: over, with nothing left to do but file
	// it. A settled swap is still Active, and the page shows it as a summary row
	// rather than a working card. Derived here rather than in JavaScript for the
	// same reason as the three flags below -- the rule for when a Bitcoin redeem
	// does NOT end the swap belongs in one place.
	Active   bool `json:"active"`
	Archived bool `json:"archived"`
	Settled  bool `json:"settled"`

	// BtcLegIsInitiators says which leg the Bitcoin contract is, which is what
	// decides the timelock ordering and which side reveals the preimage where.
	// The UI needs it to give role-correct instructions; deriving it in
	// JavaScript from role and leg would be a second copy of the rule.
	BtcLegIsInitiators   bool `json:"btcLegIsInitiators"`
	ZenonHtlcIsOurs      bool `json:"zenonHtlcIsOurs"`
	SecretArrivesOnZenon bool `json:"secretArrivesOnZenon"`
	// FundingShort flags a contract funded for less than was agreed.
	FundingShort bool `json:"fundingShort,omitempty"`
	// FundingCommitted says nothing about the counterparty's Bitcoin funding
	// stands in the way of this side creating its Zenon HTLC; where something
	// does, FundingCommitBlocker names it. Derived in Go so the card's gate and
	// planCreate's refusal are one rule.
	FundingCommitted     bool   `json:"fundingCommitted"`
	FundingCommitBlocker string `json:"fundingCommitBlocker,omitempty"`
	// MissingZenonTerms are the terms of the Zenon leg this swap never recorded
	// and so cannot verify an HTLC against -- the card's form is built from
	// this rather than from its own reading of the fields, so it and the
	// engine cannot disagree about what counts as missing (a zero amount
	// does).
	MissingZenonTerms []MissingTerm `json:"missingZenonTerms,omitempty"`

	Funding  *FundingOutput `json:"funding,omitempty"`
	RefundTx *SpendResult   `json:"refundTx,omitempty"`
	RedeemTx *SpendResult   `json:"redeemTx,omitempty"`

	// FundingBroadcast is a payment already sent to the contract from this
	// browser. The card uses it to stop offering to send a second one.
	FundingBroadcast *FundingBroadcast `json:"fundingBroadcast,omitempty"`

	Zenon  ZenonLeg `json:"zenon"`
	Events []Event  `json:"events"`
}

// keyView exposes only the public half of the swap's ephemeral keypair. The
// private key is returned solely from the recovery call, so it never sits in a
// list response.
type keyView struct {
	PubHex string `json:"pubHex"`
	PKHHex string `json:"pkhHex"`
}

func view(sw *Swap) *swapView {
	v := &swapView{
		ID:            sw.ID,
		CreatedAt:     sw.CreatedAt,
		Network:       sw.Network,
		Role:          sw.Role,
		Leg:           sw.Leg,
		State:         sw.State,
		AmountSats:    sw.AmountSats,
		DestAddr:      sw.DestAddr,
		SecretHashHex: hex.EncodeToString(sw.SecretHash),
		ContractAddr:  sw.ContractAddr,
		LockTime:      sw.LockTime,
		LockTimeAt:    time.Unix(sw.LockTime, 0).UTC().Format(time.RFC3339),
		// Terminal states are excluded: the output is gone, so offering a
		// refund button on a swap already redeemed or refunded only produces a
		// broadcast the network refuses.
		Refundable: sw.Leg == LegSend && sw.Funding != nil &&
			sw.State != StateRedeemed && sw.State != StateRefunded &&
			time.Now().Unix() >= sw.LockTime,
		Active:             sw.Active(),
		Archived:           sw.Archived,
		Settled:            sw.Settled(),
		BtcLegIsInitiators: sw.BitcoinLegIsInitiators(),
		ZenonHtlcIsOurs:    sw.ZenonHtlcIsOurs(),
		// The preimage becomes public wherever the party who holds it spends
		// first. That is the initiator, and they spend the leg they do NOT own:
		// so this user learns it on Zenon exactly when the counterparty is the
		// initiator and this user created the Zenon HTLC.
		SecretArrivesOnZenon: sw.SecretArrivesOnZenon(),
		FundingShort:         sw.Funding != nil && sw.Funding.Value < sw.AmountSats,
		FundingCommitted:     sw.FundingCommitted(),
		FundingCommitBlocker: sw.FundingCommitBlocker(),
		MissingZenonTerms:    sw.MissingZenonTerms(),
		Funding:              sw.Funding,
		FundingBroadcast:     sw.FundingBroadcast,
		RefundTx:             sw.RefundTx,
		RedeemTx:             sw.RedeemTx,
		Zenon:                sw.Zenon,
		Events:               sw.Events,
	}
	if len(sw.Contract) > 0 {
		v.ContractHex = hex.EncodeToString(sw.Contract)
	}
	if len(sw.CounterpartyPKH) > 0 {
		v.CounterpartyPKHHex = hex.EncodeToString(sw.CounterpartyPKH)
	}
	if sw.Key != nil {
		v.Key = &keyView{PubHex: sw.Key.PubHex(), PKHHex: sw.Key.PKHHex()}
	}
	// The secret IS shown: the user has to paste it into their Zenon wallet to
	// unlock that leg. The ephemeral private key is not.
	if len(sw.Secret) > 0 {
		v.SecretHex = hex.EncodeToString(sw.Secret)
	}
	return v
}

func views(swaps []*Swap) []*swapView {
	out := make([]*swapView, 0, len(swaps))
	for _, sw := range swaps {
		out = append(out, view(sw))
	}
	return out
}

// fundingCheck is the sign-time gate. planCreate runs requireCounterLegFunding
// when it BUILDS a block; this runs the same fail-closed check on its own, so
// the page can run it immediately before a built block is handed to the
// wallet -- a person may have spent minutes reading the summary, and the
// funding that was mined at plan time can have been spent or reorganised out
// since. Refresh is not a substitute: it is a projection that keeps what it
// last knew when a read fails, which is the opposite of what a check before an
// irreversible step needs.
//
// Answers {ok:true, waits:<bool>} or an error naming what is wrong. For the
// shapes that do not lock ZNN against Bitcoin funding it answers ok without
// consulting a chain.
type fundingCheckReq struct {
	ID       string   `json:"id"`
	Settings Settings `json:"settings"`
}

func handleFundingCheck(ctx context.Context, a *API, body []byte) (any, error) {
	var req fundingCheckReq
	mgr, err := withManager(a, body, &req, func(r *fundingCheckReq) Settings { return r.Settings })
	if err != nil {
		return nil, err
	}
	sw, err := a.Store.Load(req.ID)
	if err != nil {
		return nil, err
	}
	if err := requireCounterLegFunding(ctx, mgr.Chain, sw); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "waits": sw.ZenonCreateWaitsOnBtc()}, nil
}

// ---------- the call table ----------

// API is the object the page talks to. It holds the store and nothing else:
// every other dependency is rebuilt per call from the settings that call
// carried.
type API struct{ Store *Store }

// handler is one operation. It gets the raw request body so each can decode
// into its own shape, exactly as the HTTP handlers did.
type handler func(ctx context.Context, a *API, body []byte) (any, error)

// callTimeout bounds one operation: an Esplora instance that accepts a
// connection and then says nothing would otherwise leave a button spinning
// forever.
const callTimeout = 4 * time.Minute

// Call runs one operation and returns its JSON response.
//
// Errors come back as an {"error": ...} document rather than as a rejected
// promise, so the UI's single error path — the one it already had for a non-2xx
// response — keeps working unchanged.
func (a *API) Call(method string, body []byte) []byte {
	h, ok := routes[method]
	if !ok {
		return errorJSON(fmt.Errorf("unknown method %q", method))
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	out, err := h(ctx, a, body)
	if err != nil {
		return errorJSON(err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return errorJSON(fmt.Errorf("encode response: %w", err))
	}
	return raw
}

func errorJSON(err error) []byte {
	raw, merr := json.Marshal(map[string]string{"error": err.Error()})
	if merr != nil {
		return []byte(`{"error":"the error could not be encoded"}`)
	}
	return raw
}

// routes is the whole call surface. Populated in init to avoid an
// initialisation cycle: several handlers refer back to this map's own keys in
// their error text.
var routes map[string]handler

func init() {
	routes = map[string]handler{
		"config":       handleConfig,
		"list":         handleList,
		"create":       handleCreate,
		"get":          handleGet,
		"counterparty": handleCounterparty,
		"audit":        handleAudit,
		"fundingSent":  handleFundingSent,
		"refresh":      handleRefresh,
		"redeem":       handleRedeem,
		"refund":       handleRefund,
		"zenon":        handleZenon,
		"zenonFind":    handleZenonFind,
		"walletSync":   handleWalletSync,
		"walletBlock":  handleWalletBlock,
		"walletSent":   handleWalletSent,
		"fundingCheck": handleFundingCheck,
		"secret":       handleSetSecret,
		"zenonTerms":   handleZenonTerms,
		"archive":      handleArchive,
		"delete":       handleDelete,
		"offer":        handleOffer,
		"decodeOffer":  handleDecodeOffer,
		"recovery":     handleRecovery,
		"rebuild":      handleRebuild,
		"estimate":     handleEstimate,
		"sessionNew":   handleSessionNew,
		"sessionSend":  handleSessionSend,
		"sessionOpen":  handleSessionOpen,

		// The board. Everything here is about events rather than swaps: it
		// signs what this browser publishes and verifies what strangers do.
		// See board.go for why a post is signed by a key of this app's own
		// rather than by a wallet.
		"boardIdentity":  handleBoardIdentity,
		"boardStatement": handleBoardStatement,
		"boardBind":      handleBoardBind,
		"boardUnbind":    handleBoardUnbind,
		"boardPublish":   handleBoardPublish,
		"boardWithdraw":  handleBoardWithdraw,
		"boardMine":      handleBoardMine,
		"boardForget":    handleBoardForget,
		"boardLink":      handleBoardLink,
		"boardRead":      handleBoardRead,
		"boardTake":      handleBoardTake,
		"boardReadTake":  handleBoardReadTake,
		// Presence: a signed beat saying the key behind a post is at a keyboard.
		// Two calls for the same reason posts have two — one signs, one verifies,
		// and nothing decides a key is online without the second.
		"boardPresence":     handleBoardPresence,
		"boardReadPresence": handleBoardReadPresence,
		"chainId":           handleChainID,
		"export":            handleExport,
		"import":            handleImport,
	}
}

// decode parses a request body, refusing unknown fields. A field silently
// ignored is a swap term the user believed they had set, so a renamed one fails
// loudly here rather than quietly creating a swap with a default amount.
func decode(body []byte, out any) error {
	if len(body) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("bad request: %w", err)
	}
	return nil
}

// withManager decodes a body that carries settings and builds the Manager for
// it, which is the first thing almost every handler does.
func withManager[T any](a *API, body []byte, req *T, settings func(*T) Settings) (*Manager, error) {
	if err := decode(body, req); err != nil {
		return nil, err
	}
	return settings(req).manager(a.Store)
}

// ---------- handlers ----------

type configReq struct {
	Settings Settings `json:"settings"`
}

func handleConfig(ctx context.Context, a *API, body []byte) (any, error) {
	var req configReq
	mgr, err := withManager(a, body, &req, func(r *configReq) Settings { return r.Settings })
	if err != nil {
		return nil, err
	}

	cfg := map[string]any{
		"network": mgr.Network,
		"backend": mgr.Chain.Name(),
		"znn":     mgr.Znn != nil,
		// Which instance this is, from the module itself rather than from the
		// page around it. The badge in the header reads this one, so what it
		// claims is what the code signing transactions actually believes — a
		// UI-only flag could say "dev" over a build filing swaps under the
		// production namespace.
		"buildEnv":   Env(),
		"swapDir":    a.Store.Dir(),
		"secretSize": SecretSize,
		// Whether each side is on a URL this browser chose or on the built-in
		// default, so the status line can say which without the UI re-deriving
		// it from the settings it happens to hold.
		"usingOwnBtc": req.Settings.BtcEsplora != "",
		"usingOwnZnn": req.Settings.ZnnURL != "",
	}
	// Counts here rather than in the list responses, so a page can label its
	// link to the other list without fetching that list.
	if swaps, err := a.Store.List(); err == nil {
		var active int
		for _, sw := range swaps {
			if sw.Active() {
				active++
			}
		}
		cfg["activeSwaps"] = active
		cfg["historySwaps"] = len(swaps) - active
	}
	if h, err := mgr.Chain.TipHeight(ctx); err == nil {
		cfg["tipHeight"] = h
	} else {
		cfg["chainError"] = err.Error()
	}
	if mgr.Znn != nil {
		if m, err := mgr.Znn.FrontierMomentum(ctx); err == nil {
			cfg["znnHeight"] = m.Height
			cfg["znnTime"] = m.Timestamp
		} else {
			cfg["znnError"] = err.Error()
		}
	}
	return cfg, nil
}

// handleList returns stored swaps, optionally narrowed to the ones that still
// need attention or the ones that are done.
func handleList(_ context.Context, a *API, body []byte) (any, error) {
	var req struct {
		View string `json:"view"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	switch req.View {
	case "", "all", "active", "history":
	default:
		return nil, errors.New("view must be active, history or all")
	}
	swaps, err := a.Store.List()
	if err != nil {
		return nil, err
	}
	out := make([]*Swap, 0, len(swaps))
	for _, sw := range swaps {
		if req.View == "active" && !sw.Active() {
			continue
		}
		if req.View == "history" && sw.Active() {
			continue
		}
		out = append(out, sw)
	}
	return views(out), nil
}

type createReq struct {
	CreateParams
	Settings Settings `json:"settings"`
}

func handleCreate(_ context.Context, a *API, body []byte) (any, error) {
	var req createReq
	mgr, err := withManager(a, body, &req, func(r *createReq) Settings { return r.Settings })
	if err != nil {
		return nil, err
	}
	sw, err := mgr.Create(req.CreateParams)
	if err != nil {
		return nil, err
	}
	return view(sw), nil
}

// idReq is the body of every call that names a swap and nothing else.
type idReq struct {
	ID string `json:"id"`
}

func handleGet(_ context.Context, a *API, body []byte) (any, error) {
	var req idReq
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	sw, err := a.Store.Load(req.ID)
	if err != nil {
		return nil, err
	}
	return view(sw), nil
}

func handleCounterparty(_ context.Context, a *API, body []byte) (any, error) {
	var req struct {
		ID     string `json:"id"`
		PKHHex string `json:"pkhHex"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	// SetCounterpartyPKH touches no chain and no node, so it needs no settings.
	mgr := &Manager{Store: a.Store}
	sw, err := mgr.SetCounterpartyPKH(req.ID, req.PKHHex)
	if err != nil {
		return nil, err
	}
	return view(sw), nil
}

// handleFundingSent records a payment to the contract that has left a wallet but
// has not yet been seen on the chain. It needs no settings because it reaches no
// node: the whole point is that the chain does not know about this yet. Refresh
// is still the only thing that can decide a contract is funded.
func handleFundingSent(_ context.Context, a *API, body []byte) (any, error) {
	var req struct {
		ID   string `json:"id"`
		TxID string `json:"txid"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	mgr := &Manager{Store: a.Store}
	sw, err := mgr.RecordFundingBroadcast(req.ID, req.TxID)
	if err != nil {
		return nil, err
	}
	return view(sw), nil
}

func handleAudit(_ context.Context, a *API, body []byte) (any, error) {
	var req struct {
		ID          string `json:"id"`
		ContractHex string `json:"contractHex"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	// Auditing is pure: it parses the contract and compares it against terms
	// already in the swap record. Nothing is fetched, which is exactly why it
	// is the check that still works with every node in the world unreachable.
	mgr := &Manager{Store: a.Store}
	sw, err := mgr.AuditContract(req.ID, req.ContractHex)
	if err != nil {
		return nil, err
	}
	return view(sw), nil
}

type swapSettingsReq struct {
	ID       string   `json:"id"`
	Settings Settings `json:"settings"`
}

func handleRefresh(ctx context.Context, a *API, body []byte) (any, error) {
	var req swapSettingsReq
	mgr, err := withManager(a, body, &req, func(r *swapSettingsReq) Settings { return r.Settings })
	if err != nil {
		return nil, err
	}
	sw, err := mgr.Refresh(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	return view(sw), nil
}

type spendReq struct {
	ID       string   `json:"id"`
	DestAddr string   `json:"destAddr"`
	Settings Settings `json:"settings"`
}

func handleRedeem(ctx context.Context, a *API, body []byte) (any, error) {
	var req spendReq
	mgr, err := withManager(a, body, &req, func(r *spendReq) Settings { return r.Settings })
	if err != nil {
		return nil, err
	}
	sw, err := mgr.Redeem(ctx, req.ID, req.DestAddr)
	if err != nil {
		return nil, err
	}
	return view(sw), nil
}

func handleRefund(ctx context.Context, a *API, body []byte) (any, error) {
	var req spendReq
	mgr, err := withManager(a, body, &req, func(r *spendReq) Settings { return r.Settings })
	if err != nil {
		return nil, err
	}
	sw, err := mgr.Refund(ctx, req.ID, req.DestAddr)
	if err != nil {
		return nil, err
	}
	return view(sw), nil
}

type zenonReq struct {
	ID       string   `json:"id"`
	HtlcID   string   `json:"htlcId"`
	Settings Settings `json:"settings"`
}

func handleZenon(ctx context.Context, a *API, body []byte) (any, error) {
	var req zenonReq
	mgr, err := withManager(a, body, &req, func(r *zenonReq) Settings { return r.Settings })
	if err != nil {
		return nil, err
	}
	sw, info, verr := mgr.VerifyZenon(ctx, req.ID, req.HtlcID)
	if sw == nil {
		// Nothing was verified: the swap could not be loaded, no Zenon node is
		// set, or the node did not answer. That is a request or setup problem,
		// not a failed verification, so it is an error rather than a result.
		return nil, verr
	}
	// A rejected HTLC is a successful call with a bad answer. It comes back
	// alongside the updated swap so the card can show both what was checked and
	// why it failed.
	resp := map[string]any{"swap": view(sw), "htlc": info}
	if verr != nil {
		resp["error"] = verr.Error()
	}
	return resp, nil
}

// handleZenonFind is handleZenon with the id looked up instead of supplied. It
// answers in the same shape, including the verified-with-a-bad-answer case,
// because the card renders one result either way. A search that finds nothing is
// an error rather than a result: there is no entry to show, and the message
// explains which of the three ordinary reasons it is.
func handleZenonFind(ctx context.Context, a *API, body []byte) (any, error) {
	var req zenonReq
	mgr, err := withManager(a, body, &req, func(r *zenonReq) Settings { return r.Settings })
	if err != nil {
		return nil, err
	}
	sw, info, verr := mgr.FindZenonHtlc(ctx, req.ID)
	if sw == nil {
		return nil, verr
	}
	resp := map[string]any{"swap": view(sw), "htlc": info}
	if verr != nil {
		resp["error"] = verr.Error()
	}
	return resp, nil
}

// zenonTerms completes the Zenon terms a swap was created without. Blank
// fields are left alone; a field that is already recorded refuses a different
// value. See Manager.SetZenonTerms.
func handleZenonTerms(ctx context.Context, a *API, body []byte) (any, error) {
	var req struct {
		ID          string   `json:"id"`
		SelfAddress string   `json:"selfAddress"`
		PeerAddress string   `json:"peerAddress"`
		Amount      string   `json:"amount"`
		Settings    Settings `json:"settings"`
	}
	type termsReq = struct {
		ID          string   `json:"id"`
		SelfAddress string   `json:"selfAddress"`
		PeerAddress string   `json:"peerAddress"`
		Amount      string   `json:"amount"`
		Settings    Settings `json:"settings"`
	}
	mgr, err := withManager(a, body, (*termsReq)(&req), func(r *termsReq) Settings { return r.Settings })
	if err != nil {
		return nil, err
	}
	sw, err := mgr.SetZenonTerms(ctx, req.ID, req.SelfAddress, req.PeerAddress, req.Amount)
	if err != nil {
		return nil, err
	}
	return view(sw), nil
}

func handleSetSecret(_ context.Context, a *API, body []byte) (any, error) {
	var req struct {
		ID        string `json:"id"`
		SecretHex string `json:"secretHex"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	mgr := &Manager{Store: a.Store}
	sw, err := mgr.SetSecret(req.ID, req.SecretHex)
	if err != nil {
		return nil, err
	}
	return view(sw), nil
}

func handleArchive(_ context.Context, a *API, body []byte) (any, error) {
	var req struct {
		ID       string `json:"id"`
		Archived bool   `json:"archived"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	mgr := &Manager{Store: a.Store}
	sw, err := mgr.Archive(req.ID, req.Archived)
	if err != nil {
		return nil, err
	}
	return view(sw), nil
}

// handleDelete removes a swap from this browser for good.
//
// Archive files a swap away; this destroys it. The record holds the ephemeral
// private key, that key is the only thing that can spend the swap's contract,
// and localStorage has no undo -- so deleting the wrong swap costs exactly what
// losing the browser costs.
//
// Hence gated rather than merely offered. A redeemed or refunded swap is spent
// out and its key is worth nothing; one that never got a contract has nothing to
// spend. Anything else may still have money behind it, including one archived by
// hand halfway through -- the ordinary way a risky record ends up on the history
// page looking finished. Those come back as a refusal naming what is at stake,
// and go only when the caller says so a second time with `force`.
//
// `force` is a deliberate second answer to a question that named the risk, not a
// flag for skipping the check: see the confirm step in HistoryRow.vue.
func handleDelete(_ context.Context, a *API, body []byte) (any, error) {
	var req struct {
		ID    string `json:"id"`
		Force bool   `json:"force"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	sw, err := a.Store.Load(req.ID)
	if err != nil {
		return nil, err
	}
	if risk := sw.DeletionRisk(); risk != "" && !req.Force {
		return nil, fmt.Errorf("%s Deleting destroys the only key that can spend it, and there "+
			"is no undo. Download this swap's recovery file first if you have not already", risk)
	}
	if err := a.Store.Delete(req.ID); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": req.ID}, nil
}

func handleOffer(_ context.Context, a *API, body []byte) (any, error) {
	var req idReq
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	sw, err := a.Store.Load(req.ID)
	if err != nil {
		return nil, err
	}
	// Records created here always have a key; one that arrived through Import
	// is validated on the way in. This is the belt for a record hand-edited in
	// devtools, which the one-file-per-swap design deliberately allows -- a nil
	// dereference in Go/WASM is a far worse answer than a sentence.
	if sw.Key == nil {
		return nil, errors.New("this swap record has no key, so it has no pubkey hash to offer")
	}
	o := &Offer{
		Version:    1,
		Network:    sw.Network,
		FromRole:   sw.Role,
		SecretHash: hex.EncodeToString(sw.SecretHash),
		PKH:        sw.Key.PKHHex(),
		BTCLeg:     sw.Leg,
		AmountSats: sw.AmountSats,
		LockTime:   sw.LockTime,
		ZenonAddr:  sw.Zenon.SelfAddress,
		ZenonToken: sw.Zenon.TokenStandard,
		// The decimal form the user typed, not Zenon.Amount: that one holds
		// base units and is only populated once an HTLC has been verified, so
		// it is always empty at the moment an offer is sent.
		ZenonAmt: sw.Zenon.AmountDisplay,
	}
	enc, err := o.Encode()
	if err != nil {
		return nil, err
	}
	return map[string]any{"offer": enc, "decoded": o}, nil
}

func handleDecodeOffer(_ context.Context, _ *API, body []byte) (any, error) {
	var req struct {
		Offer string `json:"offer"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	o, err := DecodeOffer(strings.TrimSpace(req.Offer))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"decoded": o,
		// Tell the caller which side they should take, so the UI does not make
		// the user reason about it.
		"yourLeg":  o.BTCLeg.Opposite(),
		"yourRole": oppositeRole(o.FromRole),
	}, nil
}

func oppositeRole(r Role) Role {
	if r == RoleInitiator {
		return RoleParticipant
	}
	return RoleInitiator
}

// handleRecovery returns everything needed to recover the swap's funds without
// this app: the contract, the ephemeral key as WIF, the secret if known, and
// the pre-signed refund transaction.
//
// Losing the swap here means losing one browser's localStorage -- cleared by a
// setting, by private browsing ending, or by the same "clear site data" click
// someone uses to fix an unrelated page. The user is told to save this the
// moment a contract is funded.
func handleRecovery(_ context.Context, a *API, body []byte) (any, error) {
	var req idReq
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	sw, err := a.Store.Load(req.ID)
	if err != nil {
		return nil, err
	}
	params, err := sw.Params()
	if err != nil {
		return nil, err
	}
	if sw.Key == nil {
		return nil, errors.New("this swap record has no key, so there is nothing to recover with. " +
			"If you still hold the contract's WIF elsewhere, the Recover page takes it directly")
	}
	wif, err := sw.Key.WIF(params)
	if err != nil {
		return nil, err
	}

	rec := map[string]any{
		"swapId":        sw.ID,
		"network":       sw.Network,
		"createdAt":     sw.CreatedAt,
		"leg":           sw.Leg,
		"role":          sw.Role,
		"contractHex":   hex.EncodeToString(sw.Contract),
		"contractAddr":  sw.ContractAddr,
		"lockTime":      sw.LockTime,
		"lockTimeUTC":   time.Unix(sw.LockTime, 0).UTC().Format(time.RFC3339),
		"privateKeyWIF": wif,
		"pubkeyHex":     sw.Key.PubHex(),
		"destAddr":      sw.DestAddr,
		"funding":       sw.Funding,
		"README": []string{
			"This file can move the funds in the contract. Store it like a private key.",
			"",
			"IF THE SWAP STALLED AND YOU WANT YOUR MONEY BACK:",
			"  Wait until lockTimeUTC has passed, then broadcast presignedRefundHex.",
			"  Any node or explorer broadcast form will accept it, e.g.",
			"  https://mempool.space/tx/push -- this app does not need to be running.",
			"",
			"IF THAT TRANSACTION IS STUCK, OR YOU WANT A DIFFERENT DESTINATION:",
			"  Open the Recover page and load this file. It rebuilds and re-signs the",
			"  spend in your browser, at a fee rate you choose, and prints raw hex.",
			"  That page needs no network and no stored swap: save a copy of the site",
			"  and it works offline, from any browser, on any machine.",
			"",
			"IF YOU HOLD THE SECRET AND NEED TO CLAIM INSTEAD:",
			"  The same page takes the preimage and builds the redeem rather than the",
			"  refund. It parses the contract to work out which branch your key can take.",
		},
	}
	if len(sw.Secret) > 0 {
		rec["secretHex"] = hex.EncodeToString(sw.Secret)
	}
	if sw.RefundTx != nil {
		rec["presignedRefundHex"] = sw.RefundTx.RawHex
		rec["presignedRefundTxid"] = sw.RefundTx.TxID
	}
	if sw.Zenon.HtlcID != "" {
		rec["zenonHtlcId"] = sw.Zenon.HtlcID
	}
	return rec, nil
}

// handleEstimate prices getting back out of a contract, before getting into it.
//
// The only read-only handler that will answer with no chain behind it: pass a
// fee rate and it never touches the network, which is what lets the offline
// Recover page work out the highest rate a stuck contract can afford. Left
// blank, it asks the node the question Manager.Redeem asks and falls back the
// same way, so the quote and the spend are the same arithmetic.
func handleEstimate(ctx context.Context, a *API, body []byte) (any, error) {
	var req struct {
		AmountSats  int64    `json:"amountSats"`
		DestAddr    string   `json:"destAddr,omitempty"`
		ContractHex string   `json:"contractHex,omitempty"`
		SwapID      string   `json:"id,omitempty"`
		FeeRate     float64  `json:"feeRate,omitempty"`
		Settings    Settings `json:"settings,omitempty"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	network := req.Settings.Network
	if network == "" {
		network = "mainnet"
	}

	// Naming a swap is the accurate way to ask: it sizes against the contract
	// that was actually built and the amount that was actually locked, rather
	// than against the template and whatever is currently typed into a form.
	if req.SwapID != "" {
		sw, err := a.Store.Load(req.SwapID)
		if err != nil {
			return nil, err
		}
		// The swap's network, not the browser's. A quote is about addresses
		// belonging to this swap, and a card for a mainnet swap is still on
		// screen while the app is pointed at regtest -- decoding its payout
		// address against the wrong parameters would answer a question about
		// fees with an address error.
		network = sw.Network
		if len(sw.Contract) > 0 && req.ContractHex == "" {
			req.ContractHex = hex.EncodeToString(sw.Contract)
		}
		if req.DestAddr == "" {
			req.DestAddr = sw.DestAddr
		}
		if req.AmountSats == 0 {
			// What is in the contract, not what was agreed: a short-funded
			// contract is priced on the money that is really there.
			req.AmountSats = sw.AmountSats
			if sw.Funding != nil && sw.Funding.Value > 0 {
				req.AmountSats = sw.Funding.Value
			}
		}
	}

	params, err := NetworkParams(network)
	if err != nil {
		return nil, err
	}

	feeRate, from := req.FeeRate, "you"
	if feeRate <= 0 {
		from = "fallback"
		feeRate = fallbackFeeRate
		m, merr := req.Settings.manager(a.Store)
		if merr == nil {
			if rate, rerr := m.Chain.FeeRate(ctx, quoteConfTarget); rerr == nil && rate > 0 {
				feeRate, from = rate, "node"
			}
		}
	}
	return EstimateSpendCost(req.AmountSats, req.DestAddr, req.ContractHex, feeRate, from, params)
}

// handleChainID answers which chains this browser is on and -- given a
// counterparty's answer -- whether the two agree.
//
// Both directions are one handler because they are one conversation, and because
// the checking half must run against THIS browser's nodes. A peer's numbers are
// a claim; what makes them evidence is asking our own node the same question.
func handleChainID(ctx context.Context, a *API, body []byte) (any, error) {
	var req struct {
		// Peer, when present, is the counterparty's fingerprint to check.
		Peer     *ChainFingerprint `json:"peer,omitempty"`
		Settings Settings          `json:"settings,omitempty"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	m, err := req.Settings.manager(a.Store)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"mine": BuildFingerprint(ctx, m)}
	if req.Peer != nil {
		out["agreement"] = VerifyFingerprint(ctx, m, req.Peer)
	}
	return out, nil
}

// handleRebuild is `ferry recover` — the offline rescue path — as a call.
func handleRebuild(_ context.Context, _ *API, body []byte) (any, error) {
	var req RebuildRequest
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	return Rebuild(req)
}

func handleExport(_ context.Context, a *API, _ []byte) (any, error) {
	raw, err := a.Store.Export()
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func handleImport(_ context.Context, a *API, body []byte) (any, error) {
	var req struct {
		Data string `json:"data"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	added, skipped, rejected, err := a.Store.Import([]byte(req.Data))
	if err != nil {
		return nil, err
	}
	return map[string]any{"added": added, "skipped": skipped, "rejected": rejected}, nil
}

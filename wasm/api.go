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

	"github.com/zenon/ferry-web-v2/wasm/chain"
	"github.com/zenon/ferry-web-v2/wasm/sol"
	"github.com/zenon/ferry-web-v2/wasm/znn"
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
	SolRPC     string `json:"solRpc,omitempty"`
	// SolProgram is which deployment of the swap program this browser will use
	// for legs it creates. It is a SETTING here and a TERM on a swap: once a leg
	// exists it carries the program it was made for, and a later change to this
	// does not move it.
	SolProgram string `json:"solProgram,omitempty"`
}

// SolanaProgram is the deployment this build defaults to, set by a linker flag
// because a program's address comes from its keypair and so changes whenever it
// is deployed somewhere new.
//
// A default, not a constant: Node settings can name any deployment, and an offer
// naming a different one is refused rather than silently accepted, so a wrong
// default costs a message and not money.
var SolanaProgram = ""

// solProgram is the deployment a new leg should be created on.
func (s Settings) solProgram() string {
	if p := strings.TrimSpace(s.SolProgram); p != "" {
		return p
	}
	return strings.TrimSpace(SolanaProgram)
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
	// Solana falls back to a public endpoint where Zenon deliberately does not.
	// The asymmetry is real: an escrow is checked against a PDA this module
	// derives itself, so a node that lies about the account gets caught by the
	// derivation rather than believed.
	solURL := strings.TrimSpace(s.SolRPC)
	if solURL == "" {
		solURL = DefaultSolanaURL(network)
	}
	var solClient *sol.Client
	if solURL != "" {
		solClient = sol.New(solURL)
	}
	return &Manager{Store: store, Chain: backend, Znn: znnClient, Sol: solClient,
		Network: network}, nil
}

// ---------- response shaping ----------

// swapView is what the UI sees. The per-swap private keys are redacted here and
// served only from the explicit recovery call, so they do not sit in every list
// response.
//
// It mirrors the record's shape — two legs, each naming its chain — rather than
// flattening them, because a page that has to guess which half is which is a
// page that will guess wrong on the pair nobody tested.
type swapView struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	Network   string    `json:"network"`
	Role      Role      `json:"role"`
	State     State     `json:"state"`

	// Pair is the stable name of the trade and Chains every chain it settles
	// on, deduplicated. The board's proof rule is written over the second.
	Pair   string    `json:"pair"`
	Chains []ChainID `json:"chains"`

	Out *legView `json:"out"`
	In  *legView `json:"in"`

	// Every []byte on Swap is rendered as hex here. Left to encoding/json they
	// would come out base64, which nothing else in this system speaks.
	SecretHashHex string `json:"secretHashHex"`
	// The secret IS shown: the user may have to paste it into a wallet to claim
	// a leg. The per-swap private key is not.
	SecretHex string `json:"secretHex,omitempty"`

	Active   bool `json:"active"`
	Archived bool `json:"archived"`
	// Settled is over-with-nothing-left-to-do. A settled swap is still Active:
	// the page shows it as a summary row until the user files it.
	Settled bool `json:"settled"`

	// OutIsInitiators says which leg takes the long timelock, which decides the
	// ordering and where the preimage surfaces. Derived here rather than in
	// JavaScript so the rule lives in one place.
	OutIsInitiators bool `json:"outIsInitiators"`
	// SecretArrivesOn is the chain this user learns the preimage on, or "" when
	// they invented it.
	SecretArrivesOn ChainID `json:"secretArrivesOn,omitempty"`

	Events []Event `json:"events"`
}

// legView is one half of a swap, as the page reads it.
type legView struct {
	Chain ChainID `json:"chain"`
	Dir   Dir     `json:"dir"`
	Label string  `json:"label"`

	Token        string `json:"token,omitempty"`
	TokenName    string `json:"tokenName,omitempty"`
	Amount       string `json:"amount"`
	Base         string `json:"base,omitempty"`
	Unit         string `json:"unit,omitempty"`
	Expiry       int64  `json:"expiry,omitempty"`
	ExpiryAt     string `json:"expiryAt,omitempty"`
	SelfAddr     string `json:"selfAddr,omitempty"`
	PeerAddr     string `json:"peerAddr,omitempty"`
	IsInitiators bool   `json:"isInitiators"`

	// Funded, Verified and Done are the three questions every card asks of a
	// leg, answered the same way whatever chain it is on.
	Funded   bool `json:"funded"`
	Verified bool `json:"verified"`
	Done     bool `json:"done"`
	// Reclaimed says WHICH way a done leg finished: taken back by the side that
	// funded it, rather than claimed by the side it was owed to. Both stop the
	// money moving, and a card that cannot tell them apart tells the funder of
	// a refunded leg that their counterparty has published a preimage.
	Reclaimed bool `json:"reclaimed,omitempty"`
	// Expired is a deadline that has passed with money still in the leg.
	Expired bool `json:"expired,omitempty"`

	Btc *btcLegView `json:"btc,omitempty"`
	Znn *ZnnLeg     `json:"znn,omitempty"`
	Sol *SolLeg     `json:"sol,omitempty"`
}

// btcLegView is BtcLeg with the private key replaced by its public half.
type btcLegView struct {
	Key                *keyView          `json:"key,omitempty"`
	CounterpartyPKHHex string            `json:"counterpartyPkhHex,omitempty"`
	ContractHex        string            `json:"contractHex,omitempty"`
	ContractAddr       string            `json:"contractAddr,omitempty"`
	Funding            *FundingOutput    `json:"funding,omitempty"`
	FundingBroadcast   *FundingBroadcast `json:"fundingBroadcast,omitempty"`
	RefundTx           *SpendResult      `json:"refundTx,omitempty"`
	RedeemTx           *SpendResult      `json:"redeemTx,omitempty"`
	Claimed            bool              `json:"claimed,omitempty"`
	Reclaimed          bool              `json:"reclaimed,omitempty"`
	// FundingShort flags a contract funded for less than was agreed.
	FundingShort bool `json:"fundingShort,omitempty"`
	// Refundable is the button's own condition: this side funded it, money is
	// in it, nothing has spent it, and the clock has passed.
	Refundable bool `json:"refundable,omitempty"`
}

// keyView exposes only the public half of a leg's ephemeral keypair.
type keyView struct {
	PubHex string `json:"pubHex"`
	PKHHex string `json:"pkhHex"`
}

func legToView(sw *Swap, l *Leg) *legView {
	if l == nil {
		return nil
	}
	now := time.Now().Unix()
	v := &legView{
		Chain:        l.Chain,
		Dir:          l.Dir,
		Label:        l.Label(),
		Token:        l.Token,
		Amount:       l.Amount,
		Base:         l.Base,
		Expiry:       l.Expiry,
		SelfAddr:     l.SelfAddr,
		PeerAddr:     l.PeerAddr,
		IsInitiators: sw.legIsInitiators(l.Dir),
		Funded:       l.Funded(),
		Verified:     l.Verified(),
		Done:         legDone(l),
		Reclaimed:    legReclaimed(l),
	}
	if c, err := ChainOf(l.Chain); err == nil {
		v.Unit = c.Unit
	}
	if l.Chain == ChainZNN {
		v.TokenName = TokenShortName(l.Token)
		v.Unit = v.TokenName
	}
	if l.Expiry > 0 {
		v.ExpiryAt = utcTime(l.Expiry)
		v.Expired = now >= l.Expiry && !v.Done
	}
	switch l.Chain {
	case ChainBTC:
		if l.Btc == nil {
			break
		}
		b := &btcLegView{
			ContractAddr:     l.Btc.ContractAddr,
			Funding:          l.Btc.Funding,
			FundingBroadcast: l.Btc.FundingBroadcast,
			RefundTx:         l.Btc.RefundTx,
			RedeemTx:         l.Btc.RedeemTx,
			Claimed:          l.Btc.Claimed,
			Reclaimed:        l.Btc.Reclaimed,
		}
		if len(l.Btc.Contract) > 0 {
			b.ContractHex = hex.EncodeToString(l.Btc.Contract)
		}
		if len(l.Btc.CounterpartyPKH) > 0 {
			b.CounterpartyPKHHex = hex.EncodeToString(l.Btc.CounterpartyPKH)
		}
		if l.Btc.Key != nil {
			b.Key = &keyView{PubHex: l.Btc.Key.PubHex(), PKHHex: l.Btc.Key.PKHHex()}
		}
		b.FundingShort = l.Btc.Funding != nil && l.Btc.Funding.Value < l.Sats()
		// Terminal cases are excluded: the output is gone, so offering a refund
		// on a contract already spent only produces a broadcast the network
		// refuses.
		b.Refundable = l.Dir == DirOut && l.Btc.Funding != nil &&
			!l.Btc.Claimed && !l.Btc.Reclaimed && now >= l.Expiry
		v.Btc = b
	case ChainZNN:
		v.Znn = l.Znn
	case ChainSOL:
		v.Sol = l.Sol
	}
	return v
}

func view(sw *Swap) *swapView {
	v := &swapView{
		ID:              sw.ID,
		CreatedAt:       sw.CreatedAt,
		Network:         sw.Network,
		Role:            sw.Role,
		State:           sw.State,
		Pair:            sw.Pair(),
		Chains:          sw.Chains(),
		Out:             legToView(sw, sw.Out),
		In:              legToView(sw, sw.In),
		SecretHashHex:   hex.EncodeToString(sw.SecretHash),
		Active:          sw.Active(),
		Archived:        sw.Archived,
		Settled:         sw.Settled(),
		OutIsInitiators: sw.OutIsInitiators(),
		SecretArrivesOn: sw.SecretArrivesOn(),
		Events:          sw.Events,
	}
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

		// The Solana leg. Two calls, mirroring the Zenon pair: one builds an
		// instruction for the page's wallet to sign, one records what came
		// back. Nothing here signs or sends.
		"solInstruction": handleSolInstruction,
		"solSent":        handleSolSent,
		"walletSync":     handleWalletSync,
		"walletBlock":    handleWalletBlock,
		"walletSent":     handleWalletSent,
		"secret":         handleSetSecret,
		"archive":        handleArchive,
		"delete":         handleDelete,
		"offer":          handleOffer,
		"decodeOffer":    handleDecodeOffer,
		"recovery":       handleRecovery,
		"rebuild":        handleRebuild,
		"estimate":       handleEstimate,
		"sessionNew":     handleSessionNew,
		"sessionSend":    handleSessionSend,
		"sessionOpen":    handleSessionOpen,

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
		"usingOwnSol": req.Settings.SolRPC != "",
		"sol":         mgr.Sol != nil,
		"solProgram":  req.Settings.solProgram(),
		// Which trades this build will let somebody agree to, and which chains
		// they touch. Sent from here so the create form and the board's proof
		// gates are written over the module's list rather than a second copy of
		// it in JavaScript.
		"pairs":  pairsView(),
		"chains": chainsView(),
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
	if mgr.Sol != nil {
		if v, err := mgr.Sol.Version(ctx); err == nil {
			cfg["solVersion"] = v
			if now, nerr := mgr.Sol.Now(ctx); nerr == nil {
				cfg["solTime"] = now
			}
		} else {
			cfg["solError"] = err.Error()
		}
	}
	return cfg, nil
}

// pairsView is the trade list, as the page needs it.
func pairsView() []map[string]any {
	out := make([]map[string]any, 0, len(Pairs))
	for _, p := range Pairs {
		out = append(out, map[string]any{
			"id":     PairID(p.A, p.B),
			"label":  p.Label,
			"chains": chainsOf(p.A, p.B),
			"a":      p.A,
			"b":      p.B,
		})
	}
	return out
}

// chainsView is what each chain is called and how it is proven, so the board's
// buttons and the create form read one list.
func chainsView() []map[string]any {
	out := make([]map[string]any, 0, len(ChainOrder))
	for _, id := range ChainOrder {
		c := chains[id]
		out = append(out, map[string]any{
			"id":        c.ID,
			"label":     c.Label,
			"unit":      c.Unit,
			"decimals":  c.Decimals,
			"tokenised": c.Tokenised,
			"scheme":    c.Proof,
		})
	}
	return out
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
	ID     string `json:"id"`
	HtlcID string `json:"htlcId"`
	// Dir names WHICH Zenon leg. A ZTS↔ZTS swap has two, so the direction is a
	// parameter rather than something to search for; it defaults to the
	// incoming one, which is the leg every other pair means by "their HTLC".
	Dir      Dir      `json:"dir,omitempty"`
	Settings Settings `json:"settings"`
}

// legDir reads a request's direction, defaulting to the incoming leg.
func legDir(d Dir) (Dir, error) {
	switch d {
	case "", DirIn:
		return DirIn, nil
	case DirOut:
		return DirOut, nil
	}
	return "", fmt.Errorf("%q is not a leg direction: it is %q or %q", d, DirOut, DirIn)
}

func handleZenon(ctx context.Context, a *API, body []byte) (any, error) {
	var req zenonReq
	mgr, err := withManager(a, body, &req, func(r *zenonReq) Settings { return r.Settings })
	if err != nil {
		return nil, err
	}
	dir, err := legDir(req.Dir)
	if err != nil {
		return nil, err
	}
	sw, info, verr := mgr.VerifyZenon(ctx, req.ID, dir, req.HtlcID)
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
	dir, err := legDir(req.Dir)
	if err != nil {
		return nil, err
	}
	sw, info, verr := mgr.FindZenonHtlc(ctx, req.ID, dir)
	if sw == nil {
		return nil, verr
	}
	resp := map[string]any{"swap": view(sw), "htlc": info}
	if verr != nil {
		resp["error"] = verr.Error()
	}
	return resp, nil
}

type solInstructionReq struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	// From is the account the wallet has selected. It is checked against the
	// swap for a create, where it becomes the escrow's initiator and so the only
	// address a refund can ever reach.
	From     string   `json:"from,omitempty"`
	Settings Settings `json:"settings"`
}

type solSentReq struct {
	ID        string   `json:"id"`
	Action    string   `json:"action"`
	Signature string   `json:"signature"`
	Settings  Settings `json:"settings"`
}

// handleSolInstruction builds one Solana instruction for this swap.
//
// What comes back is program id, accounts and data — never a transaction and
// never a signature. The page assembles it, the user's wallet shows it and signs
// it, and the page submits through the node named in Node settings. That split
// is the same one the Zenon leg makes with the Syrius extension, and for the
// same reason: everything that decides whether a step is safe happens here.
func handleSolInstruction(ctx context.Context, a *API, body []byte) (any, error) {
	var req solInstructionReq
	mgr, err := withManager(a, body, &req,
		func(r *solInstructionReq) Settings { return r.Settings })
	if err != nil {
		return nil, err
	}
	ix, sw, err := mgr.SolInstruction(ctx, req.ID, req.Action, req.From)
	if err != nil {
		return nil, err
	}
	resp := map[string]any{"instruction": ix, "swap": view(sw), "action": req.Action}
	// What the step costs beyond the amount, so "the swap costs 1 SOL" is not
	// quietly untrue: an escrow account has to be rent-exempt, and the initiator
	// pays that and gets it back on either exit.
	if req.Action == "create" {
		if rent, rerr := mgr.Sol.MinimumRent(ctx); rerr == nil {
			resp["rentLamports"] = rent
			resp["rent"] = solDisplay(rent)
		}
	}
	return resp, nil
}

// handleSolSent records a signature the wallet produced, then re-reads the
// chain. A signature is not evidence on its own — a transaction can be dropped,
// and one that landed can have failed — so Refresh is what decides.
func handleSolSent(ctx context.Context, a *API, body []byte) (any, error) {
	var req solSentReq
	mgr, err := withManager(a, body, &req, func(r *solSentReq) Settings { return r.Settings })
	if err != nil {
		return nil, err
	}
	if _, err := mgr.RecordSolTx(req.ID, req.Action, req.Signature); err != nil {
		return nil, err
	}
	sw, rerr := mgr.Refresh(ctx, req.ID)
	if rerr != nil {
		reloaded, lerr := a.Store.Load(req.ID)
		if lerr != nil {
			return nil, lerr
		}
		return map[string]any{"swap": view(reloaded), "error": rerr.Error()}, nil
	}
	return map[string]any{"swap": view(sw)}, nil
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
	if sw.Out == nil || sw.In == nil {
		return nil, errors.New("this swap record has no legs, so there is nothing to offer")
	}
	o := &Offer{
		Version:    2,
		Network:    sw.Network,
		FromRole:   sw.Role,
		SecretHash: hex.EncodeToString(sw.SecretHash),
		Give:       offerLegOf(sw.Out),
		Take:       offerLegOf(sw.In),
	}
	enc, err := o.Encode()
	if err != nil {
		return nil, err
	}
	return map[string]any{"offer": enc, "decoded": o}, nil
}

// offerLegOf renders one leg as the sender's half of an offer. It carries the
// sender's own address on that chain and nothing about the counterparty: the
// receiver fills in their own side.
func offerLegOf(l *Leg) OfferLeg {
	out := OfferLeg{
		Chain:  l.Chain,
		Token:  l.Token,
		Amount: l.Amount,
		Addr:   l.SelfAddr,
		Expiry: l.Expiry,
	}
	switch l.Chain {
	case ChainBTC:
		// A Bitcoin leg commits to a pubkey hash rather than an address, and it
		// is the sender's own — the receiver puts it in whichever branch their
		// side of the contract needs.
		if l.Btc != nil && l.Btc.Key != nil {
			out.PKH = l.Btc.Key.PKHHex()
		}
		// The payout address is this browser's business, not the
		// counterparty's: nothing on the other side pays it.
		out.Addr = ""
	case ChainSOL:
		if l.Sol != nil {
			out.Program = l.Sol.ProgramID
			out.SwapID = l.Sol.SwapID
		}
	}
	return out
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
	// Tell the caller which side they should take, so the UI does not make the
	// user reason about it. The sender's give is the receiver's take, and the
	// two roles are opposite by definition.
	return map[string]any{
		"decoded":  o,
		"yourOut":  o.Take,
		"yourIn":   o.Give,
		"yourRole": o.FromRole.Opposite(),
		"pair":     PairID(o.Give.Chain, o.Take.Chain),
	}, nil
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
	// The recovery file is about the Bitcoin leg and only about it: it is the
	// one leg whose rescue needs a key this app holds. A Zenon leg is reclaimed
	// by the wallet that created it and a Solana escrow refunds itself to the
	// account that funded it, so neither needs anything exported from here — the
	// facts that matter for those are in the swap's own export.
	btc := sw.Out
	if btc == nil || btc.Chain != ChainBTC {
		btc = sw.In
	}
	if btc == nil || btc.Chain != ChainBTC || btc.Btc == nil || btc.Btc.Key == nil {
		return nil, errors.New("this swap has no Bitcoin leg, so there is no key to recover " +
			"with. A Zenon leg is reclaimed from the wallet that created it, and a Solana " +
			"escrow refunds to the account that funded it — use Export for the whole record")
	}
	wif, err := btc.Btc.Key.WIF(params)
	if err != nil {
		return nil, err
	}

	rec := map[string]any{
		"swapId":        sw.ID,
		"network":       sw.Network,
		"createdAt":     sw.CreatedAt,
		"pair":          sw.Pair(),
		"dir":           btc.Dir,
		"role":          sw.Role,
		"contractHex":   hex.EncodeToString(btc.Btc.Contract),
		"contractAddr":  btc.Btc.ContractAddr,
		"lockTime":      btc.Expiry,
		"lockTimeUTC":   utcTime(btc.Expiry),
		"privateKeyWIF": wif,
		"pubkeyHex":     btc.Btc.Key.PubHex(),
		"destAddr":      btc.SelfAddr,
		"funding":       btc.Btc.Funding,
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
	if btc.Btc.RefundTx != nil {
		rec["presignedRefundHex"] = btc.Btc.RefundTx.RawHex
		rec["presignedRefundTxid"] = btc.Btc.RefundTx.TxID
	}
	// The other leg, for the record. Not recoverable from this file — it is
	// here so somebody rescuing a swap out of a backup can see what the trade
	// was and which entry on the other chain belongs to it.
	if other := sw.other(btc); other != nil {
		leg := map[string]any{"chain": other.Chain, "amount": other.Amount, "dir": other.Dir}
		if other.Znn != nil && other.Znn.HtlcID != "" {
			leg["zenonHtlcId"] = other.Znn.HtlcID
		}
		if other.Sol != nil && other.Sol.Escrow != "" {
			leg["solEscrow"] = other.Sol.Escrow
			leg["solProgram"] = other.Sol.ProgramID
			leg["solSwapId"] = other.Sol.SwapID
		}
		rec["otherLeg"] = leg
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
		btc := sw.Out
		if btc == nil || btc.Chain != ChainBTC {
			btc = sw.In
		}
		if btc == nil || btc.Chain != ChainBTC || btc.Btc == nil {
			return nil, errors.New("this swap has no Bitcoin leg, so there is no contract to " +
				"price getting out of")
		}
		if len(btc.Btc.Contract) > 0 && req.ContractHex == "" {
			req.ContractHex = hex.EncodeToString(btc.Btc.Contract)
		}
		if req.DestAddr == "" {
			req.DestAddr = btc.SelfAddr
		}
		if req.AmountSats == 0 {
			// What is in the contract, not what was agreed: a short-funded
			// contract is priced on the money that is really there.
			req.AmountSats = btc.Sats()
			if btc.Btc.Funding != nil && btc.Btc.Funding.Value > 0 {
				req.AmountSats = btc.Btc.Funding.Value
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

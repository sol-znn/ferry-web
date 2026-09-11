package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zenon/ferry-web/wasm/chain"
)

// diffSignedBlock is the check that runs after the wallet has already acted, so
// what it must not do is stay quiet. Every case below is a way the published
// block can differ from the one this page built, and the one that matters most
// is the first: the extension re-reads its selected account at signing time, so
// the account that signs is not necessarily the account it reported.

func proposal() *walletBlock {
	b := newWalletBlock(69, "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d")
	b.Amount = "1000000000"
	b.TokenStandard = "zts1znnxxxxxxxxxxxxx9z4ulx"
	b.Data = "XH5xEAAA"
	return b
}

// signedFrom renders a proposal the way the wallet's toJson would, with the
// fields it fills in itself added.
func signedFrom(b *walletBlock) map[string]any {
	return map[string]any{
		"address":         b.Address,
		"chainIdentifier": float64(b.ChainIdentifier),
		"toAddress":       b.ToAddress,
		"amount":          b.Amount,
		"tokenStandard":   b.TokenStandard,
		"data":            b.Data,
		// The wallet's own, and deliberately not compared.
		"height":       float64(42),
		"previousHash": "aa",
		"nonce":        "0000000000000001",
		"signature":    "not-compared",
	}
}

func TestDiffSignedBlockAccepts(t *testing.T) {
	if got := diffSignedBlock(proposal(), signedFrom(proposal())); len(got) != 0 {
		t.Errorf("an untouched block was reported as changed: %v", got)
	}
}

func TestDiffSignedBlockCatchesADifferentSigner(t *testing.T) {
	signed := signedFrom(proposal())
	signed["address"] = "z1qzsomewhereelsexxxxxxxxxxxxxxxxxxxxxxx"
	got := diffSignedBlock(proposal(), signed)
	if len(got) != 1 || !strings.Contains(got[0], "signed by") {
		t.Fatalf("a different signing account was not reported: %v", got)
	}
}

func TestDiffSignedBlockCatchesTheRest(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"chain":  func(m map[string]any) { m["chainIdentifier"] = float64(1) },
		"payee":  func(m map[string]any) { m["toAddress"] = "z1qzsomewhereelsexxxxxxxxxxxxxxxxxxxxxxx" },
		"data":   func(m map[string]any) { m["data"] = "different" },
		"token":  func(m map[string]any) { m["tokenStandard"] = "zts1qsrxxxxxxxxxxxxxmrhjll" },
		"amount": func(m map[string]any) { m["amount"] = "1" },
	} {
		signed := signedFrom(proposal())
		mutate(signed)
		if got := diffSignedBlock(proposal(), signed); len(got) == 0 {
			t.Errorf("a changed %s was not reported", name)
		}
	}
}

// A wallet that reports nothing back must not read as a wallet that agreed.
// There is nothing to compare, so there is nothing to say -- the create's own
// verification against the chain is what covers this case, and inventing a
// mismatch here would block a swap over a field the extension simply omits.
func TestDiffSignedBlockWithNothingToCompare(t *testing.T) {
	if got := diffSignedBlock(proposal(), nil); got != nil {
		t.Errorf("an absent signed block produced findings: %v", got)
	}
	if got := diffSignedBlock(nil, signedFrom(proposal())); got != nil {
		t.Errorf("an absent proposal produced findings: %v", got)
	}
	// A partial block is compared on what it does carry, and silent on the rest.
	if got := diffSignedBlock(proposal(), map[string]any{"height": float64(3)}); len(got) != 0 {
		t.Errorf("a block carrying none of the compared fields produced findings: %v", got)
	}
}

// The amount arrives as a string from toJson, but a JSON number is what a
// hand-assembled payload carries, and reporting a mismatch between 1000000000
// and 1000000000 would block a swap over a type.
func TestDiffSignedBlockAmountAsNumber(t *testing.T) {
	signed := signedFrom(proposal())
	signed["amount"] = float64(1000000000)
	if got := diffSignedBlock(proposal(), signed); len(got) != 0 {
		t.Errorf("a numeric amount was reported as a mismatch: %v", got)
	}
}

func TestSameEndpoint(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"http://127.0.0.1:35997", "http://127.0.0.1:35997", true},
		{"http://127.0.0.1:35997/", "http://127.0.0.1:35997", true},
		{"HTTP://127.0.0.1:35997", "http://127.0.0.1:35997", true},
		// Almost always the same node, and deliberately not claimed to be:
		// the momentum comparison is what proves it.
		{"ws://127.0.0.1:35998", "http://127.0.0.1:35997", false},
		{"", "http://127.0.0.1:35997", false},
	} {
		if got := sameEndpoint(tc.a, tc.b); got != tc.want {
			t.Errorf("sameEndpoint(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// The participant's Zenon HTLC answers the initiator's Bitcoin funding, so it
// waits for that funding to be real: present, covering the amount, mined, and
// still unspent. Judged off the record first -- which is what the card shows --
// and then off the chain, because a record is only as current as the last poll.
func TestCounterLegFundingGate(t *testing.T) {
	const txid = "5ae77294d1bd1dea7fce8b235ae89b80585424aeaa907f181282db9ed9b9fd0a"
	// The participant receiving BTC in a Bitcoin-initiated swap: the one shape
	// that waits.
	waits := func(f *FundingOutput) *Swap {
		return &Swap{Role: RoleParticipant, Leg: LegReceive, State: StateFunded,
			AmountSats: 400_000, ContractAddr: "bcrt1qcontract", Funding: f}
	}
	mined := chain.Status{Confirmed: true, BlockHeight: 100}

	t.Run("the record", func(t *testing.T) {
		cases := []struct {
			name string
			sw   *Swap
			want string // "" means committed
		}{
			{"nothing seen", waits(nil), "not been seen"},
			{"in the mempool", waits(&FundingOutput{TxID: txid, Value: 400_000}), "mempool"},
			{"short", waits(&FundingOutput{TxID: txid, Value: 100_000, Confirmed: true, Confirmations: 3}), "400000 sat was agreed"},
			{"mined and full", waits(&FundingOutput{TxID: txid, Value: 400_000, Confirmed: true, Confirmations: 1}), ""},
		}
		type c = struct {
			name string
			sw   *Swap
			want string
		}
		full := func() *FundingOutput {
			return &FundingOutput{TxID: txid, Value: 400_000, Confirmed: true, Confirmations: 6}
		}
		spent := waits(full())
		spent.State = StateRefunded
		// Confirmed, full, and still sitting there -- but the timelock has
		// passed, so it is the counterparty's to take back the moment ZNN is
		// locked against it.
		expired := waits(full())
		expired.State = StateExpired
		expired.LockTime = time.Now().Add(-time.Hour).Unix()
		// Not expired yet, but too close: a Zenon leg has to expire before the
		// Bitcoin contract by MinLegGap and still live MinLegRemaining.
		tooClose := waits(full())
		tooClose.LockTime = time.Now().Add(MinLegGap + MinLegRemaining - time.Minute).Unix()
		roomy := waits(full())
		roomy.LockTime = time.Now().Add(MinLegGap + MinLegRemaining + time.Hour).Unix()
		cases = append(cases,
			c{"spent", spent, "already been spent"},
			c{"expired", expired, "timelock has passed"},
			c{"locktime too close", tooClose, "too close"},
			c{"locktime with room", roomy, ""},
		)
		for _, c := range cases {
			got := c.sw.FundingCommitBlocker()
			if c.want == "" && got != "" {
				t.Errorf("%s: blocked by %q, want committed", c.name, got)
			}
			if c.want != "" && !strings.Contains(got, c.want) {
				t.Errorf("%s: blocker %q does not mention %q", c.name, got, c.want)
			}
			if c.sw.FundingCommitted() != (got == "") {
				t.Errorf("%s: FundingCommitted disagrees with the blocker", c.name)
			}
		}
	})

	t.Run("the shapes that do not wait", func(t *testing.T) {
		// The Zenon leg is the initiator's: it goes first, before any Bitcoin
		// exists to wait for. And the side that funds Bitcoin never creates.
		for _, sw := range []*Swap{
			{Role: RoleInitiator, Leg: LegReceive},
			{Role: RoleInitiator, Leg: LegSend},
			{Role: RoleParticipant, Leg: LegSend},
		} {
			if sw.ZenonCreateWaitsOnBtc() {
				t.Errorf("%s/%s waits on Bitcoin funding", sw.Role, sw.Leg)
			}
			if b := sw.FundingCommitBlocker(); b != "" {
				t.Errorf("%s/%s with no funding is blocked: %q", sw.Role, sw.Leg, b)
			}
			// A chain that cannot be read must not matter here, because it is
			// never asked.
			if err := requireCounterLegFunding(context.Background(), failingBackend{}, sw); err != nil {
				t.Errorf("%s/%s consulted the chain: %v", sw.Role, sw.Leg, err)
			}
		}
	})

	t.Run("the chain, freshly", func(t *testing.T) {
		sw := waits(&FundingOutput{TxID: txid, Vout: 0, Value: 400_000, Confirmed: true, Confirmations: 2})
		cases := []struct {
			name    string
			backend chain.Backend
			want    string
		}{
			{"still there", &stubBackend{utxos: []chain.UTXO{{TxID: txid, Value: 400_000, Status: mined}}}, ""},
			{"gone", &stubBackend{utxos: nil}, "no longer unspent"},
			{"replaced by a short one", &stubBackend{utxos: []chain.UTXO{{TxID: txid, Value: 50_000, Status: mined}}}, "400000 sat was agreed"},
			{"unmined again", &stubBackend{utxos: []chain.UTXO{{TxID: txid, Value: 400_000}}}, "mempool"},
			{"unreadable", failingBackend{}, "could not re-read"},
		}
		for _, c := range cases {
			err := requireCounterLegFunding(context.Background(), c.backend, sw)
			if c.want == "" && err != nil {
				t.Errorf("%s: refused: %v", c.name, err)
			}
			if c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Errorf("%s: got %v, want a refusal mentioning %q", c.name, err, c.want)
			}
		}
		// Depth is counted against the tip the stub reports (100): a funding in
		// block 100 is one deep, so a two-deep rule refuses it and a one-deep
		// rule does not.
		fresh := &stubBackend{utxos: []chain.UTXO{{TxID: txid, Value: 400_000, Status: mined}}}
		if err := checkFundingOnChain(context.Background(), fresh, sw, 2); err == nil ||
			!strings.Contains(err.Error(), "1 of the 2 confirmations") {
			t.Errorf("a one-deep funding passed a two-deep rule: %v", err)
		}
		if err := checkFundingOnChain(context.Background(), fresh, sw, 1); err != nil {
			t.Errorf("a one-deep funding failed a one-deep rule: %v", err)
		}
		// The output is there and mined, but the tip cannot be read, so the
		// depth cannot be counted. Not counted is not enough.
		noTip := tipFails{fresh}
		if err := checkFundingOnChain(context.Background(), noTip, sw, 1); err == nil ||
			!strings.Contains(err.Error(), "chain tip") {
			t.Errorf("an unreadable tip did not refuse: %v", err)
		}
	})
}

// tipFails is a working backend whose tip cannot be read.
type tipFails struct{ *stubBackend }

func (tipFails) TipHeight(context.Context) (int64, error) { return 0, errors.New("tip unreadable") }

// The sign-time gate, through the call table the page uses: the same refusal
// planCreate makes, available on its own so it can run right before a block
// is handed over. Both answers here come without a chain -- one shape never
// asks, the other is refused on the record first.
func TestFundingCheckThroughTheAPI(t *testing.T) {
	a := &API{Store: NewStore(NewMemStorage())}
	settings := `"settings":{"network":"regtest","btcEsplora":"http://127.0.0.1:1"}`
	save := func(sw *Swap) {
		t.Helper()
		if err := a.Store.Save(sw); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	// The Zenon-initiated ordering: this side's HTLC goes first, and there is
	// no Bitcoin to wait for.
	first := &Swap{ID: "aabbccddeeff0055", Network: "regtest", Role: RoleInitiator, Leg: LegReceive,
		State: StateDraft, Key: mustKey(t), AmountSats: 400_000}
	save(first)
	var okResp struct {
		OK    bool   `json:"ok"`
		Waits bool   `json:"waits"`
		Error string `json:"error"`
	}
	raw := a.Call("fundingCheck", []byte(`{"id":"`+first.ID+`",`+settings+`}`))
	if err := json.Unmarshal(raw, &okResp); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if !okResp.OK || okResp.Waits || okResp.Error != "" {
		t.Errorf("the Zenon-initiated shape did not pass cleanly: %s", raw)
	}

	// The Bitcoin-initiated participant with nothing funded: refused, and
	// refused before any chain is consulted (the Esplora above is a dead port).
	waiting := &Swap{ID: "aabbccddeeff0066", Network: "regtest", Role: RoleParticipant, Leg: LegReceive,
		State: StateAwaitingFunding, Key: mustKey(t), AmountSats: 400_000}
	save(waiting)
	raw = a.Call("fundingCheck", []byte(`{"id":"`+waiting.ID+`",`+settings+`}`))
	var refusal struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &refusal); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if !strings.Contains(refusal.Error, "not locking ZNN yet") ||
		!strings.Contains(refusal.Error, "not been seen") {
		t.Errorf("the waiting shape was not refused on the record: %s", raw)
	}

	// The waiting shape with funding that IS settled: the record passes, and
	// the chain is asked. Served by a stub Esplora, so this exercises the real
	// client, the real URL building, and the real JSON -- the whole path the
	// page's sign-time call takes -- rather than the helper on its own.
	esplora := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/utxo"):
			fmt.Fprintf(w, `[{"txid":%q,"vout":0,"value":400000,"status":{"confirmed":true,"block_height":100}}]`,
				"5ae77294d1bd1dea7fce8b235ae89b80585424aeaa907f181282db9ed9b9fd0a")
		case r.URL.Path == "/blocks/tip/height":
			fmt.Fprint(w, "100")
		default:
			http.NotFound(w, r)
		}
	}))
	defer esplora.Close()
	settled := &Swap{ID: "aabbccddeeff0077", Network: "regtest", Role: RoleParticipant, Leg: LegReceive,
		State: StateFunded, Key: mustKey(t), AmountSats: 400_000, ContractAddr: "bcrt1qcontract",
		LockTime: time.Now().Add(48 * time.Hour).Unix(),
		Funding: &FundingOutput{TxID: "5ae77294d1bd1dea7fce8b235ae89b80585424aeaa907f181282db9ed9b9fd0a",
			Value: 400_000, Confirmed: true, Confirmations: 1}}
	save(settled)
	live := `"settings":{"network":"regtest","btcEsplora":"` + esplora.URL + `"}`
	raw = a.Call("fundingCheck", []byte(`{"id":"`+settled.ID+`",`+live+`}`))
	if err := json.Unmarshal(raw, &okResp); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if !okResp.OK || !okResp.Waits || okResp.Error != "" {
		t.Errorf("settled funding did not pass the live check: %s", raw)
	}
	// The same swap, but the chain now says the output is gone.
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/utxo") {
			fmt.Fprint(w, "[]")
			return
		}
		fmt.Fprint(w, "100")
	}))
	defer gone.Close()
	raw = a.Call("fundingCheck", []byte(`{"id":"`+settled.ID+`","settings":{"network":"regtest","btcEsplora":"`+gone.URL+`"}}`))
	if !strings.Contains(string(raw), "no longer unspent") {
		t.Errorf("a vanished output passed the live check: %s", raw)
	}

	// A stray field is refused like everywhere else on this boundary.
	raw = a.Call("fundingCheck", []byte(`{"id":"`+first.ID+`","force":true,`+settings+`}`))
	if !strings.Contains(string(raw), "error") {
		t.Errorf("an unknown field was accepted: %s", raw)
	}
}

// failingBackend answers every question with an error, for the checks that
// must refuse on a chain they cannot read -- and the shapes that must never
// ask.
type failingBackend struct{}

func (failingBackend) TipHeight(context.Context) (int64, error) {
	return 0, errors.New("node down")
}
func (failingBackend) AddressUTXOs(context.Context, string) ([]chain.UTXO, error) {
	return nil, errors.New("node down")
}
func (failingBackend) TxStatus(context.Context, string) (chain.Status, error) {
	return chain.Status{}, errors.New("node down")
}
func (failingBackend) RawTx(context.Context, string) (string, error) {
	return "", errors.New("node down")
}
func (failingBackend) OutspendOf(context.Context, string, uint32) (*chain.Outspend, error) {
	return nil, errors.New("node down")
}
func (failingBackend) FeeRate(context.Context, int) (float64, error) {
	return 0, errors.New("node down")
}
func (failingBackend) BlockHashAt(context.Context, int64) (string, error) {
	return "", errors.New("node down")
}
func (failingBackend) Broadcast(context.Context, string) (string, error) {
	return "", errors.New("node down")
}
func (failingBackend) Name() string { return "failing" }

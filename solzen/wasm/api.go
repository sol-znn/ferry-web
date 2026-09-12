package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zenon/solzen/wasm/sol"
	"github.com/zenon/solzen/wasm/znn"
)

// The call table the page talks to.
//
// One entry point, one request shape, one response shape. It is deliberately
// not a set of exported functions: the JavaScript side has to be able to call
// anything without the Go side exporting a binding per method, and every call
// crosses the same boundary with the same error handling.

// Request is one call.
type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// Response is what comes back. Exactly one of Result and Error is set.
type Response struct {
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// API holds the manager and dispatches.
type API struct {
	m *Manager
}

func NewAPI(m *Manager) *API { return &API{m: m} }

// Handle runs one request.
//
// Unknown methods and unknown parameter fields are refused rather than ignored.
// A page calling a method this build does not have, or passing a field it
// renamed, is a page whose expectations have drifted from the module's -- and
// silently doing four fifths of what was asked is how that drift stays hidden
// until it matters.
func (a *API) Handle(ctx context.Context, raw string, progress znn.Progress) Response {
	var req Request
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return Response{Error: fmt.Sprintf("the request does not decode: %v", err)}
	}
	result, err := a.dispatch(ctx, req, progress)
	if err != nil {
		return Response{Error: err.Error()}
	}
	return Response{Result: result}
}

func (a *API) dispatch(ctx context.Context, req Request, progress znn.Progress) (any, error) {
	unmarshal := func(dst any) error {
		if len(req.Params) == 0 {
			return nil
		}
		d := json.NewDecoder(strings.NewReader(string(req.Params)))
		d.DisallowUnknownFields()
		if err := d.Decode(dst); err != nil {
			return fmt.Errorf("%s: %v", req.Method, err)
		}
		return nil
	}

	switch req.Method {
	case "env":
		return map[string]any{
			"build":         BuildName,
			"defaultConfig": DefaultConfig(),
			"limits": map[string]any{
				"minLegGapSeconds":    MinLegGapSeconds,
				"minRemainingSeconds": MinRemainingSeconds,
				"maxClockSkewSeconds": MaxClockSkewSeconds,
				"defaultLongSeconds":  DefaultLongSeconds,
				"defaultShortSeconds": DefaultShortSeconds,
			},
		}, nil

	case "config.get":
		return a.m.Config(), nil

	case "config.set":
		var c Config
		if err := unmarshal(&c); err != nil {
			return nil, err
		}
		a.m.SetConfig(c)
		if err := a.m.store.SaveConfig(c); err != nil {
			return nil, err
		}
		return c, nil

	case "config.check":
		return a.checkNodes(ctx)

	case "swap.create":
		var p CreateParams
		if err := unmarshal(&p); err != nil {
			return nil, err
		}
		s, err := a.m.Create(ctx, p)
		if err != nil {
			return nil, err
		}
		return a.withOffer(s)

	case "swap.accept":
		var p AcceptParams
		if err := unmarshal(&p); err != nil {
			return nil, err
		}
		s, err := a.m.Accept(ctx, p)
		if err != nil {
			return nil, err
		}
		return a.withOffer(s)

	case "swap.applyAccept":
		var p struct {
			ID     string `json:"id"`
			Accept string `json:"accept"`
		}
		if err := unmarshal(&p); err != nil {
			return nil, err
		}
		s, err := a.m.ApplyAccept(ctx, p.ID, p.Accept)
		if err != nil {
			return nil, err
		}
		return a.withOffer(s)

	case "swap.preview":
		// Decode a pasted string without creating anything, so a counterparty's
		// offer can be read before it is agreed to.
		var p struct {
			Text string `json:"text"`
		}
		if err := unmarshal(&p); err != nil {
			return nil, err
		}
		kind, terms, err := DecodeEnvelope(p.Text)
		if err != nil {
			return nil, err
		}
		return map[string]any{"kind": kind, "terms": terms}, nil

	case "swap.list":
		out := []any{}
		for _, s := range a.m.store.List() {
			v, err := a.withOffer(s)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil

	case "swap.get":
		var p struct {
			ID string `json:"id"`
		}
		if err := unmarshal(&p); err != nil {
			return nil, err
		}
		s, err := a.m.store.Get(p.ID)
		if err != nil {
			return nil, err
		}
		return a.withOffer(s)

	case "swap.delete":
		var p struct {
			ID string `json:"id"`
		}
		if err := unmarshal(&p); err != nil {
			return nil, err
		}
		return nil, a.m.store.Delete(p.ID)

	case "swap.refresh":
		var p struct {
			ID string `json:"id"`
		}
		if err := unmarshal(&p); err != nil {
			return nil, err
		}
		s, st, err := a.m.Refresh(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		v, err := a.withOffer(s)
		if err != nil {
			return nil, err
		}
		return map[string]any{"swap": v, "status": st}, nil

	case "swap.setHtlcId":
		// The fallback for when the contract scan cannot reach far enough back:
		// the counterparty pastes the id.
		var p struct {
			ID     string `json:"id"`
			HtlcID string `json:"htlcId"`
		}
		if err := unmarshal(&p); err != nil {
			return nil, err
		}
		s, err := a.m.store.Get(p.ID)
		if err != nil {
			return nil, err
		}
		h, err := znn.ParseHashHex(p.HtlcID)
		if err != nil {
			return nil, fmt.Errorf("that is not an HTLC id: %w", err)
		}
		s.HtlcID = h.String()
		if err := a.m.store.Put(s); err != nil {
			return nil, err
		}
		return a.withOffer(s)

	case "sol.instruction":
		var p struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		}
		if err := unmarshal(&p); err != nil {
			return nil, err
		}
		return a.m.SolanaInstruction(ctx, p.ID, p.Kind)

	case "sol.recordTx":
		var p struct {
			ID        string `json:"id"`
			Kind      string `json:"kind"`
			Signature string `json:"signature"`
		}
		if err := unmarshal(&p); err != nil {
			return nil, err
		}
		return nil, a.m.RecordSolanaTx(p.ID, p.Kind, p.Signature)

	case "znn.cost":
		var p struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		}
		if err := unmarshal(&p); err != nil {
			return nil, err
		}
		return a.m.ZenonCostOf(ctx, p.ID, p.Kind)

	case "znn.act":
		var p struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		}
		if err := unmarshal(&p); err != nil {
			return nil, err
		}
		return a.m.ZenonAct(ctx, p.ID, p.Kind, progress)

	case "store.export":
		return a.m.store.Export(), nil

	case "store.import":
		var p struct {
			Text string `json:"text"`
		}
		if err := unmarshal(&p); err != nil {
			return nil, err
		}
		imported, skipped, refused, err := a.m.store.Import(p.Text)
		if err != nil {
			return nil, err
		}
		// refused is returned rather than folded into skipped: a record left
		// alone as already-current and a record this build will not accept are
		// opposite pieces of news, and a swap that did not come back is a
		// contract this browser can no longer act on.
		return map[string]any{
			"imported": imported,
			"skipped":  skipped,
			"refused":  refused,
		}, nil
	}
	return nil, fmt.Errorf("unknown method %q", req.Method)
}

// withOffer decorates a swap with the strings the user has to copy out of it,
// and with the swap address they may have to pay. Computing them here keeps the
// page from re-deriving anything that has a right answer in Go.
func (a *API) withOffer(s *Swap) (map[string]any, error) {
	out := map[string]any{"swap": s}
	if s.Role.Initiator {
		offer, err := EncodeOffer(s.Terms)
		if err != nil {
			return nil, err
		}
		out["offer"] = offer
	} else {
		accept, err := EncodeAccept(s.Terms)
		if err != nil {
			return nil, err
		}
		out["accept"] = accept
	}
	if s.ZnnSwapSeed != "" {
		key, err := znn.KeyFromSeedHex(s.ZnnSwapSeed)
		if err != nil {
			return nil, err
		}
		out["znnSwapAddress"] = key.Address.String()
	}
	if programID, err := sol.ParsePubkey(s.Terms.SolProgram); err == nil {
		if id32, err := hexBytes32(s.Terms.SwapID); err == nil {
			if addr, _, err := sol.EscrowAddress(programID, id32); err == nil {
				out["solEscrowAddress"] = addr.String()
			}
		}
	}
	return out, nil
}

// checkNodes probes both endpoints and reports what each one is, so a
// misconfiguration is found before it is found by a swap.
func (a *API) checkNodes(ctx context.Context) (any, error) {
	out := map[string]any{}

	if v, err := a.m.sol().Version(ctx); err != nil {
		out["solana"] = map[string]any{"ok": false, "error": err.Error()}
	} else {
		now, _ := a.m.sol().Now(ctx)
		res := map[string]any{"ok": true, "version": v, "now": now}
		if genesis, gerr := a.m.sol().GenesisHash(ctx); gerr == nil {
			res["genesis"] = genesis
		}
		if programID, err := sol.ParsePubkey(a.m.cfg.SolProgram); err != nil {
			res["program"] = map[string]any{"ok": false, "error": err.Error()}
		} else if info, err := a.m.sol().Program(ctx, programID); err != nil {
			res["program"] = map[string]any{"ok": false, "error": fmt.Sprintf("%s: %v", programID, err)}
		} else {
			// An account that is not executable is not a program. Saying so
			// here is cheaper than a transaction that fails with a runtime
			// error naming an address nobody recognises -- and it is now
			// actually checked, which the comment that used to sit here
			// claimed and the code did not do.
			//
			// Upgradeability is reported beside it because it is the sharper
			// question and the one this project cannot answer from its source:
			// an upgradeable program's code, and so the terms of every escrow
			// already funded under it, can be replaced by whoever holds the
			// authority named here.
			res["program"] = map[string]any{
				"ok":          info.Executable && info.Problem == "",
				"address":     info.Address,
				"owner":       info.Owner,
				"executable":  info.Executable,
				"upgradeable": info.Upgradeable,
				"authority":   info.Authority,
				"problem":     info.Problem,
			}
		}
		out["solana"] = res
	}

	if mom, err := a.m.znn().FrontierMomentum(ctx); err != nil {
		out["zenon"] = map[string]any{"ok": false, "error": err.Error()}
	} else {
		out["zenon"] = map[string]any{
			"ok": true, "height": mom.Height, "now": mom.Timestamp,
			"chainIdentifier": mom.ChainIdentifier,
		}
	}
	return out, nil
}

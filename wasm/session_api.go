package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// The session handlers.
//
// Deliberately thin. Nothing here decides anything about a swap: the module's
// job in a session is to sign, encrypt, decrypt and verify the envelope, then
// hand the contents to the handler that already knows how to check that kind of
// value -- a contract to handleAudit, a pubkey hash to handleCounterparty. Those
// refuse what does not match, exactly as they did when a human pasted it in.
//
// The relay connection itself is not here. It lives in TypeScript, because a
// WebSocket to a relay is a WebSocket, and the module has no reason to grow one
// -- everything crossing that socket is already sealed by the time it gets
// there and is opened again on the way back.

// handleSessionNew mints a code. It is the only call that needs entropy, and it
// touches no swap: a code exists before it has anything to say.
func handleSessionNew(_ context.Context, _ *API, body []byte) (any, error) {
	var req struct {
		// Code, when given, re-derives an existing session instead of minting
		// one -- which is what the joining side does.
		Code string `json:"code,omitempty"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	code := req.Code
	if strings.TrimSpace(code) == "" {
		fresh, err := NewSessionCode()
		if err != nil {
			return nil, err
		}
		code = fresh
	}
	keys, _, err := DeriveSession(code)
	if err != nil {
		return nil, err
	}
	// The grouped form is what the user reads out; the normalised form is what
	// everything else uses. Both are returned so the UI never has to re-derive
	// one from the other and get the grouping wrong.
	return map[string]any{
		"code":    keys.Code,
		"display": groupCode(keys.Code),
		"pubKey":  keys.PubKey,
		"roomId":  keys.RoomID,
		"kind":    sessionKind,
	}, nil
}

// groupCode puts a normalised code back into readable groups of eight.
func groupCode(code string) string {
	var parts []string
	for i := 0; i < len(code); i += 8 {
		end := min(i+8, len(code))
		parts = append(parts, code[i:end])
	}
	return strings.Join(parts, "-")
}

// handleSessionSend seals one message into a signed event ready to publish.
func handleSessionSend(_ context.Context, a *API, body []byte) (any, error) {
	var req struct {
		Code   string         `json:"code"`
		SwapID string         `json:"id,omitempty"`
		Msg    SessionMessage `json:"message"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	if req.Msg.Type == "" {
		return nil, errors.New("a session message needs a type")
	}
	// A preimage has no field on SessionMessage and no path to one, but the
	// refusal is stated here as well, next to the call that would publish it.
	// The cost of this check being redundant is a line; the cost of it being
	// missing one day is somebody's swap.
	if req.SwapID != "" {
		sw, err := a.Store.Load(req.SwapID)
		if err != nil {
			return nil, err
		}
		if err := fillFromSwap(&req.Msg, sw); err != nil {
			return nil, err
		}
	}
	ev, err := SealSession(req.Code, req.Msg)
	if err != nil {
		return nil, err
	}
	return map[string]any{"event": ev}, nil
}

// fillFromSwap completes a message from the swap it is about, so the UI asks
// for "send the contract" rather than assembling one.
//
// Reading the values out of the stored swap rather than off the page is what
// makes a session message describe the swap this browser actually holds. A
// field the UI filled in could be a field the UI filled in wrongly.
func fillFromSwap(msg *SessionMessage, sw *Swap) error {
	if sw.Role == RoleInitiator {
		msg.From = string(RoleInitiator)
	} else {
		msg.From = string(RoleParticipant)
	}
	switch msg.Type {
	case "pkh":
		if sw.Key == nil {
			return errors.New("this swap has no key yet")
		}
		msg.PkhHex = hex.EncodeToString(sw.Key.PKH)
	case "contract":
		if len(sw.Contract) == 0 {
			return errors.New("this swap has no contract yet — build it first")
		}
		msg.ContractHex = hex.EncodeToString(sw.Contract)
	case "zenon":
		if sw.Zenon.HtlcID == "" {
			return errors.New("this swap has no Zenon HTLC id yet")
		}
		msg.HtlcID = sw.Zenon.HtlcID
		msg.ZenonAddr = sw.Zenon.SelfAddress
	case "funded":
		if sw.Funding == nil {
			return errors.New("this swap is not funded yet")
		}
		msg.FundingTxID = sw.Funding.TxID
	case "hello", "ping", "bye", "offer", "note", "resync", "moved":
		// All of these are assembled by the caller: an offer comes from
		// handleOffer, a note is whatever the user typed, and hello/ping/bye
		// carry nothing about the swap at all -- they are presence, not swap
		// data.
		//
		// A resync carries nothing either, and that is the whole of it: it is a
		// request, not a value. "Send me everything you hold about this swap
		// again" needs no argument, and giving it one would make it a second
		// path by which a swap value could arrive -- one that skipped the
		// handler that checks that kind of value. The answer comes back as the
		// ordinary hand-offs, through the ordinary checks.
		//
		// So does a moved, for the same reason and to better effect. It says "I
		// have just done something to a chain" and carries no claim about what:
		// the other side goes and reads the chain. That is what makes it safe to
		// send after an unlock, which is the one action whose result -- the
		// preimage -- must never travel in a message. The announcement makes the
		// counterparty look a minute sooner; the chain is still what tells them.
	default:
		return fmt.Errorf("unknown session message type %q", msg.Type)
	}
	return nil
}

// handleSessionOpen verifies and decrypts one event off a relay.
//
// It returns the message rather than acting on it. Acting on it is a separate,
// explicit call to the handler for that kind of value, because those are the
// calls that check things — and because a message arriving should never change
// a swap without the change being something the user can see happen.
func handleSessionOpen(_ context.Context, _ *API, body []byte) (any, error) {
	var req struct {
		Code  string      `json:"code"`
		Event *NostrEvent `json:"event"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	msg, err := OpenSession(req.Code, req.Event)
	if err != nil {
		return nil, err
	}
	return map[string]any{"message": msg, "eventId": req.Event.ID}, nil
}

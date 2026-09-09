package main

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestSessionRoundTrip(t *testing.T) {
	code, err := NewSessionCode()
	if err != nil {
		t.Fatal(err)
	}

	sent := SessionMessage{
		Type:        "contract",
		From:        "initiator",
		ContractHex: stuckContractHex,
		Note:        "audit this <before> you send anything & tell me",
	}
	ev, err := SealSession(code, sent)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	got, err := OpenSession(code, ev)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got.ContractHex != sent.ContractHex || got.Note != sent.Note {
		t.Errorf("message did not survive the round trip: %+v", got)
	}
	if got.SentAt == 0 {
		t.Error("SealSession should stamp a message that arrives without one")
	}
	// The relay sees ciphertext, not a contract.
	if strings.Contains(ev.Content, stuckContractHex[:16]) {
		t.Error("the contract hex is readable in the published event content")
	}
}

// A code typed by a person is not the string that was generated: they lower-case
// it, they lose the dashes, they add a space. All of those have to open the same
// room, because the alternative is a feature that fails for reasons the user
// cannot see.
func TestSessionCodeIsForgivingAboutFormatting(t *testing.T) {
	code, err := NewSessionCode()
	if err != nil {
		t.Fatal(err)
	}
	ev, err := SealSession(code, SessionMessage{Type: "pkh", PkhHex: "aa"})
	if err != nil {
		t.Fatal(err)
	}

	mangled := " " + strings.ToLower(strings.ReplaceAll(code, "-", " ")) + "\n"
	if _, err := OpenSession(mangled, ev); err != nil {
		t.Errorf("a code retyped as %q would not open its own room: %v", mangled, err)
	}
}

// The room is the code. A different code must not open it, and must not say
// something that sounds like a network problem when it fails.
func TestSessionRefusesAnotherCode(t *testing.T) {
	mine, err := NewSessionCode()
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := NewSessionCode()
	if err != nil {
		t.Fatal(err)
	}
	ev, err := SealSession(mine, SessionMessage{Type: "pkh", PkhHex: "aa"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = OpenSession(theirs, ev)
	if err == nil {
		t.Fatal("a different code opened the room")
	}
	if !strings.Contains(err.Error(), "different key") && !strings.Contains(err.Error(), "code") {
		t.Errorf("the refusal does not point at the code: %v", err)
	}
}

// A relay is an untrusted intermediary that stores what it is given. Both the
// signature and the AEAD exist to make a modified message fail to open rather
// than open into something else — and the something else would be a contract.
func TestSessionRefusesTamperedEvents(t *testing.T) {
	code, err := NewSessionCode()
	if err != nil {
		t.Fatal(err)
	}
	ev, err := SealSession(code, SessionMessage{Type: "contract", ContractHex: stuckContractHex})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("content", func(t *testing.T) {
		bad := *ev
		raw := []byte(bad.Content)
		raw[len(raw)-6] ^= 0x01
		bad.Content = string(raw)
		if _, err := OpenSession(code, &bad); err == nil {
			t.Error("a modified ciphertext was accepted")
		}
	})

	t.Run("id", func(t *testing.T) {
		bad := *ev
		bad.ID = strings.Repeat("0", 64)
		if _, err := OpenSession(code, &bad); err == nil {
			t.Error("an event whose id does not match its contents was accepted")
		}
	})

	t.Run("signature", func(t *testing.T) {
		bad := *ev
		raw, derr := hex.DecodeString(bad.Sig)
		if derr != nil {
			t.Fatal(derr)
		}
		raw[10] ^= 0xff
		bad.Sig = hex.EncodeToString(raw)
		if _, err := OpenSession(code, &bad); err == nil {
			t.Error("a bad signature was accepted")
		}
	})

	t.Run("kind", func(t *testing.T) {
		bad := *ev
		bad.Kind = 1
		if _, err := OpenSession(code, &bad); err == nil {
			t.Error("an event of another kind was accepted")
		}
	})
}

// The event id is consensus: every relay recomputes it and drops an event whose
// id does not match. encoding/json escapes <, > and & by default and no other
// implementation of this protocol does, so a message containing one of those
// would be silently unpublishable. This is the test that catches it.
func TestEventIDUsesCanonicalJSON(t *testing.T) {
	ev := &NostrEvent{
		PubKey:    strings.Repeat("ab", 32),
		CreatedAt: 1788812469,
		Kind:      sessionKind,
		Tags:      [][]string{{"d", "deadbeef"}},
		Content:   "a<b>c&d",
	}
	id, err := ev.eventID()
	if err != nil {
		t.Fatal(err)
	}

	// Hash the serialization the protocol specifies, built here without any
	// help from the code under test.
	tags, _ := json.Marshal(ev.Tags)
	want := sha256Hex([]byte(
		`[0,"` + ev.PubKey + `",1788812469,` + itoa(ev.Kind) + `,` + string(tags) + `,"a<b>c&d"]`))
	if hex.EncodeToString(id[:]) != want {
		t.Errorf("event id is %s, want %s — the serialization is not canonical",
			hex.EncodeToString(id[:]), want)
	}
}

func sha256Hex(b []byte) string {
	h := SHA256(b)
	return hex.EncodeToString(h)
}

func itoa(n int) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

// A code short enough to guess is short enough to refuse: it would open a room
// a third party could stumble into while a contract was being sent through it.
func TestSessionRefusesShortCodes(t *testing.T) {
	for _, code := range []string{"", "ABC", "ABCD-EFGH"} {
		if _, _, err := DeriveSession(code); err == nil {
			t.Errorf("code %q was accepted", code)
		}
	}
}

// A resync asks; it does not tell.
//
// The message that says "send me everything about this swap again" is the one
// place a request crosses the wire rather than a value, and the whole safety of
// answering it rests on the answer coming back as ordinary hand-offs through
// the handlers that check them. If a resync could itself carry a contract or an
// HTLC id, it would be a second way for a swap value to arrive -- one nothing
// audits -- so fillFromSwap must leave it empty even when it is told exactly
// which swap it is about and that swap has every value to hand.
func TestResyncCarriesNoSwapValues(t *testing.T) {
	sw := &Swap{
		ID: "00112233445566bb", Network: "regtest", Role: RoleInitiator, Leg: LegSend,
		Contract: []byte{0xDE, 0xAD, 0xBE, 0xEF},
		Funding:  &FundingOutput{TxID: "f00d", Vout: 1, Value: 400_000},
		Key:      mustKey(t),
	}
	sw.Zenon.HtlcID = strings.Repeat("ab", 32)
	sw.Zenon.SelfAddress = "z1qqvwzz2xq7q5gwk6uhcddgrpxlfcyzc8rsu82s"

	msg := SessionMessage{Type: "resync"}
	if err := fillFromSwap(&msg, sw); err != nil {
		t.Fatalf("a resync about a fully populated swap was refused: %v", err)
	}
	if msg.ContractHex != "" || msg.PkhHex != "" || msg.HtlcID != "" ||
		msg.ZenonAddr != "" || msg.FundingTxID != "" || msg.Offer != "" {
		t.Errorf("a resync came out carrying swap data: %+v", msg)
	}
	// The sender's role still rides along, as it does on every message -- it is
	// how the receiving side drops its own traffic coming back off a relay.
	if msg.From != string(RoleInitiator) {
		t.Errorf("From = %q, want %q", msg.From, RoleInitiator)
	}
}

// An unknown type is still refused. The resync case was added to a list that is
// deliberately exhaustive, and the value of that list is entirely in its
// default branch.
func TestUnknownSessionTypeIsRefused(t *testing.T) {
	msg := SessionMessage{Type: "preimage"}
	if err := fillFromSwap(&msg, &Swap{ID: "00112233445566cc", Role: RoleParticipant}); err == nil {
		t.Fatal("an unknown session message type was accepted")
	}
}

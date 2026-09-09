package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A backup is a file off the user's disk, and an unchecked record from one
// reaches every code path a locally created swap does. handleOffer and
// handleRecovery dereference Key without asking, so a record without one used
// to be a nil dereference inside the WebAssembly module.
func TestImportRejectsRecordsThatCouldNotHaveComeFromHere(t *testing.T) {
	good := func() *Swap {
		key, err := NewSwapKey()
		if err != nil {
			t.Fatalf("NewSwapKey: %v", err)
		}
		_, hash, err := NewSecret()
		if err != nil {
			t.Fatalf("NewSecret: %v", err)
		}
		return &Swap{
			ID: "00112233445566aa", CreatedAt: time.Now().UTC(),
			Network: "regtest", Role: RoleInitiator, Leg: LegSend,
			State: StateDraft, Key: key, SecretHash: hash, AmountSats: 400_000,
		}
	}

	cases := map[string]func(*Swap){
		"no key":              func(s *Swap) { s.Key = nil },
		"unknown network":     func(s *Swap) { s.Network = "dogecoin" },
		"id that is not hex":  func(s *Swap) { s.ID = "../../etc/passwd" },
		"empty id":            func(s *Swap) { s.ID = "" },
		"truncated priv":      func(s *Swap) { s.Key.Priv = s.Key.Priv[:16] },
		"pub for another key": func(s *Swap) { other, _ := NewSwapKey(); s.Key.Pub = other.Pub },
		"pkh for another key": func(s *Swap) { other, _ := NewSwapKey(); s.Key.PKH = other.PKH },
		"secret that does not hash to its own hash": func(s *Swap) {
			other, _, _ := NewSecret()
			s.Secret = other
		},
		"contract that is not the swap template": func(s *Swap) { s.Contract = []byte{0x51, 0x52} },
	}

	for name, break_ := range cases {
		sw := good()
		break_(sw)
		doc, err := json.Marshal(map[string]any{"swaps": []*Swap{sw}})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		store := NewStore(NewMemStorage())
		added, _, rejected, ierr := store.Import(doc)
		if added != 0 || rejected != 1 {
			t.Errorf("%s: added=%d rejected=%d, want 0 and 1 (err %v)", name, added, rejected, ierr)
		}
		if ierr == nil {
			t.Errorf("%s: a file with nothing importable in it returned no error", name)
		}
	}

	// A healthy record still goes in, and one bad record beside it does not
	// take the good one down with it.
	sw, bad := good(), good()
	bad.ID = "aabbccddeeff0011"
	bad.Key = nil
	doc, err := json.Marshal(map[string]any{"swaps": []*Swap{sw, bad}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	store := NewStore(NewMemStorage())
	added, skipped, rejected, ierr := store.Import(doc)
	if ierr != nil {
		t.Fatalf("import: %v", ierr)
	}
	if added != 1 || skipped != 0 || rejected != 1 {
		t.Errorf("added=%d skipped=%d rejected=%d, want 1/0/1", added, skipped, rejected)
	}
	if _, lerr := store.Load(sw.ID); lerr != nil {
		t.Errorf("the healthy record was not stored: %v", lerr)
	}
}

// Every field of an offer is a stranger's input, and the two values derived
// from it -- which leg and which role the receiver takes -- are what the whole
// timelock ordering hangs off. Leg.Opposite maps anything it does not recognise
// to "send", so an unvalidated btcLeg tells the receiver to take the wrong side.
func TestDecodeOfferValidatesEveryField(t *testing.T) {
	good := Offer{
		Version: 1, Network: "regtest", FromRole: RoleInitiator,
		SecretHash: strings.Repeat("ab", 32), PKH: strings.Repeat("cd", 20),
		BTCLeg: LegSend, AmountSats: 400_000,
	}
	enc, err := good.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if _, err := DecodeOffer(enc); err != nil {
		t.Fatalf("rejected a well-formed offer: %v", err)
	}

	for name, break_ := range map[string]func(*Offer){
		"short secret hash": func(o *Offer) { o.SecretHash = strings.Repeat("ab", 16) },
		"short pkh":         func(o *Offer) { o.PKH = strings.Repeat("cd", 10) },
		"unknown network":   func(o *Offer) { o.Network = "dogecoin" },
		"nonsense leg":      func(o *Offer) { o.BTCLeg = Leg("sideways") },
		"nonsense role":     func(o *Offer) { o.FromRole = Role("bystander") },
		"zero amount":       func(o *Offer) { o.AmountSats = 0 },
		"negative amount":   func(o *Offer) { o.AmountSats = -1 },
	} {
		o := good
		break_(&o)
		enc, err := o.Encode()
		if err != nil {
			t.Fatalf("%s: Encode: %v", name, err)
		}
		if _, err := DecodeOffer(enc); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

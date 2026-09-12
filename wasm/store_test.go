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
			Network: "regtest", Role: RoleInitiator,
			State: StateDraft, SecretHash: hash,
			Out: &Leg{Chain: ChainBTC, Dir: DirOut, Amount: "400000", Base: "400000",
				SelfAddr: regtestDest, Btc: &BtcLeg{Key: key}},
			In: &Leg{Chain: ChainZNN, Dir: DirIn, Amount: "10", Znn: &ZnnLeg{}},
		}
	}

	cases := map[string]func(*Swap){
		"no key":              func(s *Swap) { s.Out.Btc.Key = nil },
		"unknown network":     func(s *Swap) { s.Network = "dogecoin" },
		"id that is not hex":  func(s *Swap) { s.ID = "../../etc/passwd" },
		"empty id":            func(s *Swap) { s.ID = "" },
		"truncated priv":      func(s *Swap) { s.Out.Btc.Key.Priv = s.Out.Btc.Key.Priv[:16] },
		"pub for another key": func(s *Swap) { o, _ := NewSwapKey(); s.Out.Btc.Key.Pub = o.Pub },
		"pkh for another key": func(s *Swap) { o, _ := NewSwapKey(); s.Out.Btc.Key.PKH = o.PKH },
		"secret that does not hash to its own hash": func(s *Swap) {
			other, _, _ := NewSecret()
			s.Secret = other
		},
		"contract that is not the swap template": func(s *Swap) {
			s.Out.Btc.Contract = []byte{0x51, 0x52}
		},
		// A record with one leg does not describe a trade, and every later code
		// path assumes there are two.
		"only one leg": func(s *Swap) { s.In = nil },
		// A leg on a chain against itself: both halves verify, and nothing is
		// being traded.
		"a pair that is not a trade": func(s *Swap) {
			s.In = &Leg{Chain: ChainBTC, Dir: DirIn, Amount: "1", Btc: &BtcLeg{Key: mustKey(t)}}
		},
		// Directions that disagree with the slots they are in would invert
		// every ordering rule that reads them.
		"directions swapped": func(s *Swap) { s.Out.Dir, s.In.Dir = DirIn, DirOut },
		// A Solana leg whose seed is not 32 bytes derives no escrow at all.
		"solana leg with a broken seed": func(s *Swap) {
			s.Out = &Leg{Chain: ChainSOL, Dir: DirOut, Amount: "1", SelfAddr: solSelfAddr,
				Sol: &SolLeg{ProgramID: solProgramID, SwapID: "nothex"}}
		},
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
	bad.Out.Btc.Key = nil
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

// Every field of an offer is a stranger's input, and the values derived from it
// -- which chain each half of the trade settles on, and which role the receiver
// takes -- are what the whole timelock ordering and the funding hang off.
func TestDecodeOfferValidatesEveryField(t *testing.T) {
	good := Offer{
		Version: 2, Network: "regtest", FromRole: RoleInitiator,
		SecretHash: strings.Repeat("ab", 32),
		Give:       OfferLeg{Chain: ChainBTC, Amount: "400000", PKH: strings.Repeat("cd", 20)},
		Take:       OfferLeg{Chain: ChainZNN, Amount: "10", Addr: znnSelfAddr},
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
		"short pkh":         func(o *Offer) { o.Give.PKH = strings.Repeat("cd", 10) },
		"unknown network":   func(o *Offer) { o.Network = "dogecoin" },
		"nonsense chain":    func(o *Offer) { o.Give.Chain = ChainID("sideways") },
		"nonsense role":     func(o *Offer) { o.FromRole = Role("bystander") },
		"zero amount":       func(o *Offer) { o.Give.Amount = "0" },
		"negative amount":   func(o *Offer) { o.Give.Amount = "-1" },
		"blank amount":      func(o *Offer) { o.Take.Amount = "" },
		"a pair that is not a trade": func(o *Offer) {
			o.Take = OfferLeg{Chain: ChainBTC, Amount: "1"}
		},
		"one token against itself": func(o *Offer) {
			o.Give = OfferLeg{Chain: ChainZNN, Amount: "1"}
			o.Take = OfferLeg{Chain: ChainZNN, Amount: "2"}
		},
		"a Zenon address in the Solana slot": func(o *Offer) {
			o.Give = OfferLeg{Chain: ChainSOL, Amount: "1", Addr: znnSelfAddr,
				Program: solProgramID, SwapID: solSwapIDHex}
		},
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

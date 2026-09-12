package main

import (
	"strings"
	"testing"
)

// btcOutLeg is a Bitcoin leg this user funded, in whatever state the test needs.
func btcOutLeg(addr string, funding *FundingOutput, claimed, reclaimed bool) *Leg {
	return &Leg{
		Chain: ChainBTC, Dir: DirOut, Amount: "400000", Base: "400000",
		Btc: &BtcLeg{
			ContractAddr: addr, Funding: funding, Claimed: claimed, Reclaimed: reclaimed,
		},
	}
}

func znnInLeg(done bool) *Leg {
	l := &Leg{Chain: ChainZNN, Dir: DirIn, Amount: "10", Znn: &ZnnLeg{}}
	if done {
		l.Znn.HtlcID = "aa"
		l.Znn.UnlockHash = "bb"
	}
	return l
}

// Deleting destroys the only key that can spend a swap's Bitcoin contract, and
// the record of every leg's deadline. So the question this answers is not "is
// this swap finished" but "can this record still be worth something".
func TestDeletionRisk(t *testing.T) {
	const addr = "bcrt1qx"
	cases := map[string]struct {
		sw   Swap
		free bool
	}{
		// Both legs settled: nothing on either chain is waiting on anybody.
		"settled": {
			Swap{Out: btcOutLeg(addr, &FundingOutput{Value: 1}, true, false), In: znnInLeg(true)},
			true,
		},
		"refunded": {
			Swap{Out: btcOutLeg(addr, &FundingOutput{Value: 1}, false, true), In: znnInLeg(true)},
			true,
		},
		// Nothing was ever built, so there is nothing on any chain to lose.
		"draft with no contract": {
			Swap{Out: btcOutLeg("", nil, false, false), In: znnInLeg(false)},
			true,
		},
		// The dangerous one: archived by hand mid-flight, which reaches the
		// history page looking exactly as settled as the two above.
		"funded and archived": {
			Swap{Out: btcOutLeg(addr, &FundingOutput{Value: 400_000}, false, false),
				In: znnInLeg(false), Archived: true},
			false,
		},
		// A contract this browser has not seen funded is not a contract nobody
		// funded -- only a refresh can say that, and it ran whenever it ran.
		"contract never seen funded": {
			Swap{Out: btcOutLeg(addr, nil, false, false), In: znnInLeg(false)},
			false,
		},
		// A Bitcoin leg that settled while the Zenon one this user funded is
		// still locked. On a one-leg view this looks finished; it is the shape
		// where deleting loses the only record of an entry still holding money.
		"other leg still locked": {
			Swap{Out: &Leg{Chain: ChainZNN, Dir: DirOut, Amount: "10",
				Znn: &ZnnLeg{HtlcID: "aa"}},
				In: &Leg{Chain: ChainBTC, Dir: DirIn, Amount: "400000",
					Btc: &BtcLeg{ContractAddr: addr, Claimed: true}}},
			false,
		},
	}
	for name, c := range cases {
		risk := c.sw.DeletionRisk()
		if c.free && risk != "" {
			t.Errorf("%s: warned about a delete that costs nothing: %s", name, risk)
		}
		if !c.free && risk == "" {
			t.Errorf("%s: no warning, so this deletes on one click", name)
		}
	}
	// A maybe is only useful to somebody told which one it is.
	sw := Swap{Out: btcOutLeg(addr, &FundingOutput{Value: 400_000}, false, false),
		In: znnInLeg(false)}
	if risk := sw.DeletionRisk(); !strings.Contains(risk, addr) {
		t.Errorf("warning does not name the contract: %s", risk)
	}
}

// settledSwap is the minimum a store will accept: two legs that make a pair,
// with a valid Bitcoin key.
func settledSwap(t *testing.T, id string) *Swap {
	t.Helper()
	key, err := NewSwapKey()
	if err != nil {
		t.Fatal(err)
	}
	out := btcOutLeg("bcrt1qx", &FundingOutput{Value: 1}, true, false)
	out.Btc.Key = key
	return &Swap{ID: id, Network: "regtest", State: StateSettled, Out: out, In: znnInLeg(true)}
}

func TestStoreDeleteRemovesTheRecord(t *testing.T) {
	store := NewStore(NewMemStorage())
	sw := settledSwap(t, "abcdef0123456789")
	if err := store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Delete(sw.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Load(sw.ID); err == nil {
		t.Error("the swap is still readable after being deleted")
	}
	list, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List still returns %d swaps", len(list))
	}
}

// A caller that mistyped an id should hear about it rather than conclude that a
// swap it can still see has been removed.
func TestStoreDeleteRefusesAnIdThatIsNotThere(t *testing.T) {
	store := NewStore(NewMemStorage())
	if err := store.Delete("abcdef0123456789"); err == nil {
		t.Error("reported success for a swap that was never stored")
	}
	if err := store.Delete("not-a-swap-id"); err == nil {
		t.Error("accepted a malformed id")
	}
}

// Deleting one swap must not disturb the others, which is the whole difference
// between a delete button and a broken history page.
func TestStoreDeleteLeavesTheRestAlone(t *testing.T) {
	store := NewStore(NewMemStorage())
	ids := []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc"}
	for _, id := range ids {
		if err := store.Save(settledSwap(t, id)); err != nil {
			t.Fatalf("Save %s: %v", id, err)
		}
	}
	if err := store.Delete(ids[1]); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, id := range []string{ids[0], ids[2]} {
		if _, err := store.Load(id); err != nil {
			t.Errorf("deleting one swap took %s with it: %v", id, err)
		}
	}
}

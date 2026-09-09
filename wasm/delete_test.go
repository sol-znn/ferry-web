package main

import (
	"strings"
	"testing"
)

// Deleting destroys the only key that can spend a swap's contract, so the
// question this answers is not "is this swap finished" but "can this key still
// be worth something".
func TestDeletionRisk(t *testing.T) {
	cases := map[string]struct {
		sw   Swap
		free bool
	}{
		"redeemed": {Swap{State: StateRedeemed, ContractAddr: "bcrt1qx"}, true},
		"refunded": {Swap{State: StateRefunded, ContractAddr: "bcrt1qx"}, true},
		// Nothing was ever built, so there is nothing on any chain to lose.
		"draft with no contract": {Swap{State: StateDraft}, true},
		// The dangerous one: archived by hand mid-flight, which reaches the
		// history page looking exactly as settled as the two above.
		"funded and archived": {
			Swap{State: StateFunded, ContractAddr: "bcrt1qx", Funding: &FundingOutput{Value: 400_000}},
			false,
		},
		// A contract this browser has not seen funded is not a contract nobody
		// funded — only a refresh can say that, and it ran whenever it ran.
		"contract never seen funded": {
			Swap{State: StateAwaitingFunding, ContractAddr: "bcrt1qx"},
			false,
		},
		"expired but never refunded": {
			Swap{State: StateExpired, ContractAddr: "bcrt1qx", Funding: &FundingOutput{Value: 1}},
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
		// A maybe is only useful to somebody told which one it is.
		if !c.free && !strings.Contains(risk, c.sw.ContractAddr) {
			t.Errorf("%s: warning does not name the contract: %s", name, risk)
		}
	}
}

func TestStoreDeleteRemovesTheRecord(t *testing.T) {
	store := NewStore(NewMemStorage())
	sw := &Swap{ID: "abcdef0123456789", State: StateRedeemed, Network: "regtest"}
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
		if err := store.Save(&Swap{ID: id, State: StateRedeemed, Network: "regtest"}); err != nil {
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

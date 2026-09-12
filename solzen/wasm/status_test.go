package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zenon/solzen/wasm/znn"
)

// A node that will not answer must leave plasma unknown, not report it as
// absent.
//
// The two fields the page reads are a boolean and a difficulty, and their zero
// values say "no plasma, and the next block is free" -- a pair the node can
// never actually report. Falling through to them turned a failed request into a
// specific promise that a four-minute mine would take a second, which is issue
// #10 and is what this pins.
func TestPlasmaProbeFailureLeavesPlasmaUnknown(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		status  int
		known   bool
		fused   bool
		pending uint64
	}{
		{
			name:  "the node refuses the probe",
			body:  `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"method not found in the abi"}}`,
			known: false,
		},
		{
			name:   "the node is not there at all",
			status: http.StatusBadGateway,
			body:   "no",
			known:  false,
		},
		{
			name:  "the node says the account has plasma",
			body:  `{"jsonrpc":"2.0","id":1,"result":{"availablePlasma":126000,"basePlasma":73500,"requiredDifficulty":0}}`,
			known: true, fused: true,
		},
		{
			name:  "the node prices the work",
			body:  `{"jsonrpc":"2.0","id":1,"result":{"availablePlasma":0,"basePlasma":73500,"requiredDifficulty":110250000}}`,
			known: true, fused: false, pending: 110250000,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer node.Close()

			key, err := znn.NewKey()
			if err != nil {
				t.Fatal(err)
			}
			// The receiving side, whose swap address is never funded: the only
			// question asked of the node is the one under test.
			s := &Swap{Role: Role{SendsSol: true}, ZnnSwapSeed: key.SeedHex()}
			m := NewManager(nil)
			m.SetConfig(Config{ZenonURL: node.URL})

			st := new(Status)
			if err := m.readSwapAddress(context.Background(), s, st); err != nil {
				t.Fatalf("readSwapAddress: %v", err)
			}
			sa := st.SwapAddress
			if sa == nil {
				t.Fatal("no swap address reported")
			}
			if sa.PlasmaKnown != tc.known {
				t.Errorf("plasmaKnown = %v, want %v (problem %q)", sa.PlasmaKnown, tc.known, sa.PlasmaProblem)
			}
			if !tc.known {
				if sa.PlasmaProblem == "" {
					t.Error("plasma is unknown but nothing says why")
				}
				return
			}
			if sa.PlasmaProblem != "" {
				t.Errorf("plasma is known but a problem is reported: %q", sa.PlasmaProblem)
			}
			if sa.PlasmaFused != tc.fused || sa.PendingWork != tc.pending {
				t.Errorf("fused=%v work=%d, want fused=%v work=%d",
					sa.PlasmaFused, sa.PendingWork, tc.fused, tc.pending)
			}
		})
	}
}

// The reclaim action is three blocks, so its quote has to be the three added
// up. Quoting only the reclaim would tell the user a third of the wait, and the
// progress bar is measured against this number.
func TestZenonProbeShapeCoversEveryAction(t *testing.T) {
	key, err := znn.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"znn.receive", "znn.create", "znn.unlock", "znn.reclaim", "znn.sweep"} {
		blockType, to, data, err := zenonProbeShape(kind, key.Address)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		switch kind {
		case "znn.receive":
			if blockType != znn.BlockTypeUserReceive {
				t.Errorf("%s: block type %d", kind, blockType)
			}
		case "znn.sweep":
			// A plain transfer. Addressed at the htlc contract with no payload
			// the node would be asked to decode nothing as a call, and refuse.
			if to != key.Address || len(data) != 0 {
				t.Errorf("%s: to=%s data=%d bytes", kind, to, len(data))
			}
		default:
			if to != znn.HtlcContract || len(data) == 0 {
				t.Errorf("%s: to=%s data=%d bytes", kind, to, len(data))
			}
		}
	}
	if _, _, _, err := zenonProbeShape("znn.nonsense", key.Address); err == nil {
		t.Error("an unknown action was priced instead of refused")
	}

	// reclaimHomeSteps is what ZenonCostOf prices and what zenonReclaimHome
	// publishes; they are the same list so that they cannot disagree.
	if got := strings.Join(reclaimHomeSteps, ","); got != "znn.reclaim,znn.receive,znn.sweep" {
		t.Errorf("reclaimHomeSteps = %s", got)
	}
}

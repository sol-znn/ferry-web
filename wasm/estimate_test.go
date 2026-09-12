package main

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/chaincfg"
)

// The numbers below are not invented. They come from swap 1c5762d7611fbe95 on
// mainnet, whose 1000 sat contract could not be redeemed at 2 sat/vB and was
// eventually redeemed at 1.08 — funding 123fa9ff…, spend 774b4915… in block
// 965667, which paid 653 sat to bc1qpqxt3fl… over a 321-byte transaction.
//
// Pinning the estimator to a contract that really existed is what stops this
// arithmetic drifting: the sizing here has to keep agreeing with a transaction
// that is already in the chain and cannot be edited to match.
const (
	stuckContractHex = "6382012088a82048a2bdef64537626513a978b8cc733e45838619a33dff1e045bc9fedf5510d17" +
		"8876a91441d053189e4d3c8db9e26a77385171aa39633bc36704b51c9f6ab17576a9143d3e7142218ef936fbd2" +
		"6edaefeaccc1349302df6888ac"
	stuckDestAddr = "bc1qpqxt3fljkrthfd5mup5sku0s5sy0xlss5x7qv0"
	stuckValue    = 1000

	// The redeem sized against a maximum-length signature. The transaction that
	// actually confirmed is 321 bytes because its DER signature happened to be
	// 71 rather than the 73 a fee must be budgeted for.
	stuckRedeemVSize = 323
)

func mainnetParams(t *testing.T) *chaincfg.Params {
	t.Helper()
	p, err := NetworkParams("mainnet")
	if err != nil {
		t.Fatalf("mainnet params: %v", err)
	}
	return p
}

func TestEstimateMatchesTheContractThatGotStuck(t *testing.T) {
	params := mainnetParams(t)

	sc, err := EstimateSpendCost(stuckValue, stuckDestAddr, stuckContractHex, 2.0, "you", params)
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}

	if sc.Redeem.VSize != stuckRedeemVSize {
		t.Errorf("redeem vsize = %d, want %d", sc.Redeem.VSize, stuckRedeemVSize)
	}
	// The exact pair of numbers the user was shown when the redeem was refused.
	if sc.Redeem.Fee != 646 || sc.Redeem.Net != 354 {
		t.Errorf("at 2 sat/vB got fee %d net %d, want fee 646 net 354", sc.Redeem.Fee, sc.Redeem.Net)
	}
	if sc.Redeem.Viable {
		t.Error("a 354 sat output is below the dust limit and must not be reported viable")
	}
	// The number the failure should have led with: the rate that does work.
	if sc.MaxFeeRate != 1.4 {
		t.Errorf("max viable fee rate = %v, want 1.4", sc.MaxFeeRate)
	}
	if sc.MinRelayable != dustLimit+stuckRedeemVSize {
		t.Errorf("min relayable = %d, want %d", sc.MinRelayable, dustLimit+stuckRedeemVSize)
	}
	if sc.Verdict != "tight" {
		t.Errorf("verdict = %q, want tight (spendable, but only below the quoted rate)", sc.Verdict)
	}
	if sc.DestAssumed || sc.ContractAssumed {
		t.Error("a real contract and a real address were supplied; neither should be marked assumed")
	}
}

// The rate the estimator names as the ceiling has to be a rate the signer will
// actually accept. If maxViableFeeRate rounded up by so much as a hundredth,
// this is the test that would fail rather than a user on the Recover page.
func TestMaxFeeRateIsARateTheSignerAccepts(t *testing.T) {
	params := mainnetParams(t)
	contract, err := hex.DecodeString(stuckContractHex)
	if err != nil {
		t.Fatal(err)
	}
	destScript, err := addressScript(stuckDestAddr, params)
	if err != nil {
		t.Fatal(err)
	}
	vsize, err := spendVSize(contract, destScript, make([]byte, SecretSize), 0)
	if err != nil {
		t.Fatal(err)
	}

	for _, value := range []int64{870, 1000, 1234, 5000, 100000} {
		rate := maxViableFeeRate(value, vsize)
		if rate == 0 {
			t.Errorf("value %d: no viable rate, but it is above the %d sat floor",
				value, dustLimit+feeFor(minRelayFeeRate, vsize))
			continue
		}
		if net := value - feeFor(rate, vsize); net < dustLimit {
			t.Errorf("value %d at the quoted %v sat/vB leaves %d sat, below the %d sat dust limit",
				value, rate, net, dustLimit)
		}
		// And it is genuinely the ceiling: a hundredth more must not clear.
		if net := value - feeFor(rate+0.01, vsize); net >= dustLimit {
			t.Errorf("value %d: %v sat/vB is not the ceiling, %v also clears dust",
				value, rate, rate+0.01)
		}
	}
}

// An amount below the relay floor is a different failure from an amount that is
// merely priced badly today, and it has to say so: there is no fee rate to
// retry at, and the advice "lower your fee" would be a lie.
func TestUnspendableAmountSaysSo(t *testing.T) {
	params := mainnetParams(t)

	sc, err := EstimateSpendCost(700, stuckDestAddr, stuckContractHex, 1.0, "node", params)
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}
	if sc.Verdict != "unspendable" {
		t.Fatalf("verdict = %q, want unspendable", sc.Verdict)
	}
	if sc.MaxFeeRate != 0 {
		t.Errorf("max fee rate = %v, want 0 — no rate works", sc.MaxFeeRate)
	}
	if !strings.Contains(sc.Summary, "cannot be unlocked") {
		t.Errorf("summary does not say the amount cannot be unlocked: %s", sc.Summary)
	}
}

// buildSpend and the estimator have to agree, because the whole point of the
// quote is that it predicts what the signer will do. They share spendVSize, and
// this is the test that keeps them sharing it.
func TestEstimateAgreesWithTheSigner(t *testing.T) {
	params := mainnetParams(t)
	contract, key, secret := stuckLikeContract(t)

	const value = 100000
	const rate = 4.0

	funding := FundingOutput{TxID: strings.Repeat("ab", 32), Vout: 0, Value: value}
	spend, err := BuildRedeem(contract, funding, key, secret, stuckDestAddr, rate, params)
	if err != nil {
		t.Fatalf("build redeem: %v", err)
	}

	sc, err := EstimateSpendCost(value, stuckDestAddr, hex.EncodeToString(contract), rate, "you", params)
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}
	if sc.Redeem.VSize != spend.VSize {
		t.Errorf("estimated vsize %d, signer used %d", sc.Redeem.VSize, spend.VSize)
	}
	if sc.Redeem.Fee != spend.Fee {
		t.Errorf("estimated fee %d, signer charged %d", sc.Redeem.Fee, spend.Fee)
	}
	if sc.Redeem.Net != spend.Value {
		t.Errorf("estimated net %d, signer paid out %d", sc.Redeem.Net, spend.Value)
	}
}

// The refused spend has to name the rate that would have worked, since on the
// Recover page that rate is a field the reader can type into.
func TestDustErrorNamesAWorkableRate(t *testing.T) {
	params := mainnetParams(t)
	contract, key, secret := stuckLikeContract(t)

	funding := FundingOutput{TxID: strings.Repeat("ab", 32), Vout: 0, Value: stuckValue}
	_, err := BuildRedeem(contract, funding, key, secret, stuckDestAddr, 2.0, params)
	if err == nil {
		t.Fatal("expected a dust refusal at 2 sat/vB on a 1000 sat contract")
	}
	msg := err.Error()
	for _, want := range []string{"dust limit", "1.4 sat/vB", "highest rate that works"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not mention %q:\n%s", want, msg)
		}
	}

	// And at the rate it named, the same spend must build.
	spend, err := BuildRedeem(contract, funding, key, secret, stuckDestAddr, 1.4, params)
	if err != nil {
		t.Fatalf("the rate the error recommended was refused: %v", err)
	}
	if spend.Value < dustLimit {
		t.Errorf("recommended rate produced a %d sat output, below the dust limit", spend.Value)
	}
}

// stuckLikeContract builds a contract with the same shape as the one that got
// stuck — same template, same 32-byte secret — but with a key this test holds,
// so the redeem branch can actually be signed.
func stuckLikeContract(t *testing.T) ([]byte, *SwapKey, []byte) {
	t.Helper()
	key, err := NewSwapKey()
	if err != nil {
		t.Fatal(err)
	}
	secret, hash, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	contract, err := BuildContract(make([]byte, 20), key.PKH, templateLockTime(), hash)
	if err != nil {
		t.Fatal(err)
	}
	return contract, key, secret
}

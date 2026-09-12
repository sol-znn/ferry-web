package sol

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
)

// The layout in htlc.go is one of two readings of program/src/lib.rs. These
// tests pin this one against values that came off the chain, so a drift between
// the Rust and the Go has somewhere to fail other than a swap.

// Captured from an escrow the deployed program actually wrote on the local
// validator, by ui/scripts/capture-escrow.mjs. Hand-writing this fixture would
// only prove the decoder agrees with whoever wrote it; taking it off the chain
// makes it a check against the Rust.
const (
	liveEscrowBase64 = "AQABAgMEBQYHCAkKCwwNDg8QERITFBUWFxgZGhscHR4fNheyjfr6uzamjmd1rIQg" +
		"hyPNfFQZFHGkQ2vnsa+Rs7yHnoZCw2cOd/xcKj5bA8ysHLTNx1KmOH4yNZZ07kOo" +
		"7oCy5g4AAAAAj9amp49YV9e6HP6QM7/+7IbaK6akvWCm+DMgnZ0+OQ06XZxqAAAAAP8="
	liveEscrowLamports  = 251_907_040
	liveEscrowAmount    = 250_000_000
	liveEscrowTimelock  = 1_788_632_378
	liveEscrowInitiator = "4e9yV2kLvDCV7J1Y1QkRRhnnziGGZTNNtvBZaiWwj5Qf"
	liveEscrowReceiver  = "A8QEs6iRuRHxAudbC2rd9x7NmXHawevTZ8NcvR4XZhmP"
	liveEscrowHashlock  = "8fd6a6a78f5857d7ba1cfe9033bffeec86da2ba6a4bd60a6f833209d9d3e390d"
	liveEscrowAddress   = "HVBXovURpqbzZcjAz222ruDgSjP9rs5u6N9d4bz5wxKw"
	liveEscrowProgram   = "5mcZ6qPc5YY7PnwvXJTtVDK8hgijqyXBCFH8eR3wzXFF"
)

func TestDecodeEscrowRefusesAnythingElse(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(liveEscrowBase64)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != EscrowLen {
		t.Fatalf("the fixture is %d bytes, an escrow is %d", len(raw), EscrowLen)
	}

	e, err := DecodeEscrow(raw, liveEscrowLamports)
	if err != nil {
		t.Fatal(err)
	}
	if e.Amount != liveEscrowAmount {
		t.Errorf("amount = %d, want %d", e.Amount, liveEscrowAmount)
	}
	if e.Timelock != liveEscrowTimelock {
		t.Errorf("timelock = %d, want %d", e.Timelock, liveEscrowTimelock)
	}
	if e.Initiator.String() != liveEscrowInitiator {
		t.Errorf("initiator = %s", e.Initiator)
	}
	if e.Receiver.String() != liveEscrowReceiver {
		t.Errorf("receiver = %s", e.Receiver)
	}
	if got := hex.EncodeToString(e.Hashlock[:]); got != liveEscrowHashlock {
		t.Errorf("hashlock = %s", got)
	}
	// The account holds the swapped amount and its own rent, and the two are
	// not the same number. Reading the balance as the amount is the mistake
	// this asserts against.
	if e.Lamports <= e.Amount {
		t.Error("the account should hold the amount plus rent")
	}

	// The id in the data must re-derive the address the account was found at:
	// that is what stops one real escrow being read as another.
	addr, _, err := EscrowAddress(mustPubkey(t, liveEscrowProgram), e.SwapID)
	if err != nil {
		t.Fatal(err)
	}
	if addr.String() != liveEscrowAddress {
		t.Errorf("the stored swap id derives %s, but the escrow was at %s", addr, liveEscrowAddress)
	}

	short := append([]byte(nil), raw[:EscrowLen-1]...)
	if _, err := DecodeEscrow(short, 0); err == nil {
		t.Error("a short account was decoded")
	}
	dead := append([]byte(nil), raw...)
	dead[0] = 0
	if _, err := DecodeEscrow(dead, 0); err == nil {
		t.Error("an account with a cleared tag was decoded as live")
	}
}

func TestCreateInstructionMatchesTheProgramsLayout(t *testing.T) {
	programID := mustPubkey(t, "5mcZ6qPc5YY7PnwvXJTtVDK8hgijqyXBCFH8eR3wzXFF")
	initiator := mustPubkey(t, "8S8cXUuRjrqr9obfqjdCJNRQqDgiM7QZKA4T1XkK2Fek")
	receiver := mustPubkey(t, "9tPYQGDCDpZ8Vs4bxdb4TVsNbTMuNvPHrfBK7q9tKAcH")

	var swapID, hashlock [32]byte
	for i := range swapID {
		swapID[i] = byte(i)
		hashlock[i] = byte(255 - i)
	}

	ix, err := BuildCreate(CreateParams{
		ProgramID: programID, SwapID: swapID, Initiator: initiator, Receiver: receiver,
		Amount: 400_000_000, Hashlock: hashlock, Timelock: 1_800_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := base64.StdEncoding.DecodeString(ix.Data)
	if err != nil {
		t.Fatal(err)
	}
	// The program refuses any other length outright, so this is the check that
	// catches a field added on one side only.
	if len(data) != 113 {
		t.Fatalf("create data is %d bytes, the program expects 113", len(data))
	}
	if data[0] != tagCreate {
		t.Errorf("tag = %d", data[0])
	}
	if got := binary.LittleEndian.Uint64(data[65:73]); got != 400_000_000 {
		t.Errorf("amount encoded as %d", got)
	}
	if got := int64(binary.LittleEndian.Uint64(data[105:113])); got != 1_800_000_000 {
		t.Errorf("timelock encoded as %d", got)
	}

	if len(ix.Accounts) != 3 {
		t.Fatalf("create touches 3 accounts, got %d", len(ix.Accounts))
	}
	if !ix.Accounts[0].IsSigner || ix.Accounts[0].Pubkey != initiator.String() {
		t.Error("the initiator must be the only signer")
	}
	if ix.Accounts[1].IsSigner {
		t.Error("the escrow PDA cannot sign -- that is the point of it")
	}
	if ix.Accounts[2].Pubkey != SystemProgram.String() {
		t.Errorf("the third account should be the system program, got %s", ix.Accounts[2].Pubkey)
	}

	escrow, _, err := EscrowAddress(programID, swapID)
	if err != nil {
		t.Fatal(err)
	}
	if ix.Accounts[1].Pubkey != escrow.String() {
		t.Error("the instruction names an escrow other than this swap id's")
	}
}

// Both spending branches pay addresses taken from the escrow, never from a
// parameter. This is the property that makes the page unable to redirect money,
// so it is asserted rather than assumed.
func TestSpendingBranchesTakeNoDestination(t *testing.T) {
	programID := mustPubkey(t, "5mcZ6qPc5YY7PnwvXJTtVDK8hgijqyXBCFH8eR3wzXFF")
	state := &Escrow{
		Initiator: mustPubkey(t, "8S8cXUuRjrqr9obfqjdCJNRQqDgiM7QZKA4T1XkK2Fek"),
		Receiver:  mustPubkey(t, "9tPYQGDCDpZ8Vs4bxdb4TVsNbTMuNvPHrfBK7q9tKAcH"),
		Amount:    1,
	}
	escrow := mustPubkey(t, "11111111111111111111111111111112")

	redeem, err := BuildRedeem(programID, state, escrow, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range redeem.Accounts {
		if a.IsSigner {
			t.Error("redeem should need no signer: the preimage is the authorization")
		}
	}
	if redeem.Accounts[1].Pubkey != state.Receiver.String() {
		t.Error("redeem pays someone other than the escrow's receiver")
	}
	if redeem.Accounts[2].Pubkey != state.Initiator.String() {
		t.Error("the rent should go back to the escrow's initiator")
	}

	refund, err := BuildRefund(programID, state, escrow)
	if err != nil {
		t.Fatal(err)
	}
	if refund.Accounts[1].Pubkey != state.Initiator.String() {
		t.Error("refund pays someone other than the escrow's initiator")
	}
}

func TestRedeemRefusesAnImpossiblePreimage(t *testing.T) {
	programID := mustPubkey(t, "5mcZ6qPc5YY7PnwvXJTtVDK8hgijqyXBCFH8eR3wzXFF")
	state := &Escrow{Amount: 1}
	escrow := mustPubkey(t, "11111111111111111111111111111112")
	for _, n := range []int{0, MaxPreimage + 1} {
		if _, err := BuildRedeem(programID, state, escrow, make([]byte, n)); err == nil {
			t.Errorf("a %d-byte preimage was accepted; the program caps it at %d", n, MaxPreimage)
		}
	}
}

// The preimage has to come back out of the instruction that published it --
// that is how the counterparty learns it -- and nothing else must be mistaken
// for one.
func TestDecodeRedeemPreimage(t *testing.T) {
	preimage := []byte(strings.Repeat("s", 32))
	data := append([]byte{tagRedeem, byte(len(preimage))}, preimage...)
	got, ok := DecodeRedeemPreimage(data)
	if !ok || string(got) != string(preimage) {
		t.Fatalf("round trip failed: %q %v", got, ok)
	}

	for name, bad := range map[string][]byte{
		"a create":               {tagCreate, 1, 2},
		"a refund":               {tagRefund},
		"a truncated preimage":   {tagRedeem, 32, 1, 2},
		"a length that overruns": {tagRedeem, 200},
		"nothing at all":         {},
	} {
		if _, ok := DecodeRedeemPreimage(bad); ok {
			t.Errorf("%s was read as a redeem", name)
		}
	}
}

// A program-derived address has no private key, which is what stops anyone
// signing for an escrow. The derivation is reproduced here rather than taken
// from the page's JavaScript, so this checks it agrees with itself and is
// stable.
func TestEscrowAddressIsDeterministicAndOffCurve(t *testing.T) {
	programID := mustPubkey(t, "5mcZ6qPc5YY7PnwvXJTtVDK8hgijqyXBCFH8eR3wzXFF")
	var id [32]byte
	id[0] = 7

	a, bump, err := EscrowAddress(programID, id)
	if err != nil {
		t.Fatal(err)
	}
	b, bump2, err := EscrowAddress(programID, id)
	if err != nil {
		t.Fatal(err)
	}
	if a != b || bump != bump2 {
		t.Error("the same seeds gave two different addresses")
	}
	if isOnCurve(a) {
		t.Error("the escrow address is a real public key, so someone could hold its private key")
	}

	id[0] = 8
	c, _, err := EscrowAddress(programID, id)
	if err != nil {
		t.Fatal(err)
	}
	if a == c {
		t.Error("two swap ids collided on one escrow")
	}
}

func TestBase58RoundTripsIncludingLeadingZeros(t *testing.T) {
	for _, s := range []string{
		"5mcZ6qPc5YY7PnwvXJTtVDK8hgijqyXBCFH8eR3wzXFF",
		"11111111111111111111111111111111",
	} {
		raw, err := DecodeBase58(s)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if got := EncodeBase58(raw); got != s {
			t.Errorf("%s round-tripped to %s", s, got)
		}
	}
	if _, err := DecodeBase58("0OIl"); err == nil {
		t.Error("characters outside the alphabet were accepted rather than refused")
	}
	if _, err := ParsePubkey("tooshort"); err == nil {
		t.Error("a short address was accepted")
	}
}

func mustPubkey(t *testing.T, s string) Pubkey {
	t.Helper()
	p, err := ParsePubkey(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

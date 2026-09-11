package main

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/txscript"
	"github.com/zenon/ferry-web/wasm/znn"
)

// The token an HTLC holds is as much a term of the trade as how much of it, and
// nothing else in Verify constrains it: a counterparty can issue a token this
// morning and lock the agreed NUMBER of units of it, satisfying the hashlock,
// both parties, the expiry and the amount.
func TestZenonVerifyRefusesTheWrongToken(t *testing.T) {
	c := &znn.Client{}
	base := func() *znn.HtlcInfo {
		return &znn.HtlcInfo{
			HashLocked: "z1me", TimeLocked: "z1them",
			HashType: znn.HashTypeSHA256, KeyMaxSize: 32,
			Amount:        "1000000000",
			TokenStandard: znn.ZnnTokenStandard,
		}
	}
	want := znn.VerifyParams{
		ExpectRecipient: "z1me", ExpectSender: "z1them",
		ExpectTokenStandard: znn.ZnnTokenStandard,
		MinAmount:           big.NewInt(1_000_000_000),
	}
	if err := c.Verify(base(), want); err != nil {
		t.Fatalf("rejected an HTLC holding the agreed token: %v", err)
	}

	scam := base()
	scam.TokenStandard = "zts1scamxxxxxxxxxxxxxxxxxxx"
	err := c.Verify(scam, want)
	if err == nil {
		t.Fatal("accepted an HTLC holding a token nobody agreed to")
	}
	if !strings.Contains(err.Error(), "zts1scam") {
		t.Errorf("the rejection does not name the token that was found: %v", err)
	}
}

// A swap record written before the token field was defaulted still has to
// verify, and the creation form has always said a blank token means ZNN. If
// AgreedToken did not resolve that, such a record would produce an empty
// expectation -- which is the no-token-check hole all over again -- or an
// unfixable refusal, since the token is not editable after creation.
func TestAgreedTokenDefaultsToZnn(t *testing.T) {
	if got := (ZenonLeg{}).AgreedToken(); got != znn.ZnnTokenStandard {
		t.Errorf("a blank token standard resolves to %q, want %q", got, znn.ZnnTokenStandard)
	}
	const other = "zts1qsrxxxxxxxxxxxxxmrhjll"
	if got := (ZenonLeg{TokenStandard: other}).AgreedToken(); got != other {
		t.Errorf("a named token standard resolves to %q, want %q", got, other)
	}
	// And the expectation handed to Verify carries it, so a record with no
	// token still constrains which token the HTLC may hold.
	sw := &Swap{Leg: LegSend, Role: RoleInitiator}
	if got := zenonVerifyParams(sw).ExpectTokenStandard; got != znn.ZnnTokenStandard {
		t.Errorf("verification expects token %q, want %q", got, znn.ZnnTokenStandard)
	}
}

// An amount that was agreed and then not compared must not come back as one
// that matched. This is the fail-open the token-decimals lookup used to have:
// any error there left MinAmount nil, Verify returned nil, and the card printed
// "amount ... all match".
func TestZenonVerifyRefusesAnUncheckableAmount(t *testing.T) {
	c := &znn.Client{}
	info := &znn.HtlcInfo{
		HashLocked: "z1me", TimeLocked: "z1them",
		HashType: znn.HashTypeSHA256, KeyMaxSize: 32, Amount: "1000000000",
	}
	err := c.Verify(info, znn.VerifyParams{
		ExpectRecipient:   "z1me",
		AmountUncheckable: "could not read token zts1... from the node",
	})
	if err == nil {
		t.Fatal("reported a skipped amount check as a passed one")
	}
	if !strings.Contains(err.Error(), "could not be checked") {
		t.Errorf("the rejection does not say the check was skipped: %v", err)
	}
}

// Nothing an entry says may become one of the swap's own terms. Adopting the
// observed token as the agreed one is what would let a rejected HTLC pass on a
// second attempt: the first verification records the counterparty's token as
// "what we agreed", and the second finds them equal.
func TestVerificationNeverAdoptsTheObservedToken(t *testing.T) {
	const scam = "zts1scamxxxxxxxxxxxxxxxxxxx"
	for name, sw := range map[string]*Swap{
		"a swap that named a token":    {Zenon: ZenonLeg{TokenStandard: znn.ZnnTokenStandard}},
		"a swap that named none":       {},
		"a swap with an amount agreed": {Zenon: ZenonLeg{AmountDisplay: "10"}},
	} {
		// The write-back VerifyZenon performs, without the node round trip.
		sw.Zenon.HtlcID = "aa" + strings.Repeat("00", 31)
		sw.Zenon.ObservedToken = scam
		sw.Zenon.Verified = false

		if sw.Zenon.TokenStandard == scam {
			t.Errorf("%s: the observed token became the agreed one", name)
		}
		if got := zenonVerifyParams(sw).ExpectTokenStandard; got == scam {
			t.Errorf("%s: a re-verification would now expect the counterparty's token", name)
		}
	}
}

// The node is asked for one entry. An answer describing a different one is not
// the object being verified, whatever it says.
func TestZenonVerifyRefusesAnotherEntry(t *testing.T) {
	c := &znn.Client{}
	info := &znn.HtlcInfo{
		ID: "aa" + strings.Repeat("00", 31), HashLocked: "z1me",
		HashType: znn.HashTypeSHA256, KeyMaxSize: 32, Amount: "1",
	}
	if err := c.Verify(info, znn.VerifyParams{ExpectID: info.ID}); err != nil {
		t.Fatalf("rejected the entry that was asked for: %v", err)
	}
	if err := c.Verify(info, znn.VerifyParams{ExpectID: "bb" + strings.Repeat("00", 31)}); err == nil {
		t.Error("accepted an answer describing a different entry")
	}
}

// A 64-character hex string is also valid base64, decoding without complaint
// into 48 meaningless bytes -- so "try base64, fall back to hex on error" never
// reached the fallback and a hex hashlock failed verification against a
// 96-character value.
func TestHashLockAcceptsBothEncodings(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	wantHex := hex.EncodeToString(raw)

	for name, encoded := range map[string]string{
		"base64 (what a go-zenon node sends)": base64.StdEncoding.EncodeToString(raw),
		"hex (what some tooling sends)":       wantHex,
	} {
		info := &znn.HtlcInfo{HashLock: encoded}
		if got := info.HashLockHex(); got != wantHex {
			t.Errorf("%s: HashLockHex = %s, want %s", name, got, wantHex)
		}
	}
}

// A locktime the parser reads as a sensible number but CHECKLOCKTIMEVERIFY
// refuses to read at all is a contract that passes every check this program
// makes and has a refund branch no node will ever execute. The parser is the
// last point before funding at which that can be caught.
func TestParseRejectsLocktimesTheScriptEngineWillNot(t *testing.T) {
	refund, redeem := mustKey(t), mustKey(t)
	_, hash, _ := NewSecret()
	locktime := time.Now().Add(48 * time.Hour).Unix()

	good := buildTemplate(t, SecretSize, hash, redeem.PKH, locktime, refund.PKH)
	if _, err := ParseContract(good); err != nil {
		t.Fatalf("rejected a well-formed contract: %v", err)
	}

	// The same value, padded to eight bytes. Bitcoin's CLTV reads at most five.
	padded := make([]byte, 8)
	for i, v := 0, locktime; i < 8; i, v = i+1, v>>8 {
		padded[i] = byte(v)
	}
	if _, err := ParseContract(templateWithRawLocktime(t, hash, redeem.PKH, padded, refund.PKH)); err == nil {
		t.Error("accepted a locktime wider than CHECKLOCKTIMEVERIFY will read")
	}

	// Five bytes is legal for CLTV, but only when the fifth is not pure padding.
	nonMinimal := make([]byte, 5)
	for i, v := 0, locktime; i < 5; i, v = i+1, v>>8 {
		nonMinimal[i] = byte(v)
	}
	if nonMinimal[4] != 0 {
		t.Fatalf("test needs a locktime that fits in four bytes, got a fifth byte of %#x", nonMinimal[4])
	}
	if _, err := ParseContract(templateWithRawLocktime(t, hash, redeem.PKH, nonMinimal, refund.PKH)); err == nil {
		t.Error("accepted a non-minimally-encoded locktime, which MINIMALDATA rejects")
	}
}

// templateWithRawLocktime emits the contract shape with the locktime pushed as
// exactly the given bytes, which ScriptBuilder.AddInt64 will not do.
func templateWithRawLocktime(t *testing.T, hash, pkhRedeem, locktime, pkhRefund []byte) []byte {
	t.Helper()
	b := txscript.NewScriptBuilder()
	b.AddOp(txscript.OP_IF)
	b.AddOp(txscript.OP_SIZE)
	b.AddInt64(SecretSize)
	b.AddOp(txscript.OP_EQUALVERIFY)
	b.AddOp(txscript.OP_SHA256)
	b.AddData(hash)
	b.AddOp(txscript.OP_EQUALVERIFY)
	b.AddOp(txscript.OP_DUP)
	b.AddOp(txscript.OP_HASH160)
	b.AddData(pkhRedeem)
	b.AddOp(txscript.OP_ELSE)
	b.AddData(locktime)
	b.AddOp(txscript.OP_CHECKLOCKTIMEVERIFY)
	b.AddOp(txscript.OP_DROP)
	b.AddOp(txscript.OP_DUP)
	b.AddOp(txscript.OP_HASH160)
	b.AddData(pkhRefund)
	b.AddOp(txscript.OP_ENDIF)
	b.AddOp(txscript.OP_EQUALVERIFY)
	b.AddOp(txscript.OP_CHECKSIG)
	script, err := b.Script()
	if err != nil {
		t.Fatalf("build template: %v", err)
	}
	return script
}

// A node that cannot answer is not a chain that disagrees.
//
// The two used to be recorded identically -- VerifyPending cleared, Verified
// false -- and everything downstream reads that as an answer: useAutoRefresh
// stops re-reading the leg, `owed` in the UI never releases the HTLC id over a
// session because it waits on `verified`, and autopilot will not fund the
// participant's Bitcoin leg. So one unreadable token lookup, on an HTLC that
// was entirely correct, silently ended the swap's ability to move on its own
// and left somebody to notice and press Send by hand.
func TestAnUnansweredCheckLeavesTheLegPending(t *testing.T) {
	const (
		id     = "aa" + "00000000000000000000000000000000000000000000000000000000000000"
		mine   = "z1me"
		theirs = "z1them"
	)
	secretHash := bytes.Repeat([]byte{0xab}, 32)

	// A node that knows the entry, and answers everything else asked of it
	// except the one call `tokenOK` switches off.
	node := func(tokenOK bool, hashLock []byte) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			reply := func(result any) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0", "id": req.ID, "result": result,
				})
			}
			switch req.Method {
			case "ledger.getFrontierMomentum":
				reply(map[string]any{"height": 30000, "timestamp": time.Now().Unix()})
			case "embedded.htlc.getById":
				reply(map[string]any{
					"id": id, "timeLocked": mine, "hashLocked": theirs,
					"tokenStandard": znn.ZnnTokenStandard, "amount": "1000000000",
					"expirationTime": time.Now().Add(48 * time.Hour).Unix(),
					"hashType":       znn.HashTypeSHA256, "keyMaxSize": 32,
					"hashLock": base64.StdEncoding.EncodeToString(hashLock),
				})
			case "embedded.token.getByZts":
				if !tokenOK {
					http.Error(w, "node is having a bad minute", http.StatusServiceUnavailable)
					return
				}
				reply(map[string]any{
					"name": "Zenon", "symbol": "ZNN",
					"tokenStandard": znn.ZnnTokenStandard, "decimals": 8,
				})
			default:
				http.Error(w, "unexpected method "+req.Method, http.StatusNotFound)
			}
		}))
	}

	// LegReceive is this user's own Zenon HTLC -- the one whose id has to reach
	// the counterparty, which is where the stall was felt.
	verify := func(t *testing.T, srv *httptest.Server) *Swap {
		t.Helper()
		m := &Manager{Store: NewStore(NewMemStorage()), Znn: znn.New(srv.URL), Network: "regtest"}
		sw := &Swap{
			ID: "abcdef0123456789", Network: "regtest",
			Role: RoleInitiator, Leg: LegReceive, SecretHash: secretHash,
			Zenon: ZenonLeg{
				HtlcID: id, SelfAddress: mine, PeerAddress: theirs,
				TokenStandard: znn.ZnnTokenStandard, AmountDisplay: "10",
			},
		}
		if err := m.Store.Save(sw); err != nil {
			t.Fatalf("could not save the swap: %v", err)
		}
		got, _, err := m.VerifyZenon(t.Context(), sw.ID, id)
		if err == nil {
			t.Fatal("accepted an HTLC it could not fully check")
		}
		if got == nil {
			t.Fatal("no swap came back from a refusal that read the entry")
		}
		return got
	}

	t.Run("a check that could not run keeps asking", func(t *testing.T) {
		srv := node(false, secretHash)
		defer srv.Close()
		got := verify(t, srv)
		if got.Zenon.Verified {
			t.Error("an unchecked amount was reported as verified")
		}
		if !got.Zenon.VerifyPending {
			t.Error("a node that could not answer was recorded as a verdict: the leg will " +
				"never be re-read, and its id will never reach the counterparty")
		}
	})

	t.Run("a disagreement is an answer", func(t *testing.T) {
		srv := node(true, bytes.Repeat([]byte{0xcd}, 32))
		defer srv.Close()
		got := verify(t, srv)
		if got.Zenon.VerifyPending {
			t.Error("an HTLC read and found wanting was left pending, which reads as " +
				"'not checked yet' and hides a real mismatch behind a spinner")
		}
		if got.Zenon.VerifyError == "" {
			t.Error("a refusal recorded no reason")
		}
	})
}

// A term the swap never recorded is not a check switched off; it is a check
// that cannot run, and a check that cannot run must not read as one that
// passed. This is the finding: an HTLC paying the counterparty's own address
// verified clean on a swap created without an address of its own, and the
// unlock that followed published the preimage for it.
func TestVerifyRefusesTermsThatWereNeverAgreed(t *testing.T) {
	c := &znn.Client{}
	info := &znn.HtlcInfo{
		HashLocked: "z1attacker", TimeLocked: "z1them",
		HashType: znn.HashTypeSHA256, KeyMaxSize: 32,
		Amount: "1000000000", TokenStandard: znn.ZnnTokenStandard,
	}
	err := c.Verify(info, znn.VerifyParams{
		ExpectTokenStandard: znn.ZnnTokenStandard,
		MissingTerms:        []string{"this swap records no Zenon address of your own"},
	})
	if err == nil {
		t.Fatal("an HTLC verified with no recipient to check it against")
	}
	if !strings.Contains(err.Error(), "no Zenon address of your own") {
		t.Errorf("the refusal does not name the missing term: %v", err)
	}
	if znn.CheckIncomplete(err) {
		t.Error("a term nobody agreed was recorded as a check to ask the node about again")
	}
}

// The same thing through the manager, against a node: the counterparty's HTLC
// matches every term this swap recorded -- hashlock, token, amount, expiry --
// and pays THEM. With no address of this user's own on the swap, it must be
// refused, not pending; once the address is added it must be refused by name;
// and an HTLC that pays this user must then verify.
func TestIncomingHtlcNeedsARecipientAndAnAmount(t *testing.T) {
	const (
		mine     = "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d"
		attacker = "z1qqvwzz2xq7q5gwk6uhcddgrpxlfcyzc8rsu82s"
		htlcID   = "9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f"
	)
	hash := bytes.Repeat([]byte{0xab}, 32)
	paysTo := attacker
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		reply := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		}
		switch req.Method {
		case "ledger.getFrontierMomentum":
			reply(map[string]any{"height": 30000, "timestamp": time.Now().Unix()})
		case "embedded.htlc.getById":
			reply(map[string]any{
				"id": htlcID, "timeLocked": attacker, "hashLocked": paysTo,
				"tokenStandard": znn.ZnnTokenStandard, "amount": "1000000000",
				"expirationTime": time.Now().Add(20 * time.Hour).Unix(),
				"hashType":       znn.HashTypeSHA256, "keyMaxSize": 32,
				"hashLock": base64.StdEncoding.EncodeToString(hash),
			})
		case "embedded.token.getByZts":
			reply(map[string]any{"name": "Zenon", "symbol": "ZNN",
				"tokenStandard": znn.ZnnTokenStandard, "decimals": 8})
		default:
			http.Error(w, "unexpected method "+req.Method, http.StatusNotFound)
		}
	}))
	t.Cleanup(node.Close)

	m := &Manager{Store: NewStore(NewMemStorage()), Znn: znn.New(node.URL), Network: "regtest"}
	// The Bitcoin initiator who receives ZNN: the counterparty's HTLC is the
	// incoming one, and it has to pay this user. Created without a Zenon
	// address of their own, which the form allows.
	sw := &Swap{
		ID: "abcdef0123456700", Network: "regtest", Role: RoleInitiator, Leg: LegSend,
		State: StateFunded, SecretHash: hash, AmountSats: 400_000,
		LockTime: time.Now().Add(48 * time.Hour).Unix(),
		Zenon:    ZenonLeg{PeerAddress: attacker, AmountDisplay: "10"},
	}
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, _, err := m.VerifyZenon(t.Context(), sw.ID, htlcID)
	if err == nil {
		t.Fatal("an HTLC paying the counterparty verified on a swap with no address of its own")
	}
	if !strings.Contains(err.Error(), "no Zenon address of your own") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
	if got.Zenon.Verified || got.Zenon.VerifyPending {
		t.Errorf("verified=%v pending=%v after a refusal for a missing term; want neither",
			got.Zenon.Verified, got.Zenon.VerifyPending)
	}

	// The address is added. It cannot be adopted from what the entry pays --
	// that is the attacker's -- so it comes from the user, and the HTLC is now
	// refused by name.
	if _, err := m.SetZenonTerms(sw.ID, mine, "", ""); err != nil {
		t.Fatalf("SetZenonTerms: %v", err)
	}
	_, _, err = m.VerifyZenon(t.Context(), sw.ID, htlcID)
	if err == nil || !strings.Contains(err.Error(), "hashLocked address is "+attacker) {
		t.Errorf("an HTLC paying the attacker was not refused by name: %v", err)
	}

	// An HTLC that pays this user, with every term on the swap: verified.
	paysTo = mine
	got, _, err = m.VerifyZenon(t.Context(), sw.ID, htlcID)
	if err != nil {
		t.Fatalf("an HTLC paying this user on a complete swap was refused: %v", err)
	}
	if !got.Zenon.Verified {
		t.Error("verified flag not set")
	}

	// And with the amount blank instead, the same HTLC is refused for that.
	noAmount := &Swap{
		ID: "abcdef0123456711", Network: "regtest", Role: RoleInitiator, Leg: LegSend,
		State: StateFunded, SecretHash: hash, AmountSats: 400_000,
		LockTime: time.Now().Add(48 * time.Hour).Unix(),
		Zenon:    ZenonLeg{SelfAddress: mine, PeerAddress: attacker},
	}
	if err := m.Store.Save(noAmount); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, _, err = m.VerifyZenon(t.Context(), noAmount.ID, htlcID)
	if err == nil || !strings.Contains(err.Error(), "no agreed Zenon amount") {
		t.Errorf("an HTLC verified on a swap with no agreed amount: %v", err)
	}
	if got != nil && got.Zenon.VerifyPending {
		t.Error("a missing amount was recorded as pending rather than refused")
	}
}

// Terms are filled in where blank and never changed where not: a term that can
// be edited after the fact is one that can be edited to match whatever the
// counterparty locked. Filling one in un-verifies the leg.
func TestZenonTermsFillBlanksOnly(t *testing.T) {
	const (
		mine  = "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d"
		other = "z1qqvwzz2xq7q5gwk6uhcddgrpxlfcyzc8rsu82s"
	)
	m := &Manager{Store: NewStore(NewMemStorage()), Network: "regtest"}
	sw := &Swap{ID: "abcdef0123456722", Network: "regtest", Role: RoleInitiator, Leg: LegSend,
		Zenon: ZenonLeg{PeerAddress: other, HtlcID: "9f", Verified: true}}
	if err := m.Store.Save(sw); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := m.SetZenonTerms(sw.ID, "not-an-address", "", ""); err == nil {
		t.Error("an unparseable address was accepted")
	}
	if _, err := m.SetZenonTerms(sw.ID, "", "", "10\nprintf PWNED"); err == nil {
		t.Error("a non-canonical amount was accepted")
	}
	got, err := m.SetZenonTerms(sw.ID, mine, "", "10")
	if err != nil {
		t.Fatalf("filling blanks: %v", err)
	}
	if got.Zenon.SelfAddress != mine || got.Zenon.AmountDisplay != "10" {
		t.Errorf("blanks not filled: %+v", got.Zenon)
	}
	if got.Zenon.Verified {
		t.Error("the leg stayed verified after its terms changed")
	}
	if _, err := m.SetZenonTerms(sw.ID, other, "", ""); err == nil ||
		!strings.Contains(err.Error(), "cannot be changed") {
		t.Errorf("an agreed address was changed: %v", err)
	}
	if _, err := m.SetZenonTerms(sw.ID, "", mine, ""); err == nil ||
		!strings.Contains(err.Error(), "cannot be changed") {
		t.Errorf("the counterparty's agreed address was changed: %v", err)
	}
	if _, err := m.SetZenonTerms(sw.ID, "", "", "11"); err == nil ||
		!strings.Contains(err.Error(), "cannot be changed") {
		t.Errorf("the agreed amount was changed: %v", err)
	}
	// Resubmitting what is already there is not a change.
	if _, err := m.SetZenonTerms(sw.ID, mine, other, "10"); err != nil {
		t.Errorf("resubmitting the same terms was refused: %v", err)
	}
}

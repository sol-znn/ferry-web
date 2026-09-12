package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	btcecdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/txscript"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"

	"github.com/zenon/ferry-web-v2/wasm/znn"
)

func testIdentity(t *testing.T) *BoardIdentity {
	t.Helper()
	id, err := NewBoardIdentity()
	if err != nil {
		t.Fatalf("NewBoardIdentity: %v", err)
	}
	return id
}

func samplePost(id string) BoardPost {
	now := time.Now()
	return BoardPost{
		Version:   2,
		ID:        id,
		Network:   "mainnet",
		Give:      PostLeg{Chain: ChainBTC, Amount: "1000000"},
		Want:      PostLeg{Chain: ChainZNN, Amount: "1200"},
		Role:      RoleInitiator,
		LockHours: 48,
		Status:    StatusOpen,
		Note:      "no rush",
		CreatedAt: now.Unix(),
		ExpiresAt: now.Add(DefaultPostTTL()).Unix(),
	}
}

func TestPostRoundTrips(t *testing.T) {
	id := testIdentity(t)
	post := samplePost("abcd1234")

	ev, err := SealPost(id, post)
	if err != nil {
		t.Fatalf("SealPost: %v", err)
	}
	if ev.Kind != boardKind {
		t.Fatalf("kind is %d, want %d", ev.Kind, boardKind)
	}
	// The four tags a reader's filter depends on. A post that reaches a relay
	// without them is a post nobody subscribed to the board will ever see, and
	// nothing else in this test would notice.
	for _, want := range [][2]string{
		{"d", post.ID},
		{"t", boardTag()},
		{"n", "mainnet"},
		{"s", StatusOpen},
	} {
		if got := tagValue(ev, want[0]); got != want[1] {
			t.Errorf("tag %q is %q, want %q", want[0], got, want[1])
		}
	}
	if tagValue(ev, "expiration") == "" {
		t.Error("no NIP-40 expiration tag, so relays cannot drop this post themselves")
	}

	listing, err := ReadPost(ev, "mainnet", time.Now())
	if err != nil {
		t.Fatalf("ReadPost refused a post this build just signed: %v", err)
	}
	if listing.Author != id.PubKey {
		t.Errorf("author is %q, want %q", listing.Author, id.PubKey)
	}
	if listing.Post.Give.Amount != post.Give.Amount || listing.Post.Want.Amount != post.Want.Amount {
		t.Errorf("terms changed in transit: %+v", listing.Post)
	}
	if len(listing.Problems) != 0 {
		t.Errorf("unexpected problems: %v", listing.Problems)
	}
	if listing.Expired {
		t.Error("a post with a day to run reads as expired")
	}
}

// The `n` tag carries the network, which is a setting a user can change --
// including on a development build. Point dev at "mainnet" for a moment and its
// test posts would land in the same public index as real offers on the very
// relays that carry both, unless the `t` tag itself differs. So the tag is
// scoped to the instance, the same way browser storage is, and this is the test
// that a dev build cannot be talked out of it by its own settings.
func TestBoardTagIsScopedToTheInstance(t *testing.T) {
	original := BuildEnv
	t.Cleanup(func() { BuildEnv = original })

	BuildEnv = EnvProd
	prodTag := boardTag()
	if prodTag != "ferry-board-v1" {
		t.Fatalf("prod board tag is %q", prodTag)
	}

	BuildEnv = EnvDev
	devTag := boardTag()
	if devTag == prodTag {
		t.Fatal("the dev build uses the same board tag as prod")
	}
	if !strings.Contains(devTag, "dev") {
		t.Fatalf("dev board tag %q does not say so", devTag)
	}

	// The tag actually reaches the wire, on both event kinds a board publishes —
	// a post and a take — because a reader finds either by subscribing on it.
	id := testIdentity(t)
	post := samplePost("abcd1234")
	post.Network = "mainnet" // exactly the misconfiguration this guards against
	ev, err := SealPost(id, post)
	if err != nil {
		t.Fatal(err)
	}
	if got := tagValue(ev, "t"); got != devTag {
		t.Errorf("a post published by the dev build carries tag %q, want %q", got, devTag)
	}

	other := testIdentity(t)
	code, err := NewSessionCode()
	if err != nil {
		t.Fatal(err)
	}
	take, err := SealTake(id, other.PubKey, Take{Version: 2, PostID: "abcd1234", Code: code})
	if err != nil {
		t.Fatal(err)
	}
	if got := tagValue(take, "t"); got != devTag {
		t.Errorf("a take sent by the dev build carries tag %q, want %q", got, devTag)
	}
}

// A test post is made to watch one thing happen and then abandoned, and only
// the browser that made it can withdraw it — so a day-long default leaves every
// experiment standing on public relays until tomorrow. The development instance
// gets fifteen minutes instead, and both defaults still have to sit inside the
// bounds a post is allowed to choose, or the module would refuse its own.
func TestDefaultPostTTLIsShorterOnTheDevInstance(t *testing.T) {
	original := BuildEnv
	t.Cleanup(func() { BuildEnv = original })

	BuildEnv = EnvProd
	prod := DefaultPostTTL()
	BuildEnv = EnvDev
	dev := DefaultPostTTL()

	if prod != 24*time.Hour {
		t.Errorf("prod default is %s, want 24h", prod)
	}
	if dev != 15*time.Minute {
		t.Errorf("dev default is %s, want 15m", dev)
	}
	if dev >= prod {
		t.Errorf("the dev default (%s) is not shorter than prod's (%s)", dev, prod)
	}
	for name, ttl := range map[string]time.Duration{"prod": prod, "dev": dev} {
		if ttl < MinPostTTL || ttl > MaxPostTTL {
			t.Errorf("the %s default (%s) is outside the %s..%s a post may choose",
				name, ttl, MinPostTTL, MaxPostTTL)
		}
	}
}

// The property the whole design rests on: a post is a document only its author
// can change. Editing one and handing it on has to fail, or "signed so nobody
// else can tamper with them" is decoration.
func TestPostRefusesTampering(t *testing.T) {
	id := testIdentity(t)
	ev, err := SealPost(id, samplePost("abcd1234"))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("edited terms", func(t *testing.T) {
		// A relay, or anyone between, rewriting the price. The id no longer
		// matches the content it is supposed to hash, which is caught before
		// the signature is even considered.
		bad := *ev
		var post BoardPost
		if err := json.Unmarshal([]byte(ev.Content), &post); err != nil {
			t.Fatal(err)
		}
		post.Want.Amount = "1"
		raw, err := json.Marshal(post)
		if err != nil {
			t.Fatal(err)
		}
		bad.Content = string(raw)
		if _, err := ReadPost(&bad, "mainnet", time.Now()); err == nil {
			t.Fatal("an edited post was accepted")
		}
	})

	t.Run("edited terms with a recomputed id", func(t *testing.T) {
		// The thorough version: the attacker rewrites the content AND fixes the
		// id so it hashes correctly. Only the signature stands between that and
		// a forged offer.
		bad := *ev
		var post BoardPost
		if err := json.Unmarshal([]byte(ev.Content), &post); err != nil {
			t.Fatal(err)
		}
		post.Want.Amount = "1"
		raw, err := json.Marshal(post)
		if err != nil {
			t.Fatal(err)
		}
		bad.Content = string(raw)
		fixed, err := bad.eventID()
		if err != nil {
			t.Fatal(err)
		}
		bad.ID = hex.EncodeToString(fixed[:])
		if _, err := ReadPost(&bad, "mainnet", time.Now()); err == nil {
			t.Fatal("a re-hashed edit was accepted without a valid signature")
		}
	})

	t.Run("re-signed under another key", func(t *testing.T) {
		// Not a forgery so much as impersonation: somebody republishing the same
		// terms under their own key. It has to READ fine — it is their post now
		// — and it must not carry the original author's name.
		other := testIdentity(t)
		var post BoardPost
		if err := json.Unmarshal([]byte(ev.Content), &post); err != nil {
			t.Fatal(err)
		}
		theirs, err := SealPost(other, post)
		if err != nil {
			t.Fatal(err)
		}
		listing, err := ReadPost(theirs, "mainnet", time.Now())
		if err != nil {
			t.Fatalf("a validly signed copy should read: %v", err)
		}
		if listing.Author == id.PubKey {
			t.Fatal("a post re-signed by somebody else is still attributed to the original author")
		}
	})

	t.Run("filed under a different slot", func(t *testing.T) {
		// The subtle one. A relay keys an addressable event by its `d` tag, so a
		// post whose body claims another id is one that could never be edited or
		// withdrawn — the author's replacement would land in a different slot.
		bad := samplePost("abcd1234")
		signed, err := SealPost(id, bad)
		if err != nil {
			t.Fatal(err)
		}
		signed.Tags[0] = []string{"d", "deadbeef"}
		// Re-signed so the failure is the mismatch and not the broken signature
		// that editing a tag would otherwise cause.
		priv, err := id.PrivKey()
		if err != nil {
			t.Fatal(err)
		}
		if err := signEvent(priv, signed); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadPost(signed, "mainnet", time.Now()); err == nil {
			t.Fatal("a post filed under a slot it does not name was accepted")
		}
	})
}

// Two versions of one post sharing a `created_at` is a coin flip, not an edit.
//
// NIP-01 resolves a replaceable-event tie by keeping the LOWEST event id, so a
// withdrawal that ties with the edit it retracts loses roughly half the time —
// the offer stays live for everybody except its author, whose own store was
// written locally and shows it gone. Readers merging the pair disagreed with
// each other for the same reason. The fix is not to create the tie.
func TestARepublishIsAlwaysStrictlyNewerThanWhatItReplaces(t *testing.T) {
	id := testIdentity(t)
	post := samplePost("abcd1234")

	first, err := SealPost(id, post)
	if err != nil {
		t.Fatal(err)
	}

	// The collision case: a second publish inside the same second. Without a
	// floor both events carry the same timestamp and relays pick by id.
	second, err := SealPostAfter(id, post, first.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if second.CreatedAt <= first.CreatedAt {
		t.Fatalf("a republish is stamped %d against the %d it replaces, so relays would resolve "+
			"the two by event id rather than by which is newer", second.CreatedAt, first.CreatedAt)
	}

	// It still occupies the same slot — being newer must not make it a different
	// post, or a relay would keep both and the board would show two.
	if tagValue(first, "d") != tagValue(second, "d") {
		t.Fatal("the republish landed in a different slot, so it adds rather than replaces")
	}

	// And a republish that is genuinely later is left alone rather than pushed
	// further out. The floor is a floor, not an increment.
	later := time.Now().Add(time.Hour).Unix()
	third, err := SealPostAfter(id, post, 0)
	if err != nil {
		t.Fatal(err)
	}
	if third.CreatedAt > later {
		t.Fatal("an uncontested publish was stamped into the future")
	}
}

func TestPostExpiryIsJudgedByTheReader(t *testing.T) {
	id := testIdentity(t)
	post := samplePost("abcd1234")
	post.ExpiresAt = post.CreatedAt + int64(MinPostTTL/time.Second)

	ev, err := SealPost(id, post)
	if err != nil {
		t.Fatal(err)
	}
	// The whole point of carrying the deadline in the body as well as in the
	// tag: most relays do not implement NIP-40, so the reader's clock is what
	// actually retires a post.
	listing, err := ReadPost(ev, "mainnet", time.Unix(post.ExpiresAt+1, 0))
	if err != nil {
		t.Fatalf("an expired post should still READ, so it can be shown as expired: %v", err)
	}
	if !listing.Expired {
		t.Fatal("a post past its deadline did not read as expired")
	}
	if listing.Post.Live(time.Unix(post.ExpiresAt+1, 0)) {
		t.Fatal("an expired post is still Live")
	}
	if !listing.Post.Live(time.Unix(post.CreatedAt+1, 0)) {
		t.Fatal("a post inside its window is not Live")
	}
}

func TestPostValidationRefusesIncoherentTerms(t *testing.T) {
	id := testIdentity(t)
	for name, breakIt := range map[string]func(*BoardPost){
		"no amount":         func(p *BoardPost) { p.Give.Amount = "" },
		"negative amount":   func(p *BoardPost) { p.Give.Amount = "-1" },
		"no zenon amount":   func(p *BoardPost) { p.Want.Amount = "  " },
		"negative zenon":    func(p *BoardPost) { p.Want.Amount = "-5" },
		"unknown network":   func(p *BoardPost) { p.Network = "dogecoin" },
		"unknown chain":     func(p *BoardPost) { p.Give.Chain = ChainID("dogecoin") },
		"nonsense role":     func(p *BoardPost) { p.Role = Role("bystander") },
		"nonsense status":   func(p *BoardPost) { p.Status = "haggling" },
		"inverted band":     func(p *BoardPost) { p.Give.Min, p.Give.Max = "900", "100" },
		"amount below band": func(p *BoardPost) { p.Give.Min = "2000000" },
		"amount above band": func(p *BoardPost) { p.Give.Max = "900000" },
		"band on the wrong leg": func(p *BoardPost) {
			p.Want.Min, p.Want.Max = "1000", "2000"
		},
		"expires before set": func(p *BoardPost) { p.ExpiresAt = p.CreatedAt - 1 },
		"essay for a note":   func(p *BoardPost) { p.Note = strings.Repeat("x", 501) },
		"bad zenon address": func(p *BoardPost) {
			p.Addrs = map[string]string{string(ChainZNN): "z1nonsense"}
		},
		"bad zenon token": func(p *BoardPost) { p.Want.Token = "zts1nonsense" },
		"a pair that is not a trade": func(p *BoardPost) {
			p.Give = PostLeg{Chain: ChainBTC, Amount: "1000"}
			p.Want = PostLeg{Chain: ChainBTC, Amount: "2000"}
		},
		"one token against itself": func(p *BoardPost) {
			p.Give = PostLeg{Chain: ChainZNN, Amount: "1"}
			p.Want = PostLeg{Chain: ChainZNN, Amount: "2"}
		},
		"future version": func(p *BoardPost) { p.Version = 3 },
	} {
		t.Run(name, func(t *testing.T) {
			post := samplePost("abcd1234")
			breakIt(&post)
			if _, err := SealPost(id, post); err == nil {
				t.Fatal("this was published")
			}
		})
	}
}

// ---------- wallet proofs ----------

// signBitcoinMessage does what a wallet's signMessage(msg, "ecdsa") does, so the
// verifier is tested against the format it will actually meet rather than
// against itself.
func signBitcoinMessage(t *testing.T, priv *btcec.PrivateKey, message string) string {
	t.Helper()
	hash, err := magicHash(message)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := btcecdsa.SignCompact(priv, hash, true)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

func TestBTCProofAcceptsEveryAddressFormAWalletMightUse(t *testing.T) {
	id := testIdentity(t)
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	params, err := NetworkParams("mainnet")
	if err != nil {
		t.Fatal(err)
	}
	pkh := btcutil.Hash160(priv.PubKey().SerializeCompressed())

	legacy, err := btcutil.NewAddressPubKeyHash(pkh, params)
	if err != nil {
		t.Fatal(err)
	}
	segwit, err := btcutil.NewAddressWitnessPubKeyHash(pkh, params)
	if err != nil {
		t.Fatal(err)
	}
	redeem, err := txscript.PayToAddrScript(segwit)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := btcutil.NewAddressScriptHash(redeem, params)
	if err != nil {
		t.Fatal(err)
	}
	taproot, err := btcutil.NewAddressTaproot(
		schnorr.SerializePubKey(txscript.ComputeTaprootKeyNoScript(priv.PubKey())), params)
	if err != nil {
		t.Fatal(err)
	}

	// One key, four addresses, and a wallet that never says which one it signed
	// with. Refusing three of the four would refuse most wallets — UniSat
	// defaults to taproot.
	for name, addr := range map[string]string{
		"legacy":         legacy.EncodeAddress(),
		"native segwit":  segwit.EncodeAddress(),
		"wrapped segwit": wrapped.EncodeAddress(),
		"taproot":        taproot.EncodeAddress(),
	} {
		t.Run(name, func(t *testing.T) {
			proof := WalletProof{
				Scheme:  ProofBTC,
				Address: addr,
				Sig:     signBitcoinMessage(t, priv, BindStatement(id.PubKey, addr)),
			}
			if err := VerifyProof(proof, id.PubKey, "mainnet"); err != nil {
				t.Fatalf("a genuine proof was refused: %v", err)
			}
		})
	}
}

func TestBTCProofRefusesWhatItShould(t *testing.T) {
	id := testIdentity(t)
	other := testIdentity(t)
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	params, _ := NetworkParams("mainnet")
	mine, err := btcutil.NewAddressWitnessPubKeyHash(
		btcutil.Hash160(priv.PubKey().SerializeCompressed()), params)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := btcutil.NewAddressWitnessPubKeyHash(
		btcutil.Hash160(stranger.PubKey().SerializeCompressed()), params)
	if err != nil {
		t.Fatal(err)
	}
	addr := mine.EncodeAddress()
	good := signBitcoinMessage(t, priv, BindStatement(id.PubKey, addr))

	t.Run("claiming somebody else's address", func(t *testing.T) {
		// The attack the address comparison exists for: a real signature from a
		// real key, attached to an address that key does not hold.
		proof := WalletProof{Scheme: ProofBTC, Address: theirs.EncodeAddress(), Sig: good}
		if err := VerifyProof(proof, id.PubKey, "mainnet"); err == nil {
			t.Fatal("a proof for an address the signer does not hold was accepted")
		}
	})

	t.Run("lifted onto another board key", func(t *testing.T) {
		// Why the statement names the key as well as the address: without it,
		// this signature would bind the same address to anybody's key.
		proof := WalletProof{Scheme: ProofBTC, Address: addr, Sig: good}
		if err := VerifyProof(proof, other.PubKey, "mainnet"); err == nil {
			t.Fatal("a proof made for one board key verified against another")
		}
	})

	t.Run("not a signature at all", func(t *testing.T) {
		for _, sig := range []string{"", "not base64!", base64.StdEncoding.EncodeToString([]byte("short"))} {
			proof := WalletProof{Scheme: ProofBTC, Address: addr, Sig: sig}
			if err := VerifyProof(proof, id.PubKey, "mainnet"); err == nil {
				t.Fatalf("%q was accepted as a signature", sig)
			}
		}
	})

	t.Run("unknown scheme", func(t *testing.T) {
		proof := WalletProof{Scheme: "trust-me", Address: addr, Sig: good}
		if err := VerifyProof(proof, id.PubKey, "mainnet"); err == nil {
			t.Fatal("an unknown proof scheme was accepted")
		}
	})
}

// A signed message is a signature over a hash by a key — no chain appears in it
// anywhere, and only the ENCODING of the address differs between networks. So a
// proof made with a wallet on one network has to verify on a board set to
// another, and refusing them made the feature unusable exactly where it is most
// used: UniSat has no regtest chain at all, so a developer on the regtest
// instance can only ever sign with a mainnet address.
//
// Nothing is given away by accepting it — the statement still names the board
// key and the address, so the proof cannot be lifted onto either.
func TestBTCProofDoesNotCareWhichNetworkTheBoardIsOn(t *testing.T) {
	id := testIdentity(t)
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}

	for _, signedOn := range []string{"mainnet", "testnet", "signet", "regtest"} {
		params, err := NetworkParams(signedOn)
		if err != nil {
			t.Fatal(err)
		}
		wallet, err := btcutil.NewAddressWitnessPubKeyHash(
			btcutil.Hash160(priv.PubKey().SerializeCompressed()), params)
		if err != nil {
			t.Fatal(err)
		}
		addr := wallet.EncodeAddress()
		proof := WalletProof{
			Scheme:  ProofBTC,
			Address: addr,
			Sig:     signBitcoinMessage(t, priv, BindStatement(id.PubKey, addr)),
		}
		// Every board, including the ones this address is not written for.
		for _, boardOn := range []string{"mainnet", "testnet", "signet", "regtest"} {
			if err := VerifyProof(proof, id.PubKey, boardOn); err != nil {
				t.Errorf("a %s address was refused on a %s board: %v", signedOn, boardOn, err)
			}
		}
	}
}

// The Syrius path. Nothing produces one of these yet -- the extension has no way
// to sign a message -- so this test IS the specification: sign the statement's
// raw UTF-8 bytes with the account's ed25519 key, return the signature and the
// public key. If the extension does something else, this is the test that fails
// and verifyZNNProof is the function that changes.
func TestZNNProofIsReadyForTheExtension(t *testing.T) {
	id := testIdentity(t)
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := znn.DeriveAddress(pub)
	if err != nil {
		t.Fatal(err)
	}
	statement := BindStatement(id.PubKey, addr)

	good := WalletProof{
		Scheme:  ProofZNN,
		Address: addr,
		PubKey:  hex.EncodeToString(pub),
		Sig:     hex.EncodeToString(ed25519.Sign(priv, []byte(statement))),
	}
	if err := VerifyProof(good, id.PubKey, "mainnet"); err != nil {
		t.Fatalf("a correctly formed Zenon proof was refused: %v", err)
	}

	t.Run("claiming an address the key does not hold", func(t *testing.T) {
		otherPub, _, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		otherAddr, err := znn.DeriveAddress(otherPub)
		if err != nil {
			t.Fatal(err)
		}
		bad := good
		bad.Address = otherAddr
		if err := VerifyProof(bad, id.PubKey, "mainnet"); err == nil {
			t.Fatal("a proof for somebody else's address was accepted")
		}
	})

	t.Run("signature over a different statement", func(t *testing.T) {
		bad := good
		bad.Sig = hex.EncodeToString(ed25519.Sign(priv, []byte("something else entirely")))
		if err := VerifyProof(bad, id.PubKey, "mainnet"); err == nil {
			t.Fatal("a signature over other text was accepted")
		}
	})

	t.Run("wrong sizes", func(t *testing.T) {
		short := good
		short.PubKey = hex.EncodeToString(pub[:31])
		if err := VerifyProof(short, id.PubKey, "mainnet"); err == nil {
			t.Fatal("a 31-byte public key was accepted")
		}
		stub := good
		stub.Sig = hex.EncodeToString([]byte("nope"))
		if err := VerifyProof(stub, id.PubKey, "mainnet"); err == nil {
			t.Fatal("a 4-byte signature was accepted")
		}
	})
}

// A proof is only a badge if it is for an address the post actually names.
// Otherwise it verifies something true about an address nobody is trading with,
// and the badge would read as if it applied to the one on screen.
func TestReadPostWillNotBadgeAnUnrelatedAddress(t *testing.T) {
	id := testIdentity(t)
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	params, _ := NetworkParams("mainnet")
	proven, err := btcutil.NewAddressWitnessPubKeyHash(
		btcutil.Hash160(priv.PubKey().SerializeCompressed()), params)
	if err != nil {
		t.Fatal(err)
	}
	addr := proven.EncodeAddress()

	post := samplePost("abcd1234")
	// The address in the post is not the address the proof is about.
	post.Addrs = map[string]string{
		string(ChainBTC): "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4",
	}
	post.Proofs = []WalletProof{{
		Scheme:  ProofBTC,
		Address: addr,
		Sig:     signBitcoinMessage(t, priv, BindStatement(id.PubKey, addr)),
	}}

	ev, err := SealPost(id, post)
	if err != nil {
		t.Fatal(err)
	}
	listing, err := ReadPost(ev, "mainnet", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Verified) != 0 {
		t.Fatalf("badged %v for an address the post does not name", listing.Verified)
	}
	if len(listing.Problems) == 0 {
		t.Fatal("no problem reported for a proof about an unrelated address")
	}
}

// ---------- takes ----------

func TestTakeReachesOnlyItsRecipient(t *testing.T) {
	maker := testIdentity(t)
	taker := testIdentity(t)
	eavesdropper := testIdentity(t)

	code, err := NewSessionCode()
	if err != nil {
		t.Fatal(err)
	}
	ev, err := SealTake(taker, maker.PubKey, Take{
		Version: 2,
		PostID:  "abcd1234",
		Code:    code,
		Amount:  "500000",
		Note:    "can do this now",
	})
	if err != nil {
		t.Fatalf("SealTake: %v", err)
	}

	// The code is the room, so the one thing that must never be true is that it
	// is readable off the wire.
	if strings.Contains(ev.Content, NormalizeSessionCode(code)) {
		t.Fatal("the session code is in the event content in the clear")
	}

	got, err := OpenTake(maker, ev)
	if err != nil {
		t.Fatalf("the maker could not open a take addressed to them: %v", err)
	}
	if NormalizeSessionCode(got.Take.Code) != NormalizeSessionCode(code) {
		t.Fatal("the code did not survive the round trip")
	}
	if got.From != taker.PubKey {
		t.Fatalf("take is from %q, want %q", got.From, taker.PubKey)
	}
	if got.Take.Amount != "500000" {
		t.Fatalf("amount is %q, want 500000", got.Take.Amount)
	}

	// Everyone else, including the taker's own copy coming back off a relay.
	if _, err := OpenTake(eavesdropper, ev); err == nil {
		t.Fatal("a third party opened a take addressed to somebody else")
	}
	if _, err := OpenTake(taker, ev); err == nil {
		t.Fatal("the sender opened their own take, which means the `p` check is not holding")
	}
}

func TestTakeRefusesWhatItShould(t *testing.T) {
	maker := testIdentity(t)
	taker := testIdentity(t)
	code, err := NewSessionCode()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("half a code", func(t *testing.T) {
		// A short code derives a room a third party could stumble into, which is
		// exactly what sealing the take was for.
		if _, err := SealTake(taker, maker.PubKey, Take{
			Version: 2, PostID: "abcd1234", Code: "ABCD",
		}); err == nil {
			t.Fatal("a truncated session code was sealed into a take")
		}
	})

	t.Run("no post", func(t *testing.T) {
		if _, err := SealTake(taker, maker.PubKey, Take{
			Version: 2, Code: code,
		}); err == nil {
			t.Fatal("a take that names no post was accepted")
		}
	})

	t.Run("your own post", func(t *testing.T) {
		if _, err := SealTake(taker, taker.PubKey, Take{
			Version: 2, PostID: "abcd1234", Code: code,
		}); err == nil {
			t.Fatal("a take addressed to its own sender was accepted")
		}
	})

	t.Run("tampered content", func(t *testing.T) {
		ev, err := SealTake(taker, maker.PubKey, Take{
			Version: 2, PostID: "abcd1234", Code: code,
		})
		if err != nil {
			t.Fatal(err)
		}
		ev.Content = base64.StdEncoding.EncodeToString([]byte("not a sealed box"))
		if _, err := OpenTake(maker, ev); err == nil {
			t.Fatal("a rewritten take was opened")
		}
	})
}

func TestWithdrawalIsSomethingOnlyTheAuthorCanDo(t *testing.T) {
	author := testIdentity(t)
	del, err := SealDelete(author, "abcd1234")
	if err != nil {
		t.Fatalf("SealDelete: %v", err)
	}
	if del.Kind != deleteKind {
		t.Fatalf("kind is %d, want %d", del.Kind, deleteKind)
	}
	// A relay honours a deletion request only from the key that signed the
	// original, and the `a` coordinate is what names it. Getting that string
	// wrong is a silent no-op, so it is worth pinning.
	want := "30777:" + author.PubKey + ":abcd1234"
	if got := tagValue(del, "a"); got != want {
		t.Fatalf("delete coordinate is %q, want %q", got, want)
	}
	if err := verifyEvent(del); err != nil {
		t.Fatalf("the deletion request does not verify: %v", err)
	}
}

func TestIdentityBindingsHoldOnePerScheme(t *testing.T) {
	id := testIdentity(t)
	id.Bind(WalletProof{Scheme: ProofBTC, Address: "bc1first"})
	id.Bind(WalletProof{Scheme: ProofZNN, Address: "z1first"})
	id.Bind(WalletProof{Scheme: ProofBTC, Address: "bc1second"})

	if len(id.Bindings) != 2 {
		t.Fatalf("holding %d bindings, want 2", len(id.Bindings))
	}
	// "Which Bitcoin address is this person" has one answer, so re-binding
	// replaces rather than accumulates — two would leave a reader picking.
	if got := id.Binding(ProofBTC); got == nil || got.Address != "bc1second" {
		t.Fatalf("re-binding did not replace: %+v", got)
	}
	id.Unbind(ProofBTC)
	if id.Binding(ProofBTC) != nil {
		t.Fatal("unbinding left the proof in place")
	}
	if id.Binding(ProofZNN) == nil {
		t.Fatal("unbinding one scheme removed another")
	}
}

// A post or a take may only name an address this browser has proven, on every
// chain it settles on. The gate in the page is a product guarantee; this is the
// one that survives devtools.
func TestBoardRefusesAnAddressThatIsNotProven(t *testing.T) {
	// Real addresses: publishing validates the shape of each before it gets as
	// far as asking who proved it, so a placeholder would fail the wrong check.
	const btcAddr = "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	znnAddr, err := znn.DeriveAddress(pub)
	if err != nil {
		t.Fatal(err)
	}
	otherPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	otherZnn, err := znn.DeriveAddress(otherPub)
	if err != nil {
		t.Fatal(err)
	}

	// Fresh store per case: an identity's bindings are what is under test, and
	// sharing one would make each case depend on the last.
	setup := func(t *testing.T, bindings ...WalletProof) *API {
		t.Helper()
		api := &API{Store: NewStore(NewMemStorage())}
		id, err := api.Store.Identity()
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range bindings {
			id.Bind(b)
		}
		if err := api.Store.SaveIdentity(id); err != nil {
			t.Fatal(err)
		}
		return api
	}

	publish := func(t *testing.T, api *API, btc, znn string) error {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"role": RoleInitiator,
			"give": map[string]any{"chain": ChainBTC, "amount": "100000"},
			"want": map[string]any{"chain": ChainZNN, "amount": "1200"},
			"addrs": map[string]string{
				string(ChainBTC): btc,
				string(ChainZNN): znn,
			},
			"settings": map[string]any{"network": "mainnet"},
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = handleBoardPublish(t.Context(), api, body)
		return err
	}

	proven := []WalletProof{
		{Scheme: ProofBTC, Address: btcAddr},
		{Scheme: ProofZNN, Address: znnAddr},
	}

	t.Run("nothing proven", func(t *testing.T) {
		err := publish(t, setup(t), btcAddr, znnAddr)
		if err == nil || !strings.Contains(err.Error(), "Bitcoin") {
			t.Fatalf("published with no proof at all: %v", err)
		}
	})

	// Half is not enough for a trade with two legs, and the refusal names the
	// half that is missing rather than asking again for the one already held.
	t.Run("only the Bitcoin half proven", func(t *testing.T) {
		err := publish(t, setup(t, proven[0]), btcAddr, znnAddr)
		if err == nil || !strings.Contains(err.Error(), "Zenon") {
			t.Fatalf("published with no Zenon proof: %v", err)
		}
		if strings.Contains(err.Error(), "Bitcoin") {
			t.Fatalf("the refusal asks for a proof already held: %v", err)
		}
	})

	// A proof for a different address of the same chain is not a proof of this
	// one — the exact case somebody who switched wallet accounts lands in.
	t.Run("proven, but for another address", func(t *testing.T) {
		api := setup(t, proven[0], WalletProof{Scheme: ProofZNN, Address: otherZnn})
		err := publish(t, api, btcAddr, znnAddr)
		if err == nil || !strings.Contains(err.Error(), otherZnn) {
			t.Fatalf("published against a proof for another address: %v", err)
		}
	})

	t.Run("both proven", func(t *testing.T) {
		if err := publish(t, setup(t, proven...), btcAddr, znnAddr); err != nil {
			t.Fatalf("a fully proven offer was refused: %v", err)
		}
	})

	// The same rule from the taker's end, where the addresses travel sealed
	// rather than in the open. Sealing is not proving.
	t.Run("taking", func(t *testing.T) {
		body := func(btc, znn string) []byte {
			raw, err := json.Marshal(map[string]any{
				"author": strings.Repeat("ab", 32),
				"postId": "p1",
				"chains": []ChainID{ChainBTC, ChainZNN},
				"addrs": map[string]string{
					string(ChainBTC): btc,
					string(ChainZNN): znn,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			return raw
		}
		if _, err := handleBoardTake(t.Context(), setup(t), body(btcAddr, znnAddr)); err == nil {
			t.Fatal("a take carrying unproven addresses was sealed")
		}
		api := setup(t, proven...)
		if _, err := handleBoardTake(t.Context(), api, body(btcAddr, znnAddr)); err != nil {
			t.Fatalf("a fully proven take was refused: %v", err)
		}
	})
}

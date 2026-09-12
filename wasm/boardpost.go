package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"

	"github.com/zenon/ferry-web-v2/wasm/sol"
	"github.com/zenon/ferry-web-v2/wasm/znn"
)

// One offer, its wire form, and the two things anybody does with it: publish
// one, or read somebody else's.
//
// The asymmetry between those two is the whole file. Publishing is a matter of
// filling in fields and signing them. READING is where every check lives,
// because a post is a document written by a stranger and handed over by a relay
// that is also a stranger -- so ReadPost re-derives the event id, re-checks the
// signature, re-verifies the wallet proofs against the network the reader is on,
// and refuses anything it cannot make sense of. A post that arrives is a claim;
// a post that comes back out of ReadPost is a claim with a name attached.

// BoardPost is an offer as it appears on the board.
//
// Deliberately NOT an Offer (swap.go). An Offer pins the terms of a swap that
// already exists -- it carries a secret hash and a pubkey hash, neither of which
// exists before somebody has created a swap -- and it is checked field by field
// against a create. A board post is the advertisement BEFORE that: it names what
// somebody wants to trade and nothing about how. Reusing Offer here would have
// meant inventing a secret hash for a swap nobody has agreed to, and then having
// CheckCreate compare against it.
//
// The two meet later, and through the existing path: a taker sends a session
// code, both sides join that session, and the maker's real swapoffer1 travels
// over it and lands in the create form through the same decoder a pasted one
// does. Nothing on the board becomes a swap term without passing that check.
type BoardPost struct {
	Version int `json:"v"`
	// ID is the `d` tag: the addressable slot this post occupies under its
	// author's key. Editing a post means republishing under the same ID, so this
	// is the identity of the OFFER rather than of one version of it.
	ID string `json:"id"`
	// Network the trade would happen on. A board is per network -- a mainnet
	// offer on a signet board is not a cheap offer, it is a mistake waiting.
	Network string `json:"network"`

	// Give is what the AUTHOR funds and Want is what they receive. A taker takes
	// the mirror. Stated as the author's two legs rather than as "buy"/"sell",
	// because "buying" names no asset without a convention and both sides of
	// this trade are currencies -- and because with four pairs there is no
	// longer a chain that could be the implied one.
	Give PostLeg `json:"give"`
	Want PostLeg `json:"want"`

	// Role the author intends to take. Whoever initiates invents the secret and
	// gets the longer timelock, so it is a term, not a preference.
	Role Role `json:"role"`

	// LockHours the author proposes for their own leg. Advisory: what actually
	// protects the swap is auditLegOrdering against the real contract, not a
	// number in an advertisement.
	LockHours int `json:"lockHours,omitempty"`

	// Addrs are the addresses the author is willing to advertise, keyed by
	// chain. All optional, and none is needed to trade -- the addresses that
	// matter are exchanged inside the swap. They are here because a bound wallet
	// proof has to name one, and because a counterparty who can see where the
	// money will come from can look it up.
	Addrs map[string]string `json:"addrs,omitempty"`

	// Proofs bind the addresses above to the key that signed this post. Absent
	// is normal and is shown as "unverified" rather than hidden.
	Proofs []WalletProof `json:"proofs,omitempty"`

	Status    string `json:"status"`
	Note      string `json:"note,omitempty"`
	CreatedAt int64  `json:"createdAt"`
	// ExpiresAt is duplicated into the NIP-40 `expiration` tag. Both, because
	// they do different jobs: the tag asks relays to drop the event, and this
	// field lets a reader whose relay ignored the tag drop it anyway. The
	// second is the one that is load-bearing -- expiration support is optional
	// and most relays do not have it.
	ExpiresAt int64 `json:"expiresAt"`

	// Completed is the author's own count of swaps they have finished. It is
	// self-reported, unverifiable, and trivially inflated, and it is carried
	// because people will ask for it -- shown labelled as the claim it is,
	// never as a score. Nothing in this app reads it.
	Completed int `json:"completed,omitempty"`
}

// PostLeg is one half of an advertised trade.
//
// Amounts are text, for the reason normalDecimal exists: a number that has been
// through a float is a number that has changed, and a ZTS may carry more
// precision than a float has.
type PostLeg struct {
	Chain ChainID `json:"chain"`
	// Token is the ZTS on a Zenon leg. Blank means ZNN.
	Token string `json:"token,omitempty"`
	// Amount is the headline size, in the chain's own unit.
	Amount string `json:"amount"`
	// Min and Max, when set, say the author will do any size in that band -- so
	// a taker names an amount and the two settle on one before a swap exists.
	// Blank means the amount is the amount. Only meaningful on Give: a band on
	// both halves would be a band on the RATE, which is a different offer.
	Min string `json:"min,omitempty"`
	Max string `json:"max,omitempty"`
}

// Addr returns the author's advertised address on one chain.
func (p BoardPost) Addr(c ChainID) string {
	if p.Addrs == nil {
		return ""
	}
	return strings.TrimSpace(p.Addrs[string(c)])
}

// Chains is every chain this offer settles on, deduplicated and in ChainOrder.
//
// The board's rule is per chain rather than per app: you may act on an offer
// once you have PROVEN an address on every chain it settles on. Derived from the
// legs rather than asserted beside them, because a stored list is a second thing
// that can disagree with the two amounts it summarises.
func (p BoardPost) Chains() []ChainID { return chainsOf(p.Give.Chain, p.Want.Chain) }

// Pair names this offer's trade in the stable order Pairs declares.
func (p BoardPost) Pair() string { return PairID(p.Give.Chain, p.Want.Chain) }

// Label renders one leg for a row: "10 ZNN", "400000 sat", "1.5 SOL".
func (l PostLeg) Label() string {
	switch l.Chain {
	case ChainBTC:
		return strings.TrimSpace(l.Amount) + " sat"
	case ChainSOL:
		return strings.TrimSpace(l.Amount) + " SOL"
	case ChainZNN:
		return strings.TrimSpace(l.Amount) + " " + TokenShortName(l.Token)
	}
	return strings.TrimSpace(l.Amount)
}

// validate refuses a leg that could not describe half a trade.
func (l PostLeg) validate(what string) error {
	if _, err := ChainOf(l.Chain); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if strings.TrimSpace(l.Amount) == "" {
		return fmt.Errorf("%s names no amount -- it is half the trade", what)
	}
	if err := checkTypedAmount(l.Chain, l.Token, l.Amount); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if l.Chain == ChainZNN && l.Token != "" {
		if _, err := znn.ParseTokenStandard(l.Token); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
	}
	// A band that does not contain its own headline amount is a post whose two
	// halves disagree, and a taker would have no way to tell which one is meant.
	for _, b := range []struct {
		name, value string
	}{{"smallest size", l.Min}, {"largest size", l.Max}} {
		if strings.TrimSpace(b.value) == "" {
			continue
		}
		if err := checkTypedAmount(l.Chain, l.Token, b.value); err != nil {
			return fmt.Errorf("%s: the %s: %w", what, b.name, err)
		}
	}
	min, max := decimalOrder(l.Min), decimalOrder(l.Max)
	amount := decimalOrder(l.Amount)
	if min != nil && max != nil && min.Cmp(max) > 0 {
		return fmt.Errorf("%s: the smallest size (%s) is larger than the largest (%s)",
			what, l.Min, l.Max)
	}
	if min != nil && amount != nil && amount.Cmp(min) < 0 {
		return fmt.Errorf("%s: the amount (%s) is below this post's own minimum (%s)",
			what, l.Amount, l.Min)
	}
	if max != nil && amount != nil && amount.Cmp(max) > 0 {
		return fmt.Errorf("%s: the amount (%s) is above this post's own maximum (%s)",
			what, l.Amount, l.Max)
	}
	return nil
}

// decimalOrder turns a typed amount into something comparable, at a fixed and
// generous scale.
//
// Fixed rather than the token's own, because a board post is read by browsers
// that may have no node for the chain in question -- a size band is a display
// and ordering question, not a settlement one, and refusing to check it until a
// node answers would be refusing to check it at all. Eighteen places is beyond
// any ZTS and beyond lamports; anything that does not fit is left uncompared
// rather than guessed at.
func decimalOrder(s string) *big.Int {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	v, err := decimalToBaseUnits(normalDecimal(s), 18)
	if err != nil {
		return nil
	}
	return v
}

// forWire returns the copy that gets published: local bookkeeping cleared.
//
// VerifiedAt is when THIS browser checked a proof, which says nothing to anybody
// else -- a reader re-verifies rather than believing a timestamp a stranger
// wrote. Publishing it would be publishing a field that means nothing and could
// be read as if it meant something.
func (p BoardPost) forWire() BoardPost {
	out := p
	out.Proofs = make([]WalletProof, len(p.Proofs))
	for i, proof := range p.Proofs {
		proof.VerifiedAt = time.Time{}
		out.Proofs[i] = proof
	}
	if len(out.Proofs) == 0 {
		out.Proofs = nil
	}
	return out
}

// Expired reports whether this post is past its own deadline.
func (p BoardPost) Expired(now time.Time) bool {
	return p.ExpiresAt > 0 && now.Unix() >= p.ExpiresAt
}

// Live reports whether a post should appear on the open board: still standing,
// and not past its deadline.
func (p BoardPost) Live(now time.Time) bool {
	return p.Status == StatusOpen && !p.Expired(now)
}

// Standing reports whether this post is still out there being acted on -- open
// to takers, or taken and mid-trade -- as opposed to retracted, finished, or
// past its deadline.
//
// Wider than Live by exactly one status, and the difference is the point.
// StatusTaken is not on the open board, so it is not Live; but the offer is
// still on every relay, its author is still in a session about it, and takes
// addressed to it still arrive. Anything asking "is there still something out
// there depending on this record" -- which is what DeleteMyPost asks -- has to
// count it.
func (p BoardPost) Standing(now time.Time) bool {
	return (p.Status == StatusOpen || p.Status == StatusTaken) && !p.Expired(now)
}

// NewPostID returns a fresh addressable slot.
func NewPostID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// validate refuses a post that could not describe a trade.
//
// Run on the way OUT as well as on the way in, and that is deliberate: a post
// this app would refuse to read is a post it has no business publishing, and
// having one function decide both is what keeps the two from drifting apart. A
// stranger's post fails the same checks the author's did.
//
// It is about coherence, not about whether the price is sensible. Nothing here
// is in a position to know what a fair rate is, and a board that quietly hid
// offers it thought were bad would be a board with an opinion.
func (p BoardPost) validate() error {
	if p.Version != 2 {
		return fmt.Errorf("this is a version %d board post and this build reads version 2 — "+
			"version 1 posts came from the Bitcoin-only build and do not say which chain each "+
			"half is on", p.Version)
	}
	if len(p.ID) == 0 || len(p.ID) > 64 {
		return errors.New("a board post needs an id of 1..64 characters")
	}
	for _, r := range p.ID {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return errors.New("a board post id is hex")
		}
	}
	if _, err := NetworkParams(p.Network); err != nil {
		return err
	}
	if p.Role != RoleInitiator && p.Role != RoleParticipant {
		return fmt.Errorf("%q is not a role: it is %q or %q", p.Role, RoleInitiator, RoleParticipant)
	}
	if !knownStatus(p.Status) {
		return fmt.Errorf("%q is not a board post status", p.Status)
	}
	if err := p.Give.validate("the leg they fund"); err != nil {
		return err
	}
	if err := p.Want.validate("the leg they receive"); err != nil {
		return err
	}
	if err := CheckPair(p.Give.Chain, p.Want.Chain, p.Give.Token, p.Want.Token); err != nil {
		return fmt.Errorf("this post is not a trade: %w", err)
	}
	// A band belongs on the size being offered, not on the one being asked for:
	// a band on both is a band on the rate, which is a different offer and one
	// nothing downstream knows how to settle.
	if strings.TrimSpace(p.Want.Min) != "" || strings.TrimSpace(p.Want.Max) != "" {
		return errors.New("a size band goes on the leg the author funds, not on the one they " +
			"receive — a band on both halves would be a band on the rate")
	}
	// Every advertised address has to be an address on the chain it is filed
	// under. An address in the wrong slot is the shape of accident a paste
	// between similar-looking fields produces, and it would be published as a
	// claim about where somebody's money should go.
	for name, addr := range p.Addrs {
		if strings.TrimSpace(addr) == "" {
			continue
		}
		if err := checkChainAddr(ChainID(name), addr); err != nil {
			return err
		}
	}
	if p.LockHours < 0 || p.LockHours > 720 {
		return errors.New("a lock of more than 30 days is not a swap, it is a donation")
	}
	// Bounded so one post cannot be used to push a megabyte through every relay
	// on the list. Generous enough for a real note and nothing like enough for
	// an essay.
	if len(p.Note) > 500 {
		return fmt.Errorf("that note is %d characters and the limit is 500", len(p.Note))
	}
	if p.CreatedAt <= 0 || p.ExpiresAt <= 0 {
		return errors.New("a board post needs a created and an expiry time")
	}
	if p.ExpiresAt <= p.CreatedAt {
		return errors.New("that post expires before it was written")
	}
	if p.Completed < 0 {
		return errors.New("a completed count cannot be negative")
	}
	return nil
}

// checkChainAddr refuses an address that is not one on the chain it claims.
//
// Bitcoin is checked only for shape rather than against a network: a board post
// is read by browsers on any network, and the one thing worth refusing here is
// an address that belongs to a different CHAIN. Whether it is mainnet or signet
// is settled where it matters — by the code that builds a transaction to it.
func checkChainAddr(c ChainID, addr string) error {
	addr = strings.TrimSpace(addr)
	switch c {
	case ChainZNN:
		if _, err := znn.ParseAddress(addr); err != nil {
			return fmt.Errorf("the Zenon address on this post: %w", err)
		}
	case ChainSOL:
		if _, err := sol.ParsePubkey(addr); err != nil {
			return fmt.Errorf("the Solana address on this post: %w", err)
		}
	case ChainBTC:
		if len(addr) < 14 || len(addr) > 100 {
			return fmt.Errorf("%q is not a Bitcoin address", addr)
		}
	default:
		return fmt.Errorf("this post files an address under %q, which is not a chain this "+
			"build knows", c)
	}
	return nil
}

// ---------- events ----------

// signEvent fills in the id and signature. The id is COMPUTED, never taken from
// the caller: it is what the signature commits to, so accepting one would make
// the signature a commitment to whatever somebody claimed.
func signEvent(priv *btcec.PrivateKey, ev *NostrEvent) error {
	ev.PubKey = hex.EncodeToString(schnorr.SerializePubKey(priv.PubKey()))
	id, err := ev.eventID()
	if err != nil {
		return err
	}
	sig, err := schnorr.Sign(priv, id[:])
	if err != nil {
		return fmt.Errorf("sign board event: %w", err)
	}
	ev.ID = hex.EncodeToString(id[:])
	ev.Sig = hex.EncodeToString(sig.Serialize())
	return nil
}

// verifyEvent checks an event against the key it claims to be from.
//
// The difference from OpenSession is which key is authoritative. A session knows
// the key before it reads the event -- the code derives it -- so an event under
// any other key is an intruder. A board post arrives from somebody the reader
// has never heard of, so the event's own pubkey is the only candidate, and what
// is being established is not "this is who I expected" but "whoever this is,
// they wrote it and nobody edited it".
//
// That is enough for the property the board needs. It does not matter who the
// author is; it matters that only the author can edit or withdraw the post, and
// that is exactly what a signature over a recomputed id gives.
func verifyEvent(ev *NostrEvent) error {
	if ev == nil {
		return errors.New("no event")
	}
	want, err := ev.eventID()
	if err != nil {
		return err
	}
	// Recomputed rather than believed, for the reason OpenSession gives: the id
	// is what the signature is over, so taking the relay's copy of it would make
	// the signature check a check on nothing.
	if !strings.EqualFold(ev.ID, hex.EncodeToString(want[:])) {
		return errors.New("that event's id does not match its own contents, so it has been " +
			"altered since it was signed")
	}
	pubRaw, err := hex.DecodeString(ev.PubKey)
	if err != nil {
		return fmt.Errorf("that event's public key is not hex: %w", err)
	}
	pub, err := schnorr.ParsePubKey(pubRaw)
	if err != nil {
		return fmt.Errorf("that event's public key is not usable: %w", err)
	}
	sigRaw, err := hex.DecodeString(ev.Sig)
	if err != nil {
		return fmt.Errorf("that event's signature is not hex: %w", err)
	}
	sig, err := schnorr.ParseSignature(sigRaw)
	if err != nil {
		return fmt.Errorf("that event's signature is malformed: %w", err)
	}
	if !sig.Verify(want[:], pub) {
		return errors.New("that event's signature does not verify, so it was not written by " +
			"the key it names")
	}
	return nil
}

// tag returns the first value of the named tag, or "".
func tagValue(ev *NostrEvent, name string) string {
	for _, t := range ev.Tags {
		if len(t) >= 2 && t[0] == name {
			return t[1]
		}
	}
	return ""
}

// SealPost renders a post as the signed event to publish.
//
// The tags are not decoration. `d` is what makes the event addressable, and
// therefore what makes an edit an edit; `t` is the only thing a reader needs to
// know to find the board; `n` lets a relay filter a network out before it sends
// it; `s` lets one ask for open posts only. All four are single-letter, because
// those are the tags relays index.
func SealPost(identity *BoardIdentity, post BoardPost) (*NostrEvent, error) {
	return SealPostAfter(identity, post, 0)
}

// SealPostAfter is SealPost with a floor under the event's timestamp: the
// version this replaces, so the new one is guaranteed to be strictly newer.
//
// Without it, two versions of one post can share a `created_at`, and a second is
// not a hard window to hit -- edit and immediately withdraw, or renew a post
// twice. NIP-01 resolves a replaceable-event tie by keeping the event with the
// LOWEST id, which is a coin flip over a hash: roughly half the time a relay
// answers a withdrawal by keeping the offer and discarding the retraction. The
// author sees their own board update (their store was written locally) while
// everybody else keeps being offered a trade that was taken back.
//
// Ties also make readers disagree with each other, since two clients merging the
// same pair from relays that answered in different orders had no rule that
// picked the same winner -- see `accept` in useBoard.ts, which now mirrors
// NIP-01 for the same reason. But that is the smaller half: a tie broken
// consistently is still a withdrawal that may lose. Not creating the tie is what
// actually fixes it, and it costs one second on the rare republish that would
// have collided.
func SealPostAfter(identity *BoardIdentity, post BoardPost, notBefore int64) (*NostrEvent, error) {
	if err := post.validate(); err != nil {
		return nil, err
	}
	priv, err := identity.PrivKey()
	if err != nil {
		return nil, err
	}
	content, err := json.Marshal(post.forWire())
	if err != nil {
		return nil, err
	}
	createdAt := time.Now().Unix()
	if createdAt <= notBefore {
		createdAt = notBefore + 1
	}
	ev := &NostrEvent{
		CreatedAt: createdAt,
		Kind:      boardKind,
		Tags: [][]string{
			{"d", post.ID},
			{"t", boardTag()},
			{"n", post.Network},
			{"s", post.Status},
			{"expiration", fmt.Sprint(post.ExpiresAt)},
		},
		Content: string(content),
	}
	if err := signEvent(priv, ev); err != nil {
		return nil, err
	}
	return ev, nil
}

// SealDelete builds the NIP-09 request to forget a post.
//
// Sent alongside the void replacement rather than instead of it, because a
// deletion request is a request: a relay may honour it, ignore it, or have
// never implemented it. The replacement is what actually works everywhere, and
// this is what tidies up on the relays that do more.
func SealDelete(identity *BoardIdentity, postID string) (*NostrEvent, error) {
	priv, err := identity.PrivKey()
	if err != nil {
		return nil, err
	}
	ev := &NostrEvent{
		CreatedAt: time.Now().Unix(),
		Kind:      deleteKind,
		Tags: [][]string{
			// The addressable coordinate, which is how NIP-09 names a
			// replaceable event: kind:pubkey:d.
			{"a", fmt.Sprintf("%d:%s:%s", boardKind, identity.PubKey, postID)},
			{"k", fmt.Sprint(boardKind)},
		},
		Content: "withdrawn by its author",
	}
	if err := signEvent(priv, ev); err != nil {
		return nil, err
	}
	return ev, nil
}

// Listing is a post as a reader gets it: the post, who wrote it, and what
// survived checking.
type Listing struct {
	Post BoardPost `json:"post"`
	// Author is the x-only key that signed it. The only durable identity on the
	// board, and what a taker seals a session code to.
	Author string `json:"author"`
	// EventID and PublishedAt are the relay's copy of when this version appeared.
	// Useful for showing "edited 4 minutes ago" and for nothing else -- an author
	// writes their own clock and can write any value into it.
	EventID     string `json:"eventId"`
	PublishedAt int64  `json:"publishedAt"`
	// Verified names the schemes whose proofs actually checked out, here, against
	// this reader's network. A proof that failed is not listed and its failure is
	// in Problems -- a badge that appears because a field was present would be
	// worse than no badge.
	Verified []string `json:"verified,omitempty"`
	// Problems are the things wrong with a post that did not make it unreadable:
	// a proof that failed, an address that does not parse. Shown, because a post
	// carrying a broken proof of somebody else's address is the single most
	// interesting thing that can appear on a board.
	Problems []string `json:"problems,omitempty"`
	// Mine is set by the caller side, not by this file: whether the author key is
	// this browser's own.
	Mine bool `json:"mine"`
	// Expired is computed at read time against the reader's clock, so a relay
	// that ignores NIP-40 cannot keep a dead post on the board.
	Expired bool `json:"expired"`
}

// ReadPost verifies one event off a relay and returns what it says.
//
// The order matters. Signature first, because an unsigned document has no author
// and there is nothing to attribute anything to. Then shape, so a post that
// cannot describe a trade is refused rather than displayed with blanks. Then the
// proofs, which are the only part that can fail WITHOUT the post being refused:
// a bad proof means an unverified post, not an invalid one, and hiding it would
// hide the evidence that somebody is claiming an address they do not hold.
func ReadPost(ev *NostrEvent, network string, now time.Time) (*Listing, error) {
	if err := verifyEvent(ev); err != nil {
		return nil, err
	}
	if ev.Kind != boardKind {
		return nil, fmt.Errorf("event kind %d is not a ferry board post", ev.Kind)
	}
	var post BoardPost
	if err := json.Unmarshal([]byte(ev.Content), &post); err != nil {
		return nil, fmt.Errorf("that post is not readable: %w", err)
	}
	if err := post.validate(); err != nil {
		return nil, err
	}
	// The `d` tag is what a relay keys the post by, so a post whose body claims a
	// different id is one that would be edited by republishing under an id the
	// relay does not use -- a post nobody, including its author, could withdraw.
	if d := tagValue(ev, "d"); d != post.ID {
		return nil, fmt.Errorf("that post is filed under %q but calls itself %q", d, post.ID)
	}
	// Not a mismatch worth refusing over -- the body is what is signed and the
	// body wins -- but a reader filtering by network at the relay would never
	// have seen it, so the two disagreeing is worth saying out loud.
	listing := &Listing{
		Post:        post,
		Author:      ev.PubKey,
		EventID:     ev.ID,
		PublishedAt: ev.CreatedAt,
		Expired:     post.Expired(now),
	}
	if n := tagValue(ev, "n"); n != "" && n != post.Network {
		listing.Problems = append(listing.Problems,
			fmt.Sprintf("this post is tagged for %s but its terms say %s", n, post.Network))
	}
	if post.Network != network {
		listing.Problems = append(listing.Problems,
			fmt.Sprintf("this post is for %s and you are on %s", post.Network, network))
	}

	for _, proof := range post.Proofs {
		if err := VerifyProof(proof, ev.PubKey, post.Network); err != nil {
			listing.Problems = append(listing.Problems,
				fmt.Sprintf("its %s proof does not hold: %v", proof.Scheme, err))
			continue
		}
		// A proof for an address the post does not otherwise mention proves
		// something true about an address nobody is trading with. Refused as a
		// badge, because the badge would read as "this post's address is
		// verified".
		chain := ChainForScheme(proof.Scheme)
		if chain == "" {
			listing.Problems = append(listing.Problems,
				fmt.Sprintf("it carries a %s proof, which is not a scheme this build knows",
					proof.Scheme))
			continue
		}
		if post.Addr(chain) != proof.Address {
			listing.Problems = append(listing.Problems,
				fmt.Sprintf("its %s proof is for an address the post does not name",
					ChainLabel(chain)))
			continue
		}
		listing.Verified = append(listing.Verified, proof.Scheme)
	}
	return listing, nil
}

// ---------- presence ----------

// Presence is a beat: one key, alive at one moment, signed.
//
// The content is a version and a network and nothing else, and the emptiness is
// the point -- the payload of a beat is its `created_at` and its signature.
// Anything more would be an author describing their own liveness, which is the
// one thing a reader must not take from them.
type Presence struct {
	Version int `json:"v"`
	// Network scopes a beat the way it scopes a post. A board is per network,
	// and presence answers a question asked about a row on one of them.
	Network string `json:"network"`
}

// SealPresence signs one beat.
//
// No amount of state travels with it, so this needs nothing from the store: an
// identity and a clock are the whole input. It is called on a timer, which is
// the other reason it stays this cheap.
func SealPresence(identity *BoardIdentity, network string) (*NostrEvent, error) {
	if _, err := NetworkParams(network); err != nil {
		return nil, err
	}
	priv, err := identity.PrivKey()
	if err != nil {
		return nil, err
	}
	content, err := json.Marshal(Presence{Version: 1, Network: network})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	ev := &NostrEvent{
		CreatedAt: now.Unix(),
		Kind:      presenceKind,
		Tags: [][]string{
			// One slot per key: a beat replaces the last one rather than piling
			// up beside it. See presenceSlot.
			{"d", presenceSlot},
			{"t", boardTag()},
			{"n", network},
			{"expiration", fmt.Sprint(now.Add(PresenceTTL).Unix())},
		},
		Content: string(content),
	}
	if err := signEvent(priv, ev); err != nil {
		return nil, err
	}
	return ev, nil
}

// Seen is a verified beat: whose it was, and when they were last at a keyboard.
//
// Deliberately NOT a boolean. Whether a key is online is a function of the
// reader's clock and changes every second without anything arriving, so a
// boolean decided here would be a fact that silently rots -- the same reason
// Listing.Expired is recomputed rather than trusted, and the same reason the
// page re-judges it on a timer. What Go settles is the half that cannot be
// recomputed: that this beat is genuine, is for this network, and is not stamped
// in the future.
type Seen struct {
	// Author is the x-only key that signed the beat.
	Author string `json:"author"`
	// SeenAt is the author's own clock, checked against the reader's for skew
	// but not otherwise adjusted. Seconds, like every other Nostr timestamp.
	SeenAt int64 `json:"seenAt"`
	// StaleAfter is how many seconds a beat means "online" for, so the page
	// applies the same window this file does rather than a second copy of the
	// number. See PresenceStale.
	StaleAfter int64 `json:"staleAfter"`
}

// ReadPresence verifies one beat off a relay.
//
// Everything ReadPost checks and one thing it does not: how the timestamp sits
// against the reader's clock. A post's `created_at` decides only which of two
// versions of the same post wins, and both were signed by the same key -- so a
// forward-dated one can suppress nothing but its author's own earlier work. A
// BEAT's timestamp is the entire claim, and a key stamped an hour ahead would
// read as online for an hour. See PresenceSkew for why that is refused rather
// than clamped.
func ReadPresence(ev *NostrEvent, network string, now time.Time) (*Seen, error) {
	if err := verifyEvent(ev); err != nil {
		return nil, err
	}
	if ev.Kind != presenceKind {
		return nil, fmt.Errorf("event kind %d is not a ferry presence beat", ev.Kind)
	}
	if d := tagValue(ev, "d"); d != presenceSlot {
		return nil, fmt.Errorf("that beat is filed under %q rather than %q", d, presenceSlot)
	}
	var p Presence
	if err := json.Unmarshal([]byte(ev.Content), &p); err != nil {
		return nil, fmt.Errorf("that beat is not readable: %w", err)
	}
	if p.Version != 1 {
		return nil, fmt.Errorf("this is a version %d presence beat and this build reads version 1",
			p.Version)
	}
	// Dropped rather than shown as a problem the way a post's network mismatch
	// is. A beat has nothing to display beside it -- it is one bit against
	// somebody else's row -- so the only thing to do with one from another board
	// is not to let it answer for this one.
	if p.Network != network {
		return nil, fmt.Errorf("that beat is for %s and you are on %s", p.Network, network)
	}
	if ev.CreatedAt > now.Add(PresenceSkew).Unix() {
		return nil, errors.New("that beat is stamped in the future, so it cannot say when its " +
			"author was last here")
	}
	// Ahead of this clock but within tolerance: an ordinary machine that has not
	// synced, not a claim worth refusing. Taken as "now" rather than at face
	// value, because a beat stamped a minute ahead would otherwise sit inside
	// the staleness window a minute longer than any beat should — quietly adding
	// the skew to PresenceStale for that author alone. The window has to mean
	// the same thing for everybody or it means nothing.
	seenAt := ev.CreatedAt
	if seenAt > now.Unix() {
		seenAt = now.Unix()
	}
	return &Seen{
		Author:     ev.PubKey,
		SeenAt:     seenAt,
		StaleAfter: int64(PresenceStale.Seconds()),
	}, nil
}

// ---------- takes ----------

// Take is a reader saying "this one, and here is where to meet".
//
// The session code is the whole payload and the reason this is sealed. A code is
// the room: session.go derives the publishing identity and the encryption key
// from it, so anyone holding one can read that conversation and post into it.
// Publishing a code on a public board would hand every reader the room.
//
// So the board advertises a KEY, and a taker mints a fresh code and seals it to
// that key. Every taker gets a room of their own, the author picks one, and a
// code is never in public. It also gives the author something a public code
// could not: several takes to choose between, which is what a board is for.
type Take struct {
	Version int `json:"v"`
	// PostID is the `d` of the post being taken, so an author with several posts
	// knows which one this is about.
	PostID string `json:"postId"`
	// Code is a session code the TAKER minted. Minted by the taker rather than
	// the author because the author may be asleep: a take has to be actionable
	// the moment it is read.
	Code string `json:"code"`
	// Amount is what the taker wants to do, within the post's band, in the unit
	// of the leg its author funds. Absent means the post's headline amount.
	Amount string `json:"amount,omitempty"`

	// Addrs is where the TAKER wants each half of the trade paid, keyed by
	// chain.
	//
	// They travel here because this is the first moment the author could
	// possibly learn them, and because a take is the only leg of the
	// introduction that is private: a post is a notice in a square and carries
	// its author's addresses in the clear, but a taker is answering one
	// stranger, and sealing means only that stranger reads where their money
	// goes. Publishing a take in the open would put both.
	//
	// A Zenon or Solana address is one a swap cannot be built without — both
	// contracts are created TO an address, so the author needs the taker's
	// before they can make their leg. A Bitcoin address is the taker's own
	// payout and the author never spends to it; it is carried so each side can
	// see the whole shape of what they are agreeing to, and so neither has to
	// paste an address into a chat window to be asked for it later.
	Addrs map[string]string `json:"addrs,omitempty"`

	// Note is free text. Shown as text, never parsed.
	Note string `json:"note,omitempty"`
	// SentAt is the taker's clock, for ordering an inbox.
	SentAt int64 `json:"sentAt,omitempty"`
}

func (t Take) validate() error {
	if t.Version != 2 {
		return fmt.Errorf("this is a version %d take and this build reads version 2", t.Version)
	}
	if t.PostID == "" {
		return errors.New("a take has to name the post it is taking")
	}
	// Normalised and length-checked here rather than at join time, so a take
	// carrying half a code is refused at the door instead of opening a room
	// nobody is in.
	if len(NormalizeSessionCode(t.Code)) < 16 {
		return errors.New("that take carries no usable session code")
	}
	if a := strings.TrimSpace(t.Amount); a != "" && strings.ContainsAny(a, "-eE ") {
		return fmt.Errorf("%q is not an amount a take can ask for", a)
	}
	// Every address checked against the chain it is filed under. An address that
	// is present and unparseable is not a cosmetic problem: it reaches the
	// author as a destination they might try to pay.
	for name, addr := range t.Addrs {
		if strings.TrimSpace(addr) == "" {
			continue
		}
		if len(addr) > 128 {
			return errors.New("that take carries an address far longer than any chain writes")
		}
		if err := checkChainAddr(ChainID(name), addr); err != nil {
			return err
		}
	}
	if len(t.Note) > 500 {
		return fmt.Errorf("that note is %d characters and the limit is 500", len(t.Note))
	}
	return nil
}

// boardShared derives the secret two board keys share.
//
// ECDH on secp256k1, with the x coordinate hashed under a label. The y parity of
// the peer's key is not known -- Nostr keys are x-only -- and does not need to
// be: negating a point leaves its x coordinate alone, so both sides land on the
// same value whichever parity the stored key had. NIP-04 rests on the same fact.
func boardShared(priv *btcec.PrivateKey, peerXOnly string) ([32]byte, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(peerXOnly))
	if err != nil {
		return [32]byte{}, fmt.Errorf("that board key is not hex: %w", err)
	}
	pub, err := schnorr.ParsePubKey(raw)
	if err != nil {
		return [32]byte{}, fmt.Errorf("that board key is not a usable public key: %w", err)
	}
	var point, product btcec.JacobianPoint
	pub.AsJacobian(&point)
	btcec.ScalarMultNonConst(&priv.Key, &point, &product)
	product.ToAffine()
	x := product.X.Bytes()
	return sha256.Sum256(append([]byte(boardECDHLabel+":"), x[:]...)), nil
}

// SealTake encrypts a take to the post's author and signs it under the taker's
// board key.
//
// Signed as well as encrypted, and the signature is the useful half for the
// author: it says which key sent this, which is the same key whose posts and
// history they can look up. An anonymous take would be a session code from
// nobody.
func SealTake(identity *BoardIdentity, authorPubKey string, take Take) (*NostrEvent, error) {
	if take.SentAt == 0 {
		take.SentAt = time.Now().Unix()
	}
	take.Code = NormalizeSessionCode(take.Code)
	if err := take.validate(); err != nil {
		return nil, err
	}
	priv, err := identity.PrivKey()
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(strings.TrimSpace(authorPubKey), identity.PubKey) {
		return nil, errors.New("that is your own post — taking it would open a room with yourself")
	}
	secret, err := boardShared(priv, authorPubKey)
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(take)
	if err != nil {
		return nil, err
	}
	content, err := sealBox(secret, plain)
	if err != nil {
		return nil, err
	}
	ev := &NostrEvent{
		CreatedAt: time.Now().Unix(),
		Kind:      takeKind,
		Tags: [][]string{
			// `p` is how Nostr addresses an event at somebody: it is what the
			// author subscribes on, and the only reason this reaches them.
			{"p", strings.TrimSpace(authorPubKey)},
			{"t", boardTag()},
			// Not `d` -- this kind is not addressable, and a `d` here would read
			// as replaceability it does not have. `e`-adjacent naming would be
			// wrong too: this references a post, not an event id.
			{"a", fmt.Sprintf("%d:%s:%s", boardKind, strings.TrimSpace(authorPubKey), take.PostID)},
		},
		Content: content,
	}
	if err := signEvent(priv, ev); err != nil {
		return nil, err
	}
	return ev, nil
}

// InboundTake is a take, opened, with the key that sent it.
type InboundTake struct {
	Take Take `json:"take"`
	// From is the taker's board key. The author's only handle on them, and what
	// makes "this is the same person who posted that offer last week" answerable.
	From    string `json:"from"`
	EventID string `json:"eventId"`
	At      int64  `json:"at"`
}

// OpenTake decrypts a take addressed to this identity.
//
// The signature is checked before the decryption is attempted, so a garbage
// event fails as a bad signature rather than as an undecryptable one -- which
// are different problems and would send somebody looking in different places.
func OpenTake(identity *BoardIdentity, ev *NostrEvent) (*InboundTake, error) {
	if err := verifyEvent(ev); err != nil {
		return nil, err
	}
	if ev.Kind != takeKind {
		return nil, fmt.Errorf("event kind %d is not a ferry board take", ev.Kind)
	}
	if p := tagValue(ev, "p"); !strings.EqualFold(p, identity.PubKey) {
		return nil, errors.New("that take is addressed to somebody else")
	}
	priv, err := identity.PrivKey()
	if err != nil {
		return nil, err
	}
	secret, err := boardShared(priv, ev.PubKey)
	if err != nil {
		return nil, err
	}
	plain, err := openBox(secret, ev.Content)
	if err != nil {
		return nil, err
	}
	var take Take
	if err := json.Unmarshal(plain, &take); err != nil {
		return nil, fmt.Errorf("that take is not readable: %w", err)
	}
	if err := take.validate(); err != nil {
		return nil, err
	}
	return &InboundTake{Take: take, From: ev.PubKey, EventID: ev.ID, At: ev.CreatedAt}, nil
}

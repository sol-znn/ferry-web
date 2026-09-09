package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The board handlers.
//
// Thin, like the session ones, and for the same reason: the module signs,
// verifies and decrypts, and the relay connection lives in TypeScript because a
// WebSocket to a relay is a WebSocket. What is different from a session is which
// direction the interesting work runs. A session's module mostly SEALS; a
// board's module mostly READS, because most events crossing this boundary were
// written by strangers.
//
// One rule holds across all of them: an event is never trusted for having
// arrived. handleBoardRead re-derives the id, re-checks the signature and
// re-verifies every wallet proof against the reader's own network, and it does
// that for a post this browser published thirty seconds ago exactly as for one
// from a key nobody has seen before.

// boardIdentityView is the identity as the page sees it: the public key and what
// it has been bound to, never the private key. Same rule as keyView on a swap.
type boardIdentityView struct {
	PubKey    string              `json:"pubKey"`
	Bindings  []WalletProof       `json:"bindings"`
	CreatedAt time.Time           `json:"createdAt"`
	Kinds     boardKinds          `json:"kinds"`
	Limits    boardLimits         `json:"limits"`
	Presence  boardPresenceTiming `json:"presence"`
}

// boardKinds tells the page which kinds and tag to subscribe on, rather than
// having TypeScript hold a second copy of numbers whose meaning is decided here.
// A filter built from a stale constant is a board that silently shows nothing.
type boardKinds struct {
	Post     int    `json:"post"`
	Take     int    `json:"take"`
	Delete   int    `json:"delete"`
	Presence int    `json:"presence"`
	Tag      string `json:"tag"`
}

// boardPresenceTiming is how often to beat and how long a beat lasts, in
// seconds, reported for the same reason the kinds are: the page runs the timer
// and draws the dot, and a second copy of these numbers in TypeScript would be
// two components disagreeing about who is online. The window is the one
// ReadPresence applies.
type boardPresenceTiming struct {
	BeatSeconds  int64 `json:"beatSeconds"`
	StaleSeconds int64 `json:"staleSeconds"`
}

// boardLimits is how long a post may run, and how long one runs by default.
//
// Reported rather than mirrored for the same reason the kinds are: the default
// differs by instance (fifteen minutes on dev, a day on prod), and a form that
// held its own copy would offer a day on a build whose module then refused to
// mean it -- or worse, would agree by coincidence today and drift tomorrow.
// Seconds because that is what the publish call takes.
type boardLimits struct {
	DefaultTTL int64 `json:"defaultTtl"`
	MinTTL     int64 `json:"minTtl"`
	MaxTTL     int64 `json:"maxTtl"`
}

func viewIdentity(id *BoardIdentity) boardIdentityView {
	bindings := id.Bindings
	if bindings == nil {
		bindings = []WalletProof{}
	}
	return boardIdentityView{
		PubKey:    id.PubKey,
		Bindings:  bindings,
		CreatedAt: id.CreatedAt,
		Kinds: boardKinds{
			Post:     boardKind,
			Take:     takeKind,
			Delete:   deleteKind,
			Presence: presenceKind,
			Tag:      boardTag(),
		},
		Limits: boardLimits{
			DefaultTTL: int64(DefaultPostTTL().Seconds()),
			MinTTL:     int64(MinPostTTL.Seconds()),
			MaxTTL:     int64(MaxPostTTL.Seconds()),
		},
		Presence: boardPresenceTiming{
			BeatSeconds:  int64(PresenceBeat.Seconds()),
			StaleSeconds: int64(PresenceStale.Seconds()),
		},
	}
}

// handleBoardIdentity returns this browser's board key, minting one if needed.
func handleBoardIdentity(_ context.Context, a *API, _ []byte) (any, error) {
	id, err := a.Store.Identity()
	if err != nil {
		return nil, err
	}
	return map[string]any{"identity": viewIdentity(id)}, nil
}

// handleBoardStatement returns the exact text a wallet has to sign to bind an
// address.
//
// A call rather than a string the page assembles, because the statement is what
// the signature commits to: if the page built one and Go verified against
// another, every proof would fail and the difference would be invisible. One
// producer, one verifier, one function.
func handleBoardStatement(_ context.Context, a *API, body []byte) (any, error) {
	var req struct {
		Address string `json:"address"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	addr := strings.TrimSpace(req.Address)
	if addr == "" {
		return nil, errors.New("name the address you want to prove")
	}
	id, err := a.Store.Identity()
	if err != nil {
		return nil, err
	}
	return map[string]any{"statement": BindStatement(id.PubKey, addr)}, nil
}

// handleBoardBind verifies a wallet signature and records the binding.
//
// It refuses rather than warns. A binding that did not verify is a badge that
// would appear beside somebody else's address on every post this key ever makes,
// which is worse than no badge at all -- it is the badge doing the opposite of
// its job.
func handleBoardBind(_ context.Context, a *API, body []byte) (any, error) {
	var req struct {
		Scheme   string   `json:"scheme"`
		Address  string   `json:"address"`
		PubKey   string   `json:"pubKey,omitempty"`
		Sig      string   `json:"sig"`
		Settings Settings `json:"settings"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	id, err := a.Store.Identity()
	if err != nil {
		return nil, err
	}
	network := req.Settings.Network
	if network == "" {
		network = "mainnet"
	}
	proof := WalletProof{
		Scheme:  req.Scheme,
		Address: strings.TrimSpace(req.Address),
		PubKey:  strings.TrimSpace(req.PubKey),
		Sig:     strings.TrimSpace(req.Sig),
	}
	if err := VerifyProof(proof, id.PubKey, network); err != nil {
		return nil, err
	}
	proof.VerifiedAt = time.Now().UTC()
	id.Bind(proof)
	if err := a.Store.SaveIdentity(id); err != nil {
		return nil, err
	}
	return map[string]any{"identity": viewIdentity(id)}, nil
}

// handleBoardUnbind drops a binding from future posts.
func handleBoardUnbind(_ context.Context, a *API, body []byte) (any, error) {
	var req struct {
		Scheme string `json:"scheme"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	id, err := a.Store.Identity()
	if err != nil {
		return nil, err
	}
	id.Unbind(req.Scheme)
	if err := a.Store.SaveIdentity(id); err != nil {
		return nil, err
	}
	return map[string]any{"identity": viewIdentity(id)}, nil
}

// postReq is the form behind a post. Every field the author controls, and none
// they do not: the id, the timestamps, the status and the proofs are all decided
// here rather than accepted, because each of them is a way to publish a post
// that says something the author did not agree to.
type postReq struct {
	// ID names an existing post to edit. Blank mints a new one, which is what
	// makes the same call serve both -- and what stops an edit accidentally
	// becoming a second offer because the page forgot to pass one through.
	ID   string `json:"id,omitempty"`
	Side Leg    `json:"side"`
	Role Role   `json:"role"`

	AmountSats int64 `json:"amountSats"`
	MinSats    int64 `json:"minSats,omitempty"`
	MaxSats    int64 `json:"maxSats,omitempty"`

	ZenonAmt   string `json:"zenonAmt"`
	ZenonToken string `json:"zenonToken,omitempty"`

	LockHours int    `json:"lockHours,omitempty"`
	BtcAddr   string `json:"btcAddr,omitempty"`
	ZnnAddr   string `json:"znnAddr,omitempty"`
	Note      string `json:"note,omitempty"`
	Completed int    `json:"completed,omitempty"`

	// TTLSeconds is how long this post should live. Zero takes DefaultPostTTL.
	TTLSeconds int64 `json:"ttlSeconds,omitempty"`
	// Status lets an edit move a post to taken or done. Blank keeps it open. A
	// withdrawal is handleBoardWithdraw rather than a status written here,
	// because a withdrawal also emits a deletion request.
	Status string `json:"status,omitempty"`

	Settings Settings `json:"settings"`
}

// handleBoardPublish signs a post and stores it as one of ours.
//
// The event is returned rather than sent: relays are the page's business. What
// this guarantees is that the thing the page hands a relay is a document Go
// built, validated and signed -- so a field edited in devtools between the form
// and the wire is a field whose signature no longer verifies.
func handleBoardPublish(_ context.Context, a *API, body []byte) (any, error) {
	var req postReq
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	network := req.Settings.Network
	if network == "" {
		network = "mainnet"
	}
	if _, err := NetworkParams(network); err != nil {
		return nil, err
	}
	id, err := a.Store.Identity()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	ttl := time.Duration(req.TTLSeconds) * time.Second
	if req.TTLSeconds == 0 {
		ttl = DefaultPostTTL()
	}
	if ttl < MinPostTTL || ttl > MaxPostTTL {
		return nil, fmt.Errorf("a post can run between %s and %s; %s is outside that",
			MinPostTTL, MaxPostTTL, ttl)
	}

	status := req.Status
	if status == "" {
		status = StatusOpen
	}
	if !knownStatus(status) {
		return nil, fmt.Errorf("%q is not a board post status", status)
	}

	post := BoardPost{
		Version:    1,
		ID:         strings.TrimSpace(req.ID),
		Network:    network,
		Side:       req.Side,
		Role:       req.Role,
		AmountSats: req.AmountSats,
		MinSats:    req.MinSats,
		MaxSats:    req.MaxSats,
		ZenonAmt:   strings.TrimSpace(req.ZenonAmt),
		ZenonToken: strings.TrimSpace(req.ZenonToken),
		LockHours:  req.LockHours,
		BtcAddr:    strings.TrimSpace(req.BtcAddr),
		ZnnAddr:    strings.TrimSpace(req.ZnnAddr),
		Note:       strings.TrimSpace(req.Note),
		Completed:  req.Completed,
		Status:     status,
		CreatedAt:  now.Unix(),
		ExpiresAt:  now.Add(ttl).Unix(),
	}

	// An edit keeps the slot and the original creation time. Keeping CreatedAt
	// is what makes "posted 3 hours ago" mean when the OFFER appeared rather
	// than when it was last touched -- a post edited every ten minutes would
	// otherwise read as permanently new, which is a way to sit at the top of a
	// list sorted by age.
	var existing *MyPost
	if post.ID == "" {
		fresh, err := NewPostID()
		if err != nil {
			return nil, err
		}
		post.ID = fresh
	} else {
		existing, err = a.Store.MyPost(post.ID)
		if err != nil {
			return nil, fmt.Errorf("%w — a post can only be edited by the browser that made it, "+
				"because only that browser holds the key that signs it", err)
		}
		post.CreatedAt = existing.Post.CreatedAt
	}

	// Both addresses, or no post.
	//
	// This is what makes an offer answerable without its author being reachable
	// anywhere else. A taker reading this post has to end up holding the two
	// places the author wants each half of the trade paid — the Zenon address
	// especially, because an HTLC is created TO an address and there is no swap
	// to build without one. Left optional, the common case was a post that
	// looked complete, got taken, and then needed both sides to find a chat
	// window to finish the sentence.
	//
	// Refused here rather than in validate(), and the asymmetry is deliberate.
	// validate() is shared by publishing and reading precisely so that this app
	// never publishes what it would refuse to read — but a post without
	// addresses is not incoherent, it is merely older. Making it a read-time
	// failure would blank every board on the day this shipped, and would keep
	// refusing posts from anyone still on the previous build. This build will
	// not MAKE one; it still reads what is already out there.
	if post.BtcAddr == "" || post.ZnnAddr == "" {
		return nil, errors.New("an offer has to name both a Bitcoin address and a Zenon " +
			"address — they are where each half of the trade gets paid, and a taker has no " +
			"other way to learn them. Prove an address on each chain and they fill themselves in")
	}

	// And every address it names has to be one this browser has PROVEN.
	//
	// The rule the board is built on, applied at the only place it can be
	// applied: an address in a post is a claim about where somebody's money
	// goes, and until a wallet signs for it the claim is whatever filled the
	// field in. A page can be edited from devtools; this cannot.
	//
	// Per chain rather than "both", because the requirement belongs to the
	// trade. It falls out of the addresses the post actually names, so an offer
	// with no Bitcoin leg asks for no Bitcoin proof and a chain added later is
	// a case here rather than a rule change.
	if err := requireProven(id, post.BtcAddr, post.ZnnAddr); err != nil {
		return nil, err
	}

	// Proofs are attached from the identity, never taken from the request. A
	// caller-supplied proof would be a caller-supplied claim about an address,
	// which is the exact thing the binding step exists to prevent.
	post.Proofs = proofsFor(id, post)

	// Floored at the version this replaces, so an edit is always strictly newer
	// than what relays hold. See SealPostAfter: a tie is resolved by keeping the
	// lower event id, so without this an edit made inside the same second as the
	// last one is a coin flip over whether relays keep it.
	var notBefore int64
	if existing != nil {
		notBefore = existing.PublishedAt.Unix()
	}
	ev, err := SealPostAfter(id, post, notBefore)
	if err != nil {
		return nil, err
	}
	// The event's own timestamp rather than the wall clock, because they differ
	// exactly when the floor above moved one -- and this value is the floor for
	// the NEXT republish. Recording the un-bumped time would let the very next
	// edit collide with the version this one just became.
	mine := &MyPost{Post: post, PublishedAt: time.Unix(ev.CreatedAt, 0).UTC()}
	if existing != nil {
		mine.SwapID = existing.SwapID
		mine.Code = existing.Code
		mine.Taker = existing.Taker
	}
	if err := a.Store.SaveMyPost(mine); err != nil {
		return nil, err
	}
	return map[string]any{"event": ev, "post": mine}, nil
}

// chainAddr is one address a post or a take names, and the proof that claims it.
//
// The pairing is the whole of what "per chain" means here. A board that trades
// more than one pair cannot ask for "both proofs" — an offer between two Zenon
// tokens has no Bitcoin leg to prove — so the requirement is read off the
// addresses actually named, and a chain added later is one more case in
// chainsNamed rather than a rule rewritten in two handlers.
type chainAddr struct {
	// name is how the refusal says it: a chain, not a wallet. Which extension
	// signs for it changes; the chain does not.
	name   string
	scheme string
	addr   string
}

// chainsNamed lists the chains a set of addresses settles on. Blank means the
// offer has no leg there and asks for no proof.
func chainsNamed(btcAddr, znnAddr string) []chainAddr {
	var out []chainAddr
	if a := strings.TrimSpace(btcAddr); a != "" {
		out = append(out, chainAddr{name: "Bitcoin", scheme: ProofBTC, addr: a})
	}
	if a := strings.TrimSpace(znnAddr); a != "" {
		out = append(out, chainAddr{name: "Zenon", scheme: ProofZNN, addr: a})
	}
	return out
}

// requireProven refuses an address this browser has not proven it holds.
//
// The board's rule, and the reason it is enforced in Go rather than by a
// disabled button: the page is static, so everything in it is reachable from
// devtools, and an address in a signed post is a claim a stranger acts on. A
// proof is a wallet signature over a statement naming this board key and that
// address, already verified by VerifyProof at bind time against a key the
// address is derived from.
//
// Note what it does NOT do: check whether an address is proven that the post
// does not name. A binding for an address nobody is trading with is neither
// required nor in the way — see proofsFor, which attaches only the ones the post
// speaks about, and ReadPost, which badges only those.
func requireProven(id *BoardIdentity, btcAddr, znnAddr string) error {
	for _, c := range chainsNamed(btcAddr, znnAddr) {
		p := id.Binding(c.scheme)
		if p == nil {
			return fmt.Errorf("prove your %s address first — this build only publishes an "+
				"address a wallet has signed for, because an unproven one is a claim about "+
				"where somebody's money goes. The Prove %s address button is under your board "+
				"key", c.name, c.name)
		}
		if p.Address != c.addr {
			return fmt.Errorf("your %s proof is for %s, not %s — prove the address you are "+
				"trading with, or switch back to the one you proved", c.name, p.Address, c.addr)
		}
	}
	return nil
}

// proofsFor picks the bindings that say something about THIS post.
//
// A binding for an address the post does not name proves nothing a reader can
// use, and ReadPost refuses to badge it — so sending it would be sending a field
// that only ever produces a warning on the other side.
func proofsFor(id *BoardIdentity, post BoardPost) []WalletProof {
	var out []WalletProof
	if p := id.Binding(ProofBTC); p != nil && p.Address == post.BtcAddr && post.BtcAddr != "" {
		out = append(out, *p)
	}
	if p := id.Binding(ProofZNN); p != nil && p.Address == post.ZnnAddr && post.ZnnAddr != "" {
		out = append(out, *p)
	}
	return out
}

// handleBoardWithdraw takes a post down.
//
// Two events, because relays disagree about deletion. The replacement is a valid
// newer version of the same addressable post carrying StatusVoid and an expiry
// of now, which every relay honours because it is nothing more exotic than an
// edit. The NIP-09 request asks the ones that implement it to forget the post
// outright. Publishing only the second would leave the post standing on most
// relays; publishing only the first leaves a tombstone. Both is the honest
// answer to "remove this", and the page publishes them together.
func handleBoardWithdraw(_ context.Context, a *API, body []byte) (any, error) {
	var req struct {
		ID string `json:"id"`
		// Reason picks the wording and the status: a withdrawal is void, a
		// finished swap is done. Same two events either way.
		Done bool `json:"done,omitempty"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	id, err := a.Store.Identity()
	if err != nil {
		return nil, err
	}
	mine, err := a.Store.MyPost(strings.TrimSpace(req.ID))
	if err != nil {
		return nil, err
	}

	post := mine.Post
	post.Status = StatusVoid
	if req.Done {
		post.Status = StatusDone
	}
	// Expiry pulled in rather than left where it was. A void post with a day
	// still to run is a day of relays holding a record whose only content is
	// that it is not an offer; MinPostTTL is the floor SealPost's own validation
	// will accept, and it gives a reader who is mid-refresh time to see why the
	// post went away rather than watching it disappear.
	post.ExpiresAt = time.Now().Add(MinPostTTL).Unix()
	post.Proofs = proofsFor(id, post)

	// The floor matters most here of all. A withdrawal that ties with the edit it
	// retracts loses that tie half the time — relays keep the lower event id —
	// and the offer stays live for everybody but its author, who watches their own
	// board show it as withdrawn. See SealPostAfter.
	ev, err := SealPostAfter(id, post, mine.PublishedAt.Unix())
	if err != nil {
		return nil, err
	}
	del, err := SealDelete(id, post.ID)
	if err != nil {
		return nil, err
	}
	mine.Post = post
	mine.PublishedAt = time.Unix(ev.CreatedAt, 0).UTC()
	if err := a.Store.SaveMyPost(mine); err != nil {
		return nil, err
	}
	return map[string]any{"events": []*NostrEvent{ev, del}, "post": mine}, nil
}

// handleBoardMine returns this browser's posts, expired and withdrawn included.
func handleBoardMine(_ context.Context, a *API, _ []byte) (any, error) {
	posts, err := a.Store.MyPosts()
	if err != nil {
		return nil, err
	}
	if posts == nil {
		posts = []*MyPost{}
	}
	return map[string]any{"posts": posts}, nil
}

// handleBoardForget removes the local record of a post. See Store.DeleteMyPost:
// this does not reach a relay, which is why the page withdraws first.
func handleBoardForget(_ context.Context, a *API, body []byte) (any, error) {
	var req struct {
		ID string `json:"id"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	if err := a.Store.DeleteMyPost(strings.TrimSpace(req.ID)); err != nil {
		return nil, err
	}
	return map[string]any{"forgot": req.ID}, nil
}

// handleBoardLink records which swap and session a post turned into.
//
// This is the whole of "the board updates itself when the swap completes". The
// page cannot watch a chain for a post -- a post is not on a chain -- but it
// already watches its own swap list, and this is the join between the two. Once
// a post names a swap, a settled swap names a post to withdraw.
func handleBoardLink(_ context.Context, a *API, body []byte) (any, error) {
	var req struct {
		ID     string `json:"id"`
		SwapID string `json:"swapId,omitempty"`
		Code   string `json:"code,omitempty"`
		Taker  string `json:"taker,omitempty"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	mine, err := a.Store.MyPost(strings.TrimSpace(req.ID))
	if err != nil {
		return nil, err
	}
	if req.SwapID != "" {
		// Checked rather than stored as given: a post pointing at a swap this
		// browser does not hold would sit there forever waiting for a swap that
		// can never settle.
		if _, err := a.Store.Load(req.SwapID); err != nil {
			return nil, err
		}
		mine.SwapID = req.SwapID
	}
	if req.Code != "" {
		mine.Code = NormalizeSessionCode(req.Code)
	}
	if req.Taker != "" {
		mine.Taker = strings.TrimSpace(req.Taker)
	}
	if err := a.Store.SaveMyPost(mine); err != nil {
		return nil, err
	}
	return map[string]any{"post": mine}, nil
}

// handleBoardRead verifies one event off a relay and returns what it says.
//
// Called for every event the page receives, including this browser's own coming
// back off the relays it was published to. Deliberately: a post that fails
// verification on the way back in is a post that would fail for everyone else
// too, and finding that out about your own post is worth the handful of
// microseconds.
func handleBoardRead(_ context.Context, a *API, body []byte) (any, error) {
	var req struct {
		Event    *NostrEvent `json:"event"`
		Settings Settings    `json:"settings"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	network := req.Settings.Network
	if network == "" {
		network = "mainnet"
	}
	listing, err := ReadPost(req.Event, network, time.Now())
	if err != nil {
		return nil, err
	}
	id, err := a.Store.Identity()
	if err != nil {
		return nil, err
	}
	listing.Mine = strings.EqualFold(listing.Author, id.PubKey)
	return map[string]any{"listing": listing}, nil
}

// handleBoardPresence signs one beat, saying this browser is here.
//
// It deliberately does not decide WHETHER to beat. Whether there is anything to
// be present for -- a live post somebody might take -- is a question about the
// page's own list, and the page is where the timer lives; a module that started
// broadcasting a key's liveness on its own would be publishing a fact about its
// user that its user did not ask to publish. See the heartbeat in useBoard.
func handleBoardPresence(_ context.Context, a *API, body []byte) (any, error) {
	var req struct {
		Settings Settings `json:"settings"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	network := req.Settings.Network
	if network == "" {
		network = "mainnet"
	}
	id, err := a.Store.Identity()
	if err != nil {
		return nil, err
	}
	ev, err := SealPresence(id, network)
	if err != nil {
		return nil, err
	}
	return map[string]any{"event": ev}, nil
}

// handleBoardReadPresence verifies one beat off a relay.
//
// Called for this browser's own beats coming back as well as for strangers',
// for the reason handleBoardRead gives: a beat that fails verification on the
// way back in is one that fails for everybody, and that is worth finding out.
func handleBoardReadPresence(_ context.Context, a *API, body []byte) (any, error) {
	var req struct {
		Event    *NostrEvent `json:"event"`
		Settings Settings    `json:"settings"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	network := req.Settings.Network
	if network == "" {
		network = "mainnet"
	}
	seen, err := ReadPresence(req.Event, network, time.Now())
	if err != nil {
		return nil, err
	}
	return map[string]any{"seen": seen}, nil
}

// handleBoardTake seals a session code to a post's author.
//
// The code is minted here, in the same call that seals it, so there is no moment
// where the page holds a code it might publish somewhere else. It comes back to
// the caller because the taker has to join that room themselves -- which is the
// one thing the taker must do and the author cannot do for them.
func handleBoardTake(_ context.Context, a *API, body []byte) (any, error) {
	var req struct {
		Author     string `json:"author"`
		PostID     string `json:"postId"`
		AmountSats int64  `json:"amountSats,omitempty"`
		Note       string `json:"note,omitempty"`
		// BtcAddr and ZnnAddr are where this taker wants each half paid. Sealed
		// to the author with the rest of the take -- see Take.
		BtcAddr string `json:"btcAddr,omitempty"`
		ZnnAddr string `json:"znnAddr,omitempty"`
		// Code re-uses a room the taker is already in, rather than opening a
		// second one. What a re-send needs: the same room, a new event.
		Code string `json:"code,omitempty"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	// The same rule the publish side applies, from the other end. An author
	// answering this take has to come away holding somewhere to pay, or the
	// introduction has not finished introducing anybody.
	if strings.TrimSpace(req.BtcAddr) == "" || strings.TrimSpace(req.ZnnAddr) == "" {
		return nil, errors.New("taking an offer has to say where you want each half of the " +
			"trade paid — prove an address on each chain and they fill themselves in")
	}
	id, err := a.Store.Identity()
	if err != nil {
		return nil, err
	}
	// And each of them proven, exactly as a post's are. A take is the same claim
	// made privately: the author reads these addresses and builds their half of
	// the swap against them, so an unproven one costs the same as an unproven
	// one on a post — see requireProven.
	if err := requireProven(id, req.BtcAddr, req.ZnnAddr); err != nil {
		return nil, err
	}
	code := req.Code
	if strings.TrimSpace(code) == "" {
		fresh, err := NewSessionCode()
		if err != nil {
			return nil, err
		}
		code = fresh
	}
	keys, _, err := DeriveSession(code)
	if err != nil {
		return nil, err
	}
	take := Take{
		Version:    1,
		PostID:     strings.TrimSpace(req.PostID),
		Code:       keys.Code,
		AmountSats: req.AmountSats,
		BtcAddr:    strings.TrimSpace(req.BtcAddr),
		ZnnAddr:    strings.TrimSpace(req.ZnnAddr),
		Note:       strings.TrimSpace(req.Note),
	}
	ev, err := SealTake(id, req.Author, take)
	if err != nil {
		return nil, err
	}
	// The room is returned in the same shape sessionNew returns it, so the page
	// joins a taken post exactly as it joins a code somebody read out.
	return map[string]any{
		"event": ev,
		"room": map[string]any{
			"code":    keys.Code,
			"display": groupCode(keys.Code),
			"pubKey":  keys.PubKey,
			"roomId":  keys.RoomID,
			"kind":    sessionKind,
		},
	}, nil
}

// handleBoardReadTake opens a take addressed to this browser.
func handleBoardReadTake(_ context.Context, a *API, body []byte) (any, error) {
	var req struct {
		Event *NostrEvent `json:"event"`
	}
	if err := decode(body, &req); err != nil {
		return nil, err
	}
	id, err := a.Store.Identity()
	if err != nil {
		return nil, err
	}
	inbound, err := OpenTake(id, req.Event)
	if err != nil {
		return nil, err
	}
	// The room, alongside the take, for the same reason handleBoardTake returns
	// one: the author's next action is to join it, and re-deriving a room from a
	// code in TypeScript would be a second implementation of the one thing both
	// sides must agree on exactly.
	keys, _, err := DeriveSession(inbound.Take.Code)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"take": inbound,
		"room": map[string]any{
			"code":    keys.Code,
			"display": groupCode(keys.Code),
			"pubKey":  keys.PubKey,
			"roomId":  keys.RoomID,
			"kind":    sessionKind,
		},
	}, nil
}

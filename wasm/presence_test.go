package main

import (
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

// A presence beat carries almost nothing, which puts all of its weight on the
// two things it does carry: who signed it, and when. These tests are about the
// ways either could be made to lie.

func TestPresenceRoundTrips(t *testing.T) {
	id := testIdentity(t)
	ev, err := SealPresence(id, "mainnet")
	if err != nil {
		t.Fatalf("SealPresence: %v", err)
	}
	seen, err := ReadPresence(ev, "mainnet", time.Now())
	if err != nil {
		t.Fatalf("ReadPresence: %v", err)
	}
	if seen.Author != id.PubKey {
		t.Fatalf("beat attributed to %s, want %s", seen.Author, id.PubKey)
	}
	if seen.StaleAfter != int64(PresenceStale.Seconds()) {
		t.Fatalf("staleAfter is %d, want %d", seen.StaleAfter, int64(PresenceStale.Seconds()))
	}
	// The window has to outlast the interval by enough to survive a dropped
	// publish to a blinking relay, or the dot blinks on somebody sitting right
	// there.
	if PresenceStale <= 2*PresenceBeat {
		t.Fatalf("a %s window over a %s beat leaves no room for a missed one",
			PresenceStale, PresenceBeat)
	}
	// And it has to be short enough that the answer is worth acting on. The dot
	// exists to say whether a take will be seen now; a window measured in
	// minutes shows a closed browser as present for long enough that somebody
	// sends one and waits. Beating stops when the tab is hidden precisely so
	// this can be tight -- see PresenceStale.
	if PresenceStale > 90*time.Second {
		t.Fatalf("a %s window is long enough for a closed tab to keep a green dot past the "+
			"point where a reader would have acted on it", PresenceStale)
	}
}

// The slot is what keeps this to one stored event per key rather than a beat a
// minute accumulating on every relay forever.
func TestPresenceOccupiesOneAddressableSlot(t *testing.T) {
	id := testIdentity(t)
	first, err := SealPresence(id, "mainnet")
	if err != nil {
		t.Fatal(err)
	}
	second, err := SealPresence(id, "mainnet")
	if err != nil {
		t.Fatal(err)
	}
	if d := tagValue(first, "d"); d != presenceSlot {
		t.Fatalf("beat filed under %q, want %q", d, presenceSlot)
	}
	if tagValue(first, "d") != tagValue(second, "d") {
		t.Fatal("two beats from one key landed in different slots, so they would pile up")
	}
	if first.Kind < 30000 || first.Kind > 39999 {
		t.Fatalf("kind %d is outside the addressable range, so relays would not replace a beat",
			first.Kind)
	}
	// NIP-40, so a relay that honours expiry stops carrying an abandoned key.
	// Longer than the staleness window, or a relay could drop a beat this app
	// would still have shown as online.
	if tagValue(first, "expiration") == "" {
		t.Fatal("a beat with no expiration tag is one relays keep indefinitely")
	}
	if PresenceTTL <= PresenceStale {
		t.Fatalf("a %s expiry on a %s window drops beats that are still live",
			PresenceTTL, PresenceStale)
	}
}

// The heart of it: a beat is worth reading only because nobody but its author
// could have written it.
func TestPresenceRefusesTampering(t *testing.T) {
	id := testIdentity(t)

	t.Run("moved forward in time", func(t *testing.T) {
		// The attack this exists for. A beat's timestamp IS the claim, so
		// somebody who could restamp one could keep a key green after its
		// browser closed — and unlike a post, there is no second version for a
		// forward-dated one to lose to.
		ev, err := SealPresence(id, "mainnet")
		if err != nil {
			t.Fatal(err)
		}
		ev.CreatedAt += 3600
		if _, err := ReadPresence(ev, "mainnet", time.Now()); err == nil {
			t.Fatal("a restamped beat was accepted, so anyone can hold a key online")
		}
	})

	t.Run("restamped with a recomputed id", func(t *testing.T) {
		// The thorough version: the id is fixed up to match. Only the signature
		// is left.
		ev, err := SealPresence(id, "mainnet")
		if err != nil {
			t.Fatal(err)
		}
		ev.CreatedAt += 3600
		fixed, err := ev.eventID()
		if err != nil {
			t.Fatal(err)
		}
		ev.ID = hex.EncodeToString(fixed[:])
		if _, err := ReadPresence(ev, "mainnet", time.Now()); err == nil {
			t.Fatal("a re-hashed restamp was accepted without a valid signature")
		}
	})

	t.Run("its author's own clock running fast", func(t *testing.T) {
		// Not an attack — a browser with a wrong clock — and past the tolerance
		// it fails the same way on purpose. Refused rather than clamped, because
		// clamping a day-fast clock would read as online for a day.
		ev, err := SealPresence(id, "mainnet")
		if err != nil {
			t.Fatal(err)
		}
		past := time.Now().Add(-2 * PresenceSkew)
		if _, err := ReadPresence(ev, "mainnet", past); err == nil {
			t.Fatal("a beat from ahead of the reader's clock was accepted")
		}

		// Within the tolerance it is accepted — ordinary drift between two
		// honest machines must not grey somebody out — but CLAMPED to the
		// reader's clock rather than believed.
		//
		// Believing it would quietly hand that one author a longer staleness
		// window than everybody else: a beat stamped a minute ahead sits inside
		// PresenceStale for a minute longer than any beat should, which is
		// exactly the over-long green this timing was tightened to remove.
		skewed := time.Now().Add(-PresenceSkew / 2)
		seen, err := ReadPresence(ev, "mainnet", skewed)
		if err != nil {
			t.Fatalf("a beat within the skew tolerance was refused: %v", err)
		}
		if seen.SeenAt > skewed.Unix() {
			t.Fatalf("a beat stamped %d ahead of the reader's clock (%d) was taken at face "+
				"value, extending its author's window by the skew",
				seen.SeenAt, skewed.Unix())
		}
		// And a beat that is not ahead is left exactly alone.
		straight, err := ReadPresence(ev, "mainnet", time.Now().Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if straight.SeenAt != ev.CreatedAt {
			t.Fatalf("an ordinary beat was rewritten from %d to %d", ev.CreatedAt, straight.SeenAt)
		}
	})

	t.Run("borrowed from another board", func(t *testing.T) {
		// A signet beat must not answer for a mainnet row. Dropped rather than
		// flagged: a dot has nowhere to show a caveat.
		ev, err := SealPresence(id, "signet")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ReadPresence(ev, "mainnet", time.Now()); err == nil {
			t.Fatal("a beat for another network was accepted")
		}
	})

	t.Run("filed under a post's slot", func(t *testing.T) {
		// Kinds are checked both ways round, so neither event can be read as the
		// other — a post read as a beat would make every author permanently
		// online at whatever time they last edited.
		post, err := SealPost(id, samplePost("abcd1234"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ReadPresence(post, "mainnet", time.Now()); err == nil {
			t.Fatal("a board post was accepted as a presence beat")
		}
		beat, err := SealPresence(id, "mainnet")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ReadPost(beat, "mainnet", time.Now()); err == nil {
			t.Fatal("a presence beat was accepted as a board post")
		}
	})

	t.Run("re-signed under another key", func(t *testing.T) {
		// Somebody republishing a beat as their own. It has to READ — it is
		// their beat now — and it must not say the original author is here.
		other := testIdentity(t)
		ev, err := SealPresence(other, "mainnet")
		if err != nil {
			t.Fatal(err)
		}
		seen, err := ReadPresence(ev, "mainnet", time.Now())
		if err != nil {
			t.Fatalf("a validly signed beat should read: %v", err)
		}
		if seen.Author == id.PubKey {
			t.Fatal("a beat signed by somebody else is attributed to the wrong key")
		}
	})
}

// A beat says one key was alive at one moment and nothing else. Anything more in
// the payload would be an author describing their own liveness, which is exactly
// what the reader must decide instead.
func TestPresenceCarriesNoSelfDescribedLiveness(t *testing.T) {
	id := testIdentity(t)
	ev, err := SealPresence(id, "mainnet")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(ev.Content), &body); err != nil {
		t.Fatalf("beat content is not readable: %v", err)
	}
	for k := range body {
		if k != "v" && k != "network" {
			t.Fatalf("a beat carries %q, which lets its author say something about their own "+
				"presence that the reader should be deciding", k)
		}
	}
	// And the identity a beat is published under is the same durable key that
	// signs the posts, or a reader could not join the two up.
	seen, err := ReadPresence(ev, "mainnet", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	post, err := SealPost(id, samplePost("abcd1234"))
	if err != nil {
		t.Fatal(err)
	}
	listing, err := ReadPost(post, "mainnet", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if seen.Author != listing.Author {
		t.Fatalf("a beat is under %s but the post is under %s, so no row could match them",
			seen.Author, listing.Author)
	}
}

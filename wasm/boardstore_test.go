package main

import (
	"strings"
	"testing"
	"time"
)

// Forgetting a post is local-only, and that is exactly why it has to be refused
// while the post is not.

func savedPost(t *testing.T, store *Store, id, status string, expiresAt int64) {
	t.Helper()
	post := samplePost(id)
	post.Status = status
	post.ExpiresAt = expiresAt
	if err := store.SaveMyPost(&MyPost{Post: post, PublishedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("SaveMyPost: %v", err)
	}
}

func TestForgettingIsRefusedWhileAPostIsStillStanding(t *testing.T) {
	future := time.Now().Add(time.Hour).Unix()
	past := time.Now().Add(-time.Hour).Unix()

	// The whole point: the record being deleted is the only thing that can
	// withdraw the offer. Delete it while the offer is live and it is stranded —
	// publicly takeable, until it expires, with nobody able to take it down.
	for _, status := range []string{StatusOpen, StatusTaken} {
		t.Run("refused while "+status, func(t *testing.T) {
			store := NewStore(NewMemStorage())
			savedPost(t, store, "abcd1234", status, future)
			err := store.DeleteMyPost("abcd1234")
			if err == nil {
				t.Fatal("a standing post was forgotten, stranding the offer on every relay")
			}
			// The error has to name the way through, or it is a dead end.
			if !strings.Contains(err.Error(), "withdraw") {
				t.Fatalf("the refusal does not say to withdraw first: %v", err)
			}
			// And it must not have deleted it anyway.
			if _, err := store.MyPost("abcd1234"); err != nil {
				t.Fatalf("the record was removed despite the refusal: %v", err)
			}
		})
	}

	// Withdrawing is what makes it forgettable, and that is the sequence the UI
	// is now forced through.
	t.Run("allowed once withdrawn", func(t *testing.T) {
		store := NewStore(NewMemStorage())
		savedPost(t, store, "abcd1234", StatusVoid, future)
		if err := store.DeleteMyPost("abcd1234"); err != nil {
			t.Fatalf("a withdrawn post should be forgettable: %v", err)
		}
		if _, err := store.MyPost("abcd1234"); err == nil {
			t.Fatal("the record survived a delete that reported success")
		}
	})

	t.Run("allowed once finished", func(t *testing.T) {
		store := NewStore(NewMemStorage())
		savedPost(t, store, "abcd1234", StatusDone, future)
		if err := store.DeleteMyPost("abcd1234"); err != nil {
			t.Fatalf("a finished post should be forgettable: %v", err)
		}
	})

	// An expired post is already gone as far as takers are concerned, whatever
	// its status says — there is nothing left to strand, and refusing here would
	// leave dead records nobody could ever clear.
	t.Run("allowed once expired", func(t *testing.T) {
		store := NewStore(NewMemStorage())
		savedPost(t, store, "abcd1234", StatusOpen, past)
		if err := store.DeleteMyPost("abcd1234"); err != nil {
			t.Fatalf("an expired post should be forgettable: %v", err)
		}
	})
}

// Standing is wider than Live by exactly one status, and the difference decides
// whether a record can be thrown away.
func TestStandingCountsTakenPostsAndLiveDoesNot(t *testing.T) {
	now := time.Now()
	post := samplePost("abcd1234")
	post.Status = StatusTaken
	post.ExpiresAt = now.Add(time.Hour).Unix()

	if post.Live(now) {
		t.Fatal("a taken post is not on the open board and should not be Live")
	}
	if !post.Standing(now) {
		t.Fatal("a taken post is still published and still has takes arriving against it")
	}
	// Expiry ends both, whatever the status.
	past := now.Add(2 * time.Hour)
	if post.Standing(past) {
		t.Fatal("an expired post is standing nowhere")
	}
}

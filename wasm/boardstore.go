package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Where the board's own records live.
//
// Two of them, and they are different kinds of thing. The IDENTITY is a key: one
// per browser, replaced never, and losing it costs the ability to edit posts
// already published. The POSTS are a working set: what this browser has put on
// the board, kept so that a reload does not turn an editable offer into a
// stranger's, and so a finished swap can withdraw the offer it came from.
//
// A separate prefix from swaps rather than a separate store, because Store.List
// selects on StorageKeyPrefix() and would otherwise try to parse a board record
// as a swap. Same namespacing rule as everything else on this origin: the dev
// instance cannot see the production board and vice versa.

// BoardKeyPrefix namespaces the board's keys. See StorageKeyPrefix.
func BoardKeyPrefix() string {
	if IsDev() {
		return "ferry.dev.board."
	}
	return "ferry.board."
}

const (
	identityKey  = "identity"
	postKeyGroup = "post."
)

// MyPost is one of this browser's own posts, as it was last published.
//
// The event is not kept. It can be rebuilt from the post at any time and would
// need re-signing anyway -- a republished event carries a new created_at, which
// is what makes a relay treat it as newer than the copy it holds. Keeping a
// stale signed event would only invite publishing it.
type MyPost struct {
	Post BoardPost `json:"post"`
	// SwapID is the swap this offer turned into, once one exists. It is what
	// makes "the board updates itself when the swap completes" possible: the
	// page watches its own swap list, and a settled swap names the post to
	// withdraw. Empty until a take is accepted.
	SwapID string `json:"swapId,omitempty"`
	// Code is the session code accepted for this post, so reopening the page
	// mid-trade can rejoin the room rather than losing the counterparty.
	Code string `json:"code,omitempty"`
	// Taker is the board key whose take was accepted.
	Taker string `json:"taker,omitempty"`
	// PublishedAt is when this browser last put it on a relay. Local: it says
	// what was attempted here, not what any relay kept.
	PublishedAt time.Time `json:"publishedAt"`
}

func (s *Store) boardKey(name string) (string, error) {
	if name == "" || len(name) > 80 {
		return "", errors.New("invalid board record name")
	}
	for _, r := range name {
		if !strings.ContainsRune("0123456789abcdefghijklmnopqrstuvwxyz.", r) {
			return "", errors.New("invalid board record name")
		}
	}
	return BoardKeyPrefix() + name, nil
}

// Identity returns this browser's board key, minting one the first time.
//
// Minting on read rather than behind an explicit "create identity" step,
// because there is no decision to put in front of the user: the key is not an
// account, costs nothing, reveals nothing until something is published under it,
// and a page that asked permission to generate one would be asking about
// plumbing. What DOES need a decision -- binding a wallet address to it -- is a
// separate call with a wallet prompt in it.
func (s *Store) Identity() (*BoardIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	k, err := s.boardKey(identityKey)
	if err != nil {
		return nil, err
	}
	if raw, ok := s.backing.Get(k); ok {
		var id BoardIdentity
		if err := json.Unmarshal([]byte(raw), &id); err == nil && len(id.Priv) == 32 {
			return &id, nil
		}
		// A corrupt identity is not recoverable and not worth an error the user
		// can do nothing about: the key it held could only sign, and anything it
		// signed expires within the day. Replaced, and the posts under the old
		// key are left to expire.
	}
	fresh, err := NewBoardIdentity()
	if err != nil {
		return nil, err
	}
	if err := s.saveIdentity(fresh); err != nil {
		return nil, err
	}
	return fresh, nil
}

// SaveIdentity persists a changed identity — a binding added or dropped.
func (s *Store) SaveIdentity(id *BoardIdentity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveIdentity(id)
}

func (s *Store) saveIdentity(id *BoardIdentity) error {
	k, err := s.boardKey(identityKey)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	if err := s.backing.Set(k, string(raw)); err != nil {
		return fmt.Errorf("could not save the board key: %w", err)
	}
	return nil
}

// SaveMyPost writes one of this browser's posts.
func (s *Store) SaveMyPost(p *MyPost) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	k, err := s.boardKey(postKeyGroup + p.Post.ID)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := s.backing.Set(k, string(raw)); err != nil {
		return fmt.Errorf("could not save board post %s: %w", p.Post.ID, err)
	}
	return nil
}

// MyPost reads one back.
func (s *Store) MyPost(id string) (*MyPost, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.myPost(id)
}

func (s *Store) myPost(id string) (*MyPost, error) {
	k, err := s.boardKey(postKeyGroup + id)
	if err != nil {
		return nil, err
	}
	raw, ok := s.backing.Get(k)
	if !ok {
		return nil, fmt.Errorf("no board post of yours with id %s", id)
	}
	var p MyPost
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, fmt.Errorf("board post record %s is corrupt: %w", id, err)
	}
	return &p, nil
}

// MyPosts returns every post this browser has published, newest first.
//
// Including the expired and the withdrawn. They are what the "my posts" list is
// FOR -- an offer that quietly vanished at midnight is one the author cannot
// renew, and renewing is the common case for a post that timed out while its
// author was asleep. Sweeping them is a separate, explicit delete.
func (s *Store) MyPosts() ([]*MyPost, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	prefix := BoardKeyPrefix() + postKeyGroup
	var out []*MyPost
	for _, k := range s.backing.Keys() {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		p, err := s.myPost(strings.TrimPrefix(k, prefix))
		if err != nil {
			continue // one corrupt record should not hide the rest
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Post.CreatedAt > out[j].Post.CreatedAt
	})
	return out, nil
}

// DeleteMyPost forgets a post locally.
//
// Local only, and the distinction matters enough to be in the name: this removes
// the record that lets this browser edit or renew the post. It does not reach a
// relay, and a post still standing on one stays there until it expires.
//
// Which is why a post that IS still standing cannot be deleted at all.
//
// The private key is the only thing that can withdraw a post -- that is the
// property the whole board rests on -- and this record is the only thing that
// says which slot to withdraw. Deleting it while the offer is live does not
// remove the offer; it strands it, publicly takeable, with the one browser that
// could take it down having just thrown away the ability to. Somebody then takes
// an offer nobody is answering, and the author cannot even see why.
//
// Refused here rather than in the handler, because this is the only door: a
// future caller reaching for a "local only" delete is exactly who needs to be
// told that local is not where the consequence lands. Withdraw first -- that
// publishes the void replacement and the deletion request -- and then this is a
// tidy-up with nothing left to strand.
func (s *Store) DeleteMyPost(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	k, err := s.boardKey(postKeyGroup + id)
	if err != nil {
		return err
	}
	mine, err := s.myPost(id)
	if err != nil {
		return err
	}
	if mine.Post.Standing(time.Now()) {
		return fmt.Errorf("post %s is still standing on the relays, so forgetting it here would "+
			"leave an offer nobody can take down until it expires — withdraw it first, which "+
			"publishes the retraction, and then it can be forgotten", id)
	}
	return s.backing.Remove(k)
}

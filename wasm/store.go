package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/btcsuite/btcd/btcutil"
)

// StorageKeyPrefix namespaces this app's keys.
//
// A GitHub Pages project site shares one origin -- https://<user>.github.io --
// with every other page that user publishes, and localStorage is keyed by
// origin, not by path.
//
// The dev instance uses a namespace of its own, so the two builds cannot see
// each other's swaps even when deployed to the same origin: a regtest swap
// cannot appear in the mainnet list because the mainnet build never reads that
// prefix. The recovery file and the export still move a swap between them, which
// is the one crossing that should be deliberate.
func StorageKeyPrefix() string {
	if IsDev() {
		return "ferry.dev.swap."
	}
	return "ferry.swap."
}

// Storage is the key/value store swaps are persisted in. The browser build backs
// it with localStorage; tests back it with a map.
//
// One entry per swap, because the records are tiny, are written a handful of
// times each, and hold the key material needed to recover funds -- so you can
// read one, copy it out of devtools, and hand-edit it in an emergency.
type Storage interface {
	Get(key string) (string, bool)
	Set(key, value string) error
	// Remove deletes one key. Removing a key that is not there is not an
	// error — the caller wanted it gone, and it is.
	Remove(key string) error
	Keys() []string
}

// Store persists swaps as one JSON record each.
//
// These records contain the ephemeral private keys and are NOT encrypted: see
// docs/SECURITY.md. It is why every funded swap still produces a pre-signed
// refund the user is told to download -- recovery must not depend on this
// origin's storage surviving.
type Store struct {
	backing Storage
	mu      sync.RWMutex
}

// NewStore wraps a Storage implementation.
func NewStore(backing Storage) *Store { return &Store{backing: backing} }

// Dir describes where swaps live, for the status line.
func (s *Store) Dir() string { return "browser localStorage (" + StorageKeyPrefix() + "*)" }

// NewID returns a random identifier for a swap.
func NewID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// key validates a swap id and renders its storage key. Nothing here is a path,
// but an id carrying a `.` or a prefix separator could collide with, or shadow,
// another key in a namespace shared with every other page on this origin.
func (s *Store) key(id string) (string, error) {
	if id == "" || len(id) > 64 {
		return "", errors.New("invalid swap id")
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return "", errors.New("invalid swap id")
		}
	}
	return StorageKeyPrefix() + id, nil
}

// Save writes a swap.
//
// localStorage writes are atomic per key, so there is no torn write to defend
// against. What it adds instead is a quota, and a swap that cannot be written is
// one whose refund key was never persisted -- so the error is returned rather
// than logged.
func (s *Store) Save(sw *Swap) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	k, err := s.key(sw.ID)
	if err != nil {
		return err
	}
	// Compare-and-set on the version, under the same lock as the write: the
	// record being saved must be the record that was loaded, or a decision
	// made against a stale copy would overwrite one made since. See
	// Swap.Version.
	if existing, ok := s.backing.Get(k); ok {
		var stored struct {
			Version int64 `json:"version"`
		}
		if json.Unmarshal([]byte(existing), &stored) == nil && stored.Version != sw.Version {
			return fmt.Errorf("%w: swap %s is at version %d in the store and this write is from "+
				"version %d, so something else changed it in the meantime; nothing was written",
				ErrStaleWrite, sw.ID, stored.Version, sw.Version)
		}
	}
	sw.Version++
	data, err := json.MarshalIndent(sw, "", "  ")
	if err != nil {
		return err
	}
	if err := s.backing.Set(k, string(data)); err != nil {
		return fmt.Errorf("could not save swap %s: %w", sw.ID, err)
	}
	return nil
}

// ErrStaleWrite is returned by Save when the record changed since it was
// loaded. The caller's copy is out of date; load again and decide again.
var ErrStaleWrite = errors.New("stale write")

// Delete removes a swap record permanently. This destroys the ephemeral private
// key, which is the only key that can spend the contract, and localStorage has
// no undo. Whether a given swap is safe to delete is not a storage question --
// see Swap.SpentOut and handleDelete, the one caller.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	k, err := s.key(id)
	if err != nil {
		return err
	}
	// Loaded first so deleting an id that was never there is an error rather
	// than a silent success: a caller that mistyped an id should hear about it
	// here, not conclude that a swap it can still see has been removed.
	if _, err := s.load(id); err != nil {
		return err
	}
	return s.backing.Remove(k)
}

// Load reads one swap by id.
func (s *Store) Load(id string) (*Swap, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.load(id)
}

func (s *Store) load(id string) (*Swap, error) {
	k, err := s.key(id)
	if err != nil {
		return nil, err
	}
	data, ok := s.backing.Get(k)
	if !ok {
		return nil, fmt.Errorf("no swap with id %s", id)
	}
	var sw Swap
	if err := json.Unmarshal([]byte(data), &sw); err != nil {
		return nil, fmt.Errorf("swap record %s is corrupt: %w", id, err)
	}
	// A verdict from before the rule that reached it is not a verdict under
	// the rule. Withdrawn here, at the one door every record comes through --
	// a page load, a backup import, the wallet gate -- rather than at each of
	// them.
	sw.withdrawStaleVerdict()
	return &sw, nil
}

// List returns every stored swap, newest first.
func (s *Store) List() ([]*Swap, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var swaps []*Swap
	for _, k := range s.backing.Keys() {
		if !strings.HasPrefix(k, StorageKeyPrefix()) {
			continue
		}
		sw, err := s.load(strings.TrimPrefix(k, StorageKeyPrefix()))
		if err != nil {
			continue // a corrupt record should not hide the healthy ones
		}
		swaps = append(swaps, sw)
	}
	sort.Slice(swaps, func(i, j int) bool { return swaps[i].CreatedAt.After(swaps[j].CreatedAt) })
	return swaps, nil
}

// Export returns every stored swap as one JSON document, for a backup the user
// can carry to another browser. Browser storage cannot be copied by hand, is
// scoped to one origin, and is deleted by the same "clear site data" gesture
// people use to fix an unrelated site -- so this is not a convenience, it is the
// difference between a backup existing and not.
func (s *Store) Export() ([]byte, error) {
	swaps, err := s.List()
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(map[string]any{
		"format": "ferry-web-backup-1",
		"swaps":  swaps,
		"README": []string{
			"Every swap this browser held, including the ephemeral private keys.",
			"Store it like a private key: anything in it can spend the contracts it names.",
			"Import it from the same screen it was exported from, in any browser.",
		},
	}, "", "  ")
}

// Import merges an exported backup back in.
//
// Existing records are left alone rather than overwritten: the copy in this
// browser may have advanced past the backup (a funding seen, a refund signed),
// and silently replacing it would discard the newer signature.
//
// A backup is a file off the user's disk, so every record is checked before it
// is stored -- an unchecked one reaches code paths that dereference Key without
// asking. Bad records are counted and reported rather than aborting the run, so
// one malformed record cannot take out the whole import.
func (s *Store) Import(data []byte) (added, skipped, rejected int, err error) {
	var doc struct {
		Swaps []*Swap `json:"swaps"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return 0, 0, 0, fmt.Errorf("not a ferry backup file: %w", err)
	}
	if len(doc.Swaps) == 0 {
		return 0, 0, 0, errors.New("that file contains no swaps")
	}
	var problems []string
	for _, sw := range doc.Swaps {
		if sw == nil {
			rejected++
			problems = append(problems, "a null entry")
			continue
		}
		if verr := sw.validate(); verr != nil {
			rejected++
			problems = append(problems, fmt.Sprintf("%q: %v", sw.ID, verr))
			continue
		}
		if _, lerr := s.Load(sw.ID); lerr == nil {
			skipped++
			continue
		}
		if serr := s.Save(sw); serr != nil {
			// Storage itself failed -- almost always the quota. Unlike a bad
			// record this does not get better by moving to the next one.
			return added, skipped, rejected, serr
		}
		added++
	}
	if rejected > 0 && added == 0 && skipped == 0 {
		return added, skipped, rejected,
			fmt.Errorf("nothing in that file could be imported: %s", strings.Join(problems, "; "))
	}
	return added, skipped, rejected, nil
}

// validate rejects a swap record that could not have come out of this program.
//
// It is deliberately about structure rather than plausibility: the point is
// that every later code path can assume a key exists, a network is known and an
// id is storable, not that the swap describes a sensible trade.
func (sw *Swap) validate() error {
	if sw.ID == "" {
		return errors.New("has no id")
	}
	if _, err := (&Store{}).key(sw.ID); err != nil {
		return errors.New("has an id that is not 1..64 hex characters")
	}
	if _, err := NetworkParams(sw.Network); err != nil {
		return err
	}
	if sw.Key == nil {
		return errors.New("has no key, so nothing in it could ever spend a contract")
	}
	if len(sw.Key.Priv) != 32 {
		return fmt.Errorf("has a %d-byte private key, want 32", len(sw.Key.Priv))
	}
	if len(sw.Key.Pub) != 33 {
		return fmt.Errorf("has a %d-byte public key, want a 33-byte compressed one", len(sw.Key.Pub))
	}
	if len(sw.Key.PKH) != 20 {
		return fmt.Errorf("has a %d-byte pubkey hash, want 20", len(sw.Key.PKH))
	}
	// The three halves of the key have to agree, or the record names a contract
	// branch it cannot actually satisfy -- which is a discovery worth making at
	// import time rather than at refund time.
	if pub := sw.Key.PrivKey().PubKey().SerializeCompressed(); !bytes.Equal(pub, sw.Key.Pub) {
		return errors.New("has a public key that does not belong to its private key")
	}
	if !bytes.Equal(btcutil.Hash160(sw.Key.Pub), sw.Key.PKH) {
		return errors.New("has a pubkey hash that is not the hash of its public key")
	}
	if len(sw.SecretHash) != 0 && len(sw.SecretHash) != sha256.Size {
		return fmt.Errorf("has a %d-byte secret hash, want %d", len(sw.SecretHash), sha256.Size)
	}
	if len(sw.Secret) != 0 {
		if len(sw.Secret) != SecretSize {
			return fmt.Errorf("has a %d-byte secret, want %d", len(sw.Secret), SecretSize)
		}
		if len(sw.SecretHash) != 0 && !bytes.Equal(SHA256(sw.Secret), sw.SecretHash) {
			return errors.New("has a secret that does not hash to its own secret hash")
		}
	}
	if len(sw.Contract) > 0 {
		if _, err := ParseContract(sw.Contract); err != nil {
			return fmt.Errorf("has a contract this tool cannot parse: %w", err)
		}
	}
	return nil
}

// MemStorage is an in-memory Storage, used by tests and by any build that is
// not running in a browser.
type MemStorage struct{ m map[string]string }

// NewMemStorage returns an empty in-memory store.
func NewMemStorage() *MemStorage { return &MemStorage{m: map[string]string{}} }

func (s *MemStorage) Get(key string) (string, bool) { v, ok := s.m[key]; return v, ok }
func (s *MemStorage) Set(key, value string) error   { s.m[key] = value; return nil }
func (s *MemStorage) Remove(key string) error       { delete(s.m, key); return nil }
func (s *MemStorage) Keys() []string {
	out := make([]string, 0, len(s.m))
	for k := range s.m {
		out = append(out, k)
	}
	return out
}

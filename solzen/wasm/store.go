package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Backend is where swap records live. In the browser it is localStorage; under
// a test it is a map.
type Backend interface {
	Get(key string) (string, bool)
	Set(key, value string) error
	Delete(key string) error
	Keys() []string
}

// storeKeyPrefix namespaces this app's records, so a swap record is
// distinguishable from anything else on the origin.
const storeKeyPrefix = "solzen.swap."

// configKey is where the node URLs and program id are kept.
const configKey = "solzen.config"

// Store is one record per swap.
//
// Records are not encrypted. That is a real gap and it is stated here rather
// than buried: anything running on this origin can read them, and what they
// contain is a per-swap Zenon key plus, for the initiator, the secret. Neither
// is the user's wallet key, and the exposure is bounded by what they put into
// one swap -- but on a shared static host, "this origin" includes every other
// page the same account publishes.
type Store struct {
	mu      sync.Mutex
	backend Backend
}

func NewStore(b Backend) *Store { return &Store{backend: b} }

func (s *Store) Put(sw *Swap) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.Marshal(sw)
	if err != nil {
		return fmt.Errorf("encoding the swap record: %w", err)
	}
	return s.backend.Set(storeKeyPrefix+sw.ID, string(raw))
}

func (s *Store) Get(id string) (*Swap, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.backend.Get(storeKeyPrefix + id)
	if !ok {
		return nil, fmt.Errorf("no swap with id %s in this browser", id)
	}
	sw := new(Swap)
	if err := json.Unmarshal([]byte(raw), sw); err != nil {
		return nil, fmt.Errorf("the stored record for %s will not decode: %w", id, err)
	}
	return sw, nil
}

// List returns every swap, newest first.
//
// A record that will not decode is skipped rather than failing the list: one
// damaged entry should not hide the healthy ones, which are the ones with money
// in them.
func (s *Store) List() []*Swap {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Swap
	for _, k := range s.backend.Keys() {
		if !strings.HasPrefix(k, storeKeyPrefix) {
			continue
		}
		raw, ok := s.backend.Get(k)
		if !ok {
			continue
		}
		sw := new(Swap)
		if err := json.Unmarshal([]byte(raw), sw); err != nil {
			continue
		}
		out = append(out, sw)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	return out
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backend.Delete(storeKeyPrefix + id)
}

func (s *Store) SaveConfig(c Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.backend.Set(configKey, string(raw))
}

func (s *Store) LoadConfig() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	var c Config
	if raw, ok := s.backend.Get(configKey); ok {
		_ = json.Unmarshal([]byte(raw), &c)
	}
	return c
}

// Export is every swap in this browser as one document.
//
// This is the answer to "how do I move to another machine", and to the sharper
// version of that question: browser storage is erased by clearing site data, by
// a private window closing, and by reinstalling the browser, with no warning
// and no way to get a per-swap Zenon key back afterwards.
type Export struct {
	Format string  `json:"format"`
	Swaps  []*Swap `json:"swaps"`
}

const exportFormat = "solzen-export-1"

func (s *Store) Export() *Export {
	return &Export{Format: exportFormat, Swaps: s.List()}
}

// Import merges an export in, and never overwrites a record that is newer than
// the one being imported: restoring an old backup must not undo progress the
// browser has already made.
//
// Refused records are counted and named rather than aborting the run. A file
// with one bad entry in it is still a file whose other entries hold keys, and
// stopping half way through would leave the user with an unclear subset and an
// error message.
func (s *Store) Import(raw string) (imported, skipped int, refused []string, err error) {
	var e Export
	dec := json.NewDecoder(strings.NewReader(raw))
	if err := dec.Decode(&e); err != nil {
		return 0, 0, nil, fmt.Errorf("that file does not decode as a swap export: %w", err)
	}
	if e.Format != exportFormat {
		return 0, 0, nil, fmt.Errorf("that file says it is format %q, and this build reads %q", e.Format, exportFormat)
	}
	for i, sw := range e.Swaps {
		if sw == nil {
			refused = append(refused, fmt.Sprintf("record %d is empty", i+1))
			continue
		}
		if verr := sw.validate(); verr != nil {
			refused = append(refused, fmt.Sprintf("record %d (%s): %v", i+1, shortID(sw.ID), verr))
			continue
		}
		if existing, err := s.Get(sw.ID); err == nil && existing.Updated >= sw.Updated {
			skipped++
			continue
		}
		if err := s.Put(sw); err != nil {
			return imported, skipped, refused, err
		}
		imported++
	}
	return imported, skipped, refused, nil
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12] + "..."
	}
	if id == "" {
		return "no id"
	}
	return id
}

// MemoryBackend is the Backend used by tests and by the Node smoke run.
type MemoryBackend struct {
	mu sync.Mutex
	m  map[string]string
}

func NewMemoryBackend() *MemoryBackend { return &MemoryBackend{m: map[string]string{}} }

func (b *MemoryBackend) Get(k string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.m[k]
	return v, ok
}

func (b *MemoryBackend) Set(k, v string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.m[k] = v
	return nil
}

func (b *MemoryBackend) Delete(k string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.m, k)
	return nil
}

func (b *MemoryBackend) Keys() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.m))
	for k := range b.m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

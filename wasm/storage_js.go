//go:build js && wasm

package main

import (
	"errors"
	"fmt"
	"syscall/js"
)

// LocalStorage is a Storage backed by the browser's window.localStorage.
//
// localStorage rather than IndexedDB, deliberately: the records are a few
// kilobytes each and are read and written a handful of times per swap, so an
// asynchronous API would buy nothing and would cost synchronous Store methods,
// which is what keeps manager.go straight-line Go rather than built around
// promises.
//
// The trade is a quota -- 5 MB in most browsers, shared with everything else on
// this origin. A swap record is roughly 2 KB, so that is well over a thousand
// swaps, and Set surfaces a quota failure instead of dropping the write.
type LocalStorage struct{ v js.Value }

// NewLocalStorage returns the browser's localStorage, or an error when it is
// unreachable -- which it genuinely can be: Safari in private mode historically
// threw on write, and every browser refuses storage when the user has blocked
// site data or the page is in a sandboxed iframe. Failing here, before any key
// exists, turns that into a message on load rather than a swap whose refund key
// vanished at the moment it was signed.
func NewLocalStorage() (*LocalStorage, error) {
	v := js.Global().Get("localStorage")
	if !v.Truthy() {
		return nil, errors.New("this browser has no localStorage, so swaps cannot be saved. " +
			"Private-browsing or blocked site data is the usual cause")
	}
	s := &LocalStorage{v: v}
	// Probe with a real write. Reading is permitted in setups where writing is
	// not, and a store that silently accepts nothing is worse than none.
	probe := StorageKeyPrefix() + "probe"
	if err := s.Set(probe, "1"); err != nil {
		return nil, fmt.Errorf("this browser will not let the page save data (%w), so a swap's "+
			"refund key could not be kept. Leave private-browsing mode, or allow site data "+
			"for this origin", err)
	}
	s.v.Call("removeItem", probe)
	return s, nil
}

// Get reads one key. Absent is null, which is not the same as empty: a truthiness
// test would report a stored "" as missing, and "missing" is the answer that
// makes Load say the swap does not exist rather than that its record is corrupt.
func (s *LocalStorage) Get(key string) (v string, ok bool) {
	got := s.v.Call("getItem", key)
	if got.Type() != js.TypeString {
		return "", false
	}
	return got.String(), true
}

// Set writes one key, converting the browser's exception into an error.
//
// setItem throws on quota exhaustion, which is the one failure that matters:
// it happens on the write that persists a freshly signed refund, and a
// swallowed exception there loses the only copy of a key.
func (s *LocalStorage) Set(key, value string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("browser storage refused the write: %v", r)
		}
	}()
	s.v.Call("setItem", key, value)
	return nil
}

// Remove deletes one key. removeItem does not throw on a key that is not
// there, and the recover is for the same reason Set has one: a browser that
// refuses storage writes refuses this one too, and a swallowed exception would
// report a swap as deleted while it is still on the page after a reload.
func (s *LocalStorage) Remove(key string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("browser storage refused the delete: %v", r)
		}
	}()
	s.v.Call("removeItem", key)
	return nil
}

func (s *LocalStorage) Keys() []string {
	n := s.v.Get("length").Int()
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		k := s.v.Call("key", i)
		if k.Type() == js.TypeString {
			out = append(out, k.String())
		}
	}
	return out
}

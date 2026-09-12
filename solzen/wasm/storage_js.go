//go:build js && wasm

package main

import "syscall/js"

// localStorage as a Backend.
//
// Every access is guarded. A browser in a private window, or one told to block
// site data, throws on the accessor itself rather than returning empty -- and a
// page that lets that exception escape into Go panics the module and takes the
// whole app down instead of the one feature that needed storage.
type localStorageBackend struct{}

func newLocalStorageBackend() *localStorageBackend { return &localStorageBackend{} }

func storage() (js.Value, bool) {
	defer func() { _ = recover() }()
	s := js.Global().Get("localStorage")
	if !s.Truthy() {
		return js.Undefined(), false
	}
	return s, true
}

func (b *localStorageBackend) Get(key string) (value string, ok bool) {
	defer func() {
		if recover() != nil {
			value, ok = "", false
		}
	}()
	s, have := storage()
	if !have {
		return "", false
	}
	v := s.Call("getItem", key)
	if v.Type() != js.TypeString {
		return "", false
	}
	return v.String(), true
}

func (b *localStorageBackend) Set(key, value string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errStorage(r)
		}
	}()
	s, have := storage()
	if !have {
		return errNoStorage
	}
	s.Call("setItem", key, value)
	return nil
}

func (b *localStorageBackend) Delete(key string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errStorage(r)
		}
	}()
	s, have := storage()
	if !have {
		return errNoStorage
	}
	s.Call("removeItem", key)
	return nil
}

func (b *localStorageBackend) Keys() (keys []string) {
	defer func() {
		if recover() != nil {
			keys = nil
		}
	}()
	s, have := storage()
	if !have {
		return nil
	}
	n := s.Get("length").Int()
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		k := s.Call("key", i)
		if k.Type() == js.TypeString {
			out = append(out, k.String())
		}
	}
	return out
}

type storageError struct{ msg string }

func (e *storageError) Error() string { return e.msg }

var errNoStorage = &storageError{
	"this browser is not letting the page store anything, so a swap could not be saved. " +
		"A private window or blocked site data does this. Use the export before closing the tab.",
}

func errStorage(r any) error {
	if v, ok := r.(js.Error); ok {
		return &storageError{"browser storage refused the write: " + v.Error()}
	}
	return errNoStorage
}

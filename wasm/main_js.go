//go:build js && wasm

// Command ferry-wasm is the whole of Ferry, compiled for the browser.
//
// Every line that touches a key -- generation, contract construction, signing,
// local script-engine verification -- is Go running inside the page. The page is
// static files on a CDN; there is no server to trust because there is no server.
// Go rather than a JavaScript rewrite because the parts that lose money if they
// are subtly wrong -- the strict template parser, the branch selection, the
// script engine that executes a spend before anyone sees it -- are the parts
// worth not rewriting.
//
// What running in a browser adds:
//
//   - Storage is localStorage, scoped to one origin, and holds unencrypted
//     ephemeral keys. See docs/SECURITY.md and the export/import path.
//   - Every chain call is a fetch(), so a node is only usable if it sends CORS
//     headers. That is a property of the node, not of this program.
//   - The Zenon leg stays read-only. Creating and unlocking an HTLC needs Zenon
//     keys, and those stay in the user's own wallet.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"syscall/js"
)

// main installs the API on the page and then blocks forever.
//
// A Go WASM program that returns from main is torn down, taking the exported
// functions with it, so the select is not idle — it is what keeps the module
// alive between clicks.
func main() {
	store, err := NewLocalStorage()
	if err != nil {
		// The page is up and can render, but nothing can be saved. Say so
		// loudly and refuse to start rather than letting the user create a swap
		// whose refund key evaporates: an unsaveable swap is worse than none.
		js.Global().Set("ferryWasm", js.ValueOf(map[string]any{
			"ready": false,
			"error": err.Error(),
		}))
		signalReady()
		select {}
	}

	api := &API{Store: NewStore(store)}
	js.Global().Set("ferryWasm", js.ValueOf(map[string]any{
		"ready": true,
		// call(method, bodyJSON) -> Promise<responseJSON>
		//
		// One entry point rather than one export per operation: the Go surface
		// is a table of {name, JSON in, JSON out}, so one call covers all of it.
		"call": js.FuncOf(call(api)),
	}))
	signalReady()
	select {}
}

// call returns the JS-facing entry point.
//
// It must return immediately. Go's WASM runtime shares the one browser thread
// with everything else on the page, so blocking here while an HTTP request runs
// would deadlock: the fetch cannot resolve until the event loop is free, and the
// event loop is not free until this returns. So the work goes to a goroutine and
// the caller gets a promise.
func call(api *API) func(js.Value, []js.Value) any {
	return func(_ js.Value, args []js.Value) any {
		if len(args) < 1 || args[0].Type() != js.TypeString {
			return rejected("ferryWasm.call(method, bodyJSON) needs a method name")
		}
		method := args[0].String()
		var body []byte
		if len(args) > 1 && args[1].Type() == js.TypeString {
			body = []byte(args[1].String())
		}

		return js.Global().Get("Promise").New(js.FuncOf(
			func(_ js.Value, pargs []js.Value) any {
				resolve := pargs[0]
				go func() {
					// A panic in Go/WASM kills the whole module, and the module
					// is where the only copy of an unsaved key lives. Turning it
					// into a rejected call keeps the page alive long enough for
					// the user to export their swaps.
					defer func() {
						if r := recover(); r != nil {
							resolve.Invoke(string(errorJSON(panicErr(r))))
						}
					}()
					resolve.Invoke(string(underStoreLock(func() []byte {
						return api.Call(method, body)
					})))
				}()
				return nil
			}))
	}
}

// rejected wraps an argument error in an already-resolved promise, so the
// caller's error handling is the same whichever way a call fails.
func rejected(msg string) any {
	raw, _ := json.Marshal(map[string]string{"error": msg})
	return js.Global().Get("Promise").Call("resolve", string(raw))
}

// signalReady tells the page the module is installed. `ferryWasm` appears
// partway through go.run() and nothing in the DOM changes when it does, so an
// event saves the caller a polling loop.
//
// Best-effort, and the guard is not defensive dressing: CustomEvent and
// dispatchEvent are globals of a *browser*, and this module also runs under Node
// in the smoke test. An unguarded Call on a missing property panics, after the
// store is open and before anything can be exported -- the worst possible moment
// to lose the module. The caller has a polling fallback for exactly this reason.
func signalReady() {
	ctor := js.Global().Get("CustomEvent")
	dispatch := js.Global().Get("dispatchEvent")
	if ctor.Type() != js.TypeFunction || dispatch.Type() != js.TypeFunction {
		return
	}
	js.Global().Call("dispatchEvent", ctor.New("ferry-wasm-ready"))
}

type panicError struct{ v any }

// Error renders the panic value with fmt rather than js.ValueOf.
//
// js.ValueOf itself panics on a Go value it cannot convert — a struct, a slice
// of anything but bytes — and that panic happens inside the recover handler
// that exists to keep the module alive, which is the one place a second panic
// cannot be caught. fmt has no such failure mode.
func (e panicError) Error() string {
	if err, ok := e.v.(error); ok {
		return "internal error: " + err.Error()
	}
	return fmt.Sprintf("internal error: %v", e.v)
}

func panicErr(v any) error { return panicError{v: v} }

// underStoreLock runs one call while holding the browser's Web Lock for this
// instance's records, and returns its answer.
//
// Every call loads a record, decides something, and saves it, and the
// store's own lock and version check keep two such calls in ONE module from
// interleaving. Two tabs are two modules over the same localStorage, with a
// mutex each and no way to see the other's, so a stale audit in one tab could
// still save over a funding the other had just recorded. The Web Locks API is
// the one primitive the browser offers that spans tabs of an origin, so every
// call is made under it: one at a time, for this instance's swaps, across every
// tab. The cost is that calls queue behind each other -- a refresh in one tab
// holds an audit in another for as long as its node takes -- which is what
// correctness costs here.
//
// The lock is named for the storage prefix, so the development and production
// instances on one origin do not wait on each other. Where the API is missing
// -- Node running the smoke test, an old browser -- the call runs unlocked,
// exactly as before.
func underStoreLock(fn func() []byte) []byte {
	nav := js.Global().Get("navigator")
	locks := js.Undefined()
	if nav.Type() == js.TypeObject {
		locks = nav.Get("locks")
	}
	if locks.Type() != js.TypeObject || locks.Get("request").Type() != js.TypeFunction {
		// No lock. Outside a browser -- Node running the smoke test -- there
		// is one module and one tab, and the store's own lock is the whole
		// story. Inside a browser without the API there could be two tabs,
		// and running unlocked would be the race this exists to close; so a
		// browser without it is refused rather than served.
		if js.Global().Get("document").Type() == js.TypeObject {
			return errorJSON(errors.New("this browser has no Web Locks API, which Ferry needs to " +
				"keep two tabs from acting on one swap at once. Use a current browser"))
		}
		return fn()
	}
	out := make(chan []byte, 1)
	// The callback must return a promise and not block: it is invoked from the
	// browser's event loop, and Go code that blocks there deadlocks the module.
	// The work runs on a goroutine; the promise it resolves is what the browser
	// holds the lock open for.
	var holder js.Func
	holder = js.FuncOf(func(js.Value, []js.Value) any {
		var exec js.Func
		exec = js.FuncOf(func(_ js.Value, args []js.Value) any {
			release := args[0]
			go func() {
				defer exec.Release()
				defer holder.Release()
				defer func() {
					if r := recover(); r != nil {
						out <- errorJSON(panicErr(r))
					}
					release.Invoke()
				}()
				out <- fn()
			}()
			return nil
		})
		return js.Global().Get("Promise").New(exec)
	})
	locks.Call("request", "ferry:"+StorageKeyPrefix(), holder)
	return <-out
}

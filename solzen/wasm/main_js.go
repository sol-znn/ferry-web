//go:build js && wasm

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"syscall/js"
)

// The bridge between the page and this module.
//
// It exposes one function, `solzen.call(requestJSON, onProgress)`, returning a
// promise. Everything is JSON in and JSON out: the boundary carries no Go types
// and no js.Value handles, so there is nothing to keep in step between the two
// sides beyond the call table in api.go.

func main() {
	backend := newLocalStorageBackend()
	store := NewStore(backend)
	manager := NewManager(store)

	// Settings survive a reload. Loading them here rather than making the page
	// push them on every start means a call that arrives before the UI has
	// mounted still reaches the right nodes.
	cfg := store.LoadConfig()
	if cfg.SolanaURL == "" && cfg.ZenonURL == "" && cfg.SolProgram == "" {
		cfg = DefaultConfig()
	}
	if cfg.SolProgram == "" {
		cfg.SolProgram = SolProgramID
	}
	manager.SetConfig(cfg)

	api := NewAPI(manager)

	js.Global().Set("solzen", js.ValueOf(map[string]any{
		"build": BuildName,
		"call":  js.FuncOf(callFn(api)),
	}))

	// Park forever. The module is a library, not a program: returning from main
	// would tear down the Go runtime and every callback with it.
	select {}
}

func callFn(api *API) func(js.Value, []js.Value) any {
	return func(_ js.Value, args []js.Value) any {
		if len(args) == 0 || args[0].Type() != js.TypeString {
			return rejected("solzen.call needs a JSON request string")
		}
		request := args[0].String()

		// An optional progress callback. Proof of work is the only thing slow
		// enough to need one, and it is slow enough to need one badly: without
		// it a four-minute mine is indistinguishable from a hung tab.
		var onProgress js.Value
		if len(args) > 1 && args[1].Type() == js.TypeFunction {
			onProgress = args[1]
		}

		return js.Global().Get("Promise").New(js.FuncOf(func(_ js.Value, pargs []js.Value) any {
			resolve := pargs[0]
			// Every call runs on its own goroutine. Awaiting a fetch means
			// parking until a JS callback fires, and a callback cannot fire
			// while Go is holding the only thread -- so doing this work on the
			// caller's stack would deadlock the page on the first request.
			go func() {
				// A panic in Go/WASM aborts the runtime and takes the module
				// with it -- and the module is the only way to reach the swap
				// records. There is no recovery file here (README gap #6), so
				// the per-swap Zenon key and the initiator's secret live in
				// localStorage and are readable in practice only through
				// store.export, which is a call through this bridge. Losing
				// the bridge therefore loses the money, not just the feature.
				//
				// So a panic becomes a failed call. The page stays up, every
				// other swap keeps working, and Export is still there.
				defer func() {
					if r := recover(); r != nil {
						raw, _ := json.Marshal(Response{Error: panicMessage(r)})
						resolve.Invoke(string(raw))
					}
				}()
				var progress func(uint64)
				if onProgress.Truthy() {
					progress = func(hashes uint64) {
						onProgress.Invoke(float64(hashes))
					}
				}
				resp := api.Handle(context.Background(), request, progress)
				raw, err := json.Marshal(resp)
				if err != nil {
					raw, _ = json.Marshal(Response{Error: "the reply could not be encoded: " + err.Error()})
				}
				resolve.Invoke(string(raw))
			}()
			return nil
		}))
	}
}

func rejected(message string) any {
	raw, _ := json.Marshal(Response{Error: message})
	return js.Global().Get("Promise").Call("resolve", string(raw))
}

// panicMessage renders a recovered panic value, with fmt and never js.ValueOf.
//
// js.ValueOf panics on any Go value it cannot convert -- a struct, a slice of
// anything but bytes -- and it would do so inside the recover handler that
// exists to keep the module alive, which is the one place a second panic cannot
// be caught. fmt has no such failure mode.
func panicMessage(v any) string {
	if err, ok := v.(error); ok {
		return "internal error: " + err.Error()
	}
	return fmt.Sprintf("internal error: %v", v)
}

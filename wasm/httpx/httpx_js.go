//go:build js && wasm

package httpx

import (
	"context"
	"errors"
	"fmt"
	"syscall/js"
	"time"
)

// do issues the request with the browser's fetch().
//
// It runs on a goroutine, never on the JS callback that started the operation:
// awaiting a promise here means parking the goroutine until a JS callback fires,
// and a callback cannot fire while Go is holding the only thread. Every caller
// reaches this from inside the goroutine main_js.go spawns, which is what makes
// that safe.
func do(ctx context.Context, req Request) (*Response, error) {
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// AbortController is how a fetch is cancelled. Without it a request to a
	// host that accepts the connection and then says nothing would hold the
	// goroutine past its own timeout, because the promise simply never settles.
	controller := js.Global().Get("AbortController")
	var signal js.Value
	if controller.Truthy() {
		c := controller.New()
		signal = c.Get("signal")
		stop := context.AfterFunc(ctx, func() { c.Call("abort") })
		defer stop()
	}

	opts := map[string]any{
		"method": req.Method,
		// Explicit rather than relying on the defaults: no cookies, no
		// credentials, no redirect chasing that could land the request on a
		// different origin than the one the user named in settings.
		"credentials": "omit",
		"mode":        "cors",
		"redirect":    "follow",
		"cache":       "no-store",
	}
	if len(req.Headers) > 0 {
		h := map[string]any{}
		for k, v := range req.Headers {
			h[k] = v
		}
		opts["headers"] = h
	}
	if req.Body != nil {
		opts["body"] = string(req.Body)
	}
	if signal.Truthy() {
		opts["signal"] = signal
	}

	resp, err := await(ctx, js.Global().Call("fetch", req.URL, opts))
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s: timed out after %s", req.URL, timeout)
		}
		return nil, wrapNetworkErr(req.URL, err)
	}

	// Content-Length is advisory — it can be absent or a lie — so it is used
	// only to refuse an obviously oversized body before downloading it. The
	// real cap is applied to the bytes that actually arrive.
	if cl := resp.Get("headers").Call("get", "content-length"); cl.Type() == js.TypeString {
		var n int64
		if _, ferr := fmt.Sscanf(cl.String(), "%d", &n); ferr == nil && n > MaxBody {
			return nil, fmt.Errorf("%s: response is %d bytes, over the %d byte limit", req.URL, n, MaxBody)
		}
	}

	buf, err := await(ctx, resp.Call("arrayBuffer"))
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s: timed out after %s while reading the response", req.URL, timeout)
		}
		return nil, fmt.Errorf("%s: reading the response failed: %w", req.URL, err)
	}
	u8 := js.Global().Get("Uint8Array").New(buf)
	n := u8.Get("byteLength").Int()
	if n > MaxBody {
		n = MaxBody
	}
	body := make([]byte, n)
	js.CopyBytesToGo(body, u8)

	return &Response{
		Status:     resp.Get("status").Int(),
		StatusText: resp.Get("statusText").String(),
		Body:       body,
	}, nil
}

// await blocks the calling goroutine until a JS promise settles, or until ctx
// is done.
//
// The ctx case is not redundant with the AbortController above. Aborting is what
// makes the browser settle the promise, and it is only wired up where
// AbortController exists -- so without this the one failure the timeout was
// written for, a host that accepts the connection and then says nothing, would
// park this goroutine for the life of the page on any runtime lacking it. That
// includes the Node process the smoke test runs in on older releases.
//
// The two js.Funcs are released when the PROMISE settles, not when this
// function returns. They are not garbage: each pins a Go function in a table
// the JS side holds a reference to, so leaking one per request would grow that
// table for the life of the page. But releasing them on the ctx path would free
// callbacks that JS is still holding, and the runtime answers a call to one of
// those with "call to released function" on the console.
func await(ctx context.Context, promise js.Value) (js.Value, error) {
	type result struct {
		v   js.Value
		err error
	}
	// Buffered because nobody may be receiving: after a ctx return the callbacks
	// still fire when the promise eventually settles, and a send on an
	// unbuffered channel would deadlock the goroutine running them.
	ch := make(chan result, 1)

	var onOK, onErr js.Func
	release := func() {
		onOK.Release()
		onErr.Release()
	}
	onOK = js.FuncOf(func(_ js.Value, args []js.Value) any {
		defer release()
		var v js.Value
		if len(args) > 0 {
			v = args[0]
		}
		ch <- result{v: v}
		return nil
	})
	onErr = js.FuncOf(func(_ js.Value, args []js.Value) any {
		defer release()
		msg := "the request failed"
		if len(args) > 0 && args[0].Truthy() {
			// A DOMException from abort(), or the TypeError fetch raises for a
			// network or CORS failure. Its own message is the most specific
			// thing available, even when that is only "Failed to fetch".
			if m := args[0].Get("message"); m.Type() == js.TypeString {
				msg = m.String()
			} else {
				msg = args[0].String()
			}
		}
		ch <- result{err: errors.New(msg)}
		return nil
	})

	promise.Call("then", onOK, onErr)
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		return js.Undefined(), ctx.Err()
	}
}

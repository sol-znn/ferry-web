//go:build !(js && wasm)

package httpx

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"
)

// do is the ordinary net/http implementation.
//
// Nothing in the shipped browser build reaches it — the build tag sees to that,
// which is the whole point, since linking net/http is what this package exists
// to avoid there. It is here so `go build`, `go vet` and `go test` on a normal
// GOOS still compile the packages above it, and so the same tree could produce
// a command-line build of this logic without touching a line of it.
func do(ctx context.Context, req Request) (*Response, error) {
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body io.Reader
	if req.Body != nil {
		body = bytes.NewReader(req.Body)
	}
	r, err := http.NewRequestWithContext(ctx, req.Method, req.URL, body)
	if err != nil {
		return nil, err
	}
	for k, v := range req.Headers {
		r.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return nil, wrapNetworkErr(req.URL, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody))
	if err != nil {
		return nil, err
	}
	return &Response{Status: resp.StatusCode, StatusText: resp.Status, Body: raw}, nil
}

//go:build !(js && wasm)

package znn

import (
	"context"
	"fmt"
)

// The WebSocket transport is the browser's, and there is no browser here.
//
// This build exists so the package compiles and its tests run under `go test`
// on a developer's machine. Nothing in the test suite dials a node, so a
// transport that refuses is the honest stub: quietly falling back to HTTP would
// make a native run exercise a different code path from the one that ships.
func wsCall(_ context.Context, url string, _ func(id int) ([]byte, error)) ([]byte, error) {
	return nil, fmt.Errorf("%s: websocket transport is only available in the browser build", url)
}

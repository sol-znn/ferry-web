// Package httpx is the one HTTP call this program makes, behind an interface
// thin enough to have two implementations.
//
// It exists for a size reason that turned out to be the difference between a
// usable page and an unusable one. Compiling `net/http` to WebAssembly costs
// about 8 MB raw / 2.2 MB gzipped, because the package unconditionally links
// crypto/tls, x509 and the HTTP/2 stack — none of which run under GOOS=js,
// where the round-tripper hands everything to the browser's fetch() anyway. The
// linker cannot prune them: http.Client reaches Transport reaches tls.
//
// Calling fetch() directly instead takes the whole download from ~4.0 MB to
// ~1.9 MB gzipped. That is the largest single lever in this port, and it costs
// one small file per build target rather than any change to how a request is
// made.
//
// The non-browser build keeps net/http, so `go test` and `go vet` on a normal
// GOOS still compile and exercise everything above this line.
package httpx

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// MaxBody caps a response, since a node is not trusted to be well-behaved.
// 8 MiB is far above any Solana or Zenon RPC response this app asks for.
const MaxBody = 8 << 20

// Request is one HTTP call.
type Request struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
	Timeout time.Duration
}

// Response is what came back. Body is already read and capped at MaxBody.
type Response struct {
	Status     int
	StatusText string
	Body       []byte
}

// OK reports whether the status is 2xx.
func (r *Response) OK() bool { return r.Status >= 200 && r.Status < 300 }

// Text returns the body trimmed, which is what every caller wants for an error
// message.
func (r *Response) Text() string { return strings.TrimSpace(string(r.Body)) }

// Do performs the request. It is implemented per build target: fetch() in the
// browser, net/http everywhere else.
func Do(ctx context.Context, req Request) (*Response, error) { return do(ctx, req) }

// errCORS is the hint attached to a fetch failure.
//
// The browser deliberately refuses to say whether a cross-origin request failed
// because the host is down, the TLS certificate is bad, or the response lacked
// an Access-Control-Allow-Origin header — telling a page the difference would
// itself leak information about the network it is on. All three arrive as the
// same opaque TypeError, so the message has to name all three. Getting this
// wrong sends people to check a node that was never the problem.
const errCORS = "the browser blocked or could not complete this request. " +
	"Either the node is unreachable, or it did not send the CORS headers a web page needs " +
	"(Access-Control-Allow-Origin). Solana's public RPC does; a self-hosted node or a " +
	"Zenon node usually needs it adding to its reverse proxy"

func wrapNetworkErr(url string, err error) error {
	return fmt.Errorf("%s: %s (%w)", url, errCORS, err)
}

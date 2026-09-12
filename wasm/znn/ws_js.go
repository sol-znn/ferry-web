//go:build js && wasm

package znn

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"syscall/js"
	"time"
)

// JSON-RPC over a WebSocket, for the transport most public Zenon nodes are
// actually reachable on.
//
// A go-zenon node serves the same JSON-RPC twice: over HTTP on 35997 and over a
// WebSocket on 35998. Which one a browser can use is not a preference:
//
//   - The HTTP endpoint is subject to CORS. A public node that has not been
//     configured to send Access-Control-Allow-Origin is unreachable from any
//     page, and the request fails before it is sent, with an error the page
//     cannot even read.
//   - A WebSocket handshake is not a CORS-preflighted request. The browser
//     sends an Origin header and the server decides, but there is no preflight
//     to fail and no header a node has to have been configured to return.
//
// So the WebSocket endpoint works against nodes the HTTP one does not, and for
// a static site with no backend to proxy through, that is the difference
// between verifying the Zenon leg and taking the counterparty's word for it.
//
// Everything here is still read-only. A socket carries the same calls the HTTP
// client made, and this package has no method that signs anything.

// wsIdle is how long an unused connection is kept before it is dropped.
//
// Kept rather than closed after every call because verification makes four or
// five calls in a row and a handshake per call would triple the time it takes;
// dropped rather than held forever because a page left open overnight should
// not be holding a socket to somebody's node.
const wsIdle = 2 * time.Minute

// wsHandshake bounds the connect itself, separately from the call timeout: a
// host that accepts the TCP connection and never completes the upgrade would
// otherwise consume the whole call budget before the request was even sent.
const wsHandshake = 15 * time.Second

var (
	wsMu    sync.Mutex
	wsConns = map[string]*wsConn{}
)

// wsConn is one live socket and the calls waiting on it.
type wsConn struct {
	url string

	mu      sync.Mutex
	sock    js.Value
	open    bool
	failed  error
	pending map[int]chan []byte
	nextID  int
	funcs   []js.Func
	lastUse time.Time
	ready   chan struct{}
}

// wsCall sends one JSON-RPC request over a shared socket and returns the raw
// response frame.
func wsCall(ctx context.Context, url string, request func(id int) ([]byte, error)) ([]byte, error) {
	conn, err := dialWS(ctx, url)
	if err != nil {
		return nil, err
	}

	conn.mu.Lock()
	if conn.failed != nil {
		err := conn.failed
		conn.mu.Unlock()
		return nil, err
	}
	conn.nextID++
	id := conn.nextID
	// Buffered: the socket's message callback must never block, and it will
	// still fire for a request whose caller has already given up on the timeout.
	reply := make(chan []byte, 1)
	conn.pending[id] = reply
	conn.lastUse = time.Now()
	sock := conn.sock
	conn.mu.Unlock()

	defer func() {
		conn.mu.Lock()
		delete(conn.pending, id)
		conn.mu.Unlock()
	}()

	body, err := request(id)
	if err != nil {
		return nil, err
	}
	if err := jsSend(sock, string(body)); err != nil {
		conn.fail(err)
		return nil, err
	}

	select {
	case frame := <-reply:
		return frame, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%s: no answer before the call timed out", url)
	}
}

// jsSend calls socket.send, turning the exception a closed socket throws into
// an error rather than a panic that takes the whole module down.
func jsSend(sock js.Value, payload string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("sending on the websocket failed: %v", r)
		}
	}()
	sock.Call("send", payload)
	return nil
}

// dialWS returns a connected socket for url, opening one if needed.
func dialWS(ctx context.Context, url string) (*wsConn, error) {
	wsMu.Lock()
	conn, ok := wsConns[url]
	if ok {
		conn.mu.Lock()
		stale := conn.failed != nil || (!conn.open && conn.ready == nil) ||
			(conn.open && time.Since(conn.lastUse) > wsIdle)
		conn.mu.Unlock()
		if stale {
			conn.close()
			delete(wsConns, url)
			ok = false
		}
	}
	if !ok {
		conn = newWSConn(url)
		wsConns[url] = conn
	}
	wsMu.Unlock()

	// Wait for the handshake. Every caller of a connection that is still opening
	// waits on the same channel, so five calls made at once share one socket
	// rather than racing to open five.
	handshake, cancel := context.WithTimeout(ctx, wsHandshake)
	defer cancel()
	select {
	case <-conn.ready:
	case <-handshake.Done():
		return nil, fmt.Errorf("%s: the websocket did not open within %s. Check the URL and that "+
			"the node accepts connections from a browser", url, wsHandshake)
	}

	conn.mu.Lock()
	defer conn.mu.Unlock()
	if conn.failed != nil {
		return nil, conn.failed
	}
	if !conn.open {
		return nil, fmt.Errorf("%s: the websocket closed before it could be used", url)
	}
	return conn, nil
}

// newWSConn opens a socket and wires its callbacks. It does not block: callers
// wait on conn.ready.
func newWSConn(url string) *wsConn {
	c := &wsConn{
		url:     url,
		pending: map[int]chan []byte{},
		ready:   make(chan struct{}),
		lastUse: time.Now(),
	}

	ctor := js.Global().Get("WebSocket")
	if !ctor.Truthy() {
		c.failed = errors.New("this browser has no WebSocket support")
		close(c.ready)
		return c
	}

	defer func() {
		if r := recover(); r != nil {
			c.failed = fmt.Errorf("%s is not a URL a websocket can be opened to: %v", url, r)
			close(c.ready)
		}
	}()
	c.sock = ctor.New(url)

	// One-shot, because ready is closed exactly once whichever way the
	// handshake resolves and closing a closed channel is a panic.
	var once sync.Once
	settle := func() { once.Do(func() { close(c.ready) }) }

	onOpen := js.FuncOf(func(js.Value, []js.Value) any {
		c.mu.Lock()
		c.open = true
		c.mu.Unlock()
		settle()
		return nil
	})
	onMessage := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) > 0 {
			c.deliver(args[0].Get("data"))
		}
		return nil
	})
	// A browser deliberately does not say WHY a websocket failed -- the error
	// event carries no detail, by design, so that a page cannot use it to probe
	// the network it is on. So the message says what is worth checking rather
	// than pretending to a diagnosis.
	onError := js.FuncOf(func(js.Value, []js.Value) any {
		c.fail(fmt.Errorf("%s: the websocket connection failed. The browser does not report why; "+
			"the usual causes are a wrong port (a Zenon node serves the websocket on 35998, not "+
			"35997), a node that is not reachable, or a page served over https trying to reach a "+
			"ws:// rather than wss:// node", url))
		settle()
		return nil
	})
	onClose := js.FuncOf(func(js.Value, []js.Value) any {
		c.fail(fmt.Errorf("%s: the websocket closed", url))
		settle()
		return nil
	})

	c.funcs = []js.Func{onOpen, onMessage, onError, onClose}
	c.sock.Set("onopen", onOpen)
	c.sock.Set("onmessage", onMessage)
	c.sock.Set("onerror", onError)
	c.sock.Set("onclose", onClose)
	return c
}

// deliver routes one frame to the call that is waiting for it.
func (c *wsConn) deliver(data js.Value) {
	if data.Type() != js.TypeString {
		return // a node sending binary frames is not speaking this protocol
	}
	frame := []byte(data.String())
	id, ok := frameID(frame)
	if !ok {
		return
	}
	c.mu.Lock()
	reply := c.pending[id]
	c.mu.Unlock()
	if reply != nil {
		// Non-blocking: the channel is buffered for one, and a second frame
		// carrying the same id must not wedge the socket's callback.
		select {
		case reply <- frame:
		default:
		}
	}
}

// fail marks the connection dead and releases everything waiting on it, so a
// dropped socket surfaces as an error on every in-flight call rather than as a
// set of calls that never return.
func (c *wsConn) fail(err error) {
	c.mu.Lock()
	if c.failed == nil {
		c.failed = err
	}
	c.open = false
	pending := c.pending
	c.pending = map[int]chan []byte{}
	c.mu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
}

// close releases the socket and the Go functions the JS side holds references
// to. Each js.Func pins an entry in a table that is never collected on its own,
// so a page that reconnects repeatedly would otherwise grow it for the life of
// the page.
func (c *wsConn) close() {
	c.mu.Lock()
	sock, funcs := c.sock, c.funcs
	c.funcs = nil
	c.open = false
	c.mu.Unlock()

	if sock.Truthy() {
		func() {
			defer func() { _ = recover() }()
			sock.Set("onclose", js.Null())
			sock.Call("close")
		}()
	}
	for _, f := range funcs {
		f.Release()
	}
}

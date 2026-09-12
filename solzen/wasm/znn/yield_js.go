//go:build js && wasm

package znn

import "syscall/js"

// Giving the browser its thread back, without going through a timer.
//
// Go compiled to WebAssembly runs on the page's one main thread. A mining loop
// that never blocks never lets the browser paint, deliver a click, or run the
// progress callback to any visible effect -- the tab looks crashed for the
// minutes an unlock takes.
//
// The obvious yield, time.Sleep, is the wrong one. Sleeping schedules a timer,
// and Chrome clamps timers in a page it considers hidden to roughly one a
// second. A miner yielding a thousand times would then take a thousand seconds
// of doing nothing, which turns "slow" into "apparently stuck" the moment
// somebody switches tabs.
//
// MessageChannel is the way out: a message posted to a port is a macrotask, so
// the browser gets a chance to paint between slices, and it is not subject to
// timer throttling. It is the same mechanism React's scheduler uses, for the
// same reason.
var (
	yieldChannel js.Value
	yieldPort    js.Value
	yieldReady   chan struct{}
	yieldHandler js.Func
)

func init() {
	mc := js.Global().Get("MessageChannel")
	if !mc.Truthy() {
		return
	}
	yieldChannel = mc.New()
	yieldPort = yieldChannel.Get("port2")
	yieldReady = make(chan struct{}, 1)
	yieldHandler = js.FuncOf(func(js.Value, []js.Value) any {
		select {
		case yieldReady <- struct{}{}:
		default:
		}
		return nil
	})
	yieldChannel.Get("port1").Set("onmessage", yieldHandler)
	// A port only delivers once started; port1 is started implicitly by
	// assigning onmessage, port2 is the one we post from.
	yieldPort.Call("start")
}

// yieldToHost parks this goroutine until the browser has had a turn.
//
// Blocking on the channel is what makes it work: with no runnable goroutine,
// the Go runtime returns control to JavaScript, which then runs the event loop
// -- paint included -- before delivering the message that wakes this one.
func yieldToHost() {
	if yieldReady == nil {
		return
	}
	yieldPort.Call("postMessage", 0)
	<-yieldReady
}

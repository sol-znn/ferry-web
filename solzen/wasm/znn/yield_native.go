//go:build !js

package znn

// Off the browser, nothing shares this thread with the miner: the CLI's other
// goroutines are blocked on the network, and a test wants the loop to run flat
// out. So the yield is nothing at all.
//
// It is not simply absent, because the mining loop must read the same on both
// targets. A `if runtime.GOOS == "js"` inside that loop would be one more thing
// to get wrong in the one function whose speed is the whole cost of a Zenon
// block.
func yieldToHost() {}

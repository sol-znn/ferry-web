// Loading the WebAssembly module and talking to it.
//
// The module is the whole swap engine: contract construction, verification, the
// leg-ordering rules, Zenon signing, and the store. This file is the only place
// in the page that knows how to reach it, and it exposes one function, so a
// component can never be tempted to reimplement a rule the module already has.

let ready = null

export function boot() {
  if (ready) return ready
  ready = (async () => {
    // Go's shim defines globalThis.Go. It is copied from the same toolchain
    // that compiled the module on every build, so the two cannot drift.
    await import(/* @vite-ignore */ `${import.meta.env.BASE_URL}wasm_exec.js`)
    const go = new globalThis.Go()
    const url = `${import.meta.env.BASE_URL}solzen.wasm`

    let instance
    if (WebAssembly.instantiateStreaming) {
      const result = await WebAssembly.instantiateStreaming(fetch(url), go.importObject)
      instance = result.instance
    } else {
      const bytes = await (await fetch(url)).arrayBuffer()
      const result = await WebAssembly.instantiate(bytes, go.importObject)
      instance = result.instance
    }
    // Not awaited: go.run resolves only when the Go program exits, and this one
    // parks forever on purpose so its callbacks stay alive.
    go.run(instance)

    // go.run installs the bridge synchronously before parking, but only once
    // the Go runtime has started, which is a microtask away.
    for (let i = 0; i < 200 && !globalThis.solzen; i++) {
      await new Promise((r) => setTimeout(r, 5))
    }
    if (!globalThis.solzen) throw new Error('the swap engine did not start')
    return globalThis.solzen
  })()
  return ready
}

// call is every interaction with the engine.
//
// onProgress is invoked while Zenon proof of work is being mined, which is the
// only operation slow enough to need it -- and slow enough that a page without
// it is indistinguishable from a hung one.
export async function call(method, params, onProgress) {
  const engine = await boot()
  const raw = await engine.call(JSON.stringify(params === undefined ? { method } : { method, params }), onProgress)
  const parsed = JSON.parse(raw)
  if (parsed.error) throw new Error(parsed.error)
  return parsed.result
}

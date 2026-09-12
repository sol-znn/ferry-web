/**
 * Loading the signing engine.
 *
 * Everything that can move coins lives in a WebAssembly module compiled from Go
 * -- contract construction, the strict template parser, branch selection,
 * script-engine verification. This file only gets that module running and hands
 * the app a way to call into it.
 *
 * It is about 9.5 MB, ~2.9 MB over the wire once the host has gzipped it, and
 * that is the price of not rewriting Bitcoin address decoding and transaction
 * signing in JavaScript. Fetched once and then cached under a content-hashed
 * name, so the cost lands on a first visit and nowhere else. Progress is
 * reported while it streams, because a silent multi-megabyte wait reads as a
 * broken page.
 */

/** The shape Go installs on window. */
interface FerryWasm {
  ready: boolean
  error?: string
  call?: (method: string, body: string) => Promise<string>
}

declare global {
  interface Window {
    ferryWasm?: FerryWasm
    Go?: new () => {
      importObject: WebAssembly.Imports
      run: (instance: WebAssembly.Instance) => Promise<void>
    }
  }
}

export type LoadPhase = 'idle' | 'fetching' | 'compiling' | 'starting' | 'ready' | 'failed'

export interface LoadState {
  phase: LoadPhase
  /** 0..1 while fetching, when the server sent a Content-Length. */
  progress: number
  loadedBytes: number
  totalBytes: number
  error: string
}

type Listener = (s: LoadState) => void

const state: LoadState = {
  phase: 'idle',
  progress: 0,
  loadedBytes: 0,
  totalBytes: 0,
  error: '',
}
const listeners = new Set<Listener>()

function emit(patch: Partial<LoadState>) {
  Object.assign(state, patch)
  for (const fn of listeners) fn(state)
}

export function onLoadProgress(fn: Listener): () => void {
  listeners.add(fn)
  fn(state)
  return () => listeners.delete(fn)
}

export function loadState(): LoadState {
  return state
}

declare const __WASM_VERSION__: string

/**
 * Where the module and Go's runtime shim are fetched from.
 *
 * Both live in public/, so Vite copies them to the site root verbatim rather
 * than hashing them into the asset pipeline -- the module is produced by the Go
 * toolchain, not the bundler, and the shim has to match it exactly. BASE_URL is
 * './', which resolves against the document, and with hash routing the document
 * is always index.html.
 *
 * Verbatim copying costs the content hash that would otherwise bust caches, and
 * a stale shim paired with a fresh module fails in ways that are miserable to
 * read. Hence the version query, set by the build script to the module's own
 * content hash.
 */
function assetURL(name: string): string {
  const base = import.meta.env.BASE_URL || './'
  return `${base}${name}?v=${__WASM_VERSION__}`
}

let started: Promise<void> | null = null

/** Load and start the module. Safe to call repeatedly; the first call wins. */
export function startWasm(): Promise<void> {
  started ??= boot()
  return started
}

async function boot(): Promise<void> {
  try {
    // wasm_exec.js is Go's own runtime shim, copied verbatim out of the Go
    // distribution at build time rather than vendored by hand, so it always
    // matches the compiler that produced the module beside it.
    await loadScript(assetURL('wasm_exec.js'))
    if (!window.Go) throw new Error('the Go runtime shim did not define window.Go')

    const bytes = await fetchWithProgress(assetURL('ferry.wasm'))

    emit({phase: 'compiling'})
    const go = new window.Go()
    // Compiled from the downloaded bytes rather than instantiateStreaming,
    // because streaming needs the exact Content-Type application/wasm and a
    // static host that gets it wrong would fail with no way to recover. Reading
    // the body ourselves also gives the progress the download needs.
    const {instance} = await WebAssembly.instantiate(bytes, go.importObject)

    emit({phase: 'starting'})
    // The module's main() blocks forever after installing window.ferryWasm, so
    // this promise is not awaited: awaiting it would hang until the page closes.
    // It is watched only so a crash inside Go surfaces instead of vanishing.
    void go.run(instance).catch((e: unknown) => {
      emit({phase: 'failed', error: `the signing engine stopped: ${String(e)}`})
    })

    await waitForReady()

    const api = window.ferryWasm
    if (!api?.ready) {
      // Go started but refused to run — the storage probe in main() failed, and
      // it says why. That is a real refusal, not a load error: a swap whose
      // refund key cannot be saved is worse than no swap.
      throw new Error(api?.error || 'the signing engine started but did not become ready')
    }
    emit({phase: 'ready', progress: 1})
  } catch (e) {
    emit({phase: 'failed', error: e instanceof Error ? e.message : String(e)})
    throw e
  }
}

function loadScript(src: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const el = document.createElement('script')
    el.src = src
    el.onload = () => resolve()
    el.onerror = () => reject(new Error(`could not load ${src}`))
    document.head.appendChild(el)
  })
}

/**
 * Fetch the module, reporting bytes as they arrive.
 *
 * Content-Length is missing whenever the host streams the response compressed,
 * which is the common case for a gzipped .wasm — so the bar falls back to a
 * byte counter rather than pretending to know a percentage it does not.
 */
async function fetchWithProgress(url: string): Promise<ArrayBuffer> {
  emit({phase: 'fetching', progress: 0, loadedBytes: 0, totalBytes: 0})
  const res = await fetch(url)
  if (!res.ok)
    throw new Error(`could not fetch the signing engine: ${res.status} ${res.statusText}`)

  const total = Number(res.headers.get('content-length') ?? 0)
  emit({totalBytes: total})

  if (!res.body) return res.arrayBuffer()

  const reader = res.body.getReader()
  const chunks: Uint8Array[] = []
  let loaded = 0
  for (;;) {
    const {done, value} = await reader.read()
    if (done) break
    chunks.push(value)
    loaded += value.length
    emit({loadedBytes: loaded, progress: total ? Math.min(loaded / total, 1) : 0})
  }

  const out = new Uint8Array(loaded)
  let at = 0
  for (const c of chunks) {
    out.set(c, at)
    at += c.length
  }
  return out.buffer
}

/**
 * Wait for the module to install its API. Three ways out, because none is
 * reliable alone:
 *
 *   - The value may already be there: go.run() runs synchronously up to the
 *     module's first block, so main() can finish before this is even called.
 *   - Go dispatches `ferry-wasm-ready` when it installs the API.
 *   - A poll, because that event is best-effort on the Go side and a missed
 *     signal would strand the page on a spinner forever.
 *
 * The timeout is the fourth: a module that never reaches main() should say so
 * rather than hang.
 */
function waitForReady(timeoutMs = 20000): Promise<void> {
  if (window.ferryWasm) return Promise.resolve()
  return new Promise((resolve, reject) => {
    const cleanup = () => {
      window.removeEventListener('ferry-wasm-ready', done)
      clearInterval(poll)
      clearTimeout(timer)
    }
    const done = () => {
      cleanup()
      resolve()
    }
    const poll = setInterval(() => {
      if (window.ferryWasm) done()
    }, 50)
    const timer = setTimeout(() => {
      cleanup()
      reject(new Error('the signing engine did not start within 20 seconds'))
    }, timeoutMs)
    window.addEventListener('ferry-wasm-ready', done)
  })
}

/**
 * Call into the module.
 *
 * The response is a JSON document that either is the result or carries an
 * `error` string, so there is one error path rather than two.
 */
export async function wasmCall<T>(method: string, body?: unknown): Promise<T> {
  await startWasm()
  const call = window.ferryWasm?.call
  if (!call) throw new Error(window.ferryWasm?.error || 'the signing engine is not available')

  const raw = await call(method, JSON.stringify(body ?? {}))
  let data: unknown
  try {
    data = raw ? JSON.parse(raw) : {}
  } catch {
    throw new Error(`the signing engine returned something that is not JSON: ${raw.slice(0, 200)}`)
  }
  const doc = data as {error?: string; code?: string}
  // A zenon verification failure is a successful call carrying an `error`
  // alongside its result, so only a bare error document -- error, and at most
  // a code naming its kind -- is thrown.
  if (doc.error && Object.keys(doc).every((k) => k === 'error' || k === 'code')) {
    throw new EngineError(doc.error, doc.code)
  }
  return data as T
}

/**
 * An error the engine returned, with the kind it named, if it named one. The
 * one kind so far is `stale`: the record changed under the call and the engine
 * declined to overwrite it, so the caller makes the call again against the
 * record as it now is.
 */
export class EngineError extends Error {
  constructor(
    message: string,
    public readonly code?: string,
  ) {
    super(message)
    this.name = 'EngineError'
  }
}

export function isStaleWrite(e: unknown): boolean {
  return e instanceof EngineError && e.code === 'stale'
}

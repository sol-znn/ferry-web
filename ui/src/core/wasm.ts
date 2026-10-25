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

// Generous relative to the shipped ~10 MiB module.
// Deadlines refuse a stalled load rather than leaving controls on a spinner.
export const LOAD_LIMITS = Object.freeze({
  wasmBytes: 32 * 1024 * 1024,
  fetchMs: 60_000,
  scriptMs: 20_000,
  compileMs: 60_000,
  readyMs: 20_000,
})

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
  const base = import.meta.env?.BASE_URL || './'
  return `${base}${name}?v=${__WASM_VERSION__}`
}

let started: Promise<void> | null = null

/** Load and start the module. Safe to call repeatedly; the first call wins. */
export function startWasm(): Promise<void> {
  started ??= boot()
  return started
}

async function boot(): Promise<void> {
  const lifetime = new AbortController()
  // Only the module started by this loader may declare readiness. An old or
  // partially installed API must not make a failed new load look successful.
  clearEngineAPI()
  try {
    // wasm_exec.js is Go's own runtime shim, copied verbatim out of the Go
    // distribution at build time rather than vendored by hand, so it always
    // matches the compiler that produced the module beside it.
    await loadScript(assetURL('wasm_exec.js'))
    if (!window.Go) throw new Error('the Go runtime shim did not define window.Go')

    emit({phase: 'fetching', progress: 0, loadedBytes: 0, totalBytes: 0})
    const bytes = await fetchBounded(
      assetURL('ferry.wasm'),
      LOAD_LIMITS.wasmBytes,
      LOAD_LIMITS.fetchMs,
      true,
    )

    emit({phase: 'compiling'})
    const go = new window.Go()
    // Compiled from the downloaded bytes rather than instantiateStreaming,
    // because streaming needs the exact Content-Type application/wasm and a
    // static host that gets it wrong would fail with no way to recover. Reading
    // the body ourselves also gives the progress the download needs.
    const {instance} = await deadline(
      WebAssembly.instantiate(bytes, go.importObject),
      LOAD_LIMITS.compileMs,
      'Compiling the signing engine timed out. Reload using a supported browser and trusted build.',
    )

    emit({phase: 'starting'})
    // The module's main() blocks forever after installing window.ferryWasm, so
    // this promise is not awaited: awaiting it would hang until the page closes.
    // Any return, including a clean exit, means the long-lived API is gone.
    const stopped = (error: Error) => {
      window.ferryWasm = undefined
      lifetime.abort(error)
      emit({phase: 'failed', error: error.message})
    }
    void go.run(instance).then(
      () => stopped(new Error('the signing engine exited; reload before using it')),
      (e: unknown) => stopped(new Error(`the signing engine stopped: ${String(e)}`)),
    )

    await waitForReady(lifetime.signal)
    if (lifetime.signal.aborted) throw lifetime.signal.reason

    const api = window.ferryWasm
    if (!api?.ready || typeof api.call !== 'function' || lifetime.signal.aborted) {
      // Go started but refused to run — the storage probe in main() failed, and
      // it says why. That is a real refusal, not a load error: a swap whose
      // refund key cannot be saved is worse than no swap.
      throw new Error(api?.error || 'the signing engine started but did not become ready')
    }
    emit({phase: 'ready', progress: 1})
  } catch (e) {
    lifetime.abort(e)
    window.ferryWasm = undefined
    emit({phase: 'failed', error: e instanceof Error ? e.message : String(e)})
    throw e
  }
}

function clearEngineAPI() {
  window.ferryWasm = undefined
  window.Go = undefined
}

function loadScript(src: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const el = document.createElement('script')
    el.src = src
    el.referrerPolicy = 'no-referrer'
    const cleanup = () => {
      clearTimeout(timer)
      el.onload = null
      el.onerror = null
      el.remove()
    }
    const timer = setTimeout(() => {
      cleanup()
      reject(
        new Error(
          'Loading the runtime shim timed out. Reload after checking this host or use a saved trusted build.',
        ),
      )
    }, LOAD_LIMITS.scriptMs)
    el.onload = () => {
      cleanup()
      resolve()
    }
    el.onerror = () => {
      cleanup()
      reject(new Error(`could not load ${src}`))
    }
    try {
      document.head.appendChild(el)
    } catch (e) {
      cleanup()
      reject(e)
    }
  })
}

/**
 * Fetch the module, reporting bytes as they arrive.
 *
 * Content-Length is missing whenever the host streams the response compressed,
 * which is the common case for a gzipped .wasm — so the bar falls back to a
 * byte counter rather than pretending to know a percentage it does not.
 */
async function fetchBounded(
  url: string,
  maxBytes: number,
  timeoutMs: number,
  progress = false,
): Promise<ArrayBuffer> {
  const controller = new AbortController()
  let reader: ReadableStreamDefaultReader<Uint8Array> | undefined
  const work = async () => {
    const res = await fetch(url, {signal: controller.signal})
    if (!res.ok) throw new Error(`could not fetch ${url}: ${res.status} ${res.statusText}`)
    const rawLength = Number(res.headers.get('content-length') ?? 0)
    const total = Number.isFinite(rawLength) && rawLength > 0 ? rawLength : 0
    if (total > maxBytes)
      throw new Error(`Refusing ${url}: download exceeds the ${maxBytes / 1048576} MiB limit.`)
    if (progress) emit({totalBytes: total})
    if (!res.body)
      throw new Error(`Cannot stream ${url}. Use a supported browser and a complete trusted build.`)
    reader = res.body.getReader()
    const chunks: Uint8Array[] = []
    let loaded = 0
    for (;;) {
      const {done, value} = await reader.read()
      if (done) break
      loaded += value.byteLength
      if (loaded > maxBytes)
        throw new Error(`Refusing ${url}: download exceeds the ${maxBytes / 1048576} MiB limit.`)
      chunks.push(value)
      if (progress) emit({loadedBytes: loaded, progress: total ? Math.min(loaded / total, 1) : 0})
    }
    const out = new Uint8Array(loaded)
    let at = 0
    for (const chunk of chunks) {
      out.set(chunk, at)
      at += chunk.length
    }
    return out.buffer
  }
  try {
    return await deadline(
      work(),
      timeoutMs,
      `Downloading ${url} timed out. Check connectivity or use a saved trusted build.`,
    )
  } finally {
    // Abort covers both response headers and body reads. Do not await cancel:
    // a stalled/custom stream must not keep the timeout rejection pending.
    controller.abort()
    void reader?.cancel().catch(() => {})
    try {
      reader?.releaseLock()
    } catch {
      /* a read may still be pending */
    }
  }
}

async function deadline<T>(work: Promise<T>, ms: number, message: string): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    return await Promise.race([
      work,
      new Promise<never>((_, reject) => {
        timer = setTimeout(() => reject(new Error(message)), ms)
      }),
    ])
  } finally {
    clearTimeout(timer)
  }
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
function waitForReady(signal: AbortSignal, timeoutMs = LOAD_LIMITS.readyMs): Promise<void> {
  const ready = () => window.ferryWasm?.ready && typeof window.ferryWasm.call === 'function'
  if (signal.aborted) return Promise.reject(signal.reason)
  if (ready()) return Promise.resolve()
  if (window.ferryWasm?.error) return Promise.reject(new Error(window.ferryWasm.error))
  return new Promise((resolve, reject) => {
    const cleanup = () => {
      window.removeEventListener('ferry-wasm-ready', check)
      signal.removeEventListener('abort', failed)
      clearInterval(poll)
      clearTimeout(timer)
    }
    const check = () => {
      if (ready()) {
        cleanup()
        resolve()
      } else if (window.ferryWasm?.error) {
        cleanup()
        reject(new Error(window.ferryWasm.error))
      }
    }
    const failed = () => {
      cleanup()
      reject(signal.reason)
    }
    const poll = setInterval(check, 50)
    const timer = setTimeout(() => {
      cleanup()
      reject(new Error('the signing engine did not start within 20 seconds'))
    }, timeoutMs)
    window.addEventListener('ferry-wasm-ready', check)
    signal.addEventListener('abort', failed, {once: true})
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
  if (state.phase !== 'ready') throw new Error(state.error || 'the signing engine is not ready')
  const call = window.ferryWasm?.call
  if (!call) throw new Error(window.ferryWasm?.error || 'the signing engine is not available')

  const raw = await call(method, JSON.stringify(body ?? {}))
  let data: unknown
  try {
    data = raw ? JSON.parse(raw) : {}
  } catch {
    throw new Error(`the signing engine returned something that is not JSON: ${raw.slice(0, 200)}`)
  }
  const err = (data as {error?: string}).error
  // A zenon verification failure is a successful call carrying an `error`
  // alongside its result, so only a bare error document is thrown.
  if (err && Object.keys(data as object).length === 1) throw new Error(err)
  return data as T
}

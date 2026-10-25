// Shipped WASM WebSocket transport with synthetic frames only; no live nodes.
// node scripts/ws-limits.mjs [path/to/ferry.wasm]
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const wasm = process.argv[2] ?? resolve(root, "ui/public/ferry.wasm");
const maxBytes = 8 << 20;
const storage = new Map();
globalThis.window = globalThis;
globalThis.localStorage = {
  get length() {
    return storage.size;
  },
  key: (index) => [...storage.keys()][index] ?? null,
  getItem: (key) => storage.get(key) ?? null,
  setItem: (key, value) => storage.set(key, String(value)),
  removeItem: (key) => storage.delete(key),
};
globalThis.fetch = async (url) => {
  assert.equal(url, "https://fixture.invalid/blocks/tip/height");
  return new Response("100");
};
let frame;
class Socket {
  static all = [];
  closed = false;
  constructor() {
    Socket.all.push(this);
    queueMicrotask(() => this.onopen?.({}));
  }
  send(payload) {
    const request = JSON.parse(payload);
    assert.equal(request.method, "ledger.getFrontierMomentum");
    queueMicrotask(() => this.onmessage?.({ data: frame(request.id) }));
  }
  close() {
    this.closed = true;
    this.onclose?.({});
  }
}
globalThis.WebSocket = Socket;
const goroot = execFileSync("go", ["env", "GOROOT"], {
  encoding: "utf8",
}).trim();
new Function(
  await readFile(resolve(goroot, "lib/wasm/wasm_exec.js"), "utf8"),
)();
const go = new globalThis.Go();
const { instance } = await WebAssembly.instantiate(
  await readFile(wasm),
  go.importObject,
);
void go.run(instance);
await new Promise((done) => setTimeout(done, 50));
assert.equal(globalThis.ferryWasm?.ready, true);
const config = async (name) => {
  const result = JSON.parse(
    await globalThis.ferryWasm.call(
      "config",
      JSON.stringify({
        settings: {
          network: "regtest",
          btcEsplora: "https://fixture.invalid",
          znnUrl: "wss://" + name + ".invalid",
        },
      }),
    ),
  );
  assert.equal(result.chainError, undefined);
  return result;
};
const response = (id, extra = "") =>
  JSON.stringify({
    jsonrpc: "2.0",
    id,
    result: { height: 42, timestamp: 1780000000, chainIdentifier: 69, extra },
  });
let checks = 0;
async function check(name, work) {
  await work();
  checks++;
  console.log("PASS " + name);
}

await check(
  "text JSON-RPC response at the UTF-8 byte limit is accepted",
  async () => {
    frame = (id) => {
      const body = response(id);
      return body + " ".repeat(maxBytes - body.length);
    };
    const result = await config("byte-boundary");
    assert.equal(result.znnHeight, 42);
    assert.equal(result.znnError, undefined);
    assert.equal(Socket.all.at(-1).closed, false);
  },
);

for (const kind of ["ascii", "multibyte", "binary"]) {
  await check(
    kind + " invalid frame fails the waiter and cleans up the connection",
    async () => {
      frame = (id) =>
        kind === "ascii"
          ? response(id) + " ".repeat(maxBytes)
          : kind === "multibyte"
            ? response(id, "€".repeat(Math.floor(maxBytes / 3) + 100))
            : new Uint8Array([1, 2, 3]);
      const started = performance.now();
      const result = await config(kind);
      assert.equal(result.znnHeight, undefined);
      assert.match(
        result.znnError,
        kind === "binary" ? /binary frame/ : /byte limit/,
      );
      assert.ok(
        performance.now() - started < 5000,
        "the waiter is released without its 20-second call timeout",
      );
      const socket = Socket.all.at(-1);
      assert.equal(socket.closed, true);
      for (const name of ["onopen", "onmessage", "onerror", "onclose"])
        assert.equal(socket[name], null);
    },
  );
}

await check(
  "a later request reconnects after a rejected frame and the engine stays usable",
  async () => {
    const previous = Socket.all.at(-1);
    frame = (id) => response(id);
    const result = await config("binary");
    assert.equal(result.znnHeight, 42);
    assert.notEqual(Socket.all.at(-1), previous);
    assert.equal(globalThis.ferryWasm.ready, true);
  },
);
console.log(checks + " WebSocket frame-bound checks passed.");
process.exit(0);

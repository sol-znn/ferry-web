// Checks this app's side of the `window.zenon` boundary, against a stub wallet.
//
//   node scripts/wallet-provider.mjs
//
// The only one of the three scripts that covers the extension boundary itself:
// wallet-devnet.mjs builds the block a wallet would be handed but never hands it
// over, and smoke.mjs never leaves the module.
//
// What lives in that gap is the translation layer — the reply shape, the error
// shape, the event names — and that is exactly where extension v0.2.0 broke this
// app. The reply to a published block changed from `{signedTransaction}` to
// `{hash, block}`, which is invisible until a block has actually been signed.
// The cost of learning it live is a locked lot of ZNN and a card that then
// offers to lock a second.
//
// Needs no chain, no browser and no extension: the provider is stubbed, which is
// the point. Node strips the TypeScript itself (24+), and
// ui/src/core/zenon-wallet.ts imports nothing, so it is loaded as it ships.
import { dirname, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));

let fails = 0;
const ok = (label, cond, detail = "") => {
  if (cond) console.log(`  ok   ${label}`);
  else {
    fails++;
    console.log(`  FAIL ${label}${detail ? `\n       ${detail}` : ""}`);
  }
};
const section = (name) => console.log(`\n${name}`);

// ---------- the browser globals the module expects ----------

// Only the four the module actually touches. Timers are the real ones: every
// deadline in the module is minutes long and nothing here reaches one.
const listeners = new Map();
globalThis.window = {
  setTimeout: (...a) => setTimeout(...a),
  clearTimeout: (...a) => clearTimeout(...a),
  setInterval: (...a) => setInterval(...a),
  clearInterval: (...a) => clearInterval(...a),
  addEventListener: (name, fn) => {
    if (!listeners.has(name)) listeners.set(name, new Set());
    listeners.get(name).add(fn);
  },
  removeEventListener: (name, fn) => listeners.get(name)?.delete(fn),
  zenon: undefined,
};

/**
 * A stub of the extension's provider.
 *
 * `calls` records what was asked of it, which is how the prompt claims are
 * checked: the whole point of `readAccess` is that it reaches only the three
 * read methods and never `connect`, and the only way to assert that is to
 * watch what it called.
 */
function stubProvider(over = {}) {
  const calls = [];
  // Presence, not truthiness. `over.nodeUrl ?? default` turns a deliberate null
  // — the wallet reporting no node, which is a case worth checking — straight
  // back into the default, and the check then proves nothing.
  const has = (k) => Object.prototype.hasOwnProperty.call(over, k);
  const p = {
    isSyriusExtension: true,
    isZenon: true,
    version: 2,
    accounts: [],
    chainId: null,
    _calls: calls,
    _handlers: new Map(),
    async connect() {
      calls.push("connect");
      return has("accounts")
        ? over.accounts
        : ["z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d"];
    },
    async disconnect() {
      calls.push("disconnect");
      return true;
    },
    async getAccounts() {
      calls.push("getAccounts");
      return has("connectedAccounts") ? over.connectedAccounts : [];
    },
    async getChainId() {
      calls.push("getChainId");
      return has("chainId") ? over.chainId : 69;
    },
    async getNodeUrl() {
      calls.push("getNodeUrl");
      return has("nodeUrl") ? over.nodeUrl : "http://127.0.0.1:35997";
    },
    async sendAccountBlock(block) {
      calls.push("sendAccountBlock");
      p._lastBlock = block;
      if (over.sendThrows) throw over.sendThrows;
      return (
        over.sendResult ?? {
          hash: "a".repeat(64),
          block: { ...block, height: 42 },
        }
      );
    },
    on(event, handler) {
      if (!p._handlers.has(event)) p._handlers.set(event, new Set());
      p._handlers.get(event).add(handler);
      return p;
    },
    removeListener(event, handler) {
      p._handlers.get(event)?.delete(handler);
      return p;
    },
    _emit(event, data) {
      [...(p._handlers.get(event) ?? [])].forEach((h) => h(data));
    },
    ...(over.methods ?? {}),
  };
  globalThis.window.zenon = p;
  return p;
}

// pathToFileURL, not the bare path: on Windows an absolute path starts with a
// drive letter, which the ESM loader reads as an unsupported URL scheme.
const wallet = await import(
  pathToFileURL(resolve(here, "../ui/src/core/zenon-wallet.ts")).href
);

// A complete block, the shape the module documents and the extension parses.
const BLOCK = {
  version: 1,
  chainIdentifier: 69,
  blockType: 2,
  hash: "0".repeat(64),
  previousHash: "0".repeat(64),
  height: 0,
  momentumAcknowledged: { hash: "0".repeat(64), height: 0 },
  address: "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d",
  toAddress: "z1qxemdeddedxhtlcxxxxxxxxxxxxxxxxxygecvw",
  amount: "1000000000",
  tokenStandard: "zts1znnxxxxxxxxxxxxx9z4ulx",
  fromBlockHash: "0".repeat(64),
  data: "XH5xEA==",
  fusedPlasma: 0,
  difficulty: 0,
  nonce: "0000000000000000",
  publicKey: "",
  signature: "",
};

//
section("the provider is recognised by what it can do, not what it claims");

globalThis.window.zenon = undefined;
ok("no provider is not a wallet", wallet.zenonExtension() === false);

globalThis.window.zenon = { isSyriusExtension: true };
ok(
  "the old flag alone is not a wallet",
  wallet.zenonExtension() === false,
  "a v0.1.x flag with no sendAccountBlock must not be mistaken for a provider",
);

stubProvider();
ok("a provider with the methods is a wallet", wallet.zenonExtension() === true);

//
section("a published block is read back");

{
  stubProvider();
  const r = await wallet.sendAccountBlock(BLOCK);
  ok(
    "the hash is read from the v0.2.0 reply",
    r.hash === "a".repeat(64),
    JSON.stringify(r),
  );
  ok("the signed block comes back for the diff", r.signed?.height === 42);
}

{
  // The regression this file exists for. v0.1.x answered with
  // `{signedTransaction}`; reading that shape and finding no hash is what threw
  // AFTER the ZNN was locked.
  stubProvider({ sendResult: { signedTransaction: { hash: "b".repeat(64) } } });
  let threw = "";
  try {
    await wallet.sendAccountBlock(BLOCK);
  } catch (e) {
    threw = e.message;
  }
  ok(
    "the v0.1.x reply shape is refused loudly, not silently",
    threw.includes("reported no transaction hash"),
    threw || "it resolved",
  );
}

{
  stubProvider({ sendResult: { hash: "c".repeat(64) } });
  const r = await wallet.sendAccountBlock(BLOCK);
  ok(
    "a reply with a hash and no block is still a success",
    r.hash === "c".repeat(64),
  );
  ok(
    "and reports nothing to diff rather than an empty diff",
    r.signed === undefined,
  );
}

//
section("a Vue ref cannot cross postMessage, so it is unwrapped first");

{
  const p = stubProvider();
  // Structured clone refuses a Proxy outright, naming no field. The module's
  // JSON round trip is what stops that reaching the wallet as a missing `data`.
  const reactive = new Proxy({ ...BLOCK }, { get: (t, k) => t[k] });
  await wallet.sendAccountBlock(reactive);
  ok(
    "the provider is handed a plain object",
    p._lastBlock.constructor === Object,
  );
  ok("with the contract call data intact", p._lastBlock.data === BLOCK.data);
  ok("and the payee intact", p._lastBlock.toAddress === BLOCK.toAddress);
}

//
section("the provider rejects with {code, message}, not an Error");

{
  stubProvider({
    sendThrows: { code: 4001, message: "User rejected the request" },
  });
  let err = null;
  try {
    await wallet.sendAccountBlock(BLOCK);
  } catch (e) {
    err = e;
  }
  ok("a declined send is an Error", err instanceof Error);
  ok("said in the first person", err.message.includes("declined"), err.message);
  ok(
    "and says nothing was sent",
    err.message.includes("Nothing was sent"),
    err.message,
  );
}

{
  stubProvider({
    sendThrows: { code: 4100, message: "The site is not connected" },
  });
  let err = null;
  try {
    await wallet.sendAccountBlock(BLOCK);
  } catch (e) {
    err = e;
  }
  ok(
    "an unconnected send names the fix",
    err.message.includes("Press Connect"),
    err.message,
  );
}

{
  // The shape that used to render as "[object Object]".
  stubProvider({ sendThrows: { code: -32603, message: "not enough plasma" } });
  let err = null;
  try {
    await wallet.sendAccountBlock(BLOCK);
  } catch (e) {
    err = e;
  }
  ok(
    "an unrecognised code keeps the wallet's own words",
    err.message === "not enough plasma",
  );
  ok("as an Error, not an object", err instanceof Error);
}

//
section("reading state never prompts");

{
  const p = stubProvider({ connectedAccounts: [] });
  const access = await wallet.readAccess();
  ok("an unconnected page reads as no connection", access === null);
  ok(
    "and connect was never called",
    !p._calls.includes("connect"),
    p._calls.join(","),
  );
}

{
  const p = stubProvider({
    connectedAccounts: ["z1qqvwzz2xq7q5gwk6uhcddgrpxlfcyzc8rsu82s"],
  });
  const access = await wallet.readAccess();
  ok(
    "a connected page restores the address",
    access?.address === "z1qqvwzz2xq7q5gwk6uhcddgrpxlfcyzc8rsu82s",
  );
  ok("with the chain", access?.chainId === 69);
  ok("and the node", access?.nodeUrl === "http://127.0.0.1:35997");
  ok(
    "still without a prompt",
    !p._calls.includes("connect"),
    p._calls.join(","),
  );
}

{
  // A locked wallet reports the same empty list as an unconnected origin, and
  // neither is an error worth putting in front of somebody who asked for
  // nothing.
  stubProvider({
    methods: {
      async getAccounts() {
        throw { code: 4900, message: "The wallet is locked" };
      },
    },
  });
  ok(
    "a read that could not run is absence, not a throw",
    (await wallet.readAccess()) === null,
  );
}

//
section("connecting");

{
  const p = stubProvider();
  const access = await wallet.requestAccess();
  ok("connect is what asks", p._calls.includes("connect"));
  ok(
    "and the triple comes back together",
    Boolean(access.address && access.chainId && access.nodeUrl),
  );
}

{
  stubProvider({ accounts: [] });
  let threw = "";
  try {
    await wallet.requestAccess();
  } catch (e) {
    threw = e.message;
  }
  ok(
    "a grant with no address is refused",
    threw.includes("reported no address"),
    threw,
  );
}

{
  stubProvider({
    methods: {
      async connect() {
        throw { code: 4001, message: "no" };
      },
    },
  });
  let threw = "";
  try {
    await wallet.requestAccess();
  } catch (e) {
    threw = e.message;
  }
  ok("a declined connect says so", threw.includes("declined"), threw);
}

{
  // Some builds store it as a string. Comparing "69" against 69 without
  // normalising is how a swap is refused for no reason.
  stubProvider({
    connectedAccounts: ["z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d"],
    chainId: "69",
  });
  ok(
    "a chain id given as a string is a number",
    (await wallet.readAccess())?.chainId === 69,
  );
}

{
  stubProvider({
    connectedAccounts: ["z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d"],
    nodeUrl: null,
  });
  const access = await wallet.readAccess();
  ok(
    "a node the wallet did not report is empty, not null",
    access?.nodeUrl === "",
  );
}

//
section("no provider is a sentence, not a TypeError");

{
  globalThis.window.zenon = undefined;
  let threw = "";
  try {
    await wallet.sendAccountBlock(BLOCK);
  } catch (e) {
    threw = e.message;
  }
  ok("a send with no wallet names the fix", threw.includes("reload"), threw);
  ok("reading with no wallet is null", (await wallet.readAccess()) === null);
  ok(
    "and unsubscribing from nothing is safe",
    typeof wallet.onWalletChange(() => {}) === "function",
  );
}

//
section("changes the wallet announces");

{
  const p = stubProvider();
  const seen = [];
  const stop = wallet.onWalletChange((c) => seen.push(c));

  p._emit("accountsChanged", ["z1qqvwzz2xq7q5gwk6uhcddgrpxlfcyzc8rsu82s"]);
  ok(
    "an account change carries the new account",
    seen.at(-1)?.address === "z1qqvwzz2xq7q5gwk6uhcddgrpxlfcyzc8rsu82s",
  );

  p._emit("chainChanged", "1");
  ok("a chain change is a number", seen.at(-1)?.chainId === 1);

  p._emit("nodeChanged", "http://10.0.0.2:35997");
  ok(
    "a node change carries the URL",
    seen.at(-1)?.nodeUrl === "http://10.0.0.2:35997",
  );

  // An empty account list means locked or revoked, which is a disconnection
  // however it is spelled.
  p._emit("accountsChanged", []);
  ok(
    "an emptied account list is a disconnection",
    seen.at(-1)?.disconnected === true,
  );

  p._emit("disconnect", { origin: "http://127.0.0.1:5178" });
  ok("and so is an explicit disconnect", seen.at(-1)?.disconnected === true);

  const before = seen.length;
  stop();
  p._emit("chainChanged", 5);
  ok("unsubscribing stops delivery", seen.length === before);
}

//
section("forgetting revokes something real");

{
  const p = stubProvider();
  await wallet.revokeAccess();
  ok("disconnect is called on the wallet", p._calls.includes("disconnect"));
}

{
  stubProvider({
    methods: {
      async disconnect() {
        throw { code: -32603, message: "boom" };
      },
    },
  });
  let threw = false;
  try {
    await wallet.revokeAccess();
  } catch {
    threw = true;
  }
  ok(
    "a wallet that refuses to forget is not an error in the user's face",
    !threw,
  );
}

console.log(fails === 0 ? "\nall checks passed" : `\n${fails} FAILED`);
process.exit(fails === 0 ? 0 : 1);

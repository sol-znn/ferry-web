// Checks the block this app hands a Zenon wallet, against a live devnet.
//
//   node scripts/wallet-devnet.mjs [path/to/ferry.wasm]
//
// The companion to smoke.mjs and its opposite in one respect: smoke.mjs is
// deliberately offline so it can run in CI, and therefore cannot touch the three
// things on this path that only a chain can answer — the chain identifier a
// block commits to, the token decimals an agreed "10 ZNN" is converted with, and
// the momentum clock the expiry is measured against. Each is the kind of thing
// that is wrong by a factor of a hundred million rather than slightly wrong.
//
// It stops one step short of the wallet: everything the extension would be
// handed is built and taken apart here, but pressing the button in the
// extension's own window is the manual half of docs/TESTING.md.
//
// Needs a go-zenon devnet on :35997 and the Esplora shim on :3002 — see the
// appendix in docs/TESTING.md. It writes nothing to either chain.
import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { execFileSync } from "node:child_process";
import { Buffer } from "node:buffer";

const here = dirname(fileURLToPath(import.meta.url));
const wasmPath =
  process.argv.slice(2).find((a) => !a.startsWith("--")) ??
  resolve(here, "../ui/public/ferry.wasm");

class MemStorage {
  #m = new Map();
  get length() {
    return this.#m.size;
  }
  key(i) {
    return [...this.#m.keys()][i] ?? null;
  }
  getItem(k) {
    return this.#m.has(k) ? this.#m.get(k) : null;
  }
  setItem(k, v) {
    this.#m.set(String(k), String(v));
  }
  removeItem(k) {
    this.#m.delete(k);
  }
}
globalThis.localStorage = new MemStorage();
globalThis.window = globalThis;

const goroot = execFileSync("go", ["env", "GOROOT"], {
  encoding: "utf8",
}).trim();
const shim = await readFile(
  resolve(goroot, "lib/wasm/wasm_exec.js"),
  "utf8",
).catch(() => readFile(resolve(goroot, "misc/wasm/wasm_exec.js"), "utf8"));
new Function(shim)();
const go = new globalThis.Go();
const { instance } = await WebAssembly.instantiate(
  await readFile(wasmPath),
  go.importObject,
);
// Not awaited — the module's main() blocks forever by design.
void go.run(instance);
await new Promise((r) => setTimeout(r, 100));

if (!globalThis.ferryWasm?.ready) {
  throw new Error(
    `module did not become ready: ${globalThis.ferryWasm?.error ?? "no API installed"}`,
  );
}

const call = async (m, b = {}) =>
  JSON.parse(await globalThis.ferryWasm.call(m, JSON.stringify(b)));

let fails = 0;
const ok = (label, cond, detail = "") => {
  if (cond) console.log(`  ok   ${label}`);
  else {
    fails++;
    console.log(`  FAIL ${label}${detail ? `\n       ${detail}` : ""}`);
  }
};

const S = {
  network: "regtest",
  btcEsplora: "http://127.0.0.1:3002",
  znnUrl: "http://127.0.0.1:35997",
};
// The devnet's pre-fused account, standing in for the extension's wallet. It is
// only ever named here, never signed with: this script stops before the wallet.
const WALLET = "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d";
// Any other real address. It has to be a real one — the bech32 checksum is
// decoded, which is the point, and an invented address is caught rather than
// encoded into an HTLC that pays nobody.
const PEER = "z1qqvwzz2xq7q5gwk6uhcddgrpxlfcyzc8rsu82s";
const BTC_A = "bcrt1q0rymrte6drs2nvjn73mqsl2meud7nv0dy6tgn4";
const BTC_B = "bcrt1qw508d6qejxtdg4y5r3zarvary0c5xw7kygt080";

console.log("\nsetting up a swap whose Zenon leg is ours");

// A: the initiator, sending BTC.
const a = await call("create", {
  role: "initiator",
  leg: "send",
  amountSats: 400000,
  destAddr: BTC_A,
  zenonSelfAddress: PEER,
  zenonPeerAddress: WALLET,
  zenonAmount: "10",
  settings: S,
});
ok("the initiator’s swap is created", !a.error, a.error);

// B: the participant, receiving BTC and therefore sending ZNN. This is the
// side whose Zenon HTLC is its own, so it is the side with a create to make.
const b = await call("create", {
  role: "participant",
  leg: "receive",
  amountSats: 400000,
  destAddr: BTC_B,
  secretHashHex: a.secretHashHex,
  zenonSelfAddress: WALLET,
  zenonPeerAddress: PEER,
  zenonAmount: "10",
  settings: S,
});
ok("the participant’s swap is created", !b.error, b.error);

const funded = await call("counterparty", { id: a.id, pkhHex: b.key.pkhHex });
ok(
  "the initiator builds a contract once it has their pubkey hash",
  Boolean(funded.contractHex),
);

const audited = await call("audit", {
  id: b.id,
  contractHex: funded.contractHex,
});
ok(
  "the participant audits it",
  !audited.error && Boolean(audited.contractAddr),
  audited.error,
);
ok("and it is our Zenon leg to create", audited.zenonHtlcIsOurs === true);
ok(
  "with an expiry computed from the Bitcoin locktime",
  audited.zenon?.expirationSeconds > 0,
  JSON.stringify(audited.zenon),
);
// B's leg ANSWERS A's Bitcoin funding, and there is none: the contract is a
// script nobody has paid. Locking ZNN against it is refused, and the card is
// told why in the same words.
ok(
  "but not until their Bitcoin is funded and confirmed",
  audited.fundingCommitted === false &&
    (audited.fundingCommitBlocker ?? "").includes("not been seen"),
  JSON.stringify([audited.fundingCommitted, audited.fundingCommitBlocker]),
);

console.log("\nsetting up the swap whose Zenon leg goes first");

// The other ordering: D initiates from the Zenon side, so D's HTLC is the
// LONG leg and is created before any Bitcoin exists. This is the shape whose
// create can be built with nothing on the Bitcoin chain, which is what makes
// it the one this script can take apart.
//
// D: the initiator, receiving BTC and therefore sending ZNN.
const d = await call("create", {
  role: "initiator",
  leg: "receive",
  amountSats: 400000,
  destAddr: BTC_B,
  zenonSelfAddress: WALLET,
  zenonPeerAddress: PEER,
  zenonAmount: "10",
  settings: S,
});
ok("the Zenon-side initiator’s swap is created", !d.error, d.error);

// C: the participant, sending BTC against D's hash.
const c = await call("create", {
  role: "participant",
  leg: "send",
  amountSats: 400000,
  destAddr: BTC_A,
  secretHashHex: d.secretHashHex,
  zenonSelfAddress: PEER,
  zenonPeerAddress: WALLET,
  zenonAmount: "10",
  settings: S,
});
ok("the Bitcoin-side participant’s swap is created", !c.error, c.error);

const builtC = await call("counterparty", { id: c.id, pkhHex: d.key.pkhHex });
ok("the participant builds the contract", Boolean(builtC.contractHex));

const auditedD = await call("audit", {
  id: d.id,
  contractHex: builtC.contractHex,
});
ok(
  "the initiator audits it",
  !auditedD.error && Boolean(auditedD.contractAddr),
  auditedD.error,
);
ok("and it is our Zenon leg to create", auditedD.zenonHtlcIsOurs === true);
ok(
  "with nothing on Bitcoin to wait for",
  auditedD.fundingCommitted === true && !auditedD.fundingCommitBlocker,
  JSON.stringify([auditedD.fundingCommitted, auditedD.fundingCommitBlocker]),
);

console.log("\nthe gate, against the live devnet");

const wallet = { address: WALLET, chainId: 69, nodeUrl: S.znnUrl };
const sync = await call("walletSync", {
  ...wallet,
  id: d.id,
  action: "create",
  settings: S,
});
ok(
  "the wallet and this page are proven to be on one chain",
  sync.ok && sync.sameChain,
  JSON.stringify([sync.problems, sync.unchecked]),
);

const wrongAccount = await call("walletSync", {
  ...wallet,
  address: PEER,
  id: d.id,
  action: "create",
  settings: S,
});
ok(
  "a wallet on the wrong account is refused, by name",
  !wrongAccount.ok &&
    (wrongAccount.problems ?? []).some((p) => p.includes("wrong account")),
  JSON.stringify(wrongAccount.problems),
);

console.log("\nthe block the extension would be handed");

const plan = await call("walletBlock", {
  id: d.id,
  action: "create",
  from: wallet.address,
  chainId: wallet.chainId,
  nodeUrl: wallet.nodeUrl,
  settings: S,
});
ok("a create block is built", !plan.error, plan.error);

if (!plan.error) {
  const data = Buffer.from(plan.block.data, "base64");
  const hex = data.toString("hex");
  ok(
    "it is addressed to the HTLC contract",
    plan.block.toAddress === "z1qxemdeddedxhtlcxxxxxxxxxxxxxxxxxygecvw",
    plan.block.toAddress,
  );
  ok(
    "it carries the chain identifier this browser’s node reports",
    plan.block.chainIdentifier === 69,
    String(plan.block.chainIdentifier),
  );
  ok("it is a user send", plan.block.blockType === 2);
  ok("the method is htlc.Create", hex.startsWith("5c7e7110"), hex.slice(0, 8));
  ok(
    "10 ZNN in base units",
    plan.block.amount === "1000000000",
    plan.block.amount,
  );
  ok(
    "denominated in ZNN",
    plan.block.tokenStandard === "zts1znnxxxxxxxxxxxxx9z4ulx",
  );

  // The five arguments, read straight back out of the encoding.
  const word = (i) => hex.slice(8 + i * 64, 8 + (i + 1) * 64);
  // The account this HTLC pays. It must be the counterparty and must NOT be
  // the signer: an HTLC that pays the address that created it is not a swap
  // leg, it is a way of locking your own money up for a day.
  const SIGNER_CORE = "0035941e758a1f155fb30c1217a8186ed9e12651"; // WALLET, decoded
  ok(
    "hashLocked is decoded back to the counterparty",
    plan.hashLocked === PEER,
    plan.hashLocked,
  );
  ok(
    "and it is encoded left-padded into one word",
    word(0).startsWith("000000000000000000000000"),
    word(0),
  );
  ok(
    "and it is not the account that will sign",
    !word(0).endsWith(SIGNER_CORE),
    word(0),
  );
  ok(
    "hashType is 1 (SHA-256, what Bitcoin’s OP_SHA256 needs)",
    parseInt(word(2), 16) === 1,
    word(2),
  );
  ok("keyMaxSize is 32", parseInt(word(3), 16) === 32, word(3));
  ok(
    "the hashlock is this swap’s secret hash",
    hex.endsWith(auditedD.secretHashHex),
    hex.slice(-64),
  );

  const expiry = parseInt(word(1), 16);
  ok(
    "the expiry matches what the plan says",
    expiry === plan.expirationTime,
    String(expiry),
  );
  // D is the initiator, so this is the LONG leg: it must expire AFTER the
  // Bitcoin contract — by at least the minimum gap.
  ok(
    "the initiator’s Zenon leg expires after the Bitcoin locktime",
    expiry > auditedD.lockTime,
    `zenon ${expiry} vs btc ${auditedD.lockTime}`,
  );
  ok(
    "with at least two hours to spare",
    expiry - auditedD.lockTime >= 2 * 3600,
    `gap ${(expiry - auditedD.lockTime) / 3600}h`,
  );

  console.log(`\n  summary: ${plan.summary}`);
  console.log(`  signer:  ${plan.signer}`);
  console.log(
    `  sync:    sameChain=${plan.sync?.sameChain} anchor=${plan.sync?.anchorHeight}`,
  );
}

console.log("\nrefusals");

for (const [label, body, want] of [
  [
    "unlocking a leg that is ours to create",
    { action: "unlock" },
    "nothing for you to unlock",
  ],
  [
    "reclaiming before there is an HTLC",
    { action: "reclaim" },
    "no Zenon HTLC id",
  ],
  ["an unknown action", { action: "burn" }, "unknown wallet action"],
  [
    "a wallet on a chain of its own",
    { action: "create", chainId: 1 },
    "configured for chain 1",
  ],
]) {
  const r = await call("walletBlock", {
    id: d.id,
    from: wallet.address,
    chainId: wallet.chainId,
    nodeUrl: wallet.nodeUrl,
    settings: S,
    ...body,
  });
  ok(
    `${label} is refused`,
    Boolean(r.error) && r.error.includes(want),
    r.error ?? "no error",
  );
}

// And the refusal that is this script's reason for having two swaps: B's
// create answers A's Bitcoin, which nobody has paid.
{
  const r = await call("walletBlock", {
    id: b.id,
    action: "create",
    from: wallet.address,
    chainId: wallet.chainId,
    nodeUrl: wallet.nodeUrl,
    settings: S,
  });
  ok(
    "locking ZNN against Bitcoin that is not funded is refused",
    Boolean(r.error) && r.error.includes("not locking ZNN yet"),
    r.error ?? "no error",
  );
}

console.log(fails === 0 ? "\nall checks passed" : `\n${fails} FAILED`);
process.exit(fails === 0 ? 0 : 1);

// Checks the znn-cli commands the card prints, against the one rule the engine
// cannot enforce on them: a create that answers the counterparty's Bitcoin
// funding is printed as a command only when the caller says a live, fail-closed
// check of that funding passed just now -- never off the swap's own record.
//
//   node scripts/zenon-commands.mjs
//
// The wallet button is behind Go's gate (walletBlock refuses to build the
// block, and the panel re-checks before handing it over). A command in a
// terminal is not, so the text itself has to carry the refusal -- and this is
// what pins that it does. Like wallet-provider.mjs it loads the TypeScript as
// it ships (Node 24+ strips the types), with no chain and no browser.
import { dirname, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const { znnCommands } = await import(
  pathToFileURL(resolve(here, "../ui/src/core/zenon-commands.ts")).href
);

let fails = 0;
const ok = (label, cond, detail = "") => {
  if (cond) console.log(`  ok   ${label}`);
  else {
    fails++;
    console.log(`  FAIL ${label}${detail ? `\n       ${detail}` : ""}`);
  }
};
const section = (name) => console.log(`\n${name}`);

// The participant receiving BTC in a Bitcoin-initiated swap: the shape whose
// create answers the counterparty's funding.
const base = {
  zenonHtlcIsOurs: true,
  btcLegIsInitiators: true,
  secretArrivesOnZenon: true,
  secretHashHex: "ab".repeat(32),
  zenon: {
    tokenStandard: "zts1znnxxxxxxxxxxxxx9z4ulx",
    amountDisplay: "10",
    expirationHours: 22,
    peerAddress: "z1qqvwzz2xq7q5gwk6uhcddgrpxlfcyzc8rsu82s",
    selfAddress: "z1qq6eg8n43g032hanpsfp02qcdmv7zfj3y2lt5d",
  },
};
const commands = (sw, ctx) =>
  znnCommands(sw, ctx)
    .split("\n")
    .filter((l) => l && !l.startsWith("#"));
const hasCreate = (sw, ctx) =>
  commands(sw, ctx).some((l) => l.startsWith("znn-cli htlc.create "));

section(
  "a create that answers Bitcoin funding is withheld unless a live check just passed",
);

// The reproduction from the review: the swap record says committed, and that
// record can be stale -- an expired leg, a refresh that could not read the
// chain. The generator must not trust it. No context means not checked.
const cachedCommitted = {
  ...base,
  fundingCommitted: true,
  fundingCommitBlocker: "",
};
ok(
  'a cached "committed" record alone prints no htlc.create',
  !hasCreate(cachedCommitted),
  commands(cachedCommitted).join(" | "),
);
ok(
  "and says the funding was not checked just now",
  znnCommands(cachedCommitted).includes(
    "not been checked against the chain just now",
  ),
);

for (const [name, blocker] of [
  [
    "nothing seen",
    "their Bitcoin funding has not been seen at the contract yet",
  ],
  [
    "in the mempool",
    "their funding is still in the mempool, where the sender can replace it",
  ],
  ["short", "the contract holds 100000 sat but 400000 sat was agreed"],
  ["spent", "the contract output has already been spent"],
  [
    "expired",
    "the Bitcoin contract's timelock has passed, so their funding is theirs to take back",
  ],
  ["unreadable", "could not re-read the contract address before locking ZNN"],
]) {
  const sw = {
    ...base,
    fundingCommitted: false,
    fundingCommitBlocker: blocker,
  };
  const ctx = { createAllowed: false, createBlocker: blocker };
  const text = znnCommands(sw, ctx);
  ok(
    `${name}: no htlc.create command`,
    !hasCreate(sw, ctx),
    commands(sw, ctx).join(" | "),
  );
  ok(`${name}: the reason is in the text`, text.includes(blocker));
  ok(
    `${name}: the reclaim is still there for a leg that exists later`,
    text.includes("htlc.reclaim"),
  );
}

section("and is printed once a live check has passed");
const allowed = { createAllowed: true };
ok(
  "checked and allowed: the command is printed",
  hasCreate(cachedCommitted, allowed),
);
ok(
  "and it carries the hashlock and hash type 1",
  commands(cachedCommitted, allowed).some((l) =>
    l.includes(` 1 ${"ab".repeat(32)}`),
  ),
);
ok(
  "a record that says committed does not override a live refusal",
  !hasCreate(cachedCommitted, {
    createAllowed: false,
    createBlocker: "the funding output is no longer unspent",
  }),
);

section("the shapes that never wait are unaffected");
ok(
  "the Zenon-initiated leg prints its create with nothing on Bitcoin, and no check",
  hasCreate({
    ...base,
    btcLegIsInitiators: false,
    secretArrivesOnZenon: false,
    fundingCommitted: true,
  }),
);
ok(
  "the side that unlocks the counterparty's HTLC still gets its unlock",
  commands({
    ...base,
    zenonHtlcIsOurs: false,
    fundingCommitted: true,
    zenon: { ...base.zenon, htlcId: "deadbeef" },
    secretHex: "cd".repeat(32),
  }).some((l) => l.startsWith("znn-cli htlc.unlock ")),
);

console.log(fails === 0 ? "\nall checks passed" : `\n${fails} FAILED`);
process.exit(fails === 0 ? 0 : 1);

// Checks the znn-cli commands the card prints, against the one rule the engine
// cannot enforce on them: a create that answers the counterparty's Bitcoin
// funding is never printed as a command -- the terms are, as comments -- because
// a terminal runs no check and a text gate on a remembered answer always has a
// stale moment.
//
//   node --experimental-strip-types scripts/zenon-commands.mjs
//
// The wallet button is behind Go's gate (walletBlock refuses to build the
// block, and the panel re-checks before handing it over). A command in a
// terminal is not, so the text withholds it -- and this is what pins that it
// does. Like wallet-provider.mjs it loads the TypeScript as it ships, with no
// chain and no browser.
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
  amountSats: 400000,
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
    .split(/\r\n|\r|\n/)
    .filter((l) => l && !l.startsWith("#"));
const hasCreate = (sw, ctx) =>
  commands(sw, ctx).some((l) => l.startsWith("znn-cli htlc.create "));

section("a create that answers Bitcoin funding is never printed as a command");

// Whatever the record says -- committed, blocked, or nothing -- and whatever a
// caller passes: this shape's create is withheld. The review reproduced three
// stale moments in three text gates; this is the fourth answer.
for (const [name, sw, ctx] of [
  ["a record that says committed", { ...base, fundingCommitted: true }, {}],
  [
    "nothing seen",
    {
      ...base,
      fundingCommitted: false,
      fundingCommitBlocker:
        "their Bitcoin funding has not been seen at the contract yet",
    },
    {},
  ],
  [
    "in the mempool",
    {
      ...base,
      fundingCommitted: false,
      fundingCommitBlocker:
        "their funding is still in the mempool, where the sender can replace it",
    },
    {},
  ],
  [
    "expired",
    {
      ...base,
      fundingCommitted: false,
      fundingCommitBlocker:
        "the Bitcoin contract's timelock has passed, so their funding is theirs to take back",
    },
    {},
  ],
  [
    "a caller that claims it checked",
    { ...base, fundingCommitted: true },
    { createAllowed: true },
  ],
]) {
  const text = znnCommands(sw, ctx);
  ok(
    `${name}: no htlc.create command`,
    !hasCreate(sw, ctx),
    commands(sw, ctx).join(" | "),
  );
  ok(
    `${name}: the reclaim is still there for a leg that exists later`,
    text.includes("htlc.reclaim"),
  );
  ok(
    `${name}: it points at the wallet button`,
    text.includes("Use the wallet button"),
  );
}
{
  const sw = {
    ...base,
    fundingCommitted: false,
    fundingCommitBlocker:
      "the contract holds 100000 sat but 400000 sat was agreed",
  };
  const text = znnCommands(sw);
  ok(
    "the terms are printed, as comments, for somebody composing it by hand",
    text.includes(`#   hashlock   ${"ab".repeat(32)}`) &&
      text.includes(
        "#   recipient  z1qqvwzz2xq7q5gwk6uhcddgrpxlfcyzc8rsu82s",
      ) &&
      text.includes("#   hashtype   1") &&
      text.includes("#   hours      22"),
  );
  ok(
    "and the last refresh's verdict is shown",
    text.includes("As of the last refresh: the contract holds 100000 sat"),
  );
  ok(
    "with nothing executable but the reclaim",
    commands(sw).length === 1 &&
      commands(sw)[0].startsWith("znn-cli htlc.reclaim "),
    commands(sw).join(" | "),
  );
}

section("the shapes that never wait are unaffected");
ok(
  "the Zenon-initiated leg prints its create with nothing on Bitcoin",
  hasCreate({
    ...base,
    btcLegIsInitiators: false,
    secretArrivesOnZenon: false,
    fundingCommitted: true,
  }),
);
ok(
  "and it carries the hashlock and hash type 1",
  commands({
    ...base,
    btcLegIsInitiators: false,
    secretArrivesOnZenon: false,
    fundingCommitted: true,
  }).some((l) => l.includes(` 1 ${"ab".repeat(32)}`)),
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

section("text from outside cannot become a command when the block is pasted");
// Every term that reaches the block from outside this page -- the offer, the
// counterparty, the chain, an error body -- with every line separator a shell
// would honour. The only executable line left standing must be the reclaim.
const seps = [
  ["a newline", "\n"],
  ["a carriage return", "\r"],
  ["CRLF", "\r\n"],
];
const poisoned = (field, sep) => {
  const evil = `backend failed${sep}printf PWNED`;
  const sw = { ...base, fundingCommitted: false, zenon: { ...base.zenon } };
  if (field === "fundingCommitBlocker") sw.fundingCommitBlocker = evil;
  else if (field === "secretHashHex") sw.secretHashHex = evil;
  else sw.zenon[field] = evil;
  return sw;
};
for (const field of [
  "amountDisplay",
  "peerAddress",
  "tokenStandard",
  "secretHashHex",
  "fundingCommitBlocker",
]) {
  for (const [name, sep] of seps) {
    const sw = poisoned(field, sep);
    const text = znnCommands(sw);
    const bare = commands(sw);
    ok(
      `${field} with ${name}: no uncommented line but the reclaim`,
      bare.length === 1 && bare[0].startsWith("znn-cli htlc.reclaim "),
      JSON.stringify(bare),
    );
    ok(
      `${field} with ${name}: the injected text is visible, as a comment`,
      text.includes("# printf PWNED"),
    );
  }
}

console.log(fails === 0 ? "\nall checks passed" : `\n${fails} FAILED`);
process.exit(fails === 0 ? 0 : 1);

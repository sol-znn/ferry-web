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
    "with nothing executable but, at most, a reclaim",
    commands(sw).every((l) => l.startsWith("znn-cli htlc.reclaim ")),
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
  }).some((l) => l.includes(` 1 '${"ab".repeat(32)}'`)),
);
ok(
  "the side that unlocks the counterparty's HTLC is shown the id, not a runnable unlock",
  (() => {
    const sw = {
      ...base,
      zenonHtlcIsOurs: false,
      fundingCommitted: true,
      zenon: { ...base.zenon, htlcId: "deadbeef" },
      secretHex: "cd".repeat(32),
    };
    const text = znnCommands(sw);
    return (
      !commands(sw).some((l) => l.startsWith("znn-cli htlc.unlock ")) &&
      text.includes("#   htlc id    deadbeef")
    );
  })(),
);

section(
  "the unlock is never printed as a command, and the preimage never leaves the card",
);
// Every state of the record: unverified, verified, terms missing, zero amount,
// a stale verdict. The unlock publishes the preimage; the wallet button
// verifies against the node first and a terminal cannot, so no record state
// prints a runnable unlock, and the real preimage is not in the block at all.
const unlocker = (zenon) => ({
  ...base,
  zenonHtlcIsOurs: false,
  fundingCommitted: true,
  secretHex: "cd".repeat(32),
  zenon: { ...base.zenon, htlcId: "deadbeef", ...zenon },
});
for (const [name, z] of [
  ["unverified", { verified: false }],
  ["verified", { verified: true }],
  ["own address missing", { verified: false, selfAddress: "" }],
  ["zero amount", { verified: false, amountDisplay: "0" }],
  [
    "stale verdict",
    { verified: true, verifyError: "verified by an earlier release" },
  ],
]) {
  const sw = unlocker(z);
  const text = znnCommands(sw);
  const bare = commands(sw);
  ok(
    `${name}: no runnable htlc.unlock`,
    !bare.some((l) => l.includes("htlc.unlock")),
    bare.join(" | "),
  );
  ok(
    `${name}: the preimage is not in the block`,
    !text.includes("cd".repeat(32)),
  );
  ok(
    `${name}: the id is shown as a comment`,
    text.includes("#   htlc id    deadbeef"),
  );
  ok(
    `${name}: receiveAll is still there`,
    bare.some((l) => l.startsWith("znn-cli receiveAll")),
  );
}

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
// The unlock shape prints the HTLC id as a comment; it too comes from outside.
for (const [name, sep] of seps) {
  const sw = {
    ...base,
    zenonHtlcIsOurs: false,
    fundingCommitted: true,
    secretHex: "cd".repeat(32),
    zenon: { ...base.zenon, htlcId: `deadbeef${sep}printf PWNED` },
  };
  const bare = commands(sw);
  ok(
    `htlcId with ${name}: no uncommented line but receiveAll`,
    bare.length === 1 && bare[0].startsWith("znn-cli receiveAll"),
    JSON.stringify(bare),
  );
}
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
      `${field} with ${name}: no uncommented line but, at most, a reclaim`,
      bare.every((l) => l.startsWith("znn-cli htlc.reclaim ")),
      JSON.stringify(bare),
    );
    ok(
      `${field} with ${name}: the injected text is visible, as a comment`,
      text.includes("# printf PWNED"),
    );
  }
}

section("every value in a runnable line is one shell word, whatever it holds");

// A small POSIX reader that does not execute anything, over the whole
// runnable block at once, the way a terminal receives a paste: single quotes
// group, and a newline inside them is part of the word; a backslash outside
// quotes escapes the next character, which is how a quoted single quote is
// written ('\\''); outside quotes whitespace splits, a newline ends a command,
// and $ ` ; | & ( ) < > " are metacharacters a real shell would act on. It
// returns the commands a shell would build, or the metacharacter it found
// bare.
function readShell(block) {
  const cmds = [];
  let argv = [];
  let cur = "";
  let inWord = false;
  let i = 0;
  const endWord = () => {
    if (inWord) argv.push(cur);
    cur = "";
    inWord = false;
  };
  while (i < block.length) {
    const ch = block[i];
    if (ch === "'") {
      const end = block.indexOf("'", i + 1);
      if (end < 0) return { error: "unterminated quote" };
      cur += block.slice(i + 1, end);
      inWord = true;
      i = end + 1;
      continue;
    }
    if (ch === "#" && !inWord) {
      // A comment, as a shell reads one: a bare # where a word would start,
      // OUTSIDE any quote, to the end of the line. A # inside a quoted value
      // -- a newline followed by # inside a single-quoted word -- is text.
      const nl = block.indexOf("\n", i);
      i = nl < 0 ? block.length : nl;
      continue;
    }
    if (ch === "\\") {
      if (i + 1 >= block.length) return { error: "trailing backslash" };
      cur += block[i + 1];
      inWord = true;
      i += 2;
      continue;
    }
    if (ch === "\n") {
      endWord();
      if (argv.length) cmds.push(argv);
      argv = [];
      i++;
      continue;
    }
    if (/\s/.test(ch)) {
      endWord();
      i++;
      continue;
    }
    if ('$`;|&()<>"'.includes(ch)) return { error: `bare ${ch}` };
    cur += ch;
    inWord = true;
    i++;
  }
  endWord();
  if (argv.length) cmds.push(argv);
  return { cmds };
}

const zenonFirst = {
  ...base,
  btcLegIsInitiators: false,
  secretArrivesOnZenon: false,
  fundingCommitted: true,
  zenon: { ...base.zenon, htlcId: "deadbeef" },
};
const evils = [
  "1$(id)",
  "`id`",
  "10; rm -rf /",
  "a b",
  "it's",
  "x\ny",
  "x\n# not a comment\nprintf PWNED",
  "$HOME",
];
for (const [field, where] of [
  ["peerAddress", "zenon"],
  ["tokenStandard", "zenon"],
  ["amountDisplay", "zenon"],
  ["htlcId", "zenon"],
  ["selfAddress", "zenon"],
  ["expirationHours", "zenon"],
  ["secretHashHex", "top"],
  ["nodeURL", "ctx"],
]) {
  for (const evil of evils) {
    const sw = { ...zenonFirst, zenon: { ...zenonFirst.zenon } };
    let ctx = { nodeURL: "wss://node.example:35998" };
    if (where === "zenon") sw.zenon[field] = evil;
    else if (where === "top") sw[field] = evil;
    else ctx = { nodeURL: evil };
    // The whole block, as a terminal would receive it: the reader drops the
    // comments itself, the way a shell does, so a quoted value that happens
    // to contain a newline and a # is not mistaken for one. Placeholders the
    // user fills in are angle-bracketed by design; a shell would read them as
    // redirections, which is exactly why they are unmistakable. They are
    // blanked before reading.
    const block = znnCommands(sw, ctx).replace(/<[^>]+>/g, "PLACEHOLDER");
    const r = readShell(block);
    const bad = r.error ? `${r.error} in: ${block.slice(0, 160)}` : null;
    // Every argument that carries the value carries it whole and alone: no
    // occurrence anywhere in the block is glued to anything else.
    const carrying = r.error
      ? []
      : r.cmds.flat().filter((t) => t.includes(evil));
    const found = carrying.length > 0 && carrying.every((t) => t === evil);
    ok(
      `${field} = ${JSON.stringify(evil)}: no bare metacharacter in any runnable line`,
      !bad,
      bad ?? "",
    );
    ok(
      `${field} = ${JSON.stringify(evil)}: the value arrives as exactly one argument`,
      found,
    );
  }
}

section(
  "a runnable create needs every term of the trade; otherwise it is a comment",
);
for (const [name, missing] of [
  ["no counterparty address", { peerAddress: "" }],
  ["no amount", { amountDisplay: "" }],
  ["no hours yet", { expirationHours: 0 }],
]) {
  const sw = { ...zenonFirst, zenon: { ...zenonFirst.zenon, ...missing } };
  const runnable = commands(sw);
  const text = znnCommands(sw);
  ok(
    `${name}: no runnable htlc.create`,
    !runnable.some((l) => l.includes("htlc.create")),
    runnable.join(" | "),
  );
  ok(
    `${name}: the line is shown as a comment to complete`,
    text.includes("# znn-cli htlc.create") && text.includes("Not runnable yet"),
  );
}
{
  const sw = { ...zenonFirst, secretHashHex: "" };
  ok(
    "no hashlock: no runnable htlc.create",
    !commands(sw).some((l) => l.includes("htlc.create")) &&
      znnCommands(sw).includes("Not runnable yet"),
  );
}
{
  const sw = { ...zenonFirst, zenon: { ...zenonFirst.zenon, htlcId: "" } };
  ok(
    "no htlc id: the reclaim is a comment, not a command",
    !commands(sw).some((l) => l.includes("htlc.reclaim")) &&
      znnCommands(sw).includes("# znn-cli htlc.reclaim <your htlc id>"),
  );
}

console.log(fails === 0 ? "\nall checks passed" : `\n${fails} FAILED`);
process.exit(fails === 0 ? 0 : 1);

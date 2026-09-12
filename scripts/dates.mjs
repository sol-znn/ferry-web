// Checks the date formatting the board renders with is total: a timestamp a
// relay delivers is whoever-signed-it's choice, and a value outside what a
// JavaScript Date can hold used to throw inside a shared render and blank the
// whole inbox. Go refuses such stamps at the read boundary; this pins that the
// page would survive one anyway.
//
//   node --experimental-strip-types scripts/dates.mjs
import { dirname, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const { whenUTC, isoUTC } = await import(
  pathToFileURL(resolve(here, "../ui/src/core/format.ts")).href
);

let fails = 0;
const ok = (label, cond, detail = "") => {
  if (cond) console.log(`  ok   ${label}`);
  else {
    fails++;
    console.log(`  FAIL ${label}${detail ? `\n       ${detail}` : ""}`);
  }
};

console.log("\na board time is always printable");
// 1.7e9 seconds is 2023-11-14T22:13:20Z, a fact rather than a computation.
ok(
  "an ordinary time formats",
  whenUTC(1_700_000_000) === "2023-11-14 22:13:20",
  whenUTC(1_700_000_000),
);
ok(
  "and its ISO form is the same instant",
  isoUTC(1_700_000_000) === "2023-11-14T22:13:20.000Z",
  isoUTC(1_700_000_000),
);
for (const [name, v] of [
  ["beyond what a Date can hold", 2 ** 50],
  ["hugely negative", -(2 ** 50)],
  ["not a number", Number.NaN],
  ["infinite", Number.POSITIVE_INFINITY],
  ["undefined", undefined],
  ["a string", "1_700_000_000"],
]) {
  let threw = false;
  let out;
  try {
    out = whenUTC(v);
  } catch {
    threw = true;
  }
  ok(`${name}: does not throw`, !threw);
  ok(`${name}: prints a dash`, out === "—", JSON.stringify(out));
  ok(`${name}: has no ISO form`, isoUTC(v) === "", JSON.stringify(isoUTC(v)));
}

console.log(fails === 0 ? "\nall checks passed" : `\n${fails} FAILED`);
process.exit(fails === 0 ? 0 : 1);

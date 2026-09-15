# Ferry security audit — developer handoff

Audit date: 11 September 2026. Revision: `be879e1bd61a9922ee58e16d32095bafa43bdf6e`.

**7 reportable findings: 4 high, 2 medium, 1 low.** All 152 tracked files were source-reviewed. The overall assessment remains partial because runtime and external-wallet validation are incomplete. No application changes or fixes were made.

Open **[START_HERE.html](START_HERE.html)** in a browser for a self-contained review with each finding, exact source excerpts, attack conditions, remediation and proposed regression tests. It works offline; source links open the audited GitHub commit when requested.

## Findings

| Reference | Priority | Finding | Root control |
| --- | --- | --- | --- |
| F01 | High / P1 | Auto Mode reveals the secret for an underfunded Bitcoin payment | [wasm/manager.go:945](https://github.com/sol-znn/ferry-web/blob/be879e1bd61a9922ee58e16d32095bafa43bdf6e/wasm/manager.go#L945-L951) |
| F02 | High / P1 | Zenon funds can be locked against replaceable Bitcoin funding | [wasm/walletblock.go:235](https://github.com/sol-znn/ferry-web/blob/be879e1bd61a9922ee58e16d32095bafa43bdf6e/wasm/walletblock.go#L235-L239) |
| F03 | High / P1 | Missing Zenon terms can verify an HTLC that pays the attacker | [wasm/znn/client.go:450](https://github.com/sol-znn/ferry-web/blob/be879e1bd61a9922ee58e16d32095bafa43bdf6e/wasm/znn/client.go#L450-L453) |
| F04 | High / P1 | Contract audit accepts a script that blocks the victim's redeem branch | [wasm/htlc.go:179](https://github.com/sol-znn/ferry-web/blob/be879e1bd61a9922ee58e16d32095bafa43bdf6e/wasm/htlc.go#L179-L182) |
| F05 | Medium / P2 | A session peer can replace the script of an already-funded swap | [wasm/manager.go:438](https://github.com/sol-znn/ferry-web/blob/be879e1bd61a9922ee58e16d32095bafa43bdf6e/wasm/manager.go#L438-L441) |
| F06 | Medium / P2 | An offer amount becomes shell syntax in copied CLI commands | [ui/src/core/zenon-commands.ts:94](https://github.com/sol-znn/ferry-web/blob/be879e1bd61a9922ee58e16d32095bafa43bdf6e/ui/src/core/zenon-commands.ts#L94-L98) |
| F07 | Low / P3 | A crafted take timestamp disables the shared board inbox | [ui/src/components/BoardInbox.vue:94](https://github.com/sol-znn/ferry-web/blob/be879e1bd61a9922ee58e16d32095bafa43bdf6e/ui/src/components/BoardInbox.vue#L94) |

## Recommended repair order

- Address F01–F04 before relying on the affected flows: enforce full and sufficiently confirmed consideration, require complete Zenon expectations, and accept only canonical Bitcoin scripts.
- Address F05 by freezing contract identity once either leg is committed and validating the actual funding output before signing. Preserve identical session retransmissions and recovery.
- Address F06 with strict amount validation and safe shell argument encoding; address F07 with bounded timestamps and nonthrowing rendering.
- Use the [hardening proposal](hardening/hardening.md) to compare immediate method guards with an engine-owned action-validation boundary. This proposal is not implemented.

## Validation and remaining work

The findings are source-validated, with explicit attacker prerequisites and counterevidence. Only the exact inbox date formatter was executed in isolation; an out-of-range timestamp reproduced `RangeError`. No end-to-end exploit, production site test or wallet/chain integration test ran.

The installed Go compiler was 1.25.4, while the repository declares Go 1.27.0. An isolated test copy also lacked required dependency versions offline. No Go regression test ran or passed. Current dependency advisory databases and third-party implementations were not comprehensively audited.

**Unconfirmed follow-up, not an eighth finding:** determine whether the supported Zenon wallet publishes secret-bearing Unlock data for an expired or missing HTLC before rejecting it. Use local test chains and no real funds. This proof gap is retained in coverage.json.

For contract replacement, recovery with the original script may prevent loss. For nonminimal scripts, the proven restriction is Ferry's redeem path and standard relay policy, not absolute consensus unspendability. For the inbox issue, delivery through an honest default public relay was not tested; a malicious configured relay is the supported case.

## Included files

- `START_HERE.html` — browser-readable handoff and seven detailed findings.
- `report.md` — complete sanitized generated report, including threat model and coverage.
- `findings.json`, `coverage.json`, `scan-manifest.json` — sanitized structured share exports.
- `exports/results.sarif` — finding locations for developer tooling.
- `hardening/` — repair portfolio, full design proposal, structured analysis and Mermaid diagrams.
- `BUNDLE_INFO.json`, `SHA256SUMS.txt` — export metadata and checksums.

## Sanitization and integrity

Personal identifiers and machine-local source paths were removed. Source links use repository-relative paths and the exact audited GitHub commit. The original audit files remain unchanged; this bundle is a derived share export, not the original sealed artifact set. Original seal and checkpoint metadata were omitted to avoid implying that original hashes validate the redacted files. Verify the included files after extraction with `shasum -a 256 -c SHA256SUMS.txt`.

Drafts, checkpoints, repository files, unexecuted experimental tests and operating-system metadata are not included. This report describes a source review at one revision, not a guarantee that every vulnerability has been found.

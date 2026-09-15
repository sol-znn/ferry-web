# Ferry hardening evidence context

Source root: ferry-web

Revision: be879e1bd61a9922ee58e16d32095bafa43bdf6e

Scan: f57e14cb-0b12-4e58-9724-a1cd8419eb3c

Input canonical documents: scan-manifest.json, findings.json and coverage.json in the parent scan directory. All were unsealed during this derived analysis. Their semantic data and referenced source were inspected; no sealed-integrity claim is made. Seven source-backed findings are identified below by stable candidate identity until finalization assigns canonical hashes.

- btc-underfunding — Auto Mode reveals the secret for an underfunded Bitcoin payment; wasm/manager.go
- btc-unconfirmed-create — Zenon funds can be locked against replaceable Bitcoin funding; wasm/walletblock.go
- incomplete-zenon-terms — Missing Zenon terms can verify an HTLC that pays the attacker; wasm/znn/client.go
- nonminimal-contract-push — Contract audit accepts a script that blocks the victim's redeem branch; wasm/htlc.go
- contract-replacement — A session peer can replace the script of an already-funded swap; wasm/manager.go
- cli-offer-command-injection — An offer amount becomes shell syntax in copied CLI commands; ui/src/core/zenon-commands.ts
- board-inbox-timestamp — A crafted take timestamp disables the shared board inbox; ui/src/components/BoardInbox.vue

Coverage: all 152 tracked files security-reviewed. Validation: source traces; exact inbox Date expression executed in isolation. Offline Go regressions did not run because the supported compiler and required cached modules were unavailable. No production chain, deployed site, external wallet, dependency advisory service or live relay was tested.


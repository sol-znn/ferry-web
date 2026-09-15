# Security Review: ferry-web

> Sanitized developer share copy. Start with [START_HERE.md](START_HERE.md) or [the browser handoff](START_HERE.html).

## Scope

Full security source audit of all 152 tracked files at be879e1bd61a9922ee58e16d32095bafa43bdf6e, including Go/WASM, Vue, wallets, chain clients, Nostr, storage/recovery, build/deployment, test sources and documentation.

- Scan mode: repository
- Target kind: git_revision
- Target ID: target_sha256_b6b3e0f02a705fd508b92e542efbc8c5f5412d397c222dd5dac92d75d71505fe
- Revision: be879e1bd61a9922ee58e16d32095bafa43bdf6e
- Inventory strategy: repository
- Included paths: .
- Excluded paths: none
- Runtime or test status: The extracted BoardInbox date expression reproduced RangeError for an out-of-range timestamp. No complete application, browser, wallet, chain exploit or supported test suite was executed.
- Artifacts reviewed: wasm/, ui/, scripts/, docs/, .github/workflows/deploy.yml, README.md, LICENSE

Limitations and exclusions:
- Installed Go 1.25.4 is older than the declared Go 1.27.0. An isolated disposable test copy could not compile offline because exact btcec/v2 v2.1.3 and x/crypto v0.0.0-20200622213623-75b288015ac9 sources were unavailable; no Go regression test passed or ran.
- No production deployment, wallet/chain integration, public relay acceptance policy or live penetration test was assessed.
- Dependency manifests, lockfiles and call sites were reviewed; current vulnerability-advisory databases and third-party package implementations were not comprehensively audited.
- Expired/missing-HTLC Unlock publication behavior is an explicit unresolved external-wallet proof gap.
- Source coverage is complete for this revision, not a guarantee that every vulnerability has been found.

### Scan Summary

| Field | Value |
| --- | --- |
| Scan outcome | completed |
| Reportable findings | 7 |
| Severity mix | high: 4, medium: 2, low: 1 |
| Confidence mix | high: 7 |
| Coverage | partial |
| Validation mode | Offline source analysis with an independent baseline and focused reviews; isolated execution of the exact inbox date formatter. |

Supporting share exports: `scan-manifest.json`, `findings.json`, and `coverage.json`. This is a sanitized copy of the generated audit report; original seal metadata is intentionally omitted. Bundle checksums are in `SHA256SUMS.txt`.

## Threat Model

Ferry is a static Vue application whose Go/WebAssembly module manages BTC/ZNN atomic swaps, constructs and signs Bitcoin HTLC spends, persists browser state, and verifies counterparty and chain inputs. Vue loads ./wasm_exec.js and ./ferry.wasm with a build-derived cache query; the module exposes window.ferryWasm.call(method, JSON) after checking writable localStorage. There is no production application server. Bitcoin reads and signed broadcasts go to a configured Esplora; Zenon reads go to a configured HTTP/WebSocket node. External UniSat/Syrius wallet providers fund contracts and sign/publish Zenon operations. Public offers and encrypted conversations use Nostr relays. Official builds produce dist/ for production and dist-dev/ for development; development additionally supports a loopback regtest Esplora shim. User context authorizes the entire current repository. Sources: ui/package.json:6; ui/src/main.ts:21; ui/src/core/wasm.ts:87; wasm/main_js.go:35; wasm/api.go:251; scripts/build.mjs:47; ui/vite.config.ts:28.

### Assets

- Bitcoin HTLC funds, ephemeral Swap.Key private scalars, secret preimages, signed refunds, destination addresses, and accepted swap terms. Swap JSON is unencrypted in origin localStorage at ferry.swap.\<hex-id\> for production or ferry.dev.swap.\<hex-id\> for development. Store.Save serializes the complete record; the ordinary view omits private-key bytes but includes a known preimage. Sources: wasm/store.go:29; wasm/store.go:80; wasm/store.go:98; wasm/api.go:179; wasm/api.go:184.
- Board signing identity and accepted session codes: production localStorage keys ferry.board.identity and ferry.board.post.\<id\>; development keys ferry.dev.board.identity and ferry.dev.board.post.\<id\>. Identity contains a private scalar; accepted posts can contain a session code. Their authority is offer signing/editing and decrypting addressed takes, independently of transaction signing. Sources: wasm/boardstore.go:26; wasm/boardstore.go:33; wasm/boardstore.go:51; wasm/boardstore.go:116; wasm/board.go:255; wasm/boardpost.go:842.
- Encrypted session contents and room membership authority. Generated codes use 16 random bytes; domain-separated derivation creates a shared Schnorr identity, room identifier, and AES-GCM key. Both participants possess the same room capability; authentication proves code possession, not distinct participant identities. The UI receives the code. Sources: wasm/session.go:53; wasm/session.go:124; wasm/session.go:247; wasm/session.go:293; wasm/session.go:354; wasm/session_api.go:26.
- External Bitcoin/Zenon wallet accounts and their signing authority. Ferry calls injected providers without receiving those wallet private keys; provider implementation and its approval interface are outside this repository. Sources: ui/src/core/composables/useUnisat.ts:289; ui/src/core/zenon-wallet.ts:448; ui/src/components/ZenonWallet.vue:204.
- Recovery and export files are portable spending capabilities. Export includes complete swap records; recovery includes privateKeyWIF and known preimage. The UI creates a Blob download named ferry-swaps-\<date\>.json for a full export; actual disk destination is browser/user controlled. Sources: wasm/store.go:185; wasm/api.go:757; ui/src/components/BackupPanel.vue:22; ui/src/core/api.ts:332.
- Release integrity and the publisher's GitHub Pages authority. Repository dependencies, Go compiler/runtime shim, Vite output, uploaded artifacts, and the deploying workflow determine code executed with origin-local key access. The workflow grants contents:read, pages:write, and id-token:write. Sources: ui/package.json:28; scripts/build.mjs:76; scripts/build.mjs:101; .github/workflows/deploy.yml:34; .github/workflows/deploy.yml:74; .github/workflows/deploy.yml:132.

### Trust Boundaries

- Website origin to browser key storage: same-origin scripts, including other paths on that origin, share storage and access to the unrestricted ferryWasm API. Prefixes select records but do not isolate principals. The page supplies a meta CSP with script-src 'self' 'wasm-unsafe-eval', connect-src \*, object-src 'none', base-uri 'none', and form-action 'none'. Host framing restrictions are not established by this source. Sources: wasm/main_js.go:48; wasm/storage_js.go:30; wasm/store.go:29; ui/index.html:35; docs/SECURITY.md:46.
- Counterparty offers/contracts/preimages to local swap authority: JSON handlers decode fields, Manager audits contract commitments and leg ordering, and secret input must match the existing hash. Bitcoin spends are signed with SIGHASH_ALL and executed in the local script engine before return. The HTLC itself commits branch key hashes, secret hash, and timelock; it does not covenant a fixed payout address. Sources: wasm/api.go:310; wasm/manager.go:388; wasm/manager.go:467; wasm/manager.go:848; wasm/htlc.go:58; wasm/txbuild.go:141; wasm/txbuild.go:183; wasm/txbuild.go:196.
- Configured nodes to economic decisions: each API call creates its Manager from current settings. An explicit Esplora URL overrides per-network defaults; production default is https://blockstream.info/api, testnet https://blockstream.info/testnet/api, signet https://mempool.space/signet/api, and regtest http://127.0.0.1:3002. Zenon has no production default and development initially selects http://127.0.0.1:35997. Responses inform funding, confirmations, fees, matching chain identifiers, HTLC terms, and settlement. Browser HTTP uses omitted credentials, CORS mode, timeout/cancellation, and redirect following. Sources: wasm/api.go:36; wasm/swap.go:402; ui/src/core/env.ts:45; wasm/chain/esplora.go:92; wasm/chain/esplora.go:272; wasm/znn/client.go:111; wasm/httpx/httpx_js.go:40; wasm/manager.go:1140.
- Relays to board and session consumers: custom relay settings override five built-in WSS relays; Vue transports events while Go verifies events and cryptography. ReadPost verifies signature, post structure, identity tag, and wallet proofs; OpenTake verifies signature and recipient then decrypts using identity-derived shared material. OpenSession recomputes the signed event ID and verifies/decrypts under the shared room capability. Network filtering requests sent to relays are not authority. Sources: ui/src/core/composables/useSettings.ts:118; ui/src/core/nostr.ts:60; ui/src/core/nostr.ts:168; wasm/boardpost.go:464; wasm/boardpost.go:503; wasm/boardpost.go:842; wasm/session.go:293.
- Board identity to wallet-address attribution: wallets sign a fixed statement binding an address to a particular board public key; Go verifies that proof and matches the proven address to the address actually claimed by the post. This delegates offer publication, not payment authority. Sources: wasm/board.go:382; wasm/board.go:398; wasm/boardpost.go:503.
- Ferry to Zenon wallet: Go refuses to build a block when account/chain checks fail or cannot run, directly reads the wallet-reported node, and compares both nodes at a shared momentum height. Vue previews the block, invalidates stale wallet plans, rechecks before submission, and passes returned proposed/signed blocks for post-publication comparison. Wallet approval, selected-account behavior, and publication are externally enforced; a post-publication difference check cannot undo publication. Sources: wasm/walletsync.go:115; wasm/walletsync.go:154; wasm/walletsync.go:185; wasm/walletsync.go:217; wasm/walletblock.go:175; ui/src/components/ZenonWallet.vue:142; ui/src/components/ZenonWallet.vue:185; ui/src/components/ZenonWallet.vue:204; wasm/walletblock.go:467.
- Backup file to current local records: the browser reads a user-selected file, Store.Import validates record structure and key consistency, preserves an existing readable record, and saves missing records. Recovery Rebuild consumes file material and constructs a spend without Store or network dependencies. These are explicit key-import/export capabilities, not untrusted relay operations. Sources: ui/src/components/BackupPanel.vue:38; wasm/store.go:211; wasm/store.go:256; wasm/recover.go:95.
- Developer browser to regtest shim to authenticated Bitcoin Core: the optional Node shim binds 127.0.0.1:\<configured-port\>, defaults to port 3002, sends Basic authorization derived from configured RPC credential references to the configured RPC URL, defaults to http://127.0.0.1:18443, refuses non-regtest chains, and exposes selected reads plus raw transaction broadcast with wildcard CORS. It is not part of dist/. Sources: scripts/regtest-esplora.mjs:39; scripts/regtest-esplora.mjs:44; scripts/regtest-esplora.mjs:45; scripts/regtest-esplora.mjs:46; scripts/regtest-esplora.mjs:47; scripts/regtest-esplora.mjs:58; scripts/regtest-esplora.mjs:61; scripts/regtest-esplora.mjs:249; scripts/regtest-esplora.mjs:289; scripts/regtest-esplora.mjs:370; scripts/regtest-esplora.mjs:378.
- Repository/build dependencies to published origin: push to master or workflow_dispatch builds both instances, tests them, uploads development as an artifact, and publishes the selected directory through GitHub Pages. PUBLISH defaults to prod; a manual dev selection replaces the site with development output. GitHub environment protections and production hostname are external unknowns. Sources: .github/workflows/deploy.yml:19; .github/workflows/deploy.yml:105; .github/workflows/deploy.yml:126; .github/workflows/deploy.yml:132; .github/workflows/deploy.yml:136.

### Attacker Capabilities

- A malicious counterparty can choose offer terms, contract bytes, claimed transaction/HTLC identifiers, and messages under any session code they legitimately possess. They do not initially possess the victim's swap private key or external wallet key. Relevant boundaries are contract/term verification and secret-release ordering. Sources: wasm/manager.go:388; wasm/manager.go:467; wasm/session.go:293.
- A public board participant can generate their own identity and publish signed claims or send encrypted takes to another board identity. They cannot thereby forge another wallet's binding proof or the victim's board signature. Sources: wasm/board.go:269; wasm/board.go:398; wasm/boardpost.go:464; wasm/boardpost.go:842.
- A relay operator can observe public offers and session metadata, retain/replay/drop events, and supply arbitrary frames. Without a session code or the addressed board identity secret, event signing/decryption checks limit accepted tampering. Room participants share equal code-derived authority; peerId is a replay/UI identifier rather than authentication. Sources: ui/src/core/nostr.ts:193; wasm/session.go:124; wasm/session.go:293; wasm/boardpost.go:842; ui/src/core/composables/useSession.ts:179.
- An operator of an endpoint already selected by the user or wallet can supply dishonest chain responses, fees, and timing information. Exploiting this trust requires selection, endpoint/network compromise, or another demonstrated path changing settings; mere ability to host a node is insufficient. Sources: wasm/api.go:36; wasm/chain/esplora.go:242; wasm/walletsync.go:185.
- A script executing with the deployment origin's authority can read unencrypted keys, call export/recovery, and invoke signing APIs. This requires a demonstrated same-origin script capability, compromised served build/dependency, or privileged extension; a generic unrelated website does not initially have it. Sources: wasm/main_js.go:48; wasm/storage_js.go:30; wasm/api.go:757; ui/index.html:35.
- A user-controlled import/recovery file can carry malformed records and key material, but reaching these parsers requires selection/pasting by the local user. Code execution or new spending authority is not established solely by that input capability. Sources: ui/src/components/BackupPanel.vue:38; wasm/store.go:211; wasm/recover.go:95.
- Only a conditional local-development deployment exposes the regtest shim. Cross-origin callers may reach its deliberately CORS-enabled endpoints when browser networking permits, but the shim does not provide arbitrary wallet RPC and checks that its backing chain is regtest. Sources: scripts/regtest-esplora.mjs:249; scripts/regtest-esplora.mjs:289; scripts/regtest-esplora.mjs:370.

### Security Objectives

- Preserve spendability and confidentiality of swap keys, preimages, recovery documents, and board identities within their actual browser-origin and user-file boundaries; refuse starting a swap engine when its persistence probe fails. Sources: wasm/storage_js.go:39; wasm/main_js.go:35; wasm/store.go:98.
- Accept counterparty contracts and Zenon HTLCs only under agreed keys, hashlocks, token/amount terms, chain context, and role-correct expiry ordering; maintain local destination commitments and verify generated Bitcoin spends before exposing them. Sources: wasm/manager.go:388; wasm/manager.go:467; wasm/manager.go:1140; wasm/txbuild.go:196.
- Keep room membership, board-author identity, wallet-address attribution, and wallet payment authorization distinct. A signed relay event authenticates its signing capability, not truth of a trade or permission to spend. Sources: wasm/session.go:293; wasm/board.go:382; wasm/boardpost.go:464; wasm/walletblock.go:175.
- Bind wallet proposals to the intended account, chain, operation, recipient, token, amount, and payload; reject incomplete chain comparisons before building and compare returned publication data. External wallet approval remains a required external control. Sources: wasm/walletsync.go:115; wasm/walletblock.go:175; ui/src/components/ZenonWallet.vue:185; wasm/walletblock.go:467.
- Preserve newer local recovery state when importing backups and make portable exports explicit. Network-independent Rebuild must not depend on a stored swap. Sources: wasm/store.go:211; wasm/recover.go:95; ui/src/components/BackupPanel.vue:22.
- Prevent accidental production/development record and configuration mixing through build-specific prefixes; use distinct origins when security isolation between deployments is required. Preserve compiler/runtime-shim pairing in released artifacts. Sources: wasm/store.go:29; wasm/boardstore.go:26; ui/src/core/composables/useSettings.ts:22; scripts/build.mjs:101; docs/DEPLOY.md:64.
- Bound untrusted network processing without treating filter requests or declared lengths as authoritative. Current HTTP code defines an 8 MiB body limit, but browser buffering happens before final copied-byte truncation; resource guarantees must reflect that implementation. Sources: wasm/httpx/httpx.go:25; wasm/httpx/httpx_js.go:72; wasm/httpx/httpx_js.go:82.

### Assumptions

- No actual deployed hostname, host headers, GitHub environment restrictions, selected node URLs, wallet binaries, or relay configuration were supplied. Source defaults do not prove an observed deployment. Executable directories resolve to no inherited SECURITY.md policy; docs/SECURITY.md is documentation within the docs subtree.
- Production/development separation is namespacing, not origin isolation. docs/DEPLOY.md:42 says the two never share stored state; settings, swap, and board keys are instance-specific, but ui/src/core/composables/useSession.ts:179 uses shared ferry.session.peer and all same-origin scripts can read either prefix. docs/DEPLOY.md:64 explicitly recommends different origins for actual separation.
- docs/ARCHITECTURE.md:29 says the UI never holds a key. Actual recovery/export APIs deliberately return private key material to JavaScript for download, and sessionNew returns the code from which session keys derive. Go owns cryptographic operations but is not an isolation boundary against same-origin JavaScript. Sources: wasm/api.go:757; wasm/store.go:185; ui/src/components/BackupPanel.vue:22; wasm/session_api.go:26.
- docs/SECURITY.md:10 attributes fixed destination protection to the contract and says there is no alternate payout code path. The HTLC locks branch keys/hash/timelock; buildSpend selects a destination output, and recovery intentionally permits a different destination. Fixed payout protection is application policy, not a covenant. Sources: wasm/htlc.go:58; wasm/txbuild.go:141; wasm/recover.go:95.
- wasm/httpx/httpx_js.go:40 comments promise no redirect chasing, but redirect is explicitly follow at that same location. HTTP requests omit credentials and remain browser-policy constrained; the claimed fixed-recipient guarantee is not implemented.
- docs/DEPLOY.md:73 says automatic deployment follows main, whereas .github/workflows/deploy.yml:19 triggers master. The workflow is the effective deployment behavior.
- Recovery computation is independent of storage/network, but the browser Recover route is inside EngineGate and startup refuses to expose the API when writable localStorage is unavailable. Thus docs/ARCHITECTURE.md:248 and docs/SECURITY.md:74 describe the computation more broadly than the current page-entry prerequisites support. Sources: wasm/recover.go:95; ui/src/router.ts:23; ui/src/App.vue:45; wasm/main_js.go:35.
- Official build scripts set both UI and WASM environment together, but both instance builds use the shared intermediate ui/public/ferry.wasm and ui/public/wasm_exec.js. Bare Vite defaults UI environment to dev while Go defaults to prod unless linked otherwise; mixed development artifacts are possible outside the official complete build. Sources: scripts/build.mjs:35; scripts/build.mjs:47; scripts/build.mjs:127; ui/vite.config.ts:54; wasm/env.go:26.
- Native httpx uses net/http and bounded reader semantics, whereas shipped js/wasm uses Fetch and arrayBuffer. There is no native production executable startup established here; native code supports tests and tooling. Sources: wasm/httpx/httpx_native.go:1; wasm/httpx/httpx_native.go:40; wasm/httpx/httpx_native.go:46; wasm/main_js.go:1; wasm/httpx/httpx_js.go:82.
- Wallet provider source is absent, so advertised per-operation user approval, account selection during approval, and node publication behavior remain external assumptions. The optional returned signed block makes after-publication comparison conditional. Sources: ui/src/core/zenon-wallet.ts:448; ui/src/core/zenon-wallet.ts:470; wasm/walletblock.go:467.
- This independent architecture mapping is not completed security-audit coverage and establishes no vulnerability findings.

## Findings

| Finding | Severity | Confidence | Detailed write-up |
| --- | --- | --- | --- |
| [Contract audit accepts a script that blocks the victim's redeem branch](#finding-1) | high | high | inline below |
| [Zenon funds can be locked against replaceable Bitcoin funding](#finding-2) | high | high | inline below |
| [Auto Mode reveals the secret for an underfunded Bitcoin payment](#finding-3) | high | high | inline below |
| [Missing Zenon terms can verify an HTLC that pays the attacker](#finding-4) | high | high | inline below |
| [An offer amount becomes shell syntax in copied CLI commands](#finding-5) | medium | high | inline below |
| [A session peer can replace the script of an already-funded swap](#finding-6) | medium | high | inline below |
| [A crafted take timestamp disables the shared board inbox](#finding-7) | low | high | inline below |

### Confidence Scale

| Label | Meaning |
| --- | --- |
| high | Direct evidence supports the finding with no material unresolved blocker. |
| medium | Evidence supports a plausible issue, but material runtime or reachability proof remains. |
| low | Evidence is incomplete and the item is retained only for explicit follow-up. |

<a id="finding-1"></a>

### [1] Contract audit accepts a script that blocks the victim's redeem branch

| Field | Value |
| --- | --- |
| Severity | high |
| Confidence | high |
| Confidence rationale | Complete source-to-sink trace with identified controls and counterevidence; independent review corroborates the path. Dynamic end-to-end proof was not obtained. |
| Category | atomic-swap |
| CWE | CWE-20 |
| Affected lines | wasm/htlc.go:179-182, wasm/htlc.go:148-168, wasm/manager.go:403-421, wasm/txbuild.go:193-204 |

#### Summary

A Bitcoin initiator can send a nonminimal push encoding that passes Ferry's contract audit but fails its later redeem validation. The Zenon participant may commit funds to the matching HTLC while its normal Bitcoin claim is unusable; the attacker can still use the refund branch.

#### Root Cause

`ParseContract()` records each token's opcode and data but compares opcodes only at fixed template positions. Dynamic hash and pubkey pushes are accepted based on their decoded length, and numeric values are checked separately without requiring canonical push opcodes. `AuditContract()` accepts those parsed terms. Later `BuildRedeem()` runs the script with `StandardVerifyFlags`, which enforces minimal pushes in the executing branch.

**Only fixed template opcodes are compared** — `wasm/htlc.go:148-168`

Raw peer bytes are tokenized, but dynamic push opcodes are outside the fixed opcode checks.

```go
	for tk.Next() {
		if len(tokens) == contractTemplateLen {
			return nil, errors.New("script is longer than the atomic swap template")
		}
		tokens = append(tokens, scriptToken{op: tk.Opcode(), data: tk.Data()})
	}
	if err := tk.Err(); err != nil {
		return nil, fmt.Errorf("malformed script: %w", err)
	}
	if len(tokens) != contractTemplateLen {
		return nil, fmt.Errorf("script has %d elements, the atomic swap template has %d",
			len(tokens), contractTemplateLen)
	}
	for idx, op := range contractTemplate {
		if tokens[idx].op != op {
			return nil, fmt.Errorf("script does not match the atomic swap template at element %d", idx)
		}
	}

	secretSize, err := scriptNum(tokens[2], maxScriptNumLen)
	if err != nil {
```

**Dynamic pushes are validated by payload** — `wasm/htlc.go:179-199`

A nonminimal push preserves the expected hash and pubkey payload lengths and therefore survives parsing.

```go
	}
	pkhRedeem := tokens[9].data
	if len(pkhRedeem) != 20 {
		return nil, errors.New("redeem pubkey hash is not 20 bytes")
	}
	locktime, err := scriptNum(tokens[11], maxCLTVScriptNumLen)
	if err != nil {
		return nil, fmt.Errorf("bad locktime: %w", err)
	}
	// A contract whose locktime is not a timestamp is not one this tool can
	// reason about: every deadline it reports, and the refund it pre-signs,
	// assume the refund branch opens at a wall-clock moment.
	if err := validLockTime(locktime); err != nil {
		return nil, fmt.Errorf("contract %w", err)
	}
	pkhRefund := tokens[16].data
	if len(pkhRefund) != 20 {
		return nil, errors.New("refund pubkey hash is not 20 bytes")
	}

	return &ContractDetails{
```

**Parsed contract accepted under the agreed keys** — `wasm/manager.go:403-421`

Audit checks parsed identity and hash, so the alternate encoding can satisfy those checks.

```go
	details, err := ParseContract(contract)
	if err != nil {
		return nil, err
	}
	params, err := sw.Params()
	if err != nil {
		return nil, err
	}

	if !bytes.Equal(details.PkhRedeem, sw.Key.PKH) {
		return nil, fmt.Errorf("contract lets %s redeem, not this swap's key %s: "+
			"you would not be able to claim these funds",
			hex.EncodeToString(details.PkhRedeem), sw.Key.PKHHex())
	}
	if len(sw.SecretHash) > 0 && !bytes.Equal(details.SecretHash, sw.SecretHash) {
		return nil, fmt.Errorf("contract commits to secret hash %s, this swap uses %s",
			hex.EncodeToString(details.SecretHash), hex.EncodeToString(sw.SecretHash))
	}
	if len(sw.SecretHash) == 0 {
```

**Redeem executes with standard flags** — `wasm/txbuild.go:193-204`

Ferry rejects the spend when the nonminimal instruction is executed, after the peer may have committed its other leg.

```go
	// Execute the script locally before handing the transaction to anyone.
	// A swap failing at broadcast time is recoverable; one that fails silently
	// is not.
	engine, err := txscript.NewEngine(contractPkScript, tx, 0, txscript.StandardVerifyFlags,
		txscript.NewSigCache(10), txscript.NewTxSigHashes(tx, prevoutFetcher(contractPkScript, funding.Value)),
		funding.Value, prevoutFetcher(contractPkScript, funding.Value))
	if err != nil {
		return nil, fmt.Errorf("script engine: %w", err)
	}
	if err := engine.Execute(); err != nil {
		return nil, fmt.Errorf("built transaction does not satisfy the contract: %w", err)
	}
```

#### Validation

The parent checked the exact parser and audit path and the pinned btcd v0.24.2 script engine in the local module cache. Replacing the direct 32-byte secret-hash push with `OP_PUSHDATA1 0x20` preserves the decoded tokens accepted by Ferry. The redeem branch executes this nonminimal push and fails `ScriptVerifyMinimalData`; the refund branch skips it. A prepared isolated regression did not run because required dependency versions were unavailable offline.

Validation method: static source trace

**Only fixed template opcodes are compared** — `wasm/htlc.go:148-168`

Raw peer bytes are tokenized, but dynamic push opcodes are outside the fixed opcode checks.

```go
	for tk.Next() {
		if len(tokens) == contractTemplateLen {
			return nil, errors.New("script is longer than the atomic swap template")
		}
		tokens = append(tokens, scriptToken{op: tk.Opcode(), data: tk.Data()})
	}
	if err := tk.Err(); err != nil {
		return nil, fmt.Errorf("malformed script: %w", err)
	}
	if len(tokens) != contractTemplateLen {
		return nil, fmt.Errorf("script has %d elements, the atomic swap template has %d",
			len(tokens), contractTemplateLen)
	}
	for idx, op := range contractTemplate {
		if tokens[idx].op != op {
			return nil, fmt.Errorf("script does not match the atomic swap template at element %d", idx)
		}
	}

	secretSize, err := scriptNum(tokens[2], maxScriptNumLen)
	if err != nil {
```

**Dynamic pushes are validated by payload** — `wasm/htlc.go:179-199`

A nonminimal push preserves the expected hash and pubkey payload lengths and therefore survives parsing.

```go
	}
	pkhRedeem := tokens[9].data
	if len(pkhRedeem) != 20 {
		return nil, errors.New("redeem pubkey hash is not 20 bytes")
	}
	locktime, err := scriptNum(tokens[11], maxCLTVScriptNumLen)
	if err != nil {
		return nil, fmt.Errorf("bad locktime: %w", err)
	}
	// A contract whose locktime is not a timestamp is not one this tool can
	// reason about: every deadline it reports, and the refund it pre-signs,
	// assume the refund branch opens at a wall-clock moment.
	if err := validLockTime(locktime); err != nil {
		return nil, fmt.Errorf("contract %w", err)
	}
	pkhRefund := tokens[16].data
	if len(pkhRefund) != 20 {
		return nil, errors.New("refund pubkey hash is not 20 bytes")
	}

	return &ContractDetails{
```

**Parsed contract accepted under the agreed keys** — `wasm/manager.go:403-421`

Audit checks parsed identity and hash, so the alternate encoding can satisfy those checks.

```go
	details, err := ParseContract(contract)
	if err != nil {
		return nil, err
	}
	params, err := sw.Params()
	if err != nil {
		return nil, err
	}

	if !bytes.Equal(details.PkhRedeem, sw.Key.PKH) {
		return nil, fmt.Errorf("contract lets %s redeem, not this swap's key %s: "+
			"you would not be able to claim these funds",
			hex.EncodeToString(details.PkhRedeem), sw.Key.PKHHex())
	}
	if len(sw.SecretHash) > 0 && !bytes.Equal(details.SecretHash, sw.SecretHash) {
		return nil, fmt.Errorf("contract commits to secret hash %s, this swap uses %s",
			hex.EncodeToString(details.SecretHash), hex.EncodeToString(sw.SecretHash))
	}
	if len(sw.SecretHash) == 0 {
```

**Redeem executes with standard flags** — `wasm/txbuild.go:193-204`

Ferry rejects the spend when the nonminimal instruction is executed, after the peer may have committed its other leg.

```go
	// Execute the script locally before handing the transaction to anyone.
	// A swap failing at broadcast time is recoverable; one that fails silently
	// is not.
	engine, err := txscript.NewEngine(contractPkScript, tx, 0, txscript.StandardVerifyFlags,
		txscript.NewSigCache(10), txscript.NewTxSigHashes(tx, prevoutFetcher(contractPkScript, funding.Value)),
		funding.Value, prevoutFetcher(contractPkScript, funding.Value))
	if err != nil {
		return nil, fmt.Errorf("script engine: %w", err)
	}
	if err := engine.Execute(); err != nil {
		return nil, fmt.Errorf("built transaction does not satisfy the contract: %w", err)
	}
```

Assertions:
- An accepted contract must permit the victim to execute the redeem branch under the transaction policy Ferry itself enforces.
- The victim's Bitcoin claim is blocked by Ferry and standard transaction policy while the attacker can claim Zenon and later refund the Bitcoin.

Limitations:
- No live wallet or chain exploit was run; supported Go build and isolated regressions were blocked by toolchain and offline dependency availability.
- The demonstrated claim is failure of Ferry's normal redeem and standard relay policy, not absolute Bitcoin consensus unspendability; a specially constructed nonstandard spend mined outside ordinary relay may be possible.

#### Dataflow

counterparty-supplied raw Bitcoin contract hex → accepted contract followed by a locally rejected Bitcoin redeem. The victim's Bitcoin claim is blocked by Ferry and standard transaction policy while the attacker can claim Zenon and later refund the Bitcoin.

- **Source:** counterparty-supplied raw Bitcoin contract hex

- **Sink:** accepted contract followed by a locally rejected Bitcoin redeem

- **Outcome:** The victim's Bitcoin claim is blocked by Ferry and standard transaction policy while the attacker can claim Zenon and later refund the Bitcoin.

**Only fixed template opcodes are compared** — `wasm/htlc.go:148-168`

Raw peer bytes are tokenized, but dynamic push opcodes are outside the fixed opcode checks.

```go
	for tk.Next() {
		if len(tokens) == contractTemplateLen {
			return nil, errors.New("script is longer than the atomic swap template")
		}
		tokens = append(tokens, scriptToken{op: tk.Opcode(), data: tk.Data()})
	}
	if err := tk.Err(); err != nil {
		return nil, fmt.Errorf("malformed script: %w", err)
	}
	if len(tokens) != contractTemplateLen {
		return nil, fmt.Errorf("script has %d elements, the atomic swap template has %d",
			len(tokens), contractTemplateLen)
	}
	for idx, op := range contractTemplate {
		if tokens[idx].op != op {
			return nil, fmt.Errorf("script does not match the atomic swap template at element %d", idx)
		}
	}

	secretSize, err := scriptNum(tokens[2], maxScriptNumLen)
	if err != nil {
```

**Dynamic pushes are validated by payload** — `wasm/htlc.go:179-199`

A nonminimal push preserves the expected hash and pubkey payload lengths and therefore survives parsing.

```go
	}
	pkhRedeem := tokens[9].data
	if len(pkhRedeem) != 20 {
		return nil, errors.New("redeem pubkey hash is not 20 bytes")
	}
	locktime, err := scriptNum(tokens[11], maxCLTVScriptNumLen)
	if err != nil {
		return nil, fmt.Errorf("bad locktime: %w", err)
	}
	// A contract whose locktime is not a timestamp is not one this tool can
	// reason about: every deadline it reports, and the refund it pre-signs,
	// assume the refund branch opens at a wall-clock moment.
	if err := validLockTime(locktime); err != nil {
		return nil, fmt.Errorf("contract %w", err)
	}
	pkhRefund := tokens[16].data
	if len(pkhRefund) != 20 {
		return nil, errors.New("refund pubkey hash is not 20 bytes")
	}

	return &ContractDetails{
```

**Parsed contract accepted under the agreed keys** — `wasm/manager.go:403-421`

Audit checks parsed identity and hash, so the alternate encoding can satisfy those checks.

```go
	details, err := ParseContract(contract)
	if err != nil {
		return nil, err
	}
	params, err := sw.Params()
	if err != nil {
		return nil, err
	}

	if !bytes.Equal(details.PkhRedeem, sw.Key.PKH) {
		return nil, fmt.Errorf("contract lets %s redeem, not this swap's key %s: "+
			"you would not be able to claim these funds",
			hex.EncodeToString(details.PkhRedeem), sw.Key.PKHHex())
	}
	if len(sw.SecretHash) > 0 && !bytes.Equal(details.SecretHash, sw.SecretHash) {
		return nil, fmt.Errorf("contract commits to secret hash %s, this swap uses %s",
			hex.EncodeToString(details.SecretHash), hex.EncodeToString(sw.SecretHash))
	}
	if len(sw.SecretHash) == 0 {
```

**Redeem executes with standard flags** — `wasm/txbuild.go:193-204`

Ferry rejects the spend when the nonminimal instruction is executed, after the peer may have committed its other leg.

```go
	// Execute the script locally before handing the transaction to anyone.
	// A swap failing at broadcast time is recoverable; one that fails silently
	// is not.
	engine, err := txscript.NewEngine(contractPkScript, tx, 0, txscript.StandardVerifyFlags,
		txscript.NewSigCache(10), txscript.NewTxSigHashes(tx, prevoutFetcher(contractPkScript, funding.Value)),
		funding.Value, prevoutFetcher(contractPkScript, funding.Value))
	if err != nil {
		return nil, fmt.Errorf("script engine: %w", err)
	}
	if err := engine.Execute(); err != nil {
		return nil, fmt.Errorf("built transaction does not satisfy the contract: %w", err)
	}
```

#### Reachability

The initiator supplies the contract through the normal manual or session handoff, and the malformed representation preserves the expected keys, hash and timelock.

- **Attacker:** A malicious Bitcoin initiator supplying the redeem script that a Zenon participant audits.

- **Entry point:** Only fixed template opcodes are compared

- **Outcome:** The victim's Bitcoin claim is blocked by Ferry and standard transaction policy while the attacker can claim Zenon and later refund the Bitcoin.

Limitations:
- The demonstrated claim is failure of Ferry's normal redeem and standard relay policy, not absolute Bitcoin consensus unspendability; a specially constructed nonstandard spend mined outside ordinary relay may be possible.

#### Severity

**High** — High impact and high likelihood. The initiator supplies the contract through the normal manual or session handoff, and the malformed representation preserves the expected keys, hash and timelock.

Additional runtime or deployment evidence could raise or lower this severity.

Impact assessment:
- **Level:** high
- **Why:** The victim's Bitcoin claim is blocked by Ferry and standard transaction policy while the attacker can claim Zenon and later refund the Bitcoin.

Likelihood assessment:
- **Level:** high
- **Why:** The initiator supplies the contract through the normal manual or session handoff, and the malformed representation preserves the expected keys, hash and timelock.

#### Remediation

Rebuild the canonical contract from parsed fields and require byte-for-byte equality, or enforce canonical push opcodes at every dynamic position. Add malformed-push cases for each branch and verify accepted scripts remain redeemable and refundable under StandardVerifyFlags.

Tests:
- Reject nonminimal OP_PUSHDATA1/2/4 encodings at every dynamic contract position during audit.
- Require audited scripts to equal the canonical rebuild and verify both branch spends under the same flags used by the signer.
- Ensure a rejected contract cannot enable Zenon Create or replace a previously accepted contract.

Preventive controls:
- Make the accepted contract representation canonical at the initial audit boundary and share it with the spend builder.

<a id="finding-2"></a>

### [2] Zenon funds can be locked against replaceable Bitcoin funding

| Field | Value |
| --- | --- |
| Severity | high |
| Confidence | high |
| Confidence rationale | Complete source-to-sink trace with identified controls and counterevidence; independent review corroborates the path. Dynamic end-to-end proof was not obtained. |
| Category | atomic-swap |
| CWE | CWE-841 |
| Affected lines | wasm/walletblock.go:235-239, ui/src/components/SwapCard.vue:242-247, ui/src/components/ZenonWallet.vue:251-267, wasm/walletblock.go:297-334 |

#### Summary

When Bitcoin initiates the swap, Ferry offers to lock the participant's Zenon funds as soon as an unconfirmed Bitcoin output appears. The initiator can replace that payment, then use the secret it already knows to collect the Zenon HTLC.

#### Root Cause

`zenonLegPossible` checks only the existence of a `Funding` record for Bitcoin-initiated swaps and explicitly accepts mempool funding. The resulting Zenon action reaches `planCreate()`, whose guard requires an audited script but never checks funding, confirmations, value, or whether the output remains unspent. Auto Mode can prepare and send that plan to the wallet.

**Funding existence enables the Zenon leg** — `ui/src/components/SwapCard.vue:242-247`

The initiating Bitcoin leg needs a funding object, but `confirmed` is not checked.

```typescript
const zenonLegPossible = computed(
  () =>
    !props.swap.btcLegIsInitiators ||
    Boolean(props.swap.funding) ||
    Boolean(props.swap.zenon?.htlcId),
)
```

**Automatic wallet preparation and signing** — `ui/src/components/ZenonWallet.vue:251-267`

Auto Mode progresses through the available action to prepare and sign.

```typescript
async function autoRun() {
  if (!props.auto || busy.value || walletSending.value || plan.value) return
  // Connecting is left to the user, and deliberately. Syrius approves a site in
  // a window of its own, and a page that asks for that on load — before anybody
  // has touched it, possibly for several swaps at once — is a page that gets
  // dismissed. Extensions tend to want a real gesture for it anyway. So Auto
  // Mode drives the steps and the one connection stays a button; the card goes
  // on offering it, and this picks up the moment it is done.
  if (!walletConnected.value) return
  if (!autoMode.claim(props.swap.id, `zenon:${props.action}`)) return
  await prepare()
  if (error.value || !plan.value) {
    autoMode.halt(props.swap.id, error.value || `could not build the ${props.action} block`)
    return
  }
  await sign()
  if (error.value) autoMode.halt(props.swap.id, error.value)
```

**Create requires a script but no safe funding** — `wasm/walletblock.go:224-240`

The engine checks ownership and contract presence without establishing that the peer's Bitcoin is committed.

```go
	if !sw.ZenonHtlcIsOurs() {
		return errors.New("the counterparty sends ZNN on this swap, so they create the Zenon " +
			"HTLC. There is nothing for you to create")
	}
	if sw.Zenon.HtlcID != "" {
		return fmt.Errorf("this swap already has a Zenon HTLC (%s). Creating a second one would "+
			"lock a second lot of ZNN that only expiry can return", sw.Zenon.HtlcID)
	}
	// The counterparty's Bitcoin contract is what the Zenon expiry has to be
	// ordered against, and it is also the thing that has to be audited before
	// any ZNN moves. Both are the same precondition.
	if len(sw.Contract) == 0 {
		return errors.New("the Bitcoin contract has not been audited yet. Audit it first: the " +
			"Zenon expiry is computed from its locktime, and locking ZNN against a contract you " +
			"have not checked is the one move this app will not help you make")
	}

```

**Build the funded Zenon account block** — `wasm/walletblock.go:297-334`

The agreed Zenon amount is encoded into the Create plan after the missing Bitcoin guard.

```go
	}
	if amount.Sign() <= 0 {
		return fmt.Errorf("the agreed amount %q is not a positive number", sw.Zenon.AmountDisplay)
	}

	// hashType 1 is SHA-256, which is what Bitcoin's OP_SHA256 requires. The
	// other value the contract takes is SHA3, and an HTLC created with it is
	// one the Bitcoin side can never produce a matching preimage for.
	data, err := znn.PackCreate(hashLocked, expiration, znn.HashTypeSHA256, SecretSize, sw.SecretHash)
	if err != nil {
		return err
	}

	// The counterparty will settle this leg by unlocking it, and the contract
	// pays hashLocked rather than the caller — which is what lets them do it
	// from a wallet with no HTLC support. Unless they have opted out of that,
	// in which case they need to unlock from the payee account itself, and it
	// is worth knowing now rather than at settlement.
	if allowed, perr := mgr.Znn.ProxyUnlockAllowed(ctx, peer); perr == nil && !allowed {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"%s has denied proxy unlock, so only that account itself can unlock this HTLC. "+
				"Make sure the counterparty can sign from it.", peer))
	}

	b := plan.Block
	b.Amount = amount.String()
	b.TokenStandard = token
	b.Data = base64.StdEncoding.EncodeToString(data)

	plan.HashLocked = peer
	plan.ExpirationTime = expiration
	plan.ExpiresAt = utcTime(expiration)
	plan.AmountDisplay = sw.Zenon.AmountDisplay
	plan.TokenSymbol = tok.Symbol
	plan.Summary = fmt.Sprintf("Lock %s %s to %s until %s, redeemable with the preimage of %s.",
		sw.Zenon.AmountDisplay, tok.Symbol, peer, plan.ExpiresAt,
		hex.EncodeToString(sw.SecretHash))
	return nil
```

#### Validation

The parent traced `zenonLegPossible` through the create action and `ZenonWallet.autoRun()` to `planCreate()`. All of `planCreate()` was inspected: address, token, positive amount, expiry ordering, and wallet-chain checks are present, but Bitcoin funding safety is absent. Existing devnet test setup also intentionally creates a participant HTLC with no funding; source evidence only, not an executed test.

Validation method: static source trace

**Funding existence enables the Zenon leg** — `ui/src/components/SwapCard.vue:242-247`

The initiating Bitcoin leg needs a funding object, but `confirmed` is not checked.

```typescript
const zenonLegPossible = computed(
  () =>
    !props.swap.btcLegIsInitiators ||
    Boolean(props.swap.funding) ||
    Boolean(props.swap.zenon?.htlcId),
)
```

**Automatic wallet preparation and signing** — `ui/src/components/ZenonWallet.vue:251-267`

Auto Mode progresses through the available action to prepare and sign.

```typescript
async function autoRun() {
  if (!props.auto || busy.value || walletSending.value || plan.value) return
  // Connecting is left to the user, and deliberately. Syrius approves a site in
  // a window of its own, and a page that asks for that on load — before anybody
  // has touched it, possibly for several swaps at once — is a page that gets
  // dismissed. Extensions tend to want a real gesture for it anyway. So Auto
  // Mode drives the steps and the one connection stays a button; the card goes
  // on offering it, and this picks up the moment it is done.
  if (!walletConnected.value) return
  if (!autoMode.claim(props.swap.id, `zenon:${props.action}`)) return
  await prepare()
  if (error.value || !plan.value) {
    autoMode.halt(props.swap.id, error.value || `could not build the ${props.action} block`)
    return
  }
  await sign()
  if (error.value) autoMode.halt(props.swap.id, error.value)
```

**Create requires a script but no safe funding** — `wasm/walletblock.go:224-240`

The engine checks ownership and contract presence without establishing that the peer's Bitcoin is committed.

```go
	if !sw.ZenonHtlcIsOurs() {
		return errors.New("the counterparty sends ZNN on this swap, so they create the Zenon " +
			"HTLC. There is nothing for you to create")
	}
	if sw.Zenon.HtlcID != "" {
		return fmt.Errorf("this swap already has a Zenon HTLC (%s). Creating a second one would "+
			"lock a second lot of ZNN that only expiry can return", sw.Zenon.HtlcID)
	}
	// The counterparty's Bitcoin contract is what the Zenon expiry has to be
	// ordered against, and it is also the thing that has to be audited before
	// any ZNN moves. Both are the same precondition.
	if len(sw.Contract) == 0 {
		return errors.New("the Bitcoin contract has not been audited yet. Audit it first: the " +
			"Zenon expiry is computed from its locktime, and locking ZNN against a contract you " +
			"have not checked is the one move this app will not help you make")
	}

```

**Build the funded Zenon account block** — `wasm/walletblock.go:297-334`

The agreed Zenon amount is encoded into the Create plan after the missing Bitcoin guard.

```go
	}
	if amount.Sign() <= 0 {
		return fmt.Errorf("the agreed amount %q is not a positive number", sw.Zenon.AmountDisplay)
	}

	// hashType 1 is SHA-256, which is what Bitcoin's OP_SHA256 requires. The
	// other value the contract takes is SHA3, and an HTLC created with it is
	// one the Bitcoin side can never produce a matching preimage for.
	data, err := znn.PackCreate(hashLocked, expiration, znn.HashTypeSHA256, SecretSize, sw.SecretHash)
	if err != nil {
		return err
	}

	// The counterparty will settle this leg by unlocking it, and the contract
	// pays hashLocked rather than the caller — which is what lets them do it
	// from a wallet with no HTLC support. Unless they have opted out of that,
	// in which case they need to unlock from the payee account itself, and it
	// is worth knowing now rather than at settlement.
	if allowed, perr := mgr.Znn.ProxyUnlockAllowed(ctx, peer); perr == nil && !allowed {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"%s has denied proxy unlock, so only that account itself can unlock this HTLC. "+
				"Make sure the counterparty can sign from it.", peer))
	}

	b := plan.Block
	b.Amount = amount.String()
	b.TokenStandard = token
	b.Data = base64.StdEncoding.EncodeToString(data)

	plan.HashLocked = peer
	plan.ExpirationTime = expiration
	plan.ExpiresAt = utcTime(expiration)
	plan.AmountDisplay = sw.Zenon.AmountDisplay
	plan.TokenSymbol = tok.Symbol
	plan.Summary = fmt.Sprintf("Lock %s %s to %s until %s, redeemable with the preimage of %s.",
		sw.Zenon.AmountDisplay, tok.Symbol, peer, plan.ExpiresAt,
		hex.EncodeToString(sw.SecretHash))
	return nil
```

Assertions:
- The participant must not commit its Zenon payment while the initiating Bitcoin payment can be withdrawn by a routine double-spend.
- The attacker keeps its Bitcoin and collects the victim's Zenon payment.

Limitations:
- No live wallet or chain exploit was run; supported Go build and isolated regressions were blocked by toolchain and offline dependency availability.

#### Dataflow

attacker-controlled unconfirmed Bitcoin HTLC output → wallet signing and publication of the participant's Zenon Create. The attacker keeps its Bitcoin and collects the victim's Zenon payment.

- **Source:** attacker-controlled unconfirmed Bitcoin HTLC output

- **Sink:** wallet signing and publication of the participant's Zenon Create

- **Outcome:** The attacker keeps its Bitcoin and collects the victim's Zenon payment.

**Funding existence enables the Zenon leg** — `ui/src/components/SwapCard.vue:242-247`

The initiating Bitcoin leg needs a funding object, but `confirmed` is not checked.

```typescript
const zenonLegPossible = computed(
  () =>
    !props.swap.btcLegIsInitiators ||
    Boolean(props.swap.funding) ||
    Boolean(props.swap.zenon?.htlcId),
)
```

**Automatic wallet preparation and signing** — `ui/src/components/ZenonWallet.vue:251-267`

Auto Mode progresses through the available action to prepare and sign.

```typescript
async function autoRun() {
  if (!props.auto || busy.value || walletSending.value || plan.value) return
  // Connecting is left to the user, and deliberately. Syrius approves a site in
  // a window of its own, and a page that asks for that on load — before anybody
  // has touched it, possibly for several swaps at once — is a page that gets
  // dismissed. Extensions tend to want a real gesture for it anyway. So Auto
  // Mode drives the steps and the one connection stays a button; the card goes
  // on offering it, and this picks up the moment it is done.
  if (!walletConnected.value) return
  if (!autoMode.claim(props.swap.id, `zenon:${props.action}`)) return
  await prepare()
  if (error.value || !plan.value) {
    autoMode.halt(props.swap.id, error.value || `could not build the ${props.action} block`)
    return
  }
  await sign()
  if (error.value) autoMode.halt(props.swap.id, error.value)
```

**Create requires a script but no safe funding** — `wasm/walletblock.go:224-240`

The engine checks ownership and contract presence without establishing that the peer's Bitcoin is committed.

```go
	if !sw.ZenonHtlcIsOurs() {
		return errors.New("the counterparty sends ZNN on this swap, so they create the Zenon " +
			"HTLC. There is nothing for you to create")
	}
	if sw.Zenon.HtlcID != "" {
		return fmt.Errorf("this swap already has a Zenon HTLC (%s). Creating a second one would "+
			"lock a second lot of ZNN that only expiry can return", sw.Zenon.HtlcID)
	}
	// The counterparty's Bitcoin contract is what the Zenon expiry has to be
	// ordered against, and it is also the thing that has to be audited before
	// any ZNN moves. Both are the same precondition.
	if len(sw.Contract) == 0 {
		return errors.New("the Bitcoin contract has not been audited yet. Audit it first: the " +
			"Zenon expiry is computed from its locktime, and locking ZNN against a contract you " +
			"have not checked is the one move this app will not help you make")
	}

```

**Build the funded Zenon account block** — `wasm/walletblock.go:297-334`

The agreed Zenon amount is encoded into the Create plan after the missing Bitcoin guard.

```go
	}
	if amount.Sign() <= 0 {
		return fmt.Errorf("the agreed amount %q is not a positive number", sw.Zenon.AmountDisplay)
	}

	// hashType 1 is SHA-256, which is what Bitcoin's OP_SHA256 requires. The
	// other value the contract takes is SHA3, and an HTLC created with it is
	// one the Bitcoin side can never produce a matching preimage for.
	data, err := znn.PackCreate(hashLocked, expiration, znn.HashTypeSHA256, SecretSize, sw.SecretHash)
	if err != nil {
		return err
	}

	// The counterparty will settle this leg by unlocking it, and the contract
	// pays hashLocked rather than the caller — which is what lets them do it
	// from a wallet with no HTLC support. Unless they have opted out of that,
	// in which case they need to unlock from the payee account itself, and it
	// is worth knowing now rather than at settlement.
	if allowed, perr := mgr.Znn.ProxyUnlockAllowed(ctx, peer); perr == nil && !allowed {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"%s has denied proxy unlock, so only that account itself can unlock this HTLC. "+
				"Make sure the counterparty can sign from it.", peer))
	}

	b := plan.Block
	b.Amount = amount.String()
	b.TokenStandard = token
	b.Data = base64.StdEncoding.EncodeToString(data)

	plan.HashLocked = peer
	plan.ExpirationTime = expiration
	plan.ExpiresAt = utcTime(expiration)
	plan.AmountDisplay = sw.Zenon.AmountDisplay
	plan.TokenSymbol = tok.Symbol
	plan.Summary = fmt.Sprintf("Lock %s %s to %s until %s, redeemable with the preimage of %s.",
		sw.Zenon.AmountDisplay, tok.Symbol, peer, plan.ExpiresAt,
		hex.EncodeToString(sw.SecretHash))
	return nil
```

#### Reachability

Replacing one's own unconfirmed funding is within the malicious initiator's ordinary capability; the supported wallet flow exposes the unsafe next step without defeating authentication.

- **Attacker:** A malicious Bitcoin initiator who controls the funding transaction and its replacement.

- **Entry point:** Funding existence enables the Zenon leg

- **Outcome:** The attacker keeps its Bitcoin and collects the victim's Zenon payment.

#### Severity

**High** — High impact and high likelihood. Replacing one's own unconfirmed funding is within the malicious initiator's ordinary capability; the supported wallet flow exposes the unsafe next step without defeating authentication.

Additional runtime or deployment evidence could raise or lower this severity.

Impact assessment:
- **Level:** high
- **Why:** The attacker keeps its Bitcoin and collects the victim's Zenon payment.

Likelihood assessment:
- **Level:** high
- **Why:** Replacing one's own unconfirmed funding is within the malicious initiator's ordinary capability; the supported wallet flow exposes the unsafe next step without defeating authentication.

#### Remediation

When Bitcoin is the initiating leg, require current, fully funded, sufficiently confirmed and unspent Bitcoin funding in the core before creating the Zenon HTLC. Recheck before submitting a prepared create, and make Auto Mode wait until the condition holds.

Tests:
- For Bitcoin-initiated swaps, reject Zenon Create with missing, unconfirmed, underfunded, spent, or mismatched Bitcoin funding.
- Revalidate immediately before wallet dispatch and invalidate plans when funding changes.
- Verify the Zenon-initiated ordering remains able to create its first leg without Bitcoin funding.

Preventive controls:
- Require a current role-aware Bitcoin funding proof at the engine's counter-leg commitment boundary.

<a id="finding-3"></a>

### [3] Auto Mode reveals the secret for an underfunded Bitcoin payment

| Field | Value |
| --- | --- |
| Severity | high |
| Confidence | high |
| Confidence rationale | Complete source-to-sink trace with identified controls and counterevidence; independent review corroborates the path. Dynamic end-to-end proof was not obtained. |
| Category | atomic-swap |
| CWE | CWE-841 |
| Affected lines | wasm/manager.go:945-951, wasm/manager.go:533-574, ui/src/components/SwapCard.vue:327-332, ui/src/components/SwapCard.vue:737-744 |

#### Summary

A Bitcoin participant can pay less than the agreed amount and still make a Zenon initiator's Auto Mode redeem. The redeem publishes the secret, allowing the participant to take the full Zenon payment.

#### Root Cause

`Refresh()` reads the peer-funded contract's UTXOs and stores an underpayment as `Funding` and `StateFunded`; it only logs the mismatch with `AmountSats`. `canRedeem` treats any funding record as sufficient, and `autoStep()` adds confirmation but no amount check. `Manager.Redeem()` then signs and broadcasts with the secret without enforcing the agreed consideration.

**Underpayment becomes funded state** — `wasm/manager.go:533-574`

The peer chooses the output value; the selected short payment becomes `Funding` before its mismatch is merely logged.

```go
	if fundingIsOpen(sw) {
		utxos, err := backend.AddressUTXOs(ctx, sw.ContractAddr)
		if err != nil {
			return nil, fmt.Errorf("look up contract address: %w", err)
		}
		best := pickFunding(utxos, sw.AmountSats)
		changed := best != nil && (sw.Funding == nil || best.Value > sw.Funding.Value)
		if changed {
			if sw.Funding == nil {
				sw.State = StateFunded
				sw.log("funding seen: %s:%d for %d sat (confirmed=%v)",
					best.TxID, best.Vout, best.Value, best.Status.Confirmed)
			} else {
				sw.log("a larger output appeared at the contract address: switching funding from "+
					"%s:%d (%d sat) to %s:%d (%d sat) and re-signing the refund",
					sw.Funding.TxID, sw.Funding.Vout, sw.Funding.Value,
					best.TxID, best.Vout, best.Value)
				sw.RefundTx = nil // it spent the output we just stopped using
			}
			sw.Funding = &FundingOutput{
				TxID:  best.TxID,
				Vout:  best.Vout,
				Value: best.Value,
				// The listing already carries the block, so the first depth is
				// free. Height alone is not depth -- that needs the tip, which
				// trackFundingDepth below fetches.
				Confirmed:   best.Status.Confirmed,
				BlockHeight: best.Status.BlockHeight,
			}

			// Only on a change: this block runs on every refresh while the
			// contract is short, and repeating these lines would bury the
			// event log in copies of themselves.
			if len(utxos) > 1 {
				sw.log("NOTE: the contract address holds %d unspent outputs. Only the one above is "+
					"spent by a redeem or refund; anything else must be recovered separately with "+
					"`ferry recover` after editing the funding outpoint.", len(utxos))
			}
			if sw.Funding.Value < sw.AmountSats {
				sw.log("WARNING: contract holds %d sat but %d sat was agreed; do not proceed until "+
					"this is resolved", sw.Funding.Value, sw.AmountSats)
			}
```

**Any funding enables redeem** — `ui/src/components/SwapCard.vue:327-332`

The UI checks existence of funding and a secret, so the short-payment flag does not gate redeem.

```typescript
const canRedeem = computed(
  () =>
    props.swap.leg === 'receive' &&
    Boolean(props.swap.funding) &&
    Boolean(props.swap.secretHex) &&
    props.swap.state !== 'redeemed',
```

**Auto Mode dispatches after confirmation** — `ui/src/components/SwapCard.vue:737-744`

Confirmation is required, but the amount is not compared before `api.redeem()`.

```typescript
  if (canRedeem.value && destForSpend.value && props.swap.funding?.confirmed) {
    if (!autoMode.claim(props.swap.id, 'redeem')) return
    await runAndTell('redeemed the Bitcoin contract, which publishes the preimage on Bitcoin', () =>
      api.redeem(props.swap.id, destForSpend.value, settings.value),
    )
    if (error.value) autoMode.halt(props.swap.id, error.value)
  }
}
```

**Core signs and broadcasts without amount check** — `wasm/manager.go:944-974`

The core accepts any existing funding and publishes a transaction carrying the secret.

```go
	}
	if sw.Funding == nil {
		return nil, errors.New("no funding output has been seen yet; refresh first")
	}
	if len(sw.Secret) == 0 {
		return nil, errors.New("the secret is not known yet, so this contract cannot be redeemed")
	}
	destAddr, err = payoutAddress(sw, destAddr)
	if err != nil {
		return nil, err
	}
	if now := time.Now().Unix(); now >= sw.LockTime {
		sw.log("WARNING: redeeming after the timelock at %s. The counterparty can broadcast "+
			"their refund now, so this is a race -- if it loses, treat the Bitcoin leg as gone.",
			time.Unix(sw.LockTime, 0).UTC().Format(time.RFC3339))
	}
	params, err := sw.Params()
	if err != nil {
		return nil, err
	}
	feeRate, err := backend.FeeRate(ctx, 3)
	if err != nil {
		feeRate = 2.0
	}
	spend, err := BuildRedeem(sw.Contract, *sw.Funding, sw.Key, sw.Secret, destAddr, feeRate, params)
	if err != nil {
		return nil, err
	}
	txid, err := backend.Broadcast(ctx, spend.RawHex)
	if err != nil {
		return nil, fmt.Errorf("broadcast redeem: %w", err)
```

#### Validation

The parent followed the selected UTXO through `Refresh()`, the automatic UI action, and `Manager.Redeem()` to `BuildRedeem()` and `Broadcast()`. A confirmed output below `AmountSats` but above the fee and dust floor reaches the secret-bearing transaction. The visible shortfall warning and continued search for a larger output do not stop this action.

Validation method: static source trace

**Underpayment becomes funded state** — `wasm/manager.go:533-574`

The peer chooses the output value; the selected short payment becomes `Funding` before its mismatch is merely logged.

```go
	if fundingIsOpen(sw) {
		utxos, err := backend.AddressUTXOs(ctx, sw.ContractAddr)
		if err != nil {
			return nil, fmt.Errorf("look up contract address: %w", err)
		}
		best := pickFunding(utxos, sw.AmountSats)
		changed := best != nil && (sw.Funding == nil || best.Value > sw.Funding.Value)
		if changed {
			if sw.Funding == nil {
				sw.State = StateFunded
				sw.log("funding seen: %s:%d for %d sat (confirmed=%v)",
					best.TxID, best.Vout, best.Value, best.Status.Confirmed)
			} else {
				sw.log("a larger output appeared at the contract address: switching funding from "+
					"%s:%d (%d sat) to %s:%d (%d sat) and re-signing the refund",
					sw.Funding.TxID, sw.Funding.Vout, sw.Funding.Value,
					best.TxID, best.Vout, best.Value)
				sw.RefundTx = nil // it spent the output we just stopped using
			}
			sw.Funding = &FundingOutput{
				TxID:  best.TxID,
				Vout:  best.Vout,
				Value: best.Value,
				// The listing already carries the block, so the first depth is
				// free. Height alone is not depth -- that needs the tip, which
				// trackFundingDepth below fetches.
				Confirmed:   best.Status.Confirmed,
				BlockHeight: best.Status.BlockHeight,
			}

			// Only on a change: this block runs on every refresh while the
			// contract is short, and repeating these lines would bury the
			// event log in copies of themselves.
			if len(utxos) > 1 {
				sw.log("NOTE: the contract address holds %d unspent outputs. Only the one above is "+
					"spent by a redeem or refund; anything else must be recovered separately with "+
					"`ferry recover` after editing the funding outpoint.", len(utxos))
			}
			if sw.Funding.Value < sw.AmountSats {
				sw.log("WARNING: contract holds %d sat but %d sat was agreed; do not proceed until "+
					"this is resolved", sw.Funding.Value, sw.AmountSats)
			}
```

**Any funding enables redeem** — `ui/src/components/SwapCard.vue:327-332`

The UI checks existence of funding and a secret, so the short-payment flag does not gate redeem.

```typescript
const canRedeem = computed(
  () =>
    props.swap.leg === 'receive' &&
    Boolean(props.swap.funding) &&
    Boolean(props.swap.secretHex) &&
    props.swap.state !== 'redeemed',
```

**Auto Mode dispatches after confirmation** — `ui/src/components/SwapCard.vue:737-744`

Confirmation is required, but the amount is not compared before `api.redeem()`.

```typescript
  if (canRedeem.value && destForSpend.value && props.swap.funding?.confirmed) {
    if (!autoMode.claim(props.swap.id, 'redeem')) return
    await runAndTell('redeemed the Bitcoin contract, which publishes the preimage on Bitcoin', () =>
      api.redeem(props.swap.id, destForSpend.value, settings.value),
    )
    if (error.value) autoMode.halt(props.swap.id, error.value)
  }
}
```

**Core signs and broadcasts without amount check** — `wasm/manager.go:944-974`

The core accepts any existing funding and publishes a transaction carrying the secret.

```go
	}
	if sw.Funding == nil {
		return nil, errors.New("no funding output has been seen yet; refresh first")
	}
	if len(sw.Secret) == 0 {
		return nil, errors.New("the secret is not known yet, so this contract cannot be redeemed")
	}
	destAddr, err = payoutAddress(sw, destAddr)
	if err != nil {
		return nil, err
	}
	if now := time.Now().Unix(); now >= sw.LockTime {
		sw.log("WARNING: redeeming after the timelock at %s. The counterparty can broadcast "+
			"their refund now, so this is a race -- if it loses, treat the Bitcoin leg as gone.",
			time.Unix(sw.LockTime, 0).UTC().Format(time.RFC3339))
	}
	params, err := sw.Params()
	if err != nil {
		return nil, err
	}
	feeRate, err := backend.FeeRate(ctx, 3)
	if err != nil {
		feeRate = 2.0
	}
	spend, err := BuildRedeem(sw.Contract, *sw.Funding, sw.Key, sw.Secret, destAddr, feeRate, params)
	if err != nil {
		return nil, err
	}
	txid, err := backend.Broadcast(ctx, spend.RawHex)
	if err != nil {
		return nil, fmt.Errorf("broadcast redeem: %w", err)
```

Assertions:
- The initiator must not release the preimage until the participant has committed the agreed Bitcoin amount.
- The victim receives a small Bitcoin payment while the attacker collects the full agreed Zenon payment.

Limitations:
- No live wallet or chain exploit was run; supported Go build and isolated regressions were blocked by toolchain and offline dependency availability.

#### Dataflow

attacker-selected Bitcoin output value → Bitcoin redeem broadcast containing the initiator's preimage. The victim receives a small Bitcoin payment while the attacker collects the full agreed Zenon payment.

- **Source:** attacker-selected Bitcoin output value

- **Sink:** Bitcoin redeem broadcast containing the initiator's preimage

- **Outcome:** The victim receives a small Bitcoin payment while the attacker collects the full agreed Zenon payment.

**Underpayment becomes funded state** — `wasm/manager.go:533-574`

The peer chooses the output value; the selected short payment becomes `Funding` before its mismatch is merely logged.

```go
	if fundingIsOpen(sw) {
		utxos, err := backend.AddressUTXOs(ctx, sw.ContractAddr)
		if err != nil {
			return nil, fmt.Errorf("look up contract address: %w", err)
		}
		best := pickFunding(utxos, sw.AmountSats)
		changed := best != nil && (sw.Funding == nil || best.Value > sw.Funding.Value)
		if changed {
			if sw.Funding == nil {
				sw.State = StateFunded
				sw.log("funding seen: %s:%d for %d sat (confirmed=%v)",
					best.TxID, best.Vout, best.Value, best.Status.Confirmed)
			} else {
				sw.log("a larger output appeared at the contract address: switching funding from "+
					"%s:%d (%d sat) to %s:%d (%d sat) and re-signing the refund",
					sw.Funding.TxID, sw.Funding.Vout, sw.Funding.Value,
					best.TxID, best.Vout, best.Value)
				sw.RefundTx = nil // it spent the output we just stopped using
			}
			sw.Funding = &FundingOutput{
				TxID:  best.TxID,
				Vout:  best.Vout,
				Value: best.Value,
				// The listing already carries the block, so the first depth is
				// free. Height alone is not depth -- that needs the tip, which
				// trackFundingDepth below fetches.
				Confirmed:   best.Status.Confirmed,
				BlockHeight: best.Status.BlockHeight,
			}

			// Only on a change: this block runs on every refresh while the
			// contract is short, and repeating these lines would bury the
			// event log in copies of themselves.
			if len(utxos) > 1 {
				sw.log("NOTE: the contract address holds %d unspent outputs. Only the one above is "+
					"spent by a redeem or refund; anything else must be recovered separately with "+
					"`ferry recover` after editing the funding outpoint.", len(utxos))
			}
			if sw.Funding.Value < sw.AmountSats {
				sw.log("WARNING: contract holds %d sat but %d sat was agreed; do not proceed until "+
					"this is resolved", sw.Funding.Value, sw.AmountSats)
			}
```

**Any funding enables redeem** — `ui/src/components/SwapCard.vue:327-332`

The UI checks existence of funding and a secret, so the short-payment flag does not gate redeem.

```typescript
const canRedeem = computed(
  () =>
    props.swap.leg === 'receive' &&
    Boolean(props.swap.funding) &&
    Boolean(props.swap.secretHex) &&
    props.swap.state !== 'redeemed',
```

**Auto Mode dispatches after confirmation** — `ui/src/components/SwapCard.vue:737-744`

Confirmation is required, but the amount is not compared before `api.redeem()`.

```typescript
  if (canRedeem.value && destForSpend.value && props.swap.funding?.confirmed) {
    if (!autoMode.claim(props.swap.id, 'redeem')) return
    await runAndTell('redeemed the Bitcoin contract, which publishes the preimage on Bitcoin', () =>
      api.redeem(props.swap.id, destForSpend.value, settings.value),
    )
    if (error.value) autoMode.halt(props.swap.id, error.value)
  }
}
```

**Core signs and broadcasts without amount check** — `wasm/manager.go:944-974`

The core accepts any existing funding and publishes a transaction carrying the secret.

```go
	}
	if sw.Funding == nil {
		return nil, errors.New("no funding output has been seen yet; refresh first")
	}
	if len(sw.Secret) == 0 {
		return nil, errors.New("the secret is not known yet, so this contract cannot be redeemed")
	}
	destAddr, err = payoutAddress(sw, destAddr)
	if err != nil {
		return nil, err
	}
	if now := time.Now().Unix(); now >= sw.LockTime {
		sw.log("WARNING: redeeming after the timelock at %s. The counterparty can broadcast "+
			"their refund now, so this is a race -- if it loses, treat the Bitcoin leg as gone.",
			time.Unix(sw.LockTime, 0).UTC().Format(time.RFC3339))
	}
	params, err := sw.Params()
	if err != nil {
		return nil, err
	}
	feeRate, err := backend.FeeRate(ctx, 3)
	if err != nil {
		feeRate = 2.0
	}
	spend, err := BuildRedeem(sw.Contract, *sw.Funding, sw.Key, sw.Secret, destAddr, feeRate, params)
	if err != nil {
		return nil, err
	}
	txid, err := backend.Broadcast(ctx, spend.RawHex)
	if err != nil {
		return nil, fmt.Errorf("broadcast redeem: %w", err)
```

#### Reachability

A malicious trading counterparty controls its payment value; the normal Auto Mode path advances on a confirmed underpayment with no extra compromise required.

- **Attacker:** A malicious Bitcoin participant trading with a Zenon initiator who has enabled Auto Mode.

- **Entry point:** Underpayment becomes funded state

- **Outcome:** The victim receives a small Bitcoin payment while the attacker collects the full agreed Zenon payment.

#### Severity

**High** — High impact and high likelihood. A malicious trading counterparty controls its payment value; the normal Auto Mode path advances on a confirmed underpayment with no extra compromise required.

Additional runtime or deployment evidence could raise or lower this severity.

Impact assessment:
- **Level:** high
- **Why:** The victim receives a small Bitcoin payment while the attacker collects the full agreed Zenon payment.

Likelihood assessment:
- **Level:** high
- **Why:** A malicious trading counterparty controls its payment value; the normal Auto Mode path advances on a confirmed underpayment with no extra compromise required.

#### Remediation

Enforce Funding.Value \>= AmountSats in the core before any first preimage disclosure or counter-leg commitment. Make Auto Mode wait on that invariant; preserve an explicitly separate recovery path for reclaiming a partial payment without silently releasing an unrevealed secret.

Tests:
- With Auto Mode on, a confirmed spendable underpayment must not produce a redeem or reveal the initiator's secret.
- Check the same invariant through the direct WASM redeem API, including stale or replaced funding.
- A fully funded, confirmed swap must still complete in both role/direction combinations; preserve explicit recovery when the secret is already public.

Preventive controls:
- Own the first-secret-disclosure guard inside the Go engine, with fresh confirmed funding, minimum agreed value, contract binding, and safe time remaining.

<a id="finding-4"></a>

### [4] Missing Zenon terms can verify an HTLC that pays the attacker

| Field | Value |
| --- | --- |
| Severity | high |
| Confidence | high |
| Confidence rationale | Complete source-to-sink trace with identified controls and counterevidence; independent review corroborates the path. Dynamic end-to-end proof was not obtained. |
| Category | atomic-swap |
| CWE | CWE-754 |
| Affected lines | wasm/znn/client.go:450-453, wasm/manager.go:220-237, wasm/manager.go:1074-1082, wasm/manager.go:1120-1133, wasm/manager.go:1225-1244, wasm/walletblock.go:356-383, wasm/walletsync.go:283-301 |

#### Summary

Leaving the optional own Zenon address blank disables the recipient comparison. Ferry can mark an attacker-payable HTLC as verified and send an Unlock containing the victim's secret. A missing agreed amount similarly skips the amount comparison.

#### Root Cause

`Manager.Create()` accepts empty Zenon addresses and stores optional amount text. For an incoming HTLC, `zenonVerifyParams()` copies `SelfAddress` into `ExpectRecipient`; `VerifyHtlc()` runs that comparison only when nonempty. `zenonExpectations()` leaves `MinAmount` unset for a blank amount, which also disables comparison. `VerifyZenon()` can persist `Verified`, and `planUnlock()` relies on it to encode the preimage; the wallet sync path also returns without binding an empty payout address.

**Empty Zenon addresses accepted** — `wasm/manager.go:220-237`

Only nonempty addresses are parsed, allowing a draft with no own payout identity.

```go
	// The three Zenon fields are all optional here -- the addresses are routinely
	// filled in later and a blank token means ZNN -- but a value that IS given has
	// to be the kind of thing the field asks for. A Bitcoin address, a Zenon
	// address and a Zenon token standard are all bech32, and the last two start
	// with the same letter, which is exactly the shape of accident a paste between
	// similar-looking fields produces.
	zenonSelf := strings.TrimSpace(p.ZenonSelfAddress)
	if zenonSelf != "" {
		if _, err := znn.ParseAddress(zenonSelf); err != nil {
			return nil, fmt.Errorf("your Zenon address: %w", err)
		}
	}
	zenonPeer := strings.TrimSpace(p.ZenonPeerAddress)
	if zenonPeer != "" {
		if _, err := znn.ParseAddress(zenonPeer); err != nil {
			return nil, fmt.Errorf("their Zenon address: %w", err)
		}
	}
```

**Copy optional address into verification** — `wasm/manager.go:1074-1082`

For the incoming leg the blank own address becomes a blank expected recipient.

```go
	// Checking "recipient is me" against an HTLC this user created themselves
	// would fail every time, and would skip the check that actually matters
	// there: that it pays the counterparty.
	if sw.ZenonHtlcIsOurs() {
		want.ExpectRecipient = sw.Zenon.PeerAddress
		want.ExpectSender = sw.Zenon.SelfAddress
	} else {
		want.ExpectRecipient = sw.Zenon.SelfAddress
		want.ExpectSender = sw.Zenon.PeerAddress
```

**Blank amount leaves no lower bound** — `wasm/manager.go:1120-1133`

The minimum amount is computed only for nonempty agreed text.

```go
	if sw.Zenon.AmountDisplay != "" {
		agreed := sw.Zenon.AgreedToken()
		if tok, terr := m.Znn.GetToken(ctx, agreed); terr != nil {
			want.AmountUncheckable = fmt.Sprintf("could not read token %s from the node: %v",
				agreed, terr)
		} else if amt, cerr := decimalToBaseUnits(sw.Zenon.AmountDisplay, tok.Decimals); cerr != nil {
			want.AmountUncheckable = fmt.Sprintf("the agreed amount %q could not be interpreted: %v",
				sw.Zenon.AmountDisplay, cerr)
		} else {
			want.MinAmount = amt
		}
	}
	return want
}
```

**Absent expectations skip comparisons** — `wasm/znn/client.go:450-473`

An empty expected payee or nil amount is treated as a disabled check rather than incomplete verification.

```go
	if want.ExpectRecipient != "" && !strings.EqualFold(info.HashLocked, want.ExpectRecipient) {
		problems = append(problems, fmt.Sprintf(
			"hashLocked address is %s, expected %s", info.HashLocked, want.ExpectRecipient))
	}
	if want.ExpectSender != "" && !strings.EqualFold(info.TimeLocked, want.ExpectSender) {
		problems = append(problems, fmt.Sprintf(
			"timeLocked address is %s, expected %s", info.TimeLocked, want.ExpectSender))
	}
	if want.AmountUncheckable != "" {
		problems = append(problems, fmt.Sprintf(
			"the agreed amount could not be checked (%s). A skipped check reads exactly like a "+
				"passed one, so this is a refusal: fix the node or the agreed amount and verify again",
			want.AmountUncheckable))
		incomplete++
	} else if want.MinAmount != nil {
		amt, ok := new(big.Int).SetString(info.Amount, 10)
		if !ok {
			problems = append(problems, fmt.Sprintf("amount %q is not a number", info.Amount))
		} else if amt.Cmp(want.MinAmount) < 0 {
			problems = append(problems, fmt.Sprintf(
				"amount %s is below the agreed %s", amt.String(), want.MinAmount.String()))
		}
	}
	if want.Now > 0 && want.MinRemaining > 0 {
```

**Persist successful verification** — `wasm/manager.go:1225-1244`

If no other term fails, the swap receives a verified verdict despite the skipped terms.

```go
	sw.Zenon.VerifyPending = verr != nil && znn.CheckIncomplete(verr)
	if verr != nil {
		sw.Zenon.Verified = false
		sw.Zenon.VerifyError = verr.Error()
		if !repeat {
			sw.log("Zenon HTLC %s FAILED verification: %v", htlcID, verr)
		}
	} else {
		sw.Zenon.Verified = true
		sw.Zenon.VerifyError = ""
		if !repeat {
			sw.log("Zenon HTLC %s verified: hashlock, parties, amount and expiry all match (it is the %s leg)",
				htlcID, legOwner(sw.ZenonLegIsInitiators()))
		}
	}
	if err := m.Store.Save(sw); err != nil {
		return nil, nil, err
	}
	return sw, info, verr
}
```

**Verified status permits preimage encoding** — `wasm/walletblock.go:356-383`

The prior verdict authorizes `PackUnlock`; the summary even falls back to the entry's recipient when no own address is stored.

```go
	// Verification is not a formality here. Unlocking publishes the preimage,
	// which is the one secret that keeps both legs bound together — doing it
	// against an HTLC nobody has checked hands the counterparty the Bitcoin leg
	// for whatever the Zenon entry happens to contain.
	if !sw.Zenon.Verified {
		return errors.New("this swap's Zenon HTLC has not passed verification. Unlocking " +
			"publishes the preimage, which is what lets the counterparty take your Bitcoin — so " +
			"check the HTLC really holds the agreed token, amount and expiry first")
	}
	id, err := hex.DecodeString(htlcID)
	if err != nil {
		return fmt.Errorf("the stored HTLC id %q is not hex: %w", htlcID, err)
	}
	data, err := znn.PackUnlock(id, sw.Secret)
	if err != nil {
		return err
	}

	// The contract pays hashLocked — this user's own address — whoever makes
	// the call, which is what lets a wallet holding nothing settle this leg.
	// Whether that address still permits it, and what it means when the signer
	// is somebody else, is the sync gate's business; see walletsync.go.
	payee := strings.TrimSpace(sw.Zenon.SelfAddress)

	plan.Block.Data = base64.StdEncoding.EncodeToString(data)
	plan.HtlcID = htlcID
	plan.Summary = fmt.Sprintf("Unlock HTLC %s with the preimage, paying its ZNN to %s. "+
		"This publishes the preimage on Zenon.", htlcID, orElse(payee, "the address in the entry"))
```

**Wallet sync does not repair missing recipient** — `wasm/walletsync.go:283-301`

The empty-payee path returns without establishing that the HTLC pays this user.

```go
				"wrong account: this swap's Zenon leg belongs to %s and the wallet has %s "+
					"selected. Switch the account in the extension's settings, then reconnect",
				self, out.Address))
		}

	case "unlock":
		// An unlock may come from any account, because the contract pays the
		// address in the entry rather than the caller. Unless that address has
		// turned the behaviour off, in which case it is the only account that
		// can make the call at all.
		if self == "" || strings.EqualFold(self, out.Address) {
			return
		}
		allowed, perr := mgr.Znn.ProxyUnlockAllowed(ctx, self)
		switch {
		case perr != nil:
			out.Unchecked = append(out.Unchecked, fmt.Sprintf(
				"the wallet would sign from %s while the ZNN goes to %s, and whether that address "+
					"still permits being paid by another account could not be read from the node "+
```

#### Validation

The parent followed optional input through the stored expectations to both conditional checks and the persisted verdict, then to `PackUnlock()`. With the correct hash, token and expiry but `hashLocked` set to the attacker's address, an empty expected recipient does not produce a problem. The configured-address and amount-conversion checks protect populated fields but do not close the absent-field path.

Validation method: static source trace

**Empty Zenon addresses accepted** — `wasm/manager.go:220-237`

Only nonempty addresses are parsed, allowing a draft with no own payout identity.

```go
	// The three Zenon fields are all optional here -- the addresses are routinely
	// filled in later and a blank token means ZNN -- but a value that IS given has
	// to be the kind of thing the field asks for. A Bitcoin address, a Zenon
	// address and a Zenon token standard are all bech32, and the last two start
	// with the same letter, which is exactly the shape of accident a paste between
	// similar-looking fields produces.
	zenonSelf := strings.TrimSpace(p.ZenonSelfAddress)
	if zenonSelf != "" {
		if _, err := znn.ParseAddress(zenonSelf); err != nil {
			return nil, fmt.Errorf("your Zenon address: %w", err)
		}
	}
	zenonPeer := strings.TrimSpace(p.ZenonPeerAddress)
	if zenonPeer != "" {
		if _, err := znn.ParseAddress(zenonPeer); err != nil {
			return nil, fmt.Errorf("their Zenon address: %w", err)
		}
	}
```

**Copy optional address into verification** — `wasm/manager.go:1074-1082`

For the incoming leg the blank own address becomes a blank expected recipient.

```go
	// Checking "recipient is me" against an HTLC this user created themselves
	// would fail every time, and would skip the check that actually matters
	// there: that it pays the counterparty.
	if sw.ZenonHtlcIsOurs() {
		want.ExpectRecipient = sw.Zenon.PeerAddress
		want.ExpectSender = sw.Zenon.SelfAddress
	} else {
		want.ExpectRecipient = sw.Zenon.SelfAddress
		want.ExpectSender = sw.Zenon.PeerAddress
```

**Blank amount leaves no lower bound** — `wasm/manager.go:1120-1133`

The minimum amount is computed only for nonempty agreed text.

```go
	if sw.Zenon.AmountDisplay != "" {
		agreed := sw.Zenon.AgreedToken()
		if tok, terr := m.Znn.GetToken(ctx, agreed); terr != nil {
			want.AmountUncheckable = fmt.Sprintf("could not read token %s from the node: %v",
				agreed, terr)
		} else if amt, cerr := decimalToBaseUnits(sw.Zenon.AmountDisplay, tok.Decimals); cerr != nil {
			want.AmountUncheckable = fmt.Sprintf("the agreed amount %q could not be interpreted: %v",
				sw.Zenon.AmountDisplay, cerr)
		} else {
			want.MinAmount = amt
		}
	}
	return want
}
```

**Absent expectations skip comparisons** — `wasm/znn/client.go:450-473`

An empty expected payee or nil amount is treated as a disabled check rather than incomplete verification.

```go
	if want.ExpectRecipient != "" && !strings.EqualFold(info.HashLocked, want.ExpectRecipient) {
		problems = append(problems, fmt.Sprintf(
			"hashLocked address is %s, expected %s", info.HashLocked, want.ExpectRecipient))
	}
	if want.ExpectSender != "" && !strings.EqualFold(info.TimeLocked, want.ExpectSender) {
		problems = append(problems, fmt.Sprintf(
			"timeLocked address is %s, expected %s", info.TimeLocked, want.ExpectSender))
	}
	if want.AmountUncheckable != "" {
		problems = append(problems, fmt.Sprintf(
			"the agreed amount could not be checked (%s). A skipped check reads exactly like a "+
				"passed one, so this is a refusal: fix the node or the agreed amount and verify again",
			want.AmountUncheckable))
		incomplete++
	} else if want.MinAmount != nil {
		amt, ok := new(big.Int).SetString(info.Amount, 10)
		if !ok {
			problems = append(problems, fmt.Sprintf("amount %q is not a number", info.Amount))
		} else if amt.Cmp(want.MinAmount) < 0 {
			problems = append(problems, fmt.Sprintf(
				"amount %s is below the agreed %s", amt.String(), want.MinAmount.String()))
		}
	}
	if want.Now > 0 && want.MinRemaining > 0 {
```

**Persist successful verification** — `wasm/manager.go:1225-1244`

If no other term fails, the swap receives a verified verdict despite the skipped terms.

```go
	sw.Zenon.VerifyPending = verr != nil && znn.CheckIncomplete(verr)
	if verr != nil {
		sw.Zenon.Verified = false
		sw.Zenon.VerifyError = verr.Error()
		if !repeat {
			sw.log("Zenon HTLC %s FAILED verification: %v", htlcID, verr)
		}
	} else {
		sw.Zenon.Verified = true
		sw.Zenon.VerifyError = ""
		if !repeat {
			sw.log("Zenon HTLC %s verified: hashlock, parties, amount and expiry all match (it is the %s leg)",
				htlcID, legOwner(sw.ZenonLegIsInitiators()))
		}
	}
	if err := m.Store.Save(sw); err != nil {
		return nil, nil, err
	}
	return sw, info, verr
}
```

**Verified status permits preimage encoding** — `wasm/walletblock.go:356-383`

The prior verdict authorizes `PackUnlock`; the summary even falls back to the entry's recipient when no own address is stored.

```go
	// Verification is not a formality here. Unlocking publishes the preimage,
	// which is the one secret that keeps both legs bound together — doing it
	// against an HTLC nobody has checked hands the counterparty the Bitcoin leg
	// for whatever the Zenon entry happens to contain.
	if !sw.Zenon.Verified {
		return errors.New("this swap's Zenon HTLC has not passed verification. Unlocking " +
			"publishes the preimage, which is what lets the counterparty take your Bitcoin — so " +
			"check the HTLC really holds the agreed token, amount and expiry first")
	}
	id, err := hex.DecodeString(htlcID)
	if err != nil {
		return fmt.Errorf("the stored HTLC id %q is not hex: %w", htlcID, err)
	}
	data, err := znn.PackUnlock(id, sw.Secret)
	if err != nil {
		return err
	}

	// The contract pays hashLocked — this user's own address — whoever makes
	// the call, which is what lets a wallet holding nothing settle this leg.
	// Whether that address still permits it, and what it means when the signer
	// is somebody else, is the sync gate's business; see walletsync.go.
	payee := strings.TrimSpace(sw.Zenon.SelfAddress)

	plan.Block.Data = base64.StdEncoding.EncodeToString(data)
	plan.HtlcID = htlcID
	plan.Summary = fmt.Sprintf("Unlock HTLC %s with the preimage, paying its ZNN to %s. "+
		"This publishes the preimage on Zenon.", htlcID, orElse(payee, "the address in the entry"))
```

**Wallet sync does not repair missing recipient** — `wasm/walletsync.go:283-301`

The empty-payee path returns without establishing that the HTLC pays this user.

```go
				"wrong account: this swap's Zenon leg belongs to %s and the wallet has %s "+
					"selected. Switch the account in the extension's settings, then reconnect",
				self, out.Address))
		}

	case "unlock":
		// An unlock may come from any account, because the contract pays the
		// address in the entry rather than the caller. Unless that address has
		// turned the behaviour off, in which case it is the only account that
		// can make the call at all.
		if self == "" || strings.EqualFold(self, out.Address) {
			return
		}
		allowed, perr := mgr.Znn.ProxyUnlockAllowed(ctx, self)
		switch {
		case perr != nil:
			out.Unchecked = append(out.Unchecked, fmt.Sprintf(
				"the wallet would sign from %s while the ZNN goes to %s, and whether that address "+
					"still permits being paid by another account could not be read from the node "+
```

Assertions:
- A verified incoming Zenon HTLC must pay the victim's agreed address before the victim reveals the Bitcoin preimage.
- The attacker gets its own Zenon deposit back and learns the preimage needed to take the victim's Bitcoin.

Limitations:
- No live wallet or chain exploit was run; supported Go build and isolated regressions were blocked by toolchain and offline dependency availability.

#### Dataflow

peer-created Zenon HTLC with attacker-chosen recipient/amount, paired with locally omitted expected terms → verified status and secret-bearing Zenon Unlock. The attacker gets its own Zenon deposit back and learns the preimage needed to take the victim's Bitcoin.

- **Source:** peer-created Zenon HTLC with attacker-chosen recipient/amount, paired with locally omitted expected terms

- **Sink:** verified status and secret-bearing Zenon Unlock

- **Outcome:** The attacker gets its own Zenon deposit back and learns the preimage needed to take the victim's Bitcoin.

**Empty Zenon addresses accepted** — `wasm/manager.go:220-237`

Only nonempty addresses are parsed, allowing a draft with no own payout identity.

```go
	// The three Zenon fields are all optional here -- the addresses are routinely
	// filled in later and a blank token means ZNN -- but a value that IS given has
	// to be the kind of thing the field asks for. A Bitcoin address, a Zenon
	// address and a Zenon token standard are all bech32, and the last two start
	// with the same letter, which is exactly the shape of accident a paste between
	// similar-looking fields produces.
	zenonSelf := strings.TrimSpace(p.ZenonSelfAddress)
	if zenonSelf != "" {
		if _, err := znn.ParseAddress(zenonSelf); err != nil {
			return nil, fmt.Errorf("your Zenon address: %w", err)
		}
	}
	zenonPeer := strings.TrimSpace(p.ZenonPeerAddress)
	if zenonPeer != "" {
		if _, err := znn.ParseAddress(zenonPeer); err != nil {
			return nil, fmt.Errorf("their Zenon address: %w", err)
		}
	}
```

**Copy optional address into verification** — `wasm/manager.go:1074-1082`

For the incoming leg the blank own address becomes a blank expected recipient.

```go
	// Checking "recipient is me" against an HTLC this user created themselves
	// would fail every time, and would skip the check that actually matters
	// there: that it pays the counterparty.
	if sw.ZenonHtlcIsOurs() {
		want.ExpectRecipient = sw.Zenon.PeerAddress
		want.ExpectSender = sw.Zenon.SelfAddress
	} else {
		want.ExpectRecipient = sw.Zenon.SelfAddress
		want.ExpectSender = sw.Zenon.PeerAddress
```

**Blank amount leaves no lower bound** — `wasm/manager.go:1120-1133`

The minimum amount is computed only for nonempty agreed text.

```go
	if sw.Zenon.AmountDisplay != "" {
		agreed := sw.Zenon.AgreedToken()
		if tok, terr := m.Znn.GetToken(ctx, agreed); terr != nil {
			want.AmountUncheckable = fmt.Sprintf("could not read token %s from the node: %v",
				agreed, terr)
		} else if amt, cerr := decimalToBaseUnits(sw.Zenon.AmountDisplay, tok.Decimals); cerr != nil {
			want.AmountUncheckable = fmt.Sprintf("the agreed amount %q could not be interpreted: %v",
				sw.Zenon.AmountDisplay, cerr)
		} else {
			want.MinAmount = amt
		}
	}
	return want
}
```

**Absent expectations skip comparisons** — `wasm/znn/client.go:450-473`

An empty expected payee or nil amount is treated as a disabled check rather than incomplete verification.

```go
	if want.ExpectRecipient != "" && !strings.EqualFold(info.HashLocked, want.ExpectRecipient) {
		problems = append(problems, fmt.Sprintf(
			"hashLocked address is %s, expected %s", info.HashLocked, want.ExpectRecipient))
	}
	if want.ExpectSender != "" && !strings.EqualFold(info.TimeLocked, want.ExpectSender) {
		problems = append(problems, fmt.Sprintf(
			"timeLocked address is %s, expected %s", info.TimeLocked, want.ExpectSender))
	}
	if want.AmountUncheckable != "" {
		problems = append(problems, fmt.Sprintf(
			"the agreed amount could not be checked (%s). A skipped check reads exactly like a "+
				"passed one, so this is a refusal: fix the node or the agreed amount and verify again",
			want.AmountUncheckable))
		incomplete++
	} else if want.MinAmount != nil {
		amt, ok := new(big.Int).SetString(info.Amount, 10)
		if !ok {
			problems = append(problems, fmt.Sprintf("amount %q is not a number", info.Amount))
		} else if amt.Cmp(want.MinAmount) < 0 {
			problems = append(problems, fmt.Sprintf(
				"amount %s is below the agreed %s", amt.String(), want.MinAmount.String()))
		}
	}
	if want.Now > 0 && want.MinRemaining > 0 {
```

**Persist successful verification** — `wasm/manager.go:1225-1244`

If no other term fails, the swap receives a verified verdict despite the skipped terms.

```go
	sw.Zenon.VerifyPending = verr != nil && znn.CheckIncomplete(verr)
	if verr != nil {
		sw.Zenon.Verified = false
		sw.Zenon.VerifyError = verr.Error()
		if !repeat {
			sw.log("Zenon HTLC %s FAILED verification: %v", htlcID, verr)
		}
	} else {
		sw.Zenon.Verified = true
		sw.Zenon.VerifyError = ""
		if !repeat {
			sw.log("Zenon HTLC %s verified: hashlock, parties, amount and expiry all match (it is the %s leg)",
				htlcID, legOwner(sw.ZenonLegIsInitiators()))
		}
	}
	if err := m.Store.Save(sw); err != nil {
		return nil, nil, err
	}
	return sw, info, verr
}
```

**Verified status permits preimage encoding** — `wasm/walletblock.go:356-383`

The prior verdict authorizes `PackUnlock`; the summary even falls back to the entry's recipient when no own address is stored.

```go
	// Verification is not a formality here. Unlocking publishes the preimage,
	// which is the one secret that keeps both legs bound together — doing it
	// against an HTLC nobody has checked hands the counterparty the Bitcoin leg
	// for whatever the Zenon entry happens to contain.
	if !sw.Zenon.Verified {
		return errors.New("this swap's Zenon HTLC has not passed verification. Unlocking " +
			"publishes the preimage, which is what lets the counterparty take your Bitcoin — so " +
			"check the HTLC really holds the agreed token, amount and expiry first")
	}
	id, err := hex.DecodeString(htlcID)
	if err != nil {
		return fmt.Errorf("the stored HTLC id %q is not hex: %w", htlcID, err)
	}
	data, err := znn.PackUnlock(id, sw.Secret)
	if err != nil {
		return err
	}

	// The contract pays hashLocked — this user's own address — whoever makes
	// the call, which is what lets a wallet holding nothing settle this leg.
	// Whether that address still permits it, and what it means when the signer
	// is somebody else, is the sync gate's business; see walletsync.go.
	payee := strings.TrimSpace(sw.Zenon.SelfAddress)

	plan.Block.Data = base64.StdEncoding.EncodeToString(data)
	plan.HtlcID = htlcID
	plan.Summary = fmt.Sprintf("Unlock HTLC %s with the preimage, paying its ZNN to %s. "+
		"This publishes the preimage on Zenon.", htlcID, orElse(payee, "the address in the entry"))
```

**Wallet sync does not repair missing recipient** — `wasm/walletsync.go:283-301`

The empty-payee path returns without establishing that the HTLC pays this user.

```go
				"wrong account: this swap's Zenon leg belongs to %s and the wallet has %s "+
					"selected. Switch the account in the extension's settings, then reconnect",
				self, out.Address))
		}

	case "unlock":
		// An unlock may come from any account, because the contract pays the
		// address in the entry rather than the caller. Unless that address has
		// turned the behaviour off, in which case it is the only account that
		// can make the call at all.
		if self == "" || strings.EqualFold(self, out.Address) {
			return
		}
		allowed, perr := mgr.Znn.ProxyUnlockAllowed(ctx, self)
		switch {
		case perr != nil:
			out.Unchecked = append(out.Unchecked, fmt.Sprintf(
				"the wallet would sign from %s while the ZNN goes to %s, and whether that address "+
					"still permits being paid by another account could not be read from the node "+
```

#### Reachability

The normal creation form makes these fields optional. A trading counterparty can supply an otherwise matching HTLC without compromising the node; exploitation requires the victim to omit the expected recipient or amount.

- **Attacker:** A malicious Zenon participant trading with a Bitcoin initiator whose swap was created without a Zenon self address.

- **Entry point:** Empty Zenon addresses accepted

- **Outcome:** The attacker gets its own Zenon deposit back and learns the preimage needed to take the victim's Bitcoin.

#### Severity

**High** — High impact and high likelihood. The normal creation form makes these fields optional. A trading counterparty can supply an otherwise matching HTLC without compromising the node; exploitation requires the victim to omit the expected recipient or amount.

Additional runtime or deployment evidence could raise or lower this severity.

Impact assessment:
- **Level:** high
- **Why:** The attacker gets its own Zenon deposit back and learns the preimage needed to take the victim's Bitcoin.

Likelihood assessment:
- **Level:** high
- **Why:** The normal creation form makes these fields optional. A trading counterparty can supply an otherwise matching HTLC without compromising the node; exploitation requires the victim to omit the expected recipient or amount.

#### Remediation

Require a pinned recipient before verifying an incoming Zenon leg or exposing a secret. For records created with a blank address, explicitly bind the intended recipient from the connected wallet and reverify the HTLC before permitting unlock.

Tests:
- An incoming HTLC must remain unverified when expected recipient or positive amount is absent, including manual and session discovery.
- A matching token/hash/expiry with an attacker recipient must be refused before Unlock or first-secret disclosure.
- Connecting a wallet after creation must not silently adopt an attacker-observed recipient as the agreed destination.

Preventive controls:
- Use an explicit complete agreed-terms type before verification; missing expectations must yield an incomplete verdict, never success.

<a id="finding-5"></a>

### [5] An offer amount becomes shell syntax in copied CLI commands

| Field | Value |
| --- | --- |
| Severity | medium |
| Confidence | high |
| Confidence rationale | Complete source-to-sink trace with identified controls and counterevidence; independent review corroborates the path. Dynamic end-to-end proof was not obtained. |
| Category | command-injection |
| CWE | CWE-78 |
| Affected lines | ui/src/core/zenon-commands.ts:94-98, wasm/swap.go:502-517, wasm/swap.go:541-551, ui/src/pages/ActivePage.vue:532-550, wasm/manager.go:266-279, ui/src/core/zenon-commands.ts:60-74, ui/src/components/SwapCard.vue:1316-1332 |

#### Summary

A malicious offer can place shell substitution syntax in its Zenon amount. Ferry copies that string into the optional znn-cli command without quoting. A user who fills the command's wallet placeholders and runs it can execute the counterparty's command with their local account privileges.

#### Root Cause

`DecodeOffer()` validates the envelope, key/hash fields, network, roles and Bitcoin amount, but not `ZenonAmt`. The form copies the peer's string and `Manager.Create()` stores it in `AmountDisplay`; equal offer/form strings pass the terms comparison. `znnCommands()` then interpolates this value into a shell command, and the swap card exposes a copy button. Wallet-specific numeric checks are on another path and do not sanitize command generation.

**Decode untrusted offer JSON** — `wasm/swap.go:502-517`

The counterparty controls the encoded Zenon amount field.

```go
func DecodeOffer(s string) (*Offer, error) {
	const prefix = "swapoffer1:"
	if len(s) <= len(prefix) || s[:len(prefix)] != prefix {
		return nil, errors.New("not a swap offer string (expected a swapoffer1: prefix)")
	}
	raw, err := base64.RawURLEncoding.DecodeString(s[len(prefix):])
	if err != nil {
		return nil, fmt.Errorf("offer is not valid base64: %w", err)
	}
	var o Offer
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("offer is not valid JSON: %w", err)
	}
	if o.Version != 1 {
		return nil, fmt.Errorf("unsupported offer version %d", o.Version)
	}
```

**Return without Zenon amount validation** — `wasm/swap.go:541-551`

The decoder ends after role and Bitcoin amount checks.

```go
		return nil, fmt.Errorf("offer's btcLeg is %q, must be %q or %q", o.BTCLeg, LegSend, LegReceive)
	}
	if o.FromRole != RoleInitiator && o.FromRole != RoleParticipant {
		return nil, fmt.Errorf("offer's fromRole is %q, must be %q or %q",
			o.FromRole, RoleInitiator, RoleParticipant)
	}
	if o.AmountSats <= 0 {
		return nil, fmt.Errorf("offer's amountSats is %d, which is not an amount", o.AmountSats)
	}
	return &o, nil
}
```

**Populate the form with the peer's amount** — `ui/src/pages/ActivePage.vue:532-550`

The amount string crosses unchanged into the form used to create the local swap.

```typescript
  // has to build the contract, which is the side sending BTC.
  form.value.counterpartyPkhHex = d.yourLeg === 'send' ? (d.decoded.pkh ?? '') : ''
  // Written whether or not the offer carried a value, which the guarded version
  // of this did not do. A term the offer leaves out is still a term: Go reads a
  // blank Zenon token as ZNN on both sides, so an offer that omits it and a
  // form still holding the last thing typed into that box are two different
  // swaps. Now that these fields are about to be locked, a stale value left in
  // one is the worst of both -- disagreed about, and out of reach.
  form.value.amountSats = String(d.decoded.amountSats ?? 0)
  form.value.zenonPeerAddress = d.decoded.zenonAddr ?? ''
  form.value.zenonToken = d.decoded.zenonToken ?? ''
  form.value.zenonAmount = d.decoded.zenonAmt ?? ''
  // Two of the terms an offer pins -- the token, and the pubkey hash naming the
  // key their Bitcoin is redeemable by -- live behind Advanced options, which
  // is collapsed. Checking what arrived is the whole job here, and a value
  // folded away is one nobody checks. Opened rather than moved out: they are
  // still advanced for somebody filling this in from scratch.
  showAdvanced.value = true
}
```

**Store the amount as trimmed text** — `wasm/manager.go:266-279`

Creation preserves shell syntax in the amount display field.

```go
		State:      StateDraft,
		Key:        key,
		AmountSats: p.AmountSats,
		DestAddr:   p.DestAddr,
		Zenon: ZenonLeg{
			SelfAddress:   zenonSelf,
			PeerAddress:   zenonPeer,
			TokenStandard: zenonToken,
			AmountDisplay: strings.TrimSpace(p.ZenonAmount),
			HashType:      znn.HashTypeSHA256,
			KeyMaxSize:    SecretSize,
		},
	}

```

**Read stored strings for the command** — `ui/src/core/zenon-commands.ts:60-74`

The generator pulls the unvalidated amount and other values directly into command variables.

```typescript
export function znnCommands(sw: Swap, ctx: CommandContext = {}): string {
  const lines: string[] = []
  const token = sw.zenon?.tokenStandard || 'ZNN'
  const hours = sw.zenon?.expirationHours
  const amount = sw.zenon?.amountDisplay || '<amount>'
  const peer = sw.zenon?.peerAddress || '<counterparty z1 address>'
  const self = sw.zenon?.selfAddress
  const ours = sw.btcLegIsInitiators ? "participant's" : "initiator's"

  // -k names the keystore (its file name, or the address it holds), -i the
  // account index inside it, -p the passphrase and -u the node.
  const node = ctx.nodeURL ? ` -u ${ctx.nodeURL}` : ' -u <wss://your-node:35998>'
  const cliFlags = ` -k ${self || '<your keystore>'} -i <account index> -p <passphrase>${node}`

  lines.push(`# znn-cli — ${ZNN_CLI_URL}`)
```

**Interpolate unquoted command arguments** — `ui/src/core/zenon-commands.ts:92-99`

Shell substitution is active when the copied template is run by the victim.

```typescript
          ` needs ${hours}h. Use the Syrius extension, which has no cap.`,
      )
    } else {
      lines.push(
        `znn-cli htlc.create ${peer} ${token} ${amount} ${hours || '<hours>'} 1` +
          ` ${sw.secretHashHex}${cliFlags}`,
      )
    }
```

**Expose the generated command for copying** — `ui/src/components/SwapCard.vue:1316-1332`

The optional CLI panel supports the copy-and-run workflow needed for impact.

```typescript
        <div v-if="showCli" class="min-w-0">
          <Button variant="outline" size="sm" @click="showCommands = !showCommands">
            {{ showCommands ? 'Hide' : 'Show' }} the command to run
            {{ zenonAction || zenonReclaimable ? 'instead' : '' }}
          </Button>
          <!-- The commands DO scroll rather than wrap: they are line-oriented
               shell input, and a wrapped command line is one somebody copies
               wrong. Which makes every box between this <pre> and the page a
               load-bearing min-w-0 — a grid or flex item defaults to min-width
               auto, so without one at EVERY level the widest command line is
               pushed all the way out through the card and widens the page
               instead of scrolling inside its own box. -->
          <div v-if="showCommands" class="relative mt-2 w-full min-w-0">
            <pre
              class="max-w-full overflow-x-auto rounded-md bg-muted/60 p-3 font-mono text-xs leading-relaxed"
              >{{ commands }}</pre>
            <CopyButton :value="commands" size="icon-sm" class="absolute top-2 right-2" />
```

#### Validation

The parent traced the unvalidated field from offer decoding through form population and stored state to the literal template and copy control. A string containing shell substitution, for example `1$(id)`, is emitted as executable syntax in the command's amount position. No shell payload was executed. HTML escaping protects page rendering but not a copied shell command.

Validation method: static source trace

**Decode untrusted offer JSON** — `wasm/swap.go:502-517`

The counterparty controls the encoded Zenon amount field.

```go
func DecodeOffer(s string) (*Offer, error) {
	const prefix = "swapoffer1:"
	if len(s) <= len(prefix) || s[:len(prefix)] != prefix {
		return nil, errors.New("not a swap offer string (expected a swapoffer1: prefix)")
	}
	raw, err := base64.RawURLEncoding.DecodeString(s[len(prefix):])
	if err != nil {
		return nil, fmt.Errorf("offer is not valid base64: %w", err)
	}
	var o Offer
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("offer is not valid JSON: %w", err)
	}
	if o.Version != 1 {
		return nil, fmt.Errorf("unsupported offer version %d", o.Version)
	}
```

**Return without Zenon amount validation** — `wasm/swap.go:541-551`

The decoder ends after role and Bitcoin amount checks.

```go
		return nil, fmt.Errorf("offer's btcLeg is %q, must be %q or %q", o.BTCLeg, LegSend, LegReceive)
	}
	if o.FromRole != RoleInitiator && o.FromRole != RoleParticipant {
		return nil, fmt.Errorf("offer's fromRole is %q, must be %q or %q",
			o.FromRole, RoleInitiator, RoleParticipant)
	}
	if o.AmountSats <= 0 {
		return nil, fmt.Errorf("offer's amountSats is %d, which is not an amount", o.AmountSats)
	}
	return &o, nil
}
```

**Populate the form with the peer's amount** — `ui/src/pages/ActivePage.vue:532-550`

The amount string crosses unchanged into the form used to create the local swap.

```typescript
  // has to build the contract, which is the side sending BTC.
  form.value.counterpartyPkhHex = d.yourLeg === 'send' ? (d.decoded.pkh ?? '') : ''
  // Written whether or not the offer carried a value, which the guarded version
  // of this did not do. A term the offer leaves out is still a term: Go reads a
  // blank Zenon token as ZNN on both sides, so an offer that omits it and a
  // form still holding the last thing typed into that box are two different
  // swaps. Now that these fields are about to be locked, a stale value left in
  // one is the worst of both -- disagreed about, and out of reach.
  form.value.amountSats = String(d.decoded.amountSats ?? 0)
  form.value.zenonPeerAddress = d.decoded.zenonAddr ?? ''
  form.value.zenonToken = d.decoded.zenonToken ?? ''
  form.value.zenonAmount = d.decoded.zenonAmt ?? ''
  // Two of the terms an offer pins -- the token, and the pubkey hash naming the
  // key their Bitcoin is redeemable by -- live behind Advanced options, which
  // is collapsed. Checking what arrived is the whole job here, and a value
  // folded away is one nobody checks. Opened rather than moved out: they are
  // still advanced for somebody filling this in from scratch.
  showAdvanced.value = true
}
```

**Store the amount as trimmed text** — `wasm/manager.go:266-279`

Creation preserves shell syntax in the amount display field.

```go
		State:      StateDraft,
		Key:        key,
		AmountSats: p.AmountSats,
		DestAddr:   p.DestAddr,
		Zenon: ZenonLeg{
			SelfAddress:   zenonSelf,
			PeerAddress:   zenonPeer,
			TokenStandard: zenonToken,
			AmountDisplay: strings.TrimSpace(p.ZenonAmount),
			HashType:      znn.HashTypeSHA256,
			KeyMaxSize:    SecretSize,
		},
	}

```

**Read stored strings for the command** — `ui/src/core/zenon-commands.ts:60-74`

The generator pulls the unvalidated amount and other values directly into command variables.

```typescript
export function znnCommands(sw: Swap, ctx: CommandContext = {}): string {
  const lines: string[] = []
  const token = sw.zenon?.tokenStandard || 'ZNN'
  const hours = sw.zenon?.expirationHours
  const amount = sw.zenon?.amountDisplay || '<amount>'
  const peer = sw.zenon?.peerAddress || '<counterparty z1 address>'
  const self = sw.zenon?.selfAddress
  const ours = sw.btcLegIsInitiators ? "participant's" : "initiator's"

  // -k names the keystore (its file name, or the address it holds), -i the
  // account index inside it, -p the passphrase and -u the node.
  const node = ctx.nodeURL ? ` -u ${ctx.nodeURL}` : ' -u <wss://your-node:35998>'
  const cliFlags = ` -k ${self || '<your keystore>'} -i <account index> -p <passphrase>${node}`

  lines.push(`# znn-cli — ${ZNN_CLI_URL}`)
```

**Interpolate unquoted command arguments** — `ui/src/core/zenon-commands.ts:92-99`

Shell substitution is active when the copied template is run by the victim.

```typescript
          ` needs ${hours}h. Use the Syrius extension, which has no cap.`,
      )
    } else {
      lines.push(
        `znn-cli htlc.create ${peer} ${token} ${amount} ${hours || '<hours>'} 1` +
          ` ${sw.secretHashHex}${cliFlags}`,
      )
    }
```

**Expose the generated command for copying** — `ui/src/components/SwapCard.vue:1316-1332`

The optional CLI panel supports the copy-and-run workflow needed for impact.

```typescript
        <div v-if="showCli" class="min-w-0">
          <Button variant="outline" size="sm" @click="showCommands = !showCommands">
            {{ showCommands ? 'Hide' : 'Show' }} the command to run
            {{ zenonAction || zenonReclaimable ? 'instead' : '' }}
          </Button>
          <!-- The commands DO scroll rather than wrap: they are line-oriented
               shell input, and a wrapped command line is one somebody copies
               wrong. Which makes every box between this <pre> and the page a
               load-bearing min-w-0 — a grid or flex item defaults to min-width
               auto, so without one at EVERY level the widest command line is
               pushed all the way out through the card and widens the page
               instead of scrolling inside its own box. -->
          <div v-if="showCommands" class="relative mt-2 w-full min-w-0">
            <pre
              class="max-w-full overflow-x-auto rounded-md bg-muted/60 p-3 font-mono text-xs leading-relaxed"
              >{{ commands }}</pre>
            <CopyButton :value="commands" size="icon-sm" class="absolute top-2 right-2" />
```

Assertions:
- Offer data copied into a generated command must remain a literal argument rather than executable shell syntax.
- Arbitrary command execution as the user running znn-cli, potentially exposing wallet files or other local secrets.

Limitations:
- No live wallet or chain exploit was run; supported Go build and isolated regressions were blocked by toolchain and offline dependency availability.

#### Dataflow

Zenon amount in a counterparty's swapoffer1 payload → user-executed copied znn-cli shell command. Arbitrary command execution as the user running znn-cli, potentially exposing wallet files or other local secrets.

- **Source:** Zenon amount in a counterparty's swapoffer1 payload

- **Sink:** user-executed copied znn-cli shell command

- **Outcome:** Arbitrary command execution as the user running znn-cli, potentially exposing wallet files or other local secrets.

**Decode untrusted offer JSON** — `wasm/swap.go:502-517`

The counterparty controls the encoded Zenon amount field.

```go
func DecodeOffer(s string) (*Offer, error) {
	const prefix = "swapoffer1:"
	if len(s) <= len(prefix) || s[:len(prefix)] != prefix {
		return nil, errors.New("not a swap offer string (expected a swapoffer1: prefix)")
	}
	raw, err := base64.RawURLEncoding.DecodeString(s[len(prefix):])
	if err != nil {
		return nil, fmt.Errorf("offer is not valid base64: %w", err)
	}
	var o Offer
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("offer is not valid JSON: %w", err)
	}
	if o.Version != 1 {
		return nil, fmt.Errorf("unsupported offer version %d", o.Version)
	}
```

**Return without Zenon amount validation** — `wasm/swap.go:541-551`

The decoder ends after role and Bitcoin amount checks.

```go
		return nil, fmt.Errorf("offer's btcLeg is %q, must be %q or %q", o.BTCLeg, LegSend, LegReceive)
	}
	if o.FromRole != RoleInitiator && o.FromRole != RoleParticipant {
		return nil, fmt.Errorf("offer's fromRole is %q, must be %q or %q",
			o.FromRole, RoleInitiator, RoleParticipant)
	}
	if o.AmountSats <= 0 {
		return nil, fmt.Errorf("offer's amountSats is %d, which is not an amount", o.AmountSats)
	}
	return &o, nil
}
```

**Populate the form with the peer's amount** — `ui/src/pages/ActivePage.vue:532-550`

The amount string crosses unchanged into the form used to create the local swap.

```typescript
  // has to build the contract, which is the side sending BTC.
  form.value.counterpartyPkhHex = d.yourLeg === 'send' ? (d.decoded.pkh ?? '') : ''
  // Written whether or not the offer carried a value, which the guarded version
  // of this did not do. A term the offer leaves out is still a term: Go reads a
  // blank Zenon token as ZNN on both sides, so an offer that omits it and a
  // form still holding the last thing typed into that box are two different
  // swaps. Now that these fields are about to be locked, a stale value left in
  // one is the worst of both -- disagreed about, and out of reach.
  form.value.amountSats = String(d.decoded.amountSats ?? 0)
  form.value.zenonPeerAddress = d.decoded.zenonAddr ?? ''
  form.value.zenonToken = d.decoded.zenonToken ?? ''
  form.value.zenonAmount = d.decoded.zenonAmt ?? ''
  // Two of the terms an offer pins -- the token, and the pubkey hash naming the
  // key their Bitcoin is redeemable by -- live behind Advanced options, which
  // is collapsed. Checking what arrived is the whole job here, and a value
  // folded away is one nobody checks. Opened rather than moved out: they are
  // still advanced for somebody filling this in from scratch.
  showAdvanced.value = true
}
```

**Store the amount as trimmed text** — `wasm/manager.go:266-279`

Creation preserves shell syntax in the amount display field.

```go
		State:      StateDraft,
		Key:        key,
		AmountSats: p.AmountSats,
		DestAddr:   p.DestAddr,
		Zenon: ZenonLeg{
			SelfAddress:   zenonSelf,
			PeerAddress:   zenonPeer,
			TokenStandard: zenonToken,
			AmountDisplay: strings.TrimSpace(p.ZenonAmount),
			HashType:      znn.HashTypeSHA256,
			KeyMaxSize:    SecretSize,
		},
	}

```

**Read stored strings for the command** — `ui/src/core/zenon-commands.ts:60-74`

The generator pulls the unvalidated amount and other values directly into command variables.

```typescript
export function znnCommands(sw: Swap, ctx: CommandContext = {}): string {
  const lines: string[] = []
  const token = sw.zenon?.tokenStandard || 'ZNN'
  const hours = sw.zenon?.expirationHours
  const amount = sw.zenon?.amountDisplay || '<amount>'
  const peer = sw.zenon?.peerAddress || '<counterparty z1 address>'
  const self = sw.zenon?.selfAddress
  const ours = sw.btcLegIsInitiators ? "participant's" : "initiator's"

  // -k names the keystore (its file name, or the address it holds), -i the
  // account index inside it, -p the passphrase and -u the node.
  const node = ctx.nodeURL ? ` -u ${ctx.nodeURL}` : ' -u <wss://your-node:35998>'
  const cliFlags = ` -k ${self || '<your keystore>'} -i <account index> -p <passphrase>${node}`

  lines.push(`# znn-cli — ${ZNN_CLI_URL}`)
```

**Interpolate unquoted command arguments** — `ui/src/core/zenon-commands.ts:92-99`

Shell substitution is active when the copied template is run by the victim.

```typescript
          ` needs ${hours}h. Use the Syrius extension, which has no cap.`,
      )
    } else {
      lines.push(
        `znn-cli htlc.create ${peer} ${token} ${amount} ${hours || '<hours>'} 1` +
          ` ${sw.secretHashHex}${cliFlags}`,
      )
    }
```

**Expose the generated command for copying** — `ui/src/components/SwapCard.vue:1316-1332`

The optional CLI panel supports the copy-and-run workflow needed for impact.

```typescript
        <div v-if="showCli" class="min-w-0">
          <Button variant="outline" size="sm" @click="showCommands = !showCommands">
            {{ showCommands ? 'Hide' : 'Show' }} the command to run
            {{ zenonAction || zenonReclaimable ? 'instead' : '' }}
          </Button>
          <!-- The commands DO scroll rather than wrap: they are line-oriented
               shell input, and a wrapped command line is one somebody copies
               wrong. Which makes every box between this <pre> and the page a
               load-bearing min-w-0 — a grid or flex item defaults to min-width
               auto, so without one at EVERY level the widest command line is
               pushed all the way out through the card and widens the page
               instead of scrolling inside its own box. -->
          <div v-if="showCommands" class="relative mt-2 w-full min-w-0">
            <pre
              class="max-w-full overflow-x-auto rounded-md bg-muted/60 p-3 font-mono text-xs leading-relaxed"
              >{{ commands }}</pre>
            <CopyButton :value="commands" size="icon-sm" class="absolute top-2 right-2" />
```

#### Reachability

The victim must choose the optional CLI workflow, fill visible placeholders and run the copied command; no execution occurs merely by decoding or viewing the offer.

- **Attacker:** A counterparty who supplies an encoded offer to a victim using Ferry's supported znn-cli workflow.

- **Entry point:** Decode untrusted offer JSON

- **Outcome:** Arbitrary command execution as the user running znn-cli, potentially exposing wallet files or other local secrets.

#### Severity

**Medium** — High impact and medium likelihood. The victim must choose the optional CLI workflow, fill visible placeholders and run the copied command; no execution occurs merely by decoding or viewing the offer.

Additional runtime or deployment evidence could raise or lower this severity.

Impact assessment:
- **Level:** high
- **Why:** Arbitrary command execution as the user running znn-cli, potentially exposing wallet files or other local secrets.

Likelihood assessment:
- **Level:** medium
- **Why:** The victim must choose the optional CLI workflow, fill visible placeholders and run the copied command; no execution occurs merely by decoding or viewing the offer.

#### Remediation

Validate positive bounded decimal amounts at offer decoding and core swap creation. POSIX-shell-quote every interpolated argument in CLI output, including amounts, HTLC identifiers and node URLs, and avoid emitting runnable commands while mandatory fields remain invalid.

Tests:
- Reject shell metacharacters, substitutions, whitespace and invalid numeric formats at offer decoding and swap creation.
- Never emit a runnable command for incomplete or invalid terms; test all interpolated values, including node URLs and IDs.
- Verify safe quoting by comparing parsed shell arguments in an isolated nonexecuting parser, and preserve valid decimal amounts.

Preventive controls:
- Keep peer input as typed data; validate positive decimals and encode every shell argument, or replace shell snippets with a structured wallet invocation.

<a id="finding-6"></a>

### [6] A session peer can replace the script of an already-funded swap

| Field | Value |
| --- | --- |
| Severity | medium |
| Confidence | high |
| Confidence rationale | Complete source-to-sink trace with identified controls and counterevidence; independent review corroborates the path. Dynamic end-to-end proof was not obtained. |
| Category | atomic-swap |
| CWE | CWE-841 |
| Affected lines | wasm/manager.go:438-441, ui/src/core/composables/useSession.ts:612-630, wasm/txbuild.go:145-160, wasm/txbuild.go:193-204 |

#### Summary

A counterparty can send a second valid contract through the live session after the first was funded. Ferry overwrites the script while retaining the original funding outpoint, so the victim's later redeem spends that outpoint using the wrong script and is rejected.

#### Root Cause

The session contract handler calls `api.audit()` for each accepted peer contract. `AuditContract()` checks the new redeem key, hash and timing but does not freeze contract identity once funding or the other leg is committed. It overwrites `Contract`, `ContractAddr`, `LockTime` and the refund key without clearing or rebinding `Funding`. `BuildRedeem()` constructs its local previous-output script from that new contract rather than checking the actual funding transaction.

**Peer messages automatically invoke audit** — `ui/src/core/composables/useSession.ts:612-630`

An authenticated counterparty can resend contract messages; the handler applies the returned swap.

```typescript
      // board's own `pendingTrade` is.
      peerAddresses.value = {
        btc: (msg.btcAddr ?? '').trim(),
        znn: (msg.zenonAddr ?? '').trim(),
      }
      say('in', 'They sent where to pay them — it is filled into the new-swap form.')
      break
    case 'pkh':
      await applyValue(`their pubkey hash ${short(msg.pkhHex)}`, () =>
        api.counterparty(swapId.value, msg.pkhHex ?? ''),
      )
      break
    case 'contract':
      await applyValue(`their contract ${short(msg.contractHex)}`, () =>
        api.audit(swapId.value, msg.contractHex ?? ''),
      )
      break
    case 'zenon':
      await applyValue(`their Zenon HTLC ${short(msg.htlcId)}`, async () => {
```

**Overwrite contract without funding guard** — `wasm/manager.go:429-454`

The new valid script is stored without checking commitment state or rebinding the existing outpoint.

```go
	remaining := time.Until(time.Unix(details.LockTime, 0))
	if err := auditLegOrdering(sw, details.LockTime, remaining); err != nil {
		return nil, err
	}

	addr, err := ContractAddress(contract, params)
	if err != nil {
		return nil, err
	}
	sw.Contract = contract
	sw.ContractAddr = addr.String()
	sw.LockTime = details.LockTime
	sw.CounterpartyPKH = details.PkhRefund
	if sw.State == StateDraft {
		sw.State = StateAwaitingFunding
	}
	planZenonLeg(sw, time.Now().UTC())
	sw.log("audited counterparty contract %s: redeemable by our key, locktime %s (%s away), "+
		"which is the %s leg",
		sw.ContractAddr, time.Unix(details.LockTime, 0).UTC().Format(time.RFC3339),
		remaining.Truncate(time.Minute), legOwner(sw.BitcoinLegIsInitiators()))
	if sw.ZenonHtlcIsOurs() && sw.Zenon.ExpirationSeconds > 0 && sw.Zenon.HtlcID == "" {
		sw.log("create your Zenon HTLC with an expiry of %ds (%dh) so it lands on the correct "+
			"side of this contract's locktime",
			sw.Zenon.ExpirationSeconds, sw.Zenon.ExpirationHours)
	}
```

**Derive prevout script from mutable contract** — `wasm/txbuild.go:145-160`

The signer pairs the existing outpoint with a script inferred from the replacement contract.

```go
	fundingHash, err := chainhash.NewHashFromStr(funding.TxID)
	if err != nil {
		return nil, fmt.Errorf("bad funding txid: %w", err)
	}
	contractPkScript, err := contractPkScript(contract, params)
	if err != nil {
		return nil, err
	}

	newTx := func() *wire.MsgTx {
		tx := wire.NewMsgTx(txVersion)
		tx.LockTime = uint32(locktime)
		txIn := wire.NewTxIn(&wire.OutPoint{Hash: *fundingHash, Index: funding.Vout}, nil, nil)
		// CHECKLOCKTIMEVERIFY is only enforced when the input's sequence is not
		// final, so the refund path must not use the default 0xffffffff. Using
		// 0 for both paths keeps the two transactions the same size.
```

**Validate against the assumed script** — `wasm/txbuild.go:193-204`

The local engine validates the replacement rather than the real previous output, leaving rejection to the chain.

```go
	// Execute the script locally before handing the transaction to anyone.
	// A swap failing at broadcast time is recoverable; one that fails silently
	// is not.
	engine, err := txscript.NewEngine(contractPkScript, tx, 0, txscript.StandardVerifyFlags,
		txscript.NewSigCache(10), txscript.NewTxSigHashes(tx, prevoutFetcher(contractPkScript, funding.Value)),
		funding.Value, prevoutFetcher(contractPkScript, funding.Value))
	if err != nil {
		return nil, fmt.Errorf("script engine: %w", err)
	}
	if err := engine.Execute(); err != nil {
		return nil, fmt.Errorf("built transaction does not satisfy the contract: %w", err)
	}
```

#### Validation

The parent traced the automatic session handler and all audit mutations. A second contract with the same redeem key, secret hash and admissible expiry but a different refund key passes validation and changes the P2SH address; the previously selected funding remains. Local spend validation uses the replacement script as its assumed prevout and therefore cannot detect the mismatch against the real chain output.

Validation method: static source trace

**Peer messages automatically invoke audit** — `ui/src/core/composables/useSession.ts:612-630`

An authenticated counterparty can resend contract messages; the handler applies the returned swap.

```typescript
      // board's own `pendingTrade` is.
      peerAddresses.value = {
        btc: (msg.btcAddr ?? '').trim(),
        znn: (msg.zenonAddr ?? '').trim(),
      }
      say('in', 'They sent where to pay them — it is filled into the new-swap form.')
      break
    case 'pkh':
      await applyValue(`their pubkey hash ${short(msg.pkhHex)}`, () =>
        api.counterparty(swapId.value, msg.pkhHex ?? ''),
      )
      break
    case 'contract':
      await applyValue(`their contract ${short(msg.contractHex)}`, () =>
        api.audit(swapId.value, msg.contractHex ?? ''),
      )
      break
    case 'zenon':
      await applyValue(`their Zenon HTLC ${short(msg.htlcId)}`, async () => {
```

**Overwrite contract without funding guard** — `wasm/manager.go:429-454`

The new valid script is stored without checking commitment state or rebinding the existing outpoint.

```go
	remaining := time.Until(time.Unix(details.LockTime, 0))
	if err := auditLegOrdering(sw, details.LockTime, remaining); err != nil {
		return nil, err
	}

	addr, err := ContractAddress(contract, params)
	if err != nil {
		return nil, err
	}
	sw.Contract = contract
	sw.ContractAddr = addr.String()
	sw.LockTime = details.LockTime
	sw.CounterpartyPKH = details.PkhRefund
	if sw.State == StateDraft {
		sw.State = StateAwaitingFunding
	}
	planZenonLeg(sw, time.Now().UTC())
	sw.log("audited counterparty contract %s: redeemable by our key, locktime %s (%s away), "+
		"which is the %s leg",
		sw.ContractAddr, time.Unix(details.LockTime, 0).UTC().Format(time.RFC3339),
		remaining.Truncate(time.Minute), legOwner(sw.BitcoinLegIsInitiators()))
	if sw.ZenonHtlcIsOurs() && sw.Zenon.ExpirationSeconds > 0 && sw.Zenon.HtlcID == "" {
		sw.log("create your Zenon HTLC with an expiry of %ds (%dh) so it lands on the correct "+
			"side of this contract's locktime",
			sw.Zenon.ExpirationSeconds, sw.Zenon.ExpirationHours)
	}
```

**Derive prevout script from mutable contract** — `wasm/txbuild.go:145-160`

The signer pairs the existing outpoint with a script inferred from the replacement contract.

```go
	fundingHash, err := chainhash.NewHashFromStr(funding.TxID)
	if err != nil {
		return nil, fmt.Errorf("bad funding txid: %w", err)
	}
	contractPkScript, err := contractPkScript(contract, params)
	if err != nil {
		return nil, err
	}

	newTx := func() *wire.MsgTx {
		tx := wire.NewMsgTx(txVersion)
		tx.LockTime = uint32(locktime)
		txIn := wire.NewTxIn(&wire.OutPoint{Hash: *fundingHash, Index: funding.Vout}, nil, nil)
		// CHECKLOCKTIMEVERIFY is only enforced when the input's sequence is not
		// final, so the refund path must not use the default 0xffffffff. Using
		// 0 for both paths keeps the two transactions the same size.
```

**Validate against the assumed script** — `wasm/txbuild.go:193-204`

The local engine validates the replacement rather than the real previous output, leaving rejection to the chain.

```go
	// Execute the script locally before handing the transaction to anyone.
	// A swap failing at broadcast time is recoverable; one that fails silently
	// is not.
	engine, err := txscript.NewEngine(contractPkScript, tx, 0, txscript.StandardVerifyFlags,
		txscript.NewSigCache(10), txscript.NewTxSigHashes(tx, prevoutFetcher(contractPkScript, funding.Value)),
		funding.Value, prevoutFetcher(contractPkScript, funding.Value))
	if err != nil {
		return nil, fmt.Errorf("script engine: %w", err)
	}
	if err := engine.Execute(); err != nil {
		return nil, fmt.Errorf("built transaction does not satisfy the contract: %w", err)
	}
```

Assertions:
- Once funds are committed, peer messages must not replace the script associated with the recorded funding outpoint.
- Remote corruption of a live swap's claim path, with loss of the victim's Zenon payment if the victim does not restore the original contract and claim before the Bitcoin refund deadline.

Limitations:
- No live wallet or chain exploit was run; supported Go build and isolated regressions were blocked by toolchain and offline dependency availability.
- Loss is conditional on the victim failing to restore the original script before the attacker can refund. Old recovery files or a retained transcript may permit recovery; this is not unconditional theft.

#### Dataflow

a validly authenticated peer's replacement contract message → redeem of the old funding outpoint using the new contract. Remote corruption of a live swap's claim path, with loss of the victim's Zenon payment if the victim does not restore the original contract and claim before the Bitcoin refund deadline.

- **Source:** a validly authenticated peer's replacement contract message

- **Sink:** redeem of the old funding outpoint using the new contract

- **Outcome:** Remote corruption of a live swap's claim path, with loss of the victim's Zenon payment if the victim does not restore the original contract and claim before the Bitcoin refund deadline.

**Peer messages automatically invoke audit** — `ui/src/core/composables/useSession.ts:612-630`

An authenticated counterparty can resend contract messages; the handler applies the returned swap.

```typescript
      // board's own `pendingTrade` is.
      peerAddresses.value = {
        btc: (msg.btcAddr ?? '').trim(),
        znn: (msg.zenonAddr ?? '').trim(),
      }
      say('in', 'They sent where to pay them — it is filled into the new-swap form.')
      break
    case 'pkh':
      await applyValue(`their pubkey hash ${short(msg.pkhHex)}`, () =>
        api.counterparty(swapId.value, msg.pkhHex ?? ''),
      )
      break
    case 'contract':
      await applyValue(`their contract ${short(msg.contractHex)}`, () =>
        api.audit(swapId.value, msg.contractHex ?? ''),
      )
      break
    case 'zenon':
      await applyValue(`their Zenon HTLC ${short(msg.htlcId)}`, async () => {
```

**Overwrite contract without funding guard** — `wasm/manager.go:429-454`

The new valid script is stored without checking commitment state or rebinding the existing outpoint.

```go
	remaining := time.Until(time.Unix(details.LockTime, 0))
	if err := auditLegOrdering(sw, details.LockTime, remaining); err != nil {
		return nil, err
	}

	addr, err := ContractAddress(contract, params)
	if err != nil {
		return nil, err
	}
	sw.Contract = contract
	sw.ContractAddr = addr.String()
	sw.LockTime = details.LockTime
	sw.CounterpartyPKH = details.PkhRefund
	if sw.State == StateDraft {
		sw.State = StateAwaitingFunding
	}
	planZenonLeg(sw, time.Now().UTC())
	sw.log("audited counterparty contract %s: redeemable by our key, locktime %s (%s away), "+
		"which is the %s leg",
		sw.ContractAddr, time.Unix(details.LockTime, 0).UTC().Format(time.RFC3339),
		remaining.Truncate(time.Minute), legOwner(sw.BitcoinLegIsInitiators()))
	if sw.ZenonHtlcIsOurs() && sw.Zenon.ExpirationSeconds > 0 && sw.Zenon.HtlcID == "" {
		sw.log("create your Zenon HTLC with an expiry of %ds (%dh) so it lands on the correct "+
			"side of this contract's locktime",
			sw.Zenon.ExpirationSeconds, sw.Zenon.ExpirationHours)
	}
```

**Derive prevout script from mutable contract** — `wasm/txbuild.go:145-160`

The signer pairs the existing outpoint with a script inferred from the replacement contract.

```go
	fundingHash, err := chainhash.NewHashFromStr(funding.TxID)
	if err != nil {
		return nil, fmt.Errorf("bad funding txid: %w", err)
	}
	contractPkScript, err := contractPkScript(contract, params)
	if err != nil {
		return nil, err
	}

	newTx := func() *wire.MsgTx {
		tx := wire.NewMsgTx(txVersion)
		tx.LockTime = uint32(locktime)
		txIn := wire.NewTxIn(&wire.OutPoint{Hash: *fundingHash, Index: funding.Vout}, nil, nil)
		// CHECKLOCKTIMEVERIFY is only enforced when the input's sequence is not
		// final, so the refund path must not use the default 0xffffffff. Using
		// 0 for both paths keeps the two transactions the same size.
```

**Validate against the assumed script** — `wasm/txbuild.go:193-204`

The local engine validates the replacement rather than the real previous output, leaving rejection to the chain.

```go
	// Execute the script locally before handing the transaction to anyone.
	// A swap failing at broadcast time is recoverable; one that fails silently
	// is not.
	engine, err := txscript.NewEngine(contractPkScript, tx, 0, txscript.StandardVerifyFlags,
		txscript.NewSigCache(10), txscript.NewTxSigHashes(tx, prevoutFetcher(contractPkScript, funding.Value)),
		funding.Value, prevoutFetcher(contractPkScript, funding.Value))
	if err != nil {
		return nil, fmt.Errorf("script engine: %w", err)
	}
	if err := engine.Execute(); err != nil {
		return nil, fmt.Errorf("built transaction does not satisfy the contract: %w", err)
	}
```

#### Reachability

The attack needs an established session, funded first contract, a second commitment and a timed replacement. Recovery using the original script may defeat the attempted loss.

- **Attacker:** A malicious Bitcoin initiator connected to the victim's swap session.

- **Entry point:** Peer messages automatically invoke audit

- **Outcome:** Remote corruption of a live swap's claim path, with loss of the victim's Zenon payment if the victim does not restore the original contract and claim before the Bitcoin refund deadline.

Limitations:
- Loss is conditional on the victim failing to restore the original script before the attacker can refund. Old recovery files or a retained transcript may permit recovery; this is not unconditional theft.

#### Severity

**Medium** — High impact and medium likelihood. The attack needs an established session, funded first contract, a second commitment and a timed replacement. Recovery using the original script may defeat the attempted loss.

Additional runtime or deployment evidence could raise or lower this severity.

Impact assessment:
- **Level:** high
- **Why:** Remote corruption of a live swap's claim path, with loss of the victim's Zenon payment if the victim does not restore the original contract and claim before the Bitcoin refund deadline.

Likelihood assessment:
- **Level:** medium
- **Why:** The attack needs an established session, funded first contract, a second commitment and a timed replacement. Recovery using the original script may defeat the attempted loss.

#### Remediation

Treat an identical retransmission as idempotent and reject changed contracts or participant keys after a funding broadcast, observed funding, or either-leg commitment. Bind each funding record to its actual scriptPubKey and validate that binding before signing.

Tests:
- Once either leg is committed, reject a different contract while permitting byte-identical retransmissions.
- Check that rejected re-audit leaves contract, funding, HTLC verification and refund artifacts unchanged.
- Before signing, compare the actual referenced funding output script with the frozen contract, including restored backups.

Preventive controls:
- Freeze the exact contract bytes and expected terms at commitment; treat identical session resends as idempotent.

<a id="finding-7"></a>

### [7] A crafted take timestamp disables the shared board inbox

| Field | Value |
| --- | --- |
| Severity | low |
| Confidence | high |
| Confidence rationale | Source traces event acceptance to the shared renderer; executing the exact date expression in isolation reproduced RangeError. Full encrypted delivery and browser rendering were not executed. |
| Category | denial-of-service |
| CWE | CWE-20, CWE-248 |
| Affected lines | ui/src/components/BoardInbox.vue:94, ui/src/core/nostr.ts:183-199, wasm/boardpost.go:842-871, ui/src/core/composables/useBoard.ts:321-332, ui/src/core/composables/useBoard.ts:172-178 |

#### Summary

A malicious configured relay can deliver a validly signed, encrypted take whose timestamp crashes date formatting in the shared board inbox. This blocks rendering of legitimate requests as well as the malformed one.

#### Root Cause

`OpenTake()` verifies the author's signature and decrypts the request, then returns `CreatedAt` unchanged. `useBoard` retains requests by post identity and dismissal state without validating time. `BoardInbox` calls `toISOString()` on every request's date during its shared render; values accepted by Go's `int64` can be outside JavaScript's supported Date range and throw.

**Relay frames enter application routing** — `ui/src/core/nostr.ts:183-199`

A configured relay can deliver an event regardless of client subscription filter semantics.

```
    sock.onmessage = (ev: MessageEvent) => {
      if (typeof ev.data !== 'string') return
      let frame: unknown
      try {
        frame = JSON.parse(ev.data)
      } catch {
        return
      }
      if (!Array.isArray(frame) || frame[0] !== 'EVENT') return
      const event = frame[2] as NostrEvent | undefined
      // A relay is free to send anything. Only the shape is checked here —
      // whether it is genuine is the module's business, and it checks the
      // signature over a hash it recomputes rather than one this file passed on.
      if (!event?.id || typeof event.content !== 'string') return
      if (this.seen.has(event.id)) return
      this.seen.add(event.id)
      this.onEvent?.(event)
```

**Signed and decrypted timestamp remains unbounded** — `wasm/boardpost.go:842-871`

The attacker can sign and encrypt its own take; these authenticity checks do not constrain its `created_at` value.

```
func OpenTake(identity *BoardIdentity, ev *NostrEvent) (*InboundTake, error) {
	if err := verifyEvent(ev); err != nil {
		return nil, err
	}
	if ev.Kind != takeKind {
		return nil, fmt.Errorf("event kind %d is not a ferry board take", ev.Kind)
	}
	if p := tagValue(ev, "p"); !strings.EqualFold(p, identity.PubKey) {
		return nil, errors.New("that take is addressed to somebody else")
	}
	priv, err := identity.PrivKey()
	if err != nil {
		return nil, err
	}
	secret, err := boardShared(priv, ev.PubKey)
	if err != nil {
		return nil, err
	}
	plain, err := openBox(secret, ev.Content)
	if err != nil {
		return nil, err
	}
	var take Take
	if err := json.Unmarshal(plain, &take); err != nil {
		return nil, fmt.Errorf("that take is not readable: %w", err)
	}
	if err := take.validate(); err != nil {
		return nil, err
	}
	return &InboundTake{Take: take, From: ev.PubKey, EventID: ev.ID, At: ev.CreatedAt}, nil
```

**Accepted take enters shared state** — `ui/src/core/composables/useBoard.ts:321-332`

The returned timestamp is stored with requests from all configured relays.

```
    if (seenTakes.has(ev.id)) return
    seenTakes.add(ev.id)
    // Already dealt with in an earlier session. Skipped before it is opened
    // rather than filtered after, because opening it is a call into the module
    // to decrypt something whose answer is already known.
    if (handled.value.includes(ev.id)) return
    try {
      const {take} = await api.boardReadTake(ev)
      // Newest first: an inbox where the answer to "who wants this" is at the
      // bottom is one that gets scrolled past.
      takes.value = [take, ...takes.value.filter((t) => t.eventId !== take.eventId)]
    } catch {
```

**Local post filter preserves malformed time** — `ui/src/core/composables/useBoard.ts:172-178`

An undismissed take naming an open or taken local post remains visible regardless of timestamp.

```
const inbox = computed(() =>
  takes.value.filter((t) => {
    if (handled.value.includes(t.eventId)) return false
    const post = mine.value.find((p) => p.post.id === t.take.postId)
    if (!post) return false
    return post.post.status === 'open' || post.post.status === 'taken'
  }),
```

**Unguarded date formatting throws** — `ui/src/components/BoardInbox.vue:92-96`

Formatting an out-of-range time throws while rendering the common inbox, before its request controls can be displayed.

```
        </p>
        <p class="font-mono text-xs text-muted-foreground/80">
          {{ new Date(t.at * 1000).toISOString().slice(0, 19).replace('T', ' ') }}
        </p>
      </div>
```

#### Validation

A Node vm evaluated the date expression extracted directly from `BoardInbox.vue`. Timestamp 0 produced 1970-01-01 00:00:00; timestamp 8640000000001 seconds produced RangeError: Invalid time value. Source inspection confirmed the signed/decrypted path can preserve that integer and the inbox has no per-request fallback.

Validation method: static source trace and isolated execution of the exact formatter

**Relay frames enter application routing** — `ui/src/core/nostr.ts:183-199`

A configured relay can deliver an event regardless of client subscription filter semantics.

```
    sock.onmessage = (ev: MessageEvent) => {
      if (typeof ev.data !== 'string') return
      let frame: unknown
      try {
        frame = JSON.parse(ev.data)
      } catch {
        return
      }
      if (!Array.isArray(frame) || frame[0] !== 'EVENT') return
      const event = frame[2] as NostrEvent | undefined
      // A relay is free to send anything. Only the shape is checked here —
      // whether it is genuine is the module's business, and it checks the
      // signature over a hash it recomputes rather than one this file passed on.
      if (!event?.id || typeof event.content !== 'string') return
      if (this.seen.has(event.id)) return
      this.seen.add(event.id)
      this.onEvent?.(event)
```

**Signed and decrypted timestamp remains unbounded** — `wasm/boardpost.go:842-871`

The attacker can sign and encrypt its own take; these authenticity checks do not constrain its `created_at` value.

```
func OpenTake(identity *BoardIdentity, ev *NostrEvent) (*InboundTake, error) {
	if err := verifyEvent(ev); err != nil {
		return nil, err
	}
	if ev.Kind != takeKind {
		return nil, fmt.Errorf("event kind %d is not a ferry board take", ev.Kind)
	}
	if p := tagValue(ev, "p"); !strings.EqualFold(p, identity.PubKey) {
		return nil, errors.New("that take is addressed to somebody else")
	}
	priv, err := identity.PrivKey()
	if err != nil {
		return nil, err
	}
	secret, err := boardShared(priv, ev.PubKey)
	if err != nil {
		return nil, err
	}
	plain, err := openBox(secret, ev.Content)
	if err != nil {
		return nil, err
	}
	var take Take
	if err := json.Unmarshal(plain, &take); err != nil {
		return nil, fmt.Errorf("that take is not readable: %w", err)
	}
	if err := take.validate(); err != nil {
		return nil, err
	}
	return &InboundTake{Take: take, From: ev.PubKey, EventID: ev.ID, At: ev.CreatedAt}, nil
```

**Accepted take enters shared state** — `ui/src/core/composables/useBoard.ts:321-332`

The returned timestamp is stored with requests from all configured relays.

```
    if (seenTakes.has(ev.id)) return
    seenTakes.add(ev.id)
    // Already dealt with in an earlier session. Skipped before it is opened
    // rather than filtered after, because opening it is a call into the module
    // to decrypt something whose answer is already known.
    if (handled.value.includes(ev.id)) return
    try {
      const {take} = await api.boardReadTake(ev)
      // Newest first: an inbox where the answer to "who wants this" is at the
      // bottom is one that gets scrolled past.
      takes.value = [take, ...takes.value.filter((t) => t.eventId !== take.eventId)]
    } catch {
```

**Local post filter preserves malformed time** — `ui/src/core/composables/useBoard.ts:172-178`

An undismissed take naming an open or taken local post remains visible regardless of timestamp.

```
const inbox = computed(() =>
  takes.value.filter((t) => {
    if (handled.value.includes(t.eventId)) return false
    const post = mine.value.find((p) => p.post.id === t.take.postId)
    if (!post) return false
    return post.post.status === 'open' || post.post.status === 'taken'
  }),
```

**Unguarded date formatting throws** — `ui/src/components/BoardInbox.vue:92-96`

Formatting an out-of-range time throws while rendering the common inbox, before its request controls can be displayed.

```
        </p>
        <p class="font-mono text-xs text-muted-foreground/80">
          {{ new Date(t.at * 1000).toISOString().slice(0, 19).replace('T', ' ') }}
        </p>
      </div>
```

Assertions:
- No event timestamp range check occurs before inbox storage.
- A single invalid timestamp throws in the shared inbox render expression.

Limitations:
- No encrypted network delivery, Vue browser render, or default public relay policy was tested.
- A malicious configured relay is the supported delivery case; withdrawing the referenced post or removing that relay may restore the feature.

#### Dataflow

Relay EVENT → OpenTake → InboundTake.at → shared inbox → throwing toISOString.

- **Source:** attacker-selected signed created_at integer

- **Sink:** BoardInbox date formatting

- **Outcome:** Shared board inbox cannot render/update normally, including honest requests.

**Relay frames enter application routing** — `ui/src/core/nostr.ts:183-199`

A configured relay can deliver an event regardless of client subscription filter semantics.

```
    sock.onmessage = (ev: MessageEvent) => {
      if (typeof ev.data !== 'string') return
      let frame: unknown
      try {
        frame = JSON.parse(ev.data)
      } catch {
        return
      }
      if (!Array.isArray(frame) || frame[0] !== 'EVENT') return
      const event = frame[2] as NostrEvent | undefined
      // A relay is free to send anything. Only the shape is checked here —
      // whether it is genuine is the module's business, and it checks the
      // signature over a hash it recomputes rather than one this file passed on.
      if (!event?.id || typeof event.content !== 'string') return
      if (this.seen.has(event.id)) return
      this.seen.add(event.id)
      this.onEvent?.(event)
```

**Signed and decrypted timestamp remains unbounded** — `wasm/boardpost.go:842-871`

The attacker can sign and encrypt its own take; these authenticity checks do not constrain its `created_at` value.

```
func OpenTake(identity *BoardIdentity, ev *NostrEvent) (*InboundTake, error) {
	if err := verifyEvent(ev); err != nil {
		return nil, err
	}
	if ev.Kind != takeKind {
		return nil, fmt.Errorf("event kind %d is not a ferry board take", ev.Kind)
	}
	if p := tagValue(ev, "p"); !strings.EqualFold(p, identity.PubKey) {
		return nil, errors.New("that take is addressed to somebody else")
	}
	priv, err := identity.PrivKey()
	if err != nil {
		return nil, err
	}
	secret, err := boardShared(priv, ev.PubKey)
	if err != nil {
		return nil, err
	}
	plain, err := openBox(secret, ev.Content)
	if err != nil {
		return nil, err
	}
	var take Take
	if err := json.Unmarshal(plain, &take); err != nil {
		return nil, fmt.Errorf("that take is not readable: %w", err)
	}
	if err := take.validate(); err != nil {
		return nil, err
	}
	return &InboundTake{Take: take, From: ev.PubKey, EventID: ev.ID, At: ev.CreatedAt}, nil
```

**Accepted take enters shared state** — `ui/src/core/composables/useBoard.ts:321-332`

The returned timestamp is stored with requests from all configured relays.

```
    if (seenTakes.has(ev.id)) return
    seenTakes.add(ev.id)
    // Already dealt with in an earlier session. Skipped before it is opened
    // rather than filtered after, because opening it is a call into the module
    // to decrypt something whose answer is already known.
    if (handled.value.includes(ev.id)) return
    try {
      const {take} = await api.boardReadTake(ev)
      // Newest first: an inbox where the answer to "who wants this" is at the
      // bottom is one that gets scrolled past.
      takes.value = [take, ...takes.value.filter((t) => t.eventId !== take.eventId)]
    } catch {
```

**Local post filter preserves malformed time** — `ui/src/core/composables/useBoard.ts:172-178`

An undismissed take naming an open or taken local post remains visible regardless of timestamp.

```
const inbox = computed(() =>
  takes.value.filter((t) => {
    if (handled.value.includes(t.eventId)) return false
    const post = mine.value.find((p) => p.post.id === t.take.postId)
    if (!post) return false
    return post.post.status === 'open' || post.post.status === 'taken'
  }),
```

**Unguarded date formatting throws** — `ui/src/components/BoardInbox.vue:92-96`

Formatting an out-of-range time throws while rendering the common inbox, before its request controls can be displayed.

```
        </p>
        <p class="font-mono text-xs text-muted-foreground/80">
          {{ new Date(t.at * 1000).toISOString().slice(0, 19).replace('T', ' ') }}
        </p>
      </div>
```

#### Reachability

Requires a live local post and event delivery by a malicious or permissive configured relay. The attacker needs only its own key plus the public post and recipient identity.

- **Attacker:** A malicious configured relay, or an arbitrary board participant able to have one configured relay deliver its event. The attacker needs only its own Nostr key and the victim's public board key and open/taken post ID; it does not need the victim's private key, a wallet proof, or a previously shared session code.

- **Entry point:** configured relay's signed take events

- **Outcome:** Board inbox availability loss

Limitations:
- No funds-loss claim.

#### Severity

**Low** — Feature-level availability impact with a malicious or permissive configured relay prerequisite. The source supports no fund loss or key disclosure, and honest default relay acceptance is unverified.

Additional runtime or deployment evidence could raise or lower this severity.

Impact assessment:
- **Level:** medium
- **Why:** One bad request interferes with legitimate requests from all relays, but no coin movement or key access is established.

Likelihood assessment:
- **Level:** medium
- **Why:** External relay timestamp policy may block public delivery; a malicious configured relay can still provide the event.

#### Remediation

Reject unsupported or implausible event timestamps in the take read boundary before adding requests to inbox state, and use a total date-formatting helper that returns a fallback for invalid dates. Add source-level regression coverage for a validly signed/encrypted take with an out-of-range created_at and for mixed valid/invalid inbox requests so malformed data cannot disable dismiss controls.

Tests:
- A valid signed/encrypted take outside the supported timestamp range must be rejected or safely displayed.
- A malformed take mixed with legitimate requests must not prevent accepting or dismissing legitimate requests.

Preventive controls:
- Validate numeric/time bounds at untrusted message entry and make rendering functions total with per-item error containment.

## Structural Hardening

The scan also produced derived, unsealed design guidance based on the complete finding collection. These proposals describe options and tradeoffs; they do not indicate that any finding has been remediated.

[Open the structural hardening portfolio](hardening/hardening.md)

## Reviewed Surfaces

| Surface | Risk Area | Outcome | Notes |
| --- | --- | --- | --- |
| Bitcoin and Zenon transaction engine | not recorded | Reported | Bitcoin HTLC parsing, funding, signing, recovery, Zenon verification/ABI/ledger, chain identity, transport and wallet synchronization reviewed. Four high and one medium protocol findings; expired Unlock publication remains deferred. |
| UI actions, recovery, wallets, and browser integration | not recorded | Reported | Manual and automatic actions, offers, sessions, imports/exports, browser storage, routing, wallet dispatch and recovery reviewed. CLI command injection reported; UI gates corroborate protocol findings. |
| Build, deployment, configuration, and lockfiles | not recorded | No issue found | Build/deploy scripts, workflow permissions, CSP, serving configuration, manifests and lock metadata reviewed. npm lock pins 318 dependencies, including nom-ui at a Git commit; Go checksums present. No dependency installation, advisory database lookup, production header verification, or vendored dependency source audit. |
| Go regression test sources | not recorded | No issue found | All 22 Go test files fully source-reviewed. Existing tests corroborate several missing conditions. None of these tests executed. |
| Development harnesses and documentation | not recorded | No issue found | All development scripts and Markdown/docs UI reviewed. No new tooling finding. Fixed-destination guarantees, storage-independent recovery, network/redirect claims and deployment branch instructions have source mismatches retained in threat model/observations. |
| Signed board discovery and encrypted sessions | not recorded | Reported | Signed-message authenticity and encryption reviewed; one low-severity shared inbox rendering issue reported. |
| Expired or missing Zenon HTLC before Unlock | not recorded | Needs follow-up | Source confirms stale verification can permit construction of secret-bearing Unlock after expiry or entry disappearance. External Syrius/go-zenon rejection and publication timing is not in the repository, so disclosure and funds loss are not established. |
| Bitcoin and Zenon transaction engine | not recorded | Needs follow-up | Focused engine investigation completed, with original candidates preserved. |
| UI actions, recovery, wallets, and browser integration | not recorded | Needs follow-up | Parent completed source review. |
| Build, deployment, configuration, and lockfiles | not recorded | No issue found | Current source and structured lockfile metadata inspected; no live advisories or dependency source audit implied. |
| Go regression test sources | not recorded | No issue found | All 22 Go test files reviewed; tests not executed. |
| Development harnesses and documentation | not recorded | No issue found | All scripts and docs plus informational UI pages reviewed. Documentation discrepancies retained. |
| Signed board discovery and encrypted sessions | not recorded | Needs follow-up | Completed source review; inbox timestamp candidate awaiting parent assessment. |

## Open Questions And Follow Up

- Does the supported Syrius/go-zenon path publish secret-bearing Unlock calldata when the HTLC is expired or missing, before rejecting the operation?
  - Follow-up prompt: Validate expired and missing HTLC Unlock publication with the supported wallet against local test chains; do not use real funds.
- What confirmation threshold and minimum remaining claim window should the product enforce for its supported transaction sizes?
- Source confirms stale verification can permit construction of secret-bearing Unlock after expiry or entry disappearance. External Syrius/go-zenon rejection and publication timing is not in the repository, so disclosure and funds loss are not established.
  - Follow-up prompt: Review deferred unit stale-zenon-unlock and close its stated proof gap.
- Original worker discovery payload preserved before parent validation and consolidation.
  - Follow-up prompt: Review deferred unit raw-baseline-result and close its stated proof gap.
- Original worker discovery payload preserved before parent validation and consolidation.
  - Follow-up prompt: Review deferred unit raw-engine-result and close its stated proof gap.

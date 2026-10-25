# Dependency maintenance

The UI uses a committed npm lockfile. Run `npm ci` from `ui/`, then
`npm run audit:dependencies`, `npm run test:solana`,
`npm run test:lint-config`, `npm run typecheck`, and `npm run lint`.
CI also builds and smoke-tests both application instances.

## Vite development server security

The declared minimum and committed Vite version are 7.3.7. This patch is listed
as a fixed version for the conditional development-server issues described in
[GHSA-rq7h-c2jc-7f22](https://github.com/vitejs/vite/security/advisories/GHSA-rq7h-c2jc-7f22),
[GHSA-9jrq-w75r-8gcw](https://github.com/vitejs/vite/security/advisories/GHSA-9jrq-w75r-8gcw)
and [GHSA-587h-7wmj-h5pm](https://github.com/vitejs/launch-editor/security/advisories/GHSA-587h-7wmj-h5pm).
The Node engine range is unchanged. The lockfile also records optional bundled
WASI metadata under the unchanged Tailwind Oxide package. These records do not
upgrade another existing dependency. Re-run the locked install, audits, browser
fixtures and both builds whenever this dependency tree changes. These upstream
advisories do not establish an incident in the deployed static site.

## Solana RPC compatibility

The application retains the legacy class-based transaction API on the maintained
1.x SDK line and updates `@solana/web3.js` to 1.99.0. Its `jayson` dependency
is overridden to exactly 5.0.0 **only under `@solana/web3.js`**. This is an
intentional compatibility override, outside the SDK's declared Jayson 4 range.

Jayson 5 removes `stream-json` and its `uuid` dependency. This removes the
dependency paths reported by GHSA-528h-pc64-c93x, GHSA-hqr4-qq8f-hg3x,
GHSA-mjw6-4jj6-33hc and GHSA-w5hq-g745-h8pq rather than suppressing their
audit results. Any remaining `uuid` dependency is separately locked to a
version outside that advisory's affected range.

The upstream [4.3.0 to 5.0.0 changes](https://github.com/tedeh/jayson/compare/v4.3.0...v5.0.0)
replace browser request-ID generation with native Web Crypto. The incompatible
TCP/TLS delimiter change does not affect Ferry's browser JSON-RPC client.
Node 20 or newer is required by Jayson 5; CI pins Node 24. The browser needs
`crypto.randomUUID` or `crypto.getRandomValues`, as covered by the fixtures.

The Solana checks bundle the actual application wallet module and browser SDK
with Vite and execute it without a global Buffer. They cover RPC success/error
handling, both ID-generation paths, restored fixture keys, independently
verified Ed25519 signatures, a fixed legacy transaction message, both injected
provider submission paths, required-signer rejection and declined signing.
RPC replies, providers, keys and confirmation results are synthetic. These
checks do not establish extension compatibility on a live chain or finality.

Recheck this override when upgrading the SDK or Jayson. Remove it when the
selected SDK declares a dependency range that already excludes these vulnerable
paths, and rerun the browser fixtures, audits and full CI. A migration to SDK v3
requires separate handling of its asynchronous signing and key APIs; it is not
needed to remove these dependency paths.

## TypeScript and Vue linting

ESLint 10 and `typescript-eslint` supply the supported flat presets directly.
The Vue essential preset uses `vue-eslint-parser` with the TypeScript parser,
and Vue scripts still have to declare `lang="ts"`. Existing application rule
overrides remain explicit in `ui/eslint.config.js`.

The former shared Vue config's filesystem discovery pulled in
`fast-glob -> micromatch -> braces`. The published braces advisory
[GHSA-vfj7-8cjw-p6xm](https://github.com/advisories/GHSA-vfj7-8cjw-p6xm)
has no patched version listed at the time of this change. Direct preset
composition removes that dependency chain without downgrading the shared config,
disabling correctness rules, or overriding braces to an unsupported substitute.
The lint fixtures check accepted TypeScript and Vue template syntax, unused
variables, explicit-any warnings, essential Vue rules, script language and
generated-runtime exclusions.

An audit reports the registry's known findings at the time it runs. A clean
audit and passing synthetic tests are component evidence, not production
acceptance.

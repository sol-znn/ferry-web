# Deploying

The build output is a directory and nothing else. There is no server component,
no database, no environment variable and no secret. Copying that directory onto
any static host is the entire deployment.

```sh
cd ui
npm install --allow-git=all
npm run build          # → ../dist       the production instance
npm run build:dev      # → ../dist-dev   the development instance
```

Requires **Go 1.27+** and **Node 22+** on PATH. `--allow-git=all` is needed
because `nom-ui` is a GitHub dependency and npm 12 refuses git specs by default.

---

## The two instances

There are two builds. They are the same code and look the same on screen; what
differs is which chains they assume and what they store their swaps under.

| | production | development |
|---|---|---|
| Build with | `npm run build` | `npm run build:dev` |
| Output | `dist/` | `dist-dev/` |
| Starts on | mainnet | regtest |
| Esplora default | the public instance | `http://127.0.0.1:3002` |
| Zenon default | **none, deliberately** | `http://127.0.0.1:35997` |
| Swaps stored under | `ferry.swap.*` | `ferry.dev.swap.*` |
| Settings stored under | `ferry.settings` | `ferry.dev.settings` |
| Header | nothing | an amber **DEV** badge |

The instance is decided at build time by one flag, which
`scripts/build.mjs` passes to **both** halves of the artefact: as
`-ldflags -X main.BuildEnv=…` on the Go module, and as a define on the page. It
is not a runtime setting — see the comment on `BuildEnv` in `wasm/env.go` for
why. Anything other than `dev` builds production, and the default with no flag
at all is production.

The two never share stored state, so both can be served from the same origin
without a regtest swap turning up in a mainnet list or a development session
repointing a configured browser. That is separation by construction rather than
by remembering to use different hosts. Recovery files and exports still move a
swap between them, which is the one crossing that should be deliberate.

If page and module ever disagree about which instance they are — which is
possible on a developer's machine, where `ui/public/ferry.wasm` may be left over
from the other build — the header says so in red and names the command to fix
it. Nothing else in this project can produce that state; a build always sets
both from one variable.

### Deploying them

GitHub Pages serves **one site per repository**, so the two cannot both be
published there. `.github/workflows/deploy.yml` builds and smoke-tests both on
every run, publishes production, and uploads the development build as a
downloadable artefact. To publish the development one instead, run the workflow
by hand from the Actions tab and set the `instance` input to `dev` — this
**replaces the production site at the same URL**, so it is a deliberate act and
never something a push does.

A permanently hosted development instance wants **its own origin**, not a path
on the production one. Storage is scoped to the origin, so a development build
sharing an origin with production is a page that can read production swap keys.
Its own repository, subdomain or host removes that. See [SECURITY.md](SECURITY.md).

---

## GitHub Pages

`.github/workflows/deploy.yml` does this on every push to `main`, and can be run
by hand from the Actions tab.

One-time setup:

1. **Settings → Pages → Source → GitHub Actions.** Not "Deploy from a branch" —
   the workflow publishes an artifact rather than committing to `gh-pages`.
2. Push to `main`. The workflow runs the Go tests, vets both build targets,
   typechecks and lints the UI, builds, runs the smoke test **against the module
   it is about to publish**, and then deploys.

The site lands at `https://<user>.github.io/<repo>/`. No base-path configuration
is needed: `base: './'` in the Vite config and hash routing mean the same build
works under a repo subpath, at a domain root, or from a local folder.

The workflow's push trigger is `main`. On a repository whose default branch is
`master`, either rename the branch or add it to the `branches:` list — otherwise
nothing publishes and the Actions tab shows no runs at all, which reads like a
broken workflow rather than one that was never triggered.

### The one thing worth changing

`<user>.github.io` is a single origin shared by **every** GitHub Pages project
that user publishes, and browser storage is scoped to the origin, not the path.
Any page on that origin can read the swap keys this app stores.

Serving from a **custom domain**, or from a **user site that hosts nothing
else**, removes that entirely. It is the single most valuable deployment choice
available. See [SECURITY.md](SECURITY.md).

---

## Other hosts

Anything that serves files works — Cloudflare Pages, Netlify, S3, nginx, IPFS,
`python -m http.server`. Three things to get right:

**Serve `.wasm` as `application/wasm`.** The loader compiles from bytes rather
than using `instantiateStreaming` precisely so a host with the wrong MIME type
still works, but the right type lets the browser cache and compile it better.

**Let it be compressed.** The module is 9.0 MB raw and 2.8 MB gzipped. Most CDNs
compress `application/wasm` by default. A host that does not will make first
loads three times heavier for no reason.

**Add the headers a meta CSP cannot set.** The page carries its own
Content-Security-Policy in a `<meta>` tag, but `frame-ancestors` is ignored in
meta form. A host that can set headers should add:

```
X-Frame-Options: DENY
Content-Security-Policy: frame-ancestors 'none'
```

Long-lived caching for `/static/*` is safe — every file there is content-hashed.
`index.html`, `ferry.wasm` and `wasm_exec.js` should be revalidated; the latter
two carry a `?v=<hash>` query so a changed deploy is fetched anyway.

---

## Running it locally

```sh
cd ui && npm run dev        # http://127.0.0.1:5173, the development instance
```

The dev server is plain **http** on purpose. A page served over https cannot
call an `http://` node — the browser blocks mixed content before the request
leaves — so a local Esplora on `127.0.0.1:3002` or a Zenon node on
`127.0.0.1:35997` is only reachable from a page that is itself on http. This is
also why the hosted production site cannot talk to a node on your own machine.

`npm run dev` compiles the development module first — about three seconds with a
warm Go cache — so the server can never end up serving the production module
against a development page. After changing anything under `wasm/`, rebuild it
without restarting Vite:

```sh
cd ui && npm run wasm:dev   # or npm run wasm, for the production module
```

To serve a built instance instead of the dev server:

```sh
cd ui && npm run preview        # dist/      on :4173
cd ui && npm run preview:dev    # dist-dev/  on :4174
```

Different ports, so the two are different origins and keep their storage apart
even locally.

---

## Running it offline

The **Recover** page reads no stored swap, contacts no node and needs no
settings. That makes a saved copy of `dist/` a complete offline rescue tool:

1. `npm run build`, copy `dist/` to a USB stick.
2. On the offline machine, serve it — any static server, including
   `python -m http.server` — and open `#/recover`.
3. Load a recovery file. It rebuilds and signs the spend locally and prints raw
   hex.
4. Carry the hex to a machine that has a network and broadcast it anywhere.

The key never touches a networked machine. This is the same guarantee
`ferry recover` gave as a CLI, which is why the page was written to keep it.

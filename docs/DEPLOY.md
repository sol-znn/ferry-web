# Deploying

The output is a directory: no server, no database, no environment variable, no
secret. Copying it onto a static host is the whole deployment.

```sh
cd ui
npm ci --allow-git=all        # install the committed dependency lock
npm run build                 # → ../dist       production
npm run build:dev             # → ../dist-dev   development
```

CI pins **Go 1.27.2** and **Node 24.22.0**. Use those versions on PATH to
reproduce its builds; the build copies the matching Go runtime shim.

## The two instances

| | production | development |
| --- | --- | --- |
| output | `dist/` | `dist-dev/` |
| starts on | mainnet | regtest |
| Esplora | a public instance | `http://127.0.0.1:3002` |
| Zenon | **none, deliberately** | `http://127.0.0.1:35997` |
| Solana | a public endpoint | `http://127.0.0.1:8899` |
| swaps under | `ferry.swap.*` | `ferry.dev.swap.*` |
| board tag | `ferry-board-v2` | `ferry-board-v2-dev` |
| post TTL | 24h | 15 min |
| header | nothing | an amber **DEV** badge |
| `window.__ferry` | absent | the test harness |

One build-time flag reaches **both** halves of the artefact — `-ldflags -X
main.BuildEnv=…` on the Go module and a define on the page — so a build cannot
come out half of each. Anything other than `dev` builds production. If the two
halves disagree (a stale `ui/public/ferry.wasm`), the header says so in red.

They never share stored state, so both can serve from one origin — but a
permanently hosted dev instance wants its own, since storage is per origin and a
dev build could otherwise read production swap keys.

## The Solana program

`scripts/build.mjs` bakes in a default from
`program/target/deploy/ferry_htlc-keypair.json` or `FERRY_SOL_PROGRAM`, and
leaves it blank with a warning when neither exists — the current state, so **a
Solana leg is refused until Nodes names a deployment**. It stays a *default*:
the address is a swap term, so changing it does not move an existing swap.

## Vercel

Project `ferry-web-v2` (`prj_nFfbg5ior1xmZuL7OAUO7tHlW8Tm`) at
**https://ferry-web-v2.vercel.app**.

```sh
node scripts/build.mjs
cd dist
cat > .vercel/project.json <<'EOF'
{"projectId":"prj_nFfbg5ior1xmZuL7OAUO7tHlW8Tm","orgId":"team_w6FFtJTdQQDpKm3EehzTnlfn","projectName":"ferry-web-v2"}
EOF
vercel deploy --prod --yes
```

Write `project.json` rather than running `vercel link`: link does not error on an
unknown name, it creates a new project and deploys a live site nobody meant to
make.

## GitHub Pages

`.github/workflows/deploy.yml` tests, builds, smoke-tests the module it is about
to publish, and deploys on push to `master`. Pull requests to `master` or
`feat/v2` run validation with read-only repository permissions. Pages staging,
upload and deployment are skipped for pull requests. Required branch checks
remain a maintainer setting. One-time: **Settings → Pages → Source
→ GitHub Actions**. Pages serves one site per repository, so the dev build is
uploaded as an artefact; running the workflow by hand with `instance: dev`
**replaces production at the same URL**.

## Anywhere else

`dist/` is plain files. Worth setting where you can: `Cache-Control: public,
max-age=31536000, immutable` on `/static/*` (those names carry a content hash;
`ferry.wasm` gets one in its query string), and a CSP *header*, which unlike the
meta policy already in the document can refuse framing.

It runs from `file://` too — the Recover page needs no network and no stored
swap, so a saved copy plus a recovery file rescues a stuck contract offline.

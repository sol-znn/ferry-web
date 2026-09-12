<script setup lang="ts">
import {ArrowDownIcon, CodeIcon, ServerCogIcon, ShieldIcon, TestTubeIcon} from '@lucide/vue'
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
  Heading,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from 'nom-ui'
import DataList from '@/components/DataList.vue'
import DataRow from '@/components/DataRow.vue'

// The developer's half of the documentation, for the reader docs/*.md is
// addressed to: someone about to self-host a copy, audit the threat model, or
// run the release checklist. It condenses those files rather than reproducing
// them — this ships in the bundle, so a stale fact here is one nobody catches
// by reading the repository next to it.

const S = {
  security: {id: 'security', title: 'Security model'},
  architecture: {id: 'architecture', title: 'Architecture'},
  testing: {id: 'testing', title: 'Testing a deployment'},
  deploy: {id: 'deploy', title: 'Deploying it yourself'},
} as const

const TOC = Object.values(S)

// Flex layers rather than an ASCII box diagram, which was unreadable in a <pre>
// on a phone: this reflows.
const STACK = [
  {title: 'browser tab: Vue UI', detail: null, next: 'ferryWasm.call'},
  {title: 'Go, in WebAssembly', detail: 'manager, signing, localStorage', next: 'fetch()'},
] as const
</script>

<template>
  <div class="grid gap-8 lg:grid-cols-[minmax(0,1fr)_12rem] lg:items-start lg:gap-10">
    <article class="grid min-w-0 gap-10">
      <header class="grid gap-3">
        <Heading :level="1" class="flex items-center gap-2.5 text-2xl">
          <CodeIcon class="size-5 text-muted-foreground" aria-hidden="true" />
          Under the hood
        </Heading>
        <p class="max-w-2xl text-sm leading-relaxed text-muted-foreground">
          <RouterLink to="/docs" class="text-primary underline-offset-4 hover:underline"
            >How Ferry works</RouterLink
          >
          is addressed to somebody mid-swap. This page is addressed to somebody reading the
          repository — running their own copy, auditing the threat model, or checking a deployment
          before other people trust it with money. Same facts, different reader.
        </p>
      </header>

      <!-- Security ---------------------------------------------------------- -->
      <section :id="S.security.id" class="grid scroll-mt-6 gap-4">
        <Heading :level="2" class="flex items-center gap-2 text-xl">
          <ShieldIcon class="size-4 text-muted-foreground" aria-hidden="true" />
          {{ S.security.title }}
        </Heading>
        <p class="text-sm leading-relaxed text-muted-foreground">
          What matters here is where the keys sit and who can reach them — not the cryptography,
          which is the same contract and the same verification wherever it runs.
        </p>

        <div class="grid gap-2 rounded-lg border border-border p-4 text-sm">
          <h3 class="text-sm font-semibold">What is still true</h3>
          <p class="text-muted-foreground">
            <strong>Ferry cannot steal your coins</strong> — a property of the contract, not of
            where the code runs. The per-swap key can satisfy exactly two branches of exactly one
            contract, both paying an address you supplied; funding goes straight from your wallet to
            a P2SH address Ferry never controls; the Zenon leg is signed in your own wallet, so
            Ferry only ever builds the block and reads the result.
          </p>
          <p class="text-muted-foreground">
            <strong>A malicious build could still steal your coins</strong> — true of any binary
            you've ever run. The difference is how you check: a binary you hash, a page you trust
            the host to serve what the repository builds. See
            <RouterLink :to="{name: 'under-the-hood', hash: '#security'}" class="underline"
              >trusting the deployment</RouterLink
            >
            below.
          </p>
        </div>

        <div class="grid gap-3">
          <h3 class="text-sm font-semibold">What the browser changed</h3>
          <DataList>
            <DataRow label="keys sit in localStorage, unencrypted">
              Scoped to an <em>origin</em>, not a directory — on a shared host like
              <span class="font-mono text-xs">&lt;user&gt;.github.io</span>, every other site that
              user publishes shares this storage. Readable by any script that executes on the
              origin. Erased by "clear site data", a private window closing, or a browser reinstall,
              with no warning. Mitigated by the pre-signed refund every recovery file carries, the
              Recover page (no store, no node, no network), and full export/import — but not yet
              encrypted at rest, which is the top open gap.
            </DataRow>
            <DataRow label="every chain call is a fetch() from a page">
              Bitcoin Core RPC is unreachable, so the private-node option is your own Esplora
              instance rather than your own Core node. A page on
              <span class="font-mono text-xs">https</span> cannot reach a node on
              <span class="font-mono text-xs">http</span> — mixed content is blocked before the
              request leaves.
            </DataRow>
            <DataRow label="no server to attack, none to protect you">
              There is no endpoint that can move coins — the signing code is a WebAssembly module
              reachable only by the page that loaded it, so there is nothing to authenticate and
              nothing reachable by anything else. What stands in for a server's guards is the
              same-origin policy and the CSP below.
            </DataRow>
            <DataRow label="the wallet is a separate program">
              The Syrius extension has its own node, chain and selected account, and this page can
              read all three but set none. A block signed by the wrong account is reclaimable only
              by that account; one signed for the wrong chain lands where the counterparty isn't
              looking. So no block is built unless a momentum hash read from both nodes matches, a
              check that couldn't run counts as a refusal, and the block that came back signed is
              compared field by field against the one this page proposed.
            </DataRow>
            <DataRow label="Content-Security-Policy">
              Shipped as a <span class="font-mono text-xs">&lt;meta&gt;</span> tag rather than a
              header, so a static host still enforces it — except
              <span class="font-mono text-xs">frame-ancestors</span>, which meta form ignores; a
              host that can set headers should add
              <span class="font-mono text-xs">X-Frame-Options: DENY</span>. Notably
              <span class="font-mono text-xs">script-src 'self' 'wasm-unsafe-eval'</span> (permits
              WebAssembly compilation and nothing else — no inline script, no
              <span class="font-mono text-xs">eval</span>) and
              <span class="font-mono text-xs">connect-src *</span> (a deliberate trade: a tight
              allowlist would mean the page choosing your node for you, and code that can already
              execute on this origin can read
              <span class="font-mono text-xs">localStorage</span> directly regardless).
            </DataRow>
          </DataList>
        </div>

        <div class="grid gap-2 rounded-lg border border-border p-4 text-sm">
          <h3 class="text-sm font-semibold">Trusting the deployment</h3>
          <p class="text-muted-foreground">
            The source is the artefact — CI builds <span class="font-mono text-xs">dist/</span> from
            the repository with no manual step. The build is <strong>not yet reproducible</strong>,
            so you cannot verify a deployed
            <span class="font-mono text-xs">ferry.wasm</span> against your own build by hash. Anyone
            can serve it themselves — <span class="font-mono text-xs">npm run build</span> and copy
            <span class="font-mono text-xs">dist/</span> — and the Recover page is the escape hatch
            that needs no host trusted at all: a copy on a USB stick recovers funds from a recovery
            file on an air-gapped machine.
          </p>
        </div>

        <div class="grid gap-2 rounded-lg border border-primary/40 bg-primary/5 p-4 text-sm">
          <h3 class="text-sm font-semibold">If you're moving real money</h3>
          <ul class="grid list-disc gap-1 pl-5 text-muted-foreground">
            <li>Download the recovery file the moment a swap is funded — not later.</li>
            <li>Export your swaps and keep the file where you keep other key material.</li>
            <li>Set your own Zenon node — there's no default, deliberately.</li>
            <li>
              Prefer a dedicated origin: a custom domain or a user site that hosts nothing else.
            </li>
            <li>Use a browser profile with few extensions, the way you would for a wallet.</li>
            <li>Check the contract address in two places — the card and a block explorer.</li>
            <li>
              Read the Zenon HTLC's token, not just its amount — two ZTS strings can differ in one
              character.
            </li>
          </ul>
        </div>
      </section>

      <!-- Architecture -------------------------------------------------------- -->
      <section :id="S.architecture.id" class="grid scroll-mt-6 gap-4 border-t border-border pt-8">
        <Heading :level="2" class="flex items-center gap-2 text-xl">
          <ServerCogIcon class="size-4 text-muted-foreground" aria-hidden="true" />
          {{ S.architecture.title }}
        </Heading>
        <p class="text-sm leading-relaxed text-muted-foreground">
          The design the swap rests on is three facts, none of them about the browser: funding an
          HTLC is an ordinary payment, spending out of one uses a throwaway per-swap key, and the
          initiator's leg expires last. Everything below follows from running that inside a page.
        </p>

        <div class="flex flex-col items-center gap-2">
          <div class="w-full max-w-xs overflow-hidden rounded-lg border border-border text-xs">
            <template v-for="(layer, i) in STACK" :key="layer.title">
              <div class="bg-muted/60 p-2.5" :class="i > 0 && 'border-t border-border'">
                <p class="font-medium">{{ layer.title }}</p>
                <p v-if="layer.detail" class="text-muted-foreground">{{ layer.detail }}</p>
              </div>
              <div class="flex items-center justify-center gap-1 py-1 text-muted-foreground">
                <ArrowDownIcon class="size-3" aria-hidden="true" />
                {{ layer.next }}
              </div>
            </template>
          </div>
          <p class="flex items-center gap-1 text-xs text-muted-foreground">
            <ArrowDownIcon class="size-3" aria-hidden="true" />
            Esplora, Zenon
          </p>
        </div>
        <p class="text-sm text-muted-foreground">
          Everything that can move a coin is Go; the UI decides what to show and what to ask, and
          never builds a transaction, parses a contract or holds a key. The boundary between them is
          one exported function —
          <span class="font-mono text-xs">window.ferryWasm.call(method, bodyJSON)</span> — over a
          table of <span class="font-mono text-xs">{name, JSON in, JSON out}</span>. Errors come
          back as <span class="font-mono text-xs">{"error": "..."}</span> rather than a rejected
          promise, so there is one error path rather than two.
        </p>

        <DataList dense>
          <DataRow label="calls don't block the page">
            Go's WASM runtime shares one thread with the page, so an exported call that blocked on a
            <span class="font-mono text-xs">fetch</span> would deadlock. It returns a promise
            immediately and does the work on a goroutine; everything below stays ordinary blocking
            Go.
          </DataRow>
          <DataRow label="panics are contained">
            A panic in Go/WASM tears down the module holding the only copy of an unsaved key. The
            bridge recovers and turns it into a failed call, so the page survives long enough to
            export.
          </DataRow>
          <DataRow label="localStorage, not IndexedDB">
            Deliberately: records are a couple of kilobytes written a handful of times per swap, so
            an asynchronous API would buy nothing and would cost synchronous
            <span class="font-mono text-xs">Save/Load/List</span>, which is what keeps the Go
            straight-line rather than built around promises. Export and import exist because browser
            storage can't be copied by hand.
          </DataRow>
          <DataRow label="settings ride on every call">
            A <span class="font-mono text-xs">Manager</span> is assembled per call from the settings
            that call carried, so changing a node URL takes effect on the very next one with nothing
            cached from before. The Zenon URL has <strong>no default</strong>: baking one in would
            make every user trust an endpoint they never chose, for the one check where a lying node
            costs money.
          </DataRow>
          <DataRow label="the Zenon leg, without a go-zenon dependency">
            An HTLC call is a send to an embedded contract carrying ABI-encoded arguments, so the
            page builds the block and the wallet signs it. go-zenon would pull in go-ethereum and
            end the size budget below, so the three methods are hand-rolled in
            <span class="font-mono text-xs">znn/htlcabi.go</span> and pinned byte for byte to
            vectors from go-zenon's own encoder.
          </DataRow>
        </DataList>

        <div class="grid gap-2 rounded-lg border border-border p-4 text-sm">
          <h3 class="text-sm font-semibold">Why fetch() instead of net/http</h3>
          <p class="text-muted-foreground">
            <span class="font-mono text-xs">chain/esplora.go</span> and
            <span class="font-mono text-xs">znn/client.go</span> use
            <span class="font-mono text-xs">httpx</span>, one function with two build-tagged
            implementations — <span class="font-mono text-xs">fetch()</span> under
            <span class="font-mono text-xs">js/wasm</span>,
            <span class="font-mono text-xs">net/http</span> everywhere else. This is the single
            largest size optimisation in the port:
          </p>
          <Table density="compact">
            <TableHeader>
              <TableRow>
                <TableHead>build</TableHead>
                <TableHead>raw</TableHead>
                <TableHead>gzipped</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow>
                <TableCell class="whitespace-normal text-muted-foreground"
                  >Go runtime baseline</TableCell
                >
                <TableCell class="whitespace-normal">2.0 MB</TableCell>
                <TableCell class="whitespace-normal">0.58 MB</TableCell>
              </TableRow>
              <TableRow>
                <TableCell class="whitespace-normal text-muted-foreground"
                  >whole app, over net/http</TableCell
                >
                <TableCell class="whitespace-normal">14.2 MB</TableCell>
                <TableCell class="whitespace-normal font-semibold">4.05 MB</TableCell>
              </TableRow>
              <TableRow>
                <TableCell class="whitespace-normal text-muted-foreground"
                  >whole app, over fetch()</TableCell
                >
                <TableCell class="whitespace-normal">9.5 MB</TableCell>
                <TableCell class="whitespace-normal font-semibold">2.9 MB</TableCell>
              </TableRow>
            </TableBody>
          </Table>
          <p class="text-muted-foreground">
            <span class="font-mono text-xs">net/http</span> unconditionally links
            <span class="font-mono text-xs">crypto/tls</span>,
            <span class="font-mono text-xs">crypto/x509</span> and the HTTP/2 stack, none of which
            execute under <span class="font-mono text-xs">GOOS=js</span> — the linker can't prune
            them because <span class="font-mono text-xs">http.Client</span> reaches them regardless.
            <span class="font-mono text-xs">btcutil</span> is the biggest thing left (1.35 MB) and
            it stays: address decoding across every Bitcoin address format is precisely the code
            where a hand-rolled reimplementation would be a bad trade.
          </p>
        </div>

        <DataList dense>
          <DataRow label="hash routes">
            A static host can't answer <span class="font-mono text-xs">/history</span> with the app
            — GitHub Pages 404s it, and copying <span class="font-mono text-xs">index.html</span> to
            <span class="font-mono text-xs">404.html</span> answers the link with an HTTP 404, which
            is a lie. <span class="font-mono text-xs">#/history</span> needs no host cooperation and
            works identically from a project page, a user page, or a copied folder.
          </DataRow>
          <DataRow label="one route outside the engine gate">
            Every page goes through the WebAssembly module except
            <span class="font-mono text-xs">#/docs</span>, which is prose the build already contains
            — so the failed-engine screen can still link to how to get your money out.
          </DataRow>
          <DataRow label="relative base, hashed wasm">
            <span class="font-mono text-xs">base: './'</span> lets one build work at a repo subpath,
            a domain root, or from a local folder.
            <span class="font-mono text-xs">ferry.wasm</span> and
            <span class="font-mono text-xs">wasm_exec.js</span> carry the module's content hash in
            their URL so a stale shim can never pair with a fresh module.
          </DataRow>
        </DataList>

        <div class="grid gap-3 sm:grid-cols-2">
          <div class="grid gap-1.5 rounded-lg border border-border p-3 text-sm">
            <h4 class="font-semibold">What a page can't do</h4>
            <ul class="grid list-disc gap-1 pl-4 text-muted-foreground">
              <li>Talk to Bitcoin Core RPC — no CORS, and credentials it can't safely hold</li>
              <li>Walk blocks, so there's no built-in explorer</li>
              <li>Reach an <span class="font-mono text-xs">http://</span> node from https</li>
              <li>
                Set response headers, so <span class="font-mono text-xs">frame-ancestors</span> has
                to come from the host
              </li>
            </ul>
          </div>
          <div class="grid gap-1.5 rounded-lg border border-border p-3 text-sm">
            <h4 class="font-semibold">What it does instead</h4>
            <ul class="grid list-disc gap-1 pl-4 text-muted-foreground">
              <li>This manual and the user's one at #/docs, in the bundle</li>
              <li>The Recover page — no store, no node, no settings</li>
              <li>Export/import, a network picker, a network-mismatch warning per card</li>
              <li>
                <span class="font-mono text-xs">smoke.mjs</span>,
                <span class="font-mono text-xs">wallet-provider.mjs</span> and
                <span class="font-mono text-xs">regtest-esplora.mjs</span>, covering the seams
              </li>
            </ul>
          </div>
        </div>
      </section>

      <!-- Testing -------------------------------------------------------- -->
      <section :id="S.testing.id" class="grid scroll-mt-6 gap-4 border-t border-border pt-8">
        <Heading :level="2" class="flex items-center gap-2 text-xl">
          <TestTubeIcon class="size-4 text-muted-foreground" aria-hidden="true" />
          {{ S.testing.title }}
        </Heading>
        <p class="text-sm leading-relaxed text-muted-foreground">
          A recipe for exercising a deployed Ferry the way a stranger with a Bitcoin wallet and some
          ZNN would — against the <strong>production instance</strong>, real chains, real wallets,
          because everything a unit test can reach already passes. Work through it in order; stage 0
          decides whether the rest is possible at all.
        </p>

        <Table density="compact">
          <TableHeader>
            <TableRow>
              <TableHead>Stage</TableHead>
              <TableHead>What it proves</TableHead>
              <TableHead>Costs</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow>
              <TableCell class="whitespace-normal">0 · Preflight</TableCell>
              <TableCell class="whitespace-normal text-muted-foreground"
                >A user can reach the nodes this needs</TableCell
              >
              <TableCell class="whitespace-normal">nothing</TableCell>
            </TableRow>
            <TableRow>
              <TableCell class="whitespace-normal">1 · Cold open</TableCell>
              <TableCell class="whitespace-normal text-muted-foreground"
                >The site works for someone who's never seen it</TableCell
              >
              <TableCell class="whitespace-normal">nothing</TableCell>
            </TableRow>
            <TableRow>
              <TableCell class="whitespace-normal">2 · Rehearsal</TableCell>
              <TableCell class="whitespace-normal text-muted-foreground"
                >A whole swap settles, two participants, on signet</TableCell
              >
              <TableCell class="whitespace-normal">nothing</TableCell>
            </TableRow>
            <TableRow>
              <TableCell class="whitespace-normal">3 · Production run</TableCell>
              <TableCell class="whitespace-normal text-muted-foreground"
                >It works where money is real</TableCell
              >
              <TableCell class="whitespace-normal">dust + fees</TableCell>
            </TableRow>
            <TableRow>
              <TableCell class="whitespace-normal">4 · Refund drill</TableCell>
              <TableCell class="whitespace-normal text-muted-foreground"
                >The timeout path, which nothing else has ever tested</TableCell
              >
              <TableCell class="whitespace-normal">fees</TableCell>
            </TableRow>
            <TableRow>
              <TableCell class="whitespace-normal">5 · Loss drills</TableCell>
              <TableCell class="whitespace-normal text-muted-foreground"
                >Recovery survives losing the browser</TableCell
              >
              <TableCell class="whitespace-normal">nothing</TableCell>
            </TableRow>
          </TableBody>
        </Table>

        <Accordion type="single" collapsible class="rounded-lg border border-border px-4">
          <AccordionItem value="preflight">
            <AccordionTrigger>Stage 0 — Preflight</AccordionTrigger>
            <AccordionContent class="grid gap-2 text-muted-foreground">
              <p>
                Ten minutes, no money. Every check is about reachability, not Ferry — a failure here
                means the swap will fail later, after money is committed.
              </p>
              <p>
                <strong>The Zenon node must answer JSON-RPC, over wss or https.</strong> Ferry's
                client dispatches on URL scheme: <span class="font-mono text-xs">wss://</span> goes
                over a pooled WebSocket, anything else over HTTP POST to port
                <span class="font-mono text-xs">35997</span>. Prefer wss — it's the URL people
                already have, and a WebSocket handshake isn't CORS-preflighted, so it reaches nodes
                an HTTP endpoint without CORS headers can't. Confirm with any WebSocket client
                before relying on it; a page on https can't reach a plaintext endpoint at all (mixed
                content), and an https endpoint needs
                <span class="font-mono text-xs">Access-Control-Allow-Origin</span> to be reachable
                from a browser.
              </p>
              <p>
                <strong>The extension and the page must agree.</strong> Point Syrius at the same
                node. The sync verdict on a card should read "same chain as this page"; anything
                else is the gate refusing to build a block, and it names which of the three settings
                disagreed. A wallet whose node this page can't reach can't be used at all — there is
                no override, because a node that can't be read looks exactly like one on somebody
                else's chain.
              </p>
              <p>
                Also confirm: the Esplora instance answers, the site serves
                <span class="font-mono text-xs">ferry.wasm</span> as
                <span class="font-mono text-xs">application/wasm</span> and gzipped, the origin
                isn't shared with unrelated sites (a
                <span class="font-mono text-xs">&lt;user&gt;.github.io</span> URL means stop and
                read the security section above), and the header's DEV badge matches the instance
                you mean to test.
              </p>
            </AccordionContent>
          </AccordionItem>

          <AccordionItem value="cold">
            <AccordionTrigger>Stage 1 — Cold open</AccordionTrigger>
            <AccordionContent class="grid gap-2 text-muted-foreground">
              <p>
                In a browser profile that's never seen the site: it loads and the header shows three
                status chips; Nodes shows a blank Zenon field with no placeholder (deliberate);
                Recover loads with no settings at all; a reload keeps your node settings; and a
                private window either works or plainly refuses rather than letting you create a swap
                whose refund key can't be saved.
              </p>
            </AccordionContent>
          </AccordionItem>

          <AccordionItem value="rehearsal">
            <AccordionTrigger>Stage 2 — Rehearsal on signet</AccordionTrigger>
            <AccordionContent class="grid gap-2 text-muted-foreground">
              <p>
                A complete swap, both legs, two participants, on chains where a mistake costs
                nothing — don't skip this before stage 3. Storage is per-origin, so "two
                participants" means two browser profiles or two machines, not two tabs.
              </p>
              <p>
                Run it once with Bitcoin initiating and once with Zenon initiating — they're
                different code paths. Deliberately break things along the way: audit a contract with
                one hex character changed (refused), verify an HTLC built with the wrong hash type
                (refused, the check that stops a SHA3 HTLC from becoming a loss), submit a wrong
                preimage (refused). Everything should settle from one secret, and both sides should
                end in History after archiving.
              </p>
            </AccordionContent>
          </AccordionItem>

          <AccordionItem value="production">
            <AccordionTrigger>Stage 3 — The production run</AccordionTrigger>
            <AccordionContent class="grid gap-2 text-muted-foreground">
              <p>
                Mainnet, real coins, smallest amount that isn't dust — only after stage 2 passes
                both directions. Fee estimation is one lookup with no bumping, so fund at a rate
                that confirms comfortably, not the minimum. Save the recovery file before funding is
                even confirmed. The initiator's leg genuinely runs ~48 hours — don't start one on a
                Friday.
              </p>
            </AccordionContent>
          </AccordionItem>

          <AccordionItem value="refund">
            <AccordionTrigger>Stage 4 — The refund drill</AccordionTrigger>
            <AccordionContent class="grid gap-2 text-muted-foreground">
              <p>
                The most valuable stage and the one most likely skipped because it's slow. On
                signet, with a short explicit timelock: fund, then have the counterparty do nothing.
                Confirm Refund is refused before the locktime and works after it — remembering a
                refund is only relayable once the locktime is behind the chain's median time past,
                which trails real time by roughly an hour.
              </p>
              <p>
                Then do it <strong>without the app</strong>: take the pre-signed refund hex from the
                recovery file and broadcast it through a block explorer's push page. This is the
                guarantee that Ferry disappearing doesn't cost anyone money, and the single most
                important thing this recipe checks.
              </p>
            </AccordionContent>
          </AccordionItem>

          <AccordionItem value="loss">
            <AccordionTrigger>Stage 5 — Loss drills</AccordionTrigger>
            <AccordionContent class="grid gap-2 text-muted-foreground">
              <p>
                Export and import across browser profiles; clear site data and recover from the
                export; copy <span class="font-mono text-xs">dist/</span> to a USB stick and load a
                recovery file on a machine with no network at all — it should rebuild and sign a
                spend and print raw hex, with no node, no settings and no stored swap.
              </p>
            </AccordionContent>
          </AccordionItem>
        </Accordion>
      </section>

      <!-- Deploy -------------------------------------------------------- -->
      <section :id="S.deploy.id" class="grid scroll-mt-6 gap-4 border-t border-border pt-8">
        <Heading :level="2" class="text-xl">{{ S.deploy.title }}</Heading>
        <p class="text-sm leading-relaxed text-muted-foreground">
          The build output is a directory and nothing else — no server, no database, no environment
          variable, no secret. Copying that directory onto any static host is the entire deployment.
        </p>
        <pre class="overflow-x-auto rounded-md bg-muted/60 p-3 font-mono text-xs leading-relaxed">
cd ui
npm install --allow-git=all
npm run build          # → ../dist       the production instance
npm run build:dev      # → ../dist-dev   the development instance</pre>
        <p class="text-sm text-muted-foreground">
          Requires Go 1.27+ and Node 22+.
          <span class="font-mono text-xs">--allow-git=all</span> is needed because a dependency is a
          GitHub spec and npm 12 refuses those by default.
        </p>

        <div class="grid gap-2 rounded-lg border border-border p-4 text-sm">
          <h3 class="text-sm font-semibold">Two instances, one flag</h3>
          <p class="text-muted-foreground">
            Same code, different assumptions — chosen at build time, baked into both the Go module
            and the page, and never a runtime setting.
          </p>
          <Table density="compact">
            <TableHeader>
              <TableRow>
                <TableHead />
                <TableHead>production</TableHead>
                <TableHead>development</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow>
                <TableCell class="whitespace-normal text-muted-foreground">Starts on</TableCell>
                <TableCell class="whitespace-normal">mainnet</TableCell>
                <TableCell class="whitespace-normal">regtest</TableCell>
              </TableRow>
              <TableRow>
                <TableCell class="whitespace-normal text-muted-foreground">Zenon default</TableCell>
                <TableCell class="whitespace-normal font-semibold">none, deliberately</TableCell>
                <TableCell class="whitespace-normal">http://127.0.0.1:35997</TableCell>
              </TableRow>
              <TableRow>
                <TableCell class="whitespace-normal text-muted-foreground"
                  >Swaps stored under</TableCell
                >
                <TableCell class="whitespace-normal font-mono text-xs">ferry.swap.*</TableCell>
                <TableCell class="whitespace-normal font-mono text-xs">ferry.dev.swap.*</TableCell>
              </TableRow>
            </TableBody>
          </Table>
          <p class="text-muted-foreground">
            The two never share stored state, so both can be served from the same origin without a
            regtest swap turning up in a mainnet list. GitHub Pages serves one site per repository,
            so publishing the dev instance there
            <strong>replaces production at the same URL</strong> — deliberate, and never something a
            plain push does.
          </p>
        </div>

        <DataList dense>
          <DataRow label="GitHub Pages">
            Settings → Pages → Source → GitHub Actions (not "deploy from a branch" — the workflow
            publishes an artefact). Every push to main runs Go tests, vets both build targets, lints
            the UI, builds, smoke-tests the module it's about to publish, and deploys. No base-path
            config needed.
          </DataRow>
          <DataRow label="the one thing worth changing">
            <span class="font-mono text-xs">&lt;user&gt;.github.io</span> is one origin shared by
            every project that user publishes, and storage is scoped to the origin, not the path. A
            custom domain, or a user site hosting nothing else, is the single most valuable
            deployment choice available.
          </DataRow>
          <DataRow label="other hosts">
            Anything that serves files works. Get three things right: serve
            <span class="font-mono text-xs">.wasm</span> as
            <span class="font-mono text-xs">application/wasm</span>, let it be compressed (9.5 MB
            raw, 2.9 MB gzipped), and add
            <span class="font-mono text-xs">X-Frame-Options: DENY</span> — the meta-tag CSP can't
            set <span class="font-mono text-xs">frame-ancestors</span>.
          </DataRow>
          <DataRow label="running locally">
            <span class="font-mono text-xs">cd ui && npm run dev</span> — plain http, on purpose,
            because a local node on <span class="font-mono text-xs">127.0.0.1</span> is only
            reachable from a page that's also on http. Rebuild the module after changing anything
            under <span class="font-mono text-xs">wasm/</span> with
            <span class="font-mono text-xs">npm run wasm:dev</span>.
          </DataRow>
          <DataRow label="running offline">
            The <RouterLink to="/recover" class="underline">Recover</RouterLink> page reads no
            stored swap, contacts no node, needs no settings — a saved copy of
            <span class="font-mono text-xs">dist/</span> on a USB stick is a complete offline rescue
            tool. Serve it on a networkless machine, load a recovery file, carry the resulting hex
            to a machine that can broadcast it. The key never touches a networked machine.
          </DataRow>
        </DataList>
      </section>
    </article>

    <nav
      class="row-start-1 grid gap-1 self-start lg:sticky lg:top-6 lg:row-start-auto"
      aria-label="On this page"
    >
      <p class="px-2 text-ledger text-muted-foreground">On this page</p>
      <RouterLink
        v-for="s in TOC"
        :key="s.id"
        :to="{name: 'under-the-hood', hash: `#${s.id}`}"
        class="rounded-md px-2 py-1 text-sm text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground"
      >
        {{ s.title }}
      </RouterLink>
      <RouterLink
        :to="{name: 'docs'}"
        class="mt-2 rounded-md border-t border-border px-2 pt-2 text-sm text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground"
      >
        ← How Ferry works
      </RouterLink>
    </nav>
  </div>
</template>

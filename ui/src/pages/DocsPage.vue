<script setup lang="ts">
import {
  BookOpenIcon,
  KeyRoundIcon,
  LifeBuoyIcon,
  ServerIcon,
  TriangleAlertIcon,
  WalletIcon,
} from '@lucide/vue'
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
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
import {STAGES} from '@/core/progress'
import {ZNN_CLI_MAX_HOURS, ZNN_CLI_URL} from '@/core/zenon-commands'
import {SYRIUS_EXTENSION_URL} from '@/core/zenon-wallet'
import {DEFAULT_ESPLORA, NETWORKS} from '@/core/composables/useSettings'

// The manual, in the app rather than beside it. `dist/` is the site and nothing
// else, so somebody who lands on a deployed copy has never seen a word of the
// repository's documents — and using this needs no clone and no toolchain.
//
// Written rather than rendered from those files: they address a reader of the
// repository, this addresses somebody mid-swap who wants to know why their HTLC
// was refused. Values that live in code — the stage names, the znn-cli cap, the
// per-network Esplora defaults — are imported from the modules that define them,
// so the manual cannot drift from the app it describes.

/** Section ids and titles in one place, so the contents list and the headings
 *  are the same list and a renamed section cannot leave a dead link behind. */
const S = {
  idea: {id: 'idea', title: 'One secret, two chains'},
  need: {id: 'what-you-need', title: 'What you need first'},
  steps: {id: 'steps', title: 'A swap, step by step'},
  amount: {id: 'amount', title: 'How much is worth swapping'},
  session: {id: 'session', title: 'Doing it live'},
  keys: {id: 'keys', title: 'Where your keys live'},
  recovery: {id: 'recovery', title: 'Getting your money back'},
  trouble: {id: 'trouble', title: 'When something goes wrong'},
  glossary: {id: 'glossary', title: 'Words used here'},
} as const

const TOC = Object.values(S)

/**
 * What each move of a swap actually involves.
 *
 * Keyed by the stage names in core/progress.ts rather than retyped, so this
 * fails to compile if the track on the cards ever gains, loses or renames a
 * step. The card says where you are; this says what that means.
 */
const STAGE_DETAIL: Record<(typeof STAGES)[number], string> = {
  'Set up':
    'Agree the trade between yourselves, then exchange two strings: your offer, and the pubkey ' +
    'hash from the other side. Whoever sends Bitcoin builds the contract out of them.',
  Fund:
    'The Bitcoin side pays the contract address from any wallet. Ferry watches for it and ' +
    'pre-signs the refund the moment it appears.',
  'Zenon leg':
    'The ZNN side creates the HTLC — a button with the Syrius extension, a printed command ' +
    'without it — and the other side verifies it against what was agreed: hashlock, parties, ' +
    'token, amount, expiry.',
  Claim:
    'One side spends with the secret, which publishes it. That is what lets the other side spend ' +
    'theirs. Neither party is ever holding both amounts.',
  Done:
    'Or refunded, which is the same machine ending safely: the timelocks passed and each side ' +
    'took back what they put in.',
}

const linkClass = 'text-primary underline-offset-4 hover:underline'
</script>

<template>
  <div class="grid gap-8 lg:grid-cols-[minmax(0,1fr)_12rem] lg:items-start lg:gap-10">
    <article class="grid min-w-0 gap-10">
      <!-- Lede ------------------------------------------------------------ -->
      <header class="grid gap-3">
        <Heading :level="1" class="flex items-center gap-2.5 text-2xl">
          <BookOpenIcon class="size-5 text-muted-foreground" aria-hidden="true" />
          How Ferry works
        </Heading>
        <p class="max-w-2xl text-sm leading-relaxed text-muted-foreground">
          Everything here describes the app in front of you. Nothing on this page is fetched and
          nothing is sent: like the rest of the site, it is a file your browser already has.
        </p>

        <!-- What state the software is in, first, because it decides whether
             the rest of the page is advice or a warning. The README says this
             plainly to somebody reading the repository; a person who only ever
             sees the deployed site is exactly who needs telling. -->
        <div class="flex items-start gap-2.5 rounded-lg border border-warning/40 bg-warning/5 p-3">
          <TriangleAlertIcon class="mt-0.5 size-4 shrink-0 text-warning" aria-hidden="true" />
          <div class="min-w-0 text-sm">
            <p class="font-semibold text-warning">This is a proof of concept.</p>
            <p class="mt-1 text-muted-foreground">
              A full swap has settled browser to browser on a local Bitcoin regtest chain and a
              Zenon devnet — both legs against one secret, and a wrong-hash HTLC refused. Nothing
              has run with real money on mainnet, no run has waited out a real expiry, and fee
              estimation is a single lookup with no bumping. See
              <RouterLink :to="{name: 'under-the-hood', hash: '#security'}" :class="linkClass">
                Under the hood</RouterLink
              >
              before you decide otherwise.
            </p>
          </div>
        </div>
      </header>

      <!-- The idea -------------------------------------------------------- -->
      <section :id="S.idea.id" class="grid scroll-mt-6 gap-4">
        <Heading :level="2" class="text-xl">{{ S.idea.title }}</Heading>

        <p class="text-sm leading-relaxed">
          A swap is two contracts, one on each chain, locked to the hash of the same secret.
          Spending either one with the secret publishes the secret, which is what lets the other
          side spend theirs. If nobody spends, both expire and each side takes back what they put
          in. There is no moment at which one party holds both amounts, and none at which this
          software could take either.
        </p>

        <p class="text-sm leading-relaxed">
          One asymmetry is worth carrying in your head: whoever invented the secret acts
          <em>after</em> watching the other side act, so their contract has to expire last. Ferry
          works out which chain that lands on, computes the expiry your Zenon HTLC needs, and
          refuses a counterparty contract whose locktime is on the wrong side of it — the
          <span class="text-ledger text-muted-foreground">leg ordering</span> line under a card's
          technical details names the result.
        </p>

        <Card>
          <CardHeader class="pb-2">
            <CardTitle class="text-sm">What runs where</CardTitle>
          </CardHeader>
          <CardContent>
            <DataList dense>
              <DataRow label="this tab">
                Builds the Bitcoin contract, signs every spend out of it, and checks the Zenon HTLC.
                That is all Go, compiled to WebAssembly — not Bitcoin signing rewritten in
                JavaScript.
              </DataRow>
              <DataRow label="your btc wallet">
                Never contacted and never asked for anything. You pay an address, the way you would
                pay anyone.
              </DataRow>
              <DataRow label="your zenon tooling">
                Creates and unlocks the Zenon HTLC. Both are signed operations, so they happen where
                your Zenon keys already are. Ferry only reads the result.
              </DataRow>
              <DataRow label="the nodes you name">
                Every chain query and every broadcast. Nothing else leaves the page, because there
                is no server behind it to leave for.
              </DataRow>
              <DataRow label="this browser">
                One key per swap, unencrypted, in local storage — see
                <RouterLink :to="{name: 'docs', hash: `#${S.keys.id}`}" :class="linkClass">
                  where your keys live</RouterLink
                >.
              </DataRow>
            </DataList>
          </CardContent>
        </Card>
      </section>

      <!-- What you need --------------------------------------------------- -->
      <section :id="S.need.id" class="grid scroll-mt-6 gap-4 border-t border-border pt-8">
        <Heading :level="2" class="text-xl">{{ S.need.title }}</Heading>
        <p class="text-sm text-muted-foreground">
          Ferry holds no Bitcoin key and no Zenon key, so two pieces of every swap are yours to
          supply. One of them is not what most people assume.
        </p>

        <Card>
          <CardHeader class="pb-2">
            <CardTitle class="flex items-center gap-2 text-sm">
              <WalletIcon class="size-4 text-muted-foreground" aria-hidden="true" />
              A Bitcoin wallet — any of them
            </CardTitle>
          </CardHeader>
          <CardContent class="grid gap-2 text-sm">
            <p>
              Funding a swap is an ordinary payment to an ordinary address — no script support, no
              PSBT, nothing to install. Hardware, mobile, desktop, or an exchange withdrawal all
              work.
            </p>
            <p class="text-muted-foreground">
              Payouts go to a receive address you give Ferry when you create the swap — legacy,
              P2SH, segwit and taproot are all fine. Both ways out of the contract pay that one
              address.
            </p>
          </CardContent>
        </Card>

        <Card class="border-primary/40">
          <CardHeader class="pb-2">
            <CardTitle class="flex items-center gap-2 text-sm">
              <WalletIcon class="size-4 text-primary" aria-hidden="true" />
              The Syrius extension — the recommended way to do the Zenon leg
            </CardTitle>
          </CardHeader>
          <CardContent class="grid gap-3 text-sm">
            <p>
              Install the
              <a
                :href="SYRIUS_EXTENSION_URL"
                target="_blank"
                rel="noreferrer noopener"
                :class="linkClass"
                >Syrius browser extension</a
              >
              (v0.3.1 or later) and the Zenon side is buttons. Create, unlock and reclaim each
              become one press: the page builds the account block, the extension shows it to you,
              signs it with its own key and publishes it through its own node. No terminal, and no
              key of yours ever reaches this page.
            </p>
            <p class="text-muted-foreground">
              Not to be confused with Syrius's own P2P swap screen, which is a ZNN-to-ZNN feature
              and cannot do a Bitcoin swap — wrong hash type, too short an expiry, and it will only
              unlock HTLCs it created itself. That screen is not what Ferry uses; the extension
              signs the block Ferry builds.
            </p>
            <p class="text-muted-foreground">
              Without the extension, every action is still printed as a
              <a :href="ZNN_CLI_URL" target="_blank" rel="noreferrer noopener" :class="linkClass"
                >znn-cli</a
              >
              command — but only up to {{ ZNN_CLI_MAX_HOURS }}h, which rules out every swap where
              Zenon is the initiating side and takes the long leg.
            </p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader class="pb-2">
            <CardTitle class="flex items-center gap-2 text-sm">
              <ServerIcon class="size-4 text-muted-foreground" aria-hidden="true" />
              Two nodes, chosen by you
            </CardTitle>
          </CardHeader>
          <CardContent class="grid gap-3 text-sm">
            <p>
              Open <strong>Nodes</strong> in the header. Funding detection, HTLC verification and
              the status-line heights are only as trustworthy as whoever answers them — a dishonest
              answer is the one failure here that costs money rather than time.
            </p>

            <div class="grid gap-1.5">
              <h4 class="text-sm font-semibold">Bitcoin — an Esplora base URL</h4>
              <p class="text-muted-foreground">
                Left blank it falls back to a public instance, which then sees every address you ask
                about. Your own keeps that to yourself.
              </p>
              <DataList dense>
                <DataRow v-for="n in NETWORKS" :key="n" :label="n">
                  <span class="font-mono text-xs break-all">{{ DEFAULT_ESPLORA[n] }}</span>
                </DataRow>
              </DataList>
            </div>

            <div class="grid gap-1.5">
              <h4 class="text-sm font-semibold">Zenon — a JSON-RPC URL, with no default</h4>
              <p class="text-muted-foreground">
                There is deliberately no default. Verifying a counterparty's HTLC is the one check
                where being lied to costs you the swap, so the node has to be one you picked rather
                than one this page picked for you. Until you set it the header says the Zenon side
                is off, and verification refuses rather than guessing.
              </p>
              <p>
                Either endpoint a node serves will do, and the scheme picks which:
                <span class="font-mono text-xs">wss://…:35998</span> for the WebSocket, or
                <span class="font-mono text-xs">https://…:35997</span> for HTTP JSON-RPC. They are
                the same JSON-RPC on the same node.
              </p>
              <p class="rounded-md border border-primary/40 bg-primary/5 p-2.5">
                <strong>Prefer the WebSocket.</strong> It is the endpoint Syrius and both CLIs
                already use, so it is the URL people pass around — and a WebSocket handshake is not
                CORS-preflighted, so it works against public nodes where the HTTP endpoint fails
                with an error the page cannot even read.
              </p>
            </div>

            <p class="text-muted-foreground">
              A node reached over <span class="font-mono text-xs">https</span> needs CORS headers,
              and a page on <span class="font-mono text-xs">https</span> cannot reach one on
              <span class="font-mono text-xs">http</span> at all — see
              <RouterLink :to="{name: 'docs', hash: `#${S.trouble.id}`}" :class="linkClass">
                when something goes wrong</RouterLink
              >
              if a node shows unreachable.
            </p>
          </CardContent>
        </Card>
      </section>

      <!-- Step by step ---------------------------------------------------- -->
      <section :id="S.steps.id" class="grid scroll-mt-6 gap-4 border-t border-border pt-8">
        <Heading :level="2" class="text-xl">{{ S.steps.title }}</Heading>
        <p class="text-sm text-muted-foreground">
          Every swap card carries the same five-segment track. What each segment means:
        </p>

        <ol class="grid gap-3">
          <li
            v-for="(s, i) in STAGES"
            :key="s"
            class="grid grid-cols-[auto_minmax(0,1fr)] items-start gap-x-3"
          >
            <span
              class="grid size-6 place-items-center rounded-full bg-muted font-mono text-xs tabular-nums"
              aria-hidden="true"
            >
              {{ i + 1 }}
            </span>
            <div class="min-w-0">
              <h3 class="text-sm font-semibold">{{ s }}</h3>
              <p class="text-sm text-muted-foreground">{{ STAGE_DETAIL[s] }}</p>
            </div>
          </li>
        </ol>

        <Heading :level="3" class="mt-2 text-base">Either side can start it</Heading>
        <p class="text-sm">
          The initiator generates the secret and takes the longer timelock, whichever chain that
          lands on. That's the whole difference between the two directions:
        </p>
        <Table density="compact">
          <TableHeader>
            <TableRow>
              <TableHead />
              <TableHead>Bitcoin initiates</TableHead>
              <TableHead>Zenon initiates</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow>
              <TableCell class="whitespace-normal text-muted-foreground">The long leg</TableCell>
              <TableCell class="whitespace-normal">the Bitcoin contract</TableCell>
              <TableCell class="whitespace-normal">the Zenon HTLC</TableCell>
            </TableRow>
            <TableRow>
              <TableCell class="whitespace-normal text-muted-foreground">
                The preimage surfaces on
              </TableCell>
              <TableCell class="whitespace-normal">
                Zenon, when the initiator unlocks the HTLC
              </TableCell>
              <TableCell class="whitespace-normal">
                Bitcoin, when the initiator redeems the contract
              </TableCell>
            </TableRow>
            <TableRow>
              <TableCell class="whitespace-normal text-muted-foreground">
                The ZNN sender must
              </TableCell>
              <TableCell class="whitespace-normal">paste the preimage in by hand</TableCell>
              <TableCell class="whitespace-normal">nothing — Ferry extracts it</TableCell>
            </TableRow>
            <TableRow>
              <TableCell class="whitespace-normal text-muted-foreground">
                Zenon leg creatable with
              </TableCell>
              <TableCell class="whitespace-normal">the Syrius extension, or znn-cli</TableCell>
              <TableCell class="whitespace-normal">
                the Syrius extension only — over znn-cli's {{ ZNN_CLI_MAX_HOURS }}h cap
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
        <p class="text-sm text-muted-foreground">
          None of which you have to reason about — the card says what to do next in one line, and
          whether it's your move.
        </p>

        <div class="grid gap-4 md:grid-cols-2">
          <Card>
            <CardHeader class="pb-2">
              <CardTitle class="text-sm">Sending BTC, receiving ZNN</CardTitle>
            </CardHeader>
            <CardContent>
              <ol class="grid list-decimal gap-2 pl-4 text-sm marker:text-muted-foreground">
                <li>
                  Create the swap. If you are starting it, Ferry generates the secret and commits to
                  its hash; if they started it, paste their offer and the form fills itself in.
                </li>
                <li>
                  Send them your <span class="font-mono text-xs">swapoffer1:</span> string. It is
                  public data — never the secret, never a key.
                </li>
                <li>Paste back the pubkey hash they return. Ferry builds the contract.</li>
                <li><strong>Pay the contract address from any wallet.</strong></li>
                <li>
                  Refresh. Ferry sees the funding and pre-signs your refund.
                  <strong>Download the recovery file now</strong> — the card will keep asking until
                  you do.
                </li>
                <li>
                  Verify their Zenon HTLC. Ferry finds it on chain, or you paste the id, and checks
                  the hashlock, both parties, the token, the amount, the expiry and the hash type.
                </li>
                <li>
                  If you hold the secret, unlock the Zenon HTLC to take the ZNN — that publishes the
                  preimage, which is how they claim the BTC. If you do not, wait for them to redeem
                  your contract; Refresh extracts the preimage from Bitcoin for you.
                </li>
              </ol>
            </CardContent>
          </Card>

          <Card>
            <CardHeader class="pb-2">
              <CardTitle class="text-sm">Receiving BTC, sending ZNN</CardTitle>
            </CardHeader>
            <CardContent>
              <ol class="grid list-decimal gap-2 pl-4 text-sm marker:text-muted-foreground">
                <li>
                  Decode their offer and create your swap. The terms it carries are locked — edit
                  one and Ferry refuses, because two records that disagree are two different swaps
                  and nothing on either chain would catch it.
                </li>
                <li>Send them your pubkey hash. They build and fund the contract.</li>
                <li>
                  Paste their contract hex and <strong>audit</strong> it. Ferry refuses it unless it
                  is genuinely redeemable by your key <em>and</em> its locktime sits on the correct
                  side of your Zenon leg. This check reaches no node — it works entirely offline.
                </li>
                <li>
                  Create your Zenon HTLC — a button with the Syrius extension, or the printed
                  command without it. Either way the expiry is the one Ferry computed from the
                  locktime it just audited.
                </li>
                <li>
                  If you hold the secret, redeem the BTC with it once the contract is funded. If you
                  do not, they unlock your Zenon HTLC first: copy the revealed preimage into the
                  card — unlocking deletes the entry, so Ferry cannot read it back — and then
                  redeem.
                </li>
              </ol>
            </CardContent>
          </Card>
        </div>
      </section>

      <!-- Keys ------------------------------------------------------------ -->
      <section :id="S.amount.id" class="grid scroll-mt-6 gap-4 border-t border-border pt-8">
        <Heading :level="2" class="text-lg">{{ S.amount.title }}</Heading>

        <p class="text-sm text-muted-foreground">
          A contract is emptied by one transaction whose fee is paid <em>out of the contract</em> —
          not out of the unlocking wallet. So the amount you agree is never the amount the recipient
          receives, and if the gap is large enough they receive nothing at all.
        </p>

        <div class="grid gap-2 rounded-lg border border-border p-4 text-sm">
          <h3 class="text-sm font-semibold">The arithmetic</h3>
          <p class="text-muted-foreground">
            Unlocking is about <span class="font-mono text-xs">323 vB</span> — bigger than an
            ordinary send, because the contract script and the 32-byte preimage travel inside it.
            Below the <span class="font-mono text-xs">546 sat</span> dust limit no node will relay
            the output, on either branch, so a contract under that line cannot be emptied by
            anybody.
          </p>
          <p class="text-muted-foreground">
            That puts a hard floor around <span class="font-mono text-xs">870 sat</span> at the
            network minimum fee rate, and a practical one above it — a contract sits for the length
            of its timelock, and fees may rise before it's spent. Every amount field quotes all
            three: what the recipient nets, the lowest that clears today, and one with headroom for
            fees to rise.
          </p>
          <p class="text-muted-foreground">
            Ferry will still build the spend at any rate the contract can afford, and a refused
            spend names the highest rate that would have worked.
          </p>
        </div>
      </section>

      <section :id="S.session.id" class="grid scroll-mt-6 gap-4 border-t border-border pt-8">
        <Heading :level="2" class="text-lg">{{ S.session.title }}</Heading>

        <p class="text-sm text-muted-foreground">
          Setting a swap up by hand means passing four long hex strings between two chat windows. A
          <strong>session</strong> moves them for you: one side presses "Start a session" and reads
          out the code, the other types it in, and the values arrive on their own.
        </p>

        <div class="grid gap-2 rounded-lg border border-primary/40 bg-primary/5 p-4 text-sm">
          <h3 class="text-sm font-semibold">Nothing that arrives is trusted</h3>
          <p class="text-muted-foreground">
            Every value goes through the same check it would if pasted by hand — a contract audited
            against your locktime ordering, a pubkey hash matched, an HTLC id verified against your
            node. Anything that doesn't match is refused and the swap is left as it was. A session
            removes the typing, not the checking.
          </p>
          <p class="text-muted-foreground">
            The preimage is never sent, and cannot be — there is no field for it. Revealing it early
            would hand the other side both legs of the swap.
          </p>
        </div>

        <div class="grid gap-2 rounded-lg border border-border p-4 text-sm">
          <h3 class="text-sm font-semibold">What the relay sees</h3>
          <p class="text-muted-foreground">
            Messages travel over public
            <a
              href="https://nostr.com"
              target="_blank"
              rel="noreferrer noopener"
              class="text-primary underline-offset-4 hover:underline"
              >Nostr</a
            >
            relays — the only free, redundant, no-signup network a page with no backend can use.
            Several are used at once, so one dropping messages doesn't end a session, and you can
            name your own under Nodes.
          </p>
          <p class="text-muted-foreground">
            A relay holds ciphertext under a pseudonymous key, both derived from the session code
            inside your browser — the code itself never leaves it, so a relay can carry a
            conversation it can neither read nor join.
          </p>
          <p class="text-warning">
            <strong>Treat the code like a password.</strong> Anyone holding it can read the session
            and post into it — send it the way you'd send a password, and use a fresh one per swap.
            Even then, a stranger can only offer values your own checks will refuse — but they can
            watch.
          </p>
        </div>
      </section>

      <section :id="S.keys.id" class="grid scroll-mt-6 gap-4 border-t border-border pt-8">
        <Heading :level="2" class="flex items-center gap-2 text-xl">
          <KeyRoundIcon class="size-4 text-muted-foreground" aria-hidden="true" />
          {{ S.keys.title }}
        </Heading>

        <p class="text-sm leading-relaxed">
          Each swap gets a keypair of its own, generated in this page and sent nowhere. It can
          satisfy exactly two branches of exactly one contract, both paying the address you supplied
          — worth very little to anybody but you, and everything to you. It lives in this browser's
          local storage, unencrypted. That's the sharpest edge this port has, and it's worth four
          lines. See
          <RouterLink :to="{name: 'under-the-hood', hash: '#security'}" :class="linkClass">
            Under the hood</RouterLink
          >
          for the full threat model.
        </p>

        <DataList>
          <DataRow label="scoped to an origin">
            Not to a directory — every page on the same origin can read all of it. On a shared host
            such as a GitHub Pages account, that's every other site the same user publishes there.
          </DataRow>
          <DataRow label="readable by any script">
            An injected script, a compromised dependency, a browser extension with host access — all
            can read it. The page's Content-Security-Policy is the main defence, and not a complete
            one.
          </DataRow>
          <DataRow label="erased casually">
            "Clear site data" — the thing people click to fix an unrelated site — deletes it. So
            does a private window closing, a browser reinstall, or storage-pressure eviction. No
            warning, no undo, and nothing backs it up automatically.
          </DataRow>
          <DataRow label="not encrypted yet">
            Passphrase encryption of the stored records is the top open gap. Until it lands, treat
            this browser profile the way you'd treat a wallet file.
          </DataRow>
          <DataRow label="or deleted on purpose">
            History rows have a delete button, and it is the only irreversible thing on that page —
            it destroys the swap's key along with the record. Ferry refuses for any swap it has not
            seen redeemed or refunded, and offers the recovery file before it deletes anything.
          </DataRow>
        </DataList>

        <Card class="border-primary/40">
          <CardHeader class="pb-2">
            <CardTitle class="text-sm">So: three things to save, and when</CardTitle>
          </CardHeader>
          <CardContent>
            <DataList dense>
              <DataRow label="recovery file">
                One per swap, the moment funding is seen — not later. It carries the contract, the
                key, the funding output and an already-signed refund.
              </DataRow>
              <DataRow label="full export">
                Every swap in this browser as one document, from the Backup panel at the foot of the
                Swaps page. This is how you move to another machine.
              </DataRow>
              <DataRow label="the site itself">
                Optional, and the reason the Recover page needs nothing else: a saved copy of these
                files opens from disk on a machine that has never been online.
              </DataRow>
            </DataList>
          </CardContent>
        </Card>

        <p class="text-sm text-warning">
          Both files can spend the contracts they name. They are private keys with some JSON around
          them — keep them where you keep other key material, not in a chat window.
        </p>
      </section>

      <!-- Recovery -------------------------------------------------------- -->
      <section :id="S.recovery.id" class="grid scroll-mt-6 gap-4 border-t border-border pt-8">
        <Heading :level="2" class="flex items-center gap-2 text-xl">
          <LifeBuoyIcon class="size-4 text-muted-foreground" aria-hidden="true" />
          {{ S.recovery.title }}
        </Heading>
        <p class="text-sm text-muted-foreground">
          Three paths, in increasing order of effort. None of them depends on this site still
          existing.
        </p>

        <ol class="grid gap-3">
          <li class="rounded-lg border border-border p-3">
            <h3 class="text-sm font-semibold">1 · Broadcast the pre-signed refund</h3>
            <p class="mt-1 text-sm text-muted-foreground">
              Produced the instant funding is seen, and included in every recovery file. Once the
              locktime has passed, paste it into any broadcast form — an explorer's push page, your
              own node, anything. No decision has to be made.
            </p>
          </li>
          <li class="rounded-lg border border-border p-3">
            <h3 class="text-sm font-semibold">
              2 · Rebuild it on the
              <RouterLink to="/recover" :class="linkClass">Recover</RouterLink> page
            </h3>
            <p class="mt-1 text-sm text-muted-foreground">
              Load a recovery file and rebuild the spend at a fee rate you pick, to an address you
              pick, or as a redeem if the preimage turned up. It reads no stored swap, contacts no
              node and needs no settings — the usual reason to end up here is fees rising past what
              the pre-signed refund pays.
            </p>
          </li>
          <li class="rounded-lg border border-border p-3">
            <h3 class="text-sm font-semibold">3 · Import an export somewhere else</h3>
            <p class="mt-1 text-sm text-muted-foreground">
              The full export is one JSON document, and any browser running this site can import it:
              swaps, keys, history and all. This is the answer to "how do I move machines", since
              browser storage can't be copied by hand.
            </p>
          </li>
        </ol>
      </section>

      <!-- Troubleshooting ------------------------------------------------- -->
      <section :id="S.trouble.id" class="grid scroll-mt-6 gap-4 border-t border-border pt-8">
        <Heading :level="2" class="text-xl">{{ S.trouble.title }}</Heading>
        <p class="text-sm text-muted-foreground">
          The failures that actually happen, and what each one means.
        </p>

        <Accordion type="single" collapsible class="rounded-lg border border-border px-4">
          <AccordionItem value="engine">
            <AccordionTrigger>The signing engine did not load</AccordionTrigger>
            <AccordionContent class="grid gap-2 text-muted-foreground">
              <p>
                The WebAssembly module is a few megabytes and the fetch was interrupted, or the host
                served it as the wrong content type. Reload — it's cached after the first success.
              </p>
              <p>
                Nothing is lost while it's down. Your swaps are still in this browser, merely
                unreadable until the engine is running — which is why this page works without it.
              </p>
            </AccordionContent>
          </AccordionItem>

          <AccordionItem value="unreachable">
            <AccordionTrigger>A chip in the header says "unreachable"</AccordionTrigger>
            <AccordionContent class="grid gap-2 text-muted-foreground">
              <p>
                The browser won't say why a cross-origin request failed — down, bad certificate, and
                missing CORS all look the same — so check:
              </p>
              <ul class="grid list-disc gap-1 pl-5">
                <li>Is the URL right, with a scheme?</li>
                <li>
                  For Zenon, does the port match the scheme —
                  <span class="font-mono text-xs">wss://…:35998</span> or
                  <span class="font-mono text-xs">https://…:35997</span>? Crossing them fails either
                  way.
                </li>
                <li>
                  Over <span class="font-mono text-xs">https</span>, does the node send
                  <span class="font-mono text-xs">Access-Control-Allow-Origin</span>? Most public
                  nodes don't — switch to <span class="font-mono text-xs">wss://</span>, which needs
                  none.
                </li>
                <li>
                  Is this page on <span class="font-mono text-xs">https</span> while the node is on
                  <span class="font-mono text-xs">http</span>? Mixed content is blocked before the
                  request is sent, so a node on
                  <span class="font-mono text-xs">127.0.0.1</span> needs the page served over
                  <span class="font-mono text-xs">http</span> too.
                </li>
              </ul>
            </AccordionContent>
          </AccordionItem>

          <AccordionItem value="htlc">
            <AccordionTrigger>Their Zenon HTLC was rejected</AccordionTrigger>
            <AccordionContent class="grid gap-2 text-muted-foreground">
              <p>
                That's the check working. Ferry refuses an HTLC that commits to the wrong hash, pays
                the wrong party, holds a token nobody agreed to, underpays, is about to expire,
                hashes with SHA3 rather than SHA-256, or sits on the wrong side of the Bitcoin
                locktime. The message names which — and a check it couldn't perform (an unparseable
                amount, say) is never reported as one that passed.
              </p>
              <p class="text-warning">
                Do not fund anything and do not create your own HTLC against a rejected one. Ask the
                counterparty to make it again with the exact values the card prints.
              </p>
            </AccordionContent>
          </AccordionItem>

          <AccordionItem value="network">
            <AccordionTrigger>A card's buttons are switched off</AccordionTrigger>
            <AccordionContent class="grid gap-2 text-muted-foreground">
              <p>
                A swap keeps the network it was created on. While that disagrees with the network
                set under <strong>Nodes</strong>, everything that reaches a chain is disabled —
                every address, fee lookup and broadcast would go to the wrong one. Switch back and
                the card comes alive.
              </p>
              <p>
                Auditing a contract and producing the offer string still work either way — both are
                pure, with no node involved.
              </p>
            </AccordionContent>
          </AccordionItem>

          <AccordionItem value="notyet">
            <AccordionTrigger>"Not valid for another …" on a refund</AccordionTrigger>
            <AccordionContent class="grid gap-2 text-muted-foreground">
              <p>
                <span class="font-mono text-xs">OP_CHECKLOCKTIMEVERIFY</span> is checked against the
                block median time past, which trails real time by roughly an hour — so a refund
                becomes relayable a little after the card's timestamp, not at it. Save the hex and
                retry; it doesn't go stale.
              </p>
            </AccordionContent>
          </AccordionItem>

          <AccordionItem value="short">
            <AccordionTrigger>The funding is flagged as short</AccordionTrigger>
            <AccordionContent class="grid gap-2 text-muted-foreground">
              <p>
                The contract holds less than the agreed amount — usually a wallet that took its fee
                out of the send rather than adding it on top. It's still spendable; what's broken is
                the trade, so settle that with the counterparty before either side gives up
                something in exchange for it.
              </p>
            </AccordionContent>
          </AccordionItem>

          <AccordionItem value="dust">
            <AccordionTrigger>
              "after a … fee the output would be … at or below the dust limit"
            </AccordionTrigger>
            <AccordionContent class="grid gap-2 text-muted-foreground">
              <p>
                The contract doesn't hold enough to pay the fee and still leave a spendable output —
                not a signing failure, the transaction would build correctly and no node would relay
                it.
              </p>
              <p>
                The message names the highest fee rate this contract can afford. Put that into the
                fee field on the Recover page — the button there fills it in — and the same spend
                will build, just slower to confirm.
              </p>
              <p>
                Where no rate works at all, the amount locked was below the floor in
                <RouterLink
                  :to="{name: 'docs', hash: '#amount'}"
                  class="text-primary underline-offset-4 hover:underline"
                  >How much is worth swapping</RouterLink
                >, and the coins can't be moved by either branch. Nothing to retry.
              </p>
            </AccordionContent>
          </AccordionItem>

          <AccordionItem value="htlcgone">
            <AccordionTrigger>"data non existent" when verifying a Zenon HTLC</AccordionTrigger>
            <AccordionContent class="grid gap-2 text-muted-foreground">
              <p>
                The HTLC contract <em>deletes</em> an entry when unlocked or reclaimed, so this one
                error covers two opposite situations: the id was never on this chain, or the swap
                worked and the entry is gone.
              </p>
              <p>
                Ferry tells them apart, because an HTLC id is the hash of the transaction that
                created it and that transaction stays in the ledger forever. If the entry existed
                and was unlocked, the preimage is public in the unlocking transaction — Ferry reads
                it from there and saves it to the swap, so a failed verification is often "here's
                your preimage, go redeem".
              </p>
              <p>
                If the id isn't in the ledger at all, it's mistyped, your node is on a different
                network, or the create hasn't confirmed yet — give it a couple of momentums.
              </p>
            </AccordionContent>
          </AccordionItem>

          <AccordionItem value="expiry">
            <AccordionTrigger>znn-cli refuses the expiry Ferry printed</AccordionTrigger>
            <AccordionContent class="grid gap-2 text-muted-foreground">
              <p>
                <span class="font-mono text-xs">htlc.create</span> accepts 1 to
                {{ ZNN_CLI_MAX_HOURS }} hours and nothing else — a client limit, not a chain one.
                The Syrius extension has no such cap. Past {{ ZNN_CLI_MAX_HOURS }}h the card says so
                instead of printing a znn-cli line that would be rejected.
              </p>
            </AccordionContent>
          </AccordionItem>

          <AccordionItem value="lost">
            <AccordionTrigger>My swaps are gone</AccordionTrigger>
            <AccordionContent class="grid gap-2 text-muted-foreground">
              <p>
                Local storage was cleared, or this is a different browser, profile or origin. Import
                your export from the Swaps page, or take a single recovery file to the
                <RouterLink to="/recover" :class="linkClass">Recover</RouterLink> page — that alone
                is enough to get money out, with no swap record and no node.
              </p>
              <p>With neither, the money is safe until the locktime passes, and not after it.</p>
            </AccordionContent>
          </AccordionItem>
        </Accordion>
      </section>

      <!-- Glossary -------------------------------------------------------- -->
      <section :id="S.glossary.id" class="grid scroll-mt-6 gap-4 border-t border-border pt-8">
        <Heading :level="2" class="text-xl">{{ S.glossary.title }}</Heading>
        <DataList>
          <DataRow label="HTLC">
            Hashed timelock contract: money that can be taken by whoever knows a secret, or
            reclaimed by whoever locked it once a deadline passes. One on each chain is the entire
            mechanism.
          </DataRow>
          <DataRow label="secret / preimage">
            The 32 random bytes the initiator invents. Publishing it on one chain is what opens the
            other, which is why revealing it is the point of no return.
          </DataRow>
          <DataRow label="secret hash">
            Its SHA-256. Both contracts commit to this, so both open with the one secret. 64 hex
            characters, copied exactly or not at all.
          </DataRow>
          <DataRow label="contract">
            On Bitcoin, a P2SH address whose script has two branches: redeem with the preimage, or
            refund after the locktime. Paying it is an ordinary send.
          </DataRow>
          <DataRow label="pubkey hash">
            40 hex characters naming the key allowed to take a branch. You send yours; theirs is
            what lets the Bitcoin-sending side build the contract.
          </DataRow>
          <DataRow label="offer string">
            The <span class="font-mono text-xs">swapoffer1:</span> line. Public data only — amount,
            network, secret hash, addresses. Decoding one fills in the matching side of the trade.
          </DataRow>
          <DataRow label="locktime">
            When a refund becomes possible on the Bitcoin side. The initiator's leg always expires
            last, so the side that has to move first is never the side left exposed.
          </DataRow>
          <DataRow label="initiator / participant">
            The initiator invents the secret and takes the longer timelock. If somebody sent you an
            offer, you are the participant.
          </DataRow>
          <DataRow label="leg">
            One side of the trade — the chain you are paying into, as opposed to the one you are
            being paid on.
          </DataRow>
          <DataRow label="ZTS">
            A Zenon token standard, the <span class="font-mono text-xs">zts1…</span> identifier.
            Anyone can issue a token and lock the agreed <em>number</em> of units of it, so which
            token is as much a term of the trade as how much. Verification checks it, and so should
            you: two of them can differ in one character.
          </DataRow>
        </DataList>
      </section>

      <!-- Where the rest of it lives -------------------------------------- -->
      <section class="grid gap-2 border-t border-border pt-8 text-sm text-muted-foreground">
        <p>
          This page is the user's half of the documentation. The developer's half — the browser
          threat model, how the port works, testing a deployment, hosting it yourself — is on
          <RouterLink :to="{name: 'under-the-hood'}" :class="linkClass">Under the hood</RouterLink>.
        </p>
      </section>
    </article>

    <!-- Contents. Sticky beside the article on a wide screen; above it on a
         narrow one, where a sidebar would only put seven links between the
         reader and the first sentence. -->
    <nav
      class="row-start-1 grid gap-1 self-start lg:sticky lg:top-6 lg:row-start-auto"
      aria-label="On this page"
    >
      <p class="px-2 text-ledger text-muted-foreground">On this page</p>
      <RouterLink
        v-for="s in TOC"
        :key="s.id"
        :to="{name: 'docs', hash: `#${s.id}`}"
        class="rounded-md px-2 py-1 text-sm text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground"
      >
        {{ s.title }}
      </RouterLink>
      <RouterLink
        :to="{name: 'under-the-hood'}"
        class="mt-2 rounded-md border-t border-border px-2 pt-2 text-sm text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground"
      >
        Under the hood
      </RouterLink>
    </nav>
  </div>
</template>

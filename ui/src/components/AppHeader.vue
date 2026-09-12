<script setup lang="ts">
import {computed} from 'vue'
import {useRoute} from 'vue-router'
import {MoonIcon, SunIcon} from '@lucide/vue'
import {Button, useTheme} from 'nom-ui'
import {useFerry} from '@/core/composables/useFerry'
import {FERRY_ENV, isDev} from '@/core/env'
import {height} from '@/core/format'
import FerryMark from './FerryMark.vue'
import InfoTip from './InfoTip.vue'
import NodeSettings from './NodeSettings.vue'

const route = useRoute()
const {theme, toggleTheme} = useTheme()
const {config, configError} = useFerry()

// The two instances are the same page. Nothing else on screen distinguishes a
// build wired to a regtest node on this machine from one wired to mainnet, and
// the difference between them is the difference between a test and a payment —
// so the development build says so, permanently and in the first place anyone
// looks.
//
// Production is not badged. A badge on every page would be furniture, read as
// decoration within a day, and it is the exceptional case that has to stand
// out. The header already names the network for both.

// The page and the WebAssembly module carry the instance separately — one from
// a Vite define, one from a linker flag — and scripts/build.mjs sets both from
// one variable, so in a real build they agree. They can still come apart on a
// developer's machine: `npm run dev` serves whatever ui/public/ferry.wasm was
// last compiled, which may be the other instance's module. That combination
// stores swaps under one namespace while the settings live under the other, so
// it is called out rather than left to be discovered later.
const envMismatch = computed(() => {
  const fromModule = config.value?.buildEnv
  return fromModule !== undefined && fromModule !== FERRY_ENV ? fromModule : null
})

// Both lists are always on screen now, each carrying its own count — so the nav
// says how much is waiting in the list you are not looking at, without either
// page having to fetch the other's data. A single link that renamed itself to
// wherever you were not made the app read as one page changing its mind.
const nav = computed(() => [
  {to: '/', name: 'active', label: 'Swaps', count: config.value?.activeSwaps},
  // No count. The others count what is in this browser's storage, which is a
  // number the header already has; the board's would be a count of what relays
  // are currently handing back, which changes on its own and would need a
  // subscription held open on every page to keep honest.
  {to: '/board', name: 'board', label: 'Board', count: undefined},
  {to: '/history', name: 'history', label: 'History', count: config.value?.historySwaps},
  {to: '/recover', name: 'recover', label: 'Recover', count: undefined},
  {to: '/docs', name: 'docs', label: 'Docs', count: undefined},
])

// Connection status, as chips rather than a run of small mono text.
//
// The dot is the part that gets read. Whether the two chains are answering
// decides whether anything below can be trusted, and a coloured dot says it in
// the time it takes to glance; the height beside it is for whoever wants to
// check it against their own node. What a chip means, and what to do when it is
// not green, is one hover away instead of on screen for everyone forever.
type Tone = 'ok' | 'bad' | 'idle'
const chips = computed(() => {
  const c = config.value
  if (!c) return []
  return [
    {
      key: 'network',
      label: 'network',
      value: c.network,
      tone: 'ok' as Tone,
      hint: 'The chain new swaps are created on. A swap keeps the network it was made on. Change it under Nodes.',
    },
    {
      key: 'bitcoin',
      label: 'bitcoin',
      value: c.tipHeight !== undefined ? height(c.tipHeight) : (c.chainError ?? 'unreachable'),
      tone: c.tipHeight !== undefined ? ('ok' as Tone) : ('bad' as Tone),
      hint:
        c.tipHeight !== undefined
          ? `Latest block seen by ${c.backend}, ${c.usingOwnBtc ? 'the node you chose' : 'a public node'}. Funding shows up here.`
          : 'Bitcoin is not answering, so funding cannot be detected and nothing can be broadcast. Check the Esplora URL under Nodes.',
    },
    c.znn
      ? {
          key: 'zenon',
          label: 'zenon',
          value: c.znnHeight !== undefined ? height(c.znnHeight) : (c.znnError ?? 'unreachable'),
          tone: c.znnHeight !== undefined ? ('ok' as Tone) : ('bad' as Tone),
          hint:
            c.znnHeight !== undefined
              ? 'Latest momentum from your Zenon node. This is what checks a counterparty HTLC.'
              : 'Your Zenon node is not answering, so an HTLC cannot be verified. Check the URL under Nodes.',
        }
      : {
          key: 'zenon',
          label: 'zenon',
          value: 'no node set',
          // Not an error — nothing has gone wrong — but not neutral either:
          // with no Zenon node the counterparty's HTLC cannot be verified,
          // which is the check the whole cross-chain half rests on.
          tone: 'idle' as Tone,
          hint: 'No Zenon node is set, so nothing is checking the other half of your swap. Add one under Nodes — until then you are taking the counterparty at their word.',
        },
    c.sol
      ? {
          key: 'solana',
          label: 'solana',
          value: c.solVersion ?? (c.solError ? 'unreachable' : 'connecting…'),
          tone: c.solVersion ? ('ok' as Tone) : ('bad' as Tone),
          hint: c.solVersion
            ? `Cluster answering at ${c.usingOwnSol ? 'the endpoint you chose' : 'a public endpoint'}. An escrow is read against an address this page derives itself, so a node that lied about it would be caught rather than believed.`
            : 'The Solana endpoint is not answering, so an escrow cannot be read. Check the RPC URL under Nodes.',
        }
      : {
          key: 'solana',
          label: 'solana',
          // Mainnet has no fallback endpoint, so with nothing set there is no
          // request to be waiting on. Saying "connecting…" for ever is the one
          // reading that is simply false, and it is what the chip did before
          // the mainnet default was removed.
          value: 'no node set',
          tone: 'idle' as Tone,
          hint: 'No Solana endpoint is set. There is no default on mainnet — the public one refuses browsers — so an escrow cannot be read, funded or claimed until you add one under Nodes.',
        },
  ]
})
</script>

<template>
  <header class="border-b border-border bg-background">
    <div class="mx-auto flex max-w-5xl flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3 sm:px-6">
      <RouterLink to="/" class="flex shrink-0 items-center gap-2">
        <FerryMark class="size-6" />
        <span class="text-base font-semibold tracking-tight">Ferry</span>
        <span
          v-if="isDev"
          class="rounded border border-amber-500/40 bg-amber-500/15 px-1.5 py-0.5 text-ledger text-[10px] font-semibold tracking-wider text-amber-600 uppercase dark:text-amber-400"
          title="Development instance — test chains only. Do not use this build with real money."
        >
          dev
        </span>
      </RouterLink>

      <nav class="flex flex-1 items-center gap-0.5">
        <RouterLink
          v-for="n in nav"
          :key="n.name"
          :to="n.to"
          class="rounded-md px-2.5 py-1.5 text-sm font-medium transition-colors"
          :class="
            route.name === n.name || (n.name === 'docs' && route.name === 'under-the-hood')
              ? 'bg-muted text-foreground'
              : 'text-muted-foreground hover:bg-muted/50 hover:text-foreground'
          "
        >
          {{ n.label }}
          <span v-if="n.count" class="font-mono text-xs tabular-nums opacity-70">
            {{ n.count }}
          </span>
        </RouterLink>
      </nav>

      <!-- No wallet chip here. A Solana wallet was the only one of the three
           with a permanent seat in the header, which made it look like a thing
           this app needs before anything else — it is not, and the two chains
           that ask for a wallet just as often never had one. Connecting belongs
           where a wallet is actually wanted: step two of the create form, the
           Solana leg panel, and the board's proof button, each of which says
           what it needs the wallet FOR. -->
      <NodeSettings />

      <Button
        variant="ghost"
        size="icon-sm"
        :aria-label="theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'"
        @click="toggleTheme"
      >
        <SunIcon v-if="theme === 'dark'" />
        <MoonIcon v-else />
      </Button>
    </div>

    <div class="mx-auto max-w-5xl px-4 pb-3 sm:px-6">
      <p v-if="envMismatch" class="pb-1.5 text-xs text-destructive">
        This page was built as the <span class="font-mono">{{ FERRY_ENV }}</span> instance but the
        signing module says <span class="font-mono">{{ envMismatch }}</span
        >. Each keeps its swaps and settings under different names, so this build will not find its
        own records. Rebuild with
        <span class="font-mono">npm run {{ FERRY_ENV === 'dev' ? 'wasm:dev' : 'wasm' }}</span
        >.
      </p>
      <p v-if="configError" class="text-xs text-destructive">error: {{ configError }}</p>
      <div v-else class="flex flex-wrap items-center gap-1.5">
        <span
          v-for="c in chips"
          :key="c.key"
          class="inline-flex items-center gap-1.5 rounded-full border border-border bg-muted/40 py-1 pr-1.5 pl-2.5 text-xs"
        >
          <span
            class="size-1.5 shrink-0 rounded-full"
            :class="{
              'bg-success': c.tone === 'ok',
              'bg-destructive': c.tone === 'bad',
              'bg-warning': c.tone === 'idle',
            }"
            aria-hidden="true"
          />
          <span class="text-ledger text-muted-foreground">{{ c.label }}</span>
          <span
            class="font-mono"
            :class="{
              'text-destructive': c.tone === 'bad',
              'text-muted-foreground': c.tone === 'idle',
            }"
          >
            {{ c.value }}
          </span>
          <InfoTip :label="`What ${c.label} means`" side="bottom">{{ c.hint }}</InfoTip>
        </span>
        <span v-if="!chips.length" class="text-xs text-muted-foreground">connecting…</span>
      </div>
    </div>
  </header>
</template>

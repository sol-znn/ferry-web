<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { call } from './core/engine.js'
import * as solana from './core/solana.js'
import Nodes from './components/Nodes.vue'
import NewSwap from './components/NewSwap.vue'
import SwapDetail from './components/SwapDetail.vue'

const swaps = ref([])
const selected = ref('')
const config = ref({ solanaUrl: '', zenonUrl: '', solProgram: '' })
const booting = ref(true)
const bootError = ref('')
const showNodes = ref(false)
const showNew = ref(false)
const build = ref('')
const solBalance = ref(null)
const storeFile = ref(null)
const storeNote = ref('')

const current = computed(() => swaps.value.find((r) => r.swap.id === selected.value))

onMounted(async () => {
  // Start looking for an extension before the engine loads; Phantom injects on
  // its own schedule and the button that offers it is reactive to what turns up.
  solana.watchForWallets()
  try {
    const env = await call('env')
    build.value = env.build
    config.value = await call('config.get')
    await load()
    // A reload should not cost a popup: if this browser has connected here
    // before, the extension reconnects silently or not at all.
    if (await solana.reconnectIfTrusted()) refreshBalance()
    // Nothing can be done before both nodes are named, so a first-run browser
    // lands on the settings rather than on an empty list.
    showNodes.value = !config.value.solProgram
  } catch (e) {
    bootError.value = String(e.message ?? e)
  } finally {
    booting.value = false
  }
})

async function load() {
  swaps.value = await call('swap.list')
  if (!selected.value && swaps.value.length) selected.value = swaps.value[0].swap.id
  decorate()
}

// Amounts arrive as base units, because that is what both chains agree on. The
// engine already knows the token's decimals, so the display form is derived
// once here rather than in three components.
function decorate() {
  for (const r of swaps.value) {
    const t = r.swap.terms
    r.znnAmountText = `${formatUnits(t.znnAmount, t.znnDecimals)} ${tokenName(t.znnToken)}`
    r.solAmountText = `${(t.solLamports / 1e9).toLocaleString('en-US', { maximumFractionDigits: 9 })} SOL`
  }
}

function formatUnits(base, decimals) {
  const s = String(base).padStart(decimals + 1, '0')
  const whole = s.slice(0, s.length - decimals)
  const frac = decimals ? s.slice(s.length - decimals).replace(/0+$/, '') : ''
  return frac ? `${whole}.${frac}` : whole
}

const tokenName = (zts) =>
  zts === 'zts1znnxxxxxxxxxxxxx9z4ulx' ? 'ZNN' : zts === 'zts1qsrxxxxxxxxxxxxxmrhjll' ? 'QSR' : zts

async function onUpdated(record) {
  await load()
  if (record?.swap?.id) selected.value = record.swap.id
}

async function created(id) {
  showNew.value = false
  await load()
  selected.value = id
}

async function remove(id) {
  await call('swap.delete', { id })
  if (selected.value === id) selected.value = ''
  await load()
}

async function exportAll() {
  const data = await call('store.export')
  const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = `solzen-swaps-${new Date().toISOString().slice(0, 10)}.json`
  a.click()
  URL.revokeObjectURL(a.href)
  storeNote.value = `Exported ${data.swaps?.length ?? 0} swap(s). Keep it: it holds the keys that refund them.`
}

// The other half of Export, which without this is a file nothing can read.
//
// A swap that has been funded can only be refunded by the browser holding its
// per-swap Zenon key, so the export is the one thing standing between a cleared
// profile and money nobody can reach. The engine merges by id and keeps the
// newer record, so restoring an old file over live swaps cannot roll one back.
async function importFile(event) {
  const file = event.target.files?.[0]
  event.target.value = ''
  if (!file) return
  storeNote.value = ''
  try {
    const r = await call('store.import', { text: await file.text() })
    await load()
    const took = r.imported
      ? `Imported ${r.imported} swap(s)${r.skipped ? `, and left ${r.skipped} alone as already current` : ''}.`
      : r.skipped
        ? `Nothing to import — all ${r.skipped} record(s) in that file are already here, or newer here.`
        : 'That file decodes, and holds no swaps.'
    // A refused record is not a skipped one, and saying so matters: it is a
    // swap that did not come back, whose key this browser therefore does not
    // have. Named individually, because "2 records refused" is not something
    // anyone can act on.
    const refused = r.refused ?? []
    storeNote.value = refused.length
      ? `${took} ${refused.length} record(s) were refused and are NOT in this browser: ${refused.join('; ')}.`
      : took
  } catch (e) {
    storeNote.value = String(e.message ?? e)
  }
}

async function connect(kind) {
  try {
    if (kind === 'phantom') await solana.connectPhantom()
    else if (kind === 'injected') await solana.connectInjected()
    else solana.useLocalKey()
    solBalance.value = await solana.balance(config.value.solanaUrl)
  } catch (e) {
    // Phantom reports a refused popup as 4001; saying "cancelled" is more use
    // than the code, and it is not a failure worth alarming anyone about.
    solana.wallet.error = e?.code === 4001 ? 'the wallet request was cancelled' : String(e.message ?? e)
  }
}

async function refreshBalance() {
  try {
    solBalance.value = w.address ? await solana.balance(config.value.solanaUrl) : null
  } catch {
    solBalance.value = null
  }
}

async function airdrop() {
  try {
    await solana.airdrop(config.value.solanaUrl, 2)
    solBalance.value = await solana.balance(config.value.solanaUrl)
  } catch (e) {
    solana.wallet.error = String(e.message ?? e)
  }
}

const w = solana.wallet
const d = solana.detected

// The account can change in the extension without this page being asked, so
// what is on screen follows the wallet rather than the last button pressed.
watch(() => w.address, refreshBalance)
</script>

<template>
  <div class="min-h-full flex flex-col">
    <header class="border-b border-line px-5 py-3 flex items-center gap-4 flex-wrap">
      <h1 class="text-fg">
        solzen <span class="text-dim">— Solana ⇄ Zenon atomic swaps</span>
      </h1>
      <span v-if="build" class="text-dim text-[11px] border border-line rounded px-1.5">{{ build }}</span>

      <div class="ml-auto flex items-center gap-2 text-[12px]">
        <template v-if="w.address">
          <span class="text-dim">{{ w.name }}</span>
          <code class="text-accent">{{ w.address.slice(0, 6) }}…{{ w.address.slice(-4) }}</code>
          <span v-if="solBalance !== null" class="text-dim">{{ (solBalance / 1e9).toFixed(3) }} SOL</span>
          <button class="text-dim hover:text-fg" @click="airdrop">airdrop</button>
          <button class="text-dim hover:text-fg" @click="solana.disconnect()">disconnect</button>
        </template>
        <template v-else>
          <button
            v-if="d.phantom"
            class="border border-accent/40 rounded px-2 py-1 text-accent hover:bg-accent/10 disabled:opacity-50"
            :disabled="w.connecting"
            @click="connect('phantom')"
          >
            {{ w.connecting ? 'Approve it in Phantom…' : 'Connect Phantom' }}
          </button>
          <button
            v-else-if="d.other"
            class="border border-line rounded px-2 py-1 text-dim hover:text-fg disabled:opacity-50"
            :disabled="w.connecting"
            @click="connect('injected')"
          >
            {{ w.connecting ? 'Approve it in your wallet…' : `Connect ${d.other}` }}
          </button>
          <a
            v-else
            :href="solana.PHANTOM_INSTALL_URL"
            target="_blank"
            rel="noopener noreferrer"
            class="border border-line rounded px-2 py-1 text-dim hover:text-fg"
          >
            Install Phantom
          </a>
          <button class="border border-line rounded px-2 py-1 text-dim hover:text-fg" @click="connect('local')">
            Use a key in this browser
          </button>
        </template>
        <button class="border border-line rounded px-2 py-1 text-dim hover:text-fg" @click="showNodes = !showNodes">
          Nodes
        </button>
      </div>
    </header>

    <p v-if="w.error" class="px-5 py-2 text-bad text-[12px]">{{ w.error }}</p>
    <p v-if="w.sendsThroughItsOwnRpc" class="px-5 py-2 text-bad text-[12px]">
      {{ w.name }} can only sign and send in one step, so it submits through its own node rather than the
      one named in Nodes. Against a local validator that sends the step to a different chain.
    </p>

    <main class="flex-1 grid lg:grid-cols-[22rem_1fr] gap-5 p-5 items-start">
      <aside class="space-y-4">
        <Nodes v-if="showNodes" @changed="(c) => (config = c)" />

        <div class="flex gap-2">
          <button
            class="flex-1 border border-accent/40 text-accent rounded px-3 py-1.5 hover:bg-accent/10"
            @click="showNew = !showNew"
          >
            {{ showNew ? 'Close' : 'New swap' }}
          </button>
          <button class="border border-line rounded px-3 py-1.5 text-dim hover:text-fg" @click="exportAll">
            Export
          </button>
          <button
            class="border border-line rounded px-3 py-1.5 text-dim hover:text-fg"
            @click="storeFile?.click()"
          >
            Import
          </button>
          <input ref="storeFile" type="file" accept="application/json,.json" class="hidden" @change="importFile" />
        </div>
        <p v-if="storeNote" class="text-dim text-[11px]">{{ storeNote }}</p>

        <NewSwap v-if="showNew" @created="created" />

        <nav class="border border-line rounded bg-panel divide-y divide-line">
          <p v-if="!swaps.length" class="p-4 text-dim text-[12px]">
            No swaps in this browser yet. Storage is per browser profile, so two participants means two
            profiles — not two tabs.
          </p>
          <button
            v-for="r in swaps"
            :key="r.swap.id"
            class="w-full text-left p-3 hover:bg-panel-2"
            :class="selected === r.swap.id ? 'bg-panel-2' : ''"
            @click="selected = r.swap.id"
          >
            <div class="flex items-baseline justify-between gap-2">
              <span :class="r.swap.archived ? 'text-dim' : 'text-fg'">
                {{ r.swap.role.sendsSol ? `${r.solAmountText} → ${r.znnAmountText}` : `${r.znnAmountText} → ${r.solAmountText}` }}
              </span>
              <span class="text-dim text-[11px]">{{ r.swap.role.initiator ? 'maker' : 'taker' }}</span>
            </div>
            <div class="text-dim text-[11px] truncate">{{ r.swap.outcome || r.swap.terms.swapId }}</div>
          </button>
        </nav>

        <button
          v-if="current"
          class="text-dim text-[11px] hover:text-bad"
          @click="remove(current.swap.id)"
        >
          Forget the selected swap — its per-swap key goes with it
        </button>
      </aside>

      <section>
        <p v-if="booting" class="text-dim">Loading the swap engine…</p>
        <p v-else-if="bootError" class="text-bad whitespace-pre-wrap">{{ bootError }}</p>
        <SwapDetail v-else-if="current" :record="current" :config="config" @updated="onUpdated" />
        <div v-else class="border border-line rounded bg-panel p-5 text-dim space-y-3 text-[13px]">
          <p class="text-fg">Two contracts, one secret.</p>
          <p>
            A swap is an escrow on Solana and an HTLC on Zenon that commit to the same SHA-256 hash.
            Whoever claims one leg publishes the secret on that chain, which is exactly what the other
            side needs to claim the other. Either both settle or, after the timelocks, both refund.
          </p>
          <p>
            Nothing here is custodial. The Solana escrow pays only the two addresses named when it was
            made, and your own wallet signs. The Zenon side uses a key generated for this one swap,
            holding only what you deliberately send into it — because no Zenon wallet can make the two
            contract calls a cross-chain HTLC needs.
          </p>
          <p>Pick a swap, or make one.</p>
        </div>
      </section>
    </main>
  </div>
</template>

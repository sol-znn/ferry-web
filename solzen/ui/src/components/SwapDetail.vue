<script setup>
import { computed, ref, watch } from 'vue'
import { call } from '../core/engine.js'
import * as solana from '../core/solana.js'
import Copyable from './Copyable.vue'

const props = defineProps({
  record: { type: Object, required: true },
  config: { type: Object, required: true },
})
const emit = defineEmits(['updated'])

const status = ref(null)
const busy = ref('')
const error = ref('')
const note = ref('')
const acceptText = ref('')
const htlcIdText = ref('')
const mining = ref(null)
const steps = ref([])

const swap = computed(() => props.record.swap)
const terms = computed(() => swap.value.terms)
const sendsSol = computed(() => swap.value.role.sendsSol)

watch(() => props.record.swap.id, () => {
  status.value = null
  error.value = ''
  note.value = ''
  steps.value = []
  refresh()
}, { immediate: true })

async function refresh() {
  busy.value = 'refresh'
  error.value = ''
  try {
    const r = await call('swap.refresh', { id: swap.value.id })
    status.value = r.status
    emit('updated', r.swap)
  } catch (e) {
    error.value = String(e.message ?? e)
  } finally {
    busy.value = ''
  }
}

async function applyAccept() {
  busy.value = 'accept'
  error.value = ''
  try {
    const r = await call('swap.applyAccept', { id: swap.value.id, accept: acceptText.value.trim() })
    acceptText.value = ''
    emit('updated', r)
    await refresh()
  } catch (e) {
    error.value = String(e.message ?? e)
  } finally {
    busy.value = ''
  }
}

async function setHtlcId() {
  busy.value = 'htlc'
  error.value = ''
  try {
    const r = await call('swap.setHtlcId', { id: swap.value.id, htlcId: htlcIdText.value.trim() })
    htlcIdText.value = ''
    emit('updated', r)
    await refresh()
  } catch (e) {
    error.value = String(e.message ?? e)
  } finally {
    busy.value = ''
  }
}

async function run(action) {
  error.value = ''
  note.value = ''
  steps.value = []
  busy.value = action.kind
  try {
    if (action.kind.startsWith('sol.')) {
      const ix = await call('sol.instruction', { id: swap.value.id, kind: action.kind })
      const signature = await solana.send(props.config.solanaUrl, ix)
      await call('sol.recordTx', { id: swap.value.id, kind: action.kind, signature })
      note.value = `Solana transaction ${signature}`
    } else if (action.kind.startsWith('znn.')) {
      // Ask first, so a multi-minute mine is a thing the user agreed to.
      const cost = await call('znn.cost', { id: swap.value.id, kind: action.kind })
      if (cost.needsWork) {
        mining.value = { hashes: 0, expected: cost.expectedHashes, blocks: cost.blocks }
      }
      const result = await call(
        'znn.act',
        { id: swap.value.id, kind: action.kind },
        cost.needsWork ? (hashes) => { if (mining.value) mining.value.hashes = hashes } : undefined,
      )
      // A reclaim publishes three blocks, and each one is a place the money can
      // be left. Naming them is how the user can tell "it is in your wallet"
      // from "it is at an address only this browser can reach".
      steps.value = result.steps ?? []
      note.value = result.htlcId
        ? `HTLC ${result.htlcId}${result.confirmed ? '' : ' — not confirmed by the contract yet'}`
        : `Zenon block ${result.tx}`
      // Not thrown: everything in result.steps happened, and an exception here
      // would hide it behind a message that reads as "nothing did".
      if (result.incomplete) error.value = result.incomplete
    }
    await refresh()
  } catch (e) {
    error.value = String(e.message ?? e)
  } finally {
    mining.value = null
    busy.value = ''
  }
}

const secondsText = (s) => {
  if (s === undefined || s === null) return ''
  const abs = Math.abs(s)
  if (abs < 60) return `${s}s`
  if (abs < 3600) return `${Math.round(s / 60)}m`
  if (abs < 172800) return `${Math.floor(s / 3600)}h${String(Math.floor((abs % 3600) / 60)).padStart(2, '0')}m`
  return `${Math.floor(s / 86400)}d`
}

function legTone(leg) {
  if (!leg) return 'text-dim'
  if (leg.funded && !leg.verified) return 'text-bad'
  if (leg.settled) return 'text-good'
  if (leg.refunded) return 'text-warn'
  if (leg.funded) return 'text-good'
  return 'text-dim'
}

// The node reports a difficulty, and a difficulty is a mean hash count. A
// browser tab does on the order of half a million a second, so this is an
// estimate and is worded as one -- but an estimate is what turns a progress bar
// into a decision about whether to fuse plasma instead.
const browserHashesPerSecond = 500_000
const workMinutes = computed(() => {
  const d = status.value?.swapAddress?.pendingWork ?? 0
  const seconds = d / browserHashesPerSecond
  if (seconds < 90) return `${Math.max(1, Math.round(seconds))} seconds`
  return `${Math.round(seconds / 60)} minutes`
})

// Unknown plasma is dimmed rather than coloured: it is neither the good news
// nor the bad one, and painting it as either is the mistake being avoided.
const plasmaTone = computed(() => {
  const sa = status.value?.swapAddress
  if (!sa?.plasmaKnown) return 'text-dim'
  return sa.plasmaFused ? 'text-good' : 'text-warn'
})

function legText(leg) {
  if (!leg) return '—'
  if (leg.settled) return 'settled'
  if (leg.refunded) return 'refunded'
  if (leg.funded) return leg.verified ? 'funded and verified' : 'funded, but WRONG'
  return 'not funded'
}
</script>

<template>
  <section class="space-y-4">
    <header class="border border-line rounded bg-panel p-4">
      <div class="flex items-baseline justify-between gap-4">
        <h2 class="text-fg text-[15px]">
          {{ sendsSol ? 'You send SOL, you receive Zenon' : 'You send Zenon, you receive SOL' }}
        </h2>
        <button
          class="border border-line rounded px-3 py-1 text-[12px] text-dim hover:text-fg hover:border-dim"
          :disabled="busy === 'refresh'"
          @click="refresh"
        >
          {{ busy === 'refresh' ? 'reading chains…' : 'Refresh from chains' }}
        </button>
      </div>
      <dl class="grid grid-cols-[9rem_1fr] gap-x-3 text-[12px] mt-2">
        <dt class="text-dim">Amounts</dt>
        <dd>{{ (terms.solLamports / 1e9).toLocaleString() }} SOL ⇄ {{ record.znnAmountText }}</dd>
        <dt class="text-dim">You are</dt>
        <dd>{{ swap.role.initiator ? 'the initiator — you invented the secret, your leg locks longer' : 'the participant — you go second, your leg unlocks first' }}</dd>
        <dt class="text-dim">Secret surfaces on</dt>
        <dd>{{ terms.initiator === 'sol' ? 'Zenon, when the initiator unlocks the HTLC' : 'Solana, when the initiator redeems the escrow' }}</dd>
        <dt class="text-dim">Swap id</dt>
        <dd class="truncate text-dim">{{ terms.swapId }}</dd>
      </dl>
    </header>

    <div v-if="status?.warnings?.length" class="border border-warn/40 bg-warn/5 rounded p-3 space-y-1">
      <p v-for="w in status.warnings" :key="w" class="text-warn text-[12px]">{{ w }}</p>
    </div>

    <!-- The strings that pass between the two people. -->
    <section v-if="record.offer || record.accept" class="border border-line rounded bg-panel p-4 space-y-3">
      <Copyable
        v-if="record.offer"
        :value="record.offer"
        label="Send this offer to the counterparty"
        wrap
      />
      <Copyable
        v-if="record.accept"
        :value="record.accept"
        label="Send this acceptance back"
        wrap
      />
      <p class="text-dim text-[11px]">Public terms only — never the secret, never a key.</p>

      <div v-if="record.offer && !status?.complete" class="pt-2 border-t border-line">
        <label class="text-dim text-[11px] uppercase tracking-wider">Their acceptance</label>
        <textarea
          v-model="acceptText"
          rows="2"
          placeholder="solzenaccept1:…"
          class="w-full bg-panel-2 border border-line rounded px-2 py-1.5 mt-1 break-all"
        />
        <button
          class="mt-2 border border-accent/40 text-accent rounded px-3 py-1 hover:bg-accent/10"
          :disabled="busy === 'accept'"
          @click="applyAccept"
        >
          Apply
        </button>
      </div>
    </section>

    <!-- Both legs, side by side, because the whole question is how they relate. -->
    <div class="grid md:grid-cols-2 gap-4">
      <article class="border border-line rounded bg-panel p-4 space-y-2">
        <h3 class="text-fg">Solana escrow</h3>
        <p :class="legTone(status?.sol)">{{ legText(status?.sol) }}</p>
        <dl class="grid grid-cols-[6rem_1fr] gap-x-3 text-[12px]">
          <dt class="text-dim">Address</dt>
          <dd class="truncate">{{ status?.sol?.address || record.solEscrowAddress || '—' }}</dd>
          <dt class="text-dim">Amount</dt>
          <dd>{{ status?.sol?.amount || `${(terms.solLamports / 1e9).toLocaleString()} SOL` }}</dd>
          <dt class="text-dim">Refundable in</dt>
          <dd :class="status?.sol?.expired ? 'text-warn' : ''">
            {{ status?.sol?.expiry ? secondsText(status.sol.secondsLeft) : secondsText(terms.solTimelock - (status?.solNow ?? 0)) }}
          </dd>
        </dl>
        <p v-if="status?.sol?.note" class="text-dim text-[11px]">{{ status.sol.note }}</p>
        <ul v-if="status?.sol?.problems?.length" class="text-bad text-[12px] list-disc pl-4">
          <li v-for="p in status.sol.problems" :key="p">{{ p }}</li>
        </ul>
      </article>

      <article class="border border-line rounded bg-panel p-4 space-y-2">
        <h3 class="text-fg">Zenon HTLC</h3>
        <p :class="legTone(status?.znn)">{{ legText(status?.znn) }}</p>
        <dl class="grid grid-cols-[6rem_1fr] gap-x-3 text-[12px]">
          <dt class="text-dim">Id</dt>
          <dd class="truncate">{{ swap.htlcId || '— found automatically once created' }}</dd>
          <dt class="text-dim">Amount</dt>
          <dd>{{ status?.znn?.amount || record.znnAmountText }}</dd>
          <dt class="text-dim">Expires in</dt>
          <dd :class="status?.znn?.expired ? 'text-warn' : ''">
            {{ status?.znn?.expiry ? secondsText(status.znn.secondsLeft) : secondsText(terms.znnExpiry - (status?.znnNow ?? 0)) }}
          </dd>
        </dl>
        <p v-if="status?.znn?.note" class="text-dim text-[11px]">{{ status.znn.note }}</p>
        <ul v-if="status?.znn?.problems?.length" class="text-bad text-[12px] list-disc pl-4">
          <li v-for="p in status.znn.problems" :key="p">{{ p }}</li>
        </ul>
        <details v-if="!swap.htlcId" class="text-[12px]">
          <summary class="text-dim cursor-pointer hover:text-fg">Paste the id instead</summary>
          <p class="text-dim mt-1">
            The id is found by scanning the contract's own chain. If it is older than the scan reaches,
            the counterparty can send it.
          </p>
          <div class="flex gap-2 mt-1">
            <input v-model="htlcIdText" class="flex-1 min-w-0 bg-panel-2 border border-line rounded px-2 py-1" />
            <button class="border border-line rounded px-2 text-dim hover:text-fg" @click="setHtlcId">set</button>
          </div>
        </details>
      </article>
    </div>

    <!-- The per-swap Zenon account. Both sides have one, for different reasons. -->
    <section v-if="status?.swapAddress" class="border border-line rounded bg-panel p-4 space-y-2">
      <h3 class="text-fg">This swap's Zenon address</h3>
      <p v-if="status.swapAddress.needsFunding" class="text-dim text-[12px]">
        Pay it from Syrius like any other address. The page holds a throwaway key for it so it can make
        the contract calls no Zenon wallet exposes — and a refund comes back here, then home to
        <span class="text-fg">{{ swap.znnHomeAddress }}</span>.
      </p>
      <p v-else class="text-dim text-[12px]">
        Nothing is ever paid into this one. Your ZNN goes straight to
        <span class="text-fg">{{ terms.znnReceiver }}</span> — the contract pays the address in the
        entry, not the caller. This key exists only to publish the unlock, which is still a Zenon
        block, and a block still needs plasma.
      </p>
      <Copyable :value="status.swapAddress.address" />
      <dl class="grid grid-cols-[8rem_1fr] gap-x-3 text-[12px]">
        <template v-if="status.swapAddress.needsFunding">
          <dt class="text-dim">Holds</dt>
          <dd :class="status.swapAddress.sufficient ? 'text-good' : ''">{{ status.swapAddress.balance }}</dd>
          <dt class="text-dim">Unreceived</dt>
          <dd>{{ status.swapAddress.pending }}</dd>
        </template>
        <dt class="text-dim">Plasma</dt>
        <dd :class="plasmaTone">
          <!-- Three states, not two. A probe the node never answered is not
               evidence of an empty account, and saying it is turns a failed
               request into a confident and specific promise. -->
          <template v-if="!status.swapAddress.plasmaKnown">
            unknown — the node did not say what the next block costs, so this is not a claim
            that there is none. Refresh once the node answers.
            <span v-if="status.swapAddress.plasmaProblem" class="block text-dim break-all">
              {{ status.swapAddress.plasmaProblem }}
            </span>
          </template>
          <template v-else-if="status.swapAddress.plasmaFused">fused — its blocks publish immediately</template>
          <template v-else>
            none — the next block costs about {{ workMinutes }} of proof of work in this tab.
            Fusing ~60 QSR to this address from Syrius removes it, and the QSR is reclaimed by
            cancelling the fusion.
          </template>
        </dd>
      </dl>
    </section>

    <!-- What to do next. -->
    <section class="border border-line rounded bg-panel p-4 space-y-3">
      <h3 class="text-fg">Next</h3>
      <!-- data-action names what this block does, not how it looks. The
           end-to-end test drives the page through it; the words beside it are
           for people, and change more freely than the meaning does. -->
      <div v-for="a in status?.actions ?? []" :key="a.kind" class="border border-line rounded p-3 space-y-1"
           :data-action="a.kind" :data-ready="a.ready"
           :class="a.primary ? 'border-accent/30 bg-accent/5' : ''">
        <div class="flex items-center justify-between gap-3">
          <span :class="a.primary ? 'text-accent' : 'text-fg'">{{ a.label }}</span>
          <button
            v-if="a.ready && !a.kind.startsWith('await.') && !a.kind.startsWith('exchange.')"
            class="border rounded px-3 py-1 text-[12px]"
            :class="a.primary ? 'border-accent/40 text-accent hover:bg-accent/10' : 'border-line text-dim hover:text-fg'"
            :disabled="busy === a.kind"
            @click="run(a)"
          >
            {{ busy === a.kind ? 'working…' : 'Do it' }}
          </button>
        </div>
        <p v-if="a.detail" class="text-dim text-[12px]">{{ a.detail }}</p>
        <p v-if="a.blocked" class="text-warn text-[12px]">{{ a.blocked }}</p>
      </div>

      <div v-if="mining" class="border border-line rounded p-3">
        <p class="text-dim text-[12px]">
          Mining plasma — {{ (mining.hashes / 1e6).toFixed(1) }}M of about
          {{ (mining.expected / 1e6).toFixed(0) }}M hashes<template v-if="mining.blocks > 1">, across the
          {{ mining.blocks }} blocks this step takes</template>. This is what buys them their plasma;
          fusing QSR to the swap address from Syrius removes it entirely.
        </p>
        <div class="h-1 bg-panel-2 rounded mt-2 overflow-hidden">
          <div class="h-full bg-accent/60" :style="{ width: `${Math.min(100, (mining.hashes / mining.expected) * 100)}%` }" />
        </div>
      </div>

      <!-- What a multi-block action actually published. A reclaim is three
           blocks and the money is only home after the last one, so each is
           named as it lands rather than summarised as "done". -->
      <ol v-if="steps.length" class="text-[12px] space-y-1" data-steps>
        <li v-for="s in steps" :key="s.tx" class="break-all">
          <span class="text-good">done</span>
          <span class="text-dim"> — {{ s.note }} — {{ s.tx }}</span>
        </li>
      </ol>

      <p v-if="note" class="text-good text-[12px] break-all">{{ note }}</p>
      <p v-if="error" class="text-bad text-[12px] whitespace-pre-wrap">{{ error }}</p>
      <p v-if="status?.outcome" class="text-good">{{ status.outcome }}</p>
    </section>
  </section>
</template>

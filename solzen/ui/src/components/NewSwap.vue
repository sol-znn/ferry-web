<script setup>
import { computed, ref } from 'vue'
import { call } from '../core/engine.js'
import { wallet } from '../core/solana.js'

const emit = defineEmits(['created'])

const mode = ref('make') // 'make' | 'take'

// Making an offer
const form = ref({
  sendsSol: true,
  solAmount: '0.5',
  znnAmount: '10',
  znnToken: 'ZNN',
  znnAddress: '',
  longSeconds: 48 * 3600,
  shortSeconds: 24 * 3600,
})

// Taking one
const offerText = ref('')
const preview = ref(null)
const takeZnnAddress = ref('')

const busy = ref(false)
const error = ref('')

const znnRole = computed(() => (form.value.sendsSol ? 'where your ZNN is paid' : 'your ZNN wallet — it funds the swap and a refund comes back here'))

async function previewOffer() {
  error.value = ''
  preview.value = null
  try {
    preview.value = await call('swap.preview', { text: offerText.value })
  } catch (e) {
    error.value = String(e.message ?? e)
  }
}

async function submit() {
  error.value = ''
  busy.value = true
  try {
    if (!wallet.address) throw new Error('connect a Solana wallet first — its address is where your SOL goes')
    const created =
      mode.value === 'make'
        ? await call('swap.create', {
            sendsSol: form.value.sendsSol,
            solAddress: wallet.address,
            znnAddress: form.value.znnAddress.trim(),
            solAmount: form.value.solAmount.trim(),
            znnAmount: form.value.znnAmount.trim(),
            znnToken: form.value.znnToken.trim(),
            longSeconds: Number(form.value.longSeconds),
            shortSeconds: Number(form.value.shortSeconds),
          })
        : await call('swap.accept', {
            offer: offerText.value.trim(),
            solAddress: wallet.address,
            znnAddress: takeZnnAddress.value.trim(),
          })
    emit('created', created.swap.id)
    offerText.value = ''
    preview.value = null
  } catch (e) {
    error.value = String(e.message ?? e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <section class="border border-line rounded bg-panel p-4 space-y-3">
    <div class="flex gap-1">
      <button
        v-for="m in [['make', 'Make an offer'], ['take', 'Take an offer']]"
        :key="m[0]"
        class="px-3 py-1 rounded border text-[12px]"
        :class="mode === m[0] ? 'border-accent/40 text-accent bg-accent/10' : 'border-line text-dim hover:text-fg'"
        @click="mode = m[0]"
      >
        {{ m[1] }}
      </button>
    </div>

    <template v-if="mode === 'make'">
      <p class="text-dim text-[12px]">
        The side that makes the offer invents the secret, so its leg takes the longer timelock. That is
        what stops the secret being a free option: whoever holds it must reveal it while the other side
        still has time to use it.
      </p>

      <div class="flex gap-1">
        <button
          v-for="d in [[true, 'I send SOL, I receive ZNN'], [false, 'I send ZNN, I receive SOL']]"
          :key="String(d[0])"
          class="flex-1 px-3 py-2 rounded border text-[12px]"
          :class="form.sendsSol === d[0] ? 'border-accent/40 text-accent bg-accent/10' : 'border-line text-dim hover:text-fg'"
          @click="form.sendsSol = d[0]"
        >
          {{ d[1] }}
        </button>
      </div>

      <div class="grid grid-cols-2 gap-3">
        <label class="block">
          <span class="text-dim text-[11px] uppercase tracking-wider">SOL amount</span>
          <input v-model="form.solAmount" class="w-full bg-panel-2 border border-line rounded px-2 py-1.5 mt-1" />
        </label>
        <label class="block">
          <span class="text-dim text-[11px] uppercase tracking-wider">Zenon amount</span>
          <div class="flex gap-2 mt-1">
            <input v-model="form.znnAmount" class="flex-1 min-w-0 bg-panel-2 border border-line rounded px-2 py-1.5" />
            <input v-model="form.znnToken" class="w-20 bg-panel-2 border border-line rounded px-2 py-1.5" />
          </div>
        </label>
      </div>

      <label class="block">
        <span class="text-dim text-[11px] uppercase tracking-wider">Your Zenon address</span>
        <input
          v-model="form.znnAddress"
          placeholder="z1q…"
          class="w-full bg-panel-2 border border-line rounded px-2 py-1.5 mt-1"
        />
        <span class="text-dim text-[11px]">{{ znnRole }}</span>
      </label>

      <details class="text-[12px]">
        <summary class="text-dim cursor-pointer hover:text-fg">Timelocks — 48h and 24h</summary>
        <div class="grid grid-cols-2 gap-3 mt-2">
          <label class="block">
            <span class="text-dim text-[11px] uppercase tracking-wider">Initiator (seconds)</span>
            <input v-model="form.longSeconds" class="w-full bg-panel-2 border border-line rounded px-2 py-1.5 mt-1" />
          </label>
          <label class="block">
            <span class="text-dim text-[11px] uppercase tracking-wider">Participant (seconds)</span>
            <input v-model="form.shortSeconds" class="w-full bg-panel-2 border border-line rounded px-2 py-1.5 mt-1" />
          </label>
        </div>
        <p class="text-dim mt-2">
          They must stay at least four hours apart. Below that the participant can be shown the secret
          too late to use it.
        </p>
      </details>
    </template>

    <template v-else>
      <p class="text-dim text-[12px]">
        Paste what the other side sent you. Which way round the swap goes is read from the offer, not
        chosen here — an offer that names a SOL sender is one you take by sending ZNN.
      </p>
      <textarea
        v-model="offerText"
        rows="3"
        placeholder="solzenoffer1:…"
        class="w-full bg-panel-2 border border-line rounded px-2 py-1.5 break-all"
        @blur="previewOffer"
      />
      <button class="text-[11px] text-dim hover:text-fg" @click="previewOffer">Read it</button>

      <dl v-if="preview" class="grid grid-cols-[8rem_1fr] gap-x-3 text-[12px] border-t border-line pt-2">
        <dt class="text-dim">You would send</dt>
        <dd>{{ preview.terms.solSender ? `${preview.terms.znnAmount} base units of ${preview.terms.znnToken}` : `${preview.terms.solLamports / 1e9} SOL` }}</dd>
        <dt class="text-dim">You would get</dt>
        <dd>{{ preview.terms.solSender ? `${preview.terms.solLamports / 1e9} SOL` : `${preview.terms.znnAmount} base units of ${preview.terms.znnToken}` }}</dd>
        <dt class="text-dim">Initiating leg</dt>
        <dd>{{ preview.terms.initiator }}</dd>
      </dl>

      <label class="block">
        <span class="text-dim text-[11px] uppercase tracking-wider">Your Zenon address</span>
        <input
          v-model="takeZnnAddress"
          placeholder="z1q…"
          class="w-full bg-panel-2 border border-line rounded px-2 py-1.5 mt-1"
        />
      </label>
    </template>

    <div class="flex items-center gap-3 pt-1">
      <button
        class="border border-accent/40 text-accent rounded px-3 py-1.5 hover:bg-accent/10 disabled:opacity-40"
        :disabled="busy"
        @click="submit"
      >
        {{ busy ? 'working…' : mode === 'make' ? 'Create the offer' : 'Accept' }}
      </button>
      <span class="text-dim text-[12px]">Solana wallet: {{ wallet.address || 'not connected' }}</span>
    </div>

    <p v-if="error" class="text-bad text-[12px] whitespace-pre-wrap">{{ error }}</p>
  </section>
</template>

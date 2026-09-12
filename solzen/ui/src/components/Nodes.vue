<script setup>
import { ref, onMounted } from 'vue'
import { call } from '../core/engine.js'

const config = ref({ solanaUrl: '', zenonUrl: '', solProgram: '' })
const probe = ref(null)
const busy = ref(false)
const error = ref('')

const emit = defineEmits(['changed'])

onMounted(async () => {
  config.value = await call('config.get')
  await check()
})

async function save() {
  error.value = ''
  try {
    config.value = await call('config.set', { ...config.value })
    emit('changed', config.value)
    await check()
  } catch (e) {
    error.value = String(e.message ?? e)
  }
}

async function check() {
  busy.value = true
  error.value = ''
  try {
    probe.value = await call('config.check')
  } catch (e) {
    error.value = String(e.message ?? e)
  } finally {
    busy.value = false
  }
}

defineExpose({ config, check })
</script>

<template>
  <section class="border border-line rounded bg-panel p-4 space-y-3">
    <header class="flex items-baseline justify-between">
      <h2 class="text-fg">Nodes</h2>
      <button class="text-[11px] text-dim hover:text-fg" :disabled="busy" @click="check">
        {{ busy ? 'checking…' : 'check' }}
      </button>
    </header>

    <p class="text-dim text-[12px]">
      Verifying the counterparty's contract is the one check where a dishonest answer costs money, so
      both nodes are yours to choose. The Zenon URL is
      <span class="text-fg">HTTP JSON-RPC on 35997</span> — not the WebSocket port on 35998 that Syrius
      and znn-cli use. Same node, different endpoint.
    </p>

    <label class="block">
      <span class="text-dim text-[11px] uppercase tracking-wider">Solana RPC</span>
      <input v-model="config.solanaUrl" class="w-full bg-panel-2 border border-line rounded px-2 py-1.5 mt-1" />
    </label>
    <label class="block">
      <span class="text-dim text-[11px] uppercase tracking-wider">Zenon node</span>
      <input v-model="config.zenonUrl" class="w-full bg-panel-2 border border-line rounded px-2 py-1.5 mt-1" />
    </label>
    <label class="block">
      <span class="text-dim text-[11px] uppercase tracking-wider">Swap program</span>
      <input v-model="config.solProgram" class="w-full bg-panel-2 border border-line rounded px-2 py-1.5 mt-1" />
    </label>

    <button class="border border-accent/40 text-accent rounded px-3 py-1.5 hover:bg-accent/10" @click="save">
      Save
    </button>

    <p v-if="error" class="text-bad text-[12px]">{{ error }}</p>

    <dl v-if="probe" class="grid grid-cols-[6rem_1fr] gap-x-3 gap-y-1 text-[12px] pt-2 border-t border-line">
      <dt class="text-dim">Solana</dt>
      <dd :class="probe.solana?.ok ? 'text-good' : 'text-bad'">
        <template v-if="probe.solana?.ok">
          v{{ probe.solana.version }}
          <span v-if="probe.solana.program?.ok" class="text-dim">· program deployed</span>
          <span v-else class="text-bad">· program: {{ probe.solana.program?.error }}</span>
        </template>
        <template v-else>{{ probe.solana?.error }}</template>
      </dd>
      <dt class="text-dim">Zenon</dt>
      <dd :class="probe.zenon?.ok ? 'text-good' : 'text-bad'">
        <template v-if="probe.zenon?.ok">
          chain {{ probe.zenon.chainIdentifier }} · momentum {{ probe.zenon.height }}
        </template>
        <template v-else>{{ probe.zenon?.error }}</template>
      </dd>
    </dl>
  </section>
</template>

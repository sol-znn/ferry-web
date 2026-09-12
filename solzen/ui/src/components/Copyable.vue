<script setup>
import { ref } from 'vue'

const props = defineProps({
  value: { type: String, default: '' },
  label: { type: String, default: '' },
  // Long strings -- offers, hashes -- wrap; short ones stay on one line.
  wrap: { type: Boolean, default: false },
})

const copied = ref(false)
async function copy() {
  try {
    await navigator.clipboard.writeText(props.value)
  } catch {
    // Clipboard access can be refused; selecting the text still works, and
    // saying nothing is better than an alert that interrupts a swap.
    return
  }
  copied.value = true
  setTimeout(() => (copied.value = false), 1200)
}
</script>

<template>
  <div>
    <div v-if="label" class="text-dim text-[11px] uppercase tracking-wider mb-1">{{ label }}</div>
    <div class="flex items-start gap-2">
      <code
        class="flex-1 min-w-0 bg-panel-2 border border-line rounded px-2 py-1.5 text-[12px] select-all"
        :class="wrap ? 'break-all' : 'truncate'"
        >{{ value }}</code
      >
      <button
        class="shrink-0 border border-line rounded px-2 py-1.5 text-[11px] text-dim hover:text-fg hover:border-dim"
        @click="copy"
      >
        {{ copied ? 'copied' : 'copy' }}
      </button>
    </div>
  </div>
</template>

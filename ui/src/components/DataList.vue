<script setup lang="ts">
// The key/value list the detail views are built from.
//
// Labels are ledger labels — the design system's mono/uppercase/tracked
// eyebrow — and values are laid out in a second column that keeps its own
// alignment, so a column of hashes reads as a column rather than as prose.
defineProps<{
  /** Two-column layout at md and up; single column below. */
  dense?: boolean
}>()
</script>

<template>
  <dl
    class="grid gap-x-6 gap-y-0"
    :class="dense ? 'sm:grid-cols-[minmax(7rem,auto)_1fr]' : 'sm:grid-cols-[minmax(9rem,auto)_1fr]'"
  >
    <slot />
  </dl>
</template>

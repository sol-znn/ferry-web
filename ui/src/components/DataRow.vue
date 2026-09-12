<script setup lang="ts">
defineProps<{label: string}>()
</script>

<template>
  <!-- display: contents so each row's two cells join the parent grid, which is
       what keeps every value in one column instead of each row aligning itself. -->
  <div class="contents">
    <dt
      class="border-b border-border/60 py-2 text-ledger text-muted-foreground sm:border-b-0 sm:pt-2.5"
    >
      {{ label }}
    </dt>
    <dd class="min-w-0 border-b border-border/60 pb-2 text-sm break-words sm:pt-2">
      <slot />
    </dd>
  </div>
</template>

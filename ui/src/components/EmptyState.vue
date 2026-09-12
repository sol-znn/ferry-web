<script setup lang="ts">
defineProps<{message: string; hint?: string}>()
</script>

<template>
  <div class="rounded-xl border border-dashed border-border px-6 py-16 text-center">
    <p class="text-sm text-muted-foreground">{{ message }}</p>
    <p v-if="hint" class="mt-2 font-mono text-xs text-muted-foreground/80">{{ hint }}</p>
  </div>
</template>

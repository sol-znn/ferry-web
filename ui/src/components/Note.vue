<script setup lang="ts">
import {ref} from 'vue'
import {ChevronRightIcon, InfoIcon, TriangleAlertIcon} from '@lucide/vue'

// A summary you can read at a glance, and content you only see if you ask for
// it — in place, taking a row of its own.
//
// Most of what used to be a Note is now an InfoTip: a sentence explaining one
// field belongs beside that field, not in a strip above it, and a page of
// strips is a page nobody reads. What stays here is what an InfoTip cannot
// hold — several paragraphs, or a block of data like a card's hash list.
withDefaults(defineProps<{summary: string; variant?: 'default' | 'warn'}>(), {
  variant: 'default',
})

const open = ref(false)
</script>

<template>
  <div
    class="rounded-md border text-sm"
    :class="variant === 'warn' ? 'border-warning/40 bg-warning/5' : 'border-border bg-muted/30'"
  >
    <button
      type="button"
      class="flex w-full items-center gap-2 px-3 py-2 text-left"
      :aria-expanded="open"
      @click="open = !open"
    >
      <component
        :is="variant === 'warn' ? TriangleAlertIcon : InfoIcon"
        class="size-3.5 shrink-0"
        :class="variant === 'warn' ? 'text-warning' : 'text-muted-foreground'"
      />
      <span
        class="min-w-0 flex-1 text-xs"
        :class="variant === 'warn' ? 'text-warning' : 'text-muted-foreground'"
      >
        <slot name="summary">{{ summary }}</slot>
      </span>
      <ChevronRightIcon
        class="size-3.5 shrink-0 text-muted-foreground transition-transform"
        :class="{'rotate-90': open}"
      />
    </button>

    <div v-if="open" class="border-t border-border/60 px-3 py-2.5 text-sm text-muted-foreground">
      <slot />
    </div>
  </div>
</template>

<script setup lang="ts">
import {computed} from 'vue'
import {CircleCheckIcon, InfoIcon, TriangleAlertIcon} from '@lucide/vue'

/**
 * One short message in the flow of a panel, in the colour of what it is saying.
 *
 * Distinct from `Note`, which hides several paragraphs behind a summary you can
 * skip. This is the opposite case and the commoner one on a swap card: a
 * sentence that has to be read now — a verdict, a refusal, a thing about to
 * become urgent — with nothing behind it to expand.
 */
const props = withDefaults(
  defineProps<{tone?: 'info' | 'success' | 'warning' | 'destructive'}>(),
  {tone: 'info'},
)

const skin = computed(
  () =>
    ({
      info: 'border-border bg-muted/30 text-muted-foreground',
      success: 'border-success/40 bg-success/5 text-success',
      warning: 'border-warning/40 bg-warning/5 text-warning',
      destructive: 'border-destructive/40 bg-destructive/5 text-destructive',
    })[props.tone],
)

const icon = computed(
  () =>
    ({
      info: InfoIcon,
      success: CircleCheckIcon,
      warning: TriangleAlertIcon,
      destructive: TriangleAlertIcon,
    })[props.tone],
)
</script>

<template>
  <div class="flex items-start gap-2 rounded-md border p-2.5 text-xs" :class="skin">
    <component :is="icon" class="mt-px size-3.5 shrink-0" aria-hidden="true" />
    <div class="min-w-0 flex-1"><slot /></div>
  </div>
</template>

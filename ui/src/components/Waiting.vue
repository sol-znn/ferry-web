<script setup lang="ts">
/**
 * Something is being waited on, and it is being waited on ACTIVELY.
 *
 * A swap spends most of its life in a state nobody can hurry: a block, a
 * momentum, a counterparty who has gone to make tea. The page has always said
 * so in words, and words alone read as a page that has stopped — which is
 * exactly the moment somebody starts reloading, pressing Refresh, or wondering
 * whether the thing is broken.
 *
 * Movement is the whole content of this component, and its only claim. One dot
 * flashing inside a bloom that swells and fades says "still going" without
 * pretending to know how far along anything is, which a progress bar would and
 * would be lying about: nothing here can predict when a counterparty acts or a
 * block arrives.
 *
 * `label` is what is being waited on, so the animation is never decoration on
 * its own — the movement says it is alive, the words say what for.
 */
import {computed} from 'vue'

const props = withDefaults(
  defineProps<{
    label?: string
    /** Fits the dot to whatever it sits beside. */
    tone?: 'muted' | 'primary' | 'warning' | 'success'
  }>(),
  {label: '', tone: 'muted'},
)

/** One class for both circles, so the bloom is always the dot's own colour. */
const dotClass = computed(() =>
  props.tone === 'primary'
    ? 'bg-primary'
    : props.tone === 'warning'
      ? 'bg-warning'
      : props.tone === 'success'
        ? 'bg-success'
        : 'bg-current',
)
</script>

<template>
  <span class="inline-flex items-center gap-2">
    <!-- aria-hidden, and the label beside it carries the meaning: a screen
         reader gets "waiting for their contract", not a bullet. The bloom is a
         second copy of the dot stacked underneath it, scaled out and faded by
         its own animation — drawn on the same 6px box so it costs no layout
         and cannot push the label around as it swells. Neither circle is
         gated on `motion-safe`: see the note in style.css. -->
    <span class="relative inline-flex size-1.5 shrink-0" aria-hidden="true">
      <span class="absolute inset-0 rounded-full animate-wait-bloom" :class="dotClass" />
      <span class="relative size-full rounded-full animate-wait-core" :class="dotClass" />
    </span>
    <span v-if="label" class="min-w-0">{{ label }}</span>
  </span>
</template>

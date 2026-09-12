<script setup lang="ts">
// Where a swap has got to, as a track broken into named segments.
//
// A swap is a five-move dance with a counterparty and two chains, and the state
// name on its own ("funded", "draft") says what happened without saying where
// that leaves you. This says where it leaves you, at a glance and without
// reading anything: filled segments are behind you, the half-lit one is now.
withDefaults(
  defineProps<{
    steps: readonly string[]
    current: number
    /**
     * Whether the current step is finished rather than under way.
     *
     * The half-lit segment means "this is happening now", which is the wrong
     * thing to say about the last one: a swap on the history page sat on Done
     * wearing the same washed-out bar as a swap mid-fund, so the track's only
     * two states were "in progress" and "in progress". Told the step is done,
     * the segment fills, and fills in success rather than in the track's own
     * colour — the card already answers "did this end well" in green
     * everywhere else, and the track should not be the one place that hedges.
     */
    done?: boolean
  }>(),
  {done: false},
)
</script>

<template>
  <ol
    class="grid auto-cols-fr grid-flow-col gap-1.5"
    :aria-label="
      done
        ? `Complete: ${steps[current]}`
        : `Step ${current + 1} of ${steps.length}: ${steps[current]}`
    "
  >
    <li
      v-for="(s, i) in steps"
      :key="s"
      class="grid gap-1.5"
      :aria-current="i === current ? 'step' : undefined"
    >
      <span
        class="h-1 rounded-full transition-colors"
        :class="
          i < current
            ? 'bg-primary'
            : i === current
              ? done
                ? 'bg-success'
                : 'bg-primary/45'
              : 'bg-border'
        "
      />
      <span
        class="truncate text-[11px] leading-none"
        :class="
          i === current
            ? done
              ? 'font-semibold text-success'
              : 'font-semibold text-foreground'
            : i < current
              ? 'text-muted-foreground'
              : 'text-muted-foreground/50'
        "
      >
        {{ s }}
      </span>
    </li>
  </ol>
</template>

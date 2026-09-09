<script setup lang="ts">
import {computed} from 'vue'
import {Button, CopyButton} from 'nom-ui'
import {ArrowUpRightIcon, CheckIcon, ChevronDownIcon, ChevronRightIcon} from '@lucide/vue'
import InfoTip from './InfoTip.vue'

// One value the counterparty needs, and the sentence saying why they need it.
//
// Every hand-off is a long hex string that has to arrive somewhere else intact.
// Folding them into "Technical details" is the right place for reference data
// and the wrong place for a step: a value you must send to somebody is not a
// detail, it is the move. So each gets a panel of its own for exactly as long as
// it is outstanding -- with a copy button rather than a hex string to select by
// hand, because a half-selected pubkey hash builds a contract nobody can spend.
const props = withDefaults(
  defineProps<{
    title: string
    /** What the counterparty does with it. One sentence. */
    hint: string
    value: string
    /** Tooltip label and body, when the value needs more than a sentence. */
    tipLabel?: string
    /**
     * Whether the panel is open, for a hand-off that can be finished.
     *
     * Left undefined the panel has no collapsed state and no control to reach
     * one, which is right for a value the user is still expected to send by
     * hand. Bound, the card can put it away once the hand-off has happened -- a
     * 200-character contract hex that has already gone over the session is
     * reference data from that moment on, and leaving it open pushes the step
     * that IS outstanding off the bottom of the card.
     */
    open?: boolean
    /** What to say in place of the value once it is closed. */
    doneNote?: string
  }>(),
  {
    // `open: undefined` is load-bearing, not a no-op. Vue casts an ABSENT prop
    // declared `boolean` to `false`, so the three states this component is
    // written around collapsed into two: every unbound panel came out
    // `open === false`, which reads here as collapsible-and-closed. The
    // pubkey-hash hand-off therefore rendered as a bare Show button hiding the
    // value, and pressing it did nothing, because the update:open it emits had
    // no parent listening. Declaring a default opts that cast out.
    open: undefined,
    tipLabel: undefined,
    doneNote: undefined,
  },
)

const emit = defineEmits<{'update:open': [boolean]}>()

const collapsible = computed(() => props.open !== undefined)
const shown = computed(() => props.open !== false)
</script>

<template>
  <section class="grid min-w-0 gap-2 rounded-lg border border-primary/40 bg-primary/5 p-3">
    <h3 class="flex flex-wrap items-center gap-1.5 text-sm font-semibold">
      <ArrowUpRightIcon class="size-3.5 shrink-0 text-primary" aria-hidden="true" />
      {{ title }}
      <InfoTip v-if="$slots.tip" :label="tipLabel ?? title">
        <slot name="tip" />
      </InfoTip>
      <template v-if="collapsible">
        <span class="flex-1" />
        <Button variant="ghost" size="sm" @click="emit('update:open', !shown)">
          <component :is="shown ? ChevronDownIcon : ChevronRightIcon" />
          {{ shown ? 'Hide' : 'Show' }}
        </Button>
      </template>
    </h3>

    <template v-if="shown">
      <p class="text-sm text-muted-foreground">{{ hint }}</p>
      <!-- min-w-0 is load-bearing on a grid item: without it an unbroken
           200-character contract hex widens the whole card. -->
      <div class="flex items-start gap-2 rounded-md bg-background p-3">
        <code class="min-w-0 flex-1 font-mono text-xs break-all">{{ value }}</code>
        <CopyButton :value="value" size="icon-sm" />
      </div>
      <slot />
    </template>

    <!-- Closed, it is still a row on the card rather than nothing: the value
         has been handed over, and saying so is what stops somebody sending it
         twice or wondering whether it went. -->
    <p v-else-if="doneNote" class="flex items-center gap-1.5 text-sm text-muted-foreground">
      <CheckIcon class="size-3.5 shrink-0 text-success" aria-hidden="true" />
      {{ doneNote }}
    </p>
  </section>
</template>

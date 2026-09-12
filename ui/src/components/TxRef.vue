<script setup lang="ts">
import {computed} from 'vue'
import {useClipboard} from '@vueuse/core'
import {CheckIcon, ReceiptTextIcon} from '@lucide/vue'
import {Tooltip, TooltipContent, TooltipTrigger} from 'nom-ui'

/**
 * A transaction, as the small thing people actually do with one.
 *
 * A txid printed in full is 64 characters of hex, and on the funding row it was
 * closing a line whose real content is an amount and a confirmation count —
 * three words of news behind a wall of hash. Nobody reads a txid. They copy it
 * into an explorer, or they check the first few characters against something
 * else, and both of those are one click and a hover.
 *
 * So the value is here, in full, in the bubble and on the clipboard; what is on
 * the card is the fact that there is a transaction.
 */
const props = defineProps<{
  txid: string
  /** Output index, when what is being pointed at is one output of the tx. */
  vout?: number
  /** Screen-reader name — say which transaction, e.g. "funding transaction". */
  label: string
}>()

/** What is copied and what the bubble shows: the outpoint when there is one,
 *  because `txid:vout` is what names a specific output to every Bitcoin tool
 *  that takes one. */
const value = computed(() =>
  props.vout === undefined ? props.txid : `${props.txid}:${props.vout}`,
)

// legacy: true for the same reason nom-ui's CopyButton sets it — the async
// clipboard API is unavailable outside a secure context, and this page is meant
// to run from a file:// copy as readily as from a URL.
const {copy, copied} = useClipboard({legacy: true})
</script>

<template>
  <!-- The tooltip opens on hover and the click copies, so neither gets in the
       other's way. `disable-closing-trigger` is what stops the copying click
       from also shutting the bubble it just opened — see the longer note in
       InfoTip.vue, which hit the same two problems. -->
  <Tooltip disable-closing-trigger :delay-duration="120">
    <TooltipTrigger
      type="button"
      :aria-label="copied ? `Copied ${label}` : `Copy ${label} ${value}`"
      class="ml-2 inline-flex items-center gap-1 rounded border border-border px-1.5 py-0.5 align-[-2px] font-mono text-[11px] leading-none text-muted-foreground transition-colors hover:border-primary hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
      @click="copy(value)"
    >
      <component
        :is="copied ? CheckIcon : ReceiptTextIcon"
        class="size-3 shrink-0"
        :class="copied ? 'text-success' : ''"
        aria-hidden="true"
      />
      tx
    </TooltipTrigger>
    <!-- break-all, or a 64-character hash with no break opportunity in it
         ignores the bubble's max width and runs off the side of the page. -->
    <TooltipContent :collision-padding="12" class="max-w-72 font-mono text-xs break-all text-wrap">
      {{ value }}
    </TooltipContent>
  </Tooltip>
</template>

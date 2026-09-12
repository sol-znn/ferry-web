<script setup lang="ts">
import {computed, ref} from 'vue'
import {ArchiveIcon, ChevronDownIcon, ChevronRightIcon, DownloadIcon, Trash2Icon} from '@lucide/vue'
import {Address, Badge, Button} from 'nom-ui'
import type {BadgeVariants} from 'nom-ui'
import SwapCard from './SwapCard.vue'
import {api, download} from '@/core/api'
import {stamp} from '@/core/format'
import type {Swap} from '@/types'

/**
 * One finished swap, as a line rather than a card.
 *
 * The full swap card carries hand-off panels, wallet buttons, the Zenon section
 * and the event log -- an entire working surface for a swap that has stopped
 * working. Twenty of those was several screens of controls nobody was going to
 * press.
 *
 * What a finished swap is consulted for is four things: what was traded, with
 * whom, how it ended and when. Everything else is still one click away, because
 * "what happened to that swap" is a question the card answers well.
 */
const props = defineProps<{
  swap: Swap
  /**
   * Offer the archive action on this row. Set on the Swaps page, where a
   * settled swap now waits: both legs are finished, nothing files itself, and
   * the only thing left is the press that moves it to History. On the History
   * page it is off, because everything there is already filed.
   */
  archivable?: boolean
}>()
const emit = defineEmits<{changed: []}>()

const open = ref(false)

/**
 * Deleting, as two presses rather than one.
 *
 * A delete here is not "remove from a list": the record holds the only key that
 * can spend this swap's contract, localStorage has no bin, and the swap next to
 * the one somebody meant looks exactly like it. So the trash icon opens a panel
 * that says what goes and offers the recovery file.
 *
 * Inline rather than a modal because the row it is about stays on screen: a
 * dialog over a page of near-identical rows is the question without the thing it
 * is about.
 */
const confirming = ref(false)
const busy = ref(false)
const error = ref('')

/**
 * What Go will say about this delete before it is asked. Mirrors
 * Swap.DeletionRisk, and only to decide how loudly to ask -- the refusal itself
 * is in Go, where a page cannot walk around it. Anything not redeemed or
 * refunded may still have money behind it, which is most likely for a swap
 * archived by hand halfway through and now sitting here looking settled.
 */
const risky = computed(() => !props.swap.settled)

async function saveRecovery() {
  error.value = ''
  try {
    const rec = await api.recovery(props.swap.id)
    download(`swap-${props.swap.id}-recovery.json`, JSON.stringify(rec, null, 2))
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

/**
 * File it. The one action `archivable` rows offer beyond a plain history
 * line, so it needs none of SwapCard's toggle — a settled swap offered here
 * is never archived yet. A ref of its own rather than reusing `error`: that
 * one belongs to the delete panel below, and a failure here should not have
 * to wait for that panel to be open to be seen, or appear in it unasked.
 */
const archiveError = ref('')
async function archive() {
  archiveError.value = ''
  busy.value = true
  try {
    await api.archive(props.swap.id, true)
    emit('changed')
  } catch (e) {
    archiveError.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

async function remove() {
  error.value = ''
  busy.value = true
  try {
    // `risky` is this side's read of the same rule; passing it as force is the
    // second answer to a question the panel has already put in writing. Go
    // still decides, and a swap it thinks is riskier than this page does is
    // refused rather than forced.
    await api.remove(props.swap.id, risky.value)
    emit('changed')
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    busy.value = false
  }
}

/** The trade in one line, read in the direction the coins moved. Mirrors the
 *  card's own `trade` so the line and the card it opens agree. */
const trade = computed(() => ({
  from: props.swap.out?.label ?? '',
  to: props.swap.in?.label ?? '',
}))

/**
 * Who it was with: their Zenon address, because that is the one identifier of a
 * counterparty a person recognises later. The pubkey hash stands in when a swap
 * never got as far as naming one, and neither is a claim about who they are --
 * it is what this browser was told.
 */
const counterparty = computed(
  () =>
    props.swap.in?.peerAddr ||
    props.swap.out?.peerAddr ||
    props.swap.out?.btc?.counterpartyPkhHex ||
    props.swap.in?.btc?.counterpartyPkhHex ||
    '',
)

/**
 * When it ended, or when it started if it never did. Only a redeem or a refund
 * is an ending: everything else here -- expired, or archived by hand mid-flight
 * -- stopped rather than finished, and dating those by their last event would
 * put a completion time on a swap that never completed.
 */
const finished = computed(() => props.swap.settled)

const when = computed(() => {
  if (!finished.value) return {label: 'started', at: props.swap.createdAt}
  // UpdatedAt is not in the API, but it moves only when the swap logs
  // something (see Swap.log in wasm/swap.go) — so the last event carries the
  // same instant, and it is already here.
  const last = props.swap.events?.[props.swap.events.length - 1]
  return {label: 'finished', at: last?.at ?? props.swap.createdAt}
})

const stateVariant = computed<BadgeVariants['variant']>(() => {
  switch (props.swap.state) {
    case 'settled':
      return 'success'
    case 'refunded':
    case 'expired':
      return 'warning'
    default:
      return 'outline'
  }
})
</script>

<template>
  <section class="min-w-0 rounded-lg border border-border">
    <div class="flex min-w-0 items-center gap-1 pr-2">
      <!-- Nearly the whole line is the control. A row this wide with a chevron
           at one end and nothing clickable in between is a row people click in
           the middle of and nothing happens. The delete is the one thing kept
           out of it, because it is a sibling button and not a second meaning
           for the same press. -->
      <button
        type="button"
        class="flex min-w-0 flex-1 flex-wrap items-center gap-x-3 gap-y-1.5 rounded-lg px-3 py-2.5 text-left transition-colors hover:bg-muted/40 focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        :aria-expanded="open"
        @click="open = !open"
      >
        <component
          :is="open ? ChevronDownIcon : ChevronRightIcon"
          class="size-4 shrink-0 text-muted-foreground"
          aria-hidden="true"
        />
        <Badge :variant="stateVariant">{{ swap.state.replace('_', ' ') }}</Badge>

        <span class="flex min-w-0 items-center gap-1.5 font-mono text-xs">
          <span class="truncate">{{ trade.from }}</span>
          <span class="text-muted-foreground" aria-hidden="true">→</span>
          <span class="truncate">{{ trade.to }}</span>
        </span>

        <span class="flex-1" />

        <span v-if="counterparty" class="flex items-center gap-1.5 text-xs text-muted-foreground">
          with
          <!-- copy off: this sits inside the expand button, and a copy button
               within one is a click that means two things. The address is in
               the card below, with its copy, one press away. -->
          <Address :address="counterparty" :start="8" :end="4" :copy="false" :tooltip="false" />
        </span>

        <span class="font-mono text-xs whitespace-nowrap text-muted-foreground">
          {{ when.label }} {{ stamp(when.at) }}
        </span>
      </button>

      <Button
        v-if="archivable"
        variant="outline"
        size="sm"
        class="shrink-0"
        :disabled="busy"
        @click="archive"
      >
        <ArchiveIcon />
        Archive
      </Button>
      <Button
        variant="ghost"
        class="size-7 shrink-0 text-muted-foreground hover:text-destructive"
        aria-label="Delete this swap from your history"
        @click="confirming = !confirming"
      >
        <Trash2Icon />
      </Button>
    </div>

    <p v-if="archiveError" class="px-3 pb-2 text-xs text-destructive">{{ archiveError }}</p>

    <!-- The second press. It says what goes and offers the recovery file
         first, because the file is the whole of what a deleted swap leaves
         behind and this is the last moment it can be taken. -->
    <div
      v-if="confirming"
      class="grid gap-2 border-t border-destructive/40 bg-destructive/5 p-3 text-sm"
    >
      <p class="min-w-0">
        <strong>Delete this swap?</strong> It goes from this browser for good, along with the only
        key that can spend its contract. There is no undo.
      </p>
      <p v-if="risky" class="min-w-0 text-destructive">
        This one was never seen redeemed or refunded, so its contract may still hold money. Take the
        recovery file before you delete it.
      </p>
      <p v-if="error" class="text-destructive">{{ error }}</p>
      <div class="flex flex-wrap gap-2">
        <Button size="sm" variant="destructive" :disabled="busy" @click="remove">
          Delete permanently
        </Button>
        <Button size="sm" variant="outline" :disabled="busy" @click="saveRecovery">
          <DownloadIcon />
          Recovery file
        </Button>
        <Button size="sm" variant="ghost" :disabled="busy" @click="confirming = false">
          Keep it
        </Button>
      </div>
    </div>

    <!-- Mounted only when opened. A history page holds every swap this browser
         has ever finished, and a swap card is not a cheap component: it builds
         commands, prices unlocks and watches a session. Rendering all of them
         to keep them hidden is the cost this page was redesigned to stop
         paying. -->
    <div v-if="open" class="border-t border-border p-3">
      <SwapCard :swap="swap" @changed="$emit('changed')" />
    </div>
  </section>
</template>

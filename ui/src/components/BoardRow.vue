<script setup lang="ts">
import {computed} from 'vue'
import {
  ArrowRightLeftIcon,
  BadgeCheckIcon,
  ClockIcon,
  TriangleAlertIcon,
  UserIcon,
} from '@lucide/vue'
import {Badge, Button} from 'nom-ui'
import InfoTip from './InfoTip.vue'
import PresenceDot from './PresenceDot.vue'
import {
  EXPIRING_SOON_SECONDS,
  bandLabel,
  formatRate,
  hasProof,
  inverseRate,
  postAddresses,
  rate,
  rateUnits,
  secondsLeft,
  shortKey,
  timeLeft,
} from '@/core/board'
import {
  chainLabel,
  chainList,
  chainsForPost,
  legLabel,
  missingChains,
  pairLabel,
  schemeFor,
  unitFor,
  type ChainId,
} from '@/core/chains'
import {useBoard} from '@/core/composables/useBoard'
import type {Listing} from '@/types'

// One offer, as a row.
//
// A row rather than a card, because the thing a board is for is comparison: the
// same three numbers in the same three places down the page, so a rate that is
// out of line is visible without reading anything. Cards are for the swap you
// have decided to do.
//
// The headline is written from the READER's side, not the author's. Every field
// on a post is stored from the author's point of view — `give` is what THEY fund
// — and a board that showed that verbatim would be a board every reader has to
// invert in their head, every row, forever.

const props = defineProps<{
  listing: Listing
  nowMs: number
  /** Which chains this browser has proven an address on. Passed down rather than
   *  asked per row: reading it pulls in `useUnisat`, which registers a mounted
   *  hook, so two hundred rows answering it themselves would attach two hundred
   *  of them to one question. What this row does with the answer is pure. */
  proven?: ChainId[]
}>()
defineEmits<{take: [Listing]}>()

const board = useBoard()

const post = computed(() => props.listing.post)

/**
 * Whether this offer's author is at a keyboard.
 *
 * Computed from `nowMs` rather than from the clock, so the dot lapses to grey on
 * the same ticker that moves the countdown beside it. Presence is the one thing
 * on this row that changes without anything arriving — a browser that closed
 * sends nothing to say so — so a value that only recomputed when a beat landed
 * would be a dot that could never go out.
 */
const presence = computed(() => board.presenceOf(props.listing.author, props.nowMs))

/** What the reader would be doing, in the fewest words that are still exact.
 *  The reader funds the author's `want` and receives the author's `give`. */
const headline = computed(() => ({
  verb: `Pay ${unitFor(post.value.want.chain, post.value.want.token)}`,
  detail: `you pay ${chainLabel(post.value.want.chain)}, they pay ${chainLabel(post.value.give.chain)}`,
}))

/** The chains this offer settles on, and the addresses it names on each. */
const chains = computed(() => chainsForPost(post.value))
const addresses = computed(() => postAddresses(post.value))

const soon = computed(
  () => secondsLeft(post.value, props.nowMs) <= EXPIRING_SOON_SECONDS && !props.listing.expired,
)

const takeable = computed(
  () => !props.listing.expired && post.value.status === 'open' && !props.listing.mine,
)

/**
 * Whether this offer can be answered at all, as opposed to whether YOU can
 * answer it.
 *
 * An offer carries where its author wants each half paid, and one that names
 * neither cannot be turned into a swap without both people going and finding
 * somewhere else to exchange addresses — which is the thing the board now
 * exists to avoid. This build will not publish such a post; it still reads the
 * ones already on relays, so the row stays visible and says why instead of
 * offering a button that leads nowhere.
 */
const answerable = computed(() => addresses.value.length === chains.value.length)

/**
 * Whether YOU can answer it: the chains this offer settles on, minus the ones
 * you have proven an address on.
 *
 * Per offer rather than per page, because that is the rule. A Bitcoin-against-
 * Zenon offer needs both proofs; an offer between two Zenon tokens needs Zenon
 * and nothing else, and a row that asked for a Bitcoin proof to trade it would
 * be asking for a leg that does not exist.
 */
const missing = computed(() => missingChains(chains.value, props.proven ?? []))
</script>

<template>
  <div
    class="grid gap-3 rounded-lg border p-3 sm:grid-cols-[1fr_auto] sm:items-center"
    :class="
      listing.expired || post.status !== 'open'
        ? 'border-border/60 bg-muted/20 opacity-70'
        : 'border-border'
    "
  >
    <div class="grid min-w-0 gap-1.5">
      <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span class="text-sm font-semibold">{{ headline.verb }}</span>
        <Badge variant="outline">{{ pairLabel(post.give.chain, post.want.chain) }}</Badge>
        <span class="text-xs text-muted-foreground">{{ headline.detail }}</span>

        <Badge v-if="post.status === 'taken'" variant="pending">taken</Badge>
        <Badge v-else-if="post.status === 'done'" variant="outline">done</Badge>
        <Badge v-else-if="post.status === 'void'" variant="outline">withdrawn</Badge>
        <Badge v-if="listing.expired" variant="outline">expired</Badge>
      </div>

      <!-- The three numbers, in one line, in the same order on every row. -->
      <div class="flex flex-wrap items-baseline gap-x-4 gap-y-1">
        <span class="font-mono text-sm">{{ legLabel(post.want) }}</span>
        <ArrowRightLeftIcon class="size-3 shrink-0 text-muted-foreground" />
        <span class="font-mono text-sm">{{ legLabel(post.give) }}</span>
        <span class="font-mono text-xs text-muted-foreground">
          {{ formatRate(rate(post)) }} {{ rateUnits(post) }}
          <span class="opacity-70">· {{ formatRate(inverseRate(post)) }} the other way</span>
        </span>
      </div>

      <div class="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
        <span v-if="bandLabel(post)" class="font-mono">{{ bandLabel(post) }}</span>

        <!-- The dot sits with the author, not with the offer. It is a fact
             about a person and would read as a fact about the post anywhere
             else on this row. -->
        <span class="inline-flex items-center gap-1">
          <UserIcon class="size-3" />
          <code class="font-mono">{{ shortKey(listing.author) }}</code>
          <PresenceDot :presence="presence" />
        </span>

        <!-- A badge means a signature checked out HERE, against this reader's
             network, for an address this post actually names. -->
        <span
          v-for="a in addresses"
          :key="a.chain"
          v-show="hasProof(listing, schemeFor(a.chain))"
          class="inline-flex items-center gap-1 text-success"
          :title="a.addr"
        >
          <BadgeCheckIcon class="size-3" />
          {{ chainLabel(a.chain) }}
        </span>
        <span v-if="!listing.verified?.length" class="inline-flex items-center gap-1">
          unverified
          <InfoTip label="What unverified means here">
            <p>
              This post is signed — only its author can edit or withdraw it — but no proof of the
              addresses on it checked out here. Offers made by this build always carry one, so this
              is either an older post or a proof this reader could not verify.
            </p>
            <p>
              A badge only tells you the author controls the address they named. It tells you
              nothing about whether they will trade fairly, and the swap itself is what protects
              you: both legs settle or both refund.
            </p>
          </InfoTip>
        </span>

        <span v-if="post.lockHours" class="font-mono">{{ post.lockHours }}h lock</span>

        <span
          class="inline-flex items-center gap-1"
          :class="soon ? 'text-warning' : ''"
          :title="new Date(post.expiresAt * 1000).toISOString()"
        >
          <ClockIcon class="size-3" />
          {{ timeLeft(post, nowMs) }}
        </span>

        <!-- Self-reported and labelled as such. Nothing verifies it, and a
             number presented as a score would be read as one. -->
        <span v-if="post.completed" class="inline-flex items-center gap-1">
          claims {{ post.completed }} swaps
          <InfoTip variant="warn" label="About this number">
            The author typed it. Nothing on this page or anywhere else checks it, and it costs
            nothing to inflate. It is shown because people ask for it, not because it means
            anything.
          </InfoTip>
        </span>
      </div>

      <p v-if="post.note" class="text-xs break-words text-muted-foreground">{{ post.note }}</p>

      <!-- Problems are the most interesting thing on a board: a post carrying a
           broken proof of somebody else's address belongs on screen, not
           filtered away where nobody learns about it. -->
      <p
        v-for="(problem, i) in listing.problems"
        :key="i"
        class="inline-flex items-start gap-1 text-xs text-destructive"
      >
        <TriangleAlertIcon class="mt-0.5 size-3 shrink-0" />
        {{ problem }}
      </p>
    </div>

    <div class="flex items-center gap-2 sm:justify-end">
      <template v-if="takeable">
        <Button
          size="sm"
          :disabled="missing.length > 0 || !answerable"
          @click="$emit('take', listing)"
        >
          Take
        </Button>
        <InfoTip v-if="!answerable" variant="warn" label="Why this cannot be taken">
          This offer does not name where its author wants either half of the trade paid, so there is
          no way to build a swap from it without arranging that somewhere else. Offers posted by
          this build always carry both.
        </InfoTip>
        <InfoTip v-else-if="missing.length" label="Why Take is disabled">
          <p>
            This offer settles on {{ chainList(chains) }}, and you have not proven
            {{ missing.length === 1 ? 'an address' : 'addresses' }} on {{ chainList(missing) }} yet.
            The {{ missing.length === 1 ? 'button is' : 'buttons are' }} under your board key at the
            top of this page.
          </p>
          <p>
            Taking sends the author the addresses your half gets paid to. A proof is what says they
            are yours rather than whatever a page put in the field.
          </p>
        </InfoTip>
      </template>
      <span v-else-if="listing.mine" class="text-xs text-muted-foreground">yours</span>
    </div>
  </div>
</template>

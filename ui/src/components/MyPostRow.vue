<script setup lang="ts">
import {computed} from 'vue'
import {
  ArrowRightLeftIcon,
  ClockIcon,
  PencilIcon,
  RotateCwIcon,
  Trash2Icon,
  XIcon,
} from '@lucide/vue'
import {Badge, Button} from 'nom-ui'
import InfoTip from './InfoTip.vue'
import {
  EXPIRING_SOON_SECONDS,
  formatRate,
  rate,
  rateUnits,
  secondsLeft,
  timeLeft,
} from '@/core/board'
import {legLabel, unitFor} from '@/core/chains'
import type {MyPost} from '@/types'

// One of your own posts.
//
// A different row from BoardRow because a different question is being asked. On
// the market you are comparing offers; here you are managing one, and what
// matters is its state and the four things you can do to it — edit, renew,
// withdraw, forget.
//
// Expired and withdrawn posts stay in this list. An offer that quietly vanished
// at midnight is one you cannot renew, and renewing is the common case for a
// post that timed out while its author was asleep.

const props = defineProps<{entry: MyPost; nowMs: number; busy?: boolean}>()
defineEmits<{edit: [MyPost]; renew: [MyPost]; withdraw: [MyPost]; forget: [MyPost]}>()

const post = computed(() => props.entry.post)
const expired = computed(() => secondsLeft(post.value, props.nowMs) <= 0)
/** Live on the open board — what the row's own styling reflects. */
const standing = computed(() => post.value.status === 'open' && !expired.value)
/**
 * Still out there on the relays, which is a wider question than `standing` by
 * exactly one status: a taken post is off the open board but its record is still
 * published, its session is still running, and takes still arrive against it.
 *
 * This mirrors `BoardPost.Standing` in wasm/boardpost.go, and has to: it decides
 * whether the Forget button is offered, and Go refuses the same set. A UI that
 * enabled it one status wider would be a button whose only outcome is an error.
 */
const onRelays = computed(
  () => (post.value.status === 'open' || post.value.status === 'taken') && !expired.value,
)
const soon = computed(
  () => secondsLeft(post.value, props.nowMs) <= EXPIRING_SOON_SECONDS && !expired.value,
)
</script>

<template>
  <div
    class="grid gap-2 rounded-lg border p-3 sm:grid-cols-[1fr_auto] sm:items-center"
    :class="standing ? 'border-border' : 'border-border/60 bg-muted/20'"
  >
    <div class="grid min-w-0 gap-1.5">
      <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span class="text-sm font-semibold">
          You pay {{ unitFor(post.give.chain, post.give.token) }}
        </span>
        <Badge v-if="post.status === 'open' && !expired" variant="success">live</Badge>
        <Badge v-else-if="post.status === 'taken'" variant="pending">taken</Badge>
        <Badge v-else-if="post.status === 'done'" variant="outline">done</Badge>
        <Badge v-else-if="post.status === 'void'" variant="outline">withdrawn</Badge>
        <Badge v-else variant="warning">expired</Badge>

        <!-- The join between a post and the swap it became. It is what lets the
             board retire itself: a settled swap withdraws the offer it came
             from, without anything polling a chain for a post. -->
        <Badge v-if="entry.swapId" variant="outline">swap {{ entry.swapId }}</Badge>
        <code class="font-mono text-xs text-muted-foreground">{{ post.id }}</code>
      </div>

      <div class="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <span class="font-mono text-sm">{{ legLabel(post.give) }}</span>
        <ArrowRightLeftIcon class="size-3 shrink-0 text-muted-foreground" />
        <span class="font-mono text-sm">{{ legLabel(post.want) }}</span>
        <span class="font-mono text-xs text-muted-foreground">
          {{ formatRate(rate(post)) }} {{ rateUnits(post) }}
        </span>
        <span
          class="inline-flex items-center gap-1 text-xs"
          :class="soon ? 'text-warning' : 'text-muted-foreground'"
          :title="new Date(post.expiresAt * 1000).toISOString()"
        >
          <ClockIcon class="size-3" />
          {{ timeLeft(post, nowMs) }}
        </span>
      </div>

      <p v-if="post.note" class="text-xs break-words text-muted-foreground">{{ post.note }}</p>
    </div>

    <div class="flex flex-wrap items-center gap-1.5 sm:justify-end">
      <Button
        v-if="post.status !== 'done' && post.status !== 'void'"
        variant="outline"
        size="sm"
        :disabled="busy"
        @click="$emit('edit', entry)"
      >
        <PencilIcon />
        Edit
      </Button>

      <!-- Renewing is republishing with a fresh deadline. Offered on an expired
           post as well as a live one, because the expired one is exactly the
           post somebody wants back. -->
      <Button
        v-if="post.status === 'open'"
        variant="outline"
        size="sm"
        :disabled="busy"
        @click="$emit('renew', entry)"
      >
        <RotateCwIcon />
        Renew
      </Button>

      <Button
        v-if="post.status === 'open' || post.status === 'taken'"
        variant="ghost"
        size="sm"
        :disabled="busy"
        @click="$emit('withdraw', entry)"
      >
        <XIcon />
        Withdraw
      </Button>

      <!-- Disabled while the post is standing, and the tooltip beside it says
           why. Go refuses this outright (Store.DeleteMyPost) — a button that
           only ever produced that error would be a button offering to do
           something the module will not do. -->
      <Button
        variant="ghost"
        size="icon-sm"
        :disabled="busy || onRelays"
        :aria-label="onRelays ? 'Withdraw this post before forgetting it' : 'Forget this post'"
        @click="$emit('forget', entry)"
      >
        <Trash2Icon />
      </Button>
      <InfoTip
        :variant="onRelays ? 'warn' : 'default'"
        :label="onRelays ? 'Withdraw before you can forget this' : 'What forget does'"
      >
        <p>
          Forgetting removes this browser's record of the post — the record that lets you edit,
          renew or withdraw it. It reaches no relay.
        </p>
        <p v-if="onRelays">
          This offer is still standing, so forgetting it now would not take it down. It would strand
          it: still takeable by anyone, until it expires, with the only browser that could retract
          it having thrown away the key to. <strong>Withdraw</strong> publishes the retraction —
          after that this is a tidy-up.
        </p>
        <p v-else>
          Nothing is standing on the relays for this post, so there is nothing left to strand.
        </p>
      </InfoTip>
    </div>
  </div>
</template>

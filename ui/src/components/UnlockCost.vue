<script setup lang="ts">
import {computed, ref} from 'vue'
import {Button} from 'nom-ui'
import {sats} from '@/core/format'
import type {SpendCost} from '@/types'

// What it costs to get back out, shown where the getting-in happens.
//
// The failure this exists for is silent until it is expensive: a contract is
// funded with an amount that is fine, and hours later the recipient finds that
// unlocking it leaves them under the dust limit, where no node will relay the
// transaction at all. By then the money is locked and both branches out of the
// contract meet the same arithmetic.
//
// One number answers that, so one number is what this shows: what actually
// arrives. Everything else — the fee, the vsize, the rate, the ceiling, the
// verdict and the amount that would clear it — is how that number was reached,
// and all of it now sits behind More info.
//
// That is a reversal. The verdict used to be printed on the page, on the
// argument that an amount which cannot be unlocked is not a detail. It is not a
// detail, but four lines of arithmetic in front of somebody filling in a form is
// not how you get it read either — it was the largest block of text on the
// panel, next to the field it was about, on every swap whether or not anything
// was wrong. What survives on the page is the part that cannot be skimmed past:
// the figure itself, in red when it does not work, beside a button that says so.
const detail = ref(false)
const props = defineProps<{
  cost: SpendCost | null
  loading?: boolean
  error?: string
  /** Shown when the amount is short and there is a field to raise. */
  onUseRecommended?: (amount: number) => void
  /** 'you' when this user is the one who will unlock, 'them' otherwise. It
   *  changes nothing about the arithmetic and everything about the sentence. */
  unlockedBy?: 'you' | 'them'
}>()

const who = computed(() => (props.unlockedBy === 'them' ? 'They receive' : 'You receive'))
const bad = computed(() => props.cost !== null && props.cost.verdict !== 'ok')

// A quote is only as good as the fee rate under it, and a fallback rate is a
// guess made because no node answered. Saying which is which is the difference
// between a number and a number you can act on.
const rateNote = computed(() => {
  const c = props.cost
  if (!c) return ''
  switch (c.feeRateFrom) {
    case 'node':
      return `${c.feeRate} sat/vB, live from your Bitcoin node`
    case 'you':
      return `${c.feeRate} sat/vB, the rate you set`
    default:
      return `${c.feeRate} sat/vB — no node answered, so this is a guess`
  }
})
</script>

<template>
  <p v-if="error" class="text-xs text-muted-foreground">
    Could not work out the unlock cost: {{ error }}
  </p>

  <p v-else-if="loading && !cost" class="text-xs text-muted-foreground">
    Working out what unlocking this will cost…
  </p>

  <div v-else-if="cost" class="grid gap-1.5">
    <!-- The one line that answers the question: what actually arrives. -->
    <p class="flex flex-wrap items-baseline gap-x-1.5 text-sm">
      <span class="text-muted-foreground">{{ who }}</span>
      <!-- Bitcoin orange, because this is the Bitcoin figure that matters and
           it should be findable without reading the sentence around it. -->
      <span
        class="font-mono text-sm font-semibold tabular-nums"
        :class="cost.redeem.viable ? 'text-[#F7931A]' : 'text-destructive'"
      >
        {{ cost.redeem.net.toLocaleString('en-US') }}<span class="font-normal">&nbsp;sat</span>
      </span>
      <span class="text-muted-foreground">after the unlock fee</span>
      <!-- The whole of the explanation is behind this, including the verdict —
           so when there is a problem the button has to carry that, or the only
           signal left is the colour of a number. -->
      <Button
        :variant="bad ? 'outline' : 'ghost'"
        size="sm"
        class="h-6 px-2 text-xs"
        :class="bad ? (cost.verdict === 'unspendable' ? 'text-destructive' : 'text-warning') : ''"
        :aria-expanded="detail"
        @click="detail = !detail"
      >
        {{ detail ? 'Less' : bad ? 'Why this will not work' : 'More info' }}
      </Button>
    </p>

    <div v-if="detail" class="grid gap-2 rounded-md border border-border bg-muted/40 p-3">
      <!-- An amount that cannot be unlocked leads, because it is the answer to
           the question that opened this. -->
      <p
        v-if="bad"
        class="text-sm font-semibold"
        :class="cost.verdict === 'unspendable' ? 'text-destructive' : 'text-warning'"
      >
        {{ cost.summary }}
      </p>

      <div v-if="bad && onUseRecommended" class="flex flex-wrap items-center gap-2">
        <Button variant="outline" size="sm" @click="onUseRecommended?.(cost.recommended)">
          Use {{ cost.recommended.toLocaleString('en-US') }} sat instead
        </Button>
        <span class="text-xs text-muted-foreground">
          clears the dust limit even if fees reach {{ cost.headroomRate }} sat/vB
        </span>
      </div>

      <p class="text-xs text-muted-foreground">
        A contract is emptied by one transaction with one input and one output, and its fee is paid
        out of the contract — not out of the unlocking wallet. So the amount locked is never the
        amount received.
      </p>
      <p class="text-xs text-muted-foreground">
        <strong>{{ sats(cost.amountSats) }}</strong> locked, less
        <strong>{{ sats(cost.redeem.fee) }}</strong> to unlock ({{ cost.redeem.vsize }} vB at
        {{ rateNote }}), leaves <strong>{{ sats(cost.redeem.net) }}</strong
        >.
      </p>
      <p v-if="cost.maxFeeRate" class="text-xs text-muted-foreground">
        It keeps working up to <strong>{{ cost.maxFeeRate }} sat/vB</strong>. Below the
        {{ cost.dustLimit }} sat dust limit no node relays the transaction at all, and the refund
        branch hits the same wall — so the coins would stay locked either way.
      </p>
      <p v-if="cost.destAssumed" class="text-xs text-muted-foreground">
        Sized against a taproot payout, the widest common form, because no destination address was
        given yet — a real address can only make it cheaper.
      </p>
    </div>
  </div>
</template>

<script setup lang="ts">
import {computed, ref, watch} from 'vue'
import {
  Button,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Input,
  Label,
} from 'nom-ui'
import InfoTip from './InfoTip.vue'
import Note from './Note.vue'
import {amountOf, bandLabel, fitsBand, formatRate, rate, rateUnits, shortKey} from '@/core/board'
import {legLabel, unitFor} from '@/core/chains'
import type {Listing} from '@/types'

// Taking an offer.
//
// What actually happens when this is confirmed is worth being plain about,
// because it is not what a "take" button usually does: a fresh session code is
// minted, sealed to the author's board key, published, and this browser joins
// the room. Nothing is agreed and nothing is created — it opens a conversation.
//
// The code is sealed rather than posted because a session code IS the room:
// anyone holding one can read that conversation and write into it. Publishing it
// on a public board would hand the room to every reader, which is why the board
// advertises a key and takers send codes to it.

const props = defineProps<{listing: Listing | null; busy?: boolean}>()
defineEmits<{
  confirm: [{amount: string; note: string}]
  cancel: []
}>()

const open = defineModel<boolean>('open', {required: true})

const amount = ref('')
const note = ref('')

const post = computed(() => props.listing?.post ?? null)
/** A band is only ever on the leg the AUTHOR funds — which is the leg the taker
 *  RECEIVES, so the size named here is in that leg's unit. */
const banded = computed(() => Boolean(post.value?.give.min || post.value?.give.max))
const bandUnit = computed(() =>
  post.value ? unitFor(post.value.give.chain, post.value.give.token) : '',
)

// A banded post is the only case where a taker names a size. For a fixed-amount
// post the field would be a box with exactly one right answer in it.
watch(
  () => props.listing,
  (l) => {
    amount.value = l ? l.post.give.amount : ''
    note.value = ''
  },
)

const wanted = computed(() => amountOf(amount.value))

/** Caught here rather than in conversation. A take for a size the author already
 *  said they will not do costs both sides a round trip to discover. */
const problem = computed(() => {
  if (!post.value) return ''
  if (!(wanted.value > 0)) return 'Give an amount.'
  if (!fitsBand(post.value, wanted.value)) {
    return banded.value
      ? `This offer trades ${bandLabel(post.value)} and that is outside it.`
      : `This offer is for ${legLabel(post.value.give)} exactly.`
  }
  return ''
})

/** What the taker pays and receives, spelled out. The post is written from the
 *  author's side, so this is the one place the inversion is stated in words —
 *  reading it wrong is the mistake worth spending a paragraph to prevent. */
const sides = computed(() => {
  const p = post.value
  if (!p) return null
  // Scaled where the taker named a size other than the headline one, so the
  // pair of numbers on screen is the pair being asked for rather than the pair
  // that was advertised.
  const headline = amountOf(p.give.amount)
  const factor = wanted.value > 0 && headline > 0 ? wanted.value / headline : 1
  const scale = (v: string) => {
    const n = amountOf(v)
    return Number.isFinite(n) ? String(Number((n * factor).toFixed(9))) : v
  }
  return {
    pay: legLabel({...p.want, amount: scale(p.want.amount)}),
    get: legLabel({...p.give, amount: banded.value ? amount.value : p.give.amount}),
  }
})
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[85vh] overflow-y-auto sm:max-w-lg">
      <DialogHeader>
        <DialogTitle>Take this offer</DialogTitle>
        <DialogDescription v-if="listing">
          Opens a private session with
          <code class="font-mono">{{ shortKey(listing.author) }}</code>
          and tells them you are there. Nothing is agreed yet.
        </DialogDescription>
      </DialogHeader>

      <div v-if="post && sides" class="grid gap-4">
        <div class="grid gap-2 rounded-md border border-border bg-muted/30 p-3 text-sm">
          <div class="flex justify-between gap-4">
            <span class="text-muted-foreground">you pay</span>
            <span class="font-mono">{{ sides.pay }}</span>
          </div>
          <div class="flex justify-between gap-4">
            <span class="text-muted-foreground">you receive</span>
            <span class="font-mono">{{ sides.get }}</span>
          </div>
          <div class="flex justify-between gap-4 border-t border-border/60 pt-2">
            <span class="text-muted-foreground">rate</span>
            <span class="font-mono">{{ formatRate(rate(post)) }} {{ rateUnits(post) }}</span>
          </div>
        </div>

        <div v-if="banded" class="grid gap-2">
          <Label for="bt-amount" class="flex items-center gap-1.5">
            Amount ({{ bandUnit }})
            <InfoTip label="Naming a size">
              This offer trades a range. Say what you want to do and the author sees it with your
              request — you both still agree the real terms in the session that opens.
            </InfoTip>
          </Label>
          <Input
            id="bt-amount"
            v-model="amount"
            inputmode="decimal"
            autocomplete="off"
            class="font-mono"
          />
          <p class="font-mono text-xs text-muted-foreground">{{ bandLabel(post) }}</p>
        </div>

        <div class="grid gap-2">
          <Label for="bt-note">Message (optional)</Label>
          <Input
            id="bt-note"
            v-model="note"
            autocomplete="off"
            maxlength="500"
            placeholder="Anything they should know before answering."
          />
        </div>

        <Note summary="What pressing this actually does.">
          <p>
            Your browser makes a session code, encrypts it so that only this author can read it, and
            publishes it. They see a request from your board key and can accept it or not — several
            people can take the same offer, and only one gets accepted.
          </p>
          <p>
            You join that session immediately, so you are there when they answer. Once they accept,
            their offer arrives over it and goes into the new-swap form through the same check a
            pasted offer gets. Nothing on the board becomes a swap term without passing that.
          </p>
          <p v-if="!listing?.verified?.length">
            No proof of the addresses on this post checked out here — an older post, or a proof this
            reader could not verify. Yours travel proven either way, and the swap is what protects
            you: both legs settle or both refund.
          </p>
        </Note>

        <p v-if="problem" class="text-sm text-warning">{{ problem }}</p>
      </div>

      <DialogFooter>
        <Button variant="ghost" @click="$emit('cancel')">Cancel</Button>
        <Button
          :disabled="busy || Boolean(problem)"
          @click="$emit('confirm', {amount: amount.trim(), note: note.trim()})"
        >
          Send take and open session
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>

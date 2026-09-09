<script setup lang="ts">
import {computed, reactive, watch} from 'vue'
import {
  Button,
  Input,
  Label,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from 'nom-ui'
import InfoTip from './InfoTip.vue'
import Note from './Note.vue'
import {formatRate} from '@/core/board'
import {chainLabel, chainList, chainsForPost, type ChainId} from '@/core/chains'
import {btc} from '@/core/format'
import {useBoardIdentity} from '@/core/composables/useBoardIdentity'
import {useChainProofs} from '@/core/composables/useChainProofs'
import {useSettings} from '@/core/composables/useSettings'
import type {BoardPost, MyPost} from '@/types'

// Posting an offer.
//
// The form is written from the poster's side and says so: "I pay Bitcoin" rather
// than a `side` field, because `send`/`receive` is the vocabulary a swap uses
// internally and is the vocabulary people get backwards.
//
// It advertises and commits to nothing. Nothing here creates a swap, funds
// anything, or fixes a term: the swap is created later, from an offer that
// travels over a session and is checked field by field against the create — see
// wasm/offerterms.go. That is why this form is allowed to be as loose as it is,
// and why editing a post after somebody has read it is safe.
//
// The one thing it is NOT loose about is the addresses. An offer may only name
// an address this browser has proven, on every chain the offer settles on, and
// those fields are shown rather than typed for that reason — see chains.ts for
// how "every chain the offer settles on" is worked out, and handleBoardPublish
// for the refusal that makes it real rather than a habit of this form.

const props = defineProps<{editing?: MyPost | null; busy?: boolean}>()
const emit = defineEmits<{submit: [Record<string, unknown>]; cancel: []}>()

const {btcBound, znnBound, limits} = useBoardIdentity()
const {stored} = useSettings()
const proofs = useChainProofs()

/** An hours figure with no trailing noise: `24`, `0.25`, `1.5`. */
function hoursText(seconds: number): string {
  return String(Number((seconds / 3600).toFixed(4)))
}

/** The same span as somebody would say it, for the hint beside the field. A
 *  development build defaults to a quarter of an hour, and "0.25 hours" is a
 *  number rather than a duration. */
function humanSpan(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return '—'
  if (seconds < 3600) return `${Math.round(seconds / 60)} min`
  const hours = seconds / 3600
  if (hours < 48) return `${Number(hours.toFixed(2))} h`
  return `${Number((hours / 24).toFixed(2))} days`
}

const form = reactive({
  side: 'send' as 'send' | 'receive',
  role: 'initiator' as 'initiator' | 'participant',
  amount: '',
  minAmount: '',
  maxAmount: '',
  zenonAmt: '',
  zenonToken: '',
  lockHours: '',
  btcAddr: '',
  znnAddr: '',
  note: '',
  completed: '',
  // Seeded from the module below, once it has said what this instance's default
  // is -- fifteen minutes on a development build, a day on production. A
  // literal here would be a second opinion about a number the module decides.
  ttlHours: '',
})

/** Bitcoin is typed in BTC and stored in satoshis. Both are shown wherever an
 *  amount is entered, for the reason format.ts gives: an amount without a unit
 *  is how a swap gets funded off by a factor of a hundred million. */
function toSats(v: string): number {
  const n = Number(v)
  if (!Number.isFinite(n) || n <= 0) return 0
  return Math.round(n * 1e8)
}

const sats = computed(() => toSats(form.amount))
const minSats = computed(() => toSats(form.minAmount))
const maxSats = computed(() => toSats(form.maxAmount))

/**
 * Which chains this offer would settle on, and which of those are still
 * unproven.
 *
 * Derived from the draft rather than fixed at "both", so the day this form
 * offers a Zenon-token pair the requirement follows the choice instead of
 * demanding a Bitcoin proof for a leg the offer does not have. See chains.ts.
 */
const need = computed<ChainId[]>(() =>
  chainsForPost({
    amountSats: sats.value,
    zenonAmt: form.zenonAmt,
    btcAddr: form.btcAddr,
    znnAddr: form.znnAddr,
  } as BoardPost),
)

const missing = computed(() => proofs.missingFor(need.value))

/** An edit whose post named an address this browser can no longer prove. Saving
 *  republishes it with the proven one, so the form says which and to what — a
 *  silent swap of the address somebody gets paid at is not a detail. */
const rewriting = computed(() => {
  const p = props.editing?.post
  if (!p) return []
  const was: {chain: ChainId; was: string}[] = []
  if (p.btcAddr && btcBound.value && p.btcAddr !== btcBound.value) {
    was.push({chain: 'btc', was: p.btcAddr})
  }
  if (p.znnAddr && znnBound.value && p.znnAddr !== znnBound.value) {
    was.push({chain: 'znn', was: p.znnAddr})
  }
  return was
})

const rate = computed(() => {
  const znn = Number(form.zenonAmt)
  const b = sats.value / 1e8
  if (!Number.isFinite(znn) || znn <= 0 || b <= 0) return NaN
  return znn / b
})

/** What the TTL field resolves to, in the seconds the publish call takes.
 *  A blank field means the module's own default rather than zero. */
const ttlSeconds = computed(() => {
  const typed = Number(form.ttlHours)
  if (!form.ttlHours.trim() || !Number.isFinite(typed) || typed <= 0) {
    return limits.value.defaultTtl
  }
  return Math.round(typed * 3600)
})

/** Whether the form could be published. Go validates properly and is the
 *  authority; this exists so the button is not offered for something that will
 *  certainly be refused. */
const problem = computed(() => {
  if (sats.value <= 0) return 'Give a Bitcoin amount.'
  if (!form.zenonAmt.trim()) return 'Give a Zenon amount.'
  if (!Number.isFinite(Number(form.zenonAmt)) || Number(form.zenonAmt) <= 0) {
    return 'That Zenon amount is not a number above zero.'
  }
  if (minSats.value && maxSats.value && minSats.value > maxSats.value) {
    return 'The smallest size is larger than the largest.'
  }
  if (minSats.value && sats.value < minSats.value) return 'The amount is below your own minimum.'
  if (maxSats.value && sats.value > maxSats.value) return 'The amount is above your own maximum.'
  // A proven address on every chain this offer settles on. The addresses are
  // filled in from the proofs rather than typed, so this is not a typo check —
  // it catches a proof dropped or gone stale while the form was open, which is
  // the one way the fields below can be empty. Go refuses such a post anyway;
  // saying so here is the difference between a sentence and a failed publish.
  if (missing.value.length) {
    return (
      `Prove your ${chainList(missing.value)} address before posting this offer — it is where ` +
      `your half of the trade gets paid.`
    )
  }
  if (ttlSeconds.value < limits.value.minTtl || ttlSeconds.value > limits.value.maxTtl) {
    return `A post can run between ${humanSpan(limits.value.minTtl)} and ${humanSpan(
      limits.value.maxTtl,
    )}.`
  }
  return ''
})

// Seed the deadline from whatever this instance calls default, once the module
// has said. Only into an untouched field: somebody who has typed a span keeps
// it, and an edit loads its post's own remaining life below.
watch(
  () => limits.value.defaultTtl,
  (seconds) => {
    if (!form.ttlHours.trim() && seconds > 0) form.ttlHours = hoursText(seconds)
  },
  {immediate: true},
)

// The proven addresses, always — not a default somebody can type over.
//
// There is nothing to choose between: Go refuses to sign a post carrying an
// address this browser holds no proof for, so a field that accepted a second
// address would be a box with exactly one right answer in it and a failed
// publish for every other. Editing an old post that named a different address
// rewrites it to the proven one, and the form says so where that happens.
//
// A watcher rather than a computed because the form is one reactive object the
// submit reads whole; the fields are not editable, so nothing here can be
// clobbering something typed.
watch(
  [btcBound, znnBound],
  () => {
    form.btcAddr = btcBound.value
    form.znnAddr = znnBound.value
  },
  {immediate: true},
)

// Editing loads the post as it stands. The id travels back on submit, which is
// what makes it an edit rather than a second offer — a relay keys an addressable
// event by its slot.
watch(
  () => props.editing,
  (p) => {
    if (!p) return
    form.side = p.post.side
    form.role = p.post.role
    form.amount = String(p.post.amountSats / 1e8)
    form.minAmount = p.post.minSats ? String(p.post.minSats / 1e8) : ''
    form.maxAmount = p.post.maxSats ? String(p.post.maxSats / 1e8) : ''
    form.zenonAmt = p.post.zenonAmt
    form.zenonToken = p.post.zenonToken ?? ''
    form.lockHours = p.post.lockHours ? String(p.post.lockHours) : ''
    // Addresses deliberately not loaded from the post. They come from the
    // proofs held now, which is what saving would publish anyway — a post
    // edited a week after switching wallets carries an address this browser can
    // no longer prove, and loading it would only put a value on screen that the
    // save is about to replace. `rewriting` below names it where it differs.
    form.note = p.post.note ?? ''
    form.completed = p.post.completed ? String(p.post.completed) : ''
  },
  {immediate: true},
)

function submit() {
  if (problem.value) return
  emit('submit', {
    ...(props.editing ? {id: props.editing.post.id} : {}),
    side: form.side,
    role: form.role,
    amountSats: sats.value,
    minSats: minSats.value,
    maxSats: maxSats.value,
    zenonAmt: form.zenonAmt.trim(),
    zenonToken: form.zenonToken.trim(),
    lockHours: Number(form.lockHours) || 0,
    btcAddr: form.btcAddr.trim(),
    znnAddr: form.znnAddr.trim(),
    note: form.note.trim(),
    completed: Number(form.completed) || 0,
    ttlSeconds: ttlSeconds.value,
  })
}
</script>

<template>
  <form class="grid gap-4" @submit.prevent="submit">
    <div class="grid gap-4 sm:grid-cols-2">
      <div class="grid gap-2">
        <Label class="flex items-center gap-1.5">
          Which way
          <InfoTip label="Which side you are taking">
            <p>
              Stated from your side. Whoever takes this offer takes the other one — you do not pick
              their half, and neither does this page.
            </p>
          </InfoTip>
        </Label>
        <Select
          :model-value="form.side"
          @update:model-value="(v) => (form.side = String(v) as 'send' | 'receive')"
        >
          <SelectTrigger><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="send">I pay Bitcoin, I receive ZNN</SelectItem>
            <SelectItem value="receive">I pay ZNN, I receive Bitcoin</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <div class="grid gap-2">
        <Label class="flex items-center gap-1.5">
          Your role
          <InfoTip label="Initiator or participant">
            <p>
              The initiator invents the secret and takes the longer timelock; the participant must
              be able to refund strictly first. It is a term of the trade rather than a preference —
              a swap has exactly one of each.
            </p>
            <p>
              If you do not know, take initiator. It costs you a longer wait on a refund and hands
              you the secret, which is the safer half to hold.
            </p>
          </InfoTip>
        </Label>
        <Select
          :model-value="form.role"
          @update:model-value="(v) => (form.role = String(v) as 'initiator' | 'participant')"
        >
          <SelectTrigger><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="initiator">initiator</SelectItem>
            <SelectItem value="participant">participant</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <div class="grid gap-2">
        <Label for="bp-amount">Bitcoin amount (BTC)</Label>
        <Input
          id="bp-amount"
          v-model="form.amount"
          inputmode="decimal"
          autocomplete="off"
          placeholder="0.01"
          class="font-mono"
        />
        <p class="font-mono text-xs text-muted-foreground">
          {{ sats.toLocaleString('en-US') }} sat
        </p>
      </div>

      <div class="grid gap-2">
        <Label for="bp-znn">Zenon amount</Label>
        <div class="flex gap-2">
          <Input
            id="bp-znn"
            v-model="form.zenonAmt"
            inputmode="decimal"
            autocomplete="off"
            placeholder="1200"
            class="font-mono"
          />
          <Input
            v-model="form.zenonToken"
            spellcheck="false"
            autocapitalize="none"
            autocomplete="off"
            placeholder="ZNN"
            class="max-w-40 font-mono"
            aria-label="Token standard — blank is ZNN"
          />
        </div>
        <p class="font-mono text-xs text-muted-foreground">{{ formatRate(rate) }} ZNN/BTC</p>
      </div>

      <div class="grid gap-2 sm:col-span-2">
        <Label class="flex items-center gap-1.5">
          Size band (optional)
          <InfoTip label="Trading a range instead of one amount">
            <p>
              Leave both blank and this offer is for exactly the amount above. Fill them in and a
              taker can name any size in the band — the two of you settle on one before a swap
              exists.
            </p>
            <p>
              The band is advertising, not a commitment. Nothing here can hold you to it, and the
              terms that bind are the ones checked when the swap is created.
            </p>
          </InfoTip>
        </Label>
        <div class="flex flex-wrap gap-2">
          <Input
            v-model="form.minAmount"
            inputmode="decimal"
            autocomplete="off"
            placeholder="smallest (BTC)"
            class="max-w-48 font-mono"
            aria-label="Smallest size in BTC"
          />
          <Input
            v-model="form.maxAmount"
            inputmode="decimal"
            autocomplete="off"
            placeholder="largest (BTC)"
            class="max-w-48 font-mono"
            aria-label="Largest size in BTC"
          />
          <span
            v-if="minSats || maxSats"
            class="self-center font-mono text-xs text-muted-foreground"
          >
            {{ minSats ? btc(minSats) : 'any' }} – {{ maxSats ? btc(maxSats) : 'any' }}
          </span>
        </div>
      </div>

      <div class="grid gap-2">
        <Label for="bp-lock" class="flex items-center gap-1.5">
          Your lock (hours)
          <InfoTip label="What this number does and does not do">
            Advisory. It says what you intend your Bitcoin contract's timelock to be, so a taker can
            see it before agreeing. What actually protects the swap is the ordering check run
            against the real contract when it exists, not a number in an advertisement.
          </InfoTip>
        </Label>
        <Input
          id="bp-lock"
          v-model="form.lockHours"
          inputmode="numeric"
          autocomplete="off"
          placeholder="48"
          class="font-mono"
        />
      </div>

      <div class="grid gap-2">
        <Label for="bp-ttl" class="flex items-center gap-1.5">
          Expires after (hours)
          <InfoTip label="What the deadline is for">
            <p>
              A day by default on the production instance, because that is roughly the life of the
              thing being advertised: prices move, and a board of week-old offers is a board where
              every third one is dead.
            </p>
            <p>
              The development instance defaults to fifteen minutes instead. A test post is made to
              watch one thing happen and then abandoned, and only the browser that made it can
              withdraw it — so a day-long default would leave every experiment standing on public
              relays until tomorrow.
            </p>
            <p>
              That is also what covers you closing this tab. Nothing else can withdraw your post, so
              the deadline is what retires an offer you walked away from.
            </p>
          </InfoTip>
        </Label>
        <Input
          id="bp-ttl"
          v-model="form.ttlHours"
          inputmode="decimal"
          autocomplete="off"
          class="font-mono"
        />
        <p class="font-mono text-xs text-muted-foreground">
          {{ humanSpan(ttlSeconds) }}
          <span v-if="!form.ttlHours.trim()" class="opacity-70">· this instance's default</span>
        </p>
      </div>

      <!-- Where each half gets paid. Shown rather than typed: these are the
           addresses this browser has proven, and they are the only ones Go will
           sign a post for. -->
      <div class="grid gap-2 sm:col-span-2">
        <Label class="flex items-center gap-1.5">
          Where you get paid
          <InfoTip label="Why these cannot be edited">
            <p>
              A post carries the addresses your half of the trade settles to, and this build only
              publishes ones you have proven — a signature from the wallet holding the address,
              tying it to your board key. So there is nothing to type: the proof decides.
            </p>
            <p>
              To be paid somewhere else, prove that address instead. The buttons are under your
              board key at the top of the board.
            </p>
          </InfoTip>
        </Label>
        <div class="grid gap-2 rounded-md border border-border bg-muted/30 p-3">
          <div v-for="c in need" :key="c" class="grid gap-0.5">
            <span class="text-ledger text-muted-foreground">{{ chainLabel(c) }}</span>
            <code v-if="proofs.addressFor(c)" class="font-mono text-xs break-all text-success">
              {{ proofs.addressFor(c) }}
            </code>
            <span v-else class="text-xs text-warning">
              not proven — this offer cannot be posted until it is
            </span>
          </div>
        </div>
        <p v-if="rewriting.length" class="text-xs text-warning">
          This post named {{ rewriting.map((r) => r.was).join(' and ') }}. Saving republishes it
          with the {{ chainList(rewriting.map((r) => r.chain)) }}
          {{ rewriting.length === 1 ? 'address' : 'addresses' }} above, which
          {{ rewriting.length === 1 ? 'is' : 'are' }} what you can prove now.
        </p>
      </div>

      <div class="grid gap-2 sm:col-span-2">
        <Label for="bp-note">Note (optional)</Label>
        <Input
          id="bp-note"
          v-model="form.note"
          autocomplete="off"
          maxlength="500"
          placeholder="Timezone, how fast you answer, anything a counterparty should know."
        />
      </div>

      <div class="grid gap-2">
        <Label for="bp-completed" class="flex items-center gap-1.5">
          Swaps completed (optional)
          <InfoTip variant="warn" label="Nothing checks this">
            You type it and nothing verifies it — not this page, not a relay, not a chain. It is
            shown on your post labelled as your own claim, because a number presented as a score
            would be read as one.
          </InfoTip>
        </Label>
        <Input
          id="bp-completed"
          v-model="form.completed"
          inputmode="numeric"
          autocomplete="off"
          class="font-mono"
        />
      </div>
    </div>

    <Note summary="This posts an advertisement. It does not create a swap or move anything.">
      <p>
        Nothing here is a commitment. No contract is built, no address is funded, and no term is
        fixed — the swap is created later, from terms that travel over a private session and are
        checked field by field against the form you fill in then.
      </p>
      <p>
        Your post is public and stays on relays until it expires or you withdraw it. It carries the
        amounts, the note and your proven addresses, signed by your board key so that only you can
        change it — and each address travels with the proof that it is yours, which is what puts the
        verified badge on the row a stranger reads.
      </p>
      <p>
        It is posted for
        <span class="font-mono">{{ stored.network }}</span
        >, the network this page is set to. Somebody on a different one will not see it.
      </p>
    </Note>

    <p v-if="problem" class="text-sm text-warning">{{ problem }}</p>

    <div class="flex flex-wrap gap-2">
      <Button type="submit" :disabled="busy || Boolean(problem)">
        {{ editing ? 'Save changes' : 'Post to the board' }}
      </Button>
      <Button type="button" variant="ghost" @click="$emit('cancel')">Cancel</Button>
    </div>
  </form>
</template>

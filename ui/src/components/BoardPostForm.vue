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
import {
  KNOWN_TOKENS,
  PAIRS,
  chainLabel,
  chainList,
  orderChains,
  pairFor,
  unitFor,
  type ChainId,
} from '@/core/chains'
import {useBoardIdentity} from '@/core/composables/useBoardIdentity'
import {useChainProofs} from '@/core/composables/useChainProofs'
import {useSettings} from '@/core/composables/useSettings'
import type {MyPost} from '@/types'

// Posting an offer.
//
// The form is written from the poster's side and says so: "I fund X, I receive
// Y" rather than a `side` field, because the swap's own vocabulary is the one
// people get backwards.
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

const {limits} = useBoardIdentity()
const {stored} = useSettings()
const proofs = useChainProofs()

/** An hours figure with no trailing noise: `24`, `0.25`, `1.5`. */
function hoursText(seconds: number): string {
  return String(Number((seconds / 3600).toFixed(4)))
}

/** The same span as somebody would say it, for the hint beside the field. */
function humanSpan(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return '—'
  if (seconds < 3600) return `${Math.round(seconds / 60)} min`
  const hours = seconds / 3600
  if (hours < 48) return `${Number(hours.toFixed(2))} h`
  return `${Number((hours / 24).toFixed(2))} days`
}

const form = reactive({
  pair: PAIRS[0].id,
  /** Which chain the AUTHOR funds. The other half of the pair is what they
   *  receive, so one choice settles both. */
  give: PAIRS[0].a as ChainId,
  giveToken: '',
  wantToken: KNOWN_TOKENS[1].zts,
  role: 'initiator' as 'initiator' | 'participant',
  giveAmount: '',
  minAmount: '',
  maxAmount: '',
  wantAmount: '',
  lockHours: '',
  note: '',
  completed: '',
  // Seeded from the module below, once it has said what this instance's default
  // is — fifteen minutes on a development build, a day on production. A literal
  // here would be a second opinion about a number the module decides.
  ttlHours: '',
})

const pair = computed(() => pairFor(form.pair) ?? PAIRS[0])
/** The chain the author receives: the half of the pair they are not funding. */
const want = computed<ChainId>(() =>
  pair.value.a === pair.value.b
    ? pair.value.a
    : form.give === pair.value.a
      ? pair.value.b
      : pair.value.a,
)

/** Both directions of the chosen pair, as sentences. A pair whose two halves
 *  are the same chain has one direction and no choice to offer. */
const directions = computed(() =>
  pair.value.a === pair.value.b
    ? []
    : [
        {value: pair.value.a, label: `I fund ${chainLabel(pair.value.a)}, I receive ${chainLabel(pair.value.b)}`},
        {value: pair.value.b, label: `I fund ${chainLabel(pair.value.b)}, I receive ${chainLabel(pair.value.a)}`},
      ],
)

const giveUnit = computed(() => unitFor(form.give, form.giveToken))
const wantUnit = computed(() => unitFor(want.value, form.wantToken))

/** Which chains this offer would settle on, and which of those are still
 *  unproven. Derived from the draft rather than fixed, so a ZTS↔ZTS offer asks
 *  for one proof and a SOL↔BTC one asks for no Zenon wallet at all. */
const need = computed<ChainId[]>(() => orderChains([form.give, want.value]))
const missing = computed(() => proofs.missingFor(need.value))

// Keep the direction inside the chosen pair. Switching from BTC⇄ZTS to SOL⇄BTC
// while "I fund Zenon" is selected would otherwise leave a post describing a
// trade the pair does not contain.
watch(
  () => form.pair,
  () => {
    const p = pair.value
    if (form.give !== p.a && form.give !== p.b) form.give = p.a
    // A ZTS against a ZTS needs two different tokens, and Go refuses one
    // against itself. Seeding the second with the other well-known token means
    // the commonest case is right without anybody typing a standard.
    if (p.a === 'znn' && p.b === 'znn' && form.giveToken === form.wantToken) {
      form.wantToken = form.giveToken === KNOWN_TOKENS[0].zts
        ? KNOWN_TOKENS[1].zts
        : KNOWN_TOKENS[0].zts
    }
  },
)

const rate = computed(() => {
  const g = Number(form.giveAmount)
  const w = Number(form.wantAmount)
  if (!Number.isFinite(g) || !Number.isFinite(w) || g <= 0 || w <= 0) return NaN
  return w / g
})

/** What the TTL field resolves to, in the seconds the publish call takes. A
 *  blank field means the module's own default rather than zero. */
const ttlSeconds = computed(() => {
  const typed = Number(form.ttlHours)
  if (!form.ttlHours.trim() || !Number.isFinite(typed) || typed <= 0) {
    return limits.value.defaultTtl
  }
  return Math.round(typed * 3600)
})

const num = (v: string) => (v.trim() ? Number(v) : NaN)

/** Whether the form could be published. Go validates properly and is the
 *  authority; this exists so the button is not offered for something that will
 *  certainly be refused. */
const problem = computed(() => {
  if (!(num(form.giveAmount) > 0)) return `Give an amount in ${giveUnit.value}.`
  if (!(num(form.wantAmount) > 0)) return `Give an amount in ${wantUnit.value}.`
  if (form.give === 'btc' && !Number.isInteger(num(form.giveAmount))) {
    return 'A Bitcoin amount is a whole number of satoshi.'
  }
  if (want.value === 'btc' && !Number.isInteger(num(form.wantAmount))) {
    return 'A Bitcoin amount is a whole number of satoshi.'
  }
  const min = num(form.minAmount)
  const max = num(form.maxAmount)
  if (min > 0 && max > 0 && min > max) return 'The smallest size is larger than the largest.'
  if (min > 0 && num(form.giveAmount) < min) return 'The amount is below your own minimum.'
  if (max > 0 && num(form.giveAmount) > max) return 'The amount is above your own maximum.'
  if (form.give === 'znn' && want.value === 'znn') {
    const a = form.giveToken.trim() || KNOWN_TOKENS[0].zts
    const b = form.wantToken.trim() || KNOWN_TOKENS[0].zts
    if (a === b) return 'Both legs are the same token, so nothing is being traded.'
  }
  // A proven address on every chain this offer settles on. The addresses are
  // filled in from the proofs rather than typed, so this is not a typo check —
  // it catches a proof dropped or gone stale while the form was open, which is
  // the one way they can be empty. Go refuses such a post anyway; saying so here
  // is the difference between a sentence and a failed publish.
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
// has said. Only into an untouched field.
watch(
  () => limits.value.defaultTtl,
  (seconds) => {
    if (!form.ttlHours.trim() && seconds > 0) form.ttlHours = hoursText(seconds)
  },
  {immediate: true},
)

// Editing loads the post as it stands. The id travels back on submit, which is
// what makes it an edit rather than a second offer — a relay keys an addressable
// event by its slot.
//
// The addresses are deliberately NOT loaded: they come from the proofs held now,
// which is what saving would publish anyway.
watch(
  () => props.editing,
  (p) => {
    if (!p) return
    form.pair = `${p.post.give.chain}-${p.post.want.chain}`
    if (!pairFor(form.pair)) form.pair = `${p.post.want.chain}-${p.post.give.chain}`
    form.give = p.post.give.chain
    form.giveToken = p.post.give.token ?? ''
    form.wantToken = p.post.want.token ?? ''
    form.role = p.post.role
    form.giveAmount = p.post.give.amount
    form.minAmount = p.post.give.min ?? ''
    form.maxAmount = p.post.give.max ?? ''
    form.wantAmount = p.post.want.amount
    form.lockHours = p.post.lockHours ? String(p.post.lockHours) : ''
    form.note = p.post.note ?? ''
    form.completed = p.post.completed ? String(p.post.completed) : ''
  },
  {immediate: true},
)

function submit() {
  if (problem.value) return
  emit('submit', {
    ...(props.editing ? {id: props.editing.post.id} : {}),
    role: form.role,
    give: {
      chain: form.give,
      ...(form.give === 'znn' ? {token: form.giveToken.trim()} : {}),
      amount: form.giveAmount.trim(),
      min: form.minAmount.trim(),
      max: form.maxAmount.trim(),
    },
    want: {
      chain: want.value,
      ...(want.value === 'znn' ? {token: form.wantToken.trim()} : {}),
      amount: form.wantAmount.trim(),
    },
    lockHours: Number(form.lockHours) || 0,
    addrs: proofs.addrsFor(need.value),
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
          Which trade
          <InfoTip label="What a pair is">
            <p>
              Two chains, and what a swap locks on each. The list is what this build knows how to
              settle — a pair is not a preference, it is two contracts and the rules that keep them
              ordered.
            </p>
            <p>
              ZTS against ZTS is one chain twice, which is allowed only because the tokens differ.
              Both legs are ordinary Zenon HTLCs and one Zenon proof covers them.
            </p>
          </InfoTip>
        </Label>
        <Select :model-value="form.pair" @update:model-value="(v) => (form.pair = String(v))">
          <SelectTrigger><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem v-for="p in PAIRS" :key="p.id" :value="p.id">{{ p.label }}</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <div v-if="directions.length" class="grid gap-2">
        <Label class="flex items-center gap-1.5">
          Which way
          <InfoTip label="Which side you are taking">
            Stated from your side. Whoever takes this offer takes the other one — you do not pick
            their half, and neither does this page.
          </InfoTip>
        </Label>
        <Select
          :model-value="form.give"
          @update:model-value="(v) => (form.give = String(v) as ChainId)"
        >
          <SelectTrigger><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem v-for="d in directions" :key="d.value" :value="d.value">
              {{ d.label }}
            </SelectItem>
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
        <Label for="bp-give">You pay ({{ giveUnit }})</Label>
        <div class="flex gap-2">
          <Input
            id="bp-give"
            v-model="form.giveAmount"
            inputmode="decimal"
            autocomplete="off"
            :placeholder="form.give === 'btc' ? '400000' : '10'"
            class="font-mono"
          />
          <Input
            v-if="form.give === 'znn'"
            v-model="form.giveToken"
            spellcheck="false"
            autocapitalize="none"
            autocomplete="off"
            placeholder="ZNN"
            class="max-w-40 font-mono"
            aria-label="Token standard — blank is ZNN"
          />
        </div>
      </div>

      <div class="grid gap-2">
        <Label for="bp-want">You receive ({{ wantUnit }})</Label>
        <div class="flex gap-2">
          <Input
            id="bp-want"
            v-model="form.wantAmount"
            inputmode="decimal"
            autocomplete="off"
            :placeholder="want === 'btc' ? '400000' : '10'"
            class="font-mono"
          />
          <Input
            v-if="want === 'znn'"
            v-model="form.wantToken"
            spellcheck="false"
            autocapitalize="none"
            autocomplete="off"
            placeholder="ZNN"
            class="max-w-40 font-mono"
            aria-label="Token standard — blank is ZNN"
          />
        </div>
        <p class="font-mono text-xs text-muted-foreground">
          {{ formatRate(rate) }} {{ wantUnit }} per {{ giveUnit }}
        </p>
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
              It goes on the leg you fund. A band on both halves would be a band on the rate, which
              is a different offer and one nothing downstream knows how to settle.
            </p>
          </InfoTip>
        </Label>
        <div class="flex flex-wrap gap-2">
          <Input
            v-model="form.minAmount"
            inputmode="decimal"
            autocomplete="off"
            :placeholder="`smallest (${giveUnit})`"
            class="max-w-48 font-mono"
            aria-label="Smallest size"
          />
          <Input
            v-model="form.maxAmount"
            inputmode="decimal"
            autocomplete="off"
            :placeholder="`largest (${giveUnit})`"
            class="max-w-48 font-mono"
            aria-label="Largest size"
          />
        </div>
      </div>

      <div class="grid gap-2">
        <Label for="bp-lock" class="flex items-center gap-1.5">
          Your lock (hours)
          <InfoTip label="What this number does and does not do">
            Advisory. It says what you intend your own leg's deadline to be, so a taker can see it
            before agreeing. What actually protects the swap is the ordering check run against the
            real contract when it exists, not a number in an advertisement.
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

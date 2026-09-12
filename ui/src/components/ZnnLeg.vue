<script setup lang="ts">
import {computed, ref, watch} from 'vue'
import {SearchIcon} from '@lucide/vue'
import {Address, Button, Input, Label, Switch} from 'nom-ui'
import DataList from './DataList.vue'
import DataRow from './DataRow.vue'
import Callout from './Callout.vue'
import ZenonWallet from './ZenonWallet.vue'
import {api} from '@/core/api'
import {until} from '@/core/format'
import {cliNodeURL, znnCommands} from '@/core/zenon-commands'
import {useSettings} from '@/core/composables/useSettings'
import {useCliCommands} from '@/core/composables/useCliCommands'
import type {LegView, Swap, WalletAction} from '@/types'

/**
 * One Zenon leg of a swap, whichever side of it this user is on.
 *
 * A swap may have two of these — a ZTS against a different ZTS is the one pair
 * where both halves live on the same chain — so everything here is addressed by
 * the leg it was given rather than by "the Zenon leg". The direction decides
 * which of the three calls is this side's move: an out leg is created and
 * reclaimed, an in leg is verified and unlocked.
 */
const props = defineProps<{swap: Swap; leg: LegView; blocked: boolean}>()
const emit = defineEmits<{
  changed: []
  moved: [what: string]
  error: [message: string]
}>()

const {body: settings, hasZenon} = useSettings()
const cli = useCliCommands()

const busy = ref(false)
const htlcIn = ref('')
const result = ref<{ok: boolean; text: string} | null>(null)

const znn = computed(() => props.leg.znn)
const ours = computed(() => props.leg.dir === 'out')

// A verification result describes one entry at one moment; leaving it on the
// card would keep showing "verified" beside a leg since re-checked and refused.
watch(
  [() => props.swap.id, () => znn.value?.htlcId, () => znn.value?.verified],
  () => (result.value = null),
)

// An id the swap already holds belongs in the field that shows ids. The case
// this exists for is the session: an id arriving from the counterparty is
// verified and recorded without this panel being touched, so the box stayed
// empty beside a leg that had already been checked. Anything typed wins.
watch(
  () => znn.value?.htlcId,
  (id) => {
    if (id && !htlcIn.value.trim()) htlcIn.value = id
  },
  {immediate: true},
)

const htlcToVerify = computed(() => htlcIn.value.trim() || znn.value?.htlcId || '')

/** Not checked YET is not checked and found wanting. The distinction decides
 *  whether the timer keeps asking, so it decides what the badge says. */
const pending = computed(() => Boolean(znn.value?.verifyPending))
const bad = computed(() => Boolean(znn.value?.verifyError) && !pending.value)

/**
 * Which call is this side's outstanding move, if any.
 *
 * It decides which button to offer, not whether the call is allowed: Go checks
 * every precondition again and refuses with a sentence naming the one that
 * failed. Duplicating those rules here would be a second copy of the leg
 * ordering and the preimage ownership, which is precisely the pair that must not
 * drift.
 */
const action = computed<WalletAction | null>(() => {
  if (ours.value) {
    if (znn.value?.htlcId || props.leg.done) return null
    return 'create'
  }
  if (znn.value?.unlockHash || props.leg.done) return null
  return znn.value?.htlcId && props.swap.secretHex ? 'unlock' : null
})

/** Reclaim is the failure path, so it is offered only once it can actually
 *  succeed: the contract refuses one before expiry, and a button that spends a
 *  fee to be told so is worse than no button. */
const reclaimable = computed(
  () => ours.value && Boolean(znn.value?.htlcId) && Boolean(props.leg.expired),
)

const commands = computed(() =>
  znnCommands(props.swap, props.leg, {nodeURL: cliNodeURL(settings.value.znnUrl)}),
)

async function verify() {
  busy.value = true
  emit('error', '')
  try {
    const r = await api.verifyZenon(props.swap.id, htlcToVerify.value, settings.value, props.leg.dir)
    result.value = r.error
      ? {ok: false, text: `REJECTED: ${r.error}`}
      : {ok: true, text: 'Verified — hashlock, parties, token, amount and expiry all match'}
    emit('changed')
  } catch (e) {
    result.value = {ok: false, text: e instanceof Error ? e.message : String(e)}
  } finally {
    busy.value = false
  }
}

/**
 * Find the entry rather than asking for its id. Everything needed to recognise
 * it is already on this card — the address that creates this leg, and the hash
 * both legs lock to — so the id is a lookup rather than a hand-off. Every
 * candidate is verified against the agreed terms before one is adopted, and the
 * field is still there to paste into.
 */
async function find() {
  busy.value = true
  result.value = null
  emit('error', '')
  try {
    const r = await api.findZenon(props.swap.id, settings.value, props.leg.dir)
    const found = props.leg.dir === 'out' ? r.swap?.out?.znn?.htlcId : r.swap?.in?.znn?.htlcId
    htlcIn.value = found ?? htlcIn.value
    result.value = r.error
      ? {ok: false, text: `REJECTED: ${r.error}`}
      : {ok: true, text: `Found ${found ?? ''} — every agreed term matches`}
    emit('changed')
  } catch (e) {
    result.value = {ok: false, text: e instanceof Error ? e.message : String(e)}
  } finally {
    busy.value = false
  }
}

function walletDone(which: WalletAction) {
  emit('changed')
  emit(
    'moved',
    {
      create: 'created their Zenon HTLC',
      unlock: 'unlocked the Zenon HTLC, which publishes the preimage on Zenon',
      reclaim: 'reclaimed their tokens from the Zenon HTLC',
    }[which],
  )
}
</script>

<template>
  <section class="grid gap-3 rounded-lg border border-border p-3">
    <header class="flex flex-wrap items-baseline justify-between gap-2">
      <h3 class="text-sm font-medium">
        Zenon — {{ ours ? 'the leg you fund' : 'the leg they fund' }}
      </h3>
      <span class="font-mono text-xs text-muted-foreground">{{ leg.label }}</span>
    </header>

    <Callout v-if="!hasZenon" tone="warning">
      No Zenon node is set, so nothing on this leg can be checked. Open Nodes and give this browser
      a JSON-RPC URL it can reach — verifying a counterparty's HTLC is the one check where a
      dishonest answer costs money, so the node has to be one you chose.
    </Callout>

    <DataList>
      <DataRow v-if="leg.selfAddr" label="Your address">
        <Address :address="leg.selfAddr" />
      </DataRow>
      <DataRow v-if="leg.peerAddr" label="Their address">
        <Address :address="leg.peerAddr" />
      </DataRow>
      <DataRow v-if="znn?.htlcId" label="HTLC">
        <Address :address="znn.htlcId" />
      </DataRow>
      <DataRow v-if="leg.expiryAt" label="Deadline">
        <span class="font-mono text-xs">{{ leg.expiryAt }}</span>
        <span class="text-xs text-muted-foreground"> · {{ until(leg.expiryAt) }}</span>
      </DataRow>
      <DataRow v-else-if="ours && znn?.expirationHours" label="Create it for">
        <span class="text-xs">
          {{ znn.expirationSeconds }}s ({{ znn.expirationHours }}h), computed from the other leg's
          deadline
        </span>
      </DataRow>
    </DataList>

    <!-- The verdict. Pending is not a failure and must not read as one: a block
         published a moment ago is simply not readable yet, which is the ordinary
         case after a create. -->
    <Callout v-if="pending" tone="info">
      Not checked against the chain yet — a new account block takes a momentum or two to become
      readable. This re-checks by itself every minute.
    </Callout>
    <Callout v-else-if="bad" tone="destructive">{{ znn?.verifyError }}</Callout>
    <Callout v-else-if="znn?.verified" tone="success">
      Verified against your own node: hashlock, parties, token, amount and expiry all match.
    </Callout>

    <div v-if="znn?.observedToken && znn.observedToken !== leg.token" class="text-xs text-destructive">
      This entry holds {{ znn.observedToken }}, and the agreed token is {{ leg.token }}.
    </div>

    <!-- Verifying. Offered on both directions: an entry this user created is
         still one nobody has looked at, and the id a wallet reports is exactly
         as untrustworthy as one a counterparty pastes. -->
    <div v-if="hasZenon" class="grid gap-2">
      <Label for="htlc-in">HTLC id</Label>
      <div class="flex flex-wrap items-center gap-2">
        <Input id="htlc-in" v-model="htlcIn" class="flex-1 font-mono text-xs" placeholder="0x…" />
        <Button size="sm" :disabled="blocked || busy || !htlcToVerify" @click="verify">
          Verify
        </Button>
        <Button variant="outline" size="sm" :disabled="blocked || busy" @click="find">
          <SearchIcon />
          Find it
        </Button>
      </div>
      <p
        v-if="result"
        class="text-xs"
        :class="result.ok ? 'text-success' : 'text-destructive'"
      >
        {{ result.text }}
      </p>
    </div>

    <!-- The wallet path. Ferry builds the block and Syrius signs it; no Zenon
         key ever reaches this page. -->
    <ZenonWallet
      v-if="action"
      :swap="swap"
      :leg="leg"
      :action="action"
      :disabled="blocked || busy"
      @done="() => walletDone(action!)"
    />
    <ZenonWallet
      v-if="reclaimable"
      :swap="swap"
      :leg="leg"
      action="reclaim"
      :disabled="blocked || busy"
      @done="() => walletDone('reclaim')"
    />

    <Callout v-if="znn?.unlockHash" tone="success">
      You unlocked this entry in {{ znn.unlockHash }}. The tokens arrive as an unreceived block —
      collect them in your wallet.
    </Callout>
    <Callout v-else-if="znn?.unlockSeen" tone="info">
      The counterparty collected this leg, and the preimage that did it is public.
    </Callout>
    <Callout v-if="znn?.reclaimHash" tone="info">
      You reclaimed this entry in {{ znn.reclaimHash }}.
    </Callout>

    <!-- The CLI is one of two ways rather than the only one, so it is off by
         default. It stays because not everybody's wallet is that extension. -->
    <div class="grid gap-2">
      <label class="flex items-center gap-2 text-xs text-muted-foreground">
        <Switch :model-value="cli.show.value" @update:model-value="cli.set" />
        Show znn-cli commands
      </label>
      <pre
        v-if="cli.show.value && commands"
        class="overflow-x-auto rounded-md bg-muted/40 p-2 font-mono text-xs"
        >{{ commands }}</pre
      >
    </div>
  </section>
</template>

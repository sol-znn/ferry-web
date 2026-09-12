<script setup lang="ts">
import {computed, ref} from 'vue'
import {DownloadIcon, TriangleAlertIcon} from '@lucide/vue'
import {Address, Button, CopyButton, Input, Label, Textarea} from 'nom-ui'
import DataList from './DataList.vue'
import DataRow from './DataRow.vue'
import Callout from './Callout.vue'
import TxRef from './TxRef.vue'
import UnlockCost from './UnlockCost.vue'
import WalletConnect from './WalletConnect.vue'
import {api} from '@/core/api'
import {sats, until} from '@/core/format'
import {useSettings} from '@/core/composables/useSettings'
import {useUnisat} from '@/core/composables/useUnisat'
import {useUnlockCost} from '@/core/composables/useUnlockCost'
import type {LegView, Swap} from '@/types'

/**
 * The Bitcoin leg of a swap, whichever side of it this user is on.
 *
 * One component for both directions, because the contract is one object and the
 * two sides are the same object read from opposite ends: the funder holds the
 * refund key and pays into it, the receiver holds the redeem key and audits it.
 * Splitting them would be two copies of the same script facts.
 *
 * Nothing here decides whether an action is allowed. Every button calls into Go,
 * which re-checks its preconditions and refuses with a sentence naming the one
 * that failed — so what this file decides is which button to OFFER.
 */
const props = defineProps<{swap: Swap; leg: LegView; blocked: boolean}>()
const emit = defineEmits<{
  changed: []
  /** Something landed on a chain, so the counterparty is worth nudging. */
  moved: [what: string]
  error: [message: string]
}>()

const {body: settings} = useSettings()
const wallet = useUnisat()

const busy = ref(false)
const contractIn = ref('')
/** Their pubkey hash, when it arrives after the swap was made rather than on
 *  the create form. Without a field here a swap started by hand could not be
 *  built at all: `useSession` was the only writer of this value. */
const pkhIn = ref('')
const destIn = ref('')
const fundingTxid = ref('')
const fundAgain = ref(false)

const btc = computed(() => props.leg.btc)
const ours = computed(() => props.leg.dir === 'out')
const contractAddr = computed(() => btc.value?.contractAddr ?? '')
const funding = computed(() => btc.value?.funding ?? null)
const amount = computed(() => Number(props.leg.base || props.leg.amount || 0))

/** Both branches of the contract pay this address and there is no other. Swaps
 *  created now always have one; a record restored from an older backup may not,
 *  and without it nothing here can build a transaction. */
const dest = computed(() => destIn.value.trim() || props.leg.selfAddr || '')
const needsDest = computed(() => !props.leg.selfAddr && Boolean(funding.value))

/** What emptying this contract costs, priced against the contract that was
 *  really built and the money really in it. Sized in Go by the code that signs
 *  the spend, so the number on the card is the number the button will charge. */
const costInput = computed(() =>
  contractAddr.value ? {id: props.swap.id, amountSats: 0} : null,
)
const {cost, loading: costLoading, error: costError} = useUnlockCost(costInput, settings)

const needsPkh = computed(() => ours.value && !contractAddr.value)
const needsAudit = computed(() => !ours.value && !contractAddr.value)
const canFund = computed(
  () => ours.value && Boolean(contractAddr.value) && !funding.value && !props.leg.done,
)
const fundingSent = computed(() => fundingTxid.value || btc.value?.fundingBroadcast?.txid || '')
const canRedeem = computed(
  () => !ours.value && Boolean(funding.value) && Boolean(props.swap.secretHex) && !props.leg.done,
)
const canRefund = computed(() => Boolean(btc.value?.refundable))

/** Six is where the number stops being interesting; the bar stops there too. */
const depth = computed(() => {
  const n = funding.value?.confirmations ?? 0
  return {n, of: 6, pct: Math.min(100, (n / 6) * 100)}
})

async function run(fn: () => Promise<unknown>, moved?: string) {
  busy.value = true
  emit('error', '')
  try {
    await fn()
    emit('changed')
    if (moved) emit('moved', moved)
  } catch (e) {
    emit('error', e instanceof Error ? e.message : String(e))
  } finally {
    busy.value = false
  }
}

/**
 * Fund the contract from the connected wallet.
 *
 * An ordinary send to an ordinary address — the contract is a P2SH output and
 * the wallet neither knows nor needs to know that it is a swap. What it saves is
 * the copy-paste of an address and an amount, which is the step where a funding
 * goes to the wrong place or for the wrong figure.
 */
async function fundWithWallet() {
  if (!contractAddr.value) return
  busy.value = true
  emit('error', '')
  try {
    const txid = await wallet.send(contractAddr.value, amount.value)
    fundingTxid.value = txid
    fundAgain.value = false
    // Written down before anything else is attempted, and on the swap rather
    // than in this component. Everything after this line can fail — a slow node,
    // a refresh that throws, a reload — and every one of those used to end with
    // the card offering to send a second payment to a contract that only ever
    // spends one output.
    await api.fundingSent(props.swap.id, txid)
    await api.refresh(props.swap.id, settings.value)
    emit('changed')
    emit('moved', 'paid the Bitcoin contract')
  } catch (e) {
    emit('error', e instanceof Error ? e.message : String(e))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <section class="grid gap-3 rounded-lg border border-border p-3">
    <header class="flex flex-wrap items-baseline justify-between gap-2">
      <h3 class="text-sm font-medium">
        Bitcoin — {{ ours ? 'the leg you fund' : 'the leg they fund' }}
      </h3>
      <span class="font-mono text-xs text-muted-foreground">{{ leg.label }}</span>
    </header>

    <!-- Setting up. Which side owes what is decided by who funds it: the
         receiver's pubkey hash goes into the redeem branch, and the funder sends
         back the contract that commits to it. -->
    <div v-if="needsPkh" class="grid gap-2">
      <Callout tone="info">
        Waiting for their pubkey hash. The contract commits to it, so it cannot be built without
        one.
      </Callout>
      <Label for="pkh-in">Their pubkey hash</Label>
      <Input
        id="pkh-in"
        v-model="pkhIn"
        spellcheck="false"
        autocapitalize="none"
        autocomplete="off"
        class="font-mono text-xs"
        placeholder="20 bytes of hex"
      />
      <div class="flex flex-wrap items-center gap-2">
        <Button
          size="sm"
          :disabled="busy || !pkhIn.trim()"
          @click="run(() => api.counterparty(swap.id, pkhIn.trim()))"
        >
          Build the contract
        </Button>
        <span class="text-xs text-muted-foreground">
          A session fills this in for you. By hand it is the other half of what you sent them —
          paste it here and the contract is built from it.
        </span>
      </div>
    </div>

    <div v-if="needsAudit" class="grid gap-2">
      <Label for="contract-in">Their contract (hex)</Label>
      <Textarea
        id="contract-in"
        v-model="contractIn"
        rows="3"
        class="font-mono text-xs"
        placeholder="63a820…"
      />
      <div class="flex flex-wrap items-center gap-2">
        <Button
          size="sm"
          :disabled="busy || !contractIn.trim()"
          @click="run(() => api.audit(swap.id, contractIn.trim()))"
        >
          Audit it
        </Button>
        <span class="text-xs text-muted-foreground">
          Checked entirely offline: that your key can take the redeem branch, that it commits to
          this swap's hash, and that its locktime sits on the safe side of your own leg.
        </span>
      </div>
    </div>

    <DataList v-if="contractAddr">
      <DataRow label="Contract">
        <Address :address="contractAddr" />
      </DataRow>
      <DataRow v-if="leg.expiryAt" label="Deadline">
        <span class="font-mono text-xs">{{ leg.expiryAt }}</span>
        <span class="text-xs text-muted-foreground"> · {{ until(leg.expiryAt) }}</span>
      </DataRow>
      <DataRow v-if="funding" label="Funding">
        <TxRef :txid="funding.txid" label="transaction" />
        <span class="text-xs text-muted-foreground"> · {{ sats(funding.value) }}</span>
      </DataRow>
      <DataRow v-if="funding" label="Confirmations">
        <span class="text-xs">{{ depth.n }} / {{ depth.of }}</span>
        <span
          class="ml-2 inline-block h-1.5 w-24 overflow-hidden rounded-full bg-muted align-middle"
        >
          <span class="block h-full bg-primary" :style="{width: `${depth.pct}%`}" />
        </span>
      </DataRow>
    </DataList>

    <Callout v-if="btc?.fundingShort" tone="warning">
      <TriangleAlertIcon class="size-4" />
      This contract holds less than was agreed. Do not act on the other leg until it is resolved.
    </Callout>

    <!-- Funding. The contract hex goes to the counterparty either way, which is
         the card's job rather than this panel's — here there is only the send. -->
    <div v-if="canFund || (fundingSent && !funding)" class="grid gap-2">
      <UnlockCost
        v-if="cost"
        :cost="cost"
        :loading="costLoading"
        :error="costError"
        :unlocked-by="ours ? 'them' : 'you'"
      />
      <template v-if="fundingSent && !funding && !fundAgain">
        <Callout tone="info">
          A payment was sent as
          <TxRef :txid="fundingSent" label="transaction" />, and no node has listed it against
          the contract yet. Refresh from chain is what decides a contract is funded.
        </Callout>
        <Button variant="ghost" size="sm" @click="fundAgain = true">Send another payment</Button>
      </template>
      <template v-else>
        <WalletConnect />
        <div class="flex flex-wrap items-center gap-2">
          <Button size="sm" :disabled="blocked || busy || !wallet.connected.value" @click="fundWithWallet">
            Pay {{ sats(amount) }} from the wallet
          </Button>
          <span class="text-xs text-muted-foreground">
            or pay {{ contractAddr }} from any wallet — it is an ordinary send.
          </span>
        </div>
      </template>
    </div>

    <!-- Claiming and reclaiming. Both spend the contract, and both pay the one
         address the swap already names. -->
    <div v-if="needsDest" class="grid gap-2">
      <Label for="dest-in">Where your Bitcoin should land</Label>
      <Input id="dest-in" v-model="destIn" class="font-mono text-xs" placeholder="bc1…" />
      <p class="text-xs text-muted-foreground">
        This record predates the address being required. Whatever is supplied here becomes the
        swap's address for good — both ways out of the contract pay it and no other.
      </p>
    </div>

    <div v-if="canRedeem || canRefund" class="flex flex-wrap items-center gap-2">
      <Button
        v-if="canRedeem"
        size="sm"
        :disabled="blocked || busy || !dest"
        @click="run(() => api.redeem(swap.id, dest, settings), 'redeemed the Bitcoin contract')"
      >
        Redeem the Bitcoin
      </Button>
      <Button
        v-if="canRefund"
        variant="outline"
        size="sm"
        :disabled="blocked || busy || !dest"
        @click="run(() => api.refund(swap.id, dest, settings), 'refunded the Bitcoin contract')"
      >
        Refund
      </Button>
      <span v-if="canRefund" class="text-xs text-muted-foreground">
        Bitcoin compares a refund against the block median time past, which trails real time by
        about an hour — the network may refuse it for a little longer.
      </span>
    </div>

    <DataList v-if="btc?.refundTx || btc?.redeemTx">
      <DataRow v-if="btc?.refundTx" label="Refund">
        <TxRef :txid="btc.refundTx.txid" label="transaction" />
        <CopyButton :value="btc.refundTx.rawHex" label="raw hex" />
      </DataRow>
      <DataRow v-if="btc?.redeemTx" label="Redeem">
        <TxRef :txid="btc.redeemTx.txid" label="transaction" />
      </DataRow>
    </DataList>

    <!-- The pre-signed refund is the one thing on this card that is worth
         nothing until it is off this machine. localStorage is erased by "clear
         site data", by a private window closing, and by reinstalling a browser,
         with no warning. -->
    <Callout v-if="funding && !leg.done && ours" tone="warning">
      <span class="flex flex-wrap items-center gap-2">
        <span class="min-w-0 flex-1">
          Save the recovery file. It carries the key that spends this contract and the pre-signed
          refund, and it works with this site gone.
        </span>
        <Button
          variant="outline"
          size="sm"
          @click="
            run(async () => {
              const rec = await api.recovery(swap.id)
              const {download} = await import('@/core/api')
              download(`ferry-recovery-${swap.id}.json`, JSON.stringify(rec, null, 2))
            })
          "
        >
          <DownloadIcon />
          Download
        </Button>
      </span>
    </Callout>
  </section>
</template>

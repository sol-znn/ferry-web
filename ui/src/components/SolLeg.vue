<script setup lang="ts">
import {computed, ref} from 'vue'
import {Address, Button} from 'nom-ui'
import DataList from './DataList.vue'
import DataRow from './DataRow.vue'
import Callout from './Callout.vue'
import SolanaWallet from './SolanaWallet.vue'
import {api} from '@/core/api'
import {until} from '@/core/format'
import {lamportsToSol} from '@/core/solana'
import {useSettings} from '@/core/composables/useSettings'
import {useSolana} from '@/core/composables/useSolana'
import type {LegView, SolAction, Swap} from '@/types'

/**
 * The Solana leg of a swap, whichever side of it this user is on.
 *
 * Two steps with a look in between, the same shape the Zenon leg has with
 * Syrius: Go builds the instruction and says what it does, with nothing signed
 * and nothing sent; then the wallet opens its own window and signs it, and this
 * page submits the result through the node named in Node settings.
 *
 * Ferry never sees a Solana key. What it produced was a program id, a list of
 * accounts and some bytes; every step that could move a lamport happened inside
 * the wallet.
 *
 * The one thing worth knowing about this chain in particular: neither exit needs
 * a signature from the party being paid. A redeem is authorised by the preimage
 * and a refund by an expired clock, so whoever is watching can finish a swap the
 * other side has walked away from — and the connected wallet is only paying the
 * fee, not authorising the payment.
 */
const props = defineProps<{swap: Swap; leg: LegView; blocked: boolean}>()
const emit = defineEmits<{
  changed: []
  moved: [what: string]
  error: [message: string]
}>()

const {body: settings} = useSettings()
const solana = useSolana()

const busy = ref(false)
const rent = ref('')

const sol = computed(() => props.leg.sol)
const ours = computed(() => props.leg.dir === 'out')

const pending = computed(() => Boolean(sol.value?.verifyPending))
const bad = computed(() => Boolean(sol.value?.verifyError) && !pending.value)

/** Which instruction is this side's outstanding move, if any. Offered rather
 *  than allowed: Go re-checks every precondition and refuses by name. */
const action = computed<SolAction | null>(() => {
  if (props.leg.done) return null
  if (ours.value) {
    if (props.leg.expired && sol.value?.funded) return 'refund'
    return sol.value?.funded || sol.value?.createSig ? null : 'create'
  }
  return sol.value?.funded && props.swap.secretHex ? 'redeem' : null
})

const label = computed(
  () =>
    ({
      create: `Lock ${props.leg.label} in the escrow`,
      redeem: 'Claim your SOL',
      refund: 'Take your SOL back',
    })[action.value ?? 'create'],
)

/**
 * Build the instruction, sign it, submit it, and tell Go what came back.
 *
 * The signature is recorded before anything is concluded from it: a transaction
 * can be dropped, and one that landed can have failed, so a refresh is what
 * decides. What the record buys is that a reload in between does not offer to
 * fund the escrow a second time.
 */
async function act(which: SolAction) {
  busy.value = true
  emit('error', '')
  try {
    if (!solana.connected.value) {
      if (!(await solana.connect())) {
        emit('error', solana.error.value || 'No Solana wallet was connected.')
        return
      }
    }
    const built = await api.solInstruction(
      props.swap.id,
      which,
      solana.address.value,
      settings.value,
    )
    if (built.rent) rent.value = built.rent
    const signature = await solana.send(built.instruction)
    await api.solSent(props.swap.id, which, signature, settings.value)
    emit('changed')
    emit(
      'moved',
      {
        create: 'funded their Solana escrow',
        redeem: 'redeemed the Solana escrow, which publishes the preimage on Solana',
        refund: 'refunded their Solana escrow',
      }[which],
    )
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
        Solana — {{ ours ? 'the leg you fund' : 'the leg they fund' }}
      </h3>
      <span class="font-mono text-xs text-muted-foreground">{{ leg.label }}</span>
    </header>

    <Callout v-if="!sol?.programId" tone="warning">
      This swap does not name a Solana program, so there is no contract to look at. An escrow on one
      deployment is not an escrow on another — set one in Nodes, and make sure both sides agree
      which.
    </Callout>

    <DataList>
      <DataRow v-if="sol?.escrow" label="Escrow">
        <Address :address="sol.escrow" />
      </DataRow>
      <DataRow v-if="leg.selfAddr" label="Your address">
        <Address :address="leg.selfAddr" />
      </DataRow>
      <DataRow v-if="leg.peerAddr" label="Their address">
        <Address :address="leg.peerAddr" />
      </DataRow>
      <DataRow v-if="sol?.programId" label="Program">
        <Address :address="sol.programId" />
      </DataRow>
      <DataRow v-if="leg.expiryAt" label="Deadline">
        <span class="font-mono text-xs">{{ leg.expiryAt }}</span>
        <span class="text-xs text-muted-foreground"> · {{ until(leg.expiryAt) }}</span>
      </DataRow>
      <DataRow v-if="sol?.observedLamports" label="Account holds">
        <span class="text-xs">
          {{ lamportsToSol(sol.observedAmount ?? 0) }} SOL plus
          {{ lamportsToSol((sol.observedLamports ?? 0) - (sol.observedAmount ?? 0)) }} rent
        </span>
      </DataRow>
    </DataList>

    <Callout v-if="pending" tone="info">
      Not read from the cluster yet. This re-checks by itself every minute.
    </Callout>
    <Callout v-else-if="bad" tone="destructive">{{ sol?.verifyError }}</Callout>
    <Callout v-else-if="sol?.verified" tone="success">
      Verified against the escrow account this swap's id derives: parties, amount, hashlock and
      deadline all match.
    </Callout>

    <div v-if="action" class="grid gap-2">
      <SolanaWallet />
      <div class="flex flex-wrap items-center gap-2">
        <Button size="sm" :disabled="blocked || busy" @click="act(action)">{{ label }}</Button>
        <span v-if="action === 'create' && rent" class="text-xs text-muted-foreground">
          plus {{ rent }} SOL of rent, which comes back to you on either exit
        </span>
        <span v-else-if="action !== 'create'" class="text-xs text-muted-foreground">
          Your wallet only pays the fee — neither exit needs a signature from the party being paid.
        </span>
      </div>
    </div>

    <Callout v-if="sol?.redeemed" tone="success">
      Redeemed in {{ sol.redeemSig }}. The preimage that did it is public on Solana.
    </Callout>
    <Callout v-if="sol?.refunded" tone="info">
      Refunded to its funder in {{ sol.refundSig }}.
    </Callout>
    <Callout v-else-if="sol?.createSig && !sol.funded" tone="info">
      A create was submitted as {{ sol.createSig }} and the cluster has not confirmed it yet.
      Refresh from chain is what decides an escrow is funded.
    </Callout>
  </section>
</template>

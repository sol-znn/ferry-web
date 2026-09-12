<script setup lang="ts">
import {computed, reactive, ref, watch} from 'vue'
import {CheckIcon, LockIcon} from '@lucide/vue'
import {
  Badge,
  Button,
  Input,
  Label,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from 'nom-ui'
import Callout from './Callout.vue'
import InfoTip from './InfoTip.vue'
import SolanaWallet from './SolanaWallet.vue'
import UnlockCost from './UnlockCost.vue'
import WalletConnect from './WalletConnect.vue'
import ZenonWalletStrip from './ZenonWalletStrip.vue'
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
import {useSettings} from '@/core/composables/useSettings'
import {useSolana} from '@/core/composables/useSolana'
import {useUnisat} from '@/core/composables/useUnisat'
import {useUnlockCost} from '@/core/composables/useUnlockCost'
import {useZenonWallet} from '@/core/composables/useZenonWallet'
import type {DecodedOffer, Role} from '@/types'

/**
 * Making a swap, in the order the decisions actually come.
 *
 * v1 asked for everything at once, which worked while there was one pair: the
 * chains were implied, so the first real question was an amount. With four pairs
 * the chains are the first question and everything after depends on the answer —
 * which units the amounts are in, which addresses are needed, which wallets have
 * to be there at all.
 *
 * So it is three steps, and the middle one is the point:
 *
 *   1. Which chains. Nothing else can be asked before this.
 *   2. The wallets those chains need, connected here, before any term is typed.
 *   3. The terms.
 *
 * Step 2 is a gate rather than a reminder because of what the wallets supply:
 * the addresses both ways out of this swap pay. Asking for them at the end means
 * discovering, after the amounts are agreed, that the extension for one of these
 * chains is not installed — and it means the address fields get typed into by
 * hand, which is the single easiest place in a swap to paste somebody else's.
 *
 * Nothing here is a swap term until Go says so. An offer this form was filled in
 * from is sent with the create, and `Offer.CheckCreate` refuses anything that
 * drifted from it — this component locks those fields so nobody reaches that
 * refusal, but the refusal is the guarantee.
 */
const props = defineProps<{
  /** The offer this form is answering, when it answers one. */
  decoded?: DecodedOffer | null
  /** Whether the offer's terms are pinned. */
  locked?: boolean
  busy?: boolean
  error?: string
}>()
const emit = defineEmits<{submit: [Record<string, unknown>]; cancel: []}>()

const {body: settings, stored, hasZenon, solProgram} = useSettings()
const unisat = useUnisat()
const zenon = useZenonWallet()
const solana = useSolana()

const step = ref<1 | 2 | 3>(1)

const form = reactive({
  pair: PAIRS[0].id,
  /** Which chain THIS user funds. The other half of the pair is what they
   *  receive, so one choice settles both. */
  give: PAIRS[0].a as ChainId,
  role: 'initiator' as Role,
  giveToken: KNOWN_TOKENS[0].zts,
  wantToken: KNOWN_TOKENS[1].zts,
  giveAmount: '',
  wantAmount: '',
  giveSelfAddr: '',
  givePeerAddr: '',
  wantSelfAddr: '',
  wantPeerAddr: '',
  /** Bitcoin only, and only where this user builds the contract. */
  peerPkh: '',
  secretHashHex: '',
  lockHours: '',
})

const pair = computed(() => pairFor(form.pair) ?? PAIRS[0])
const want = computed<ChainId>(() =>
  pair.value.a === pair.value.b
    ? pair.value.a
    : form.give === pair.value.a
      ? pair.value.b
      : pair.value.a,
)
const chains = computed(() => orderChains([form.give, want.value]))

const directions = computed(() =>
  pair.value.a === pair.value.b
    ? []
    : [
        {
          value: pair.value.a,
          label: `I pay ${chainLabel(pair.value.a)}, I receive ${chainLabel(pair.value.b)}`,
        },
        {
          value: pair.value.b,
          label: `I pay ${chainLabel(pair.value.b)}, I receive ${chainLabel(pair.value.a)}`,
        },
      ],
)

const giveUnit = computed(() => unitFor(form.give, form.giveToken))
const wantUnit = computed(() => unitFor(want.value, form.wantToken))

// ---------- the wallet gate ----------

/** What each chain needs, and whether it is there. A wallet is "connected" when
 *  it has handed over an address: that is what the swap actually consumes, and a
 *  connection that produced none is a connection that has not happened yet. */
const wallets = computed(() =>
  chains.value.map((chain) => {
    const state = {
      btc: {name: 'UniSat', address: unisat.address.value, connected: unisat.connected.value},
      znn: {name: 'Syrius', address: zenon.address.value, connected: zenon.connected.value},
      sol: {name: solana.walletName.value, address: solana.address.value, connected: solana.connected.value},
    }[chain]
    return {chain, ...state, ready: state.connected && Boolean(state.address)}
  }),
)

const walletsReady = computed(() => wallets.value.every((w) => w.ready))
const missingWallets = computed(() =>
  wallets.value.filter((w) => !w.ready).map((w) => w.chain as ChainId),
)

/** A Zenon leg cannot be checked without a node, and the check is the one place
 *  a dishonest answer costs money. Said loudly, not blocking: somebody may be
 *  setting a swap up before configuring one. */
const zenonNodeMissing = computed(() => chains.value.includes('znn') && !hasZenon.value)
/** A Solana leg cannot be created at all without naming a deployment. */
const solProgramMissing = computed(() => chains.value.includes('sol') && !solProgram.value)

/**
 * Fill in the addresses this user's own wallets can supply.
 *
 * Only into empty fields, and only from a wallet that is connected. Somebody who
 * typed an address meant it, and stepping back and forth through this form is
 * not a reason to overwrite it with whichever account an extension has selected.
 */
function fillOwnAddresses() {
  const addr = (c: ChainId) =>
    ({btc: unisat.address.value, znn: zenon.address.value, sol: solana.address.value})[c] ?? ''
  if (!form.giveSelfAddr.trim()) form.giveSelfAddr = addr(form.give)
  if (!form.wantSelfAddr.trim()) form.wantSelfAddr = addr(want.value)
}

watch([() => unisat.address.value, () => zenon.address.value, () => solana.address.value], () =>
  fillOwnAddresses(),
)

// Keep the direction inside the chosen pair, and the two Zenon tokens apart.
watch(
  () => form.pair,
  () => {
    const p = pair.value
    if (form.give !== p.a && form.give !== p.b) form.give = p.a
    if (p.a === 'znn' && p.b === 'znn' && form.giveToken === form.wantToken) {
      form.wantToken =
        form.giveToken === KNOWN_TOKENS[0].zts ? KNOWN_TOKENS[1].zts : KNOWN_TOKENS[0].zts
    }
    // The addresses belong to the chains, so a changed pair invalidates them.
    form.giveSelfAddr = ''
    form.wantSelfAddr = ''
    fillOwnAddresses()
  },
)
watch(
  () => form.give,
  () => {
    form.giveSelfAddr = ''
    form.wantSelfAddr = ''
    fillOwnAddresses()
  },
)

// ---------- an offer being answered ----------

/**
 * Put every term the offer pins into the form.
 *
 * The sender's `take` is this side's give and vice versa, which is the one
 * inversion in this file and the one worth stating: getting it backwards is two
 * people each waiting for the other to fund a leg neither of them has.
 */
function applyOffer(d: DecodedOffer) {
  const out = d.yourOut
  const inn = d.yourIn
  form.pair = pairFor(`${out.chain}-${inn.chain}`)?.id ?? `${inn.chain}-${out.chain}`
  form.give = out.chain
  form.role = d.yourRole
  form.giveToken = out.token ?? KNOWN_TOKENS[0].zts
  form.wantToken = inn.token ?? KNOWN_TOKENS[0].zts
  form.giveAmount = out.amount
  form.wantAmount = inn.amount
  // The addresses in an offer are the SENDER's, so each is what this side calls
  // the peer. Their pubkey hash is only useful to whoever builds the contract.
  form.givePeerAddr = out.addr ?? ''
  form.wantPeerAddr = inn.addr ?? ''
  // Whichever half is Bitcoin carries it, and only one half ever can — so
  // taking it from either leg is unambiguous. It used to be read from the In
  // leg alone, which left the taker of a swap they fund in Bitcoin waiting for
  // a hash the offer had already handed them.
  form.peerPkh = inn.pkh ?? out.pkh ?? ''
  form.secretHashHex = d.decoded.secretHash ?? ''
  fillOwnAddresses()
  step.value = 2
}

watch(
  () => props.decoded,
  (d) => {
    if (d) applyOffer(d)
  },
  {immediate: true},
)

/** Prefill from a trade agreed on the board, which is a head start and never an
 *  authority: nothing there has been signed. */
function applyTrade(t: {
  out: {chain: ChainId; token?: string; amount: string; peerAddr?: string}
  in: {chain: ChainId; token?: string; amount: string; peerAddr?: string}
  role: Role
  lockHours: number
}) {
  form.pair = pairFor(`${t.out.chain}-${t.in.chain}`)?.id ?? `${t.in.chain}-${t.out.chain}`
  form.give = t.out.chain
  form.role = t.role
  form.giveToken = t.out.token || KNOWN_TOKENS[0].zts
  form.wantToken = t.in.token || KNOWN_TOKENS[0].zts
  form.giveAmount = t.out.amount
  form.wantAmount = t.in.amount
  form.givePeerAddr = t.out.peerAddr ?? ''
  form.wantPeerAddr = t.in.peerAddr ?? ''
  form.lockHours = t.lockHours ? String(t.lockHours) : ''
  fillOwnAddresses()
  step.value = 2
}

defineExpose({applyTrade, applyOffer})

/** Their address on the leg THEY fund. Needed everywhere except Bitcoin, where
 *  a contract commits to a pubkey hash rather than an address. */
const needPeerOnIn = computed(() => want.value !== 'btc')

const isParticipant = computed(() => form.role === 'participant')

/** What emptying a Bitcoin contract will cost, while the amount is still a
 *  number in a field rather than money in a contract. */
const unlockInput = computed(() => {
  const btcLeg = form.give === 'btc' ? 'give' : want.value === 'btc' ? 'want' : null
  if (!btcLeg) return null
  const amountSats = Number(btcLeg === 'give' ? form.giveAmount : form.wantAmount)
  if (!Number.isFinite(amountSats) || amountSats <= 0) return null
  return {
    amountSats,
    destAddr: btcLeg === 'want' ? form.wantSelfAddr.trim() : form.giveSelfAddr.trim(),
  }
})
const {cost: unlockCost, loading: unlockLoading, error: unlockError} = useUnlockCost(
  unlockInput,
  settings,
)
const unlockedBy = computed<'you' | 'them'>(() => (want.value === 'btc' ? 'you' : 'them'))

const problem = computed(() => {
  if (!(Number(form.giveAmount) > 0)) return `Give an amount in ${giveUnit.value}.`
  if (!(Number(form.wantAmount) > 0)) return `Give an amount in ${wantUnit.value}.`
  if (!form.giveSelfAddr.trim()) {
    return `A ${chainLabel(form.give)} address of yours is required — it is where this leg comes back to if the swap does not complete.`
  }
  if (want.value === 'btc' && !form.wantSelfAddr.trim()) {
    return 'A Bitcoin destination address is required — it is where both branches of the contract pay.'
  }
  if (want.value !== 'btc' && !form.wantSelfAddr.trim()) {
    return `A ${chainLabel(want.value)} address of yours is required — it is where your half gets paid.`
  }
  if (isParticipant.value && !form.secretHashHex.trim()) {
    return "As the participant you need the initiator's secret hash."
  }
  if (form.give === 'znn' && want.value === 'znn' && form.giveToken === form.wantToken) {
    return 'Both legs are the same token, so nothing is being traded.'
  }
  if (solProgramMissing.value) {
    return 'No Solana program is set. Name one in Nodes — an escrow on one deployment is not an escrow on another.'
  }
  return ''
})

function submit() {
  if (problem.value) return
  emit('submit', {
    role: form.role,
    out: {
      chain: form.give,
      ...(form.give === 'znn' ? {token: form.giveToken.trim()} : {}),
      amount: form.giveAmount.trim(),
      selfAddr: form.giveSelfAddr.trim(),
      peerAddr: form.givePeerAddr.trim(),
      // A Bitcoin leg this user funds is a contract this user builds, and the
      // redeem branch commits to the counterparty's key — so the hash is needed
      // here for exactly the reason it is needed on the In leg below.
      ...(form.give === 'btc' ? {peerPkh: form.peerPkh.trim()} : {}),
      // The offer's own leg values win over this browser's settings, and the
      // swap id is the reason: it is the PDA seed, so a taker that mints a
      // fresh one funds an escrow the proposer is not watching. Same rule as
      // the In leg below — `yourOut` is the offer's `take`.
      ...(form.give === 'sol'
        ? {
            program: props.decoded?.yourOut.program || solProgram.value,
            swapId: props.decoded?.yourOut.swapId ?? '',
          }
        : {}),
    },
    in: {
      chain: want.value,
      ...(want.value === 'znn' ? {token: form.wantToken.trim()} : {}),
      amount: form.wantAmount.trim(),
      selfAddr: form.wantSelfAddr.trim(),
      peerAddr: form.wantPeerAddr.trim(),
      ...(want.value === 'btc' ? {peerPkh: form.peerPkh.trim()} : {}),
      ...(want.value === 'sol'
        ? {
            program: props.decoded?.yourIn.program || solProgram.value,
            swapId: props.decoded?.yourIn.swapId ?? '',
          }
        : {}),
    },
    lockHours: Number(form.lockHours || 0),
    secretHashHex: form.secretHashHex.trim(),
  })
}

const LOCKED_FIELD = 'bg-muted/60 text-muted-foreground focus-visible:ring-0'
</script>

<template>
  <div class="grid gap-5">
    <!-- The track. Three steps, and the numbers are there because the order is
         the point: nothing after step one can be asked before it is answered. -->
    <ol class="flex flex-wrap items-center gap-2 text-xs">
      <li
        v-for="(name, i) in ['Chains', 'Wallets', 'Terms']"
        :key="name"
        class="flex items-center gap-1.5"
      >
        <span
          class="flex size-5 items-center justify-center rounded-full border text-[10px]"
          :class="
            step > i + 1
              ? 'border-success bg-success/10 text-success'
              : step === i + 1
                ? 'border-primary bg-primary/10 text-primary'
                : 'border-border text-muted-foreground'
          "
        >
          <CheckIcon v-if="step > i + 1" class="size-3" />
          <template v-else>{{ i + 1 }}</template>
        </span>
        <span :class="step === i + 1 ? 'font-medium' : 'text-muted-foreground'">{{ name }}</span>
        <span v-if="i < 2" class="mx-1 text-muted-foreground">›</span>
      </li>
    </ol>

    <!-- ---------- step 1: the chains ---------- -->
    <section v-if="step === 1" class="grid gap-4">
      <div class="grid gap-2">
        <Label class="flex items-center gap-1.5">
          Which chains
          <InfoTip label="What a pair decides">
            <p>
              Everything after it. The units the amounts are in, the addresses the swap needs, which
              wallets have to be connected, and which contract each half locks in.
            </p>
            <p>
              ZTS against ZTS is one chain twice, which is a trade only because the tokens differ.
              Both legs are ordinary Zenon HTLCs against one secret.
            </p>
          </InfoTip>
        </Label>
        <div class="grid gap-2 sm:grid-cols-2">
          <button
            v-for="p in PAIRS"
            :key="p.id"
            type="button"
            class="rounded-lg border p-3 text-left transition-colors"
            :class="
              form.pair === p.id
                ? 'border-primary bg-primary/5'
                : 'border-border hover:border-primary/40'
            "
            :disabled="locked"
            @click="form.pair = p.id"
          >
            <span class="block text-sm font-medium">{{ p.label }}</span>
            <span class="block text-xs text-muted-foreground">
              {{ chainList(p.chains) }}
            </span>
          </button>
        </div>
      </div>

      <div v-if="directions.length" class="grid gap-2">
        <Label>Which way</Label>
        <Select
          :model-value="form.give"
          :disabled="locked"
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

      <div v-if="form.give === 'znn' && want === 'znn'" class="grid gap-2 sm:grid-cols-2">
        <div class="grid gap-2">
          <Label for="ns-gtoken">You pay (token)</Label>
          <Input id="ns-gtoken" v-model="form.giveToken" class="font-mono" :disabled="locked" />
        </div>
        <div class="grid gap-2">
          <Label for="ns-wtoken">You receive (token)</Label>
          <Input id="ns-wtoken" v-model="form.wantToken" class="font-mono" :disabled="locked" />
        </div>
      </div>

      <Callout v-if="locked" tone="info">
        These are the chains the offer you are answering settles on, so they are not yours to
        change. To trade something else, discard the offer and propose one of your own.
      </Callout>

      <div class="flex flex-wrap gap-2">
        <Button @click="((step = 2), fillOwnAddresses())">
          Next — connect {{ chainList(chains) }}
        </Button>
        <Button variant="ghost" @click="$emit('cancel')">Cancel</Button>
      </div>
    </section>

    <!-- ---------- step 2: the wallets ---------- -->
    <section v-if="step === 2" class="grid gap-4">
      <p class="text-sm text-muted-foreground">
        This trade settles on {{ chainList(chains) }}, so it needs a wallet on
        {{ chains.length === 1 ? 'that chain' : 'each' }}. They supply the addresses both ways out
        of this swap pay — which is why they are asked for here rather than typed in later.
      </p>

      <div
        v-for="w in wallets"
        :key="w.chain"
        class="grid gap-2 rounded-lg border p-3"
        :class="w.ready ? 'border-success/40 bg-success/5' : 'border-border'"
      >
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-sm font-medium">{{ chainLabel(w.chain) }}</span>
          <Badge v-if="w.ready" variant="success">connected</Badge>
          <Badge v-else variant="outline">{{ w.name }} needed</Badge>
        </div>
        <WalletConnect v-if="w.chain === 'btc'" />
        <ZenonWalletStrip v-else-if="w.chain === 'znn'" />
        <SolanaWallet v-else />
      </div>

      <Callout v-if="zenonNodeMissing" tone="warning">
        No Zenon node is set. The leg on that chain cannot be verified until one is — open Nodes and
        give this browser a JSON-RPC URL it can reach.
      </Callout>
      <Callout v-if="solProgramMissing" tone="warning">
        No Solana program is set. A Solana leg cannot be created without naming the deployment its
        escrow lives on — set one in Nodes.
      </Callout>

      <div class="flex flex-wrap items-center gap-2">
        <Button variant="ghost" @click="step = 1">Back</Button>
        <Button :disabled="!walletsReady" @click="((step = 3), fillOwnAddresses())">
          Next — the terms
        </Button>
        <span v-if="!walletsReady" class="text-xs text-warning">
          Still waiting on {{ chainList(missingWallets) }}.
        </span>
      </div>
    </section>

    <!-- ---------- step 3: the terms ---------- -->
    <section v-if="step === 3" class="grid gap-4">
      <Callout v-if="locked" tone="info">
        <LockIcon class="mr-1 inline size-3" />
        The terms below came from the offer you are answering and are not yours to edit. Both
        browsers build their own contract from their own record, so a term that differs here is two
        people running different swaps — checked again in Go before anything is created.
      </Callout>

      <div class="grid gap-4 sm:grid-cols-2">
        <div class="grid gap-2">
          <Label for="ns-give">You pay ({{ giveUnit }})</Label>
          <Input
            id="ns-give"
            v-model="form.giveAmount"
            inputmode="decimal"
            autocomplete="off"
            class="font-mono"
            :readonly="locked"
            :class="locked ? LOCKED_FIELD : ''"
          />
        </div>
        <div class="grid gap-2">
          <Label for="ns-want">You receive ({{ wantUnit }})</Label>
          <Input
            id="ns-want"
            v-model="form.wantAmount"
            inputmode="decimal"
            autocomplete="off"
            class="font-mono"
            :readonly="locked"
            :class="locked ? LOCKED_FIELD : ''"
          />
        </div>

        <div class="grid gap-2">
          <Label for="ns-giveself">
            Your {{ chainLabel(form.give) }} address
            <InfoTip label="What this address is for">
              The leg you fund comes back here if the swap does not complete. On Solana it is also
              the account the escrow records as its funder, and the only one a refund can reach.
            </InfoTip>
          </Label>
          <Input id="ns-giveself" v-model="form.giveSelfAddr" class="font-mono text-xs" />
        </div>
        <div class="grid gap-2">
          <Label for="ns-wantself">Your {{ chainLabel(want) }} address</Label>
          <Input id="ns-wantself" v-model="form.wantSelfAddr" class="font-mono text-xs" />
          <p v-if="want === 'btc'" class="text-xs text-muted-foreground">
            Both branches of the contract pay this and no other.
          </p>
        </div>

        <div v-if="form.give !== 'btc'" class="grid gap-2">
          <Label for="ns-givepeer">Their {{ chainLabel(form.give) }} address</Label>
          <Input
            id="ns-givepeer"
            v-model="form.givePeerAddr"
            class="font-mono text-xs"
            placeholder="where your leg pays them"
          />
        </div>
        <div v-if="needPeerOnIn" class="grid gap-2">
          <Label for="ns-wantpeer">Their {{ chainLabel(want) }} address</Label>
          <Input
            id="ns-wantpeer"
            v-model="form.wantPeerAddr"
            class="font-mono text-xs"
            :readonly="locked && Boolean(decoded?.yourIn.addr)"
            :class="locked && decoded?.yourIn.addr ? LOCKED_FIELD : ''"
            placeholder="filled in over a session, or by hand"
          />
        </div>
        <div v-if="want === 'btc' || form.give === 'btc'" class="grid gap-2">
          <Label for="ns-pkh">
            Their pubkey hash
            <InfoTip label="Why Bitcoin needs this instead of an address">
              A contract commits to a pubkey hash rather than an address: it is what names the key
              allowed to take the redeem branch. They send it; you build the contract from it.
            </InfoTip>
          </Label>
          <Input
            id="ns-pkh"
            v-model="form.peerPkh"
            class="font-mono text-xs"
            :readonly="locked && Boolean(decoded?.yourIn.pkh ?? decoded?.yourOut.pkh)"
            :class="locked && (decoded?.yourIn.pkh ?? decoded?.yourOut.pkh) ? LOCKED_FIELD : ''"
          />
        </div>

        <div class="grid gap-2">
          <Label>
            Your role
            <InfoTip label="Initiator or participant">
              The initiator invents the secret and takes the longer deadline; the participant must
              be able to take their leg back strictly first. A swap has exactly one of each.
            </InfoTip>
          </Label>
          <Select
            :model-value="form.role"
            :disabled="locked"
            @update:model-value="(v) => (form.role = String(v) as Role)"
          >
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="initiator">initiator</SelectItem>
              <SelectItem value="participant">participant</SelectItem>
            </SelectContent>
          </Select>
        </div>

        <div v-if="isParticipant" class="grid gap-2">
          <Label for="ns-hash">Their secret hash</Label>
          <Input
            id="ns-hash"
            v-model="form.secretHashHex"
            class="font-mono text-xs"
            :readonly="locked"
            :class="locked ? LOCKED_FIELD : ''"
          />
        </div>

        <div class="grid gap-2">
          <Label for="ns-lock">
            Your deadline (hours, optional)
            <InfoTip label="What happens if you leave it blank">
              Ferry works it out from your role: 48 hours for the initiator's leg and 24 for the
              participant's, which is the ordering the whole scheme rests on. Setting it by hand is
              for the cases where a counterparty asked for something else. Three hours is the
              floor: a leg needs two hours left at the moment it is funded, and funding is always
              some minutes after this form.
            </InfoTip>
          </Label>
          <Input
            id="ns-lock"
            v-model="form.lockHours"
            inputmode="numeric"
            autocomplete="off"
            class="font-mono"
          />
        </div>
      </div>

      <UnlockCost
        v-if="unlockInput"
        :cost="unlockCost"
        :loading="unlockLoading"
        :error="unlockError"
        :unlocked-by="unlockedBy"
      />

      <Callout v-if="problem" tone="warning">{{ problem }}</Callout>
      <Callout v-if="error" tone="destructive">{{ error }}</Callout>

      <div class="flex flex-wrap gap-2">
        <Button variant="ghost" @click="step = 2">Back</Button>
        <Button :disabled="busy || Boolean(problem)" @click="submit">Create the swap</Button>
        <Button variant="ghost" @click="$emit('cancel')">Cancel</Button>
      </div>
    </section>

    <p class="text-xs text-muted-foreground">
      Posted for <span class="font-mono">{{ stored.network }}</span
      >, the network this page is set to.
    </p>
  </div>
</template>

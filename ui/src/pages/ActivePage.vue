<script setup lang="ts">
import {computed, onMounted, ref, watch} from 'vue'
import {ArrowRightLeftIcon, PlusIcon, RefreshCwIcon, ShieldCheckIcon, XIcon} from '@lucide/vue'
import {Button, Card, CardContent, CardHeader, CardTitle, Heading, Textarea} from 'nom-ui'
import BackupPanel from '@/components/BackupPanel.vue'
import Callout from '@/components/Callout.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import HistoryRow from '@/components/HistoryRow.vue'
import InfoTip from '@/components/InfoTip.vue'
import LoadingState from '@/components/LoadingState.vue'
import NewSwapForm from '@/components/NewSwapForm.vue'
import SessionPanel from '@/components/SessionPanel.vue'
import SwapCard from '@/components/SwapCard.vue'
import TermsGate from '@/components/TermsGate.vue'
import {api} from '@/core/api'
import {useFerry} from '@/core/composables/useFerry'
import {useSession, type PendingTrade} from '@/core/composables/useSession'
import {useSettings} from '@/core/composables/useSettings'
import type {DecodedOffer} from '@/types'

const {swaps, listError, loading, reload} = useFerry()
const {body: settings} = useSettings()
const session = useSession()

const createError = ref('')
const creating = ref(false)
const newOpen = ref(false)
const termsOpen = ref(false)
const offerBlob = ref('')
const decoded = ref<DecodedOffer | null>(null)
/** The exact string `decoded` came from, which is not always what the textarea
 *  holds now: editing the box after decoding leaves the two disagreeing, and
 *  sending the live text to the create check would have Go comparing the form
 *  against an offer the form was never filled in from. */
const decodedFrom = ref('')
const decodeError = ref('')
const showRawOffer = ref(false)
/** Which board post this form came from, for the banner. */
const fromBoard = ref<{postId: string; peer: string} | null>(null)

const formRef = ref<InstanceType<typeof NewSwapForm> | null>(null)

/**
 * The terms an offer pins stop being this side's to type.
 *
 * A swap is two records that have to say the same thing, and an offer is how the
 * second one gets filled in from the first — after which every value it filled
 * in stayed editable in v1. That made the cheapest way to break a swap a stray
 * keystroke in a number both sides had already agreed to, found (if at all) at
 * whichever chain-touching step first depended on the field that moved, which is
 * always after somebody has committed money.
 *
 * This is the second line and not the line. Go refuses a create whose terms
 * drifted from the offer it claims to answer (Offer.CheckCreate), whatever the
 * form did — that is the guarantee, and this is what stops anyone reaching it.
 */
const locked = computed(() => Boolean(decoded.value))

/**
 * The gate, on the way IN to the form rather than on the way out.
 *
 * Every path that reveals the form goes through this rather than setting
 * `newOpen` directly, which is what makes it impossible to skip: the check does
 * not depend on knowing every place the panel can be opened from, it depends on
 * the panel only ever becoming visible one way.
 */
function openNewSwapForm() {
  if (newOpen.value) return
  termsOpen.value = true
}

function onTermsAccepted() {
  newOpen.value = true
}

// An offer that arrived over a session is exactly the offer somebody would have
// pasted, so it goes in the same box and through the same decoder. Filling the
// field rather than applying it is deliberate: the user still presses the button
// that reads it, and can see what they are agreeing to first.
watch(session.pendingOffer, (offer) => {
  if (!offer) return
  offerBlob.value = offer
  openNewSwapForm()
})

/**
 * A trade agreed on the board, arriving with the user who was redirected here.
 *
 * A head start and never an authority: nothing on a board has been signed by a
 * counterparty, so none of it may pin a swap term. The fields stay editable, and
 * the real `swapoffer2` — which arrives over the session moments later and IS
 * signed — overwrites them and locks the terms it carries. Go still refuses any
 * create that drifts from that offer.
 */
function applyPendingTrade(trade: PendingTrade) {
  fromBoard.value = {postId: trade.postId, peer: trade.peer}
  session.pendingTrade.value = null
  openNewSwapForm()
  // After the panel exists, so the form component is mounted to receive it.
  void Promise.resolve().then(() => formRef.value?.applyTrade(trade))
}

// Set while this page was already open — which a trade agreed on the board is
// not. Taking or accepting happens on /board and sets it before redirecting
// here, so the arrival case is handled at mount below.
watch(session.pendingTrade, (trade) => {
  if (trade) applyPendingTrade(trade)
})

// A session message that changed a swap changed it in storage, not here.
watch(session.changed, () => void reload('active'))

onMounted(async () => {
  const trade = session.pendingTrade.value
  if (trade) applyPendingTrade(trade)
  await reload('active')
})

async function decode() {
  decodeError.value = ''
  decoded.value = null
  try {
    const raw = offerBlob.value.trim()
    const d = await api.decodeOffer(raw)
    decoded.value = d
    decodedFrom.value = raw
    openNewSwapForm()
  } catch (e) {
    decodeError.value = e instanceof Error ? e.message : String(e)
  }
}

/** Stop answering their offer and start proposing one — the honest way to trade
 *  on different terms, as against quietly creating a swap that says so. */
function discardOffer() {
  decoded.value = null
  decodedFrom.value = ''
  offerBlob.value = ''
}

async function create(body: Record<string, unknown>) {
  createError.value = ''
  creating.value = true
  try {
    const created = await api.create(
      {
        ...body,
        // The offer this form answers, when it answers one. Go re-decodes it and
        // refuses the create if any term it pins has been edited — see
        // Offer.CheckCreate. Sending the string rather than a "these matched"
        // flag is what makes that a check rather than a claim.
        offer: decoded.value ? decodedFrom.value : '',
      },
      settings.value,
    )
    await reload('active')
    // A session open before the swap existed now has something to be about.
    // Attaching is what gives arriving values somewhere to go — until then they
    // are received, checked against nothing, and reported as such.
    if (session.active.value) {
      session.attach(created.id, created.role)
      // The offer is the exception, sent here because it is not a hand-off: it
      // does not update the counterparty's swap, it is what brings one into
      // being, so there is nothing on either record for a watcher to notice.
      if (created.role === 'initiator') {
        try {
          const {offer} = await api.offer(created.id)
          await session.send({type: 'offer', offer}, 'Sent your offer')
        } catch (e) {
          session.say(
            'sys',
            `Could not send the offer: ${e instanceof Error ? e.message : String(e)}`,
            false,
          )
        }
      }
    }
    // The swap it made is now the thing worth looking at, so the form that made
    // it gets out of the way.
    newOpen.value = false
    fromBoard.value = null
    discardOffer()
  } catch (e) {
    createError.value = e instanceof Error ? e.message : String(e)
  } finally {
    creating.value = false
  }
}
</script>

<template>
  <div class="grid min-w-0 gap-8">
    <!-- What is true of every swap on this page, in one line, with the long
         version behind the icons rather than above the fold. -->
    <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
      <span class="flex items-center gap-1.5">
        <ShieldCheckIcon class="size-3.5 text-success" />
        Ferry never holds your coins or your wallet keys.
        <InfoTip label="How non-custodial works here">
          A Bitcoin leg gets a throwaway key that can only move coins locked in that one contract,
          and both ways out of it pay an address you gave. The Zenon and Solana legs are signed by
          your own wallets, in their own windows. Neither is ever asked for a key.
        </InfoTip>
      </span>
      <span class="flex items-center gap-1.5 text-warning">
        Those keys live only in this browser — keep a backup.
        <InfoTip variant="warn" label="Why a backup matters">
          Stored unencrypted in this browser: clearing site data, closing a private window or
          reinstalling destroys it with no warning. Download each swap's recovery file once it is
          funded, and export from the Backup panel below.
        </InfoTip>
      </span>
    </div>

    <SessionPanel />

    <!-- Swaps in progress. min-w-0 because a grid item defaults to min-width
         auto, and this one holds cards wide enough to push the whole page
         sideways if any level of the chain forgets it. -->
    <section class="grid min-w-0 gap-4">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <Heading :level="2" class="text-xl">
          Swaps in progress
          <span v-if="swaps.length" class="ml-1 font-mono text-base text-muted-foreground">
            {{ swaps.length }}
          </span>
        </Heading>
        <div class="flex gap-2">
          <Button variant="outline" size="sm" @click="reload('active')">
            <RefreshCwIcon />
            Reload
          </Button>
          <!-- Closing needs no gate — only opening does — so this is the one
               place that still touches `newOpen` directly. -->
          <Button
            size="sm"
            @click="newOpen ? ((newOpen = false), (fromBoard = null)) : openNewSwapForm()"
          >
            <component :is="newOpen ? XIcon : PlusIcon" />
            {{ newOpen ? 'Close' : 'New swap' }}
          </Button>
        </div>
      </div>

      <ErrorState v-if="listError" :message="listError" />
      <LoadingState v-else-if="loading && !swaps.length" />
      <EmptyState
        v-else-if="!swaps.length"
        message="Nothing in progress."
        hint="Press New swap above to start one or paste an offer somebody sent you."
      />
      <!-- A settled swap has nothing left to do but be filed: both legs are
           finished, and it sits here only because nobody has pressed Archive
           yet. Nothing files itself — see Swap.Active in wasm/swap.go — so this
           is where a completed swap is actually read, and it stays until the
           person who ran it says so. It gets the shrunk row rather than the
           full card, for the one button it still needs. -->
      <template v-for="sw in swaps" v-else :key="sw.id">
        <HistoryRow v-if="sw.settled" :swap="sw" archivable @changed="reload('active')" />
        <SwapCard v-else :swap="sw" @changed="reload('active')" />
      </template>
    </section>

    <Card v-if="newOpen" class="border-primary/40">
      <CardHeader class="gap-3">
        <CardTitle class="flex flex-wrap items-center gap-2">
          New swap
          <span class="rounded-full bg-muted px-2 py-0.5 font-mono text-xs font-normal">
            {{ settings.network }}
          </span>
        </CardTitle>
      </CardHeader>

      <CardContent class="grid gap-6">
        <!-- A trade agreed on the board. Said plainly, because the amounts were
             not typed by the person looking at them and a form that filled
             itself in without saying so is a form nobody checks. -->
        <div
          v-if="fromBoard && !locked"
          class="flex flex-wrap items-start gap-2 rounded-md border border-primary/40 bg-primary/5 p-3 text-sm"
        >
          <ArrowRightLeftIcon class="mt-0.5 size-4 shrink-0 text-primary" />
          <p class="min-w-0 flex-1">
            From the board — post <span class="font-mono">{{ fromBoard.postId }}</span> with
            <span class="font-mono">{{ fromBoard.peer.slice(0, 8) }}…</span>. Nothing here is
            agreed yet: their signed offer arrives over the session in a moment and pins the terms.
          </p>
        </div>

        <!-- Paste an offer. It is not a third way of starting a swap — it is
             how the form finds out which side you are. -->
        <div class="grid gap-3">
          <div class="flex items-center gap-1.5 text-sm text-muted-foreground">
            Answering somebody? Paste their <code class="font-mono">swapoffer2:</code> string.
            <InfoTip label="What an offer string is">
              One line of public data — the two legs, the network, the secret hash, addresses — and
              never a secret or a key. Decoding it fills in your side of their trade and pins every
              term it carries.
            </InfoTip>
          </div>
          <Textarea
            v-model="offerBlob"
            rows="2"
            spellcheck="false"
            autocapitalize="none"
            placeholder="swapoffer2:…"
            class="font-mono text-xs"
          />
          <div class="flex flex-wrap gap-2">
            <Button variant="outline" size="sm" :disabled="!offerBlob.trim()" @click="decode">
              Decode and fill the form
            </Button>
            <Button v-if="decoded" variant="ghost" size="sm" @click="discardOffer">
              Discard their offer
            </Button>
            <Button
              v-if="decoded"
              variant="ghost"
              size="sm"
              @click="showRawOffer = !showRawOffer"
            >
              {{ showRawOffer ? 'Hide' : 'Show' }} what was in it
            </Button>
          </div>
          <Callout v-if="decodeError" tone="destructive">{{ decodeError }}</Callout>
          <pre
            v-if="decoded && showRawOffer"
            class="min-w-0 overflow-x-auto rounded-md bg-muted/60 p-3 font-mono text-xs"
            >{{ JSON.stringify(decoded.decoded, null, 2) }}</pre
          >
        </div>

        <NewSwapForm
          ref="formRef"
          :decoded="decoded"
          :locked="locked"
          :busy="creating"
          :error="createError"
          @submit="create"
          @cancel="((newOpen = false), (fromBoard = null))"
        />
      </CardContent>
    </Card>

    <BackupPanel />

    <!-- Accepting is what reveals the form; declining leaves it closed and
         nothing on the page changes. -->
    <TermsGate v-model:open="termsOpen" @accepted="onTermsAccepted" />
  </div>
</template>

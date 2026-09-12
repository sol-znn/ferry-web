<script setup lang="ts">
import {computed, ref} from 'vue'
import {
  ArchiveIcon,
  ArchiveRestoreIcon,
  ArrowRightIcon,
  ClockIcon,
  RefreshCwIcon,
} from '@lucide/vue'
import {Badge, Button, Card, CardContent, CardHeader, CopyButton, Switch} from 'nom-ui'
import type {BadgeVariants} from 'nom-ui'
import BtcLeg from './BtcLeg.vue'
import Callout from './Callout.vue'
import Handoff from './Handoff.vue'
import Note from './Note.vue'
import SessionSend from './SessionSend.vue'
import SolLeg from './SolLeg.vue'
import StepBar from './StepBar.vue'
import Waiting from './Waiting.vue'
import ZnnLeg from './ZnnLeg.vue'
import {api} from '@/core/api'
import {pairLabel} from '@/core/chains'
import {stamp} from '@/core/format'
import {STAGES, nextStep, stage, waitingOnThem} from '@/core/progress'
import type {HandoffType} from '@/core/handoffs'
import {useAutoMode} from '@/core/composables/useAutoMode'
import {useSession} from '@/core/composables/useSession'
import {useSettings} from '@/core/composables/useSettings'
import type {LegView, Swap} from '@/types'

/**
 * One swap, as a card.
 *
 * v1 was one component holding both legs and every branch of both, which worked
 * while there was exactly one shape of swap. This is the shell: the header, the
 * track, the session, the log and the filing — with the two legs rendered by
 * whichever panel their chain calls for.
 *
 * The division is not cosmetic. Everything specific to a chain is now in one
 * file per chain, on both sides of the trade at once, so "what does the funder
 * do on Solana" has one answer in one place rather than two halves of a branch
 * fifty lines apart.
 */
const props = defineProps<{swap: Swap}>()
const emit = defineEmits<{changed: []}>()

const {body: settings} = useSettings()
const session = useSession()
const autoMode = useAutoMode()

const error = ref('')
const busy = ref(false)
const showLog = ref(false)
const offer = ref('')

const legs = computed<LegView[]>(() =>
  [props.swap.out, props.swap.in].filter(Boolean) as LegView[],
)

function panelFor(leg: LegView) {
  return {btc: BtcLeg, znn: ZnnLeg, sol: SolLeg}[leg.chain]
}

const at = computed(() => stage(props.swap))
const step = computed(() => nextStep(props.swap))
const theirMove = computed(() => waitingOnThem(props.swap))
const live = computed(() => props.swap.active && !props.swap.settled)

const title = computed(() =>
  props.swap.out && props.swap.in
    ? pairLabel(props.swap.out.chain, props.swap.in.chain)
    : props.swap.pair,
)

/** A finished swap is not a failed one: "refunded" means the deadline did its
 *  job. Only a shortfall and a rejected leg are actually bad. */
const stateVariant = computed<BadgeVariants['variant']>(() => {
  if (props.swap.out?.btc?.fundingShort) return 'destructive'
  if (props.swap.in && props.swap.in.funded && !props.swap.in.verified) return 'destructive'
  if (props.swap.settled) return 'secondary'
  if (props.swap.state === 'expired') return 'warning'
  return 'default'
})

/**
 * A swap created on one network cannot be acted on while the app is pointed at
 * another: every address, fee lookup and broadcast would go to the wrong chain.
 * Every button that reaches a chain is disabled rather than merely accompanied
 * by a warning, because a warning next to a live Refresh button is one people
 * click past.
 */
const networkMismatch = computed(() => props.swap.network !== settings.value.network)
const chainBlocked = computed(() => busy.value || networkMismatch.value)

// A session can send a value this card is holding, but only for the swap it is
// attached to: publishing this swap's contract into a room that is about a
// different swap would hand the other side something they will rightly refuse.
const inSession = computed(() => session.active.value && session.swapId.value === props.swap.id)

function attachSession() {
  void session.attach(props.swap.id, props.swap.role)
}

/** Whether the session has already carried this exact value, so the button can
 *  offer a repeat rather than claim to be the first time. */
function sent(type: HandoffType, value: string | undefined): boolean {
  return session.wasSent(props.swap.id, type, value ?? '')
}

async function sendOverSession(type: HandoffType, describe: string) {
  await session.send({type}, describe)
}

/**
 * The two hand-offs a card still shows by hand.
 *
 * The session publishes each of them by itself the moment this browser comes to
 * hold it. What is left for the panel is what the automatic send cannot cover: a
 * relay that dropped the event, a counterparty who joined and missed it, or two
 * people with no session at all passing strings through a chat window.
 */
const handOffPkh = computed(
  () => props.swap.in?.chain === 'btc' && !props.swap.in.btc?.contractAddr,
)
const handOffContract = computed(() => Boolean(props.swap.out?.btc?.contractHex))

/** The hashlock is the one argument of a counterparty's contract call they
 *  cannot derive from anything else on screen. */
const showHashlock = computed(() => live.value && at.value > 0)

async function run(fn: () => Promise<unknown>) {
  error.value = ''
  busy.value = true
  try {
    await fn()
    emit('changed')
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

/**
 * A leg landed something on a chain. Tell the counterparty, without telling them
 * anything: the announcement says a chain moved, and their browser goes and
 * reads the chain. What it buys is the minute between an action and the other
 * side's next heartbeat, which is the largest source of dead time in a swap.
 */
async function moved(what: string) {
  emit('changed')
  if (inSession.value) await session.nudge(what)
}

async function makeOffer() {
  await run(async () => {
    offer.value = (await api.offer(props.swap.id)).offer
  })
}

// Autopilot for this swap: armed by the user, stopped by the first thing that
// goes wrong. See core/composables/useAutoMode.ts for what it will and will not
// do; this is the half of it that lives where the buttons are.
const autoOn = computed(() => autoMode.isOn(props.swap.id))
const autoHalted = computed(() => autoMode.reason(props.swap.id))
</script>

<template>
  <Card>
    <CardHeader class="grid gap-2">
      <div class="flex flex-wrap items-center gap-2">
        <h2 class="text-base font-semibold">{{ title }}</h2>
        <Badge :variant="stateVariant">{{ swap.state.replace('_', ' ') }}</Badge>
        <Badge variant="outline">{{ swap.role }}</Badge>
        <Badge v-if="swap.network !== 'mainnet'" variant="outline">{{ swap.network }}</Badge>
        <span class="ml-auto text-xs text-muted-foreground">{{ stamp(swap.createdAt) }}</span>
      </div>

      <p class="flex flex-wrap items-center gap-2 font-mono text-sm">
        <span>{{ swap.out?.label }}</span>
        <ArrowRightIcon class="size-3.5 text-muted-foreground" />
        <span>{{ swap.in?.label }}</span>
      </p>

      <StepBar :steps="STAGES" :current="at" :done="swap.settled" />

      <p class="flex items-center gap-1.5 text-sm" :class="theirMove ? 'text-muted-foreground' : ''">
        <Waiting v-if="theirMove" />
        <ClockIcon v-else class="size-3.5 text-primary" />
        {{ step }}
      </p>
    </CardHeader>

    <CardContent class="grid gap-4">
      <Callout v-if="networkMismatch" tone="warning">
        This swap is on <span class="font-mono">{{ swap.network }}</span> and the app is set to
        <span class="font-mono">{{ settings.network }}</span
        >. Everything that reaches a chain is switched off until they agree.
      </Callout>

      <Callout v-if="error" tone="destructive">{{ error }}</Callout>

      <!-- The legs. Each panel owns everything about its own chain, on both
           sides of the trade; this decides only which two to render. -->
      <component
        :is="panelFor(leg)"
        v-for="leg in legs"
        :key="leg.dir"
        :swap="swap"
        :leg="leg"
        :blocked="chainBlocked"
        @changed="emit('changed')"
        @moved="moved"
        @error="(m: string) => (error = m)"
      />

      <!-- Hand-offs. Each is shown for exactly as long as it is the outstanding
           move, and each carries public data only. -->
      <Handoff
        v-if="handOffPkh && swap.in?.btc?.key?.pkhHex"
        title="Send them your pubkey hash"
        hint="They put it in the redeem branch of the contract they build and fund."
        :value="swap.in.btc.key.pkhHex"
      >
        <SessionSend
          :in-session="inSession"
          :session-active="session.active.value"
          :sent="sent('pkh', swap.in.btc.key.pkhHex)"
          label="Send it over the session"
          @send="sendOverSession('pkh', 'Sent your pubkey hash')"
          @attach="attachSession"
        />
      </Handoff>

      <Handoff
        v-if="handOffContract && swap.out?.btc?.contractHex"
        title="Send them the contract"
        hint="They audit it before committing anything on their side — offline, against their own record of this swap."
        :value="swap.out.btc.contractHex"
      >
        <SessionSend
          :in-session="inSession"
          :session-active="session.active.value"
          :sent="sent('contract', swap.out.btc.contractHex)"
          label="Send it over the session"
          @send="sendOverSession('contract', 'Sent the contract')"
          @attach="attachSession"
        />
      </Handoff>

      <Handoff
        v-if="showHashlock"
        title="The hashlock"
        hint="Both legs commit to this one hash. It is the one argument of a contract call that cannot be derived from anything else on screen."
        :value="swap.secretHashHex"
      />

      <!-- The offer string, for two people with no session. It carries public
           data only: there is no field for the secret and no path that puts one
           in it. -->
      <div class="grid gap-2">
        <div class="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" :disabled="busy" @click="makeOffer">
            Make an offer string
          </Button>
          <CopyButton v-if="offer" :value="offer" label="offer" />
        </div>
        <pre
          v-if="offer"
          class="overflow-x-auto rounded-md bg-muted/40 p-2 font-mono text-xs break-all"
          >{{ offer }}</pre
        >
      </div>

      <!-- Autopilot. Armed per swap and never globally, because approval is
           about this trade rather than about trading.

           It is currently a watcher and not a driver. `ZenonWallet` carries a
           complete auto path -- claim the step once, build the block, stop at
           the wallet's window, halt the swap on the first failure -- behind an
           `auto` prop that nothing passes, and the Bitcoin and Solana panels
           have no such path at all. Wiring it is `:auto="autoOn"` down to the
           panels and on to the action wallet but NOT the reclaim one, which is
           where "never refunds" would stop being a promise and start being a
           missing binding. Left undone rather than half-done: on one chain of
           three it would be harder to reason about than none. -->
      <div class="grid gap-1.5 rounded-lg border border-border p-3">
        <label class="flex items-center gap-2 text-sm">
          <Switch
            :model-value="autoMode.isArmed(swap.id)"
            @update:model-value="(v: boolean) => autoMode.set(swap.id, v)"
          />
          Auto Mode
        </label>
        <p class="text-xs text-muted-foreground">
          Keeps this swap reading the chains while its tab is in the background, so a leg that
          appears while you are not looking is noticed. It never signs, never skips a check, and
          never refunds.
        </p>
        <p class="text-xs text-warning">
          It does not yet press anything for you: every step is still your click. The description
          used to promise otherwise — see HANDOFF.md.
        </p>
        <Callout v-if="autoHalted" tone="warning">
          Autopilot stopped: {{ autoHalted }}
        </Callout>
        <Callout v-else-if="autoOn" tone="info">Armed — watching this swap.</Callout>
      </div>

      <div class="flex flex-wrap items-center gap-2">
        <Button
          variant="outline"
          size="sm"
          :disabled="chainBlocked"
          @click="run(() => api.refresh(swap.id, settings))"
        >
          <RefreshCwIcon />
          Refresh from chain
        </Button>
        <Button
          variant="ghost"
          size="sm"
          :disabled="busy"
          @click="run(() => api.archive(swap.id, !swap.archived))"
        >
          <component :is="swap.archived ? ArchiveRestoreIcon : ArchiveIcon" />
          {{ swap.archived ? 'Un-archive' : 'Archive' }}
        </Button>
        <button
          type="button"
          class="ml-auto text-xs text-muted-foreground underline-offset-4 hover:underline"
          @click="showLog = !showLog"
        >
          {{ showLog ? 'Hide' : 'Show' }} log ({{ swap.events.length }})
        </button>
      </div>

      <Note v-if="showLog" summary="Everything this browser observed, in order">
        <ol class="grid gap-1.5">
          <li v-for="(ev, i) in [...swap.events].reverse()" :key="i" class="text-xs">
            <span class="text-muted-foreground">{{ stamp(ev.at) }}</span>
            — {{ ev.message }}
          </li>
        </ol>
      </Note>
    </CardContent>
  </Card>
</template>

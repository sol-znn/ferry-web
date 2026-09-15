<script setup lang="ts">
import {computed, ref, watch} from 'vue'
import {
  AlertTriangleIcon,
  CircleCheckIcon,
  LinkIcon,
  RotateCwIcon,
  UnlinkIcon,
  WalletIcon,
} from '@lucide/vue'
import {Button, CopyButton} from 'nom-ui'
import InfoTip from './InfoTip.vue'
import {api} from '@/core/api'
import {useSettings} from '@/core/composables/useSettings'
import {useAutoMode} from '@/core/composables/useAutoMode'
import {useZenonWallet} from '@/core/composables/useZenonWallet'
import type {Swap, WalletAction, WalletBlockPlan, WalletSync} from '@/types'

/**
 * The Zenon leg, driven by the Syrius extension instead of by a terminal.
 *
 * The same operation the printed commands describe, sitting beside them rather
 * than replacing them. What the button removes is the copy-paste, which is where
 * the hashlock and the expiry get mistyped.
 *
 * Two steps with a look in between: Go builds the block and says in a sentence
 * what it does, with nothing signed and nothing sent; then the user presses
 * Sign, and the extension opens its own window, shows the raw block, signs with
 * its own key and publishes through its own node.
 *
 * Ferry never sees a Zenon private key. What it produced was bytes; every step
 * that could move a coin happened inside the wallet.
 */
const props = defineProps<{
  swap: Swap
  /** Which call this card is offering. Decided by the card, checked by Go. */
  action: WalletAction
  disabled?: boolean
  /**
   * Auto Mode is armed for this swap, so get as far as the wallet without being
   * asked. See useAutoMode: this builds the block and hands it over, and stops
   * dead at the wallet's own window, which is the one thing it must not pass.
   */
  auto?: boolean
}>()

const emit = defineEmits<{(e: 'done', swap: Swap): void}>()

const {body: settings} = useSettings()
const autoMode = useAutoMode()
const {
  available: walletAvailable,
  address: walletAddress,
  chainId: walletChainId,
  connected: walletConnected,
  connecting: walletConnecting,
  sending: walletSending,
  epoch: walletEpoch,
  identity: walletIdentity,
  connect: walletConnect,
  forget: walletForget,
  send: walletSend,
} = useZenonWallet()

const plan = ref<WalletBlockPlan | null>(null)
const sync = ref<WalletSync | null>(null)
const error = ref('')
const notice = ref('')
const preparing = ref(false)
const checking = ref(false)

/**
 * The wallet state the plan on screen was built against. A plan is a proposal
 * about one account on one chain; if the wallet moves, the plan describes a
 * wallet that no longer exists, and signing it would sign something other than
 * what was read and approved here. So it is discarded rather than re-pointed.
 */
const planEpoch = ref(-1)

watch(walletEpoch, () => {
  if (plan.value) {
    plan.value = null
    error.value =
      'The wallet changed while this was waiting to be signed — a different account, node or ' +
      'chain than the block was built for. It has been discarded; check it again.'
  }
  sync.value = null
})

// The swap moving is the other way a prepared block goes stale: an expiry
// computed against one Bitcoin locktime is wrong once that locktime changes.
//
// Three separate getters rather than one returning an array: a getter that
// builds an array hands the watcher a new array every run, and a watcher
// compares by identity, so it fired on every reload of the swap list. That was
// invisible while a reload only followed something the user did, and became a
// prepared block vanishing mid-read once the list started refreshing itself.
watch(
  [
    () => props.swap.id,
    () => props.swap.lockTime,
    () => props.swap.zenon?.htlcId,
    // The Bitcoin funding this create answers moving -- reorganised out,
    // replaced, spent -- is the third way. The block is not wrong, but the
    // reason to sign it has gone.
    () => props.swap.fundingCommitted,
  ],
  () => {
    plan.value = null
    sync.value = null
  },
)

const label = computed(
  () =>
    ({
      create: 'Create the HTLC',
      unlock: 'Unlock the HTLC',
      reclaim: 'Reclaim your ZNN',
    })[props.action],
)

const busy = computed(
  () => preparing.value || checking.value || walletSending.value || Boolean(props.disabled),
)

/**
 * Check the wallet against this page, and refuse rather than warn. `ok` is false
 * both for a proven mismatch and for a check that could not run, and both stop
 * here: an unreachable wallet node is exactly what a wallet pointed at somebody
 * else's chain looks like from this side.
 */
async function checkSync(): Promise<WalletSync> {
  checking.value = true
  try {
    const result = await api.walletSync(walletIdentity(), settings.value, {
      id: props.swap.id,
      action: props.action,
    })
    sync.value = result
    if (!result.ok) {
      throw new Error(
        [...(result.problems ?? []), ...(result.unchecked ?? [])].join(' — ') ||
          'The wallet could not be checked against this page.',
      )
    }
    return result
  } finally {
    checking.value = false
  }
}

async function prepare() {
  error.value = ''
  notice.value = ''
  plan.value = null
  preparing.value = true
  try {
    if (!walletConnected.value) await walletConnect()
    await checkSync()
    plan.value = await api.walletBlock(
      props.swap.id,
      props.action,
      walletIdentity(),
      settings.value,
    )
    planEpoch.value = walletEpoch.value
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    preparing.value = false
  }
}

/**
 * Do the failed step again, whichever one it was.
 *
 * Everything the wallet can refuse leaves this card in one of two recoverable
 * states: a block was built and not signed, so sign it again; or there is no
 * block, so build one. Neither has moved anything -- the whole point of the gate
 * is that a refusal happens before the send -- so retrying is safe in a way that
 * pressing create twice after a successful publish is emphatically not.
 *
 * The failures here are overwhelmingly transient and not the user's doing: a
 * wallet that locked itself while the block was being read, a window closed by
 * accident, a node that blinked. Making somebody work out which button repeats
 * the bit that broke is how a recoverable moment turns into an abandoned swap.
 */
async function retry() {
  error.value = ''
  notice.value = ''
  if (plan.value) await sign()
  else await prepare()
}

async function sign() {
  const current = plan.value
  if (!current) return
  error.value = ''
  notice.value = ''
  try {
    // Checked again, immediately before handing the block over. The wallet may
    // have been switched in the time it took to read the summary, and the
    // extension announces that only sometimes — so the state is re-read rather
    // than assumed to be the one prepare() saw.
    await checkSync()
    if (walletEpoch.value !== planEpoch.value || walletAddress.value !== current.signer) {
      plan.value = null
      throw new Error(
        `This block was built for ${current.signer} and the wallet now has ` +
          `${walletAddress.value || 'no account'} selected. Nothing was sent — check it again.`,
      )
    }

    // For a create that answers the counterparty's Bitcoin funding, the gate
    // runs one more time, now. The engine re-read the chain when it BUILT the
    // block; a person may have spent minutes reading the summary since, and a
    // funding that was mined then can have been spent or reorganised out. This
    // is the same fail-closed check as at plan time -- exact outpoint, full
    // value, mined, deep enough, and a chain that cannot be read is a refusal
    // -- not a Refresh, which keeps what it last knew when a read fails.
    if (props.action === 'create' && props.swap.btcLegIsInitiators) {
      try {
        await api.fundingCheck(props.swap.id, settings.value)
      } catch (err) {
        plan.value = null
        const why = err instanceof Error ? err.message : String(err)
        throw new Error(
          `Not sent: ${why} The block has been discarded; it is rebuilt when their funding ` +
            `is settled again.`,
        )
      }
    }

    const sent = await walletSend(current.block)
    // What comes back next is checked, not believed — twice over. The block as
    // published is diffed against the block as proposed, which is the only way
    // to catch a wallet that signed with an account other than the one it
    // reported; and for a create the hash IS the HTLC id, so it goes through
    // the same verification a counterparty's id goes through.
    const result = await api.walletSent(
      props.swap.id,
      props.action,
      sent.hash,
      walletAddress.value,
      settings.value,
      {proposed: current.block, signed: sent.signed},
    )
    plan.value = null
    emit('done', result.swap)
    if (result.error) {
      // Almost always "not on the chain yet": an account block takes a momentum
      // or two to appear, and the wallet answers as soon as it has published.
      // Nothing has to be done about that — the page asks again on its own until
      // a node answers — so this says what is happening rather than handing back
      // an instruction the reader would have to remember to carry out.
      notice.value = result.pending
        ? `Published as ${sent.hash}. This browser has not been able to check it against a ` +
          `node yet; it asks again every minute until it can. (${result.error})`
        : result.error
    } else {
      notice.value = `Published as ${sent.hash}.`
    }
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  }
}

/**
 * Autopilot: build the block and hand it to the wallet, without being asked.
 *
 * It stops where it has to. `sign()` puts the block in front of Syrius, which
 * opens its own window and waits for a person. The point is to remove the two
 * clicks BEFORE the wallet, not the decision inside it.
 *
 * Claimed once per swap per action, and a failure halts autopilot for this swap
 * rather than being retried. This is the path that opens wallet windows and
 * publishes blocks that move coins, so a driver re-running it on every reactive
 * tick would open a wallet window on every tick -- or lock a second lot of ZNN
 * because the first create was not readable yet.
 */
async function autoRun() {
  if (!props.auto || busy.value || walletSending.value || plan.value) return
  // Connecting is left to the user, and deliberately. Syrius approves a site in
  // a window of its own, and a page that asks for that on load — before anybody
  // has touched it, possibly for several swaps at once — is a page that gets
  // dismissed. Extensions tend to want a real gesture for it anyway. So Auto
  // Mode drives the steps and the one connection stays a button; the card goes
  // on offering it, and this picks up the moment it is done.
  if (!walletConnected.value) return
  if (!autoMode.claim(props.swap.id, `zenon:${props.action}`)) return
  await prepare()
  if (error.value || !plan.value) {
    autoMode.halt(props.swap.id, error.value || `could not build the ${props.action} block`)
    return
  }
  await sign()
  if (error.value) autoMode.halt(props.swap.id, error.value)
}

// Driven by the props rather than called from the parent, because the moment
// this becomes possible is not a moment the parent does anything: it is a swap
// arriving from a reload with a new field on it, or a wallet finally being
// connected. `immediate` covers the commonest case of all, which is a page
// opened on a swap whose next step was already outstanding.
watch(
  [() => props.auto, () => props.action, () => props.swap.id, busy, walletConnected],
  () => void autoRun(),
  {immediate: true},
)
</script>

<template>
  <div class="grid gap-2 rounded-md border border-border bg-muted/30 p-3">
    <div class="flex flex-wrap items-center gap-2">
      <template v-if="!walletConnected">
        <Button variant="outline" size="sm" :disabled="busy" @click="prepare">
          <LinkIcon />
          {{ walletConnecting ? 'Waiting for Syrius…' : `Connect Syrius and ${label}` }}
        </Button>
        <!-- What the wait is actually waiting on. Syrius approves in a window
             of its own, so a page that says nothing here leaves somebody
             watching a spinner while the thing they have to answer is behind
             the browser. -->
        <span v-if="walletConnecting" class="text-xs text-muted-foreground">
          Syrius should open its own window — approve this site there. If nothing appears within 30
          seconds this gives up rather than leaving you waiting.
        </span>
        <span
          v-if="!walletAvailable"
          class="flex items-center gap-1.5 text-xs text-muted-foreground"
        >
          <WalletIcon class="size-3.5" />
          No Syrius extension on this page
          <InfoTip label="What the extension is asked to do">
            <p>
              The Syrius browser extension will sign and publish an account block a page hands it.
              An HTLC call is an ordinary send to an embedded contract carrying encoded arguments,
              so this page can build one and let your wallet do the signing.
            </p>
            <p>
              Your Zenon key never comes near this page. It builds bytes; the extension shows you
              the whole block, signs it with its own key, mines its own plasma and publishes through
              its own node.
            </p>
            <p>
              The wallet announces itself as the page loads, so a wallet installed or enabled since
              this page opened will not appear until you reload.
            </p>
          </InfoTip>
        </span>
      </template>

      <template v-else>
        <span
          class="flex items-center gap-1.5 rounded-md border border-border bg-background px-2 py-1 font-mono text-xs"
        >
          <WalletIcon class="size-3.5 text-muted-foreground" />
          {{ walletAddress }}
          <span v-if="walletChainId" class="text-muted-foreground">
            · chain {{ walletChainId }}
          </span>
        </span>
        <Button v-if="!plan" size="sm" :disabled="busy" @click="prepare">
          {{ checking ? 'Checking the wallet…' : preparing ? 'Building the block…' : label }}
        </Button>
        <Button variant="ghost" size="sm" :disabled="busy" @click="walletForget()">
          <UnlinkIcon />
          Forget
        </Button>
      </template>
    </div>

    <!-- What was actually established about the wallet, as opposed to what it
         claimed. `sameChain` is the only line here that is proof of anything:
         it means a momentum hash was read from both nodes and matched. Equal
         chain identifiers are not proof — every go-zenon devnet is chain 69 —
         so the two are shown as separate facts rather than one tick. -->
    <div
      v-if="sync"
      class="grid gap-1 rounded-md border p-2.5 text-xs"
      :class="
        sync.ok
          ? 'border-success/40 bg-success/5 text-success'
          : 'border-destructive/50 bg-destructive/5 text-destructive'
      "
    >
      <span class="flex items-center gap-1.5 font-semibold">
        <component :is="sync.ok ? CircleCheckIcon : AlertTriangleIcon" class="size-3.5" />
        <template v-if="sync.ok">
          Same chain as this page
          <span class="font-normal">
            — chain {{ sync.chainIdentifier }}, momentum {{ sync.anchorHeight }} matches on both
            nodes
          </span>
        </template>
        <template v-else>The wallet does not agree with this page</template>
      </span>
      <ul v-if="sync.problems?.length || sync.unchecked?.length" class="grid gap-1">
        <li v-for="p in sync.problems ?? []" :key="p">{{ p }}</li>
        <li v-for="u in sync.unchecked ?? []" :key="u">{{ u }}</li>
      </ul>
      <p v-if="sync.ok && !sync.sameChain" class="text-warning">
        The chains could not be compared momentum for momentum, only by identifier.
      </p>
    </div>

    <!-- What is about to be signed, in words, before the extension shows it in
         hex. The wallet's own window is the authoritative view; this is the one
         that can be read. -->
    <div v-if="plan" class="grid gap-2 rounded-md border border-primary/40 bg-primary/5 p-2.5">
      <p class="font-mono text-xs text-muted-foreground">Will be signed by {{ plan.signer }}</p>
      <p class="text-sm">{{ plan.summary }}</p>
      <ul v-if="plan.warnings?.length" class="grid gap-1 text-xs text-warning">
        <li v-for="w in plan.warnings" :key="w" class="flex items-start gap-1.5">
          <AlertTriangleIcon class="mt-0.5 size-3.5 shrink-0" />
          <span class="min-w-0 flex-1">{{ w }}</span>
        </li>
      </ul>
      <details class="text-xs">
        <summary class="cursor-pointer text-muted-foreground">
          The block, exactly as the wallet will receive it
        </summary>
        <div class="relative mt-1.5 w-full min-w-0">
          <pre class="max-w-full overflow-x-auto rounded bg-muted/60 p-2 font-mono text-xs">{{
            JSON.stringify(plan.block, undefined, 2)
          }}</pre>
          <CopyButton
            :value="JSON.stringify(plan.block, undefined, 2)"
            size="icon-sm"
            class="absolute top-1 right-1"
          />
        </div>
      </details>
      <div class="flex flex-wrap gap-2">
        <Button size="sm" :disabled="walletSending" @click="sign">
          {{ walletSending ? 'Waiting for the wallet…' : 'Sign and publish in Syrius' }}
        </Button>
        <Button variant="ghost" size="sm" :disabled="walletSending" @click="plan = null">
          Cancel
        </Button>
      </div>
      <p v-if="walletSending" class="text-xs text-muted-foreground">
        Syrius has opened its own window. Unlock the wallet, read the block and approve it there —
        it may have to mine plasma first, which takes a while on an unfused account.
      </p>
    </div>

    <p v-if="notice" class="text-xs break-all text-success">{{ notice }}</p>

    <!-- A refusal happens before anything is sent, so the way out of one is to
         do it again. The button says which step it will repeat, because "try
         again" on its own leaves the user wondering whether it is about to
         re-sign something that already went through. -->
    <div v-if="error" class="grid gap-2">
      <!-- Not shown when the sync box above already says this: checkSync()
           throws the exact sentence it just put in sync.problems, and a
           mismatch failing that check has nothing else it could be, so the
           box and this paragraph would otherwise repeat the same sentence
           twice on one card. The retry button still belongs here regardless
           of which one explains the failure. -->
      <p v-if="!sync || sync.ok" class="text-xs text-destructive">{{ error }}</p>
      <div>
        <Button variant="outline" size="sm" :disabled="busy || walletSending" @click="retry">
          <RotateCwIcon />
          {{ plan ? 'Try signing again' : `Try again — ${label.toLowerCase()}` }}
        </Button>
      </div>
    </div>
  </div>
</template>

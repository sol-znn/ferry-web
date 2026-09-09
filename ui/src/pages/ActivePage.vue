<script setup lang="ts">
import {computed, onMounted, ref, watch} from 'vue'
import {
  ArrowRightLeftIcon,
  ClipboardCheckIcon,
  LockIcon,
  PlusIcon,
  RefreshCwIcon,
  ShieldCheckIcon,
  TriangleAlertIcon,
  XIcon,
} from '@lucide/vue'
import {
  Button,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  Heading,
  Input,
  Label,
  Tabs,
  TabsList,
  TabsTrigger,
  Textarea,
} from 'nom-ui'
import InfoTip from '@/components/InfoTip.vue'
import UnlockCost from '@/components/UnlockCost.vue'
import WalletConnect from '@/components/WalletConnect.vue'
import SwapCard from '@/components/SwapCard.vue'
import HistoryRow from '@/components/HistoryRow.vue'
import SessionPanel from '@/components/SessionPanel.vue'
import TermsGate from '@/components/TermsGate.vue'
import BackupPanel from '@/components/BackupPanel.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingState from '@/components/LoadingState.vue'
import {api} from '@/core/api'
import {ZNN_ZTS, btc} from '@/core/format'
import {useFerry} from '@/core/composables/useFerry'
import {useSettings} from '@/core/composables/useSettings'
import {useUnlockCost} from '@/core/composables/useUnlockCost'
import {useSession, type PendingTrade} from '@/core/composables/useSession'
import {useUnisat} from '@/core/composables/useUnisat'
import {useZenonWallet} from '@/core/composables/useZenonWallet'
import type {DecodedOffer, Leg, Role} from '@/types'

const {swaps, listError, loading, reload} = useFerry()
const {body: settings} = useSettings()
const session = useSession()
const unisat = useUnisat()
const zenon = useZenonWallet()

// An offer that arrived over a session is exactly the offer somebody would have
// pasted, so it goes in the same box and through the same decoder. Filling the
// field rather than applying it is deliberate: the user still presses the
// button that reads it, and can see what they are agreeing to first.
watch(session.pendingOffer, (offer) => {
  if (!offer) return
  offerBlob.value = offer
  tab.value = 'offer'
  // Gated like every other way this panel opens — see openNewSwapForm. An
  // offer arriving is the clearest case of "skipped the button": nobody
  // pressed anything, and the form is about to appear regardless.
  openNewSwapForm()
})

/**
 * A trade agreed on the board, arriving with the user who was redirected here.
 *
 * This fills the form in with what the two of them just settled on — which way
 * round, how much of each — so the page opens on the trade rather than on a
 * blank slate they would have to retype from memory.
 *
 * It is a head start and never an authority, and the distinction is load-bearing
 * for the same reason `applyOffer` exists: nothing on a board has been signed by
 * a counterparty, so none of it may pin a swap term. `prefilled` and `decoded`
 * are deliberately left alone, so the fields stay editable and the real
 * `swapoffer1` — which arrives over the session moments later and IS signed —
 * overwrites these values and locks them through the ordinary path. Go still
 * refuses any create that drifts from that offer.
 *
 * What it does not fill in is the pair only this user can supply: the Bitcoin
 * payout address and their own Zenon address. Those come from the wallets below
 * when either is connected, and are the one thing left to do on arrival.
 */
function applyPendingTrade(trade: PendingTrade) {
  form.value.leg = trade.leg
  form.value.role = trade.role
  // Through the tab as well, so the panel showing agrees with the side it just
  // filled in — the same reason applyOffer sets both.
  tab.value = trade.role === 'participant' ? 'join' : 'start'
  form.value.amountSats = String(trade.amountSats)
  form.value.zenonAmount = trade.zenonAmount
  form.value.zenonToken = trade.zenonToken
  form.value.lockHours = trade.lockHours ? String(trade.lockHours) : ''
  form.value.zenonPeerAddress = trade.zenonPeerAddress
  fromBoard.value = {postId: trade.postId, peer: trade.peer}
  // Consumed rather than left standing: it has been applied, and a later visit
  // to this page should not refill a form from a trade already made.
  session.pendingTrade.value = null
  fillOwnAddresses()
  // Gated like every other way this panel opens — see openNewSwapForm.
  openNewSwapForm()
}

// Set while this page was already open — which a trade agreed on the board is
// not. Taking or accepting happens on /board and sets it before redirecting
// here, so the value is standing by the time this component exists and the
// watcher alone never fires; the redirected user landed on a blank form. The
// arrival case is handled at mount, below.
watch(session.pendingTrade, (trade) => {
  if (trade) applyPendingTrade(trade)
})

/** Which board post this form came from, for the banner. Cleared when the swap
 *  is created or the form is abandoned. */
const fromBoard = ref<{postId: string; peer: string} | null>(null)

/**
 * Fill in the two addresses that are this user's own, from the wallets they
 * have already connected.
 *
 * Only into empty fields. Somebody who typed an address meant it, and a board
 * trade arriving is not a reason to overwrite it with whichever account a wallet
 * happens to have selected.
 */
function fillOwnAddresses() {
  if (!form.value.destAddr.trim() && unisat.connected.value && unisat.address.value) {
    form.value.destAddr = unisat.address.value
  }
  if (!form.value.zenonSelfAddress.trim() && zenon.connected.value && zenon.address.value) {
    form.value.zenonSelfAddress = zenon.address.value
  }
}

/**
 * Their Zenon address, arriving over the session rather than with the trade.
 *
 * A board accept sends one, so the side that was waiting on the board gets the
 * field filled without asking. Into an empty field only, and never over a term
 * an offer has pinned — `lockedPeerZenon` is what a signed `swapoffer1` sets,
 * and a message with no signature behind it may not move a value that has one.
 *
 * Their Bitcoin address is carried in the message and deliberately not applied
 * anywhere: the Bitcoin side of a swap is identified by a pubkey hash, which is
 * what the contract actually commits to, and filling an address field from it
 * would be inventing a second, weaker way to say the same thing.
 */
watch(session.peerAddresses, (addrs) => {
  if (!addrs?.znn) return
  if (lockedPeerZenon.value) return
  if (!form.value.zenonPeerAddress.trim()) form.value.zenonPeerAddress = addrs.znn
})

// A session message that changed a swap changed it in storage, not here.
watch(session.changed, () => void reload('active'))

const createError = ref('')
const creating = ref(false)
const form = ref({
  leg: 'send' as Leg,
  role: 'initiator' as Role,
  amountSats: '100000',
  lockHours: '',
  destAddr: '',
  secretHashHex: '',
  counterpartyPkhHex: '',
  zenonSelfAddress: '',
  zenonPeerAddress: '',
  zenonToken: '',
  zenonAmount: '',
})

// The new-swap panel is a panel rather than the top of the page. A returning
// user came to see what is in flight; a form they filled in yesterday sitting
// above it is furniture.
//
// It does NOT open itself, even with nothing in flight -- that was tried, and it
// meant a brand-new visitor's first sight was a blocking terms dialog they had
// done nothing to earn. See openNewSwapForm.
const newOpen = ref(false)

/**
 * Which of the three ways in is showing.
 *
 * `start` and `join` are not two views of one form -- they are the two sides of
 * the trade, and the tab is where that is now chosen. It used to be a dropdown
 * inside the form ("Who starts this swap"), which put the single most consequential
 * choice on the page three fieldsets below the two buttons that look like they
 * make it. `offer` is neither side; it is the paste box that works out which
 * side you are and comes back here.
 */
const tab = ref<'start' | 'join' | 'offer'>('start')

/** The two role tabs share one form; only the paste box is a different view. */
const showForm = computed(() => tab.value !== 'offer')

const ROLE_BY_TAB = {start: 'initiator', join: 'participant'} as const

const offerBlob = ref('')
const decoded = ref<DecodedOffer | null>(null)
/**
 * The exact string `decoded` came from, which is not always what the textarea
 * holds now: editing the box after decoding leaves the two disagreeing, and
 * sending the live text to the create check would have Go comparing the form
 * against an offer the form was never filled in from.
 */
const decodedFrom = ref('')
const decodeError = ref('')
const showRawOffer = ref(false)
const showAdvanced = ref(false)

/** Set when the form was filled in from a decoded offer, so the banner can say
 *  so and the user knows which fields are still theirs to supply. */
const prefilled = ref(false)

/**
 * The terms an offer pins stop being this side's to type.
 *
 * A swap is two records that have to say the same thing, and an offer is how
 * the second one gets filled in from the first — after which every value it
 * filled in stayed editable. That made the cheapest way to break a swap a stray
 * keystroke in a number both sides had already agreed to, found (if at all) at
 * whichever chain-touching step first depended on the field that moved, which
 * is always after somebody has committed money.
 *
 * So the received terms are locked rather than watched, and what is left
 * editable is exactly what the offer could not contain: this user's own payout
 * address, their own Zenon address, and a timelock the offer does not pin. The
 * job in front of somebody holding an offer is to CHECK it, not to fill it in.
 *
 * Read-only rather than disabled, deliberately: a locked value still has to be
 * selectable, because comparing it against what they were told somewhere else
 * is the whole of that job.
 *
 * This is the second line and not the line. Go refuses a create whose terms
 * drifted from the offer it claims to answer (Offer.CheckCreate), whatever the
 * form did — that is the guarantee, and this is what stops anyone reaching it.
 */
const lockedByOffer = computed(() => prefilled.value && Boolean(decoded.value))

/**
 * The two terms pinned only when the offer actually carried them, mirroring
 * what Go compares: an offer with no Zenon address or no pubkey hash leaves
 * that value to be supplied later, over a session or by hand, and locking an
 * empty field would take away the only place there is to put it.
 */
const lockedPeerZenon = computed(
  () => lockedByOffer.value && Boolean(decoded.value?.decoded.zenonAddr),
)
const lockedPeerPkh = computed(() => lockedByOffer.value && Boolean(decoded.value?.decoded.pkh))
/** Pinned for the participant, who was handed the hash. An initiator invents
 *  their own and is not held to one that turns up in somebody else's offer. */
const lockedSecretHash = computed(
  () => lockedByOffer.value && Boolean(decoded.value?.decoded.secretHash),
)

/** What a term that is theirs rather than yours looks like: still legible,
 *  still selectable, not typeable. */
const LOCKED_FIELD = 'bg-muted/60 text-muted-foreground focus-visible:ring-0'

// The tab is how a user chooses a role -- keeping `form.role` as the value the
// create call sends rather than deriving it from the tab means the drift check
// below still compares one field against one field. The other writer is
// applyOffer, which sets both together.
//
// `offer` is skipped rather than mapped: looking at what somebody sent you is
// not a claim about which side you are, and mapping it would quietly make every
// visit to the paste box an initiator again -- inventing a role drift against
// the very offer being read.
//
// Locked is skipped because an offer STATES which side you are; the tabs are
// then a view of that rather than a way to change it. Guarded here as well as
// on the triggers themselves, because a tab strip also moves under the arrow
// keys.
watch(tab, (t) => {
  if (t !== 'offer' && !lockedByOffer.value) form.value.role = ROLE_BY_TAB[t]
})

onMounted(async () => {
  // Before the reload, and not inside it: this is the trade the user agreed a
  // moment ago on the board, and it should be on screen when the page paints
  // rather than after a round trip. It cannot run any earlier than mount — the
  // refs it writes are still in their temporal dead zone where the watcher above
  // is declared.
  const trade = session.pendingTrade.value
  if (trade) applyPendingTrade(trade)
  await reload('active')
})

// The two sides of the trade, in whichever order this direction makes them.
// "Amount (sats)" and "Zenon amount" as two unrelated fields is the same trade
// described twice; labelling them send/receive is describing it once.
const sending = computed(() => (form.value.leg === 'send' ? 'btc' : 'znn'))

const LEGS: {value: Leg; title: string; sub: string}[] = [
  {value: 'send', title: 'Send BTC', sub: 'You pay Bitcoin and receive ZNN'},
  {value: 'receive', title: 'Receive BTC', sub: 'You pay ZNN and receive Bitcoin'},
]

const amountBtc = computed(() => {
  const n = Number(form.value.amountSats)
  return Number.isFinite(n) && n > 0 ? btc(n) : ''
})

// What getting back out of this contract will cost, while the amount is still a
// number in a field rather than money in a contract.
//
// The destination is only passed on the leg where this user is the one
// unlocking. On the other leg the redeem pays the counterparty at an address
// nobody here has seen, so quoting it against this user's address would size the
// wrong output -- Go assumes the widest common form instead and says that it did.
const unlockInput = computed(() => {
  const amountSats = Number(form.value.amountSats)
  if (!Number.isFinite(amountSats) || amountSats <= 0) return null
  return {
    amountSats,
    destAddr: form.value.leg === 'receive' ? form.value.destAddr.trim() : '',
  }
})
const {
  cost: unlockCost,
  loading: unlockLoading,
  error: unlockError,
} = useUnlockCost(unlockInput, settings)

// Who pays the unlock fee out of the contract: whoever receives the Bitcoin.
const unlockedBy = computed<'you' | 'them'>(() => (form.value.leg === 'receive' ? 'you' : 'them'))

function useRecommendedAmount(amount: number) {
  form.value.amountSats = String(amount)
}

// A participant was handed a hash; an initiator makes one. Showing an initiator
// a "secret hash" field they must leave blank is the single most reliable way
// to get one filled in wrongly.
const isParticipant = computed(() => form.value.role === 'participant')

const offerNetworkClash = computed(
  () => Boolean(decoded.value) && decoded.value?.decoded.network !== settings.value.network,
)

/**
 * Which terms the form now disagrees with the offer it was filled in from.
 *
 * Both browsers build their own contract out of their own record and nothing on
 * either chain compares the two, so an edited term is not a form error -- it is
 * two people running different swaps, discovered at whichever chain-touching
 * step first depends on the field that moved. That is always after money is
 * committed, and it usually reads as the counterparty cheating.
 *
 * The refusal lives in Go (Offer.CheckCreate), where it cannot be walked around;
 * this is the same comparison run on every keystroke, so the answer arrives
 * while the field is still under the cursor. The two lists have to stay in step.
 */
const termDrift = computed(() => {
  const d = decoded.value
  if (!d) return []
  const o = d.decoded
  const f = form.value
  const drift: {label: string; theirs: string; yours: string}[] = []
  const add = (label: string, theirs: string, yours: string) =>
    drift.push({label, theirs: theirs || '(blank)', yours: yours || '(blank)'})

  const hex = (s?: string) => (s ?? '').trim().toLowerCase()
  // "10", "10.0" and "010.00" are one term typed three ways; flagging those
  // would be this panel inventing a disagreement of its own.
  const amount = (s?: string) => {
    const v = (s ?? '').trim()
    if (!v || !/^\d*\.?\d*$/.test(v)) return v
    const [i = '', fr = ''] = v.split('.')
    return `${i.replace(/^0+/, '') || '0'}.${fr.replace(/0+$/, '')}`.replace(/\.$/, '')
  }
  const token = (s?: string) => (s ?? '').trim() || ZNN_ZTS

  if (f.leg !== d.yourLeg) add('your Bitcoin leg', d.yourLeg, f.leg)
  if (f.role !== d.yourRole) add('your role', d.yourRole, f.role)
  if (f.role === 'participant' && hex(f.secretHashHex) !== hex(o.secretHash))
    add('secret hash', o.secretHash ?? '', f.secretHashHex)
  if (Number(f.amountSats || 0) !== (o.amountSats ?? 0))
    add('Bitcoin amount (sats)', String(o.amountSats ?? 0), f.amountSats)
  if (token(f.zenonToken) !== token(o.zenonToken))
    add('Zenon token', token(o.zenonToken), token(f.zenonToken))
  if (amount(f.zenonAmount) !== amount(o.zenonAmt))
    add('Zenon amount', o.zenonAmt ?? '', f.zenonAmount)
  // Blank is not a mismatch on either of these: both are routinely supplied
  // after creation, over a session or by hand.
  if (o.zenonAddr && f.zenonPeerAddress.trim() && f.zenonPeerAddress.trim() !== o.zenonAddr)
    add('their Zenon address', o.zenonAddr, f.zenonPeerAddress)
  if (o.pkh && f.counterpartyPkhHex.trim() && hex(f.counterpartyPkhHex) !== hex(o.pkh))
    add('their pubkey hash', o.pkh, f.counterpartyPkhHex)

  return drift
})

/**
 * The gate, on the way IN to the form rather than on the way out.
 *
 * It used to sit on Create, which reads naturally -- accept these terms to
 * create a swap -- but it meant somebody could fill in every field and be handed
 * the warning at the very end, after the work of deciding was done. Asking here
 * means the warning is the first thing anybody sees.
 *
 * Every path that reveals the form goes through this rather than setting
 * `newOpen` directly, which is what makes it impossible to skip: the check does
 * not depend on knowing every place the panel can be opened from, it depends on
 * the panel only ever becoming visible one way. The third path -- the panel
 * opening itself for a browser with nothing in flight -- is gone rather than
 * gated: a blocking dialog for somebody who has done nothing but load the page
 * was worse than the click it saved.
 *
 * It still asks every time the form opens, not once per browser: see TermsGate.
 */
const termsOpen = ref(false)

function openNewSwapForm() {
  if (newOpen.value) return
  termsOpen.value = true
}

/** What Accept in the dialog actually unlocks: the form becomes visible, with
 *  whatever it was going to hold — a blank slate, or an offer already decoded
 *  in above — already set on the refs behind it. */
function onTermsAccepted() {
  newOpen.value = true
}

async function create() {
  createError.value = ''
  // Checked here as well as in Go so the message lands next to the field rather
  // than under the button. Both branches of the contract pay this address and
  // there is no other, so a swap without one can be funded and then not spent.
  if (!form.value.destAddr.trim()) {
    createError.value =
      'A Bitcoin destination address is required — it is where both branches of the contract pay.'
    return
  }
  creating.value = true
  try {
    const created = await api.create(
      {
        role: form.value.role,
        leg: form.value.leg,
        amountSats: Number(form.value.amountSats || 0),
        destAddr: form.value.destAddr.trim(),
        lockHours: Number(form.value.lockHours || 0),
        secretHashHex: form.value.secretHashHex.trim(),
        counterpartyPkhHex: form.value.counterpartyPkhHex.trim(),
        zenonSelfAddress: form.value.zenonSelfAddress.trim(),
        zenonPeerAddress: form.value.zenonPeerAddress.trim(),
        zenonToken: form.value.zenonToken.trim(),
        zenonAmount: form.value.zenonAmount.trim(),
        // The offer this form answers, when it answers one. Go re-decodes it
        // and refuses the create if any term it pins has been edited — see
        // Offer.CheckCreate. Sending the string rather than a "these matched"
        // flag is what makes that a check rather than a claim.
        offer: decoded.value ? decodedFrom.value : '',
      },
      settings.value,
    )
    await reload('active')
    // A session open before the swap existed now has something to be about.
    // Attaching is what gives arriving values somewhere to go -- until then they
    // are received, checked against nothing, and reported as such.
    //
    // It is also all that is needed to start the outgoing half: the session
    // watches the attached swap and publishes each hand-off as it comes to
    // exist. See publishOwed in useSession.
    if (session.active.value) {
      session.attach(created.id, created.role)
      // The offer is the exception, sent here because it is not a hand-off: it
      // does not update the counterparty's swap, it is what brings one into
      // being, so there is nothing on either record for a watcher to notice.
      //
      // On creation rather than on the peer arriving, because relays store these
      // events and replay them to whoever subscribes next.
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
    prefilled.value = false
    fromBoard.value = null
    offerBlob.value = ''
    decoded.value = null
    decodedFrom.value = ''
  } catch (e) {
    createError.value = e instanceof Error ? e.message : String(e)
  } finally {
    creating.value = false
  }
}

/**
 * Put every term the offer pins back into the form.
 *
 * Filling the form in is the whole point of decoding: everything the offer
 * carries is public data it computed for us, and what is left blank is exactly
 * what only this user can supply.
 *
 * Separate from `decode` so Restore can call it too. The two must set the same
 * fields -- a term this reapplies but decode never applied, or the reverse, is a
 * Restore that does not restore.
 */
function applyOffer(d: DecodedOffer) {
  form.value.leg = d.yourLeg
  // Onto the field as well as through the tab below. The tab watcher used to be
  // the only thing that wrote the role, and it runs on Vue's pre-flush queue
  // rather than synchronously -- so with the lock in place it would fire after
  // `prefilled` was already set, find the form locked, and decline to write the
  // very role the offer had just chosen.
  form.value.role = d.yourRole
  // Through the tab rather than onto the field, so the panel showing agrees
  // with the side it filled in: an answered offer opens on Join one. Receiving
  // an offer is the commonest way anybody becomes a participant, and it is the
  // one way that requires nobody to have understood the word.
  tab.value = d.yourRole === 'participant' ? 'join' : 'start'
  form.value.secretHashHex = d.decoded.secretHash ?? ''
  // The pkh in an offer is the sender's. It is only useful to the side that
  // has to build the contract, which is the side sending BTC.
  form.value.counterpartyPkhHex = d.yourLeg === 'send' ? (d.decoded.pkh ?? '') : ''
  // Written whether or not the offer carried a value, which the guarded version
  // of this did not do. A term the offer leaves out is still a term: Go reads a
  // blank Zenon token as ZNN on both sides, so an offer that omits it and a
  // form still holding the last thing typed into that box are two different
  // swaps. Now that these fields are about to be locked, a stale value left in
  // one is the worst of both -- disagreed about, and out of reach.
  form.value.amountSats = String(d.decoded.amountSats ?? 0)
  form.value.zenonPeerAddress = d.decoded.zenonAddr ?? ''
  form.value.zenonToken = d.decoded.zenonToken ?? ''
  form.value.zenonAmount = d.decoded.zenonAmt ?? ''
  // Two of the terms an offer pins -- the token, and the pubkey hash naming the
  // key their Bitcoin is redeemable by -- live behind Advanced options, which
  // is collapsed. Checking what arrived is the whole job here, and a value
  // folded away is one nobody checks. Opened rather than moved out: they are
  // still advanced for somebody filling this in from scratch.
  showAdvanced.value = true
}

/** Undo an edit to a term of the trade, which is the only thing this side can
 *  do about one short of proposing a swap of their own. */
function restoreTerms() {
  if (decoded.value) applyOffer(decoded.value)
}

/** Stop answering their offer and start proposing one — the honest way to
 *  trade on different terms, as against quietly creating a swap that says so. */
function discardOffer() {
  decoded.value = null
  decodedFrom.value = ''
  offerBlob.value = ''
  prefilled.value = false
}

async function decode() {
  decodeError.value = ''
  decoded.value = null
  try {
    const raw = offerBlob.value.trim()
    const d = await api.decodeOffer(raw)
    decoded.value = d
    decodedFrom.value = raw
    applyOffer(d)
    prefilled.value = true
  } catch (e) {
    decodeError.value = e instanceof Error ? e.message : String(e)
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
          Each swap gets a throwaway key that can only move coins locked in that swap's own
          contract, and both ways out of it pay an address you gave. Your own wallets are never
          asked for a key.
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
         auto, and this one holds cards that can contain a block of shell
         commands wide enough to push the whole page sideways if any level of
         the chain down to that block forgets it. -->
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
               place that still touches `newOpen` directly rather than going
               through openNewSwapForm. -->
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
           yet. Nothing files itself — see Swap.Active in wasm/swap.go — so
           this is where a completed swap is actually read, and it stays until
           the person who ran it says so.

           Giving it the full card — hand-off panels, wallet buttons, event
           log — for that one press is the same over-built surface HistoryRow
           exists to avoid on the History page, so it gets the same shrunk row
           here, with the one button a plain history row does not need.

           Asked of the swap rather than matched against nextStep's wording,
           which is the mistake waitingOnThem exists to avoid: a reworded
           sentence would silently put the full card back. -->
      <template v-for="sw in swaps" v-else :key="sw.id">
        <HistoryRow v-if="sw.settled" :swap="sw" archivable @changed="reload('active')" />
        <SwapCard v-else :swap="sw" @changed="reload('active')" />
      </template>
    </section>

    <!-- New swap: the two sides of a trade, plus the paste box that picks one -->
    <Card v-if="newOpen" class="border-primary/40">
      <CardHeader class="gap-3">
        <CardTitle class="flex flex-wrap items-center gap-2">
          New swap
          <span class="rounded-full bg-muted px-2 py-0.5 font-mono text-xs font-normal">
            {{ settings.network }}
          </span>
        </CardTitle>
        <div class="flex flex-wrap items-center gap-2">
          <Tabs v-model="tab">
            <TabsList
              class="grid w-full grid-cols-3 rounded-lg bg-muted p-1 sm:w-auto sm:inline-grid"
            >
              <!-- Which side you are is the offer's to state once one has
                   filled this form in, so the other tab goes dead rather than
                   disappearing: it still shows what the two choices were. -->
              <TabsTrigger value="start" :disabled="lockedByOffer && form.role !== 'initiator'">
                Start one
              </TabsTrigger>
              <TabsTrigger value="join" :disabled="lockedByOffer && form.role !== 'participant'">
                Join one
              </TabsTrigger>
              <TabsTrigger value="offer">Paste their offer</TabsTrigger>
            </TabsList>
          </Tabs>
          <!-- What the dropdown this replaced used to explain. -->
          <InfoTip label="Start one, or join one">
            The side that starts invents the secret and takes the longer timelock; the side that
            joins is sent the hash of it. Which chain each of those lands on is worked out for you.
            Sent an offer? Paste it — you are joining, and decoding it says so.
          </InfoTip>
        </div>
      </CardHeader>

      <CardContent class="grid gap-6">
        <!-- Paste an offer ------------------------------------------------ -->
        <div v-if="tab === 'offer'" class="grid gap-3">
          <div class="flex items-center gap-1.5 text-sm text-muted-foreground">
            Paste the <code class="font-mono">swapoffer1:</code> string they sent you.
            <InfoTip label="What an offer string is">
              One line of public data — amount, network, secret hash, addresses — and never a secret
              or a key. Decoding it fills in your side of their trade.
            </InfoTip>
          </div>
          <Textarea
            v-model="offerBlob"
            rows="3"
            spellcheck="false"
            autocapitalize="none"
            placeholder="swapoffer1:…"
            class="font-mono text-xs"
          />
          <Button class="justify-self-start" :disabled="!offerBlob.trim()" @click="decode">
            Decode and fill the form
          </Button>

          <p v-if="decodeError" class="text-sm text-destructive">{{ decodeError }}</p>

          <div v-if="decoded" class="grid min-w-0 gap-2">
            <Button
              variant="ghost"
              size="sm"
              class="justify-self-start"
              @click="showRawOffer = !showRawOffer"
            >
              {{ showRawOffer ? 'Hide' : 'Show' }} what was in it
            </Button>
            <pre
              v-if="showRawOffer"
              class="min-w-0 overflow-x-auto rounded-md bg-muted/60 p-3 font-mono text-xs"
              >{{ JSON.stringify(decoded.decoded, null, 2) }}</pre>
          </div>
        </div>

        <!-- The form ------------------------------------------------------ -->
        <template v-if="showForm">
          <!-- A trade agreed on the board. Said plainly, because the amounts
               below were not typed by the person looking at them and a form
               that filled itself in without saying so is a form nobody checks.

               It is deliberately not the locked treatment an offer gets: nothing
               on a board is signed by a counterparty, so none of it may pin a
               term. These are still editable, and their real offer arrives over
               the session in a moment and locks the terms it carries. -->
          <div
            v-if="fromBoard && !prefilled"
            class="flex flex-wrap items-start gap-2 rounded-md border border-primary/40 bg-primary/5 p-3 text-sm"
          >
            <ArrowRightLeftIcon class="mt-0.5 size-4 shrink-0 text-primary" />
            <div class="grid min-w-0 flex-1 gap-2">
              <p>
                From the board — post <span class="font-mono">{{ fromBoard.postId }}</span> with
                <span class="font-mono">{{ fromBoard.peer.slice(0, 8) }}…</span>. You are the
                <strong>{{ form.role }}</strong> and you
                {{ form.leg === 'send' ? 'send Bitcoin' : 'receive Bitcoin' }}.
              </p>
              <p class="text-muted-foreground">
                The amounts and direction are what the two of you agreed. Add your addresses below
                and press Create — their signed offer arrives over the session and locks the terms
                it carries, which is what a swap is actually built against.
              </p>
            </div>
          </div>

          <div
            v-if="prefilled && !termDrift.length"
            class="flex flex-wrap items-start gap-2 rounded-md border border-success/40 bg-success/5 p-3 text-sm"
          >
            <ClipboardCheckIcon class="mt-0.5 size-4 shrink-0 text-success" />
            <div class="grid min-w-0 flex-1 gap-2">
              <p>
                Filled in from their offer — you are the
                <strong>{{ form.role }}</strong> and you
                {{ form.leg === 'send' ? 'send Bitcoin' : 'receive Bitcoin' }}.
              </p>
              <p class="flex items-start gap-1.5 text-muted-foreground">
                <LockIcon class="mt-0.5 size-3.5 shrink-0" />
                <span class="min-w-0">
                  Every term they set is locked — check each one against what they told you, then
                  add your own addresses below. Nothing here is yours to change: a term that differs
                  from their offer is two people running different swaps.
                </span>
              </p>
              <!-- The way out of a locked form, and the only honest one. It
                   used to live in the drift panel, which locking has made
                   unreachable -- so somebody who disagrees with a term would
                   have had no move at all. -->
              <Button variant="ghost" size="sm" class="justify-self-start" @click="discardOffer">
                Drop their offer and propose my own terms
              </Button>
            </div>
          </div>

          <!-- The terms have been edited since the offer filled them in, which
               is not a form error: it is two people about to run different
               swaps. Go refuses the create outright (Offer.CheckCreate) — this
               is the same finding, delivered while the field is still under the
               cursor and with the two ways out of it attached. -->
          <div
            v-if="prefilled && termDrift.length"
            class="grid gap-3 rounded-md border border-destructive/50 bg-destructive/5 p-3 text-sm"
          >
            <div class="flex items-start gap-2">
              <TriangleAlertIcon class="mt-0.5 size-4 shrink-0 text-destructive" />
              <p class="min-w-0 flex-1">
                <strong>This no longer matches their offer.</strong> Both browsers build their own
                contract from their own record and nothing on either chain compares the two, so a
                term that differs here is not found until money is committed — as a refused audit, a
                refused HTLC, or a funding that arrives short.
              </p>
            </div>
            <dl class="grid gap-1.5">
              <div
                v-for="d in termDrift"
                :key="d.label"
                class="grid gap-x-3 gap-y-0.5 sm:grid-cols-[minmax(0,10rem)_minmax(0,1fr)]"
              >
                <dt class="text-xs font-semibold">{{ d.label }}</dt>
                <dd class="grid min-w-0 gap-0.5 font-mono text-xs break-all">
                  <span class="text-muted-foreground">their offer: {{ d.theirs }}</span>
                  <span class="text-destructive">this form: {{ d.yours }}</span>
                </dd>
              </div>
            </dl>
            <div class="flex flex-wrap gap-2">
              <Button variant="outline" size="sm" @click="restoreTerms">
                Restore their terms
              </Button>
              <!-- The honest alternative, and the only other one: a swap that
                   is not an answer to their offer is a proposal of your own,
                   and it has to be sent to them as one. -->
              <Button variant="ghost" size="sm" @click="discardOffer">
                Drop their offer and propose my own terms
              </Button>
            </div>
          </div>
          <div
            v-if="prefilled && offerNetworkClash"
            class="rounded-md border border-warning/40 bg-warning/5 p-3 text-sm text-warning"
          >
            That offer is for
            <span class="font-mono">{{ decoded?.decoded.network }}</span> but you are set to
            <span class="font-mono">{{ settings.network }}</span
            >. Switch network under Nodes before creating this, or the two swaps will never meet.
          </div>

          <!-- 1. The trade -->
          <fieldset class="grid gap-3">
            <legend class="pb-2 text-ledger text-muted-foreground">1 · The trade</legend>

            <div class="grid gap-2 sm:grid-cols-2">
              <button
                v-for="opt in LEGS"
                :key="opt.value"
                type="button"
                class="rounded-lg border p-3 text-left transition-colors"
                :class="[
                  form.leg === opt.value ? 'border-primary bg-primary/10' : 'border-border',
                  lockedByOffer ? 'cursor-not-allowed' : '',
                  lockedByOffer && form.leg !== opt.value ? 'opacity-50' : '',
                  !lockedByOffer && form.leg !== opt.value
                    ? 'hover:border-border hover:bg-muted/50'
                    : '',
                ]"
                :disabled="lockedByOffer"
                :aria-pressed="form.leg === opt.value"
                @click="form.leg = opt.value"
              >
                <span class="block text-sm font-semibold">{{ opt.title }}</span>
                <span class="block text-xs text-muted-foreground">{{ opt.sub }}</span>
              </button>
            </div>

            <div class="grid items-end gap-3 sm:grid-cols-[1fr_auto_1fr]">
              <div class="grid gap-2">
                <Label for="c-amount" class="flex items-center gap-1.5">
                  {{ sending === 'btc' ? 'You send' : 'You receive' }} — Bitcoin (sats)
                  <LockIcon v-if="lockedByOffer" class="size-3 shrink-0 text-muted-foreground" />
                </Label>
                <Input
                  id="c-amount"
                  v-model="form.amountSats"
                  type="number"
                  inputmode="numeric"
                  min="1"
                  :readonly="lockedByOffer"
                  class="font-mono tabular-nums"
                  :class="lockedByOffer ? LOCKED_FIELD : ''"
                />
                <p class="h-4 font-mono text-xs text-muted-foreground">{{ amountBtc }}</p>
              </div>
              <ArrowRightLeftIcon
                class="mb-8 hidden size-4 self-center text-muted-foreground sm:block"
                aria-hidden="true"
              />
              <div class="grid gap-2">
                <Label for="c-znn-amount" class="flex items-center gap-1.5">
                  {{ sending === 'znn' ? 'You send' : 'You receive' }} — Zenon (whole tokens)
                  <LockIcon v-if="lockedByOffer" class="size-3 shrink-0 text-muted-foreground" />
                </Label>
                <Input
                  id="c-znn-amount"
                  v-model="form.zenonAmount"
                  inputmode="decimal"
                  placeholder="e.g. 10 or 1.25"
                  :readonly="lockedByOffer"
                  class="font-mono tabular-nums"
                  :class="lockedByOffer ? LOCKED_FIELD : ''"
                />
                <p class="h-4 text-xs text-muted-foreground">
                  {{ form.zenonToken ? 'of the token set below' : 'ZNN' }}
                </p>
              </div>
            </div>

            <!-- The number that decides whether this swap can be undone. It sits
                 under the amount because that is the field it is about, and it
                 updates as the amount does. -->
            <UnlockCost
              :cost="unlockCost"
              :loading="unlockLoading"
              :error="unlockError"
              :unlocked-by="unlockedBy"
              :on-use-recommended="lockedByOffer ? undefined : useRecommendedAmount"
            />
          </fieldset>

          <!-- 2. Addresses -->
          <fieldset class="grid gap-3">
            <legend class="pb-2 text-ledger text-muted-foreground">2 · Your addresses</legend>

            <div class="grid gap-2 rounded-lg border border-primary/40 bg-primary/5 p-3">
              <Label for="c-dest" class="flex items-center gap-1.5">
                Your Bitcoin address
                <span class="text-destructive">*</span>
                <InfoTip label="Why a Bitcoin address is required">
                  Redeem and refund both pay this address and no other, and the refund is pre-signed
                  to it the moment funding appears. Use a receiving address from a wallet you
                  control.
                </InfoTip>
              </Label>
              <Input
                id="c-dest"
                v-model="form.destAddr"
                required
                spellcheck="false"
                autocapitalize="none"
                autocomplete="off"
                placeholder="bcrt1… / tb1… / bc1…"
                class="font-mono"
              />
              <p class="text-xs text-muted-foreground">
                In a wallet you control. Everything this swap pays out goes here.
              </p>
              <!-- The address is the one field where a typo is unrecoverable and
                   invisible until payout, so the wallet fills it rather than the
                   user retyping it. -->
              <WalletConnect :on-address="(a: string) => (form.destAddr = a)" />
            </div>

            <div class="grid gap-3 sm:grid-cols-2">
              <div class="grid gap-2">
                <Label for="c-znn-self">Your Zenon address</Label>
                <Input
                  id="c-znn-self"
                  v-model="form.zenonSelfAddress"
                  spellcheck="false"
                  autocapitalize="none"
                  autocomplete="off"
                  placeholder="z1…"
                  class="font-mono"
                />
              </div>
              <div class="grid gap-2">
                <Label for="c-znn-peer" class="flex items-center gap-1.5">
                  Their Zenon address
                  <LockIcon v-if="lockedPeerZenon" class="size-3 shrink-0 text-muted-foreground" />
                </Label>
                <Input
                  id="c-znn-peer"
                  v-model="form.zenonPeerAddress"
                  spellcheck="false"
                  autocapitalize="none"
                  autocomplete="off"
                  placeholder="z1…"
                  :readonly="lockedPeerZenon"
                  class="font-mono"
                  :class="lockedPeerZenon ? LOCKED_FIELD : ''"
                />
              </div>
            </div>
          </fieldset>

          <!-- 3. Only what a participant needs -->
          <fieldset v-if="isParticipant" class="grid gap-3">
            <legend class="pb-2 text-ledger text-muted-foreground">3 · From the initiator</legend>
            <div class="grid gap-2">
              <Label for="c-hash" class="flex items-center gap-1.5">
                Secret hash
                <LockIcon v-if="lockedSecretHash" class="size-3 shrink-0 text-muted-foreground" />
                <InfoTip label="What the secret hash is">
                  The initiator's unseen secret, hashed. Both contracts lock to it, so both open
                  with the one secret. 64 hex characters — copy it exactly.
                </InfoTip>
              </Label>
              <Input
                id="c-hash"
                v-model="form.secretHashHex"
                spellcheck="false"
                autocapitalize="none"
                autocomplete="off"
                placeholder="64 hex chars"
                :readonly="lockedSecretHash"
                class="font-mono"
                :class="lockedSecretHash ? LOCKED_FIELD : ''"
              />
            </div>
          </fieldset>

          <!-- Everything that has a working default -->
          <div class="grid gap-3">
            <Button
              variant="ghost"
              size="sm"
              class="justify-self-start"
              @click="showAdvanced = !showAdvanced"
            >
              {{ showAdvanced ? 'Hide' : 'Show' }} advanced options
            </Button>
            <div v-if="showAdvanced" class="grid gap-3 rounded-lg border border-border p-3">
              <div class="grid gap-3 sm:grid-cols-2">
                <div class="grid gap-2">
                  <Label for="c-hours" class="flex items-center gap-1.5">
                    Bitcoin timelock (hours)
                    <InfoTip label="About the timelock">
                      How long before you can reclaim the Bitcoin if the swap stalls. Auto keeps it
                      on the safe side of the Zenon leg; override only if you know why.
                    </InfoTip>
                  </Label>
                  <Input
                    id="c-hours"
                    v-model="form.lockHours"
                    type="number"
                    inputmode="numeric"
                    min="1"
                    placeholder="auto"
                    class="font-mono tabular-nums"
                  />
                </div>
                <div class="grid gap-2">
                  <Label for="c-znn-token" class="flex items-center gap-1.5">
                    Zenon token
                    <LockIcon v-if="lockedByOffer" class="size-3 shrink-0 text-muted-foreground" />
                    <InfoTip label="Which Zenon token">
                      Blank trades ZNN. Anyone can issue a token, so which one is as much a term of
                      the trade as how much — verification checks it.
                    </InfoTip>
                  </Label>
                  <Input
                    id="c-znn-token"
                    v-model="form.zenonToken"
                    spellcheck="false"
                    autocapitalize="none"
                    autocomplete="off"
                    placeholder="zts1… (blank = ZNN)"
                    :readonly="lockedByOffer"
                    class="font-mono"
                    :class="lockedByOffer ? LOCKED_FIELD : ''"
                  />
                </div>
              </div>
              <div v-if="form.leg === 'send'" class="grid gap-2">
                <Label for="c-pkh" class="flex items-center gap-1.5">
                  Their Bitcoin pubkey hash
                  <LockIcon v-if="lockedPeerPkh" class="size-3 shrink-0 text-muted-foreground" />
                  <InfoTip label="Their pubkey hash">
                    Only the side sending BTC needs it, to build the contract — it names the key
                    allowed to take the redeem branch, which is how their Bitcoin reaches them.
                    Leave it blank and paste it into the swap card later, or let their offer fill it
                    in. An offer that carried one has fixed it: it is the counterparty's own value
                    and not a field to correct.
                  </InfoTip>
                </Label>
                <Input
                  id="c-pkh"
                  v-model="form.counterpartyPkhHex"
                  spellcheck="false"
                  autocapitalize="none"
                  autocomplete="off"
                  placeholder="40 hex chars, optional"
                  :readonly="lockedPeerPkh"
                  class="font-mono"
                  :class="lockedPeerPkh ? LOCKED_FIELD : ''"
                />
              </div>
            </div>
          </div>

          <!-- The warning that used to live here is now a question, asked
               before this form was ever reachable — see openNewSwapForm. A
               paragraph that is always on the page is one nobody reads, and it
               was competing for attention with the address field where the
               mistake it warns about actually happens. -->
          <div class="flex flex-wrap items-center gap-3 border-t border-border pt-4">
            <!-- Go refuses a create whose terms drifted from the offer either
                 way; this is only so the button stops looking available while
                 the panel above says why it is not. -->
            <Button size="lg" :disabled="creating || termDrift.length > 0" @click="create">
              Create swap
            </Button>
            <p v-if="termDrift.length" class="text-xs text-destructive">
              Restore their terms, or drop their offer, before creating this.
            </p>
            <p v-else class="text-xs text-muted-foreground">
              Nothing is sent anywhere. This builds the swap in your browser.
            </p>
          </div>
          <p v-if="createError" class="text-sm text-destructive">{{ createError }}</p>
        </template>
      </CardContent>
    </Card>

    <BackupPanel />

    <!-- Accepting is what reveals the form; declining leaves it closed and
         nothing on the page changes. -->
    <TermsGate v-model:open="termsOpen" @accepted="onTermsAccepted" />
  </div>
</template>

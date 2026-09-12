<script setup lang="ts">
import {computed, ref, watch} from 'vue'
import {
  ArchiveIcon,
  ArchiveRestoreIcon,
  ArrowRightIcon,
  CircleCheckIcon,
  ClockIcon,
  DownloadIcon,
  RefreshCcwDotIcon,
  RefreshCwIcon,
  SearchIcon,
  ShareIcon,
  TriangleAlertIcon,
} from '@lucide/vue'
import {
  Address,
  Badge,
  Button,
  Card,
  CardContent,
  CardHeader,
  CopyButton,
  Input,
  Label,
  Switch,
  Textarea,
} from 'nom-ui'
import type {BadgeVariants} from 'nom-ui'
import InfoTip from './InfoTip.vue'
import Handoff from './Handoff.vue'
import SessionSend from './SessionSend.vue'
import Note from './Note.vue'
import UnlockCost from './UnlockCost.vue'
import WalletConnect from './WalletConnect.vue'
import ZenonWallet from './ZenonWallet.vue'
import StepBar from './StepBar.vue'
import TxRef from './TxRef.vue'
import Waiting from './Waiting.vue'
import DataList from './DataList.vue'
import DataRow from './DataRow.vue'
import {api, download} from '@/core/api'
import {btc, sats, stamp, tokenName, until} from '@/core/format'
import {STAGES, nextStep, stage, waitingOnThem} from '@/core/progress'
import {cliNodeURL, znnCommands} from '@/core/zenon-commands'
import {useSettings} from '@/core/composables/useSettings'
import {useUnisat} from '@/core/composables/useUnisat'
import {useUnlockCost} from '@/core/composables/useUnlockCost'
import {useSession} from '@/core/composables/useSession'
import {useCliCommands} from '@/core/composables/useCliCommands'
import {useZenonWallet} from '@/core/composables/useZenonWallet'
import {useAutoMode} from '@/core/composables/useAutoMode'
import type {HandoffType} from '@/core/handoffs'
import type {Swap, WalletAction} from '@/types'

const props = defineProps<{swap: Swap}>()
const emit = defineEmits<{changed: []}>()

const {body: settings, hasZenon} = useSettings()

const error = ref('')
const busy = ref(false)
const showLog = ref(false)
const showCommands = ref(false)
const offer = ref('')
const znnResult = ref<{ok: boolean; text: string} | null>(null)

const wallet = useUnisat()
const session = useSession()
const autoMode = useAutoMode()
const cli = useCliCommands()
const showCli = cli.show
const funding = ref('')

/**
 * Deliberately reopening the funding controls after a payment was broadcast.
 *
 * A broadcast that never arrives is a real state -- a transaction can be dropped
 * from a mempool -- so the way back has to exist. A separate, quiet, explicitly
 * pressed control rather than a live Send button: this is a decision, not a
 * click.
 */
const fundAgain = ref(false)

// A session can send a value this card is holding, but only for the swap it is
// attached to: publishing this swap's contract into a room that is about a
// different swap would hand the other side something they will rightly refuse.
const inSession = computed(() => session.active.value && session.swapId.value === props.swap.id)

/** Put this card into the open session, so its values can travel. */
function attachSession() {
  session.attach(props.swap.id, props.swap.role)
}

/**
 * Whether the contract hand-off panel is still open.
 *
 * It closes the moment the contract has gone over the session, because from then
 * on it is a block of hex sitting between the reader and the step that is
 * actually outstanding. It closes rather than disappears -- the hand-off is the
 * most common thing to repeat -- and a contract that has to be sent by hand is
 * never put away on its own.
 *
 * Derived from whether it has gone rather than set when this card sends it,
 * because an attached session publishes the contract by itself. Closing only in
 * the manual path missed the ordinary case entirely.
 */
const contractSent = computed(() => sent('contract', props.swap.contractHex))

/** Show/Hide, when the reader has overruled the line above. `null` is deferring
 *  to it, which is not the same as either answer and cannot be spelled with a
 *  boolean. */
const contractToggled = ref<boolean | null>(null)

const contractOpen = computed({
  get: () => contractToggled.value ?? !contractSent.value,
  set: (open) => (contractToggled.value = open),
})

// A rebuilt contract is a value the counterparty does not have, whatever was
// decided about the one it replaces.
watch(
  () => props.swap.contractHex,
  () => (contractToggled.value = null),
)

const pkhIn = ref('')
const contractIn = ref('')
const htlcIn = ref('')
const secretIn = ref('')
const destIn = ref('')

// A verification result describes one HTLC id at one moment; leaving it on the
// card would keep showing "verified" beside a leg since re-checked and refused.
// Separate getters, so this fires when one of the three actually changes rather
// than on every reload of the swap list.
watch(
  [() => props.swap.id, () => props.swap.zenon?.htlcId, () => props.swap.zenon?.verified],
  () => (znnResult.value = null),
)

// An id the swap already holds belongs in the field that shows ids. The case
// this exists for is the session: an HTLC id arriving from the counterparty is
// verified and recorded without this card being touched, so the box stayed empty
// beside a leg that had already been checked. Anything typed wins, because a
// field overwritten mid-keystroke is worse than one that needs a paste.
watch(
  () => props.swap.zenon?.htlcId,
  (id) => {
    if (id && !htlcIn.value.trim()) htlcIn.value = id
  },
  {immediate: true},
)

// A finished swap is not a failed one: "refunded" means the timelock did its
// job. Only a funding shortfall and a rejected HTLC are actually bad.
const stateVariant = computed<BadgeVariants['variant']>(() => {
  switch (props.swap.state) {
    case 'redeemed':
      return 'success'
    case 'refunded':
    case 'expired':
      return 'warning'
    case 'funded':
      return 'pending'
    default:
      return 'outline'
  }
})

/**
 * The Zenon leg's verification state, as three answers rather than two.
 * "Not verified" used to mean both "the chain disagrees" and "the chain has not
 * been asked yet", which are opposite pieces of news -- and a create published
 * through the wallet is unreadable for a momentum or two, so every healthy HTLC
 * spent its first minute wearing a red badge.
 */
const zenonPending = computed(
  () =>
    Boolean(props.swap.zenon?.htlcId) &&
    !props.swap.zenon?.verified &&
    Boolean(props.swap.zenon?.verifyPending),
)
const zenonBad = computed(
  () => Boolean(props.swap.zenon?.htlcId) && !props.swap.zenon?.verified && !zenonPending.value,
)

/**
 * What became of the Zenon HTLC, for the badge beside the verification one.
 *
 * The unlock is the moment the swap turns, and until this it was the one move
 * neither side could see on the card. Verification is a claim about an entry
 * that EXISTS, and an unlock deletes the entry -- so the verified badge cannot
 * carry this news, and cannot be re-checked to get it: a collected leg and an id
 * that was never there answer identically.
 *
 * Which is why each side reads a different field, rather than one shared truth
 * about the chain. Neither can ask the contract, so each knows only its own half:
 * the unlocker recorded what they published, and the creator recorded the unlock
 * transaction ferry found while pulling the preimage out of it. Both are facts;
 * they are simply not the same fact, and the wording says which one is being
 * shown rather than flattening them into "unlocked".
 *
 * Deliberately not derived from holding the preimage. On the creator's side one
 * pasted in by hand hashes just as correctly as one read off a block, and a badge
 * asserting a chain event on the strength of a paste would be the card's only
 * claim that nothing checked.
 */
const zenonUnlock = computed(() => {
  const z = props.swap.zenon
  if (z?.unlockHash) return {mine: true, text: 'unlocked by you — ZNN collected'}
  if (z?.unlockSeen) return {mine: false, text: 'unlocked by them — preimage recovered'}
  return null
})

/**
 * Whether the Zenon leg can be at a stage worth putting controls on screen for.
 *
 * Whichever leg is locked SECOND cannot be at any stage until the first one
 * holds money, so until the Bitcoin contract turns up on chain there is no
 * hashlock worth acting on, no id to paste, none to find and nothing to
 * verify. The panel sat there from the first render regardless, asking for
 * values that could not exist yet, which reads as a step being skipped rather
 * than as one not reached.
 *
 * `funding` is the right test and `fundingBroadcast` is not. Funding is set
 * only once a refresh has actually found the output at the contract address,
 * which makes it a fact about the chain that BOTH sides can see rather than a
 * note about what this browser sent; and `confirmed: false` on it means the
 * payment is sitting in a mempool. So this opens the moment the money is
 * visible, not once it is buried.
 *
 * The exemption is the timelock ordering, and it is not optional. The
 * initiator's leg has the longer lock and must be locked first, so where the
 * Bitcoin contract is the PARTICIPANT's leg it is the Zenon HTLC that goes
 * first and the Bitcoin funding that waits on it. Gating the panel on funding
 * in that shape deadlocks the swap: the HTLC could only be created from a
 * panel that would not appear until the payment which is itself waiting for
 * the HTLC. An id that already exists is never hidden either, however it got
 * here — a restored backup or an out-of-order counterparty still has something
 * to verify.
 */
const zenonLegPossible = computed(
  () =>
    !props.swap.btcLegIsInitiators ||
    Boolean(props.swap.funding) ||
    Boolean(props.swap.zenon?.htlcId),
)

/**
 * Whether there is still a question to put to a node about the Zenon leg.
 *
 * Once an HTLC has passed verification there is not, and the side that created
 * it never had a reason to type its own id into a box. A live Verify button
 * beside a verified leg reads as though the tick above it cannot be relied on.
 *
 * The verdict decides this, not the create, so the row stays for the two states
 * that need it: pending, where pressing is the only thing that asks again, and
 * refused, where the id may be the wrong one and the field is how the right one
 * gets in.
 */
const needsVerify = computed(() => !props.swap.zenon?.verified && zenonLegPossible.value)

// Where the swap has got to, and whose move it is. A card that is waiting on
// somebody else should not look like a card demanding something of you.
const at = computed(() => stage(props.swap))
const step = computed(() => nextStep(props.swap))
const theirMove = computed(() => waitingOnThem(props.swap))

/**
 * Whether the last segment of the track is a finished swap rather than a step
 * still under way -- see StepBar's `done`.
 *
 * Confirmed means the FUNDING is confirmed. That is a narrower claim than the
 * bar looks like it is making, and it is the only one the data supports: a
 * `SpendResult` carries a txid, a fee and a size, and nothing about whether the
 * spend was ever mined. A swap whose funding never confirmed does not get the
 * green.
 */
const settled = computed(() => at.value === 4 && Boolean(props.swap.funding?.confirmed))

/** The trade in one line, from this side. */
const trade = computed(() => {
  const amount = props.swap.zenon?.amountDisplay
  const token = tokenName(props.swap.zenon?.tokenStandard)
  const zside = amount ? `${amount} ${token}` : token
  return props.swap.leg === 'send'
    ? {from: sats(props.swap.amountSats), to: zside}
    : {from: zside, to: sats(props.swap.amountSats)}
})

// Which step is this swap waiting on?
const needsPkh = computed(() => !props.swap.contractAddr && props.swap.leg === 'send')
const needsAudit = computed(() => !props.swap.contractAddr && props.swap.leg === 'receive')
const needsFunding = computed(
  () => Boolean(props.swap.contractAddr) && props.swap.leg === 'send' && !props.swap.funding,
)

/**
 * A payment to this contract that has already left a wallet -- either one this
 * card broadcast a moment ago, or one recorded on the swap before the last
 * reload. The second is what makes this a guard rather than a nicety: reloading
 * while waiting for a payment to appear is the most natural thing in the world,
 * and it used to hand back a live Send button beside an untouched-looking
 * contract.
 *
 * It is not funding. The contract holds money when a node says an output is
 * there, which is what `swap.funding` means.
 */
const fundingSent = computed(() => funding.value || props.swap.fundingBroadcast?.txid || '')

/**
 * How settled the funding is, in the terms the rest of Bitcoin uses. Funded and
 * settled are not the same claim: a payment sitting in a mempool is one the
 * sender can still replace. Counted up to six and pinned there.
 */
const fundingDepth = computed(() => {
  const f = props.swap.funding
  if (!f) return null
  if (!f.confirmed) return {text: 'unconfirmed — still in the mempool', variant: 'warning' as const}
  const n = f.confirmations ?? 0
  if (n >= 6) return {text: '6+ confirmations', variant: 'success' as const}
  return {
    text: `${n} confirmation${n === 1 ? '' : 's'}`,
    variant: (n >= 3 ? 'success' : 'pending') as BadgeVariants['variant'],
  }
})
const canRedeem = computed(
  () =>
    props.swap.leg === 'receive' &&
    Boolean(props.swap.funding) &&
    Boolean(props.swap.secretHex) &&
    props.swap.state !== 'redeemed',
)
// Only one shape needs the preimage typed in: the participant who created the
// Zenon HTLC. The initiator unlocking it publishes the preimage on Zenon, and
// unlocking DELETES the entry, so it cannot be read back. Every other shape
// either already holds the secret or picks it up from Bitcoin automatically.
// It is also strictly an AFTER: the preimage this asks for only becomes public
// when the counterparty unlocks the HTLC, and there is nothing to unlock until
// the HTLC has been created. Offered before that, it asks for a secret nobody
// on either side is holding yet.
const needsSecret = computed(
  () =>
    props.swap.secretArrivesOnZenon && !props.swap.secretHex && Boolean(props.swap.zenon?.htlcId),
)
const canRefund = computed(() => props.swap.refundable && props.swap.state !== 'refunded')

// The commands are recomputed rather than called twice in the template: the
// <pre> and the copy button have to hand over the same text, and calling the
// builder in two places is how they end up one render apart.
const commands = computed(() =>
  znnCommands(props.swap, {nodeURL: cliNodeURL(settings.value.znnUrl)}),
)

/**
 * Still going: this swap has something left to do on one chain or the other.
 *
 * `active` used to mean exactly this, because a swap dropped out of the active
 * list the moment its Bitcoin leg went terminal. It no longer does — nothing
 * files itself now, and a finished swap stays on the Swaps page as a summary
 * row until the user archives it, so `active` means only "not filed away yet".
 *
 * Everything on this card that asks "is there anything left to do here" wants
 * the old question, which is `settled` inverted. Keeping it as one computed is
 * what stops the two meanings being confused again a field at a time.
 */
const live = computed(() => props.swap.active && !props.swap.settled)

// A funded contract whose recovery file has not been taken off this machine is
// the sharpest edge here: the keys sit in localStorage, which nothing backs up
// and which "clear site data" erases. So the prompt is not a tip tucked into a
// Note — it is on the card, for exactly as long as it applies.
const urgeRecovery = computed(() => Boolean(props.swap.funding) && live.value)

// A swap created on one network cannot be acted on while the app is pointed at
// another: every address, fee lookup and broadcast would go to the wrong chain.
// Every button that reaches a chain is disabled rather than merely accompanied
// by a warning, because a warning next to a live Refresh button is one people
// click past.
const networkMismatch = computed(() => props.swap.network !== settings.value.network)

// Every chain-touching action is off while the networks disagree. Auditing and
// the offer string are not: both are pure and work with every node in the world
// unreachable, which is precisely when they are most worth having.
const chainBlocked = computed(() => busy.value || networkMismatch.value)

// The three hand-offs, each shown for exactly as long as it is the outstanding
// move. A joiner owes their pubkey hash before a contract can exist; the funder
// owes the contract itself once it does; and the joiner then needs the hashlock
// in front of them, because it is the one argument of the Zenon call they cannot
// derive from anything else on screen.
const handOffPkh = computed(
  () => props.swap.leg === 'receive' && !props.swap.contractAddr && Boolean(props.swap.key?.pkhHex),
)
const handOffContract = computed(() => props.swap.leg === 'send' && Boolean(props.swap.contractHex))
const showHashlock = computed(
  () => props.swap.leg === 'receive' && Boolean(props.swap.contractAddr) && live.value,
)

// Which Zenon call is this side's outstanding move, if any.
//
// It decides which button to offer, not whether the call is allowed: Go checks
// every precondition again and refuses with a sentence naming the one that
// failed. Duplicating those rules here would be a second copy of the leg
// ordering and the preimage ownership, which is precisely the pair that must not
// drift.
//
// The two halves are gated differently. Creating pays INTO the swap, so it is
// only ever a move on a live one. Unlocking takes value OUT, and being owed
// something does not stop being true because the other leg finished: `live` is
// a fact about the BITCOIN side, and for the user who sends BTC the ordinary
// ending leaves them owed ZNN still sitting in an HTLC only their key opens. So
// gating on `live` made the whole Zenon panel vanish at exactly the moment it
// held the only thing left to do. What ends an unlock is having unlocked.
const zenonAction = computed<WalletAction | null>(() => {
  if (props.swap.zenonHtlcIsOurs) {
    if (!live.value || props.swap.zenon?.htlcId) return null
    // Where this leg answers the counterparty's Bitcoin funding, it is not
    // offered until that funding is real: present, covering the amount, mined
    // and unspent, as Go judges it. Not mounting the wallet panel is what
    // makes Auto Mode wait here rather than halt on the engine's refusal --
    // and the panel appears, and autopilot moves, the moment a refresh says
    // the funding has settled.
    if (!props.swap.fundingCommitted) return null
    return 'create'
  }
  if (props.swap.zenon?.unlockHash) return null
  return props.swap.zenon?.htlcId && props.swap.secretHex ? 'unlock' : null
})

// Reclaim is the failure path, so it is offered only once it can actually
// succeed: the contract refuses one before expiry, and a button that spends a
// fee to be told so is worse than no button.
const zenonReclaimable = computed(() => {
  const z = props.swap.zenon
  if (!props.swap.zenonHtlcIsOurs || !z?.htlcId || !z.expirationTime) return false
  return Date.now() / 1000 >= z.expirationTime
})

// The Zenon block holds the wallet buttons, the commands and the verify box, so
// it stays up as long as this side still has something to do on that leg. That
// outlasts the Bitcoin side: a contract the counterparty has already redeemed
// leaves the ZNN uncollected for whoever sent BTC.
const showZenon = computed(
  () =>
    Boolean(props.swap.contractAddr) &&
    zenonLegPossible.value &&
    (live.value || Boolean(zenonAction.value) || zenonReclaimable.value),
)

// What unlocking this contract costs, priced against the contract that was
// really built and the money really in it. Sized in Go by the code that signs
// the spend, so the number on the card is the number the button will charge.
const unlockInput = computed(() =>
  live.value || props.swap.funding ? {id: props.swap.id, amountSats: 0} : null,
)
const {
  cost: unlockCost,
  loading: unlockLoading,
  error: unlockError,
} = useUnlockCost(unlockInput, settings)
// Whoever receives the Bitcoin pays the unlock fee out of the contract.
const unlockedBy = computed<'you' | 'them'>(() => (props.swap.leg === 'receive' ? 'you' : 'them'))

// Both branches of the contract pay this address and there is no other. Swaps
// created now always have one; a record restored from an older backup may not,
// and without it nothing on this card can build a transaction.
const needsDest = computed(
  () => !props.swap.destAddr && (canRedeem.value || canRefund.value || Boolean(props.swap.funding)),
)
const destForSpend = computed(() => destIn.value.trim() || props.swap.destAddr || '')

/**
 * Where THIS side's proceeds land, and in which coin.
 *
 * A swap has two payouts and each side only has one of them. Whoever creates
 * the Zenon HTLC is paying ZNN out and taking Bitcoin in, so their payout is
 * the Bitcoin destination; the other side is taking ZNN in, and theirs is the
 * Zenon address the HTLC pays. Showing the Bitcoin address to somebody whose
 * proceeds arrive as ZNN names their REFUND route as though it were their
 * payout — right about the swap, wrong about them.
 *
 * `zenonHtlcIsOurs` is the discriminator rather than `leg` because it is the
 * one the Go verification keys on when it decides which address the entry must
 * pay (see the ExpectRecipient branch in manager.go), and the two rules must
 * not be able to drift apart.
 */
const payout = computed(() => {
  if (props.swap.zenonHtlcIsOurs) {
    return props.swap.destAddr ? {coin: 'btc', addr: props.swap.destAddr} : null
  }
  const addr = props.swap.zenon?.selfAddress
  return addr ? {coin: tokenName(props.swap.zenon?.tokenStandard).toLowerCase(), addr} : null
})

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
 * Do something that lands on a chain, and tell the counterparty it happened.
 *
 * The announcement carries no value -- it says a chain moved, and their browser
 * goes and reads the chain. What it buys is the minute between an action and the
 * other side's next heartbeat, which is the largest source of dead time in a
 * swap.
 *
 * Only after the call succeeds: announcing a spend that was refused would send
 * the other side looking for something that is not there.
 */
async function runAndTell(what: string, fn: () => Promise<unknown>) {
  error.value = ''
  busy.value = true
  try {
    await fn()
    emit('changed')
    if (inSession.value) await session.nudge(what)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

/**
 * The Zenon wallet published something.
 *
 * All three are worth announcing, and the unlock most of all: it puts the
 * preimage on Zenon, and the counterparty who created that HTLC cannot finish
 * until they read it off the chain. Telling them to look is the whole of the
 * help that can honestly be given -- the preimage itself never travels.
 */
async function zenonDone(action: WalletAction) {
  emit('changed')
  if (!inSession.value) return
  await session.nudge(
    {
      create: 'created their Zenon HTLC',
      unlock: 'unlocked the Zenon HTLC, which publishes the preimage on Zenon',
      reclaim: 'reclaimed their ZNN from the Zenon HTLC',
    }[action],
  )
}

/**
 * The id to verify: the one typed in, or the one this swap already holds.
 *
 * A leg published through the wallet records its id immediately, because a new
 * account block is unreadable for a momentum or two and pending is the normal
 * answer. The card then says to come back and press Verify HTLC -- which was a
 * button disabled unless the user pasted an id ferry had already written down.
 *
 * The field still wins when it has something in it: pasting is how a
 * counterparty's id arrives, and that must always override.
 */
const htlcToVerify = computed(() => htlcIn.value.trim() || props.swap.zenon?.htlcId || '')

/**
 * The Zenon terms this swap is missing, and so cannot verify an HTLC against.
 *
 * The addresses are optional at creation because they are routinely filled in
 * later; verification is where they stop being optional. An expectation left
 * blank is not a check switched off but a check with no answer -- an HTLC
 * paying anybody at all would pass it -- so Go refuses to verify until these
 * are on the swap, and this form is how they get there. Each can be set once
 * and never changed: they are terms of the trade.
 */
const zenonWallet = useZenonWallet()
const TERM_LABELS = {
  selfAddress: 'your Zenon address',
  peerAddress: "the counterparty's Zenon address",
  amount: 'the agreed Zenon amount',
} as const
// Read off the swap view, where Go decided it, rather than from the fields:
// the engine's rule is what refuses to verify, and a form that read the
// fields itself would leave a record the engine calls incomplete -- a zero
// amount -- looking complete here, with no way to repair it.
const missingZenonTerms = computed(() =>
  (props.swap.missingZenonTerms ?? []).map((t) => ({key: t.key, label: TERM_LABELS[t.key]})),
)
const termIn = ref({selfAddress: '', peerAddress: '', amount: ''})
const termsReady = computed(() =>
  missingZenonTerms.value.every((t) => termIn.value[t.key].trim() !== ''),
)
function useWalletAddress() {
  if (zenonWallet.address.value) termIn.value.selfAddress = zenonWallet.address.value
}
async function saveZenonTerms() {
  await run(() =>
    api.zenonTerms(
      props.swap.id,
      {
        selfAddress: termIn.value.selfAddress.trim() || undefined,
        peerAddress: termIn.value.peerAddress.trim() || undefined,
        amount: termIn.value.amount.trim() || undefined,
      },
      settings.value,
    ),
  )
}

async function verifyZenon() {
  error.value = ''
  busy.value = true
  try {
    const r = await api.verifyZenon(props.swap.id, htlcToVerify.value, settings.value)
    znnResult.value = r.error
      ? {ok: false, text: `REJECTED: ${r.error}`}
      : {ok: true, text: 'Verified — hashlock, parties, amount and expiry all match'}
    emit('changed')
  } catch (e) {
    znnResult.value = {ok: false, text: e instanceof Error ? e.message : String(e)}
  } finally {
    busy.value = false
  }
}

/**
 * Find the Zenon HTLC id rather than asking for it. Everything needed to
 * recognise the entry is already on this card -- the address that creates this
 * leg, and the hash both legs lock to -- so the id is a lookup rather than a
 * hand-off. Every candidate is verified against the agreed terms before one is
 * adopted, and the field is still there to paste into.
 */
async function findZenon() {
  error.value = ''
  znnResult.value = null
  busy.value = true
  try {
    const r = await api.findZenon(props.swap.id, settings.value)
    htlcIn.value = r.swap?.zenon?.htlcId ?? htlcIn.value
    znnResult.value = r.error
      ? {ok: false, text: `REJECTED: ${r.error}`}
      : {
          ok: true,
          text: `Found ${r.swap?.zenon?.htlcId ?? ''} — hashlock, parties, amount and expiry all match`,
        }
    emit('changed')
  } catch (e) {
    znnResult.value = {ok: false, text: e instanceof Error ? e.message : String(e)}
  } finally {
    busy.value = false
  }
}

/**
 * Fund the contract from the connected wallet.
 *
 * An ordinary send to an ordinary address -- the contract is a P2SH output and
 * the wallet neither knows nor needs to know that it is a swap. What it saves is
 * the copy-paste of an address and an amount, which is the step where a funding
 * goes to the wrong place or for the wrong figure.
 *
 * The refresh afterwards is not cosmetic: seeing the funding is what makes the
 * app pre-sign the refund.
 */
async function fundWithWallet() {
  error.value = ''
  funding.value = ''
  if (!props.swap.contractAddr) return
  busy.value = true
  try {
    const txid = await wallet.send(props.swap.contractAddr, props.swap.amountSats)
    funding.value = txid
    fundAgain.value = false
    // Written down before anything else is attempted, and on the swap rather
    // than in this component. Everything after this line can fail -- a slow node,
    // a refresh that throws, a reload -- and every one of those used to end with
    // the card offering to send a second payment to a contract that only ever
    // spends one output.
    await api.fundingSent(props.swap.id, txid)
    await api.refresh(props.swap.id, settings.value)
    // The `funded` hand-off is not sent here. The refresh is what decides a
    // contract is funded, the reload that follows puts that on the swap, and
    // the session publishes it from there — which also covers the funding that
    // arrives from a hardware wallet, from an exchange, or while this tab was
    // closed, none of which pass through this function.
    emit('changed')
    // The nudge is a different thing and does belong here: it claims nothing,
    // it only says a chain moved. A payment this browser has just broadcast may
    // not be listed anywhere yet — including by our own node, which is why the
    // hand-off waits — but their node may well see it first, and either way
    // their burst keeps looking for the minute it takes to propagate.
    if (inSession.value) await session.nudge('paid the Bitcoin contract')
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

/**
 * Hand one of this card's values to the counterparty over the session, again.
 *
 * Again, because the session publishes each of these by itself the moment this
 * browser comes to hold it. What is left for the button is what the automatic
 * send cannot cover: a relay that dropped the event, or a counterparty who
 * joined and missed it. Repeating a hand-off is free and is the commonest way
 * out of a stalled swap.
 *
 * The value itself is not passed: the module reads it out of the stored swap, so
 * what travels is what this browser holds rather than what the page is
 * rendering.
 */
async function sendOverSession(type: HandoffType, describe: string) {
  await session.send({type}, describe)
  // Whoever opened this panel opened it to send the contract again, and has.
  // Dropping the override rather than closing outright leaves the panel open if
  // the send did not take — the one case where there is still something to do
  // here.
  if (type === 'contract') contractToggled.value = null
}

/** Whether the session has already carried this exact value, so the button can
 *  offer a repeat rather than claim to be the first time. */
function sent(type: HandoffType, value: string | undefined): boolean {
  return session.wasSent(props.swap.id, type, value ?? '')
}

// Autopilot for this swap: armed by the user, stopped by the first thing that
// goes wrong. See core/composables/useAutoMode.ts for what it will and will
// not do; this is the half of it that lives where the buttons are.
const autoOn = computed(() => autoMode.isOn(props.swap.id))
const autoHalted = computed(() => autoMode.reason(props.swap.id))

/**
 * Whether autopilot may fund yet -- a question about who goes first.
 *
 * The initiator's leg has the longer timelock and must be locked first, because
 * the participant needs a window in which the initiator can no longer walk away
 * with both. Funding out of turn is not fatal -- the timelock still returns the
 * money -- but it means paying into a swap where the other side has committed
 * nothing, then waiting out a deadline to undo it.
 *
 * A person with the card in front of them generally waits; a driver has to be
 * told. So when this contract is the PARTICIPANT's leg, funding waits for the
 * initiator's Zenon HTLC to exist and to have passed verification against this
 * browser's own node.
 */
const autoMayFund = computed(
  () => props.swap.btcLegIsInitiators || Boolean(props.swap.zenon?.verified),
)

/**
 * Take the outstanding step, if autopilot is armed and there is one to take.
 * Two steps live here; the Zenon ones drive themselves from inside ZenonWallet,
 * where the wallet checks already are.
 *
 * Funding runs only through the same function the button calls -- which records
 * the broadcast before anything else can fail, precisely so a second payment
 * cannot be sent to a contract that spends one output -- and it opens UniSat,
 * which asks. Autopilot gets the amount and the address right; the wallet still
 * gets the last word.
 *
 * Redeeming is the safe one and, oddly, the one most often left undone: signed
 * in this page with the swap's own key, no wallet at all, paying an address the
 * user chose at creation. A swap sitting redeemable-but-not-redeemed is money
 * waiting on somebody to notice a button.
 *
 * It waits for a confirmation, though. A redeem PUBLISHES the preimage, and
 * against a funding still in a mempool that is a trade the other side can undo
 * for free: they replace the payment, keep their Bitcoin, and hold the secret
 * that opens the Zenon leg. One block makes that expensive rather than free. The
 * button beside it stays live and unconditional -- somebody watching a specific
 * swap may have a reason to accept that risk.
 *
 * Refunding is deliberately absent -- see the note in useAutoMode.
 */
async function autoStep() {
  if (!autoOn.value || busy.value || networkMismatch.value) return

  if (needsFunding.value && !fundingSent.value && wallet.connected.value && autoMayFund.value) {
    if (!autoMode.claim(props.swap.id, 'fund')) return
    await fundWithWallet()
    if (error.value) autoMode.halt(props.swap.id, error.value)
    return
  }

  if (canRedeem.value && destForSpend.value && props.swap.funding?.confirmed) {
    if (!autoMode.claim(props.swap.id, 'redeem')) return
    await runAndTell('redeemed the Bitcoin contract, which publishes the preimage on Bitcoin', () =>
      api.redeem(props.swap.id, destForSpend.value, settings.value),
    )
    if (error.value) autoMode.halt(props.swap.id, error.value)
  }
}

// The swap arriving from a reload with a new field on it is what makes a step
// possible, and nothing in this component is called at that moment — so the
// trigger is the swap itself changing, plus the two things outside it that
// decide whether a step can run at all.
watch([() => props.swap, autoOn, () => wallet.connected.value], () => void autoStep(), {
  immediate: true,
})

/**
 * Say everything about this swap again, and ask them to do the same. It emits
 * `changed` like any other action: their answers arrive as ordinary hand-offs
 * and can update this swap.
 */
async function resyncSession() {
  await run(async () => {
    // Attach first when the session is about some other swap. Everywhere else
    // that offers to attach does so as a separate press, because those are about
    // publishing a value and putting one in the wrong room hands the
    // counterparty something they will refuse. This is the button a stalled swap
    // sends somebody to, and by then the panels carrying that offer may all have
    // closed. Not silent: attaching writes its own line in the transcript.
    if (!inSession.value) attachSession()
    await session.resync()
  })
}

async function showOffer() {
  try {
    const r = await api.offer(props.swap.id)
    offer.value = r.offer
  } catch (e) {
    offer.value = `error: ${e instanceof Error ? e.message : String(e)}`
  }
}

async function downloadRecovery() {
  error.value = ''
  try {
    const rec = await api.recovery(props.swap.id)
    download(`swap-${props.swap.id}-recovery.json`, JSON.stringify(rec, null, 2))
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}
</script>

<template>
  <Card class="min-w-0">
    <!-- The header answers "what is this trade", not "what is its id". The id
         is still there, last, for when you need to name the swap to someone. -->
    <CardHeader class="gap-3">
      <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
        <span class="flex items-center gap-2 text-sm font-semibold">
          <span class="font-mono tabular-nums">{{ trade.from }}</span>
          <ArrowRightIcon class="size-3.5 text-muted-foreground" aria-hidden="true" />
          <span class="font-mono tabular-nums">{{ trade.to }}</span>
        </span>
        <Badge :variant="stateVariant">{{ swap.state.replace('_', ' ') }}</Badge>
        <Badge v-if="networkMismatch" variant="warning">{{ swap.network }}</Badge>
        <span class="flex-1" />
        <span class="text-xs text-muted-foreground">{{ swap.role }}</span>
        <Address :address="swap.id" :start="6" :end="4" class="text-xs" />
      </div>
      <StepBar :steps="STAGES" :current="at" :done="settled" />
    </CardHeader>

    <CardContent class="grid min-w-0 gap-4">
      <!-- The loudest thing on the card, because it is the only thing most
           visits need: what happens next, and whose move it is. -->
      <div
        class="flex items-center gap-2.5 rounded-lg border p-3"
        :class="
          theirMove
            ? 'border-border bg-muted/40'
            : at === 4
              ? 'border-success/40 bg-success/5'
              : 'border-primary/50 bg-primary/10'
        "
      >
        <component
          :is="theirMove ? ClockIcon : at === 4 ? CircleCheckIcon : ArrowRightIcon"
          class="size-4 shrink-0"
          :class="[
            theirMove ? 'text-muted-foreground' : at === 4 ? 'text-success' : 'text-primary',
            // A clock that does not move is a stopped clock, which is the exact
            // impression to avoid on a card that is working.
            theirMove ? 'motion-safe:animate-pulse' : '',
          ]"
          aria-hidden="true"
        />
        <p class="min-w-0 flex-1 text-sm font-semibold">{{ step }}</p>
        <Waiting
          v-if="theirMove"
          class="shrink-0 text-xs text-muted-foreground"
          tone="success"
          label="their move"
        />
      </div>

      <!-- The one part of Auto Mode that earns space of its own: the toggle
           itself lives compactly with the other buttons near the foot of the
           card (see the actions row below), but a halt is news, not a setting
           — it means autopilot saw something wrong and stopped, and that is
           worth more than an icon somebody has to think to hover. -->
      <p v-if="autoHalted" class="-mt-2 flex items-start gap-1.5 text-xs text-warning">
        <TriangleAlertIcon class="mt-0.5 size-3 shrink-0" aria-hidden="true" />
        <span class="min-w-0 flex-1">
          Auto Mode stopped: {{ autoHalted }} — switch it off and on again to retry.
        </span>
      </p>

      <div
        v-if="networkMismatch"
        class="flex items-start gap-1.5 rounded-md border border-warning/40 bg-warning/5 p-3 text-sm text-warning"
      >
        <p class="min-w-0 flex-1">
          This swap is on <span class="font-mono">{{ swap.network }}</span> and you are set to
          <span class="font-mono">{{ settings.network }}</span
          >. Anything that reaches a chain is switched off.
        </p>
        <InfoTip variant="warn" label="Why actions are disabled">
          Every address, fee lookup and broadcast would go to the wrong chain. Switch network under
          Nodes to act on it. Auditing a contract and the offer string still work — neither reaches
          a node.
        </InfoTip>
      </div>

      <!-- Only what is needed at a glance. Everything else is one click away,
           because ten rows of hex on every card is what made this hard to read. -->
      <DataList dense>
        <DataRow v-if="swap.contractAddr" label="contract">
          <Address :address="swap.contractAddr" :start="10" :end="8" wrap />
        </DataRow>
        <DataRow v-if="swap.funding" label="funded with">
          <span class="font-mono text-xs">{{ sats(swap.funding.value) }}</span>
          <Badge v-if="swap.fundingShort" variant="destructive" class="ml-2">
            short of the agreed {{ sats(swap.amountSats) }}
          </Badge>
          <!-- How settled it is, not merely that it arrived. A payment still in
               a mempool is one its sender can replace, and the swap's other leg
               should not be acted on against one. -->
          <Badge v-if="fundingDepth" :variant="fundingDepth.variant" class="ml-2">
            {{ fundingDepth.text }}
          </Badge>
          <InfoTip label="What the confirmations mean" side="right">
            <p>
              Counted as of the last Refresh from chain, including the block the payment is in — so
              a funding just mined reads one. It stops at six, which is where the rest of Bitcoin
              stops treating a reversal as worth worrying about.
            </p>
            <p>
              Until it is confirmed the payment is only in a mempool, where whoever sent it can
              still replace it. Wait for it before acting on the Zenon leg.
            </p>
          </InfoTip>
          <TxRef :txid="swap.funding.txid" :vout="swap.funding.vout" label="funding transaction" />
        </DataRow>
        <DataRow label="refund after">
          <span class="font-mono text-xs">{{ until(swap.lockTimeAt) }}</span>
          <span class="mx-2 font-mono text-xs text-muted-foreground">
            {{ stamp(swap.lockTimeAt) }}
          </span>
          <InfoTip label="What the timelock does" side="right">
            If the swap stalls, this is when you can take your Bitcoin back. Until then the contract
            can only be opened with the secret.
          </InfoTip>
        </DataRow>
        <!-- Where the money actually lands, on the face of the card rather than
             folded away in Technical details — and in the coin this side is
             actually being paid in, which is the half that was wrong when this
             row only ever named the Bitcoin address.
             It is NOT a defence against the counterparty. Each leg already
             refuses to be acted on unless it pays this side: the Bitcoin
             contract is rejected at audit unless its redeem branch is locked to
             this swap's own key, and the Zenon entry is rejected at
             verification unless its hashLocked address is the one below. What
             neither checks is whether the address in THIS record is still one
             the user holds — a restored backup or a retired wallet settles
             perfectly correctly into coins nobody can spend, and this was the
             last value on the card nobody was shown before the other leg got
             locked. -->
        <DataRow v-if="payout" :label="`${payout.coin} payout`">
          <Address :address="payout.addr" :start="10" :end="8" wrap />
          <InfoTip label="Where your proceeds end up" side="right">
            <template v-if="payout.coin === 'btc'">
              <p>
                Both ways out of the contract pay this address and there is no other — redeeming
                with the secret and reclaiming after the timelock both send here.
              </p>
              <p>
                The counterparty cannot point it somewhere else. Their contract is refused at audit
                unless its redeem branch is locked to this swap's own key, so this browser is the
                only thing that can spend it, and it spends to here.
              </p>
            </template>
            <template v-else>
              <p>
                The Zenon HTLC pays this address when it is unlocked, whoever presses the button —
                the contract pays its hashLocked address rather than its caller.
              </p>
              <p>
                The counterparty cannot point it somewhere else without the entry being refused:
                verification checks the address it really pays against this one before this swap
                will treat the leg as good.
              </p>
            </template>
            <p>Check it is an address you still control before locking anything on your own leg.</p>
          </InfoTip>
        </DataRow>
        <!-- The badge holds a whole sentence when verification fails, so it has
             to wrap inside the row rather than run off the side of the card.
             `min-w-0` is the load-bearing half: a flex child defaults to
             min-width:auto, which refuses to shrink below its content, so
             without it the text pushes the badge past the card and off the
             page however the wrapping is set. -->
        <DataRow v-if="swap.zenon?.htlcId" label="zenon">
          <Badge
            :variant="zenonBad ? 'destructive' : zenonPending ? 'outline' : 'success'"
            class="inline-block h-auto max-w-full min-w-0 py-1 text-left leading-snug break-words whitespace-normal"
          >
            <template v-if="zenonPending">
              <!-- The dots are the half that says this is still being looked
                   at, so the sentence no longer has to: "this checks again
                   until a node answers" was the words repeating what the
                   animation is for.
                   What the sentence does carry is the last thing that stopped
                   the check, when there was one. Pending has two causes now —
                   an entry too new to read, and one read but not judgeable
                   because a check could not be run — and "takes a momentum or
                   two" is a wrong explanation of the second, aimed at a wait
                   that is not the one happening. -->
              <Waiting
                :label="
                  swap.zenon.verifyError
                    ? `HTLC not checked yet — ${swap.zenon.verifyError}`
                    : 'HTLC awaiting confirmation — a new account block takes a momentum or two.'
                "
              />
            </template>
            <template v-else-if="zenonBad">
              HTLC not verified — {{ swap.zenon.verifyError ?? '' }}
            </template>
            <template v-else>HTLC verified</template>
          </Badge>
          <!-- The unlock, which is the news the badge above can never carry: it
               is a claim about an entry that EXISTS, and an unlock deletes the
               entry. Shown to both sides, each off the only record its own
               browser has — see zenonUnlock. Wrapped the same way and for the
               same reason as the badge above. -->
          <Badge
            v-if="zenonUnlock"
            variant="success"
            class="ml-2 inline-block h-auto max-w-full min-w-0 py-1 text-left leading-snug break-words whitespace-normal"
          >
            {{ zenonUnlock.text }}
          </Badge>
          <InfoTip v-if="zenonUnlock" label="What unlocked means" side="right">
            <template v-if="zenonUnlock.mine">
              <p>
                You published the transaction that opened this HTLC with the preimage, which paid
                its ZNN to your Zenon address and deleted the entry from the contract.
              </p>
              <p>
                That transaction also published the preimage. The counterparty reads it off Zenon
                and uses it to take the Bitcoin — which is theirs to do now, and needs nothing
                further from you.
              </p>
              <p>
                This records what this browser sent, not what a node has since confirmed. A new
                account block takes a momentum or two to appear.
              </p>
            </template>
            <template v-else>
              <p>
                The counterparty opened this HTLC with the preimage and took its ZNN. The entry is
                gone from the contract — that is what an unlock does — so nothing can be read back
                off it.
              </p>
              <p>
                The preimage was recovered from the transaction that did it and checked against this
                swap's hash, so it is now on this card and the Bitcoin contract can be redeemed with
                it.
              </p>
            </template>
          </InfoTip>
          <TxRef
            v-if="swap.zenon.unlockHash"
            :txid="swap.zenon.unlockHash"
            label="Zenon unlock transaction"
          />
        </DataRow>
      </DataList>

      <!-- Step: the counterparty's pubkey hash -->
      <section v-if="needsPkh" class="grid gap-2 rounded-lg border border-primary/40 p-3">
        <h3 class="flex items-center gap-1.5 text-sm font-semibold">
          Paste their pubkey hash
          <InfoTip label="Where the pubkey hash comes from">
            Send them your offer string (the button below) and they send back a 40-character hex
            pubkey hash from their own swap card. It names the key allowed to take the other branch
            of the contract you are about to build.
          </InfoTip>
        </h3>
        <Input
          v-model="pkhIn"
          spellcheck="false"
          autocapitalize="none"
          autocomplete="off"
          placeholder="40 hex chars"
          class="font-mono"
        />
        <Button
          class="justify-self-start"
          :disabled="busy || !pkhIn.trim()"
          @click="run(() => api.counterparty(swap.id, pkhIn.trim()))"
        >
          Build contract
        </Button>
      </section>

      <!-- Hand-off: your pubkey hash, which they need before a contract can
           exist at all. It used to be reachable only by opening "Technical
           details", which made the joiner's first move the one move the card
           never mentioned. -->
      <Handoff
        v-if="handOffPkh"
        title="Send them your pubkey hash"
        hint="They cannot build the contract without it — it names the key allowed to take the redeem branch, which is yours."
        tip-label="What your pubkey hash is"
        :value="swap.key?.pkhHex ?? ''"
      >
        <template #tip>
          <p>
            The hash of this swap's own throwaway public key, made in this browser. It is public
            data: it identifies the key that may claim the contract, and reveals nothing that could
            spend one.
          </p>
          <p>They paste it into their card, which builds the contract and funds it.</p>
        </template>
        <SessionSend
          :in-session="inSession"
          :session-active="session.active.value"
          :sent="sent('pkh', swap.key?.pkhHex)"
          label="Send it over the session"
          @send="sendOverSession('pkh', 'Sent your pubkey hash')"
          @attach="attachSession"
        />
      </Handoff>

      <!-- Step: audit their contract -->
      <section v-if="needsAudit" class="grid gap-2 rounded-lg border border-primary/40 p-3">
        <h3 class="flex items-center gap-1.5 text-sm font-semibold">
          Check their contract
          <InfoTip label="What gets checked">
            They fund the Bitcoin side. Pasting their contract hex here checks that it really is
            redeemable by you, and that its locktime sits on the correct side of your Zenon leg —
            before you send anything on Zenon. The check runs in this page and reaches no node, so
            it works entirely offline.
          </InfoTip>
        </h3>
        <Textarea
          v-model="contractIn"
          rows="2"
          spellcheck="false"
          autocapitalize="none"
          placeholder="contract hex"
          class="font-mono text-xs"
        />
        <Button
          class="justify-self-start"
          :disabled="busy || !contractIn.trim()"
          @click="run(() => api.audit(swap.id, contractIn.trim()))"
        >
          Audit contract
        </Button>
      </section>

      <!-- Hand-off: the contract itself. They audit this before they will put
           anything on Zenon, so it is shown from the moment it exists — and it
           stays up after funding, because "I never got the contract" is the
           most common way a funded swap stalls. -->
      <Handoff
        v-if="handOffContract"
        v-model:open="contractOpen"
        title="Send them the contract"
        hint="They paste this into their own card to check it really is redeemable by them, and that its locktime sits on the safe side of the Zenon leg. They will not act until they have."
        tip-label="What the contract hex is"
        done-note="Sent over the session — reopen it if they need it again."
        :value="swap.contractHex ?? ''"
      >
        <template #tip>
          <p>
            The full swap script: the hashlock, both pubkey hashes and the locktime. It is public —
            it is what the contract address is the hash of — and it contains no secret and no key.
          </p>
          <p>
            Auditing it is what tells them the address you funded is really the contract you both
            agreed, and it runs entirely in their browser.
          </p>
        </template>
        <SessionSend
          :in-session="inSession"
          :session-active="session.active.value"
          :sent="sent('contract', swap.contractHex)"
          label="Send it over the session"
          @send="sendOverSession('contract', 'Sent the contract')"
          @attach="attachSession"
        />
      </Handoff>

      <!-- Step: fund it -->
      <section
        v-if="needsFunding"
        class="grid gap-3 rounded-lg border border-primary/50 bg-primary/5 p-3"
      >
        <h3 class="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm font-semibold">
          Send
          <span class="font-mono">{{ sats(swap.amountSats) }}</span>
          <span class="font-normal text-muted-foreground">({{ btc(swap.amountSats) }})</span>
          to this address
          <InfoTip label="How to fund it">
            Send from <em>any</em> Bitcoin wallet — hardware, mobile, desktop or exchange. It is an
            ordinary address and the wallet needs to know nothing about swaps. Then press Refresh
            from chain.
          </InfoTip>
        </h3>
        <!-- The one string a user must reproduce exactly, so it is shown in
             full and copied with a button rather than long-pressed. -->
        <div class="flex items-start gap-2 rounded-md bg-background p-3">
          <code class="min-w-0 flex-1 font-mono text-sm break-all">{{ swap.contractAddr }}</code>
          <CopyButton :value="swap.contractAddr ?? ''" size="icon-sm" />
        </div>

        <!-- The same send, with the address and the amount already filled in.
             Copying an address by hand is where a funding goes to the wrong
             place; copying an amount by hand is where it goes for the wrong
             figure. -->
        <div class="grid gap-2 border-t border-primary/20 pt-3">
          <!-- A payment is already on its way to this address. The send button
               is removed rather than merely disabled: this contract spends
               exactly one output, so a second payment to it is not a duplicate
               that resolves itself — it is money that has to be dug back out by
               hand with the recovery file. -->
          <template v-if="fundingSent && !fundAgain">
            <div class="grid gap-1.5 rounded-md border border-success/40 bg-success/5 p-3">
              <p class="flex items-center gap-1.5 text-sm font-semibold text-success">
                <CircleCheckIcon class="size-3.5 shrink-0" aria-hidden="true" />
                Payment broadcast
              </p>
              <code class="font-mono text-xs break-all">{{ fundingSent }}</code>
              <Waiting
                class="text-sm text-muted-foreground"
                label="Waiting for a node to list it — the confirmations appear here on their own.
                       Do not send a second payment."
              />
            </div>
            <Button variant="ghost" size="sm" class="justify-self-start" @click="fundAgain = true">
              It never arrived — let me send again
            </Button>
          </template>
          <template v-else>
            <p
              v-if="fundAgain && fundingSent"
              class="flex items-start gap-1.5 rounded-md border border-warning/40 bg-warning/5 p-2.5 text-sm text-warning"
            >
              <span class="min-w-0 flex-1">
                <span class="font-mono break-all">{{ fundingSent }}</span> was already broadcast to
                this address. Send again only if you have checked it is not in a mempool — the
                contract spends one output, and a second payment has to be recovered separately.
              </span>
            </p>
            <WalletConnect />
            <div v-if="wallet.connected.value" class="flex flex-wrap items-center gap-2">
              <Button :disabled="chainBlocked" @click="fundWithWallet">
                Send {{ sats(swap.amountSats) }} with UniSat
              </Button>
              <span class="text-xs text-muted-foreground">
                to <span class="font-mono">{{ swap.contractAddr }}</span>
              </span>
            </div>
          </template>
        </div>
      </section>

      <!-- The create is withheld, and why. This is the participant in a swap
           the Bitcoin side initiated: their ZNN answers a payment that is not
           yet real, and locking it against one the sender can still replace
           hands them the ZNN for nothing. Outside the Zenon section on purpose:
           that section waits for a funding record to exist at all, and the
           commonest reason to be waiting is that nothing has been paid yet,
           which is exactly when the explanation is owed. -->
      <Note
        v-if="
          swap.contractAddr &&
          swap.zenonHtlcIsOurs &&
          !swap.zenon?.htlcId &&
          live &&
          swap.fundingCommitBlocker
        "
        variant="warn"
        summary="Not locking ZNN yet: their Bitcoin funding is not settled"
      >
        <p>
          {{
            swap.fundingCommitBlocker.charAt(0).toUpperCase() + swap.fundingCommitBlocker.slice(1)
          }}. Your Zenon HTLC answers that payment, so it waits until the payment is mined and
          covers the agreed amount. A payment still in a mempool is one its sender can replace
          &mdash; and they already hold the secret that would open your HTLC.
        </p>
        <p class="mt-2">
          Refresh keeps checking. The create appears &mdash; and runs by itself under Auto Mode
          &mdash; once it is settled. No znn-cli create command is printed for this leg at any
          point: a terminal runs no check, so the wallet button is the way to create it.
        </p>
      </Note>

      <!-- The Zenon leg -->
      <section v-if="showZenon" class="grid min-w-0 gap-3 rounded-lg border border-border p-3">
        <h3 class="flex flex-wrap items-center gap-1.5 text-sm font-semibold">
          The Zenon side
          <InfoTip label="Where the signing happens">
            <p>
              Creating and unlocking a Zenon HTLC are signed operations, and this page holds no
              Zenon key. It builds the account block; the signing happens in your wallet.
            </p>
            <p>
              With the Syrius browser extension that is a button: the block goes to the extension,
              which shows it to you, signs it with its own key, mines its own plasma and publishes
              through its own node. Without it, the same operation is the printed
              <code>znn-cli</code> command &mdash; except for a create that answers the
              counterparty's Bitcoin funding, which is never printed as a command: a terminal runs
              no check, and that check is the whole point. The terms are printed instead.
            </p>
          </InfoTip>
          <span class="flex-1" />
          <!-- The step that strands a first-time user: the commands below need
               tooling that is not the wallet they already have, and which one
               it is takes a paragraph to explain. -->
          <RouterLink
            :to="{name: 'docs', hash: '#what-you-need'}"
            class="text-xs font-normal text-primary underline-offset-4 hover:underline"
          >
            Which Zenon tooling
          </RouterLink>
        </h3>

        <!-- The hashlock, for the side that has to type it into htlc.create.
             It is the one argument of that command which cannot be read off
             anything else on the card, and pasting the wrong one produces an
             HTLC that looks right and can never be unlocked. -->
        <!-- Tight on purpose: this is a label and one line of hex, and the
             default panel padding plus an icon-sm copy button wrapped 78px of
             box around 32px of text. The button is the half that did it — a
             32px control beside a 16px line sets the row height by itself — so
             it comes down with the padding rather than after it. -->
        <div
          v-if="showHashlock"
          class="grid gap-1 rounded-md border border-border bg-muted/40 px-3 py-2"
        >
          <span class="flex flex-wrap items-center gap-1.5 text-xs font-semibold">
            Hashlock for your HTLC
            <InfoTip label="Where the hashlock comes from">
              <p>
                The initiator hashed a secret nobody has seen and both legs lock to that hash, which
                is what makes the two legs open with one preimage.
              </p>
              <p>
                It is already filled into the command below — this row is here so you can check the
                command against the contract you just audited.
              </p>
            </InfoTip>
          </span>
          <div class="flex items-start gap-2">
            <code class="min-w-0 flex-1 font-mono text-xs break-all">{{ swap.secretHashHex }}</code>
            <CopyButton :value="swap.secretHashHex" class="size-6 shrink-0" />
          </div>
        </div>

        <!-- The same operation as the commands below, done in the wallet the
             user already has. It is offered first because it is the path that
             cannot mistype a hashlock, and the commands stay because they are
             the answer for every other wallet and the explanation for this
             one. -->
        <ZenonWallet
          v-if="zenonAction"
          :swap="swap"
          :action="zenonAction"
          :disabled="chainBlocked"
          :auto="autoOn"
          @done="zenonDone(zenonAction)"
        />
        <ZenonWallet
          v-if="zenonReclaimable"
          :swap="swap"
          action="reclaim"
          :disabled="chainBlocked"
          :auto="autoOn"
          @done="zenonDone('reclaim')"
        />

        <!-- Behind the toggle at the foot of the card. The commands were the
             only way to drive this leg when they were written and are now one of
             two, so they are kept in full and shown on request rather than by
             default. -->
        <div v-if="showCli" class="min-w-0">
          <Button variant="outline" size="sm" @click="showCommands = !showCommands">
            {{ showCommands ? 'Hide' : 'Show' }} the command to run
            {{ zenonAction || zenonReclaimable ? 'instead' : '' }}
          </Button>
          <!-- The commands DO scroll rather than wrap: they are line-oriented
               shell input, and a wrapped command line is one somebody copies
               wrong. Which makes every box between this <pre> and the page a
               load-bearing min-w-0 — a grid or flex item defaults to min-width
               auto, so without one at EVERY level the widest command line is
               pushed all the way out through the card and widens the page
               instead of scrolling inside its own box. -->
          <div v-if="showCommands" class="relative mt-2 w-full min-w-0">
            <pre
              class="max-w-full overflow-x-auto rounded-md bg-muted/60 p-3 font-mono text-xs leading-relaxed"
              >{{ commands }}</pre>
            <CopyButton :value="commands" size="icon-sm" class="absolute top-2 right-2" />
          </div>
        </div>

        <!-- Everything that asks a node about this leg, for exactly as long as
             the leg has an open question. A verified HTLC has none: the row
             disappears and the badge in the summary above carries the verdict
             from then on. -->
        <!-- Terms the swap was created without. Verification refuses until they
             are here, and says so; this is the way in, ahead of the Verify row
             it unblocks. -->
        <div
          v-if="live && !swap.zenon?.verified && missingZenonTerms.length"
          class="grid gap-2 rounded-md border border-warning/40 bg-warning/5 p-3"
        >
          <p class="text-sm text-warning">
            The Zenon HTLC cannot be verified yet: a check with nothing to compare against would
            pass an HTLC that pays anybody. Missing here:
            {{ (swap.missingZenonTerms ?? []).map((t) => t.reason).join('; ') }}. Each is a term of
            the trade and cannot be changed once added.
          </p>
          <template v-for="t in missingZenonTerms" :key="t.key">
            <div v-if="t.key === 'selfAddress'" class="flex min-w-0 flex-wrap gap-2">
              <Input
                v-model="termIn.selfAddress"
                spellcheck="false"
                autocapitalize="none"
                autocomplete="off"
                placeholder="your Zenon address (z1…), where their HTLC pays you"
                class="min-w-56 flex-1 font-mono"
              />
              <Button
                v-if="zenonWallet.connected.value && zenonWallet.address.value"
                variant="outline"
                @click="useWalletAddress"
              >
                Use the connected wallet's address
              </Button>
            </div>
            <Input
              v-else-if="t.key === 'peerAddress'"
              v-model="termIn.peerAddress"
              spellcheck="false"
              autocapitalize="none"
              autocomplete="off"
              placeholder="the counterparty's Zenon address (z1…), which your HTLC pays"
              class="font-mono"
            />
            <Input
              v-else
              v-model="termIn.amount"
              inputmode="decimal"
              autocomplete="off"
              placeholder="the agreed Zenon amount, e.g. 10 or 1.25"
              class="font-mono"
            />
          </template>
          <Button
            class="justify-self-start"
            :disabled="busy || !termsReady"
            @click="saveZenonTerms"
          >
            Add to the swap
          </Button>
        </div>

        <template v-if="needsVerify">
          <p v-if="!hasZenon" class="flex items-start gap-1.5 text-sm text-warning">
            <span class="min-w-0 flex-1">
              No Zenon node is set, so this HTLC cannot be verified. Add one under Nodes.
            </span>
            <InfoTip variant="warn" label="Why verification needs a node">
              Without one you would be taking the counterparty's word for the hashlock, the parties,
              the amount and the expiry — the four things a dishonest HTLC gets wrong.
            </InfoTip>
          </p>
          <div class="flex min-w-0 flex-wrap gap-2">
            <Input
              v-model="htlcIn"
              spellcheck="false"
              autocapitalize="none"
              autocomplete="off"
              placeholder="Zenon HTLC id (hash)"
              class="min-w-56 flex-1 font-mono"
            />
            <Button :disabled="chainBlocked || !hasZenon || !htlcToVerify" @click="verifyZenon">
              Verify HTLC
            </Button>
            <!-- Nobody has to carry the id across if the chain already holds it.
                 The address that creates this leg and the hash both legs lock to
                 are both on this card, and a create is a send to the HTLC
                 contract carrying that hash — so the id is a lookup. It is
                 offered beside the field rather than instead of it: a search
                 needs a node and a confirmed create, and pasting always works. -->
            <Button variant="outline" :disabled="chainBlocked || !hasZenon" @click="findZenon">
              <SearchIcon />
              Find it
              <InfoTip label="How the id is found">
                Ferry reads the chain of the address that creates this leg, looks for the
                transaction that creates an HTLC locked to this swap's hash, and checks what it
                finds against the agreed token, amount, parties and expiry before accepting it. The
                id is that transaction's own hash. Nothing is taken on trust — a candidate that
                fails the check is reported, not adopted.
              </InfoTip>
            </Button>
          </div>
          <p
            v-if="znnResult"
            class="text-sm"
            :class="znnResult.ok ? 'text-success' : 'text-destructive'"
          >
            {{ znnResult.text }}
          </p>
        </template>
        <!-- Their side has to verify this id against a node before they act on
             it, and they cannot do that until they have it. It goes by itself
             once this browser has verified it; this is how to send it again. -->
        <SessionSend
          v-if="swap.zenon?.htlcId && swap.zenonHtlcIsOurs"
          :in-session="inSession"
          :session-active="session.active.value"
          :sent="sent('zenon', swap.zenon.htlcId)"
          label="Send this HTLC id over the session"
          @send="sendOverSession('zenon', 'Sent your Zenon HTLC id')"
          @attach="attachSession"
        />

        <div v-if="needsSecret" class="grid gap-2 border-t border-border pt-3">
          <h4 class="flex items-center gap-1.5 text-sm font-semibold">
            Paste the preimage they revealed
            <InfoTip label="Where the preimage comes from">
              <p>
                You created the Zenon HTLC and the counterparty holds the secret. When they unlock
                it the preimage becomes public on Zenon — but unlocking deletes the entry, so it
                cannot simply be read back off it afterwards.
              </p>
              <p>
                Ferry looks for it every minute: when the entry goes missing it finds the
                transaction that removed it and reads the preimage out of that, so this field is
                usually filled in for you. It is here for when that search comes up empty — and
                either way the value is only accepted if it hashes to this swap's secret hash.
              </p>
              <p>
                It is never sent over a session, by either side. A preimage that arrives in a
                message is a preimage somebody could have made up; one taken off the chain was
                accepted by the contract that is holding the money.
              </p>
            </InfoTip>
          </h4>
          <Input
            v-model="secretIn"
            spellcheck="false"
            autocapitalize="none"
            autocomplete="off"
            placeholder="64 hex chars"
            class="font-mono"
          />
          <Button
            class="justify-self-start"
            :disabled="busy || !secretIn.trim()"
            @click="run(() => api.setSecret(swap.id, secretIn.trim()))"
          >
            Submit preimage
          </Button>
        </div>
      </section>

      <!-- No destination: nothing on this card can build a transaction. Swaps
           created now always carry one, so this is the restored-from-an-old-
           backup case, and the fix belongs where the problem is visible. -->
      <section
        v-if="needsDest"
        class="grid gap-2 rounded-lg border border-destructive/50 bg-destructive/5 p-3"
      >
        <h3 class="flex items-center gap-1.5 text-sm font-semibold text-destructive">
          This swap has no payout address
          <InfoTip variant="warn" label="Why this happened">
            Both ways out of the contract pay one address of yours and this record names none, so
            the refund could not be pre-signed and nothing here can build a spend. Records created
            by this build always carry one — this is an older backup. Give an address now and it is
            saved on the swap.
          </InfoTip>
        </h3>
        <Input
          v-model="destIn"
          spellcheck="false"
          autocapitalize="none"
          autocomplete="off"
          placeholder="your own BTC address on this swap's network"
          class="font-mono"
        />
      </section>

      <!-- What emptying this contract costs, beside the button that empties it.
           A swap can be perfectly correct and still hold less than it costs to
           move, and there is no point discovering that from a refused
           signature. -->
      <UnlockCost
        v-if="unlockCost || unlockLoading"
        :cost="unlockCost"
        :loading="unlockLoading"
        :error="unlockError"
        :unlocked-by="unlockedBy"
      />

      <!-- Actions, loudest first: the one thing this swap can do now, then the
           ones that are always available. -->
      <div class="flex flex-wrap gap-2">
        <!-- A redeem is announced because it is how the preimage reaches the
             other side: it goes into the witness of this transaction, and their
             browser reads it out of there. Telling them to look is not telling
             them the secret. -->
        <Button
          v-if="canRedeem"
          :disabled="chainBlocked || !destForSpend"
          @click="
            runAndTell(
              'redeemed the Bitcoin contract, which publishes the preimage on Bitcoin',
              () => api.redeem(swap.id, destForSpend, settings),
            )
          "
        >
          Redeem with secret
        </Button>
        <Button
          v-if="canRefund"
          variant="destructive"
          :disabled="chainBlocked || !destForSpend"
          @click="
            runAndTell('refunded the Bitcoin contract — this swap is over', () =>
              api.refund(swap.id, destForSpend, settings),
            )
          "
        >
          Refund (timelock expired)
        </Button>
        <Button
          variant="outline"
          size="sm"
          :disabled="chainBlocked"
          @click="run(() => api.refresh(swap.id, settings))"
        >
          <RefreshCwIcon />
          Refresh from chain
        </Button>
        <!-- The counterparty's half of the same idea. Refresh re-asks the
             chain; this re-asks the other browser. It is offered whenever a
             session is attached, not only when something looks wrong, because
             the state it repairs is the one that looks like nothing is wrong:
             each side believing it has already said everything it holds. -->
        <Button
          v-if="session.active.value"
          variant="outline"
          size="sm"
          :disabled="busy"
          @click="resyncSession"
        >
          <RefreshCcwDotIcon />
          Re-sync with counterparty
          <InfoTip label="What re-syncing does">
            <p>
              Both sides throw away their record of what has already been sent and send all of it
              again — your pubkey hash, the contract, the HTLC id, the funding — and everything that
              arrives goes through the same verification it always does.
            </p>
            <p>
              It is for the stall that looks like nothing: a reload, a dropped relay message or a
              rejoined session can leave one side sure it delivered a value the other never
              received, and neither card can tell. Re-sending costs a line in the transcript, so
              there is no reason not to when a swap has stopped moving.
            </p>
            <p>The preimage is not included, because it is never sent over a session at all.</p>
            <p v-if="!inSession">
              The open session is currently about a different swap. This will point it at this one
              first, which is the same thing the Send buttons offer to do.
            </p>
          </InfoTip>
        </Button>
        <!-- An offer for a swap that has already settled is not good for anything. -->
        <Button v-if="live" variant="outline" size="sm" @click="showOffer">
          <ShareIcon />
          Offer string
        </Button>
        <Button v-if="!urgeRecovery" variant="outline" size="sm" @click="downloadRecovery">
          <DownloadIcon />
          Recovery file
        </Button>
        <!-- Always offered: archiving is now the only way in or out of
             History, in either direction. Only the label turns on which way
             this particular press would move it. -->
        <Button
          variant="outline"
          size="sm"
          :disabled="busy"
          @click="run(() => api.archive(swap.id, !swap.archived))"
        >
          <component :is="swap.archived ? ArchiveRestoreIcon : ArchiveIcon" />
          {{ swap.archived ? 'Move back to active' : 'Move to History' }}
        </Button>

        <!-- A preference, not an action on this swap — see the comment on
             useCliCommands.ts. It sits with the buttons rather than up next to
             the commands it hides, because a switch found only when the thing
             it controls is already showing is not much of a switch.
             `showZenon` is a weaker condition than that and not the same
             mistake: it asks whether this card has a Zenon section at all, not
             whether the commands inside it are open. A finished swap has no
             Zenon call left to run, so on a history card the switch was a
             control over nothing — offering to reveal commands that were not
             on the card in either position. -->
        <div
          v-if="showZenon"
          class="flex h-8 items-center gap-1.5 rounded-md border border-border px-2"
        >
          <Switch
            :id="`cli-${swap.id}`"
            :model-value="showCli"
            aria-label="Show CLI commands"
            @update:model-value="cli.set($event)"
          />
          <Label :for="`cli-${swap.id}`" class="text-xs font-medium whitespace-nowrap">
            Show CLI commands
          </Label>
          <InfoTip label="What this shows">
            The <code class="font-mono">znn-cli</code> lines for driving the Zenon leg from a
            terminal — for whoever isn't using the Syrius wallet button. One switch for every swap
            on this page, not just this one.
          </InfoTip>
        </div>
      </div>

      <!-- The offer is one long token with no line structure, so it wraps. -->
      <div v-if="offer" class="relative min-w-0">
        <pre
          class="rounded-md bg-muted/60 p-3 pr-12 font-mono text-xs break-all whitespace-pre-wrap"
          >{{ offer }}</pre>
        <CopyButton :value="offer" size="icon-sm" class="absolute top-2 right-2" />
        <p class="mt-1 text-xs text-muted-foreground">
          Send this to your counterparty — public data only.
        </p>
      </div>

      <p v-if="error" class="text-sm text-destructive">{{ error }}</p>

      <!-- Everything a card does not need to show to be useful. -->
      <Note summary="Technical details, hashes and event log">
        <DataList dense>
          <DataRow v-if="swap.contractHex" label="contract hex">
            <span class="font-mono text-xs break-all">{{ swap.contractHex }}</span>
          </DataRow>
          <DataRow v-if="swap.key?.pkhHex" label="my pubkey hash">
            <Address :address="swap.key.pkhHex" wrap />
          </DataRow>
          <DataRow label="secret hash">
            <Address :address="swap.secretHashHex" wrap />
          </DataRow>
          <DataRow v-if="swap.secretHex" label="secret">
            <Address :address="swap.secretHex" wrap />
          </DataRow>
          <DataRow v-if="swap.destAddr" label="btc destination">
            <Address :address="swap.destAddr" wrap />
          </DataRow>
          <DataRow label="network">
            <span class="font-mono text-xs">{{ swap.network }}</span>
          </DataRow>
          <DataRow label="leg ordering">
            bitcoin is the {{ swap.btcLegIsInitiators ? "initiator's" : "participant's" }} leg,
            zenon the {{ swap.btcLegIsInitiators ? "participant's" : "initiator's" }} (the
            initiator's must expire last)
          </DataRow>
          <DataRow v-if="swap.zenon?.htlcId" label="zenon htlc">
            <Address :address="swap.zenon.htlcId" wrap />
          </DataRow>
          <!-- The agreed token, and — when they differ — the one the entry
               actually holds. Anyone can issue a token and lock the agreed
               NUMBER of units of it, so which token is as much a term of the
               trade as how much. -->
          <DataRow v-if="swap.zenon?.tokenStandard" label="zenon token">
            <span class="font-mono text-xs break-all">{{ swap.zenon.tokenStandard }}</span>
            <span
              v-if="
                swap.zenon.observedToken && swap.zenon.observedToken !== swap.zenon.tokenStandard
              "
              class="font-mono text-xs text-destructive"
            >
              — the HTLC holds {{ swap.zenon.observedToken }}
            </span>
          </DataRow>
          <DataRow v-if="swap.zenon?.expirationSeconds" label="zenon expiry to use">
            <span class="font-mono tabular-nums">
              {{ swap.zenon.expirationSeconds }}s ({{ swap.zenon.expirationHours }}h)
            </span>
          </DataRow>
          <DataRow v-if="swap.refundTx" label="pre-signed refund">
            <Address :address="swap.refundTx.txid" wrap />
          </DataRow>
          <DataRow v-if="swap.redeemTx" label="redeem tx">
            <Address :address="swap.redeemTx.txid" wrap />
          </DataRow>
        </DataList>

        <div v-if="swap.events?.length" class="mt-3">
          <Button variant="ghost" size="sm" @click="showLog = !showLog">
            {{ showLog ? 'Hide' : 'Show' }} event log ({{ swap.events.length }})
          </Button>
          <!-- Newest last, which is the order they happened in. -->
          <ul v-if="showLog" class="mt-2 grid min-w-0 gap-1 rounded-md bg-muted/40 p-3">
            <!-- Event messages carry addresses and txids, which are unbroken
                 tokens long enough to widen the card if they are allowed to. -->
            <li v-for="(ev, i) in swap.events" :key="i" class="font-mono text-xs break-words">
              <span class="text-muted-foreground">{{ stamp(ev.at) }}</span>
              {{ ev.message }}
            </li>
          </ul>
        </div>
      </Note>

      <!-- Save the recovery file. Not a suggestion. -->
      <section
        v-if="urgeRecovery"
        class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-warning/50 bg-warning/5 p-3"
      >
        <div class="min-w-0 flex-1">
          <h3 class="flex items-center gap-1.5 text-sm font-semibold text-warning">
            Save the recovery file
            <InfoTip variant="warn" label="Why the recovery file matters">
              This swap's key exists in one place: this browser's storage for this site. Clearing
              site data, a private window closing, or a reinstalled browser takes it with no
              warning. The recovery file is a copy you keep, and it can spend the contract with
              nothing but a text field on any node.
            </InfoTip>
          </h3>
          <p class="text-sm text-muted-foreground">
            It is the only copy of this swap's key that leaves the browser.
          </p>
        </div>
        <Button variant="outline" size="sm" @click="downloadRecovery">
          <DownloadIcon />
          Download
        </Button>
      </section>
    </CardContent>
  </Card>
</template>

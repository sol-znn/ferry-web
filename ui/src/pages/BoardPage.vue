<script setup lang="ts">
import {computed, onMounted, onUnmounted, reactive, ref, watch} from 'vue'
import {useRouter} from 'vue-router'
import {PlusIcon, RadioIcon, RefreshCwIcon, SearchIcon, XIcon} from '@lucide/vue'
import {
  Badge,
  Button,
  Heading,
  Input,
  Label,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Switch,
} from 'nom-ui'
import BoardIdentity from '@/components/BoardIdentity.vue'
import BoardInbox from '@/components/BoardInbox.vue'
import BoardPostForm from '@/components/BoardPostForm.vue'
import BoardRow from '@/components/BoardRow.vue'
import BoardTakeDialog from '@/components/BoardTakeDialog.vue'
import MyPostRow from '@/components/MyPostRow.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import InfoTip from '@/components/InfoTip.vue'
import LoadingState from '@/components/LoadingState.vue'
import Note from '@/components/Note.vue'
import SessionPanel from '@/components/SessionPanel.vue'
import TermsGate from '@/components/TermsGate.vue'
import {DEFAULT_FILTERS, applyFilters, sortListings, type BoardFilters} from '@/core/board'
import {chainList, chainsForPost} from '@/core/chains'
import {useBoard} from '@/core/composables/useBoard'
import {useBoardIdentity} from '@/core/composables/useBoardIdentity'
import {useChainProofs} from '@/core/composables/useChainProofs'
import {useSettings} from '@/core/composables/useSettings'
import type {InboundTake, Listing, MyPost} from '@/types'

/**
 * The board.
 *
 * A public place to say what you want to trade, and nothing more than that.
 * There is no operator, no listing fee, no ranking, and nothing here can remove
 * somebody else's post or promote its own — the list is whatever the relays hand
 * back, sorted by numbers the reader chose.
 *
 * What it adds over shouting in a chat room is one property: a post is signed,
 * so only its author can edit or withdraw it, and impersonating one means
 * forging a signature. What it deliberately does NOT add is any judgement about
 * whether an offer is good. Nothing on this page has a basis for that, and a
 * board that quietly hid offers it disliked would be a board with an opinion.
 *
 * The safety of actually trading with a stranger is not this page's doing and is
 * not weakened by it: the swap is what protects both sides, and every term is
 * re-checked when a swap is created — see wasm/offerterms.go. This page is an
 * introduction.
 */

const board = useBoard()
const boardIdentity = useBoardIdentity()
const proofs = useChainProofs()
const {stored} = useSettings()
const router = useRouter()

/**
 * The chains this browser has proven an address on.
 *
 * Read once here and passed down rather than asked per row: `useChainProofs`
 * reads the identity, which pulls in the wallet composables, and `useUnisat`
 * registers a mounted hook — so asking each of two hundred rows for itself
 * would attach two hundred of them to answer one question. What a row does with
 * the answer is a pure function of its own post; see chains.ts.
 */
const proven = computed(() => proofs.proven.value)

/** Why the Post button is refusing, naming what is still unproven rather than
 *  asking for "your proofs" from somebody who has made one. */
const postMessage = computed(
  () =>
    `Prove your ${chainList(proofs.postBlockers.value)} address before posting an offer — the ` +
    `address on a post is where your half of the trade gets paid, and a proof is what says it is ` +
    `yours. The buttons are under your board key.`,
)

const filters = reactive<BoardFilters>({...DEFAULT_FILTERS})
const showAll = ref(false)
const posting = ref(false)
const editing = ref<MyPost | null>(null)
const taking = ref<Listing | null>(null)
const takeOpen = ref(false)
const termsOpen = ref(false)

/**
 * One clock for every countdown on the page.
 *
 * A ref rather than each row reading Date.now(): a row that computed its own
 * would never re-render, because nothing reactive changed when the second did.
 * Ten seconds is fine for a board whose finest granularity is a minute, and it
 * is one timer rather than one per row.
 */
const nowMs = ref(Date.now())
let clock: ReturnType<typeof setInterval> | null = null

const rows = computed(() => {
  // revision is read so the list recomputes when a post is replaced in place by
  // a newer version of itself — see useBoard's byKey map.
  void board.revision.value
  const source = showAll.value ? board.all.value.filter((l) => !l.mine) : board.open.value
  return sortListings(applyFilters(source, filters), filters)
})

const mineSorted = computed(() =>
  [...board.mine.value].sort((a, b) => b.post.createdAt - a.post.createdAt),
)

const live = computed(() => board.mine.value.filter((p) => p.post.status === 'open').length)

onMounted(() => {
  void board.start()
  clock = setInterval(() => {
    nowMs.value = Date.now()
    board.reap(nowMs.value)
  }, 10_000)
})

onUnmounted(() => {
  if (clock) clearInterval(clock)
  // The subscription is closed with the page. A board left subscribed in the
  // background would hold five sockets open for a list nobody is looking at.
  board.stop()
})

// The board is per network, and the posts already loaded are for the old one.
// Resubscribing rather than filtering, because the filter is applied at the
// relay: a page that kept them would be showing a mainnet board to somebody who
// just switched to signet.
watch(
  () => stored.value.network,
  () => void board.start(),
)

/**
 * Posting goes through the same gate a swap does.
 *
 * Not because a post is a swap — it commits to nothing — but because it is the
 * first step of one, and the terms say what this software does and does not
 * promise. Asking here means it is read before there is anything to lose by
 * reading it, which is the same argument TermsGate makes on the swap form.
 */
function openPostForm() {
  if (!proofs.canPostAny.value) {
    board.error.value = postMessage.value
    return
  }
  termsOpen.value = true
}

function onTermsAccepted() {
  posting.value = true
}

function startEdit(entry: MyPost) {
  editing.value = entry
  posting.value = true
}

async function submitPost(form: Record<string, unknown>) {
  const saved = await board.publish(form)
  if (saved) {
    posting.value = false
    editing.value = null
  }
}

/** Renewing is a republish with a fresh deadline and nothing else changed. The
 *  deadline is this instance's default rather than a literal — fifteen minutes
 *  on a development build, a day on production. */
async function renew(entry: MyPost) {
  const p = entry.post
  // The addresses come from the proofs held now, not from the post. A post
  // renewed after switching wallet accounts would otherwise carry an address
  // this browser can no longer prove, which Go refuses — and the offer somebody
  // answers should say where the money goes today. The form says the same thing
  // out loud when it is an edit rather than a renewal.
  const need = chainsForPost(p)
  if (!proofs.readyFor(need)) {
    board.error.value = proofs.messageFor(need)
    return
  }
  await board.publish({
    id: p.id,
    side: p.side,
    role: p.role,
    amountSats: p.amountSats,
    minSats: p.minSats ?? 0,
    maxSats: p.maxSats ?? 0,
    zenonAmt: p.zenonAmt,
    zenonToken: p.zenonToken ?? '',
    lockHours: p.lockHours ?? 0,
    btcAddr: proofs.addressFor('btc'),
    znnAddr: proofs.addressFor('znn'),
    note: p.note ?? '',
    completed: p.completed ?? 0,
    ttlSeconds: boardIdentity.limits.value.defaultTtl,
  })
}

function openTake(listing: Listing) {
  taking.value = listing
  takeOpen.value = true
}

/**
 * Take an offer, then go to where the swap gets made.
 *
 * The redirect is the point. A take is not the end of anything — it opens a
 * session and settles what the trade is — and everything after it happens on
 * the swaps page. Leaving somebody on the board with a line in a transcript
 * saying "waiting for the other side" made them find that page themselves and
 * retype an amount and a direction the two of them had just agreed.
 *
 * `pendingTrade` is what travels: the swaps page opens its form already holding
 * this trade. Only after the session is up, because the session is what the
 * other side answers on — arriving at a form with nowhere for their offer to
 * land would be arriving early.
 */
async function confirmTake(opts: {amountSats: number; note: string}) {
  const listing = taking.value
  if (!listing) return
  // Checked here as well as on the button, because this is the call that
  // actually publishes. A proof can be dropped between opening the dialog and
  // confirming it, and a take carrying an unproven address is one Go refuses
  // anyway — better to say so than to send it and translate the refusal.
  //
  // Against the chains THIS offer settles on, not against every chain that
  // exists. That is the whole rule: an offer between two Zenon tokens is
  // takeable by somebody who has never touched Bitcoin.
  const need = chainsForPost(listing.post)
  if (!proofs.readyFor(need)) {
    board.error.value = proofs.messageFor(need)
    return
  }
  const room = await board.take(listing, {
    ...opts,
    btcAddr: proofs.addressFor('btc'),
    znnAddr: proofs.addressFor('znn'),
  })
  if (room) {
    takeOpen.value = false
    taking.value = null
    await router.push('/')
  }
}

/** Accepting is the same journey from the other end: the session is live, the
 *  terms are settled, and the swap is made on the swaps page.
 *
 *  Gated on the same proofs a take is, because accepting sends this side's
 *  addresses back over the session — and a proof dropped since the offer went up
 *  would put an unproven one on the wire. */
async function onAccept(inbound: InboundTake) {
  const post = board.mine.value.find((p) => p.post.id === inbound.take.postId)
  if (post) {
    const need = chainsForPost(post.post)
    if (!proofs.readyFor(need)) {
      board.error.value = proofs.messageFor(need)
      return
    }
  }
  if (await board.accept(inbound)) await router.push('/')
}
</script>

<template>
  <section class="grid min-w-0 gap-4">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <Heading :level="2" class="text-xl">Board</Heading>
      <div class="flex flex-wrap items-center gap-2">
        <Badge :variant="board.connected.value ? 'success' : 'warning'" class="gap-1">
          <RadioIcon class="size-3" />
          {{ board.connected.value }}/{{ board.relays.value.length }} relays
        </Badge>
        <Badge variant="outline">{{ stored.network }}</Badge>
        <Button variant="outline" size="sm" :disabled="board.loading.value" @click="board.start()">
          <RefreshCwIcon />
          Reload
        </Button>
        <Button size="sm" :disabled="!proofs.canPostAny.value" @click="openPostForm">
          <PlusIcon />
          Post an offer
        </Button>
        <!-- A disabled button with nothing beside it is a dead control. The
             proofs it wants are two panels down, so the tip names them and
             where they are made rather than leaving the reader to guess which
             of the things on this page is in the way. -->
        <InfoTip v-if="!proofs.canPostAny.value" label="Why posting is disabled">
          <p>
            An offer names where your half of the trade gets paid, and this build only publishes an
            address a wallet has signed for. Prove your
            {{ chainList(proofs.postBlockers.value) }}
            {{ proofs.postBlockers.value.length === 1 ? 'address' : 'addresses' }} under your board
            key below and this opens.
          </p>
          <p>
            Every offer here is Bitcoin against Zenon for now, so both are needed. A pair that
            settles on one chain will only ask for that one.
          </p>
        </InfoTip>
      </div>
    </div>

    <p class="flex flex-wrap items-center gap-x-1.5 text-sm text-muted-foreground">
      Offers people have posted, on relays, with nobody hosting them.
      <InfoTip label="What this board is, and what it is not">
        <p>
          Every post is signed by the key of whoever wrote it, so only they can edit or withdraw
          one, and impersonating somebody means forging a signature. That is the whole of what this
          page guarantees.
        </p>
        <p>
          It does <strong>not</strong> vouch for anybody. An offer is a stranger's claim about what
          they will do, a verified badge only means they hold the address they named, and the "swaps
          completed" number is typed by the person claiming it.
        </p>
        <p>
          What protects you is the swap, not the board: both legs settle or both refund, and every
          term is re-checked against the offer when the swap is created. Taking an offer opens a
          conversation and agrees nothing.
        </p>
      </InfoTip>
    </p>

    <BoardIdentity />

    <BoardInbox
      :takes="board.inbox.value"
      :mine="board.mine.value"
      :proven="proven"
      :busy="board.busy.value"
      @accept="onAccept"
      @dismiss="board.dismiss($event)"
    />

    <!-- The session panel, on this page as well as the swaps page. Taking an
         offer opens a session, and the panel is where the code, the relays and
         the transcript live — putting the reader on another page to see whether
         anybody answered would be hiding the result of the button they pressed. -->
    <SessionPanel />

    <!-- Posting -->
    <section v-if="posting" class="grid gap-3 rounded-lg border border-primary/40 p-4">
      <div class="flex items-center justify-between gap-3">
        <h2 class="text-sm font-semibold">
          {{ editing ? `Editing post ${editing.post.id}` : 'New offer' }}
        </h2>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="Close"
          @click="((posting = false), (editing = null))"
        >
          <XIcon />
        </Button>
      </div>
      <BoardPostForm
        :editing="editing"
        :busy="board.busy.value"
        @submit="submitPost"
        @cancel="((posting = false), (editing = null))"
      />
    </section>

    <!-- Your own posts -->
    <section v-if="board.mine.value.length" class="grid gap-2">
      <h2 class="flex items-center gap-1.5 text-sm font-semibold">
        Your posts
        <span class="font-mono text-xs font-normal text-muted-foreground">
          {{ live }} live of {{ board.mine.value.length }}
        </span>
        <InfoTip label="Why expired posts stay here">
          <p>
            Expired and withdrawn posts stay in this list so you can renew them. An offer that timed
            out overnight is exactly the one you want back, and a list that hid it would leave you
            retyping it.
          </p>
          <p>
            Only this browser can edit or withdraw your posts, because only it holds the key that
            signs them. That is also why an offer left running when you close the tab stays up until
            it expires — which is most of the reason the default is a day.
          </p>
        </InfoTip>
      </h2>
      <MyPostRow
        v-for="entry in mineSorted"
        :key="entry.post.id"
        :entry="entry"
        :now-ms="nowMs"
        :busy="board.busy.value"
        @edit="startEdit"
        @renew="renew"
        @withdraw="board.withdraw($event.post.id)"
        @forget="board.forget($event.post.id)"
      />
    </section>

    <!-- Filters -->
    <div class="grid gap-3 rounded-lg border border-border p-3">
      <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <div class="grid gap-1.5">
          <Label class="text-ledger text-muted-foreground">I want to</Label>
          <Select
            :model-value="filters.direction"
            @update:model-value="(v) => (filters.direction = v as BoardFilters['direction'])"
          >
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="any">see everything</SelectItem>
              <SelectItem value="buy-znn">buy ZNN with BTC</SelectItem>
              <SelectItem value="sell-znn">sell ZNN for BTC</SelectItem>
            </SelectContent>
          </Select>
        </div>

        <div class="grid gap-1.5">
          <Label class="flex items-center gap-1.5 text-ledger text-muted-foreground">
            Sort by
            <InfoTip label="How 'best rate' knows which way round">
              <p>
                Best rate means the most ZNN per BTC when you are buying and the fewest when you are
                selling, so it needs to know which you are doing. With no direction chosen there is
                no "best" to compute — the two halves are not comparable — so a mixed board falls
                back to newest rather than sorting by a number that means opposite things in
                adjacent rows.
              </p>
            </InfoTip>
          </Label>
          <Select
            :model-value="filters.sort"
            @update:model-value="(v) => (filters.sort = v as BoardFilters['sort'])"
          >
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="rate">best rate</SelectItem>
              <SelectItem value="newest">newest</SelectItem>
              <SelectItem value="expiring">expiring soonest</SelectItem>
              <SelectItem value="size">largest</SelectItem>
            </SelectContent>
          </Select>
        </div>

        <div class="grid gap-1.5">
          <Label class="text-ledger text-muted-foreground">Size (BTC)</Label>
          <div class="flex gap-2">
            <Input
              :model-value="filters.minSats ? String(filters.minSats / 1e8) : ''"
              inputmode="decimal"
              placeholder="min"
              class="font-mono"
              aria-label="Smallest size in BTC"
              @update:model-value="(v) => (filters.minSats = Math.round((Number(v) || 0) * 1e8))"
            />
            <Input
              :model-value="filters.maxSats ? String(filters.maxSats / 1e8) : ''"
              inputmode="decimal"
              placeholder="max"
              class="font-mono"
              aria-label="Largest size in BTC"
              @update:model-value="(v) => (filters.maxSats = Math.round((Number(v) || 0) * 1e8))"
            />
          </div>
        </div>

        <div class="grid gap-1.5">
          <Label for="bf-search" class="text-ledger text-muted-foreground">Search</Label>
          <div class="relative">
            <SearchIcon
              class="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground"
            />
            <Input
              id="bf-search"
              v-model="filters.search"
              autocomplete="off"
              placeholder="note, key, address"
              class="pl-8"
            />
          </div>
        </div>
      </div>

      <div class="flex flex-wrap items-center gap-x-5 gap-y-2">
        <label class="flex items-center gap-2 text-sm">
          <Switch
            :model-value="filters.verifiedOnly"
            @update:model-value="(v) => (filters.verifiedOnly = Boolean(v))"
          />
          Verified addresses only
          <InfoTip label="What this filters on">
            Shows only posts where a wallet signature checked out here, against your network, for an
            address the post names. It says the author holds that address. It says nothing about
            whether they will trade fairly.
          </InfoTip>
        </label>

        <label class="flex items-center gap-2 text-sm">
          <Switch :model-value="showAll" @update:model-value="(v) => (showAll = Boolean(v))" />
          Include expired and withdrawn
        </label>

        <span class="flex-1" />
        <span class="font-mono text-xs text-muted-foreground">
          {{ rows.length }} shown of {{ board.open.value.length }} live
        </span>
      </div>
    </div>

    <ErrorState v-if="board.error.value" :message="board.error.value" />
    <LoadingState v-else-if="board.loading.value && !rows.length" />
    <EmptyState
      v-else-if="!rows.length && !board.open.value.length"
      message="Nothing on the board yet."
      hint="Post an offer, or wait — relays replay what they hold when you connect."
    />
    <EmptyState
      v-else-if="!rows.length"
      message="Nothing matches those filters."
      hint="Widen the size range, or set the direction back to everything."
    />
    <div v-else class="grid min-w-0 gap-2">
      <BoardRow
        v-for="l in rows"
        :key="`${l.author}:${l.post.id}`"
        :listing="l"
        :now-ms="nowMs"
        :proven="proven"
        @take="openTake"
      />
    </div>

    <Note variant="warn" summary="Nobody is vetting anything here. What that means in practice.">
      <p>
        A post proves one thing: whoever holds a key wrote it and nobody has altered it. A verified
        badge proves one more: they hold the address they named. Neither says they will trade
        fairly, and both are cheap to obtain — a key costs nothing and an address costs a signature.
      </p>
      <p>
        Everything that actually protects you happens in the swap. Both legs settle or both refund;
        the counterparty's contract is audited against terms you agreed before either side funds;
        and a timelock returns your money if they walk away. A stranger on a board is exactly the
        threat model that was designed for.
      </p>
      <p>
        The usual advice still applies. Check the rate against somewhere else before taking it, be
        wary of an offer far better than the rest, and prefer a Zenon node of your own — verifying
        the other half of a swap is the one check where a dishonest answer costs money.
      </p>
    </Note>

    <BoardTakeDialog
      v-model:open="takeOpen"
      :listing="taking"
      :busy="board.busy.value"
      @confirm="confirmTake"
      @cancel="((takeOpen = false), (taking = null))"
    />

    <TermsGate v-model:open="termsOpen" @accepted="onTermsAccepted" />
  </section>
</template>

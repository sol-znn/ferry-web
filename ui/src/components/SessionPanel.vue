<script setup lang="ts">
import {computed, ref} from 'vue'
import {
  ChevronDownIcon,
  ChevronRightIcon,
  LinkIcon,
  RadioIcon,
  UserCheckIcon,
  UserXIcon,
  XIcon,
} from '@lucide/vue'
import {Badge, Button, CopyButton, Input, Label} from 'nom-ui'
import InfoTip from './InfoTip.vue'
import {useSession} from '@/core/composables/useSession'

// The session, as one panel.
//
// It is above the swap list rather than inside a card because a session is not
// a property of a swap: it starts before either side has one, and it is how the
// swap gets created on the other side. Its job on screen is to answer three
// questions at a glance — is it connected, is anybody else here, and what has
// been exchanged — and the third is a transcript rather than a status, because
// the interesting entries are the refusals.
//
// Collapsing keeps the first two answers and drops the third. What survives is
// chosen by what you would want to see mid-swap without scrolling: relay health,
// whether the other party is there, whether your chains agree, and the code
// itself with its copy button, because the most common reason to look at this
// panel at all is to send the code again.
const {
  room,
  lines,
  error,
  busy,
  relays,
  active,
  connected,
  swapId,
  peers,
  peerPresent,
  peerChain,
  join,
  leave,
} = useSession()

const codeIn = ref('')
const showJoin = ref(false)
const collapsed = ref(false)

const relaySummary = computed(() => `${connected.value}/${relays.value.length} relays`)

/** Green only when a block hash was actually compared and nothing disagreed. */
const chainBadge = computed(() => {
  const c = peerChain.value
  if (!c) return null
  if (c.problems?.length) return {variant: 'destructive' as const, text: 'chain mismatch'}
  if (c.sameChain) return {variant: 'success' as const, text: 'same chain'}
  return {variant: 'warning' as const, text: 'chain unverified'}
})

async function host() {
  await join()
  showJoin.value = false
}

async function joinWithCode() {
  if (!codeIn.value.trim()) return
  await join(codeIn.value)
  codeIn.value = ''
  showJoin.value = false
}

function stamp(at: number): string {
  return new Date(at).toISOString().slice(11, 19)
}
</script>

<template>
  <section class="grid gap-3 rounded-lg border border-border p-4">
    <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
      <!-- Collapsing is only offered once there is something to collapse. -->
      <button
        v-if="active"
        type="button"
        class="flex items-center gap-1.5 text-sm font-semibold"
        :aria-expanded="!collapsed"
        @click="collapsed = !collapsed"
      >
        <component :is="collapsed ? ChevronRightIcon : ChevronDownIcon" class="size-4" />
        <RadioIcon class="size-4 text-success" />
        Live session
      </button>
      <h2 v-else class="flex items-center gap-1.5 text-sm font-semibold">
        <RadioIcon class="size-4 text-muted-foreground" />
        Live session
        <InfoTip label="What a session does, and what it does not">
          <p>
            Setting a swap up by hand means pasting four long hex strings between chat windows. A
            session moves them for you: one side makes a code, the other types it in.
          </p>
          <p>
            <strong>Nothing arriving is trusted.</strong> Every value goes through the check the
            paste box used, and anything that fails is refused and shown below. The preimage is
            never sent — there is no field for it.
          </p>
        </InfoTip>
      </h2>

      <template v-if="active">
        <Badge :variant="connected ? 'success' : 'warning'">{{ relaySummary }}</Badge>

        <!-- Presence. A relay has no membership, so this is built from a
             greeting each side publishes on arrival. -->
        <Badge :variant="peerPresent ? 'success' : 'outline'" class="gap-1">
          <component :is="peerPresent ? UserCheckIcon : UserXIcon" class="size-3" />
          {{ peerPresent ? `${peers.length} joined` : 'waiting for them' }}
        </Badge>

        <Badge v-if="chainBadge" :variant="chainBadge.variant">{{ chainBadge.text }}</Badge>
        <Badge v-if="swapId" variant="outline">swap {{ swapId }}</Badge>

        <span class="flex-1" />
        <Button variant="ghost" size="sm" @click="leave">
          <XIcon />
          Leave
        </Button>
      </template>
      <template v-else>
        <span class="flex-1" />
        <Button variant="outline" size="sm" :disabled="busy" @click="host">Start a session</Button>
        <Button variant="ghost" size="sm" @click="showJoin = !showJoin">
          <LinkIcon />
          Join with a code
        </Button>
      </template>
    </div>

    <p v-if="!active && !showJoin" class="text-sm text-muted-foreground">
      Optional. Swaps work perfectly well by copying values across by hand — this only saves the
      copying, and checks everything that arrives exactly as before.
    </p>

    <!-- Joining -->
    <div v-if="!active && showJoin" class="grid gap-2 sm:max-w-md">
      <Label for="sess-code">Their session code</Label>
      <div class="flex gap-2">
        <Input
          id="sess-code"
          v-model="codeIn"
          spellcheck="false"
          autocapitalize="characters"
          autocomplete="off"
          placeholder="ABCD1234-EFGH5678-…"
          class="font-mono"
          @keyup.enter="joinWithCode"
        />
        <Button :disabled="busy || !codeIn.trim()" @click="joinWithCode">Join</Button>
      </div>
    </div>

    <!-- The code. It survives collapsing, because wanting to send it again is
         the commonest reason to come back to this panel. -->
    <div
      v-if="active && room"
      class="grid gap-2 rounded-md border p-3"
      :class="peerPresent ? 'border-border bg-muted/30' : 'border-primary/40 bg-primary/5'"
    >
      <span class="flex flex-wrap items-center gap-1.5 text-xs font-semibold">
        {{ peerPresent ? 'Session code' : 'Session code — send it to them' }}
        <InfoTip variant="warn" label="Treat the code like a password">
          <p>
            The code is the room: anyone holding it can read this session and post into it. Send it
            like a password, and use a fresh one per swap.
          </p>
          <p>
            It never reaches a relay — it is the seed for the signing and encryption keys, derived
            in your browser.
          </p>
        </InfoTip>
      </span>
      <div class="flex items-center gap-2">
        <code
          class="min-w-0 flex-1 font-mono tracking-wider break-all"
          :class="collapsed ? 'text-sm' : 'text-lg'"
        >
          {{ room.display }}
        </code>
        <CopyButton :value="room.display" size="icon-sm" />
      </div>
    </div>

    <!-- A chain mismatch is the one thing that must not be collapsible: it means
         the swap cannot complete, whatever else the panel says. -->
    <div
      v-if="active && peerChain?.problems?.length"
      class="grid gap-1 rounded-md border border-destructive/50 bg-destructive/5 p-3 text-sm text-destructive"
    >
      <strong>You are not on the same chain</strong>
      <p v-for="(problem, i) in peerChain.problems" :key="i">{{ problem }}</p>
    </div>

    <template v-if="active && !collapsed">
      <!-- Which relays, and how they are doing. A session that is silent because
           every relay is down should look different from one nobody has joined. -->
      <div class="flex flex-wrap gap-1.5">
        <span
          v-for="r in relays"
          :key="r.url"
          class="rounded border px-1.5 py-0.5 font-mono text-[11px]"
          :class="
            r.status === 'open'
              ? 'border-success/40 text-success'
              : r.status === 'connecting'
                ? 'border-border text-muted-foreground'
                : 'border-destructive/40 text-destructive'
          "
        >
          {{ r.url.replace(/^wss:\/\//, '') }}
        </span>
      </div>

      <!-- The transcript. Refusals are the entries worth reading, so they are the
           entries that look like something. -->
      <ul v-if="lines.length" class="grid max-h-64 gap-1 overflow-y-auto">
        <li
          v-for="line in lines"
          :key="line.id"
          class="flex min-w-0 gap-2 text-xs"
          :class="line.ok === false ? 'text-destructive' : 'text-muted-foreground'"
        >
          <span class="shrink-0 font-mono tabular-nums opacity-60">{{ stamp(line.at) }}</span>
          <span class="shrink-0 font-mono opacity-60">
            {{ line.dir === 'in' ? '←' : line.dir === 'out' ? '→' : '·' }}
          </span>
          <span class="min-w-0 flex-1 break-words" :class="{'text-success': line.ok === true}">
            {{ line.text }}
          </span>
        </li>
      </ul>
    </template>

    <p v-if="error" class="text-sm text-destructive">{{ error }}</p>
  </section>
</template>

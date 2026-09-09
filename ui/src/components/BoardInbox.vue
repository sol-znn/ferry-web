<script setup lang="ts">
import {computed} from 'vue'
import {InboxIcon, XIcon} from '@lucide/vue'
import {Badge, Button} from 'nom-ui'
import InfoTip from './InfoTip.vue'
import {shortKey} from '@/core/board'
import {chainList, chainsForPost, missingChains, type ChainId} from '@/core/chains'
import {btc} from '@/core/format'
import type {InboundTake, MyPost} from '@/types'

// Who wants your offers.
//
// The half of the board a static page is not obviously able to do, and the
// reason takes are encrypted rather than public: several people can take the
// same post, each gets a room of their own, and you pick one. A public session
// code would have put all of them in the same room.
//
// A take is not a commitment either way. Accepting one joins its session and
// marks the post taken — it does not create a swap, agree a price, or move
// anything. Declining tells the taker nothing, deliberately: there is no message
// worth sending, and a refusal published under your key would be a permanent
// public record of who you would not trade with.

const props = defineProps<{
  takes: InboundTake[]
  mine: MyPost[]
  /** Which chains this browser has proven an address on — see BoardRow for why
   *  it is passed rather than read here. */
  proven?: ChainId[]
  busy?: boolean
}>()
defineEmits<{accept: [InboundTake]; dismiss: [string]}>()

/** A take for a post this browser no longer holds — expired, withdrawn, or made
 *  in a browser whose storage has since been cleared — is a take that cannot be
 *  accepted. Shown rather than hidden, so the sender's effort is visible. */
function postFor(take: InboundTake): MyPost | undefined {
  return props.mine.find((p) => p.post.id === take.take.postId)
}

/**
 * Which proofs this take still needs, of the chains its post settles on.
 *
 * Normally none: posting the offer required them in the first place. It is
 * checked again because a proof can be dropped or go stale while an offer is
 * live, and accepting sends this side's addresses back over the session — so
 * the one moment a dropped proof matters is exactly this one.
 */
function missingFor(take: InboundTake): ChainId[] {
  const post = postFor(take)
  if (!post) return []
  return missingChains(chainsForPost(post.post), props.proven ?? [])
}

const sorted = computed(() => [...props.takes].sort((a, b) => b.at - a.at))
</script>

<template>
  <section
    v-if="takes.length"
    class="grid gap-3 rounded-lg border border-primary/40 bg-primary/5 p-4"
  >
    <h2 class="flex items-center gap-1.5 text-sm font-semibold">
      <InboxIcon class="size-4 text-primary" />
      {{ takes.length }} {{ takes.length === 1 ? 'person wants' : 'people want' }} to trade
      <InfoTip label="What a take is">
        <p>
          Somebody read one of your offers and sent you a private session code, encrypted so that
          only your board key can open it. Accepting joins that session; it agrees nothing.
        </p>
        <p>
          Several people can take the same offer. Accepting one marks the post as taken — which the
          others can see, so nobody is left wondering why an offer went quiet.
        </p>
      </InfoTip>
    </h2>

    <div
      v-for="t in sorted"
      :key="t.eventId"
      class="grid gap-2 rounded-md border border-border bg-background p-3 sm:grid-cols-[1fr_auto] sm:items-center"
    >
      <div class="grid min-w-0 gap-1">
        <div class="flex flex-wrap items-center gap-2 text-sm">
          <code class="font-mono text-xs">{{ shortKey(t.from) }}</code>
          <span v-if="t.take.amountSats" class="font-mono">{{ btc(t.take.amountSats) }}</span>
          <Badge v-if="postFor(t)" variant="outline">post {{ t.take.postId }}</Badge>
          <Badge v-else variant="warning">post is gone</Badge>
        </div>
        <p v-if="t.take.note" class="text-xs break-words text-muted-foreground">
          {{ t.take.note }}
        </p>
        <p class="font-mono text-xs text-muted-foreground/80">
          {{ new Date(t.at * 1000).toISOString().slice(0, 19).replace('T', ' ') }}
        </p>
      </div>

      <div class="flex items-center gap-2 sm:justify-end">
        <Button
          size="sm"
          :disabled="busy || !postFor(t) || missingFor(t).length > 0"
          @click="$emit('accept', t)"
        >
          Accept
        </Button>
        <InfoTip v-if="postFor(t) && missingFor(t).length" variant="warn" label="Why Accept is off">
          Your proof for {{ chainList(missingFor(t)) }} is gone or has moved to another address, and
          accepting sends this side's addresses back over the session. Prove it again under your
          board key and this take is answerable.
        </InfoTip>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="Dismiss this take"
          @click="$emit('dismiss', t.eventId)"
        >
          <XIcon />
        </Button>
      </div>
    </div>

    <p class="text-xs text-muted-foreground">
      Dismissing only clears it from here. Nothing is sent back — a decline is not worth publishing
      under your key.
    </p>
  </section>
</template>

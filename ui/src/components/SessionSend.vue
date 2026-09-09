<script setup lang="ts">
import {CheckIcon, SendIcon} from '@lucide/vue'
import {Button} from 'nom-ui'

// The "send this over the session" affordance, which appears in three places and
// has the same states in each.
//
// The attach state is the one worth having: a session is open but is not about
// this swap. That happens the moment someone runs two swaps at once, and
// silently publishing this card's contract into the other swap's room would
// hand the counterparty a value they will refuse and a reason to distrust the
// session. So it offers to attach rather than to send, and says which it is.
//
// `sent` is the other. An attached session publishes each of these by itself, so
// the ordinary state of this button is beside a value that has already gone —
// and a button reading "Send it over the session" there says the opposite of
// what is true, which is how somebody ends up pressing it and waiting for
// something that happened a minute ago. Told that it went, it can offer the one
// thing still worth doing: sending it again, for a relay that dropped it or a
// counterparty who arrived after it.
defineProps<{
  /** The session is open AND attached to this swap. */
  inSession: boolean
  /** A session is open, whether or not it is about this swap. */
  sessionActive: boolean
  /** This exact value has already gone over this session. */
  sent?: boolean
  label: string
}>()

defineEmits<{send: []; attach: []}>()
</script>

<template>
  <div v-if="sessionActive" class="flex flex-wrap items-center gap-2">
    <template v-if="inSession">
      <Button variant="outline" size="sm" @click="$emit('send')">
        <component :is="sent ? CheckIcon : SendIcon" />
        {{ sent ? 'Send it again' : label }}
      </Button>
      <span v-if="sent" class="text-xs text-muted-foreground"> already sent over the session </span>
    </template>
    <template v-else>
      <Button variant="ghost" size="sm" @click="$emit('attach')">
        Use the open session for this swap
      </Button>
      <span class="text-xs text-muted-foreground">
        it is currently attached to a different one
      </span>
    </template>
  </div>
</template>

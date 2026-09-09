<script setup lang="ts">
import {computed} from 'vue'
import type {Presence} from '@/core/composables/useBoard'

// Whether the person behind a post has the board in front of them.
//
// One bit, and it is worth being exact about which bit. It says a browser
// holding that board key published a signed beat in the last minute, from a tab
// that was not hidden at the time — so a take sent now is likely to be seen now.
// It says nothing about whether they will answer, agree, or trade fairly, and a
// swap protects both sides either way.
//
// Grey covers a tab that closed, one merely buried behind others, and a key that
// has never beaten at all. Two words for all three, because a reader cannot act
// on the difference: none of them is somebody who is going to see a take land,
// and a dot that hedged about which kind of absent this is would be asking them
// to weigh a distinction with no decision attached to it.
//
// `useBoard` still keeps `offline` and `unknown` apart. The distinction earns its
// place there — it is the difference between a beat that lapsed and no beat ever
// — and this is simply the component that has no use for it.

const props = defineProps<{presence: Presence}>()

// Still rather than pulsing, and the same size as the status dots in the header.
// This sits in a list where every row has one, and a page of blinking dots is a
// page that is hard to read past — the same reason the rows are rows, not cards.
const look = computed(() =>
  props.presence === 'online'
    ? {cls: 'bg-success', label: 'User is online'}
    : {cls: 'bg-muted-foreground/40', label: 'User is offline'},
)
</script>

<template>
  <!-- title carries the tooltip and the accessible name together. Unlike the
       header's dots this one is NOT aria-hidden: there is no text beside it
       saying the same thing, so hiding it would drop the fact entirely. -->
  <span
    class="inline-block size-1.5 shrink-0 rounded-full"
    :class="look.cls"
    role="img"
    :aria-label="look.label"
    :title="look.label"
  />
</template>

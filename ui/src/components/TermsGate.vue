<script setup lang="ts">
import {TriangleAlertIcon} from '@lucide/vue'
import {
  Button,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from 'nom-ui'
import {useTerms} from '@/core/composables/useTerms'

/**
 * The question, asked before the new-swap form ever becomes visible.
 *
 * It is a gate rather than a notice, and every choice here follows from that.
 * There is no close button and no dismissing it by clicking away or pressing
 * escape, because "I did not answer" is not one of the available positions —
 * the two buttons are the two answers, and one of them is Decline. Declining is
 * a real outcome that leaves the form closed and nothing built.
 *
 * It sits in front of the FORM, not the Create button behind it. Gating Create
 * meant somebody could fill in every field — an amount, an address, all of it
 * — and be handed the warning only at the very end, after the work of deciding
 * was already done. Asking here instead means it is the first thing anybody
 * sees, before there is anything to lose by reading it. See openNewSwapForm in
 * ActivePage.vue, which every way the form opens goes through.
 *
 * It asks every time on purpose, not once per browser. A swap is a contract, a
 * deadline and a counterparty that cannot be undone once it exists, which is
 * exactly as true of the fifth one as the first — an answer remembered from an
 * earlier swap would make this readable exactly once, on the occasion where a
 * person has the least idea yet what they are agreeing to.
 *
 * The text is the warning that used to sit permanently on the new-swap form as
 * furniture nobody read twice. Here it is read once per attempt to open that
 * form, by somebody who has to answer it before the form exists.
 */

const open = defineModel<boolean>('open', {required: true})
const emit = defineEmits<{accepted: []}>()

const {accept} = useTerms()

function onAccept() {
  accept()
  open.value = false
  // Emitted after recording, so whatever was waiting on this — the create that
  // opened the dialog — runs against an answer that has already been logged.
  emit('accepted')
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent
      hide-close
      class="max-w-xl"
      @interact-outside.prevent
      @escape-key-down.prevent
      @pointer-down-outside.prevent
    >
      <DialogHeader>
        <DialogTitle class="flex items-center gap-2">
          <TriangleAlertIcon class="size-5 shrink-0 text-warning" aria-hidden="true" />
          Before this swap
        </DialogTitle>
        <!-- sr-only: Reka's DialogContent warns without a description or
             aria-describedby, and a screen reader benefits from one even
             though sighted users get the same point from the title and the
             paragraphs below. Nothing here duplicates what is on screen. -->
        <DialogDescription class="sr-only">
          Review what this swap involves before it is created.
        </DialogDescription>
      </DialogHeader>

      <div class="grid gap-3 text-sm">
        <p class="font-semibold text-warning">
          Experimental software. Swaps move real coins and cannot be reversed.
        </p>
        <p class="text-muted-foreground">
          Nobody holds your funds and nobody can recover them for you: no operator, no support, no
          refunds. Your keys live in this browser alone — clearing site data, closing a private
          window or reinstalling the browser destroys them with no warning, so download each swap's
          recovery file as soon as it is funded.
        </p>
        <p class="text-muted-foreground">
          A mistake in an address, an amount or a deadline is final. From the moment a swap exists
          there is a contract, a deadline and a counterparty, and none of the three can be undone by
          anyone.
        </p>
        <p class="text-muted-foreground">
          Accepting means you accept the
          <RouterLink to="/terms" target="_blank" class="text-primary underline underline-offset-4">
            terms and conditions </RouterLink
          >, which open in a new tab so this stays where it is.
        </p>
      </div>

      <DialogFooter class="gap-2 sm:justify-start">
        <Button @click="onAccept">I accept — show me the form</Button>
        <Button variant="ghost" @click="open = false">Decline</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>

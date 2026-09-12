<script setup lang="ts">
import {ref} from 'vue'
import {InfoIcon, TriangleAlertIcon} from '@lucide/vue'
import {Tooltip, TooltipContent, TooltipTrigger} from 'nom-ui'

// The small ⓘ that sits beside a label. Hover or focus it and the explanation
// appears in a bubble; the label stays one line long either way. A sentence that
// only some readers need should not push the field it describes down the screen
// for everyone.
//
// Two details make it work on a touch screen, where hover does not exist:
//
//  - the click handler. reka-ui's trigger deliberately ignores pointermove from
//    a touch pointer, so without this the bubble would be desktop-only.
//  - disable-closing-trigger. The trigger's own click closes the tooltip by
//    default, which would shut what the click above just opened.
//
// And one keeps it from opening when nobody asked. A tooltip opens on FOCUS as
// well as hover, which is right for a keyboard user and wrong for every other
// way focus moves on its own -- opening a dialog focuses its first focusable
// child, and where that child is one of these the bubble appears over the dialog
// with the pointer nowhere near it. ignore-non-keyboard-focus matches against
// :focus-visible, so a keyboard tab still opens it and a programmatic focus does
// not.
withDefaults(
  defineProps<{
    /** Screen-reader name for the trigger. Say what it explains. */
    label?: string
    variant?: 'default' | 'warn'
    side?: 'top' | 'right' | 'bottom' | 'left'
  }>(),
  {label: 'More information', variant: 'default', side: 'top'},
)

const open = ref(false)
</script>

<template>
  <Tooltip
    v-model:open="open"
    disable-closing-trigger
    ignore-non-keyboard-focus
    :delay-duration="120"
  >
    <TooltipTrigger
      type="button"
      :aria-label="label"
      class="inline-flex size-4 shrink-0 cursor-help items-center justify-center rounded-full align-[-3px] focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
      :class="
        variant === 'warn'
          ? 'text-warning hover:text-warning/80'
          : 'text-muted-foreground/70 hover:text-foreground'
      "
      @click="open = !open"
    >
      <component :is="variant === 'warn' ? TriangleAlertIcon : InfoIcon" class="size-3.5" />
    </TooltipTrigger>
    <TooltipContent
      :side="side"
      :collision-padding="12"
      class="max-w-72 text-xs leading-relaxed text-wrap [&_code]:rounded [&_code]:bg-background/20 [&_code]:px-1 [&_code]:font-mono [&_p+p]:mt-2"
    >
      <slot />
    </TooltipContent>
  </Tooltip>
</template>

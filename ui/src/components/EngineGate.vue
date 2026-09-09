<script setup lang="ts">
import {onMounted, onUnmounted, ref} from 'vue'
import {Spinner} from 'nom-ui'
import ErrorState from './ErrorState.vue'
import {loadState, onLoadProgress, startWasm} from '@/core/wasm'
import type {LoadState} from '@/core/wasm'

// Nothing below this component can run without the WebAssembly module: every
// operation, down to listing what is already stored, goes through it. So the
// wait is shown rather than hidden behind a spinner that never explains itself.
//
// The size is stated plainly. A few megabytes on a first visit is a real cost
// and the honest thing is to say what it buys — that the code which signs a
// transaction is the audited Go implementation, running here, and not a
// reimplementation written for the browser.

const state = ref<LoadState>({...loadState()})
let stop: (() => void) | null = null

onMounted(() => {
  stop = onLoadProgress((s) => (state.value = {...s}))
  void startWasm().catch(() => {})
})
onUnmounted(() => stop?.())

function mb(n: number) {
  return `${(n / 1048576).toFixed(1)} MB`
}

function retry() {
  // A failed load is almost always transient — a dropped connection on a
  // multi-megabyte fetch. Reloading is the only way back: the module's start is
  // deliberately once-only, so there is nothing to re-run in place.
  window.location.reload()
}
</script>

<template>
  <slot v-if="state.phase === 'ready'" />

  <div v-else-if="state.phase === 'failed'" class="grid gap-4 py-12">
    <ErrorState title="The signing engine did not load" :message="state.error" />
    <div class="text-sm text-muted-foreground">
      <p>
        Nothing has been lost — your swaps are still stored in this browser, just unreadable until
        the engine loads.
      </p>
      <div class="mt-3 flex flex-wrap items-center gap-3">
        <button
          type="button"
          class="rounded-md border border-border px-3 py-1.5 text-sm hover:bg-muted/60"
          @click="retry"
        >
          Reload and try again
        </button>
        <!-- Docs is the one page outside this gate, which is precisely so that
             it can be offered from inside it: a reader stuck here still needs
             to know that a recovery file plus any broadcast form is enough. -->
        <RouterLink
          :to="{name: 'docs', hash: '#recovery'}"
          class="text-sm text-primary underline-offset-4 hover:underline"
        >
          How to get your money out without this page
        </RouterLink>
      </div>
    </div>
  </div>

  <div v-else class="grid place-items-center gap-4 py-24 text-center">
    <Spinner class="size-5" />
    <div class="grid gap-1">
      <p class="text-sm font-medium">
        {{
          state.phase === 'fetching'
            ? 'Downloading the signing engine'
            : state.phase === 'compiling'
              ? 'Compiling the signing engine'
              : 'Starting the signing engine'
        }}
      </p>
      <p class="font-mono text-xs text-muted-foreground tabular-nums">
        <template v-if="state.phase === 'fetching' && state.totalBytes">
          {{ mb(state.loadedBytes) }} / {{ mb(state.totalBytes) }}
        </template>
        <template v-else-if="state.phase === 'fetching' && state.loadedBytes">
          {{ mb(state.loadedBytes) }}
        </template>
        <template v-else>&nbsp;</template>
      </p>
    </div>

    <!-- A determinate bar when the host sent a length, an indeterminate sweep
         when it did not — which is the norm for a gzipped response. -->
    <div class="h-1 w-64 max-w-full overflow-hidden rounded-full bg-muted">
      <div
        v-if="state.progress > 0"
        class="h-full bg-primary transition-[width] duration-200"
        :style="{width: `${Math.round(state.progress * 100)}%`}"
      />
      <div v-else class="h-full w-1/3 animate-pulse bg-primary/60" />
    </div>

    <p class="max-w-md text-xs text-muted-foreground">
      A few megabytes on a first visit, cached afterwards. It is the swap logic compiled to
      WebAssembly, and the reason there is no server to trust: every key and every signature stays
      in this page.
    </p>
  </div>
</template>

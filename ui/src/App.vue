<script setup lang="ts">
import {onMounted, onUnmounted} from 'vue'
import {Toaster, TooltipProvider} from 'nom-ui'
import AppFooter from '@/components/AppFooter.vue'
import AppHeader from '@/components/AppHeader.vue'
import EngineGate from '@/components/EngineGate.vue'
import {useFerry} from '@/core/composables/useFerry'
import {useAutoRefresh} from '@/core/composables/useAutoRefresh'

const {loadConfig, startPolling, stopPolling} = useFerry()
// Started here rather than on the swaps page, because a swap does not stop
// waiting on a chain while the user is reading the docs. It only ever asks
// about swaps already in the list, so on a page that never loaded one it costs
// a timer and no requests.
const {startAutoRefresh, stopAutoRefresh} = useAutoRefresh()

onMounted(async () => {
  // Both of these go through the WebAssembly module, which resolves its own
  // load before answering, so this needs no gate of its own.
  await loadConfig()
  startPolling()
  startAutoRefresh()
})
onUnmounted(() => {
  stopPolling()
  stopAutoRefresh()
})
</script>

<template>
  <!-- One provider for the whole app: it is what makes the ⓘ bubbles share a
       delay and close each other, so moving along a row of them reads as one
       control rather than a pile of overlapping ones. -->
  <TooltipProvider :delay-duration="120" :skip-delay-duration="300">
    <div class="flex min-h-screen flex-col">
      <AppHeader />
      <main class="mx-auto w-full max-w-5xl flex-1 px-4 py-8 sm:px-6">
        <!-- Almost every page is inside the gate, because almost every page
             needs the engine — including the one that only lists what is
             already stored. Docs is the exception, and it declares that on its
             own route rather than being named here. It is prose the build
             already contains: gating it would withhold "how do I get my money
             out" from the one reader who cannot get past the gate. -->
        <RouterView v-slot="{Component, route: r}">
          <EngineGate v-if="r.meta.engine !== false">
            <component :is="Component" />
          </EngineGate>
          <component :is="Component" v-else />
        </RouterView>
      </main>
      <AppFooter />
      <Toaster />
    </div>
  </TooltipProvider>
</template>

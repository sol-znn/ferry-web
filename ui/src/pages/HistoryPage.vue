<script setup lang="ts">
import {onMounted} from 'vue'
import {RefreshCwIcon} from '@lucide/vue'
import {Button, Heading} from 'nom-ui'
import InfoTip from '@/components/InfoTip.vue'
import HistoryRow from '@/components/HistoryRow.vue'
import EmptyState from '@/components/EmptyState.vue'
import ErrorState from '@/components/ErrorState.vue'
import LoadingState from '@/components/LoadingState.vue'
import {useFerry} from '@/core/composables/useFerry'
import {useAutoRefresh} from '@/core/composables/useAutoRefresh'

const {swaps, listError, loading, reload} = useFerry()
const {syncHistoryOnce} = useAutoRefresh()

// The list first, then the chain. Reading storage is instant and the cards can
// be up while the nodes are still being asked; going the other way would hold a
// blank page open for however long the slowest lookup takes.
//
// Not awaited into the mount: nothing on this page is waiting for it, and a
// node that hangs must not be able to hold the page's own load open. It reloads
// the list itself if anything came back different.
async function refresh() {
  await reload('history')
  void syncHistoryOnce()
}

onMounted(refresh)
</script>

<template>
  <section class="grid min-w-0 gap-4">
    <div class="flex items-center justify-between gap-4">
      <Heading :level="2" class="text-xl">History</Heading>
      <!-- The same two steps as arriving on the page, and for the same reason:
           a Reload that only re-read storage would be the weaker of the two
           ways to ask for the list, which is not what the button looks like. -->
      <Button variant="outline" size="sm" @click="refresh">
        <RefreshCwIcon />
        Reload
      </Button>
    </div>

    <p class="flex flex-wrap items-center gap-x-1.5 text-sm text-muted-foreground">
      Swaps you have filed away. Open one for everything it holds.
      <InfoTip label="What lands in history">
        <p>
          Only what you archive. Nothing arrives here on its own — a swap that finishes stays on the
          Swaps page as a summary line with an Archive button, so the last thing it does is show you
          how it ended rather than disappear.
        </p>
        <p>
          One of those lines waits longer than it looks: a contract you funded that the counterparty
          redeemed, when you are the participant. Their redeem is what publishes the preimage, so
          that is the moment your claim on the Zenon leg <em>begins</em>, not the moment the swap
          ends — it keeps the full card until you have collected the ZNN.
        </p>
        <p>
          Opening a row gives the full card — recovery file, event log, every hash — and any swap
          can be moved back to the active list. Deleting one is the only thing here that cannot be
          undone.
        </p>
      </InfoTip>
    </p>

    <ErrorState v-if="listError" :message="listError" />
    <LoadingState v-else-if="loading && !swaps.length" />
    <EmptyState v-else-if="!swaps.length" message="No finished swaps yet." />
    <!-- gap-2, not the page's gap-4: these are rows now, and a card's worth of
         air between them reads as separate things rather than a list. -->
    <div v-else class="grid min-w-0 gap-2">
      <HistoryRow v-for="sw in swaps" :key="sw.id" :swap="sw" @changed="reload('history')" />
    </div>
  </section>
</template>

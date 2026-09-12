<script setup lang="ts">
import {LinkIcon, UnlinkIcon, WalletIcon} from '@lucide/vue'
import {Button} from 'nom-ui'
import InfoTip from './InfoTip.vue'
import {SYRIUS_EXTENSION_URL} from '@/core/zenon-wallet'
import {useZenonWallet} from '@/core/composables/useZenonWallet'

/**
 * The Syrius connect control, and nothing else.
 *
 * `ZenonWallet` is the panel that builds and hands over a block; this is the
 * strip that gets an address. They are separate because the address is wanted
 * long before any block exists — the create form needs it in step two, when
 * there is no swap to sign for yet.
 *
 * Offered whether or not the extension was detected, for the reason
 * WalletConnect gives about UniSat: detection is a race against whenever the
 * content script runs, and pressing the button asks the wallet directly.
 */
const props = withDefaults(
  defineProps<{
    /** Called with a fresh address whenever one becomes available. */
    onAddress?: (addr: string) => void
    size?: 'sm' | 'default'
  }>(),
  {size: 'sm'},
)

const {available, address, connected, connecting, error, connect, forget} = useZenonWallet()

async function onConnect() {
  await connect()
  hand()
}

function hand() {
  if (address.value) props.onAddress?.(address.value)
}
</script>

<template>
  <div class="grid gap-2">
    <div class="flex flex-wrap items-center gap-2">
      <template v-if="!connected">
        <Button variant="outline" :size="props.size" :disabled="connecting" @click="onConnect">
          <LinkIcon />
          {{ connecting ? 'Waiting for Syrius…' : 'Connect Syrius' }}
        </Button>
        <span v-if="available" class="text-xs text-muted-foreground">
          to fill in your address and sign the Zenon leg
        </span>
        <span v-else class="flex items-center gap-1.5 text-xs text-muted-foreground">
          <WalletIcon class="size-3.5" />
          No extension detected yet — press Connect anyway
          <InfoTip label="What the Zenon wallet is used for">
            <p>
              Ferry builds the HTLC call as bytes and the extension signs it with its own key, mines
              its own plasma and publishes through its own node. No Zenon key ever reaches this
              page.
            </p>
            <p>
              Get it from
              <a
                :href="SYRIUS_EXTENSION_URL"
                target="_blank"
                rel="noreferrer noopener"
                class="text-primary underline-offset-4 hover:underline"
                >the Syrius extension releases</a
              >.
            </p>
          </InfoTip>
        </span>
      </template>

      <template v-else>
        <span
          class="flex items-center gap-1.5 rounded-md border border-border bg-muted/40 px-2 py-1 font-mono text-xs"
        >
          <WalletIcon class="size-3.5 text-muted-foreground" />
          {{ address }}
        </span>
        <Button v-if="props.onAddress" variant="ghost" size="sm" @click="hand">
          Use this address
        </Button>
        <Button variant="ghost" size="sm" @click="forget">
          <UnlinkIcon />
          Disconnect
        </Button>
      </template>
    </div>

    <p v-if="error" class="text-xs text-destructive">{{ error }}</p>
  </div>
</template>

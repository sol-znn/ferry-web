<script setup lang="ts">
import {computed} from 'vue'
import {LinkIcon, UnlinkIcon, WalletIcon} from '@lucide/vue'
import {Button} from 'nom-ui'
import InfoTip from './InfoTip.vue'
import {lamportsToSol, shortSol} from '@/core/solana'
import {useSolana} from '@/core/composables/useSolana'
import {isDev} from '@/core/env'

/**
 * The Solana connect control.
 *
 * Offered whether or not a wallet was detected, for the reason WalletConnect
 * gives about UniSat: detection is a race against whenever the extension's
 * content script runs, and a page that hides its only wallet control on losing
 * that race tells people they have nothing installed while it sits in their
 * toolbar. Pressing the button asks the wallet directly, which is the only
 * answer that was ever authoritative.
 *
 * The one warning that travels with it is not about a chain but about a
 * mechanism: a wallet that can only sign-and-send submits through whichever
 * cluster IT is pointed at, not through the node named in Node settings. On a
 * local validator those are never the same chain, and the difference is a swap
 * step landing or vanishing.
 */
const props = withDefaults(
  defineProps<{
    /** Called with a fresh address whenever one becomes available. */
    onAddress?: (addr: string) => void
    size?: 'sm' | 'default'
  }>(),
  {size: 'sm'},
)

const {
  wallet,
  available,
  walletName,
  connected,
  address,
  lamports,
  busy,
  error,
  connect,
  disconnect,
  useThrowawayKey,
  airdrop,
  installUrl,
} = useSolana()

const local = computed(() => wallet.kind === 'local')

async function onConnect() {
  if (await connect()) hand()
}

// Handing the address up is a separate step from connecting, so the same button
// can refill a field the user has since cleared without a second prompt.
function hand() {
  if (address.value) props.onAddress?.(address.value)
}

function onLocalKey() {
  if (useThrowawayKey()) hand()
}
</script>

<template>
  <div class="grid gap-2">
    <div class="flex flex-wrap items-center gap-2">
      <template v-if="!connected">
        <Button variant="outline" :size="props.size" :disabled="busy" @click="onConnect">
          <LinkIcon />
          {{ busy ? `Waiting for ${walletName}…` : `Connect ${walletName}` }}
        </Button>
        <span v-if="available" class="text-xs text-muted-foreground">
          to fill in your address and sign the Solana leg
        </span>
        <span v-else class="flex items-center gap-1.5 text-xs text-muted-foreground">
          <WalletIcon class="size-3.5" />
          No wallet detected yet — press Connect anyway
          <InfoTip label="What a Solana wallet is used for">
            <p>
              “Not detected” usually means “not yet”: an extension injects itself whenever its own
              script runs. Pressing Connect asks the wallet directly.
            </p>
            <p>
              With
              <a
                :href="installUrl"
                target="_blank"
                rel="noreferrer noopener"
                class="text-primary underline-offset-4 hover:underline"
                >Phantom</a
              >
              — or Solflare, or Backpack — it fills in your payout address, signs the escrow when
              you fund one, and signs a statement once so the board can badge your address. It is
              never asked for a key.
            </p>
          </InfoTip>
        </span>
        <!-- A throwaway key, on the development instance only. The whole point
             of a local validator is that it gets wiped; pointing a real wallet
             at a chain like that is a bad trade. -->
        <Button v-if="isDev" variant="ghost" size="sm" :disabled="busy" @click="onLocalKey">
          Use a key in this browser
        </Button>
      </template>

      <template v-else>
        <span
          class="flex items-center gap-1.5 rounded-md border border-border bg-muted/40 px-2 py-1 font-mono text-xs"
        >
          <WalletIcon class="size-3.5 text-muted-foreground" />
          {{ shortSol(address) }}
          <span v-if="lamports !== null" class="text-muted-foreground">
            · {{ lamportsToSol(lamports) }} SOL
          </span>
        </span>
        <Button v-if="props.onAddress" variant="ghost" size="sm" @click="hand">
          Use this address
        </Button>
        <Button v-if="isDev" variant="ghost" size="sm" :disabled="busy" @click="() => airdrop(2)">
          Airdrop 2 SOL
        </Button>
        <Button variant="ghost" size="sm" :disabled="busy" @click="disconnect">
          <UnlinkIcon />
          Disconnect
        </Button>
      </template>
    </div>

    <p v-if="local" class="text-xs text-muted-foreground">
      This is a key in this browser, not a wallet. It exists for a chain that gets reset; it is not
      how a swap with money in it should be signed.
    </p>

    <!-- Not a chain mismatch but a submission one, and it is the sharper edge on
         a local validator: a wallet that only sign-and-sends never sees the node
         this page is pointed at. -->
    <div
      v-if="wallet.sendsThroughItsOwnRpc"
      class="rounded-md border border-warning/50 bg-warning/5 p-2.5 text-xs text-warning"
    >
      {{ wallet.name }} can only sign-and-send, so it submits through whichever cluster it is
      pointed at rather than the endpoint in Node settings. Make sure the two are the same chain.
    </div>

    <p v-if="error" class="text-xs text-destructive">{{ error }}</p>
  </div>
</template>

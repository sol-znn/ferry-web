<script setup lang="ts">
import {computed} from 'vue'
import {LinkIcon, UnlinkIcon, WalletIcon} from '@lucide/vue'
import {Button} from 'nom-ui'
import InfoTip from './InfoTip.vue'
import {sats} from '@/core/format'
import {shortAddr} from '@/core/unisat'
import {useUnisat} from '@/core/composables/useUnisat'
import {useSettings, type Network} from '@/core/composables/useSettings'

// The connect control, and the one warning that has to travel with it.
//
// A connected wallet on the wrong chain is worse than no wallet: every address
// it hands over looks fine and pays out somewhere unreachable. So the mismatch
// is not a note under the button, it is the button — while the wallet and the
// app disagree, the only thing offered is the switch.
//
// The connect button is offered whether or not a wallet was detected, and that
// is deliberate. Detection is a race against whenever the extension's content
// script runs, and a page that hides its only wallet control on losing that
// race tells people they have nothing installed while it sits in their
// toolbar. Pressing the button asks the wallet directly, which is the only
// answer that was ever authoritative; undetected merely changes the sentence
// beside it from an invitation into a hint about what to try.
const props = withDefaults(
  defineProps<{
    /** Called with a fresh address whenever one becomes available. */
    onAddress?: (addr: string) => void
    size?: 'sm' | 'default'
  }>(),
  {size: 'sm'},
)

const {
  available,
  connected,
  address,
  balance,
  walletNetwork,
  busy,
  error,
  connect,
  disconnect,
  switchTo,
} = useUnisat()
const {stored} = useSettings()

const network = computed(() => stored.value.network as Network)
const mismatch = computed(
  () => connected.value && Boolean(walletNetwork.value) && walletNetwork.value !== network.value,
)

async function onConnect() {
  if (await connect()) hand()
}

// Handing the address up is a separate step from connecting, so the same button
// can refill a field the user has since cleared without a second prompt.
function hand() {
  if (address.value && !mismatch.value) props.onAddress?.(address.value)
}

async function onSwitch() {
  if (await switchTo(network.value)) hand()
}
</script>

<template>
  <div class="grid gap-2">
    <div class="flex flex-wrap items-center gap-2">
      <template v-if="!connected">
        <!-- The label changes because the click can now WAIT for a wallet that
             is still injecting rather than failing on the spot, and a button
             that has gone quiet for a second reads as broken unless it says
             what it is doing. -->
        <Button variant="outline" :size="props.size" :disabled="busy" @click="onConnect">
          <LinkIcon />
          {{ busy ? 'Waiting for UniSat…' : 'Connect UniSat' }}
        </Button>
        <span v-if="available" class="text-xs text-muted-foreground">
          to fill in your address and fund with one click
        </span>
        <span v-else class="flex items-center gap-1.5 text-xs text-muted-foreground">
          <WalletIcon class="size-3.5" />
          No wallet detected yet — press Connect anyway
          <InfoTip label="What a wallet connection is used for">
            <p>
              “Not detected” usually means “not yet”: an extension injects itself whenever its own
              script runs. Pressing Connect asks the wallet directly.
            </p>
            <p>
              With
              <a
                href="https://unisat.io/download"
                target="_blank"
                rel="noreferrer noopener"
                class="text-primary underline-offset-4 hover:underline"
                >UniSat</a
              >
              it fills in your payout address and funds a contract in one click. The swap's own key
              is never shown to it — the wallet is only asked for an address and an ordinary send.
            </p>
          </InfoTip>
        </span>
      </template>

      <template v-else>
        <span
          class="flex items-center gap-1.5 rounded-md border border-border bg-muted/40 px-2 py-1 font-mono text-xs"
        >
          <WalletIcon class="size-3.5 text-muted-foreground" />
          {{ shortAddr(address) }}
          <span v-if="balance" class="text-muted-foreground">· {{ sats(balance.total) }}</span>
        </span>
        <!-- Only where something is listening. On the board key box there is no
             field to fill, so this was a button that did nothing when pressed. -->
        <Button v-if="props.onAddress" variant="ghost" size="sm" @click="hand">
          Use this address
        </Button>
        <Button variant="ghost" size="sm" :disabled="busy" @click="disconnect">
          <UnlinkIcon />
          Disconnect
        </Button>
      </template>
    </div>

    <!-- The wrong chain is the failure worth interrupting for: it produces a
         valid-looking address that pays out on a network this swap is not on. -->
    <div
      v-if="mismatch"
      class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md border border-warning/50 bg-warning/5 p-2.5 text-xs text-warning"
    >
      <span class="min-w-0 flex-1">
        UniSat is on <span class="font-mono">{{ walletNetwork }}</span> and this app is set to
        <span class="font-mono">{{ network }}</span
        >. Its addresses will not work here.
      </span>
      <Button variant="outline" size="sm" :disabled="busy" @click="onSwitch">
        Switch wallet to {{ network }}
      </Button>
    </div>

    <p v-if="error" class="text-xs text-destructive">{{ error }}</p>
  </div>
</template>

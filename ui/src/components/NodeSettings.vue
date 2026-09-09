<script setup lang="ts">
import {ref, watch} from 'vue'
import {ServerIcon} from '@lucide/vue'
import {
  Button,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
  Input,
  Label,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from 'nom-ui'
import InfoTip from './InfoTip.vue'
import {DEFAULT_ESPLORA, NETWORKS, useSettings} from '@/core/composables/useSettings'
import {useFerry} from '@/core/composables/useFerry'
import type {Network} from '@/core/composables/useSettings'
import {DEFAULT_RELAYS} from '@/core/nostr'
import type {Settings} from '@/types'

// This dialog IS the node choice — there is no operator to fall back to. A
// blank Esplora field falls back to a public instance; a blank Zenon field falls
// back to nothing at all, which is deliberate: see the Zenon field below.

const {stored, save, reload} = useSettings()
const {loadConfig} = useFerry()

const emit = defineEmits<{reloaded: []}>()

/**
 * A public Zenon node, offered as a click rather than as a default. A default is
 * a node the user never chose and would never think about, on the one call where
 * being lied to costs money; a filled field they pressed is a node they picked,
 * next to a sentence saying whose it is. The wss endpoint, because that is the
 * one a browser can open against infrastructure nobody here configured.
 */
const PUBLIC_ZNN_WS = 'wss://node.zenonhub.io:35998'

const open = ref(false)
const error = ref('')
const form = ref<Required<Settings>>({
  network: 'mainnet',
  btcEsplora: '',
  znnUrl: '',
  relays: '',
})

// Re-sync every time it opens, so a change made in another tab is reflected
// rather than showing stale fields.
watch(open, (isOpen) => {
  if (!isOpen) return
  reload()
  error.value = ''
  form.value = {
    network: stored.value.network,
    btcEsplora: stored.value.btcEsplora ?? '',
    znnUrl: stored.value.znnUrl ?? '',
    relays: stored.value.relays ?? '',
  }
})

function onSave() {
  error.value = ''
  const next: Settings = {
    network: form.value.network,
    btcEsplora: form.value.btcEsplora.trim(),
    znnUrl: form.value.znnUrl.trim(),
    relays: form.value.relays.trim(),
  }
  // A URL typed without a scheme is the single most common way this fails, and
  // it fails as an opaque fetch error minutes later rather than here.
  if (next.btcEsplora && !/^https?:\/\//i.test(next.btcEsplora)) {
    error.value = 'The Esplora URL needs a scheme — start it with https:// or http://'
    return
  }
  // The Zenon side takes either transport, because which one reaches a given
  // node is a property of the node rather than a preference: the HTTP endpoint
  // is a cross-origin request most public nodes refuse, and the WebSocket has
  // no preflight to refuse.
  if (next.znnUrl && !/^(https?|wss?):\/\//i.test(next.znnUrl)) {
    error.value = 'The Zenon URL needs a scheme — wss:// (port 35998) or https:// (port 35997)'
    return
  }
  // Mixed content is blocked before the request leaves, and the resulting
  // failure names nothing useful, so it is worth catching against the page's
  // own scheme rather than letting it surface as "connection failed".
  if (window.location.protocol === 'https:' && /^(http|ws):\/\//i.test(next.znnUrl ?? '')) {
    error.value =
      'This page is served over https, so it cannot reach an http:// or ws:// node — ' +
      'the browser blocks it as mixed content. Use wss:// or https://.'
    return
  }
  for (const url of (next.relays ?? '').split(/[\s,]+/).filter(Boolean)) {
    if (!/^wss?:\/\//i.test(url)) {
      error.value = `Relay URLs are websockets — ${url} needs to start with wss://`
      return
    }
  }
  save(next)
  open.value = false
  void loadConfig().then(() => emit('reloaded'))
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogTrigger as-child>
      <Button variant="outline" size="sm" class="gap-1.5">
        <ServerIcon />
        Nodes
        <span
          v-if="stored.btcEsplora || stored.znnUrl"
          class="size-1.5 rounded-full bg-primary"
          aria-hidden="true"
        />
      </Button>
    </DialogTrigger>

    <DialogContent class="max-h-[85vh] overflow-y-auto sm:max-w-lg">
      <DialogHeader>
        <DialogTitle class="flex items-center gap-1.5">
          Network and nodes
          <InfoTip label="Why the node you pick matters">
            Funding detection, HTLC verification and block heights are only as trustworthy as the
            node that answers them. A dishonest one can tell you an unsafe HTLC is fine, or that a
            funded contract is empty. Pointing these at infrastructure you run is the difference
            between verifying a swap and being told about one.
          </InfoTip>
        </DialogTitle>
        <DialogDescription>
          Which chains this browser watches, and who it asks. Saved here and nowhere else — they
          never travel with a swap, and no server sees them.
        </DialogDescription>
      </DialogHeader>

      <div class="grid gap-4">
        <div class="grid gap-2">
          <Label class="flex items-center gap-1.5">
            Network
            <InfoTip label="What changing the network does">
              It applies to swaps you create from now on. A swap keeps the network it was created
              on, and while the two disagree its chain actions are switched off.
            </InfoTip>
          </Label>
          <Select
            :model-value="form.network"
            @update:model-value="(v) => (form.network = String(v) as Network)"
          >
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem v-for="n in NETWORKS" :key="n" :value="n">{{ n }}</SelectItem>
            </SelectContent>
          </Select>
        </div>

        <div class="grid gap-2">
          <Label for="ns-esplora" class="flex items-center gap-1.5">
            Bitcoin — Esplora base URL
            <InfoTip label="About the Esplora URL">
              This is what sees your funding transaction and broadcasts your spends. Your own
              instance needs to send CORS headers for a web page to reach it.
            </InfoTip>
          </Label>
          <Input
            id="ns-esplora"
            v-model="form.btcEsplora"
            spellcheck="false"
            autocapitalize="none"
            autocomplete="off"
            :placeholder="DEFAULT_ESPLORA[form.network as Network]"
            class="font-mono"
          />
          <p class="text-xs text-muted-foreground">
            Blank uses <span class="font-mono">{{ DEFAULT_ESPLORA[form.network as Network] }}</span
            >.
          </p>
        </div>

        <div class="grid gap-2">
          <Label for="ns-znn" class="flex items-center gap-1.5">
            Zenon — node JSON-RPC URL
            <InfoTip variant="warn" label="Which URL, and why there is no default">
              <p>
                Verifying a counterparty's HTLC is the one check where a dishonest answer costs
                money, so the node has to be one you chose rather than one this page picked for you.
              </p>
              <p>
                Either transport works. <code>wss://…:35998</code> is the one to reach for: it is
                the endpoint Syrius and znn-cli use, and a WebSocket needs no CORS preflight, so it
                works against public nodes that the HTTP endpoint on 35997 does not.
              </p>
            </InfoTip>
          </Label>
          <Input
            id="ns-znn"
            v-model="form.znnUrl"
            spellcheck="false"
            autocapitalize="none"
            autocomplete="off"
            placeholder="wss://your-node:35998"
            class="font-mono"
          />
          <p class="text-xs text-muted-foreground">
            <button
              type="button"
              class="font-mono text-primary underline-offset-4 hover:underline"
              @click="form.znnUrl = PUBLIC_ZNN_WS"
            >
              {{ PUBLIC_ZNN_WS }}
            </button>
            is a public node — it is somebody else's, so use your own where you can.
          </p>
          <p class="text-xs text-warning">
            No default on purpose. Until you set one, the Zenon leg cannot be verified.
          </p>
        </div>

        <div class="grid gap-2">
          <Label for="ns-relays" class="flex items-center gap-1.5">
            Session relays
            <InfoTip label="What relays carry">
              <p>
                Only the live sessions feature uses these — swaps work without them. A session is
                two browsers passing a pubkey hash, a contract and an HTLC id to each other, and a
                relay is what lets them find each other with no server in between.
              </p>
              <p>
                What a relay holds is ciphertext under a key your session code derives, so it can
                carry a conversation it cannot read and cannot join. Blank uses a few well-known
                public relays; name your own to use nobody else's.
              </p>
            </InfoTip>
          </Label>
          <Input
            id="ns-relays"
            v-model="form.relays"
            spellcheck="false"
            autocapitalize="none"
            autocomplete="off"
            :placeholder="DEFAULT_RELAYS.join(', ')"
            class="font-mono"
          />
          <p class="text-xs text-muted-foreground">
            Space- or comma-separated <span class="font-mono">wss://</span> URLs. More than one is
            the point: any single relay may be down or dropping messages.
          </p>
        </div>

        <p v-if="error" class="text-sm text-destructive">{{ error }}</p>
      </div>

      <DialogFooter class="sm:justify-between">
        <!-- Closed on the way out: leaving a modal open over the page it just
             navigated to is how a link inside a dialog usually goes wrong. -->
        <RouterLink
          :to="{name: 'docs', hash: '#what-you-need'}"
          class="self-center text-sm text-primary underline-offset-4 hover:underline"
          @click="open = false"
        >
          What these nodes have to be
        </RouterLink>
        <Button @click="onSave">Save</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>

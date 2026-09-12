<script setup lang="ts">
import {computed, ref, watch} from 'vue'
import {UploadIcon} from '@lucide/vue'
import {
  Badge,
  Button,
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  CopyButton,
  Heading,
  Input,
  Label,
  Textarea,
} from 'nom-ui'
import InfoTip from '@/components/InfoTip.vue'
import UnlockCost from '@/components/UnlockCost.vue'
import DataList from '@/components/DataList.vue'
import DataRow from '@/components/DataRow.vue'
import {api} from '@/core/api'
import {sats} from '@/core/format'
import {useUnlockCost} from '@/core/composables/useUnlockCost'
import type {RebuildResult, Settings} from '@/types'

// Offline rescue, as a page.
//
// It depends on nothing: no stored swap, no node, no network, no settings --
// just a recovery file the user saved earlier. Save this site to disk and open
// it with the machine unplugged and this page still gets money out of a
// contract, because the signing engine is already in the page and the contract
// tells it which branch the key can take.
//
// It prints hex and does not broadcast. Getting the hex to a node is a separate,
// reversible step any public broadcast form will take, and keeping it separate
// is what lets this page stay offline.

const fileText = ref('')
const destAddr = ref('')
const feeRate = ref('')
const secretHex = ref('')
// A file from before the funding's script was recorded cannot be checked
// offline against what the output pays. Building it anyway is the user's call,
// asked for explicitly and only once the refusal has said why.
const allowUnbound = ref(false)
const unboundRefusal = computed(() =>
  /does not record what the funding output pays/.test(error.value),
)

const busy = ref(false)
const error = ref('')
const result = ref<RebuildResult | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)

/** Whatever of the file this page can read without the module. */
const parsed = computed<Record<string, unknown> | null>(() => {
  if (!fileText.value.trim()) return null
  try {
    const doc: unknown = JSON.parse(fileText.value)
    return doc && typeof doc === 'object' ? (doc as Record<string, unknown>) : null
  } catch {
    return null
  }
})

/** The pre-signed refund, if the file already carries one. */
const presigned = computed(() => {
  const hex = parsed.value?.presignedRefundHex
  return typeof hex === 'string' ? hex : ''
})

/**
 * What this contract can afford, from the file alone.
 *
 * This is the page somebody reaches after a spend was refused for dust, and the
 * question they arrive with is what rate DOES work. It needs no network, because
 * a fee rate is supplied -- which is what keeps this page usable with the
 * machine unplugged.
 */
const fileNetwork = computed(() => {
  const n = parsed.value?.network
  return typeof n === 'string' && n ? n : 'mainnet'
})
const unlockInput = computed(() => {
  const funding = parsed.value?.funding as {value?: unknown} | undefined
  const value = typeof funding?.value === 'number' ? funding.value : 0
  const contract = parsed.value?.contractHex
  if (!value || typeof contract !== 'string') return null
  const dest = destAddr.value.trim() || (parsed.value?.destAddr as string | undefined) || ''
  const typed = Number(feeRate.value)
  return {
    amountSats: value,
    contractHex: contract,
    destAddr: dest,
    // A rate typed into the field is quoted as typed. Left blank, none is sent
    // — which makes the module ask the Bitcoin node for a live estimate rather
    // than assume a figure. That is a network call, and it is the one place on
    // this page that makes one: it degrades to a stated fallback when nothing
    // answers, so the page still works with the machine unplugged, which is the
    // whole point of it.
    ...(typed > 0 ? {feeRate: typed} : {}),
  }
})
const quoteSettings = computed<Settings>(() => ({network: fileNetwork.value}))
const {
  cost: unlockCost,
  loading: unlockLoading,
  error: unlockError,
} = useUnlockCost(unlockInput, quoteSettings)

/** Drop the highest workable rate into the fee field. */
function useMaxFeeRate() {
  if (unlockCost.value?.maxFeeRate) feeRate.value = String(unlockCost.value.maxFeeRate)
}

async function onFile(ev: Event) {
  const input = ev.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  fileText.value = await file.text()
  result.value = null
  error.value = ''
}
// Consent is about one file. A different file, loaded or pasted, starts over.
watch(fileText, () => {
  allowUnbound.value = false
})

async function rebuild() {
  error.value = ''
  result.value = null
  busy.value = true
  try {
    result.value = await api.rebuild({
      file: fileText.value,
      destAddr: destAddr.value.trim(),
      feeRate: Number(feeRate.value || 0),
      secretHex: secretHex.value.trim(),
      allowUnboundFunding: allowUnbound.value,
    })
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="grid gap-6">
    <div class="grid gap-2">
      <Heading :level="2" class="text-xl">Recover from a file</Heading>
      <p class="flex flex-wrap items-center gap-x-1.5 text-sm text-muted-foreground">
        Get money out of a swap contract using nothing but the recovery file you saved.
        <InfoTip label="What a recovery file holds">
          <p>
            Everything needed to spend one contract: the contract itself, the ephemeral private key,
            the funding output, and — once funding was seen — an already-signed refund transaction.
          </p>
          <p>
            Nothing on this page reaches a node. It needs no swap in this browser and no settings,
            so it works from a saved copy of this site on a machine that has never been online.
          </p>
        </InfoTip>
      </p>
    </div>

    <!-- The most likely visitor to this page does not need this page. Say so
         first, in the loudest thing on it, rather than after two paragraphs. -->
    <div class="rounded-lg border border-primary/40 bg-primary/5 p-4">
      <h3 class="text-sm font-semibold">If the swap just stalled, you probably do not need this</h3>
      <p class="mt-1 text-sm text-muted-foreground">
        Wait for the locktime to pass, then broadcast the pre-signed refund already in your file —
        it is valid as it stands. Rebuild here only when fees have risen since it was signed, the
        money should go somewhere else, or the preimage turned up and you want to claim rather than
        reclaim.
      </p>
      <RouterLink
        :to="{name: 'docs', hash: '#recovery'}"
        class="mt-2 inline-block text-sm text-primary underline-offset-4 hover:underline"
      >
        All three ways out of a contract
      </RouterLink>
    </div>

    <Card>
      <CardHeader><CardTitle>1 · The recovery file</CardTitle></CardHeader>
      <CardContent class="grid gap-3">
        <div>
          <Button variant="outline" size="sm" @click="fileInput?.click()">
            <UploadIcon />
            Choose file
          </Button>
          <input
            ref="fileInput"
            type="file"
            accept="application/json,.json"
            class="hidden"
            @change="onFile"
          />
        </div>
        <Textarea
          v-model="fileText"
          rows="6"
          spellcheck="false"
          autocapitalize="none"
          placeholder="…or paste the file's contents here"
          class="font-mono text-xs"
        />

        <div
          v-if="presigned"
          class="grid gap-2 rounded-lg border border-success/50 bg-success/5 p-3"
        >
          <h3 class="text-sm font-semibold">This file already has a signed refund</h3>
          <p class="text-sm text-muted-foreground">
            Broadcast this after the locktime and you are done — no decisions, no rebuild. It pays
            the destination and fee it was signed with.
          </p>
          <!-- min-w-0 is load-bearing: a grid item defaults to min-width:auto,
               so an unbroken 500-character hex string widens the track and the
               whole page scrolls sideways. And the hex WRAPS rather than
               scrolling, because it has no line structure worth preserving —
               a scrollbar would just hide the end of it. -->
          <div class="relative min-w-0">
            <pre
              class="max-h-40 overflow-y-auto rounded-md bg-background p-3 pr-12 font-mono text-xs break-all whitespace-pre-wrap"
              >{{ presigned }}</pre>
            <CopyButton :value="presigned" size="icon-sm" class="absolute top-2 right-2" />
          </div>
        </div>
      </CardContent>
    </Card>

    <Card>
      <CardHeader class="gap-1.5">
        <CardTitle class="flex items-center gap-1.5">
          2 · Rebuild the spend
          <InfoTip label="Which branch gets built">
            The contract has two branches — redeem, which needs the preimage, and refund, which
            needs the timelock to have passed. Which one your key can take is a fact about the
            contract, so it is read out of the contract rather than chosen here. Asking for the
            wrong one gets an explanation instead of a broken signature.
          </InfoTip>
        </CardTitle>
        <p class="text-sm text-muted-foreground">
          Every field is optional — leave them blank and the file's own values are used.
        </p>
      </CardHeader>
      <CardContent class="grid gap-4">
        <div class="grid gap-4 sm:grid-cols-2">
          <div class="grid gap-2">
            <Label for="r-dest">Destination address</Label>
            <Input
              id="r-dest"
              v-model="destAddr"
              spellcheck="false"
              autocapitalize="none"
              autocomplete="off"
              placeholder="blank = the address in the file"
              class="font-mono"
            />
          </div>
          <div class="grid gap-2">
            <Label for="r-fee" class="flex items-center gap-1.5">
              Fee rate (sat/vB)
              <InfoTip label="When to raise the fee">
                Raise it if the pre-signed refund is too cheap to be relayed at today's fees. It is
                the one reason most people end up on this page.
              </InfoTip>
            </Label>
            <Input
              id="r-fee"
              v-model="feeRate"
              inputmode="decimal"
              placeholder="blank = 2"
              class="font-mono tabular-nums"
            />
          </div>
        </div>
        <!-- The arithmetic behind the refusal that sent most people here, with
             the rate that works beside it rather than in an error message they
             have to read twice. -->
        <UnlockCost
          v-if="unlockCost || unlockLoading"
          :cost="unlockCost"
          :loading="unlockLoading"
          :error="unlockError"
          unlocked-by="you"
        />
        <div
          v-if="unlockCost && unlockCost.maxFeeRate && !unlockCost.redeem.viable"
          class="flex flex-wrap items-center gap-2"
        >
          <Button variant="outline" size="sm" @click="useMaxFeeRate">
            Use {{ unlockCost.maxFeeRate }} sat/vB — the highest that works
          </Button>
          <span class="text-xs text-muted-foreground">
            it is a low rate, so this may take a while to confirm
          </span>
        </div>

        <div class="grid gap-2">
          <Label for="r-secret" class="flex items-center gap-1.5">
            Preimage
            <InfoTip label="Only to claim rather than reclaim">
              Fill this in only if you have the secret and want to take the contract's redeem
              branch. Left blank, a refund is built instead.
            </InfoTip>
          </Label>
          <Input
            id="r-secret"
            v-model="secretHex"
            spellcheck="false"
            autocapitalize="none"
            autocomplete="off"
            placeholder="64 hex chars, blank = build the refund"
            class="font-mono"
          />
        </div>

        <label
          v-if="unboundRefusal || allowUnbound"
          class="flex items-start gap-2 rounded-md border border-warning/40 bg-warning/5 p-3 text-sm"
        >
          <input v-model="allowUnbound" type="checkbox" class="mt-0.5" />
          <span>
            Build the refund anyway. This file does not record what the funding output pays, and
            this page cannot ask a node, so the refund is built for the contract in the file. A
            refund reveals nothing: if that is not the contract that was funded, the network refuses
            the transaction and nothing is lost. A redeem is never built this way, because a redeem
            carries the preimage.
          </span>
        </label>
        <Button class="justify-self-start" :disabled="busy || !fileText.trim()" @click="rebuild">
          Rebuild and sign
        </Button>
        <p v-if="error" class="text-sm whitespace-pre-line text-destructive">{{ error }}</p>
      </CardContent>
    </Card>

    <Card v-if="result">
      <CardHeader class="flex flex-row flex-wrap items-center gap-3">
        <CardTitle>Signed {{ result.action }}</CardTitle>
        <Badge :variant="result.action === 'redeem' ? 'success' : 'warning'">
          {{ result.action }}
        </Badge>
        <Badge v-if="result.warning" variant="warning">built on an assumption</Badge>
        <Badge v-if="result.notYet" variant="destructive">
          not valid for another {{ result.notYet }}
        </Badge>
      </CardHeader>
      <CardContent class="grid gap-4">
        <p v-if="result.warning" class="text-sm text-warning">{{ result.warning }}</p>
        <DataList dense>
          <DataRow label="swap">
            <span class="font-mono text-xs">{{ result.swapId }} ({{ result.network }})</span>
          </DataRow>
          <DataRow label="contract">
            <span class="font-mono text-xs break-all">{{ result.contractAddr }}</span>
          </DataRow>
          <DataRow label="spending">
            <span class="font-mono text-xs break-all">{{ result.spending }}</span>
          </DataRow>
          <DataRow label="paying">
            <span class="font-mono text-xs break-all">
              {{ sats(result.value) }} to {{ result.destAddr }}
            </span>
          </DataRow>
          <DataRow label="fee">
            <span class="font-mono text-xs tabular-nums">
              {{ sats(result.fee) }} ({{ result.feeRate }} sat/vB over {{ result.vsize }} vB)
            </span>
          </DataRow>
          <DataRow label="txid">
            <span class="font-mono text-xs break-all">{{ result.txid }}</span>
          </DataRow>
          <DataRow v-if="result.validFrom" label="valid from">
            <span class="font-mono text-xs">{{ result.validFrom }}</span>
          </DataRow>
        </DataList>

        <p v-if="result.notYet" class="flex items-start gap-1.5 text-sm text-warning">
          <span class="min-w-0 flex-1">
            The network will reject this until the locktime passes, and for roughly an hour after
            that. Save the hex and retry later.
          </span>
          <InfoTip variant="warn" label="Why an hour after">
            <code>OP_CHECKLOCKTIMEVERIFY</code> is compared against the block median time past,
            which trails real time.
          </InfoTip>
        </p>

        <div class="relative min-w-0">
          <pre
            class="max-h-56 overflow-y-auto rounded-md bg-muted/60 p-3 pr-12 font-mono text-xs break-all whitespace-pre-wrap"
            >{{ result.rawHex }}</pre>
          <CopyButton :value="result.rawHex" size="icon-sm" class="absolute top-2 right-2" />
        </div>
        <p class="text-xs text-muted-foreground">
          Broadcast it anywhere — <span class="font-mono">https://mempool.space/tx/push</span>, your
          own node's <span class="font-mono">sendrawtransaction</span>, or any explorer's push form.
        </p>
      </CardContent>
    </Card>
  </div>
</template>

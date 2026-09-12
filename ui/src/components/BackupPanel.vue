<script setup lang="ts">
import {ref} from 'vue'
import {DownloadIcon, UploadIcon} from '@lucide/vue'
import {Button, Card, CardContent, CardHeader, CardTitle} from 'nom-ui'
import InfoTip from './InfoTip.vue'
import {api, download} from '@/core/api'
import {useFerry} from '@/core/composables/useFerry'

// Browser storage has no directory to copy: it is scoped to one origin,
// invisible to every backup tool, and erased by the same "clear site data"
// gesture people use to fix an unrelated site. So the way out has to be built,
// and it has to be obvious, because what is being carried is a set of keys that
// can spend live contracts.

const {reload} = useFerry()

const busy = ref(false)
const message = ref('')
const error = ref('')
const fileInput = ref<HTMLInputElement | null>(null)

async function exportAll() {
  error.value = ''
  message.value = ''
  busy.value = true
  try {
    const doc = await api.exportAll()
    const stamp = new Date().toISOString().slice(0, 10)
    download(`ferry-swaps-${stamp}.json`, JSON.stringify(doc, null, 2))
    message.value = `exported ${doc.swaps?.length ?? 0} swap(s)`
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

async function onFile(ev: Event) {
  const input = ev.target as HTMLInputElement
  const file = input.files?.[0]
  // Cleared immediately so choosing the same file twice fires the event again.
  input.value = ''
  if (!file) return

  error.value = ''
  message.value = ''
  busy.value = true
  try {
    const r = await api.importAll(await file.text())
    message.value =
      `imported ${r.added} swap(s)` +
      (r.skipped ? `, skipped ${r.skipped} already in this browser` : '')
    // Refused records are not a detail to bury in the success line: a swap that
    // did not come back is a contract this browser can no longer spend, and the
    // user needs to know before they assume the backup restored cleanly.
    error.value = r.rejected
      ? `${r.rejected} record(s) in that file were refused as malformed and were NOT imported. ` +
        `Keep the file — do not overwrite it — and check it against another copy.`
      : ''
    await reload('active')
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Card class="border-warning/40">
    <CardHeader class="gap-1.5">
      <CardTitle class="flex items-center gap-1.5">
        Backup
        <InfoTip variant="warn" label="What is at stake">
          <p>
            Swaps live in this browser's storage and nothing backs it up. Private browsing ending,
            "clear site data" or a reinstall wipes it with no warning and no undo — and a swap lost
            that way is a contract nobody can spend until its timelock expires.
          </p>
          <p>
            The export holds the ephemeral private keys in the clear. Treat the file as a private
            key: it can spend every contract it names.
          </p>
        </InfoTip>
      </CardTitle>
      <p class="text-sm text-muted-foreground">
        Your swaps exist only in this browser. Export them somewhere safe — the file holds private
        keys, so treat it like one.
      </p>
    </CardHeader>
    <CardContent class="grid gap-3">
      <div class="flex flex-wrap gap-2">
        <Button :disabled="busy" @click="exportAll">
          <DownloadIcon />
          Export all swaps
        </Button>
        <Button variant="outline" :disabled="busy" @click="fileInput?.click()">
          <UploadIcon />
          Import a backup
        </Button>
        <input
          ref="fileInput"
          type="file"
          accept="application/json,.json"
          class="hidden"
          @change="onFile"
        />
      </div>
      <p class="flex flex-wrap items-center gap-x-1.5 text-xs text-muted-foreground">
        Importing only adds what is missing; it never overwrites a swap already here.
        <InfoTip label="Why import never overwrites">
          This browser's copy may have moved on since the backup, and replacing it would discard a
          newer signature.
        </InfoTip>
      </p>
      <p v-if="message" class="text-sm text-success">{{ message }}</p>
      <p v-if="error" class="text-sm text-destructive">{{ error }}</p>
    </CardContent>
  </Card>
</template>

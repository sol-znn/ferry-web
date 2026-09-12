import {ref} from 'vue'
import {api} from '@/core/api'
import {useSettings} from './useSettings'
import type {Config, Swap} from '@/types'

// One shared config and one shared swap list for the whole app.
//
// Every mutation ends the same way — reload the list, then reload the config —
// because an action that changes a swap almost always changes the counts in
// the header too, and letting each view refresh its own copy is how the two
// pages ended up disagreeing about how many swaps were active.
const config = ref<Config | null>(null)
const configError = ref('')
const swaps = ref<Swap[]>([])
const listError = ref('')
const loading = ref(false)
/**
 * Which list is loaded, so anything that reloads on its own reloads the one on
 * screen. Without it a background refresh finishing while the user is reading
 * History would quietly replace it with the active list.
 */
const view = ref<'active' | 'history'>('active')
let timer: number | null = null

const {body: settings} = useSettings()

async function loadConfig() {
  try {
    config.value = await api.config(settings.value)
    configError.value = ''
  } catch (e) {
    configError.value = e instanceof Error ? e.message : String(e)
  }
}

async function loadSwaps(which: 'active' | 'history') {
  view.value = which
  loading.value = true
  try {
    swaps.value = await api.list(which)
    listError.value = ''
  } catch (e) {
    listError.value = e instanceof Error ? e.message : String(e)
    swaps.value = []
  } finally {
    loading.value = false
  }
}

async function reload(which: 'active' | 'history') {
  await loadSwaps(which)
  await loadConfig()
}

// The chain tips move on their own, so the status line is polled. The swap list
// is not: it lives in this browser and only changes when something on this page
// changes it — either by a user pressing something, or by the background
// refresh in useAutoRefresh, and both of those reload it themselves.
function startPolling(intervalMs = 30000) {
  if (timer !== null) return
  timer = window.setInterval(() => void loadConfig(), intervalMs)
}

// Paired with startPolling so the interval does not outlive whatever started
// it. The app mounts once, so this is tidiness rather than a leak today — but
// an interval that nothing can stop is the kind of thing that becomes one.
function stopPolling() {
  if (timer === null) return
  window.clearInterval(timer)
  timer = null
}

export function useFerry() {
  return {
    config,
    configError,
    swaps,
    view,
    listError,
    loading,
    loadConfig,
    loadSwaps,
    reload,
    startPolling,
    stopPolling,
  }
}

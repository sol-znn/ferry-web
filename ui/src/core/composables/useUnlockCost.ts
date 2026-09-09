import {ref, watch, type Ref} from 'vue'
import {api} from '@/core/api'
import type {Settings, SpendCost} from '@/types'

/**
 * A live answer to "can this amount actually be unlocked?".
 *
 * Getting out of a contract is a one-input, one-output transaction whose fee
 * comes out of the contract itself, so the amount agreed is never the amount
 * received — and below the dust limit it is not received at all. That is only
 * discoverable after funding unless something asks the question first, which is
 * what this does, from every surface where an amount is chosen or shown.
 *
 * The arithmetic is all in Go, sized by the same code that signs the spend. All
 * that lives here is the debounce and the plumbing: an amount typed a digit at
 * a time would otherwise be one call per keystroke, and on the create form
 * every one of those is a fee-estimate request to a public Esplora.
 */
export interface UnlockCostInput {
  amountSats: number
  destAddr?: string
  contractHex?: string
  /** Naming a swap prices the contract that was really built, and the money
   *  really in it, rather than the template and a number in a form. */
  id?: string
  /** sat/vB. Supplied, the quote needs no network at all. */
  feeRate?: number
}

export function useUnlockCost(
  input: Ref<UnlockCostInput | null>,
  settings?: Ref<Settings>,
  debounceMs = 400,
) {
  const cost = ref<SpendCost | null>(null)
  const error = ref('')
  const loading = ref(false)

  let timer: ReturnType<typeof setTimeout> | undefined
  // Every request is numbered so a slow answer to an old amount cannot land on
  // top of a fast answer to the current one. At the dust boundary a stale
  // "that works" is exactly the wrong thing to leave on screen.
  let seq = 0

  async function run(req: UnlockCostInput, mine: number) {
    loading.value = true
    try {
      const result = await api.estimate(req, settings?.value)
      if (mine !== seq) return
      cost.value = result
      error.value = ''
    } catch (e) {
      if (mine !== seq) return
      cost.value = null
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      if (mine === seq) loading.value = false
    }
  }

  watch(
    // Watched by value, not by reference: callers build the input object in a
    // computed, so a fresh object arrives on every unrelated re-render.
    () => (input.value ? JSON.stringify(input.value) : ''),
    (key) => {
      clearTimeout(timer)
      const req = input.value
      seq += 1
      if (!key || !req || !(req.amountSats > 0)) {
        cost.value = null
        error.value = ''
        loading.value = false
        return
      }
      const mine = seq
      timer = setTimeout(() => void run(req, mine), debounceMs)
    },
    {immediate: true},
  )

  return {cost, error, loading}
}

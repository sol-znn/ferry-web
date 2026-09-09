import {ref} from 'vue'
import {isDev} from '@/core/env'

/**
 * Whether to show the printed znn-cli commands. Off by default: the extension
 * does this now, and a block of shell most people will never run was the largest
 * thing on a card otherwise about pressing one button.
 *
 * One preference for the whole app rather than one per swap — whether you drive
 * chains from a terminal is a fact about you, not about a trade.
 */

const STORAGE_KEY = isDev ? 'ferry.dev.cli' : 'ferry.cli'

function read(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY) === '1'
  } catch {
    return false
  }
}

const show = ref(read())

function set(on: boolean) {
  show.value = on
  try {
    localStorage.setItem(STORAGE_KEY, on ? '1' : '0')
  } catch {
    // It holds for this page and asks again on the next one, which for a
    // display preference is a fine place to fail.
  }
}

export function useCliCommands() {
  return {show, set}
}

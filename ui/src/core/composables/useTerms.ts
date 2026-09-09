import {isDev} from '@/core/env'

/**
 * A record that the terms were accepted, kept for one swap at a time.
 *
 * There is deliberately no gate here -- no flag this reads to decide whether to
 * ask again. The dialog is shown on every press of Create, because the question
 * is whether you accept what THIS swap is, and that is as true of the fifth as
 * the first. Remembering an answer across swaps was tried and was wrong: it made
 * the warning readable exactly once, on the occasion where a person has least
 * idea what they are agreeing to.
 *
 * What this keeps is smaller and drives no UI: a timestamped log, so that "did
 * this browser actually see this warning" has an answer better than an
 * assumption.
 */

export const TERMS_VERSION = 1

const STORAGE_KEY = isDev ? 'ferry.dev.terms.log' : 'ferry.terms.log'

/** Kept short — this is a receipt, not a database, and nothing here reads it
 *  back to decide anything. */
const LOG_LIMIT = 50

function record(entry: {version: number; at: string}) {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    const list: unknown = raw ? JSON.parse(raw) : []
    const log = Array.isArray(list) ? list : []
    log.push(entry)
    localStorage.setItem(STORAGE_KEY, JSON.stringify(log.slice(-LOG_LIMIT)))
  } catch {
    // A browser that will not keep the receipt still let the dialog do its
    // job: it was shown, and it was answered. Nothing downstream depends on
    // this write succeeding.
  }
}

function accept() {
  record({version: TERMS_VERSION, at: new Date().toISOString()})
}

export function useTerms() {
  return {accept}
}

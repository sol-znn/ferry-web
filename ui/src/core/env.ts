import type {Settings} from '@/types'

/**
 * Which instance this build is, and what it assumes about the world.
 *
 * Two deployments exist, the same code, separated by which chains they expect:
 * development points at a regtest node, a local Esplora shim and a devnet Zenon
 * node; production points at mainnet, where a mistake costs money.
 *
 * Baked in at build time by scripts/build.mjs, which sets it on the Go module in
 * the same step -- see wasm/env.go for why it is a build flag rather than
 * something the page can flip. This half is used for the two things that happen
 * before the module answers: seeding a first-run configuration, and labelling
 * the page.
 *
 * `Config.buildEnv` from the module is the authority for anything shown to the
 * user, because it comes from the code that actually signs. The header compares
 * the two.
 */
declare const __FERRY_ENV__: string

export type FerryEnv = 'dev' | 'prod'

/** Only the exact string selects development, matching Env() in wasm/env.go. */
export const FERRY_ENV: FerryEnv = __FERRY_ENV__ === 'dev' ? 'dev' : 'prod'

export const isDev = FERRY_ENV === 'dev'

/** Shown in the footer. Bump by hand until a release process needs more. */
export const APP_VERSION = 'v0.1.0'

/**
 * What a browser that has never opened this instance starts with.
 *
 * Defaults for an empty configuration and nothing more: written on first run and
 * never applied over a choice already saved, so changing instances cannot
 * silently repoint a browser that has been configured by hand.
 *
 * Production deliberately seeds no Zenon node. There is no endpoint this app
 * could name that a user did not choose, and verifying a counterparty's HTLC is
 * the one check where a dishonest answer costs money -- so it starts with the
 * Zenon leg switched off and says so. Development seeds the devnet on loopback,
 * where the only party who could lie to you is you.
 */
export const INSTANCE_DEFAULTS: Record<FerryEnv, Settings> = {
  dev: {
    network: 'regtest',
    // Blank would resolve to the same URL through DEFAULT_ESPLORA, but writing
    // it makes the settings dialog show what is in use rather than a
    // placeholder, which is the difference between "configured" and "probably
    // fine".
    btcEsplora: 'http://127.0.0.1:3002',
    znnUrl: 'http://127.0.0.1:35997',
  },
  prod: {
    network: 'mainnet',
    btcEsplora: '',
    znnUrl: '',
  },
}

/** The instance's own first-run configuration. */
export const DEFAULT_SETTINGS: Settings = INSTANCE_DEFAULTS[FERRY_ENV]

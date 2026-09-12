import {createApp} from 'vue'
import {useTheme} from 'nom-ui'
import App from './App.vue'
import {router} from './router'
import {startWasm} from './core/wasm'
import {watchForWallets} from './core/solana'
import {isDev} from './core/env'
import './style.css'

// Dark is the default, matching what ferry shipped before and what the design
// system calls its native habitat. The header toggle pins an explicit choice,
// and useTheme's stored preference wins on every later visit.
const {initTheme, setTheme} = useTheme()
if (localStorage.getItem('nom-wallet-theme') === null) {
  setTheme('dark')
} else {
  void initTheme()
}

// Start the download before Vue mounts, and do not await it: the app renders
// its own progress and error states around a module that is still arriving, so
// blocking here would replace a page that explains itself with a blank one.
void startWasm().catch(() => {})

// Start looking for a Solana wallet before anything renders. An extension
// injects itself whenever its own content script runs, which is a race the
// first frame usually loses — so this listens for Phantom's announcement and
// retries briefly, and every button that names a wallet reads the answer.
watchForWallets()

// The development instance carries a driver's seat: stub wallets, a way to set
// the nodes without the dialog, and clicking by label. Behind a build-time
// constant and imported dynamically, so the production bundle does not contain
// it -- see core/testkit.ts. It installs before mount because a provider that
// turns up after the first frame is the awkward case every connector here has
// to guess about.
if (isDev) {
  const {installTestkit} = await import('./core/testkit')
  installTestkit()
}

createApp(App).use(router).mount('#app')

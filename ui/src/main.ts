import {createApp} from 'vue'
import {useTheme} from 'nom-ui'
import App from './App.vue'
import {router} from './router'
import {startWasm} from './core/wasm'
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

createApp(App).use(router).mount('#app')

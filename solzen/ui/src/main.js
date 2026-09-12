import { createApp } from 'vue'
import App from './App.vue'
import './style.css'
import { boot } from './core/engine.js'

// Start fetching the module before Vue mounts. It is the largest thing the page
// loads, and every screen needs it, so the download overlaps with the render
// instead of following it.
boot().catch(() => {
  /* the error surfaces in App.vue, which can show it */
})

createApp(App).mount('#app')

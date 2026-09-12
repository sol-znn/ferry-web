import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  // Relative, so the built site works from a subdirectory as well as a domain
  // root -- which is what a static host like GitHub Pages gives you.
  base: './',
  server: {
    port: 5188,
    strictPort: true,
    // The Solana test validator and the Zenon devnet both answer on loopback
    // over plain http, and a page served over http can call them. Serving this
    // over https during development would break both, so the dev server stays
    // on http on purpose.
    host: '127.0.0.1',
  },
  build: {
    target: 'es2022',
    chunkSizeWarningLimit: 2000,
  },
})

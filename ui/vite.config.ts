import {defineConfig} from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import {resolve} from 'path'

// The build output is a directory of static files and nothing else. There is no
// server component to deploy beside it, which is the whole point of this port:
// GitHub Pages, any CDN, or a folder on a USB stick all serve it identically.
export default defineConfig({
  // Relative, not absolute. A GitHub Pages project site lives under /<repo>/, a
  // user site under /, and a local preview wherever it was pointed. './' makes
  // one build work at all three with no base-path flag to get wrong at deploy
  // time — and it is what lets the offline recovery page work from file-backed
  // copies of the site.
  base: './',
  plugins: [vue(), tailwindcss()],
  resolve: {
    alias: {
      '@': resolve(__dirname, './src'),
    },
  },
  build: {
    // One directory per instance, so both can exist at once and neither can be
    // uploaded as the other. Sharing `dist/` would mean the last build won and
    // left no trace of which one it was — and the two are visually identical
    // once served, so the mistake would be found by a user on mainnet rather
    // than by whoever made it.
    outDir: resolve(__dirname, process.env.FERRY_ENV === 'dev' ? '../dist-dev' : '../dist'),
    emptyOutDir: true,
    assetsDir: 'static',
    assetsInlineLimit: 0,
    // The WebAssembly module is a few megabytes and every JS chunk beside it is
    // small, so the default warning fires on a file the warning is not about.
    chunkSizeWarningLimit: 1024,
  },
  optimizeDeps: {
    // nom-ui ships Vue/TS source rather than a build, so it is compiled by this
    // app's pipeline instead of being pre-bundled.
    exclude: ['nom-ui'],
  },
  assetsInclude: ['**/*.wasm'],
  define: {
    // The content hash of public/ferry.wasm, set by scripts/build-wasm. It is
    // appended to the module and shim URLs so a deploy replaces a cached copy;
    // 'dev' is what a hand-run `vite dev` sees, where caching is off anyway.
    __WASM_VERSION__: JSON.stringify(process.env.FERRY_WASM_VERSION ?? 'dev'),
    // Which instance this is, set by scripts/build.mjs in the same step that
    // sets it on the Go module, so the two halves of one artefact agree by
    // construction. A bare `vite dev` or `vite build` run without the script
    // gets the development instance: that path is somebody working on the app
    // locally, and defaulting it to production would point their browser at
    // mainnet and a public Esplora on first load. The deployed production build
    // always comes through scripts/build.mjs, which sets this explicitly.
    __FERRY_ENV__: JSON.stringify(process.env.FERRY_ENV ?? 'dev'),
  },
  server: {
    // Plain http on purpose. A page served over https cannot call an http://
    // node — the browser blocks mixed content before the request leaves — so a
    // local Esplora on 127.0.0.1:3002 or a Zenon node on 127.0.0.1:35997 is
    // only reachable from a page that is itself on http.
    host: '127.0.0.1',
    port: 5173,
  },
})

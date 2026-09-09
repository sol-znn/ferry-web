import {createRouter, createWebHashHistory} from 'vue-router'

// Hash routes rather than real paths.
//
// A static host cannot answer /history with the app: GitHub Pages looks for a
// file there, does not find one, and serves its 404. Copying index.html to
// 404.html makes the link work but answers it with an HTTP 404, which is a lie
// that breaks caches and crawlers.
//
// So the route lives in the fragment. #/history needs no host cooperation and
// works identically from a project page, a user page, a local preview or a copy
// of the site on a USB stick -- the same portability offline recovery depends on.
export const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    {path: '/', name: 'active', component: () => import('@/pages/ActivePage.vue')},
    // The board needs the engine like every other working page: a post is signed
    // by a key the module holds, and a stranger's post is verified by it before
    // anything is shown. A board without the engine would be a list of
    // unverified claims, which is precisely what it is not.
    {path: '/board', name: 'board', component: () => import('@/pages/BoardPage.vue')},
    {path: '/history', name: 'history', component: () => import('@/pages/HistoryPage.vue')},
    {path: '/recover', name: 'recover', component: () => import('@/pages/RecoverPage.vue')},
    // The only route that does not need the WebAssembly module, and it says so
    // here rather than in App.vue so the fact travels with the route. A reader
    // whose engine will not load is exactly the reader who needs "how do I get
    // my money out" to still render — see EngineGate in App.vue.
    {
      path: '/docs',
      name: 'docs',
      component: () => import('@/pages/DocsPage.vue'),
      meta: {engine: false},
    },
    {
      path: '/docs/under-the-hood',
      name: 'under-the-hood',
      component: () => import('@/pages/UnderTheHoodPage.vue'),
      meta: {engine: false},
    },
    // Outside the engine gate for the same reason the docs are: the terms say
    // what this software does and does not promise, and a reader whose
    // WebAssembly module will not load is not a reader who forfeits that.
    {
      path: '/terms',
      name: 'terms',
      component: () => import('@/pages/TermsPage.vue'),
      meta: {engine: false},
    },
    {path: '/:pathMatch(.*)*', redirect: '/'},
  ],

  // A docs link carries a fragment inside the route fragment — #/docs#keys —
  // and vue-router hands the inner one over as to.hash. Nothing scrolls to it
  // on its own: the browser spent the fragment picking the route.
  scrollBehavior(to) {
    if (to.hash) {
      // querySelector throws on a fragment that is not a valid selector, and a
      // fragment is whatever somebody typed into the address bar.
      let el: Element | null = null
      try {
        el = document.querySelector(to.hash)
      } catch {
        el = null
      }
      // Smooth unless the reader has asked for less motion, in which case
      // sliding a page they cannot see the top of is precisely the effect they
      // turned off.
      if (el) {
        const still = window.matchMedia('(prefers-reduced-motion: reduce)').matches
        return {el, top: 12, behavior: still ? 'auto' : 'smooth'}
      }
    }
    return {top: 0}
  },
})

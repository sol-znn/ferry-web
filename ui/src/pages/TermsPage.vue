<script setup lang="ts">
import {ScaleIcon, TriangleAlertIcon} from '@lucide/vue'
import {Heading} from 'nom-ui'

// The terms, in the app rather than only on the site that hosts it.
//
// Ferry is a static page: there is no operator in the loop, no account, no
// custody and nobody to appeal to. That is the good news and it is also the
// whole of the risk, so it is written down in one place a person can be pointed
// at — from the footer of every page, and from the warning beside the button
// that creates a swap.
//
// It is deliberately short and specific to this software. A long document
// nobody reads protects nobody; the four things that actually decide whether a
// person should use this — it is a proof of concept, the keys are in this
// browser only, mistakes are final, and the law where you live is yours to
// know — are each a heading of their own.
//
/** The date the terms below last changed, so a reader can tell. */
const UPDATED = '6 September 2026'
</script>

<template>
  <article class="grid max-w-3xl gap-8">
    <header class="grid gap-3">
      <Heading :level="1" class="flex items-center gap-2.5 text-2xl">
        <ScaleIcon class="size-5 text-muted-foreground" aria-hidden="true" />
        Terms and conditions
      </Heading>
      <p class="text-sm leading-relaxed text-muted-foreground">
        Last updated {{ UPDATED }}. Using Ferry means accepting what is on this page.
      </p>

      <div class="flex items-start gap-2.5 rounded-lg border border-warning/40 bg-warning/5 p-3">
        <TriangleAlertIcon class="mt-0.5 size-4 shrink-0 text-warning" aria-hidden="true" />
        <div class="min-w-0 text-sm">
          <p class="font-semibold text-warning">This is experimental software.</p>
          <p class="mt-1 text-muted-foreground">
            It is a proof of concept. Nothing here has been audited, and a swap moves real coins
            irreversibly. Do not put in money you are not prepared to lose entirely.
          </p>
        </div>
      </div>
    </header>

    <section class="grid gap-3">
      <Heading :level="2" class="text-lg">No custody, no operator, no service</Heading>
      <p class="text-sm leading-relaxed">
        Ferry is a static web page. It has no server, no account and no operator in the loop. Every
        key is generated in your browser and stays there; nobody — including whoever published this
        page — can move your coins, freeze a swap, reverse a transaction or recover anything on your
        behalf. There is no support desk, and there is no counterparty to this software: your
        counterparty is the person you are swapping with.
      </p>
      <p class="text-sm leading-relaxed">
        Nothing on this site is an offer, a solicitation, a brokerage, an exchange service or
        financial advice. The software matches nobody with anybody; it builds and checks contracts
        for a trade you have already agreed elsewhere.
      </p>
    </section>

    <section class="grid gap-3">
      <Heading :level="2" class="text-lg">Your keys live in this browser</Heading>
      <p class="text-sm leading-relaxed">
        Each swap's key is stored unencrypted in this browser's storage for this site. Clearing site
        data, closing a private window, a browser reinstall or a lost device destroys it with no
        warning and with no way back. Downloading the recovery file for every funded swap, and
        keeping it somewhere you control, is your responsibility and nobody else's.
      </p>
    </section>

    <section class="grid gap-3">
      <Heading :level="2" class="text-lg">Mistakes are final</Heading>
      <p class="text-sm leading-relaxed">
        A swap is two timelocked contracts on two chains. If you fund the wrong address, agree the
        wrong amount, act on an HTLC you did not verify, miss a deadline, or lose access to this
        browser before a swap settles, the coins may be lost or claimable only by your counterparty.
        Verify every value the app shows you against your own nodes before acting on it. Fee
        estimates, expiry countdowns and chain data shown here can be stale, incomplete or wrong.
      </p>
    </section>

    <section class="grid gap-3">
      <Heading :level="2" class="text-lg">No warranty, no liability</Heading>
      <p class="text-sm leading-relaxed">
        The software is provided “as is”, without warranty of any kind, express or implied,
        including but not limited to merchantability, fitness for a particular purpose and
        non-infringement. To the fullest extent permitted by law, the authors and publishers accept
        no liability for any loss or damage — including total loss of funds — arising from use of
        this software, from reliance on anything it displays, or from the conduct of any
        counterparty, node, relay or wallet it interacts with.
      </p>
    </section>

    <section class="grid gap-3">
      <Heading :level="2" class="text-lg">Your jurisdiction is your responsibility</Heading>
      <p class="text-sm leading-relaxed">
        You are responsible for knowing and obeying the laws that apply where you are, including any
        tax, reporting, licensing or sanctions obligations arising from a swap. Do not use this
        software where doing so would be unlawful.
      </p>
    </section>

    <section class="grid gap-3">
      <Heading :level="2" class="text-lg">Privacy</Heading>
      <p class="text-sm leading-relaxed">
        This page collects nothing. There are no cookies, no analytics and no tracking, and no swap
        data is sent anywhere by the app itself. What does leave your browser is what a swap
        requires: requests to the Bitcoin and Zenon nodes you configure, and — only while you have a
        live session open — messages encrypted to your session code, published through the Nostr
        relays you configure. Those third parties see the traffic you send them; choose them
        accordingly.
      </p>
    </section>

    <section class="grid gap-3">
      <Heading :level="2" class="text-lg">Changes</Heading>
      <p class="text-sm leading-relaxed">
        These terms can change with any deployment of the site, and the date at the top is how you
        tell. Continuing to use the app after a change means accepting the version then published.
      </p>
    </section>
  </article>
</template>

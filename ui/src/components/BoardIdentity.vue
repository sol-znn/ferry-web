<script setup lang="ts">
import {computed, onMounted} from 'vue'
import {BadgeCheckIcon, BitcoinIcon, KeyRoundIcon, WalletIcon, XIcon} from '@lucide/vue'
import {Badge, Button, CopyButton, ZnnLogo} from 'nom-ui'
import InfoTip from './InfoTip.vue'
import Note from './Note.vue'
import PhantomMark from './PhantomMark.vue'
import {useBoardIdentity} from '@/core/composables/useBoardIdentity'
import {useChainProofs} from '@/core/composables/useChainProofs'
import {useSolana} from '@/core/composables/useSolana'
import {useUnisat} from '@/core/composables/useUnisat'
import {useZenonWallet} from '@/core/composables/useZenonWallet'
import {CHAIN_IDS, chainList} from '@/core/chains'
import {shortKey} from '@/core/board'
import {shortSol} from '@/core/solana'
import {shortAddr} from '@/core/unisat'

// Who you are on the board, and what you can do with it.
//
// Two things sit in one strip, and the strip's job is to keep them apart.
//
// The KEY is not optional and not a decision: it exists so a post can be signed,
// which is what makes a post yours to edit and nobody else's to forge.
//
// A PROOF is one per chain, and it is what lets you act on that chain. Posting
// an offer or taking one requires a proven address on every chain the offer
// settles on — both for Bitcoin against Zenon, Zenon alone for two Zenon tokens
// traded against each other. See chains.ts: the requirement is read off the
// offer, so this panel never has to know which pairs exist.
//
// One button per chain, named for the chain and not for the wallet behind it.
// "Prove Bitcoin address" rather than "Connect UniSat & prove address", and it
// says that whether or not a wallet is connected: connecting is a step inside
// what the button does, not a different button and not a different intention. A
// board that will trade three chains next year cannot have its controls named
// after this year's two extensions.
//
// The box stays small on purpose: the header, the Prove buttons, and nothing
// else standing between a returning visitor and the thing they came to press.
// Everything it used to say permanently — what a proof is for, why one chain
// might be enough, why an address might need re-proving — is one InfoTip or one
// "More info" away rather than a paragraph nobody rereads.

const {
  pubKey,
  btcBound,
  znnBound,
  solBound,
  busy,
  error,
  load,
  bindBitcoin,
  bindZenon,
  bindSolana,
  unbind,
} = useBoardIdentity()

const unisat = useUnisat()
const zenon = useZenonWallet()
const solana = useSolana()

onMounted(load)

/** Whether the bound address is still the wallet's selected one.
 *
 *  Worth saying because it is silent otherwise: somebody who switches account in
 *  UniSat keeps a badge for an address they are no longer using, and their next
 *  post would carry a proof for the old one. */
const btcStale = computed(() =>
  Boolean(btcBound.value && unisat.address.value && btcBound.value !== unisat.address.value),
)
const znnStale = computed(() =>
  Boolean(znnBound.value && zenon.address.value && znnBound.value !== zenon.address.value),
)
const solStale = computed(() =>
  Boolean(solBound.value && solana.address.value && solBound.value !== solana.address.value),
)

/** What is proven and what is not, for the line under the buttons. From the
 *  chain list rather than from two booleans, so a third chain appears in that
 *  sentence without this file being edited. */
const {proven} = useChainProofs()
const unproven = computed(() => CHAIN_IDS.filter((id) => !proven.value.includes(id)))
</script>

<template>
  <section class="grid gap-3 rounded-lg border border-border p-4">
    <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
      <h2 class="flex items-center gap-1.5 text-sm font-semibold">
        <KeyRoundIcon class="size-4 text-muted-foreground" />
        Your board key
        <InfoTip label="What the board key is and is not">
          <p>
            A signing key this browser made for you. Every post you make is signed with it, which is
            what stops anyone else editing or withdrawing your offers — they would have to forge a
            signature.
          </p>
          <p>
            It is not an account and holds no money. It cannot spend, cannot sign a swap, and cannot
            claim an address you have not separately proven. Losing it costs you the ability to edit
            posts already out there, and those expire within a day.
          </p>
        </InfoTip>
      </h2>

      <code v-if="pubKey" class="font-mono text-xs text-muted-foreground">
        {{ shortKey(pubKey) }}
      </code>
      <CopyButton v-if="pubKey" :value="pubKey" size="icon-sm" />

      <span class="flex-1" />

      <!-- Green where an address is proven, plain where a wallet is merely
           connected, absent where there is no wallet.

           Only the green one lets you act. The plain one is not decoration
           either: it is the address the Prove button beside it would claim, so
           somebody looking at the wrong account in their wallet sees that here
           rather than after a signature. -->
      <Badge v-if="btcBound && !btcStale" variant="success" class="gap-1">
        <BadgeCheckIcon class="size-3" />
        {{ shortAddr(btcBound) }}
      </Badge>
      <Badge
        v-else-if="unisat.connected.value && unisat.address.value"
        variant="outline"
        class="gap-1"
      >
        <WalletIcon class="size-3" />
        {{ shortAddr(unisat.address.value) }}
      </Badge>

      <Badge v-if="znnBound && !znnStale" variant="success" class="gap-1">
        <BadgeCheckIcon class="size-3" />
        {{ shortAddr(znnBound) }}
      </Badge>
      <Badge
        v-else-if="zenon.connected.value && zenon.address.value"
        variant="outline"
        class="gap-1"
      >
        <WalletIcon class="size-3" />
        {{ shortAddr(zenon.address.value) }}
      </Badge>

      <Badge v-if="solBound && !solStale" variant="success" class="gap-1">
        <BadgeCheckIcon class="size-3" />
        {{ shortSol(solBound) }}
      </Badge>
      <Badge
        v-else-if="solana.connected.value && solana.address.value"
        variant="outline"
        class="gap-1"
      >
        <WalletIcon class="size-3" />
        {{ shortSol(solana.address.value) }}
      </Badge>
    </div>

    <!-- One Prove button per chain, unconditionally, named for the chain.
         Everything that used to sit as permanent prose below them — what
         proving does, why a button might not work, what a stale proof looks
         like — is behind "More info" instead.

         Neither is disabled for a wallet that is not connected, and that is the
         fix for a dead end rather than a relaxation: this page has no wallet
         strip of its own, so a button greyed out until the user finds the swaps
         page was a control with no way to earn it. The buttons connect first.
         Detection is not a gate either, for the reason WalletConnect gives at
         length — an extension injects whenever its own script runs, and the
         authoritative test for whether a wallet is there has always been asking
         it. What cannot work comes back as a sentence naming the reason.

         Which is also why the label never mentions the wallet. The button is a
         promise about a chain — prove you hold an address on it — and which
         extension is asked to sign is an implementation detail that changes
         with the wallet, the build, and the chain being added next. -->
    <div class="flex flex-wrap items-center gap-2">
      <!-- Bitcoin. One signature, once, over a sentence naming this key and the
           address — it authorises no payment and commits to no transaction. -->
      <Button
        v-if="!btcBound || btcStale"
        variant="outline"
        size="sm"
        :disabled="busy"
        @click="bindBitcoin"
      >
        <BitcoinIcon />
        {{ btcStale ? 'Re-prove Bitcoin address' : 'Prove Bitcoin address' }}
      </Button>
      <Button v-else variant="ghost" size="sm" :disabled="busy" @click="unbind('btc-ecdsa')">
        <XIcon />
        Drop Bitcoin proof
      </Button>

      <Button
        v-if="!znnBound || znnStale"
        variant="outline"
        size="sm"
        :disabled="busy"
        @click="bindZenon"
      >
        <ZnnLogo />
        {{ znnStale ? 'Re-prove Zenon address' : 'Prove Zenon address' }}
      </Button>
      <Button v-else variant="ghost" size="sm" :disabled="busy" @click="unbind('znn-ed25519')">
        <XIcon />
        Drop Zenon proof
      </Button>

      <!-- Solana. The same promise about a chain, and the shortest of the three
           checks behind it: a Solana address IS its public key, so a signature
           that verifies against the address in the proof was made by whoever
           holds it — there is no key to carry alongside and no derivation to
           check. Phantom, Solflare and Backpack all sign a message.

           The mark is Phantom's ghost, which is the one place on this strip
           where a wallet is named rather than a chain. The label is still about
           the chain, and the button still works with any of the three; the ghost
           is there because Solana has no glyph a person recognises at 16px the
           way they do a bitcoin B, and a generic coin icon said nothing at
           all. -->
      <Button
        v-if="!solBound || solStale"
        variant="outline"
        size="sm"
        :disabled="busy"
        @click="bindSolana"
      >
        <PhantomMark />
        {{ solStale ? 'Re-prove Solana address' : 'Prove Solana address' }}
      </Button>
      <Button v-else variant="ghost" size="sm" :disabled="busy" @click="unbind('sol-ed25519')">
        <XIcon />
        Drop Solana proof
      </Button>
    </div>

    <!-- What the proofs held so far are worth, in one line. Without it the
         panel showed two badges and left the reader to work out for themselves
         which offers those badges opened — which is the question they came with
         when a Take button is disabled two screens down. -->
    <p class="text-xs text-muted-foreground">
      <template v-if="proven.length === CHAIN_IDS.length">
        Proven on {{ chainList(proven) }} — you can post and take any offer on the board.
      </template>
      <template v-else-if="proven.length">
        Proven on {{ chainList(proven) }}. Offers that also settle on {{ chainList(unproven) }} need
        that proof too.
      </template>
      <template v-else>
        Nothing proven yet. Reading the board needs no proof; posting an offer or taking one needs a
        proven address on every chain that offer settles on.
      </template>
    </p>

    <!-- A live failure, not boilerplate — this stays on screen rather than
         behind a click, because it is the answer to "why did that just fail". -->
    <p v-if="error" class="text-sm whitespace-pre-line text-destructive">{{ error }}</p>

    <Note summary="What the key and the proofs are each for">
      <p>
        <strong>The key</strong> is made by this browser and signs your offers. It is what stops
        anyone else editing or withdrawing them. It holds no money and is not an account.
      </p>

      <p>
        <strong>A proof</strong> is what lets you act on a chain. Each button connects the wallet if
        it is not already, then asks it to sign a short statement tying that address to your key.
        The signature authorises no payment and moves nothing.
      </p>

      <p>
        You need one for every chain an offer settles on, whether you are posting it or taking it:
        both for a Bitcoin-against-Zenon trade, Zenon alone for a trade between two Zenon tokens,
        and no Zenon wallet at all for Solana against Bitcoin. Reading the board needs no proof.
      </p>

      <p>
        It is required rather than decorative because the address is the whole point of the
        introduction: a swap settles to four addresses and two of them are yours, and an unproven
        address is a claim by whatever page filled the field in. Proving it means the address on
        your offer carries a signature a counterparty can check, and that a trade agreed here
        finishes here with nothing to paste into a chat window.
      </p>

      <p v-if="btcStale">
        Your wallet has moved to {{ shortAddr(unisat.address.value) }} but your offers carry
        {{ shortAddr(btcBound) }}. Re-prove it, or drop the proof — a badge for an address you are
        not using is worse than none.
      </p>
      <p v-if="znnStale">
        Syrius has moved to {{ shortAddr(zenon.address.value) }} but your offers carry
        {{ shortAddr(znnBound) }}.
      </p>
      <p v-if="solStale">
        Your Solana wallet has moved to {{ shortSol(solana.address.value) }} but your offers carry
        {{ shortSol(solBound) }}.
      </p>
    </Note>
  </section>
</template>

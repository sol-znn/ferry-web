<template>
  <!-- The ghost, drawn rather than fetched: every asset this app uses is in the
       bundle, because a page that reaches out to a wallet's CDN for a logo tells
       that CDN who is looking at a swap page and when.

       One path, filled with currentColor, with the eyes as sub-paths and
       `evenodd` punching them through — so it inherits the button's colour and
       is legible in both themes without a second copy for dark. The hem is four
       arcs of equal span, which is what makes it read as a ghost rather than as
       a rounded rectangle at 16px. -->
  <svg viewBox="0 0 24 24" fill="none" aria-hidden="true">
    <path
      fill="currentColor"
      fill-rule="evenodd"
      d="M3.5 19.5V11a8.5 8.5 0 0 1 17 0v8.5a2.125 2.125 0 0 1-4.25 0 2.125 2.125 0 0 1-4.25 0 2.125 2.125 0 0 1-4.25 0 2.125 2.125 0 0 1-4.25 0Z
         M9.1 9.05a1.3 1.85 0 1 0 0 3.7 1.3 1.85 0 1 0 0-3.7Z
         M14.9 9.05a1.3 1.85 0 1 0 0 3.7 1.3 1.85 0 1 0 0-3.7Z"
    />
  </svg>
</template>

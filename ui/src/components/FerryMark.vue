<template>
  <!-- Two arrows crossing: the swap, not a brand. The plasma gradient fills it
       because this is the one mark on the page, and the design system reserves
       plasma for the thing that matters most on a surface. The gradient id is
       namespaced so a second copy of the mark cannot collide with it. -->
  <svg viewBox="0 0 24 24" fill="none" aria-hidden="true">
    <defs>
      <linearGradient id="ferry-plasma" x1="0" y1="0" x2="0" y2="1">
        <stop offset="0%" stop-color="hsl(120 86% 63%)" />
        <stop offset="100%" stop-color="hsl(145 100% 38%)" />
      </linearGradient>
    </defs>
    <path
      d="M3 8h13l-3.5-3.5L14 3l6 6-6 6-1.5-1.5L16 10H3zM21 16H8l3.5 3.5L10 21l-6-6 6-6 1.5 1.5L8 14h13z"
      fill="url(#ferry-plasma)"
    />
  </svg>
</template>

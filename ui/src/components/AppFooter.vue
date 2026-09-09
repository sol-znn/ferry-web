<script setup lang="ts">
import {APP_VERSION} from '@/core/env'
</script>

<template>
  <footer class="border-t border-border">
    <!-- The terms sit in the footer because that is where a person looks for
         them, and because they have to be reachable from every page rather than
         only from the one that happens to warn about them. -->
    <div
      class="mx-auto flex max-w-5xl flex-wrap items-center justify-center gap-x-4 gap-y-1 px-4 py-4 text-center text-xs text-muted-foreground sm:px-6"
    >
      <span>Ferry {{ APP_VERSION }}</span>
      <RouterLink to="/terms" class="text-primary underline-offset-4 hover:underline">
        Terms and conditions
      </RouterLink>
      <RouterLink to="/docs" class="text-primary underline-offset-4 hover:underline">
        How it works
      </RouterLink>
    </div>
  </footer>
</template>

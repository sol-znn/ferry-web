<script setup lang="ts">
import {TriangleAlertIcon} from '@lucide/vue'
import {Alert, AlertDescription, AlertTitle} from 'nom-ui'

defineProps<{title?: string; message: string}>()
</script>

<template>
  <Alert variant="destructive">
    <TriangleAlertIcon />
    <AlertTitle>{{ title ?? 'Something went wrong' }}</AlertTitle>
    <AlertDescription class="whitespace-pre-line">{{ message }}</AlertDescription>
  </Alert>
</template>

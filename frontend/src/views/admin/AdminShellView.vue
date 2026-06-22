<template>
  <component :is="shellless ? 'div' : AppLayout">
    <RouterView />
  </component>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { RouterView, useRoute } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'

const route = useRoute()

/** Ops fullscreen mode renders edge-to-edge without sidebar/header. */
const shellless = computed(
  () => route.name === 'AdminOps' && String(route.query.fullscreen ?? '') === '1'
)
</script>

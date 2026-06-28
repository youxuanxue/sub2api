<template>
  <component :is="shellless ? 'div' : AppLayout">
    <RouterView />
  </component>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, watch } from 'vue'
import { RouterView, useRoute } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import { TK_ADMIN_UI_ZOOM } from '@/constants/layout'

const route = useRoute()

/** Ops fullscreen mode renders edge-to-edge without sidebar/header. */
const shellless = computed(
  () => route.name === 'AdminOps' && String(route.query.fullscreen ?? '') === '1'
)

function syncAdminUiZoom() {
  document.documentElement.style.zoom = shellless.value ? '' : String(TK_ADMIN_UI_ZOOM)
}

watch(shellless, syncAdminUiZoom)
onMounted(syncAdminUiZoom)
onUnmounted(() => {
  document.documentElement.style.zoom = ''
})
</script>

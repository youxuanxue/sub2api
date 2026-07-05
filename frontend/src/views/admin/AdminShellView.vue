<template>
  <component :is="shellless ? 'div' : AppLayout">
    <RouterView v-slot="{ Component }">
      <KeepAlive :include="cachedAdminViews">
        <component :is="Component" />
      </KeepAlive>
    </RouterView>
  </component>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, watch } from 'vue'
import { RouterView, useRoute } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import { TK_ADMIN_UI_ZOOM } from '@/constants/layout'
import '@/styles/admin-ui-zoom.tk.css'

/**
 * Admin views to keep alive across navigations within the admin shell.
 * Names must match the defineOptions({ name }) in each view component.
 * Settings / Ops pages are excluded — they should always show fresh state.
 */
const cachedAdminViews = [
  'AdminAccountsView',
  'AdminChannelsView',
  'AdminUsersView',
]

const route = useRoute()
const TK_ADMIN_UI_ZOOM_CLASS = 'tk-admin-ui-zoom'

/** Ops fullscreen mode renders edge-to-edge without sidebar/header. */
const shellless = computed(
  () => route.name === 'AdminOps' && String(route.query.fullscreen ?? '') === '1'
)

function clearAdminUiZoom() {
  document.documentElement.style.zoom = ''
  document.documentElement.classList.remove(TK_ADMIN_UI_ZOOM_CLASS)
  document.documentElement.style.removeProperty('--tk-admin-ui-zoom')
}

function syncAdminUiZoom() {
  if (shellless.value) {
    clearAdminUiZoom()
    return
  }

  const root = document.documentElement
  document.documentElement.style.zoom = String(TK_ADMIN_UI_ZOOM)
  root.classList.add(TK_ADMIN_UI_ZOOM_CLASS)
  root.style.setProperty('--tk-admin-ui-zoom', String(TK_ADMIN_UI_ZOOM))
}

watch(shellless, syncAdminUiZoom)
onMounted(syncAdminUiZoom)
onUnmounted(clearAdminUiZoom)
</script>

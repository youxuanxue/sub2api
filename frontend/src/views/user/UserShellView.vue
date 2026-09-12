<template>
  <AppLayout v-if="useChrome">
    <RouterView v-slot="{ Component }">
      <KeepAlive :include="cachedUserViews">
        <component :is="Component" />
      </KeepAlive>
    </RouterView>
  </AppLayout>
  <RouterView v-else v-slot="{ Component }">
    <KeepAlive :include="cachedUserViews">
      <component :is="Component" />
    </KeepAlive>
  </RouterView>
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent } from 'vue'
import { RouterView } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

// The public model catalog shares this shell but does not need console chrome.
const AppLayout = defineAsyncComponent(() => import('@/components/layout/AppLayout.vue'))

/**
 * User console views to keep alive across navigations within the user shell.
 * Names must match defineOptions({ name }) in each view component.
 * KeepAlive applies to page content only — AppLayout / AppSidebar stay mounted.
 */
const cachedUserViews = [
  'UserDashboardView',
  'UserUsageView',
  'UserKeysView',
  'UserProfileView',
  'UserStudioView',
  'KeyUsageView',
]

const authStore = useAuthStore()
/** Guest-facing shell children (e.g. /models) render without sidebar chrome. */
const useChrome = computed(() => authStore.isAuthenticated)
</script>

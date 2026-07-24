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
import { computed } from 'vue'
import { RouterView } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import { useAuthStore } from '@/stores/auth'

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

<template>
  <AppLayout v-if="isAuthenticated">
    <div :class="contentClass" :data-tk="authedDataTk">
      <slot />
    </div>
  </AppLayout>
  <div
    v-else
    class="relative flex min-h-screen flex-col bg-gradient-to-br from-gray-50 via-primary-50/30 to-gray-100 dark:from-dark-950 dark:via-dark-900 dark:to-dark-950"
    :class="guestRootClass"
  >
    <main class="relative z-10 flex-1 px-4 pb-16 pt-6 sm:px-6" :class="guestMainClass">
      <div :class="contentClass">
        <slot name="guest-chrome" />
        <slot />
      </div>
    </main>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useAuthStore } from '@/stores/auth'
import AppLayout from '@/components/layout/AppLayout.vue'

withDefaults(
  defineProps<{
    contentClass?: string
    authedDataTk?: string
    guestRootClass?: string
    guestMainClass?: string
  }>(),
  {
    contentClass: 'mx-auto max-w-6xl space-y-6',
    authedDataTk: '',
    guestRootClass: '',
    guestMainClass: '',
  },
)

const authStore = useAuthStore()
const isAuthenticated = computed(() => authStore.isAuthenticated)
</script>

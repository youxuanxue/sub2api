<template>
  <!-- Custom Home Content: Full Page Mode (upstream admin override) -->
  <div v-if="hasHomeContent" class="min-h-screen">
    <nav class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-200 bg-white px-4 py-3 dark:border-dark-700 dark:bg-dark-900">
      <router-link to="/quickstart" class="font-medium text-primary-600">{{ t('onboarding.guide') }}</router-link>
      <RegistrationActionTk />
    </nav>
    <!-- iframe mode -->
    <iframe
      v-if="isHomeContentUrl"
      :src="homeContent.trim()"
      class="h-screen w-full border-0"
      allowfullscreen
    ></iframe>
    <!-- HTML mode - SECURITY: homeContent is admin-only setting, XSS risk is acceptable -->
    <div v-else v-html="homeContent"></div>
  </div>

  <HomeTkCompactLanding v-else-if="compactHomeEnabled" />

  <!-- Default Home Page: TokenKey landing (TK-only, isolated from upstream — CLAUDE.md §5) -->
  <HomeTkLanding v-else :profile="homepageProfile" />
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import RegistrationActionTk from '@/components/auth/RegistrationActionTk.vue'
import { computed, onMounted, watch } from 'vue'
import { useAppStore } from '@/stores'
import HomeTkCompactLanding from '@/components/home/HomeTkCompactLanding.tk.vue'
import HomeTkLanding from '@/components/home/HomeTkLanding.tk.vue'
import { applyHomepageSeo } from '@/features/home/homepageSeo.tk'
import { resolveHomepageProfile } from '@/features/home/marketProfile.tk'

const appStore = useAppStore()
const { t } = useI18n()
const homepageProfile = resolveHomepageProfile(window.location.hostname)

// Admin-configurable custom home content (upstream feature). When empty, the
// default TokenKey landing renders instead.
const homeContent = computed(() => appStore.cachedPublicSettings?.home_content || '')
const hasHomeContent = computed(
  () => homepageProfile === 'current' && homeContent.value.trim().length > 0,
)
const compactHomeEnabled = computed(
  () =>
    homepageProfile === 'current' &&
    appStore.cachedPublicSettings?.compact_home_enabled === true,
)

// Check if homeContent is a URL (for iframe display)
const isHomeContentUrl = computed(() => {
  const content = homeContent.value.trim()
  return content.startsWith('http://') || content.startsWith('https://')
})

onMounted(() => {
  // Ensure public settings are loaded (will use cache if already loaded from injected config)
  if (!appStore.publicSettingsLoaded) {
    appStore.fetchPublicSettings()
  }
})

watch(
  () => appStore.siteName,
  () => applyHomepageSeo(homepageProfile),
  { immediate: true, flush: 'post' },
)
</script>

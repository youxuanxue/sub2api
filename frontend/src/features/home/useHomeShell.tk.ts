import { computed, onMounted, ref } from 'vue'
import { useAppStore, useAuthStore } from '@/stores'
import { resolveSiteLogo } from '@/utils/branding'
import { FeatureFlags, isFeatureFlagEnabled } from '@/utils/featureFlags'
import { sanitizeUrl } from '@/utils/url'

export function useHomeShell() {
  const appStore = useAppStore()
  const authStore = useAuthStore()

  const siteName = computed(
    () => appStore.cachedPublicSettings?.site_name || appStore.siteName || 'TokenKey',
  )
  const siteLogo = computed(() =>
    resolveSiteLogo(appStore.cachedPublicSettings?.site_logo || appStore.siteLogo),
  )
  const siteSubtitle = computed(
    () => appStore.cachedPublicSettings?.site_subtitle || 'AI API Gateway Platform',
  )
  const docUrl = computed(() =>
    sanitizeUrl(appStore.cachedPublicSettings?.doc_url || appStore.docUrl || ''),
  )
  const isAuthenticated = computed(() => authStore.isAuthenticated)
  const isAdmin = computed(() => authStore.isAdmin)
  const dashboardPath = computed(() => (isAdmin.value ? '/admin/dashboard' : '/dashboard'))
  const userInitial = computed(() => authStore.user?.email?.charAt(0).toUpperCase() || '')
  const modelPlazaRequiresAuth = computed(
    () => appStore.cachedPublicSettings?.model_plaza_require_auth === true,
  )
  const showModelPlazaEntry = computed(
    () =>
      isFeatureFlagEnabled(FeatureFlags.modelPlaza) &&
      (isAuthenticated.value || !modelPlazaRequiresAuth.value),
  )
  const currentYear = computed(() => new Date().getFullYear())
  const isDark = ref(document.documentElement.classList.contains('dark'))

  function toggleTheme() {
    isDark.value = !isDark.value
    document.documentElement.classList.toggle('dark', isDark.value)
    localStorage.setItem('theme', isDark.value ? 'dark' : 'light')
  }

  function initTheme() {
    const savedTheme = localStorage.getItem('theme')
    if (
      savedTheme === 'dark' ||
      (!savedTheme && window.matchMedia('(prefers-color-scheme: dark)').matches)
    ) {
      isDark.value = true
      document.documentElement.classList.add('dark')
    }
  }

  onMounted(() => {
    initTheme()
    authStore.checkAuth()
  })

  return {
    currentYear,
    dashboardPath,
    docUrl,
    isAuthenticated,
    isDark,
    showModelPlazaEntry,
    siteLogo,
    siteName,
    siteSubtitle,
    toggleTheme,
    userInitial,
  }
}

<template>
  <CatalogHubShell
    authed-data-tk="catalog-hub-authed"
    content-class="mx-auto flex min-h-0 w-full max-w-[90rem] flex-1 flex-col"
    :guest-root-class="guestShellRootClass"
    :guest-main-class="guestShellMainClass"
  >
    <template #guest-chrome>
      <div class="mb-6 shrink-0 space-y-4 text-center" data-tk="catalog-hub-header">
        <div>
          <h1 class="mb-2 text-3xl font-bold text-gray-900 dark:text-white sm:text-4xl">
            {{ t('models.title') }}
          </h1>
          <p class="text-base text-gray-600 dark:text-dark-400">
            {{ t('models.subtitle') }}
          </p>
        </div>
        <div class="flex flex-wrap items-center justify-center gap-3">
          <router-link
            to="/home"
            class="inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-sm text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 dark:text-dark-400 dark:hover:bg-dark-800 dark:hover:text-white"
          >
            <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.5">
              <path stroke-linecap="round" stroke-linejoin="round" d="M10.5 19.5L3 12m0 0l7.5-7.5M3 12h18" />
            </svg>
            {{ t('pricing.nav.home') }}
          </router-link>
          <CatalogViewSwitcher />
        </div>
      </div>
    </template>

    <div
      v-if="isAuthenticated"
      class="mb-4 flex shrink-0 flex-wrap items-center gap-3 border-b border-gray-200/80 pb-3 dark:border-dark-800"
      data-tk="catalog-hub-authed-toolbar"
    >
      <CatalogViewSwitcher />
    </div>

    <div class="catalog-hub-panel flex min-h-0 flex-1 flex-col">
      <KeepAlive>
        <ModelMarketplaceCatalog
          v-if="!isPricingView"
          key="browse"
          :show-bottom-cta="!isAuthenticated"
        />
        <PricingView v-else key="pricing" />
      </KeepAlive>
    </div>
  </CatalogHubShell>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import CatalogHubShell from '@/components/catalog/CatalogHubShell.vue'
import CatalogViewSwitcher from '@/components/catalog/CatalogViewSwitcher.vue'
import ModelMarketplaceCatalog from './ModelMarketplaceCatalog.vue'
import PricingView from './PricingView.vue'

const route = useRoute()
const { t } = useI18n()
const authStore = useAuthStore()

const isPricingView = computed(() => route.query.view === 'pricing')
const isAuthenticated = computed(() => authStore.isAuthenticated)

/** Pricing table uses internal scroll; guest shell stays one continuous column. */
const guestShellRootClass = computed(() =>
  isPricingView.value && !isAuthenticated.value
    ? 'h-[100dvh] max-h-[100dvh] overflow-hidden'
    : '',
)

const guestShellMainClass = computed(() =>
  isPricingView.value && !isAuthenticated.value
    ? 'flex min-h-0 flex-1 flex-col overflow-hidden pb-4 pt-3 sm:px-6 sm:pt-4'
    : 'pt-3 sm:pt-4',
)
</script>

<style scoped>
.catalog-hub-panel :deep(.catalog-panel-enter-active),
.catalog-hub-panel :deep(.catalog-panel-leave-active) {
  transition: opacity 0.18s ease;
}

.catalog-hub-panel :deep(.catalog-panel-enter-from),
.catalog-hub-panel :deep(.catalog-panel-leave-to) {
  opacity: 0;
}
</style>

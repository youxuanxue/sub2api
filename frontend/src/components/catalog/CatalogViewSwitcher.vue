<template>
  <nav
    role="tablist"
    :aria-label="t('catalog.viewSwitcherAria')"
    class="inline-flex rounded-xl border border-gray-200 bg-gray-50 p-1 text-sm font-medium dark:border-dark-700 dark:bg-dark-800"
    data-tk="catalog-view-switcher"
  >
    <router-link
      :to="browseTo"
      role="tab"
      :aria-selected="activeView === 'browse'"
      data-tk="catalog-view-browse"
      class="rounded-lg px-4 py-1.5 transition-colors"
      :class="
        activeView === 'browse'
          ? 'bg-primary-600 text-white shadow-sm dark:bg-primary-500'
          : 'text-gray-600 hover:text-primary-700 dark:text-dark-300'
      "
    >
      {{ t('catalog.viewBrowse') }}
    </router-link>
    <router-link
      :to="pricingTo"
      role="tab"
      :aria-selected="activeView === 'pricing'"
      data-tk="catalog-view-pricing"
      class="rounded-lg px-4 py-1.5 transition-colors"
      :class="
        activeView === 'pricing'
          ? 'bg-primary-600 text-white shadow-sm dark:bg-primary-500'
          : 'text-gray-600 hover:text-primary-700 dark:text-dark-300'
      "
    >
      {{ t('catalog.viewPricing') }}
    </router-link>
  </nav>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'

const route = useRoute()
const { t } = useI18n()

const activeView = computed<'browse' | 'pricing'>(() =>
  route.query.view === 'pricing' ? 'pricing' : 'browse',
)

const browseTo = computed(() => {
  const query = { ...route.query }
  delete query.view
  return Object.keys(query).length > 0 ? { path: '/models', query } : { path: '/models' }
})

const pricingTo = computed(() => ({
  path: '/models',
  query: { ...route.query, view: 'pricing' },
}))
</script>

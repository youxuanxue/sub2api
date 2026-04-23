<template>
  <div
    class="relative flex min-h-screen flex-col bg-gradient-to-br from-gray-50 via-primary-50/30 to-gray-100 dark:from-dark-950 dark:via-dark-900 dark:to-dark-950"
  >
    <header class="relative z-20 px-6 py-4">
      <nav class="mx-auto flex max-w-6xl items-center justify-between">
        <router-link
          to="/home"
          class="flex items-center gap-2 text-gray-700 transition-colors hover:text-primary-600 dark:text-dark-300 dark:hover:text-primary-300"
        >
          <Icon name="arrowLeft" size="sm" />
          <span class="text-sm font-medium">{{ t('pricing.backHome') }}</span>
        </router-link>
        <LocaleSwitcher />
      </nav>
    </header>

    <main class="relative z-10 flex-1 px-6 pb-16 pt-8">
      <div class="mx-auto max-w-6xl">
        <div class="mb-8 text-center">
          <h1
            class="text-3xl font-bold tracking-tight text-gray-900 dark:text-white sm:text-4xl"
          >
            {{ t('pricing.title') }}
          </h1>
          <p class="mt-3 text-base text-gray-600 dark:text-dark-300">
            {{ t('pricing.subtitle') }}
          </p>
          <p class="mx-auto mt-4 max-w-3xl text-sm text-gray-500 dark:text-dark-400">
            {{ t('pricing.description') }}
          </p>
        </div>

        <div
          v-if="loading"
          class="flex items-center justify-center rounded-2xl bg-white/80 py-24 shadow-sm backdrop-blur dark:bg-dark-900/60"
        >
          <Icon name="refresh" size="lg" class="animate-spin text-primary-500" />
          <span class="ml-3 text-sm text-gray-600 dark:text-dark-300">{{
            t('common.loading')
          }}</span>
        </div>

        <div
          v-else-if="errorMessage"
          class="rounded-2xl border border-red-200 bg-red-50/80 p-8 text-center dark:border-red-900/40 dark:bg-red-950/30"
        >
          <Icon name="exclamationTriangle" size="xl" class="mx-auto text-red-500" />
          <h2 class="mt-4 text-lg font-semibold text-red-800 dark:text-red-200">
            {{ t('pricing.errorTitle') }}
          </h2>
          <p class="mt-2 text-sm text-red-700/90 dark:text-red-300/80">{{ errorMessage }}</p>
          <p class="mt-1 text-xs text-red-600/80 dark:text-red-300/70">
            {{ t('pricing.errorHint') }}
          </p>
          <button
            type="button"
            class="mt-5 inline-flex items-center gap-1.5 rounded-lg border border-red-300 bg-white px-4 py-2 text-sm font-medium text-red-700 transition-colors hover:bg-red-100 dark:border-red-800 dark:bg-red-950/60 dark:text-red-200 dark:hover:bg-red-900/60"
            @click="loadCatalog"
          >
            <Icon name="refresh" size="sm" />
            {{ t('pricing.retry') }}
          </button>
        </div>

        <div
          v-else-if="!catalog || catalog.data.length === 0"
          class="rounded-2xl border border-dashed border-gray-300 bg-white/60 p-12 text-center dark:border-dark-700 dark:bg-dark-900/40"
        >
          <Icon name="inbox" size="xl" class="mx-auto text-gray-400 dark:text-dark-500" />
          <h2 class="mt-4 text-lg font-semibold text-gray-800 dark:text-dark-100">
            {{ t('pricing.empty.title') }}
          </h2>
          <p class="mt-2 text-sm text-gray-500 dark:text-dark-400">
            {{ t('pricing.empty.hint') }}
          </p>
        </div>

        <div
          v-else
          class="overflow-hidden rounded-2xl border border-gray-200 bg-white shadow-sm dark:border-dark-800 dark:bg-dark-900"
          data-tk="cold-start-pricing-table"
        >
          <div class="overflow-x-auto">
            <table class="min-w-full divide-y divide-gray-200 dark:divide-dark-800">
              <thead class="bg-gray-50 dark:bg-dark-800/60">
                <tr>
                  <th
                    scope="col"
                    class="px-4 py-3 text-left text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-dark-300"
                  >
                    {{ t('pricing.columns.model') }}
                  </th>
                  <th
                    scope="col"
                    class="px-4 py-3 text-left text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-dark-300"
                  >
                    {{ t('pricing.columns.vendor') }}
                  </th>
                  <th
                    scope="col"
                    class="px-4 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-dark-300"
                  >
                    {{ t('pricing.columns.input') }}
                  </th>
                  <th
                    scope="col"
                    class="px-4 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-dark-300"
                  >
                    {{ t('pricing.columns.output') }}
                  </th>
                  <th
                    v-if="hasCacheColumns"
                    scope="col"
                    class="px-4 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-dark-300"
                  >
                    {{ t('pricing.columns.cacheRead') }}
                  </th>
                  <th
                    v-if="hasCacheColumns"
                    scope="col"
                    class="px-4 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-dark-300"
                  >
                    {{ t('pricing.columns.cacheWrite') }}
                  </th>
                  <th
                    scope="col"
                    class="px-4 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-dark-300"
                  >
                    {{ t('pricing.columns.contextWindow') }}
                  </th>
                  <th
                    scope="col"
                    class="px-4 py-3 text-left text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-dark-300"
                  >
                    {{ t('pricing.columns.capabilities') }}
                  </th>
                </tr>
              </thead>
              <tbody
                class="divide-y divide-gray-100 bg-white dark:divide-dark-800/60 dark:bg-dark-900"
              >
                <tr
                  v-for="model in catalog.data"
                  :key="model.model_id"
                  class="hover:bg-primary-50/30 dark:hover:bg-dark-800/40"
                >
                  <td class="whitespace-nowrap px-4 py-3 font-mono text-sm text-gray-900 dark:text-white">
                    {{ model.model_id }}
                  </td>
                  <td class="whitespace-nowrap px-4 py-3 text-sm text-gray-600 dark:text-dark-300">
                    {{ model.vendor || '—' }}
                  </td>
                  <td class="whitespace-nowrap px-4 py-3 text-right text-sm tabular-nums text-gray-900 dark:text-white">
                    {{ formatPrice(model.pricing.input_per_1k_tokens) }}
                    <span class="ml-0.5 text-xs text-gray-400">{{
                      t('pricing.perThousandTokens')
                    }}</span>
                  </td>
                  <td class="whitespace-nowrap px-4 py-3 text-right text-sm tabular-nums text-gray-900 dark:text-white">
                    {{ formatPrice(model.pricing.output_per_1k_tokens) }}
                    <span class="ml-0.5 text-xs text-gray-400">{{
                      t('pricing.perThousandTokens')
                    }}</span>
                  </td>
                  <td
                    v-if="hasCacheColumns"
                    class="whitespace-nowrap px-4 py-3 text-right text-sm tabular-nums text-gray-700 dark:text-dark-200"
                  >
                    {{
                      model.pricing.cache_read_per_1k != null
                        ? formatPrice(model.pricing.cache_read_per_1k)
                        : '—'
                    }}
                  </td>
                  <td
                    v-if="hasCacheColumns"
                    class="whitespace-nowrap px-4 py-3 text-right text-sm tabular-nums text-gray-700 dark:text-dark-200"
                  >
                    {{
                      model.pricing.cache_write_per_1k != null
                        ? formatPrice(model.pricing.cache_write_per_1k)
                        : '—'
                    }}
                  </td>
                  <td class="whitespace-nowrap px-4 py-3 text-right text-sm tabular-nums text-gray-600 dark:text-dark-300">
                    <template v-if="model.context_window && model.context_window > 0">
                      {{ formatNumber(model.context_window) }}
                    </template>
                    <template v-else>—</template>
                  </td>
                  <td class="px-4 py-3">
                    <div class="flex flex-wrap gap-1">
                      <span
                        v-for="cap in model.capabilities"
                        :key="cap"
                        class="inline-flex items-center rounded-full bg-primary-50 px-2 py-0.5 text-xs font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
                      >
                        {{ cap }}
                      </span>
                      <span
                        v-if="!model.capabilities || model.capabilities.length === 0"
                        class="text-xs text-gray-400"
                        >—</span
                      >
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <div
            class="border-t border-gray-100 bg-gray-50/60 px-4 py-2 text-right text-xs text-gray-500 dark:border-dark-800 dark:bg-dark-800/40 dark:text-dark-400"
          >
            {{ t('pricing.updatedAt', { time: formattedUpdatedAt }) }}
          </div>
        </div>
      </div>
    </main>
  </div>
</template>

<script setup lang="ts">
/**
 * Public model + pricing catalog page.
 *
 * Backed by GET /api/v1/public/pricing (no auth required). Renders an empty
 * state when the catalog is unavailable (US-028 AC-005) and a 404 navigation
 * (handled by the API client) when the admin has flipped
 * `pricing_catalog_public = false`.
 *
 * docs/approved/user-cold-start.md §2 (v1 MVP).
 */
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getPublicPricing, type PublicCatalogResponse } from '@/api/pricing'
import Icon from '@/components/icons/Icon.vue'
import LocaleSwitcher from '@/components/common/LocaleSwitcher.vue'

const { t } = useI18n()

const catalog = ref<PublicCatalogResponse | null>(null)
const loading = ref(true)
const errorMessage = ref('')

const hasCacheColumns = computed(() => {
  if (!catalog.value) return false
  return catalog.value.data.some(
    (m) =>
      (m.pricing.cache_read_per_1k != null && m.pricing.cache_read_per_1k > 0) ||
      (m.pricing.cache_write_per_1k != null && m.pricing.cache_write_per_1k > 0)
  )
})

const formattedUpdatedAt = computed(() => {
  if (!catalog.value?.updated_at) return ''
  try {
    return new Date(catalog.value.updated_at).toLocaleString()
  } catch {
    return catalog.value.updated_at
  }
})

function formatPrice(value: number): string {
  if (!Number.isFinite(value)) return '—'
  if (value === 0) return '$0'
  if (value < 0.01) return `$${value.toFixed(6)}`
  if (value < 1) return `$${value.toFixed(4)}`
  return `$${value.toFixed(2)}`
}

function formatNumber(value: number): string {
  return new Intl.NumberFormat().format(value)
}

async function loadCatalog(): Promise<void> {
  loading.value = true
  errorMessage.value = ''
  try {
    catalog.value = await getPublicPricing()
  } catch (err) {
    const apiErr = err as { status?: number; message?: string }
    if (apiErr.status === 404) {
      // Admin disabled the public catalog; treat as "empty" rather than error
      // so the page still renders cleanly when the entry leaks through cache.
      catalog.value = { object: 'list', data: [], updated_at: '' }
    } else {
      errorMessage.value = apiErr.message || 'Network error'
    }
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  loadCatalog()
})
</script>

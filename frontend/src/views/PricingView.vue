<template>
  <div
    class="relative flex min-h-screen flex-col bg-gradient-to-br from-gray-50 via-primary-50/30 to-gray-100 dark:from-dark-950 dark:via-dark-900 dark:to-dark-950"
  >
    <header class="relative z-20 px-4 py-4 sm:px-6">
      <nav
        class="mx-auto flex max-w-[90rem] flex-wrap items-center justify-between gap-4"
        :aria-label="t('pricing.nav.aria')"
      >
        <div class="flex flex-wrap items-center gap-2">
          <router-link to="/home" :class="NAV_LINK_CLASS">
            <Icon name="home" size="sm" :class="NAV_ICON_CLASS" />
            <span>{{ t('pricing.nav.home') }}</span>
          </router-link>
          <router-link :to="consolePath" :class="NAV_LINK_CLASS" :title="consoleLinkTitle">
            <Icon name="grid" size="sm" :class="NAV_ICON_CLASS" />
            <span>{{ t('pricing.nav.console') }}</span>
          </router-link>
        </div>
        <LocaleSwitcher />
      </nav>
    </header>

    <main class="relative z-10 flex-1 px-4 pb-16 pt-8 sm:px-6">
      <div class="mx-auto max-w-[90rem]">
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
          <div
            v-if="bonusCtaVisible"
            class="mx-auto mt-8 flex max-w-lg flex-col items-center gap-3 rounded-2xl border border-primary-200/70 bg-primary-50/90 px-6 py-5 text-center shadow-sm dark:border-primary-900/40 dark:bg-primary-950/40"
          >
            <router-link
              to="/register"
              class="inline-flex items-center justify-center rounded-xl bg-primary-600 px-6 py-3 text-sm font-semibold text-white shadow-sm transition hover:bg-primary-700 dark:bg-primary-500 dark:hover:bg-primary-600"
            >
              {{ t('pricing.ctaBonus', { amount: signupBonusFormatted }) }}
            </router-link>
            <p class="text-xs text-gray-600 dark:text-dark-400">{{ t('pricing.ctaBonusHint') }}</p>
          </div>
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
          <div
            class="flex flex-col gap-3 border-b border-gray-100 bg-gray-50/80 px-4 py-3 dark:border-dark-800 dark:bg-dark-800/40 sm:flex-row sm:items-center sm:justify-between"
          >
            <label class="sr-only" for="pricing-model-search">{{ t('pricing.search.placeholder') }}</label>
            <div class="relative min-w-0 flex-1 xl:max-w-xl">
              <Icon
                name="search"
                size="sm"
                class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 dark:text-dark-500"
              />
              <input
                id="pricing-model-search"
                v-model="modelSearchQuery"
                type="search"
                autocomplete="off"
                :placeholder="t('pricing.search.placeholder')"
                class="w-full rounded-lg border border-gray-200 bg-white py-2 pl-9 pr-3 text-sm text-gray-900 placeholder:text-gray-400 focus:border-primary-500 focus:outline-none focus:ring-2 focus:ring-primary-500/20 dark:border-dark-700 dark:bg-dark-900 dark:text-white dark:placeholder:text-dark-500"
              />
            </div>
            <div class="flex shrink-0 flex-wrap items-center gap-4">
              <fieldset class="flex items-center gap-3 text-xs">
                <legend class="sr-only">{{ t('pricing.search.modeLabel') }}</legend>
                <label class="inline-flex cursor-pointer items-center gap-1.5 text-gray-700 dark:text-dark-200">
                  <input v-model="modelSearchMode" type="radio" value="fuzzy" class="text-primary-600" />
                  {{ t('pricing.search.modeFuzzy') }}
                </label>
                <label class="inline-flex cursor-pointer items-center gap-1.5 text-gray-700 dark:text-dark-200">
                  <input v-model="modelSearchMode" type="radio" value="exact" class="text-primary-600" />
                  {{ t('pricing.search.modeExact') }}
                </label>
              </fieldset>
              <span
                v-if="modelSearchQuery.trim()"
                class="text-xs tabular-nums text-gray-500 dark:text-dark-400"
              >
                {{ t('pricing.search.resultCount', { count: filteredCatalogRows.length }) }}
              </span>
            </div>
          </div>
          <p
            class="border-b border-gray-100 bg-gray-50/50 px-4 py-2 text-xs text-gray-500 dark:border-dark-800 dark:bg-dark-800/30 dark:text-dark-400 lg:hidden"
          >
            {{ t('pricing.tableHint') }}
          </p>
          <div
            v-if="filteredCatalogRows.length === 0 && modelSearchQuery.trim()"
            class="border-t border-gray-100 px-4 py-12 text-center dark:border-dark-800"
          >
            <Icon name="inbox" size="xl" class="mx-auto text-gray-400 dark:text-dark-500" />
            <p class="mt-3 text-sm font-medium text-gray-700 dark:text-dark-200">
              {{ t('pricing.search.noMatches') }}
            </p>
          </div>
          <div v-else class="overflow-x-auto [-webkit-overflow-scrolling:touch]">
            <table class="min-w-[72rem] w-full divide-y divide-gray-200 dark:divide-dark-800">
              <thead class="bg-gray-50 dark:bg-dark-800/60">
                <tr>
                  <th
                    scope="col"
                    class="sticky left-0 z-20 min-w-[14rem] max-w-[28rem] border-r border-gray-200 bg-gray-50 px-3 py-3 text-left text-xs font-semibold uppercase tracking-wider text-gray-500 shadow-[4px_0_12px_-8px_rgba(0,0,0,0.15)] dark:border-dark-700 dark:bg-dark-800/60 dark:text-dark-300 dark:shadow-[4px_0_12px_-8px_rgba(0,0,0,0.4)]"
                  >
                    {{ t('pricing.columns.model') }}
                  </th>
                  <th
                    scope="col"
                    class="min-w-[7rem] px-3 py-3 text-left text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-dark-300"
                  >
                    {{ t('pricing.columns.vendor') }}
                  </th>
                  <th
                    scope="col"
                    class="px-3 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-dark-300"
                  >
                    {{ t('pricing.columns.input') }}
                  </th>
                  <th
                    scope="col"
                    class="px-3 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-dark-300"
                  >
                    {{ t('pricing.columns.output') }}
                  </th>
                  <th
                    v-if="hasCacheColumns"
                    scope="col"
                    class="px-3 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-dark-300"
                  >
                    {{ t('pricing.columns.cacheRead') }}
                  </th>
                  <th
                    v-if="hasCacheColumns"
                    scope="col"
                    class="px-3 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-dark-300"
                  >
                    {{ t('pricing.columns.cacheWrite') }}
                  </th>
                  <th
                    scope="col"
                    class="px-3 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-dark-300"
                  >
                    {{ t('pricing.columns.contextWindow') }}
                  </th>
                  <th
                    v-if="hasMaxOutputColumn"
                    scope="col"
                    class="px-3 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-dark-300"
                  >
                    {{ t('pricing.columns.maxOutput') }}
                  </th>
                  <th
                    scope="col"
                    class="min-w-[10rem] px-3 py-3 text-left text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-dark-300"
                  >
                    {{ t('pricing.columns.capabilities') }}
                  </th>
                </tr>
              </thead>
              <tbody
                class="divide-y divide-gray-100 bg-white dark:divide-dark-800/60 dark:bg-dark-900"
              >
                <tr
                  v-for="model in filteredCatalogRows"
                  :key="model.model_id"
                  class="group hover:bg-primary-50/30 dark:hover:bg-dark-800/40"
                >
                  <td
                    class="sticky left-0 z-10 min-w-[14rem] max-w-[28rem] border-r border-gray-200 bg-white px-3 py-3 align-top font-mono text-sm leading-snug text-gray-900 shadow-[4px_0_12px_-8px_rgba(0,0,0,0.12)] break-words group-hover:bg-primary-50/30 dark:border-dark-700 dark:bg-dark-900 dark:text-white dark:shadow-[4px_0_12px_-8px_rgba(0,0,0,0.45)] dark:group-hover:bg-dark-800/40"
                  >
                    {{ model.model_id }}
                  </td>
                  <td
                    class="min-w-[7rem] px-3 py-3 align-top text-sm leading-snug text-gray-600 break-words dark:text-dark-300"
                  >
                    {{ model.vendor || '—' }}
                  </td>
                  <td class="whitespace-nowrap px-3 py-3 text-right text-sm tabular-nums text-gray-900 dark:text-white">
                    {{ formatPrice(model.pricing.input_per_1k_tokens) }}
                    <span class="ml-0.5 text-xs text-gray-400">{{
                      t('pricing.perThousandTokens')
                    }}</span>
                  </td>
                  <td class="whitespace-nowrap px-3 py-3 text-right text-sm tabular-nums text-gray-900 dark:text-white">
                    {{ formatPrice(model.pricing.output_per_1k_tokens) }}
                    <span class="ml-0.5 text-xs text-gray-400">{{
                      t('pricing.perThousandTokens')
                    }}</span>
                  </td>
                  <td
                    v-if="hasCacheColumns"
                    class="whitespace-nowrap px-3 py-3 text-right text-sm tabular-nums text-gray-700 dark:text-dark-200"
                  >
                    {{
                      model.pricing.cache_read_per_1k != null
                        ? formatPrice(model.pricing.cache_read_per_1k)
                        : '—'
                    }}
                  </td>
                  <td
                    v-if="hasCacheColumns"
                    class="whitespace-nowrap px-3 py-3 text-right text-sm tabular-nums text-gray-700 dark:text-dark-200"
                  >
                    {{
                      model.pricing.cache_write_per_1k != null
                        ? formatPrice(model.pricing.cache_write_per_1k)
                        : '—'
                    }}
                  </td>
                  <td class="whitespace-nowrap px-3 py-3 text-right text-sm tabular-nums text-gray-600 dark:text-dark-300">
                    <template v-if="model.context_window && model.context_window > 0">
                      {{ formatNumber(model.context_window) }}
                    </template>
                    <template v-else>—</template>
                  </td>
                  <td
                    v-if="hasMaxOutputColumn"
                    class="whitespace-nowrap px-3 py-3 text-right text-sm tabular-nums text-gray-600 dark:text-dark-300"
                  >
                    <template v-if="model.max_output_tokens && model.max_output_tokens > 0">
                      {{ formatNumber(model.max_output_tokens) }}
                    </template>
                    <template v-else>—</template>
                  </td>
                  <td class="px-3 py-3 align-top">
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
            class="flex flex-col gap-2 border-t border-gray-100 bg-gray-50/60 px-4 py-3 text-xs text-gray-500 sm:flex-row sm:items-center sm:justify-between dark:border-dark-800 dark:bg-dark-800/40 dark:text-dark-400"
          >
            <p class="text-left tabular-nums">
              <template v-if="modelSearchQuery.trim()">
                {{
                  t('pricing.footer.filtered', {
                    shown: filteredCatalogRows.length,
                    total: catalog?.data.length ?? 0
                  })
                }}
              </template>
              <template v-else>
                {{ t('pricing.footer.total', { count: catalog?.data.length ?? 0 }) }}
              </template>
            </p>
            <p class="text-left sm:text-right">
              {{ t('pricing.updatedAt', { time: formattedUpdatedAt }) }}
            </p>
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
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import { formatCurrency } from '@/utils/format'
import {
  filterPricingCatalogByModel,
  type PricingCatalogSearchMode
} from '@/utils/pricingCatalogSearch'

const { t } = useI18n()
const authStore = useAuthStore()
const appStore = useAppStore()

const signupBonusFormatted = computed(() =>
  formatCurrency(appStore.cachedPublicSettings?.signup_bonus_balance_usd ?? 0, 'USD')
)

const bonusCtaVisible = computed(() => {
  const s = appStore.cachedPublicSettings
  if (!s?.registration_enabled) return false
  if (s.backend_mode_enabled) return false
  if (!s.signup_bonus_enabled) return false
  const amt = s.signup_bonus_balance_usd ?? 0
  return amt > 0 && !authStore.isAuthenticated
})

/** Shared nav pill styles — single source to reduce churn vs upstream-style pages. */
const NAV_LINK_CLASS =
  'group inline-flex items-center gap-2 rounded-xl border border-gray-200/80 bg-white/90 px-3.5 py-2 text-sm font-medium text-gray-700 shadow-sm backdrop-blur transition-colors hover:border-primary-200 hover:bg-primary-50/80 hover:text-primary-800 dark:border-dark-700 dark:bg-dark-900/70 dark:text-dark-200 dark:hover:border-primary-700/60 dark:hover:bg-primary-950/40 dark:hover:text-primary-200'
const NAV_ICON_CLASS =
  'text-gray-500 transition-colors group-hover:text-primary-600 dark:text-dark-400 dark:group-hover:text-primary-300'

const consolePath = computed(() => {
  if (!authStore.isAuthenticated) {
    return { path: '/login', query: { redirect: '/dashboard' } }
  }
  return authStore.isAdmin ? '/admin/dashboard' : '/dashboard'
})

const consoleLinkTitle = computed(() =>
  authStore.isAuthenticated ? t('pricing.nav.consoleTitleAuthed') : t('pricing.nav.consoleTitleGuest')
)

const catalog = ref<PublicCatalogResponse | null>(null)
const loading = ref(true)
const errorMessage = ref('')
const modelSearchQuery = ref('')
const modelSearchMode = ref<PricingCatalogSearchMode>('fuzzy')

const filteredCatalogRows = computed(() => {
  if (!catalog.value) return []
  return filterPricingCatalogByModel(
    catalog.value.data,
    modelSearchQuery.value,
    modelSearchMode.value
  )
})

const hasCacheColumns = computed(() => {
  if (!catalog.value) return false
  return catalog.value.data.some(
    (m) =>
      (m.pricing.cache_read_per_1k != null && m.pricing.cache_read_per_1k > 0) ||
      (m.pricing.cache_write_per_1k != null && m.pricing.cache_write_per_1k > 0)
  )
})

/** Show column when catalog has max-output metadata (often unset for slim entries). */
const hasMaxOutputColumn = computed(() => {
  if (!catalog.value) return false
  return catalog.value.data.some((m) => (m.max_output_tokens ?? 0) > 0)
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
  void appStore.fetchPublicSettings()
})
</script>

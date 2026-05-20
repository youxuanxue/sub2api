<template>
  <div
    class="relative flex flex-col bg-gradient-to-br from-gray-50 via-primary-50/30 to-gray-100 dark:from-dark-950 dark:via-dark-900 dark:to-dark-950"
    :class="
      pricingCatalogScrollMode
        ? 'h-[100dvh] max-h-[100dvh] overflow-hidden'
        : 'min-h-screen'
    "
  >
    <main
      class="relative z-10 flex min-h-0 flex-1 flex-col px-4 pt-8 sm:px-6"
      :class="pricingCatalogScrollMode ? 'overflow-hidden pb-4' : 'pb-16'"
    >
      <!-- Sticky chrome: nav + hero (when catalog table is shown, only tbody scrolls) -->
      <div class="mx-auto w-full max-w-[90rem] shrink-0 pb-4">
        <header class="relative z-20 py-4 sm:py-0">
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

        <div class="mt-6 text-center sm:mt-8">
          <h1
            class="text-3xl font-bold tracking-tight text-gray-900 dark:text-white sm:text-4xl"
          >
            {{ heroTitle }}
          </h1>
          <p class="mt-3 text-base text-gray-600 dark:text-dark-300">
            {{ heroSubtitle }}
          </p>
          <p class="mx-auto mt-4 max-w-3xl text-sm text-gray-500 dark:text-dark-400">
            {{ heroDescription }}
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

          <!-- Segmented view switch: 我的菜单 / 公开目录. Only shown when logged in. -->
          <div v-if="canShowMyView" class="mt-6 flex justify-center">
            <div
              role="tablist"
              class="inline-flex rounded-xl border border-gray-200 bg-white/80 p-1 shadow-sm dark:border-dark-700 dark:bg-dark-900/70"
            >
              <button
                v-for="opt in viewOptions"
                :key="opt.value"
                role="tab"
                :aria-selected="viewMode === opt.value"
                type="button"
                class="rounded-lg px-4 py-1.5 text-sm font-medium transition-colors"
                :class="
                  viewMode === opt.value
                    ? 'bg-primary-600 text-white shadow-sm dark:bg-primary-500'
                    : 'text-gray-600 hover:text-primary-700 dark:text-dark-300 dark:hover:text-primary-200'
                "
                @click="setView(opt.value)"
              >
                {{ opt.label }}
              </button>
            </div>
          </div>
        </div>
      </div>

      <div class="mx-auto flex min-h-0 w-full max-w-[90rem] flex-1 flex-col">
        <!-- "我的菜单" toolbar: key picker + group switcher + banner -->
        <div
          v-if="viewMode === 'my' && !loading && !errorMessage && myCatalog"
          class="mb-4 flex flex-col gap-3"
        >
          <div
            class="flex flex-col gap-3 rounded-2xl border border-gray-200 bg-white/80 px-4 py-3 shadow-sm dark:border-dark-800 dark:bg-dark-900/70 sm:flex-row sm:items-center sm:justify-between"
          >
            <div class="flex flex-col gap-1 sm:flex-row sm:items-center sm:gap-3">
              <label
                v-if="myCatalog.my_keys.length > 0"
                class="flex items-center gap-2 text-sm text-gray-700 dark:text-dark-200"
              >
                <span class="font-medium">{{ t('pricing.my.pickerKey') }}</span>
                <select
                  :value="displayKeyId"
                  class="rounded-lg border border-gray-200 bg-white px-3 py-1.5 text-sm dark:border-dark-700 dark:bg-dark-900 dark:text-white"
                  @change="onPickKey"
                >
                  <option
                    v-for="k in myCatalog.my_keys"
                    :key="k.id"
                    :value="k.id"
                  >
                    {{ k.name }} · {{ k.group_name }}
                  </option>
                </select>
              </label>
              <span
                v-else
                class="text-sm text-gray-500 dark:text-dark-400"
              >
                {{ t('pricing.my.noKeyHint') }}
              </span>
            </div>
            <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-dark-200">
              <span>{{ t('pricing.my.pickerCompare') }}</span>
              <select
                :value="selectedGroupId"
                class="rounded-lg border border-gray-200 bg-white px-3 py-1.5 text-sm dark:border-dark-700 dark:bg-dark-900 dark:text-white"
                @change="onPickGroup"
              >
                <option :value="0">{{ t('pricing.my.compareDefault') }}</option>
                <option
                  v-for="g in groupsForComparison"
                  :key="g.id"
                  :value="g.id"
                >
                  {{ g.name }}{{ formatGroupRateBadge(g) }}
                </option>
              </select>
            </label>
          </div>

          <!-- Banner: exploring a group the user doesn't have a key in -->
          <div
            v-if="exploreBanner"
            class="flex flex-col gap-2 rounded-2xl border border-primary-200 bg-primary-50/80 px-4 py-3 text-sm text-primary-900 shadow-sm dark:border-primary-900/50 dark:bg-primary-950/40 dark:text-primary-200 sm:flex-row sm:items-center sm:justify-between"
          >
            <p>{{ exploreBanner.message }}</p>
            <router-link
              :to="exploreBanner.ctaTo"
              class="inline-flex items-center justify-center rounded-lg bg-primary-600 px-3.5 py-1.5 text-xs font-semibold text-white shadow-sm transition hover:bg-primary-700 dark:bg-primary-500 dark:hover:bg-primary-600"
            >
              {{ exploreBanner.ctaLabel }}
            </router-link>
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
            @click="reload"
          >
            <Icon name="refresh" size="sm" />
            {{ t('pricing.retry') }}
          </button>
        </div>

        <div
          v-else-if="normalizedRows.length === 0 && !hasFilterActive"
          class="rounded-2xl border border-dashed border-gray-300 bg-white/60 p-12 text-center dark:border-dark-700 dark:bg-dark-900/40"
        >
          <Icon name="inbox" size="xl" class="mx-auto text-gray-400 dark:text-dark-500" />
          <h2 class="mt-4 text-lg font-semibold text-gray-800 dark:text-dark-100">
            {{ emptyTitle }}
          </h2>
          <p class="mt-2 text-sm text-gray-500 dark:text-dark-400">
            {{ emptyHint }}
          </p>
        </div>

        <div
          v-else
          class="flex min-h-0 flex-1 flex-col overflow-hidden rounded-2xl border border-gray-200 bg-white shadow-sm dark:border-dark-800 dark:bg-dark-900"
          data-tk="cold-start-pricing-table"
        >
          <div
            class="flex shrink-0 flex-col gap-3 border-b border-gray-100 bg-gray-50/80 px-4 py-3 dark:border-dark-800 dark:bg-dark-800/40 sm:flex-row sm:items-center sm:justify-between"
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
                {{ t('pricing.search.resultCount', { count: filteredRows.length }) }}
              </span>
            </div>
          </div>
          <p
            class="shrink-0 border-b border-gray-100 bg-gray-50/50 px-4 py-2 text-xs text-gray-500 dark:border-dark-800 dark:bg-dark-800/30 dark:text-dark-400 lg:hidden"
          >
            {{ t('pricing.tableHint') }}
          </p>
          <div
            v-if="filteredRows.length === 0 && modelSearchQuery.trim()"
            class="border-t border-gray-100 px-4 py-12 text-center dark:border-dark-800"
          >
            <Icon name="inbox" size="xl" class="mx-auto text-gray-400 dark:text-dark-500" />
            <p class="mt-3 text-sm font-medium text-gray-700 dark:text-dark-200">
              {{ t('pricing.search.noMatches') }}
            </p>
          </div>
          <div v-else class="flex min-h-0 flex-1 flex-col overflow-hidden">
            <div
              class="min-h-0 flex-1 overflow-x-auto overflow-y-auto overscroll-y-contain [-webkit-overflow-scrolling:touch]"
            >
              <table class="min-w-[72rem] w-full border-collapse divide-y divide-gray-200 dark:divide-dark-800">
                <thead class="bg-gray-50 dark:bg-dark-800/60">
                  <tr>
                    <th
                      scope="col"
                      class="sticky left-0 top-0 z-[45] min-w-[14rem] max-w-[28rem] border-r border-gray-200 bg-gray-50 px-3 py-3 text-left text-xs font-semibold uppercase tracking-wider text-gray-500 shadow-[4px_0_12px_-8px_rgba(0,0,0,0.15)] dark:border-dark-700 dark:bg-dark-800/60 dark:text-dark-300 dark:shadow-[4px_0_12px_-8px_rgba(0,0,0,0.4)]"
                    >
                      {{ t('pricing.columns.model') }}
                    </th>
                    <th
                      scope="col"
                      class="sticky top-0 z-40 min-w-[7rem] bg-gray-50 px-3 py-3 text-left text-xs font-semibold uppercase tracking-wider text-gray-500 dark:bg-dark-800/60 dark:text-dark-300"
                    >
                      {{ t('pricing.columns.vendor') }}
                    </th>
                    <th
                      scope="col"
                      class="sticky top-0 z-40 bg-gray-50 px-3 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:bg-dark-800/60 dark:text-dark-300"
                    >
                      {{ inputColumnTitle }}
                    </th>
                    <th
                      scope="col"
                      class="sticky top-0 z-40 bg-gray-50 px-3 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:bg-dark-800/60 dark:text-dark-300"
                    >
                      {{ outputColumnTitle }}
                    </th>
                    <th
                      v-if="hasCacheColumns"
                      scope="col"
                      class="sticky top-0 z-40 bg-gray-50 px-3 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:bg-dark-800/60 dark:text-dark-300"
                    >
                      {{ t('pricing.columns.cacheRead') }}
                    </th>
                    <th
                      v-if="hasCacheColumns"
                      scope="col"
                      class="sticky top-0 z-40 bg-gray-50 px-3 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:bg-dark-800/60 dark:text-dark-300"
                    >
                      {{ t('pricing.columns.cacheWrite') }}
                    </th>
                    <th
                      scope="col"
                      class="sticky top-0 z-40 bg-gray-50 px-3 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:bg-dark-800/60 dark:text-dark-300"
                    >
                      {{ t('pricing.columns.contextWindow') }}
                    </th>
                    <th
                      v-if="hasMaxOutputColumn"
                      scope="col"
                      class="sticky top-0 z-40 bg-gray-50 px-3 py-3 text-right text-xs font-semibold uppercase tracking-wider text-gray-500 dark:bg-dark-800/60 dark:text-dark-300"
                    >
                      {{ t('pricing.columns.maxOutput') }}
                    </th>
                    <th
                      scope="col"
                      class="sticky top-0 z-40 min-w-[10rem] bg-gray-50 px-3 py-3 text-left text-xs font-semibold uppercase tracking-wider text-gray-500 dark:bg-dark-800/60 dark:text-dark-300"
                    >
                      {{ t('pricing.columns.capabilities') }}
                    </th>
                  </tr>
                </thead>
                <tbody
                  class="divide-y divide-gray-100 bg-white dark:divide-dark-800/60 dark:bg-dark-900"
                >
                  <tr
                    v-for="row in filteredRows"
                    :key="row.model_id"
                    class="group hover:bg-primary-50/30 dark:hover:bg-dark-800/40"
                  >
                    <td
                      class="sticky left-0 z-10 min-w-[14rem] max-w-[28rem] border-r border-gray-200 bg-white px-3 py-3 align-top font-mono text-sm leading-snug text-gray-900 shadow-[4px_0_12px_-8px_rgba(0,0,0,0.12)] break-words group-hover:bg-primary-50/30 dark:border-dark-700 dark:bg-dark-900 dark:text-white dark:shadow-[4px_0_12px_-8px_rgba(0,0,0,0.45)] dark:group-hover:bg-dark-800/40"
                    >
                      {{ row.model_id }}
                      <span
                        v-if="row.billingMode && row.billingMode !== 'token'"
                        class="ml-1 inline-flex items-center rounded-md bg-amber-50 px-1.5 py-0.5 text-[10px] font-medium text-amber-800 dark:bg-amber-900/30 dark:text-amber-200"
                      >
                        {{ formatBillingMode(row.billingMode) }}
                      </span>
                    </td>
                    <td
                      class="min-w-[7rem] px-3 py-3 align-top text-sm leading-snug text-gray-600 break-words dark:text-dark-300"
                    >
                      {{ row.vendor || '—' }}
                    </td>
                    <td class="whitespace-nowrap px-3 py-3 text-right text-sm tabular-nums text-gray-900 dark:text-white">
                      <template v-if="row.billingMode === 'per_request' && row.perRequest != null">
                        {{ formatPrice(row.perRequest) }}
                        <span class="ml-0.5 text-xs text-gray-400">{{ t('pricing.perRequest') }}</span>
                      </template>
                      <template v-else-if="row.inputPer1K != null">
                        {{ formatPrice(row.inputPer1K) }}
                        <span class="ml-0.5 text-xs text-gray-400">{{
                          t('pricing.perThousandTokens')
                        }}</span>
                      </template>
                      <template v-else>—</template>
                    </td>
                    <td class="whitespace-nowrap px-3 py-3 text-right text-sm tabular-nums text-gray-900 dark:text-white">
                      <template v-if="row.billingMode === 'per_request'">—</template>
                      <template v-else-if="row.outputPer1K != null">
                        {{ formatPrice(row.outputPer1K) }}
                        <span class="ml-0.5 text-xs text-gray-400">{{
                          t('pricing.perThousandTokens')
                        }}</span>
                      </template>
                      <template v-else>—</template>
                    </td>
                    <td
                      v-if="hasCacheColumns"
                      class="whitespace-nowrap px-3 py-3 text-right text-sm tabular-nums text-gray-700 dark:text-dark-200"
                    >
                      {{ row.cacheReadPer1K != null ? formatPrice(row.cacheReadPer1K) : '—' }}
                    </td>
                    <td
                      v-if="hasCacheColumns"
                      class="whitespace-nowrap px-3 py-3 text-right text-sm tabular-nums text-gray-700 dark:text-dark-200"
                    >
                      {{ row.cacheWritePer1K != null ? formatPrice(row.cacheWritePer1K) : '—' }}
                    </td>
                    <td class="whitespace-nowrap px-3 py-3 text-right text-sm tabular-nums text-gray-600 dark:text-dark-300">
                      <template v-if="row.contextWindow && row.contextWindow > 0">
                        {{ formatNumber(row.contextWindow) }}
                      </template>
                      <template v-else>—</template>
                    </td>
                    <td
                      v-if="hasMaxOutputColumn"
                      class="whitespace-nowrap px-3 py-3 text-right text-sm tabular-nums text-gray-600 dark:text-dark-300"
                    >
                      <template v-if="row.maxOutputTokens && row.maxOutputTokens > 0">
                        {{ formatNumber(row.maxOutputTokens) }}
                      </template>
                      <template v-else>—</template>
                    </td>
                    <td class="px-3 py-3 align-top">
                      <div class="flex flex-wrap gap-1">
                        <span
                          v-for="cap in row.capabilities"
                          :key="cap"
                          class="inline-flex items-center rounded-full bg-primary-50 px-2 py-0.5 text-xs font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
                        >
                          {{ cap }}
                        </span>
                        <span
                          v-if="!row.capabilities || row.capabilities.length === 0"
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
              class="flex shrink-0 flex-col gap-2 border-t border-gray-100 bg-gray-50/60 px-4 py-3 text-xs text-gray-500 sm:flex-row sm:items-center sm:justify-between dark:border-dark-800 dark:bg-dark-800/40 dark:text-dark-400"
            >
              <p class="text-left tabular-nums">
                <template v-if="modelSearchQuery.trim()">
                  {{
                    t('pricing.footer.filtered', {
                      shown: filteredRows.length,
                      total: rowTotal
                    })
                  }}
                </template>
                <template v-else>
                  {{ t('pricing.footer.total', { count: rowTotal }) }}
                </template>
                <span v-if="rateHint" class="ml-2 text-gray-600 dark:text-dark-300">
                  · {{ rateHint }}
                </span>
              </p>
              <p class="text-left sm:text-right">
                {{ t('pricing.updatedAt', { time: formattedUpdatedAt }) }}
              </p>
            </div>
          </div>
        </div>
      </div>
    </main>
  </div>
</template>

<script setup lang="ts">
/**
 * Model + pricing catalog page.
 *
 * Two views share the same URL `/pricing`:
 *  - Public view (unauthenticated default): GET /api/v1/public/pricing returns
 *    the platform-wide LiteLLM list-price catalog. Behaves per US-028 AC-005
 *    (empty when source unavailable; 404 when admin disabled public catalog).
 *  - "我的菜单" view (authenticated default when accessible groups exist):
 *    GET /api/v1/me/pricing-catalog returns models scoped to the user's
 *    selected key's group, with prices already multiplied by their effective
 *    rate (group default × per-user override). The user can switch keys or
 *    explore other accessible groups for upgrade comparison.
 *
 * Both feed a single normalized row shape so the table markup stays identical
 * between modes — this is by design; the v1 trade-off accepted in
 * /Users/xuejiao/.claude/plans/bubbly-bouncing-sunbeam.md.
 */
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getPublicPricing, type PublicCatalogResponse } from '@/api/pricing'
import {
  getMePricingCatalog,
  type MePricingCatalogResponse,
  type MePricingGroupRef
} from '@/api/me-pricing'
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

type ViewMode = 'my' | 'public'

/**
 * Internal row shape — single source of truth for the shared table markup.
 *
 * Uses snake_case `model_id` (not camelCase modelId) so the row can flow
 * directly through `filterPricingCatalogByModel<T extends {model_id:string}>`
 * without an adapter mapping. Other fields stay camelCase because they're
 * derived/UI-shaped, not echoes of backend DTOs.
 */
interface NormalizedRow {
  model_id: string
  vendor: string
  inputPer1K: number | null
  outputPer1K: number | null
  cacheReadPer1K: number | null
  cacheWritePer1K: number | null
  contextWindow: number
  maxOutputTokens: number
  capabilities: string[]
  billingMode?: string
  perRequest?: number | null
}

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

// ============================== view-state ==============================

const viewMode = ref<ViewMode>('public')
const loading = ref(true)
const errorMessage = ref('')
const modelSearchQuery = ref('')
const modelSearchMode = ref<PricingCatalogSearchMode>('fuzzy')

// Public catalog state (unchanged from v1 — US-028 backing data).
const publicCatalog = ref<PublicCatalogResponse | null>(null)

// My catalog state.
const myCatalog = ref<MePricingCatalogResponse | null>(null)
const selectedKeyId = ref<number>(0)
const selectedGroupId = ref<number>(0)

const canShowMyView = computed(() => authStore.isAuthenticated)

const viewOptions = computed(() => [
  { value: 'my' as ViewMode, label: t('pricing.my.tabMy') },
  { value: 'public' as ViewMode, label: t('pricing.my.tabPublic') }
])

function setView(next: ViewMode): void {
  if (next === viewMode.value) return
  viewMode.value = next
  modelSearchQuery.value = ''
  void load()
}

// ============================== hero copy ==============================

const heroTitle = computed(() =>
  viewMode.value === 'my' ? t('pricing.my.title') : t('pricing.title')
)
const heroSubtitle = computed(() =>
  viewMode.value === 'my' ? t('pricing.my.subtitle') : t('pricing.subtitle')
)
const heroDescription = computed(() =>
  viewMode.value === 'my' ? t('pricing.my.description') : t('pricing.description')
)

const inputColumnTitle = computed(() =>
  viewMode.value === 'my' ? t('pricing.my.columns.input') : t('pricing.columns.input')
)
const outputColumnTitle = computed(() =>
  viewMode.value === 'my' ? t('pricing.my.columns.output') : t('pricing.columns.output')
)

// ============================== normalize ==============================

const normalizedRows = computed<NormalizedRow[]>(() => {
  if (viewMode.value === 'public') {
    if (!publicCatalog.value) return []
    return publicCatalog.value.data.map((m) => ({
      model_id: m.model_id,
      vendor: m.vendor ?? '',
      inputPer1K: m.pricing.input_per_1k_tokens ?? null,
      outputPer1K: m.pricing.output_per_1k_tokens ?? null,
      cacheReadPer1K: m.pricing.cache_read_per_1k ?? null,
      cacheWritePer1K: m.pricing.cache_write_per_1k ?? null,
      contextWindow: m.context_window ?? 0,
      maxOutputTokens: m.max_output_tokens ?? 0,
      capabilities: m.capabilities ?? [],
      billingMode: 'token',
      perRequest: null
    }))
  }
  // 'my' view
  if (!myCatalog.value) return []
  return myCatalog.value.models.map((m) => ({
    model_id: m.model_id,
    vendor: m.vendor ?? '',
    inputPer1K: m.your_price.input_per_1k ?? null,
    outputPer1K: m.your_price.output_per_1k ?? null,
    cacheReadPer1K: m.your_price.cache_read_per_1k ?? null,
    cacheWritePer1K: m.your_price.cache_write_per_1k ?? null,
    contextWindow: m.context_window ?? 0,
    maxOutputTokens: m.max_output_tokens ?? 0,
    capabilities: m.capabilities ?? [],
    billingMode: m.billing_mode,
    perRequest: m.your_price.per_request ?? null
  }))
})

const filteredRows = computed(() =>
  filterPricingCatalogByModel(normalizedRows.value, modelSearchQuery.value, modelSearchMode.value)
)

const hasCacheColumns = computed(() =>
  normalizedRows.value.some(
    (r) => (r.cacheReadPer1K != null && r.cacheReadPer1K > 0) ||
           (r.cacheWritePer1K != null && r.cacheWritePer1K > 0)
  )
)

const hasMaxOutputColumn = computed(() =>
  normalizedRows.value.some((r) => r.maxOutputTokens > 0)
)

const rowTotal = computed(() => normalizedRows.value.length)
const hasFilterActive = computed(() => modelSearchQuery.value.trim().length > 0)

// ============================== empty-state copy ==============================

const emptyTitle = computed(() => {
  if (viewMode.value === 'my' && myCatalog.value) {
    return myCatalog.value.my_keys.length === 0 && myCatalog.value.accessible_groups.length === 0
      ? t('pricing.my.empty.noAccess.title')
      : t('pricing.my.empty.noModels.title')
  }
  return t('pricing.empty.title')
})

const emptyHint = computed(() => {
  if (viewMode.value === 'my' && myCatalog.value) {
    return myCatalog.value.my_keys.length === 0 && myCatalog.value.accessible_groups.length === 0
      ? t('pricing.my.empty.noAccess.hint')
      : t('pricing.my.empty.noModels.hint')
  }
  return t('pricing.empty.hint')
})

// ============================== updated-at ==============================

const formattedUpdatedAt = computed(() => {
  const raw =
    viewMode.value === 'my'
      ? myCatalog.value?.updated_at
      : publicCatalog.value?.updated_at
  if (!raw) return ''
  try {
    return new Date(raw).toLocaleString()
  } catch {
    return raw
  }
})

// ============================== rate-hint ==============================

const rateHint = computed(() => {
  if (viewMode.value !== 'my' || !myCatalog.value) return ''
  const tg = myCatalog.value.target_group
  const rate = tg.rate_multiplier
  if (rate === 1 && !tg.has_override) return ''
  const fmt = rate === 0 ? '×0' : `×${rate}`
  const suffix = tg.has_override ? ` (${t('pricing.my.rateOverride')})` : ''
  return t('pricing.my.rateHint', { multiplier: fmt }) + suffix
})

// ============================== group switcher ==============================

const groupsForComparison = computed<MePricingGroupRef[]>(() => {
  if (!myCatalog.value) return []
  return myCatalog.value.accessible_groups
})

function formatGroupRateBadge(g: MePricingGroupRef): string {
  if (g.rate_multiplier === 1) return ''
  return ` · ×${g.rate_multiplier}`
}

interface ExploreBanner {
  message: string
  ctaLabel: string
  ctaTo: { path: string; query?: Record<string, string> }
}

const exploreBanner = computed<ExploreBanner | null>(() => {
  if (viewMode.value !== 'my' || !myCatalog.value) return null
  const tg = myCatalog.value.target_group
  const currentKeysInTarget = myCatalog.value.my_keys.some((k) => k.group_id === tg.id)
  if (currentKeysInTarget) return null
  // User is viewing a group they don't hold a key in → upgrade-CTA banner.
  return {
    message: t('pricing.my.exploreBanner.message', {
      group: tg.name,
      multiplier: tg.rate_multiplier
    }),
    ctaLabel: t('pricing.my.exploreBanner.cta', { group: tg.name }),
    ctaTo: { path: '/dashboard/keys', query: { group_id: String(tg.id) } }
  }
})

// ============================== misc formatters ==============================

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

function formatBillingMode(mode: string): string {
  return t(`pricing.my.billingMode.${mode}`, mode)
}

// ============================== layout ==============================

const pricingCatalogScrollMode = computed(() => {
  if (loading.value || errorMessage.value) return false
  return rowTotal.value > 0
})

// ============================== loaders ==============================

async function loadPublicCatalog(): Promise<void> {
  try {
    publicCatalog.value = await getPublicPricing()
  } catch (err) {
    const apiErr = err as { status?: number; message?: string }
    if (apiErr.status === 404) {
      publicCatalog.value = { object: 'list', data: [], updated_at: '' }
    } else {
      throw err
    }
  }
}

async function loadMyCatalog(): Promise<void> {
  const params: { apiKeyId?: number; groupId?: number } = {}
  if (selectedGroupId.value > 0) {
    params.groupId = selectedGroupId.value
  } else if (selectedKeyId.value > 0) {
    params.apiKeyId = selectedKeyId.value
  }
  myCatalog.value = await getMePricingCatalog(params)
}

/**
 * `displayKeyId` is the value the key-picker shows. Derived (never written
 * back into selectedKeyId by the loader) to avoid the watch-loop where a
 * post-load writeback fires another fetch. User picks are explicit via
 * onPickKey — no implicit reactive ping-pong.
 */
const displayKeyId = computed<number>(() => {
  if (selectedKeyId.value > 0) return selectedKeyId.value
  if (!myCatalog.value || myCatalog.value.my_keys.length === 0) return 0
  const tgID = myCatalog.value.target_group.id
  const match = myCatalog.value.my_keys.find((k) => k.group_id === tgID)
  return match?.id ?? myCatalog.value.my_keys[0].id
})

function onPickKey(e: Event): void {
  const next = Number((e.target as HTMLSelectElement).value)
  if (!Number.isFinite(next) || next <= 0) return
  selectedKeyId.value = next
  // Switching key clears any explore-group override.
  selectedGroupId.value = 0
  void load()
}

function onPickGroup(e: Event): void {
  const next = Number((e.target as HTMLSelectElement).value)
  if (!Number.isFinite(next) || next < 0) return
  selectedGroupId.value = next
  void load()
}

async function load(): Promise<void> {
  loading.value = true
  errorMessage.value = ''
  try {
    if (viewMode.value === 'my') {
      await loadMyCatalog()
    } else {
      await loadPublicCatalog()
    }
  } catch (err) {
    const apiErr = err as { message?: string }
    errorMessage.value = apiErr.message || 'Network error'
  } finally {
    loading.value = false
  }
}

function reload(): void {
  void load()
}

onMounted(() => {
  if (canShowMyView.value) {
    viewMode.value = 'my'
  }
  void load()
  void appStore.fetchPublicSettings()
})
</script>

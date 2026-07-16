<template>
    <div class="space-y-6">
      <!-- Loading State -->
      <div v-if="loading" class="flex items-center justify-center py-12">
        <LoadingSpinner />
      </div>

      <template v-else-if="stats">
        <!-- Row 1: Core Stats -->
        <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <!-- Total API Keys -->
          <div class="card p-4">
            <div class="flex items-center gap-3">
              <div class="rounded-lg bg-blue-100 p-2 dark:bg-blue-900/30">
                <Icon name="key" size="md" class="text-blue-600 dark:text-blue-400" :stroke-width="2" />
              </div>
              <div>
                <p class="text-xs font-medium text-gray-500 dark:text-gray-400">
                  {{ t('admin.dashboard.apiKeys') }}
                </p>
                <p class="text-xl font-bold text-gray-900 dark:text-white">
                  {{ stats.total_api_keys }}
                </p>
                <p class="text-xs text-green-600 dark:text-green-400">
                  {{ stats.active_api_keys }} {{ t('common.active') }}
                </p>
              </div>
            </div>
          </div>

          <!-- Service Accounts -->
          <div class="card p-4">
            <div class="flex items-center gap-3">
              <div class="rounded-lg bg-purple-100 p-2 dark:bg-purple-900/30">
                <Icon name="server" size="md" class="text-purple-600 dark:text-purple-400" :stroke-width="2" />
              </div>
              <div>
                <p class="text-xs font-medium text-gray-500 dark:text-gray-400">
                  {{ t('admin.dashboard.accounts') }}
                </p>
                <p class="text-xl font-bold text-gray-900 dark:text-white">
                  {{ stats.total_accounts }}
                </p>
                <p class="text-xs">
                  <span class="text-green-600 dark:text-green-400"
                    >{{ stats.normal_accounts }} {{ t('common.active') }}</span
                  >
                  <span v-if="stats.error_accounts > 0" class="ml-1 text-red-500"
                    >{{ stats.error_accounts }} {{ t('common.error') }}</span
                  >
                </p>
              </div>
            </div>
          </div>

          <!-- Today Requests -->
          <div class="card p-4">
            <div class="flex items-center gap-3">
              <div class="rounded-lg bg-green-100 p-2 dark:bg-green-900/30">
                <Icon name="chart" size="md" class="text-green-600 dark:text-green-400" :stroke-width="2" />
              </div>
              <div>
                <p class="text-xs font-medium text-gray-500 dark:text-gray-400">
                  {{ t('admin.dashboard.todayRequests') }}
                </p>
                <p class="text-xl font-bold text-gray-900 dark:text-white">
                  {{ stats.today_requests }}
                </p>
                <p class="text-xs text-gray-500 dark:text-gray-400">
                  {{ t('common.total') }}: {{ formatNumber(stats.total_requests) }}
                </p>
              </div>
            </div>
          </div>

          <!-- New Users Today -->
          <div class="card p-4">
            <div class="flex items-center gap-3">
              <div class="rounded-lg bg-emerald-100 p-2 dark:bg-emerald-900/30">
                <Icon name="userPlus" size="md" class="text-emerald-600 dark:text-emerald-400" :stroke-width="2" />
              </div>
              <div>
                <p class="text-xs font-medium text-gray-500 dark:text-gray-400">
                  {{ t('admin.dashboard.users') }}
                </p>
                <p class="text-xl font-bold text-emerald-600 dark:text-emerald-400">
                  +{{ stats.today_new_users }}
                </p>
                <p class="text-xs text-gray-500 dark:text-gray-400">
                  {{ t('common.total') }}: {{ formatNumber(stats.total_users) }}
                </p>
              </div>
            </div>
          </div>
        </div>

        <!-- Row 2: Token Stats -->
        <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <!-- Today Tokens -->
          <div class="card p-4">
            <div class="flex items-center gap-3">
              <div class="rounded-lg bg-amber-100 p-2 dark:bg-amber-900/30">
                <Icon name="cube" size="md" class="text-amber-600 dark:text-amber-400" :stroke-width="2" />
              </div>
              <div>
                <p class="text-xs font-medium text-gray-500 dark:text-gray-400">
                  {{ t('admin.dashboard.todayTokens') }}
                </p>
                <p class="text-xl font-bold text-gray-900 dark:text-white">
                  {{ formatTokens(stats.today_tokens) }}
                </p>
                <p class="text-xs">
                  <span
                    class="text-green-600 dark:text-green-400"
                    :title="t('admin.dashboard.actual')"
                    >${{ formatCost(stats.today_actual_cost) }}</span
                  >
                  <span class="text-gray-400 dark:text-gray-500"> / </span>
                  <span
                    class="text-orange-500 dark:text-orange-400"
                    :title="t('admin.dashboard.accountCost')"
                    >${{ formatCost(stats.today_account_cost) }}</span
                  >
                  <span class="text-gray-400 dark:text-gray-500"> / </span>
                  <span
                    class="text-gray-400 dark:text-gray-500"
                    :title="t('admin.dashboard.standard')"
                    >${{ formatCost(stats.today_cost) }}</span
                  >
                </p>
              </div>
            </div>
          </div>

          <!-- Total Tokens -->
          <div class="card p-4">
            <div class="flex items-center gap-3">
              <div class="rounded-lg bg-indigo-100 p-2 dark:bg-indigo-900/30">
                <Icon name="database" size="md" class="text-indigo-600 dark:text-indigo-400" :stroke-width="2" />
              </div>
              <div>
                <p class="text-xs font-medium text-gray-500 dark:text-gray-400">
                  {{ t('admin.dashboard.totalTokens') }}
                </p>
                <p class="text-xl font-bold text-gray-900 dark:text-white">
                  {{ formatTokens(stats.total_tokens) }}
                </p>
                <p class="text-xs">
                  <span
                    class="text-green-600 dark:text-green-400"
                    :title="t('admin.dashboard.actual')"
                    >${{ formatCost(stats.total_actual_cost) }}</span
                  >
                  <span class="text-gray-400 dark:text-gray-500"> / </span>
                  <span
                    class="text-orange-500 dark:text-orange-400"
                    :title="t('admin.dashboard.accountCost')"
                    >${{ formatCost(stats.total_account_cost) }}</span
                  >
                  <span class="text-gray-400 dark:text-gray-500"> / </span>
                  <span
                    class="text-gray-400 dark:text-gray-500"
                    :title="t('admin.dashboard.standard')"
                    >${{ formatCost(stats.total_cost) }}</span
                  >
                </p>
              </div>
            </div>
          </div>

          <!-- Performance (RPM/TPM) -->
          <div class="card p-4">
            <div class="flex items-center gap-3">
              <div class="rounded-lg bg-violet-100 p-2 dark:bg-violet-900/30">
                <Icon name="bolt" size="md" class="text-violet-600 dark:text-violet-400" :stroke-width="2" />
              </div>
              <div class="flex-1">
                <p class="text-xs font-medium text-gray-500 dark:text-gray-400">
                  {{ t('admin.dashboard.performance') }}
                </p>
                <div class="flex items-baseline gap-2">
                  <p class="text-xl font-bold text-gray-900 dark:text-white">
                    {{ formatTokens(stats.rpm) }}
                  </p>
                  <span class="text-xs text-gray-500 dark:text-gray-400">RPM</span>
                </div>
                <div class="flex items-baseline gap-2">
                  <p class="text-sm font-semibold text-violet-600 dark:text-violet-400">
                    {{ formatTokens(stats.tpm) }}
                  </p>
                  <span class="text-xs text-gray-500 dark:text-gray-400">TPM</span>
                </div>
              </div>
            </div>
          </div>

          <!-- Avg Response Time -->
          <div class="card p-4">
            <div class="flex items-center gap-3">
              <div class="rounded-lg bg-rose-100 p-2 dark:bg-rose-900/30">
                <Icon name="clock" size="md" class="text-rose-600 dark:text-rose-400" :stroke-width="2" />
              </div>
              <div>
                <p class="text-xs font-medium text-gray-500 dark:text-gray-400">
                  {{ t('admin.dashboard.avgResponse') }}
                </p>
                <p class="text-xl font-bold text-gray-900 dark:text-white">
                  {{ formatDuration(stats.average_duration_ms) }}
                </p>
                <p class="text-xs text-gray-500 dark:text-gray-400">
                  {{ stats.active_users }} {{ t('admin.dashboard.activeUsers') }}
                </p>
              </div>
            </div>
          </div>
        </div>

        <!-- Row 3: observable prompt-cache performance by group -->
        <div class="card p-4" data-testid="prompt-cache-card">
          <div class="flex flex-col gap-4 lg:flex-row lg:items-start">
            <div class="self-start rounded-lg bg-cyan-100 p-2 dark:bg-cyan-900/30">
              <Icon
                name="bolt"
                size="md"
                class="text-cyan-600 dark:text-cyan-400"
                :stroke-width="2"
              />
            </div>
            <div class="min-w-0 flex-1 lg:grid lg:grid-cols-[minmax(12rem,0.7fr)_minmax(24rem,1.3fr)] lg:gap-8">
              <div>
                <div class="flex flex-wrap items-baseline justify-between gap-2">
                  <p class="text-xs font-medium text-gray-500 dark:text-gray-400">
                    {{ t('admin.dashboard.promptCacheObservableHitRate') }}
                  </p>
                  <p class="text-[11px] text-gray-400 dark:text-gray-500">
                    {{ t('admin.dashboard.promptCacheSelectedWindow') }}
                  </p>
                </div>
                <p
                  v-if="promptCacheOverview.rate !== null"
                  class="mt-2 text-2xl font-bold text-cyan-600 dark:text-cyan-400"
                  data-testid="prompt-cache-rate"
                >
                  {{ formatPercent(promptCacheOverview.rate) }}
                </p>
                <p
                  v-else-if="promptCacheOverview.hasUnavailableTelemetry"
                  class="mt-2 text-lg font-semibold text-gray-700 dark:text-gray-200"
                  data-testid="prompt-cache-status"
                >
                  {{ t('admin.dashboard.promptCacheUnavailable') }}
                </p>
                <p v-else class="mt-2 text-2xl font-bold text-gray-400">—</p>
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                  {{ t('admin.dashboard.promptCacheHitRateHint') }}
                </p>
              </div>

              <div v-if="promptCacheOverview.groups.length" class="mt-4 min-w-0 lg:mt-0">
                <button
                  v-for="row in promptCacheOverview.groups"
                  :key="row.group.group_id"
                  type="button"
                  class="grid w-full grid-cols-[minmax(0,1fr)_auto_auto_auto] items-center gap-2 border-b border-gray-100 px-2 py-2 text-left last:border-b-0 hover:bg-gray-50 dark:border-dark-700 dark:hover:bg-dark-800/50"
                  data-testid="prompt-cache-group-row"
                  :data-group-id="row.group.group_id"
                  @click="goToGroupUsage(row.group.group_id)"
                >
                  <span class="min-w-0 truncate text-sm font-medium text-gray-900 dark:text-white">
                    {{ row.group.group_name || t('admin.dashboard.noGroup') }}
                  </span>
                  <span class="whitespace-nowrap text-xs text-gray-500 dark:text-gray-400">
                    {{ t('admin.dashboard.cacheReadTokens') }} {{ formatTokens(row.cacheReadTokens) }}
                  </span>
                  <span
                    v-if="row.rate !== null"
                    class="min-w-14 whitespace-nowrap text-right text-sm font-semibold text-gray-900 dark:text-white"
                    data-testid="prompt-cache-rate"
                  >
                    {{ formatPercent(row.rate) }}
                  </span>
                  <span
                    v-else
                    class="whitespace-nowrap text-xs font-medium text-gray-500 dark:text-gray-400"
                    data-testid="prompt-cache-status"
                  >
                    {{ t('admin.dashboard.promptCacheUnavailable') }}
                  </span>
                  <Icon name="chevronRight" size="xs" class="text-gray-400" />
                  <span
                    v-if="row.partiallyObservable"
                    class="col-start-2 col-span-3 text-right text-[11px] text-amber-600 dark:text-amber-400"
                    data-testid="prompt-cache-status"
                  >
                    {{ t('admin.dashboard.promptCachePartiallyObservable') }}
                  </span>
                </button>
              </div>
              <div v-else class="mt-4 text-sm text-gray-500 dark:text-gray-400 lg:mt-0">
                {{ t('admin.dashboard.promptCacheNoTraffic') }}
              </div>
            </div>
          </div>
        </div>

        <!-- Quick Actions -->
        <div class="card p-4">
          <div class="mb-3 flex items-center justify-between">
            <h2 class="text-sm font-semibold text-gray-900 dark:text-white">
              {{ t('admin.dashboard.quickActions') }}
            </h2>
          </div>
          <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
            <button
              v-if="canUseBatchImage"
              type="button"
              class="group flex items-center gap-3 rounded-lg bg-gray-50 p-3 text-left transition-colors hover:bg-sky-50 dark:bg-dark-800/50 dark:hover:bg-sky-900/20"
              @click="router.push('/batch-image')"
            >
              <span class="flex h-10 w-10 flex-shrink-0 items-center justify-center rounded-lg bg-sky-100 text-sky-600 dark:bg-sky-900/30 dark:text-sky-400">
                <Icon name="sparkles" size="md" :stroke-width="2" />
              </span>
              <span class="min-w-0 flex-1">
                <span class="block text-sm font-medium text-gray-900 dark:text-white">
                  {{ t('admin.dashboard.batchImage') }}
                </span>
                <span class="block text-xs text-gray-500 dark:text-gray-400">
                  {{ t('admin.dashboard.batchImageDesc') }}
                </span>
              </span>
              <Icon name="chevronRight" size="sm" class="text-gray-400 group-hover:text-sky-500" />
            </button>
            <button
              type="button"
              class="group flex items-center gap-3 rounded-lg bg-gray-50 p-3 text-left transition-colors hover:bg-emerald-50 dark:bg-dark-800/50 dark:hover:bg-emerald-900/20"
              @click="router.push('/admin/groups')"
            >
              <span class="flex h-10 w-10 flex-shrink-0 items-center justify-center rounded-lg bg-emerald-100 text-emerald-600 dark:bg-emerald-900/30 dark:text-emerald-400">
                <Icon name="grid" size="md" :stroke-width="2" />
              </span>
              <span class="min-w-0 flex-1">
                <span class="block text-sm font-medium text-gray-900 dark:text-white">
                  {{ t('admin.dashboard.groupPricing') }}
                </span>
                <span class="block text-xs text-gray-500 dark:text-gray-400">
                  {{ t('admin.dashboard.groupPricingDesc') }}
                </span>
              </span>
              <Icon name="chevronRight" size="sm" class="text-gray-400 group-hover:text-emerald-500" />
            </button>
          </div>
        </div>

        <!-- Charts Section -->
        <div class="space-y-6">
          <!-- Date Range Filter -->
          <div class="card p-4">
            <div class="flex flex-wrap items-center gap-4">
              <div class="flex items-center gap-2">
                <span class="text-sm font-medium text-gray-700 dark:text-gray-300"
                  >{{ t('admin.dashboard.timeRange') }}:</span
                >
                <DateRangePicker
                  v-model:start-date="startDate"
                  v-model:end-date="endDate"
                  @change="onDateRangeChange"
                />
              </div>
              <button @click="loadDashboardStats" :disabled="dashboardChartsLoading" class="btn btn-secondary">
                {{ t('common.refresh') }}
              </button>
              <div class="ml-auto flex items-center gap-2">
                <span class="text-sm font-medium text-gray-700 dark:text-gray-300"
                  >{{ t('admin.dashboard.granularity') }}:</span
                >
                <div class="w-28">
                  <Select
                    v-model="granularity"
                    :options="granularityOptions"
                    @change="loadChartData"
                  />
                </div>
              </div>
            </div>
          </div>

          <!-- Charts Grid -->
          <div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
            <ModelDistributionChart
              :model-stats="modelStats"
              :enable-ranking-view="true"
              :ranking-items="rankingItems"
              :ranking-total-actual-cost="rankingTotalActualCost"
              :ranking-total-requests="rankingTotalRequests"
              :ranking-total-tokens="rankingTotalTokens"
              :loading="modelStatsLoading"
              :ranking-loading="rankingLoading"
              :ranking-error="rankingError"
              :start-date="startDate"
              :end-date="endDate"
              @ranking-click="goToUserUsage"
            />
            <TokenUsageTrend :trend-data="trendData" :loading="chartsLoading" />
          </div>

          <!-- User Usage Trend (Full Width) -->
          <div class="card p-4">
            <h3 class="mb-4 text-sm font-semibold text-gray-900 dark:text-white">
              {{ t('admin.dashboard.recentUsage') }} (Top 12)
            </h3>
            <div class="h-64">
              <div v-if="userTrendLoading" class="flex h-full items-center justify-center">
                <LoadingSpinner size="md" />
              </div>
              <div v-else-if="userTrendChartData" class="flex h-full flex-col">
                <div
                  v-if="userTrendLegendItems.length > 0"
                  class="mb-3 flex flex-wrap items-center justify-center gap-x-4 gap-y-2 px-2"
                  data-test="recent-usage-legend"
                >
                  <button
                    v-for="item in userTrendLegendItems"
                    :key="item.userId"
                    type="button"
                    class="inline-flex h-7 max-w-full items-center gap-1.5 rounded px-1.5 text-xs font-medium transition hover:bg-gray-100 focus:outline-none focus:ring-2 focus:ring-primary-500/40 dark:hover:bg-dark-700"
                    :class="
                      isUserTrendHidden(item.userId)
                        ? 'text-gray-400 dark:text-gray-500'
                        : 'text-gray-600 dark:text-gray-300'
                    "
                    :aria-pressed="!isUserTrendHidden(item.userId)"
                    :title="item.name"
                    @click="toggleUserTrendDataset(item.userId)"
                  >
                    <span
                      class="h-3 w-3 flex-shrink-0 rounded-full border-2"
                      :style="{
                        borderColor: item.color,
                        backgroundColor: isUserTrendHidden(item.userId)
                          ? 'transparent'
                          : `${item.color}26`,
                      }"
                    ></span>
                    <span
                      class="max-w-[12rem] truncate"
                      :class="{ 'line-through': isUserTrendHidden(item.userId) }"
                    >
                      {{ item.name }}
                    </span>
                  </button>
                </div>
                <div class="min-h-0 flex-1">
                  <Line :data="userTrendChartData" :options="lineOptions" />
                </div>
              </div>
              <div
                v-else
                class="flex h-full items-center justify-center text-sm text-gray-500 dark:text-gray-400"
              >
                {{ t('admin.dashboard.noDataAvailable') }}
              </div>
            </div>
          </div>
        </div>
      </template>
    </div>
  </template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useAppStore } from '@/stores/app'

const { t } = useI18n()
import { adminAPI } from '@/api/admin'
import type {
  DashboardStats,
  TrendDataPoint,
  ModelStat,
  GroupStat,
  UserUsageTrendPoint,
  UserSpendingRankingItem
} from '@/types'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Icon from '@/components/icons/Icon.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import Select from '@/components/common/Select.vue'
import ModelDistributionChart from '@/components/charts/ModelDistributionChart.vue'
import TokenUsageTrend from '@/components/charts/TokenUsageTrend.vue'
import { dashboardWindowParams, rollingWindowTs } from '@/utils/dashboardWindow.tk'
import { useBatchImageAccess } from '@/composables/useBatchImageAccess'

import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Tooltip,
  Legend,
  Filler
} from 'chart.js'
import { Line } from 'vue-chartjs'

// Register Chart.js components
ChartJS.register(
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Tooltip,
  Legend,
  Filler
)

const appStore = useAppStore()
const router = useRouter()
const { canUseBatchImage, refreshBatchImageAccess } = useBatchImageAccess()
const stats = ref<DashboardStats | null>(null)
const loading = ref(false)
const chartsLoading = ref(false)
const modelStatsLoading = ref(false)
const userTrendLoading = ref(false)
const rankingLoading = ref(false)
const rankingError = ref(false)
const dashboardChartsLoading = computed(() => chartsLoading.value || modelStatsLoading.value)

// Chart data
const trendData = ref<TrendDataPoint[]>([])
const modelStats = ref<ModelStat[]>([])
const groupStats = ref<GroupStat[]>([])
const loadedDashboardRollingWindow = ref<{ start_ts: number; end_ts: number } | null>(null)
const userTrend = ref<UserUsageTrendPoint[]>([])
const rankingItems = ref<UserSpendingRankingItem[]>([])
const rankingTotalActualCost = ref(0)
const rankingTotalRequests = ref(0)
const rankingTotalTokens = ref(0)
let statsLoadSeq = 0
let chartLoadSeq = 0
let userTrendLoadSeq = 0
let rankingLoadSeq = 0
const rankingLimit = 12

// Helper function to format date in local timezone
const formatLocalDate = (date: Date): string => {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}

const getLast24HoursRangeDates = (): { start: string; end: string } => {
  const end = new Date()
  const start = new Date(end.getTime() - 24 * 60 * 60 * 1000)
  return {
    start: formatLocalDate(start),
    end: formatLocalDate(end)
  }
}

// Date range
const granularity = ref<'day' | 'hour'>('hour')
const defaultRange = getLast24HoursRangeDates()
const startDate = ref(defaultRange.start)
const endDate = ref(defaultRange.end)
// TK: tracks the active DateRangePicker preset so rolling presets can be sent
// as an absolute (timezone-independent) epoch-ms window. Defaults to
// 'last24Hours' to match the default range above. See utils/dashboardWindow.tk.ts.
const activePreset = ref<string | null>('last24Hours')

// Granularity options for Select component
const granularityOptions = computed(() => [
  { value: 'day', label: t('admin.dashboard.day') },
  { value: 'hour', label: t('admin.dashboard.hour') }
])

// Dark mode detection
const isDarkMode = computed(() => {
  return document.documentElement.classList.contains('dark')
})

// Chart colors
const chartColors = computed(() => ({
  text: isDarkMode.value ? '#e5e7eb' : '#374151',
  grid: isDarkMode.value ? '#374151' : '#e5e7eb'
}))

const userTrendColors = [
  '#3b82f6',
  '#10b981',
  '#f59e0b',
  '#ef4444',
  '#8b5cf6',
  '#ec4899',
  '#14b8a6',
  '#f97316',
  '#6366f1',
  '#84cc16',
  '#06b6d4',
  '#a855f7'
]

const hiddenUserTrendIds = ref<Set<number>>(new Set())

// Line chart options (for user trend chart)
const lineOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  interaction: {
    intersect: false,
    mode: 'index' as const
  },
  plugins: {
    legend: {
      display: false
    },
    tooltip: {
      itemSort: (a: any, b: any) => {
        const aValue = typeof a?.raw === 'number' ? a.raw : Number(a?.parsed?.y ?? 0)
        const bValue = typeof b?.raw === 'number' ? b.raw : Number(b?.parsed?.y ?? 0)
        return bValue - aValue
      },
      callbacks: {
        label: (context: any) => {
          return `${context.dataset.label}: ${formatTokens(context.raw)}`
        }
      }
    }
  },
  scales: {
    x: {
      grid: {
        color: chartColors.value.grid
      },
      ticks: {
        color: chartColors.value.text,
        font: {
          size: 10
        }
      }
    },
    y: {
      grid: {
        color: chartColors.value.grid
      },
      ticks: {
        color: chartColors.value.text,
        font: {
          size: 10
        },
        callback: (value: string | number) => formatTokens(Number(value))
      }
    }
  }
}))

const userTrendSeries = computed(() => {
  const getDisplayName = (point: UserUsageTrendPoint): string => {
    const username = point.username?.trim()
    if (username) {
      return username
    }

    const email = point.email?.trim()
    if (email) {
      return email
    }

    return t('admin.redeem.userPrefix', { id: point.user_id })
  }

  // Group by user_id to avoid merging different users with the same display name
  const userGroups = new Map<number, { name: string; data: Map<string, number> }>()
  const allDates = new Set<string>()

  userTrend.value.forEach((point) => {
    allDates.add(point.date)
    const key = point.user_id
    if (!userGroups.has(key)) {
      userGroups.set(key, { name: getDisplayName(point), data: new Map() })
    }
    userGroups.get(key)!.data.set(point.date, point.tokens)
  })

  const sortedDates = Array.from(allDates).sort()

  const series = Array.from(userGroups.entries()).map(([userId, group], idx) => ({
    userId,
    name: group.name,
    color: userTrendColors[idx % userTrendColors.length],
    data: sortedDates.map((date) => group.data.get(date) || 0)
  }))

  return {
    labels: sortedDates,
    series
  }
})

const userTrendLegendItems = computed(() => userTrendSeries.value.series)

const isUserTrendHidden = (userId: number): boolean => hiddenUserTrendIds.value.has(userId)

const toggleUserTrendDataset = (userId: number) => {
  const next = new Set(hiddenUserTrendIds.value)
  if (next.has(userId)) {
    next.delete(userId)
  } else {
    next.add(userId)
  }
  hiddenUserTrendIds.value = next
}

// User trend chart data
const userTrendChartData = computed(() => {
  const { labels, series } = userTrendSeries.value
  if (!series.length) return null

  const datasets = series.map((group) => ({
    userId: group.userId,
    label: group.name,
    data: group.data,
    borderColor: group.color,
    backgroundColor: `${group.color}20`,
    fill: false,
    hidden: isUserTrendHidden(group.userId),
    tension: 0.3
  }))

  return {
    labels,
    datasets
  }
})

// Format helpers
const formatTokens = (value: number | undefined): string => {
  if (value === undefined || value === null) return '0'
  if (value >= 1_000_000_000) {
    return `${(value / 1_000_000_000).toFixed(2)}B`
  } else if (value >= 1_000_000) {
    return `${(value / 1_000_000).toFixed(2)}M`
  } else if (value >= 1_000) {
    return `${(value / 1_000).toFixed(2)}K`
  }
  return value.toLocaleString()
}

const toFiniteNumber = (value: unknown): number => {
  const numberValue = Number(value)
  return Number.isFinite(numberValue) ? numberValue : 0
}

const formatNumber = (value: number | null | undefined): string => {
  return toFiniteNumber(value).toLocaleString()
}

const formatCost = (value: number | null | undefined): string => {
  const safeValue = toFiniteNumber(value)
  if (safeValue >= 1000) {
    return (safeValue / 1000).toFixed(2) + 'K'
  } else if (safeValue >= 1) {
    return safeValue.toFixed(2)
  } else if (safeValue >= 0.01) {
    return safeValue.toFixed(3)
  }
  return safeValue.toFixed(4)
}

const formatDuration = (ms: number): string => {
  if (ms >= 1000) {
    return `${(ms / 1000).toFixed(2)}s`
  }
  return `${Math.round(ms)}ms`
}

const promptCacheOverview = computed(() => {
  const rows = groupStats.value.map((group) => {
    const inputTokens = Math.max(0, toFiniteNumber(group.input_tokens))
    const unavailableInputTokens = Math.min(
      inputTokens,
      Math.max(0, toFiniteNumber(group.cache_telemetry_unavailable_input_tokens))
    )
    const cacheCreationTokens = Math.max(0, toFiniteNumber(group.cache_creation_tokens))
    const cacheReadTokens = Math.max(0, toFiniteNumber(group.cache_read_tokens))
    const observableDenominator = inputTokens - unavailableInputTokens + cacheCreationTokens + cacheReadTokens
    return {
      group,
      cacheReadTokens,
      unavailableInputTokens,
      observableDenominator,
      impactTokens: inputTokens + cacheCreationTokens + cacheReadTokens,
      rate: observableDenominator > 0 ? cacheReadTokens / observableDenominator : null,
      partiallyObservable: unavailableInputTokens > 0 && observableDenominator > 0
    }
  })

  const observableDenominator = rows.reduce((sum, row) => sum + row.observableDenominator, 0)
  const cacheReadTokens = rows.reduce((sum, row) => sum + row.cacheReadTokens, 0)
  return {
    rate: observableDenominator > 0 ? cacheReadTokens / observableDenominator : null,
    hasUnavailableTelemetry: rows.some((row) => row.unavailableInputTokens > 0),
    groups: rows
      .filter((row) => row.impactTokens > 0)
      .sort((a, b) => b.impactTokens - a.impactTokens || a.group.group_id - b.group.group_id)
      .slice(0, 5)
  }
})

const formatPercent = (rate: number | null): string => {
  if (rate === null) return '—'
  return `${(rate * 100).toFixed(1)}%`
}

const usageDrilldownQuery = (filter: { user_id?: string; group_id?: string }) => ({
  ...filter,
  start_date: startDate.value,
  end_date: endDate.value,
  // Usage consumes absolute rolling instants, not Dashboard's server-TZ range token.
  // Reuse the window that produced the visible snapshot so the drilldown is exact.
  ...(loadedDashboardRollingWindow.value ?? rollingWindowTs(activePreset.value) ?? {})
})

const goToUserUsage = (item: UserSpendingRankingItem) => {
  void router.push({
    path: '/admin/usage',
    query: usageDrilldownQuery({ user_id: String(item.user_id) })
  })
}

const goToGroupUsage = (groupId: number) => {
  void router.push({
    path: '/admin/usage',
    query: usageDrilldownQuery({ group_id: String(groupId) })
  })
}

// Date range change handler
const onDateRangeChange = (range: {
  startDate: string
  endDate: string
  preset: string | null
}) => {
  // TK: remember the preset so rolling windows go out as absolute epoch-ms.
  activePreset.value = range.preset
  // Auto-select granularity based on date range
  const start = new Date(range.startDate)
  const end = new Date(range.endDate)
  const daysDiff = Math.ceil((end.getTime() - start.getTime()) / (1000 * 60 * 60 * 24))

  // If range is 1 day, use hourly granularity
  if (daysDiff <= 1) {
    granularity.value = 'hour'
  } else {
    granularity.value = 'day'
  }

  loadChartData()
}

// Load data — single SnapshotV2 call with all facets
const loadAllDashboardData = async () => {
  const currentStatsSeq = ++statsLoadSeq
  const currentChartSeq = ++chartLoadSeq
  const currentUserTrendSeq = ++userTrendLoadSeq
  if (!stats.value) loading.value = true
  chartsLoading.value = true
  modelStatsLoading.value = true
  userTrendLoading.value = true
  const rollingWindow = rollingWindowTs(activePreset.value)
  try {
    const response = await adminAPI.dashboard.getSnapshotV2({
      start_date: startDate.value,
      end_date: endDate.value,
      ...(rollingWindow ?? dashboardWindowParams(activePreset.value)),
      granularity: granularity.value,
      include_stats: true,
      include_trend: true,
      include_model_stats: true,
      include_group_stats: true,
      include_users_trend: true,
      users_trend_limit: 12
    })
    if (currentStatsSeq === statsLoadSeq && response.stats) {
      stats.value = response.stats
    }
    if (currentChartSeq === chartLoadSeq) {
      trendData.value = response.trend || []
      modelStats.value = response.models || []
      groupStats.value = response.groups || []
      loadedDashboardRollingWindow.value = rollingWindow
    }
    if (currentUserTrendSeq === userTrendLoadSeq) {
      userTrend.value = response.users_trend || []
    }
  } catch (error) {
    if (currentStatsSeq === statsLoadSeq) {
      appStore.showError(t('admin.dashboard.failedToLoad'))
    }
    if (currentChartSeq === chartLoadSeq) {
      trendData.value = []
      modelStats.value = []
      groupStats.value = []
      loadedDashboardRollingWindow.value = null
    }
    if (currentUserTrendSeq === userTrendLoadSeq) {
      userTrend.value = []
    }
    console.error('Error loading dashboard snapshot:', error)
  } finally {
    if (currentStatsSeq === statsLoadSeq) loading.value = false
    if (currentChartSeq === chartLoadSeq) {
      chartsLoading.value = false
      modelStatsLoading.value = false
    }
    if (currentUserTrendSeq === userTrendLoadSeq) userTrendLoading.value = false
  }
}

const loadUserSpendingRanking = async () => {
  const currentSeq = ++rankingLoadSeq
  rankingLoading.value = true
  rankingError.value = false
  try {
    const response = await adminAPI.dashboard.getUserSpendingRanking({
      start_date: startDate.value,
      end_date: endDate.value,
      ...(dashboardWindowParams(activePreset.value)),
      limit: rankingLimit
    })
    if (currentSeq !== rankingLoadSeq) return
    rankingItems.value = response.ranking || []
    rankingTotalActualCost.value = response.total_actual_cost || 0
    rankingTotalRequests.value = response.total_requests || 0
    rankingTotalTokens.value = response.total_tokens || 0
  } catch (error) {
    if (currentSeq !== rankingLoadSeq) return
    console.error('Error loading user spending ranking:', error)
    rankingItems.value = []
    rankingTotalActualCost.value = 0
    rankingTotalRequests.value = 0
    rankingTotalTokens.value = 0
    rankingError.value = true
  } finally {
    if (currentSeq === rankingLoadSeq) {
      rankingLoading.value = false
    }
  }
}

const loadDashboardStats = async () => {
  await Promise.all([
    loadAllDashboardData(),
    loadUserSpendingRanking()
  ])
}

const loadChartData = async () => {
  await Promise.all([
    loadAllDashboardData(),
    loadUserSpendingRanking()
  ])
}

onMounted(() => {
  void refreshBatchImageAccess()
  void Promise.all([
    loadAllDashboardData(),
    loadUserSpendingRanking()
  ])
})
</script>

<style scoped>
</style>

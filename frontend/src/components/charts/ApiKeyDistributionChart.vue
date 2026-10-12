<template>
  <div class="card p-4">
    <div class="mb-4 flex items-center justify-between gap-3">
      <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
        {{ t('usage.apiKeyDistribution') }}
      </h3>
      <div
        v-if="showMetricToggle"
        class="inline-flex rounded-lg border border-gray-200 bg-gray-50 p-0.5 dark:border-dark-700 dark:bg-dark-800"
      >
        <button
          type="button"
          class="rounded-md px-2.5 py-1 text-xs font-medium transition-colors"
          :class="metric === 'tokens'
            ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-700 dark:text-white'
            : 'text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200'"
          @click="emit('update:metric', 'tokens')"
        >
          {{ t('admin.dashboard.metricTokens') }}
        </button>
        <button
          type="button"
          class="rounded-md px-2.5 py-1 text-xs font-medium transition-colors"
          :class="metric === 'actual_cost'
            ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-700 dark:text-white'
            : 'text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200'"
          @click="emit('update:metric', 'actual_cost')"
        >
          {{ t('admin.dashboard.metricActualCost') }}
        </button>
      </div>
    </div>

    <div v-if="loading" class="flex h-48 items-center justify-center">
      <LoadingSpinner />
    </div>
    <div v-else-if="displayApiKeyStats.length > 0 && chartData" class="flex flex-col items-center gap-4 sm:flex-row sm:gap-6">
      <div class="h-48 w-48 shrink-0">
        <Doughnut :data="chartData" :options="doughnutOptions" />
      </div>
      <div class="max-h-48 w-full min-w-0 flex-1 overflow-auto">
        <table class="w-full text-xs">
          <thead>
            <tr class="text-gray-500 dark:text-gray-400">
              <th class="pb-2 text-left">{{ t('usage.apiKeyFilter') }}</th>
              <th class="pb-2 text-right">{{ t('admin.dashboard.requests') }}</th>
              <th class="pb-2 text-right">{{ t('admin.dashboard.tokens') }}</th>
              <th class="pb-2 text-right">{{ t('usage.cost') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="key in displayApiKeyStats"
              :key="key.api_key_id"
              class="border-t border-gray-100 dark:border-dark-700"
            >
              <td
                class="max-w-[140px] truncate py-1.5 font-medium text-gray-900 dark:text-white"
                :title="keyLabel(key)"
              >
                {{ keyLabel(key) }}
              </td>
              <td class="py-1.5 text-right text-gray-600 dark:text-gray-400">
                {{ formatNumber(key.requests) }}
              </td>
              <td class="py-1.5 text-right text-gray-600 dark:text-gray-400">
                {{ formatTokens(key.total_tokens) }}
              </td>
              <td class="py-1.5 text-right text-green-600 dark:text-green-400">
                ${{ formatCost(key.actual_cost) }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
    <div
      v-else
      class="flex h-48 items-center justify-center text-sm text-gray-500 dark:text-gray-400"
    >
      {{ t('admin.dashboard.noDataAvailable') }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Chart as ChartJS, ArcElement, Tooltip, Legend } from 'chart.js'
import { Doughnut } from 'vue-chartjs'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import type { ApiKeyStat } from '@/types'

ChartJS.register(ArcElement, Tooltip, Legend)

const { t } = useI18n()

type DistributionMetric = 'tokens' | 'actual_cost'

const props = withDefaults(defineProps<{
  apiKeyStats: ApiKeyStat[]
  loading?: boolean
  metric?: DistributionMetric
  showMetricToggle?: boolean
}>(), {
  loading: false,
  metric: 'tokens',
  showMetricToggle: true,
})

const emit = defineEmits<{
  'update:metric': [value: DistributionMetric]
}>()

const chartColors = [
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
]

const keyLabel = (key: ApiKeyStat): string =>
  key.api_key_name?.trim() || (key.api_key_id > 0 ? `#${key.api_key_id}` : t('usage.unknownApiKey'))

const displayApiKeyStats = computed(() => {
  if (!props.apiKeyStats?.length) return []
  const metricKey = props.metric === 'actual_cost' ? 'actual_cost' : 'total_tokens'
  return [...props.apiKeyStats].sort((a, b) => toFiniteNumber(b[metricKey]) - toFiniteNumber(a[metricKey]))
})

const chartData = computed(() => {
  if (!displayApiKeyStats.value.length) return null
  return {
    labels: displayApiKeyStats.value.map((key) => keyLabel(key)),
    datasets: [
      {
        data: displayApiKeyStats.value.map((key) =>
          toFiniteNumber(props.metric === 'actual_cost' ? key.actual_cost : key.total_tokens)
        ),
        backgroundColor: chartColors.slice(0, displayApiKeyStats.value.length),
        borderWidth: 0,
      },
    ],
  }
})

const doughnutOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: { display: false },
    tooltip: {
      callbacks: {
        label: (context: { label?: string; parsed?: number }) => {
          const value = toFiniteNumber(context.parsed)
          if (props.metric === 'actual_cost') {
            return `${context.label}: $${formatCost(value)}`
          }
          return `${context.label}: ${formatTokens(value)}`
        },
      },
    },
  },
}))

const toFiniteNumber = (value: unknown): number => {
  const n = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(n) ? n : 0
}

const formatNumber = (value: number): string =>
  new Intl.NumberFormat().format(toFiniteNumber(value))

const formatTokens = (value: number): string => formatNumber(value)

const formatCost = (value: number): string => toFiniteNumber(value).toFixed(4)
</script>

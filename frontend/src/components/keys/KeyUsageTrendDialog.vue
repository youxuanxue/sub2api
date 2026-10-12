<template>
  <BaseDialog
    :show="show"
    :title="title"
    width="wide"
    @close="emit('close')"
  >
    <div v-if="loading" class="flex h-56 items-center justify-center">
      <LoadingSpinner />
    </div>
    <div v-else-if="chartData" class="h-56">
      <Line :data="chartData" :options="lineOptions as any" />
    </div>
    <div
      v-else
      class="flex h-56 items-center justify-center text-sm text-gray-500 dark:text-gray-400"
    >
      {{ t('admin.dashboard.noDataAvailable') }}
    </div>
    <p v-if="rangeLabel" class="mt-3 text-xs text-gray-500 dark:text-gray-400">
      {{ rangeLabel }}
    </p>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Tooltip,
  Filler,
} from 'chart.js'
import { Line } from 'vue-chartjs'
import BaseDialog from '@/components/common/BaseDialog.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import { usageAPI } from '@/api'
import type { ApiKeyDailyUsagePoint } from '@/api/usage'

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Tooltip, Filler)

const props = defineProps<{
  show: boolean
  apiKeyId: number | null
  apiKeyName: string
  startDate: string
  endDate: string
  timezone?: string
}>()

const emit = defineEmits<{
  close: []
}>()

const { t } = useI18n()

const loading = ref(false)
const points = ref<ApiKeyDailyUsagePoint[]>([])

const title = computed(() =>
  t('keys.usageTrendTitle', { name: props.apiKeyName || `#${props.apiKeyId ?? ''}` }),
)

const rangeLabel = computed(() => {
  if (!props.startDate || !props.endDate) return ''
  return t('keys.usageTrendRange', { start: props.startDate, end: props.endDate })
})

const isDarkMode = computed(() => document.documentElement.classList.contains('dark'))

const chartData = computed(() => {
  if (!points.value.length) return null
  return {
    labels: points.value.map((p) => p.date),
    datasets: [
      {
        label: t('usage.cost'),
        data: points.value.map((p) => p.actual_cost),
        borderColor: '#10b981',
        backgroundColor: '#10b98133',
        fill: true,
        tension: 0.3,
      },
    ],
  }
})

const lineOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: { display: false },
    tooltip: {
      callbacks: {
        label: (ctx: { parsed?: { y?: number } }) => {
          const value = typeof ctx.parsed?.y === 'number' ? ctx.parsed.y : 0
          return `$${value.toFixed(4)}`
        },
      },
    },
  },
  scales: {
    x: {
      ticks: { color: isDarkMode.value ? '#e5e7eb' : '#374151', font: { size: 10 } },
      grid: { color: isDarkMode.value ? '#374151' : '#e5e7eb' },
    },
    y: {
      ticks: {
        color: isDarkMode.value ? '#e5e7eb' : '#374151',
        font: { size: 10 },
        callback: (value: string | number) => `$${Number(value).toFixed(2)}`,
      },
      grid: { color: isDarkMode.value ? '#374151' : '#e5e7eb' },
    },
  },
}))

const loadTrend = async () => {
  if (!props.show || !props.apiKeyId || !props.startDate || !props.endDate) {
    points.value = []
    return
  }
  loading.value = true
  try {
    const response = await usageAPI.getMyApiKeyDailyUsage(props.apiKeyId, {
      start_date: props.startDate,
      end_date: props.endDate,
      timezone: props.timezone,
    })
    points.value = response.items || []
  } catch (error) {
    console.error('Failed to load key usage trend:', error)
    points.value = []
  } finally {
    loading.value = false
  }
}

watch(
  () => [props.show, props.apiKeyId, props.startDate, props.endDate, props.timezone] as const,
  () => {
    void loadTrend()
  },
  { immediate: true },
)
</script>

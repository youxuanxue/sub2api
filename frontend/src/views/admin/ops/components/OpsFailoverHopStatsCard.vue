<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import { opsAPI, type OpsFailoverHopStatsResponse, type OpsFailoverHopStatsTimeRange } from '@/api/admin/ops'
import { formatNumber } from '@/utils/format'

interface Props {
  platformFilter?: string
  groupIdFilter?: number | null
  refreshToken: number
}

const props = withDefaults(defineProps<Props>(), {
  platformFilter: '',
  groupIdFilter: null
})

const { t } = useI18n()

const loading = ref(false)
const errorMessage = ref('')
const response = ref<OpsFailoverHopStatsResponse | null>(null)

const timeRange = ref<OpsFailoverHopStatsTimeRange>('1d')
const topN = ref<number>(10)

const items = computed(() => response.value?.items ?? [])
const total = computed(() => response.value?.total ?? 0)

const timeRangeOptions = computed(() => [
  { value: '30m', label: t('admin.ops.timeRange.30m') },
  { value: '1h', label: t('admin.ops.timeRange.1h') },
  { value: '1d', label: t('admin.ops.timeRange.1d') },
  { value: '15d', label: t('admin.ops.timeRange.15d') },
  { value: '30d', label: t('admin.ops.timeRange.30d') }
])

const topNOptions = computed(() => [
  { value: 10, label: 'Top 10' },
  { value: 20, label: 'Top 20' },
  { value: 50, label: 'Top 50' },
  { value: 100, label: 'Top 100' }
])

function formatRate(v?: number | null): string {
  if (typeof v !== 'number' || !Number.isFinite(v)) return '-'
  return v.toFixed(2)
}

function formatInt(v?: number | null): string {
  if (typeof v !== 'number' || !Number.isFinite(v)) return '-'
  return formatNumber(Math.round(v))
}

function buildParams() {
  return {
    time_range: timeRange.value,
    top_n: topN.value,
    platform: props.platformFilter || undefined,
    group_id: typeof props.groupIdFilter === 'number' && props.groupIdFilter > 0 ? props.groupIdFilter : undefined
  }
}

async function loadData() {
  loading.value = true
  errorMessage.value = ''
  try {
    response.value = await opsAPI.getFailoverHopStats(buildParams())
  } catch (err: any) {
    console.error('[OpsFailoverHopStatsCard] Failed to load data', err)
    response.value = null
    errorMessage.value = err?.message || t('admin.ops.failoverHopStats.failedToLoad')
  } finally {
    loading.value = false
  }
}

watch(
  () => ({
    timeRange: timeRange.value,
    topN: topN.value,
    platform: props.platformFilter,
    groupId: props.groupIdFilter,
    refreshToken: props.refreshToken
  }),
  () => {
    void loadData()
  },
  { immediate: true }
)
</script>

<template>
  <section class="card p-4 md:p-5">
    <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
      <div>
        <h3 class="text-sm font-bold text-gray-900 dark:text-white">
          {{ t('admin.ops.failoverHopStats.title') }}
        </h3>
        <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.ops.failoverHopStats.hint') }}
        </p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <div class="w-36">
          <Select v-model="timeRange" :options="timeRangeOptions" />
        </div>
        <div class="w-28">
          <Select v-model="topN" :options="topNOptions" />
        </div>
      </div>
    </div>

    <div v-if="errorMessage" class="mb-4 rounded-lg bg-red-50 px-3 py-2 text-xs text-red-600 dark:bg-red-900/20 dark:text-red-400">
      {{ errorMessage }}
    </div>

    <div v-if="loading" class="py-8 text-center text-sm text-gray-500 dark:text-gray-400">
      {{ t('admin.ops.loadingText') }}
    </div>

    <EmptyState
      v-else-if="items.length === 0"
      :title="t('common.noData')"
      :description="t('admin.ops.failoverHopStats.empty')"
    />

    <div v-else class="space-y-3">
      <div class="overflow-hidden rounded-xl border border-gray-200 dark:border-dark-700">
        <div class="max-h-[420px] overflow-auto">
          <table class="min-w-full text-left text-xs md:text-sm">
            <thead class="sticky top-0 z-10 bg-white dark:bg-dark-800">
              <tr class="border-b border-gray-200 text-gray-500 dark:border-dark-700 dark:text-gray-400">
                <th class="px-2 py-2 font-semibold">{{ t('admin.ops.failoverHopStats.table.account') }}</th>
                <th class="px-2 py-2 font-semibold">{{ t('admin.ops.failoverHopStats.table.platform') }}</th>
                <th class="px-2 py-2 font-semibold">{{ t('admin.ops.failoverHopStats.table.recoveredCount') }}</th>
                <th class="px-2 py-2 font-semibold">{{ t('admin.ops.failoverHopStats.table.totalFailoverHops') }}</th>
                <th class="px-2 py-2 font-semibold">{{ t('admin.ops.failoverHopStats.table.totalWastedAttempts') }}</th>
                <th class="px-2 py-2 font-semibold">{{ t('admin.ops.failoverHopStats.table.avgHopsPerRecovered') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="row in items"
                :key="row.account_id"
                class="border-b border-gray-100 text-gray-700 last:border-b-0 dark:border-dark-800 dark:text-gray-200"
              >
                <td class="px-2 py-2 font-medium">{{ row.account_name }}</td>
                <td class="px-2 py-2">{{ row.platform }}</td>
                <td class="px-2 py-2">{{ formatInt(row.recovered_count) }}</td>
                <td class="px-2 py-2">{{ formatInt(row.total_failover_hops) }}</td>
                <td class="px-2 py-2">{{ formatInt(row.total_wasted_attempts) }}</td>
                <td class="px-2 py-2">{{ formatRate(row.avg_failover_hops_per_recovered) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
      <div class="mt-3 text-xs text-gray-500 dark:text-gray-400">
        {{ t('admin.ops.failoverHopStats.totalAccounts', { total }) }}
      </div>
    </div>
  </section>
</template>

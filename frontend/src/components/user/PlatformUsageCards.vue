<template>
  <div
    v-if="cards.length > 0"
    class="card p-4"
    data-testid="platform-usage-breakdown"
  >
    <div class="mb-3 flex items-center justify-between">
      <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
        {{ t('dashboard.platformBreakdown') }}
      </h3>
      <span class="text-xs text-gray-500 dark:text-gray-400">
        {{ t('dashboard.platformCount', { count: publicStats.length }) }}
      </span>
    </div>
    <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
      <div
        v-for="item in cards"
        :key="item.platform"
        :class="[
          'rounded-lg border p-3',
          item.isOther
            ? 'border-dashed border-gray-300 bg-gray-50 dark:border-dark-500 dark:bg-dark-700/30'
            : 'border-gray-200 dark:border-dark-600'
        ]"
      >
        <div class="flex items-center justify-between">
          <span class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ item.isOther ? t('dashboard.platformOther') : getPublicPlatformLabel(item.platform) }}
          </span>
          <span class="font-mono text-sm text-purple-600 dark:text-purple-400" :title="t('dashboard.actual')">
            ${{ formatCost(item.total_actual_cost) }}
          </span>
        </div>
        <div class="mt-2 space-y-1 text-xs">
          <div class="flex items-center justify-between">
            <span class="text-gray-500 dark:text-gray-400">{{ t('dashboard.todayCost') }}</span>
            <span class="font-mono text-gray-900 dark:text-white">${{ formatCost(item.today_actual_cost) }}</span>
          </div>
          <div class="flex items-center justify-between">
            <span class="text-gray-500 dark:text-gray-400">{{ t('dashboard.requests') }}</span>
            <span class="font-mono text-gray-700 dark:text-gray-300">
              {{ item.total_requests > 0 ? formatNumber(item.total_requests) : '-' }}
            </span>
          </div>
          <div class="flex items-center justify-between">
            <span class="text-gray-500 dark:text-gray-400">{{ t('dashboard.tokens') }}</span>
            <span class="font-mono text-gray-700 dark:text-gray-300">
              {{ item.total_tokens > 0 ? formatTokens(item.total_tokens) : '-' }}
            </span>
          </div>
        </div>

        <div v-if="hasAnyLimit(item.quota) && !item.isOther" class="mt-3 space-y-1.5 border-t border-gray-200 pt-2 dark:border-dark-700">
          <p class="text-[10px] uppercase tracking-wide text-gray-400">
            {{ t('dashboard.platformQuota.title') }}
          </p>
          <template v-for="window in (['daily', 'weekly', 'monthly'] as const)" :key="window">
            <div v-if="quotaVal(item.quota, `${window}_limit_usd`) != null" class="space-y-0.5">
              <template v-if="(quotaVal(item.quota, `${window}_limit_usd`) as number) === 0">
                <div class="flex items-center justify-between text-xs">
                  <span class="text-gray-600 dark:text-gray-300">{{ t(`dashboard.platformQuota.${window}`) }}</span>
                  <span class="font-mono text-red-500">{{ t('dashboard.platformQuota.disabled') }}</span>
                </div>
                <div class="h-1.5 w-full overflow-hidden rounded-full bg-gray-200 dark:bg-dark-700">
                  <div class="h-full w-full rounded-full bg-red-500" />
                </div>
              </template>
              <template v-else>
                <div class="flex items-center justify-between text-xs">
                  <span class="text-gray-600 dark:text-gray-300">{{ t(`dashboard.platformQuota.${window}`) }}</span>
                  <span class="font-mono text-gray-700 dark:text-gray-200">
                    ${{ formatUsd((quotaVal(item.quota, `${window}_usage_usd`) as number) ?? 0) }} /
                    ${{ formatUsd(quotaVal(item.quota, `${window}_limit_usd`) as number) }}
                  </span>
                </div>
                <div class="h-1.5 w-full overflow-hidden rounded-full bg-gray-200 dark:bg-dark-700">
                  <div
                    class="h-full rounded-full transition-all"
                    :class="quotaBarClass(calcPercent((quotaVal(item.quota, `${window}_usage_usd`) as number) ?? 0, quotaVal(item.quota, `${window}_limit_usd`) as number))"
                    :style="{ width: calcPercent((quotaVal(item.quota, `${window}_usage_usd`) as number) ?? 0, quotaVal(item.quota, `${window}_limit_usd`) as number) + '%' }"
                  />
                </div>
                <p v-if="quotaVal(item.quota, `${window}_window_resets_at`)" class="text-[10px] text-gray-400">
                  {{ t('dashboard.platformQuota.resetsAt', { time: formatResetTime(quotaVal(item.quota, `${window}_window_resets_at`) as string) }) }}
                </p>
              </template>
            </div>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PlatformDashboardStats, PlatformQuotaItem } from '@/types'
import {
  PUBLIC_PLATFORM_ORDER,
  getPublicPlatformLabel,
  normalizePlatformDashboardStats,
  normalizePublicPlatformQuotas,
} from '@/utils/publicPlatforms'

interface PlatformCard extends PlatformDashboardStats {
  isOther?: boolean
  quota?: PlatformQuotaItem
}

const props = defineProps<{
  byPlatform?: PlatformDashboardStats[] | null
  totalActualCost: number
  todayActualCost: number
  platformQuotas?: PlatformQuotaItem[] | null
}>()

const { t } = useI18n()
const publicStats = computed(() => normalizePlatformDashboardStats(props.byPlatform))
const OTHER_THRESHOLD = 0.0001

const cards = computed<PlatformCard[]>(() => {
  const stats = new Map(publicStats.value.map((item) => [item.platform, item]))
  const publicQuotas = normalizePublicPlatformQuotas(props.platformQuotas)
    .filter((quota) => quota.platform !== 'gemini' && quota.platform !== 'antigravity')
  const quotas = new Map<string, PlatformQuotaItem>(publicQuotas.map((item) => [item.platform, item]))
  const platforms = new Set(stats.keys())

  for (const quota of publicQuotas) {
    platforms.add(quota.platform)
  }

  const result = [...platforms].map<PlatformCard>((platform) => {
    const item = stats.get(platform)
    return {
      platform,
      total_requests: item?.total_requests ?? 0,
      total_tokens: item?.total_tokens ?? 0,
      total_actual_cost: item?.total_actual_cost ?? 0,
      today_requests: item?.today_requests ?? 0,
      today_tokens: item?.today_tokens ?? 0,
      today_actual_cost: item?.today_actual_cost ?? 0,
      quota: quotas.get(platform),
    }
  })

  result.sort((a, b) => {
    const ai = PUBLIC_PLATFORM_ORDER.indexOf(a.platform as typeof PUBLIC_PLATFORM_ORDER[number])
    const bi = PUBLIC_PLATFORM_ORDER.indexOf(b.platform as typeof PUBLIC_PLATFORM_ORDER[number])
    if (ai === -1 && bi === -1) return a.platform.localeCompare(b.platform)
    if (ai === -1) return 1
    if (bi === -1) return -1
    return ai - bi
  })

  const sumTotal = result.reduce((sum, item) => sum + item.total_actual_cost, 0)
  const sumToday = result.reduce((sum, item) => sum + item.today_actual_cost, 0)
  const totalDiff = Math.max(0, props.totalActualCost - sumTotal)
  const todayDiff = Math.max(0, props.todayActualCost - sumToday)
  if (totalDiff > OTHER_THRESHOLD || todayDiff > OTHER_THRESHOLD) {
    result.push({
      platform: '__other__',
      total_requests: 0,
      total_tokens: 0,
      total_actual_cost: totalDiff,
      today_requests: 0,
      today_tokens: 0,
      today_actual_cost: todayDiff,
      isOther: true,
    })
  }
  return result
})

type QuotaWindow = 'daily' | 'weekly' | 'monthly'
type QuotaField = `${QuotaWindow}_limit_usd` | `${QuotaWindow}_usage_usd` | `${QuotaWindow}_window_resets_at`

function quotaVal(quota: PlatformQuotaItem | undefined, key: QuotaField): PlatformQuotaItem[QuotaField] {
  return quota?.[key]
}

function hasAnyLimit(quota: PlatformQuotaItem | undefined): boolean {
  return !!quota && (
    quota.daily_limit_usd != null ||
    quota.weekly_limit_usd != null ||
    quota.monthly_limit_usd != null
  )
}

function calcPercent(usage: number, limit: number): number {
  if (!limit || limit <= 0) return 0
  return Math.min(100, Math.max(0, Math.round((usage / limit) * 100)))
}

function quotaBarClass(percent: number): string {
  if (percent >= 95) return 'bg-red-500'
  if (percent >= 75) return 'bg-amber-500'
  return 'bg-green-500'
}

const usdFormatter = new Intl.NumberFormat('en-US', {
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
})

function formatUsd(value: number): string {
  return Number.isFinite(value) ? usdFormatter.format(value) : '0.00'
}

function formatResetTime(iso: string | null | undefined): string {
  if (!iso) return ''
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  return date.toLocaleString(undefined, {
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  })
}

const formatNumber = (value: number) => value.toLocaleString()
const formatCost = (value: number) => value.toFixed(4)
const formatTokens = (value: number) => {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1000) return `${(value / 1000).toFixed(1)}K`
  return value.toString()
}
</script>

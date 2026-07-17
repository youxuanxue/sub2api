<template>
  <div ref="rootRef">
    <TodayStatsBadges :stats="todayStats" :loading="todayStatsLoading" />

    <!-- Loading state -->
    <div v-if="loading" class="space-y-1.5">
      <div class="flex items-center gap-1">
        <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
        <div class="h-1.5 w-8 animate-pulse rounded-full bg-gray-200 dark:bg-gray-700"></div>
        <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
      </div>
      <template v-if="account.type === 'oauth'">
        <div class="flex items-center gap-1">
          <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
          <div class="h-1.5 w-8 animate-pulse rounded-full bg-gray-200 dark:bg-gray-700"></div>
          <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
        </div>
        <div class="flex items-center gap-1">
          <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
          <div class="h-1.5 w-8 animate-pulse rounded-full bg-gray-200 dark:bg-gray-700"></div>
          <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
        </div>
      </template>
    </div>

    <!-- Error state -->
    <div v-else-if="error" class="text-xs text-red-500">
      {{ error }}
    </div>

    <!-- Usage data -->
    <div v-else-if="usageInfo" class="space-y-1">
      <div
        v-if="usageInfo.error"
        class="text-xs text-amber-600 dark:text-amber-400 truncate max-w-[200px]"
        :title="usageInfo.error"
      >
        {{ usageInfo.error }}
      </div>
      <UsageProgressBar
        v-if="usageInfo.five_hour"
        label="5h"
        :utilization="usageInfo.five_hour.utilization"
        :resets-at="usageInfo.five_hour.resets_at"
        :window-stats="usageInfo.five_hour.window_stats"
        color="indigo"
      />
      <UsageProgressBar
        v-if="usageInfo.seven_day"
        label="7d"
        :utilization="usageInfo.seven_day.utilization"
        :resets-at="usageInfo.seven_day.resets_at"
        color="emerald"
      />
      <UsageProgressBar
        v-if="usageInfo.seven_day_sonnet"
        label="7d S"
        :utilization="usageInfo.seven_day_sonnet.utilization"
        :resets-at="usageInfo.seven_day_sonnet.resets_at"
        color="purple"
      />
      <UsageProgressBar
        v-if="usageInfo.seven_day_fable"
        label="7d F"
        :utilization="usageInfo.seven_day_fable.utilization"
        :resets-at="usageInfo.seven_day_fable.resets_at"
        color="amber"
      />
      <UpstreamQuotaSummary
        :quota="usageInfo.upstream_quota"
        :hidden-dimension-keys="upstreamQuotaWindowDimensionKeys"
      />
      <div class="flex items-center gap-1.5 mt-0.5">
        <span
          v-if="usageInfo.source === 'passive'"
          class="text-[9px] text-gray-400 dark:text-gray-500 italic"
        >
          {{ t('admin.accounts.usageWindow.passiveSampled') }}
        </span>
        <button
          type="button"
          class="inline-flex items-center gap-0.5 rounded px-1.5 py-0.5 text-[9px] font-medium text-blue-600 hover:bg-blue-50 dark:text-blue-400 dark:hover:bg-blue-900/30 transition-colors"
          :disabled="activeQueryLoading"
          @click="loadActiveUsage"
        >
          <svg
            class="h-2.5 w-2.5"
            :class="{ 'animate-spin': activeQueryLoading }"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
            />
          </svg>
          {{ t('admin.accounts.usageWindow.activeQuery') }}
        </button>
      </div>
    </div>

    <div v-else class="text-xs text-gray-400">-</div>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import UsageProgressBar from '../UsageProgressBar.vue'
import UpstreamQuotaSummary from './UpstreamQuotaSummary.vue'
import TodayStatsBadges from './TodayStatsBadges.vue'
import {
  accountUsageCellPropDefaults,
  type AccountUsageCellProps
} from '../accountUsageCellProps'
import { useAccountUsageFetch } from './useAccountUsageFetch'

const props = withDefaults(defineProps<AccountUsageCellProps>(), accountUsageCellPropDefaults)

const { t } = useI18n()
const rootRef = ref<HTMLElement | null>(null)
const upstreamQuotaWindowDimensionKeys = [
  'anthropic_5h',
  'anthropic_7d',
  'anthropic_7d_sonnet',
  'anthropic_7d_fable'
]

const { loading, activeQueryLoading, error, usageInfo, loadActiveUsage } = useAccountUsageFetch(
  props,
  rootRef
)
</script>

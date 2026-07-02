<template>
  <div ref="rootRef">
    <div v-if="hasOpenAIUsageFallback" class="space-y-1">
      <UsageProgressBar
        v-if="usageInfo?.five_hour"
        label="5h"
        :utilization="usageInfo.five_hour.utilization"
        :resets-at="usageInfo.five_hour.resets_at"
        :window-stats="usageInfo.five_hour.window_stats"
        :show-now-when-idle="true"
        color="indigo"
      />
      <UsageProgressBar
        v-if="usageInfo?.seven_day"
        label="7d"
        :utilization="usageInfo.seven_day.utilization"
        :resets-at="usageInfo.seven_day.resets_at"
        :window-stats="usageInfo.seven_day.window_stats"
        :show-now-when-idle="true"
        color="emerald"
      />
      <UpstreamQuotaSummary :quota="usageInfo?.upstream_quota" />
      <OpenAIQuotaResetCell :account="account">
        <template #pre-actions>
          <button
            type="button"
            class="inline-flex items-center gap-0.5 rounded px-1.5 py-0.5 text-[10px] font-medium text-blue-600 hover:bg-blue-50 dark:text-blue-400 dark:hover:bg-blue-900/30 transition-colors disabled:cursor-not-allowed disabled:opacity-50"
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
        </template>
      </OpenAIQuotaResetCell>
    </div>
    <div v-else-if="loading" class="space-y-1.5">
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
    </div>
    <div v-else>
      <div class="text-xs text-gray-400">-</div>
      <OpenAIQuotaResetCell :account="account" class="mt-1" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import UsageProgressBar from '../UsageProgressBar.vue'
import OpenAIQuotaResetCell from '../OpenAIQuotaResetCell.vue'
import UpstreamQuotaSummary from './UpstreamQuotaSummary.vue'
import {
  accountUsageCellPropDefaults,
  type AccountUsageCellProps
} from '../accountUsageCellProps'
import { useAccountUsageFetch } from './useAccountUsageFetch'

const props = withDefaults(defineProps<AccountUsageCellProps>(), accountUsageCellPropDefaults)

const { t } = useI18n()
const rootRef = ref<HTMLElement | null>(null)

const { loading, activeQueryLoading, usageInfo, loadActiveUsage } = useAccountUsageFetch(
  props,
  rootRef,
  { enableOpenAIRefreshKeyWatch: true }
)

const hasOpenAIUsageFallback = computed(() => {
  return !!usageInfo.value?.five_hour || !!usageInfo.value?.seven_day
})
</script>

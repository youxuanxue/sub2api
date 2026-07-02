<template>
  <div ref="rootRef">
    <!-- Loading state -->
    <div v-if="loading" class="space-y-1.5">
      <div class="flex items-center gap-1">
        <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
        <div class="h-1.5 w-8 animate-pulse rounded-full bg-gray-200 dark:bg-gray-700"></div>
        <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
      </div>
    </div>

    <!-- Error state -->
    <div v-else-if="error" class="text-xs text-red-500">
      {{ error }}
    </div>

    <!-- Usage data -->
    <div v-else-if="kiro" class="space-y-1">
      <div
        v-if="usageInfo?.error"
        class="text-xs text-amber-600 dark:text-amber-400 truncate max-w-[200px]"
        :title="usageInfo.error"
      >
        {{ usageInfo.error }}
      </div>

      <!-- Credits budget (monthly reset) -->
      <UsageProgressBar
        :label="t('admin.accounts.usageWindow.kiroCredits')"
        :utilization="kiro.percent ?? 0"
        :resets-at="kiro.next_reset_date || null"
        color="indigo"
      />
      <div
        v-if="kiro.limit"
        class="pl-[36px] text-[9px] leading-tight text-gray-400 dark:text-gray-500"
      >
        {{ formatCredits(kiro.current) }} / {{ formatCredits(kiro.limit) }}
      </div>

      <!-- Free-trial allowance (separate expiry) -->
      <template v-if="kiro.trial">
        <UsageProgressBar
          :label="t('admin.accounts.usageWindow.kiroTrial')"
          :utilization="kiro.trial.percent ?? 0"
          :resets-at="kiro.trial.expires_at || null"
          color="amber"
        />
        <div
          v-if="kiro.trial.expires_at"
          class="pl-[36px] text-[9px] leading-tight text-gray-400 dark:text-gray-500"
        >
          {{ t('admin.accounts.usageWindow.kiroTrialExpires') }} {{ formatDateOnly(kiro.trial.expires_at) }}
        </div>
      </template>

      <!-- Subscription title -->
      <div
        v-if="kiro.subscription_title"
        class="text-[9px] leading-tight text-gray-400 dark:text-gray-500 truncate max-w-[200px]"
        :title="kiro.subscription_title"
      >
        {{ kiro.subscription_title }}
      </div>
      <UpstreamQuotaSummary :quota="usageInfo?.upstream_quota" />

      <div class="flex items-center gap-1.5 mt-0.5">
        <span
          v-if="usageInfo?.source === 'passive'"
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

    <div v-else class="space-y-1">
      <div class="text-xs text-gray-400">-</div>
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
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import UsageProgressBar from '../UsageProgressBar.vue'
import UpstreamQuotaSummary from './UpstreamQuotaSummary.vue'
import { formatDateOnly } from '@/utils/format'
import {
  accountUsageCellPropDefaults,
  type AccountUsageCellProps
} from '../accountUsageCellProps'
import { useAccountUsageFetch } from './useAccountUsageFetch'

const props = withDefaults(defineProps<AccountUsageCellProps>(), accountUsageCellPropDefaults)

const { t } = useI18n()
const rootRef = ref<HTMLElement | null>(null)

const { loading, activeQueryLoading, error, usageInfo, loadActiveUsage } = useAccountUsageFetch(
  props,
  rootRef
)

// kiro_usage rides on the same AccountUsageInfo the other cells read (active getUsage
// JSON, or the edge passive DTO lifted in edgeAccounts.tk.ts → toUsageInfo).
const kiro = computed(() => usageInfo.value?.kiro_usage ?? null)

function formatCredits(n?: number): string {
  if (n == null) return '0'
  return Number.isInteger(n) ? String(n) : n.toFixed(2)
}
</script>

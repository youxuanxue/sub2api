<template>
  <div ref="rootRef">
    <div v-if="loading" class="space-y-1.5">
      <div class="flex items-center gap-1">
        <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
        <div class="h-1.5 w-8 animate-pulse rounded-full bg-gray-200 dark:bg-gray-700"></div>
        <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
      </div>
    </div>
    <div v-else-if="error" class="text-xs text-red-500">
      {{ error }}
    </div>
    <div v-else-if="needsReauth" class="space-y-1">
      <span
        class="inline-block rounded bg-orange-100 px-1.5 py-0.5 text-[10px] font-medium text-orange-700 dark:bg-orange-900/40 dark:text-orange-300"
      >
        {{ t('admin.accounts.needsReauth') }}
      </span>
    </div>
    <div v-else-if="isForbidden" class="space-y-1">
      <span
        class="inline-block rounded bg-red-100 px-1.5 py-0.5 text-[10px] font-medium text-red-700 dark:bg-red-900/40 dark:text-red-300"
      >
        {{ grokEntitlementLabel || t('admin.accounts.forbidden') }}
      </span>
    </div>
    <div v-else-if="usageInfo" class="space-y-1">
      <div v-if="grokEntitlementLabel" class="mb-0.5">
        <span
          class="inline-block rounded bg-zinc-100 px-1.5 py-0.5 text-[10px] font-medium text-zinc-800 dark:bg-zinc-800 dark:text-zinc-200"
        >
          {{ grokEntitlementLabel }}
        </span>
      </div>
      <div v-if="grokLocalUsage" class="mb-0.5 flex items-center">
        <div class="flex items-center gap-1.5 text-[9px] text-gray-500 dark:text-gray-400">
          <span class="rounded bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800">
            {{ formatWindowRequests(grokLocalUsage) }} req
          </span>
          <span class="rounded bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800">
            {{ formatWindowTokens(grokLocalUsage) }}
          </span>
          <span
            class="rounded bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800"
            :title="t('usage.accountBilled')"
          >
            A ${{ formatWindowCost(grokLocalUsage) }}
          </span>
        </div>
      </div>
      <UsageProgressBar
        v-if="grokRequestQuotaBar"
        :label="t('admin.accounts.usageWindow.grokRequests')"
        :utilization="grokRequestQuotaBar.utilization"
        :resets-at="grokRequestQuotaBar.resetsAt"
        color="indigo"
      />
      <UsageProgressBar
        v-if="grokTokenQuotaBar"
        :label="t('admin.accounts.usageWindow.grokTokens')"
        :utilization="grokTokenQuotaBar.utilization"
        :resets-at="grokTokenQuotaBar.resetsAt"
        color="emerald"
      />
      <div v-if="grokRetryAfterLabel" class="text-[10px] text-amber-600 dark:text-amber-400">
        {{ t('admin.accounts.usageWindow.grokRetryAfter', { time: grokRetryAfterLabel }) }}
      </div>
      <div v-if="grokQuotaUnknown" class="text-[10px] text-gray-500 dark:text-gray-400">
        {{ grokQuotaUnknownLabel }}
      </div>
      <div
        v-else-if="usageInfo.error"
        class="max-w-[200px] truncate text-xs text-amber-600 dark:text-amber-400"
        :title="usageInfo.error"
      >
        {{ usageErrorLabel }}
      </div>
      <div v-if="grokQuotaStatusLine" class="text-[10px] text-gray-500 dark:text-gray-400">
        {{ grokQuotaStatusLine }}
      </div>
      <GrokQuotaProbeCell :account="account" />
    </div>
    <div v-else class="text-xs text-gray-400">-</div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { WindowStats } from '@/types'
import { formatCompactNumber, formatRelativeTime } from '@/utils/format'
import GrokQuotaProbeCell from '../GrokQuotaProbeCell.vue'
import UsageProgressBar from '../UsageProgressBar.vue'
import {
  accountUsageCellPropDefaults,
  type AccountUsageCellProps
} from '../accountUsageCellProps'
import { useAccountUsageFetch } from './useAccountUsageFetch'

interface GrokQuotaBarInfo {
  utilization: number
  resetsAt: string | null
}

const props = withDefaults(defineProps<AccountUsageCellProps>(), accountUsageCellPropDefaults)

const { t } = useI18n()
const rootRef = ref<HTMLElement | null>(null)

const { loading, error, usageInfo } = useAccountUsageFetch(props, rootRef)

const makeGrokQuotaBar = (
  quota?: { limit?: number | null; remaining?: number | null; reset_at?: string | null } | null
): GrokQuotaBarInfo | null => {
  if (!quota || quota.limit == null || quota.remaining == null || quota.limit <= 0) return null
  const used = Math.max(0, quota.limit - quota.remaining)
  return {
    utilization: Math.min(100, (used / quota.limit) * 100),
    resetsAt: quota.reset_at || null
  }
}

const grokRequestQuotaBar = computed(() => makeGrokQuotaBar(usageInfo.value?.grok_request_quota))
const grokTokenQuotaBar = computed(() => makeGrokQuotaBar(usageInfo.value?.grok_token_quota))
const grokQuotaUnknown = computed(() => {
  if (grokRequestQuotaBar.value || grokTokenQuotaBar.value) return false
  return usageInfo.value?.grok_quota_snapshot_state !== 'observed'
})
const grokQuotaUnknownLabel = computed(() => {
  return usageInfo.value?.grok_quota_snapshot_state === 'no_headers'
    ? t('admin.accounts.usageWindow.grokNoHeaders')
    : t('admin.accounts.usageWindow.grokUnknown')
})
const grokQuotaStatusLine = computed(() => {
  const parts: string[] = []
  const status = usageInfo.value?.grok_last_status_code
  if (status) {
    parts.push(t('admin.accounts.usageWindow.grokLastStatus', { status }))
  }
  if (usageInfo.value?.grok_last_quota_probe_at) {
    parts.push(
      t('admin.accounts.usageWindow.grokLastProbe', {
        time: formatRelativeTime(usageInfo.value.grok_last_quota_probe_at)
      })
    )
  }
  if (usageInfo.value?.grok_last_headers_seen_at) {
    parts.push(
      t('admin.accounts.usageWindow.grokLastHeadersSeen', {
        time: formatRelativeTime(usageInfo.value.grok_last_headers_seen_at)
      })
    )
  }
  return parts.length > 0 ? parts.join(' | ') : null
})
const grokLocalUsage = computed(() => usageInfo.value?.grok_local_usage || props.todayStats || null)
const grokEntitlementLabel = computed(() => {
  const status = (usageInfo.value?.grok_entitlement_status || '').trim()
  return status || null
})
const grokRetryAfterLabel = computed(() => {
  const seconds = usageInfo.value?.grok_retry_after_seconds
  if (seconds == null || seconds <= 0) return null
  if (seconds < 60) return `${seconds}s`
  return `${Math.ceil(seconds / 60)}m`
})
const needsReauth = computed(() => !!usageInfo.value?.needs_reauth)
const isForbidden = computed(() => !!usageInfo.value?.is_forbidden)
const usageErrorLabel = computed(() => {
  const code = usageInfo.value?.error_code
  if (code === 'rate_limited') return t('admin.accounts.rateLimited')
  return t('admin.accounts.usageError')
})

const formatWindowRequests = (stats: WindowStats) =>
  formatCompactNumber(stats.requests, { allowBillions: false })
const formatWindowTokens = (stats: WindowStats) => formatCompactNumber(stats.tokens)
const formatWindowCost = (stats: WindowStats) => stats.cost.toFixed(2)
</script>

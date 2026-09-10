<template>
  <div v-if="visible" class="space-y-1">
    <div class="flex flex-wrap items-center gap-1.5">
      <button
        type="button"
        class="inline-flex items-center gap-0.5 rounded px-1.5 py-0.5 text-[10px] font-medium text-cyan-700 transition-colors hover:bg-cyan-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-cyan-300 dark:hover:bg-cyan-900/30"
        :disabled="loading"
        :title="t('admin.accounts.usageWindow.grokProbeTooltip')"
        @click="handleProbe"
      >
        <svg
          class="h-2.5 w-2.5"
          :class="{ 'animate-spin': loading }"
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
        {{ t('admin.accounts.usageWindow.grokProbe') }}
      </button>
    </div>

    <UsageProgressBar
      v-if="billing?.period_type === 'weekly' && billing.usage_percent != null"
      label="7d"
      :utilization="billing.usage_percent"
      :resets-at="billing.period_end"
      :window-stats="usage?.grok_local_usage_7d"
      :window-stats-label="t('admin.accounts.usageWindow.grokBillingPeriod')"
      color="emerald"
    />
    <UsageStatsRow label="24h" :stats="usage?.grok_local_usage_24h" />
    <div v-if="monthlyLimit != null && monthlyLimit > 0 && monthlyUsed != null" class="text-[10px] text-gray-600 dark:text-gray-300">
      <span :title="billing?.billing_period_end">{{ t('admin.accounts.usageWindow.grokMonthlyLimit') }}: ${{ monthlyUsed.toFixed(2) }} / ${{ monthlyLimit.toFixed(2) }}</span>
      <UsageStatsRow :label="t('admin.accounts.usageWindow.grokBillingPeriod')" :stats="usage?.grok_local_usage_monthly" />
    </div>
    <div v-if="billing?.prepaid_balance != null" class="text-[10px] text-gray-600 dark:text-gray-300">
      {{ t('admin.accounts.usageWindow.grokPrepaid') }}: ${{ billing.prepaid_balance.toFixed(2) }}
    </div>
    <div v-if="error" class="truncate text-[10px] text-red-600 dark:text-red-400" :title="error">
      {{ truncatedError }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { GrokQuotaProbeResult } from '@/api/admin/grok'
import type { Account, AccountUsageInfo } from '@/types'
import { PLATFORM_GROK } from '@/constants/gatewayPlatforms'
import UsageProgressBar from './UsageProgressBar.vue'
import UsageStatsRow from './UsageStatsRow.vue'

const props = defineProps<{
  account: Account
  usage?: AccountUsageInfo | null
  activeUsageLoader?: () => Promise<AccountUsageInfo>
}>()

const emit = defineEmits<{ probed: [result: GrokQuotaProbeResult] }>()

const { t } = useI18n()

const visible = computed(() => props.account.platform === PLATFORM_GROK && props.account.type === 'oauth')
const loading = ref(false)
const error = ref<string | null>(null)
const queriedUsage = ref<Partial<AccountUsageInfo> | null>(null)

const extractErrorMessage = (e: unknown): string => {
  const err = e as {
    message?: string
    reason?: string
    response?: { data?: { message?: string; error?: string } }
  }
  return (
    err?.message ||
    err?.reason ||
    err?.response?.data?.message ||
    err?.response?.data?.error ||
    t('common.error')
  )
}

const usage = computed(() => queriedUsage.value ?? props.usage)
const billing = computed(() => usage.value?.grok_billing)
const monthlyLimit = computed(() => billing.value?.monthly_limit ?? (
  billing.value?.monthly_limit_cents == null ? null : billing.value.monthly_limit_cents / 100
))
const monthlyUsed = computed(() => billing.value?.monthly_used ?? (
  billing.value?.used_cents == null ? null : billing.value.used_cents / 100
))

const truncatedError = computed(() => {
  if (!error.value) return ''
  return error.value.length > 80 ? `${error.value.slice(0, 80)}...` : error.value
})

const handleProbe = async () => {
  if (loading.value) return
  loading.value = true
  error.value = null
  try {
    if (props.activeUsageLoader) {
      queriedUsage.value = await props.activeUsageLoader()
      error.value = queriedUsage.value.error || null
    } else {
      const result = await adminAPI.grok.queryQuota(props.account.id)
      queriedUsage.value = {
        grok_billing: result.billing,
        grok_local_usage_24h: result.local_usage_24h,
        grok_local_usage_7d: result.local_usage_7d,
        grok_local_usage_monthly: result.local_usage_monthly,
      }
      error.value = result.probe_error || null
      emit('probed', result)
    }
  } catch (e) {
    error.value = extractErrorMessage(e)
  } finally {
    loading.value = false
  }
}

watch(
  () => props.account.id,
  () => {
    queriedUsage.value = null
    error.value = null
    loading.value = false
  }
)
</script>

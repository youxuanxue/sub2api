<template>
  <div v-if="quota && visible" class="space-y-0.5">
    <div class="flex flex-wrap items-center gap-1">
      <span
        :class="[
          'inline-flex max-w-[7rem] items-center rounded px-1.5 py-0.5 text-[9px] font-medium uppercase leading-tight',
          stateClass
        ]"
        :title="stateTitle"
      >
        {{ providerLabel }}
      </span>
      <span
        v-if="subscriptionLabel"
        class="max-w-[7rem] truncate rounded bg-gray-100 px-1.5 py-0.5 text-[9px] text-gray-600 dark:bg-gray-800 dark:text-gray-300"
        :title="subscriptionLabel"
      >
        {{ subscriptionLabel }}
      </span>
      <span
        v-if="retryAfterLabel"
        class="rounded bg-amber-100 px-1.5 py-0.5 text-[9px] text-amber-700 dark:bg-amber-900/40 dark:text-amber-300"
      >
        {{ retryAfterLabel }}
      </span>
    </div>
    <div v-if="quotaLines.length > 0" class="flex flex-wrap gap-1 text-[9px] text-gray-500 dark:text-gray-400">
      <span
        v-for="line in quotaLines"
        :key="line.key"
        class="max-w-[9rem] truncate rounded bg-gray-100 px-1.5 py-0.5 dark:bg-gray-800"
        :title="line.title"
      >
        {{ line.text }}
      </span>
    </div>
    <div
      v-else-if="quota.state === 'unsupported' || quota.state === 'unknown'"
      class="truncate text-[9px] text-gray-400 dark:text-gray-500"
      :title="quota.error || stateTitle"
    >
      {{ quota.state === 'unsupported' ? t('admin.accounts.usageWindow.upstreamUnsupported') : t('admin.accounts.usageWindow.upstreamUnknown') }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { UpstreamQuotaCredit, UpstreamQuotaDimension, UpstreamQuotaInfo } from '@/types'

const props = defineProps<{
  quota?: UpstreamQuotaInfo | null
  maxItems?: number
}>()

const { t } = useI18n()

const quota = computed(() => props.quota ?? null)
const visible = computed(() => {
  if (!quota.value) return false
  if (quota.value.state === 'unsupported') return true
  return (
    !!quota.value.error ||
    !!quota.value.entitlement_status ||
    !!quota.value.subscription_tier ||
    !!quota.value.subscription_tier_raw ||
    !!quota.value.retry_after_seconds ||
    (quota.value.dimensions?.length ?? 0) > 0 ||
    (quota.value.credits?.length ?? 0) > 0
  )
})

const providerLabel = computed(() => quota.value?.provider || t('admin.accounts.usageWindow.upstreamQuota'))

const stateClass = computed(() => {
  switch (quota.value?.state) {
    case 'observed':
      return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300'
    case 'simulated':
      return 'bg-blue-100 text-blue-700 dark:bg-blue-900/40 dark:text-blue-300'
    case 'degraded':
      return 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300'
    case 'unsupported':
      return 'bg-gray-100 text-gray-500 dark:bg-gray-800 dark:text-gray-400'
    default:
      return 'bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-300'
  }
})

const stateTitle = computed(() => {
  const parts = [
    quota.value?.state,
    quota.value?.source,
    quota.value?.error_code,
    quota.value?.status_code ? `HTTP ${quota.value.status_code}` : null
  ].filter(Boolean)
  if (quota.value?.error) parts.push(quota.value.error)
  return parts.join(' | ')
})

const subscriptionLabel = computed(() => {
  return quota.value?.subscription_tier || quota.value?.subscription_tier_raw || quota.value?.entitlement_status || ''
})

const retryAfterLabel = computed(() => {
  const seconds = quota.value?.retry_after_seconds
  if (seconds == null || seconds <= 0) return ''
  if (seconds < 60) return t('admin.accounts.usageWindow.grokRetryAfter', { time: `${seconds}s` })
  return t('admin.accounts.usageWindow.grokRetryAfter', { time: `${Math.ceil(seconds / 60)}m` })
})

const quotaLines = computed(() => {
  const max = props.maxItems ?? 3
  const lines = [
    ...(quota.value?.credits ?? []).map(formatCreditLine).filter(Boolean),
    ...(quota.value?.dimensions ?? []).map(formatDimensionLine).filter(Boolean)
  ] as Array<{ key: string; text: string; title: string }>
  return lines.slice(0, max)
})

function formatDimensionLine(dim: UpstreamQuotaDimension): { key: string; text: string; title: string } | null {
  const label = compactLabel(dim.label || dim.key)
  let value = ''
  if (dim.remaining != null && dim.limit != null) {
    value = `${formatNumber(dim.remaining)}/${formatNumber(dim.limit)}`
  } else if (dim.used != null && dim.limit != null) {
    value = `${formatNumber(dim.used)}/${formatNumber(dim.limit)}`
  } else if (dim.utilization != null) {
    value = `${formatPercent(dim.utilization)}`
  }
  if (!value) return null
  const suffix = dim.window ? ` ${dim.window}` : ''
  const text = `${label}${suffix} ${value}`
  return { key: `d:${dim.key}`, text, title: text }
}

function formatCreditLine(credit: UpstreamQuotaCredit): { key: string; text: string; title: string } | null {
  const label = compactLabel(credit.label || credit.key)
  let value = ''
  if (credit.remaining != null && credit.limit != null) {
    value = `${formatNumber(credit.remaining)}/${formatNumber(credit.limit)}`
  } else if (credit.current != null && credit.limit != null) {
    value = `${formatNumber(credit.current)}/${formatNumber(credit.limit)}`
  } else if (credit.remaining != null) {
    value = formatNumber(credit.remaining)
  } else if (credit.current != null) {
    value = formatNumber(credit.current)
  }
  if (!value) return null
  const text = `${label} ${value}`
  return { key: `c:${credit.key}`, text, title: text }
}

function compactLabel(raw: string): string {
  const value = raw.replace(/^antigravity_model_/, '').replace(/^antigravity_credit_/, '')
  if (value.length <= 18) return value
  return `${value.slice(0, 15)}...`
}

function formatNumber(n: number): string {
  if (!Number.isFinite(n)) return '0'
  if (Math.abs(n) >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (Math.abs(n) >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  if (Number.isInteger(n)) return String(n)
  return n.toFixed(2)
}

function formatPercent(n: number): string {
  if (!Number.isFinite(n)) return '0%'
  if (Math.abs(n) >= 10) return `${Math.round(n)}%`
  return `${n.toFixed(1)}%`
}
</script>

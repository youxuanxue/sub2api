import { computed, type ComputedRef, type Ref } from 'vue'
import type { AccountUsageInfo } from '@/types'

export interface AntigravityUsageResult {
  utilization: number
  resetTime: string | null
}

function getAntigravityUsageFromAPI(
  quota: AccountUsageInfo['antigravity_quota'] | undefined,
  modelNames: string[]
): AntigravityUsageResult | null {
  if (!quota) return null

  let maxUtilization = 0
  let earliestReset: string | null = null

  for (const model of modelNames) {
    const modelQuota = quota[model]
    if (!modelQuota) continue

    if (modelQuota.utilization > maxUtilization) {
      maxUtilization = modelQuota.utilization
    }
    if (modelQuota.reset_time) {
      if (!earliestReset || modelQuota.reset_time < earliestReset) {
        earliestReset = modelQuota.reset_time
      }
    }
  }

  if (maxUtilization === 0 && earliestReset === null) {
    const hasAnyData = modelNames.some((m) => quota[m])
    if (!hasAnyData) return null
  }

  return {
    utilization: maxUtilization,
    resetTime: earliestReset
  }
}

export function useAntigravityQuota(usageInfo: Ref<AccountUsageInfo | null>) {
  const hasAntigravityQuotaFromAPI = computed(() => {
    return (
      !!usageInfo.value?.antigravity_quota &&
      Object.keys(usageInfo.value.antigravity_quota).length > 0
    )
  })

  const antigravity3ProUsageFromAPI: ComputedRef<AntigravityUsageResult | null> = computed(() =>
    getAntigravityUsageFromAPI(usageInfo.value?.antigravity_quota, [
      'gemini-3-pro-low',
      'gemini-3-pro-high',
      'gemini-3-pro-preview'
    ])
  )

  const antigravity3FlashUsageFromAPI: ComputedRef<AntigravityUsageResult | null> = computed(() =>
    getAntigravityUsageFromAPI(usageInfo.value?.antigravity_quota, ['gemini-3-flash'])
  )

  const antigravity3ImageUsageFromAPI: ComputedRef<AntigravityUsageResult | null> = computed(() =>
    getAntigravityUsageFromAPI(usageInfo.value?.antigravity_quota, [
      'gemini-2.5-flash-image',
      'gemini-3.1-flash-image',
      'gemini-3-pro-image'
    ])
  )

  const antigravityClaudeUsageFromAPI: ComputedRef<AntigravityUsageResult | null> = computed(() =>
    getAntigravityUsageFromAPI(usageInfo.value?.antigravity_quota, [
      'claude-fable-5',
      'claude-sonnet-4-5',
      'claude-opus-4-5-thinking',
      'claude-sonnet-4-6',
      'claude-opus-4-6',
      'claude-opus-4-6-thinking',
      'claude-opus-4-7',
      'claude-opus-4-8'
    ])
  )

  const aiCreditsDisplay = computed(() => {
    const credits = usageInfo.value?.ai_credits
    if (!credits || credits.length === 0) return null
    const total = credits.reduce((sum, credit) => sum + (credit.amount ?? 0), 0)
    if (total <= 0) return null
    return total.toFixed(0)
  })

  return {
    hasAntigravityQuotaFromAPI,
    antigravity3ProUsageFromAPI,
    antigravity3FlashUsageFromAPI,
    antigravity3ImageUsageFromAPI,
    antigravityClaudeUsageFromAPI,
    aiCreditsDisplay
  }
}

export function useAntigravityAccountMeta(account: { extra?: unknown }) {
  const antigravityTier = computed(() => {
    const extra = account.extra as Record<string, unknown> | undefined
    if (!extra) return null

    const loadCodeAssist = extra.load_code_assist as Record<string, unknown> | undefined
    if (!loadCodeAssist) return null

    const paidTier = loadCodeAssist.paidTier as Record<string, unknown> | undefined
    if (paidTier && typeof paidTier.id === 'string') {
      return paidTier.id
    }

    const currentTier = loadCodeAssist.currentTier as Record<string, unknown> | undefined
    if (currentTier && typeof currentTier.id === 'string') {
      return currentTier.id
    }

    return null
  })

  const hasIneligibleTiers = computed(() => {
    const extra = account.extra as Record<string, unknown> | undefined
    if (!extra) return false

    const loadCodeAssist = extra.load_code_assist as Record<string, unknown> | undefined
    if (!loadCodeAssist) return false

    const ineligibleTiers = loadCodeAssist.ineligibleTiers as unknown[] | undefined
    return Array.isArray(ineligibleTiers) && ineligibleTiers.length > 0
  })

  return {
    antigravityTier,
    hasIneligibleTiers
  }
}

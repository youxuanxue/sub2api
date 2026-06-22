import { computed, type ComputedRef, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Account, AccountUsageInfo, GeminiCredentials, WindowStats } from '@/types'

export function useGeminiUsageMeta(
  account: Account,
  usageInfo: Ref<AccountUsageInfo | null>
) {
  const { t } = useI18n()

  const geminiTier = computed(() => {
    if (account.platform !== 'gemini') return null
    const creds = account.credentials as GeminiCredentials | undefined
    return creds?.tier_id || null
  })

  const geminiOAuthType = computed(() => {
    if (account.platform !== 'gemini') return null
    const creds = account.credentials as GeminiCredentials | undefined
    return (creds?.oauth_type || '').trim() || null
  })

  const isGeminiCodeAssist = computed(() => {
    if (account.platform !== 'gemini') return false
    const creds = account.credentials as GeminiCredentials | undefined
    return creds?.oauth_type === 'code_assist' || (!creds?.oauth_type && !!creds?.project_id)
  })

  const geminiChannelShort = computed((): 'ai studio' | 'gcp' | 'google one' | 'client' | null => {
    if (account.platform !== 'gemini') return null

    if (account.type === 'apikey') return 'ai studio'

    if (geminiOAuthType.value === 'google_one') return 'google one'
    if (isGeminiCodeAssist.value) return 'gcp'
    if (geminiOAuthType.value === 'ai_studio') return 'client'

    return 'ai studio'
  })

  const geminiUserLevel = computed((): string | null => {
    if (account.platform !== 'gemini') return null

    const tier = (geminiTier.value || '').toString().trim()
    const tierLower = tier.toLowerCase()
    const tierUpper = tier.toUpperCase()

    if (geminiOAuthType.value === 'google_one') {
      if (tierLower === 'google_one_free') return 'free'
      if (tierLower === 'google_ai_pro') return 'pro'
      if (tierLower === 'google_ai_ultra') return 'ultra'

      if (tierUpper === 'AI_PREMIUM' || tierUpper === 'GOOGLE_ONE_STANDARD') return 'pro'
      if (tierUpper === 'GOOGLE_ONE_UNLIMITED') return 'ultra'
      if (
        tierUpper === 'FREE' ||
        tierUpper === 'GOOGLE_ONE_BASIC' ||
        tierUpper === 'GOOGLE_ONE_UNKNOWN' ||
        tierUpper === ''
      ) {
        return 'free'
      }

      return null
    }

    if (isGeminiCodeAssist.value) {
      if (tierLower === 'gcp_enterprise') return 'enterprise'
      if (tierLower === 'gcp_standard') return 'standard'

      if (tierUpper.includes('ULTRA') || tierUpper.includes('ENTERPRISE')) return 'enterprise'
      return 'standard'
    }

    if (account.type === 'apikey' || geminiOAuthType.value === 'ai_studio') {
      if (tierLower === 'aistudio_paid') return 'paid'
      if (tierLower === 'aistudio_free') return 'free'

      if (tierUpper.includes('PAID') || tierUpper.includes('PAYG') || tierUpper.includes('PAY')) {
        return 'paid'
      }
      if (tierUpper.includes('FREE')) return 'free'
      if (account.type === 'apikey') return 'free'
      return null
    }

    return null
  })

  const geminiAuthTypeLabel = computed(() => {
    if (account.platform !== 'gemini') return null
    if (!geminiChannelShort.value) return null
    return geminiUserLevel.value
      ? `${geminiChannelShort.value} ${geminiUserLevel.value}`
      : geminiChannelShort.value
  })

  const geminiTierClass = computed(() => {
    const channel = geminiChannelShort.value
    const level = geminiUserLevel.value

    if (channel === 'client' || channel === 'ai studio') {
      return 'bg-blue-100 text-blue-600 dark:bg-blue-900/40 dark:text-blue-300'
    }

    if (channel === 'google one') {
      if (level === 'ultra') {
        return 'bg-purple-100 text-purple-600 dark:bg-purple-900/40 dark:text-purple-300'
      }
      if (level === 'pro') {
        return 'bg-blue-100 text-blue-600 dark:bg-blue-900/40 dark:text-blue-300'
      }
      return 'bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-300'
    }

    if (channel === 'gcp') {
      if (level === 'enterprise') {
        return 'bg-purple-100 text-purple-600 dark:bg-purple-900/40 dark:text-purple-300'
      }
      return 'bg-blue-100 text-blue-600 dark:bg-blue-900/40 dark:text-blue-300'
    }

    return ''
  })

  const geminiQuotaPolicyChannel = computed(() => {
    if (geminiOAuthType.value === 'google_one') {
      return t('admin.accounts.gemini.quotaPolicy.rows.googleOne.channel')
    }
    if (isGeminiCodeAssist.value) {
      return t('admin.accounts.gemini.quotaPolicy.rows.gcp.channel')
    }
    return t('admin.accounts.gemini.quotaPolicy.rows.aiStudio.channel')
  })

  const geminiQuotaPolicyLimits = computed(() => {
    const tierLower = (geminiTier.value || '').toString().trim().toLowerCase()

    if (geminiOAuthType.value === 'google_one') {
      if (tierLower === 'google_ai_ultra' || geminiUserLevel.value === 'ultra') {
        return t('admin.accounts.gemini.quotaPolicy.rows.googleOne.limitsUltra')
      }
      if (tierLower === 'google_ai_pro' || geminiUserLevel.value === 'pro') {
        return t('admin.accounts.gemini.quotaPolicy.rows.googleOne.limitsPro')
      }
      return t('admin.accounts.gemini.quotaPolicy.rows.googleOne.limitsFree')
    }

    if (isGeminiCodeAssist.value) {
      if (tierLower === 'gcp_enterprise' || geminiUserLevel.value === 'enterprise') {
        return t('admin.accounts.gemini.quotaPolicy.rows.gcp.limitsEnterprise')
      }
      return t('admin.accounts.gemini.quotaPolicy.rows.gcp.limitsStandard')
    }

    if (tierLower === 'aistudio_paid' || geminiUserLevel.value === 'paid') {
      return t('admin.accounts.gemini.quotaPolicy.rows.aiStudio.limitsPaid')
    }
    return t('admin.accounts.gemini.quotaPolicy.rows.aiStudio.limitsFree')
  })

  const geminiQuotaPolicyDocsUrl = computed(() => {
    if (geminiOAuthType.value === 'google_one' || isGeminiCodeAssist.value) {
      return 'https://developers.google.com/gemini-code-assist/resources/quotas'
    }
    return 'https://ai.google.dev/pricing'
  })

  const geminiUsesSharedDaily = computed(() => {
    if (account.platform !== 'gemini') return false
    return (
      !!usageInfo.value?.gemini_shared_daily ||
      !!usageInfo.value?.gemini_shared_minute ||
      geminiOAuthType.value === 'google_one' ||
      isGeminiCodeAssist.value
    )
  })

  const geminiUsageAvailable = computed(() => {
    return (
      !!usageInfo.value?.gemini_shared_daily ||
      !!usageInfo.value?.gemini_pro_daily ||
      !!usageInfo.value?.gemini_flash_daily ||
      !!usageInfo.value?.gemini_shared_minute ||
      !!usageInfo.value?.gemini_pro_minute ||
      !!usageInfo.value?.gemini_flash_minute
    )
  })

  const geminiUsageBars: ComputedRef<
    Array<{
      key: string
      label: string
      utilization: number
      resetsAt: string | null
      windowStats?: WindowStats | null
      color: 'indigo' | 'emerald'
    }>
  > = computed(() => {
    if (account.platform !== 'gemini') return []
    if (!usageInfo.value) return []

    const bars: Array<{
      key: string
      label: string
      utilization: number
      resetsAt: string | null
      windowStats?: WindowStats | null
      color: 'indigo' | 'emerald'
    }> = []

    if (geminiUsesSharedDaily.value) {
      const sharedDaily = usageInfo.value.gemini_shared_daily
      if (sharedDaily) {
        bars.push({
          key: 'shared_daily',
          label: '1d',
          utilization: sharedDaily.utilization,
          resetsAt: sharedDaily.resets_at,
          windowStats: sharedDaily.window_stats,
          color: 'indigo'
        })
      }
      return bars
    }

    const pro = usageInfo.value.gemini_pro_daily
    if (pro) {
      bars.push({
        key: 'pro_daily',
        label: 'pro',
        utilization: pro.utilization,
        resetsAt: pro.resets_at,
        windowStats: pro.window_stats,
        color: 'indigo'
      })
    }

    const flash = usageInfo.value.gemini_flash_daily
    if (flash) {
      bars.push({
        key: 'flash_daily',
        label: 'flash',
        utilization: flash.utilization,
        resetsAt: flash.resets_at,
        windowStats: flash.window_stats,
        color: 'emerald'
      })
    }

    return bars
  })

  const showGeminiTodayStats = computed(() => {
    return account.platform === 'gemini' && account.type === 'service_account'
  })

  return {
    geminiAuthTypeLabel,
    geminiTierClass,
    geminiQuotaPolicyChannel,
    geminiQuotaPolicyLimits,
    geminiQuotaPolicyDocsUrl,
    geminiUsageAvailable,
    geminiUsageBars,
    showGeminiTodayStats
  }
}

<template>
  <div ref="rootRef">
    <div v-if="antigravityTierLabel" class="mb-1 flex items-center gap-1">
      <span
        :class="[
          'inline-block rounded px-1.5 py-0.5 text-[10px] font-medium',
          antigravityTierClass
        ]"
      >
        {{ antigravityTierLabel }}
      </span>
      <span v-if="hasIneligibleTiers" class="group relative cursor-help">
        <svg class="h-3.5 w-3.5 text-red-500" fill="currentColor" viewBox="0 0 20 20">
          <path
            fill-rule="evenodd"
            d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7 4a1 1 0 11-2 0 1 1 0 012 0zm-1-9a1 1 0 00-1 1v4a1 1 0 102 0V6a1 1 0 00-1-1z"
            clip-rule="evenodd"
          />
        </svg>
        <span
          class="pointer-events-none absolute left-0 top-full z-50 mt-1 w-80 whitespace-normal break-words rounded bg-gray-900 px-3 py-2 text-xs leading-relaxed text-white opacity-0 shadow-lg transition-opacity group-hover:opacity-100 dark:bg-gray-700"
        >
          {{ t('admin.accounts.ineligibleWarning') }}
        </span>
      </span>
    </div>

    <div v-if="isForbidden" class="space-y-1">
      <span
        :class="[
          'inline-block rounded px-1.5 py-0.5 text-[10px] font-medium',
          forbiddenBadgeClass
        ]"
      >
        {{ forbiddenLabel }}
      </span>
      <div v-if="validationURL" class="flex items-center gap-1">
        <a
          :href="validationURL"
          target="_blank"
          rel="noopener noreferrer"
          class="text-[10px] text-blue-600 hover:text-blue-800 hover:underline dark:text-blue-400 dark:hover:text-blue-300"
          :title="t('admin.accounts.openVerification')"
        >
          {{ t('admin.accounts.openVerification') }}
        </a>
        <button
          type="button"
          class="text-[10px] text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200"
          :title="t('admin.accounts.copyLink')"
          @click="copyValidationURL"
        >
          {{ linkCopied ? t('admin.accounts.linkCopied') : t('admin.accounts.copyLink') }}
        </button>
      </div>
    </div>

    <div v-else-if="needsReauth" class="space-y-1">
      <span
        class="inline-block rounded px-1.5 py-0.5 text-[10px] font-medium bg-orange-100 text-orange-700 dark:bg-orange-900/40 dark:text-orange-300"
      >
        {{ t('admin.accounts.needsReauth') }}
      </span>
    </div>

    <div v-else-if="usageInfo?.error" class="space-y-1">
      <span
        class="inline-block rounded px-1.5 py-0.5 text-[10px] font-medium bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300"
      >
        {{ usageErrorLabel }}
      </span>
    </div>

    <div v-else-if="loading" class="space-y-1.5">
      <div class="flex items-center gap-1">
        <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
        <div class="h-1.5 w-8 animate-pulse rounded-full bg-gray-200 dark:bg-gray-700"></div>
        <div class="h-3 w-[32px] animate-pulse rounded bg-gray-200 dark:bg-gray-700"></div>
      </div>
    </div>

    <div v-else-if="error" class="text-xs text-red-500">
      {{ error }}
    </div>

    <div v-else-if="hasAntigravityQuotaFromAPI" class="space-y-1">
      <UsageProgressBar
        v-if="antigravity3ProUsageFromAPI !== null"
        :label="t('admin.accounts.usageWindow.gemini3Pro')"
        :utilization="antigravity3ProUsageFromAPI.utilization"
        :resets-at="antigravity3ProUsageFromAPI.resetTime"
        color="indigo"
      />
      <UsageProgressBar
        v-if="antigravity3FlashUsageFromAPI !== null"
        :label="t('admin.accounts.usageWindow.gemini3Flash')"
        :utilization="antigravity3FlashUsageFromAPI.utilization"
        :resets-at="antigravity3FlashUsageFromAPI.resetTime"
        color="emerald"
      />
      <UsageProgressBar
        v-if="antigravity3ImageUsageFromAPI !== null"
        :label="t('admin.accounts.usageWindow.gemini3Image')"
        :utilization="antigravity3ImageUsageFromAPI.utilization"
        :resets-at="antigravity3ImageUsageFromAPI.resetTime"
        color="purple"
      />
      <UsageProgressBar
        v-if="antigravityClaudeUsageFromAPI !== null"
        :label="t('admin.accounts.usageWindow.claude')"
        :utilization="antigravityClaudeUsageFromAPI.utilization"
        :resets-at="antigravityClaudeUsageFromAPI.resetTime"
        color="amber"
      />
      <div v-if="aiCreditsDisplay" class="mt-1 text-[10px] text-gray-500 dark:text-gray-400">
        💳 {{ t('admin.accounts.aiCreditsBalance') }}: {{ aiCreditsDisplay }}
      </div>
      <UpstreamQuotaSummary :quota="usageInfo?.upstream_quota" />
    </div>
    <div v-else-if="aiCreditsDisplay" class="text-[10px] text-gray-500 dark:text-gray-400">
      💳 {{ t('admin.accounts.aiCreditsBalance') }}: {{ aiCreditsDisplay }}
      <UpstreamQuotaSummary :quota="usageInfo?.upstream_quota" class="mt-1" />
    </div>
    <div v-else class="text-xs text-gray-400">-</div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import UsageProgressBar from '../UsageProgressBar.vue'
import UpstreamQuotaSummary from './UpstreamQuotaSummary.vue'
import {
  accountUsageCellPropDefaults,
  type AccountUsageCellProps
} from '../accountUsageCellProps'
import { useAccountUsageFetch } from './useAccountUsageFetch'
import { useAntigravityAccountMeta, useAntigravityQuota } from './useAntigravityQuota'

const props = withDefaults(defineProps<AccountUsageCellProps>(), accountUsageCellPropDefaults)

const { t } = useI18n()
const rootRef = ref<HTMLElement | null>(null)

const { loading, error, usageInfo } = useAccountUsageFetch(props, rootRef)

const {
  hasAntigravityQuotaFromAPI,
  antigravity3ProUsageFromAPI,
  antigravity3FlashUsageFromAPI,
  antigravity3ImageUsageFromAPI,
  antigravityClaudeUsageFromAPI,
  aiCreditsDisplay
} = useAntigravityQuota(usageInfo)

const { antigravityTier, hasIneligibleTiers } = useAntigravityAccountMeta(props.account)

const antigravityTierLabel = computed(() => {
  switch (antigravityTier.value) {
    case 'free-tier':
      return t('admin.accounts.tier.free')
    case 'g1-pro-tier':
      return t('admin.accounts.tier.pro')
    case 'g1-ultra-tier':
      return t('admin.accounts.tier.ultra')
    default:
      return null
  }
})

const antigravityTierClass = computed(() => {
  switch (antigravityTier.value) {
    case 'free-tier':
      return 'bg-gray-100 text-gray-600 dark:bg-gray-700 dark:text-gray-300'
    case 'g1-pro-tier':
      return 'bg-blue-100 text-blue-600 dark:bg-blue-900/40 dark:text-blue-300'
    case 'g1-ultra-tier':
      return 'bg-purple-100 text-purple-600 dark:bg-purple-900/40 dark:text-purple-300'
    default:
      return ''
  }
})

const isForbidden = computed(() => !!usageInfo.value?.is_forbidden)
const forbiddenType = computed(() => usageInfo.value?.forbidden_type || 'forbidden')
const validationURL = computed(() => usageInfo.value?.validation_url || '')
const needsReauth = computed(() => !!usageInfo.value?.needs_reauth)

const usageErrorLabel = computed(() => {
  const code = usageInfo.value?.error_code
  if (code === 'rate_limited') return t('admin.accounts.rateLimited')
  return t('admin.accounts.usageError')
})

const forbiddenLabel = computed(() => {
  switch (forbiddenType.value) {
    case 'validation':
      return t('admin.accounts.forbiddenValidation')
    case 'violation':
      return t('admin.accounts.forbiddenViolation')
    default:
      return t('admin.accounts.forbidden')
  }
})

const forbiddenBadgeClass = computed(() => {
  if (forbiddenType.value === 'validation') {
    return 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/40 dark:text-yellow-300'
  }
  return 'bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300'
})

const linkCopied = ref(false)
const copyValidationURL = async () => {
  if (!validationURL.value) return
  try {
    await navigator.clipboard.writeText(validationURL.value)
    linkCopied.value = true
    setTimeout(() => {
      linkCopied.value = false
    }, 2000)
  } catch {
    // fallback: ignore
  }
}
</script>

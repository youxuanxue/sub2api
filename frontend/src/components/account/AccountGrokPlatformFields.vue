<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'

withDefaults(
  defineProps<{
    variant?: 'create' | 'edit'
  }>(),
  {
    variant: 'create',
  }
)

const refreshToken = defineModel<string>('refreshToken', { required: true })
const baseUrl = defineModel<string>('baseUrl', { default: '' })

const { t } = useI18n()
</script>

<template>
  <div class="space-y-4">
    <!-- How to obtain the refresh_token: one-line hint (xAI public client is loopback-only). -->
    <div class="rounded-lg border border-slate-200 bg-slate-50 p-3 dark:border-slate-700/50 dark:bg-slate-800/30">
      <p class="text-xs text-slate-700 dark:text-slate-300">
        <Icon name="infoCircle" size="sm" class="mr-1 inline" :stroke-width="2" />
        {{ t('admin.accounts.grokPlatform.refreshTokenHowTo') }}
      </p>
    </div>

    <div>
      <label class="input-label">
        {{ t('admin.accounts.grokPlatform.refreshToken') }}
        <span v-if="variant === 'create'" class="text-red-500">*</span>
      </label>
      <textarea
        v-model="refreshToken"
        rows="3"
        spellcheck="false"
        :class="variant === 'create' ? 'input mt-1 font-mono text-xs' : 'input font-mono text-xs'"
        :placeholder="t('admin.accounts.grokPlatform.refreshTokenPlaceholder')"
      />
      <p class="input-hint">{{ t('admin.accounts.grokPlatform.refreshTokenHint') }}</p>
      <p v-if="variant === 'edit'" class="input-hint">{{ t('admin.accounts.grokPlatform.tokenEditHint') }}</p>
    </div>

    <div>
      <label class="input-label">{{ t('admin.accounts.grokPlatform.baseUrl') }}</label>
      <input
        v-model="baseUrl"
        type="text"
        :class="variant === 'create' ? 'input mt-1 font-mono' : 'input font-mono'"
        placeholder="https://api.x.ai/v1"
      />
      <p class="input-hint">{{ t('admin.accounts.grokPlatform.baseUrlHint') }}</p>
    </div>

    <!-- SuperGrok Heavy entitlement note: xAI gates the OAuth API surface to Heavy. -->
    <div class="rounded-lg border border-amber-200 bg-amber-50 p-3 dark:border-amber-800/40 dark:bg-amber-900/20">
      <p class="text-xs text-amber-800 dark:text-amber-200">
        <Icon name="exclamationTriangle" size="sm" class="mr-1 inline" :stroke-width="2" />
        {{ t('admin.accounts.grokPlatform.heavyNote') }}
      </p>
    </div>
  </div>
</template>

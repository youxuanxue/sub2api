<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'

withDefaults(
  defineProps<{
    channelTypeOptions: Array<{ value: number; label: string }>
    channelTypesLoading: boolean
    channelTypesError?: string | null
    selectedChannelTypeBaseUrl: string
    variant?: 'create' | 'edit'
  }>(),
  {
    channelTypesError: null,
    variant: 'create',
  }
)

const channelType = defineModel<number>('channelType', { required: true })
const baseUrl = defineModel<string>('baseUrl', { required: true })
const apiKey = defineModel<string>('apiKey', { required: true })

const { t } = useI18n()
</script>

<template>
  <div class="space-y-4">
    <div>
      <label class="input-label">
        {{ t('admin.accounts.newApiPlatform.channelType') }}
        <span v-if="variant === 'create'" class="text-red-500">*</span>
      </label>
      <Select
        v-model="channelType"
        :options="channelTypeOptions"
        :placeholder="t('admin.accounts.newApiPlatform.channelTypePlaceholder')"
        :loading="channelTypesLoading"
        searchable
        class="mt-1"
      />
      <p v-if="variant === 'create' && channelTypesError" class="mt-1 text-xs text-red-500">{{ channelTypesError }}</p>
    </div>
    <div>
      <label class="input-label">
        {{ t('admin.accounts.newApiPlatform.baseUrl') }}
        <span class="text-red-500">*</span>
      </label>
      <input
        v-model="baseUrl"
        type="text"
        :class="variant === 'create' ? 'input mt-1' : 'input'"
        :placeholder="selectedChannelTypeBaseUrl || 'https://api.deepseek.com'"
      />
      <p class="input-hint">{{ t('admin.accounts.newApiPlatform.baseUrlHint') }}</p>
    </div>
    <div>
      <label class="input-label">
        {{ t('admin.accounts.newApiPlatform.apiKey') }}
        <span v-if="variant === 'create'" class="text-red-500">*</span>
      </label>
      <input
        v-model="apiKey"
        type="password"
        :class="variant === 'create' ? 'input mt-1' : 'input'"
        :placeholder="t('admin.accounts.newApiPlatform.apiKeyPlaceholder')"
      />
      <p v-if="variant === 'edit'" class="input-hint">{{ t('admin.accounts.newApiPlatform.apiKeyEditHint') }}</p>
    </div>
  </div>
</template>

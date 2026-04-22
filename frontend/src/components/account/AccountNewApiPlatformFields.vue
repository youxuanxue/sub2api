<script setup lang="ts">
import { computed } from 'vue'
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

// US-019: three forwarding-affecting credentials. Bridge already reads
// model_mapping; openai_organization and status_code_mapping shipped in the
// same PR so admins can match the new-api channel UI without falling back
// to API-only configuration. All three are optional — empty values are
// skipped downstream by PopulateContextKeys.
const modelMapping = defineModel<string>('modelMapping', { default: '' })
const statusCodeMapping = defineModel<string>('statusCodeMapping', { default: '' })
const openaiOrganization = defineModel<string>('openaiOrganization', { default: '' })

const { t } = useI18n()

const modelMappingError = computed(() => validateOptionalJsonObject(modelMapping.value))
const statusCodeMappingError = computed(() => validateOptionalJsonObject(statusCodeMapping.value))

function validateOptionalJsonObject(raw: string): string {
  const s = (raw ?? '').trim()
  if (s === '') return ''
  try {
    const parsed = JSON.parse(s)
    if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
      return t('admin.accounts.newApiPlatform.jsonObjectRequired')
    }
  } catch {
    return t('admin.accounts.newApiPlatform.jsonInvalid')
  }
  return ''
}
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

    <!--
      US-019: optional forwarding-affecting fields. Each is a JSON-object
      textarea (or plain text for the OpenAI org id), validated locally so
      malformed JSON never reaches the bridge. Bridge skips empty/`{}`
      values, so leaving them blank is the documented "use upstream defaults"
      path.
    -->
    <div>
      <label class="input-label">{{ t('admin.accounts.newApiPlatform.modelMapping') }}</label>
      <textarea
        v-model="modelMapping"
        rows="3"
        spellcheck="false"
        :class="variant === 'create' ? 'input mt-1 font-mono text-xs' : 'input font-mono text-xs'"
        :placeholder="'{\n  &quot;gpt-4&quot;: &quot;gpt-4-turbo&quot;\n}'"
      />
      <p class="input-hint">{{ t('admin.accounts.newApiPlatform.modelMappingHint') }}</p>
      <p v-if="modelMappingError" class="mt-1 text-xs text-red-500">{{ modelMappingError }}</p>
    </div>
    <div>
      <label class="input-label">{{ t('admin.accounts.newApiPlatform.statusCodeMapping') }}</label>
      <textarea
        v-model="statusCodeMapping"
        rows="2"
        spellcheck="false"
        :class="variant === 'create' ? 'input mt-1 font-mono text-xs' : 'input font-mono text-xs'"
        :placeholder="'{\n  &quot;404&quot;: &quot;500&quot;\n}'"
      />
      <p class="input-hint">{{ t('admin.accounts.newApiPlatform.statusCodeMappingHint') }}</p>
      <p v-if="statusCodeMappingError" class="mt-1 text-xs text-red-500">{{ statusCodeMappingError }}</p>
    </div>
    <div>
      <label class="input-label">{{ t('admin.accounts.newApiPlatform.openaiOrganization') }}</label>
      <input
        v-model="openaiOrganization"
        type="text"
        :class="variant === 'create' ? 'input mt-1' : 'input'"
        placeholder="org-xxxxxxxxxxxxxxxx"
      />
      <p class="input-hint">{{ t('admin.accounts.newApiPlatform.openaiOrganizationHint') }}</p>
    </div>
  </div>
</template>

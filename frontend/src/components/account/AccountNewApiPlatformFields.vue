<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import ModelWhitelistSelector from '@/components/account/ModelWhitelistSelector.vue'
import { isValidWildcardPattern } from '@/composables/useModelWhitelist'

withDefaults(
  defineProps<{
    channelTypeOptions: Array<{ value: number; label: string }>
    channelTypesLoading: boolean
    channelTypesError?: string | null
    selectedChannelTypeBaseUrl: string
    variant?: 'create' | 'edit'
    /**
     * Show the «获取模型列表» button. Parent computes this from
     * `isNewApiUpstreamFetchableChannelType(channelType)` so unknown / non-fetchable
     * channels (e.g. Bedrock-style auth) do not display a misleading button.
     */
    fetchModelsEnabled?: boolean
    /** Disable the fetch button while in-flight or when base_url / api_key are insufficient. */
    fetchModelsDisabled?: boolean
    /** Spinner state for the fetch button. */
    fetchModelsLoading?: boolean
  }>(),
  {
    channelTypesError: null,
    variant: 'create',
    fetchModelsEnabled: false,
    fetchModelsDisabled: false,
    fetchModelsLoading: false,
  }
)

const emit = defineEmits<{
  fetchModels: []
}>()

const channelType = defineModel<number>('channelType', { required: true })
const baseUrl = defineModel<string>('baseUrl', { required: true })
const apiKey = defineModel<string>('apiKey', { required: true })

// US-019: two forwarding-affecting transit credentials kept as raw fields:
//   - status_code_mapping: rewrites upstream HTTP code → another code
//   - openai_organization: outbound OpenAI-Organization header
// `model_mapping` was previously also a raw JSON textarea here, but it
// **collides** with the structured whitelist/mapping selector below
// (both target `credentials.model_mapping` server-side). The selector wins;
// raw JSON entry is dropped from the UI to remove the dual-source bug.
// The `modelMapping` defineModel is preserved for backwards compatibility
// with parents that still pass v-model (Edit modal sync path), but the
// textarea is no longer rendered.
// Kept as a defineModel so v-model:modelMapping parents (Edit modal sync path)
// don't break — but the textarea no longer renders. ESLint vue/no-ref-as-operand
// requires either reading .value or removing the binding; we use a noop read
// inside a discardable expression to make the intent explicit.
const modelMapping = defineModel<string>('modelMapping', { default: '' })
const _modelMappingNoop = (): string | undefined => modelMapping.value
void _modelMappingNoop
const statusCodeMapping = defineModel<string>('statusCodeMapping', { default: '' })
const openaiOrganization = defineModel<string>('openaiOrganization', { default: '' })

// New (D3 + D4): structured model selection. Bound to allowedModels when
// `restrictionMode === 'whitelist'`, or to modelMappings when 'mapping'.
const allowedModels = defineModel<string[]>('allowedModels', { default: () => [] })
const modelMappings = defineModel<{ from: string; to: string }[]>('modelMappings', { default: () => [] })
const restrictionMode = defineModel<'whitelist' | 'mapping'>('restrictionMode', { default: 'whitelist' })

const { t } = useI18n()

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

function addMapping(): void {
  modelMappings.value = [...modelMappings.value, { from: '', to: '' }]
}

function removeMapping(index: number): void {
  modelMappings.value = modelMappings.value.filter((_, i) => i !== index)
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
      D3 + D4: structured model section, mirrors native new-api 添加渠道 «模型».
      Whitelist mode = exact list of allowed models (writes credentials.model_mapping = {model: model}).
      Mapping mode = src→dst rewrite (writes credentials.model_mapping = {from: to}).
      «获取模型列表» button shown only when channel_type is in the upstream-fetchable set
      (mirrors new-api MODEL_FETCHABLE_CHANNEL_TYPES); button itself disabled until base_url + api_key
      (or stored account credential) are present — the parent owns this gating because the «edit account
      with stored key» fallback is a parent-side concern.
    -->
    <div>
      <label class="input-label">{{ t('admin.accounts.newApiPlatform.models') }}</label>

      <div class="mb-3 mt-1 flex gap-2">
        <button
          type="button"
          @click="restrictionMode = 'whitelist'"
          :class="[
            'flex-1 rounded-lg px-3 py-1.5 text-xs font-medium transition-all',
            restrictionMode === 'whitelist'
              ? 'bg-primary-100 text-primary-700 dark:bg-primary-900/30 dark:text-primary-400'
              : 'bg-gray-100 text-gray-600 hover:bg-gray-200 dark:bg-dark-600 dark:text-gray-400 dark:hover:bg-dark-500'
          ]"
        >
          {{ t('admin.accounts.modelWhitelist') }}
        </button>
        <button
          type="button"
          @click="restrictionMode = 'mapping'"
          :class="[
            'flex-1 rounded-lg px-3 py-1.5 text-xs font-medium transition-all',
            restrictionMode === 'mapping'
              ? 'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-400'
              : 'bg-gray-100 text-gray-600 hover:bg-gray-200 dark:bg-dark-600 dark:text-gray-400 dark:hover:bg-dark-500'
          ]"
        >
          {{ t('admin.accounts.modelMapping') }}
        </button>
      </div>

      <div v-if="restrictionMode === 'whitelist'">
        <ModelWhitelistSelector v-model="allowedModels" platform="newapi" />
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.accounts.selectedModels', { count: allowedModels.length }) }}
          <span v-if="allowedModels.length === 0">{{ t('admin.accounts.supportsAllModels') }}</span>
        </p>
      </div>

      <div v-else>
        <div v-if="modelMappings.length > 0" class="mb-3 space-y-2">
          <div
            v-for="(m, index) in modelMappings"
            :key="index"
            class="space-y-1"
          >
            <div class="flex items-center gap-2">
              <input
                v-model="m.from"
                type="text"
                :class="['input flex-1', !isValidWildcardPattern(m.from) ? 'border-red-500 dark:border-red-500' : '']"
                :placeholder="t('admin.accounts.requestModel')"
              />
              <Icon name="arrowRight" size="sm" class="flex-shrink-0 text-gray-400" />
              <input
                v-model="m.to"
                type="text"
                :class="['input flex-1', m.to.includes('*') ? 'border-red-500 dark:border-red-500' : '']"
                :placeholder="t('admin.accounts.actualModel')"
              />
              <button
                type="button"
                @click="removeMapping(index)"
                class="rounded-lg p-2 text-red-500 transition-colors hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20"
              >
                <Icon name="trash" size="sm" />
              </button>
            </div>
            <p v-if="!isValidWildcardPattern(m.from)" class="text-xs text-red-500">
              {{ t('admin.accounts.wildcardOnlyAtEnd') }}
            </p>
            <p v-if="m.to.includes('*')" class="text-xs text-red-500">
              {{ t('admin.accounts.targetNoWildcard') }}
            </p>
          </div>
        </div>
        <button
          type="button"
          @click="addMapping"
          class="w-full rounded-lg border-2 border-dashed border-gray-300 px-4 py-2 text-sm text-gray-600 transition-colors hover:border-gray-400 hover:text-gray-700 dark:border-dark-500 dark:text-gray-400 dark:hover:border-dark-400 dark:hover:text-gray-300"
        >
          + {{ t('admin.accounts.addMapping') }}
        </button>
      </div>

      <div class="mt-3 flex flex-wrap items-center gap-2">
        <button
          v-if="fetchModelsEnabled"
          type="button"
          :disabled="fetchModelsDisabled || fetchModelsLoading"
          @click="emit('fetchModels')"
          class="btn btn-secondary text-xs disabled:cursor-not-allowed disabled:opacity-50"
        >
          <Icon
            v-if="fetchModelsLoading"
            name="refresh"
            size="sm"
            class="mr-1 animate-spin"
          />
          {{ t('admin.accounts.newApiPlatform.fetchUpstreamModels') }}
        </button>
        <span v-if="fetchModelsEnabled" class="text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.accounts.newApiPlatform.fetchUpstreamModelsHint') }}
        </span>
      </div>
    </div>

    <!--
      US-019: status_code_mapping + openai_organization remain raw fields
      because they are *transit* concerns (response code rewrite + outbound
      header), orthogonal to model selection. model_mapping is no longer
      exposed as a JSON textarea here — the structured selector above writes
      it to credentials.model_mapping with the same shape the bridge expects.
    -->
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

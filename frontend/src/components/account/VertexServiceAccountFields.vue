<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { VERTEX_LOCATION_OPTIONS } from '@/constants/account'
import type { VertexServiceAccountFieldBag } from '@/composables/useVertexServiceAccountFields'

const props = withDefaults(
  defineProps<{
    fields: VertexServiceAccountFieldBag
    variant?: 'create' | 'edit'
    jsonWriteOnce?: boolean
    /** 是否提供 API Key 认证切换（仅 create + Gemini 平台；edit 模式由账号现有 auth_mode 决定）。 */
    allowApiKeyMode?: boolean
  }>(),
  {
    variant: 'create',
    jsonWriteOnce: false,
    allowApiKeyMode: false
  }
)

const fields = props.fields
const { t } = useI18n()

const jsonInputModel = computed({
  get: () => fields.jsonInput.value,
  set: (value: string) => {
    fields.jsonInput.value = value
  }
})

const locationModel = computed({
  get: () => fields.location.value,
  set: (value: string) => {
    fields.location.value = value
  }
})

const jsonHintKey = () => {
  if (props.variant === 'create') {
    return 'admin.accounts.vertexSaJsonUploadHint'
  }
  return props.jsonWriteOnce
    ? 'admin.accounts.vertexSaJsonEditWriteOnceHint'
    : 'admin.accounts.vertexSaJsonEditHint'
}

const jsonPlaceholderKey = () => {
  if (props.variant === 'create') {
    return 'admin.accounts.vertexSaJsonPastePlaceholder'
  }
  return props.jsonWriteOnce
    ? 'admin.accounts.vertexSaJsonEditWriteOncePlaceholder'
    : 'admin.accounts.vertexSaJsonPastePlaceholder'
}
</script>

<template>
  <div class="space-y-4">
    <!-- API Key 认证切换（upstream 2026-10）：仅创建 Gemini Vertex 账号时提供。 -->
    <div v-if="allowApiKeyMode">
      <label class="input-label">{{ t('admin.accounts.vertexAuthMode') }}</label>
      <div class="mt-2 flex gap-4">
        <label class="flex cursor-pointer items-center">
          <input
            v-model="fields.authMode.value"
            type="radio"
            value="service_account"
            data-testid="vertex-auth-mode-serviceaccount"
            class="mr-2 text-primary-600 focus:ring-primary-500"
          />
          <span class="text-sm text-gray-700 dark:text-gray-300">{{ t('admin.accounts.vertexAuthModeServiceAccount') }}</span>
        </label>
        <label class="flex cursor-pointer items-center">
          <input
            v-model="fields.authMode.value"
            type="radio"
            value="apikey"
            data-testid="vertex-auth-mode-apikey"
            class="mr-2 text-primary-600 focus:ring-primary-500"
          />
          <span class="text-sm text-gray-700 dark:text-gray-300">{{ t('admin.accounts.vertexAuthModeApiKey') }}</span>
        </label>
      </div>
    </div>

    <!-- API Key 认证字段 -->
    <div v-if="fields.isApiKeyMode.value">
      <label class="input-label">API Key</label>
      <input
        v-model="fields.apiKey.value"
        type="password"
        class="input font-mono"
        :data-testid="variant === 'edit' ? 'edit-vertex-api-key-input' : 'vertex-api-key-input'"
        :placeholder="variant === 'edit' ? t('admin.accounts.leaveEmptyToKeep') : ''"
      />
      <p class="input-hint">{{ t('admin.accounts.vertexApiKeyHint') }}</p>
    </div>

    <div v-if="!fields.isApiKeyMode.value">
      <label class="input-label">{{ t('admin.accounts.vertexSaJsonLabel') }}</label>
      <input
        :ref="(el) => { fields.fileInputRef.value = el as HTMLInputElement | null }"
        type="file"
        accept="application/json,.json"
        class="hidden"
        @change="fields.handleFileChange"
      />
      <div
        :class="[
          'rounded-lg border-2 border-dashed px-4 py-5 transition-colors',
          fields.dragActive.value
            ? 'border-sky-500 bg-sky-50 dark:border-sky-500 dark:bg-sky-900/20'
            : 'border-gray-300 bg-gray-50 hover:border-sky-400 hover:bg-sky-50/60 dark:border-dark-500 dark:bg-dark-700/40 dark:hover:border-sky-600 dark:hover:bg-sky-900/10'
        ]"
        @dragenter.prevent="fields.dragActive.value = true"
        @dragover.prevent="fields.dragActive.value = true"
        @dragleave.prevent="fields.dragActive.value = false"
        @drop.prevent="fields.handleFileDrop"
      >
        <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div class="min-w-0">
            <div class="flex items-center gap-2 text-sm font-medium text-gray-900 dark:text-white">
              <Icon name="upload" size="sm" />
              <span>{{
                fields.isLoaded.value
                  ? t('admin.accounts.vertexSaJsonLoaded')
                  : t('admin.accounts.vertexSaJsonDrop')
              }}</span>
            </div>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              {{
                fields.isLoaded.value
                  ? t('admin.accounts.vertexSaJsonKeyHidden')
                  : t('admin.accounts.vertexSaJsonDropHint')
              }}
            </p>
          </div>
          <button
            type="button"
            class="btn btn-secondary shrink-0"
            @click="fields.fileInputRef.value?.click()"
          >
            <Icon name="upload" size="sm" />
            {{ t('admin.accounts.vertexSaJsonSelectBtn') }}
          </button>
        </div>
        <div
          v-if="fields.isLoaded.value"
          class="mt-3 rounded-md border border-sky-200 bg-white px-3 py-2 text-xs text-sky-900 dark:border-sky-800/50 dark:bg-dark-800 dark:text-sky-200"
        >
          <div class="truncate">
            Project ID:
            <span class="font-mono">{{ fields.projectId.value }}</span>
          </div>
          <div class="truncate">
            Client Email:
            <span class="font-mono">{{ fields.clientEmail.value }}</span>
          </div>
        </div>
      </div>
      <textarea
        v-model="jsonInputModel"
        rows="4"
        spellcheck="false"
        autocomplete="off"
        data-testid="vertex-sa-json-input"
        class="input mt-3 font-mono text-xs"
        :placeholder="t(jsonPlaceholderKey())"
        @change="fields.previewJsonInput()"
      />
      <p class="input-hint">{{ t(jsonHintKey()) }}</p>
    </div>

    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
      <div>
        <label class="input-label">Project ID</label>
        <input
          v-if="fields.isApiKeyMode.value"
          v-model="fields.projectId.value"
          type="text"
          class="input font-mono"
          :data-testid="variant === 'edit' ? 'edit-vertex-api-key-project-id-input' : 'vertex-api-key-project-id-input'"
          :placeholder="t('admin.accounts.vertexApiKeyProjectIdPlaceholder')"
        />
        <input
          v-else
          :value="fields.projectId.value"
          type="text"
          class="input font-mono"
          readonly
          :placeholder="t('admin.accounts.vertexProjectIdPlaceholder')"
        />
        <p v-if="fields.isApiKeyMode.value" class="input-hint">
          {{ t('admin.accounts.vertexApiKeyProjectIdHint') }}
        </p>
      </div>
      <div>
        <label class="input-label">Location</label>
        <select v-model="locationModel" required class="input font-mono">
          <optgroup
            v-for="group in VERTEX_LOCATION_OPTIONS"
            :key="group.label"
            :label="group.label"
          >
            <option v-for="option in group.options" :key="option.value" :value="option.value">
              {{ option.label }}
            </option>
          </optgroup>
        </select>
        <p class="input-hint">{{ t('admin.accounts.vertexLocationHint') }}</p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { KiroAuthMethod } from '@/composables/useTkAccountKiroPlatform'

withDefaults(
  defineProps<{
    variant?: 'create' | 'edit'
  }>(),
  {
    variant: 'create',
  }
)

const accessToken = defineModel<string>('accessToken', { required: true })
const refreshToken = defineModel<string>('refreshToken', { required: true })
const region = defineModel<string>('region', { required: true })
const authMethod = defineModel<KiroAuthMethod>('authMethod', { required: true })
const machineId = defineModel<string>('machineId', { default: '' })
const clientId = defineModel<string>('clientId', { default: '' })
const clientSecret = defineModel<string>('clientSecret', { default: '' })
const profileArn = defineModel<string>('profileArn', { default: '' })
const tosAcknowledged = defineModel<boolean>('tosAcknowledged', { default: false })

const { t } = useI18n()
</script>

<template>
  <div class="space-y-4">
    <div>
      <label class="input-label">
        {{ t('admin.accounts.kiroPlatform.accessToken') }}
        <span v-if="variant === 'create'" class="text-red-500">*</span>
      </label>
      <textarea
        v-model="accessToken"
        rows="2"
        spellcheck="false"
        :class="variant === 'create' ? 'input mt-1 font-mono text-xs' : 'input font-mono text-xs'"
        :placeholder="t('admin.accounts.kiroPlatform.accessTokenPlaceholder')"
      />
      <p v-if="variant === 'edit'" class="input-hint">{{ t('admin.accounts.kiroPlatform.tokenEditHint') }}</p>
    </div>

    <div>
      <label class="input-label">
        {{ t('admin.accounts.kiroPlatform.refreshToken') }}
        <span v-if="variant === 'create'" class="text-red-500">*</span>
      </label>
      <textarea
        v-model="refreshToken"
        rows="2"
        spellcheck="false"
        :class="variant === 'create' ? 'input mt-1 font-mono text-xs' : 'input font-mono text-xs'"
        :placeholder="t('admin.accounts.kiroPlatform.refreshTokenPlaceholder')"
      />
    </div>

    <div>
      <label class="input-label">{{ t('admin.accounts.kiroPlatform.region') }}</label>
      <input
        v-model="region"
        type="text"
        :class="variant === 'create' ? 'input mt-1' : 'input'"
        placeholder="us-east-1"
      />
      <p class="input-hint">{{ t('admin.accounts.kiroPlatform.regionHint') }}</p>
    </div>

    <div>
      <label class="input-label">
        {{ t('admin.accounts.kiroPlatform.authMethod') }}
        <span class="text-red-500">*</span>
      </label>
      <select
        v-model="authMethod"
        :class="variant === 'create' ? 'input mt-1' : 'input'"
      >
        <option value="social">{{ t('admin.accounts.kiroPlatform.authMethodSocial') }}</option>
        <option value="idc">{{ t('admin.accounts.kiroPlatform.authMethodIdc') }}</option>
      </select>
      <p class="input-hint">{{ t('admin.accounts.kiroPlatform.authMethodHint') }}</p>
    </div>

    <!-- IDC-only credentials: client_id + client_secret required when auth_method=idc. -->
    <template v-if="authMethod === 'idc'">
      <div>
        <label class="input-label">
          {{ t('admin.accounts.kiroPlatform.clientId') }}
          <span class="text-red-500">*</span>
        </label>
        <input
          v-model="clientId"
          type="text"
          :class="variant === 'create' ? 'input mt-1 font-mono' : 'input font-mono'"
          :placeholder="t('admin.accounts.kiroPlatform.clientIdPlaceholder')"
        />
      </div>
      <div>
        <label class="input-label">
          {{ t('admin.accounts.kiroPlatform.clientSecret') }}
          <span v-if="variant === 'create'" class="text-red-500">*</span>
        </label>
        <input
          v-model="clientSecret"
          type="password"
          :class="variant === 'create' ? 'input mt-1 font-mono' : 'input font-mono'"
          :placeholder="t('admin.accounts.kiroPlatform.clientSecretPlaceholder')"
        />
        <p v-if="variant === 'edit'" class="input-hint">{{ t('admin.accounts.kiroPlatform.tokenEditHint') }}</p>
      </div>
    </template>

    <div>
      <label class="input-label">{{ t('admin.accounts.kiroPlatform.machineId') }}</label>
      <input
        v-model="machineId"
        type="text"
        :class="variant === 'create' ? 'input mt-1 font-mono' : 'input font-mono'"
        :placeholder="t('admin.accounts.kiroPlatform.machineIdPlaceholder')"
      />
      <p class="input-hint">{{ t('admin.accounts.kiroPlatform.machineIdHint') }}</p>
    </div>

    <div>
      <label class="input-label">{{ t('admin.accounts.kiroPlatform.profileArn') }}</label>
      <input
        v-model="profileArn"
        type="text"
        :class="variant === 'create' ? 'input mt-1 font-mono' : 'input font-mono'"
        :placeholder="t('admin.accounts.kiroPlatform.profileArnPlaceholder')"
      />
      <p class="input-hint">{{ t('admin.accounts.kiroPlatform.profileArnHint') }}</p>
    </div>

    <!-- ToS acknowledgement: backend forces tos_acknowledged=true to create. -->
    <div class="rounded-lg border border-amber-200 bg-amber-50 p-3 dark:border-amber-800/40 dark:bg-amber-900/20">
      <label class="flex cursor-pointer items-start gap-2">
        <input
          v-model="tosAcknowledged"
          type="checkbox"
          class="mt-0.5 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-500"
        />
        <span class="text-xs text-amber-800 dark:text-amber-200">
          <Icon name="exclamationTriangle" size="sm" class="mr-1 inline" :stroke-width="2" />
          {{ t('admin.accounts.kiroPlatform.tosAcknowledge') }}
        </span>
      </label>
    </div>
  </div>
</template>

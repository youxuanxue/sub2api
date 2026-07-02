<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { KiroPlatformFieldBag } from '@/composables/useTkAccountKiroPlatform'

const props = withDefaults(
  defineProps<{
    fields: KiroPlatformFieldBag
    variant?: 'create' | 'edit'
    jsonWriteOnce?: boolean
  }>(),
  {
    variant: 'create',
    jsonWriteOnce: false
  }
)

const fields = props.fields
const { t } = useI18n()

const tokenJsonModel = computed({
  get: () => fields.tokenJsonInput.value,
  set: (value: string) => {
    fields.tokenJsonInput.value = value
  }
})

const registrationJsonModel = computed({
  get: () => fields.registrationJsonInput.value,
  set: (value: string) => {
    fields.registrationJsonInput.value = value
  }
})

const regionModel = computed({
  get: () => fields.region.value,
  set: (value: string) => {
    fields.region.value = value
  }
})

const authMethodModel = computed({
  get: () => fields.authMethod.value,
  set: (value: 'social' | 'idc') => {
    fields.authMethod.value = value
  }
})

const machineIdModel = computed({
  get: () => fields.machineId.value,
  set: (value: string) => {
    fields.machineId.value = value
  }
})

const profileArnModel = computed({
  get: () => fields.profileArn.value,
  set: (value: string) => {
    fields.profileArn.value = value
  }
})

const tosModel = computed({
  get: () => fields.tosAcknowledged.value,
  set: (value: boolean) => {
    fields.tosAcknowledged.value = value
  }
})

const tokenHintKey = () => {
  if (props.variant === 'create') {
    return 'admin.accounts.kiroPlatform.tokenJsonCreateHint'
  }
  return props.jsonWriteOnce
    ? 'admin.accounts.kiroPlatform.tokenJsonEditWriteOnceHint'
    : 'admin.accounts.kiroPlatform.tokenJsonEditHint'
}

const registrationHintKey = () => {
  if (props.variant === 'create') {
    return 'admin.accounts.kiroPlatform.registrationJsonCreateHint'
  }
  return props.jsonWriteOnce
    ? 'admin.accounts.kiroPlatform.registrationJsonEditWriteOnceHint'
    : 'admin.accounts.kiroPlatform.registrationJsonEditHint'
}
</script>

<template>
  <div class="space-y-4">
    <div>
      <label class="input-label">
        {{ t('admin.accounts.kiroPlatform.tokenJsonLabel') }}
        <span v-if="variant === 'create'" class="text-red-500">*</span>
      </label>
      <textarea
        v-model="tokenJsonModel"
        data-testid="kiro-token-json-input"
        rows="5"
        spellcheck="false"
        :class="variant === 'create' ? 'input mt-1 font-mono text-xs' : 'input font-mono text-xs'"
        :placeholder="t('admin.accounts.kiroPlatform.tokenJsonPlaceholder')"
        @blur="fields.previewTokenJsonInput()"
      />
      <p class="input-hint">{{ t(tokenHintKey()) }}</p>
      <p
        v-if="fields.tokenLoaded.value"
        class="mt-1 text-xs text-emerald-700 dark:text-emerald-300"
      >
        <Icon name="checkCircle" size="sm" class="mr-1 inline" :stroke-width="2" />
        {{ t('admin.accounts.kiroPlatform.tokenJsonParsed') }}
      </p>
    </div>

    <div>
      <label class="input-label">{{ t('admin.accounts.kiroPlatform.region') }}</label>
      <input
        v-model="regionModel"
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
        v-model="authMethodModel"
        :class="variant === 'create' ? 'input mt-1' : 'input'"
      >
        <option value="social">{{ t('admin.accounts.kiroPlatform.authMethodSocial') }}</option>
        <option value="idc">{{ t('admin.accounts.kiroPlatform.authMethodIdc') }}</option>
      </select>
      <p class="input-hint">{{ t('admin.accounts.kiroPlatform.authMethodHint') }}</p>
    </div>

    <div v-if="authMethodModel === 'idc'">
      <label class="input-label">
        {{ t('admin.accounts.kiroPlatform.registrationJsonLabel') }}
        <span v-if="variant === 'create'" class="text-red-500">*</span>
      </label>
      <textarea
        v-model="registrationJsonModel"
        data-testid="kiro-registration-json-input"
        rows="4"
        spellcheck="false"
        :class="variant === 'create' ? 'input mt-1 font-mono text-xs' : 'input font-mono text-xs'"
        :placeholder="t('admin.accounts.kiroPlatform.registrationJsonPlaceholder')"
        @blur="fields.previewRegistrationJsonInput()"
      />
      <p class="input-hint">{{ t(registrationHintKey()) }}</p>
      <p
        v-if="fields.registrationLoaded.value"
        class="mt-1 text-xs text-emerald-700 dark:text-emerald-300"
      >
        <Icon name="checkCircle" size="sm" class="mr-1 inline" :stroke-width="2" />
        {{
          t('admin.accounts.kiroPlatform.registrationJsonParsed', {
            clientId: fields.registrationClientIdPreview.value
          })
        }}
      </p>
    </div>

    <div>
      <label class="input-label">{{ t('admin.accounts.kiroPlatform.machineId') }}</label>
      <input
        v-model="machineIdModel"
        type="text"
        :class="variant === 'create' ? 'input mt-1 font-mono' : 'input font-mono'"
        :placeholder="t('admin.accounts.kiroPlatform.machineIdPlaceholder')"
      />
      <p class="input-hint">{{ t('admin.accounts.kiroPlatform.machineIdHint') }}</p>
    </div>

    <div>
      <label class="input-label">{{ t('admin.accounts.kiroPlatform.profileArn') }}</label>
      <input
        v-model="profileArnModel"
        type="text"
        :class="variant === 'create' ? 'input mt-1 font-mono' : 'input font-mono'"
        :placeholder="t('admin.accounts.kiroPlatform.profileArnPlaceholder')"
      />
      <p class="input-hint">{{ t('admin.accounts.kiroPlatform.profileArnHint') }}</p>
    </div>

    <div class="rounded-lg border border-amber-200 bg-amber-50 p-3 dark:border-amber-800/40 dark:bg-amber-900/20">
      <label class="flex cursor-pointer items-start gap-2">
        <input
          v-model="tosModel"
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

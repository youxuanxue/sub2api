<template>
  <div class="space-y-4 border-t border-gray-200 pt-4 dark:border-dark-600" data-testid="auto-reset-credit-settings">
    <div class="flex items-center justify-between gap-4">
      <label class="input-label mb-0" for="auto-reset-credit-enabled">{{ t('admin.accounts.autoResetCredit.title') }}</label>
      <button
        id="auto-reset-credit-enabled"
        type="button"
        role="switch"
        :aria-checked="enabled"
        :aria-label="t('admin.accounts.autoResetCredit.title')"
        data-testid="auto-reset-credit-enabled"
        class="relative inline-flex h-6 w-11 shrink-0 rounded-full border-2 border-transparent transition-colors"
        :class="enabled ? 'bg-primary-600' : 'bg-gray-200 dark:bg-dark-600'"
        @click="enabled = !enabled"
      >
        <span class="inline-block h-5 w-5 rounded-full bg-white transition-transform" :class="enabled ? 'translate-x-5' : 'translate-x-0'" />
      </button>
    </div>
    <div class="grid gap-4 sm:grid-cols-2">
      <label class="input-label">
        {{ t('admin.accounts.autoResetCredit.threshold5h') }}
        <input v-model.number="fiveHour" type="number" min="0.1" max="100" step="0.1" class="input" :disabled="!enabled" data-testid="auto-reset-credit-5h-threshold" />
      </label>
      <label class="input-label">
        {{ t('admin.accounts.autoResetCredit.threshold7d') }}
        <input v-model.number="sevenDay" type="number" min="0.1" max="100" step="0.1" class="input" :disabled="!enabled" data-testid="auto-reset-credit-7d-threshold" />
      </label>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'

const enabled = defineModel<boolean>('enabled', { required: true })
const fiveHour = defineModel<number>('fiveHour', { required: true })
const sevenDay = defineModel<number>('sevenDay', { required: true })
const { t } = useI18n()
</script>

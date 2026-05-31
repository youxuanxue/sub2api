<template>
  <Teleport to="body">
    <div v-if="show" class="fixed inset-0 z-[9998] flex items-center justify-center">
      <!-- backdrop -->
      <div class="absolute inset-0 bg-black/40" @click="$emit('close')"></div>

      <!-- dialog -->
      <div
        class="relative z-[9999] w-[28rem] max-w-[92vw] overflow-hidden rounded-xl bg-white shadow-xl ring-1 ring-black/5 dark:bg-dark-800"
        @click.stop
      >
        <div class="border-b border-gray-100 px-5 py-4 dark:border-dark-700">
          <h3 class="text-base font-semibold text-gray-900 dark:text-gray-100">
            {{ t('admin.accounts.setTierDialog.title') }}
          </h3>
          <p v-if="account" class="mt-1 truncate text-sm text-gray-500 dark:text-gray-400">
            {{ account.name }}
          </p>
        </div>

        <div class="px-5 py-4">
          <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t('admin.accounts.setTierDialog.selectLabel') }}
          </label>
          <div class="space-y-2">
            <label
              v-for="opt in tierOptions"
              :key="opt.value"
              class="flex cursor-pointer items-start gap-3 rounded-lg border px-3 py-2 transition-colors"
              :class="modelValue === opt.value
                ? 'border-emerald-500 bg-emerald-50 dark:border-emerald-500/60 dark:bg-emerald-500/10'
                : 'border-gray-200 hover:bg-gray-50 dark:border-dark-700 dark:hover:bg-dark-700'"
            >
              <input
                type="radio"
                class="mt-1"
                :value="opt.value"
                :checked="modelValue === opt.value"
                @change="$emit('update:modelValue', opt.value)"
              />
              <span class="flex flex-col">
                <span class="text-sm font-semibold text-gray-900 dark:text-gray-100">{{ opt.label }}</span>
                <span class="text-xs text-gray-500 dark:text-gray-400">{{ opt.hint }}</span>
              </span>
            </label>
          </div>

          <!-- local-deployment-only warning: applying a tier here only writes
               THIS deployment's DB; fleet fan-out still needs the pipeline. -->
          <p class="mt-3 rounded-md bg-amber-50 px-3 py-2 text-xs text-amber-700 dark:bg-amber-500/10 dark:text-amber-300">
            {{ t('admin.accounts.setTierDialog.localScopeWarning') }}
          </p>
        </div>

        <div class="flex justify-end gap-2 border-t border-gray-100 px-5 py-3 dark:border-dark-700">
          <button
            class="rounded-lg px-4 py-2 text-sm text-gray-600 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-700"
            @click="$emit('close')"
          >
            {{ t('common.cancel') }}
          </button>
          <button
            class="rounded-lg bg-emerald-600 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-700 disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="!modelValue || submitting"
            @click="$emit('apply')"
          >
            {{ submitting ? t('common.processing') : t('admin.accounts.setTierDialog.applyButton') }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { Account } from '@/types'
import type { AccountTierOption } from '@/constants/accountTierOptions.tk'

defineProps<{
  show: boolean
  account: Account | null
  modelValue: string
  tierOptions: AccountTierOption[]
  submitting: boolean
}>()

defineEmits(['close', 'apply', 'update:modelValue'])

const { t } = useI18n()
</script>

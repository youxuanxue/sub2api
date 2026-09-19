<template>
  <BaseDialog
    :show="show" :title="t('admin.accounts.duplicateAccount')" width="narrow"
    :close-on-escape="!submitting" :show-close-button="!submitting" @close="close"
  >
    <form id="duplicate-account-form" class="space-y-4" @submit.prevent="submit">
      <p class="break-words text-sm text-gray-600 dark:text-gray-300">{{ t('admin.accounts.duplicatePrompt', { name: account?.name }) }}</p>
      <label for="duplicate-account-count" class="block text-sm font-medium text-gray-700 dark:text-gray-200">{{ t('admin.accounts.duplicateCount') }}</label>
      <input id="duplicate-account-count" v-model.number="count" type="number" min="1" max="100" step="1" required
        :disabled="submitting" class="input w-full" data-testid="duplicate-account-count" />
      <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.duplicateHint') }}</p>
      <p v-if="!valid" role="alert" class="text-sm text-red-600">{{ t('admin.accounts.duplicateCountInvalid') }}</p>
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
    </form>
    <template #footer>
      <button type="button" class="btn btn-secondary" :disabled="submitting" @click="close">{{ t('common.cancel') }}</button>
      <button type="submit" form="duplicate-account-form" class="btn btn-primary" :disabled="!valid || submitting">
        {{ submitting ? t('common.loading') : t('admin.accounts.duplicateAccount') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { Account } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{ show: boolean; account: Account | null }>()
const emit = defineEmits<{ close: []; duplicated: [accounts: Account[]] }>()
const { t } = useI18n()
const count = ref(1)
const submitting = ref(false)
const error = ref('')
const valid = computed(() => Number.isInteger(count.value) && count.value >= 1 && count.value <= 100)
watch(() => props.show, show => { if (show) { count.value = 1; error.value = '' } })
const close = () => { if (!submitting.value) emit('close') }
async function submit() {
  if (!props.account || !valid.value || submitting.value) return
  submitting.value = true
  error.value = ''
  try {
    const accounts = await adminAPI.accounts.duplicateMany(props.account.id, count.value)
    emit('duplicated', accounts)
    emit('close')
  } catch (err) {
    error.value = extractApiErrorMessage(err, t('admin.accounts.duplicateFailed'))
  } finally {
    submitting.value = false
  }
}
</script>

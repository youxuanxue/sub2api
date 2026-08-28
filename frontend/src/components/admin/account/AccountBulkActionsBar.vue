<template>
  <div class="mb-4 flex items-center justify-between rounded-lg bg-primary-50 p-3 dark:bg-primary-900/20">
    <div class="flex flex-wrap items-center gap-2">
      <span v-if="allResultsSelected" class="text-sm font-medium text-primary-900 dark:text-primary-100">
        {{ t('admin.accounts.bulkActions.selectedAll', { count: selectedIds.length }) }}
      </span>
      <span v-else-if="selectedIds.length > 0" class="text-sm font-medium text-primary-900 dark:text-primary-100">
        {{ t('admin.accounts.bulkActions.selected', { count: selectedIds.length }) }}
      </span>
      <span v-else class="text-sm font-medium text-primary-900 dark:text-primary-100">
        {{ t('admin.accounts.bulkEdit.title') }}
      </span>
      <template v-if="selectedIds.length > 0">
        <button
          @click="$emit('select-page')"
          class="text-xs font-medium text-primary-700 hover:text-primary-800 dark:text-primary-300 dark:hover:text-primary-200"
        >
          {{ t('admin.accounts.bulkActions.selectCurrentPage') }}
        </button>
      </template>
      <template v-if="!allResultsSelected && totalResults > selectedIds.length">
        <span v-if="selectedIds.length > 0" class="text-gray-300 dark:text-primary-800">•</span>
        <button
          :disabled="selectingAll"
          @click="$emit('select-all-results')"
          class="text-xs font-medium text-primary-700 hover:text-primary-800 disabled:cursor-not-allowed disabled:opacity-60 dark:text-primary-300 dark:hover:text-primary-200"
        >
          {{
            selectingAll
              ? t('admin.accounts.bulkActions.selectingAll')
              : t('admin.accounts.bulkActions.selectAllResults', { count: totalResults })
          }}
        </button>
      </template>
      <template v-if="selectedIds.length > 0">
        <span class="text-gray-300 dark:text-primary-800">•</span>
        <button
          @click="$emit('clear')"
          class="text-xs font-medium text-primary-700 hover:text-primary-800 dark:text-primary-300 dark:hover:text-primary-200"
        >
          {{ t('admin.accounts.bulkActions.clear') }}
        </button>
      </template>
      <div
        v-if="containsSupplierManaged"
        data-testid="supplier-managed-bulk-notice"
        class="basis-full text-xs font-medium text-amber-800 dark:text-amber-200"
      >
        {{ readOnlyReason }}
        <a class="ml-1 underline" href="/admin/supplier-sources">
          {{ t('admin.accounts.supplierManaged.openManagement') }}
        </a>
      </div>
    </div>
    <div class="flex gap-2">
      <template v-if="selectedIds.length > 0">
        <button :disabled="containsSupplierManaged" :title="writeTitle" @click="emitWrite('delete')" class="btn btn-danger btn-sm">{{ t('admin.accounts.bulkActions.delete') }}</button>
        <button @click="$emit('reset-status')" class="btn btn-secondary btn-sm">{{ t('admin.accounts.bulkActions.resetStatus') }}</button>
        <button :disabled="containsSupplierManaged" :title="writeTitle" @click="emitWrite('refresh-token')" class="btn btn-secondary btn-sm">{{ t('admin.accounts.bulkActions.refreshToken') }}</button>
        <button @click="$emit('probe-upstream-billing')" class="btn btn-secondary btn-sm">{{ t('admin.accounts.bulkActions.probeUpstreamBilling') }}</button>
        <button :disabled="containsSupplierManaged" :title="writeTitle" @click="emitWrite('toggle-schedulable', true)" class="btn btn-success btn-sm">{{ t('admin.accounts.bulkActions.enableScheduling') }}</button>
        <button :disabled="containsSupplierManaged" :title="writeTitle" @click="emitWrite('toggle-schedulable', false)" class="btn btn-warning btn-sm">{{ t('admin.accounts.bulkActions.disableScheduling') }}</button>
        <button :disabled="containsSupplierManaged" :title="writeTitle" @click="emitWrite('edit-selected')" class="btn btn-primary btn-sm">{{ t('admin.accounts.bulkActions.edit') }}</button>
      </template>
      <button :disabled="containsSupplierManaged" :title="writeTitle" @click="emitWrite('edit-filtered')" class="btn btn-primary btn-sm">
        {{ t('admin.accounts.bulkEdit.submit') }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useSupplierManagedAccount } from '@/composables/useSupplierManagedAccount'

const props = withDefaults(defineProps<{
  selectedIds: number[]
  totalResults: number
  selectingAll: boolean
  allResultsSelected: boolean
  containsSupplierManaged?: boolean
}>(), {
  containsSupplierManaged: false
})

const emit = defineEmits([
  'delete',
  'edit-selected',
  'edit-filtered',
  'clear',
  'select-page',
  'select-all-results',
  'toggle-schedulable',
  'reset-status',
  'refresh-token',
  'probe-upstream-billing'
])

const { t } = useI18n()
const { readOnlyReason } = useSupplierManagedAccount()
const writeTitle = computed(() => props.containsSupplierManaged ? readOnlyReason.value : undefined)

type WriteEvent = 'delete' | 'edit-selected' | 'edit-filtered' | 'toggle-schedulable' | 'refresh-token'

const emitWrite = (event: WriteEvent, value?: boolean) => {
  if (props.containsSupplierManaged) return
  if (event === 'toggle-schedulable') {
    emit(event, value)
    return
  }
  emit(event)
}
</script>

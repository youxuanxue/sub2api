<template>
  <div data-testid="group-messages-permission">
    <h4 class="text-sm font-medium text-gray-700 dark:text-gray-300 mb-3">
      {{ t('admin.groups.openaiMessages.title') }}
    </h4>
    <div class="flex items-center justify-between">
      <span class="text-sm text-gray-600 dark:text-gray-400">
        {{ t('admin.groups.openaiMessages.allowDispatch') }}
      </span>
      <Toggle
        :model-value="modelValue"
        :aria-label="t('admin.groups.openaiMessages.allowDispatch')"
        @update:model-value="$emit('update:modelValue', $event)"
      />
    </div>
    <p class="text-xs text-gray-500 dark:text-gray-400 mt-1">
      {{ t('admin.groups.openaiMessages.allowDispatchHint') }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'

defineProps<{ modelValue: boolean }>()
defineEmits<{ (event: 'update:modelValue', value: boolean): void }>()
const { t } = useI18n()
</script>

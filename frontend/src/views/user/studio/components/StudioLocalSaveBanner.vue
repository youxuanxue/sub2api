<script setup lang="ts">
import { useI18n } from 'vue-i18n'

withDefaults(
  defineProps<{
    /** Override default `studio-local-save-banner` test id (e.g. per-surface). */
    testId?: string
    class?: string
  }>(),
  { testId: 'studio-local-save-banner' }
)

const { t } = useI18n()
</script>

<template>
  <div
    class="flex items-start gap-2.5 rounded-lg border-2 border-amber-300 bg-amber-50 px-3 py-2.5 text-xs text-amber-950 shadow-sm dark:border-amber-700 dark:bg-amber-950/40 dark:text-amber-50"
    :class="$props.class"
    :data-testid="testId"
    role="alert"
  >
    <span aria-hidden="true" class="mt-0.5 shrink-0 text-base leading-none">⬇</span>
    <div class="min-w-0">
      <p class="font-semibold leading-snug">{{ t('studio.saveReminderTitle') }}</p>
      <p class="mt-1 font-medium leading-snug opacity-90">{{ t('studio.saveReminder') }}</p>
    </div>
  </div>
</template>

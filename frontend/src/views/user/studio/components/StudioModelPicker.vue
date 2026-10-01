<script setup lang="ts">
/**
 * Compact single-select model cards for Studio Image / Video left rails.
 * Keep card chrome here so ImageStudio and VideoStudio cannot drift on height.
 */
import { useI18n } from 'vue-i18n'

export type StudioModelPickerItem = {
  modelId: string
  displayName: string
  qualityBadgeKey: string
  /** Preformatted price line (incl. unit) or usage-priced fallback. */
  priceLabel: string
  vendorLabel: string
  servedId: string
  needsApikeyAccount?: boolean
}

defineProps<{
  label: string
  models: StudioModelPickerItem[]
  modelValue: string
  /** Per-surface test id (studio-image-model / studio-video-model). */
  testId: string
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', id: string): void
}>()

const { t } = useI18n()
</script>

<template>
  <div class="rounded-xl border border-gray-200 bg-white p-3 shadow-sm dark:border-dark-700 dark:bg-dark-900">
    <div class="mb-1.5 text-xs font-semibold uppercase tracking-wide text-gray-400 dark:text-dark-500">{{ label }}</div>
    <div class="space-y-1.5">
      <button
        v-for="m in models"
        :key="m.modelId"
        type="button"
        class="w-full rounded-lg border px-2.5 py-1.5 text-left transition"
        :class="modelValue === m.modelId
          ? 'border-primary-500 bg-primary-50 ring-2 ring-primary-500/30 dark:border-primary-500 dark:bg-primary-950/40'
          : 'border-gray-200 hover:border-primary-300 dark:border-dark-600'"
        :data-testid="testId"
        @click="emit('update:modelValue', m.modelId)"
      >
        <div class="flex items-center justify-between gap-2">
          <span class="truncate text-[12px] font-semibold leading-snug text-gray-900 dark:text-white" :title="m.displayName">{{ m.displayName }}</span>
          <span class="shrink-0 rounded bg-gray-100 px-1.5 py-0.5 text-[10px] font-medium leading-none text-gray-600 dark:bg-dark-800 dark:text-dark-300">{{ t(m.qualityBadgeKey) }}</span>
        </div>
        <div class="mt-0.5 flex min-w-0 items-baseline gap-1.5 text-[10px] leading-snug">
          <span class="shrink-0 font-bold text-primary-700 dark:text-primary-300">{{ m.priceLabel }}</span>
          <span class="min-w-0 truncate text-gray-400 dark:text-dark-500" :title="`${t('studio.via', { vendor: m.vendorLabel })} · ${m.servedId}`">
            {{ t('studio.via', { vendor: m.vendorLabel }) }} · <span class="font-mono">{{ m.servedId }}</span>
          </span>
        </div>
        <div v-if="m.needsApikeyAccount" class="mt-0.5 text-[10px] font-medium leading-snug text-amber-600 dark:text-amber-400">{{ t('studio.needsApikeyAccount') }}</div>
      </button>
    </div>
  </div>
</template>

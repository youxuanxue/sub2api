<script setup lang="ts">
import { useI18n } from 'vue-i18n'

withDefaults(
  defineProps<{
    aspectRatio?: string
    seconds?: number
    aspectLabel?: string
    testId?: string
  }>(),
  { aspectRatio: '16 / 9', testId: 'studio-video-checking' }
)

const { t } = useI18n()
</script>

<template>
  <div
    class="relative flex w-full flex-col items-center justify-center gap-2 rounded-lg border border-dashed border-gray-200 bg-gray-50 px-3 py-6 text-center dark:border-dark-600 dark:bg-dark-800/60"
    :style="{ aspectRatio }"
    :data-testid="testId"
  >
    <span class="inline-flex items-center gap-2 text-xs text-gray-500 dark:text-dark-400">
      <span class="h-2 w-2 animate-pulse rounded-full bg-primary-500" aria-hidden="true" />
      {{ t('studio.video.checkingPreview') }}
    </span>
    <span
      v-if="seconds"
      class="pointer-events-none absolute bottom-2 left-2 rounded bg-black/55 px-1.5 py-0.5 font-mono text-[10px] tabular-nums text-white/90"
    >
      {{ seconds }}s<span v-if="aspectLabel"> · {{ aspectLabel }}</span>
    </span>
  </div>
</template>

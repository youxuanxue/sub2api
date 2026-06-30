<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { VideoTaskItem } from '@/composables/useMediaLibrary'
import StudioPlaybackBadge from '@/views/user/studio/components/StudioPlaybackBadge.vue'

defineProps<{
  prompt: string
  task: Pick<VideoTaskItem, 'playbackStorage' | 'urlExpired' | 'url'>
  seconds?: number
  aspectLabel?: string
  copied?: boolean
  testId?: string
  downloadTestId?: string
  copyTestId?: string
}>()

defineEmits<{
  download: []
  copyLink: []
}>()

const { t } = useI18n()
</script>

<template>
  <div
    class="rounded-lg border border-amber-200/80 bg-amber-50/60 px-3 py-4 dark:border-amber-900/50 dark:bg-amber-950/20"
    :data-testid="testId ?? 'studio-video-download-only'"
  >
    <p class="text-xs font-semibold uppercase tracking-wide text-amber-900 dark:text-amber-100">
      {{ t('studio.video.downloadOnlyTitle') }}
    </p>
    <p class="mt-2 whitespace-pre-wrap break-words text-sm text-gray-800 dark:text-dark-100">{{ prompt }}</p>
    <StudioPlaybackBadge class="mt-2" :task="task" />
    <p class="mt-2 text-[11px] leading-snug text-amber-900/90 dark:text-amber-100/90">
      {{ t('studio.video.downloadOnlyHint') }}
    </p>
    <p
      v-if="seconds"
      class="mt-2 font-mono text-[10px] tabular-nums text-gray-500 dark:text-dark-400"
    >
      {{ seconds }}s<span v-if="aspectLabel"> · {{ aspectLabel }}</span>
    </p>
    <div class="mt-3 flex flex-wrap gap-2">
      <button
        type="button"
        class="inline-flex items-center justify-center rounded-lg bg-primary-600 px-3 py-2 text-xs font-semibold text-white shadow-sm transition hover:bg-primary-700 dark:bg-primary-500 dark:hover:bg-primary-400"
        :data-testid="downloadTestId ?? 'studio-video-download-primary'"
        @click="$emit('download')"
      >
        {{ t('studio.video.download') }}
      </button>
      <button
        type="button"
        class="inline-flex items-center justify-center rounded-lg border border-gray-300 bg-white px-3 py-2 text-xs font-medium text-gray-700 transition hover:bg-gray-50 dark:border-dark-600 dark:bg-dark-900 dark:text-dark-100 dark:hover:bg-dark-800"
        :data-testid="copyTestId ?? 'studio-video-copy-primary'"
        @click="$emit('copyLink')"
      >
        {{ copied ? t('studio.video.copied') : t('studio.video.copyLink') }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { VideoTaskItem } from '@/composables/useMediaLibrary'
import StudioPlaybackBadge from '@/views/user/studio/components/StudioPlaybackBadge.vue'

defineProps<{
  prompt: string
  task?: Pick<VideoTaskItem, 'playbackStorage' | 'urlExpired' | 'url'>
  testId?: string
}>()

const { t } = useI18n()
</script>

<template>
  <div
    class="rounded-lg border border-dashed border-gray-200 bg-gray-50 px-3 py-4 dark:border-dark-600 dark:bg-dark-800/60"
    :data-testid="testId ?? 'studio-video-expired'"
  >
    <p class="whitespace-pre-wrap break-words text-sm text-gray-800 dark:text-dark-100">{{ prompt }}</p>
    <StudioPlaybackBadge v-if="task" class="mt-2" :task="task" />
    <p v-else class="mt-2 text-[11px] text-gray-400 dark:text-dark-500">{{ t('studio.video.expiredReload') }}</p>
    <slot />
  </div>
</template>

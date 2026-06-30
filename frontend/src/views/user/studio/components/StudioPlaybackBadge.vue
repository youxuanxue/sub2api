<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { VideoTaskItem } from '@/composables/useMediaLibrary'
import {
  studioPlaybackBadgeClass,
  studioPlaybackStorageI18nKey,
  videoTaskPlaybackStorageKind,
} from '@/utils/studioPlaybackStorage.tk'

const props = withDefaults(
  defineProps<{
    task: Pick<VideoTaskItem, 'playbackStorage' | 'urlExpired' | 'url'>
    testId?: string
  }>(),
  { testId: 'studio-playback-badge' }
)

const { t } = useI18n()
const kind = computed(() => videoTaskPlaybackStorageKind(props.task))
</script>

<template>
  <p
    v-if="kind !== 'unknown'"
    class="inline-flex max-w-full items-center gap-1 rounded-md px-2 py-1 text-[10px] font-medium leading-snug"
    :class="studioPlaybackBadgeClass(kind)"
    :data-testid="testId"
    :data-playback-storage="kind"
  >
    <span class="shrink-0 opacity-80">{{ t('studio.playback.label') }}:</span>
    <span>{{ t(studioPlaybackStorageI18nKey(kind)) }}</span>
  </p>
  <p v-else class="text-[10px] text-gray-400 dark:text-dark-500">
    {{ t(studioPlaybackStorageI18nKey(kind)) }}
  </p>
</template>

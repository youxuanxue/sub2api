<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { formatUsd } from '@/utils/mediaCostEstimate.tk'
import { downloadMedia } from '@/utils/studioDownload.tk'
import type { StudioVideoPreviewState } from '@/composables/useStudioVideoPreview'

const props = defineProps<{
  open: boolean
  previewState: StudioVideoPreviewState
  previewUrl: string
  downloadUrl: string
  downloadFilename: string
  label: string
  subtitle?: string
  cost: number | null
  previewMediaReady: boolean
  copiedLink: boolean
  testId?: string
  closeTestId?: string
  copyLinkTestId?: string
  showReusePrompt?: boolean
}>()

const emit = defineEmits<{
  close: []
  error: []
  retry: []
  reuse: []
  'copy-link': []
  'media-ready': []
}>()

const { t } = useI18n()

function onDownload(): void {
  if (!props.downloadUrl) return
  downloadMedia(props.downloadUrl, props.downloadFilename)
}
</script>

<template>
  <Teleport to="body">
    <div
      v-if="open"
      class="fixed inset-0 z-[100] flex flex-col bg-black/85 backdrop-blur-sm"
      :data-testid="testId ?? 'studio-video-preview'"
      @click.self="emit('close')"
    >
      <div class="flex items-center justify-end p-3">
        <button
          type="button"
          class="rounded-lg bg-white/10 px-3 py-1.5 text-sm font-medium text-white hover:bg-white/20"
          :data-testid="closeTestId ?? 'studio-video-preview-close'"
          :aria-label="t('studio.video.close')"
          @click="emit('close')"
        >
          {{ t('studio.video.close') }} ✕
        </button>
      </div>
      <div class="relative flex min-h-0 flex-1 items-center justify-center px-4" @click.self="emit('close')">
        <video
          v-if="previewState === 'ready' && previewUrl"
          :src="previewUrl"
          controls
          autoplay
          playsinline
          preload="auto"
          class="h-full max-h-full w-full max-w-full rounded-lg bg-black object-contain shadow-2xl"
          @loadeddata="$emit('media-ready')"
          @error="$emit('error')"
        ></video>
        <div
          v-if="previewState === 'ready' && previewUrl && !previewMediaReady"
          class="absolute text-sm text-white/80"
        >
          {{ t('studio.video.previewBuffering') }}
        </div>
        <div v-else-if="previewState === 'loading'" class="text-sm text-white/80">
          {{ t('studio.video.loadingPreview') }}
        </div>
        <div v-else class="max-w-sm rounded-xl bg-white/10 p-6 text-center">
          <p class="text-sm font-semibold text-white">{{ t('studio.video.expiredTitle') }}</p>
          <p class="mt-1 text-xs text-white/70">{{ t('studio.video.expiredHint') }}</p>
          <div class="mt-3 flex items-center justify-center gap-2">
            <button
              type="button"
              class="rounded-md bg-white px-3 py-1.5 text-[12px] font-medium text-gray-900 hover:bg-gray-100"
              @click="emit('retry')"
            >
              {{ t('studio.video.retry') }}
            </button>
            <button
              v-if="showReusePrompt"
              type="button"
              class="rounded-md bg-white/90 px-3 py-1.5 text-[12px] font-medium text-gray-800 hover:bg-white"
              @click="emit('reuse')"
            >
              {{ t('studio.image.usePrompt') }}
            </button>
          </div>
        </div>
      </div>
      <div class="flex flex-wrap items-center justify-center gap-3 p-4">
        <span class="max-w-[60vw] truncate text-xs text-white/80" :title="subtitle || label">{{ subtitle || label }}</span>
        <span v-if="cost != null" class="shrink-0 rounded bg-white/15 px-1.5 py-0.5 text-[11px] font-semibold text-white">
          {{ formatUsd(cost) }}
        </span>
        <button
          v-if="downloadUrl"
          type="button"
          class="rounded-md bg-white px-3 py-1.5 text-[12px] font-medium text-gray-900 hover:bg-gray-100"
          @click="onDownload"
        >
          {{ t('studio.video.download') }}
        </button>
        <button
          v-if="previewState === 'ready' && previewUrl"
          type="button"
          class="rounded-md bg-white/90 px-3 py-1.5 text-[12px] font-medium text-gray-800 hover:bg-white"
          :data-testid="copyLinkTestId ?? 'studio-video-copy-link'"
          @click="emit('copy-link')"
        >
          {{ copiedLink ? t('studio.video.copied') : t('studio.video.copyLink') }}
        </button>
        <button
          v-if="showReusePrompt"
          type="button"
          class="rounded-md bg-white/90 px-3 py-1.5 text-[12px] font-medium text-gray-800 hover:bg-white"
          @click="emit('reuse')"
        >
          {{ t('studio.image.usePrompt') }}
        </button>
      </div>
    </div>
  </Teleport>
</template>

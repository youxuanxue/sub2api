<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { formatUsd } from '@/utils/mediaCostEstimate.tk'
import type { ImageHistoryItem } from '@/composables/useMediaLibrary'
import { imageHistoryPromptTitle } from '@/utils/studioImageHistory.tk'

defineProps<{
  preview: ImageHistoryItem | null
  showUseAsInput?: boolean
  testId?: string
  closeTestId?: string
}>()

const emit = defineEmits<{
  close: []
  download: [item: ImageHistoryItem]
  reuse: [item: ImageHistoryItem]
  'use-as-input': [item: ImageHistoryItem]
}>()

const { t } = useI18n()

function promptTitle(img: ImageHistoryItem): string {
  return imageHistoryPromptTitle(img, (text) => t('studio.image.revisedPromptHint', { text }))
}
</script>

<template>
  <Teleport to="body">
    <div
      v-if="preview"
      class="fixed inset-0 z-[100] flex flex-col bg-black/85 backdrop-blur-sm"
      :data-testid="testId ?? 'studio-image-preview'"
      @click.self="emit('close')"
    >
      <div class="flex items-center justify-end p-3">
        <button
          type="button"
          class="rounded-lg bg-white/10 px-3 py-1.5 text-sm font-medium text-white hover:bg-white/20"
          :data-testid="closeTestId ?? 'studio-image-preview-close'"
          @click="emit('close')"
        >
          {{ t('studio.image.close') }} ✕
        </button>
      </div>
      <div class="flex min-h-0 flex-1 items-center justify-center px-4" @click.self="emit('close')">
        <img
          :src="preview.src"
          :alt="preview.prompt"
          class="max-h-full max-w-full rounded-lg object-contain shadow-2xl"
        />
      </div>
      <div class="flex flex-wrap items-center justify-center gap-3 p-4">
        <span class="max-w-[60vw] truncate text-xs text-white/80" :title="promptTitle(preview)">{{ preview.prompt }}</span>
        <span class="shrink-0 rounded bg-white/15 px-1.5 py-0.5 text-[11px] font-semibold text-white">{{ formatUsd(preview.cost) }}</span>
        <button
          type="button"
          class="rounded-md bg-white px-3 py-1.5 text-[12px] font-medium text-gray-900 hover:bg-gray-100"
          @click="emit('download', preview)"
        >
          {{ t('studio.image.download') }}
        </button>
        <button
          type="button"
          class="rounded-md bg-white/90 px-3 py-1.5 text-[12px] font-medium text-gray-800 hover:bg-white"
          @click="emit('reuse', preview)"
        >
          {{ t('studio.image.usePrompt') }}
        </button>
        <button
          v-if="showUseAsInput"
          type="button"
          class="rounded-md bg-white/90 px-3 py-1.5 text-[12px] font-medium text-gray-800 hover:bg-white"
          @click="emit('use-as-input', preview)"
        >
          {{ t('studio.image.useAsInput') }}
        </button>
      </div>
    </div>
  </Teleport>
</template>

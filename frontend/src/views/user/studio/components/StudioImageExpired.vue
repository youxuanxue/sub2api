<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { STUDIO_IMAGE_EXPIRED_I18N_KEY } from '@/utils/studioMedia.tk'

withDefaults(
  defineProps<{
    testId?: string
    compact?: boolean
  }>(),
  { testId: 'studio-image-expired', compact: false }
)

const { t } = useI18n()
</script>

<template>
  <div
    class="flex w-full flex-col items-center justify-center gap-1 bg-gray-50 px-2 text-center dark:bg-dark-800"
    :class="compact ? 'aspect-square text-[11px]' : 'aspect-square text-[10px]'"
    :data-testid="testId"
  >
    <span aria-hidden="true" class="text-2xl text-gray-300 dark:text-dark-600">🖼</span>
    <span class="leading-tight text-gray-400 dark:text-dark-500">{{ t(STUDIO_IMAGE_EXPIRED_I18N_KEY) }}</span>
    <slot />
  </div>
</template>

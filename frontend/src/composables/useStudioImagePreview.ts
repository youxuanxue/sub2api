import { onBeforeUnmount, onMounted, ref } from 'vue'
import type { ImageHistoryItem } from '@/composables/useMediaLibrary'
import { imageHistoryItemAvailable } from '@/utils/studioMedia.tk'

/**
 * Shared in-page image lightbox for ImageStudio (mirrors useStudioVideoPreview from #1092).
 * data: URIs cannot open in a new tab; preview stays in-page.
 */
export function useStudioImagePreview() {
  const preview = ref<ImageHistoryItem | null>(null)

  function openPreview(img: ImageHistoryItem): void {
    if (!imageHistoryItemAvailable(img)) return
    preview.value = img
  }

  function closePreview(): void {
    preview.value = null
  }

  function onKeydown(e: KeyboardEvent): void {
    if (e.key === 'Escape' && preview.value) closePreview()
  }

  onMounted(() => window.addEventListener('keydown', onKeydown))
  onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))

  return { preview, openPreview, closePreview }
}

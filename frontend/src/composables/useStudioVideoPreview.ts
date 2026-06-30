import { computed, onBeforeUnmount, ref } from 'vue'
import { videoPlaybackUrl } from '@/utils/studioMedia.tk'

export type StudioVideoPreviewState = 'loading' | 'ready' | 'expired'

export interface StudioVideoPreviewSource {
  url: string
  label: string
  cost: number
  taskId?: string
  /** Shown under the label in the lightbox footer (e.g. full prompt). */
  subtitle?: string
  downloadFilename?: string
}

export interface UseStudioVideoPreviewOptions {
  onUrlExpired?: (taskId: string) => void
}

/**
 * Shared in-page video lightbox state for VideoStudio and BakeOff.
 * http(s) URLs play directly; inline data:video becomes a tab-local Blob URL.
 */
export function useStudioVideoPreview(options: UseStudioVideoPreviewOptions = {}) {
  const open = ref(false)
  const previewUrl = ref('')
  const rawUrl = ref('')
  const taskId = ref<string | undefined>()
  const label = ref('')
  const subtitle = ref('')
  const cost = ref<number | null>(null)
  const downloadFilename = ref('tokenkey-preview.mp4')
  const previewState = ref<StudioVideoPreviewState>('loading')
  const previewMediaReady = ref(false)
  const copiedLink = ref(false)

  let previewRevoke: () => void = () => {}
  let copiedTimer: ReturnType<typeof setTimeout> | undefined

  const downloadUrl = computed(() => rawUrl.value || previewUrl.value)

  function openPreview(source: StudioVideoPreviewSource): void {
    if (!source.url) return
    previewRevoke()
    open.value = true
    label.value = source.label
    subtitle.value = source.subtitle ?? ''
    cost.value = source.cost
    rawUrl.value = source.url
    taskId.value = source.taskId
    downloadFilename.value = source.downloadFilename ?? 'tokenkey-preview.mp4'
    previewUrl.value = ''
    previewState.value = 'loading'
    previewMediaReady.value = false
    copiedLink.value = false

    const playback = videoPlaybackUrl(source.url)
    previewRevoke = playback.revoke
    previewUrl.value = playback.url
    previewState.value = playback.url ? 'ready' : 'expired'
  }

  function closePreview(): void {
    previewRevoke()
    previewRevoke = () => {}
    open.value = false
    label.value = ''
    subtitle.value = ''
    cost.value = null
    rawUrl.value = ''
    taskId.value = undefined
    previewUrl.value = ''
    previewState.value = 'loading'
    previewMediaReady.value = false
    copiedLink.value = false
  }

  function onPreviewError(): void {
    const id = taskId.value
    closePreview()
    if (id) options.onUrlExpired?.(id)
  }

  function retryPreview(): void {
    if (!rawUrl.value) return
    openPreview({
      url: rawUrl.value,
      label: label.value,
      cost: cost.value ?? 0,
      taskId: taskId.value,
      subtitle: subtitle.value,
      downloadFilename: downloadFilename.value,
    })
  }

  function onPreviewMediaReady(): void {
    previewMediaReady.value = true
  }

  async function copyPreviewLink(): Promise<void> {
    if (!previewUrl.value) return
    try {
      await navigator.clipboard?.writeText(previewUrl.value)
      copiedLink.value = true
      if (copiedTimer) clearTimeout(copiedTimer)
      copiedTimer = setTimeout(() => (copiedLink.value = false), 1500)
    } catch {
      /* clipboard unavailable — Download still works */
    }
  }

  onBeforeUnmount(closePreview)

  return {
    open,
    previewUrl,
    rawUrl,
    taskId,
    label,
    subtitle,
    cost,
    downloadFilename,
    downloadUrl,
    previewState,
    previewMediaReady,
    copiedLink,
    openPreview,
    closePreview,
    onPreviewError,
    retryPreview,
    onPreviewMediaReady,
    copyPreviewLink,
  }
}

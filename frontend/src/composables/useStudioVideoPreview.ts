import { computed, onBeforeUnmount, ref } from 'vue'
import { copyStudioVideoLink, downloadMedia } from '@/utils/studioDownload.tk'
import { isInlineStudioVideoUrl } from '@/utils/studioInlineVideo.tk'
import { videoPlaybackUrl } from '@/utils/studioMedia.tk'

export type StudioVideoPreviewState = 'loading' | 'ready' | 'expired'

export interface StudioVideoPreviewSource {
  url: string
  label: string
  cost: number
  taskId?: string
  urlExpired?: boolean
  /** Shown under the label in the lightbox footer (e.g. full prompt). */
  subtitle?: string
  downloadFilename?: string
}

export interface UseStudioVideoPreviewOptions {
  /** Toast when lightbox download is blocked (playback failed or persisted url stripped). */
  onExpiredDownload?: () => void
  /** Inline Veo clip: copy-link is not shareable — prompt download instead. */
  onInlineCopyUnsupported?: () => void
}

/**
 * Shared in-page video lightbox state for VideoStudio and BakeOff.
 * http(s) URLs play directly; inline data:video becomes a tab-local Blob URL.
 *
 * Lightbox playback failure is session-local (`previewState === 'expired'`) and must
 * not mutate library/panel task cards — card poster SSOT stays on the raw upstream url.
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
  const urlExpired = ref(false)
  const previewInline = ref(false)

  let previewRevoke: () => void = () => {}
  let copiedTimer: ReturnType<typeof setTimeout> | undefined

  const downloadUrl = computed(() => rawUrl.value || previewUrl.value)
  const copyLinkUrl = computed(() => (isInlineStudioVideoUrl(rawUrl.value) ? '' : rawUrl.value || previewUrl.value))

  function openPreview(source: StudioVideoPreviewSource): void {
    if (!source.url) return
    previewRevoke()
    open.value = true
    label.value = source.label
    subtitle.value = source.subtitle ?? ''
    cost.value = source.cost
    rawUrl.value = source.url
    taskId.value = source.taskId
    urlExpired.value = source.urlExpired ?? false
    previewInline.value = isInlineStudioVideoUrl(source.url)
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
    urlExpired.value = false
    previewInline.value = false
    previewUrl.value = ''
    previewState.value = 'loading'
    previewMediaReady.value = false
    copiedLink.value = false
  }

  function onPreviewError(): void {
    previewRevoke()
    previewRevoke = () => {}
    previewUrl.value = ''
    previewState.value = 'expired'
    previewMediaReady.value = false
    urlExpired.value = true
  }

  function retryPreview(): void {
    if (!rawUrl.value) return
    openPreview({
      url: rawUrl.value,
      label: label.value,
      cost: cost.value ?? 0,
      taskId: taskId.value,
      urlExpired: urlExpired.value,
      subtitle: subtitle.value,
      downloadFilename: downloadFilename.value,
    })
  }

  function onPreviewMediaReady(): void {
    previewMediaReady.value = true
  }

  async function copyPreviewLink(): Promise<void> {
    const url = rawUrl.value || copyLinkUrl.value
    if (!url) return
    const result = await copyStudioVideoLink(url)
    if (result === 'copied') {
      copiedLink.value = true
      if (copiedTimer) clearTimeout(copiedTimer)
      copiedTimer = setTimeout(() => (copiedLink.value = false), 1500)
      return
    }
    if (result === 'inline-unsupported') {
      options.onInlineCopyUnsupported?.()
      downloadPreview()
    }
  }

  function downloadPreview(): void {
    const url = rawUrl.value
    if (!url) {
      options.onExpiredDownload?.()
      return
    }
    downloadMedia(url, downloadFilename.value)
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
    copyLinkUrl,
    previewState,
    previewMediaReady,
    copiedLink,
    urlExpired,
    previewInline,
    openPreview,
    closePreview,
    onPreviewError,
    retryPreview,
    onPreviewMediaReady,
    copyPreviewLink,
    downloadPreview,
  }
}

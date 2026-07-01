import { ref } from 'vue'
import type { ComposerTranslation } from 'vue-i18n'
import { copyStudioVideoLink, downloadMedia } from '@/utils/studioDownload.tk'

export interface UseStudioVideoCardActionsOptions {
  onExpiredDownload?: () => void
  /** Inline Veo clip: copy-link is not shareable — prompt download instead. */
  onInlineCopyUnsupported?: () => void
}

/** Toast surface shared by VideoStudio + BakeOff video card/lightbox actions. */
export interface StudioVideoToastStore {
  showWarning(message: string, duration?: number): void
  showInfo(message: string, duration?: number): void
}

/** Shared expired-download + inline-copy toasts for card and lightbox composables. */
export function createStudioVideoActionHandlers(
  store: StudioVideoToastStore,
  t: ComposerTranslation
): UseStudioVideoCardActionsOptions {
  return {
    onExpiredDownload: () => store.showWarning(t('studio.video.expiredHint'), 8000),
    onInlineCopyUnsupported: () => store.showInfo(t('studio.video.inlineCopyHint'), 5000),
  }
}

/** Card-level copy-link + download actions shared by VideoStudio and BakeOff. */
export function useStudioVideoCardActions(options: UseStudioVideoCardActionsOptions = {}) {
  const copiedUrl = ref('')
  let copiedTimer: ReturnType<typeof setTimeout> | undefined

  async function copyCardLink(url: string, downloadFilename?: string): Promise<void> {
    if (!url) return
    const result = await copyStudioVideoLink(url)
    if (result === 'copied') {
      copiedUrl.value = url
      if (copiedTimer) clearTimeout(copiedTimer)
      copiedTimer = setTimeout(() => (copiedUrl.value = ''), 1500)
      return
    }
    if (result === 'inline-unsupported') {
      options.onInlineCopyUnsupported?.()
      if (downloadFilename) downloadCardVideo(url, downloadFilename)
    }
  }

  function downloadCardVideo(url: string, filename: string, urlExpired?: boolean): void {
    if (!url) return
    if (urlExpired) {
      options.onExpiredDownload?.()
      return
    }
    downloadMedia(url, filename)
  }

  return { copiedUrl, copyCardLink, downloadCardVideo }
}

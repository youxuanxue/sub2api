import { ref } from 'vue'
import { copyMediaLink, downloadMedia } from '@/utils/studioDownload.tk'

/** Card-level copy-link + download actions shared by VideoStudio and BakeOff. */
export function useStudioVideoCardActions(onExpiredDownload?: () => void) {
  const copiedUrl = ref('')
  let copiedTimer: ReturnType<typeof setTimeout> | undefined

  async function copyCardLink(url: string): Promise<void> {
    if (!url) return
    if (await copyMediaLink(url)) {
      copiedUrl.value = url
      if (copiedTimer) clearTimeout(copiedTimer)
      copiedTimer = setTimeout(() => (copiedUrl.value = ''), 1500)
    }
  }

  function downloadCardVideo(url: string, filename: string, urlExpired?: boolean): void {
    if (!url) return
    if (urlExpired) {
      onExpiredDownload?.()
      return
    }
    downloadMedia(url, filename)
  }

  return { copiedUrl, copyCardLink, downloadCardVideo }
}

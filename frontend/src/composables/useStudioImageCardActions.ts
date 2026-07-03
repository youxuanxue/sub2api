import type { ImageHistoryItem } from '@/composables/useMediaLibrary'
import { downloadMedia } from '@/utils/studioDownload.tk'

/**
 * Canonical download filename for a generated Studio image. Both ImageStudio
 * cards/lightbox and the BakeOff image panels save as `tokenkey-<id>.png`;
 * keeping the format here prevents the two surfaces from drifting apart.
 */
export function studioImageDownloadFilename(id: string): string {
  return `tokenkey-${id}.png`
}

/** Browsers throttle a burst of synchronous downloads; stagger each save. */
const BATCH_DOWNLOAD_STAGGER_MS = 350

/**
 * Card-level image download actions — the image sibling of
 * useStudioVideoCardActions (Studio SSOT, CLAUDE.md §5.1 / agent-reference
 * "Studio SSOT" table). Delegates the actual save to the studioDownload.tk
 * owner (`downloadMedia` handles data:/blob:/remote URL differences); never
 * reimplement the anchor-click download here or in a view.
 */
export function useStudioImageCardActions() {
  function downloadCardImage(img: Pick<ImageHistoryItem, 'id' | 'src'>): void {
    downloadMedia(img.src, studioImageDownloadFilename(img.id))
  }

  /**
   * Batch export: stagger each save so the browser doesn't drop downloads.
   * Order matches the given list (ImageStudio passes newest first).
   */
  function downloadAllImages(imgs: readonly Pick<ImageHistoryItem, 'id' | 'src'>[]): void {
    imgs.forEach((img, i) => {
      window.setTimeout(() => downloadCardImage(img), i * BATCH_DOWNLOAD_STAGGER_MS)
    })
  }

  return { downloadCardImage, downloadAllImages }
}

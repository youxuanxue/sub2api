import type { Ref } from 'vue'
import { gatewayImagePresign } from '@/api/playground'
import type { ImageHistoryItem, MediaLibrary } from '@/composables/useMediaLibrary'

export type StudioImageLibrary = Pick<
  MediaLibrary,
  'images' | 'hydrateFromBlobCache' | 'rehydrateImageFromBlob'
>

/**
 * Re-mint presigned URLs for gateway-offloaded images (s3Key rows).
 * Shared by ImageStudio and BakeOff — single reload path after hydrateFromBlobCache.
 */
export async function refreshStudioOffloadedImageUrls(
  apiKey: string,
  gatewayBase: string,
  images: Ref<ImageHistoryItem[]> | ImageHistoryItem[]
): Promise<void> {
  if (!apiKey) return
  const rows = 'value' in images ? images.value : images
  const stale = rows.filter((it) => it.s3Key)
  if (!stale.length) return
  await Promise.all(
    stale.map(async (it) => {
      try {
        const url = await gatewayImagePresign(apiKey, gatewayBase, it.s3Key as string)
        if (url) it.src = url
      } catch {
        /* history is best-effort */
      }
    })
  )
}

/** Mount hook: IndexedDB hydrate, then presign refresh for offloaded rows. */
export async function mountStudioImageLibrary(
  apiKey: string,
  gatewayBase: string,
  library: StudioImageLibrary
): Promise<void> {
  await library.hydrateFromBlobCache()
  await refreshStudioOffloadedImageUrls(apiKey, gatewayBase, library.images)
}

/** Thumbnail load failure: try IndexedDB mirror before showing expired placeholder. */
export async function onStudioImageThumbError(
  library: StudioImageLibrary,
  itemId: string
): Promise<void> {
  await library.rehydrateImageFromBlob(itemId)
}

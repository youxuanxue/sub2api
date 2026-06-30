import type { MediaLibrary } from '@/composables/useMediaLibrary'

export type StudioVideoLibrary = Pick<MediaLibrary, 'hydrateFromBlobCache'>

/**
 * Rehydrate IndexedDB-mirrored video clips after reload.
 * Shared by VideoStudio and BakeOff — single reload path (mirrors mountStudioImageLibrary
 * without s3Key presign; video passthrough keeps http urls out of localStorage).
 */
export async function mountStudioVideoLibrary(library: StudioVideoLibrary): Promise<void> {
  await library.hydrateFromBlobCache()
}

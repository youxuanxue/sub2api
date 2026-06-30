import type { MediaLibrary } from '@/composables/useMediaLibrary'

export type StudioVideoLibrary = Pick<MediaLibrary, 'hydrateFromBlobCache' | 'rehydrateVideoFromBlob'>

/**
 * Rehydrate IndexedDB-mirrored video clips after reload.
 * Shared by VideoStudio and BakeOff — single reload path (mirrors mountStudioImageLibrary
 * without s3Key presign; video passthrough keeps http urls out of localStorage).
 */
export async function mountStudioVideoLibrary(library: StudioVideoLibrary): Promise<void> {
  await library.hydrateFromBlobCache()
}

/** Play / card replay: try IndexedDB mirror before showing expired placeholder. */
export async function onStudioVideoReplayError(
  library: StudioVideoLibrary,
  taskId: string
): Promise<boolean> {
  return library.rehydrateVideoFromBlob(taskId)
}

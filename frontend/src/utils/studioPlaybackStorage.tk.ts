import type { VideoTaskItem } from '@/composables/useMediaLibrary'

/**
 * TokenKey-only: classify how a generated video can be replayed / cached in-browser.
 * Surfaces honest labels in Studio so ops/users can tell CORS-blocked upstream URLs
 * from inline data: clips (#944 pass-through — TokenKey is not a media CDN).
 */
export type StudioPlaybackStorage =
  /** data: / blob: — IndexedDB mirror possible in this browser. */
  | 'inline-local'
  /** http(s) and a CORS fetch succeeded — may mirror into IndexedDB. */
  | 'upstream-cors-ok'
  /** http(s) plays in-tab but fetch/cache blocked — refresh loses preview. */
  | 'upstream-cors-blocked'
  /** Stripped / expired / empty url after reload. */
  | 'expired'
  | 'unknown'

export function classifyVideoUrlStorage(url: string): StudioPlaybackStorage {
  if (!url) return 'expired'
  if (/^data:video/i.test(url) || url.startsWith('blob:')) return 'inline-local'
  if (/^https?:\/\//i.test(url)) return 'unknown' // resolved by probeUpstreamVideoCacheability
  return 'unknown'
}

/** Best-effort CORS probe — does NOT download the whole clip (Range bytes=0-0). */
export async function probeUpstreamVideoCacheability(
  url: string
): Promise<'upstream-cors-ok' | 'upstream-cors-blocked'> {
  if (!/^https?:\/\//i.test(url)) return 'upstream-cors-blocked'
  try {
    const res = await fetch(url, {
      method: 'GET',
      mode: 'cors',
      headers: { Range: 'bytes=0-0' },
    })
    if (res.ok || res.status === 206) return 'upstream-cors-ok'
    return 'upstream-cors-blocked'
  } catch {
    return 'upstream-cors-blocked'
  }
}

export async function resolveVideoPlaybackStorage(url: string): Promise<StudioPlaybackStorage> {
  const base = classifyVideoUrlStorage(url)
  if (base === 'unknown' && /^https?:\/\//i.test(url)) {
    return probeUpstreamVideoCacheability(url)
  }
  return base
}

export function studioPlaybackStorageI18nKey(storage: StudioPlaybackStorage | undefined): string {
  switch (storage) {
    case 'inline-local':
      return 'studio.playback.inlineLocal'
    case 'upstream-cors-ok':
      return 'studio.playback.upstreamCorsOk'
    case 'upstream-cors-blocked':
      return 'studio.playback.upstreamCorsBlocked'
    case 'expired':
      return 'studio.playback.expired'
    default:
      return 'studio.playback.unknown'
  }
}

/** Resolve persisted or derived playback-storage kind for a video task row. */
export function videoTaskPlaybackStorageKind(
  task: Pick<VideoTaskItem, 'playbackStorage' | 'urlExpired' | 'url'>
): StudioPlaybackStorage {
  return task.playbackStorage ?? (task.urlExpired || !task.url ? 'expired' : 'unknown')
}

export interface TagStudioVideoPlaybackDeps {
  patchVideoTask: (id: string, patch: Partial<VideoTaskItem>) => void
  cacheInlineMedia: (kind: 'video', itemId: string, src: string) => Promise<void>
  onUpstreamCorsBlocked?: () => void
}

/** Classify upstream playback storage and mirror inline clips into IndexedDB when safe. */
export async function tagStudioVideoPlayback(
  deps: TagStudioVideoPlaybackDeps,
  taskId: string,
  url: string
): Promise<void> {
  if (!url) {
    deps.patchVideoTask(taskId, { playbackStorage: 'expired' })
    return
  }
  const storage = await resolveVideoPlaybackStorage(url)
  deps.patchVideoTask(taskId, { playbackStorage: storage })
  if (storage === 'upstream-cors-blocked') deps.onUpstreamCorsBlocked?.()
  if (storage === 'inline-local' || storage === 'upstream-cors-ok') {
    void deps.cacheInlineMedia('video', taskId, url)
  }
}

/** Tailwind classes for the honest playback-source badge (Image / Video / Bake-Off). */
export function studioPlaybackBadgeClass(storage: StudioPlaybackStorage): string {
  switch (storage) {
    case 'inline-local':
      return 'bg-green-50 text-green-800 dark:bg-green-950/40 dark:text-green-200'
    case 'upstream-cors-ok':
      return 'bg-blue-50 text-blue-800 dark:bg-blue-950/40 dark:text-blue-200'
    case 'upstream-cors-blocked':
      return 'bg-amber-50 text-amber-900 dark:bg-amber-950/40 dark:text-amber-100'
    case 'expired':
      return 'bg-gray-100 text-gray-600 dark:bg-dark-800 dark:text-dark-300'
    default:
      return 'bg-gray-50 text-gray-500 dark:bg-dark-800 dark:text-dark-400'
  }
}

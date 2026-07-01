/**
 * TokenKey-only: resolve a generated-video src into a browser-playable URL WITHOUT
 * making TokenKey host the media — the #944 "not a media CDN" rule, shared by every
 * Studio surface that plays video on demand (VideoStudio lightbox, Bake-Off panels).
 *
 * http(s) upstream URLs are handed to the browser as-is. An inline `data:video`
 * result (the one response already delivered to this tab) is converted to a
 * tab-local Blob URL so it plays without re-fetching and without persisting. The
 * returned `revoke()` frees the Blob when playback closes (a no-op for http URLs).
 *
 * Never throws: on any failure (non-base64 data URI, missing atob/createObjectURL,
 * decode error) it falls back to the original src with a no-op revoke, so a caller
 * always gets something usable to hand to a <video :src>.
 */
import type { ImageHistoryItem, VideoTaskItem } from '@/composables/useMediaLibrary'
import { isInlineStudioVideoUrl, parseDataVideoUri } from '@/utils/studioInlineVideo.tk'
import { videoTaskPlaybackStorageKind } from '@/utils/studioPlaybackStorage.tk'

/** i18n key for image thumbnails that lost their in-browser bytes after reload. */
export const STUDIO_IMAGE_EXPIRED_I18N_KEY = 'studio.image.expiredReload' as const

/** True when an image history row still has a renderable thumbnail in this tab. */
export function imageHistoryItemAvailable(img: Pick<ImageHistoryItem, 'src'>): boolean {
  return !!img.src?.trim()
}

/** Card surface for a succeeded video task after CORS/storage classification. */
export type VideoTaskCardPresentation = 'loading' | 'inline-play' | 'download-only' | 'expired'

/**
 * Decide which video card UI to show. Upstream http(s) URLs stay in `loading` until
 * tagStudioVideoPlayback resolves; CORS-blocked upstream clips skip the play tile
 * and surface download-first (honest UX — no fake ▶).
 */
export function videoTaskCardPresentation(
  task: Pick<VideoTaskItem, 'state' | 'url' | 'urlExpired' | 'playbackStorage'>
): VideoTaskCardPresentation {
  if (task.state !== 'succeeded') return 'expired'
  if (task.urlExpired || !task.url?.trim()) return 'expired'

  const url = task.url.trim()
  if (/^data:video/i.test(url) || url.startsWith('blob:')) return 'inline-play'

  const kind = videoTaskPlaybackStorageKind(task)
  switch (kind) {
    case 'upstream-cors-blocked':
      return 'download-only'
    case 'unknown':
      return 'loading'
    case 'expired':
      return 'expired'
    case 'inline-local':
    case 'upstream-cors-ok':
      return 'inline-play'
    default:
      return 'loading'
  }
}

/** True when the card may show the in-page play tile / open the lightbox. */
export function videoTaskPlaybackAvailable(
  task: Pick<VideoTaskItem, 'state' | 'url' | 'urlExpired' | 'playbackStorage'>
): boolean {
  return videoTaskCardPresentation(task) === 'inline-play'
}

/**
 * True when a Studio video surface may show a copy-link affordance.
 * Inline Veo clips (data:video) and download-only / expired cards are download-only.
 */
export function videoCopyLinkAvailable(
  url: string | undefined | null,
  cardPresentation?: VideoTaskCardPresentation
): boolean {
  const trimmed = url?.trim()
  if (!trimmed) return false
  if (isInlineStudioVideoUrl(trimmed)) return false
  if (cardPresentation === 'download-only' || cardPresentation === 'expired') return false
  return true
}

/** Copy-link visibility for a library/history video task row. */
export function videoTaskCopyLinkAvailable(
  task: Pick<VideoTaskItem, 'state' | 'url' | 'urlExpired' | 'playbackStorage'>
): boolean {
  return videoCopyLinkAvailable(task.url, videoTaskCardPresentation(task))
}

/** When any Studio surface should show the local-save download banner. */
export function shouldShowStudioSaveReminder(opts: {
  imageCount: number
  videoTaskCount: number
  /** In-tab panels or in-flight jobs not yet counted in library rows. */
  activeResultCount?: number
}): boolean {
  return opts.imageCount > 0 || opts.videoTaskCount > 0 || (opts.activeResultCount ?? 0) > 0
}

export interface ImageHistoryRun {
  ts: number
  prompt: string
  items: ImageHistoryItem[]
}

/** Group image history rows that share a generation batch (`ts`). Bake-Off and multi-`n` Image runs use the same field. */
export function groupImageHistoryByTs(images: readonly ImageHistoryItem[], minItems = 2): ImageHistoryRun[] {
  const byTs = new Map<number, ImageHistoryItem[]>()
  for (const img of images) {
    const list = byTs.get(img.ts) ?? []
    list.push(img)
    byTs.set(img.ts, list)
  }
  return [...byTs.entries()]
    .filter(([, items]) => items.length >= minItems)
    .sort((a, b) => b[0] - a[0])
    .map(([ts, items]) => ({ ts, prompt: items[0]?.prompt ?? '', items }))
}

export interface VideoHistoryRun {
  batchMs: number
  prompt: string
  items: VideoTaskItem[]
}

/** Group video tasks submitted in the same Bake-Off batch (`submittedAtMs`). */
export function groupVideoHistoryByBatch(tasks: readonly VideoTaskItem[], minItems = 2): VideoHistoryRun[] {
  const byBatch = new Map<number, VideoTaskItem[]>()
  for (const task of tasks) {
    const list = byBatch.get(task.submittedAtMs) ?? []
    list.push(task)
    byBatch.set(task.submittedAtMs, list)
  }
  return [...byBatch.entries()]
    .filter(([, items]) => items.length >= minItems)
    .sort((a, b) => b[0] - a[0])
    .map(([batchMs, items]) => ({ batchMs, prompt: items[0]?.prompt ?? '', items }))
}

export interface VideoPlayback {
  /** URL ready for a <video :src> — http(s) upstream URL or a tab-local blob: URL. */
  url: string
  /** Free the Blob URL (no-op for http(s) URLs); call when playback closes. */
  revoke: () => void
}

const NOOP = (): void => {}

export function videoPlaybackUrl(src: string): VideoPlayback {
  if (!src) return { url: '', revoke: NOOP }
  // http(s) (incl. presigned upstream/S3) — hand straight to the browser, no Blob.
  if (!/^data:video/i.test(src)) return { url: src, revoke: NOOP }
  try {
    const parsed = parseDataVideoUri(src)
    if (!parsed || typeof atob !== 'function' || typeof URL?.createObjectURL !== 'function') {
      return { url: src, revoke: NOOP }
    }
    const binary = atob(parsed.base64)
    const bytes = new Uint8Array(binary.length)
    for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
    const objectUrl = URL.createObjectURL(new Blob([bytes], { type: parsed.mime }))
    return {
      url: objectUrl,
      revoke: () => {
        try {
          URL.revokeObjectURL?.(objectUrl)
        } catch {
          /* ignore */
        }
      },
    }
  } catch {
    return { url: src, revoke: NOOP }
  }
}

/** Async variant: fetch() decodes large data:video URIs without blocking the main thread. */
export async function videoPlaybackUrlAsync(src: string): Promise<VideoPlayback> {
  if (!src) return { url: '', revoke: NOOP }
  if (!/^data:video/i.test(src)) return { url: src, revoke: NOOP }
  if (typeof fetch === 'function' && typeof URL?.createObjectURL === 'function') {
    try {
      const res = await fetch(src)
      const blob = await res.blob()
      if (blob.size > 0) {
        const objectUrl = URL.createObjectURL(blob)
        return {
          url: objectUrl,
          revoke: () => {
            try {
              URL.revokeObjectURL?.(objectUrl)
            } catch {
              /* ignore */
            }
          },
        }
      }
    } catch {
      /* fall through to sync decode */
    }
  }
  return videoPlaybackUrl(src)
}

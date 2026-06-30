/**
 * TokenKey-only: persistent media library for the Studio.
 *
 * Replaces the old in-memory `.slice(0,8)` image list and the single
 * overwrite-on-submit video task with a localStorage-backed history that
 * survives reloads — and, critically, lets an in-flight video task (vt_…) be
 * re-attached to its poll loop after a refresh (the backend keeps the task in
 * Redis for 24h; we just stop throwing the handle away).
 *
 * SECURITY: we persist the API key's numeric id, never the raw key string. The
 * key is resolved from the live key list at runtime (useVideoTaskPoll). A
 * deleted key simply means the task can no longer be polled.
 *
 * Storage is best-effort. Inline data: payloads are NOT written to localStorage
 * (gemini-native / pass-through #944 images, data:video clips) — they are mirrored
 * into IndexedDB (`studioBlobCache.tk.ts`) keyed by item id so a reload can still
 * show thumbnails / replay video without TokenKey hosting media. A residual
 * QuotaExceededError on localStorage still trims the oldest metadata entries.
 */
import { ref, watch, type Ref } from 'vue'
import {
  cacheStudioBlobFromHttpUrl,
  cacheStudioBlobFromSrc,
  deleteStudioBlob,
  getStudioBlobObjectUrl,
  pruneStudioBlobCache,
} from '@/utils/studioBlobCache.tk'
import type { StudioPlaybackStorage } from '@/utils/studioPlaybackStorage.tk'
import { isEphemeralImageSrc } from '@/utils/studioImageHistory.tk'

export type VideoTaskState = 'processing' | 'succeeded' | 'failed'

export interface ImageHistoryItem {
  id: string
  src: string
  /**
   * S3 key when the image was offloaded by the gateway (opt-in image offload only).
   * `src` is then a short-lived presigned URL; on reload the Studio re-mints a fresh
   * one from this key (POST /v1/images/presign) so persisted thumbnails don't break.
   * Absent for inline data: URI images (gemini-native, or imagen/gpt-image under the
   * #944 pass-through default) — those carry the bytes inline and are NOT persisted
   * (sanitizeImageForPersistence blanks src); bytes may still live in IndexedDB.
   */
  s3Key?: string
  /** True when bytes were written to the browser blob cache (reload hydration). */
  blobCached?: boolean
  prompt: string
  revisedPrompt?: string
  model: string
  vendorLabel: string
  /** requested size string (e.g. "1024x1024") */
  size: string
  /** estimated cost shown as the receipt chip (USD); the hold/settlement is authoritative server-side. */
  cost: number
  ts: number
}

export interface VideoTaskItem {
  id: string
  prompt: string
  model: string
  vendorLabel: string
  seconds: number
  aspectRatio?: string
  estCost: number
  /** numeric API key id — resolved to the raw key at runtime; NEVER persist the key string. */
  keyId: number
  state: VideoTaskState
  url: string
  /** Set when an upstream http(s) link expired or was stripped on reload — card shows prompt only. */
  urlExpired?: boolean
  /** True when clip bytes were written to IndexedDB (reload playback). */
  blobCached?: boolean
  /** How replay / browser cache behaves for this clip (diagnostics). */
  playbackStorage?: StudioPlaybackStorage
  submittedAtMs: number
  elapsedS: number
  errorMessage?: string
}

const IMAGE_CAP = 50
const VIDEO_CAP = 20

interface PersistShape {
  images: ImageHistoryItem[]
  videoTasks: VideoTaskItem[]
}

function storageKey(userId: number | string): string {
  return `tk_media_lib_v1:${userId}`
}

function loadPersisted(key: string): PersistShape {
  if (typeof window === 'undefined') return { images: [], videoTasks: [] }
  try {
    const raw = window.localStorage.getItem(key)
    if (!raw) return { images: [], videoTasks: [] }
    const parsed = JSON.parse(raw) as Partial<PersistShape> | null
    return {
      images: Array.isArray(parsed?.images)
        ? (parsed!.images as ImageHistoryItem[]).map(normalizeLoadedImage)
        : [],
      videoTasks: Array.isArray(parsed?.videoTasks)
        ? (parsed!.videoTasks as VideoTaskItem[]).map(sanitizeVideoTaskForPersistence)
        : [],
    }
  } catch {
    return { images: [], videoTasks: [] }
  }
}

function sanitizeVideoTaskForPersistence(task: VideoTaskItem): VideoTaskItem {
  if (task.urlExpired && task.url) {
    return { ...task, url: '' }
  }
  if (/^data:video/i.test(task.url) || task.url.startsWith('blob:')) {
    return { ...task, url: '' }
  }
  // Upstream presigned http(s) links are short-lived (#944 — TokenKey is not a media CDN).
  // Persist prompt/history only so a reload never offers a stale play tile.
  if (/^https?:\/\//i.test(task.url)) {
    return { ...task, url: '', urlExpired: true }
  }
  return task
}

function sanitizeVideoTasksForPersistence(tasks: VideoTaskItem[]): VideoTaskItem[] {
  return tasks.map(sanitizeVideoTaskForPersistence)
}

/**
 * Inline / blob / upstream http(s) thumbnails must NOT be persisted in localStorage:
 * data: blows the quota; blob: URLs die on reload; presigned http links expire.
 * Bytes (or s3Key for gateway offload) are the reload path — IndexedDB mirror and/or
 * POST /v1/images/presign on Studio mount.
 */
function sanitizeImageForPersistence(item: ImageHistoryItem): ImageHistoryItem {
  if (item.s3Key) return { ...item, src: '' }
  if (isEphemeralImageSrc(item.src)) return { ...item, src: '' }
  return item
}

/** Strip legacy persisted blob:/http src so reload always rehydrates from IDB / presign. */
function normalizeLoadedImage(item: ImageHistoryItem): ImageHistoryItem {
  if (item.s3Key) return { ...item, src: '' }
  if (isEphemeralImageSrc(item.src)) return { ...item, src: '' }
  return item
}

/**
 * Persist with graceful quota degradation: on QuotaExceededError, drop the
 * oldest images and retry; if still failing, persist video tasks only.
 */
function persist(key: string, data: PersistShape): void {
  if (typeof window === 'undefined') return
  let images = data.images.map(sanitizeImageForPersistence)
  const videoTasks = sanitizeVideoTasksForPersistence(data.videoTasks)
  for (let attempt = 0; attempt < 6; attempt++) {
    try {
      window.localStorage.setItem(key, JSON.stringify({ images, videoTasks }))
      return
    } catch {
      if (images.length === 0) {
        try {
          window.localStorage.setItem(key, JSON.stringify({ images: [], videoTasks }))
        } catch {
          /* give up silently — history is a convenience, not correctness */
        }
        return
      }
      // drop the oldest ~third and retry
      images = images.slice(0, Math.max(0, Math.floor(images.length * 0.66)))
    }
  }
}

export interface MediaLibrary {
  images: Ref<ImageHistoryItem[]>
  videoTasks: Ref<VideoTaskItem[]>
  addImages: (items: ImageHistoryItem[]) => void
  clearImages: () => void
  upsertVideoTask: (task: VideoTaskItem) => void
  patchVideoTask: (id: string, patch: Partial<VideoTaskItem>) => void
  removeVideoTask: (id: string) => void
  clearVideoTasks: () => void
  /** Rehydrate empty src/url from IndexedDB after reload; call once on Studio mount. */
  hydrateFromBlobCache: () => Promise<void>
  /** Best-effort mirror of inline media into IndexedDB (generation / poll terminal). */
  cacheInlineMedia: (kind: 'image' | 'video', itemId: string, src: string) => Promise<void>
  /** On <img> error: try IndexedDB mirror, else mark thumbnail unavailable. */
  rehydrateImageFromBlob: (id: string) => Promise<boolean>
}

export function useMediaLibrary(userId: number | string): MediaLibrary {
  const key = storageKey(userId)
  const initial = loadPersisted(key)
  const images = ref<ImageHistoryItem[]>(initial.images.slice(0, IMAGE_CAP))
  const videoTasks = ref<VideoTaskItem[]>(initial.videoTasks.slice(0, VIDEO_CAP))

  // Deep-watch both lists and persist on any mutation (state transitions from
  // the poll loop included).
  watch(
    [images, videoTasks],
    () => persist(key, { images: images.value, videoTasks: videoTasks.value }),
    { deep: true }
  )

  async function cacheInlineMedia(kind: 'image' | 'video', itemId: string, src: string): Promise<void> {
    if (!src) return
    const ok = /^https?:\/\//i.test(src)
      ? await cacheStudioBlobFromHttpUrl(userId, kind, itemId, src)
      : await cacheStudioBlobFromSrc(userId, kind, itemId, src)
    if (!ok) return
    if (kind === 'image') {
      const idx = images.value.findIndex((it) => it.id === itemId)
      if (idx >= 0) {
        const next = [...images.value]
        next[idx] = { ...next[idx], blobCached: true }
        images.value = next
      }
    } else {
      const idx = videoTasks.value.findIndex((t) => t.id === itemId)
      if (idx >= 0) {
        const next = [...videoTasks.value]
        next[idx] = { ...next[idx], blobCached: true, urlExpired: false }
        videoTasks.value = next
      }
    }
  }

  function addImages(items: ImageHistoryItem[]): void {
    if (!items.length) return
    images.value = [...items, ...images.value].slice(0, IMAGE_CAP)
    for (const it of items) {
      if (it.src && (/^data:/i.test(it.src) || /^https?:\/\//i.test(it.src))) {
        void cacheInlineMedia('image', it.id, it.src)
      }
    }
  }

  function clearImages(): void {
    for (const it of images.value) {
      if (it.blobCached) void deleteStudioBlob(userId, 'image', it.id)
    }
    images.value = []
  }

  function upsertVideoTask(task: VideoTaskItem): void {
    const idx = videoTasks.value.findIndex((t) => t.id === task.id)
    if (idx >= 0) {
      const next = [...videoTasks.value]
      next[idx] = task
      videoTasks.value = next
    } else {
      videoTasks.value = [task, ...videoTasks.value].slice(0, VIDEO_CAP)
    }
    if (task.state === 'succeeded' && task.url && (/^data:video/i.test(task.url) || task.url.startsWith('blob:'))) {
      void cacheInlineMedia('video', task.id, task.url)
    }
  }

  function patchVideoTask(id: string, patch: Partial<VideoTaskItem>): void {
    const idx = videoTasks.value.findIndex((t) => t.id === id)
    if (idx < 0) return
    const next = [...videoTasks.value]
    next[idx] = { ...next[idx], ...patch }
    videoTasks.value = next
    const merged = next[idx]
    if (merged.state === 'succeeded' && merged.url && (/^data:video/i.test(merged.url) || merged.url.startsWith('blob:'))) {
      void cacheInlineMedia('video', id, merged.url)
    }
  }

  function removeVideoTask(id: string): void {
    const task = videoTasks.value.find((t) => t.id === id)
    if (task?.blobCached) void deleteStudioBlob(userId, 'video', id)
    videoTasks.value = videoTasks.value.filter((t) => t.id !== id)
  }

  function clearVideoTasks(): void {
    for (const t of videoTasks.value) {
      if (t.blobCached) void deleteStudioBlob(userId, 'video', t.id)
    }
    videoTasks.value = []
  }

  async function hydrateFromBlobCache(): Promise<void> {
    await pruneStudioBlobCache(userId)
    let imagesChanged = false
    const nextImages = [...images.value]
    for (let i = 0; i < nextImages.length; i++) {
      const it = nextImages[i]
      if (it.s3Key) continue
      const needsBlob =
        !it.src?.trim() || it.src.startsWith('blob:') || /^https?:\/\//i.test(it.src)
      if (!needsBlob) continue
      const blobUrl = await getStudioBlobObjectUrl(userId, 'image', it.id)
      if (!blobUrl) continue
      nextImages[i] = { ...it, src: blobUrl, blobCached: true }
      imagesChanged = true
    }
    if (imagesChanged) images.value = nextImages

    let videosChanged = false
    const nextVideos = [...videoTasks.value]
    for (let i = 0; i < nextVideos.length; i++) {
      const t = nextVideos[i]
      if (t.url || t.state !== 'succeeded') continue
      const blobUrl = await getStudioBlobObjectUrl(userId, 'video', t.id)
      if (!blobUrl) continue
      nextVideos[i] = { ...t, url: blobUrl, blobCached: true, urlExpired: false }
      videosChanged = true
    }
    if (videosChanged) videoTasks.value = nextVideos
  }

  async function rehydrateImageFromBlob(id: string): Promise<boolean> {
    const idx = images.value.findIndex((it) => it.id === id)
    if (idx < 0) return false
    const it = images.value[idx]
    if (it.s3Key) return false
    const blobUrl = await getStudioBlobObjectUrl(userId, 'image', id)
    const next = [...images.value]
    if (!blobUrl) {
      next[idx] = { ...next[idx], src: '' }
      images.value = next
      return false
    }
    next[idx] = { ...next[idx], src: blobUrl, blobCached: true }
    images.value = next
    return true
  }

  return {
    images,
    videoTasks,
    addImages,
    clearImages,
    upsertVideoTask,
    patchVideoTask,
    removeVideoTask,
    clearVideoTasks,
    hydrateFromBlobCache,
    cacheInlineMedia,
    rehydrateImageFromBlob,
  }
}

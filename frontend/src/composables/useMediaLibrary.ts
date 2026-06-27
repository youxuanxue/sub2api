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
 * Storage is best-effort. Inline data: payloads stay browser-local instead of
 * being written to localStorage: video tasks (data:video / blob:) and now images
 * (data: src with no s3Key — gemini-native, or imagen/gpt-image under the #944
 * pass-through default) are sanitized before persistence. A residual
 * QuotaExceededError still trims the oldest image entries and retries.
 */
import { ref, watch, type Ref } from 'vue'

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
   * (sanitizeImageForPersistence blanks src), so on reload they show a regenerate hint.
   */
  s3Key?: string
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
      images: Array.isArray(parsed?.images) ? (parsed!.images as ImageHistoryItem[]) : [],
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
 * Inline data: image bytes (gemini-native, or imagen/gpt-image once image S3
 * offload is off — the #944 pass-through default) must NOT be persisted: a multi-MB
 * base64 string per image blows the ~5-10MB localStorage budget and silently evicts
 * real history through the degradation loop below. Mirror the data:video sanitizer:
 * blank the src on the PERSISTED copy only — the in-memory images.value keeps the
 * full data: src so the current session's grid + lightbox still render browser-local.
 * Offloaded (s3Key) and http(s) images are tiny + re-mintable, so they persist as-is.
 */
function sanitizeImageForPersistence(item: ImageHistoryItem): ImageHistoryItem {
  if (item.s3Key || !/^data:/i.test(item.src)) return item
  return { ...item, src: '' }
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

  function addImages(items: ImageHistoryItem[]): void {
    if (!items.length) return
    images.value = [...items, ...images.value].slice(0, IMAGE_CAP)
  }

  function clearImages(): void {
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
  }

  function patchVideoTask(id: string, patch: Partial<VideoTaskItem>): void {
    const idx = videoTasks.value.findIndex((t) => t.id === id)
    if (idx < 0) return
    const next = [...videoTasks.value]
    next[idx] = { ...next[idx], ...patch }
    videoTasks.value = next
  }

  function removeVideoTask(id: string): void {
    videoTasks.value = videoTasks.value.filter((t) => t.id !== id)
  }

  function clearVideoTasks(): void {
    videoTasks.value = []
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
  }
}

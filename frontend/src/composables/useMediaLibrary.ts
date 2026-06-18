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
 * Storage is best-effort: a QuotaExceededError (large base64 images) trims the
 * oldest image entries and retries, then degrades to video-tasks-only, never
 * throwing into the UI.
 */
import { ref, watch, type Ref } from 'vue'

export type VideoTaskState = 'processing' | 'succeeded' | 'failed'

export interface ImageHistoryItem {
  id: string
  src: string
  /**
   * S3 key when the image was offloaded by the gateway. `src` is then a short-lived
   * presigned URL; on reload the Studio re-mints a fresh one from this key (POST
   * /v1/images/presign) so persisted thumbnails don't break. Absent for inline
   * data: URI images (gemini-native), which never expire.
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
      videoTasks: Array.isArray(parsed?.videoTasks) ? (parsed!.videoTasks as VideoTaskItem[]) : [],
    }
  } catch {
    return { images: [], videoTasks: [] }
  }
}

/**
 * Persist with graceful quota degradation: on QuotaExceededError, drop the
 * oldest images and retry; if still failing, persist video tasks only.
 */
function persist(key: string, data: PersistShape): void {
  if (typeof window === 'undefined') return
  let images = data.images
  for (let attempt = 0; attempt < 6; attempt++) {
    try {
      window.localStorage.setItem(key, JSON.stringify({ images, videoTasks: data.videoTasks }))
      return
    } catch {
      if (images.length === 0) {
        try {
          window.localStorage.setItem(key, JSON.stringify({ images: [], videoTasks: data.videoTasks }))
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

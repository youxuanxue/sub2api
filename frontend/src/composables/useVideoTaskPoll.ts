/**
 * TokenKey-only: video task polling engine for the Studio.
 *
 * Drives the async submit→poll lifecycle for one OR many concurrent video tasks
 * (the task stack), on top of the existing backend skeleton: a vt_ task id +
 * VideoTaskCache (Redis, 24h TTL) + terminal-status auto-delete. Mirrors the
 * proven loop from the old PlaygroundView (5s interval, 3 consecutive-error
 * tolerance so one network blip never kills a billed task), but adds:
 *   - multi-task polling (the old code polled exactly one task)
 *   - reattach after reload (resume a persisted processing task; its key is
 *     resolved from the live key list by id, never persisted raw)
 *   - terminal completion notification (in-app + optional browser Notification)
 *
 * The composable mutates nothing directly: it calls `patch(id, …)` so the owner
 * (useMediaLibrary) stays the single source of truth and persistence point.
 */
import { onUnmounted } from 'vue'
import { gatewayVideoFetch } from '@/api/playground'
import { videoStateFromFetch, extractVideoUrl, type PlaygroundVideoState } from '@/constants/playgroundMedia.tk'
import type { VideoTaskItem } from '@/composables/useMediaLibrary'

const POLL_INTERVAL_MS = 5_000
const MAX_CONSECUTIVE_ERRORS = 3

export interface VideoTaskPollOptions {
  gatewayBase: () => string
  /** Resolve the raw API key for a task's keyId; undefined when the key is gone. */
  resolveKey: (keyId: number) => string | undefined
  /** Apply a state/url/elapsed update to the owning store. */
  patch: (id: string, patch: Partial<VideoTaskItem>) => void
  /** Fired once when a task reaches a terminal state (succeeded/failed). */
  onTerminal?: (task: VideoTaskItem, state: Exclude<PlaygroundVideoState, 'processing'>) => void
}

interface Poller {
  timer: ReturnType<typeof setTimeout> | null
  abort: AbortController | null
  errors: number
}

export interface VideoTaskPoller {
  /** Begin or resume polling a task (no-op unless state==='processing'). */
  resume: (task: VideoTaskItem) => void
  stop: (id: string) => void
  stopAll: () => void
}

/** Request browser notification permission (call from a user gesture). */
export async function requestVideoNotifyPermission(): Promise<boolean> {
  if (typeof window === 'undefined' || !('Notification' in window)) return false
  if (Notification.permission === 'granted') return true
  if (Notification.permission === 'denied') return false
  try {
    const res = await Notification.requestPermission()
    return res === 'granted'
  } catch {
    return false
  }
}

/** Fire a browser notification when permitted; silently no-op otherwise. */
export function maybeNotify(title: string, body: string): void {
  if (typeof window === 'undefined' || !('Notification' in window)) return
  if (Notification.permission !== 'granted') return
  try {
    // eslint-disable-next-line no-new
    new Notification(title, { body })
  } catch {
    /* notifications are best-effort */
  }
}

export function useVideoTaskPoll(opts: VideoTaskPollOptions): VideoTaskPoller {
  const pollers = new Map<string, Poller>()

  function stop(id: string): void {
    const p = pollers.get(id)
    if (!p) return
    if (p.timer) clearTimeout(p.timer)
    p.abort?.abort()
    pollers.delete(id)
  }

  function stopAll(): void {
    for (const id of [...pollers.keys()]) stop(id)
  }

  function schedule(task: VideoTaskItem): void {
    const p = pollers.get(task.id)
    if (!p) return
    p.timer = setTimeout(() => {
      void pollOnce(task)
    }, POLL_INTERVAL_MS)
  }

  async function pollOnce(task: VideoTaskItem): Promise<void> {
    const p = pollers.get(task.id)
    if (!p) return
    const key = opts.resolveKey(task.keyId)
    if (!key) {
      // Key gone (deleted / different session) — cannot poll. Leave the card as
      // processing but stop the loop; the user sees it stalled rather than a crash.
      stop(task.id)
      opts.patch(task.id, { errorMessage: 'key_unavailable' })
      return
    }
    opts.patch(task.id, { elapsedS: Math.round((Date.now() - task.submittedAtMs) / 1000) })
    const ctrl = new AbortController()
    p.abort = ctrl
    try {
      const raw = await gatewayVideoFetch(key, opts.gatewayBase(), task.id, ctrl.signal)
      if (ctrl.signal.aborted || !pollers.has(task.id)) return
      p.errors = 0
      const state = videoStateFromFetch(raw)
      const url = state === 'succeeded' ? extractVideoUrl(raw) : ''
      opts.patch(task.id, { state, url })
      if (state === 'processing') {
        schedule(task)
      } else {
        stop(task.id)
        opts.onTerminal?.({ ...task, state, url }, state)
      }
    } catch (e) {
      if (ctrl.signal.aborted || !pollers.has(task.id)) return
      p.errors += 1
      if (p.errors < MAX_CONSECUTIVE_ERRORS) {
        schedule(task)
        return
      }
      // Terminal records are deleted server-side and TTL-expire after 24h —
      // repeated fetch errors (404 included) end the loop instead of forever.
      stop(task.id)
      const message = e instanceof Error ? e.message : 'fetch_failed'
      opts.patch(task.id, { state: 'failed', errorMessage: message })
      opts.onTerminal?.({ ...task, state: 'failed', errorMessage: message }, 'failed')
    }
  }

  function resume(task: VideoTaskItem): void {
    if (task.state !== 'processing') return
    if (pollers.has(task.id)) return
    pollers.set(task.id, { timer: null, abort: null, errors: 0 })
    schedule(task)
  }

  onUnmounted(stopAll)

  return { resume, stop, stopAll }
}

/**
 * useVisibilityAwarePoller — a polling helper that only ticks while the browser
 * tab is visible.
 *
 * Why this exists: several long-lived pollers (user refresh, subscription
 * refresh, risk-control status, ops dashboard) used bare `setInterval`, so a
 * backgrounded tab kept hitting the server every 15–300s forever — wasted
 * battery, wasted RTT, and (for rolling-window dashboards) repeatedly re-paying
 * cold aggregation cost. This centralizes the "pause when hidden, catch up when
 * visible" pattern so we don't copy-paste `visibilitychange` wiring into every
 * caller.
 *
 * Semantics:
 *   - Hidden tab: the interval is cleared (no ticks at all).
 *   - Becomes visible again: if at least one full interval has elapsed since the
 *     last run, runs the callback once immediately (catch-up), then resumes the
 *     interval. Rapid tab toggles therefore do NOT spam the callback.
 *   - `start()` assumes the caller has just performed an initial fetch (the
 *     common pattern is "fetch once, then start polling"), so it does not fire
 *     an immediate extra call on start.
 *   - `start()` / `stop()` are idempotent.
 *
 * SSR-safe: all `document` access is guarded.
 */
export interface VisibilityAwarePoller {
  /** Begin polling. Idempotent. Call after an initial fetch. */
  start: () => void
  /** Stop polling and detach the visibility listener. Idempotent. */
  stop: () => void
  /** Whether the poller is currently active (independent of tab visibility). */
  isRunning: () => boolean
}

export interface VisibilityAwarePollerOptions {
  /**
   * When the tab becomes visible after being hidden, run the callback once
   * immediately if a full interval has elapsed. Default true.
   */
  catchUpOnVisible?: boolean
}

const isDocHidden = (): boolean =>
  typeof document !== 'undefined' && document.visibilityState === 'hidden'

export function useVisibilityAwarePoller(
  callback: () => void | Promise<void>,
  intervalMs: number,
  options: VisibilityAwarePollerOptions = {}
): VisibilityAwarePoller {
  const { catchUpOnVisible = true } = options

  let timer: ReturnType<typeof setInterval> | null = null
  let active = false
  let listenerBound = false
  let lastRunAt = 0

  const run = (): void => {
    lastRunAt = Date.now()
    void callback()
  }

  const clearTimer = (): void => {
    if (timer !== null) {
      clearInterval(timer)
      timer = null
    }
  }

  const startTimer = (): void => {
    clearTimer()
    if (isDocHidden()) return // don't tick while hidden
    timer = setInterval(run, intervalMs)
  }

  const onVisibilityChange = (): void => {
    if (!active) return
    if (isDocHidden()) {
      clearTimer()
      return
    }
    // Became visible: catch up if we missed at least one interval, then resume.
    if (catchUpOnVisible && Date.now() - lastRunAt >= intervalMs) {
      run()
    }
    startTimer()
  }

  const bindListener = (): void => {
    if (listenerBound || typeof document === 'undefined') return
    document.addEventListener('visibilitychange', onVisibilityChange)
    listenerBound = true
  }

  const unbindListener = (): void => {
    if (!listenerBound || typeof document === 'undefined') return
    document.removeEventListener('visibilitychange', onVisibilityChange)
    listenerBound = false
  }

  const start = (): void => {
    if (active) return
    active = true
    // Assume the caller just fetched; avoid an immediate duplicate call.
    lastRunAt = Date.now()
    startTimer()
    bindListener()
  }

  const stop = (): void => {
    active = false
    clearTimer()
    unbindListener()
  }

  return { start, stop, isRunning: () => active }
}

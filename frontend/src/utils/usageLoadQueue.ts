/**
 * Usage request scheduler.
 *
 * The admin accounts list batches Anthropic OAuth/SetupToken passive usage in a
 * single request (see useTkAccountUsageBatch), so those rows no longer self-fetch.
 * The remaining per-cell fetches (gemini/antigravity/openai rows, and any view
 * that renders AccountUsageCell without an override) still go through here.
 *
 * A small concurrency cap is the safety net: if a page renders many residual
 * usage cells, they can't stampede the browser's connection pool (or the
 * upstream probe behind the active path) all at once. The cap is intentionally
 * low — these are best-effort UI reads, not latency-critical.
 */

import type { Account } from '@/types'

const MAX_CONCURRENT_USAGE_REQUESTS = 5

let active = 0
const waiters: Array<() => void> = []

function acquire(): Promise<void> {
  if (active < MAX_CONCURRENT_USAGE_REQUESTS) {
    active++
    return Promise.resolve()
  }
  return new Promise<void>((resolve) => {
    waiters.push(() => {
      active++
      resolve()
    })
  })
}

function release(): void {
  active--
  const next = waiters.shift()
  if (next) next()
}

/**
 * Schedule a usage fetch, bounded to MAX_CONCURRENT_USAGE_REQUESTS in flight.
 * Preserves the original signature so call sites are unchanged.
 */
export async function enqueueUsageRequest<T>(
  _account: Account,
  fn: () => Promise<T>
): Promise<T> {
  await acquire()
  try {
    return await fn()
  } finally {
    release()
  }
}

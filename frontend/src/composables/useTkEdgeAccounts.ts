/**
 * Edge Accounts overview composable (TokenKey-only).
 *
 * Owns all state + the API call for the read-only cross-edge account view, and
 * keeps the data live with a periodic auto-refresh (mirroring the per-edge admin
 * accounts page, which auto-refreshes). Without it the page only fetched once on
 * mount and could show a stale snapshot — e.g. a 5h-window utilization captured
 * just after a window roll — diverging from the always-fresh per-edge page.
 * Mirrors the TK convention of useTk*.ts composables (CLAUDE.md §5).
 */

import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { useIntervalFn } from '@vueuse/core'
import { adminAPI } from '@/api/admin'
import type { EdgeAccountsResult } from '@/api/admin/edgeAccounts'

// Each refresh fans out to every edge, so keep the cadence gentle. 30s is fresh
// enough to track the per-edge page without hammering the fan-out.
const AUTO_REFRESH_MS = 30_000

// 'all' is the sentinel the backend maps to an empty platform filter (every
// platform). The page defaults to it so the overview is complete; the filter
// narrows to a single platform.
export function useTkEdgeAccounts(initialPlatform = 'all') {
  const platform = ref(initialPlatform)
  const edges = ref<EdgeAccountsResult[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)
  const lastFetchedAt = ref<Date | null>(null)

  const okEdges = computed(() => edges.value.filter(e => e.ok))
  const failedEdges = computed(() => edges.value.filter(e => !e.ok))
  const totalAccounts = computed(() =>
    edges.value.reduce((n, e) => n + e.accounts.length, 0)
  )

  // Fleet-wide configured caps across all currently-schedulable accounts on
  // reachable edges. It is the sum of each account's *configured* limits — the
  // capacity that can take traffic right now — NOT live usage. Paused / disabled /
  // temp-unschedulable accounts are excluded (is_schedulable === false) so the
  // totals reflect only schedulable capacity. The active platform filter is already
  // applied upstream (the backend scopes `edges` to it), so this needs no extra
  // filtering. stickyRpm is base_rpm + rpm_sticky_buffer (the effective RPM ceiling
  // a sticky-routed request may reach), mirroring AccountCapacityCell's rpm display.
  const configTotals = computed(() => {
    let count = 0
    let concurrency = 0
    let baseRpm = 0
    let stickyRpm = 0
    let sessions = 0
    for (const e of edges.value) {
      if (!e.ok) continue
      for (const a of e.accounts) {
        if (!a.is_schedulable) continue
        count++
        concurrency += a.concurrency ?? 0
        baseRpm += a.base_rpm ?? 0
        stickyRpm += (a.base_rpm ?? 0) + (a.rpm_sticky_buffer ?? 0)
        sessions += a.max_sessions ?? 0
      }
    }
    return { count, concurrency, baseRpm, stickyRpm, sessions }
  })

  async function fetch() {
    if (loading.value) return
    loading.value = true
    error.value = null
    try {
      const agg = await adminAPI.edgeAccounts.list({ platform: platform.value })
      edges.value = agg.edges ?? []
      lastFetchedAt.value = new Date()
    } catch (e: unknown) {
      error.value = e instanceof Error ? e.message : String(e)
      edges.value = []
    } finally {
      loading.value = false
    }
  }

  // Switch the platform filter and immediately refetch (the auto-refresh would
  // otherwise leave the old platform's rows on screen for up to AUTO_REFRESH_MS).
  function setPlatform(p: string) {
    if (p === platform.value) return
    platform.value = p
    void fetch()
  }

  // Periodic auto-refresh; skip when the tab is hidden or a fetch is in flight.
  const { pause, resume } = useIntervalFn(
    () => {
      if (typeof document !== 'undefined' && document.hidden) return
      void fetch()
    },
    AUTO_REFRESH_MS,
    { immediate: false }
  )

  onMounted(() => {
    void fetch()
    resume()
  })
  onBeforeUnmount(() => pause())

  return {
    platform,
    edges,
    loading,
    error,
    lastFetchedAt,
    okEdges,
    failedEdges,
    totalAccounts,
    configTotals,
    fetch,
    setPlatform
  }
}

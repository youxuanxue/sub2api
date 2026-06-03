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

export function useTkEdgeAccounts(initialPlatform = 'anthropic') {
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
    fetch
  }
}

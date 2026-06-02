/**
 * Edge Accounts overview composable (TokenKey-only).
 *
 * Owns all state + the single API call for the read-only cross-edge account
 * view, keeping EdgeAccountsView.vue a thin template. Mirrors the TK convention
 * of useTk*.ts composables (CLAUDE.md §5).
 */

import { ref, computed } from 'vue'
import { adminAPI } from '@/api/admin'
import type { EdgeAccountsResult } from '@/api/admin/edgeAccounts'

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

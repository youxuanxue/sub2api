/**
 * Inline edge-panel state for the prod /accounts page (TokenKey-only).
 *
 * Composes useTkEdgeAccounts (edge data + ETag auto-refresh) and adds the
 * expand-state machine that drives the DataTable's #row-detail panels under each
 * `cc-<edge>` mirror-stub row — unified prod+edge governance, so the operator never
 * leaves /accounts to see/manage an edge's accounts.
 *
 * Expand policy (predictable: an explicit user choice ALWAYS wins):
 *   - explicit per-row toggle (persisted to localStorage) → its value, else
 *   - searching (prod search box non-empty) → expanded (the match auto-opens), else
 *   - the edge has an anomaly (unreachable / stub paused or cooling / any abnormal
 *     edge account) → expanded, else collapsed (healthy edges stay a one-line
 *     summary so the prod table isn't drowned in nested rows).
 *
 * State-only (CLAUDE.md §5): pure decisions live in utils/accountsEdgePanels.tk.ts.
 */

import { ref, computed, type Ref } from 'vue'
import type { Account } from '@/types'
import type { EdgeAccountSummary, EdgeAccountsResult } from '@/api/admin/edgeAccounts'
import { useTkEdgeAccounts } from '@/composables/useTkEdgeAccounts'
import { isStubPanelExpanded } from '@/utils/accountsEdgePanels.tk'

const OVERRIDES_STORAGE_KEY = 'tk-accounts-edge-panel-overrides'

// Persisted explicit expand(true)/collapse(false) choices, keyed by prod stub id.
function loadOverrides(): Map<number, boolean> {
  const m = new Map<number, boolean>()
  if (typeof localStorage === 'undefined') return m
  try {
    const raw = localStorage.getItem(OVERRIDES_STORAGE_KEY)
    if (!raw) return m
    const parsed = JSON.parse(raw) as Record<string, boolean>
    for (const [k, v] of Object.entries(parsed)) {
      const id = Number(k)
      if (Number.isFinite(id) && typeof v === 'boolean') m.set(id, v)
    }
  } catch {
    // ignore corrupt / privacy-mode reads
  }
  return m
}

type Getter<T> = Ref<T> | (() => T)
function read<T>(src: Getter<T>): T {
  return typeof src === 'function' ? (src as () => T)() : src.value
}

/**
 * @param prodAccounts reactive source of the CURRENT prod accounts page rows (so
 *   expand state is computed only for the stubs actually on screen).
 * @param search reactive source of the prod search box value (non-empty →
 *   auto-expand matching stubs).
 */
export function useTkAccountsEdgePanels(options: {
  prodAccounts: Getter<Account[]>
  search: Getter<string>
}) {
  // platform='all' → the panel shows the edge's FULL inventory across every
  // platform (anthropic + antigravity + grok + …), so a non-anthropic edge account
  // that has no prod mirror stub of its own is still visible/manageable here.
  const tk = useTkEdgeAccounts('all')

  const overrides = ref<Map<number, boolean>>(loadOverrides())

  function persist() {
    if (typeof localStorage === 'undefined') return
    try {
      const obj: Record<string, boolean> = {}
      overrides.value.forEach((v, k) => {
        obj[String(k)] = v
      })
      localStorage.setItem(OVERRIDES_STORAGE_KEY, JSON.stringify(obj))
    } catch {
      // ignore quota / privacy-mode write failures
    }
  }

  // edge_id → that edge's full (unfiltered) slice. Raw `edges` (not displayEdges):
  // the panel wants the complete edge inventory, not the standalone page's
  // status/group-filtered view.
  const edgeIndex = computed(() => {
    const m = new Map<string, EdgeAccountsResult>()
    for (const e of tk.edges.value) m.set(e.edge_id, e)
    return m
  })

  /** Only prod anthropic mirror stubs (edge_id set) can host an edge panel. */
  function isExpandable(account: Account): boolean {
    return !!account.edge_id
  }

  /** The edge slice backing a stub's panel, or null if not (yet) discovered. */
  function panelForStub(account: Account): EdgeAccountsResult | null {
    if (!account.edge_id) return null
    return edgeIndex.value.get(account.edge_id) ?? null
  }

  // The Set<stub id> the DataTable consumes. Recomputes on prod rows, edge data,
  // overrides, or search change.
  const expandedKeys = computed<Set<number>>(() => {
    const set = new Set<number>()
    const searching = (read(options.search) ?? '').trim().length > 0
    const idx = edgeIndex.value
    for (const acc of read(options.prodAccounts)) {
      if (!acc.edge_id) continue
      if (isStubPanelExpanded(overrides.value.get(acc.id), searching, idx.get(acc.edge_id) ?? null)) {
        set.add(acc.id)
      }
    }
    return set
  })

  /** Toggle one stub's panel; records an explicit override (persisted). */
  function toggle(account: Account) {
    if (!account.edge_id) return
    const cur = expandedKeys.value.has(account.id)
    const next = new Map(overrides.value)
    next.set(account.id, !cur)
    overrides.value = next
    persist()
  }

  /**
   * Pin a stub's panel to an explicit state. Called after an inline op resolves an
   * anomaly (set true) so the panel the operator is working in does NOT auto-collapse
   * when its anomaly clears — untouched anomalous panels still auto-collapse on
   * resolve (attention follows problems), but a touched one stays put.
   */
  function setExpanded(stubId: number, expanded: boolean) {
    const next = new Map(overrides.value)
    next.set(stubId, expanded)
    overrides.value = next
    persist()
  }

  /**
   * Surgically replace one edge-local account in the edge data after a successful
   * inline write op returns the updated DTO — instant, precise UI update with no
   * full re-fetch (and no all-panels loading flicker). Immutable swaps so Vue
   * reactivity fires and accountVm's WeakMap mints a fresh vm for the new object.
   */
  function applyAccountUpdate(edgeId: string, updated: EdgeAccountSummary) {
    const list = tk.edges.value
    const ei = list.findIndex((e) => e.edge_id === edgeId)
    if (ei < 0) return
    const edge = list[ei]
    const ai = edge.accounts.findIndex((a) => a.id === updated.id)
    if (ai < 0) return
    const newAccounts = edge.accounts.slice()
    newAccounts[ai] = updated
    const newList = list.slice()
    newList[ei] = { ...edge, accounts: newAccounts }
    tk.edges.value = newList
  }

  function setAllVisible(expanded: boolean) {
    const next = new Map(overrides.value)
    for (const acc of read(options.prodAccounts)) {
      if (acc.edge_id) next.set(acc.id, expanded)
    }
    overrides.value = next
    persist()
  }
  function expandAll() {
    setAllVisible(true)
  }
  function collapseAll() {
    setAllVisible(false)
  }

  // Whether ANY visible stub is currently collapsed — drives the toolbar toggle
  // label ("展开全部" vs "折叠全部").
  const hasCollapsedVisible = computed(() => {
    const keys = expandedKeys.value
    for (const acc of read(options.prodAccounts)) {
      if (acc.edge_id && !keys.has(acc.id)) return true
    }
    return false
  })
  // Whether ANY prod row on the current page is an edge stub (gates the toolbar control).
  const hasAnyStub = computed(() => read(options.prodAccounts).some((a) => !!a.edge_id))

  return {
    // edge data lifecycle (from useTkEdgeAccounts)
    edges: tk.edges,
    edgeLoading: tk.loading,
    edgeError: tk.error,
    refreshEdges: tk.fetch,
    // panel resolution + expand state
    edgeIndex,
    isExpandable,
    panelForStub,
    expandedKeys,
    toggle,
    setExpanded,
    applyAccountUpdate,
    expandAll,
    collapseAll,
    hasCollapsedVisible,
    hasAnyStub
  }
}

/**
 * Inline edge-panel state for the prod /accounts page (TokenKey-only).
 *
 * Composes useTkEdgeAccounts (edge data + ETag auto-refresh) and adds the
 * expand-state machine that drives the DataTable's #row-detail panels under each
 * `cc-<edge>` mirror-stub row — unified prod+edge governance, so the operator never
 * leaves /accounts to see/manage an edge's accounts.
 *
 * Expand policy (v2, predictable): default-full-expand — every stub's panel is open
 * unless the operator explicitly collapsed it (per-row toggle / collapse-all,
 * persisted to localStorage). Anomaly drives highlight + within-panel ordering, not
 * visibility.
 *
 * State-only (CLAUDE.md §5): pure decisions live in utils/accountsEdgePanels.tk.ts.
 */

import { ref, computed, type Ref } from 'vue'
import type { Account } from '@/types'
import type { EdgeAccountSummary, EdgeAccountsResult } from '@/api/admin/edgeAccounts'
import { useTkEdgeAccounts } from '@/composables/useTkEdgeAccounts'
import {
  isStubPanelExpanded,
  edgePanelCounts,
  edgePanelAbnormalCount
} from '@/utils/accountsEdgePanels.tk'

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
 */
export function useTkAccountsEdgePanels(options: {
  prodAccounts: Getter<Account[]>
}) {
  // byStub → the backend returns one result PER prod mirror stub (any platform),
  // each scoped to exactly that stub key's edge-side group (precise correspondence:
  // cc-us4 → its default group's 2 accounts, not all of us4). Keyed by stub account
  // id below, NOT edge_id — cc-us4 / openai-us4 / grok-us4 share one edge host but
  // are three distinct panels.
  const tk = useTkEdgeAccounts('all', { byStub: true })

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

  // stub_account_id → that stub's precise slice (the accounts its edge-side key
  // schedules). Keyed by stub id, NOT edge_id: multiple stubs (cc-us4 / openai-us4 /
  // grok-us4) share one edge host but each is its own panel. Raw `edges` (the by-stub
  // aggregate); the standalone page's status/group filters do not apply here.
  const stubIndex = computed(() => {
    const m = new Map<number, EdgeAccountsResult>()
    for (const e of tk.edges.value) {
      if (typeof e.stub_account_id === 'number') m.set(e.stub_account_id, e)
    }
    return m
  })

  /** Only prod mirror stubs (any platform; edge_id set) can host an edge panel. */
  function isExpandable(account: Account): boolean {
    return !!account.edge_id
  }

  /** The precise slice backing this stub's panel, or null if not (yet) discovered. */
  function panelForStub(account: Account): EdgeAccountsResult | null {
    if (!account.edge_id) return null
    return stubIndex.value.get(account.id) ?? null
  }

  /**
   * One-line summary for a stub's COLLAPSED row, so a folded panel still shows what
   * it holds (the #885 invisible-collapsed bug). `discovered=false` until the by-stub
   * aggregate resolves this stub.
   */
  function panelSummary(account: Account): {
    discovered: boolean
    total: number
    schedulable: number
    abnormal: number
  } {
    const e = panelForStub(account)
    if (!e) return { discovered: false, total: 0, schedulable: 0, abnormal: 0 }
    const { total, schedulable } = edgePanelCounts(e)
    return { discovered: true, total, schedulable, abnormal: edgePanelAbnormalCount(e) }
  }

  // The Set<stub id> the DataTable consumes. Recomputes on prod rows, edge data,
  // overrides, or search change.
  const expandedKeys = computed<Set<number>>(() => {
    const set = new Set<number>()
    for (const acc of read(options.prodAccounts)) {
      if (!acc.edge_id) continue
      if (isStubPanelExpanded(overrides.value.get(acc.id))) set.add(acc.id)
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
    let changed = false
    // Per-stub: several results can share one edge host, and an edge account may sit
    // in more than one stub's group, so update the account in EVERY result on that
    // edge that holds it (immutable swaps so Vue reactivity + accountVm's WeakMap fire).
    const newList = list.map((e) => {
      if (e.edge_id !== edgeId) return e
      const ai = e.accounts.findIndex((a) => a.id === updated.id)
      if (ai < 0) return e
      const newAccounts = e.accounts.slice()
      newAccounts[ai] = updated
      changed = true
      return { ...e, accounts: newAccounts }
    })
    if (changed) tk.edges.value = newList
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
    stubIndex,
    isExpandable,
    panelForStub,
    panelSummary,
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

/**
 * Pure helpers for the inline edge-account panels on the prod /accounts page
 * (TokenKey-only). A `cc-<edge>` mirror-stub row expands into a panel showing that
 * edge's real accounts; these decide WHEN a panel auto-expands and how to key an
 * edge account for unified-table identity.
 *
 * Kept as a *.tk.ts module (CLAUDE.md §5: pure maps live in `*.tk.ts`) so the
 * components stay template-only and the composable stays state-only. No side
 * effects. Reuses edgeAccounts.tk.ts so the anomaly/health semantics match the
 * standalone Edge Accounts overview exactly.
 */

import type { EdgeAccountSummary, EdgeAccountsResult } from '@/api/admin/edgeAccounts'
import {
  schedulableCount,
  isTempUnschedActive,
  isStubRateLimited,
  isStubTempUnschedActive
} from '@/utils/edgeAccounts.tk'

function isFuture(ts?: string | null): boolean {
  return !!ts && new Date(ts).getTime() > Date.now()
}

/**
 * Whether ONE edge-local account is in a state worth the operator's attention —
 * the per-account half of the panel's anomaly test. Genuinely-problematic states
 * only (NOT operator-intentional pauses): error/inactive status, or a STILL-LIVE
 * rate-limit / temp-unschedulable / overload cooldown. Mirrors the cooldown gates
 * in edgeAccounts.tk (isTempUnschedActive) / AccountStatusIndicator so a lapsed
 * cooldown breadcrumb doesn't read as a live problem forever.
 */
export function edgeAccountIsAbnormal(a: EdgeAccountSummary): boolean {
  if (a.status === 'error' || a.status === 'inactive') return true
  if (isFuture(a.rate_limit_reset_at)) return true
  if (isTempUnschedActive(a)) return true
  if (isFuture(a.overload_until)) return true
  return false
}

/**
 * Whether an edge's panel should DEFAULT-expand: something needs attention. True
 * when the edge is unreachable, the prod→edge relay (stub) is paused or in a live
 * cooldown, or ANY edge-local account is abnormal. Healthy edges stay collapsed to
 * a one-line summary so the prod table isn't drowned in nested rows. The same
 * stub-health gates the standalone overview's header badges use are reused here.
 */
export function edgePanelHasAnomaly(edge: EdgeAccountsResult): boolean {
  if (!edge.ok) return true
  if (!edge.stub_schedulable) return true
  if (isStubRateLimited(edge)) return true
  if (isStubTempUnschedActive(edge)) return true
  return edge.accounts.some(edgeAccountIsAbnormal)
}

/**
 * The composite key for ONE edge-local account in the unified table. Prod stub ids
 * and edge-local account ids are independent DB primary keys (both small ints) and
 * collide, so an edge account is keyed as `edge:<edge_id>:<local_id>` — unique
 * across edges and distinct from any prod row's bare numeric id.
 */
export function compositeEdgeAccountKey(edgeId: string, localId: number): string {
  return `edge:${edgeId}:${localId}`
}

/** Counts for an edge's collapsed one-line summary ("N 账号 · M 可调度"). */
export function edgePanelCounts(edge: EdgeAccountsResult): { total: number; schedulable: number } {
  return { total: edge.accounts.length, schedulable: schedulableCount(edge) }
}

/**
 * Whether ONE stub's panel is expanded — the core of the expand-state machine,
 * kept pure so the composable's expandedKeys computed is a thin wrapper and this
 * decision is unit-tested directly.
 *
 * v2 core flip: the DEFAULT is expanded (一目了然 — the operator sees every stub's
 * accounts on arrival, no manual expand, no切页). #885's anomaly-only default left
 * healthy stubs as invisible flat rows that read as "the feature didn't ship". The
 * `edge` arg is no longer consulted for the expand decision — anomaly now drives
 * HIGHLIGHT + ordering (edgePanelHasAnomaly / compareStubPanels), not visibility.
 *
 * Priority (an explicit user choice ALWAYS wins, so the operator stays in control):
 *   1. `override` set (the user toggled / expand-all / collapse-all, persisted) → its value
 *   2. otherwise expanded (default-full-expand). searching is subsumed: the default
 *      is already open, and an explicit collapse override still wins over a search.
 */
export function isStubPanelExpanded(
  override: boolean | undefined,
  _searching: boolean,
  _edge: EdgeAccountsResult | null
): boolean {
  if (override !== undefined) return override
  return true
}

/** Count of attention-worthy edge accounts in a panel (for the collapsed summary). */
export function edgePanelAbnormalCount(edge: EdgeAccountsResult): number {
  return edge.accounts.reduce((n, a) => (edgeAccountIsAbnormal(a) ? n + 1 : n), 0)
}

/**
 * Edge accounts sorted abnormal-first (仅异常高亮 的排序版: 高亮让坏的跳出来, 置顶让坏的
 * 够得到), keeping a stable order within each band by priority then id so the list does
 * not churn across refreshes. Returns a new array; never mutates the input.
 */
export function sortEdgeAccountsAbnormalFirst(accounts: EdgeAccountSummary[]): EdgeAccountSummary[] {
  return accounts.slice().sort((a, b) => {
    const aBad = edgeAccountIsAbnormal(a)
    const bBad = edgeAccountIsAbnormal(b)
    if (aBad !== bBad) return aBad ? -1 : 1
    if (a.priority !== b.priority) return a.priority - b.priority
    return a.id - b.id
  })
}

/**
 * Pure presentational helpers for the Edge Accounts overview (TokenKey-only).
 *
 * Kept as a *.tk.ts module (CLAUDE.md §5: pure maps live in `*.tk.ts`) so the
 * view stays template-only and the composable stays state-only. No side effects.
 */

import type { EdgeAccountSummary, EdgeAccountsResult } from '@/api/admin/edgeAccounts'
import type { Account, WindowStats, AccountUsageInfo } from '@/types'

/**
 * Count of effectively-schedulable accounts in an edge slice.
 */
export function schedulableCount(e: EdgeAccountsResult): number {
  return e.accounts.reduce((n, a) => (a.is_schedulable ? n + 1 : n), 0)
}

/**
 * Whether an account's temp-unschedulable cooldown is STILL active right now.
 *
 * `temp_unschedulable_reason` is NOT cleared when the cooldown lapses — it
 * persists in the DB as a forensic breadcrumb. A populated reason therefore does
 * NOT mean the account is currently cooled; the decisive check is whether
 * `temp_unschedulable_until` is still in the future (mirrors the same gate in
 * AccountStatusIndicator.vue / TempUnschedStatusModal.vue). The Edge Accounts
 * page must gate the alarming amber reason styling on this, or every account
 * that ever hit a short 429 cooldown reads as a live problem forever.
 */
export function isTempUnschedActive(s: EdgeAccountSummary): boolean {
  if (!s.temp_unschedulable_until) return false
  return new Date(s.temp_unschedulable_until).getTime() > Date.now()
}

/**
 * Adapts a credential-free EdgeAccountSummary into the admin `Account` shape so
 * the read-only Edge Accounts page can reuse AccountCapacityCell verbatim (the
 * cell only reads capacity/gauge fields). Missing-but-required Account fields are
 * filled with inert defaults; `expires_at` is dropped (the edge DTO carries it as
 * an RFC3339 string while Account types it as a unix number, and the cell doesn't
 * read it). No side effects — pure mapping.
 */
export function toAccountLike(s: EdgeAccountSummary): Account {
  return {
    id: s.id,
    name: s.name,
    platform: s.platform as Account['platform'],
    type: s.type as Account['type'],
    channel_type: s.channel_type,
    proxy_id: null,
    concurrency: s.concurrency,
    current_concurrency: s.current_concurrency,
    priority: s.priority,
    rate_multiplier: s.rate_multiplier,
    status: s.status as Account['status'],
    error_message: s.error_message ?? null,
    last_used_at: s.last_used_at ?? null,
    expires_at: null,
    auto_pause_on_expired: false,
    created_at: s.created_at,
    updated_at: s.created_at,
    schedulable: s.schedulable,
    rate_limited_at: s.rate_limited_at ?? null,
    rate_limit_reset_at: s.rate_limit_reset_at ?? null,
    overload_until: s.overload_until ?? null,
    temp_unschedulable_until: s.temp_unschedulable_until ?? null,
    temp_unschedulable_reason: s.temp_unschedulable_reason ?? null,
    session_window_start: null,
    session_window_end: s.session_window_end ?? null,
    session_window_status: (s.session_window_status as Account['session_window_status']) ?? null,
    window_cost_limit: s.window_cost_limit ?? null,
    window_cost_sticky_reserve: s.window_cost_sticky_reserve ?? null,
    max_sessions: s.max_sessions ?? null,
    session_idle_timeout_minutes: s.session_idle_timeout_minutes ?? null,
    base_rpm: s.base_rpm ?? null,
    rpm_strategy: s.rpm_strategy ?? null,
    rpm_sticky_buffer: s.rpm_sticky_buffer ?? null,
    current_window_cost: s.current_window_cost ?? null,
    active_sessions: s.active_sessions ?? null,
    current_rpm: s.current_rpm ?? null
  }
}

// --- Client-side status / group filters -------------------------------------
//
// The Edge Accounts page filters the already-fetched aggregate on the prod side
// (no per-edge re-query): the backend fan-out returns every status/group, and
// these predicates narrow the view in the browser. Status semantics MIRROR the
// admin accounts page's server-side filter (backend/internal/repository/
// account_repo.go applyStatusFilter): the 'active' bucket splits the
// status='active' rows into active / rate_limited / temp_unschedulable /
// unschedulable derived states; 'inactive' / 'error' match the raw status column.
// Keeping the exact same partition means the two pages read consistently.

/** Status filter sentinel for "all statuses" — matches the admin page's '' value. */
export const EDGE_STATUS_ALL = ''
/** Group filter sentinels — mirror the admin page ('' = all, 'ungrouped' = no group). */
export const EDGE_GROUP_ALL = ''
export const EDGE_GROUP_UNGROUPED = 'ungrouped'

function isFuture(ts?: string | null): boolean {
  return !!ts && new Date(ts).getTime() > Date.now()
}

/**
 * Whether an account matches the selected status filter. Replicates the
 * server-side predicates so the prod-side client filter and the admin accounts
 * page agree on what each bucket means. Unknown / '' status → always matches.
 */
export function matchesStatusFilter(s: EdgeAccountSummary, status: string): boolean {
  if (!status || status === EDGE_STATUS_ALL) return true
  const active = s.status === 'active'
  const rateLimited = isFuture(s.rate_limit_reset_at)
  const tempUnsched = isFuture(s.temp_unschedulable_until)
  switch (status) {
    case 'active':
      // status=active AND schedulable AND not rate-limited AND not temp-unsched.
      return active && s.schedulable && !rateLimited && !tempUnsched
    case 'rate_limited':
      // status=active, rate-limit window still open, not temp-unsched.
      return active && rateLimited && !tempUnsched
    case 'temp_unschedulable':
      return active && tempUnsched
    case 'unschedulable':
      // status=active but operator-paused (schedulable=false), no live cooldown.
      return active && !s.schedulable && !rateLimited && !tempUnsched
    default:
      // 'inactive' / 'error' — literal status column match.
      return s.status === status
  }
}

/**
 * Whether an account matches the selected group filter. '' = all, 'ungrouped' =
 * account belongs to no group, otherwise an exact group-name match (the edge DTO
 * carries `groups` as names, so we filter by name).
 */
export function matchesGroupFilter(s: EdgeAccountSummary, group: string): boolean {
  if (!group || group === EDGE_GROUP_ALL) return true
  if (group === EDGE_GROUP_UNGROUPED) return !s.groups || s.groups.length === 0
  return !!s.groups && s.groups.includes(group)
}

/**
 * Sorted, de-duplicated set of group names present across the reachable edges'
 * accounts — the option source for the 分组 dropdown. Derived from the live data
 * (not the prod group catalog) so the dropdown only ever offers groups that
 * actually appear, with no name/id mismatch against the edge-local groups.
 */
export function collectGroupNames(edges: EdgeAccountsResult[]): string[] {
  const names = new Set<string>()
  for (const e of edges) {
    if (!e.ok) continue
    for (const a of e.accounts) {
      for (const g of a.groups ?? []) names.add(g)
    }
  }
  return Array.from(names).sort((a, b) => a.localeCompare(b))
}

/**
 * Per-account presentational view-model: the three adapter outputs the row cells
 * consume, computed once and memoized by the account object's identity. The
 * composable's incremental merge (mergeEdges) preserves an account object's
 * reference whenever its edge payload is byte-identical across a refresh, so this
 * WeakMap returns the SAME vm object — keeping AccountCapacityCell /
 * AccountUsageCell / AccountStatusIndicator props reference-stable so they skip
 * re-rendering on unrelated reactivity ticks (auto-refresh, hover, filter change).
 * Previously the template called toAccountLike/toWindowStats/toUsageInfo inline,
 * minting fresh objects on every render and forcing those cells to re-render.
 */
export interface EdgeAccountVm {
  accountLike: Account
  windowStats: WindowStats | null
  usageInfo: AccountUsageInfo | null
}

const vmCache = new WeakMap<EdgeAccountSummary, EdgeAccountVm>()

export function accountVm(s: EdgeAccountSummary): EdgeAccountVm {
  const cached = vmCache.get(s)
  if (cached) return cached
  const vm: EdgeAccountVm = {
    accountLike: toAccountLike(s),
    windowStats: toWindowStats(s),
    usageInfo: toUsageInfo(s)
  }
  vmCache.set(s, vm)
  return vm
}

/** Extracts the today-stats as the WindowStats shape AccountTodayStatsCell wants. */
export function toWindowStats(s: EdgeAccountSummary): WindowStats | null {
  if (!s.today_stats) return null
  return {
    requests: s.today_stats.requests,
    tokens: s.today_stats.tokens,
    cost: s.today_stats.cost,
    user_cost: s.today_stats.user_cost
  }
}

/**
 * Builds the AccountUsageInfo shape AccountUsageCell renders (5h/7d bars), from
 * the edge DTO's passive `usage`. Returns null when the edge reported no usage
 * windows (non-oauth accounts) — passing null as usageOverride still suppresses
 * the cell's self-fetch (the account lives on a remote edge). The countdown is
 * derived from resets_at by UsageProgressBar, so remaining_seconds is inert.
 */
export function toUsageInfo(s: EdgeAccountSummary): AccountUsageInfo | null {
  if (!s.usage) return null
  const mk = (p?: { utilization: number; resets_at?: string | null }) =>
    p ? { utilization: p.utilization, resets_at: p.resets_at ?? null, remaining_seconds: 0 } : null
  return {
    source: s.usage.source === 'active' ? 'active' : 'passive',
    updated_at: null,
    five_hour: mk(s.usage.five_hour),
    seven_day: mk(s.usage.seven_day),
    seven_day_sonnet: mk(s.usage.seven_day_sonnet)
  }
}

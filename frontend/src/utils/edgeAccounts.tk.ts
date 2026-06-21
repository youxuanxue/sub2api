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
 * The edge stores per-class cooldowns under the full scope key
 * `anthropic:class:sonnet`. AccountStatusIndicator's `formatScopeName` has no alias
 * for that raw key (it would render verbatim), so the TK adapter strips the
 * `anthropic:class:` prefix to the bare model class (`sonnet`) before handing the
 * map to the shared badge — keeping the upstream-shared component untouched
 * (CLAUDE.md §5 minimal injection). The badge then reads `sonnet 限流至 HH:MM`.
 *
 * The shared `Account.extra.model_rate_limits` consumer requires non-optional
 * `rate_limited_at` / `rate_limit_reset_at` strings; the edge DTO leaves
 * `rate_limited_at` optional, so fall back to `rate_limit_reset_at` (or '') — the
 * indicator only gates visibility on `rate_limit_reset_at` being in the future.
 */
const ANTHROPIC_CLASS_PREFIX = 'anthropic:class:'

export function stripClassPrefix(
  m: Record<string, { rate_limited_at?: string; rate_limit_reset_at?: string; reason?: string }>
): Record<string, { rate_limited_at: string; rate_limit_reset_at: string }> {
  const out: Record<string, { rate_limited_at: string; rate_limit_reset_at: string }> = {}
  for (const [scope, info] of Object.entries(m)) {
    const key = scope.startsWith(ANTHROPIC_CLASS_PREFIX)
      ? scope.slice(ANTHROPIC_CLASS_PREFIX.length)
      : scope
    const resetAt = info.rate_limit_reset_at ?? ''
    out[key] = {
      rate_limited_at: info.rate_limited_at ?? resetAt,
      rate_limit_reset_at: resetAt
    }
  }
  return out
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
    current_rpm: s.current_rpm ?? null,
    // Light up the already-mounted AccountStatusIndicator per-class 限流 badge with
    // the edge's active model_rate_limits (scope prefix stripped). Keep `extra`
    // undefined when absent — do NOT emit `{}`, so mergeEdges' JSON diff stays
    // stable and the row doesn't re-render on every auto-refresh.
    extra: s.model_rate_limits
      ? { model_rate_limits: stripClassPrefix(s.model_rate_limits) }
      : undefined
  }
}

// --- Client-side status / group filters -------------------------------------
//
// The Edge Accounts page filters the already-fetched aggregate on the prod side
// (no per-edge re-query): the backend fan-out returns every status/group plus the
// PROD mirror stub's own status/group snapshot, and these predicates narrow the
// view in the browser.
//
// Filter source = the PROD stub, NOT the edge-local accounts (per the page's
// contract):
//   - 分组: the dropdown options + filter key on the prod stub's groups
//     (`EdgeAccountsResult.stub_groups`) — how prod organizes the fleet — so it is
//     edge-level (one stub per edge), not per-account.
//   - 状态: combine the prod stub's status with each edge account's own status.
//     正常 (active) requires BOTH to be healthy; every other (abnormal) bucket is
//     an OR — surface the row if EITHER the prod stub or the edge account is in
//     that state.
//
// The per-account status partition itself MIRRORS the admin accounts page's
// server-side filter (backend/internal/repository/account_repo.go applyStatusFilter):
// 'active' splits the status='active' rows into active / rate_limited /
// temp_unschedulable / unschedulable derived states; 'inactive' / 'error' match the
// raw status column. The same predicate is reused for the stub and the account so
// both buckets identically.

/** Status filter sentinel for "all statuses" — matches the admin page's '' value. */
export const EDGE_STATUS_ALL = ''
/** Group filter sentinels — mirror the admin page ('' = all, 'ungrouped' = no group). */
export const EDGE_GROUP_ALL = ''
export const EDGE_GROUP_UNGROUPED = 'ungrouped'

/**
 * Minimal status-bearing shape both an edge account (EdgeAccountSummary) and a prod
 * mirror stub satisfy, so matchesStatusFilter buckets them identically.
 */
export interface EdgeStatusBearing {
  status: string
  schedulable: boolean
  rate_limit_reset_at?: string | null
  temp_unschedulable_until?: string | null
}

function isFuture(ts?: string | null): boolean {
  return !!ts && new Date(ts).getTime() > Date.now()
}

/**
 * Whether a status-bearing record (edge account OR prod stub) matches the selected
 * status filter. Replicates the server-side predicates so the prod-side client
 * filter and the admin accounts page agree on what each bucket means. Unknown / ''
 * status → always matches.
 */
export function matchesStatusFilter(s: EdgeStatusBearing, status: string): boolean {
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
 * The prod mirror stub's status descriptor for an edge. The overview's 状态 filter
 * combines THIS (prod's relay handle for the edge) with each edge account's status.
 * status is hard-coded 'active': mirror stubs are discovered status='active' only
 * (ListByPlatform pins it, discovery skips StatusDisabled), so the column is a
 * provable constant and is NOT plumbed over the wire — only the genuinely-variable
 * schedulable/cooldown fields are. schedulable=false here is the 关调度 (prod stopped
 * routing) case the "调度已关闭" badge shows.
 */
export function edgeStubStatusBearing(e: EdgeAccountsResult): EdgeStatusBearing {
  return {
    status: 'active',
    schedulable: e.stub_schedulable,
    rate_limit_reset_at: e.stub_rate_limit_reset_at ?? null,
    temp_unschedulable_until: e.stub_temp_unschedulable_until ?? null
  }
}

/**
 * Whether the prod stub's rate-limit / temp-unschedulable cooldown is STILL live.
 * These drive the edge-header badges so a stub-driven 状态 filter match (which keeps
 * an edge's otherwise-healthy rows via the OR) has a visible cause, the same way the
 * "调度已关闭" badge surfaces stub_schedulable=false. A populated reset/until in the
 * past does not count (the cooldown lapsed) — mirrors isTempUnschedActive.
 */
export function isStubRateLimited(e: EdgeAccountsResult): boolean {
  return isFuture(e.stub_rate_limit_reset_at)
}

export function isStubTempUnschedActive(e: EdgeAccountsResult): boolean {
  return isFuture(e.stub_temp_unschedulable_until)
}

/**
 * Combined status match for one edge account ROW: combine the prod stub's status
 * with the account's own. 正常 (active) requires BOTH the prod stub AND the edge
 * account to be healthy; every other (abnormal) bucket is an OR — surface the row
 * if EITHER side is in that state. '' (all) always matches.
 */
export function matchesCombinedStatusFilter(
  account: EdgeAccountSummary,
  edge: EdgeAccountsResult,
  status: string
): boolean {
  if (!status || status === EDGE_STATUS_ALL) return true
  const stub = edgeStubStatusBearing(edge)
  if (status === 'active') {
    return matchesStatusFilter(stub, 'active') && matchesStatusFilter(account, 'active')
  }
  return matchesStatusFilter(stub, status) || matchesStatusFilter(account, status)
}

/**
 * Status match for an UNREACHABLE edge (no account rows to combine): fall back to
 * the prod stub's own status. 正常 can't be confirmed when the edge's accounts are
 * unknown, so it is never shown under the 正常 filter; an abnormal filter shows the
 * edge iff the stub itself is in that state. '' (all) always matches.
 */
export function matchesStubOnlyStatusFilter(edge: EdgeAccountsResult, status: string): boolean {
  if (!status || status === EDGE_STATUS_ALL) return true
  if (status === 'active') return false
  return matchesStatusFilter(edgeStubStatusBearing(edge), status)
}

/**
 * Whether an edge's PROD mirror stub matches the selected 分组 filter. The page
 * filters by the prod stub's group (how prod organizes the fleet), NOT the
 * edge-local accounts' groups: '' = all, 'ungrouped' = the stub belongs to no
 * group, otherwise an exact stub-group-name match. Edge-level (one stub per edge).
 */
export function matchesStubGroupFilter(edge: EdgeAccountsResult, group: string): boolean {
  if (!group || group === EDGE_GROUP_ALL) return true
  const groups = edge.stub_groups
  if (group === EDGE_GROUP_UNGROUPED) return !groups || groups.length === 0
  return !!groups && groups.includes(group)
}

/**
 * Sorted, de-duplicated set of PROD mirror stub group names across edges — the
 * option source for the 分组 dropdown. Sourced from the prod stubs (the page filters
 * by stub group) and from ALL edges including unreachable ones: a stub's group is
 * known from the prod DB regardless of whether its edge is reachable.
 */
export function collectStubGroupNames(edges: EdgeAccountsResult[]): string[] {
  const names = new Set<string>()
  for (const e of edges) {
    for (const g of e.stub_groups ?? []) names.add(g)
  }
  return Array.from(names).sort((a, b) => a.localeCompare(b))
}

/**
 * The post-filter edge list for the overview, given the active 状态 + 分组 filters.
 * Pure (no Vue reactivity) so it is unit-testable and the composable's displayEdges
 * computed is a thin wrapper. Encodes the Edge Accounts filter contract:
 *   - 分组 filters edge-level by the PROD stub's group;
 *   - 状态 combines the prod stub's status with each edge account's status (正常 =
 *     both healthy; any other bucket = stub OR account);
 *   - an UNREACHABLE edge keeps no account rows and is matched on the stub alone.
 *
 * Fast path: with NO filter active, return the input `edges` array unchanged (same
 * reference) so the composable's incremental-merge reference stability (no re-render
 * on auto-refresh) is preserved. A reachable edge whose accounts all filter out and
 * whose stub alone does not match is dropped; an unfiltered view still shows
 * reachable-but-empty edges.
 */
export function filterDisplayEdges(
  edges: EdgeAccountsResult[],
  statusFilter: string,
  groupFilter: string
): EdgeAccountsResult[] {
  const statusInactive = !statusFilter || statusFilter === EDGE_STATUS_ALL
  const groupInactive = !groupFilter || groupFilter === EDGE_GROUP_ALL
  if (statusInactive && groupInactive) return edges
  const out: EdgeAccountsResult[] = []
  for (const e of edges) {
    if (!matchesStubGroupFilter(e, groupFilter)) continue
    if (!e.ok) {
      if (matchesStubOnlyStatusFilter(e, statusFilter)) out.push(e)
      continue
    }
    const accounts = e.accounts.filter((a) => matchesCombinedStatusFilter(a, e, statusFilter))
    if (accounts.length === 0) {
      // No matching account rows. A reachable edge with zero local accounts (fresh
      // edge / all accounts deleted) is still surfaced when the prod STUB alone
      // matches — mirroring the unreachable branch, so the "stub OR account" rule
      // holds symmetrically (e.g. a rate-limited stub on an account-less edge). When
      // accounts exist but the stub matches an abnormal bucket they all match via the
      // OR, so this fallback only ever fires on a genuinely empty edge.
      if (matchesStubOnlyStatusFilter(e, statusFilter)) out.push({ ...e, accounts })
      continue
    }
    out.push({ ...e, accounts })
  }
  return out
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
 *
 * Deliberate trade-off: with stable props, AccountStatusIndicator's time-based
 * displays (rate-limit / overload countdowns) no longer re-tick every second —
 * they refresh when the account's edge payload changes (the ~30s auto-refresh
 * poll), which is exactly the per-second full-table re-render this removes. On a
 * read-only overview that is the right call; the live per-second view is the
 * per-edge /admin/accounts page reached via 管理账号. Do NOT "fix" a frozen
 * countdown by dropping this memo — that reintroduces the perf regression.
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

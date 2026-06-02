/**
 * Pure presentational helpers for the Edge Accounts overview (TokenKey-only).
 *
 * Kept as a *.tk.ts module (CLAUDE.md §5: pure maps live in `*.tk.ts`) so the
 * view stays template-only and the composable stays state-only. No side effects.
 */

import type { EdgeAccountSummary, EdgeAccountsResult } from '@/api/admin/edgeAccounts'
import type { Account, WindowStats } from '@/types'

export type StatusVariant = 'success' | 'warning' | 'danger' | 'neutral'

/**
 * Maps an account's effective state to a colour variant for a status pill.
 * Read-only derivation — does not call any API.
 */
export function accountStatusVariant(a: EdgeAccountSummary): StatusVariant {
  if (a.status !== 'active') return 'danger'
  if (a.error_message) return 'danger'
  if (a.rate_limit_reset_at || a.overload_until || a.temp_unschedulable_until) return 'warning'
  if (!a.is_schedulable) return 'warning'
  return 'success'
}

/**
 * Short human label for an account's effective schedulability.
 */
export function accountStateLabel(a: EdgeAccountSummary): string {
  if (a.status !== 'active') return a.status
  if (a.overload_until) return 'overloaded'
  if (a.rate_limit_reset_at) return 'rate-limited'
  if (a.temp_unschedulable_until) return 'temp-unschedulable'
  if (!a.schedulable) return 'unschedulable'
  return 'schedulable'
}

/**
 * Count of effectively-schedulable accounts in an edge slice.
 */
export function schedulableCount(e: EdgeAccountsResult): number {
  return e.accounts.reduce((n, a) => (a.is_schedulable ? n + 1 : n), 0)
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

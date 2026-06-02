/**
 * Pure presentational helpers for the Edge Accounts overview (TokenKey-only).
 *
 * Kept as a *.tk.ts module (CLAUDE.md §5: pure maps live in `*.tk.ts`) so the
 * view stays template-only and the composable stays state-only. No side effects.
 */

import type { EdgeAccountSummary, EdgeAccountsResult } from '@/api/admin/edgeAccounts'

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
 * Variant for an edge's reachability pill.
 */
export function edgeStatusVariant(e: EdgeAccountsResult): StatusVariant {
  return e.ok ? 'success' : 'danger'
}

/**
 * Count of effectively-schedulable accounts in an edge slice.
 */
export function schedulableCount(e: EdgeAccountsResult): number {
  return e.accounts.reduce((n, a) => (a.is_schedulable ? n + 1 : n), 0)
}

/**
 * Admin Edge Accounts API (TokenKey-only).
 *
 * Read-only cross-edge account overview. The prod backend discovers every edge
 * via the local anthropic mirror stubs and fans out to each edge's
 * GET /api/v1/edge/accounts, returning a per-edge grouped, credential-free
 * payload. See backend/internal/service/edge_accounts_aggregator_tk.go and
 * backend/internal/handler/admin/edge_accounts_handler_tk.go.
 *
 * Mirrors api/admin/tier.ts (CLAUDE.md §5: TK-only surface in a dedicated module).
 */

import { apiClient } from '../client'
import type { AccountUsageInfo } from '@/types'

/**
 * One account as reported by an edge. Mirrors backend handler.edgeAccountDTO /
 * service.EdgeAccountSummary. NEVER contains credentials.
 */
export interface EdgeAccountSummary {
  id: number
  name: string
  platform: string
  type: string
  channel_type?: number
  status: string
  schedulable: boolean
  is_schedulable: boolean
  concurrency: number
  priority: number
  rate_multiplier: number
  error_message?: string
  // Operator 备注 (admin remark), mirrors the admin accounts page. Non-credential.
  notes?: string
  last_used_at?: string
  expires_at?: string
  created_at: string
  session_window_status?: string
  session_window_end?: string
  temp_unschedulable_until?: string
  temp_unschedulable_reason?: string
  rate_limited_at?: string
  rate_limit_reset_at?: string
  overload_until?: string
  // Configured caps (anthropic oauth/setup-token).
  window_cost_limit?: number
  window_cost_sticky_reserve?: number
  max_sessions?: number
  session_idle_timeout_minutes?: number
  base_rpm?: number
  rpm_strategy?: string
  rpm_sticky_buffer?: number
  // Live gauges from the edge's local Redis/DB (align with the per-edge admin page).
  current_concurrency?: number
  current_window_cost?: number
  active_sessions?: number
  current_rpm?: number
  today_stats?: EdgeTodayStats
  // Passive 5h/7d usage windows (anthropic oauth/setup-token), source="passive".
  usage?: EdgeUsageWindows
  tier_id?: number
  groups?: string[]
}

/** Passive usage windows for one account (mirrors backend edgeUsageWindows). */
export interface EdgeUsageWindows {
  source: string
  five_hour?: EdgeUsageProgress
  seven_day?: EdgeUsageProgress
  seven_day_sonnet?: EdgeUsageProgress
}

export interface EdgeUsageProgress {
  utilization: number
  resets_at?: string | null
}

/** Today's usage for one account (mirrors backend WindowStats subset). */
export interface EdgeTodayStats {
  requests: number
  tokens: number
  cost: number
  user_cost: number
}

/**
 * One edge's slice of the aggregate. `ok` distinguishes a reachable edge (even
 * with zero accounts) from an unreachable one (`error` set).
 *
 * `stub_schedulable` mirrors the prod-side mirror stub's own scheduling toggle —
 * prod's "route traffic to this edge / don't" switch. When false the stub was
 * 关调度 (taken out of prod rotation) while the edge itself stays reachable, so the
 * overview flags it; see backend service.EdgeAccountsResult.StubSchedulable.
 *
 * `stub_rate_limit_reset_at` / `stub_temp_unschedulable_until` / `stub_groups`
 * carry the PROD mirror stub's own cooldown + group snapshot. The Edge Accounts
 * page filters by the prod stub (prod's handle for the edge), NOT the edge-local
 * accounts: the 分组 dropdown + filter key on `stub_groups`, and the 状态 filter
 * combines the stub's schedulable/cooldown state with each edge account's status
 * (正常 = stub正常 AND account正常; any other bucket = stub OR account). The stub's
 * status column is always 'active' (active-only discovery) so it is not carried —
 * the frontend hard-codes it. Surfaced regardless of reachability (data is prod-side).
 */
export interface EdgeAccountsResult {
  edge_id: string
  base_url: string
  ok: boolean
  error?: string
  stub_schedulable: boolean
  stub_rate_limit_reset_at?: string
  stub_temp_unschedulable_until?: string
  stub_groups?: string[]
  // Per-stub identity (only set by the by-stub view the inline /accounts panel uses;
  // the per-edge overview leaves them undefined). The panel keys each result by
  // stub_account_id (NOT edge_id — multiple stubs share one edge host) and labels the
  // precise correspondence with stub_platform + edge_group ("调度自 <edge_group> 组").
  // edge_group is "" for a universal/single-pool key → whole-platform footnote.
  stub_account_id?: number
  stub_platform?: string
  edge_group?: string
  accounts: EdgeAccountSummary[]
}

/**
 * The full cross-edge payload. Matches backend service.EdgeAccountsAggregate.
 */
export interface EdgeAccountsAggregate {
  platform: string
  edges: EdgeAccountsResult[]
  ts: number
}

export interface EdgeAccountsListParams {
  platform?: string
  // view='by-stub' → the inline /accounts panel's per-stub inventory (each prod
  // mirror stub fanned out with its own key → precise per-key correspondence).
  // Omitted → the per-edge fleet overview (the standalone /edge-accounts page).
  view?: 'by-stub'
}

export async function list(params: EdgeAccountsListParams = {}): Promise<EdgeAccountsAggregate> {
  const { data } = await apiClient.get<EdgeAccountsAggregate>('/admin/edge-accounts', { params })
  return data
}

/**
 * ETag-aware variant for the periodic auto-refresh. Sends If-None-Match and, when
 * the backend's aggregate is unchanged (304), returns `notModified` with no body so
 * the caller can skip a re-render entirely. Mirrors api/admin/accounts.ts:listWithEtag.
 */
export interface EdgeAccountsListWithEtagResult {
  notModified: boolean
  etag: string | null
  data: EdgeAccountsAggregate | null
}

export async function listWithEtag(
  params: EdgeAccountsListParams = {},
  options?: { signal?: AbortSignal; etag?: string | null }
): Promise<EdgeAccountsListWithEtagResult> {
  const headers: Record<string, string> = {}
  if (options?.etag) {
    headers['If-None-Match'] = options.etag
  }

  const response = await apiClient.get<EdgeAccountsAggregate>('/admin/edge-accounts', {
    params,
    headers,
    signal: options?.signal,
    validateStatus: (status) => (status >= 200 && status < 300) || status === 304
  })

  const etagHeader = typeof response.headers?.etag === 'string' ? response.headers.etag : null
  if (response.status === 304) {
    return { notModified: true, etag: etagHeader, data: null }
  }
  return { notModified: false, etag: etagHeader, data: response.data }
}

/**
 * Mint result for the "manage accounts" handoff: a ready-to-open URL on the
 * target edge that auto-logs-in and lands on its own /admin/accounts page. The
 * short-lived token rides in the URL fragment (see backend buildEdgeHandoffURL).
 */
export interface EdgeAdminSessionResult {
  edge_id: string
  handoff_url: string
  expires_in: number
}

/**
 * Request a one-shot admin-session handoff URL for a specific edge. Prod forwards
 * to the edge (mirror-stub api-key) which mints a short-lived admin JWT.
 */
export async function adminSession(edgeId: string): Promise<EdgeAdminSessionResult> {
  const { data } = await apiClient.post<EdgeAdminSessionResult>(
    `/admin/edge-accounts/${encodeURIComponent(edgeId)}/admin-session`
  )
  return data
}

/**
 * Inline edge-account WRITE ops (status-class only — never credentials). Prod
 * forwards each to the target edge's least-privilege endpoint
 * (POST|DELETE|GET /api/v1/edge/accounts/:id/<op>) using the mirror-stub api-key;
 * see backend service.EdgeAccountsAggregator.ForwardAccountOp + the prod proxy
 * handler admin/edge_account_ops_handler_tk.go. Credential-class ops (edit /
 * reauth / create / delete) are deliberately NOT here — they stay on the edge via
 * the admin-session handoff (adminSession above), so secrets never reach prod.
 *
 * The mutation ops return the edge's updated, credential-free account DTO
 * (EdgeAccountSummary) so the caller can merge the post-op state into the panel.
 *
 * edgeId resolves the prod mirror stub; accountId is the EDGE-local account id.
 */
function opPath(edgeId: string, accountId: number, op: string): string {
  return `/admin/edge-accounts/${encodeURIComponent(edgeId)}/accounts/${accountId}/${op}`
}

export async function clearRateLimit(edgeId: string, accountId: number): Promise<EdgeAccountSummary> {
  const { data } = await apiClient.post<EdgeAccountSummary>(opPath(edgeId, accountId, 'clear-rate-limit'))
  return data
}

export async function resetQuota(edgeId: string, accountId: number): Promise<EdgeAccountSummary> {
  const { data } = await apiClient.post<EdgeAccountSummary>(opPath(edgeId, accountId, 'reset-quota'))
  return data
}

export async function clearTempUnschedulable(edgeId: string, accountId: number): Promise<EdgeAccountSummary> {
  const { data } = await apiClient.delete<EdgeAccountSummary>(opPath(edgeId, accountId, 'temp-unschedulable'))
  return data
}

export async function setSchedulable(
  edgeId: string,
  accountId: number,
  schedulable: boolean
): Promise<EdgeAccountSummary> {
  const { data } = await apiClient.post<EdgeAccountSummary>(opPath(edgeId, accountId, 'schedulable'), { schedulable })
  return data
}

/**
 * Active/passive usage query for one edge account. source='active' (default) runs
 * a real upstream query on the edge (the "查询" button); 'passive' returns the
 * persisted-sample windows. Mirrors api/admin/accounts.ts:getUsage so the shared
 * AccountUsageCell behaves identically for an edge account.
 */
export async function getUsage(
  edgeId: string,
  accountId: number,
  source?: 'passive' | 'active',
  force?: boolean
): Promise<AccountUsageInfo> {
  const params: Record<string, string> = {}
  if (source) params.source = source
  if (force) params.force = 'true'
  const { data } = await apiClient.get<AccountUsageInfo>(opPath(edgeId, accountId, 'usage'), { params })
  return data
}

export const edgeAccountsAPI = {
  list,
  listWithEtag,
  adminSession,
  clearRateLimit,
  resetQuota,
  clearTempUnschedulable,
  setSchedulable,
  getUsage
}

export default edgeAccountsAPI

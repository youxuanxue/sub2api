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
 */
export interface EdgeAccountsResult {
  edge_id: string
  base_url: string
  ok: boolean
  error?: string
  stub_schedulable: boolean
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

export const edgeAccountsAPI = {
  list,
  listWithEtag,
  adminSession
}

export default edgeAccountsAPI

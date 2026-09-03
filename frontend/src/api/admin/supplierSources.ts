import { apiClient } from '../client'

export type SupplierProbeStatus =
  | 'passed'
  | 'failed'
  | 'auth_failed'
  | 'model_unsupported'
  | 'protocol_unsupported'

export interface SupplierSourceModel {
  client_model_id: string
  upstream_model_id: string
  purchase_ratio: number | null
}

export interface SupplierSource {
  id: number
  supplier_name: string
  supplier_lane: string
  channel_type: number
  endpoint: string
  base_priority: number
  account_concurrency: number
  models: SupplierSourceModel[]
  notes: string
  created_at: string
  updated_at: string
}

export interface SupplierSourceInput {
  supplier_name: string
  supplier_lane: string
  channel_type: number
  endpoint: string
  credential: string
  base_priority: number
  account_concurrency: number
  models: SupplierSourceModel[]
  notes: string
}

export interface SupplierPriorityPreviewEntry {
  source_id: number
  supplier_name: string
  supplier_lane: string
  discount_band: number
  discount_priority: number
  priority: number
  client_model_ids: string[]
}

export interface SupplierPriorityPreviewWarning {
  code: string
  priority: number
  source_ids: number[]
}

export interface SupplierPriorityPreview {
  entries: SupplierPriorityPreviewEntry[]
  warnings: SupplierPriorityPreviewWarning[]
}

export interface SupplierProbeResult {
  client_model_id: string
  upstream_model_id: string
  status: SupplierProbeStatus
  protocol?: string
  detail?: string
}

export interface SupplierSourceAccountChange {
  account_id: number
  discount_band: number
  action: string
  added_models: string[]
  removed_models: string[]
  priority_before?: number
  priority_after: number
  schedulable_before?: boolean
  schedulable_after: boolean
}

export interface SupplierSourceSyncResult {
  source_id: number
  probe_results: SupplierProbeResult[]
  changes: SupplierSourceAccountChange[]
  failed_step?: string
}

export interface SupplierSourceValidateResult {
  source_id: number
  probe_results: SupplierProbeResult[]
  failed_step?: string
}

export interface SupplierUpstreamModelEntry {
  id: string
  type?: string
}

export interface SupplierModelNormalizeChange {
  from_client_model_id: string
  from_upstream_model_id: string
  to_client_model_id: string
  to_upstream_model_id: string
}

export interface SupplierProbeConfiguredIssue {
  client_model_id: string
  upstream_model_id: string
  reason: string
}

export interface SupplierProbeRejectedCandidate {
  upstream_model_id: string
  type?: string
  reason: string
  detail?: string
}

export type SupplierProbeJobStatus = 'pending' | 'running' | 'completed' | 'failed'

export interface SupplierSourceProbeResult {
  source_id: number
  job_id?: string
  probe_status: SupplierProbeJobStatus
  probe_total: number
  probe_done: number
  upstream_models: SupplierUpstreamModelEntry[]
  normalized_models: SupplierSourceModel[]
  normalized_changes: SupplierModelNormalizeChange[]
  suggested_appends: SupplierSourceModel[]
  rejected_candidates: SupplierProbeRejectedCandidate[]
  configured_issues: SupplierProbeConfiguredIssue[]
  probe_results: SupplierProbeResult[]
  needs_confirmation: boolean
  failed_step?: string
}

async function list(): Promise<SupplierSource[]> {
  const { data } = await apiClient.get<SupplierSource[]>('/admin/supplier-sources')
  return data
}

async function get(id: number): Promise<SupplierSource> {
  const { data } = await apiClient.get<SupplierSource>(`/admin/supplier-sources/${id}`)
  return data
}

async function create(input: SupplierSourceInput): Promise<SupplierSource> {
  const { data } = await apiClient.post<SupplierSource>('/admin/supplier-sources', input)
  return data
}

async function update(id: number, input: SupplierSourceInput): Promise<SupplierSource> {
  const { data } = await apiClient.put<SupplierSource>(`/admin/supplier-sources/${id}`, input)
  return data
}

async function priorityPreview(): Promise<SupplierPriorityPreview> {
  const { data } = await apiClient.get<SupplierPriorityPreview>('/admin/supplier-sources/priority-preview')
  return data
}

async function discover(id: number): Promise<SupplierSourceProbeResult> {
  const { data } = await apiClient.post<SupplierSourceProbeResult>(
    `/admin/supplier-sources/${id}/discover`,
  )
  return data
}

async function getDiscoverJob(id: number, jobId: string): Promise<SupplierSourceProbeResult> {
  const { data } = await apiClient.get<SupplierSourceProbeResult>(
    `/admin/supplier-sources/${id}/discover/jobs/${encodeURIComponent(jobId)}`,
  )
  return data
}

async function validate(id: number): Promise<SupplierSourceValidateResult> {
  const { data } = await apiClient.post<SupplierSourceValidateResult>(
    `/admin/supplier-sources/${id}/validate`,
  )
  return data
}

async function sync(id: number): Promise<SupplierSourceSyncResult> {
  const { data } = await apiClient.post<SupplierSourceSyncResult>(`/admin/supplier-sources/${id}/sync`)
  return data
}

export default { list, get, create, update, priorityPreview, discover, getDiscoverJob, validate, sync }

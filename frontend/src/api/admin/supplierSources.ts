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
  channel_name: string
  endpoint: string
  base_priority: number
  models: SupplierSourceModel[]
  notes: string
  created_at: string
  updated_at: string
}

export interface SupplierSourceInput {
  supplier_name: string
  channel_name: string
  endpoint: string
  credential: string
  base_priority: number
  models: SupplierSourceModel[]
  notes: string
}

export interface SupplierPriorityPreviewEntry {
  source_id: number
  supplier_name: string
  channel_name: string
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

export interface SupplierModelDiscoverIssue {
  client_model_id: string
  upstream_model_id: string
  reason: string
}

export interface SupplierModelDiscoverRejection {
  upstream_model_id: string
  type?: string
  reason: string
  detail?: string
}

export interface SupplierModelsDiscoverResult {
  source_id: number
  upstream_models: SupplierUpstreamModelEntry[]
  normalized_models: SupplierSourceModel[]
  normalized_changes: SupplierModelNormalizeChange[]
  suggested_appends: SupplierSourceModel[]
  rejected_candidates: SupplierModelDiscoverRejection[]
  configured_issues: SupplierModelDiscoverIssue[]
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

async function discoverModels(id: number): Promise<SupplierModelsDiscoverResult> {
  // Discover may probe several upstream candidates; keep above the global 30s client timeout.
  const { data } = await apiClient.post<SupplierModelsDiscoverResult>(
    `/admin/supplier-sources/${id}/models-discover`,
    undefined,
    { timeout: 90_000 },
  )
  return data
}

async function sync(id: number): Promise<SupplierSourceSyncResult> {
  const { data } = await apiClient.post<SupplierSourceSyncResult>(`/admin/supplier-sources/${id}/sync`)
  return data
}

export default { list, get, create, update, priorityPreview, discoverModels, sync }

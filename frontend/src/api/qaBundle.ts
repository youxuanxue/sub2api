import { apiClient } from './client'

export type QABundleStatus = 'pending' | 'ready' | 'failed'

export interface QABundleRecord {
  request_id: string
  api_key_id: number
  platform: string
  provider?: string
  requested_model: string
  upstream_model?: string
  status_code: number
  success: boolean
  duration_ms: number
  first_token_ms?: number
  input_tokens: number
  output_tokens: number
  cached_tokens: number
  captured_at: string
  detail?: Record<string, unknown>
}

export interface QABundlePage {
  schema_version: string
  page: number
  records: QABundleRecord[]
}

export interface QABundlePageAccess {
  page: number
  record_count: number
  sha256: string
  url: string
}

export interface QABundleJob {
  job_id: string
  status: QABundleStatus
  api_key_id: number
  data_from: string
  data_until: string
  archive_watermark: string
  record_count: number
  pages?: QABundlePageAccess[]
  error?: string
}

export interface QABundleExportJob {
  job_id: string
  bundle_job_id: string
  status: QABundleStatus
  record_count: number
  download_url?: string
  expires_at?: string
  error?: string
}

async function createBundle(apiKeyId: number): Promise<QABundleJob> {
  const { data } = await apiClient.post<QABundleJob>('/users/me/qa/bundles', { api_key_id: apiKeyId })
  return data
}

async function getBundle(jobId: string): Promise<QABundleJob> {
  const { data } = await apiClient.get<QABundleJob>(`/users/me/qa/bundles/${encodeURIComponent(jobId)}`)
  return data
}

async function fetchPage(url: string): Promise<QABundlePage> {
  const response = await fetch(url, { credentials: 'omit' })
  if (!response.ok) throw new Error(`QA Bundle page HTTP ${response.status}`)
  return response.json() as Promise<QABundlePage>
}

async function createExport(bundleJobId: string): Promise<QABundleExportJob> {
  const { data } = await apiClient.post<QABundleExportJob>(
    `/users/me/qa/bundles/${encodeURIComponent(bundleJobId)}/export`
  )
  return data
}

async function getExport(jobId: string): Promise<QABundleExportJob> {
  const { data } = await apiClient.get<QABundleExportJob>(
    `/users/me/qa/bundle-exports/${encodeURIComponent(jobId)}`
  )
  return data
}

function download(url: string, filename: string): void {
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  anchor.rel = 'noopener'
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
}

export const qaBundleAPI = { createBundle, getBundle, fetchPage, createExport, getExport, download }

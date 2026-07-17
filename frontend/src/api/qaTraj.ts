/**
 * TokenKey: per-API-key conversation-record (traj) export.
 *
 * Surfaces the already-shipped self-export endpoints (qa traj v2, issue #685)
 * scoped to a single API key. Gated end-to-end by the admin-granted
 * `traj_export_enabled` user switch — the UI only renders the entry when the
 * switch is on, and the backend returns 403 otherwise.
 */

import { apiClient } from './client'

export type TrajExportStatus = 'pending' | 'running' | 'done' | 'failed'

/**
 * Async export job. POST enqueues and returns {job_id, status:"pending"}; the
 * poll endpoint returns the evolving status and, once done, a download_url +
 * record_count. The heavy work runs off the request path on the server's single
 * export worker, so a large key can't block or starve the gateway.
 */
export interface TrajExportJob {
  job_id: string
  status: TrajExportStatus
  /** 'manual' = user-triggered "export now"; 'auto' = server-scheduled snapshot. */
  kind?: 'manual' | 'auto'
  /** The API key this export belongs to. */
  api_key_id?: number
  download_url?: string
  expires_at?: string
  record_count: number
  /** Enqueue time (ISO-8601). Newest-first ordering key for the export panel. */
  created_at?: string
  error?: string
}

/**
 * Enqueue a trajectory export for a single API key. Returns the job to poll.
 * Format is always v2 (.examples-aligned, training-ready).
 */
async function exportKey(apiKeyId: number): Promise<TrajExportJob> {
  const { data } = await apiClient.post<TrajExportJob>('/users/me/qa/traj/export', {
    api_key_id: apiKeyId,
    format: 'v2'
  })
  return data
}

/** Poll one export job's status. */
async function getJob(jobId: string): Promise<TrajExportJob> {
  const { data } = await apiClient.get<TrajExportJob>(
    `/users/me/qa/traj/export/jobs/${encodeURIComponent(jobId)}`
  )
  return data
}

/**
 * List this user's recent export jobs, newest first. Scoped to one API key when
 * `apiKeyId` is provided. Each job is downloadable while within its 24h TTL
 * (i.e. `download_url` present and not yet `expires_at`).
 */
async function listExports(apiKeyId?: number): Promise<TrajExportJob[]> {
  const { data } = await apiClient.get<{ exports: TrajExportJob[] }>(
    '/users/me/qa/traj/export/jobs',
    apiKeyId != null ? { params: { api_key_id: apiKeyId } } : undefined
  )
  return data.exports ?? []
}

function triggerBlobDownload(blob: Blob, filename: string): void {
  const url = window.URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  window.URL.revokeObjectURL(url)
}

/**
 * Download the export zip and save it as `filename`. Same-origin URLs (the
 * localfs-backed authenticated download path) go through apiClient so the JWT
 * is attached; an off-origin presigned URL (S3) is opened directly — it carries
 * its own signature and must not receive an Authorization header.
 */
async function download(downloadUrl: string, filename: string): Promise<void> {
  const sameOrigin = downloadUrl.startsWith('/') || downloadUrl.startsWith(window.location.origin)
  if (sameOrigin) {
    const resp = await apiClient.get(downloadUrl, { responseType: 'blob' })
    triggerBlobDownload(resp.data as Blob, filename)
    return
  }
  const a = document.createElement('a')
  a.href = downloadUrl
  a.download = filename
  a.rel = 'noopener'
  document.body.appendChild(a)
  a.click()
  a.remove()
}

export const qaTrajAPI = {
  exportKey,
  getJob,
  listExports,
  download
}

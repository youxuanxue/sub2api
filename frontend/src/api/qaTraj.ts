/**
 * TokenKey: per-API-key conversation-record (traj) export.
 *
 * Surfaces the already-shipped self-export endpoints (qa traj v2, issue #685)
 * scoped to a single API key. Gated end-to-end by the admin-granted
 * `traj_export_enabled` user switch — the UI only renders the entry when the
 * switch is on, and the backend returns 403 otherwise.
 */

import { apiClient } from './client'

export interface TrajExportResult {
  download_url: string
  expires_at: string
  record_count: number
}

/**
 * Build a trajectory export for a single API key. Returns the download URL plus
 * record_count so the caller can distinguish "captured but empty" (0) from a
 * real export. Format is always v2 (.examples-aligned, training-ready).
 */
async function exportKey(apiKeyId: number): Promise<TrajExportResult> {
  const { data } = await apiClient.post<TrajExportResult>('/users/me/qa/traj/export', {
    api_key_id: apiKeyId,
    format: 'v2'
  })
  return data
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
  download
}

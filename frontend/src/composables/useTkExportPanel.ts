/**
 * TokenKey: backing logic for the per-API-key "Export Panel" modal
 * (components/keys/ExportPanel.vue).
 *
 * Replaces the old fire-and-forget inline export button on the key card with a
 * panel that lists the key's recent export jobs (downloadable while within their
 * 24h TTL), shows the last export time, and offers an "Export now" action that
 * enqueues a fresh manual export and polls it to completion.
 *
 * This composable owns data + behaviour; ExportPanel.vue stays template + wiring
 * (TokenKey upstream-isolation pattern, CLAUDE.md §5).
 */

import { computed, ref, type Ref } from 'vue'
import { qaTrajAPI, type TrajExportJob } from '@/api/qaTraj'
import { useAppStore } from '@/stores/app'
import { useI18n } from 'vue-i18n'

interface UseTkExportPanelArgs {
  /** The api key currently shown in the panel (null when closed / no key). */
  apiKeyId: Ref<number | null>
  /** The key's display name — used to build a friendly download filename. */
  apiKeyName: Ref<string | undefined>
}

/** Poll cadence + ceiling for a manual export. The server prepares the zip on a
 * single off-request-path worker, so a large key can take a while — bounded so a
 * stuck job never spins forever. */
const POLL_INTERVAL_MS = 2000
const POLL_DEADLINE_MS = 10 * 60 * 1000

export function useTkExportPanel(args: UseTkExportPanelArgs) {
  const appStore = useAppStore()
  const { t } = useI18n()

  const exports = ref<TrajExportJob[]>([])
  const loading = ref(false)
  const error = ref(false)

  // Set of api-key ids with an in-flight manual export. The panel is a SINGLE
  // persistent instance reused for every key (KeysView toggles :show + swaps
  // :api-key-id), and exportNow() polls for up to 10 min off the request path —
  // so "is an export running" MUST be scoped per key, not a shared boolean.
  // Otherwise exporting key A and switching to key B (or reopening the panel for
  // B) would show B as "exporting" and disable its button until A's poll loop
  // ends. Keyed independence also lets A and B export concurrently (the backend
  // already serializes them on its single export worker).
  const runningKeyIds = ref<Set<number>>(new Set())

  /** Whether the key currently shown in the panel has an in-flight export. */
  const running = computed(() => {
    const id = args.apiKeyId.value
    return id != null && runningKeyIds.value.has(id)
  })

  function setRunning(id: number, on: boolean): void {
    const next = new Set(runningKeyIds.value)
    if (on) next.add(id)
    else next.delete(id)
    runningKeyIds.value = next
  }

  /** (Re)load the key's recent export jobs, newest first. */
  async function refresh(): Promise<void> {
    const id = args.apiKeyId.value
    if (id == null) {
      exports.value = []
      return
    }
    loading.value = true
    error.value = false
    try {
      exports.value = await qaTrajAPI.listExports(id)
    } catch {
      error.value = true
      exports.value = []
    } finally {
      loading.value = false
    }
  }

  /**
   * Enqueue a fresh export for the current key, poll until done/failed, then
   * refresh the list so the new job (and any drift) is reflected. Empty capture
   * or failure surfaces a friendly toast; the heavy work never blocks the UI.
   */
  async function exportNow(): Promise<void> {
    const id = args.apiKeyId.value
    // Re-entry guard is per-key: block a second export of THIS key, but never
    // block a different key whose own export isn't running.
    if (id == null || runningKeyIds.value.has(id)) return
    // `viewing` answers "is the user still on the key this export belongs to?".
    // Toasts and the list refresh are gated on it so a backgrounded export
    // (panel closed, or switched to another key) never fires feedback that
    // appears to belong to whatever key is on screen now — the panel's job list
    // (refreshed on open) is the durable record for those cases.
    const viewing = () => args.apiKeyId.value === id
    setRunning(id, true)
    try {
      let job = await qaTrajAPI.exportKey(id)
      const deadline = Date.now() + POLL_DEADLINE_MS
      while (job.status === 'pending' || job.status === 'running') {
        if (Date.now() > deadline) throw new Error('export timed out')
        await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS))
        job = await qaTrajAPI.getJob(job.job_id)
      }
      if (viewing()) {
        if (job.status === 'failed') {
          appStore.showInfo(job.error === 'no_records' ? t('keys.exportEmpty') : t('keys.exportFailed'))
        } else if (!job.record_count) {
          appStore.showInfo(t('keys.exportEmpty'))
        } else {
          appStore.showSuccess(t('keys.exportSuccess', { count: job.record_count }))
        }
      }
    } catch {
      if (viewing()) appStore.showError(t('keys.exportFailed'))
    } finally {
      setRunning(id, false)
      if (viewing()) await refresh()
    }
  }

  /** Download a finished export's zip, saved as a friendly per-key filename. */
  async function download(job: TrajExportJob): Promise<void> {
    if (!job.download_url) return
    const stamp = (job.created_at ? new Date(job.created_at) : new Date()).toISOString().slice(0, 10)
    const safeName = (args.apiKeyName.value || `key-${args.apiKeyId.value ?? ''}`).replace(/[^\w.-]+/g, '_')
    // Re-mint the URL at click time. On prod the export blob store signs S3 URLs
    // with the EC2 instance role's temporary credentials, whose session token
    // (X-Amz-Security-Token) rotates within hours — so a presigned URL captured
    // when the panel first loaded fails with S3 "ExpiredToken" when clicked
    // later. getJob re-presigns server-side, so we fetch a freshly signed URL
    // right before opening it (the browser then navigates straight to the
    // off-origin S3 URL — CORS-free, fresh token). Best effort: fall back to the
    // already-listed URL if the refresh fails (a recently signed one may still work).
    let downloadUrl = job.download_url
    try {
      const fresh = await qaTrajAPI.getJob(job.job_id)
      if (fresh.download_url) downloadUrl = fresh.download_url
    } catch {
      // keep the listed URL
    }
    try {
      await qaTrajAPI.download(downloadUrl, `conversations-${safeName}-${stamp}.zip`)
    } catch {
      appStore.showError(t('keys.exportFailed'))
    }
  }

  return {
    exports,
    loading,
    running,
    error,
    refresh,
    exportNow,
    download,
  }
}

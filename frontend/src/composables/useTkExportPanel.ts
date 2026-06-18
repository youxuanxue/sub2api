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

import { ref, type Ref } from 'vue'
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
  const running = ref(false)
  const error = ref(false)

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
    if (id == null || running.value) return
    running.value = true
    try {
      let job = await qaTrajAPI.exportKey(id)
      const deadline = Date.now() + POLL_DEADLINE_MS
      while (job.status === 'pending' || job.status === 'running') {
        if (Date.now() > deadline) throw new Error('export timed out')
        await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS))
        job = await qaTrajAPI.getJob(job.job_id)
      }
      if (job.status === 'failed') {
        appStore.showInfo(job.error === 'no_records' ? t('keys.exportEmpty') : t('keys.exportFailed'))
      } else if (!job.record_count) {
        appStore.showInfo(t('keys.exportEmpty'))
      } else {
        appStore.showSuccess(t('keys.exportSuccess', { count: job.record_count }))
      }
    } catch {
      appStore.showError(t('keys.exportFailed'))
    } finally {
      running.value = false
      await refresh()
    }
  }

  /** Download a finished export's zip, saved as a friendly per-key filename. */
  async function download(job: TrajExportJob): Promise<void> {
    if (!job.download_url) return
    const stamp = (job.created_at ? new Date(job.created_at) : new Date()).toISOString().slice(0, 10)
    const safeName = (args.apiKeyName.value || `key-${args.apiKeyId.value ?? ''}`).replace(/[^\w.-]+/g, '_')
    try {
      await qaTrajAPI.download(job.download_url, `conversations-${safeName}-${stamp}.zip`)
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

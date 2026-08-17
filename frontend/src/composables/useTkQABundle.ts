import { computed, ref, type Ref } from 'vue'
import {
  qaBundleAPI,
  type QABundleJob,
  type QABundleRecord
} from '@/api/qaBundle'
import { useAppStore } from '@/stores/app'
import { useI18n } from 'vue-i18n'

interface UseTkQABundleArgs {
  apiKeyId: Ref<number | null>
  apiKeyName: Ref<string | undefined>
}
const POLL_INTERVAL_MS = 2000
const POLL_DEADLINE_MS = 10 * 60 * 1000

export function useTkQABundle(args: UseTkQABundleArgs) {
  const appStore = useAppStore()
  const { t } = useI18n()
  const job = ref<QABundleJob | null>(null)
  const records = ref<QABundleRecord[]>([])
  const selected = ref<QABundleRecord | null>(null)
  const pageIndex = ref(0)
  const loading = ref(false)
  const exporting = ref(false)
  const error = ref(false)
  let lifecycleGeneration = 0

  const pages = computed(() => job.value?.pages ?? [])
  const hasPreviousPage = computed(() => pageIndex.value > 0)
  const hasNextPage = computed(() => pageIndex.value + 1 < pages.value.length)

  function isCurrent(generation: number, apiKeyId: number | null): boolean {
    return generation === lifecycleGeneration && args.apiKeyId.value === apiKeyId
  }

  function cancel(): void {
    lifecycleGeneration += 1
    loading.value = false
    exporting.value = false
  }

  async function pollBundle(initial: QABundleJob, generation: number, apiKeyId: number): Promise<QABundleJob | null> {
    let current = initial
    const deadline = Date.now() + POLL_DEADLINE_MS
    while (current.status === 'pending') {
      if (!isCurrent(generation, apiKeyId)) return null
      if (Date.now() > deadline) throw new Error('QA Bundle timed out')
      await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS))
      if (!isCurrent(generation, apiKeyId)) return null
      const next = await qaBundleAPI.getBundle(current.job_id)
      if (!isCurrent(generation, apiKeyId)) return null
      current = next
    }
    return current
  }

  async function load(): Promise<void> {
    const apiKeyId = args.apiKeyId.value
    const generation = ++lifecycleGeneration
    exporting.value = false
    job.value = null
    records.value = []
    selected.value = null
    pageIndex.value = 0
    error.value = false
    if (apiKeyId == null) return
    loading.value = true
    try {
      const next = await pollBundle(await qaBundleAPI.createBundle(apiKeyId), generation, apiKeyId)
      if (!next || !isCurrent(generation, apiKeyId)) return
      job.value = next
      if (next.status === 'failed') {
        error.value = true
        return
      }
      if (next.status === 'ready' && (next.pages?.length ?? 0) > 0) await loadPage(0)
    } catch {
      if (isCurrent(generation, apiKeyId)) error.value = true
    } finally {
      if (isCurrent(generation, apiKeyId)) loading.value = false
    }
  }

  async function loadPage(index: number): Promise<void> {
    const descriptor = pages.value[index]
    if (!descriptor) return
    const generation = lifecycleGeneration
    const apiKeyId = args.apiKeyId.value
    loading.value = true
    error.value = false
    try {
      const page = await qaBundleAPI.fetchPage(descriptor.url)
      if (!isCurrent(generation, apiKeyId)) return
      if (page.page !== descriptor.page) throw new Error('QA Bundle page mismatch')
      pageIndex.value = index
      records.value = page.records
      selected.value = page.records[0] ?? null
    } catch {
      if (isCurrent(generation, apiKeyId)) {
        error.value = true
        records.value = []
        selected.value = null
      }
    } finally {
      if (isCurrent(generation, apiKeyId)) loading.value = false
    }
  }

  function select(record: QABundleRecord): void {
    selected.value = record
  }

  async function exportZip(): Promise<void> {
    const current = job.value
    if (!current || current.status !== 'ready' || exporting.value) return
    const generation = lifecycleGeneration
    const apiKeyId = args.apiKeyId.value
    const apiKeyName = args.apiKeyName.value
    exporting.value = true
    try {
      let exportJob = await qaBundleAPI.createExport(current.job_id)
      if (!isCurrent(generation, apiKeyId)) return
      const deadline = Date.now() + POLL_DEADLINE_MS
      while (exportJob.status === 'pending') {
        if (!isCurrent(generation, apiKeyId)) return
        if (Date.now() > deadline) throw new Error('QA Bundle export timed out')
        await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS))
        if (!isCurrent(generation, apiKeyId)) return
        const next = await qaBundleAPI.getExport(exportJob.job_id)
        if (!isCurrent(generation, apiKeyId)) return
        exportJob = next
      }
      if (exportJob.status !== 'ready' || !exportJob.download_url) throw new Error('QA Bundle export failed')
      if (!isCurrent(generation, apiKeyId)) return
      const safeName = (apiKeyName || `key-${apiKeyId ?? ''}`).replace(/[^\w.-]+/g, '_')
      const stamp = (current.data_until || new Date().toISOString()).slice(0, 10)
      qaBundleAPI.download(exportJob.download_url, `qa-${safeName}-${stamp}.zip`)
    } catch {
      if (isCurrent(generation, apiKeyId)) appStore.showError(t('keys.qaBundle.failed'))
    } finally {
      if (isCurrent(generation, apiKeyId)) exporting.value = false
    }
  }

  return {
    job, records, selected, pageIndex, pages, loading, exporting, error,
    hasPreviousPage, hasNextPage, load, loadPage, select, exportZip, cancel
  }
}

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const showError = vi.fn()
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError }) }))
vi.mock('@/api/qaBundle', () => ({
  qaBundleAPI: {
    createBundle: vi.fn(), getBundle: vi.fn(), fetchPage: vi.fn(),
    createExport: vi.fn(), getExport: vi.fn(), download: vi.fn()
  }
}))

import { qaBundleAPI } from '@/api/qaBundle'
import { useTkQABundle } from '@/composables/useTkQABundle'

describe('useTkQABundle', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.clearAllMocks()
  })
  afterEach(() => vi.useRealTimers())

  it('loads list and detail from the same committed Bundle page', async () => {
    const apiKeyId = ref<number | null>(11)
    vi.mocked(qaBundleAPI.createBundle).mockResolvedValue({
      job_id: 'b1', status: 'ready', api_key_id: 11,
      data_from: '2026-08-14T10:00:00Z', data_until: '2026-08-15T10:00:00Z',
      archive_watermark: '2026-08-15T10:00:00Z', record_count: 1,
      pages: [{ page: 1, record_count: 1, sha256: 'a', url: 'https://s3/page-1' }]
    })
    vi.mocked(qaBundleAPI.fetchPage).mockResolvedValue({
      schema_version: 'qa-bundle-v1', page: 1,
      records: [{
        request_id: 'req-1', api_key_id: 11, platform: 'anthropic', requested_model: 'claude',
        status_code: 200, success: true, duration_ms: 10, input_tokens: 1, output_tokens: 2,
        cached_tokens: 0, captured_at: '2026-08-15T09:00:00Z', detail: { request: { messages: [] } }
      }]
    })
    const state = useTkQABundle({ apiKeyId, apiKeyName: ref('key') })

    await state.load()

    expect(state.records.value).toHaveLength(1)
    expect(state.selected.value?.request_id).toBe('req-1')
    expect(state.selected.value?.detail?.request).toEqual({ messages: [] })
    expect(qaBundleAPI.fetchPage).toHaveBeenCalledWith('https://s3/page-1')
  })

  it('polls ZIP separately and downloads only the worker artifact', async () => {
    const state = useTkQABundle({ apiKeyId: ref(11), apiKeyName: ref('my key') })
    state.job.value = {
      job_id: 'b1', status: 'ready', api_key_id: 11, data_from: '', data_until: '',
      archive_watermark: '', record_count: 1, pages: []
    }
    vi.mocked(qaBundleAPI.createExport).mockResolvedValue({ job_id: 'e1', bundle_job_id: 'b1', status: 'pending', record_count: 0 })
    vi.mocked(qaBundleAPI.getExport).mockResolvedValue({
      job_id: 'e1', bundle_job_id: 'b1', status: 'ready', record_count: 1,
      download_url: 'https://s3/export.zip'
    })

    const pending = state.exportZip()
    await vi.advanceTimersByTimeAsync(2100)
    await pending

    expect(qaBundleAPI.download).toHaveBeenCalledWith('https://s3/export.zip', expect.stringContaining('qa-my_key-'))
  })

  it('stops polling a pending Bundle after cancellation', async () => {
    vi.mocked(qaBundleAPI.createBundle).mockResolvedValue({
      job_id: 'b1', status: 'pending', api_key_id: 11, data_from: '', data_until: '',
      archive_watermark: '', record_count: 0, pages: []
    })
    vi.mocked(qaBundleAPI.getBundle).mockResolvedValue({
      job_id: 'b1', status: 'pending', api_key_id: 11, data_from: '', data_until: '',
      archive_watermark: '', record_count: 0, pages: []
    })
    const state = useTkQABundle({ apiKeyId: ref(11), apiKeyName: ref('key') })

    const pending = state.load()
    await Promise.resolve()
    state.cancel()
    await vi.advanceTimersByTimeAsync(2100)
    await pending

    expect(qaBundleAPI.getBundle).not.toHaveBeenCalled()
    expect(state.loading.value).toBe(false)
  })

  it('does not poll or download a pending ZIP after cancellation', async () => {
    const state = useTkQABundle({ apiKeyId: ref(11), apiKeyName: ref('key') })
    state.job.value = {
      job_id: 'b1', status: 'ready', api_key_id: 11, data_from: '', data_until: '',
      archive_watermark: '', record_count: 1, pages: []
    }
    vi.mocked(qaBundleAPI.createExport).mockResolvedValue({
      job_id: 'e1', bundle_job_id: 'b1', status: 'pending', record_count: 0
    })
    vi.mocked(qaBundleAPI.getExport).mockResolvedValue({
      job_id: 'e1', bundle_job_id: 'b1', status: 'ready', record_count: 1,
      download_url: 'https://s3/export.zip'
    })

    const pending = state.exportZip()
    await Promise.resolve()
    state.cancel()
    await vi.advanceTimersByTimeAsync(2100)
    await pending

    expect(qaBundleAPI.getExport).not.toHaveBeenCalled()
    expect(qaBundleAPI.download).not.toHaveBeenCalled()
    expect(state.exporting.value).toBe(false)
  })
})

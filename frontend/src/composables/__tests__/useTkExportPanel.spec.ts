import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { ref } from 'vue'

const showError = vi.fn()
const showSuccess = vi.fn()
const showInfo = vi.fn()

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess, showInfo })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string, args?: Record<string, unknown>) => (args ? `${key}:${JSON.stringify(args)}` : key) })
}))

vi.mock('@/api/qaTraj', () => ({
  qaTrajAPI: {
    exportKey: vi.fn(),
    getJob: vi.fn(),
    listExports: vi.fn(),
    download: vi.fn()
  }
}))

import { useTkExportPanel } from '@/composables/useTkExportPanel'
import { qaTrajAPI } from '@/api/qaTraj'

const mockExport = vi.mocked(qaTrajAPI.exportKey)
const mockGetJob = vi.mocked(qaTrajAPI.getJob)
const mockList = vi.mocked(qaTrajAPI.listExports)

describe('useTkExportPanel — per-key export independence', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    showError.mockReset()
    showSuccess.mockReset()
    showInfo.mockReset()
    mockExport.mockReset()
    mockGetJob.mockReset()
    mockList.mockReset()
    mockList.mockResolvedValue([])
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  // The panel is one reused instance whose api-key-id swaps as the user opens it
  // for different keys. "running" must be scoped to the key actually exporting,
  // not a shared boolean — otherwise exporting grok disables yace's button.
  it('running is true only for the key whose export is in flight', async () => {
    const apiKeyId = ref<number | null>(1) // grok
    const apiKeyName = ref<string | undefined>('grok')
    const tk = useTkExportPanel({ apiKeyId, apiKeyName })

    mockExport.mockResolvedValue({ job_id: 'j1', status: 'pending', record_count: 0 })
    let status = 'running'
    mockGetJob.mockImplementation(async () => ({ job_id: 'j1', status, record_count: status === 'done' ? 5 : 0 }) as any)

    const p = tk.exportNow() // in flight (setRunning runs synchronously before first await)

    expect(tk.running.value).toBe(true) // grok exporting

    // Switch the panel to a different key with no export of its own.
    apiKeyId.value = 2 // yace
    apiKeyName.value = 'yace'
    expect(tk.running.value).toBe(false) // yace's button is enabled — the bug fix

    // Switch back to grok while it is still polling.
    apiKeyId.value = 1
    apiKeyName.value = 'grok'
    expect(tk.running.value).toBe(true)

    // Finish grok's export.
    status = 'done'
    await vi.advanceTimersByTimeAsync(2100)
    await p
    expect(tk.running.value).toBe(false)
  })

  // A backgrounded export (user closed the dialog or switched keys) must NOT fire
  // a toast — it would appear to belong to whatever key is on screen now. The
  // panel's job list (refreshed on open) carries the result instead.
  it('does not toast when the user switched away before the export finished', async () => {
    const apiKeyId = ref<number | null>(1)
    const apiKeyName = ref<string | undefined>('grok')
    const tk = useTkExportPanel({ apiKeyId, apiKeyName })

    mockExport.mockResolvedValue({ job_id: 'j1', status: 'pending', record_count: 0 })
    let status = 'running'
    mockGetJob.mockImplementation(async () => ({ job_id: 'j1', status, record_count: status === 'done' ? 7 : 0 }) as any)

    const p = tk.exportNow()
    apiKeyId.value = 2 // user moved to another key
    status = 'done'
    await vi.advanceTimersByTimeAsync(2100)
    await p

    expect(showSuccess).not.toHaveBeenCalled()
    expect(showInfo).not.toHaveBeenCalled()
    // the in-flight key was cleared even though we are no longer viewing it
    apiKeyId.value = 1
    expect(tk.running.value).toBe(false)
  })

  it('toasts success when the user is still viewing the exported key', async () => {
    const apiKeyId = ref<number | null>(1)
    const apiKeyName = ref<string | undefined>('grok')
    const tk = useTkExportPanel({ apiKeyId, apiKeyName })

    mockExport.mockResolvedValue({ job_id: 'j1', status: 'pending', record_count: 0 })
    let status = 'running'
    mockGetJob.mockImplementation(async () => ({ job_id: 'j1', status, record_count: status === 'done' ? 9 : 0 }) as any)

    const p = tk.exportNow()
    status = 'done'
    await vi.advanceTimersByTimeAsync(2100)
    await p

    expect(showSuccess).toHaveBeenCalledTimes(1)
    expect(showSuccess).toHaveBeenCalledWith('keys.exportSuccess:{"count":9}')
  })
})

// A prod S3 presigned URL is signed with the EC2 instance role's temporary
// session token (rotates within hours), so the URL captured when the panel
// loaded ExpiredTokens when clicked later. download() must re-mint a fresh URL
// at click time via getJob (which re-presigns server-side) before opening it.
describe('useTkExportPanel — download re-mints the URL at click time', () => {
  const mockDownload = vi.mocked(qaTrajAPI.download)
  beforeEach(() => {
    vi.useRealTimers()
    showError.mockReset()
    mockGetJob.mockReset()
    mockDownload.mockReset()
    mockDownload.mockResolvedValue(undefined as any)
  })

  it('opens the freshly re-signed URL, not the stale listed one', async () => {
    const apiKeyId = ref<number | null>(1)
    const apiKeyName = ref<string | undefined>('grok')
    const tk = useTkExportPanel({ apiKeyId, apiKeyName })
    mockGetJob.mockResolvedValue({ job_id: 'j9', status: 'done', record_count: 3, download_url: 'https://s3/fresh' } as any)

    await tk.download({ job_id: 'j9', status: 'done', record_count: 3, download_url: 'https://s3/stale' } as any)

    expect(mockGetJob).toHaveBeenCalledWith('j9')
    expect(mockDownload).toHaveBeenCalledTimes(1)
    expect(mockDownload.mock.calls[0][0]).toBe('https://s3/fresh')
  })

  it('falls back to the listed URL when the re-mint fetch fails', async () => {
    const apiKeyId = ref<number | null>(1)
    const apiKeyName = ref<string | undefined>('grok')
    const tk = useTkExportPanel({ apiKeyId, apiKeyName })
    mockGetJob.mockRejectedValue(new Error('network'))

    await tk.download({ job_id: 'j9', status: 'done', record_count: 3, download_url: 'https://s3/stale' } as any)

    expect(mockDownload).toHaveBeenCalledTimes(1)
    expect(mockDownload.mock.calls[0][0]).toBe('https://s3/stale')
  })

  it('does nothing (no fetch, no open) when the job has no download_url', async () => {
    const apiKeyId = ref<number | null>(1)
    const apiKeyName = ref<string | undefined>('grok')
    const tk = useTkExportPanel({ apiKeyId, apiKeyName })

    await tk.download({ job_id: 'j9', status: 'done', record_count: 0 } as any)

    expect(mockGetJob).not.toHaveBeenCalled()
    expect(mockDownload).not.toHaveBeenCalled()
  })
})

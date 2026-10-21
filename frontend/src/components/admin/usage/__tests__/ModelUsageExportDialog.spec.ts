import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ModelUsageExportDialog from '../ModelUsageExportDialog.vue'
const { getModelStats, saveAs, warning, error } = vi.hoisted(() => ({ getModelStats: vi.fn(), saveAs: vi.fn(), warning: vi.fn(), error: vi.fn() }))
vi.mock('@/api/admin/dashboard', () => ({ getModelStats }))
vi.mock('file-saver', () => ({ saveAs }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showWarning: warning, showError: error, showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const params = { user_id: 16, start_ts: 100, end_ts: 200, start_date: '2026-10-20', end_date: '2026-10-21', billing_mode: 'token', native_compaction_v2: true }
const mountDialog = (value = params) => mount(ModelUsageExportDialog, {
  props: { params: value },
  global: { stubs: { BaseDialog: { template: '<div><slot/><slot name="footer"/></div>' } } },
})
beforeEach(() => { vi.clearAllMocks() })
describe('customer model export lifecycle', () => {
  it.each([0, -1, 1.5, NaN])('rejects invalid user %s without querying all users', async (userId) => {
    const wrapper = mountDialog({ ...params, user_id: userId })
    await wrapper.get('form').trigger('submit')
    expect(getModelStats).not.toHaveBeenCalled()
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
  })
  it('warns for empty data without creating a file', async () => {
    getModelStats.mockResolvedValue({ models: [] })
    const wrapper = mountDialog()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(warning).toHaveBeenCalledWith('usage.noDataToExport')
    expect(saveAs).not.toHaveBeenCalled()
  })
  it('cancels pending requests and does not download or show an error after cancellation', async () => {
    let finish!: (value: unknown) => void
    getModelStats.mockImplementation(() => new Promise(resolve => { finish = resolve }))
    const wrapper = mountDialog()
    await wrapper.get('form').trigger('submit')
    const signal = getModelStats.mock.calls[0][1].signal as AbortSignal
    await wrapper.get('button[type="button"]').trigger('click')
    expect(signal.aborted).toBe(true)
    finish({ models: [] })
    await flushPromises()
    expect(saveAs).not.toHaveBeenCalled()
    expect(error).not.toHaveBeenCalled()
  })
  it('sends the captured filters once and reports aggregate request failures without a download', async () => {
    getModelStats.mockRejectedValueOnce(new Error('Unavailable'))
    const wrapper = mountDialog()
    await wrapper.get('form').trigger('submit')
    await wrapper.setProps({ params: { ...params, user_id: 99, end_ts: 999 } })
    await flushPromises()
    expect(getModelStats).toHaveBeenCalledTimes(1)
    expect(getModelStats).toHaveBeenCalledWith(params, expect.anything())
    expect(error).toHaveBeenCalledWith('usage.exportFailed')
    expect(saveAs).not.toHaveBeenCalled()
  })
})
